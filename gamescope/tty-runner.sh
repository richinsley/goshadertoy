#!/bin/bash
QUEUE_FILE="/tmp/tty2-queue"
RESULT_FILE="/tmp/tty2-result"
PID_FILE="/tmp/tty2-pid"

echo "TTY2 Runner started on $(tty) at $(date)"
echo "Watching for commands in $QUEUE_FILE"

cleanup() {
    echo "Cleaning up..."
    rm -f "$QUEUE_FILE" "$RESULT_FILE" "$PID_FILE"
    exit 0
}

trap cleanup SIGTERM SIGINT

while true; do
    if [[ -f "$QUEUE_FILE" ]]; then
        COMMAND=$(cat "$QUEUE_FILE")
        echo "$(date): Executing: $COMMAND"
        
        # Remove queue file immediately to prevent loops
        rm -f "$QUEUE_FILE"
        
        # Execute the command
        bash -c "$COMMAND" > "$RESULT_FILE" 2>&1 &
        COMMAND_PID=$!
        
        echo "$COMMAND_PID" > "$PID_FILE"
        echo "$(date): Started PID $COMMAND_PID"
        
        # Wait for completion
        wait $COMMAND_PID
        EXIT_CODE=$?
        
        echo "$(date): Command finished with exit code $EXIT_CODE"
        echo "EXIT_CODE: $EXIT_CODE" >> "$RESULT_FILE"
        
        # Clean up PID file when done
        rm -f "$PID_FILE"
    fi
    sleep 1
done