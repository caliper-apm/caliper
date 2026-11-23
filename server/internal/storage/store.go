package storage

import (
	"context"

	"github.com/caliper-apm/caliper/server/internal/contract"
	"github.com/caliper-apm/caliper/server/internal/protocol"
)

// ReleaseRecord tracks auto-discovered and CI-registered app builds.
type ReleaseRecord struct {
	AppID       string `json:"app_id"`
	Platform    string `json:"platform"`
	Env         string `json:"env"`
	AppVersion  string `json:"app_version"`
	BuildNumber string `json:"build_number"`
	GitSHA      string `json:"git_sha,omitempty"`
	SymbolID    string `json:"symbol_id,omitempty"`
	Adapter     string `json:"adapter,omitempty"`
	Registered  bool   `json:"registered"`
	FirstSeenMS int64  `json:"first_seen_ms"`
	LastSeenMS  int64  `json:"last_seen_ms"`
	EventCount  int64  `json:"event_count"`
}

// SamplingPolicy configures deterministic client-side collection boundaries.
type SamplingPolicy struct {
	PolicyID          string   `json:"policy_id"`
	AppID             string   `json:"app_id"`
	Platform          string   `json:"platform"`
	Env               string   `json:"env"`
	RatePPM           uint32   `json:"rate_ppm"`
	LeaseTTLMS        int64    `json:"lease_ttl_ms"`
	MaxEvents24h      int      `json:"max_events_24h"`
	EnabledCollectors []string `json:"enabled_collectors"`
	UpdatedAtMS       int64    `json:"updated_at_ms"`
}

// IngestResult summarizes idempotent batch ingestion outcomes.
type IngestResult struct {
	Accepted   int `json:"accepted"`
	Duplicates int `json:"duplicates"`
	Rejected   int `json:"rejected"`
}

// QueryFilter scopes analytical reads by app, platform, release, date range, and scene.
type QueryFilter struct {
	AppID       string
	Platform    string // "android" | "ios"
	Env         string // default "prod"
	AppVersion  string // optional
	BuildNumber string // optional
	Adapter     string // optional: "native" | "react-native"
	Scene       string // optional
	DeviceModel string // optional
	OSVersion   string // optional
	PolicyID    string // optional
	Algorithm   string // optional fluency algorithm filter
	StartDayUTC string // YYYY-MM-DD inclusive
	EndDayUTC   string // YYYY-MM-DD inclusive
}

// StartupAggregate holds mergeable cold-start histograms and stage coverage counts.
type StartupAggregate struct {
	TotalHist      contract.Histogram
	NativeInitHist contract.Histogram
	RuntimeHist    contract.Histogram
	FirstFrameHist contract.Histogram
	SuccessCount   int64
	AbortedCount   int64
	TimeoutCount   int64
	StagedCount    int64
	ContractVer    int
	PolicyIDs      map[string]struct{}
}

// FluencyAggregate holds frame & zero-frame window rollups isolated by algorithm and scene.
type FluencyAggregate struct {
	Scene             string
	Algorithm         string
	TotalWindows      int64
	ValidWindows      int64
	ZeroFrameWindows  int64
	TotalFrames       int64
	JankFrames        int64
	SevereJankFrames  int64
	FrameDurationHist contract.Histogram
	StallWindowCount  int64
	SampleStallStacks [][]string
	PolicyIDs         map[string]struct{}
}

// ResourceAggregate holds CPU, memory footprint, and JS LongTask window rollups per scene.
type ResourceAggregate struct {
	Scene             string
	MemoryKind        string
	ResourceWindows   int64
	CPUPctSum         float64
	MemoryPeakHistMiB contract.Histogram
	JSLongTaskWindows int64
	JSLongTaskCount   int64
	JSBlockingSumMS   float64
	JSMaxTaskMS       float64
	PolicyIDs         map[string]struct{}
}

// NetworkAggregate holds logical API denominator rollups and transport stage histograms.
type NetworkAggregate struct {
	SummaryWindows   int64
	TotalCalls       int64
	FailureCounts    protocol.NetworkFailureCounts
	DurationHist     contract.Histogram
	DurationSumMS    float64
	TransportSamples map[string]*TransportOpAggregate
	PolicyIDs        map[string]struct{}
}

// TransportOpAggregate summarizes per-operation Native DNS/TCP/TLS/TTFB/Body observations.
type TransportOpAggregate struct {
	Operation   string
	Method      string
	TotalCount  int64
	SuccessCnt  int64
	FailedCnt   int64
	TimeoutCnt  int64
	ReusedCnt   int64
	DNSHist     contract.Histogram
	TCPHist     contract.Histogram
	TLSHist     contract.Histogram
	TTFBHist    contract.Histogram
	BodyHist    contract.Histogram
	TotalHist   contract.Histogram
}

// IssueState stores mutable enterprise triage workflow metadata per stability fingerprint.
type IssueState struct {
	Fingerprint       string `json:"fingerprint"`
	Status            string `json:"status"` // open | in_progress | resolved | ignored
	Assignee          string `json:"assignee,omitempty"`
	Note              string `json:"note,omitempty"`
	ResolvedInVersion string `json:"resolved_in_version,omitempty"`
	UpdatedAtMS       int64  `json:"updated_at_ms"`
}

// IssueSummary aggregates crash, ANR, and abnormal_exit events by fingerprint.
type IssueSummary struct {
	Fingerprint       string                `json:"fingerprint"`
	Kind              string                `json:"kind"`
	Title             string                `json:"title"`
	Fatal             bool                  `json:"fatal"`
	ExceptionType     string                `json:"exception_type,omitempty"`
	ExitReason        string                `json:"exit_reason,omitempty"`
	EventCount        int64                 `json:"event_count"`
	AffectedInstalls  int64                 `json:"affected_installs"`
	FirstSeenMS       int64                 `json:"first_seen_ms"`
	LastSeenMS        int64                 `json:"last_seen_ms"`
	Releases          []string              `json:"releases"`
	DeviceBreakdown   map[string]int64      `json:"device_breakdown,omitempty"`
	OSBreakdown       map[string]int64      `json:"os_breakdown,omitempty"`
	ReleaseBreakdown  map[string]int64      `json:"release_breakdown,omitempty"`
	SampleUserHash    string                `json:"sample_user_hash,omitempty"`
	SampleInstallHash string                `json:"sample_install_hash,omitempty"`
	Status            string                `json:"status,omitempty"`
	Assignee          string                `json:"assignee,omitempty"`
	Note              string                `json:"note,omitempty"`
	ResolvedInVersion string                `json:"resolved_in_version,omitempty"`
	StateUpdatedAtMS  int64                 `json:"state_updated_at_ms,omitempty"`
	SampleFrames      []protocol.StackFrame `json:"sample_frames,omitempty"`
	SampleCrumbs      []protocol.Breadcrumb `json:"sample_breadcrumbs,omitempty"`
}

// Stats exposes runtime store metrics for /healthz.
type Stats struct {
	Engine       string `json:"engine"`
	TotalEvents  int64  `json:"total_events"`
	ReleaseCount int    `json:"release_count"`
	IssueCount   int    `json:"issue_count"`
}

// SessionTimelineEvent is a bounded chronological summary record for audited UID/install troubleshooting.
type SessionTimelineEvent struct {
	EventID      string `json:"event_id"`
	OccurredAtMS int64  `json:"occurred_at_ms"`
	AppID        string `json:"app_id"`
	Platform     string `json:"platform"`
	Env          string `json:"env"`
	AppVersion   string `json:"app_version"`
	BuildNumber  string `json:"build_number"`
	Adapter      string `json:"adapter,omitempty"`
	InstallHash  string `json:"install_hash"`
	UserHash     string `json:"user_hash,omitempty"`
	Kind         string `json:"kind"`
	Scene        string `json:"scene,omitempty"`
	Summary      string `json:"summary"`
}

// SessionLookupFilter scopes an audited user/install timeline lookup.
type SessionLookupFilter struct {
	AppID       string
	Platform    string
	InstallHash string
	UserHash    string
	Limit       int
}

// AuditLogEntry records an immutable security audit event (e.g. audited UID/session lookup).
type AuditLogEntry struct {
	AuditID     string `json:"audit_id"`
	TimestampMS int64  `json:"timestamp_ms"`
	Actor       string `json:"actor"`
	Role        string `json:"role"`
	Action      string `json:"action"`      // session_lookup | policy_update | alert_upsert
	TargetType  string `json:"target_type"` // install_hash | user_hash | rule | policy
	TargetHash  string `json:"target_hash"`
	Reason      string `json:"reason"`
	RemoteIP    string `json:"remote_ip,omitempty"`
	ResultCount int    `json:"result_count"`
}

// CPUProfileRecord stores one ingested Native or Hermes folded-stack CPU profile.
type CPUProfileRecord struct {
	ProfileID        string                        `json:"profile_id"`
	OccurredAtMS     int64                         `json:"occurred_at_ms"`
	AppID            string                        `json:"app_id"`
	Platform         string                        `json:"platform"`
	Env              string                        `json:"env"`
	AppVersion       string                        `json:"app_version"`
	BuildNumber      string                        `json:"build_number"`
	Scene            string                        `json:"scene"`
	Runtime          string                        `json:"runtime"` // native | hermes
	ThreadName       string                        `json:"thread_name,omitempty"`
	SampleIntervalMS int                           `json:"sample_interval_ms"`
	DurationMS       int64                         `json:"duration_ms"`
	TotalSamples     int64                         `json:"total_samples"`
	Stacks           []protocol.ProfileStackSample `json:"stacks"`
}

// HeapSnapshotRecord stores one ingested Zero-PII trimmed heap/OOM dominator snapshot.
type HeapSnapshotRecord struct {
	SnapshotID        string                   `json:"snapshot_id"`
	OccurredAtMS      int64                    `json:"occurred_at_ms"`
	AppID             string                   `json:"app_id"`
	Platform          string                   `json:"platform"`
	Env               string                   `json:"env"`
	AppVersion        string                   `json:"app_version"`
	BuildNumber       string                   `json:"build_number"`
	Scene             string                   `json:"scene"`
	TriggerReason     string                   `json:"trigger_reason"`
	MemoryKind        string                   `json:"memory_kind"`
	TotalAllocatedKiB float64                  `json:"total_allocated_kib"`
	TotalRetainedKiB  float64                  `json:"total_retained_kib"`
	MemoryLimitKiB    float64                  `json:"memory_limit_kib,omitempty"`
	Dominators        []protocol.HeapDominator `json:"dominators"`
}

// RemoteLogTask coordinates an audited, bounded remote diagnostic log pull from a target device/user.
type RemoteLogTask struct {
	TaskID        string `json:"task_id"`
	AppID         string `json:"app_id"`
	Platform      string `json:"platform"`
	TargetType    string `json:"target_type"` // install_hash | user_hash
	TargetHash    string `json:"target_hash"`
	Reason        string `json:"reason"`
	RequestedBy   string `json:"requested_by"`
	Status        string `json:"status"` // pending | completed | expired
	MaxBytes      int64  `json:"max_bytes"`
	CreatedAtMS   int64  `json:"created_at_ms"`
	ExpiresAtMS   int64  `json:"expires_at_ms"`
	CompletedAtMS int64  `json:"completed_at_ms,omitempty"`
	UploadedBytes int64  `json:"uploaded_bytes,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	StoragePath   string `json:"storage_path,omitempty"`
}

// DailyTrendPoint captures one UTC calendar day's Zero-Lie aggregated observability signals.
// Missing or under-sampled metrics remain nil (never zero-filled).
type DailyTrendPoint struct {
	DayUTC          string   `json:"day_utc"`
	DominantVersion string   `json:"dominant_version,omitempty"`
	StartupP50MS    *float64 `json:"startup_p50_ms"`
	StartupP90MS    *float64 `json:"startup_p90_ms"`
	StartupSamples  int64    `json:"startup_samples"`
	JankRatePct     *float64 `json:"jank_rate_pct"`
	FluencyWindows  int64    `json:"fluency_windows"`
	APISuccessPct   *float64 `json:"api_success_rate_pct"`
	APIP90MS        *float64 `json:"api_p90_ms"`
	APICalls        int64    `json:"api_calls"`
	AvgCPUPct       *float64 `json:"avg_cpu_pct"`
	MemoryP90MiB    *float64 `json:"memory_p90_mib"`
	ResourceWindows int64    `json:"resource_windows"`
	CrashEvents     int64    `json:"crash_events"`
	ANREvents       int64    `json:"anr_events"`
	AbnormalExits   int64    `json:"abnormal_exit_events"`
}

// AnalyticsStore is the pluggable OLAP ingestion and query interface.
type AnalyticsStore interface {
	IngestBatch(ctx context.Context, batch *protocol.BatchEnvelope) (IngestResult, error)
	QueryStartup(ctx context.Context, f QueryFilter) (StartupAggregate, error)
	QueryFluency(ctx context.Context, f QueryFilter) ([]FluencyAggregate, error)
	QueryResources(ctx context.Context, f QueryFilter) ([]ResourceAggregate, error)
	QueryNetwork(ctx context.Context, f QueryFilter) (NetworkAggregate, error)
	QueryIssues(ctx context.Context, f QueryFilter, limit int) ([]IssueSummary, error)
	GetIssue(ctx context.Context, fingerprint string, f QueryFilter) (*IssueSummary, bool, error)
	QueryDailyTrends(ctx context.Context, f QueryFilter) ([]DailyTrendPoint, error)
	QuerySessionTimeline(ctx context.Context, f SessionLookupFilter) ([]SessionTimelineEvent, error)
	QueryCPUProfiles(ctx context.Context, f QueryFilter, runtime string, limit int) ([]CPUProfileRecord, error)
	GetCPUProfile(ctx context.Context, profileID string) (*CPUProfileRecord, bool, error)
	QueryHeapSnapshots(ctx context.Context, f QueryFilter, limit int) ([]HeapSnapshotRecord, error)
	Stats(ctx context.Context) Stats
	Close() error
}

// SymbolRecord tracks build-bound symbol archives (R8, dSYM, Hermes SourceMap, Breakpad).
type SymbolRecord struct {
	SymbolID     string `json:"symbol_id"`
	AppID        string `json:"app_id"`
	Platform     string `json:"platform"`
	Env          string `json:"env"`
	AppVersion   string `json:"app_version"`
	BuildNumber  string `json:"build_number"`
	GitSHA       string `json:"git_sha,omitempty"`
	Kind         string `json:"kind"` // r8_mapping | dsym | hermes_sourcemap | breakpad
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"size_bytes"`
	StoragePath  string `json:"storage_path,omitempty"`
	UploadedAtMS int64  `json:"uploaded_at_ms"`
}

// AlertRule defines a threshold-based alert rule with explicit Zero-Lie minimum sample gate.
type AlertRule struct {
	RuleID        string  `json:"rule_id"`
	Name          string  `json:"name"`
	AppID         string  `json:"app_id"`
	Platform      string  `json:"platform"` // android | ios
	Env           string  `json:"env"`      // prod | staging | dev
	AppVersion    string  `json:"app_version,omitempty"`
	Scene         string  `json:"scene,omitempty"`
	MetricID      string  `json:"metric_id"`
	Operator      string  `json:"operator"` // gt | lt
	Threshold     float64 `json:"threshold"`
	MinSamples    int64   `json:"min_samples"`
	WebhookURL    string  `json:"webhook_url"`
	WebhookFormat string  `json:"webhook_format"` // generic | feishu | slack | discord
	CooldownSec   int64   `json:"cooldown_sec"`
	Enabled       bool    `json:"enabled"`
	UpdatedAtMS   int64   `json:"updated_at_ms"`
}

// AlertIncident tracks the live firing/resolved lifecycle and real webhook delivery receipts.
type AlertIncident struct {
	IncidentID         string   `json:"incident_id"`
	RuleID             string   `json:"rule_id"`
	RuleName           string   `json:"rule_name"`
	AppID              string   `json:"app_id"`
	Platform           string   `json:"platform"`
	MetricID           string   `json:"metric_id"`
	Status             string   `json:"status"` // firing | resolved
	ObservedValue      *float64 `json:"observed_value"`
	Threshold          float64  `json:"threshold"`
	SampleSize         int64    `json:"sample_size"`
	TriggeredAtMS      int64    `json:"triggered_at_ms"`
	LastEvaluatedMS    int64    `json:"last_evaluated_ms"`
	ResolvedAtMS       int64    `json:"resolved_at_ms,omitempty"`
	LastNotifiedMS     int64    `json:"last_notified_ms,omitempty"`
	DeliveryStatus     string   `json:"delivery_status,omitempty"` // delivered | failed | skipped_cooldown | none
	DeliveryHTTPStatus int      `json:"delivery_http_status,omitempty"`
	DeliveryError      string   `json:"delivery_error,omitempty"`
}

// MetaStore manages releases, symbols, sampling policies, and alert rules.
type MetaStore interface {
	RegisterRelease(ctx context.Context, r ReleaseRecord) error
	ListReleases(ctx context.Context, appID, platform, env string) ([]ReleaseRecord, error)
	SaveSymbol(ctx context.Context, s SymbolRecord, content []byte) (SymbolRecord, error)
	ListSymbols(ctx context.Context, appID, platform, appVersion, buildNumber string) ([]SymbolRecord, error)
	GetSymbolContent(ctx context.Context, symbolID string) (SymbolRecord, []byte, bool, error)
	GetPolicy(ctx context.Context, appID, platform, env string) (SamplingPolicy, error)
	UpsertPolicy(ctx context.Context, p SamplingPolicy) error
	UpsertAlertRule(ctx context.Context, rule AlertRule) (AlertRule, error)
	DeleteAlertRule(ctx context.Context, ruleID string) error
	ListAlertRules(ctx context.Context, appID, platform string) ([]AlertRule, error)
	SaveAlertIncident(ctx context.Context, inc AlertIncident) error
	ListAlertIncidents(ctx context.Context, ruleID string) ([]AlertIncident, error)
	UpsertIssueState(ctx context.Context, state IssueState) (IssueState, error)
	GetIssueState(ctx context.Context, fingerprint string) (IssueState, bool, error)
	ListIssueStates(ctx context.Context) ([]IssueState, error)
	AppendAuditLog(ctx context.Context, entry AuditLogEntry) error
	ListAuditLogs(ctx context.Context, limit int) ([]AuditLogEntry, error)
	CreateRemoteLogTask(ctx context.Context, task RemoteLogTask) (RemoteLogTask, error)
	ListRemoteLogTasks(ctx context.Context, appID, platform string) ([]RemoteLogTask, error)
	PollPendingLogTasks(ctx context.Context, appID, platform, installHash, userHash string, nowMS int64) ([]RemoteLogTask, error)
	CompleteRemoteLogTask(ctx context.Context, taskID string, content []byte, sha256Hex string, nowMS int64) (RemoteLogTask, error)
	GetRemoteLogContent(ctx context.Context, taskID string) (RemoteLogTask, []byte, bool, error)
}
