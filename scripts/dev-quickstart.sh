#!/usr/bin/env bash
set -euo pipefail

# Local zero-Docker 30-second quickstart for Caliper APM.
# Builds Go binaries + Console SPA, starts caliper (:8080) & mock-api (:8090),
# uploads sample R8/SourceMap/dSYM symbols, and seeds the v1.0 -> v1.1 (regressed) -> v1.2 (fixed) story.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DATA_DIR="${CALIPER_DATA_DIR:-$ROOT_DIR/.data}"
PORT="${CALIPER_PORT:-8080}"
MOCK_PORT="${CALIPER_MOCK_PORT:-8090}"
BIN_DIR="$ROOT_DIR/server/bin"

mkdir -p "$BIN_DIR" "$DATA_DIR"

echo "[1/4] Building Go binaries..."
(cd "$ROOT_DIR/server" && go build -o "$BIN_DIR/caliper" ./cmd/caliper && go build -o "$BIN_DIR/seed-replay" ./cmd/seed-replay)
(cd "$ROOT_DIR/packages/cli-symbols" && go build -o "$BIN_DIR/caliper-symbols" .)
(cd "$ROOT_DIR/demo-app/mock-api" && go build -o "$BIN_DIR/mock-api" .)

echo "[2/4] Checking Web Console build..."
if [[ ! -f "$ROOT_DIR/console/dist/index.html" ]]; then
  if [[ -f "$ROOT_DIR/console/package-lock.json" ]]; then
    (cd "$ROOT_DIR/console" && npm ci --no-audit --no-fund && npm run build)
  else
    (cd "$ROOT_DIR/console" && npm install --no-audit --no-fund && npm run build)
  fi
fi

echo "[3/4] Starting Caliper APM (:$PORT) and Atlas Reader mock-api (:$MOCK_PORT)..."
"$BIN_DIR/caliper" --addr "127.0.0.1:$PORT" --data-dir "$DATA_DIR" --console-dist "$ROOT_DIR/console/dist" --alert-interval 30s &
CALIPER_PID=$!
"$BIN_DIR/mock-api" --addr "127.0.0.1:$MOCK_PORT" &
MOCK_PID=$!

trap 'kill "$CALIPER_PID" "$MOCK_PID" 2>/dev/null || true' EXIT

for i in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.1
done

echo "[4/4] Uploading sample symbols & seeding v1.0.0 -> v1.1.0 (regressed) -> v1.2.0 (fixed)..."
"$BIN_DIR/caliper-symbols" upload --server "http://127.0.0.1:$PORT" --app-id dev.caliper.demo --platform android --version 1.1.0 --build 110 --git-sha 1100002abcdef --kind r8_mapping --file "$ROOT_DIR/demo-app/chaos-lab/symbols/atlas-android-1.1.0-110.r8.txt" >/dev/null
"$BIN_DIR/caliper-symbols" upload --server "http://127.0.0.1:$PORT" --app-id dev.caliper.demo --platform android --version 1.1.0 --build 110 --git-sha 1100002abcdef --kind hermes_sourcemap --file "$ROOT_DIR/demo-app/chaos-lab/symbols/atlas-rn-1.1.0-110.sourcemap.json" >/dev/null
"$BIN_DIR/caliper-symbols" upload --server "http://127.0.0.1:$PORT" --app-id dev.caliper.demo --platform ios --version 1.1.0 --build 110 --git-sha 1100002abcdef --kind dsym --file "$ROOT_DIR/demo-app/chaos-lab/symbols/atlas-ios-1.1.0-110.dsym.json" >/dev/null
"$BIN_DIR/seed-replay" --target "http://127.0.0.1:$PORT" --events 30000 --verify=true

echo ""
echo "=============================================================="
echo "  Caliper APM Console:  http://127.0.0.1:$PORT/"
echo "  Atlas mock-api:       http://127.0.0.1:$MOCK_PORT/"
echo "  Press Ctrl+C to stop both services cleanly."
echo "=============================================================="
wait "$CALIPER_PID"
