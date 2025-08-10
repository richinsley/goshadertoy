#!/bin/bash
QUEUE_FILE="/tmp/tty2-queue"
RESULT_FILE="/tmp/tty2-result"
PID_FILE="/tmp/tty2-pid"

# --- Cleanup Function and Trap ---
# This function will be called on script exit or Ctrl+C
cleanup() {
    echo ""
    echo "Cleaning up..."
    if [[ -f "$PID_FILE" ]]; then
        PID_TO_KILL=$(cat "$PID_FILE")
        if ps -p "$PID_TO_KILL" > /dev/null; then
            echo "Stopping Delve process (PID: $PID_TO_KILL)..."
            kill "$PID_TO_KILL"
        fi
    fi
    rm -f "$QUEUE_FILE" "$RESULT_FILE" "$PID_FILE"
    echo "Cleanup complete."
    exit 0
}

# Trap SIGINT (Ctrl+C) and SIGTERM to run the cleanup function
trap cleanup SIGINT SIGTERM

# --- Main Script Logic ---
if [[ $# -lt 2 ]]; then
    echo "Usage: $0 <go-program-path> <dlv-port> [additional-args]"
    exit 1
fi

PROGRAM_PATH="$1"
DLV_PORT="$2"
shift 2
PROGRAM_ARGS="$@"

# Clean up any previous state before starting
rm -f "$QUEUE_FILE" "$RESULT_FILE" "$PID_FILE"

echo "Starting Delve debugger on target tty..."

# --- Recommended: Remove --accept-multiclient to help dlv exit on disconnect ---
DLV_CMD="~/go/bin/dlv exec $(printf "%q" "$PROGRAM_PATH") --headless --listen=0.0.0.0:$DLV_PORT --api-version=2"

if [[ -n "$PROGRAM_ARGS" ]]; then
    DLV_CMD+=" -- $PROGRAM_ARGS"
fi

echo "Command to be executed on TTY: $DLV_CMD"
echo "$DLV_CMD" > "$QUEUE_FILE"

echo "Waiting for debugger to start on port $DLV_PORT..."
for i in {1..15}; do
    if ss -ltn | grep -q ":$DLV_PORT"; then
        break # Port is listening, proceed
    fi
    sleep 1
done

if ! ss -ltn | grep -q ":$DLV_PORT"; then
    echo "Error: Debugger failed to start or listen on the port in time."
    if [[ -f "$RESULT_FILE" ]]; then
        echo "------------------- Debugger Output --------------------"
        cat "$RESULT_FILE"
        echo "--------------------------------------------------------"
    fi
    exit 1
fi

# Check if PID file was created
if [[ ! -f "$PID_FILE" ]]; then
    echo "Error: Debugger is listening, but PID file was not found. Cannot monitor."
    exit 1
fi

PID=$(cat "$PID_FILE")
echo "Success! Delve is running with PID: $PID and listening on port $DLV_PORT."
echo "You can now attach your VS Code debugger."
echo ""
echo "--- This script is now blocking. Press Ctrl+C to terminate the debugger and exit. ---"
echo ""

# --- Blocking Loop ---
# This loop waits until the dlv process no longer exists.
# `kill -0` is a portable way to check if a process exists.
while kill -0 "$PID" 2>/dev/null; do
    sleep 1
done

echo "Delve process (PID: $PID) has exited."
# Call cleanup to remove the tmp files and exit gracefully
cleanup