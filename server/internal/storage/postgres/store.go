package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage"
	"github.com/caliper-apm/caliper/server/internal/storage/objectstore"
)

//go:embed schema.sql
var schemaSQL string

// Config configures the pure-Go PostgreSQL MetaStore connection pool.
type Config struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
	ObjectStore     objectstore.ObjectStore
}

// Store implements storage.MetaStore over PostgreSQL + ObjectStore, enabling
// stateless multi-replica Caliper APM deployments with zero local disk state.
type Store struct {
	db     *sql.DB
	mu     sync.RWMutex
	blobs  objectstore.ObjectStore
	closed bool
}

// Compile-time check that *Store satisfies storage.MetaStore completely.
var _ storage.MetaStore = (*Store)(nil)

// Open connects to PostgreSQL using the pure-Go zero-CGO pgx/v5 stdlib driver.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	if strings.TrimSpace(cfg.DSN) == "" {
		return nil, errors.New("postgres DSN is required (e.g. postgres://caliper:pass@host:5432/caliper?sslmode=require)")
	}
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open postgres driver: %w", err)
	}
	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 25
	}
	maxIdle := cfg.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = 10
	}
	connLife := cfg.ConnMaxLifetime
	if connLife <= 0 {
		connLife = 30 * time.Minute
	}
	connIdle := cfg.ConnMaxIdleTime
	if connIdle <= 0 {
		connIdle = 5 * time.Minute
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(connLife)
	db.SetConnMaxIdleTime(connIdle)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	blobs := cfg.ObjectStore
	if blobs == nil {
		blobs = objectstore.NewMemory()
	}
	return &Store{db: db, blobs: blobs}, nil
}

// NewWithDB wraps an existing *sql.DB (useful for testing or custom pool wiring).
func NewWithDB(db *sql.DB, blobs objectstore.ObjectStore) *Store {
	if blobs == nil {
		blobs = objectstore.NewMemory()
	}
	return &Store{db: db, blobs: blobs}
}

// SetObjectStore replaces the blob backend used for symbol archives and remote diagnostic logs.
func (s *Store) SetObjectStore(blobs objectstore.ObjectStore) {
	if blobs == nil {
		return
	}
	s.mu.Lock()
	s.blobs = blobs
	s.mu.Unlock()
}

func (s *Store) objectStore() objectstore.ObjectStore {
	s.mu.RLock()
	b := s.blobs
	s.mu.RUnlock()
	return b
}

// Ping verifies live connectivity to PostgreSQL.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// Close closes the underlying PostgreSQL connection pool.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	return s.db.Close()
}

// Migrate applies the embedded idempotent PostgreSQL DDL schema statement-by-statement.
func (s *Store) Migrate(ctx context.Context) error {
	for _, stmt := range splitSQLStatements(schemaSQL) {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("postgres migrate (%s): %w", firstLine(stmt), err)
		}
	}
	return nil
}

// SchemaStatements returns the ordered idempotent DDL statements embedded in this package.
func SchemaStatements() []string {
	return splitSQLStatements(schemaSQL)
}

func splitSQLStatements(raw string) []string {
	var cleanLines []string
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		cleanLines = append(cleanLines, line)
	}
	joined := strings.Join(cleanLines, "\n")
	parts := strings.Split(joined, ";")
	var out []string
	for _, p := range parts {
		stmt := strings.TrimSpace(p)
		if stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	if len(s) > 72 {
		return s[:72] + "..."
	}
	return s
}

func randomHexSuffix(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ===== Releases =====

func (s *Store) RegisterRelease(ctx context.Context, r storage.ReleaseRecord) error {
	if r.Env == "" {
		r.Env = "prod"
	}
	if r.Adapter == "" {
		r.Adapter = "native"
	}
	now := time.Now().UTC().UnixMilli()
	if r.FirstSeenMS == 0 {
		r.FirstSeenMS = now
	}
	if r.LastSeenMS == 0 {
		r.LastSeenMS = now
	}
	const q = `
INSERT INTO caliper_releases (
    app_id, platform, env, app_version, build_number,
    git_sha, symbol_id, adapter, binary_size_bytes, binary_artifact_kind,
    registered, first_seen_ms, last_seen_ms, event_count
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT (app_id, platform, env, app_version, build_number) DO UPDATE SET
    git_sha              = CASE WHEN EXCLUDED.git_sha <> '' THEN EXCLUDED.git_sha ELSE caliper_releases.git_sha END,
    symbol_id            = CASE WHEN EXCLUDED.symbol_id <> '' THEN EXCLUDED.symbol_id ELSE caliper_releases.symbol_id END,
    adapter              = CASE WHEN EXCLUDED.adapter <> '' THEN EXCLUDED.adapter ELSE caliper_releases.adapter END,
    binary_size_bytes    = CASE WHEN EXCLUDED.binary_size_bytes > 0 THEN EXCLUDED.binary_size_bytes ELSE caliper_releases.binary_size_bytes END,
    binary_artifact_kind = CASE WHEN EXCLUDED.binary_artifact_kind <> '' THEN EXCLUDED.binary_artifact_kind ELSE caliper_releases.binary_artifact_kind END,
    registered           = caliper_releases.registered OR EXCLUDED.registered,
    first_seen_ms        = LEAST(caliper_releases.first_seen_ms, EXCLUDED.first_seen_ms),
    last_seen_ms         = GREATEST(caliper_releases.last_seen_ms, EXCLUDED.last_seen_ms),
    event_count          = caliper_releases.event_count + EXCLUDED.event_count`
	_, err := s.db.ExecContext(ctx, q,
		r.AppID, r.Platform, r.Env, r.AppVersion, r.BuildNumber,
		r.GitSHA, r.SymbolID, r.Adapter, r.BinarySizeBytes, r.BinaryArtifactKind,
		r.Registered, r.FirstSeenMS, r.LastSeenMS, r.EventCount,
	)
	return err
}

func (s *Store) ListReleases(ctx context.Context, appID, platform, env string) ([]storage.ReleaseRecord, error) {
	where, args := buildFilters([]filterArg{
		{"app_id", appID},
		{"platform", platform},
		{"env", env},
	})
	q := `SELECT app_id, platform, env, app_version, build_number, git_sha, symbol_id, adapter, binary_size_bytes, binary_artifact_kind, registered, first_seen_ms, last_seen_ms, event_count
FROM caliper_releases` + where + ` ORDER BY last_seen_ms DESC, app_version DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]storage.ReleaseRecord, 0)
	for rows.Next() {
		var r storage.ReleaseRecord
		if err := rows.Scan(
			&r.AppID, &r.Platform, &r.Env, &r.AppVersion, &r.BuildNumber,
			&r.GitSHA, &r.SymbolID, &r.Adapter, &r.BinarySizeBytes, &r.BinaryArtifactKind,
			&r.Registered, &r.FirstSeenMS, &r.LastSeenMS, &r.EventCount,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ===== Symbols =====

func (s *Store) SaveSymbol(ctx context.Context, rec storage.SymbolRecord, content []byte) (storage.SymbolRecord, error) {
	if rec.Env == "" {
		rec.Env = "prod"
	}
	if rec.SymbolID == "" {
		rec.SymbolID = fmt.Sprintf("sym-%s-%s-%s-%s", rec.Platform, rec.AppVersion, rec.BuildNumber, randomHexSuffix(4))
	}
	rec.SizeBytes = int64(len(content))
	if rec.UploadedAtMS == 0 {
		rec.UploadedAtMS = time.Now().UTC().UnixMilli()
	}
	objectKey := fmt.Sprintf("symbols/%s/%s/%s/%s/%s.bin", rec.AppID, rec.Platform, rec.AppVersion, rec.BuildNumber, rec.SymbolID)
	if err := s.objectStore().PutObject(ctx, objectKey, content, "application/octet-stream"); err != nil {
		return storage.SymbolRecord{}, fmt.Errorf("object store put symbol: %w", err)
	}
	rec.StoragePath = objectKey

	const q = `
INSERT INTO caliper_symbols (
    symbol_id, app_id, platform, env, app_version, build_number,
    git_sha, kind, sha256, size_bytes, object_key, uploaded_at_ms
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
ON CONFLICT (symbol_id) DO UPDATE SET
    git_sha        = EXCLUDED.git_sha,
    kind           = EXCLUDED.kind,
    sha256         = EXCLUDED.sha256,
    size_bytes     = EXCLUDED.size_bytes,
    object_key     = EXCLUDED.object_key,
    uploaded_at_ms = EXCLUDED.uploaded_at_ms`
	if _, err := s.db.ExecContext(ctx, q,
		rec.SymbolID, rec.AppID, rec.Platform, rec.Env, rec.AppVersion, rec.BuildNumber,
		rec.GitSHA, rec.Kind, rec.SHA256, rec.SizeBytes, objectKey, rec.UploadedAtMS,
	); err != nil {
		return storage.SymbolRecord{}, err
	}
	return rec, nil
}

func (s *Store) ListSymbols(ctx context.Context, appID, platform, appVersion, buildNumber string) ([]storage.SymbolRecord, error) {
	where, args := buildFilters([]filterArg{
		{"app_id", appID},
		{"platform", platform},
		{"app_version", appVersion},
		{"build_number", buildNumber},
	})
	q := `SELECT symbol_id, app_id, platform, env, app_version, build_number, git_sha, kind, sha256, size_bytes, object_key, uploaded_at_ms
FROM caliper_symbols` + where + ` ORDER BY uploaded_at_ms DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]storage.SymbolRecord, 0)
	for rows.Next() {
		var r storage.SymbolRecord
		if err := rows.Scan(
			&r.SymbolID, &r.AppID, &r.Platform, &r.Env, &r.AppVersion, &r.BuildNumber,
			&r.GitSHA, &r.Kind, &r.SHA256, &r.SizeBytes, &r.StoragePath, &r.UploadedAtMS,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetSymbolContent(ctx context.Context, symbolID string) (storage.SymbolRecord, []byte, bool, error) {
	const q = `SELECT symbol_id, app_id, platform, env, app_version, build_number, git_sha, kind, sha256, size_bytes, object_key, uploaded_at_ms
FROM caliper_symbols WHERE symbol_id = $1`
	var r storage.SymbolRecord
	err := s.db.QueryRowContext(ctx, q, symbolID).Scan(
		&r.SymbolID, &r.AppID, &r.Platform, &r.Env, &r.AppVersion, &r.BuildNumber,
		&r.GitSHA, &r.Kind, &r.SHA256, &r.SizeBytes, &r.StoragePath, &r.UploadedAtMS,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.SymbolRecord{}, nil, false, nil
	}
	if err != nil {
		return storage.SymbolRecord{}, nil, false, err
	}
	data, ok, err := s.objectStore().GetObject(ctx, r.StoragePath)
	if err != nil || !ok {
		return r, nil, false, err
	}
	return r, data, true, nil
}

// ===== Sampling Policies =====

func (s *Store) GetPolicy(ctx context.Context, appID, platform, env string) (storage.SamplingPolicy, error) {
	if env == "" {
		env = "prod"
	}
	const q = `SELECT policy_id, app_id, platform, env, rate_ppm, lease_ttl_ms, max_events_24h, enabled_collectors, updated_at_ms
FROM caliper_sampling_policies WHERE app_id = $1 AND platform = $2 AND env = $3`
	var p storage.SamplingPolicy
	var ratePPM int64
	var collectorsJSON []byte
	err := s.db.QueryRowContext(ctx, q, appID, platform, env).Scan(
		&p.PolicyID, &p.AppID, &p.Platform, &p.Env, &ratePPM,
		&p.LeaseTTLMS, &p.MaxEvents24h, &collectorsJSON, &p.UpdatedAtMS,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.SamplingPolicy{
			PolicyID:          "default_v1",
			AppID:             appID,
			Platform:          platform,
			Env:               env,
			RatePPM:           30_000,
			LeaseTTLMS:        24 * 60 * 60 * 1000,
			MaxEvents24h:      50,
			EnabledCollectors: []string{"startup", "crash", "anr", "abnormal_exit", "fluency", "resources", "network"},
			UpdatedAtMS:       time.Now().UTC().UnixMilli(),
		}, nil
	}
	if err != nil {
		return storage.SamplingPolicy{}, err
	}
	p.RatePPM = uint32(ratePPM)
	_ = json.Unmarshal(collectorsJSON, &p.EnabledCollectors)
	return p, nil
}

func (s *Store) UpsertPolicy(ctx context.Context, p storage.SamplingPolicy) error {
	if p.Env == "" {
		p.Env = "prod"
	}
	p.UpdatedAtMS = time.Now().UTC().UnixMilli()
	collectorsJSON, err := json.Marshal(p.EnabledCollectors)
	if err != nil {
		return err
	}
	const q = `
INSERT INTO caliper_sampling_policies (
    app_id, platform, env, policy_id, rate_ppm, lease_ttl_ms, max_events_24h, enabled_collectors, updated_at_ms
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9)
ON CONFLICT (app_id, platform, env) DO UPDATE SET
    policy_id          = EXCLUDED.policy_id,
    rate_ppm           = EXCLUDED.rate_ppm,
    lease_ttl_ms       = EXCLUDED.lease_ttl_ms,
    max_events_24h     = EXCLUDED.max_events_24h,
    enabled_collectors = EXCLUDED.enabled_collectors,
    updated_at_ms      = EXCLUDED.updated_at_ms`
	_, err = s.db.ExecContext(ctx, q,
		p.AppID, p.Platform, p.Env, p.PolicyID, int(p.RatePPM), p.LeaseTTLMS, p.MaxEvents24h, string(collectorsJSON), p.UpdatedAtMS,
	)
	return err
}

// ===== Alert Rules & Incidents =====

func (s *Store) UpsertAlertRule(ctx context.Context, rule storage.AlertRule) (storage.AlertRule, error) {
	if rule.RuleID == "" {
		rule.RuleID = "rule-" + randomHexSuffix(6)
	}
	if rule.Env == "" {
		rule.Env = "prod"
	}
	if rule.WebhookFormat == "" {
		rule.WebhookFormat = "generic"
	}
	rule.UpdatedAtMS = time.Now().UTC().UnixMilli()
	const q = `
INSERT INTO caliper_alert_rules (
    rule_id, name, app_id, platform, env, app_version, scene,
    metric_id, operator, threshold, min_samples, webhook_url, webhook_format,
    cooldown_sec, enabled, updated_at_ms
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
ON CONFLICT (rule_id) DO UPDATE SET
    name           = EXCLUDED.name,
    app_id         = EXCLUDED.app_id,
    platform       = EXCLUDED.platform,
    env            = EXCLUDED.env,
    app_version    = EXCLUDED.app_version,
    scene          = EXCLUDED.scene,
    metric_id      = EXCLUDED.metric_id,
    operator       = EXCLUDED.operator,
    threshold      = EXCLUDED.threshold,
    min_samples    = EXCLUDED.min_samples,
    webhook_url    = EXCLUDED.webhook_url,
    webhook_format = EXCLUDED.webhook_format,
    cooldown_sec   = EXCLUDED.cooldown_sec,
    enabled        = EXCLUDED.enabled,
    updated_at_ms  = EXCLUDED.updated_at_ms`
	if _, err := s.db.ExecContext(ctx, q,
		rule.RuleID, rule.Name, rule.AppID, rule.Platform, rule.Env, rule.AppVersion, rule.Scene,
		rule.MetricID, rule.Operator, rule.Threshold, rule.MinSamples, rule.WebhookURL, rule.WebhookFormat,
		rule.CooldownSec, rule.Enabled, rule.UpdatedAtMS,
	); err != nil {
		return storage.AlertRule{}, err
	}
	return rule, nil
}

func (s *Store) DeleteAlertRule(ctx context.Context, ruleID string) error {
	if strings.TrimSpace(ruleID) == "" {
		return errors.New("rule_id is required")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM caliper_alert_rules WHERE rule_id = $1`, ruleID)
	return err
}

func (s *Store) ListAlertRules(ctx context.Context, appID, platform string) ([]storage.AlertRule, error) {
	where, args := buildFilters([]filterArg{
		{"app_id", appID},
		{"platform", platform},
	})
	q := `SELECT rule_id, name, app_id, platform, env, app_version, scene, metric_id, operator, threshold, min_samples, webhook_url, webhook_format, cooldown_sec, enabled, updated_at_ms
FROM caliper_alert_rules` + where + ` ORDER BY updated_at_ms DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]storage.AlertRule, 0)
	for rows.Next() {
		var r storage.AlertRule
		if err := rows.Scan(
			&r.RuleID, &r.Name, &r.AppID, &r.Platform, &r.Env, &r.AppVersion, &r.Scene,
			&r.MetricID, &r.Operator, &r.Threshold, &r.MinSamples, &r.WebhookURL, &r.WebhookFormat,
			&r.CooldownSec, &r.Enabled, &r.UpdatedAtMS,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) SaveAlertIncident(ctx context.Context, inc storage.AlertIncident) error {
	var obs sql.NullFloat64
	if inc.ObservedValue != nil {
		obs = sql.NullFloat64{Float64: *inc.ObservedValue, Valid: true}
	}
	var resolvedAt, lastNotified sql.NullInt64
	if inc.ResolvedAtMS > 0 {
		resolvedAt = sql.NullInt64{Int64: inc.ResolvedAtMS, Valid: true}
	}
	if inc.LastNotifiedMS > 0 {
		lastNotified = sql.NullInt64{Int64: inc.LastNotifiedMS, Valid: true}
	}
	if inc.DeliveryStatus == "" {
		inc.DeliveryStatus = "none"
	}
	const q = `
INSERT INTO caliper_alert_incidents (
    incident_id, rule_id, rule_name, app_id, platform, metric_id, status,
    observed_value, threshold, sample_size, triggered_at_ms, last_evaluated_ms,
    resolved_at_ms, last_notified_ms, delivery_status, delivery_http_status, delivery_error
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
ON CONFLICT (incident_id) DO UPDATE SET
    status               = EXCLUDED.status,
    observed_value       = EXCLUDED.observed_value,
    sample_size          = EXCLUDED.sample_size,
    last_evaluated_ms    = EXCLUDED.last_evaluated_ms,
    resolved_at_ms       = EXCLUDED.resolved_at_ms,
    last_notified_ms     = EXCLUDED.last_notified_ms,
    delivery_status      = EXCLUDED.delivery_status,
    delivery_http_status = EXCLUDED.delivery_http_status,
    delivery_error       = EXCLUDED.delivery_error`
	_, err := s.db.ExecContext(ctx, q,
		inc.IncidentID, inc.RuleID, inc.RuleName, inc.AppID, inc.Platform, inc.MetricID, inc.Status,
		obs, inc.Threshold, inc.SampleSize, inc.TriggeredAtMS, inc.LastEvaluatedMS,
		resolvedAt, lastNotified, inc.DeliveryStatus, inc.DeliveryHTTPStatus, inc.DeliveryError,
	)
	return err
}

func (s *Store) ListAlertIncidents(ctx context.Context, ruleID string) ([]storage.AlertIncident, error) {
	where, args := buildFilters([]filterArg{{"rule_id", ruleID}})
	q := `SELECT incident_id, rule_id, rule_name, app_id, platform, metric_id, status,
observed_value, threshold, sample_size, triggered_at_ms, last_evaluated_ms,
resolved_at_ms, last_notified_ms, delivery_status, delivery_http_status, delivery_error
FROM caliper_alert_incidents` + where + ` ORDER BY last_evaluated_ms DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]storage.AlertIncident, 0)
	for rows.Next() {
		var inc storage.AlertIncident
		var obs sql.NullFloat64
		var resolvedAt, lastNotified sql.NullInt64
		if err := rows.Scan(
			&inc.IncidentID, &inc.RuleID, &inc.RuleName, &inc.AppID, &inc.Platform, &inc.MetricID, &inc.Status,
			&obs, &inc.Threshold, &inc.SampleSize, &inc.TriggeredAtMS, &inc.LastEvaluatedMS,
			&resolvedAt, &lastNotified, &inc.DeliveryStatus, &inc.DeliveryHTTPStatus, &inc.DeliveryError,
		); err != nil {
			return nil, err
		}
		if obs.Valid {
			v := obs.Float64
			inc.ObservedValue = &v
		}
		if resolvedAt.Valid {
			inc.ResolvedAtMS = resolvedAt.Int64
		}
		if lastNotified.Valid {
			inc.LastNotifiedMS = lastNotified.Int64
		}
		out = append(out, inc)
	}
	return out, rows.Err()
}

// ===== Issue Lifecycle Triage =====

func (s *Store) UpsertIssueState(ctx context.Context, st storage.IssueState) (storage.IssueState, error) {
	if st.Fingerprint == "" {
		return storage.IssueState{}, errors.New("fingerprint is required")
	}
	switch st.Status {
	case "open", "in_progress", "resolved", "ignored":
	default:
		return storage.IssueState{}, fmt.Errorf("invalid issue status %q (expected open, in_progress, resolved, or ignored)", st.Status)
	}
	st.UpdatedAtMS = time.Now().UTC().UnixMilli()
	const q = `
INSERT INTO caliper_issue_states (fingerprint, status, assignee, note, resolved_in_version, updated_at_ms)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (fingerprint) DO UPDATE SET
    status              = EXCLUDED.status,
    assignee            = EXCLUDED.assignee,
    note                = EXCLUDED.note,
    resolved_in_version = EXCLUDED.resolved_in_version,
    updated_at_ms       = EXCLUDED.updated_at_ms`
	if _, err := s.db.ExecContext(ctx, q, st.Fingerprint, st.Status, st.Assignee, st.Note, st.ResolvedInVersion, st.UpdatedAtMS); err != nil {
		return storage.IssueState{}, err
	}
	return st, nil
}

func (s *Store) GetIssueState(ctx context.Context, fingerprint string) (storage.IssueState, bool, error) {
	const q = `SELECT fingerprint, status, assignee, note, resolved_in_version, updated_at_ms
FROM caliper_issue_states WHERE fingerprint = $1`
	var st storage.IssueState
	err := s.db.QueryRowContext(ctx, q, fingerprint).Scan(
		&st.Fingerprint, &st.Status, &st.Assignee, &st.Note, &st.ResolvedInVersion, &st.UpdatedAtMS,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.IssueState{}, false, nil
	}
	if err != nil {
		return storage.IssueState{}, false, err
	}
	return st, true, nil
}

func (s *Store) ListIssueStates(ctx context.Context) ([]storage.IssueState, error) {
	const q = `SELECT fingerprint, status, assignee, note, resolved_in_version, updated_at_ms
FROM caliper_issue_states ORDER BY updated_at_ms DESC`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]storage.IssueState, 0)
	for rows.Next() {
		var st storage.IssueState
		if err := rows.Scan(&st.Fingerprint, &st.Status, &st.Assignee, &st.Note, &st.ResolvedInVersion, &st.UpdatedAtMS); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// ===== Append-Only Audit Trail =====

func (s *Store) AppendAuditLog(ctx context.Context, entry storage.AuditLogEntry) error {
	if entry.AuditID == "" {
		entry.AuditID = fmt.Sprintf("aud-%d-%s", time.Now().UTC().UnixNano(), randomHexSuffix(3))
	}
	if entry.TimestampMS == 0 {
		entry.TimestampMS = time.Now().UTC().UnixMilli()
	}
	const q = `
INSERT INTO caliper_audit_logs (
    audit_id, timestamp_ms, actor, role, action, target_type, target_hash, reason, remote_ip, result_count
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`
	_, err := s.db.ExecContext(ctx, q,
		entry.AuditID, entry.TimestampMS, entry.Actor, entry.Role, entry.Action,
		entry.TargetType, entry.TargetHash, entry.Reason, entry.RemoteIP, entry.ResultCount,
	)
	return err
}

func (s *Store) ListAuditLogs(ctx context.Context, limit int) ([]storage.AuditLogEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	const q = `SELECT audit_id, timestamp_ms, actor, role, action, target_type, target_hash, reason, remote_ip, result_count
FROM caliper_audit_logs ORDER BY timestamp_ms DESC LIMIT $1`
	rows, err := s.db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]storage.AuditLogEntry, 0)
	for rows.Next() {
		var e storage.AuditLogEntry
		if err := rows.Scan(
			&e.AuditID, &e.TimestampMS, &e.Actor, &e.Role, &e.Action,
			&e.TargetType, &e.TargetHash, &e.Reason, &e.RemoteIP, &e.ResultCount,
		); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ===== Remote Diagnostic Log Tasks =====

func (s *Store) CreateRemoteLogTask(ctx context.Context, task storage.RemoteLogTask) (storage.RemoteLogTask, error) {
	if task.TargetHash == "" || task.Reason == "" || task.RequestedBy == "" {
		return storage.RemoteLogTask{}, errors.New("target_hash, reason, and requested_by are required")
	}
	if task.TargetType != "install_hash" && task.TargetType != "user_hash" {
		return storage.RemoteLogTask{}, errors.New("target_type must be install_hash or user_hash")
	}
	if task.Platform != "android" && task.Platform != "ios" {
		return storage.RemoteLogTask{}, errors.New("platform must be android or ios")
	}
	if task.MaxBytes <= 0 || task.MaxBytes > protocol.MaxPayloadBytes {
		task.MaxBytes = 512 * 1024
	}
	now := time.Now().UTC().UnixMilli()
	if task.CreatedAtMS == 0 {
		task.CreatedAtMS = now
	}
	if task.ExpiresAtMS <= task.CreatedAtMS {
		task.ExpiresAtMS = task.CreatedAtMS + 3600_000
	}
	task.Status = "pending"
	if task.TaskID == "" {
		task.TaskID = fmt.Sprintf("rlog-%d-%s", task.CreatedAtMS, randomHexSuffix(4))
	}
	const q = `
INSERT INTO caliper_remote_log_tasks (
    task_id, app_id, platform, target_type, target_hash, reason, requested_by,
    status, max_bytes, created_at_ms, expires_at_ms, uploaded_bytes, sha256, object_key
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,0,'','')`
	if _, err := s.db.ExecContext(ctx, q,
		task.TaskID, task.AppID, task.Platform, task.TargetType, task.TargetHash, task.Reason, task.RequestedBy,
		task.Status, task.MaxBytes, task.CreatedAtMS, task.ExpiresAtMS,
	); err != nil {
		return storage.RemoteLogTask{}, err
	}
	return task, nil
}

func (s *Store) ListRemoteLogTasks(ctx context.Context, appID, platform string) ([]storage.RemoteLogTask, error) {
	where, args := buildFilters([]filterArg{
		{"app_id", appID},
		{"platform", platform},
	})
	q := `SELECT task_id, app_id, platform, target_type, target_hash, reason, requested_by,
status, max_bytes, created_at_ms, expires_at_ms, completed_at_ms, uploaded_bytes, sha256, object_key
FROM caliper_remote_log_tasks` + where + ` ORDER BY created_at_ms DESC`
	return s.scanRemoteLogTasks(ctx, q, args...)
}

func (s *Store) PollPendingLogTasks(ctx context.Context, appID, platform, installHash, userHash string, nowMS int64) ([]storage.RemoteLogTask, error) {
	if nowMS <= 0 {
		nowMS = time.Now().UTC().UnixMilli()
	}
	var conds []string
	var args []any
	add := func(expr string, val any) {
		args = append(args, val)
		conds = append(conds, fmt.Sprintf(expr, len(args)))
	}
	if appID != "" {
		add("app_id = $%d", appID)
	}
	if platform != "" {
		add("platform = $%d", platform)
	}
	add("status = $%d", "pending")
	add("expires_at_ms > $%d", nowMS)

	var targetOr []string
	if installHash != "" {
		args = append(args, installHash)
		targetOr = append(targetOr, fmt.Sprintf("(target_type = 'install_hash' AND target_hash = $%d)", len(args)))
	}
	if userHash != "" {
		args = append(args, userHash)
		targetOr = append(targetOr, fmt.Sprintf("(target_type = 'user_hash' AND target_hash = $%d)", len(args)))
	}
	if len(targetOr) == 0 {
		return []storage.RemoteLogTask{}, nil
	}
	conds = append(conds, "("+strings.Join(targetOr, " OR ")+")")

	q := `SELECT task_id, app_id, platform, target_type, target_hash, reason, requested_by,
status, max_bytes, created_at_ms, expires_at_ms, completed_at_ms, uploaded_bytes, sha256, object_key
FROM caliper_remote_log_tasks WHERE ` + strings.Join(conds, " AND ") + ` ORDER BY created_at_ms ASC`
	return s.scanRemoteLogTasks(ctx, q, args...)
}

func (s *Store) CompleteRemoteLogTask(ctx context.Context, taskID string, content []byte, sha256Hex string, nowMS int64) (storage.RemoteLogTask, error) {
	if nowMS <= 0 {
		nowMS = time.Now().UTC().UnixMilli()
	}
	const sel = `SELECT task_id, app_id, platform, target_type, target_hash, reason, requested_by,
status, max_bytes, created_at_ms, expires_at_ms, completed_at_ms, uploaded_bytes, sha256, object_key
FROM caliper_remote_log_tasks WHERE task_id = $1`
	tasks, err := s.scanRemoteLogTasks(ctx, sel, taskID)
	if err != nil {
		return storage.RemoteLogTask{}, err
	}
	if len(tasks) == 0 {
		return storage.RemoteLogTask{}, errors.New("remote log task not found")
	}
	task := tasks[0]
	if task.Status != "pending" {
		return storage.RemoteLogTask{}, fmt.Errorf("remote log task %s already %s", taskID, task.Status)
	}
	if nowMS > task.ExpiresAtMS {
		_, _ = s.db.ExecContext(ctx, `UPDATE caliper_remote_log_tasks SET status = 'expired' WHERE task_id = $1`, taskID)
		return storage.RemoteLogTask{}, errors.New("remote log task expired")
	}
	if int64(len(content)) > task.MaxBytes {
		return storage.RemoteLogTask{}, fmt.Errorf("uploaded log (%d bytes) exceeds task max_bytes (%d)", len(content), task.MaxBytes)
	}
	objKey := "remotelog/" + taskID + ".log"
	if err := s.objectStore().PutObject(ctx, objKey, content, "text/plain; charset=utf-8"); err != nil {
		return storage.RemoteLogTask{}, fmt.Errorf("object store put remotelog: %w", err)
	}

	task.Status = "completed"
	task.CompletedAtMS = nowMS
	task.UploadedBytes = int64(len(content))
	task.SHA256 = sha256Hex
	task.StoragePath = objKey

	const upd = `
UPDATE caliper_remote_log_tasks
SET status = 'completed', completed_at_ms = $2, uploaded_bytes = $3, sha256 = $4, object_key = $5
WHERE task_id = $1 AND status = 'pending'`
	if _, err := s.db.ExecContext(ctx, upd, taskID, nowMS, task.UploadedBytes, sha256Hex, objKey); err != nil {
		return storage.RemoteLogTask{}, err
	}
	return task, nil
}

func (s *Store) GetRemoteLogContent(ctx context.Context, taskID string) (storage.RemoteLogTask, []byte, bool, error) {
	const sel = `SELECT task_id, app_id, platform, target_type, target_hash, reason, requested_by,
status, max_bytes, created_at_ms, expires_at_ms, completed_at_ms, uploaded_bytes, sha256, object_key
FROM caliper_remote_log_tasks WHERE task_id = $1`
	tasks, err := s.scanRemoteLogTasks(ctx, sel, taskID)
	if err != nil || len(tasks) == 0 {
		return storage.RemoteLogTask{}, nil, false, err
	}
	task := tasks[0]
	if task.StoragePath == "" {
		return task, nil, false, nil
	}
	data, ok, err := s.objectStore().GetObject(ctx, task.StoragePath)
	if err != nil || !ok {
		return task, nil, false, err
	}
	return task, data, true, nil
}

func (s *Store) scanRemoteLogTasks(ctx context.Context, query string, args ...any) ([]storage.RemoteLogTask, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]storage.RemoteLogTask, 0)
	for rows.Next() {
		var t storage.RemoteLogTask
		var completedAt sql.NullInt64
		if err := rows.Scan(
			&t.TaskID, &t.AppID, &t.Platform, &t.TargetType, &t.TargetHash, &t.Reason, &t.RequestedBy,
			&t.Status, &t.MaxBytes, &t.CreatedAtMS, &t.ExpiresAtMS, &completedAt,
			&t.UploadedBytes, &t.SHA256, &t.StoragePath,
		); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			t.CompletedAtMS = completedAt.Int64
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type filterArg struct {
	col string
	val string
}

func buildFilters(items []filterArg) (string, []any) {
	var conds []string
	var args []any
	for _, it := range items {
		if it.val == "" {
			continue
		}
		args = append(args, it.val)
		conds = append(conds, fmt.Sprintf("%s = $%d", it.col, len(args)))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}
