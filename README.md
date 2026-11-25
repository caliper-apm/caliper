<div align="center">

# Caliper APM

**Native-First, React Native Compatible, Evidence-First (Zero-Lie) Open-Source Mobile APM.**

[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/caliper-apm/caliper?color=165dff)](https://github.com/caliper-apm/caliper/releases)
[![CI](https://github.com/caliper-apm/caliper/actions/workflows/ci.yml/badge.svg)](https://github.com/caliper-apm/caliper/actions/workflows/ci.yml)
[![Docker E2E](https://github.com/caliper-apm/caliper/actions/workflows/docker-e2e.yml/badge.svg)](https://github.com/caliper-apm/caliper/actions/workflows/docker-e2e.yml)
[![Go: Zero-CGO](https://img.shields.io/badge/Go-1.23%2B%20Zero--CGO-00ADD8.svg)](server/go.mod)

**English** · [简体中文](README.zh-CN.md) · [日本語](README.ja.md) · [한국어](README.ko.md) · [Español](README.es.md) · [Deutsch](README.de.md)

[Architecture](docs/architecture.md) · [Self-Hosting](docs/self-hosting.md) · [Android SDK](docs/sdk-android.md) · [iOS SDK](docs/sdk-ios.md) · [RN Adapter](docs/sdk-react-native.md) · [Benchmarks](benchmarks/REPORT.md) · [Changelog](CHANGELOG.md)

<br />

<img src="docs/assets/console-overview-dark.png" alt="Caliper APM Console 2.4 — App Overview (Dark Mode)" width="100%" />

*Real-data Console 2.4 running on a single zero-CGO Go binary (~15 MiB idle / ~69 MiB at 30k events) with DDSketch (±2%) P50/P90/P95/P99 trends, release rollout markers, multi-dimension slicing, Native vs React Native comparison, Dark/Light themes, and 6-language UI.*

</div>

---

## Console 2.4 Visual Tour (100% Real Ingested Data)

| **Startup & Fluency Workbench** | **Network & Resources (5-Domain + Waterfall)** |
| :---: | :---: |
| <img src="docs/assets/console-fluency-dark.png" alt="3-Stage Cold Start & 19-Bucket Latency Histogram" width="100%" /> | <img src="docs/assets/console-network-dark.png" alt="Logical API Success Rate, 5-Domain Failure Donut & Latency Distribution" width="100%" /> |
| **Deep Diagnostics (CPU Flamegraph & Heap Leak Diff)** | **Crashes, ANR & Auto-Symbolication (Light Mode)** |
| <img src="docs/assets/console-diagnostics-dark.png" alt="Online CPU Flamegraph & Trimmed Heap Dominator Leak Diff" width="100%" /> | <img src="docs/assets/console-stability-light.png" alt="Daily Crash/ANR/Abnormal Exit Trend & Issue Explorer in Light Mode" width="100%" /> |

---

## Why Caliper APM?

Most mid-to-large mobile apps run **Native code for performance-critical core flows** (feeds, media, startup, networking) and **React Native for fast-moving business screens**. Existing tooling leaves a painful gap:
- Client-only diagnostic libraries lack a backend, cross-version analytics, symbolication pipeline, and alerting.
- Heavy general-purpose observability suites require large clusters to self-host and lack deep mobile fluency, multi-stage startup, and zero-lie statistical rigor.
- Commercial closed-source APMs are expensive black boxes with opaque aggregation formulas.

**Caliper APM** fills that gap with five core principles:
1. **Native-First Core + RN First-Class Adapter**: `sdk-android` (Kotlin + NDK) and `sdk-ios` (Swift SPM + C/Mach) have **zero React Native dependencies**. Hybrid apps attach `sdk-react-native` onto the exact same Native lifecycle, window correlation, W3C `traceparent`, and symbolication timeline.
2. **Zero-Lie Analytics Engine**: Pure-Go zero-dependency **DDSketch ($\alpha = 2\%$)** for mergeable `P50 / P90 / P95 / P99` quantiles and **`DDSketchKSTest`** intra-bucket regression detection (with backward-compatible 19-bucket histogram interpolation, never averaged daily P90), missing values never zero-filled, `frame_count == 0` never reported as smooth, Android API 31+ `deadline` vs `heuristic` strictly separated, and explicit sample-size gates (`n >= 100` for release deltas).
3. **Built-In Multi-Format Auto-Symbolication**: Pure-Go R8/ProGuard Retrace, Source Map v3 VLQ (JS/Hermes), **native Mach-O `.dSYM`/`.dSYM.zip` & Android NDK ELF `.so` DWARF (with inlined-frame expansion, pure-Go Swift 5+ demangler & Itanium C++ demangling)**, and Breakpad `.sym` resolution bound strictly to `app_version + build_number + symbol_id`.
4. **Actionable Real Alerting**: Sample-gated rule evaluation (including `P95`/`P99` tail alerts) with cooldown deduplication and native **Feishu Interactive Card**, **Slack Block Kit**, **Discord Embed**, and **Generic JSON** Webhooks.
5. **Single-Binary Self-Hosting + Chaos Lab Showcase**: Zero-CGO Go binary serving both APIs and the 5-page real-data React 19 Web Console, paired with the **Atlas Reader** hybrid demo & **Chaos Lab**.

---

## Architecture

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/architecture-dark.png" />
    <source media="(prefers-color-scheme: light)" srcset="docs/assets/architecture-light.png" />
    <img src="docs/assets/architecture-light.png" alt="Caliper APM — End-to-End System Architecture (Native-First Mobile Edge, Pure-Go Zero-CGO Server Platform, Dual-Mode Storage, CI Gate & AI MCP)" width="100%" />
  </picture>
</p>

- **Data Plane vs. Control Loop**: Mobile SDKs push gzipped OTLP-aligned telemetry (`POST /v1/ingest`, W3C `traceparent`) while pulling deterministic FNV-1a sampling leases (`GET /v1/policy`) and bounded remote log tasks (`/v1/diagnostics/log-tasks/*`) back from the Go Core.
- **Zero-CGO All-in-One Server Platform**: `server/cmd/caliper` (`CGO_ENABLED=0`) unifies fail-closed multi-tenant RBAC, Zero-Lie `DDSketch (±2%)` statistical analytics & profiling, and pure-Go in-process symbolication (R8, SourceMap v3, Mach-O `.dSYM`/ELF `.so` DWARF, Swift 5+ & Itanium C++, Breakpad) with zero external symbolicator sidecars.
- **Pluggable Dual-Mode Storage & Ecosystem**: Start in < 60s with embedded JSONL WAL + local artifacts (~50–100 MiB RAM), or scale horizontally onto **ClickHouse + PostgreSQL + S3/MinIO** via the identical `storage.Store` / `objectstore.Store` interfaces — serving Console 2.4, `caliper-gate`, AI-Native MCP (`2025-06-18`), webhooks, and async OTLP/HTTP export with zero SDK changes.

---

## Quickstart (< 1 Minute)

### Option A · Docker Compose (Recommended)
```bash
git clone https://github.com/caliper-apm/caliper.git
cd caliper
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

## Measured Benchmarks (Host + Real Physical Devices · 2026-11-23)

Full methodology, raw distributions & physical-device Release gates: [`benchmarks/REPORT.md`](benchmarks/REPORT.md). Reproduce host benches via `./benchmarks/run-overhead-bench.sh` or physical benches via `./benchmarks/collect-device-bench.sh`.

| Platform / Component | Workload (Release OFF vs ON) | Measured Result |
|---|---|---|
| **Android Physical (`vivo V2343A`, API 34, AOT)** | 20 cold starts + 15-fling scroll + PSS + APK size | Cold Start **`+6.0 ms` P50 (`-1 ms` P95)** · Scroll Jank **`+0.00%`** · PSS **`+2.67 MiB`** · APK **`+28.0 KiB`** · Crashes **`0`** |
| **iOS Physical (`iPhone 13 Pro Max`, iOS 27.2.1, `-O`)** | 18 cold starts + 3×3s ProMotion scroll + `phys_footprint` + Mach-O | Cold Start **`+3.98 ms` P50 (`+1.51 ms` P90)** · Scroll Jank **`+0.00%`** · Memory **`+0.34 MiB`** · CPU **`+0.10%`** · Binary **`+247.5 KiB`** |
| **Go Embedded Store + Live WAL Replay** | 100-event batch / 99,996-event gzip HTTP replay | **104.3 µs / 100 events** · **33,566–76,904 events/s** · compare query **0.17–0.90 ms** |
| **RN Adapter (`sdk-react-native`)** | 100k API calls + 10k LongTasks | **42 ns / API call** · **550× bridge traffic reduction** |
| **Chaos Lab Full E2E** | 3-platform R8/SourceMap/dSYM symbolication + 2 real Webhooks | **2.74–2.96 s** end-to-end |

---

## Production Cluster & CI Plugins (v0.5)

See [`docs/roadmap-v0.5.md`](docs/roadmap-v0.5.md) for full details:
- **ClickHouse OLAP Backend**: Zero-CGO ClickHouse HTTP engine (`--storage-driver=clickhouse --clickhouse-endpoint=http://clickhouse:8123`) with partitioned `ReplacingMergeTree` tables, vectorized `sumForEach` histogram merges, and native `sumMapIf` **DDSketch (±2%) persistence** (`docker compose --profile cluster up`).
- **Statistical Regression Engine**: Two-proportion Z-test + Wilson 95% CIs for API rates, **window-clustered fluency Z-test (`cluster_proportion_z`)** avoiding frame-autocorrelation false positives, **`DDSketchKSTest` (`ddsketch_ks`)** + 19-bucket fallback, and **Benjamini-Hochberg FDR** multiple-testing correction in `POST /v1/analytics/regressions/scan`.
- **RBAC & Fail-Closed Audited Session Lookup**: Role-scoped API keys/Bearer tokens (`ingest_only`, `viewer`, `operator`, `admin` via `--auth-config`), non-PII hex `user_hash`/`install_hash` timeline lookup (`GET /v1/sessions/lookup?user_hash=...&reason=INC-123`) with immutable append-only audit trail (`GET /v1/audit/logs`).
- **CI Build Plugins**: Android Gradle plugin (`packages/gradle-plugin`, ID `io.caliper.apm.symbols`), iOS Fastlane action (`packages/fastlane-plugin`), and composite GitHub Action (`packages/github-action`).

## Deep Diagnostics, OTLP Push & AI-Native MCP Server (v1.0 – v1.10)

See [`docs/roadmap-v1.0.md`](docs/roadmap-v1.0.md):
- **CPU Flamegraphs & Trimmed Heap Diagnostics *(Server Engine Ready + Android Java CPU Sampler)***: Server-side auto-symbolicated folded-stack flamegraphs (`GET /v1/diagnostics/profiles/{id}/flamegraph`) and cross-version class dominator diff (`POST /v1/diagnostics/heap/compare`) are implemented and verified end-to-end; Android ships `CaliperCpuProfiler` (Java stack sampling), while automated iOS/Hermes profilers and optional client heap/OOM plugins are on the v2.0 roadmap.
- **Audited Remote Log Retrieval Tasks**: Pull bounded, PII-scrubbed diagnostic ring logs on demand (`POST /v1/diagnostics/log-tasks`, SDK poll + gzip upload, fail-closed immutable audit trail).
- **Full Duplex OTLP Interoperability**: Ingest OTLP/HTTP JSON traces & logs (`POST /v1/otlp/v1/traces`, `POST /v1/otlp/v1/logs`), export aggregated `resourceMetrics` (`GET /v1/otlp/export`) and W3C `CLIENT` spans (`GET /v1/otlp/export/traces`), AND stream live W3C `traceparent` spans asynchronously via **OTLP Push Exporter** (`--otlp-traces-endpoint=http://tempo:4318/v1/traces`) directly to Jaeger, Grafana Tempo, or an OTel Collector.
- **AI-Native Caliper MCP Server (`POST /v1/mcp` + `caliper-mcp`)**: Zero-dependency Model Context Protocol (MCP) JSON-RPC 2.0 server (Streamable HTTP `2025-06-18` / `2025-03-26` / `2024-11-05`, `Origin` DNS-rebinding protection, fail-closed RBAC `app_id` tenant scoping, and bounded output budgets) exposing `caliper_overview`, `caliper_compare_releases`, `caliper_evaluate_gate`, `caliper_scan_regressions`, `caliper_list_issues`, and `caliper_inspect_issue` (with live pure-Go R8/dSYM/SourceMap/Swift 5+ symbolication) directly to Cursor, Claude Code, Codex, and Antigravity — with **zero server-side LLM API keys required**:
  ```json
  {
    "mcpServers": {
      "caliper-apm": {
        "command": "caliper-mcp",
        "args": ["--server", "http://127.0.0.1:8080"]
      }
    }
  }
  ```
- **Zero-Lie Release Binary Size Tracking (`binary_size_bytes` + `binary_artifact_kind`)**: Optional CI-registered artifact byte size (`apk`, `aab`, `ipa`, `xcarchive`, `bundle`) with zero mobile SDK overhead; `ReleaseGateReport` compares deltas strictly between matching artifact kinds (never mixing `.aab` with `.apk`, and never zero-filling missing sizes).

## How Caliper Compares (Honest Ecosystem Positioning)

| Platform / Project | License | Where It Excels | How Caliper Differs |
|---|---|---|---|
| **Tencent Matrix / ByteDance OSS (`btrace`, `ShadowHook`, `ByteHook`, `Tailor`) / Kuaishou KOOM / xCrash** | BSD-3 / Apache-2.0 / MIT | Best-in-class client-side native hook, HPROF trimming, Perfetto trace, and deep OS diagnostics. | Client diagnostic libraries without a self-hosted backend, symbolication pipeline, analytics UI, or release gate. Caliper ships a complete 15-minute full-stack platform with strict zero-hook safety in Core. |
| **Sentry Self-Hosted** *(and lightweight clones: GlitchTip / Bugsink)* | FSL-1.1-Apache-2.0 *(free for internal self-hosting; prohibits competing SaaS; converts to Apache-2.0 after 2y)* | Mature 30+ language ecosystem, mobile Session Replay, massive integrations. | Sentry Self-Hosted requires 16–32 GB RAM and 20+ containers; GlitchTip/Bugsink focus on error tracking without mobile performance analytics. Caliper is pure OSI **Apache-2.0**, boots as a single zero-CGO Go binary (with bounded dedup & WAL compaction), scales to ClickHouse + PG + S3, and centers on mobile Zero-Lie statistical release gating. |
| **Measure.sh** | Apache-2.0 | Mobile-first full-stack APM with Android, iOS, Flutter, React Native (Expo), KMP, lightweight layout-snapshot timelines, adaptive capture, and MCP server. | Caliper focuses on **Zero-Lie statistical rigor** (DDSketch ±2% in both Embedded and ClickHouse, zero-frame/algorithm isolation, cluster-adjusted fluency Z-test, BH-FDR), pure-Go zero-CGO single-binary startup + Swift 5+/DWARF symbolicator, and built-in CI/CD canary blocking (`caliper-gate`). |
| **OpenTelemetry Mobile / Embrace OSS SDK + Grafana / SigNoz** | Apache-2.0 | Open vendor-neutral OTLP pipelines unified with backend observability. | Requires assembling mobile-specific fluency contracts, FOOM state machines, native dSYM/R8/SourceMap symbolication, and release regression gates. Caliper interoperates via OTLP ingest & async trace push while delivering turnkey mobile analytics out of the box. |

## Enterprise Console 2.4, Production SDKs, DDSketch & CI Release Gate (v1.1 – v1.10)

See [`CHANGELOG.md`](CHANGELOG.md):
- **Pure-Go DDSketch ($\pm 2\%$) Tail Quantiles & `DDSketchKSTest` (`v1.10`)**: Clean-room, zero-dependency logarithmic `DDSketch` (`server/internal/contract/ddsketch.go`) delivering $\le 2\%$ relative-error `P50 / P90 / P95 / P99` quantiles across Startup, Resources, Network Transport, Custom Journeys, Daily Trends, Alerting, OTLP export, and Console 2.4, plus `ddsketch_ks` intra-bucket regression detection with automatic 19-bucket linear-interpolation fallback.
- **Pure-Go Swift 5+ Demangler, W3C `traceparent` & AGP 8.7 + R8 Verification (`v1.9`)**: Zero-dependency Swift 5+ symbol demangler (covering common production frames, cross-verified on 32 unit vectors + 18 live `swiftc`-compiled symbols against Apple `xcrun swift-demangle`, with honest fallback on unrecognized syntax), end-to-end opt-in W3C `traceparent` propagation across Android (`CaliperOkHttpNetworkBridge`), iOS (`CaliperAutoURLSessionDelegate`), React Native (`wrapFetchForDenominator`), ClickHouse, Console 2.4 `trace:` badges, and `GET /v1/otlp/export/traces`, plus full AGP 8.7.3 + OkHttp 4.12 + NDK 27 + R8 8.7.18 `dexdump` verification (`examples/android-agp-r8-sample`).
- **Console 2.4 (ECharts Multi-Dimension APM Workbench)**: **6-language UI (`English / 简体中文 / 日本語 / 한국어 / Español / Deutsch`)** with **Dark & Light theme** toggle, **global Device Model / OS Version / Scene / Version / Adapter filter bar** (`GET /v1/ops/dimensions`), **shareable URL query state**, **1-click Export Diagnostic JSON Snapshot**, Vite vendor code-splitting (`39 KB` gz app shell), 14-day release rollout trends with P50/P90/P95/P99, 19-bucket histograms, 5-domain network error breakdown, HTTP status code distribution, clickable slow/error network sample waterfalls with W3C `trace:` badges, multi-stage custom business journey traces (`GET /v1/analytics/journeys`), cross-page drill-down, and visual single-user session timelines.
- **CI/CD Zero-Lie Release Quality Gate (`caliper-gate` + GitHub Action)**: Automated canary promotion gate (`POST /v1/analytics/release-gate`) + composite PR Comment Action (`packages/github-action/release-gate/action.yml`) returning exit code `0` (`PASS`), `2` (`BLOCK` on statistically significant regression), or `3` (`GATED` when under-sampled):
  ```bash
  caliper-gate --server http://127.0.0.1:8080 --platform android --baseline 1.0.0 --candidate 1.1.0 --format markdown
  ```
- **Stability Issue Lifecycle Triage & Impact Charts**: Persistent status workflow (`open` / `in_progress` / `resolved` / `ignored`, assignee, resolved-in version, note via `PATCH /v1/stability/issues/{fp}`), per-issue Device/OS/Release ECharts charts, honest device/OS-scoped issue counts, and 1-click jump into Single-User Trace.
- **Dynamic Sampling & Collector Policy Control**: Hot-update sampling rate (`%` / PPM), 24h device quotas, lease TTL, and collector switches via `GET/PUT /v1/policy` with immutable audit logging, plus Data Source Health (`GET /v1/ops/source-health`) and Prometheus `GET /metrics`.
- **Production Mobile SDK Depth, Zero-Boilerplate Auto-Instrumentation & Stateless Cluster HA (`v1.6 – v1.8`)**: **Android Gradle ASM9 plugin (`io.caliper.apm.autoinstrument` for OkHttp & `@CaliperTrace`)**, **1-line `CaliperIOSAgent` + SwiftUI `.caliperScene()` + forwarding `URLSession` metrics proxy**, **Battery/Thermal, Zero-PII Disk Audit & WebView/WKWebView blank-screen probes**, **iOS real-time C/Mach Exception + Unix Signal crash reporter (`8.4 KiB` C core) & 9-branch FOOM state machine**, **Android NDK C++ signal crash & `SIGQUIT` ANR library (`libcaliper_native.so`, zero `libc++_shared.so`, `45 KiB` arm64 stripped) + Main-Thread `Looper` MessageQueue latency tracer**, **runtime zero-CGO PostgreSQL `MetaStore` (`--meta-driver=postgres`)**, pure-Go AWS SigV4 S3/MinIO `--object-store=s3`, ClickHouse OLAP, native Mach-O `.dSYM`/ELF `.so` DWARF symbolication, and Kubernetes Helm Chart (`deploy/helm/caliper-apm`).

---

## Documentation Hub

| Guide | What You'll Learn |
|---|---|
| [`docs/architecture.md`](docs/architecture.md) | System topology (Mermaid), Zero-Lie histogram & statistical gate architecture, Clean-Room design choices |
| [`docs/self-hosting.md`](docs/self-hosting.md) | Embedded WAL single-binary vs ClickHouse + S3 HA Cluster, Kubernetes Helm, RBAC, GitHub PR Gate, Prometheus |
| [`docs/sdk-android.md`](docs/sdk-android.md) | Pure-Kotlin Android integration (`CaliperAndroidAgent`, `FrameMetrics`, Watchdog, Gradle R8 plugin, `vivo V2343A` benchmarks) |
| [`docs/sdk-ios.md`](docs/sdk-ios.md) | Pure-Swift SPM iOS integration (`CaliperClient`, `CaliperMetricKitBridge`, Watchdog, Fastlane dSYM plugin, `iPhone 13 Pro Max` benchmarks) |
| [`docs/sdk-react-native.md`](docs/sdk-react-native.md) | React Native First-Class Adapter (`wrapFetchForDenominator`, 550× bridge compression, Hermes SourceMap v3, Kotlin/Swift bridges) |
| [`spec/metrics-contract.md`](spec/metrics-contract.md) & [`NORTH_STAR.md`](NORTH_STAR.md) | Frozen architectural charter & Zero-Lie statistical metrics specification |

---

## License

Apache License 2.0 — see [LICENSE](LICENSE).
