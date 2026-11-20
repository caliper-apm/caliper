# Caliper APM — North Star Charter (v1.0 Frozen)

> **Status**: Frozen Baseline (2026-11-19)  
> **Governance Rule**: This document defines the permanent direction, quantifiable exit criteria, and hard non-goals of Caliper APM. **No AI agent or contributor may alter, weaken, merge stages in, or remove items from this charter without explicit written approval from the project owner.**

---

## 1. Positioning (What We Are Building)

An open-source, **Native-First (Android & iOS)**, **React Native First-Class Compatible**, lightweight self-hosted, **Evidence-First (Zero-Lie Analytics)** Mobile APM platform.

- **Native is the foundation**: Pure Android (Kotlin) and iOS (Swift) applications integrate with zero React Native dependency and receive full-depth observability.
- **React Native is a first-class adapter**: Hybrid apps (Native core + RN high-velocity screens) attach `adapter-react-native` onto the exact same Native lifecycle, window, and symbolication timeline—never two disconnected monitoring worlds.

---

## 2. North Star Goal

> Enable any mid-to-large Native or Native+RN mobile engineering team to **integrate the SDK and self-host the full APM stack within 15 minutes**, and reliably execute the complete production loop:
>
> **Automatically detect version/scenario regressions → Symbolicate to exact source line & frame/network evidence within minutes → Quantitatively verify fix impact with statistically valid comparisons.**

---

## 3. Quantifiable Expected Outcomes (Industry-Leading Exit Criteria)

### Outcome 1 · Verifiably Low Overhead & Zero Self-Harm
Published, reproducible Release-build device benchmarks (SDK OFF vs ON) must hold on representative mid-tier Android and iOS devices:
- Cold start P95 overhead: **≤ 15 ms** (or **≤ 1.5%** of baseline)
- Resident memory overhead (Android PSS / iOS `phys_footprint`): **≤ 4 MiB**
- Active-state CPU overhead: **≤ 1% of a single core**
- Binary size delta: **Android < 300 KB**, **iOS < 500 KB**
- Self-induced crash rate: **0** (all collector paths isolated, bounded, off main-thread I/O, fail-closed circuit breaker)

### Outcome 2 · Trustworthy Regression Detection & Root-Cause Speed
- Covers all 5 mobile pillars with truthful contracts:
  1. **Stability**: Native Crash, JS Error, ANR / Watchdog, Abnormal Exit (OOM/LMK)
  2. **Startup & Fluency**: Multi-stage Cold Start (Native Shell / Runtime / First Frame), FrameMetrics / CADisplayLink jank windows, same-window main-thread stall stacks, RN LongTasks
  3. **Resources**: Process CPU, Android PSS/RSS, iOS `phys_footprint`, per-scene attribution
  4. **Network**: Overall logical request success/latency denominator + Native transport stages (DNS / TCP / TLS / TTFB / Body)
  5. **Custom Experience**: Business-critical journeys with explicit start/terminal outcome semantics
- **Zero-Lie Statistics**: Mergeable histogram quantiles (never average daily P90), missing values never zero-filled, zero-frame windows never marked 100% smooth, explicit sample-size & same-policy comparison gates.
- **Attribution SLA**: End-to-end latency from client fault to symbolicated source line / stack / network waterfall in console **< 30 seconds**; routine crash/jank/slow-API triage compressed to **< 5 minutes**.

### Outcome 3 · Effortless Self-Hosting & Zero-Manual-Ops Lifecycle
- **Single-node mode** (Embedded DuckDB + SQLite): `docker compose up` brings up the complete stack in **< 60 seconds**.
- **Production cluster mode** (ClickHouse + Postgres + Object Storage, v0.5+): Horizontally scales to tens of millions of daily events without third-party analytics export quotas or T+1 delays.
- **Zero manual release registration**: Build plugins automatically register `app_version + build_number + git_sha + symbol_uuid` and upload symbols.
- **Actionable alerting**: Real scheduled rule evaluation with deduplicated external webhooks (Slack / Feishu / Discord / generic HTTP).

### Outcome 4 · Benchmark-Grade Hybrid Demo & Chaos Lab
- Ships a realistic, non-trivial **Hybrid Mobile Demo App** (Native core host with Feed/Video/Network + RN feature screens + standalone pure-Android / pure-iOS minimal examples) with a built-in **Chaos Lab** capable of injecting 8 classes of real faults and replaying a `v1.0 (Baseline) → v1.1 (Regressed) → v1.2 (Fixed)` story out of the box.

---

## 4. Hard Non-Goals & Anti-Drift Redlines

1. **Strict Clean-Room Purity**: Zero copying of any proprietary/employer source code, test fixtures, internal business terminology, credentials, or private telemetry artifacts. Enforced by `scripts/cleanroom-guard.sh` in pre-commit and CI.
2. **No Hollow UI / No Mock Placeholder Pages**: Every screen shipped in `console/` must be backed by real end-to-end ingested data. Never build fake/mock dashboard tabs or "coming soon" charts to inflate perceived progress.
3. **No Proprietary Warehouse Lock-In**: The default core stack must never depend on Firebase Analytics / GA4 export quotas or BigQuery batch scans.
4. **Overhead Veto Power**: No collector feature may ship without passing the OFF-vs-ON device overhead check or by relying on private/undocumented OS APIs or unsafe swizzling.
5. **Strict Stage Gating**: Deliver in strict order:
   - **v0.1**: Native-First + RN core loop, embedded DuckDB/SQLite server, auto-symbolication, 5 real-data console pages, Hybrid Demo + Chaos Lab, initial real-device overhead benchmark.
   - **v0.5**: ClickHouse production storage, statistical regression detector, build CI plugins, RBAC & audited UID lookup, full alerting workflows.
   - **v1.0**: Online native/Hermes CPU flamegraphs, trimmed heap/OOM deep diagnostics, remote log retrieval tasks, full OTLP interoperability.
   - **Rule**: Do not expand into the next stage while current-stage end-to-end acceptance gates remain red.
