-- Canonical v0.1 Analytical (DuckDB / ClickHouse) & Metadata (SQLite / Postgres) Schema.
-- EmbeddedStore implements the exact same data model in-process with zero CGO dependencies,
-- enabling seamless plug-in of DuckDB/SQLite or ClickHouse backends behind AnalyticsStore/MetaStore.

CREATE TABLE IF NOT EXISTS releases (
    app_id       TEXT NOT NULL,
    platform     TEXT NOT NULL,
    env          TEXT NOT NULL,
    app_version  TEXT NOT NULL,
    build_number TEXT NOT NULL,
    git_sha      TEXT,
    symbol_id    TEXT,
    adapter      TEXT,
    registered   BOOLEAN NOT NULL DEFAULT FALSE,
    first_seen_ms BIGINT NOT NULL,
    last_seen_ms  BIGINT NOT NULL,
    event_count   BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (app_id, platform, env, app_version, build_number)
);

CREATE TABLE IF NOT EXISTS symbols (
    symbol_id    TEXT PRIMARY KEY,
    app_id       TEXT NOT NULL,
    platform     TEXT NOT NULL,
    app_version  TEXT NOT NULL,
    build_number TEXT NOT NULL,
    kind         TEXT NOT NULL, -- dsym | r8_mapping | hermes_sourcemap | breakpad
    sha256       TEXT NOT NULL,
    storage_path TEXT NOT NULL,
    uploaded_at_ms BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS sampling_policies (
    policy_id       TEXT PRIMARY KEY,
    app_id          TEXT NOT NULL,
    platform        TEXT NOT NULL,
    env             TEXT NOT NULL,
    rate_ppm        INTEGER NOT NULL,
    lease_ttl_ms    BIGINT NOT NULL,
    max_events_24h  INTEGER NOT NULL,
    enabled_json    TEXT NOT NULL,
    updated_at_ms   BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS alert_rules (
    rule_id        TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    app_id         TEXT NOT NULL,
    platform       TEXT NOT NULL,
    metric_id      TEXT NOT NULL,
    operator       TEXT NOT NULL, -- gt | lt
    threshold      DOUBLE PRECISION NOT NULL,
    min_samples    BIGINT NOT NULL DEFAULT 100,
    webhook_url    TEXT NOT NULL,
    cooldown_sec   INTEGER NOT NULL DEFAULT 3600,
    enabled        BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at_ms  BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS raw_events (
    event_id         TEXT PRIMARY KEY,
    app_id           TEXT NOT NULL,
    platform         TEXT NOT NULL,
    env              TEXT NOT NULL,
    app_version      TEXT NOT NULL,
    build_number     TEXT NOT NULL,
    adapter          TEXT NOT NULL,
    install_hash     TEXT NOT NULL,
    kind             TEXT NOT NULL,
    scene            TEXT NOT NULL,
    policy_id        TEXT NOT NULL,
    sample_rate_ppm  INTEGER NOT NULL,
    contract_version INTEGER NOT NULL,
    occurred_at_ms   BIGINT NOT NULL,
    day_utc          TEXT NOT NULL,
    payload_json     TEXT NOT NULL
);
