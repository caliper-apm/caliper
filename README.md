# Caliper APM

**Native-First, React Native Compatible, Evidence-First Open-Source Mobile APM.**

[简体中文](README.zh-CN.md) · [North Star Charter](NORTH_STAR.md) · [Zero-Lie Metrics Contract](spec/metrics-contract.md) · [Changelog](CHANGELOG.md) · [Benchmark Report](benchmarks/REPORT.md)

---

## Why Caliper APM?

Most mid-to-large mobile apps run **Native code for performance-critical core flows** (feeds, media, startup, networking) and **React Native for fast-moving business screens**. Existing tooling leaves a painful gap:
- Client-only diagnostic libraries lack a backend, cross-version analytics, symbolication pipeline, and alerting.
- Heavy general-purpose observability suites require large clusters to self-host and lack deep mobile fluency, multi-stage startup, and zero-lie statistical rigor.
- Commercial closed-source APMs are expensive black boxes with opaque aggregation formulas.

**Caliper APM** fills that gap with five core principles:
1. **Native-First Core + RN First-Class Adapter**: `sdk-android` (Kotlin) and `sdk-ios` (Swift SPM) have **zero React Native dependencies**. Hybrid apps attach `sdk-react-native` onto the exact same Native lifecycle, window correlation, and symbolication timeline.
2. **Zero-Lie Analytics Engine**: Mergeable 19-bucket histograms (never averaged daily P90), missing values never zero-filled, `frame_count == 0` never reported as smooth, Android API 31+ `deadline` vs `heuristic` strictly separated, and explicit sample-size gates (`n >= 100` for release deltas).
3. **Built-In Multi-Format Auto-Symbolication**: Pure-Go R8/ProGuard Retrace, Source Map v3 VLQ (JS/Hermes), and Breakpad/JSON dSYM address resolution bound strictly to `app_version + build_number + symbol_id`.
4. **Actionable Real Alerting**: Sample-gated rule evaluation with cooldown deduplication and native **Feishu Interactive Card**, **Slack Block Kit**, **Discord Embed**, and **Generic JSON** Webhooks.
5. **Single-Binary Self-Hosting + Chaos Lab Showcase**: Zero-CGO Go binary serving both APIs and the 5-page real-data React 19 Web Console, paired with the **Atlas Reader** hybrid demo & **Chaos Lab**.

---

## Architecture

```text
┌────────────────────────────────────────────────────────────────────┐
│  Native-First Mobile SDKs (Zero RN deps in Android / iOS cores)    │
│  ├─ packages/sdk-android (Kotlin): FrameMetrics, PSS/CPU, OkHttp   │
│  ├─ packages/sdk-ios (Swift SPM): CADisplayLink, phys_footprint,   │
│  │                                getrusage, URLSession metrics    │
│  └─ packages/sdk-react-native (Optional TS Adapter): LongTask,     │
│                                API denominator, JS errors, routes  │
└───────────────────────────────┬────────────────────────────────────┘
                                │ POST /v1/ingest (Gzip, OTLP-aligned)
                                ▼
┌────────────────────────────────────────────────────────────────────┐
│  Single-Binary Go Server (server/cmd/caliper)                      │
│  ├─ Collector & Policy API   (POST /v1/ingest, GET /v1/policy)     │
│  ├─ Storage Interface        (Embedded WAL-Columnar; DuckDB/CH DDL)│
│  ├─ Zero-Lie Analytics       (Mergeable P50/P90, gated comparisons)│
│  ├─ Auto-Symbolicator        (R8 Retrace, SourceMap v3, dSYM/BP)   │
│  ├─ Alert Engine             (Feishu / Slack / Discord / HTTP)     │
│  └─ Static Web Console Host  (React 19 + TS 5-Page Real-Data UI)   │
└────────────────────────────────────────────────────────────────────┘
```

---

## Quickstart (< 1 Minute)

### Option A · Docker Compose (Recommended)
```bash
git clone https://github.com/<your-org>/caliper-apm.git
cd caliper-apm
docker compose up --build
# Open http://localhost:8080 — preloaded with v1.0.0 -> v1.1.0 (regressed) -> v1.2.0 (fixed)
```

### Option B · Local Zero-Docker Quickstart (Go 1.23+ & Node 22+)
```bash
./scripts/dev-quickstart.sh
# Open http://127.0.0.1:8080/
```

### Run the Live Chaos Lab E2E Verification (< 3 seconds)
Uploads R8, Hermes SourceMap v3, and iOS dSYM symbol bundles, injects live HTTP 502s & obfuscated crashes/jank stalls, asserts source-line auto-symbolication, and delivers real Feishu + Slack Webhooks:
```bash
./demo-app/chaos-lab/run-chaos-e2e.sh
```

---

## Minimal SDK Integration

### Pure iOS (Swift SPM — Zero RN Dependencies)
```swift
import CaliperSDK

let client = CaliperClient(config: CaliperConfig(
    endpointURL: URL(string: "http://127.0.0.1:8080")!,
    resource: ResourceIdentity(
        appID: "dev.caliper.demo",
        appVersion: "1.0.0",
        buildNumber: "100",
        installHash: "0123456789abcdef"
    )
))
client.setScene("home_feed")
client.captureResourceWindow(windowDurationMS: 30_000, cpuPct: 14.2)
try await client.flush()
```

### Pure Android (Kotlin — Zero RN Dependencies)
```kotlin
val client = CaliperClient(CaliperConfig(
    endpointUrl = "http://127.0.0.1:8080",
    resource = ResourceIdentity(
        appId = "dev.caliper.demo",
        appVersion = "1.0.0",
        buildNumber = "100",
        installHash = "0123456789abcdef"
    )
))
val fluency = AndroidFluencySession(client, sdkInt = Build.VERSION.SDK_INT, scene = "home_feed")
// Feed FrameMetrics into fluency.onFrame(durMs, targetMs), then:
fluency.emitWindow()
client.flushAsync()
```

### Upload Build Symbols in CI
```bash
caliper-symbols upload \
  --server http://127.0.0.1:8080 \
  --app-id dev.caliper.demo \
  --platform android --version 1.1.0 --build 110 --git-sha 1100002abcdef \
  --kind r8_mapping --file app/build/outputs/mapping/release/mapping.txt
```

---

## Measured Host Benchmarks (2026-11-20)

Full details & physical-device Release gates: [`benchmarks/REPORT.md`](benchmarks/REPORT.md). Reproduce with `./benchmarks/run-overhead-bench.sh`.

| Component | Workload | Measured Result |
|---|---|---|
| Go Embedded Store | 100-event batch ingest | **104.3 µs / 100 events** (~958k events/s in-process) |
| Live HTTP + WAL Replay | 99,996 events (504 gzip batches) | **2.98 s** (**33,566 events/s**); compare query **0.34–0.90 ms** |
| RN Adapter OFF vs ON | 100k API calls + 10k LongTasks | **42 ns / API call**; **550× bridge traffic reduction** |
| Chaos Lab Full E2E | Symbol upload + 3-platform symbolication + 2 Webhooks | **2.74 s** end-to-end |

---

## Production Cluster & CI Plugins (v0.5)

See [`docs/roadmap-v0.5.md`](docs/roadmap-v0.5.md) for full details:
- **ClickHouse OLAP Backend**: Zero-CGO ClickHouse HTTP engine (`--storage-driver=clickhouse --clickhouse-endpoint=http://clickhouse:8123`) with partitioned `ReplacingMergeTree` tables and vectorized `sumForEach` histogram merges (`docker compose --profile cluster up`).
- **Statistical Regression Engine**: Two-proportion Z-test + Wilson 95% CIs for rate metrics, two-sample Histogram KS test for latency/memory quantiles, and automated per-scene regression scanner (`POST /v1/analytics/regressions/scan`).
- **RBAC & Fail-Closed Audited Session Lookup**: Role-scoped API keys/Bearer tokens (`ingest_only`, `viewer`, `operator`, `admin` via `--auth-config`), non-PII hex `user_hash`/`install_hash` timeline lookup (`GET /v1/sessions/lookup?user_hash=...&reason=INC-123`) with immutable append-only audit trail (`GET /v1/audit/logs`).
- **CI Build Plugins**: Android Gradle plugin (`packages/gradle-plugin`, ID `io.caliper.apm.symbols`), iOS Fastlane action (`packages/fastlane-plugin`), and composite GitHub Action (`packages/github-action`).

## Deep Diagnostics & OpenTelemetry Interoperability (v1.0)

See [`docs/roadmap-v1.0.md`](docs/roadmap-v1.0.md):
- **Online Native & Hermes CPU Flamegraphs**: Bounded folded-stack profiles (`cpu_profile`) auto-symbolicated via R8 / Hermes SourceMap v3 / iOS dSYM and rendered in the zoomable Console Flamegraph (`GET /v1/diagnostics/profiles/{id}/flamegraph`).
- **Trimmed Heap / OOM Deep Diagnostics**: Zero-PII class-level dominator summaries (`heap_snapshot`) with automated cross-version leak suspect diff (`POST /v1/diagnostics/heap/compare`).
- **Audited Remote Log Retrieval Tasks**: Pull bounded, PII-scrubbed diagnostic ring logs on demand (`POST /v1/diagnostics/log-tasks`, SDK poll + gzip upload, fail-closed immutable audit trail).
- **Full OTLP/HTTP JSON Interoperability**: Ingest standard OpenTelemetry traces & logs (`POST /v1/otlp/v1/traces`, `POST /v1/otlp/v1/logs`) and export aggregated `resourceMetrics` (`GET /v1/otlp/export`).

## Enterprise Console 2.3, Production SDKs & CI Release Gate (v1.1 – v1.4)

See [`CHANGELOG.md`](CHANGELOG.md):
- **Console 2.3 (ECharts Multi-Dimension APM Workbench)**: Bilingual (`中文`/`EN`) dark-chrome enterprise console with 14-day release rollout trends, **Release / Device Model / OS Version / Native-vs-RN** slicing, 19-bucket histograms, 5-domain network error breakdown, HTTP status code distribution, clickable slow/error network sample waterfalls, multi-stage custom business journey traces (`GET /v1/analytics/journeys`), cross-page drill-down, and visual single-user session timelines.
- **CI/CD Zero-Lie Release Quality Gate (`caliper-gate` + GitHub Action)**: Automated canary promotion gate (`POST /v1/analytics/release-gate`) + composite PR Comment Action (`packages/github-action/release-gate/action.yml`) returning exit code `0` (`PASS`), `2` (`BLOCK` on statistically significant regression), or `3` (`GATED` when under-sampled):
  ```bash
  caliper-gate --server http://127.0.0.1:8080 --platform android --baseline 1.0.0 --candidate 1.1.0 --format markdown
  ```
- **Stability Issue Lifecycle Triage & Impact Charts**: Persistent status workflow (`open` / `in_progress` / `resolved` / `ignored`, assignee, resolved-in version, note via `PATCH /v1/stability/issues/{fp}`), per-issue Device/OS/Release ECharts charts, and 1-click jump into Single-User Trace.
- **Dynamic Sampling & Collector Policy Control**: Hot-update sampling rate (`%` / PPM), 24h device quotas, lease TTL, and collector switches via `GET/PUT /v1/policy` with immutable audit logging, plus Data Source Health (`GET /v1/ops/source-health`) and Prometheus `GET /metrics`.
- **Production Mobile SDK Depth & Cluster HA**: Offline disk queues, main-thread Watchdogs, Android `CaliperAndroidAgent` (`FrameMetrics`, `ApplicationExitInfo`, `Debug.MemoryInfo`), iOS `CaliperMetricKitBridge`, RN native bridges, pure-Go AWS SigV4 S3/MinIO `--object-store=s3`, ClickHouse `device_model`/`os_version` parity, PostgreSQL metadata DDL, and Kubernetes Helm Chart (`deploy/helm/caliper-apm`).

---

## License

Apache License 2.0 — see [LICENSE](LICENSE).
