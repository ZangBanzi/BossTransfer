#!/bin/sh
set -eu

PORT="${BOSSTRANSFER_MANAGER_HTTP_PORT:-8085}"
ADDR="127.0.0.1:${PORT}"
BASE_URL="http://${ADDR}"
PID_FILE="./manager.pid"
LOG_FILE="./manager.log"
DATA_DIR="${BOSSTRANSFER_SMOKE_DATA_DIR:-./smoke-data/manager}"

mkdir -p "$DATA_DIR"

if [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
  kill "$(cat "$PID_FILE")" 2>/dev/null || true
  rm -f "$PID_FILE"
fi

BOSSTRANSFER_MANAGER_ADDR="$ADDR" \
  BOSSTRANSFER_DATA_DIR="$DATA_DIR" \
  ./bin/manager >"$LOG_FILE" 2>&1 &
echo "$!" >"$PID_FILE"

cleanup() {
  if [ -f "$PID_FILE" ]; then
    kill "$(cat "$PID_FILE")" 2>/dev/null || true
    rm -f "$PID_FILE"
  fi
}
trap cleanup EXIT INT TERM

attempt=0
while [ "$attempt" -lt 50 ]; do
  if command -v curl >/dev/null 2>&1; then
    body="$(curl -fsS "${BASE_URL}/api/v1/health/live" 2>/dev/null || true)"
  elif command -v wget >/dev/null 2>&1; then
    body="$(wget -qO- "${BASE_URL}/api/v1/health/live" 2>/dev/null || true)"
  else
    echo "curl or wget is required for remote smoke test" >&2
    exit 2
  fi
  case "$body" in
    *'"status":"ok"'*|*'"status": "ok"'*)
      echo "manager live health ok on ${BASE_URL}"
      exit 0
      ;;
  esac
  attempt=$((attempt + 1))
  sleep 0.2
done

echo "manager did not pass live health check" >&2
tail -n 40 "$LOG_FILE" >&2 || true
exit 1
