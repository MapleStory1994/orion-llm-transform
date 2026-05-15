#!/bin/bash
set -e

PROJECT_DIR="$(dirname "$0")"
SRV_DIR="$PROJECT_DIR/code/ext-proc-server/llm-transform"
BINARY="$SRV_DIR/ext-proc-server"
CONFIG_FILE="$PROJECT_DIR/conf/llm-transform.yaml"
PID_FILE="$PROJECT_DIR/.llm-transform.pid"

case "${1:-}" in
    build)
        echo "Building llm-transform..."
        cd "$SRV_DIR" && go build -o ext-proc-server .
        echo "Done"
        ;;
    clean)
        echo "Cleaning..."
        cd "$SRV_DIR" && go clean
        rm -f "$BINARY"
        rm -f "$PROJECT_DIR"/logs/llm-transform.log*
        echo "Done"
        ;;
    start)
        if [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
            echo "llm-transform is already running (PID: $(cat "$PID_FILE"))"
            exit 1
        fi
        if [ ! -f "$BINARY" ]; then
            echo "Binary not found, building first..."
            cd "$SRV_DIR" && go build -o ext-proc-server .
        fi
        mkdir -p "$PROJECT_DIR/logs"
        nohup "$BINARY" --config "$CONFIG_FILE" > /dev/null 2>&1 &
        PID=$!
        echo "$PID" > "$PID_FILE"
        echo "llm-transform started (PID: $PID), listening on :9001"
        echo "Logs: $PROJECT_DIR/logs/llm-transform.log"
        ;;
    stop)
        if [ ! -f "$PID_FILE" ]; then
            echo "llm-transform is not running"
            exit 0
        fi
        PID=$(cat "$PID_FILE")
        echo "Stopping llm-transform (PID: $PID)..."
        kill "$PID"
        rm -f "$PID_FILE"
        rm -f "$PROJECT_DIR"/logs/llm-transform.log*
        echo "llm-transform stopped"
        ;;
    *)
        echo "Usage: $0 {build|clean|start|stop}"
        exit 1
        ;;
esac
