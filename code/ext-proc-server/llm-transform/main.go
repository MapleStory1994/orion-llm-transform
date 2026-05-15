package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	"gopkg.in/yaml.v3"
)

type LogConfig struct {
	Level     string `yaml:"log_level"`
	Directory string `yaml:"log_directory"`
	File      string `yaml:"log_file"`
}

type ServerConfig struct {
	Addr string `yaml:"addr"`
}

type HeaderEntry struct {
	Key   string `yaml:"key"`
	Value string `yaml:"value"`
}

type HeaderConfig struct {
	Set    []HeaderEntry `yaml:"set"`
	Remove []string      `yaml:"remove"`
}

type Config struct {
	Server  ServerConfig `yaml:"server"`
	Logging LogConfig    `yaml:"logging"`
	Headers HeaderConfig `yaml:"headers"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

func buildHeaderOptions(cfg *HeaderConfig) ([]*core.HeaderValueOption, []string) {
	if cfg == nil {
		return nil, nil
	}
	setHeaders := make([]*core.HeaderValueOption, 0, len(cfg.Set))
	for _, h := range cfg.Set {
		if h.Key == "" {
			continue
		}
		setHeaders = append(setHeaders, &core.HeaderValueOption{
			Header: &core.HeaderValue{
				Key:   h.Key,
				Value: h.Value,
			},
		})
	}
	removeHeaders := make([]string, 0, len(cfg.Remove))
	for _, r := range cfg.Remove {
		if r == "" {
			continue
		}
		removeHeaders = append(removeHeaders, r)
	}
	return setHeaders, removeHeaders
}

func setupLog(cfg *LogConfig) (*os.File, error) {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.Level) {
	case "error":
		level = slog.LevelError
	case "warn", "warning":
		level = slog.LevelWarn
	case "info":
		level = slog.LevelInfo
	case "debug":
		level = slog.LevelDebug
	case "trace":
		level = slog.LevelDebug
	}

	if cfg.Directory != "" && cfg.File != "" {
		if err := os.MkdirAll(cfg.Directory, 0755); err != nil {
			return nil, fmt.Errorf("create log dir: %w", err)
		}
		path := filepath.Join(cfg.Directory, cfg.File)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, fmt.Errorf("open log file: %w", err)
		}
		h := slog.NewTextHandler(f, &slog.HandlerOptions{Level: level})
		slog.SetDefault(slog.New(h))
		return f, nil
	}

	h := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(h))
	return nil, nil
}

func main() {
	cfgPath := flag.String("config", "../../conf/llm-transform.yaml", "path to config file")
	flag.Parse()

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	addr := cfg.Server.Addr
	if addr == "" {
		addr = ":9001"
	}

	logFile, err := setupLog(&cfg.Logging)
	if err != nil {
		slog.Error("setup log", "error", err)
		os.Exit(1)
	}
	if logFile != nil {
		defer logFile.Close()
	}

	setHeaders, removeHeaders := buildHeaderOptions(&cfg.Headers)

	slog.Info("starting ext-proc server", "addr", addr)
	if err := startGRPCServer(addr, setHeaders, removeHeaders); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}
