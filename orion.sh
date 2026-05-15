#!/bin/bash
set -e

PROJECT_DIR="$(dirname "$0")"
ORION_DIR="$PROJECT_DIR/code/orion"
CONFIG_FILE="$PROJECT_DIR/conf/orion.yaml"
BINARY="$ORION_DIR/target/release/orion"
PID_FILE="$PROJECT_DIR/.orion.pid"

case "${1:-}" in
    build)
        echo "Building orion..."
        cd "$ORION_DIR" && cargo build --workspace --release --locked
        ;;
    clean)
        echo "Cleaning build artifacts and logs..."
        cd "$ORION_DIR" && cargo clean
        rm -f "$PROJECT_DIR"/logs/orion.log*
        echo "Done"
        ;;
    start)
        if [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
            echo "Orion is already running (PID: $(cat "$PID_FILE"))"
            exit 1
        fi
        if [ ! -f "$BINARY" ]; then
            echo "Binary not found, building first..."
            cd "$ORION_DIR" && cargo build --workspace --release --locked
        fi
        mkdir -p "$PROJECT_DIR/logs"
        nohup "$BINARY" --config "$CONFIG_FILE" > /dev/null 2>&1 &
        PID=$!
        echo "$PID" > "$PID_FILE"
        echo "Orion started (PID: $PID), listening on 127.0.0.1:10000 and 127.0.0.1:10001"
        echo "Logs: $PROJECT_DIR/logs/orion.log"
        ;;
    stop)
        if [ ! -f "$PID_FILE" ]; then
            echo "Orion is not running"
            exit 0
        fi
        PID=$(cat "$PID_FILE")
        echo "Stopping orion (PID: $PID)..."
        kill "$PID"
        rm -f "$PID_FILE"
        rm -f "$PROJECT_DIR"/logs/orion.log*
        echo "Orion stopped"
        ;;
    *)
        echo "Usage: $0 {build|clean|start|stop}"
        exit 1
        ;;
esac
