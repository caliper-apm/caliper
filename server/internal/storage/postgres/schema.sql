-- Caliper APM Cluster Production MetaStore Schema (PostgreSQL 15+)
-- Enables stateless multi-replica caliper collector pods sharing releases, symbols,
-- alert rules, remote log tasks, and immutable append-only security audit trails.

CREATE TABLE IF NOT EXISTS caliper_releases (
    app_id        TEXT NOT NULL,
    platform      TEXT NOT NULL CHECK (platform IN ('android', 'ios')),
    env           TEXT NOT NULL CHECK (env IN ('prod', 'staging', 'dev')),
    app_version   TEXT NOT NULL,
    build_number  TEXT NOT NULL,
    git_sha       TEXT NOT NULL DEFAULT '',
    symbol_id     TEXT NOT NULL DEFAULT '',
    adapter       TEXT NOT NULL DEFAULT 'native',
    registered    BOOLEAN NOT NULL DEFAULT FALSE,
    first_seen_ms BIGINT NOT NULL,
    last_seen_ms  BIGINT NOT NULL,
    event_count   BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (app_id, platform, env, app_version, build_number)
);

CREATE TABLE IF NOT EXISTS caliper_symbols (
    symbol_id      TEXT PRIMARY KEY,
    app_id         TEXT NOT NULL,
    platform       TEXT NOT NULL,
    env            TEXT NOT NULL DEFAULT 'prod',
    app_version    TEXT NOT NULL,
    build_number   TEXT NOT NULL,
    git_sha        TEXT NOT NULL DEFAULT '',
    kind           TEXT NOT NULL CHECK (kind IN ('r8_mapping', 'dsym', 'hermes_sourcemap', 'breakpad')),
    sha256         TEXT NOT NULL,
    size_bytes     BIGINT NOT NULL,
    object_key     TEXT NOT NULL, -- S3 / MinIO object key via objectstore.ObjectStore
    uploaded_at_ms BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_caliper_symbols_build ON caliper_symbols (app_id, platform, app_version, build_number);

CREATE TABLE IF NOT EXISTS caliper_sampling_policies (
    app_id             TEXT NOT NULL,
    platform           TEXT NOT NULL,
    env                TEXT NOT NULL,
    policy_id          TEXT NOT NULL,
    rate_ppm           INTEGER NOT NULL CHECK (rate_ppm >= 0 AND rate_ppm <= 1000000),
    lease_ttl_ms       BIGINT NOT NULL,
    max_events_24h     INTEGER NOT NULL,
    enabled_collectors JSONB NOT NULL,
    updated_at_ms      BIGINT NOT NULL,
    PRIMARY KEY (app_id, platform, env)
);

CREATE TABLE IF NOT EXISTS caliper_alert_rules (
    rule_id        TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    app_id         TEXT NOT NULL,
    platform       TEXT NOT NULL,
    env            TEXT NOT NULL DEFAULT 'prod',
    app_version    TEXT NOT NULL DEFAULT '',
    scene          TEXT NOT NULL DEFAULT '',
    metric_id      TEXT NOT NULL,
    operator       TEXT NOT NULL CHECK (operator IN ('gt', 'lt')),
    threshold      DOUBLE PRECISION NOT NULL,
    min_samples    BIGINT NOT NULL DEFAULT 10,
    webhook_url    TEXT NOT NULL DEFAULT '',
    webhook_format TEXT NOT NULL DEFAULT 'generic',
    cooldown_sec   BIGINT NOT NULL DEFAULT 300,
    enabled        BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at_ms  BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS caliper_alert_incidents (
    incident_id          TEXT PRIMARY KEY,
    rule_id              TEXT NOT NULL REFERENCES caliper_alert_rules(rule_id) ON DELETE CASCADE,
    rule_name            TEXT NOT NULL,
    app_id               TEXT NOT NULL,
    platform             TEXT NOT NULL,
    metric_id            TEXT NOT NULL,
    status               TEXT NOT NULL CHECK (status IN ('firing', 'resolved')),
    observed_value       DOUBLE PRECISION,
    threshold            DOUBLE PRECISION NOT NULL,
    sample_size          BIGINT NOT NULL,
    triggered_at_ms      BIGINT NOT NULL,
    last_evaluated_ms    BIGINT NOT NULL,
    resolved_at_ms       BIGINT,
    last_notified_ms     BIGINT,
    delivery_status      TEXT NOT NULL DEFAULT 'none',
    delivery_http_status INTEGER NOT NULL DEFAULT 0,
    delivery_error       TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS caliper_remote_log_tasks (
    task_id         TEXT PRIMARY KEY,
    app_id          TEXT NOT NULL,
    platform        TEXT NOT NULL,
    target_type     TEXT NOT NULL CHECK (target_type IN ('install_hash', 'user_hash')),
    target_hash     TEXT NOT NULL,
    reason          TEXT NOT NULL,
    requested_by    TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('pending', 'completed', 'expired')),
    max_bytes       BIGINT NOT NULL,
    created_at_ms   BIGINT NOT NULL,
    expires_at_ms   BIGINT NOT NULL,
    completed_at_ms BIGINT,
    uploaded_bytes  BIGINT DEFAULT 0,
    sha256          TEXT NOT NULL DEFAULT '',
    object_key      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_caliper_rlog_pending ON caliper_remote_log_tasks (platform, status, target_hash);

-- Append-only security audit trail (never UPDATE or DELETE in application role).
CREATE TABLE IF NOT EXISTS caliper_audit_logs (
    audit_id     TEXT PRIMARY KEY,
    timestamp_ms BIGINT NOT NULL,
    actor        TEXT NOT NULL,
    role         TEXT NOT NULL,
    action       TEXT NOT NULL,
    target_type  TEXT NOT NULL,
    target_hash  TEXT NOT NULL,
    reason       TEXT NOT NULL,
    remote_ip    TEXT NOT NULL DEFAULT '',
    result_count INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_caliper_audit_ts ON caliper_audit_logs (timestamp_ms DESC);
