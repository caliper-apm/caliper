# Changelog

All notable releases of **Caliper APM** are documented here.

## v1.2.0 — Production Mobile SDK Depth, Cluster HA & Volcano-Grade Multi-Dimension Governance
- **Android & iOS Production SDK Modules**: Bounded FIFO offline disk queues (`CaliperDiskQueue`), main-thread Watchdog ANR/hang stack capture (`CaliperWatchdog` / `CaliperMainThreadWatchdog`), PII-scrubbed diagnostic ring logs + remote pull poller, Android `CaliperAndroidAgent` (`ActivityLifecycleCallbacks`, API 31+/26-30 `FrameMetrics`, API 30+ `ApplicationExitInfo`, `UncaughtExceptionHandler` chain, `Debug.MemoryInfo` PSS), and iOS `CaliperMetricKitBridge`.
- **React Native Native Bridges & Auto-Instrumentation**: `wrapFetchForDenominator` allowlisted logical API interceptor + Kotlin & Swift native bridges attaching directly onto Native `CaliperClient`.
- **Runnable Demo Hosts & Pure-Native Examples**: SwiftPM-verified `demo-app/ios-host`, Android Studio `demo-app/android-host`, plus 15-line Pure-Android & Pure-iOS integration samples.
- **Volcano APM Plus Device/OS Slicing & Source Health**: `GET /v1/analytics/breakdown?dimension=device|os`, `GET /v1/ops/source-health` (ingest contract pass rate, symbol coverage, zero-frame ratio), and standard Prometheus `GET /metrics` self-observability.
- **Production Cluster & Open-Source Launch Kit**: Pure-Go AWS SigV4 S3/MinIO `objectstore`, PostgreSQL cluster `MetaStore` schema, Kubernetes Helm Chart (`deploy/helm/caliper-apm`), Docker Compose CI workflow, GoReleaser & release automation, and physical-device Macrobenchmark/XCTMetric harnesses.

## v1.1.0 — Console 2.0 (ECharts Enterprise Visualizations & Daily Time-Series Engine)
- Rebuilt Clean-Room Web Console aligned with enterprise APM UX (Volcano APM Plus paradigm): dark global chrome, grouped sidebar, sticky multi-dimension filter bar, bilingual (`中文`/`EN`), ECharts sparklines, 14-day release rollout trends, 19-bucket distribution histograms, 5-domain error donut, and multi-tab issue drawer.
- Added `GET /v1/analytics/timeseries` and `GET /v1/analytics/breakdown?dimension=version|adapter|scene`.
- Codified permanent Console Visual & Interactive Quality Gates in `AGENTS.md` and `docs/lessons-learned-console-v2.md`.

## v1.0.0 — Deep Diagnostics, Remote Log Retrieval & Full OTLP Interoperability
- Online Native & Hermes CPU flamegraphs (`cpu_profile`) with build-bound R8 / SourceMap v3 / dSYM auto-symbolication and zoomable Console viewer.
- Zero-PII trimmed heap / OOM dominator snapshots (`heap_snapshot`) with cross-release leak suspect diff (`POST /v1/diagnostics/heap/compare`).
- Audited remote diagnostic log retrieval tasks (`POST/GET /v1/diagnostics/log-tasks*`).
- Full OTLP/HTTP JSON receiver (`POST /v1/otlp/v1/traces`, `/v1/otlp/v1/logs`) and `resourceMetrics` exporter (`GET /v1/otlp/export`).

## v0.5.0 — ClickHouse OLAP, Statistical Regression Engine, RBAC & CI Plugins
- Zero-CGO ClickHouse HTTP OLAP backend (`ReplacingMergeTree` + `sumForEach` histograms).
- Two-Proportion Z-test + Wilson 95% CIs, two-sample Histogram KS test, and automated per-scene regression scanner (`POST /v1/analytics/regressions/scan`).
- Role-based API key / Bearer RBAC (`ingest_only`, `viewer`, `operator`, `admin`) + fail-closed audited UID/session timeline lookup.
- Android Gradle plugin (`io.caliper.apm.symbols`), iOS Fastlane plugin, and composite GitHub Action.

## v0.1.0 — MVP Baseline
- Native-First Android (Kotlin), iOS (Swift SPM), optional RN TypeScript adapter, golden contract parity across 4 languages, pure-Go single-binary server, R8/SourceMap/dSYM symbolicator, real Feishu/Slack/Discord/HTTP webhooks, and Chaos Lab E2E suite.
