package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"

	"github.com/caliper-apm/caliper/server/internal/contract"
)

const (
	CurrentSchemaVersion = 1
	MaxBatchEvents       = 200
	MaxPayloadBytes      = 2 * 1024 * 1024 // 2 MiB uncompressed cap per request
)

// Supported event kinds matching spec/protocol/envelope.schema.json.
const (
	KindStartup         = "startup"
	KindCrash           = "crash"
	KindANR             = "anr"
	KindAbnormalExit    = "abnormal_exit"
	KindFluencyWindow   = "fluency_window"
	KindResourceWindow  = "resource_window"
	KindJSLongTask      = "js_longtask_window"
	KindNetworkSummary  = "network_summary"
	KindNetworkStage    = "network_transport"
	KindCustomScene     = "custom_scene"
	KindCPUProfile      = "cpu_profile"
	KindHeapSnapshot    = "heap_snapshot"
)

var (
	reHexHash = regexp.MustCompile(`^[a-fA-F0-9]{7,64}$`)
	reInstall = regexp.MustCompile(`^[a-f0-9]{16,64}$`)
)

// BatchEnvelope is the top-level ingestion payload sent by Native & RN SDKs.
type BatchEnvelope struct {
	SchemaVersion int      `json:"schema_version"`
	SentAtMS      int64    `json:"sent_at_ms"`
	Resource      Resource `json:"resource"`
	Events        []Event  `json:"events"`
}

// Resource binds every ingested batch to an exact build and platform identity.
type Resource struct {
	AppID       string `json:"app_id"`
	Platform    string `json:"platform"` // "android" | "ios"
	Env         string `json:"env"`      // "prod" | "staging" | "dev"
	AppVersion  string `json:"app_version"`
	BuildNumber string `json:"build_number"`
	GitSHA      string `json:"git_sha,omitempty"`
	SymbolID    string `json:"symbol_id,omitempty"`
	Adapter     string `json:"adapter,omitempty"` // "native" | "react-native"
	SDKName     string `json:"sdk_name"`
	SDKVersion  string `json:"sdk_version"`
	InstallHash string `json:"install_hash"`
	UserHash    string `json:"user_hash,omitempty"` // optional client-hashed UID (never raw PII)
	OSVersion   string `json:"os_version,omitempty"`
	DeviceModel string `json:"device_model,omitempty"`
}

// Event is one bounded telemetry record inside a batch.
type Event struct {
	EventID         string          `json:"event_id"`
	Kind            string          `json:"kind"`
	OccurredAtMS    int64           `json:"occurred_at_ms"`
	ContractVersion int             `json:"contract_version"`
	Scene           string          `json:"scene,omitempty"`
	WindowID        string          `json:"window_id,omitempty"`
	PolicyID        string          `json:"policy_id,omitempty"`
	SampleRatePPM   int             `json:"sample_rate_ppm,omitempty"`
	Payload         json.RawMessage `json:"payload"`
}

// StartupPayload tracks single-anchor multi-stage startup timing.
type StartupPayload struct {
	TotalDurationMS float64  `json:"total_duration_ms"`
	NativeInitMS    *float64 `json:"native_init_ms,omitempty"`
	RuntimeInitMS   *float64 `json:"runtime_init_ms,omitempty"`
	FirstFrameMS    *float64 `json:"first_frame_ms,omitempty"`
	ColdStart       bool     `json:"cold_start"`
	StartAnchor     string   `json:"start_anchor"`
	EndAnchor       string   `json:"end_anchor"`
	Result          string   `json:"result"` // "success" | "aborted" | "timeout"
}

// StackFrame represents one raw or symbolicated frame.
type StackFrame struct {
	Module  string `json:"module,omitempty"`
	Symbol  string `json:"symbol"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Address string `json:"address,omitempty"`
	InApp   bool   `json:"in_app,omitempty"`
}

// Breadcrumb records a bounded trail of pre-fault lifecycle/user events.
type Breadcrumb struct {
	TimestampMS int64  `json:"timestamp_ms"`
	Category    string `json:"category"`
	Message     string `json:"message"`
}

// StabilityPayload covers crash, ANR, and abnormal_exit records.
type StabilityPayload struct {
	Fingerprint       string       `json:"fingerprint"`
	Title             string       `json:"title"`
	Fatal             bool         `json:"fatal"`
	ExceptionType     string       `json:"exception_type,omitempty"`
	ThreadName        string       `json:"thread_name,omitempty"`
	ExitReason        string       `json:"exit_reason,omitempty"`
	ProcessImportance string       `json:"process_importance,omitempty"`
	Frames            []StackFrame `json:"frames"`
	Breadcrumbs       []Breadcrumb `json:"breadcrumbs,omitempty"`
}

// FluencyWindowPayload tracks frame rendering smoothness and optional same-window stall stack.
type FluencyWindowPayload struct {
	Algorithm            string   `json:"algorithm"` // android_frame_metrics_deadline | android_frame_metrics_heuristic | ios_display_link
	WindowDurationMS     int64    `json:"window_duration_ms"`
	FrameCount           int64    `json:"frame_count"`
	JankFrameCount       int64    `json:"jank_frame_count"`
	SevereJankFrameCount int64    `json:"severe_jank_frame_count,omitempty"`
	RefreshRateHz        float64  `json:"refresh_rate_hz,omitempty"`
	FrameDurationBuckets []int64  `json:"frame_duration_buckets,omitempty"`
	StallStack           []string `json:"stall_stack,omitempty"`
}

// ResourceWindowPayload tracks low-frequency process CPU and resident memory readings.
type ResourceWindowPayload struct {
	WindowDurationMS int64   `json:"window_duration_ms"`
	CPUPct           float64 `json:"cpu_pct"`
	MemoryKind       string  `json:"memory_kind"` // android_pss_kib | android_rss_kib | ios_phys_footprint_kib
	MemoryPeakKiB    float64 `json:"memory_peak_kib"`
	MemoryAvgKiB     float64 `json:"memory_avg_kib,omitempty"`
	SampleCount      int     `json:"sample_count"`
}

// JSLongTaskPayload tracks React Native JS main-thread blocking windows (optional RN adapter).
type JSLongTaskPayload struct {
	WindowDurationMS int64   `json:"window_duration_ms"`
	LongTaskCount    int64   `json:"longtask_count"`
	TotalBlockingMS  float64 `json:"total_blocking_ms"`
	MaxTaskMS        float64 `json:"max_task_ms"`
	DroppedEntries   int64   `json:"dropped_entries,omitempty"`
}

// NetworkFailureCounts breaks down logical API call failures by domain.
type NetworkFailureCounts struct {
	HTTP     int64 `json:"http,omitempty"`
	Business int64 `json:"business,omitempty"`
	Parse    int64 `json:"parse,omitempty"`
	Network  int64 `json:"network,omitempty"`
	Timeout  int64 `json:"timeout,omitempty"`
}

// Total returns total failed calls across all failure domains.
func (f NetworkFailureCounts) Total() int64 {
	return f.HTTP + f.Business + f.Parse + f.Network + f.Timeout
}

// NetworkSummaryPayload tracks allowlisted logical API denominator & latency histogram per window.
type NetworkSummaryPayload struct {
	WindowDurationMS int64                `json:"window_duration_ms"`
	CallCount        int64                `json:"call_count"`
	FailureCounts    NetworkFailureCounts `json:"failure_counts"`
	DurationBuckets  []int64              `json:"duration_buckets"`
	DurationSumMS    float64              `json:"duration_sum_ms"`
}

// NetworkTransportPayload tracks sampled Native transport stages (OkHttp / URLSession).
type NetworkTransportPayload struct {
	Operation        string   `json:"operation"`
	Method           string   `json:"method"`
	StatusCode       int      `json:"status_code,omitempty"`
	Result           string   `json:"result"` // "success" | "failed" | "timeout" | "cancelled"
	DNSMS            *float64 `json:"dns_ms,omitempty"`
	TCPMS            *float64 `json:"tcp_ms,omitempty"`
	TLSMS            *float64 `json:"tls_ms,omitempty"`
	TTFBMS           *float64 `json:"ttfb_ms,omitempty"`
	BodyMS           *float64 `json:"body_ms,omitempty"`
	TotalMS          float64  `json:"total_ms"`
	ConnectionReused bool     `json:"connection_reused,omitempty"`
	Protocol         string   `json:"protocol,omitempty"`
	NetworkType      string   `json:"network_type,omitempty"`
}

// SceneStage captures one ordered stage duration inside a custom business journey trace.
type SceneStage struct {
	Name       string  `json:"name"`
	DurationMS float64 `json:"duration_ms"`
}

// CustomScenePayload tracks arbitrary business journeys with explicit start/terminal outcome
// and optional ordered child stage durations for multi-stage waterfall attribution.
type CustomScenePayload struct {
	JourneyName string       `json:"journey_name"`
	Status      string       `json:"status"` // "success" | "failure" | "cancelled" | "timeout"
	DurationMS  *float64     `json:"duration_ms,omitempty"`
	ErrorCode   string       `json:"error_code,omitempty"`
	Stages      []SceneStage `json:"stages,omitempty"`
}

// ProfileStackSample holds one folded root-to-leaf callstack and sample count for CPU flamegraphs.
type ProfileStackSample struct {
	Frames  []string `json:"frames"` // root-to-leaf frame strings (raw or obfuscated)
	Samples int64    `json:"samples"`
}

// CPUProfilePayload captures bounded Native (ART/mach) or Hermes JS sampling CPU profiles.
type CPUProfilePayload struct {
	Runtime          string               `json:"runtime"` // "native" | "hermes"
	ThreadName       string               `json:"thread_name,omitempty"`
	SampleIntervalMS int                  `json:"sample_interval_ms"`
	DurationMS       int64                `json:"duration_ms"`
	TotalSamples     int64                `json:"total_samples"`
	Stacks           []ProfileStackSample `json:"stacks"`
}

// HeapDominator summarizes retained bytes for one class/type without uploading raw object values/PII.
type HeapDominator struct {
	ClassName     string   `json:"class_name"`
	InstanceCount int64    `json:"instance_count"`
	RetainedKiB   float64  `json:"retained_kib"`
	RetainerPath  []string `json:"retainer_path,omitempty"`
}

// HeapSnapshotPayload captures privacy-safe trimmed heap/OOM dominator diagnostics.
type HeapSnapshotPayload struct {
	TriggerReason     string          `json:"trigger_reason"` // "oom_warning" | "high_watermark" | "manual"
	MemoryKind        string          `json:"memory_kind"`    // android_pss_kib | ios_phys_footprint_kib | hermes_heap_kib
	TotalAllocatedKiB float64         `json:"total_allocated_kib"`
	TotalRetainedKiB  float64         `json:"total_retained_kib"`
	MemoryLimitKiB    float64         `json:"memory_limit_kib,omitempty"`
	Dominators        []HeapDominator `json:"dominators"`
}

// Validate checks top-level envelope and resource identity constraints.
func (b *BatchEnvelope) Validate() error {
	if b.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported schema_version: %d", b.SchemaVersion)
	}
	if b.SentAtMS <= 0 {
		return errors.New("sent_at_ms must be positive")
	}
	if err := b.Resource.Validate(); err != nil {
		return fmt.Errorf("invalid resource: %w", err)
	}
	if len(b.Events) == 0 || len(b.Events) > MaxBatchEvents {
		return fmt.Errorf("events count must be in [1, %d], got %d", MaxBatchEvents, len(b.Events))
	}
	return nil
}

// Validate checks required build & platform identity fields.
func (r *Resource) Validate() error {
	if r.AppID == "" || len(r.AppID) > 128 {
		return errors.New("app_id is required (max 128 chars)")
	}
	if r.Platform != "android" && r.Platform != "ios" {
		return fmt.Errorf("platform must be android or ios, got %q", r.Platform)
	}
	if r.Env != "prod" && r.Env != "staging" && r.Env != "dev" {
		return fmt.Errorf("env must be prod, staging, or dev, got %q", r.Env)
	}
	if r.AppVersion == "" || len(r.AppVersion) > 64 {
		return errors.New("app_version is required")
	}
	if r.BuildNumber == "" || len(r.BuildNumber) > 64 {
		return errors.New("build_number is required")
	}
	if r.GitSHA != "" && !reHexHash.MatchString(r.GitSHA) {
		return fmt.Errorf("invalid git_sha: %q", r.GitSHA)
	}
	if r.Adapter != "" && r.Adapter != "native" && r.Adapter != "react-native" {
		return fmt.Errorf("adapter must be native or react-native, got %q", r.Adapter)
	}
	if r.SDKName == "" || r.SDKVersion == "" {
		return errors.New("sdk_name and sdk_version are required")
	}
	if !reInstall.MatchString(r.InstallHash) {
		return errors.New("install_hash must be 16-64 lowercase hex characters")
	}
	if r.UserHash != "" && !reInstall.MatchString(r.UserHash) {
		return errors.New("user_hash must be 16-64 lowercase hex characters (never raw PII)")
	}
	return nil
}

// ValidateEvent parses and strictly validates a single event's envelope and typed payload.
func ValidateEvent(e *Event) (any, error) {
	if len(e.EventID) < 8 || len(e.EventID) > 64 {
		return nil, errors.New("event_id must be 8-64 chars")
	}
	if e.OccurredAtMS <= 0 {
		return nil, errors.New("occurred_at_ms must be positive")
	}
	if e.ContractVersion < 1 {
		return nil, errors.New("contract_version must be >= 1")
	}
	if e.SampleRatePPM < 0 || e.SampleRatePPM > contract.MaxSamplingRatePPM {
		return nil, errors.New("sample_rate_ppm out of [0, 1000000]")
	}
	if len(e.Payload) == 0 {
		return nil, errors.New("payload is required")
	}

	switch e.Kind {
	case KindStartup:
		var p StartupPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode startup: %w", err)
		}
		if p.StartAnchor == "" || p.EndAnchor == "" {
			return nil, errors.New("startup anchors are required")
		}
		if p.Result != "success" && p.Result != "aborted" && p.Result != "timeout" {
			return nil, fmt.Errorf("invalid startup result %q", p.Result)
		}
		if !isFiniteNonNeg(p.TotalDurationMS) || p.TotalDurationMS > 600_000 {
			return nil, errors.New("startup total_duration_ms out of [0, 600000]")
		}
		if !contract.ValidateStartupStages(p.TotalDurationMS, p.NativeInitMS, p.RuntimeInitMS, p.FirstFrameMS) {
			return nil, errors.New("startup stage durations violate conservation against total_duration_ms")
		}
		return p, nil

	case KindCrash, KindANR, KindAbnormalExit:
		var p StabilityPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode %s: %w", e.Kind, err)
		}
		if len(p.Fingerprint) < 4 || len(p.Fingerprint) > 128 {
			return nil, errors.New("stability fingerprint must be 4-128 chars")
		}
		if p.Title == "" || len(p.Title) > 256 {
			return nil, errors.New("stability title is required")
		}
		if len(p.Frames) > 128 || len(p.Breadcrumbs) > 32 {
			return nil, errors.New("frames or breadcrumbs exceed bounds")
		}
		return p, nil

	case KindFluencyWindow:
		var p FluencyWindowPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode fluency_window: %w", err)
		}
		switch p.Algorithm {
		case "android_frame_metrics_deadline", "android_frame_metrics_heuristic", "ios_display_link":
		default:
			return nil, fmt.Errorf("invalid fluency algorithm %q", p.Algorithm)
		}
		if p.WindowDurationMS <= 0 || p.WindowDurationMS > 300_000 {
			return nil, errors.New("fluency window_duration_ms out of (0, 300000]")
		}
		if p.FrameCount < 0 || p.JankFrameCount < 0 || p.JankFrameCount > p.FrameCount {
			return nil, errors.New("fluency frame/jank counts inconsistent")
		}
		if len(p.FrameDurationBuckets) > 0 {
			if len(p.FrameDurationBuckets) != contract.HistogramBucketCount {
				return nil, errors.New("frame_duration_buckets must have 19 entries")
			}
			for _, c := range p.FrameDurationBuckets {
				if c < 0 {
					return nil, errors.New("negative bucket count")
				}
			}
		}
		return p, nil

	case KindResourceWindow:
		var p ResourceWindowPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode resource_window: %w", err)
		}
		if p.WindowDurationMS <= 0 || p.WindowDurationMS > 300_000 {
			return nil, errors.New("resource window_duration_ms out of bounds")
		}
		if !isFiniteNonNeg(p.CPUPct) || p.CPUPct > 1600 {
			return nil, errors.New("cpu_pct out of [0, 1600]")
		}
		switch p.MemoryKind {
		case "android_pss_kib", "android_rss_kib", "ios_phys_footprint_kib":
		default:
			return nil, fmt.Errorf("invalid memory_kind %q", p.MemoryKind)
		}
		if !isFiniteNonNeg(p.MemoryPeakKiB) || p.SampleCount < 1 {
			return nil, errors.New("invalid memory_peak_kib or sample_count")
		}
		return p, nil

	case KindJSLongTask:
		var p JSLongTaskPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode js_longtask_window: %w", err)
		}
		if p.WindowDurationMS <= 0 || p.WindowDurationMS > 300_000 || p.LongTaskCount < 0 || !isFiniteNonNeg(p.TotalBlockingMS) || !isFiniteNonNeg(p.MaxTaskMS) {
			return nil, errors.New("invalid js_longtask_window metrics")
		}
		return p, nil

	case KindNetworkSummary:
		var p NetworkSummaryPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode network_summary: %w", err)
		}
		if p.WindowDurationMS <= 0 || p.CallCount < 0 || p.CallCount > 100_000 || !isFiniteNonNeg(p.DurationSumMS) {
			return nil, errors.New("invalid network_summary bounds")
		}
		if p.FailureCounts.Total() > p.CallCount {
			return nil, errors.New("failure_counts exceed call_count")
		}
		if len(p.DurationBuckets) != contract.HistogramBucketCount {
			return nil, errors.New("duration_buckets must have 19 entries")
		}
		var bucketSum int64
		for _, c := range p.DurationBuckets {
			if c < 0 {
				return nil, errors.New("negative duration bucket")
			}
			bucketSum += c
		}
		if bucketSum != p.CallCount {
			return nil, fmt.Errorf("duration_buckets sum %d != call_count %d", bucketSum, p.CallCount)
		}
		return p, nil

	case KindNetworkStage:
		var p NetworkTransportPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode network_transport: %w", err)
		}
		if p.Operation == "" || len(p.Operation) > 128 {
			return nil, errors.New("operation is required")
		}
		switch p.Method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			return nil, fmt.Errorf("invalid HTTP method %q", p.Method)
		}
		switch p.Result {
		case "success", "failed", "timeout", "cancelled":
		default:
			return nil, fmt.Errorf("invalid network result %q", p.Result)
		}
		if !isFiniteNonNeg(p.TotalMS) || p.TotalMS > 600_000 {
			return nil, errors.New("total_ms out of [0, 600000]")
		}
		for _, stage := range []*float64{p.DNSMS, p.TCPMS, p.TLSMS, p.TTFBMS, p.BodyMS} {
			if stage != nil && !isFiniteNonNeg(*stage) {
				return nil, errors.New("transport stage ms must be finite non-negative")
			}
		}
		return p, nil

	case KindCustomScene:
		var p CustomScenePayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode custom_scene: %w", err)
		}
		if p.JourneyName == "" || len(p.JourneyName) > 64 {
			return nil, errors.New("journey_name is required")
		}
		switch p.Status {
		case "success", "failure", "cancelled", "timeout":
		default:
			return nil, fmt.Errorf("invalid custom_scene status %q", p.Status)
		}
		if p.DurationMS != nil && (!isFiniteNonNeg(*p.DurationMS) || *p.DurationMS > 600_000) {
			return nil, errors.New("custom_scene duration_ms out of bounds")
		}
		if len(p.Stages) > 16 {
			return nil, errors.New("custom_scene stages exceed maximum of 16")
		}
		for _, st := range p.Stages {
			if st.Name == "" || len(st.Name) > 48 {
				return nil, errors.New("custom_scene stage name must be 1..48 chars")
			}
			if !isFiniteNonNeg(st.DurationMS) || st.DurationMS > 600_000 {
				return nil, errors.New("custom_scene stage duration_ms out of bounds")
			}
		}
		return p, nil

	case KindCPUProfile:
		var p CPUProfilePayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode cpu_profile: %w", err)
		}
		if p.Runtime != "native" && p.Runtime != "hermes" {
			return nil, fmt.Errorf("invalid cpu_profile runtime %q", p.Runtime)
		}
		if p.SampleIntervalMS <= 0 || p.SampleIntervalMS > 1000 || p.DurationMS <= 0 || p.DurationMS > 300_000 {
			return nil, errors.New("cpu_profile timing bounds exceeded")
		}
		if len(p.Stacks) == 0 || len(p.Stacks) > 256 {
			return nil, errors.New("cpu_profile stacks count must be in [1, 256]")
		}
		var sum int64
		for _, s := range p.Stacks {
			if len(s.Frames) == 0 || len(s.Frames) > 64 || s.Samples <= 0 {
				return nil, errors.New("cpu_profile stack frames (1..64) and positive samples required")
			}
			sum += s.Samples
		}
		if p.TotalSamples <= 0 {
			p.TotalSamples = sum
		}
		return p, nil

	case KindHeapSnapshot:
		var p HeapSnapshotPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil, fmt.Errorf("decode heap_snapshot: %w", err)
		}
		switch p.TriggerReason {
		case "oom_warning", "high_watermark", "manual":
		default:
			return nil, fmt.Errorf("invalid heap_snapshot trigger_reason %q", p.TriggerReason)
		}
		switch p.MemoryKind {
		case "android_pss_kib", "ios_phys_footprint_kib", "hermes_heap_kib":
		default:
			return nil, fmt.Errorf("invalid heap_snapshot memory_kind %q", p.MemoryKind)
		}
		if !isFiniteNonNeg(p.TotalAllocatedKiB) || !isFiniteNonNeg(p.TotalRetainedKiB) {
			return nil, errors.New("heap_snapshot totals must be finite non-negative")
		}
		if len(p.Dominators) == 0 || len(p.Dominators) > 128 {
			return nil, errors.New("heap_snapshot dominators must be in [1, 128]")
		}
		for _, d := range p.Dominators {
			if d.ClassName == "" || d.InstanceCount < 0 || !isFiniteNonNeg(d.RetainedKiB) {
				return nil, errors.New("invalid heap dominator entry")
			}
		}
		return p, nil

	default:
		return nil, fmt.Errorf("unsupported event kind %q", e.Kind)
	}
}

func isFiniteNonNeg(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0
}
