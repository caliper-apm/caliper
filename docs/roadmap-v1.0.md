# Caliper APM — v1.0 Deep Diagnostics & Open Standards Roadmap

> Aligned strictly with frozen [`NORTH_STAR.md`](../NORTH_STAR.md) (v1.0). Every v1.0 diagnostic feature preserves Zero-PII privacy, bounded payload caps, fail-closed auditing, and 100% real-data Console views.

---

## v1.0 Scope (Per North Star Charter)

1. **Online Native & Hermes CPU Flamegraphs (`server/internal/profiler/flamegraph.go`)**
   - Bounded folded-stack CPU profile ingestion (`kind: "cpu_profile"`, max 256 stacks × 64 frames) supporting both Native (Android ART / iOS mach) and Hermes JS sampling profilers.
   - Automatic build-bound frame symbolication (R8 / Hermes SourceMap v3 / iOS dSYM) before flamegraph tree construction (`self_samples`, `total_samples`, `self_pct`, `total_pct`).
   - REST endpoints: `GET /v1/diagnostics/profiles`, `GET /v1/diagnostics/profiles/{profile_id}/flamegraph`.
2. **Trimmed Heap / OOM Deep Diagnostics (`server/internal/profiler/heap.go`)**
   - Zero-PII trimmed dominator summary (`kind: "heap_snapshot"`, max 128 dominator classes — never raw heap object values/strings).
   - Automatic R8 class-name de-obfuscation, leak-suspect classification, and cross-version retained-size regression diff (`GET /v1/diagnostics/heap`, `POST /v1/diagnostics/heap/compare`).
3. **Audited Remote Log Retrieval Tasks (`server/internal/remotelog`)**
   - Operator/Admin creates a bounded, TTL-expiring pull task (`POST /v1/diagnostics/log-tasks`, mandatory `reason` ticket ID, fail-closed `AuditLogEntry`, max 2 MiB).
   - SDK polls pending tasks (`GET /v1/diagnostics/log-tasks/pending`), uploads gzipped PII-scrubbed ring logs (`POST /v1/diagnostics/log-tasks/{task_id}/upload`), and operators inspect the bounded payload (`GET /v1/diagnostics/log-tasks/{task_id}/content`).
4. **Full OTLP Interoperability (`server/internal/otlp`)**
   - Native OTLP/HTTP JSON receiver (`POST /v1/otlp/v1/traces`, `POST /v1/otlp/v1/logs`) translating OpenTelemetry `resourceSpans` and `resourceLogs` into Caliper's Zero-Lie `BatchEnvelope` pipeline.
   - OTLP JSON export endpoint (`GET /v1/otlp/export`) exporting aggregated release metrics in standard OTLP `resourceMetrics` format for Prometheus/Grafana/Datadog federation.
5. **Web Console v1.0 Deep Diagnostics View (`console/src/pages/DiagnosticsPage.tsx`)**
   - Zoomable interactive SVG CPU Flamegraph, Trimmed Heap Dominator Explorer, and Audited Remote Log Task console — backed 100% by real APIs and real seeded/Chaos data.

---

## Milestone Checklist

- [x] **M1 · Protocol & Storage Extensions**: `cpu_profile` & `heap_snapshot` strictly bounded event kinds (`protocol/types.go`, `envelope.schema.json`), `EmbeddedStore` + `ClickHouseStore` (`caliper_cpu_profiles` & `caliper_heap_snapshots` DDL) + `RemoteLogTask` persistence.
- [x] **M2 · CPU Flamegraph + Trimmed Heap Engine**: `server/internal/profiler` merged hierarchical flamegraph tree + build-bound auto-symbolication (R8/SourceMap/dSYM) + cross-version heap dominator diff & leak suspect detection (`profiler_test.go`).
- [x] **M3 · Audited Remote Log Tasks**: `POST/GET /v1/diagnostics/log-tasks*` with RBAC, fail-closed immutable audit logging, gzipped upload cap, SHA-256 receipt, TTL expiry, and `-race` tests.
- [x] **M4 · Full OTLP Interoperability**: `server/internal/otlp` OTLP/HTTP JSON receiver (`POST /v1/otlp/v1/traces`, `POST /v1/otlp/v1/logs`) + standard `resourceMetrics` exporter (`GET /v1/otlp/export`) + `-race` tests.
- [x] **M5 · Console Deep Diagnostics + Seed/Chaos Integration + Full Verification & `v1.0.0` Tag**: `DiagnosticsPage.tsx` (zoomable SVG/HTML Flamegraph, Trimmed Heap Dominator Diff, Remote Log Task Manager, live OTLP Export preview) + `seed-replay` real diagnostic profiles & snapshots.
