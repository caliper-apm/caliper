package clickhouse

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/caliper-apm/caliper/server/internal/contract"
	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

//go:embed schema.sql
var schemaSQL string

var reValidIdent = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,63}$`)

// Config configures the zero-CGO native ClickHouse HTTP backend.
type Config struct {
	Endpoint string        // e.g. http://127.0.0.1:8123
	Database string        // default: caliper
	Username string        // optional X-ClickHouse-User
	Password string        // optional X-ClickHouse-Key
	Timeout  time.Duration // default: 15s
	Meta     storage.MetaStore
}

// Store implements storage.AnalyticsStore backed by ClickHouse HTTP (JSONEachRow + sumForEach histograms).
type Store struct {
	endpoint   string
	database   string
	username   string
	password   string
	client     *http.Client
	meta       storage.MetaStore
	totalWrite atomic.Int64
}

var _ storage.AnalyticsStore = (*Store)(nil)

// New creates a ClickHouse AnalyticsStore without CGO or external driver dependencies.
func New(cfg Config) (*Store, error) {
	ep := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if ep == "" {
		return nil, errors.New("clickhouse endpoint is required")
	}
	if _, err := url.Parse(ep); err != nil {
		return nil, fmt.Errorf("invalid clickhouse endpoint: %w", err)
	}
	db := strings.TrimSpace(cfg.Database)
	if db == "" {
		db = "caliper"
	}
	if !reValidIdent.MatchString(db) {
		return nil, fmt.Errorf("invalid clickhouse database identifier: %q", db)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Store{
		endpoint: ep,
		database: db,
		username: cfg.Username,
		password: cfg.Password,
		client:   &http.Client{Timeout: timeout},
		meta:     cfg.Meta,
	}, nil
}

// Migrate creates the database and all 7 MergeTree tables idempotently.
func (s *Store) Migrate(ctx context.Context) error {
	if err := s.execDDL(ctx, "", fmt.Sprintf("CREATE DATABASE IF NOT EXISTS %s", s.database)); err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	for _, stmt := range splitStatements(schemaSQL) {
		if err := s.execDDL(ctx, s.database, stmt); err != nil {
			return fmt.Errorf("migrate table: %w", err)
		}
	}
	return nil
}

func splitStatements(raw string) []string {
	parts := strings.Split(raw, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		lines := strings.Split(p, "\n")
		kept := make([]string, 0, len(lines))
		for _, line := range lines {
			t := strings.TrimSpace(line)
			if t == "" || strings.HasPrefix(t, "--") {
				continue
			}
			kept = append(kept, line)
		}
		stmt := strings.TrimSpace(strings.Join(kept, "\n"))
		if stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}

func (s *Store) IngestBatch(ctx context.Context, batch *protocol.BatchEnvelope) (storage.IngestResult, error) {
	if err := batch.Validate(); err != nil {
		return storage.IngestResult{Rejected: len(batch.Events)}, err
	}

	nowMS := uint64(time.Now().UTC().UnixMilli())
	adapter := batch.Resource.Adapter
	if adapter == "" {
		adapter = "native"
	}

	tableRows := map[string][]any{}
	var res storage.IngestResult
	var minTS, maxTS int64

	for i := range batch.Events {
		ev := batch.Events[i]
		parsed, err := protocol.ValidateEvent(&ev)
		if err != nil {
			res.Rejected++
			continue
		}
		if minTS == 0 || ev.OccurredAtMS < minTS {
			minTS = ev.OccurredAtMS
		}
		if ev.OccurredAtMS > maxTS {
			maxTS = ev.OccurredAtMS
		}

		dayUTC := time.UnixMilli(ev.OccurredAtMS).UTC().Format("2006-01-02")
		scene := ev.Scene
		if scene == "" {
			scene = "unknown"
		}

		// Always record compact timeline row for audited UID/install troubleshooting.
		tableRows["caliper_session_timeline"] = append(tableRows["caliper_session_timeline"], map[string]any{
			"event_id":       ev.EventID,
			"ingested_at_ms": nowMS,
			"occurred_at_ms": uint64(ev.OccurredAtMS),
			"day_utc":        dayUTC,
			"app_id":         batch.Resource.AppID,
			"platform":       batch.Resource.Platform,
			"env":            batch.Resource.Env,
			"app_version":    batch.Resource.AppVersion,
			"build_number":   batch.Resource.BuildNumber,
			"adapter":        adapter,
			"install_hash":   batch.Resource.InstallHash,
			"user_hash":      batch.Resource.UserHash,
			"kind":           ev.Kind,
			"scene":          scene,
			"summary":        storage.SummarizeEvent(ev.Kind, parsed),
		})

		switch p := parsed.(type) {
		case protocol.StartupPayload:
			if p.ColdStart {
				var tot, nat, rt, ff contract.Histogram
				staged := uint8(0)
				if p.Result == "success" {
					tot[contract.DurationBucketIndex(p.TotalDurationMS)] = 1
					if p.NativeInitMS != nil {
						nat[contract.DurationBucketIndex(*p.NativeInitMS)] = 1
					}
					if p.RuntimeInitMS != nil {
						rt[contract.DurationBucketIndex(*p.RuntimeInitMS)] = 1
					}
					if p.FirstFrameMS != nil {
						ff[contract.DurationBucketIndex(*p.FirstFrameMS)] = 1
					}
					if p.NativeInitMS != nil && p.FirstFrameMS != nil {
						staged = 1
					}
				}
				tableRows["caliper_startup_events"] = append(tableRows["caliper_startup_events"], map[string]any{
					"event_id":            ev.EventID,
					"ingested_at_ms":      nowMS,
					"occurred_at_ms":      uint64(ev.OccurredAtMS),
					"day_utc":             dayUTC,
					"app_id":              batch.Resource.AppID,
					"platform":            batch.Resource.Platform,
					"env":                 batch.Resource.Env,
					"app_version":         batch.Resource.AppVersion,
					"build_number":        batch.Resource.BuildNumber,
					"adapter":             adapter,
					"policy_id":           ev.PolicyID,
					"contract_version":    ev.ContractVersion,
					"result":              p.Result,
					"staged":              staged,
					"total_buckets":       tot[:],
					"native_buckets":      nat[:],
					"runtime_buckets":     rt[:],
					"first_frame_buckets": ff[:],
				})
			}

		case protocol.FluencyWindowPayload:
			var fb contract.Histogram
			if len(p.FrameDurationBuckets) == contract.HistogramBucketCount {
				_ = fb.AddSlice(p.FrameDurationBuckets)
			}
			isValid := uint8(0)
			isZero := uint8(1)
			if p.FrameCount > 0 {
				isValid = 1
				isZero = 0
			}
			hasStall := uint8(0)
			stack := p.StallStack
			if len(stack) > 0 {
				hasStall = 1
			} else {
				stack = []string{}
			}
			tableRows["caliper_fluency_events"] = append(tableRows["caliper_fluency_events"], map[string]any{
				"event_id":                ev.EventID,
				"ingested_at_ms":          nowMS,
				"occurred_at_ms":          uint64(ev.OccurredAtMS),
				"day_utc":                 dayUTC,
				"app_id":                  batch.Resource.AppID,
				"platform":                batch.Resource.Platform,
				"env":                     batch.Resource.Env,
				"app_version":             batch.Resource.AppVersion,
				"build_number":            batch.Resource.BuildNumber,
				"adapter":                 adapter,
				"scene":                   scene,
				"algorithm":               p.Algorithm,
				"policy_id":               ev.PolicyID,
				"frame_count":             p.FrameCount,
				"jank_frame_count":        p.JankFrameCount,
				"severe_jank_frame_count": p.SevereJankFrameCount,
				"is_valid_window":         isValid,
				"is_zero_frame_window":    isZero,
				"has_stall_stack":         hasStall,
				"frame_buckets":           fb[:],
				"stall_stack":             stack,
			})

		case protocol.ResourceWindowPayload:
			var mb contract.Histogram
			mb[contract.MemoryBucketIndex(p.MemoryPeakKiB/1024.0)] = 1
			tableRows["caliper_resource_events"] = append(tableRows["caliper_resource_events"], map[string]any{
				"event_id":            ev.EventID,
				"ingested_at_ms":      nowMS,
				"occurred_at_ms":      uint64(ev.OccurredAtMS),
				"day_utc":             dayUTC,
				"app_id":              batch.Resource.AppID,
				"platform":            batch.Resource.Platform,
				"env":                 batch.Resource.Env,
				"app_version":         batch.Resource.AppVersion,
				"build_number":        batch.Resource.BuildNumber,
				"adapter":             adapter,
				"scene":               scene,
				"policy_id":           ev.PolicyID,
				"event_kind":          protocol.KindResourceWindow,
				"memory_kind":         p.MemoryKind,
				"cpu_pct":             p.CPUPct,
				"memory_peak_buckets": mb[:],
				"js_longtask_count":   0,
				"js_blocking_ms":      0.0,
				"js_max_task_ms":      0.0,
			})

		case protocol.JSLongTaskPayload:
			var empty contract.Histogram
			tableRows["caliper_resource_events"] = append(tableRows["caliper_resource_events"], map[string]any{
				"event_id":            ev.EventID,
				"ingested_at_ms":      nowMS,
				"occurred_at_ms":      uint64(ev.OccurredAtMS),
				"day_utc":             dayUTC,
				"app_id":              batch.Resource.AppID,
				"platform":            batch.Resource.Platform,
				"env":                 batch.Resource.Env,
				"app_version":         batch.Resource.AppVersion,
				"build_number":        batch.Resource.BuildNumber,
				"adapter":             adapter,
				"scene":               scene,
				"policy_id":           ev.PolicyID,
				"event_kind":          protocol.KindJSLongTask,
				"memory_kind":         "",
				"cpu_pct":             0.0,
				"memory_peak_buckets": empty[:],
				"js_longtask_count":   p.LongTaskCount,
				"js_blocking_ms":      p.TotalBlockingMS,
				"js_max_task_ms":      p.MaxTaskMS,
			})

		case protocol.NetworkSummaryPayload:
			var db contract.Histogram
			_ = db.AddSlice(p.DurationBuckets)
			tableRows["caliper_network_summary_events"] = append(tableRows["caliper_network_summary_events"], map[string]any{
				"event_id":         ev.EventID,
				"ingested_at_ms":   nowMS,
				"occurred_at_ms":   uint64(ev.OccurredAtMS),
				"day_utc":          dayUTC,
				"app_id":           batch.Resource.AppID,
				"platform":         batch.Resource.Platform,
				"env":              batch.Resource.Env,
				"app_version":      batch.Resource.AppVersion,
				"build_number":     batch.Resource.BuildNumber,
				"adapter":          adapter,
				"scene":            scene,
				"policy_id":        ev.PolicyID,
				"call_count":       p.CallCount,
				"fail_http":        p.FailureCounts.HTTP,
				"fail_business":    p.FailureCounts.Business,
				"fail_parse":       p.FailureCounts.Parse,
				"fail_network":     p.FailureCounts.Network,
				"fail_timeout":     p.FailureCounts.Timeout,
				"duration_buckets": db[:],
				"duration_sum_ms":  p.DurationSumMS,
			})

		case protocol.NetworkTransportPayload:
			var dns, tcp, tls, ttfb, body, tot contract.Histogram
			if p.DNSMS != nil {
				dns[contract.DurationBucketIndex(*p.DNSMS)] = 1
			}
			if p.TCPMS != nil {
				tcp[contract.DurationBucketIndex(*p.TCPMS)] = 1
			}
			if p.TLSMS != nil {
				tls[contract.DurationBucketIndex(*p.TLSMS)] = 1
			}
			if p.TTFBMS != nil {
				ttfb[contract.DurationBucketIndex(*p.TTFBMS)] = 1
			}
			if p.BodyMS != nil {
				body[contract.DurationBucketIndex(*p.BodyMS)] = 1
			}
			tot[contract.DurationBucketIndex(p.TotalMS)] = 1
			reused := uint8(0)
			if p.ConnectionReused {
				reused = 1
			}
			tableRows["caliper_network_transport_events"] = append(tableRows["caliper_network_transport_events"], map[string]any{
				"event_id":          ev.EventID,
				"ingested_at_ms":    nowMS,
				"occurred_at_ms":    uint64(ev.OccurredAtMS),
				"day_utc":           dayUTC,
				"app_id":            batch.Resource.AppID,
				"platform":          batch.Resource.Platform,
				"env":               batch.Resource.Env,
				"app_version":       batch.Resource.AppVersion,
				"build_number":      batch.Resource.BuildNumber,
				"adapter":           adapter,
				"scene":             scene,
				"policy_id":         ev.PolicyID,
				"operation":         p.Operation,
				"method":            p.Method,
				"status_code":       p.StatusCode,
				"result":            p.Result,
				"connection_reused": reused,
				"protocol":          p.Protocol,
				"network_type":      p.NetworkType,
				"install_hash":      batch.Resource.InstallHash,
				"user_hash":         batch.Resource.UserHash,
				"total_ms":          p.TotalMS,
				"dns_buckets":       dns[:],
				"tcp_buckets":       tcp[:],
				"tls_buckets":       tls[:],
				"ttfb_buckets":      ttfb[:],
				"body_buckets":      body[:],
				"total_buckets":     tot[:],
			})

		case protocol.CustomScenePayload:
			var dh contract.Histogram
			dur := 0.0
			if p.DurationMS != nil {
				dur = *p.DurationMS
				dh[contract.DurationBucketIndex(dur)] = 1
			}
			stagesJSON, _ := json.Marshal(p.Stages)
			tableRows["caliper_custom_scene_events"] = append(tableRows["caliper_custom_scene_events"], map[string]any{
				"event_id":         ev.EventID,
				"ingested_at_ms":   nowMS,
				"occurred_at_ms":   uint64(ev.OccurredAtMS),
				"day_utc":          dayUTC,
				"app_id":           batch.Resource.AppID,
				"platform":         batch.Resource.Platform,
				"env":              batch.Resource.Env,
				"app_version":      batch.Resource.AppVersion,
				"build_number":     batch.Resource.BuildNumber,
				"adapter":          adapter,
				"policy_id":        ev.PolicyID,
				"journey_name":     p.JourneyName,
				"status":           p.Status,
				"error_code":       p.ErrorCode,
				"duration_ms":      dur,
				"duration_buckets": dh[:],
				"stages_json":      string(stagesJSON),
			})

		case protocol.StabilityPayload:
			framesJSON, _ := json.Marshal(p.Frames)
			crumbsJSON, _ := json.Marshal(p.Breadcrumbs)
			fatal := uint8(0)
			if p.Fatal {
				fatal = 1
			}
			tableRows["caliper_stability_events"] = append(tableRows["caliper_stability_events"], map[string]any{
				"event_id":         ev.EventID,
				"ingested_at_ms":   nowMS,
				"occurred_at_ms":   uint64(ev.OccurredAtMS),
				"day_utc":          dayUTC,
				"app_id":           batch.Resource.AppID,
				"platform":         batch.Resource.Platform,
				"env":              batch.Resource.Env,
				"app_version":      batch.Resource.AppVersion,
				"build_number":     batch.Resource.BuildNumber,
				"adapter":          adapter,
				"install_hash":     batch.Resource.InstallHash,
				"user_hash":        batch.Resource.UserHash,
				"fingerprint":      p.Fingerprint,
				"kind":             ev.Kind,
				"title":            p.Title,
				"fatal":            fatal,
				"exception_type":   p.ExceptionType,
				"exit_reason":      p.ExitReason,
				"frames_json":      string(framesJSON),
				"breadcrumbs_json": string(crumbsJSON),
			})

		case protocol.CPUProfilePayload:
			stacksJSON, _ := json.Marshal(p.Stacks)
			tableRows["caliper_cpu_profiles"] = append(tableRows["caliper_cpu_profiles"], map[string]any{
				"profile_id":         ev.EventID,
				"ingested_at_ms":     nowMS,
				"occurred_at_ms":     uint64(ev.OccurredAtMS),
				"day_utc":            dayUTC,
				"app_id":             batch.Resource.AppID,
				"platform":           batch.Resource.Platform,
				"env":                batch.Resource.Env,
				"app_version":        batch.Resource.AppVersion,
				"build_number":       batch.Resource.BuildNumber,
				"scene":              scene,
				"runtime":            p.Runtime,
				"thread_name":        p.ThreadName,
				"sample_interval_ms": p.SampleIntervalMS,
				"duration_ms":        uint64(p.DurationMS),
				"total_samples":      uint64(p.TotalSamples),
				"stacks_json":        string(stacksJSON),
			})

		case protocol.HeapSnapshotPayload:
			domJSON, _ := json.Marshal(p.Dominators)
			tableRows["caliper_heap_snapshots"] = append(tableRows["caliper_heap_snapshots"], map[string]any{
				"snapshot_id":         ev.EventID,
				"ingested_at_ms":      nowMS,
				"occurred_at_ms":      uint64(ev.OccurredAtMS),
				"day_utc":             dayUTC,
				"app_id":              batch.Resource.AppID,
				"platform":            batch.Resource.Platform,
				"env":                 batch.Resource.Env,
				"app_version":         batch.Resource.AppVersion,
				"build_number":        batch.Resource.BuildNumber,
				"scene":               scene,
				"trigger_reason":      p.TriggerReason,
				"memory_kind":         p.MemoryKind,
				"total_allocated_kib": p.TotalAllocatedKiB,
				"total_retained_kib":  p.TotalRetainedKiB,
				"memory_limit_kib":    p.MemoryLimitKiB,
				"dominators_json":     string(domJSON),
			})
		}

		res.Accepted++
	}

	if res.Accepted == 0 {
		return res, nil
	}

	for table, rows := range tableRows {
		if len(rows) == 0 {
			continue
		}
		if table != "caliper_session_timeline" {
			for _, row := range rows {
				if m, ok := row.(map[string]any); ok {
					m["device_model"] = batch.Resource.DeviceModel
					m["os_version"] = batch.Resource.OSVersion
				}
			}
		}
		if err := s.insertJSONEachRow(ctx, table, rows); err != nil {
			return res, fmt.Errorf("clickhouse insert %s: %w", table, err)
		}
	}
	s.totalWrite.Add(int64(res.Accepted))

	if s.meta != nil {
		_ = s.meta.RegisterRelease(ctx, storage.ReleaseRecord{
			AppID:       batch.Resource.AppID,
			Platform:    batch.Resource.Platform,
			Env:         batch.Resource.Env,
			AppVersion:  batch.Resource.AppVersion,
			BuildNumber: batch.Resource.BuildNumber,
			GitSHA:      batch.Resource.GitSHA,
			SymbolID:    batch.Resource.SymbolID,
			Adapter:     adapter,
			FirstSeenMS: minTS,
			LastSeenMS:  maxTS,
			EventCount:  int64(res.Accepted),
		})
	}
	return res, nil
}

func (s *Store) QueryStartup(ctx context.Context, f storage.QueryFilter) (storage.StartupAggregate, error) {
	where := buildWhereClause(f, false, false)
	q := fmt.Sprintf(`
SELECT
    countIf(result = 'success') AS success_count,
    countIf(result = 'aborted') AS aborted_count,
    countIf(result = 'timeout') AS timeout_count,
    sum(staged) AS staged_count,
    sumForEach(total_buckets) AS total_buckets,
    sumForEach(native_buckets) AS native_buckets,
    sumForEach(runtime_buckets) AS runtime_buckets,
    sumForEach(first_frame_buckets) AS first_frame_buckets,
    groupUniqArray(policy_id) AS policy_ids
FROM %s.caliper_startup_events FINAL
WHERE %s
FORMAT JSON`, s.database, where)

	var resp chJSONResp[struct {
		SuccessCount      flexInt64   `json:"success_count"`
		AbortedCount      flexInt64   `json:"aborted_count"`
		TimeoutCount      flexInt64   `json:"timeout_count"`
		StagedCount       flexInt64   `json:"staged_count"`
		TotalBuckets      []flexInt64 `json:"total_buckets"`
		NativeBuckets     []flexInt64 `json:"native_buckets"`
		RuntimeBuckets    []flexInt64 `json:"runtime_buckets"`
		FirstFrameBuckets []flexInt64 `json:"first_frame_buckets"`
		PolicyIDs         []string    `json:"policy_ids"`
	}]
	if err := s.queryJSON(ctx, q, &resp); err != nil {
		return storage.StartupAggregate{}, err
	}
	agg := storage.StartupAggregate{PolicyIDs: map[string]struct{}{}}
	if len(resp.Data) == 0 {
		return agg, nil
	}
	row := resp.Data[0]
	agg.SuccessCount = int64(row.SuccessCount)
	agg.AbortedCount = int64(row.AbortedCount)
	agg.TimeoutCount = int64(row.TimeoutCount)
	agg.StagedCount = int64(row.StagedCount)
	agg.TotalHist = toHistogram(row.TotalBuckets)
	agg.NativeInitHist = toHistogram(row.NativeBuckets)
	agg.RuntimeHist = toHistogram(row.RuntimeBuckets)
	agg.FirstFrameHist = toHistogram(row.FirstFrameBuckets)
	for _, pid := range row.PolicyIDs {
		if pid != "" {
			agg.PolicyIDs[pid] = struct{}{}
		}
	}
	return agg, nil
}

func (s *Store) QueryFluency(ctx context.Context, f storage.QueryFilter) ([]storage.FluencyAggregate, error) {
	where := buildWhereClause(f, true, true)
	q := fmt.Sprintf(`
SELECT
    scene,
    algorithm,
    count() AS total_windows,
    sum(is_valid_window) AS valid_windows,
    sum(is_zero_frame_window) AS zero_frame_windows,
    sum(frame_count) AS total_frames,
    sum(jank_frame_count) AS jank_frames,
    sum(severe_jank_frame_count) AS severe_jank_frames,
    sum(has_stall_stack) AS stall_window_count,
    sumForEach(frame_buckets) AS frame_buckets,
    groupArrayIf(5)(stall_stack, has_stall_stack = 1) AS sample_stall_stacks,
    groupUniqArray(policy_id) AS policy_ids
FROM %s.caliper_fluency_events FINAL
WHERE %s
GROUP BY scene, algorithm
ORDER BY total_windows DESC, scene ASC
FORMAT JSON`, s.database, where)

	var resp chJSONResp[struct {
		Scene             string      `json:"scene"`
		Algorithm         string      `json:"algorithm"`
		TotalWindows      flexInt64   `json:"total_windows"`
		ValidWindows      flexInt64   `json:"valid_windows"`
		ZeroFrameWindows  flexInt64   `json:"zero_frame_windows"`
		TotalFrames       flexInt64   `json:"total_frames"`
		JankFrames        flexInt64   `json:"jank_frames"`
		SevereJankFrames  flexInt64   `json:"severe_jank_frames"`
		StallWindowCount  flexInt64   `json:"stall_window_count"`
		FrameBuckets      []flexInt64 `json:"frame_buckets"`
		SampleStallStacks [][]string  `json:"sample_stall_stacks"`
		PolicyIDs         []string    `json:"policy_ids"`
	}]
	if err := s.queryJSON(ctx, q, &resp); err != nil {
		return nil, err
	}
	out := make([]storage.FluencyAggregate, 0, len(resp.Data))
	for _, r := range resp.Data {
		pids := map[string]struct{}{}
		for _, pid := range r.PolicyIDs {
			if pid != "" {
				pids[pid] = struct{}{}
			}
		}
		out = append(out, storage.FluencyAggregate{
			Scene:             r.Scene,
			Algorithm:         r.Algorithm,
			TotalWindows:      int64(r.TotalWindows),
			ValidWindows:      int64(r.ValidWindows),
			ZeroFrameWindows:  int64(r.ZeroFrameWindows),
			TotalFrames:       int64(r.TotalFrames),
			JankFrames:        int64(r.JankFrames),
			SevereJankFrames:  int64(r.SevereJankFrames),
			FrameDurationHist: toHistogram(r.FrameBuckets),
			StallWindowCount:  int64(r.StallWindowCount),
			SampleStallStacks: r.SampleStallStacks,
			PolicyIDs:         pids,
		})
	}
	return out, nil
}

func (s *Store) QueryResources(ctx context.Context, f storage.QueryFilter) ([]storage.ResourceAggregate, error) {
	where := buildWhereClause(f, true, false)
	q := fmt.Sprintf(`
SELECT
    scene,
    anyIf(memory_kind, memory_kind != '') AS memory_kind,
    countIf(event_kind = 'resource_window') AS resource_windows,
    sumIf(cpu_pct, event_kind = 'resource_window') AS cpu_pct_sum,
    sumForEachIf(memory_peak_buckets, event_kind = 'resource_window') AS memory_peak_buckets,
    countIf(event_kind = 'js_longtask_window') AS js_longtask_windows,
    sumIf(js_longtask_count, event_kind = 'js_longtask_window') AS js_longtask_count,
    sumIf(js_blocking_ms, event_kind = 'js_longtask_window') AS js_blocking_sum_ms,
    maxIf(js_max_task_ms, event_kind = 'js_longtask_window') AS js_max_task_ms,
    groupUniqArray(policy_id) AS policy_ids
FROM %s.caliper_resource_events FINAL
WHERE %s
GROUP BY scene
ORDER BY scene ASC
FORMAT JSON`, s.database, where)

	var resp chJSONResp[struct {
		Scene             string      `json:"scene"`
		MemoryKind        string      `json:"memory_kind"`
		ResourceWindows   flexInt64   `json:"resource_windows"`
		CPUPctSum         float64     `json:"cpu_pct_sum"`
		MemoryPeakBuckets []flexInt64 `json:"memory_peak_buckets"`
		JSLongTaskWindows flexInt64   `json:"js_longtask_windows"`
		JSLongTaskCount   flexInt64   `json:"js_longtask_count"`
		JSBlockingSumMS   float64     `json:"js_blocking_sum_ms"`
		JSMaxTaskMS       float64     `json:"js_max_task_ms"`
		PolicyIDs         []string    `json:"policy_ids"`
	}]
	if err := s.queryJSON(ctx, q, &resp); err != nil {
		return nil, err
	}
	out := make([]storage.ResourceAggregate, 0, len(resp.Data))
	for _, r := range resp.Data {
		pids := map[string]struct{}{}
		for _, pid := range r.PolicyIDs {
			if pid != "" {
				pids[pid] = struct{}{}
			}
		}
		out = append(out, storage.ResourceAggregate{
			Scene:             r.Scene,
			MemoryKind:        r.MemoryKind,
			ResourceWindows:   int64(r.ResourceWindows),
			CPUPctSum:         r.CPUPctSum,
			MemoryPeakHistMiB: toHistogram(r.MemoryPeakBuckets),
			JSLongTaskWindows: int64(r.JSLongTaskWindows),
			JSLongTaskCount:   int64(r.JSLongTaskCount),
			JSBlockingSumMS:   r.JSBlockingSumMS,
			JSMaxTaskMS:       r.JSMaxTaskMS,
			PolicyIDs:         pids,
		})
	}
	return out, nil
}

func (s *Store) QueryNetwork(ctx context.Context, f storage.QueryFilter) (storage.NetworkAggregate, error) {
	where := buildWhereClause(f, true, false)
	out := storage.NetworkAggregate{
		TransportSamples: map[string]*storage.TransportOpAggregate{},
		PolicyIDs:        map[string]struct{}{},
	}

	qSummary := fmt.Sprintf(`
SELECT
    count() AS summary_windows,
    sum(call_count) AS total_calls,
    sum(fail_http) AS fail_http,
    sum(fail_business) AS fail_business,
    sum(fail_parse) AS fail_parse,
    sum(fail_network) AS fail_network,
    sum(fail_timeout) AS fail_timeout,
    sumForEach(duration_buckets) AS duration_buckets,
    sum(duration_sum_ms) AS duration_sum_ms,
    groupUniqArray(policy_id) AS policy_ids
FROM %s.caliper_network_summary_events FINAL
WHERE %s
FORMAT JSON`, s.database, where)

	var sumResp chJSONResp[struct {
		SummaryWindows  flexInt64   `json:"summary_windows"`
		TotalCalls      flexInt64   `json:"total_calls"`
		FailHTTP        flexInt64   `json:"fail_http"`
		FailBusiness    flexInt64   `json:"fail_business"`
		FailParse       flexInt64   `json:"fail_parse"`
		FailNetwork     flexInt64   `json:"fail_network"`
		FailTimeout     flexInt64   `json:"fail_timeout"`
		DurationBuckets []flexInt64 `json:"duration_buckets"`
		DurationSumMS   float64     `json:"duration_sum_ms"`
		PolicyIDs       []string    `json:"policy_ids"`
	}]
	if err := s.queryJSON(ctx, qSummary, &sumResp); err != nil {
		return out, err
	}
	if len(sumResp.Data) > 0 {
		r := sumResp.Data[0]
		out.SummaryWindows = int64(r.SummaryWindows)
		out.TotalCalls = int64(r.TotalCalls)
		out.FailureCounts = protocol.NetworkFailureCounts{
			HTTP:     int64(r.FailHTTP),
			Business: int64(r.FailBusiness),
			Parse:    int64(r.FailParse),
			Network:  int64(r.FailNetwork),
			Timeout:  int64(r.FailTimeout),
		}
		out.DurationHist = toHistogram(r.DurationBuckets)
		out.DurationSumMS = r.DurationSumMS
		for _, pid := range r.PolicyIDs {
			if pid != "" {
				out.PolicyIDs[pid] = struct{}{}
			}
		}
	}

	qTransport := fmt.Sprintf(`
SELECT
    operation,
    method,
    count() AS total_count,
    countIf(result = 'success') AS success_cnt,
    countIf(result = 'failed') AS failed_cnt,
    countIf(result = 'timeout') AS timeout_cnt,
    sum(connection_reused) AS reused_cnt,
    sumForEach(dns_buckets) AS dns_buckets,
    sumForEach(tcp_buckets) AS tcp_buckets,
    sumForEach(tls_buckets) AS tls_buckets,
    sumForEach(ttfb_buckets) AS ttfb_buckets,
    sumForEach(body_buckets) AS body_buckets,
    sumForEach(total_buckets) AS total_buckets
FROM %s.caliper_network_transport_events FINAL
WHERE %s
GROUP BY operation, method
ORDER BY total_count DESC
FORMAT JSON`, s.database, where)

	var trResp chJSONResp[struct {
		Operation    string      `json:"operation"`
		Method       string      `json:"method"`
		TotalCount   flexInt64   `json:"total_count"`
		SuccessCnt   flexInt64   `json:"success_cnt"`
		FailedCnt    flexInt64   `json:"failed_cnt"`
		TimeoutCnt   flexInt64   `json:"timeout_cnt"`
		ReusedCnt    flexInt64   `json:"reused_cnt"`
		DNSBuckets   []flexInt64 `json:"dns_buckets"`
		TCPBuckets   []flexInt64 `json:"tcp_buckets"`
		TLSBuckets   []flexInt64 `json:"tls_buckets"`
		TTFBBuckets  []flexInt64 `json:"ttfb_buckets"`
		BodyBuckets  []flexInt64 `json:"body_buckets"`
		TotalBuckets []flexInt64 `json:"total_buckets"`
	}]
	if err := s.queryJSON(ctx, qTransport, &trResp); err != nil {
		return out, err
	}
	for _, r := range trResp.Data {
		key := r.Method + " " + r.Operation
		out.TransportSamples[key] = &storage.TransportOpAggregate{
			Operation:  r.Operation,
			Method:     r.Method,
			TotalCount: int64(r.TotalCount),
			SuccessCnt: int64(r.SuccessCnt),
			FailedCnt:  int64(r.FailedCnt),
			TimeoutCnt: int64(r.TimeoutCnt),
			ReusedCnt:  int64(r.ReusedCnt),
			DNSHist:    toHistogram(r.DNSBuckets),
			TCPHist:    toHistogram(r.TCPBuckets),
			TLSHist:    toHistogram(r.TLSBuckets),
			TTFBHist:   toHistogram(r.TTFBBuckets),
			BodyHist:   toHistogram(r.BodyBuckets),
			TotalHist:  toHistogram(r.TotalBuckets),
		}
	}
	return out, nil
}

func (s *Store) QueryNetworkSamples(ctx context.Context, f storage.QueryFilter, operation string, onlyIssues bool, limit int) ([]storage.NetworkTransportSample, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := buildWhereClause(f, true, false)
	if operation != "" {
		where += " AND operation = " + quoteSQLString(operation)
	}
	if onlyIssues {
		where += " AND (result != 'success' OR status_code >= 400 OR total_ms >= 800)"
	}
	q := fmt.Sprintf(`
SELECT event_id, occurred_at_ms, app_id, platform, env, app_version, build_number, adapter, scene,
       device_model, os_version, install_hash, user_hash, operation, method, status_code, result,
       connection_reused, protocol, network_type, total_ms
FROM caliper_network_transport_events FINAL
WHERE %s
ORDER BY occurred_at_ms DESC
LIMIT %d FORMAT JSON`, where, limit)

	var resp chJSONResp[struct {
		EventID          string    `json:"event_id"`
		OccurredAtMS     flexInt64 `json:"occurred_at_ms"`
		AppID            string    `json:"app_id"`
		Platform         string    `json:"platform"`
		Env              string    `json:"env"`
		AppVersion       string    `json:"app_version"`
		BuildNumber      string    `json:"build_number"`
		Adapter          string    `json:"adapter"`
		Scene            string    `json:"scene"`
		DeviceModel      string    `json:"device_model"`
		OSVersion        string    `json:"os_version"`
		InstallHash      string    `json:"install_hash"`
		UserHash         string    `json:"user_hash"`
		Operation        string    `json:"operation"`
		Method           string    `json:"method"`
		StatusCode       int       `json:"status_code"`
		Result           string    `json:"result"`
		ConnectionReused uint8     `json:"connection_reused"`
		Protocol         string    `json:"protocol"`
		NetworkType      string    `json:"network_type"`
		TotalMS          float64   `json:"total_ms"`
	}]
	if err := s.queryJSON(ctx, q, &resp); err != nil {
		return nil, err
	}
	out := make([]storage.NetworkTransportSample, 0, len(resp.Data))
	for _, r := range resp.Data {
		out = append(out, storage.NetworkTransportSample{
			EventID:          r.EventID,
			OccurredAtMS:     int64(r.OccurredAtMS),
			AppID:            r.AppID,
			Platform:         r.Platform,
			Env:              r.Env,
			AppVersion:       r.AppVersion,
			BuildNumber:      r.BuildNumber,
			Adapter:          r.Adapter,
			Scene:            r.Scene,
			DeviceModel:      r.DeviceModel,
			OSVersion:        r.OSVersion,
			InstallHash:      r.InstallHash,
			UserHash:         r.UserHash,
			Operation:        r.Operation,
			Method:           r.Method,
			StatusCode:       r.StatusCode,
			Result:           r.Result,
			ConnectionReused: r.ConnectionReused != 0,
			Protocol:         r.Protocol,
			NetworkType:      r.NetworkType,
			TotalMS:          r.TotalMS,
		})
	}
	return out, nil
}

func (s *Store) QueryCustomScenes(ctx context.Context, f storage.QueryFilter) ([]storage.CustomSceneAggregate, error) {
	where := buildWhereClause(f, false, false)
	q := fmt.Sprintf(`
SELECT
    journey_name,
    count() AS total_count,
    countIf(status = 'success') AS success_count,
    countIf(status = 'failure') AS failure_count,
    countIf(status = 'timeout') AS timeout_count,
    countIf(status = 'cancelled') AS cancelled_count,
    sumForEach(duration_buckets) AS duration_buckets
FROM caliper_custom_scene_events FINAL
WHERE %s
GROUP BY journey_name
ORDER BY total_count DESC
LIMIT 100 FORMAT JSON`, where)

	var resp chJSONResp[struct {
		JourneyName     string      `json:"journey_name"`
		TotalCount      flexInt64   `json:"total_count"`
		SuccessCount    flexInt64   `json:"success_count"`
		FailureCount    flexInt64   `json:"failure_count"`
		TimeoutCount    flexInt64   `json:"timeout_count"`
		CancelledCount  flexInt64   `json:"cancelled_count"`
		DurationBuckets []flexInt64 `json:"duration_buckets"`
	}]
	if err := s.queryJSON(ctx, q, &resp); err != nil {
		return nil, err
	}
	out := make([]storage.CustomSceneAggregate, 0, len(resp.Data))
	for _, r := range resp.Data {
		out = append(out, storage.CustomSceneAggregate{
			JourneyName:    r.JourneyName,
			TotalCount:     int64(r.TotalCount),
			SuccessCount:   int64(r.SuccessCount),
			FailureCount:   int64(r.FailureCount),
			TimeoutCount:   int64(r.TimeoutCount),
			CancelledCount: int64(r.CancelledCount),
			DurationHist:   toHistogram(r.DurationBuckets),
			StageHists:     map[string]contract.Histogram{},
			ErrorCodes:     map[string]int64{},
			PolicyIDs:      map[string]struct{}{},
		})
	}
	return out, nil
}

func (s *Store) QueryIssues(ctx context.Context, f storage.QueryFilter, limit int) ([]storage.IssueSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := buildWhereClause(f, false, false)
	q := fmt.Sprintf(`
SELECT
    fingerprint,
    any(kind) AS kind,
    any(title) AS title,
    max(fatal) AS fatal,
    any(exception_type) AS exception_type,
    any(exit_reason) AS exit_reason,
    count() AS event_count,
    uniqExact(install_hash) AS affected_installs,
    min(occurred_at_ms) AS first_seen_ms,
    max(occurred_at_ms) AS last_seen_ms,
    groupUniqArray(concat(app_version, ' (', build_number, ')')) AS releases,
    argMax(frames_json, occurred_at_ms) AS frames_json,
    argMax(breadcrumbs_json, occurred_at_ms) AS breadcrumbs_json
FROM %s.caliper_stability_events FINAL
WHERE %s
GROUP BY fingerprint
ORDER BY event_count DESC, last_seen_ms DESC
LIMIT %d
FORMAT JSON`, s.database, where, limit)

	return s.decodeIssueRows(ctx, q)
}

func (s *Store) GetIssue(ctx context.Context, fingerprint string, f storage.QueryFilter) (*storage.IssueSummary, bool, error) {
	where := buildWhereClause(f, false, false) + " AND fingerprint = " + quoteSQLString(fingerprint)
	q := fmt.Sprintf(`
SELECT
    fingerprint,
    any(kind) AS kind,
    any(title) AS title,
    max(fatal) AS fatal,
    any(exception_type) AS exception_type,
    any(exit_reason) AS exit_reason,
    count() AS event_count,
    uniqExact(install_hash) AS affected_installs,
    min(occurred_at_ms) AS first_seen_ms,
    max(occurred_at_ms) AS last_seen_ms,
    groupUniqArray(concat(app_version, ' (', build_number, ')')) AS releases,
    argMax(frames_json, occurred_at_ms) AS frames_json,
    argMax(breadcrumbs_json, occurred_at_ms) AS breadcrumbs_json
FROM %s.caliper_stability_events FINAL
WHERE %s
GROUP BY fingerprint
LIMIT 1
FORMAT JSON`, s.database, where)

	rows, err := s.decodeIssueRows(ctx, q)
	if err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	return &rows[0], true, nil
}

func (s *Store) decodeIssueRows(ctx context.Context, q string) ([]storage.IssueSummary, error) {
	var resp chJSONResp[struct {
		Fingerprint      string    `json:"fingerprint"`
		Kind             string    `json:"kind"`
		Title            string    `json:"title"`
		Fatal            flexInt64 `json:"fatal"`
		ExceptionType    string    `json:"exception_type"`
		ExitReason       string    `json:"exit_reason"`
		EventCount       flexInt64 `json:"event_count"`
		AffectedInstalls flexInt64 `json:"affected_installs"`
		FirstSeenMS      flexInt64 `json:"first_seen_ms"`
		LastSeenMS       flexInt64 `json:"last_seen_ms"`
		Releases         []string  `json:"releases"`
		FramesJSON       string    `json:"frames_json"`
		BreadcrumbsJSON  string    `json:"breadcrumbs_json"`
	}]
	if err := s.queryJSON(ctx, q, &resp); err != nil {
		return nil, err
	}
	out := make([]storage.IssueSummary, 0, len(resp.Data))
	for _, r := range resp.Data {
		var frames []protocol.StackFrame
		var crumbs []protocol.Breadcrumb
		if r.FramesJSON != "" {
			_ = json.Unmarshal([]byte(r.FramesJSON), &frames)
		}
		if r.BreadcrumbsJSON != "" {
			_ = json.Unmarshal([]byte(r.BreadcrumbsJSON), &crumbs)
		}
		out = append(out, storage.IssueSummary{
			Fingerprint:      r.Fingerprint,
			Kind:             r.Kind,
			Title:            r.Title,
			Fatal:            r.Fatal > 0,
			ExceptionType:    r.ExceptionType,
			ExitReason:       r.ExitReason,
			EventCount:       int64(r.EventCount),
			AffectedInstalls: int64(r.AffectedInstalls),
			FirstSeenMS:      int64(r.FirstSeenMS),
			LastSeenMS:       int64(r.LastSeenMS),
			Releases:         r.Releases,
			SampleFrames:     frames,
			SampleCrumbs:     crumbs,
		})
	}
	return out, nil
}

func (s *Store) QuerySessionTimeline(ctx context.Context, f storage.SessionLookupFilter) ([]storage.SessionTimelineEvent, error) {
	if f.InstallHash == "" && f.UserHash == "" {
		return nil, errors.New("install_hash or user_hash is required")
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	conds := []string{"1 = 1"}
	if f.AppID != "" {
		conds = append(conds, "app_id = "+quoteSQLString(f.AppID))
	}
	if f.Platform != "" {
		conds = append(conds, "platform = "+quoteSQLString(f.Platform))
	}
	if f.InstallHash != "" {
		conds = append(conds, "install_hash = "+quoteSQLString(f.InstallHash))
	}
	if f.UserHash != "" {
		conds = append(conds, "user_hash = "+quoteSQLString(f.UserHash))
	}
	q := fmt.Sprintf(`
SELECT
    event_id, occurred_at_ms, app_id, platform, env, app_version, build_number,
    adapter, install_hash, user_hash, kind, scene, summary
FROM %s.caliper_session_timeline FINAL
WHERE %s
ORDER BY occurred_at_ms DESC, event_id ASC
LIMIT %d
FORMAT JSON`, s.database, strings.Join(conds, " AND "), limit)

	var resp chJSONResp[struct {
		EventID      string    `json:"event_id"`
		OccurredAtMS flexInt64 `json:"occurred_at_ms"`
		AppID        string    `json:"app_id"`
		Platform     string    `json:"platform"`
		Env          string    `json:"env"`
		AppVersion   string    `json:"app_version"`
		BuildNumber  string    `json:"build_number"`
		Adapter      string    `json:"adapter"`
		InstallHash  string    `json:"install_hash"`
		UserHash     string    `json:"user_hash"`
		Kind         string    `json:"kind"`
		Scene        string    `json:"scene"`
		Summary      string    `json:"summary"`
	}]
	if err := s.queryJSON(ctx, q, &resp); err != nil {
		return nil, err
	}
	out := make([]storage.SessionTimelineEvent, 0, len(resp.Data))
	for _, r := range resp.Data {
		out = append(out, storage.SessionTimelineEvent{
			EventID:      r.EventID,
			OccurredAtMS: int64(r.OccurredAtMS),
			AppID:        r.AppID,
			Platform:     r.Platform,
			Env:          r.Env,
			AppVersion:   r.AppVersion,
			BuildNumber:  r.BuildNumber,
			Adapter:      r.Adapter,
			InstallHash:  r.InstallHash,
			UserHash:     r.UserHash,
			Kind:         r.Kind,
			Scene:        r.Scene,
			Summary:      r.Summary,
		})
	}
	return out, nil
}

func (s *Store) QueryDailyTrends(ctx context.Context, f storage.QueryFilter) ([]storage.DailyTrendPoint, error) {
	where := buildWhereClause(f, false, false)
	q := fmt.Sprintf(`
SELECT
    toString(day_utc) AS day_utc,
    topK(1)(app_version)[1] AS dominant_version,
    countIf(result = 'success') AS startup_samples,
    sumForEach(total_buckets) AS total_buckets
FROM %s.caliper_startup_events FINAL
WHERE %s
GROUP BY day_utc
ORDER BY day_utc ASC
FORMAT JSON`, s.database, where)

	var resp chJSONResp[struct {
		DayUTC          string      `json:"day_utc"`
		DominantVersion string      `json:"dominant_version"`
		StartupSamples  flexInt64   `json:"startup_samples"`
		TotalBuckets    []flexInt64 `json:"total_buckets"`
	}]
	if err := s.queryJSON(ctx, q, &resp); err != nil {
		return nil, err
	}
	out := make([]storage.DailyTrendPoint, 0, len(resp.Data))
	for _, r := range resp.Data {
		h := toHistogram(r.TotalBuckets)
		pt := storage.DailyTrendPoint{
			DayUTC:          r.DayUTC,
			DominantVersion: r.DominantVersion,
			StartupSamples:  int64(r.StartupSamples),
		}
		if q50 := contract.QuantileDuration(h, 0.50, contract.DefaultDisplayMinSamples); q50.Value != nil {
			pt.StartupP50MS = q50.Value
		}
		if q90 := contract.QuantileDuration(h, 0.90, contract.DefaultDisplayMinSamples); q90.Value != nil {
			pt.StartupP90MS = q90.Value
		}
		out = append(out, pt)
	}
	return out, nil
}

func (s *Store) QueryCPUProfiles(ctx context.Context, f storage.QueryFilter, runtime string, limit int) ([]storage.CPUProfileRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	where := buildWhereClause(f, true, false)
	if runtime != "" {
		where += " AND runtime = " + quoteSQLString(runtime)
	}
	q := fmt.Sprintf(`
SELECT profile_id, occurred_at_ms, app_id, platform, env, app_version, build_number, scene,
       runtime, thread_name, sample_interval_ms, duration_ms, total_samples, stacks_json
FROM %s.caliper_cpu_profiles FINAL
WHERE %s
ORDER BY occurred_at_ms DESC
LIMIT %d
FORMAT JSON`, s.database, where, limit)
	return s.decodeCPUProfiles(ctx, q)
}

func (s *Store) GetCPUProfile(ctx context.Context, profileID string) (*storage.CPUProfileRecord, bool, error) {
	q := fmt.Sprintf(`
SELECT profile_id, occurred_at_ms, app_id, platform, env, app_version, build_number, scene,
       runtime, thread_name, sample_interval_ms, duration_ms, total_samples, stacks_json
FROM %s.caliper_cpu_profiles FINAL
WHERE profile_id = %s
LIMIT 1
FORMAT JSON`, s.database, quoteSQLString(profileID))
	rows, err := s.decodeCPUProfiles(ctx, q)
	if err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	return &rows[0], true, nil
}

func (s *Store) decodeCPUProfiles(ctx context.Context, q string) ([]storage.CPUProfileRecord, error) {
	var resp chJSONResp[struct {
		ProfileID        string    `json:"profile_id"`
		OccurredAtMS     flexInt64 `json:"occurred_at_ms"`
		AppID            string    `json:"app_id"`
		Platform         string    `json:"platform"`
		Env              string    `json:"env"`
		AppVersion       string    `json:"app_version"`
		BuildNumber      string    `json:"build_number"`
		Scene            string    `json:"scene"`
		Runtime          string    `json:"runtime"`
		ThreadName       string    `json:"thread_name"`
		SampleIntervalMS flexInt64 `json:"sample_interval_ms"`
		DurationMS       flexInt64 `json:"duration_ms"`
		TotalSamples     flexInt64 `json:"total_samples"`
		StacksJSON       string    `json:"stacks_json"`
	}]
	if err := s.queryJSON(ctx, q, &resp); err != nil {
		return nil, err
	}
	out := make([]storage.CPUProfileRecord, 0, len(resp.Data))
	for _, r := range resp.Data {
		var stacks []protocol.ProfileStackSample
		if r.StacksJSON != "" {
			_ = json.Unmarshal([]byte(r.StacksJSON), &stacks)
		}
		out = append(out, storage.CPUProfileRecord{
			ProfileID:        r.ProfileID,
			OccurredAtMS:     int64(r.OccurredAtMS),
			AppID:            r.AppID,
			Platform:         r.Platform,
			Env:              r.Env,
			AppVersion:       r.AppVersion,
			BuildNumber:      r.BuildNumber,
			Scene:            r.Scene,
			Runtime:          r.Runtime,
			ThreadName:       r.ThreadName,
			SampleIntervalMS: int(r.SampleIntervalMS),
			DurationMS:       int64(r.DurationMS),
			TotalSamples:     int64(r.TotalSamples),
			Stacks:           stacks,
		})
	}
	return out, nil
}

func (s *Store) QueryHeapSnapshots(ctx context.Context, f storage.QueryFilter, limit int) ([]storage.HeapSnapshotRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	where := buildWhereClause(f, true, false)
	q := fmt.Sprintf(`
SELECT snapshot_id, occurred_at_ms, app_id, platform, env, app_version, build_number, scene,
       trigger_reason, memory_kind, total_allocated_kib, total_retained_kib, memory_limit_kib, dominators_json
FROM %s.caliper_heap_snapshots FINAL
WHERE %s
ORDER BY occurred_at_ms DESC
LIMIT %d
FORMAT JSON`, s.database, where, limit)
	var resp chJSONResp[struct {
		SnapshotID        string    `json:"snapshot_id"`
		OccurredAtMS      flexInt64 `json:"occurred_at_ms"`
		AppID             string    `json:"app_id"`
		Platform          string    `json:"platform"`
		Env               string    `json:"env"`
		AppVersion        string    `json:"app_version"`
		BuildNumber       string    `json:"build_number"`
		Scene             string    `json:"scene"`
		TriggerReason     string    `json:"trigger_reason"`
		MemoryKind        string    `json:"memory_kind"`
		TotalAllocatedKiB float64   `json:"total_allocated_kib"`
		TotalRetainedKiB  float64   `json:"total_retained_kib"`
		MemoryLimitKiB    float64   `json:"memory_limit_kib"`
		DominatorsJSON    string    `json:"dominators_json"`
	}]
	if err := s.queryJSON(ctx, q, &resp); err != nil {
		return nil, err
	}
	out := make([]storage.HeapSnapshotRecord, 0, len(resp.Data))
	for _, r := range resp.Data {
		var doms []protocol.HeapDominator
		if r.DominatorsJSON != "" {
			_ = json.Unmarshal([]byte(r.DominatorsJSON), &doms)
		}
		out = append(out, storage.HeapSnapshotRecord{
			SnapshotID:        r.SnapshotID,
			OccurredAtMS:      int64(r.OccurredAtMS),
			AppID:             r.AppID,
			Platform:          r.Platform,
			Env:               r.Env,
			AppVersion:        r.AppVersion,
			BuildNumber:       r.BuildNumber,
			Scene:             r.Scene,
			TriggerReason:     r.TriggerReason,
			MemoryKind:        r.MemoryKind,
			TotalAllocatedKiB: r.TotalAllocatedKiB,
			TotalRetainedKiB:  r.TotalRetainedKiB,
			MemoryLimitKiB:    r.MemoryLimitKiB,
			Dominators:        doms,
		})
	}
	return out, nil
}

func (s *Store) Stats(_ context.Context) storage.Stats {
	return storage.Stats{
		Engine:      "clickhouse-http",
		TotalEvents: s.totalWrite.Load(),
	}
}

// ListDimensionValues discovers distinct device_model or os_version values for breakdown slicing.
func (s *Store) ListDimensionValues(ctx context.Context, f storage.QueryFilter, dimension string) []string {
	col := ""
	switch dimension {
	case "device":
		col = "device_model"
	case "os":
		col = "os_version"
	case "scene":
		col = "scene"
	default:
		return nil
	}
	sub := f
	sub.DeviceModel = ""
	sub.OSVersion = ""
	where := buildWhereClause(sub, false, false)
	q := fmt.Sprintf(`
SELECT DISTINCT %s AS val FROM (
  SELECT %s FROM caliper_startup_events FINAL WHERE %s AND %s != ''
  UNION ALL
  SELECT %s FROM caliper_fluency_events FINAL WHERE %s AND %s != ''
) ORDER BY val ASC LIMIT 50 FORMAT JSON`, col, col, where, col, col, where, col)

	var resp chJSONResp[struct {
		Val string `json:"val"`
	}]
	if err := s.queryJSON(ctx, q, &resp); err != nil {
		return nil
	}
	out := make([]string, 0, len(resp.Data))
	for _, row := range resp.Data {
		if row.Val != "" {
			out = append(out, row.Val)
		}
	}
	return out
}

func (s *Store) Close() error {
	s.client.CloseIdleConnections()
	return nil
}

func (s *Store) insertJSONEachRow(ctx context.Context, table string, rows []any) error {
	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	enc := json.NewEncoder(gz)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			_ = gz.Close()
			return err
		}
	}
	if err := gz.Close(); err != nil {
		return err
	}

	u := s.buildURL(s.database, fmt.Sprintf("INSERT INTO %s.%s FORMAT JSONEachRow", s.database, table))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, &gzBuf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Content-Type", "application/x-ndjson")
	s.applyAuth(req)

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("clickhouse HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (s *Store) execDDL(ctx context.Context, db, ddl string) error {
	u := s.buildURL(db, "")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(ddl))
	if err != nil {
		return err
	}
	s.applyAuth(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("clickhouse DDL HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (s *Store) queryJSON(ctx context.Context, sqlQuery string, dest any) error {
	u := s.buildURL(s.database, "")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(sqlQuery))
	if err != nil {
		return err
	}
	s.applyAuth(req)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("clickhouse query HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(dest)
}

func (s *Store) buildURL(db, query string) string {
	vals := url.Values{}
	if db != "" {
		vals.Set("database", db)
	}
	if query != "" {
		vals.Set("query", query)
	}
	encoded := vals.Encode()
	if encoded == "" {
		return s.endpoint + "/"
	}
	return s.endpoint + "/?" + encoded
}

func (s *Store) applyAuth(req *http.Request) {
	if s.username != "" {
		req.Header.Set("X-ClickHouse-User", s.username)
	}
	if s.password != "" {
		req.Header.Set("X-ClickHouse-Key", s.password)
	}
}

func buildWhereClause(f storage.QueryFilter, withScene, withAlgo bool) string {
	conds := []string{"1 = 1"}
	if f.AppID != "" {
		conds = append(conds, "app_id = "+quoteSQLString(f.AppID))
	}
	if f.Platform != "" {
		conds = append(conds, "platform = "+quoteSQLString(f.Platform))
	}
	if f.Env != "" {
		conds = append(conds, "env = "+quoteSQLString(f.Env))
	}
	if f.AppVersion != "" {
		conds = append(conds, "app_version = "+quoteSQLString(f.AppVersion))
	}
	if f.BuildNumber != "" {
		conds = append(conds, "build_number = "+quoteSQLString(f.BuildNumber))
	}
	if f.Adapter != "" {
		conds = append(conds, "adapter = "+quoteSQLString(f.Adapter))
	}
	if f.DeviceModel != "" {
		conds = append(conds, "device_model = "+quoteSQLString(f.DeviceModel))
	}
	if f.OSVersion != "" {
		conds = append(conds, "os_version = "+quoteSQLString(f.OSVersion))
	}
	if f.PolicyID != "" {
		conds = append(conds, "policy_id = "+quoteSQLString(f.PolicyID))
	}
	if withScene && f.Scene != "" {
		conds = append(conds, "scene = "+quoteSQLString(f.Scene))
	}
	if withAlgo && f.Algorithm != "" {
		conds = append(conds, "algorithm = "+quoteSQLString(f.Algorithm))
	}
	if f.StartDayUTC != "" {
		conds = append(conds, "day_utc >= toDate("+quoteSQLString(f.StartDayUTC)+")")
	}
	if f.EndDayUTC != "" {
		conds = append(conds, "day_utc <= toDate("+quoteSQLString(f.EndDayUTC)+")")
	}
	return strings.Join(conds, " AND ")
}

func quoteSQLString(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `'`, `\'`)
	return "'" + v + "'"
}

type chJSONResp[T any] struct {
	Data []T `json:"data"`
}

// flexInt64 unmarshals either JSON numbers or ClickHouse's string-encoded UInt64/Int64 values.
type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == `""` {
		*f = 0
		return nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*f = flexInt64(n)
	return nil
}

func toHistogram(src []flexInt64) contract.Histogram {
	var h contract.Histogram
	for i := 0; i < len(src) && i < contract.HistogramBucketCount; i++ {
		h[i] = int64(src[i])
	}
	return h
}
