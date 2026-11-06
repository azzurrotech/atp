#!/bin/bash
# run.sh for atp - Standalone ATP service
#
# Detects if atp is already running locally and prompts:
#   use local / start new local / use remote
# Default: use any running local instance

set -e

ATO_PORT="${ATO_PORT:-8080}"
ATO_ADDR="${ATO_ADDRESS:-127.0.0.1:$ATO_PORT}"

# Check if atp is already running locally
is_local_running() {
    curl -s "http://127.0.0.1:$ATO_PORT/api/health" > /dev/null 2>&1
    return $?
}

# Check if atp remote is reachable
is_remote_running() {
    curl -s "http://$ATO_ADDR/api/health" > /dev/null 2>&1
    return $?
}

echo "=== atp Service ==="
echo "Configuration:"
echo "  Local address: 127.0.0.1:$ATO_PORT"
if [ -n "$ATO_REMOTE" ]; then
    echo "  Remote address: $ATO_REMOTE"
fi

# Prompt user
SELECTED=""

if is_local_running; then
    echo "✓ Local atp is already running on 127.0.0.1:$ATO_PORT"
    read -p "Use local instance? [Y/n/remote]: " choice
    case "$choice" in
        n|N)
            SELECTED="remote"
            ;;
        *)
            SELECTED="local"
            ;;
    esac
fi

if [ "$SELECTED" = "" ]; then
    if is_remote_running; then
        echo "✓ Remote atp is reachable at $ATO_ADDR"
        read -p "Use remote instance? [y/N/local]: " choice
        case "$choice" in
            y|Y)
                SELECTED="remote"
                ;;
            l|L)
                SELECTED="local"
                ;;
            *)
                SELECTED="local"  # default
                ;;
        esac
    else
        echo "✗ No local or remote atp running. Starting new local instance..."
        SELECTED="local"
    fi
fi

# Execute based on selection
case "$SELECTED" in
    local)
        echo ">>> Starting new local atp instance..."
        if [ -x "$(command -v atp)" ]; then
            atp -addr ":$ATO_PORT" -root "./data/atp" &
            echo "    PID: $!"
            echo "✓ atp started locally on 127.0.0.1:$ATO_PORT"
        else
            echo "✗ atp binary not found. Please build first: go build ./..."
        fi
        ;;
    remote)
        echo ">>> Using remote atp at $ATO_ADDR"
        if [ -z "$ATO_REMOTE" ]; then
            export ATO_REMOTE="$ATO_ADDR"
            echo "✓ Remote set: $ATO_REMOTE"
        fi
        ;;
    *)
        echo ">>> Starting new local atp instance..."
        if [ -x "$(command -v atp)" ]; then
            atp -addr ":$ATO_PORT" -root "./data/atp" &
            echo "    PID: $!"
            echo "✓ atp started locally on 127.0.0.1:$ATO_PORT"
        else
            echo "✗ atp binary not found. Please build first: go build ./..."
        fi
        ;;
esac

echo ""
echo "atp service ready on port $ATO_PORT"
echo "API endpoints:"
echo "  Health:     http://127.0.0.1:$ATO_PORT/api/health"
echo "  Platform:   http://127.0.0.1:$ATO_PORT/api/platform"
echo "  Services:   http://127.0.0.1:$ATO_PORT/api/services"
echo "  Functions:  http://127.0.0.1:$ATO_PORT/api/functions"
