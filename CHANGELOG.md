# Changelog

All notable releases of **Caliper APM** are documented here.

## v1.8.0 — Zero-Boilerplate ASM/iOS Auto-Instrumentation & Thermal/Disk/WebView Specialty Probes
- **Android Gradle ASM9 Auto-Instrumentation Plugin (`io.caliper.apm.autoinstrument`)**:
  - Added AGP 8.x `AsmClassVisitorFactory` (`packages/gradle-plugin/src/main/kotlin/io/caliper/apm/gradle/CaliperAsmWeaver.kt`) with strict package allowlist/denylist weaving `CaliperAutoInstrumentHooks.onOkHttpBuilder(...)` before `OkHttpClient$Builder.build()` and wrapping `@CaliperTrace` methods across normal returns and exception throws (`failed = true`), verified by live JVM bytecode execution (`CaliperAsmWeaverTest`).
- **iOS One-Line Auto-Instrumentation Agent (`CaliperIOSAgent`)**:
  - Added `CaliperIOSAgent.install(config:options:)` automatically coordinating C/Mach crash capture, 9-branch FOOM detection, Watchdog, MetricKit, zero-swizzle lifecycle/thermal observers, safe opt-in `UIViewController.viewDidAppear` scene tracking, forwarding `CaliperAutoURLSessionDelegate` (preserving host SSL pinning/auth delegates), and SwiftUI `.caliperScene(...)` modifier.
- **Battery/Thermal, Zero-PII Bounded Disk Audit & WebView/H5 Probes (iOS + Android)**:
  - Thermal/Battery escalation probes (`CaliperThermalMonitor` / `CaliperThermalBatteryProbe`) emitting `device_thermal_state` journeys.
  - 24h rate-limited, max-depth/max-files bounded sandbox disk auditors (`CaliperDiskAuditor` / `CaliperDiskAuditProbe`) with Zero-PII regex path scrubbing (emails, UUIDs, hex hashes, numeric IDs) emitting `disk_storage_audit` with per-bucket MiB stages and `DISK_QUOTA_EXCEEDED` detection.
  - Shared ~1.3 KB vanilla JS WebView/WKWebView probe (`CaliperWebViewBridge` / `CaliperWKWebViewBridge`) reporting NavigationTiming/FCP stages, URL query-stripped paths, `WEBVIEW_BLANK_SCREEN` detection, and JS errors with zero server schema breakage.

## v1.7.0 — Low-Level Mobile Native Crash, SIGQUIT ANR, Looper Tracer & iOS FOOM Engine
- **iOS Real-Time C/Mach Exception + Unix Signal Crash Reporter (`CaliperCrashCore` + `CaliperNativeCrashReporter`)**:
  - Zero-dependency pure-C Mach exception port listener (`EXC_BAD_ACCESS`, `EXC_BAD_INSTRUCTION`, `EXC_ARITHMETIC`, `EXC_CRASH`, arm64e PAC-safe register/FP unwinding) + 64 KiB `sigaltstack` async-signal-safe Unix signal handler (`SIGSEGV`, `SIGABRT`, `SIGBUS`, `SIGFPE`, `SIGILL`, `SIGTRAP`, `SIGSYS`), adding only **8.4 KiB** of `__TEXT` machine code.
  - Lock-free dyld image cache computes ASLR-subtracted relative hex offsets (`0x...`) + Mach-O `LC_UUID` with zero heap allocations in the crash path, verified end-to-end against `v1.6.0` Mach-O DWARF symbolication (`crash_app.c:4` exact source line).
- **iOS Heuristic Foreground OOM State Machine (`CaliperFOOMDetector`)**:
  - Deterministic 9-branch exclusion classifier ruling out first launch, clean exit, detected crash, debugger (`P_TRACED`), app upgrade, OS upgrade, device reboot (`kern.boottime`), and background Jetsam before attributing `abnormal_exit` (`FOOM_HEURISTIC`) with last `phys_footprint` & active scene.
- **Android NDK C++ Signal Crash & `SIGQUIT` ANR Library (`libcaliper_native.so` + `CaliperNativeBridge`)**:
  - Cross-compiled and verified with Android NDK 27.1 (`arm64-v8a`, `45 KiB` stripped, zero `-Wall -Wextra` warnings): 64 KiB `sigaltstack` fatal signal handler using `_Unwind_Backtrace` + pre-cached `/proc/self/maps` ELF-relative offsets directly consumable by `native_elf` DWARF symbolication, plus non-blocking `SIGQUIT` ANR pipe interceptor that forwards cleanly to ART's Signal Catcher.
- **Android Main-Thread `Looper` Message-Dispatch Latency & History Tracer (`LooperDispatchTracker` + `CaliperLooperMonitor`)**:
  - Zero-regex, zero-reflection `Looper.setMessageLogging` monitor recording slow UI dispatches (`>= 700 ms`) and attaching in-flight + 20-message `MessageQueue` history breadcrumbs to Watchdog and `SIGQUIT` ANR reports.

## v1.6.0 — Stateless Multi-Replica PostgreSQL HA & Native Mach-O/ELF DWARF Symbolicator
- **Runtime PostgreSQL `MetaStore` Driver (`--meta-driver=postgres`, zero-CGO `pgx/v5`)**: Implemented all 22 `storage.MetaStore` methods over PostgreSQL 15+ (`server/internal/storage/postgres/store.go`) with embedded idempotent startup migrations (`//go:embed schema.sql`, including `caliper_issue_states` and `native_elf`), atomic `ON CONFLICT` upserts, S3/Local `ObjectStore` blob integration, live `/healthz` DB ping probes, `docker-compose --profile cluster` PostgreSQL 16 service, and Helm Chart `metaStore` templates. Running `--storage-driver=clickhouse --meta-driver=postgres --object-store=s3` requires zero local disk state across N collector replicas.
- **Pure-Go Native Mach-O `.dSYM`/`.dSYM.zip` & Android NDK ELF `.so` DWARF Symbolicator (`server/internal/symbolicator/{dwarf.go,demangle.go}`)**:
  - Auto-detects raw Mach-O Thin, Universal/Fat Mach-O (arm64 priority), `.dSYM.zip` archives (with 256 MiB zip-bomb guard), Android NDK ELF-32/64 `.so` (`--kind native_elf`, 128 MiB upload limit), Breakpad `.sym`, and JSON address maps with zero CGO and zero external `llvm-symbolizer`.
  - Extracts Mach-O `LC_UUID` and ELF `.note.gnu.build-id`, resolves both absolute virtual addresses and `__TEXT` slide-relative offsets, parses `DW_TAG_subprogram` + `DW_TAG_inlined_subroutine` (expanding inlined frames with `inlined: true` and exact call-site `file:line`), falls back to Mach-O/ELF symbol tables, and includes a zero-dep Itanium C++ ABI demangler.
  - Enhanced `caliper-symbols upload` CLI to accept `--kind native_elf` and auto-zip `.dSYM` bundle directories in memory.

## v1.5.0 — Console 2.4 Enterprise UX Polish & Production OSS Documentation Suite
- **Global Device Model / OS Version Filter Bar & Dynamic Dimensions API (`GET /v1/ops/dimensions`)**: Added live-discovered `devices`, `os_versions`, and `scenes` selectors directly in the sticky global filter bar; wired `device_model` and `os_version` query parameters through Overview, Stability (`QueryIssues` now honors honest per-device/OS event counts), Fluency, Startup, Network, and Journeys.
- **Shareable URL Query State, Dark/Light Theme & 1-Click Diagnostic Export**: Synced active tab and all global filters (`platform`, `env`, `version`, `device`, `os`, `adapter`, `scene`, `range`) to URL query parameters with a 1-click "Share View" copy button, added persistent Dark/Light theme switching (`[data-theme="dark"]`), and added 1-click JSON Diagnostic Report export containing active filters, summary, top issues, source health, and release gate verdict.
- **Vite 8 / Rolldown Vendor Code Splitting**: Split `echarts-vendor` (`194 KB` gz) and `react-vendor` (`68 KB` gz) from the `32 KB` gz application shell, eliminating all >500 KB chunk warnings.
- **Production Open-Source Documentation Suite (`docs/`)**: Added five dedicated production guides with Mermaid diagrams and verified physical-device benchmarks — [`docs/architecture.md`](docs/architecture.md), [`docs/self-hosting.md`](docs/self-hosting.md), [`docs/sdk-android.md`](docs/sdk-android.md), [`docs/sdk-ios.md`](docs/sdk-ios.md), and [`docs/sdk-react-native.md`](docs/sdk-react-native.md).

## v1.4.1 — Dual Physical-Device Release Benchmarks Verified (Android + iOS) & Watchdog Tuning
- **Android Physical Release AOT Verification (`vivo V2343A`, Snapdragon 6 Gen 1, Android 14 / API 34)**: Cold Start `+6.0 ms` P50 (`-1 ms` P95, n=20), 15-fling scroll jank `+0.00%` (0/518 jank frames), PSS `+2.67 MiB`, pre-R8 Release APK delta `+28.0 KiB`, `0` self-induced crashes, and live physical-device E2E telemetry ingestion (`0` rejected).
- **iOS Physical Release Verification (`iPhone 13 Pro Max`, A15 Bionic, iOS 27.2.1, `-O wholemodule`)**: Cold Start `+3.98 ms` P50 (`+1.51 ms` P90, n=18), ProMotion scroll jank `+0.00%` aggregate (3/517 vs 3/516 frames), `phys_footprint` `+0.34 MiB`, active scroll CPU `+0.10%` of 1 core, stripped arm64 Mach-O binary delta `+247.5 KiB`, `0` self-induced crashes, and live physical-device E2E telemetry ingestion (`0` rejected).
- **Android Agent Real-SDK Fix & Tuning**: Fixed `FluencyWindowAccumulator` integration in `CaliperAndroidAgent.kt` verified directly against Android 35 SDK and tuned default `CaliperWatchdog` poll cadence to `1000 ms`.

## v1.4.0 — Deep Network Forensics, Multi-Stage Business Journeys, GitHub PR Gate Action & Device Bench Collector
- **Deep Network Slow/Error Samples & HTTP Status Code Distribution**: Added `status_codes` (`2xx` / `3xx` / `4xx` / `5xx` / `timeout`) onto `GET /v1/analytics/network`, bounded per-request transport sample retention (`GET /v1/analytics/network/samples`), HTTP Status Code ECharts bar chart, and a clickable Endpoint Drill-Down Drawer displaying individual DNS/TCP/TLS/TTFB/Body waterfalls with 1-click jump to Single-User Trace.
- **Custom Business Journey / Multi-Stage Transaction Traces (`custom_scene`)**: Extended `CustomScenePayload` with optional ordered `stages[]` across Go server, ClickHouse (`caliper_custom_scene_events`), Android Kotlin (`CaliperClient.recordCustomJourney`), iOS Swift (`CaliperClient.recordCustomJourney`), and RN TypeScript (`CaliperRNAdapter.recordCustomJourney`), served via `GET /v1/analytics/journeys` and visualized with multi-stage P90 waterfalls & 19-bucket distribution drawers in Console 2.3.
- **GitHub Action Zero-Lie PR Release Gate**: Added `packages/github-action/release-gate/action.yml` to run `caliper-gate` directly in CI, write to `$GITHUB_STEP_SUMMARY`, optionally post Markdown PR comments, and enforce `PASS`/`BLOCK`/`GATED` exit codes.
- **Physical Device Benchmark Collector**: Added `benchmarks/collect-device-bench.sh` to detect attached Android (`adb`) / iOS (`xcrun devicectl`) physical hardware and drive Release OFF-vs-ON Macrobenchmark/XCTMetric collection under Zero-Lie rules.

## v1.3.0 — Enterprise Actionability, CI/CD Release Quality Gate, S3 Wiring & Console 2.2
- **CI/CD Zero-Lie Release Quality Gate (`POST /v1/analytics/release-gate` + `caliper-gate` CLI)**: Turns Z-test + Wilson CI + KS-test + symbol-coverage checks into a deterministic canary promotion gate (`exit 0` PASS, `exit 2` BLOCK, `exit 3` GATED on under-sampled releases) with transparent `0..100` health score and copy-paste Markdown PR summaries.
- **Stability Issue Lifecycle Triage & Impact Breakdown**: Persistent issue states (`open` / `in_progress` / `resolved` / `ignored`, assignee, resolved-in version, note) via `PATCH /v1/stability/issues/{fp}` with immutable audit trail, per-issue Device / OS / Release ECharts impact charts, and 1-click "Trace Sample Session" drill-down into Single-User Trace.
- **Dynamic Sampling & Collector Policy Control (`PUT /v1/policy`)**: Live remote sampling rate (`%` / PPM), 24h device quota, lease TTL, and per-collector switches directly from Console 2.2 with immutable audit logging.
- **Alert Rule Management & Webhook Test Delivery**: Added `DELETE /v1/alerts/rules/{id}`, enable/pause toggle, and `POST /v1/alerts/rules/{id}/test` for 1-click Feishu/Slack/Discord delivery verification.
- **Production Storage Wiring & ClickHouse Parity**: Wired pure-Go AWS SigV4 S3/MinIO object store (`--object-store=s3 --s3-endpoint ... --s3-bucket ...`) into `caliper` server & Helm Chart for symbols/remote logs, and added full `device_model` / `os_version` column & `ListDimensionValues` parity to ClickHouse.
- **Console 2.2 Cross-Page Drill-Down & Visual Session Timeline**: Hardware-tier-differentiated seed telemetry, Release Quality Gate banner, clickable Overview -> Stability/Fluency drill-down, demo user presets, and color-coded chronological single-user timeline filters.

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
