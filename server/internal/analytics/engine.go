package analytics

import (
	"context"
	"sort"

	"github.com/caliper-apm/caliper/server/internal/contract"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

// Engine executes Zero-Lie analytical queries and gated release comparisons over AnalyticsStore.
type Engine struct {
	store storage.AnalyticsStore
}

// New creates an analytics engine backed by store.
func New(store storage.AnalyticsStore) *Engine {
	return &Engine{store: store}
}

// StartupReport breaks down multi-stage cold start with mergeable histogram quantiles.
type StartupReport struct {
	Platform        string                  `json:"platform"`
	AppVersion      string                  `json:"app_version,omitempty"`
	SuccessCount    int64                   `json:"success_count"`
	AbortedCount    int64                   `json:"aborted_count"`
	TimeoutCount    int64                   `json:"timeout_count"`
	StagedCoverage  *float64                `json:"staged_coverage_pct"`
	TotalP50        contract.QuantileResult `json:"total_p50_ms"`
	TotalP90        contract.QuantileResult `json:"total_p90_ms"`
	NativeInitP90   contract.QuantileResult `json:"native_init_p90_ms"`
	RuntimeInitP90  contract.QuantileResult `json:"runtime_init_p90_ms"`
	FirstFrameP90   contract.QuantileResult `json:"first_frame_p90_ms"`
}

// FluencySceneReport exposes Zero-Lie frame smoothness per (scene, algorithm).
type FluencySceneReport struct {
	Scene             string                  `json:"scene"`
	Algorithm         string                  `json:"algorithm"`
	TotalWindows      int64                   `json:"total_windows"`
	ValidWindows      int64                   `json:"valid_windows"`
	ZeroFrameWindows  int64                   `json:"zero_frame_windows"`
	TotalFrames       int64                   `json:"total_frames"`
	JankFrames        int64                   `json:"jank_frames"`
	JankRatePct       *float64                `json:"jank_rate_pct"`
	Severity          string                  `json:"severity"`
	MissingReason     string                  `json:"missing_reason,omitempty"`
	FrameP90          contract.QuantileResult `json:"frame_p90_ms"`
	StallWindowCount  int64                   `json:"stall_window_count"`
	SampleStallStacks [][]string              `json:"sample_stall_stacks,omitempty"`
}

// ResourceSceneReport summarizes process CPU, resident memory, and optional RN JS LongTasks.
type ResourceSceneReport struct {
	Scene               string                  `json:"scene"`
	MemoryKind          string                  `json:"memory_kind,omitempty"`
	ResourceWindows     int64                   `json:"resource_windows"`
	AvgCPUPct           *float64                `json:"avg_cpu_pct"`
	CPUMissingReason    string                  `json:"cpu_missing_reason,omitempty"`
	MemoryPeakP50MiB    contract.QuantileResult `json:"memory_peak_p50_mib"`
	MemoryPeakP90MiB    contract.QuantileResult `json:"memory_peak_p90_mib"`
	JSLongTaskWindows   int64                   `json:"js_longtask_windows"`
	JSLongTaskCount     int64                   `json:"js_longtask_count"`
	AvgJSBlockingPerWin *float64                `json:"avg_js_blocking_ms_per_window"`
	JSMaxTaskMS         *float64                `json:"js_max_task_ms"`
}

// TransportOpReport summarizes DNS/TCP/TLS/TTFB/Body stages per allowlisted operation.
type TransportOpReport struct {
	Operation       string                  `json:"operation"`
	Method          string                  `json:"method"`
	SampleCount     int64                   `json:"sample_count"`
	SuccessRatePct  *float64                `json:"success_rate_pct"`
	ReuseRatePct    *float64                `json:"connection_reuse_rate_pct"`
	DNSP90          contract.QuantileResult `json:"dns_p90_ms"`
	TCPP90          contract.QuantileResult `json:"tcp_p90_ms"`
	TLSP90          contract.QuantileResult `json:"tls_p90_ms"`
	TTFBP90         contract.QuantileResult `json:"ttfb_p90_ms"`
	BodyP90         contract.QuantileResult `json:"body_p90_ms"`
	TotalP90        contract.QuantileResult `json:"total_p90_ms"`
}

// NetworkReport exposes logical API denominator metrics alongside sampled transport stages.
type NetworkReport struct {
	SummaryWindows int64                   `json:"summary_windows"`
	TotalCalls     int64                   `json:"total_calls"`
	FailedCalls    int64                   `json:"failed_calls"`
	SuccessRatePct *float64                `json:"success_rate_pct"`
	MissingReason  string                  `json:"missing_reason,omitempty"`
	DurationP50    contract.QuantileResult `json:"duration_p50_ms"`
	DurationP90    contract.QuantileResult `json:"duration_p90_ms"`
	Operations     []TransportOpReport     `json:"operations"`
}

// OverviewReport combines the 5 mobile observability domains for a scope.
type OverviewReport struct {
	Platform   string               `json:"platform"`
	AppVersion string               `json:"app_version,omitempty"`
	Startup    StartupReport        `json:"startup"`
	Fluency    []FluencySceneReport `json:"fluency"`
	Resources  []ResourceSceneReport `json:"resources"`
	Network    NetworkReport        `json:"network"`
	TopIssues  []storage.IssueSummary `json:"top_issues"`
}

// CompareRequest compares baseline vs candidate within the same platform and env.
type CompareRequest struct {
	AppID           string `json:"app_id"`
	Platform        string `json:"platform"`
	Env             string `json:"env"`
	BaselineVersion string `json:"baseline_version"`
	CandidateVersion string `json:"candidate_version"`
	Scene           string `json:"scene,omitempty"`
	Algorithm       string `json:"algorithm,omitempty"`
	StartDayUTC     string `json:"start_day_utc,omitempty"`
	EndDayUTC       string `json:"end_day_utc,omitempty"`
	MinSamples      int64  `json:"min_samples,omitempty"`
}

// CompareReport returns strictly gated metric comparisons and an overall verdict.
type CompareReport struct {
	Platform         string                      `json:"platform"`
	BaselineVersion  string                      `json:"baseline_version"`
	CandidateVersion string                      `json:"candidate_version"`
	Verdict          string                      `json:"verdict"` // "regressed" | "improved" | "neutral" | "inconclusive"
	RegressedCount   int                         `json:"regressed_count"`
	GatedCount       int                         `json:"gated_count"`
	Metrics          []contract.MetricComparison `json:"metrics"`
}

func (e *Engine) Startup(ctx context.Context, f storage.QueryFilter) (StartupReport, error) {
	agg, err := e.store.QueryStartup(ctx, f)
	if err != nil {
		return StartupReport{}, err
	}
	var stagedCov *float64
	if agg.SuccessCount > 0 {
		v := (float64(agg.StagedCount) / float64(agg.SuccessCount)) * 100.0
		stagedCov = &v
	}
	return StartupReport{
		Platform:       f.Platform,
		AppVersion:     f.AppVersion,
		SuccessCount:   agg.SuccessCount,
		AbortedCount:   agg.AbortedCount,
		TimeoutCount:   agg.TimeoutCount,
		StagedCoverage: stagedCov,
		TotalP50:       contract.QuantileDuration(agg.TotalHist, 0.50, contract.DefaultDisplayMinSamples),
		TotalP90:       contract.QuantileDuration(agg.TotalHist, 0.90, contract.DefaultDisplayMinSamples),
		NativeInitP90:  contract.QuantileDuration(agg.NativeInitHist, 0.90, contract.DefaultDisplayMinSamples),
		RuntimeInitP90: contract.QuantileDuration(agg.RuntimeHist, 0.90, contract.DefaultDisplayMinSamples),
		FirstFrameP90:  contract.QuantileDuration(agg.FirstFrameHist, 0.90, contract.DefaultDisplayMinSamples),
	}, nil
}

func (e *Engine) Fluency(ctx context.Context, f storage.QueryFilter) ([]FluencySceneReport, error) {
	aggs, err := e.store.QueryFluency(ctx, f)
	if err != nil {
		return nil, err
	}
	out := make([]FluencySceneReport, 0, len(aggs))
	for _, agg := range aggs {
		item := FluencySceneReport{
			Scene:             agg.Scene,
			Algorithm:         agg.Algorithm,
			TotalWindows:      agg.TotalWindows,
			ValidWindows:      agg.ValidWindows,
			ZeroFrameWindows:  agg.ZeroFrameWindows,
			TotalFrames:       agg.TotalFrames,
			JankFrames:        agg.JankFrames,
			FrameP90:          contract.QuantileDuration(agg.FrameDurationHist, 0.90, contract.DefaultDisplayMinSamples),
			StallWindowCount:  agg.StallWindowCount,
			SampleStallStacks: agg.SampleStallStacks,
		}
		if agg.ValidWindows == 0 || agg.TotalFrames == 0 {
			item.Severity = "unknown"
			item.MissingReason = contract.MissingNoObservations
		} else {
			assessment := contract.ClassifyFluency(agg.TotalFrames, agg.JankFrames)
			item.Severity = assessment.Severity
			if assessment.JankRatio != nil {
				pct := *assessment.JankRatio * 100.0
				item.JankRatePct = &pct
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func (e *Engine) Resources(ctx context.Context, f storage.QueryFilter) ([]ResourceSceneReport, error) {
	aggs, err := e.store.QueryResources(ctx, f)
	if err != nil {
		return nil, err
	}
	out := make([]ResourceSceneReport, 0, len(aggs))
	for _, agg := range aggs {
		item := ResourceSceneReport{
			Scene:             agg.Scene,
			MemoryKind:        agg.MemoryKind,
			ResourceWindows:   agg.ResourceWindows,
			MemoryPeakP50MiB:  contract.QuantileMemoryMiB(agg.MemoryPeakHistMiB, 0.50, contract.DefaultDisplayMinSamples),
			MemoryPeakP90MiB:  contract.QuantileMemoryMiB(agg.MemoryPeakHistMiB, 0.90, contract.DefaultDisplayMinSamples),
			JSLongTaskWindows: agg.JSLongTaskWindows,
			JSLongTaskCount:   agg.JSLongTaskCount,
		}
		if agg.ResourceWindows == 0 {
			item.CPUMissingReason = contract.MissingNoObservations
		} else if agg.ResourceWindows < contract.DefaultDisplayMinSamples {
			item.CPUMissingReason = contract.MissingInsufficientSamples
		} else {
			avg := agg.CPUPctSum / float64(agg.ResourceWindows)
			item.AvgCPUPct = &avg
		}
		if agg.JSLongTaskWindows >= contract.DefaultDisplayMinSamples {
			avgBlk := agg.JSBlockingSumMS / float64(agg.JSLongTaskWindows)
			maxT := agg.JSMaxTaskMS
			item.AvgJSBlockingPerWin = &avgBlk
			item.JSMaxTaskMS = &maxT
		}
		out = append(out, item)
	}
	return out, nil
}

func (e *Engine) Network(ctx context.Context, f storage.QueryFilter) (NetworkReport, error) {
	agg, err := e.store.QueryNetwork(ctx, f)
	if err != nil {
		return NetworkReport{}, err
	}
	failed := agg.FailureCounts.Total()
	rep := NetworkReport{
		SummaryWindows: agg.SummaryWindows,
		TotalCalls:     agg.TotalCalls,
		FailedCalls:    failed,
		DurationP50:    contract.QuantileDuration(agg.DurationHist, 0.50, contract.DefaultDisplayMinSamples),
		DurationP90:    contract.QuantileDuration(agg.DurationHist, 0.90, contract.DefaultDisplayMinSamples),
		Operations:     make([]TransportOpReport, 0, len(agg.TransportSamples)),
	}
	if agg.TotalCalls == 0 {
		rep.MissingReason = contract.MissingDenominator
	} else if agg.TotalCalls < contract.DefaultDisplayMinSamples {
		rep.MissingReason = contract.MissingInsufficientSamples
	} else {
		rate := (float64(agg.TotalCalls-failed) / float64(agg.TotalCalls)) * 100.0
		rep.SuccessRatePct = &rate
	}

	for _, op := range agg.TransportSamples {
		row := TransportOpReport{
			Operation:   op.Operation,
			Method:      op.Method,
			SampleCount: op.TotalCount,
			DNSP90:      contract.QuantileDuration(op.DNSHist, 0.90, contract.DefaultDisplayMinSamples),
			TCPP90:      contract.QuantileDuration(op.TCPHist, 0.90, contract.DefaultDisplayMinSamples),
			TLSP90:      contract.QuantileDuration(op.TLSHist, 0.90, contract.DefaultDisplayMinSamples),
			TTFBP90:     contract.QuantileDuration(op.TTFBHist, 0.90, contract.DefaultDisplayMinSamples),
			BodyP90:     contract.QuantileDuration(op.BodyHist, 0.90, contract.DefaultDisplayMinSamples),
			TotalP90:    contract.QuantileDuration(op.TotalHist, 0.90, contract.DefaultDisplayMinSamples),
		}
		if op.TotalCount >= contract.DefaultDisplayMinSamples {
			sr := (float64(op.SuccessCnt) / float64(op.TotalCount)) * 100.0
			rr := (float64(op.ReusedCnt) / float64(op.TotalCount)) * 100.0
			row.SuccessRatePct = &sr
			row.ReuseRatePct = &rr
		}
		rep.Operations = append(rep.Operations, row)
	}
	sort.Slice(rep.Operations, func(i, j int) bool {
		if rep.Operations[i].SampleCount != rep.Operations[j].SampleCount {
			return rep.Operations[i].SampleCount > rep.Operations[j].SampleCount
		}
		return rep.Operations[i].Operation < rep.Operations[j].Operation
	})
	return rep, nil
}

func (e *Engine) Overview(ctx context.Context, f storage.QueryFilter) (OverviewReport, error) {
	st, err := e.Startup(ctx, f)
	if err != nil {
		return OverviewReport{}, err
	}
	fl, err := e.Fluency(ctx, f)
	if err != nil {
		return OverviewReport{}, err
	}
	res, err := e.Resources(ctx, f)
	if err != nil {
		return OverviewReport{}, err
	}
	net, err := e.Network(ctx, f)
	if err != nil {
		return OverviewReport{}, err
	}
	issues, err := e.store.QueryIssues(ctx, f, 10)
	if err != nil {
		return OverviewReport{}, err
	}
	return OverviewReport{
		Platform:   f.Platform,
		AppVersion: f.AppVersion,
		Startup:    st,
		Fluency:    fl,
		Resources:  res,
		Network:    net,
		TopIssues:  issues,
	}, nil
}

// Compare compares baseline vs candidate across all core metrics with strict Zero-Lie gates.
func (e *Engine) Compare(ctx context.Context, req CompareRequest) (CompareReport, error) {
	minSamples := req.MinSamples
	if minSamples <= 0 {
		minSamples = contract.DefaultComparisonMinSamples
	}
	env := req.Env
	if env == "" {
		env = "prod"
	}

	baseFilter := storage.QueryFilter{
		AppID:       req.AppID,
		Platform:    req.Platform,
		Env:         env,
		AppVersion:  req.BaselineVersion,
		Scene:       req.Scene,
		Algorithm:   req.Algorithm,
		StartDayUTC: req.StartDayUTC,
		EndDayUTC:   req.EndDayUTC,
	}
	candFilter := baseFilter
	candFilter.AppVersion = req.CandidateVersion

	var metrics []contract.MetricComparison

	// 1. Cold start P50 & P90
	baseSt, err := e.store.QueryStartup(ctx, baseFilter)
	if err != nil {
		return CompareReport{}, err
	}
	candSt, err := e.store.QueryStartup(ctx, candFilter)
	if err != nil {
		return CompareReport{}, err
	}
	bP50 := contract.QuantileDuration(baseSt.TotalHist, 0.50, minSamples)
	cP50 := contract.QuantileDuration(candSt.TotalHist, 0.50, minSamples)
	metrics = append(metrics, contract.CompareValues(
		"startup_cold_p50_ms", "ms", "lower_is_better",
		bP50.Value, bP50.SampleSize, bP50.MissingReason,
		cP50.Value, cP50.SampleSize, cP50.MissingReason,
		minSamples, 50.0,
	))
	bP90 := contract.QuantileDuration(baseSt.TotalHist, 0.90, minSamples)
	cP90 := contract.QuantileDuration(candSt.TotalHist, 0.90, minSamples)
	metrics = append(metrics, contract.CompareValues(
		"startup_cold_p90_ms", "ms", "lower_is_better",
		bP90.Value, bP90.SampleSize, bP90.MissingReason,
		cP90.Value, cP90.SampleSize, cP90.MissingReason,
		minSamples, 100.0,
	))

	// 2. Fluency jank rate (only across identical algorithms!)
	baseFl, err := e.store.QueryFluency(ctx, baseFilter)
	if err != nil {
		return CompareReport{}, err
	}
	candFl, err := e.store.QueryFluency(ctx, candFilter)
	if err != nil {
		return CompareReport{}, err
	}
	bJank, bFlN, bAlgo, bFlMiss := aggregateSingleAlgorithmFluency(baseFl)
	cJank, cFlN, cAlgo, cFlMiss := aggregateSingleAlgorithmFluency(candFl)
	if bFlMiss == "" && cFlMiss == "" && bAlgo != cAlgo {
		cFlMiss = contract.MissingIncompatibleAlgo
	}
	metrics = append(metrics, contract.CompareValues(
		"fluency_jank_rate_pct", "%", "lower_is_better",
		bJank, bFlN, bFlMiss,
		cJank, cFlN, cFlMiss,
		minSamples, 0.5,
	))

	// 3. Resources: Avg CPU & Peak Memory P90
	baseRes, err := e.store.QueryResources(ctx, baseFilter)
	if err != nil {
		return CompareReport{}, err
	}
	candRes, err := e.store.QueryResources(ctx, candFilter)
	if err != nil {
		return CompareReport{}, err
	}
	bCPU, bMemHist, bResN := mergeResources(baseRes)
	cCPU, cMemHist, cResN := mergeResources(candRes)
	metrics = append(metrics, contract.CompareValues(
		"resource_cpu_avg_pct", "%", "lower_is_better",
		bCPU, bResN, missingIfZero(bResN),
		cCPU, cResN, missingIfZero(cResN),
		minSamples, 2.0,
	))
	bMemP90 := contract.QuantileMemoryMiB(bMemHist, 0.90, minSamples)
	cMemP90 := contract.QuantileMemoryMiB(cMemHist, 0.90, minSamples)
	metrics = append(metrics, contract.CompareValues(
		"resource_memory_peak_p90_mib", "MiB", "lower_is_better",
		bMemP90.Value, bMemP90.SampleSize, bMemP90.MissingReason,
		cMemP90.Value, cMemP90.SampleSize, cMemP90.MissingReason,
		minSamples, 16.0,
	))

	// 4. Network logical API success rate & P90
	baseNet, err := e.store.QueryNetwork(ctx, baseFilter)
	if err != nil {
		return CompareReport{}, err
	}
	candNet, err := e.store.QueryNetwork(ctx, candFilter)
	if err != nil {
		return CompareReport{}, err
	}
	bNetRate, bNetMiss := networkSuccessRate(baseNet)
	cNetRate, cNetMiss := networkSuccessRate(candNet)
	metrics = append(metrics, contract.CompareValues(
		"network_api_success_rate_pct", "%", "higher_is_better",
		bNetRate, baseNet.TotalCalls, bNetMiss,
		cNetRate, candNet.TotalCalls, cNetMiss,
		minSamples, 0.5,
	))
	bNetP90 := contract.QuantileDuration(baseNet.DurationHist, 0.90, minSamples)
	cNetP90 := contract.QuantileDuration(candNet.DurationHist, 0.90, minSamples)
	metrics = append(metrics, contract.CompareValues(
		"network_api_p90_ms", "ms", "lower_is_better",
		bNetP90.Value, bNetP90.SampleSize, bNetP90.MissingReason,
		cNetP90.Value, cNetP90.SampleSize, cNetP90.MissingReason,
		minSamples, 50.0,
	))

	regressed, improved, gated := 0, 0, 0
	for _, m := range metrics {
		switch m.Status {
		case "regressed":
			regressed++
		case "improved":
			improved++
		case "gated":
			gated++
		}
	}
	verdict := "neutral"
	switch {
	case regressed > 0:
		verdict = "regressed"
	case gated == len(metrics):
		verdict = "inconclusive"
	case improved > 0:
		verdict = "improved"
	}

	return CompareReport{
		Platform:         req.Platform,
		BaselineVersion:  req.BaselineVersion,
		CandidateVersion: req.CandidateVersion,
		Verdict:          verdict,
		RegressedCount:   regressed,
		GatedCount:       gated,
		Metrics:          metrics,
	}, nil
}

func aggregateSingleAlgorithmFluency(rows []storage.FluencyAggregate) (*float64, int64, string, string) {
	if len(rows) == 0 {
		return nil, 0, "", contract.MissingNoObservations
	}
	algos := map[string]struct{}{}
	var singleAlgo string
	var validWindows, totalFrames, jankFrames int64
	for _, r := range rows {
		if r.ValidWindows > 0 {
			algos[r.Algorithm] = struct{}{}
			singleAlgo = r.Algorithm
			validWindows += r.ValidWindows
			totalFrames += r.TotalFrames
			jankFrames += r.JankFrames
		}
	}
	if validWindows == 0 || totalFrames == 0 {
		return nil, 0, "", contract.MissingNoObservations
	}
	if len(algos) > 1 {
		return nil, validWindows, "", contract.MissingIncompatibleAlgo
	}
	pct := (float64(jankFrames) / float64(totalFrames)) * 100.0
	return &pct, validWindows, singleAlgo, ""
}

func mergeResources(rows []storage.ResourceAggregate) (*float64, contract.Histogram, int64) {
	var totalWin int64
	var cpuSum float64
	var memHist contract.Histogram
	for _, r := range rows {
		totalWin += r.ResourceWindows
		cpuSum += r.CPUPctSum
		memHist.Add(r.MemoryPeakHistMiB)
	}
	if totalWin == 0 {
		return nil, memHist, 0
	}
	avg := cpuSum / float64(totalWin)
	return &avg, memHist, totalWin
}

func networkSuccessRate(n storage.NetworkAggregate) (*float64, string) {
	if n.TotalCalls == 0 {
		return nil, contract.MissingDenominator
	}
	rate := (float64(n.TotalCalls-n.FailureCounts.Total()) / float64(n.TotalCalls)) * 100.0
	return &rate, ""
}

func missingIfZero(n int64) string {
	if n == 0 {
		return contract.MissingNoObservations
	}
	return ""
}
