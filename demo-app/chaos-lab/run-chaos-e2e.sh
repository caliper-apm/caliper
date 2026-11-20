#!/usr/bin/env bash
set -euo pipefail

# Week 3 End-to-End Chaos Lab Verification:
# Live caliper server + live mock-api + live caliper-symbols + real auto-symbolication + real Webhook delivery.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_DIR="$(mktemp -d /tmp/caliper-chaos-e2e.XXXXXX)"
CALIPER_PORT=18180
MOCK_PORT=18190
HOOK_PORT=18195
API_KEY="chaos-e2e-key"

cleanup() {
  for pid in "${CALIPER_PID:-}" "${MOCK_PID:-}" "${HOOK_PID:-}"; do
    [[ -n "$pid" ]] && kill "$pid" 2>/dev/null || true
  done
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

START_MS="$(python3 -c 'import time; print(int(time.time()*1000))')"

echo "[1/6] Building Go binaries (caliper, caliper-symbols, mock-api)..."
(cd "$ROOT_DIR/server" && go build -o "$TMP_DIR/caliper" ./cmd/caliper)
(cd "$ROOT_DIR/packages/cli-symbols" && go build -o "$TMP_DIR/caliper-symbols" .)
(cd "$ROOT_DIR/demo-app/mock-api" && go build -o "$TMP_DIR/mock-api" .)

echo "[2/6] Starting live caliper server, mock-api, and local Webhook receiver..."
"$TMP_DIR/caliper" --addr "127.0.0.1:$CALIPER_PORT" --data-dir "$TMP_DIR/data" --api-key "$API_KEY" >"$TMP_DIR/caliper.log" 2>&1 &
CALIPER_PID=$!
"$TMP_DIR/mock-api" --addr "127.0.0.1:$MOCK_PORT" >"$TMP_DIR/mock.log" 2>&1 &
MOCK_PID=$!

python3 - "$HOOK_PORT" "$TMP_DIR/webhooks.jsonl" >"$TMP_DIR/hook.log" 2>&1 <<'PY' &
import sys, http.server
port = int(sys.argv[1])
out_path = sys.argv[2]
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(n).decode("utf-8", errors="replace")
        with open(out_path, "a", encoding="utf-8") as f:
            f.write(body + "\n")
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b'{"ok":true}')
    def log_message(self, *args): pass
http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()
PY
HOOK_PID=$!

for i in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:$CALIPER_PORT/healthz" >/dev/null 2>&1 && \
     curl -fsS "http://127.0.0.1:$MOCK_PORT/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done

echo "[3/6] Uploading R8, Hermes SourceMap v3, and iOS dSYM symbol bundles via caliper-symbols..."
"$TMP_DIR/caliper-symbols" upload \
  --server "http://127.0.0.1:$CALIPER_PORT" --api-key "$API_KEY" \
  --app-id dev.caliper.demo --platform android --version 1.1.0 --build 110 \
  --git-sha 1100002abcdef --kind r8_mapping \
  --file "$ROOT_DIR/demo-app/chaos-lab/symbols/atlas-android-1.1.0-110.r8.txt" >/dev/null

"$TMP_DIR/caliper-symbols" upload \
  --server "http://127.0.0.1:$CALIPER_PORT" --api-key "$API_KEY" \
  --app-id dev.caliper.demo --platform android --version 1.1.0 --build 110 \
  --git-sha 1100002abcdef --kind hermes_sourcemap \
  --file "$ROOT_DIR/demo-app/chaos-lab/symbols/atlas-rn-1.1.0-110.sourcemap.json" >/dev/null

"$TMP_DIR/caliper-symbols" upload \
  --server "http://127.0.0.1:$CALIPER_PORT" --api-key "$API_KEY" \
  --app-id dev.caliper.demo --platform ios --version 1.1.0 --build 110 \
  --git-sha 1100002abcdef --kind dsym \
  --file "$ROOT_DIR/demo-app/chaos-lab/symbols/atlas-ios-1.1.0-110.dsym.json" >/dev/null

echo "[4/6] Configuring live Alert Rules (Feishu + Slack) & triggering mock-api Chaos (502 storm)..."
curl -fsS -X POST "http://127.0.0.1:$CALIPER_PORT/v1/alerts/rules" \
  -H "Content-Type: application/json" -H "X-Caliper-Key: $API_KEY" \
  -d "{
    \"rule_id\":\"rule-chaos-jank\",\"name\":\"Atlas Feed Jank Spike\",
    \"app_id\":\"dev.caliper.demo\",\"platform\":\"android\",\"env\":\"prod\",\"app_version\":\"1.1.0\",
    \"scene\":\"home_feed\",\"metric_id\":\"fluency_jank_rate_pct\",\"operator\":\"gt\",\"threshold\":5.0,
    \"min_samples\":10,\"webhook_url\":\"http://127.0.0.1:$HOOK_PORT/feishu\",\"webhook_format\":\"feishu\",
    \"cooldown_sec\":60,\"enabled\":true
  }" >/dev/null

curl -fsS -X POST "http://127.0.0.1:$CALIPER_PORT/v1/alerts/rules" \
  -H "Content-Type: application/json" -H "X-Caliper-Key: $API_KEY" \
  -d "{
    \"rule_id\":\"rule-chaos-api\",\"name\":\"Atlas API Success Drop\",
    \"app_id\":\"dev.caliper.demo\",\"platform\":\"android\",\"env\":\"prod\",\"app_version\":\"1.1.0\",
    \"metric_id\":\"network_api_success_rate_pct\",\"operator\":\"lt\",\"threshold\":95.0,
    \"min_samples\":10,\"webhook_url\":\"http://127.0.0.1:$HOOK_PORT/slack\",\"webhook_format\":\"slack\",
    \"cooldown_sec\":60,\"enabled\":true
  }" >/dev/null

curl -fsS -X POST "http://127.0.0.1:$MOCK_PORT/chaos/config" \
  -H "Content-Type: application/json" \
  -d '{"latency_ms":20,"error_rate":1.0,"ttft_delay_ms":10,"token_gap_ms":5}' >/dev/null

echo "[5/6] Firing live Chaos Lab faults against mock-api and ingesting into caliper..."
python3 - "$CALIPER_PORT" "$MOCK_PORT" "$API_KEY" <<'PY'
import gzip, json, sys, time, urllib.request, urllib.error

caliper_url = f"http://127.0.0.1:{sys.argv[1]}"
mock_url = f"http://127.0.0.1:{sys.argv[2]}"
api_key = sys.argv[3]
now = int(time.time() * 1000)

# Call real mock-api 12 times under Chaos 502 injection and bucket actual durations.
http_fails = 0
buckets = [0] * 19
bounds = [16, 33, 50, 100, 250, 500, 750, 1000, 1500, 2000, 3000, 5000, 8000, 10000, 15000, 30000, 60000, 120000]
dur_sum = 0.0
for _ in range(12):
    t0 = time.perf_counter()
    try:
        urllib.request.urlopen(mock_url + "/api/v1/feed", timeout=5)
    except urllib.error.HTTPError as e:
        if e.code >= 500:
            http_fails += 1
    ms = (time.perf_counter() - t0) * 1000.0
    dur_sum += ms
    idx = next((i for i, b in enumerate(bounds) if ms <= b), 18)
    buckets[idx] += 1

assert http_fails == 12, f"expected 12 mock-api 502s, got {http_fails}"

def post_batch(resource, events):
    raw = json.dumps({"schema_version": 1, "sent_at_ms": now, "resource": resource, "events": events}).encode()
    req = urllib.request.Request(
        caliper_url + "/v1/ingest",
        data=gzip.compress(raw),
        headers={"Content-Type": "application/json", "Content-Encoding": "gzip", "X-Caliper-Key": api_key},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=10) as resp:
        assert resp.status == 202

# Android Native + RN Chaos events
android_res = {
    "app_id": "dev.caliper.demo", "platform": "android", "env": "prod",
    "app_version": "1.1.0", "build_number": "110", "git_sha": "1100002abcdef",
    "adapter": "react-native", "sdk_name": "caliper-android", "sdk_version": "0.1.0",
    "install_hash": "0123456789abcdef",
}
android_events = [
    {
        "event_id": "chaos-android-r8-crash", "kind": "crash", "occurred_at_ms": now, "contract_version": 1,
        "scene": "home_feed",
        "payload": {
            "fingerprint": "fp-android-r8-decode-oom", "title": "OutOfMemoryError in a.b.c.a", "fatal": True,
            "frames": [
                {"module": "AtlasDemo", "symbol": "a.b.c.a", "line": 12, "in_app": True},
                {"module": "AtlasDemo", "symbol": "a.b.d.a", "line": 32, "in_app": True},
            ],
        },
    },
    {
        "event_id": "chaos-rn-js-crash", "kind": "crash", "occurred_at_ms": now + 1, "contract_version": 1,
        "scene": "streaming_reader",
        "payload": {
            "fingerprint": "fp-rn-minified-stream-err", "title": "TypeError in minified bundle", "fatal": True,
            "frames": [
                {"module": "Hermes", "symbol": "at r (index.android.bundle:1:0)", "in_app": True},
                {"module": "Hermes", "symbol": "at p (index.android.bundle:1:24)", "in_app": True},
            ],
        },
    },
    {
        "event_id": "chaos-net-summary-1", "kind": "network_summary", "occurred_at_ms": now + 2, "contract_version": 1,
        "scene": "home_feed", "policy_id": "default_v1",
        "payload": {
            "window_duration_ms": 30000, "call_count": 12,
            "failure_counts": {"http": http_fails},
            "duration_buckets": buckets, "duration_sum_ms": dur_sum,
        },
    },
]
for i in range(12):
    android_events.append({
        "event_id": f"chaos-fluency-{i}", "kind": "fluency_window", "occurred_at_ms": now + 10 + i, "contract_version": 1,
        "scene": "home_feed", "policy_id": "default_v1",
        "payload": {
            "algorithm": "android_frame_metrics_deadline", "window_duration_ms": 30000,
            "frame_count": 1000, "jank_frame_count": 120,
            "stall_stack": ["a.b.c.a(SourceFile:12)", "a.b.d.a(SourceFile:32)"],
        },
    })
post_batch(android_res, android_events)

# iOS Native Chaos crash
ios_res = {
    "app_id": "dev.caliper.demo", "platform": "ios", "env": "prod",
    "app_version": "1.1.0", "build_number": "110", "git_sha": "1100002abcdef",
    "adapter": "native", "sdk_name": "caliper-ios", "sdk_version": "0.1.0",
    "install_hash": "fedcba9876543210",
}
post_batch(ios_res, [{
    "event_id": "chaos-ios-native-crash", "kind": "crash", "occurred_at_ms": now + 3, "contract_version": 1,
    "scene": "home_feed",
    "payload": {
        "fingerprint": "fp-ios-image-decoder-exc", "title": "EXC_BAD_ACCESS at 0x10004228", "fatal": True,
        "frames": [
            {"module": "AtlasDemo", "symbol": "0x10004228", "address": "0x10004228", "in_app": True},
            {"module": "AtlasDemo", "symbol": "0x10008010", "address": "0x10008010", "in_app": True},
        ],
    },
}])
PY

echo "[6/6] Evaluating alerts & asserting auto-symbolication + real Webhook delivery..."
curl -fsS -X POST "http://127.0.0.1:$CALIPER_PORT/v1/alerts/evaluate" -H "X-Caliper-Key: $API_KEY" > "$TMP_DIR/alerts.json"

python3 - "$CALIPER_PORT" "$TMP_DIR/alerts.json" "$TMP_DIR/webhooks.jsonl" "$START_MS" <<'PY'
import json, sys, time, urllib.request

base = f"http://127.0.0.1:{sys.argv[1]}"

def get_json(path):
    with urllib.request.urlopen(base + path, timeout=5) as r:
        assert r.status == 200
        return json.loads(r.read())

# 1. Android R8 auto-symbolication
a = get_json("/v1/stability/issues/fp-android-r8-decode-oom?app_id=dev.caliper.demo&platform=android")
assert a["symbol_status"] == "symbolicated", a
assert a["sample_frames"][0]["file"] == "FeedImageDecoder.kt" and a["sample_frames"][0]["line"] == 92, a
assert a["sample_frames"][1]["file"] == "HomeFeedController.kt" and a["sample_frames"][1]["line"] == 62, a
print("  PASS Android R8 symbolication:", a["sample_frames"][0]["symbol"], f"({a['sample_frames'][0]['file']}:{a['sample_frames'][0]['line']})")

# 2. RN Hermes SourceMap v3 auto-symbolication
rn = get_json("/v1/stability/issues/fp-rn-minified-stream-err?app_id=dev.caliper.demo&platform=android")
assert rn["symbol_status"] == "symbolicated", rn
assert rn["sample_frames"][0]["file"] == "src/screens/StreamingReaderScreen.tsx" and rn["sample_frames"][0]["line"] == 10 and rn["sample_frames"][0]["symbol"] == "renderTokenChunk", rn
print("  PASS RN SourceMap v3 symbolication:", rn["sample_frames"][0]["symbol"], f"({rn['sample_frames'][0]['file']}:{rn['sample_frames'][0]['line']})")

# 3. iOS dSYM hex address auto-symbolication
ios = get_json("/v1/stability/issues/fp-ios-image-decoder-exc?app_id=dev.caliper.demo&platform=ios")
assert ios["symbol_status"] == "symbolicated", ios
assert ios["sample_frames"][0]["file"] == "Sources/AtlasDemo/ImageDecoder.swift" and ios["sample_frames"][0]["line"] == 84, ios
print("  PASS iOS dSYM symbolication:", ios["sample_frames"][0]["symbol"], f"({ios['sample_frames'][0]['file']}:{ios['sample_frames'][0]['line']})")

# 4. Fluency same-window stall stack auto-retrace
fl = get_json("/v1/analytics/fluency?app_id=dev.caliper.demo&platform=android&app_version=1.1.0&build_number=110")
stack0 = fl["scenes"][0]["sample_stall_stacks"][0][0]
assert "FeedImageDecoder.decodeSync(FeedImageDecoder.kt:92)" in stack0, fl
print("  PASS Fluency stall stack retrace:", stack0)

# 5. Real Webhook deliveries (Feishu + Slack)
alerts = json.load(open(sys.argv[2]))["outcomes"]
firing = [o for o in alerts if o["state"] == "firing" and o["incident"]["delivery_status"] == "delivered"]
assert len(firing) == 2, alerts
lines = [json.loads(line) for line in open(sys.argv[3]).read().strip().splitlines()]
assert len(lines) == 2, lines
assert any(d.get("msg_type") == "interactive" for d in lines), "missing Feishu card"
assert any("blocks" in d for d in lines), "missing Slack Block Kit"
elapsed_s = (int(time.time() * 1000) - int(sys.argv[4])) / 1000.0
assert elapsed_s < 15.0, f"exceeded 15s Week 3 gate: {elapsed_s:.2f}s"
print(f"  PASS Real Webhooks delivered (Feishu + Slack): 2/2 HTTP 200 in {elapsed_s:.2f}s total E2E!")
PY
