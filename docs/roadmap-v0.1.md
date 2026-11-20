# Caliper APM — v0.1 MVP 4-Week Execution Roadmap

> Aligned with [`NORTH_STAR.md`](../NORTH_STAR.md). Every week has a hard, verifiable exit gate. Never advance while the previous week's gate is failing.

---

## Finalized v0.1 Tech Stack

| Layer | Choice | Notes |
|---|---|---|
| **Server** | **Go 1.23+** | Single binary, low footprint, high-concurrency collector, clean storage interfaces |
| **OLAP Storage (v0.1)** | **Embedded WAL Columnar Rollup Store** (DuckDB DDL ready) | Zero-CGO, zero-ops local analytics behind `AnalyticsStore`; DuckDB/ClickHouse adapters plug in for v0.5 |
| **Metadata Storage** | **Embedded WAL Store** (SQLite DDL ready) | Releases, symbol bundles, alert rules, delivery receipts behind `MetaStore` |
| **Wire Protocol** | **JSON + Gzip** (OTLP-aligned schema) | Inspectable in v0.1; Protobuf/OTLP wire encoding added in v0.5 |
| **Symbolication** | **R8 Retrace + SourceMap v3 VLQ + Breakpad/JSON dSYM** | Pure-Go zero-dependency build-bound source-line resolution for Android/iOS/RN |
| **Android SDK** | **Kotlin** (minSdk 26+ FrameMetrics; 24–25 graceful fallback) | Zero RN dependency; pure public OS & OkHttp APIs |
| **iOS SDK** | **Swift** (SPM primary, Podspec compatible) | Zero RN dependency; pure public `CADisplayLink`, `task_vm_info`, `URLSessionTaskTransactionMetrics` |
| **RN Adapter** | **TypeScript + TurboModule / JSI** | Optional first-class adapter attaching to Native lifecycle & windows |
| **Web Console** | **React 19 + TypeScript + Vite** | Clean-Room 5-page real-data UI (76.6 kB gzip bundle, zero copied code, zero mock placeholders) |
| **Demo & Automation** | **Native Host (Android/iOS) + RN Hybrid Screens + Chaos Lab + Maestro** | Plus minimal Pure-Android & Pure-iOS integration samples |

---

## v0.1 Strict Scope

### Included (End-to-End Real Data Required)
1. **Multi-Stage Cold Start**: Total + Native Init + Runtime/Bridge + First Content Frame (mergeable histogram P50/P90).
2. **Stability & Auto-Symbolication**: Native Crash, JS Error, ANR, Abnormal Exit markers → build-bound symbolication to source line + breadcrumbs.
3. **Fluency & Stall Stacks**: Android FrameMetrics / iOS CADisplayLink windows, same-window main-thread stall stack, RN `LongTask` window.
4. **Process Resources**: Android CPU/PSS and iOS CPU/`phys_footprint` windows grouped by scene.
5. **Network Denominator & Transport Stages**: Logical API success/duration denominator + Native DNS/TCP/TLS/TTFB/Body waterfall.
6. **Version Regression Comparison & Real Webhooks**: `v1.0` vs `v1.1` gated statistical comparison + scheduled Webhook alerts (Slack/Feishu/Discord/HTTP).

### Explicitly Excluded from v0.1 (Deferred to v0.5 / v1.0)
- Multi-tenant RBAC / OAuth login (v0.1 uses single-workspace API keys)
- Remote log retrieval tasks & cloud object storage
- Online native CPU flamegraphs & trimmed online HPROF upload
- ClickHouse / BigQuery adapters
- Any mock-only placeholder pages in `console/`

---

## Week-by-Week Breakdown & Exit Gates

### Week 1 · Protocol, Statistical Engine & Embedded Server
- [x] Bootstrap repo hygiene, CI workflow (`.github/workflows/ci.yml`), and `scripts/cleanroom-guard.sh` pre-commit hook.
- [x] Finalize `spec/protocol/` JSON Schemas (`envelope.schema.json`, `events.schema.json`) and `spec/metrics-contract.md`.
- [x] Finalize `packages/sdk-core/fixtures/contract-vectors.json` golden test vectors.
- [x] Implement Go `server/internal/collector` (`POST /v1/ingest`, Gzip, validation, API key guard, `GET /v1/policy`, releases & analytics routes).
- [x] Implement `server/internal/storage` (`AnalyticsStore` / `MetaStore` interfaces, zero-CGO WAL-durable columnar rollup `EmbeddedStore`, and canonical DuckDB/SQLite DDL in `sqlschema/schema.sql` ready for SQL driver plug-in).
- [x] Implement `server/internal/analytics` (histogram merge P50/P90, missing-reason preservation, zero-frame guard, sample-size & algorithm comparison gates) with golden contract + race-detector unit tests.
- [x] Implement `server/cmd/seed-replay` pushing realistic `v1.0.0 → v1.1.0 (regressed) → v1.2.0 (fixed)` events through real `POST /v1/ingest`.
- **Week 1 Exit Gate**: **PASSED (2026-11-19)** — Replayed 99,996 events in 504 gzipped HTTP batches in 2.98s (33.5k events/s) with WAL persistence; cross-version P50/P90 & fluency comparisons returned exact `regressed` / `improved` verdicts in **0.34–0.90 ms** (< 200 ms gate) with 100% golden contract & `-race` tests passing.

### Week 2 · Native-First SDKs (Android & iOS) + RN Adapter + Symbol CLI
- [x] Implement deterministic sampling, lease TTL, 24h sliding quota, bounded queue, and fail-closed circuit breaker in `packages/sdk-android` (Kotlin) and `packages/sdk-ios` (Swift) — both verified against `contract-vectors.json`.
- [x] Implement Native collectors & OS bindings on Android & iOS:
  - Cold start stage conservation & abnormal exit marker (`CaliperAbnormalExitMarker` / Android `ApplicationExitInfo` contract);
  - Crash & breadcrumb ring buffer (`BreadcrumbRingBuffer`);
  - FrameMetrics (API 31+ deadline / 26–30 heuristic) & `CADisplayLink` fluency windows (`FluencyWindowAccumulator`);
  - Darwin `getrusage` CPU & `task_info(TASK_VM_INFO) phys_footprint` live readings + Android PSS/CPU contracts;
  - `URLSessionTaskTransactionMetrics` transport extractor & OkHttp `EventListener` contract.
- [x] Verify **pure-Native compilation & tests** with zero RN dependencies (`swift test`: 3/3 passed; Gradle Kotlin `:caliper-core:test`: passed offline).
- [x] Implement `packages/sdk-react-native` (TurboModule/JSI bridge spec, route scene sync, LongTask window, logical API denominator window with strict bucket conservation, JS error capture — Node 24 `node --test`: 2/2 passed).
- [x] Implement `packages/cli-symbols` (`caliper-symbols upload`) + server `POST /v1/symbols/upload` & `GET /v1/symbols` with SHA-256 verification and automatic build-to-symbol binding.
- **Week 2 Exit Gate**: **PASSED (2026-11-19)** — All 4 languages (Go 1.26, Swift 6.3.1, Kotlin 2.1.20/JVM 21, TypeScript/Node 24) pass 100% golden contract parity against `contract-vectors.json`; live Gzip HTTP ingestion and `caliper-symbols upload` SHA-256 receipt verified end-to-end against running `caliper` server. (Physical Android/iOS device host integration continues in Week 3 Demo App.)

### Week 3 · Hybrid Complex Demo App + Chaos Lab + Symbolicator + Overhead Benchmark
- [x] Build `demo-app/mock-api` (controllable latency, streaming SSE, HTTP 502/503 fault endpoints, unit tested with `-race`).
- [x] Build Hybrid Demo core controllers (`demo-app/android-host/HomeFeedController.kt`, `demo-app/ios-host/HomeFeedController.swift`, `demo-app/rn-modules/StreamingReaderController.ts`) demonstrating pure-Native core + RN adapter integration.
- [x] Build **Chaos Lab** specification (`demo-app/chaos-lab/scenarios.json`), matching build symbol fixtures (R8, Hermes SourceMap v3, iOS dSYM JSON), and live E2E driver (`demo-app/chaos-lab/run-chaos-e2e.sh`).
- [x] Write Maestro replay flows (`demo-app/flows/01-baseline-healthy.yaml`, `02-chaos-regression-suite.yaml`).
- [x] Implement `server/internal/symbolicator` (R8 Retrace + SourceMap v3 VLQ + Breakpad/JSON dSYM auto-resolution on issues & fluency stall stacks) and `server/internal/alerts` (Zero-Lie sample-gated evaluation + cooldown + real Feishu/Slack/Discord/Generic Webhook delivery).
- [x] Implement `benchmarks/run-overhead-bench.sh` & `benchmarks/REPORT.md` capturing host OFF-vs-ON micro-benchmarks (physical mid-tier Release device gates explicitly tracked as `pending_physical_device`).
- **Week 3 Exit Gate**: **PASSED (2026-11-20)** — Live `run-chaos-e2e.sh` executed in **2.74 s** (< 15 s gate): uploaded R8 + SourceMap v3 + iOS dSYM bundles via `caliper-symbols`, triggered live `mock-api` 502s & Chaos faults, auto-symbolicated Android/RN/iOS crashes & fluency stall stacks to exact `File:Line`, and delivered real Feishu + Slack Webhooks (2/2 HTTP 200).

### Week 4 · Clean-Room Web Console + Single-Binary & Docker Quickstart + v0.1.0 Polish
- [x] Build the 5 real-data Console views (`console/src/pages/`, zero copied legacy code, zero mock tabs, Zero-Lie `format.ts` unit tested):
  1. **Overview & Version Comparison** (`OverviewPage.tsx`): KPI strip, `v1.0` vs `v1.1` vs `v1.2` delta matrix with explicit sample/missing/gate badges;
  2. **Stability** (`StabilityPage.tsx`): Crash/ANR/Abnormal-Exit issue explorer, auto-symbolicated stack & breadcrumb drawer;
  3. **Experience & Fluency** (`FluencyPage.tsx`): 3-stage cold start breakdown, per-scene jank rate & P90 frame duration, same-window symbolicated stall stack drawer;
  4. **Resources & Network** (`ResourcesNetworkPage.tsx`): Per-scene CPU/Memory/JS LongTask table + logical API denominator + DNS/TCP/TLS/TTFB/Body transport waterfall;
  5. **Releases, Symbols & Alerts** (`OperationsPage.tsx`): Auto-registered build directory, symbol coverage table, alert rule creation/evaluation & live incident history.
- [x] Wire single-binary static Console hosting (`caliper --console-dist`) with SPA fallback, immutable `/assets/` cache headers, and path-traversal protection (`TestConsoleStaticServingAndSecurity`).
- [x] Author multi-stage `Dockerfile`, `docker-compose.yml` (`caliper` + `mock-api` + `caliper-seed`), zero-Docker `scripts/dev-quickstart.sh`, and CI `console-web` job (`.github/workflows/ci.yml`).
- [x] Complete bilingual `README.md` (English) & `README.zh-CN.md` (Chinese), architecture diagram, published Overhead Benchmark table (`benchmarks/REPORT.md`), and full Clean-Room audit pass (`scripts/cleanroom-guard.sh`).
- **Week 4 Exit Gate**: **PASSED (2026-11-20, local single-binary path)** — `console` builds cleanly (`tsc --noEmit` 0 errors, `vite build` 388 ms / 76.6 kB gzip JS), `caliper --console-dist` serves built SPA + all 7 analytics APIs with 30k-event replay in **501 ms** (59.9k events/s) and **0.48–0.83 ms** cross-version compare latency; live `run-chaos-e2e.sh` passes in **2.37 s**; `cleanroom-guard.sh` reports 0 warnings. *(Note: `docker compose up` manifest is authored and ready for Docker hosts; local verification ran via native Go + Node because Docker CLI is not installed on the build host. 60s demo GIF and physical mid-tier Release device benchmark runs are tracked for public release polish / v0.5.)*
