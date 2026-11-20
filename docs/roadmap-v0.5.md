# Caliper APM — v0.5 Production Evolution Roadmap

> Aligned strictly with frozen [`NORTH_STAR.md`](../NORTH_STAR.md) (v1.0). Every v0.5 capability ships with real end-to-end tests, zero mock placeholders, and zero Clean-Room violations.

---

## v0.5 Scope (Per North Star Charter)

1. **Production ClickHouse Storage Backend (`server/internal/storage/clickhouse`)**
   - Zero-CGO native ClickHouse HTTP wire implementation (`JSONEachRow` gzipped batch ingestion + `sumForEach` mergeable histogram SQL aggregation) implementing `storage.AnalyticsStore`.
   - Partitioned `MergeTree` DDL (`schema.sql`), automatic migration, strict SQL identifier/literal escaping, `--storage-driver=embedded|clickhouse` server flag, and optional `--profile cluster` ClickHouse service in `docker-compose.yml`.
2. **Statistical Regression Detector (`server/internal/analytics/regression.go`)**
   - Two-proportion Z-test + Wilson score 95% confidence intervals + two-sided `p_value` for rate metrics (Jank Rate, API Success Rate).
   - Two-sample Kolmogorov-Smirnov (KS) distribution test over 19-bucket mergeable histogram CDFs + exact asymptotic `p_value` for latency & memory quantiles.
   - Automated release regression scanner (`POST /v1/analytics/regressions/scan`) ranking per-scene & global regressions by statistical significance and impact.
3. **RBAC & Audited UID / Session Lookup (`server/internal/auth`, `storage`, `collector`)**
   - Role-based API key / Bearer token auth (`ingest_only`, `viewer`, `operator`, `admin`) with optional `app_ids` workspace scoping, backward-compatible with v0.1 single `--api-key`.
   - Optional client-hashed `user_hash` alongside `install_hash` (strictly non-PII hex hashes).
   - Audited `GET /v1/sessions/lookup?install_hash=...&reason=...` (requires `operator`/`admin` + mandatory non-empty `reason` ticket/justification) writing immutable append-only `AuditLogEntry` records queryable via `GET /v1/audit/logs` (`admin` only).
4. **Build CI Plugins (`packages/gradle-plugin`, `packages/fastlane-plugin`, `packages/github-action`)**
   - Zero-manual-ops release registration & symbol upload for Android (Gradle Kotlin plugin), iOS (Fastlane Ruby action), and CI pipelines (reusable GitHub Action).
5. **Web Console v0.5 Upgrades (`console/`)**
   - Statistical `p_value` / confidence & regression scanner evidence in Overview.
   - Audited Session/UID Lookup & immutable Audit Log views in Operations (backed 100% by real APIs).

---

## Milestone Checklist

- [x] **M1 · ClickHouse Storage Backend**: `schema.sql` (7 `ReplacingMergeTree` tables with `sumForEach` 19-bucket arrays), zero-CGO `ClickHouseStore`, HTTP protocol + SQL-escaping `-race` tests, `--storage-driver=clickhouse` CLI flags, cluster compose profile.
- [x] **M2 · Statistical Regression Engine**: Two-proportion Z-test + Wilson 95% CIs, two-sample Histogram KS test + Stephens finite-sample adjustment, `POST /v1/analytics/regressions/scan` per-scene impact ranker, unit & `-race` tests.
- [x] **M3 · RBAC & Audited UID Lookup**: `internal/auth` (`ingest_only`/`viewer`/`operator`/`admin` + app scoping), optional non-PII `UserHash`, `QuerySessionTimeline`, fail-closed immutable `AuditLogEntry` (`audit.wal.jsonl`), `GET /v1/sessions/lookup`, `GET /v1/audit/logs`, `GET /v1/auth/whoami`, full HTTP RBAC `-race` tests.
- [x] **M4 · Build CI Plugins**: `packages/gradle-plugin` (pure Kotlin `io.caliper.apm.symbols`, offline Gradle `ProjectBuilder` + HTTP upload test passed), `packages/fastlane-plugin` (Ruby `caliper_upload_symbols`, Minitest loopback gzip+SHA-256 test passed), `packages/github-action` (`action.yml`).
- [x] **M5 · Console v0.5 & Full Verification**: Console real-data statistical p-value/KS/Z-test evidence + ranked per-scene regression scanner (`OverviewPage`) + Audited Session Lookup & Immutable Audit Trail (`OperationsPage`), full Go/TS/Swift/Kotlin/Ruby/Chaos E2E + Clean-Room audit.
