# Caliper APM Server (`server/`)

Single-binary Go 1.23+ backend organized into five decoupled packages:
- `collector/` — High-concurrency `POST /v1/ingest` (Gzip, schema validation, rate limiting) & `GET /v1/policy`
- `storage/` — `AnalyticsStore` (v0.1 embedded DuckDB; v0.5 ClickHouse) + `MetaStore` (SQLite WAL)
- `analytics/` — Zero-Lie statistical engine (mergeable histogram quantiles, sample & policy gates)
- `symbolicator/` — Build-bound symbol resolution (R8 Retrace, dSYM, Hermes SourceMap)
- `alerts/` — Scheduled rule evaluation & deduplicated Webhook delivery (Slack, Feishu, Discord, HTTP)
