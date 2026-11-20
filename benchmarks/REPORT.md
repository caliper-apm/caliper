# Caliper APM — Reproducible Overhead Benchmark Report

> **Honesty Rule (`AGENTS.md` §4)**: Host micro-benchmarks validate algorithmic efficiency and memory bounds, but do **not** substitute for physical mid-tier Android/iOS Release device OFF-vs-ON measurements. Both are tracked separately below.

---

## 1. Host Micro-Benchmarks (Measured · 2026-11-20)

Environment: `darwin/arm64` (Apple Silicon), Go 1.26.5, Node 24.15.0, Apple Swift 6.3.1 (`-c release`).  
Reproduce via: `./benchmarks/run-overhead-bench.sh`

| Component | Workload | Measured Result | Notes |
|---|---|---|---|
| **Go Server (`EmbeddedStore`)** | 100-event batch ingest (`BenchmarkEmbeddedIngest100Batch`) | **104.3 µs / 100 events** (~958k events/s in-process, 1.08 KB/event) | Zero-CGO, race-free |
| **Go Server (Live HTTP + WAL)** | 99,996 events in 504 gzipped HTTP batches + WAL disk persistence | **2.98 s** (**33,566 events/s**); cross-version compare **0.34–0.90 ms** | Week 1 Exit Gate passed |
| **RN Adapter (`sdk-react-native`)** | 100,000 API calls + 10,000 LongTask entries + 100 window flushes | OFF `0.464 ms` vs ON `4.654 ms` (**42 ns / API call**, **550× bridge reduction**: 200 bridge calls for 110k raw observations, +419.7 KiB heap) | Zero per-call native bridge chatter |
| **iOS Native SDK (`sdk-ios`)** | Release build (`swift test -c release`) incl. Darwin `getrusage` + `phys_footprint` | **3/3 passed in 0.005 s** | Zero RN / third-party deps |
| **Chaos Lab Full E2E** | Build + upload 3 symbol bundles + live `mock-api` 502s + R8/SourceMap/dSYM auto-symbolication + 2 real Webhooks | **2.74 s** total wall time | Week 3 Exit Gate (< 15 s) passed |

---

## 2. North Star Physical Device Release Gates (Pending Physical Device Runs)

Target thresholds from [`NORTH_STAR.md`](../NORTH_STAR.md) (must be verified on physical mid-tier Android & iOS Release builds, SDK OFF vs ON):

| Metric | Target Gate | Android Release Status | iOS Release Status |
|---|---|---|---|
| Cold Start P95 Delta | ≤ 15 ms (or ≤ 1.5%) | `pending_physical_device` | `pending_physical_device` |
| Resident Memory Delta (PSS / `phys_footprint`) | ≤ 4 MiB | `pending_physical_device` | `pending_physical_device` |
| Active-State CPU Delta | ≤ 1% of 1 core | `pending_physical_device` | `pending_physical_device` |
| Binary Size Delta | Android < 300 KB / iOS < 500 KB | `pending_physical_device` | `pending_physical_device` |
| Self-Induced Crash Rate | 0 | `pending_physical_device` | `pending_physical_device` |
