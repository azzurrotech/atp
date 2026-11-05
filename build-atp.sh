#!/bin/bash
# build-atp.sh - Build and run the ATP (AzzurroTech Platform) service
#
# ATP is the core platform that embeds song, pod, and shepherd modules.
#
# Usage:
#   ./build-atp.sh           # build and start ATP on default port
#   ./build-atp.sh 8080    # start ATP on port 8080
#   ./build-atp.sh song    # build and start song first, then ATP
#   ./build-atp.sh pod     # build and start pod first, then ATP
#   ./build-atp.sh shepherd # build and start shepherd first, then ATP
#   ./build-atp.sh url     # connect to existing ATP at given URL
#
# Automatically detects an open local port if none specified,
# otherwise accepts a URL as argument.

set -e

# Resolve script directory (handle cd cases)
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Function to build and run a sub-service
run_subservice() {
  local SUBSERVICE="$1"
  echo ">>> Building and running $SUBSERVICE subsystem..."
  case "$SUBSERVICE" in
    song)
      cd "$SCRIPT_DIR/../song" && ./build-song.sh "${2:-}"
      ;;
    pod)
      cd "$SCRIPT_DIR/../pod" && ./build-pod.sh "${2:-}"
      ;;
    shepherd)
      cd "$SCRIPT_DIR/../shepherd" && ./build-shepherd.sh "${2:-}"
      ;;
    *)
      echo "ERROR: Unknown subservice: $SUBSERVICE"
      exit 1
      ;;
  esac
}

# Parse arguments: if a port number or URL is given, use it
PORT=""
RUN_SUBSERVICE=""

while [ $# -gt 0 ]; do
  case "$1" in
    song|pod|shepherd)
      RUN_SUBSERVICE="$1"
      shift
      ;;
    http://*|https://*)
      # URL provided - use as ATP address
      echo ">>> Using provided ATP URL: $1"
      PORT="$1"
      shift
      ;;
    --port|-p)
      PORT=":$2"
      shift 2
      ;;
    *)
      # Assume it's a port number
      if echo "$1" | grep -qE '^[0-9]+$'; then
        PORT=":$1"
      else
        echo "ERROR: Expected a port number, subservice (song/pod/shepherd), or URL, got: $1"
        exit 1
      fi
      shift
      ;;
  esac
done

# If a subservice was requested, build and run it, then exit (or continue to ATP)
if [ -n "$RUN_SUBSERVICE" ]; then
  run_subservice "$RUN_SUBSERVICE" "$PORT"
  exit $?
fi

# Find an open port if not provided
if [ -z "$PORT" ]; then
  for trial_port in 8080 8081 8082 8083 8084 9090; do
    if ! curl -s "http://localhost${trial_port}" > /dev/null 2>&1; then
      PORT=":$trial_port"
      echo ">>> Using available port $trial_port"
      break
    fi
  done
  if [ -z "$PORT" ]; then
    echo ">>> No default port available, relying on Go defaults/env"
    PORT=""
  fi
fi

# Build the Go binary
echo ">>> Building ATP binary..."
cd "$SCRIPT_DIR"
go build -o atp ./cmd/atp/

echo ">>> Starting ATP..."
if [ -n "$PORT" ]; then
  echo ">>> Using port $PORT"
  export ATP_PORT="${PORT#:}"
  ./"atp" -addr "$PORT" -root "./data" -secret "${ATP_SECRET:-}"
else
  ./"atp" -addr ":8080" -root "./data" -secret "${ATP_SECRET:-}"
fi