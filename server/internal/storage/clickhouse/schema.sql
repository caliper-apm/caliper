-- Caliper APM v0.5 Production ClickHouse Schema
-- Uses ReplacingMergeTree for idempotent event_id deduplication and Array(UInt64)
-- 19-bucket mergeable histograms aggregated natively with sumForEach().

CREATE TABLE IF NOT EXISTS caliper_startup_events (
    event_id            String,
    ingested_at_ms      UInt64,
    occurred_at_ms      UInt64,
    day_utc             Date,
    app_id              LowCardinality(String),
    platform            LowCardinality(String),
    env                 LowCardinality(String),
    app_version         LowCardinality(String),
    build_number        LowCardinality(String),
    adapter             LowCardinality(String),
    device_model        LowCardinality(String) DEFAULT '',
    os_version          LowCardinality(String) DEFAULT '',
    policy_id           LowCardinality(String),
    contract_version    UInt16,
    result              LowCardinality(String),
    staged              UInt8,
    total_buckets          Array(UInt64),
    native_buckets         Array(UInt64),
    runtime_buckets        Array(UInt64),
    first_frame_buckets    Array(UInt64),
    total_sketch_bin       Int32 DEFAULT -2147483648,
    native_sketch_bin      Int32 DEFAULT -2147483648,
    runtime_sketch_bin     Int32 DEFAULT -2147483648,
    first_frame_sketch_bin Int32 DEFAULT -2147483648
) ENGINE = ReplacingMergeTree(ingested_at_ms)
PARTITION BY toYYYYMM(day_utc)
ORDER BY (app_id, platform, env, app_version, day_utc, event_id)
TTL day_utc + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS caliper_fluency_events (
    event_id               String,
    ingested_at_ms         UInt64,
    occurred_at_ms         UInt64,
    day_utc                Date,
    app_id                 LowCardinality(String),
    platform               LowCardinality(String),
    env                    LowCardinality(String),
    app_version            LowCardinality(String),
    build_number           LowCardinality(String),
    adapter                LowCardinality(String),
    device_model           LowCardinality(String) DEFAULT '',
    os_version             LowCardinality(String) DEFAULT '',
    scene                  LowCardinality(String),
    algorithm              LowCardinality(String),
    policy_id              LowCardinality(String),
    frame_count            UInt64,
    jank_frame_count       UInt64,
    severe_jank_frame_count UInt64,
    is_valid_window        UInt8,
    is_zero_frame_window   UInt8,
    has_stall_stack        UInt8,
    frame_buckets          Array(UInt64),
    stall_stack            Array(String)
) ENGINE = ReplacingMergeTree(ingested_at_ms)
PARTITION BY toYYYYMM(day_utc)
ORDER BY (app_id, platform, env, app_version, scene, algorithm, day_utc, event_id)
TTL day_utc + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS caliper_resource_events (
    event_id           String,
    ingested_at_ms     UInt64,
    occurred_at_ms     UInt64,
    day_utc            Date,
    app_id             LowCardinality(String),
    platform           LowCardinality(String),
    env                LowCardinality(String),
    app_version        LowCardinality(String),
    build_number       LowCardinality(String),
    adapter            LowCardinality(String),
    device_model       LowCardinality(String) DEFAULT '',
    os_version         LowCardinality(String) DEFAULT '',
    scene              LowCardinality(String),
    policy_id          LowCardinality(String),
    event_kind         LowCardinality(String),
    memory_kind        LowCardinality(String),
    cpu_pct            Float64,
    memory_peak_buckets Array(UInt64),
    memory_sketch_bin   Int32 DEFAULT -2147483648,
    js_longtask_count   UInt64,
    js_blocking_ms     Float64,
    js_max_task_ms     Float64
) ENGINE = ReplacingMergeTree(ingested_at_ms)
PARTITION BY toYYYYMM(day_utc)
ORDER BY (app_id, platform, env, app_version, scene, day_utc, event_id)
TTL day_utc + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS caliper_network_summary_events (
    event_id         String,
    ingested_at_ms   UInt64,
    occurred_at_ms   UInt64,
    day_utc          Date,
    app_id           LowCardinality(String),
    platform         LowCardinality(String),
    env              LowCardinality(String),
    app_version      LowCardinality(String),
    build_number     LowCardinality(String),
    adapter          LowCardinality(String),
    device_model     LowCardinality(String) DEFAULT '',
    os_version       LowCardinality(String) DEFAULT '',
    scene            LowCardinality(String),
    policy_id        LowCardinality(String),
    call_count       UInt64,
    fail_http        UInt64,
    fail_business    UInt64,
    fail_parse       UInt64,
    fail_network     UInt64,
    fail_timeout     UInt64,
    duration_buckets Array(UInt64),
    duration_sum_ms  Float64
) ENGINE = ReplacingMergeTree(ingested_at_ms)
PARTITION BY toYYYYMM(day_utc)
ORDER BY (app_id, platform, env, app_version, day_utc, event_id)
TTL day_utc + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS caliper_network_transport_events (
    event_id          String,
    ingested_at_ms    UInt64,
    occurred_at_ms    UInt64,
    day_utc           Date,
    app_id            LowCardinality(String),
    platform          LowCardinality(String),
    env               LowCardinality(String),
    app_version       LowCardinality(String),
    build_number      LowCardinality(String),
    adapter           LowCardinality(String),
    device_model      LowCardinality(String) DEFAULT '',
    os_version        LowCardinality(String) DEFAULT '',
    scene             LowCardinality(String),
    policy_id         LowCardinality(String),
    operation         LowCardinality(String),
    method            LowCardinality(String),
    status_code       UInt16 DEFAULT 0,
    result            LowCardinality(String),
    connection_reused UInt8,
    protocol          LowCardinality(String) DEFAULT '',
    network_type      LowCardinality(String) DEFAULT '',
    install_hash      String DEFAULT '',
    user_hash         String DEFAULT '',
    trace_id          String DEFAULT '',
    span_id           String DEFAULT '',
    total_ms          Float64 DEFAULT 0,
    dns_buckets       Array(UInt64),
    tcp_buckets       Array(UInt64),
    tls_buckets       Array(UInt64),
    ttfb_buckets      Array(UInt64),
    body_buckets      Array(UInt64),
    total_buckets     Array(UInt64),
    total_sketch_bin  Int32 DEFAULT -2147483648
) ENGINE = ReplacingMergeTree(ingested_at_ms)
PARTITION BY toYYYYMM(day_utc)
ORDER BY (app_id, platform, env, app_version, operation, day_utc, event_id)
TTL day_utc + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS caliper_custom_scene_events (
    event_id         String,
    ingested_at_ms   UInt64,
    occurred_at_ms   UInt64,
    day_utc          Date,
    app_id           LowCardinality(String),
    platform         LowCardinality(String),
    env              LowCardinality(String),
    app_version      LowCardinality(String),
    build_number     LowCardinality(String),
    adapter          LowCardinality(String),
    device_model     LowCardinality(String) DEFAULT '',
    os_version       LowCardinality(String) DEFAULT '',
    policy_id        LowCardinality(String),
    journey_name     LowCardinality(String),
    status           LowCardinality(String),
    error_code       LowCardinality(String) DEFAULT '',
    duration_ms      Float64 DEFAULT 0,
    duration_buckets    Array(UInt64),
    duration_sketch_bin Int32 DEFAULT -2147483648,
    stages_json         String DEFAULT '[]'
) ENGINE = ReplacingMergeTree(ingested_at_ms)
PARTITION BY toYYYYMM(day_utc)
ORDER BY (app_id, platform, env, app_version, journey_name, day_utc, event_id)
TTL day_utc + INTERVAL 90 DAY;

CREATE TABLE IF NOT EXISTS caliper_stability_events (
    event_id         String,
    ingested_at_ms   UInt64,
    occurred_at_ms   UInt64,
    day_utc          Date,
    app_id           LowCardinality(String),
    platform         LowCardinality(String),
    env              LowCardinality(String),
    app_version      LowCardinality(String),
    build_number     LowCardinality(String),
    adapter          LowCardinality(String),
    device_model     LowCardinality(String) DEFAULT '',
    os_version       LowCardinality(String) DEFAULT '',
    install_hash     String,
    user_hash        String,
    fingerprint      String,
    kind             LowCardinality(String),
    title            String,
    fatal            UInt8,
    exception_type   String,
    exit_reason      String,
    frames_json      String,
    breadcrumbs_json String
) ENGINE = ReplacingMergeTree(ingested_at_ms)
PARTITION BY toYYYYMM(day_utc)
ORDER BY (app_id, platform, env, fingerprint, day_utc, event_id)
TTL day_utc + INTERVAL 180 DAY;

CREATE TABLE IF NOT EXISTS caliper_session_timeline (
    event_id       String,
    ingested_at_ms UInt64,
    occurred_at_ms UInt64,
    day_utc        Date,
    app_id         LowCardinality(String),
    platform       LowCardinality(String),
    env            LowCardinality(String),
    app_version    LowCardinality(String),
    build_number   LowCardinality(String),
    adapter        LowCardinality(String),
    install_hash   String,
    user_hash      String,
    kind           LowCardinality(String),
    scene          LowCardinality(String),
    summary        String
) ENGINE = ReplacingMergeTree(ingested_at_ms)
PARTITION BY toYYYYMM(day_utc)
ORDER BY (app_id, install_hash, user_hash, occurred_at_ms, event_id)
TTL day_utc + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS caliper_cpu_profiles (
    profile_id         String,
    ingested_at_ms     UInt64,
    occurred_at_ms     UInt64,
    day_utc            Date,
    app_id             LowCardinality(String),
    platform           LowCardinality(String),
    env                LowCardinality(String),
    app_version        LowCardinality(String),
    build_number       LowCardinality(String),
    device_model       LowCardinality(String) DEFAULT '',
    os_version         LowCardinality(String) DEFAULT '',
    scene              LowCardinality(String),
    runtime            LowCardinality(String),
    thread_name        String,
    sample_interval_ms UInt32,
    duration_ms        UInt64,
    total_samples      UInt64,
    stacks_json        String
) ENGINE = ReplacingMergeTree(ingested_at_ms)
PARTITION BY toYYYYMM(day_utc)
ORDER BY (app_id, platform, env, app_version, occurred_at_ms, profile_id)
TTL day_utc + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS caliper_heap_snapshots (
    snapshot_id         String,
    ingested_at_ms      UInt64,
    occurred_at_ms      UInt64,
    day_utc             Date,
    app_id              LowCardinality(String),
    platform            LowCardinality(String),
    env                 LowCardinality(String),
    app_version         LowCardinality(String),
    build_number        LowCardinality(String),
    device_model        LowCardinality(String) DEFAULT '',
    os_version          LowCardinality(String) DEFAULT '',
    scene               LowCardinality(String),
    trigger_reason      LowCardinality(String),
    memory_kind         LowCardinality(String),
    total_allocated_kib Float64,
    total_retained_kib  Float64,
    memory_limit_kib    Float64,
    dominators_json     String
) ENGINE = ReplacingMergeTree(ingested_at_ms)
PARTITION BY toYYYYMM(day_utc)
ORDER BY (app_id, platform, env, app_version, occurred_at_ms, snapshot_id)
TTL day_utc + INTERVAL 30 DAY;
