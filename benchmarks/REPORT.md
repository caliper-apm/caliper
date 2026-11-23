# Caliper APM — Reproducible Overhead Benchmark Report

> **Honesty Rule (`AGENTS.md` §4)**: Host micro-benchmarks validate algorithmic efficiency and memory bounds, but do **not** substitute for physical mid-tier Android/iOS Release device OFF-vs-ON measurements. Both are tracked separately below with exact hardware & methodology disclosure.

---

## 1. Host Micro-Benchmarks (Measured · 2026-11-20)

Environment: `darwin/arm64` (Apple Silicon), Go 1.26.5, Node 24.15.0, Apple Swift 6.3.1 (`-c release`).  
Reproduce via: `./benchmarks/run-overhead-bench.sh`

| Component | Workload | Measured Result | Notes |
|---|---|---|---|
| **Go Server (`EmbeddedStore`)** | 100-event batch ingest (`BenchmarkEmbeddedIngest100Batch`) | **104.3 µs / 100 events** (~958k events/s in-process, 1.08 KB/event) | Zero-CGO, race-free |
| **Go Server (Live HTTP + WAL)** | 99,996 events in 504 gzipped HTTP batches + WAL disk persistence | **2.98 s** (**33,566 events/s**); cross-version compare **0.34–0.90 ms** | Week 1 Exit Gate passed |
| **RN Adapter (`sdk-react-native`)** | 100,000 API calls + 10,000 LongTask entries + 100 window flushes | OFF `0.464 ms` vs ON `4.654 ms` (**42 ns / API call**, **550× bridge reduction**: 200 bridge calls for 110k raw observations, +419.7 KiB heap) | Zero per-call native bridge chatter |
| **iOS Native SDK (`sdk-ios`)** | Release build (`swift test -c release`) incl. Darwin `getrusage` + `phys_footprint` | **6/6 passed in 0.13 s** | Zero RN / third-party deps |
| **Chaos Lab Full E2E** | Build + upload 3 symbol bundles + live `mock-api` 502s + R8/SourceMap/dSYM auto-symbolication + 2 real Webhooks | **2.74–2.96 s** total wall time | Week 3 Exit Gate (< 15 s) passed |

---

## 2. Physical Device Release Benchmarks (OFF vs ON)

### 2.1 Android Physical Device — Measured · 2026-11-23
- **Device**: `vivo V2343A` (Qualcomm Snapdragon 6 Gen 1 `parrot`, 12 GB RAM, 1080×2400 60/90/120 Hz display, Android 14 / API 34, `arm64-v8a`)
- **Build & Compilation**: Two separate Release APKs compiled with `d8 --release --min-api 26` + `zipalign` + `apksigner`, force-AOT compiled on device via `cmd package compile -m speed -f` (matching Play Cloud Profiles). `OFF` APK links zero Caliper classes; `ON` APK installs full `CaliperAndroidAgent` (100% active sampling lease, API 34 `FrameMetrics.DEADLINE`, `CaliperDiskQueue`, `CaliperWatchdog`, `Debug.MemoryInfo` PSS, `recordCustomJourney`, live gzipped HTTP upload via `adb reverse`).
- **Methodology**: 3 warmup cold starts discarded + 20 measured cold starts (`am force-stop` + `am start -W -S TotalTime`), followed by 15 consecutive finger-fling scrolls across a 500-card feed (`dumpsys gfxinfo`, `dumpsys meminfo`, `/proc/<pid>/stat`), plus live E2E telemetry verification.

| Metric | SDK OFF | SDK ON (Full 100% Lease) | Measured Delta | North Star Gate | Verdict |
|---|---:|---:|---:|---|---|
| **Cold Start Median (P50, n=20)** | `227.5 ms` | `233.5 ms` | **`+6.0 ms`** (`+2.6%`) | ≤ 15 ms | **PASS** |
| **Cold Start Mean (n=20)** | `231.2 ms` | `235.7 ms` | **`+4.5 ms`** (`+1.9%`) | ≤ 15 ms | **PASS** |
| **Cold Start P90 / P95 (n=20)** | `234 / 247 ms` | `246 / 246 ms` | **`+12 / -1 ms`** | ≤ 15 ms | **PASS** |
| **Scroll Fluency (`gfxinfo`, 15 flings)** | `512 frames`, `0` jank (`0.00%`), P50/P90/P99 `6/8/11 ms` | `518 frames`, `0` jank (`0.00%`), P50/P90/P99 `6/8/11 ms` | **`+0.00%` jank**, **`+0 ms`** | ≤ +1% jank | **PASS** |
| **Resident Memory (`TOTAL PSS`)** | `23,556 KiB` (`23.00 MiB`) | `26,288 KiB` (`25.67 MiB`) | **`+2.67 MiB`** (`+2,732 KiB`) | ≤ 4 MiB | **PASS** |
| **Release APK Binary Size (pre-R8)** | `639,397 B` (`624.4 KiB`) | `668,069 B` (`652.4 KiB`) | **`+28.0 KiB`** (`+28,672 B`) | < 300 KB | **PASS (10.7× under)** |
| **Active Burst CPU (`/proc/stat`, 15 flings)** | `314` ticks (`3.14 s`) | `333` ticks (`3.33 s`) | **`+19` ticks (`+0.19 s`)** over ~10.5 s burst incl. live gzip HTTP flush + 500 ms Watchdog | ≤ 1% steady-state (1 core) | **NOTE*** |
| **Self-Induced Crashes** | `0 / 22` | `0 / 26` | **`0`** | 0 | **PASS** |
| **Live Physical-Device E2E Ingest** | — | `25` startups, `159` API 34 `deadline` frames, PSS, `/api/v1/feed`, 3-stage custom journey | **`0` rejected events** | 0 schema errors | **PASS** |

> \* **Honest CPU Note**: The `+0.19 s` (`~1.8%` of 1 core during the 10.5 s short burst) included an immediate startup HTTP Gzip upload and 500 ms Watchdog polling inside the short scroll window. Steady-state amortized flush interval is 30–60 s, and default `CaliperWatchdog` poll interval was tuned to `1000 ms` following this physical measurement.

---

## 3. North Star Physical Device Release Gates Summary

| Metric | Target Gate | Android Physical Release (`vivo V2343A`, API 34) | iOS Physical Release |
|---|---|---|---|
| Cold Start P95 Delta | ≤ 15 ms (or ≤ 1.5%) | **PASS** (`-1 ms` P95 / `+6.0 ms` P50, n=20) | `pending_physical_device` |
| Resident Memory Delta (PSS / `phys_footprint`) | ≤ 4 MiB | **PASS** (`+2.67 MiB` PSS) | `pending_physical_device` |
| Active-State CPU Delta | ≤ 1% of 1 core (steady-state) | **Measured** (`+0.19 s` in 10.5 s burst w/ live HTTP flush; Watchdog tuned 500→1000 ms) | `pending_physical_device` |
| Binary Size Delta | Android < 300 KB / iOS < 500 KB | **PASS** (`+28.0 KiB`, pre-R8) | `pending_physical_device` |
| Self-Induced Crash Rate | 0 | **PASS** (`0 / 26` launches & scroll sessions) | `pending_physical_device` |
