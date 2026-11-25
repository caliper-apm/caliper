# Caliper APM Architecture

Caliper APM is designed around three non-negotiable engineering axioms frozen in [`NORTH_STAR.md`](../NORTH_STAR.md):
1. **Native-First, React Native Compatible**: Android (`caliper-core` / `caliper-android`) and iOS (`CaliperSDK`) ship with **zero React Native dependencies**. Hybrid apps attach `@caliper-apm/react-native` onto the exact same Native lifecycle, window correlation, and symbolication timeline.
2. **Zero-Lie Statistics (Evidence-First)**: Pure-Go zero-dependency **DDSketch ($\alpha = 2\%$)** for mergeable `P50 / P90 / P95 / P99` quantiles and `DDSketchKSTest` intra-bucket regression detection, paired with backward-compatible 19-bucket histograms (with intra-bucket linear interpolation, never averaging daily P90s), zero-frame windows strictly isolated from smoothness denominators, missing values never zero-filled, Android API 31+ `deadline` separated from API 26–30 `heuristic`, and explicit sample-size gates (`n >= 10` display, `n >= 100` release comparison).
3. **Zero-CGO Single Binary -> Stateless Horizontal Cluster**: Start on a laptop with one pure-Go binary (`EmbeddedStore` WAL), and scale seamlessly to ClickHouse OLAP + PostgreSQL 15+/16 HA `MetaStore` (`pgx/v5`) + S3/MinIO object storage + Kubernetes Helm without changing a single mobile SDK line.

---

## End-to-End System Topology

```mermaid
flowchart LR
    subgraph Mobile["Mobile Clients (Native-First + Optional RN)"]
        AND["Android Native SDK\n(Kotlin + NDK · ASM9 · OkHttp W3C traceparent)"]
        IOS["iOS Native SDK\n(Swift SPM + C/Mach · FOOM · URLSession W3C)"]
        RN["Optional RN Adapter\n(550x bridge compression · LongTask · W3C Fetch)"]
        RN --> AND
        RN --> IOS
    end

    subgraph Ingest["Caliper Go Server (Zero-CGO)"]
        COL["POST /v1/ingest (Gzip)\nPOST /v1/otlp/v1/{traces,logs}\nGET/PUT /v1/policy · RBAC"]
        SYM["Pure-Go Symbolicator\n(R8 · SourceMap v3 · Mach-O/ELF DWARF · Swift 5+ & C++)"]
        ANA["Zero-Lie Analytics & Stat Engine\n(DDSketch ±2% P50/P90/P95/P99 · Z-Test · DDSketch KS · Gate)"]
        ALT["Alert Engine\n(Sample-Gated · Feishu / Slack / Discord / HTTP)"]
    end

    subgraph Storage["Pluggable Storage Tier"]
        EMB["Embedded WAL-Columnar\n(Single-Binary Dev / Small Prod)"]
        CH["ClickHouse HTTP OLAP\n(ReplacingMergeTree · sumForEach)"]
        PG["PostgreSQL 15+/16 HA MetaStore\n(Zero-CGO pgx/v5 · Triage / Audit / Policy)"]
        S3["Local FS or Pure-Go S3/MinIO\n(SigV4 Symbols & Remote Logs)"]
    end

    subgraph Consumers["Operators & CI/CD"]
        UI["Console 2.4\n(React 19 · ECharts · P95/P99 · W3C Trace · ZH/EN)"]
        GATE["caliper-gate CLI &\nGitHub PR Gate Action"]
        PROM["Prometheus /metrics &\nOTLP Metrics & Traces Export"]
    end

    AND -->|Gzip JSON| COL
    IOS -->|JSON / Gzip| COL
    COL --> EMB
    COL --> CH
    COL --> PG
    COL --> S3
    EMB --> ANA
    CH --> ANA
    PG --> ANA
    S3 --> SYM
    SYM --> ANA
    ANA --> ALT
    ANA --> UI
    ANA --> GATE
    ANA --> PROM
```

---

## Core Data Pipeline Guarantees

```mermaid
sequenceDiagram
    participant SDK as Mobile SDK (Android / iOS)
    participant API as Caliper Collector
    participant Store as Embedded WAL / ClickHouse
    participant Sym as Symbolicator (R8/dSYM/SM)

    SDK->>API: GET /v1/policy (fail-closed sampling lease)
    API-->>SDK: rate_ppm, lease_ttl_ms, max_events_24h, enabled_collectors
    Note over SDK: Bounded FIFO disk queue + PII scrubbing + window aggregation
    SDK->>API: POST /v1/ingest (Gzip, idempotent event_id)
    API->>API: Strict contract validation (reject bad schema/PII)
    API->>Store: Append WAL + columnar histogram rollup
    API-->>SDK: 202 Accepted {accepted, duplicates, rejected}
    Note over Sym: On issue detail / flamegraph / stall stack read
    API->>Sym: Resolve frames bound to (app_version, build_number, symbol_id)
    Sym-->>API: Source file + line number + original method symbol
```

---

## Honest Architectural Comparison

| Capability | Caliper APM (Apache-2.0) | Typical Commercial Mobile APM | General-Purpose OSS Observability |
|---|---|---|---|
| **Native-First (Zero RN deps)** + Hybrid RN Correlation | Native Kotlin/Swift core + 550× RN bridge compression | Often native-only or heavy multi-MB SDKs | Limited mobile frame/ANR/PSS depth |
| **Statistical Rigor (Zero-Lie)** | Pure-Go DDSketch (±2% P50/P90/P95/P99) + 19-bucket fallback, Z-test + Wilson CI + `DDSketchKSTest`, explicit sample gates, zero-frame isolation | Opaque closed-source aggregation formulas | Manual PromQL/SQL required per team |
| **Self-Hosting Footprint** | 1 pure-Go binary (embedded WAL) or stateless ClickHouse + PostgreSQL + S3 | Closed SaaS / heavy enterprise license | Multi-service cluster required upfront |
| **CI/CD Statistical Release Gate** | Built-in `caliper-gate` CLI + GitHub PR Action (`exit 0/2/3`) | Usually manual dashboard checks | Custom scripting required |
| **Build-Bound Auto-Symbolication** | Built-in pure-Go R8, SourceMap v3, Mach-O `.dSYM`/ELF `.so` DWARF + Swift 5+ & Itanium C++ demanglers, Breakpad | Built-in | Requires external symbolication service |
| **Data Ownership & Privacy** | Non-PII hex hashes only, fail-closed audited UID trace & remote log pull | Vendor-hosted | Self-hosted |
| **Verified Physical Overhead** | Android `+6.0 ms` / `+28 KB` APK; iOS `+3.98 ms` / `+0.34 MiB` mem | Rarely published with reproducible harnesses | N/A |
