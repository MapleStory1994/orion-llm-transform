package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	epb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"net"
)

type extProcServer struct {
	epb.UnimplementedExternalProcessorServer
	setHeaders    []*core.HeaderValueOption
	removeHeaders []string
}

func (s *extProcServer) Process(stream epb.ExternalProcessor_ProcessServer) error {
	reqMutation := &epb.HeaderMutation{}
	if len(s.setHeaders) > 0 || len(s.removeHeaders) > 0 {
		reqMutation = &epb.HeaderMutation{
			SetHeaders:    s.setHeaders,
			RemoveHeaders: s.removeHeaders,
		}
	}
	for {
		req, err := stream.Recv()
		if err != nil {
			return err
		}

		switch v := req.Request.(type) {
		case *epb.ProcessingRequest_RequestHeaders:
			slog.Info(">>> 请求头", "headers", toMap(v.RequestHeaders.Headers.Headers))
			stream.Send(&epb.ProcessingResponse{
				Response: &epb.ProcessingResponse_RequestHeaders{
					RequestHeaders: &epb.HeadersResponse{
						Response: &epb.CommonResponse{
							HeaderMutation: reqMutation,
						},
					},
				},
			})

		case *epb.ProcessingRequest_RequestBody:
			body := v.RequestBody.Body
			slog.Info(">>> 请求体", "body", string(body))
			resp := &epb.CommonResponse{}
			if fixed := fixBody(body); fixed != nil {
				slog.Info(">>> 请求体 已修复instructions和tools")
				resp.BodyMutation = &epb.BodyMutation{
					Mutation: &epb.BodyMutation_Body{Body: fixed},
				}
			}
			stream.Send(&epb.ProcessingResponse{
				Response: &epb.ProcessingResponse_RequestBody{
					RequestBody: &epb.BodyResponse{Response: resp},
				},
			})

		case *epb.ProcessingRequest_ResponseHeaders:
			slog.Info("<<< 响应头", "headers", toMap(v.ResponseHeaders.Headers.Headers))
			stream.Send(&epb.ProcessingResponse{
				Response: &epb.ProcessingResponse_ResponseHeaders{
					ResponseHeaders: &epb.HeadersResponse{
						Response: &epb.CommonResponse{HeaderMutation: &epb.HeaderMutation{}},
					},
				},
			})

		case *epb.ProcessingRequest_ResponseBody:
			body := v.ResponseBody.Body
			slog.Info("<<< 响应体", "body", string(body))
			resp := &epb.CommonResponse{}
			if fixed := fixResponseBody(body); fixed != nil {
				slog.Info("<<< 响应体 已转换为Responses API格式")
				resp.BodyMutation = &epb.BodyMutation{
					Mutation: &epb.BodyMutation_Body{Body: fixed},
				}
			}
			stream.Send(&epb.ProcessingResponse{
				Response: &epb.ProcessingResponse_ResponseBody{
					ResponseBody: &epb.BodyResponse{Response: resp},
				},
			})

		default:
			slog.Warn("未处理", "type", fmt.Sprintf("%T", req.Request))
		}
	}
}

// convertInputMessage converts a Responses API message to a Chat Completions message.
func convertInputMessage(m any) map[string]any {
	msg, ok := m.(map[string]any)
	if !ok {
		return nil
	}

	// Extract role
	role, _ := msg["role"].(string)
	if role == "" {
		return nil
	}

	// Convert developer role to system (DeepSeek doesn't support "developer")
	if role == "developer" {
		role = "system"
	}

	// Extract text from content array: [{type:"input_text", text:"..."}, ...]
	var contentText string
	if content, ok := msg["content"]; ok {
		switch cv := content.(type) {
		case string:
			contentText = cv
		case []any:
			var parts []string
			for _, item := range cv {
				if itemMap, ok := item.(map[string]any); ok {
					if text, ok := itemMap["text"]; ok {
						if str, ok := text.(string); ok {
							parts = append(parts, str)
						}
					}
				}
			}
			contentText = strings.Join(parts, "")
		}
	}

	return map[string]any{
		"role":    role,
		"content": contentText,
	}
}

// fixBody converts Codex request format (Responses API) to DeepSeek-compatible format (Chat Completions).
// 1. instructions → messages[{role:"system", content:...}]
// 2. input → messages (with developer→system, content array→string)
// 3. tools from [{type:"function", name:"x", ...}] → [{type:"function", function:{name:"x", ...}}]
func fixBody(body []byte) []byte {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil
	}
	changed := false

	// Ensure messages array exists
	msgs, _ := obj["messages"].([]any)
	if msgs == nil {
		msgs = make([]any, 0)
	}

	// Convert instructions to messages (only if no messages already exist)
	if len(msgs) == 0 {
		if instr, ok := obj["instructions"]; ok {
			if str, ok := instr.(string); ok && str != "" {
				msgs = append(msgs, map[string]any{
					"role":    "system",
					"content": str,
				})
				delete(obj, "instructions")
				changed = true
			}
		}
	}

	// Convert input to messages (Responses API -> Chat Completions)
	if input, ok := obj["input"]; ok && input != nil {
		switch v := input.(type) {
		case string:
			if v != "" {
				msgs = append(msgs, map[string]any{
					"role":    "user",
					"content": v,
				})
				changed = true
			}
		case []any:
			for _, m := range v {
				if converted := convertInputMessage(m); converted != nil {
					msgs = append(msgs, converted)
					changed = true
				}
			}
		}
		delete(obj, "input")
		changed = true
	}

	if len(msgs) > 0 {
		obj["messages"] = msgs
	} else {
		delete(obj, "messages")
	}

	// Fix tools format
	if raw, ok := obj["tools"]; ok {
		if tools, ok := raw.([]any); ok {
			valid := make([]any, 0, len(tools))
			for _, t := range tools {
				tool, ok := t.(map[string]any)
				if !ok {
					changed = true
					continue
				}
				if _, has := tool["function"]; has {
					valid = append(valid, tool)
					continue
				}
				if typ, _ := tool["type"].(string); typ != "function" {
					tool["type"] = "function"
					changed = true
				}
				fn := make(map[string]any)
				for k, v := range tool {
					if k != "type" {
						fn[k] = v
						delete(tool, k)
					}
				}
				if len(fn) == 0 {
					changed = true
					continue
				}
				name, hasName := fn["name"]
				if !hasName || name == "" {
					changed = true
					continue
				}
				tool["function"] = fn
				valid = append(valid, tool)
				changed = true
			}
			obj["tools"] = valid
		}
	}

	if !changed {
		return nil
	}
	fixed, err := json.Marshal(obj)
	if err != nil {
		return nil
	}
	return fixed
}

// fixResponseBody converts Chat Completions response body to Responses API format.
// Supports both SSE streaming (data: {...}) and non-streaming JSON.
func fixResponseBody(body []byte) []byte {
	if len(body) == 0 {
		return nil
	}
	if bytes.HasPrefix(body, []byte("data: ")) {
		return fixSSEBody(body)
	}
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '{' {
		return fixJSONResponseBody(body)
	}
	return nil
}

// fixSSEBody converts Chat Completions SSE events to Responses API event format.
// Handles multiple events in one chunk (separated by \n\n).
func fixSSEBody(body []byte) []byte {
	// Split on \n\n to handle multiple SSE events in one chunk
	raw := bytes.Split(body, []byte("\n\n"))
	result := make([][]byte, 0, len(raw))
	changed := false

	for _, part := range raw {
		part = bytes.TrimSpace(part)
		if len(part) == 0 {
			continue
		}

		// Convert [DONE] sentinel to response.completed
		if bytes.Equal(part, []byte("data: [DONE]")) {
			completedData, _ := json.Marshal(map[string]any{
				"output": []any{},
			})
			result = append(result, []byte("event: response.completed\ndata: "+string(completedData)))
			changed = true
			continue
		}

		// Process data: {...} lines
		if !bytes.HasPrefix(part, []byte("data: ")) {
			result = append(result, part)
			continue
		}

		line := bytes.TrimPrefix(part, []byte("data: "))
		if converted := convertSSEEvent(line); converted != nil {
			result = append(result, converted)
			changed = true
		} else {
			result = append(result, part)
		}
	}

	if !changed {
		return nil
	}
	joined := bytes.Join(result, []byte("\n\n"))
	// Ensure trailing \n\n so SSE parser recognizes complete event boundary
	joined = append(joined, []byte("\n\n")...)
	return joined
}

// convertSSEEvent converts a single Chat Completions data payload to Responses API event format.
func convertSSEEvent(data []byte) []byte {
	var event map[string]any
	if err := json.Unmarshal(data, &event); err != nil {
		return nil
	}

	choices, ok := event["choices"].([]any)
	if !ok || len(choices) == 0 {
		return nil
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return nil
	}
	delta, ok := choice["delta"].(map[string]any)
	if !ok {
		return nil
	}

	// Handle finish_reason — emit done + completed events
	if fr, has := choice["finish_reason"]; has && fr != nil {
		frStr, _ := fr.(string)
		if frStr != "" {
			doneData, _ := json.Marshal(map[string]any{
				"type":  "output_text",
				"text":  "",
				"index": 0,
			})
			completedData, _ := json.Marshal(map[string]any{
				"output": []any{},
			})
			result := "event: response.output_text.done\ndata: " + string(doneData) + "\n\n"
			result += "event: response.completed\ndata: " + string(completedData)
			return []byte(result)
		}
	}

	// Handle text content delta
	if content, has := delta["content"]; has && content != nil {
		text, _ := content.(string)
		if text != "" {
			deltaData, _ := json.Marshal(map[string]any{
				"delta": text,
				"index": 0,
			})
			return []byte("event: response.output_text.delta\ndata: " + string(deltaData))
		}
	}

	// Handle role announcement — emit item/content started
	if _, has := delta["role"]; has {
		result := "event: response.output_item.added\ndata: {\"type\":\"message\",\"role\":\"assistant\"}\n\n"
		result += "event: response.content_part.added\ndata: {\"type\":\"text\"}"
		return []byte(result)
	}

	// Handle tool_calls delta
	if tcs, has := delta["tool_calls"]; has {
		if tcList, ok := tcs.([]any); ok {
			for _, tc := range tcList {
				tcMap, ok := tc.(map[string]any)
				if !ok {
					continue
				}
				fnDelta := map[string]any{}
				if id, has := tcMap["id"]; has && id != nil {
					fnDelta["id"] = id
					fnDelta["type"] = "function_call"
					if fn, has := tcMap["function"].(map[string]any); has {
						if name, has := fn["name"]; has {
							fnDelta["name"] = name
						}
					}
					itemJSON, _ := json.Marshal(fnDelta)
					return []byte("event: response.function_call.created\ndata: " + string(itemJSON))
				} else if fn, has := tcMap["function"].(map[string]any); has {
					if args, has := fn["arguments"]; has {
						argsStr, _ := args.(string)
						if argsStr != "" {
							deltaJSON, _ := json.Marshal(map[string]any{
								"arguments": argsStr,
							})
							return []byte("event: response.function_call.delta\ndata: " + string(deltaJSON))
						}
					}
				}
			}
		}
	}

	return nil
}

// fixJSONResponseBody converts a non-streaming Chat Completions JSON response to Responses API format.
func fixJSONResponseBody(body []byte) []byte {
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}

	choices, ok := resp["choices"].([]any)
	if !ok || len(choices) == 0 {
		return nil
	}

	newResp := map[string]any{}
	if id, ok := resp["id"]; ok {
		newResp["id"] = "resp_" + strings.TrimPrefix(fmt.Sprint(id), "chatcmpl-")
	}
	if model, ok := resp["model"]; ok {
		newResp["model"] = model
	}
	if created, ok := resp["created"]; ok {
		newResp["created"] = created
	}

	// Convert choices to output array
	output := make([]any, 0, len(choices))
	for _, choice := range choices {
		c, ok := choice.(map[string]any)
		if !ok {
			continue
		}
		msg, ok := c["message"].(map[string]any)
		if !ok {
			continue
		}

		// Build content array
		content := make([]any, 0)
		if text, ok := msg["content"]; ok && text != nil {
			if str, ok := text.(string); ok && str != "" {
				content = append(content, map[string]any{
					"type":        "output_text",
					"text":        str,
					"annotations": []any{},
				})
			}
		}

		output = append(output, map[string]any{
			"type":    "message",
			"role":    "assistant",
			"content": content,
		})

		// Handle tool_calls -> function_call output items
		if tcs, ok := msg["tool_calls"]; ok && tcs != nil {
			if tcList, ok := tcs.([]any); ok {
				for _, tc := range tcList {
					tcMap, ok := tc.(map[string]any)
					if !ok {
						continue
					}
					fnItem := map[string]any{
						"type": "function_call",
						"id":   tcMap["id"],
					}
					if fn, ok := tcMap["function"].(map[string]any); ok {
						fnItem["name"] = fn["name"]
						fnItem["arguments"] = fn["arguments"]
						fnItem["status"] = "completed"
					}
					output = append(output, fnItem)
				}
			}
		}
	}
	newResp["output"] = output

	// Map usage field names
	if usage, ok := resp["usage"]; ok {
		if u, ok := usage.(map[string]any); ok {
			newUsage := map[string]any{}
			if pt, ok := u["prompt_tokens"]; ok {
				newUsage["input_tokens"] = pt
			}
			if ct, ok := u["completion_tokens"]; ok {
				newUsage["output_tokens"] = ct
			}
			if tt, ok := u["total_tokens"]; ok {
				newUsage["total_tokens"] = tt
			}
			newResp["usage"] = newUsage
		}
	}

	fixed, err := json.Marshal(newResp)
	if err != nil {
		return nil
	}
	return fixed
}

func toMap(hdrs []*core.HeaderValue) map[string]string {
	m := make(map[string]string, len(hdrs))
	for _, h := range hdrs {
		v := h.Value
		if v == "" && len(h.RawValue) > 0 {
			v = string(h.RawValue)
		}
		m[h.Key] = v
	}
	return m
}

func startGRPCServer(addr string, setHeaders []*core.HeaderValueOption, removeHeaders []string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	srv := grpc.NewServer()
	epb.RegisterExternalProcessorServer(srv, &extProcServer{
		setHeaders:    setHeaders,
		removeHeaders: removeHeaders,
	})
	reflection.Register(srv)
	return srv.Serve(lis)
}
