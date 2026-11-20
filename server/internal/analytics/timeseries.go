package analytics

import (
	"context"
	"fmt"
	"sort"

	"github.com/caliper-apm/caliper/server/internal/contract"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

// HistogramBucket exposes one fixed-bound bucket count for real distribution charts.
type HistogramBucket struct {
	Label string   `json:"label"`
	Lower float64  `json:"lower"`
	Upper *float64 `json:"upper"` // nil for overflow bucket (>120000 ms)
	Count int64    `json:"count"`
}

// DurationBucketsSlice converts a 19-bucket duration histogram into chart-ready bucket metadata.
func DurationBucketsSlice(h contract.Histogram) []HistogramBucket {
	out := make([]HistogramBucket, contract.HistogramBucketCount)
	for i := 0; i < contract.HistogramBucketCount; i++ {
		if i < len(contract.DurationBoundsMS) {
			lo := 0.0
			if i > 0 {
				lo = contract.DurationBoundsMS[i-1]
			}
			hi := contract.DurationBoundsMS[i]
			out[i] = HistogramBucket{
				Label: formatDurationLabel(lo, hi),
				Lower: lo,
				Upper: &hi,
				Count: h[i],
			}
		} else {
			lo := contract.DurationBoundsMS[len(contract.DurationBoundsMS)-1]
			out[i] = HistogramBucket{
				Label: fmt.Sprintf(">%.0fs", lo/1000.0),
				Lower: lo,
				Upper: nil,
				Count: h[i],
			}
		}
	}
	return out
}

func formatDurationLabel(lo, hi float64) string {
	fmtSec := func(ms float64) string {
		s := ms / 1000.0
		if s == float64(int64(s)) {
			return fmt.Sprintf("%.0fs", s)
		}
		return fmt.Sprintf("%.1fs", s)
	}
	if hi >= 1000 {
		if lo >= 1000 {
			return fmt.Sprintf("%s-%s", fmtSec(lo), fmtSec(hi))
		}
		return fmt.Sprintf("%.0fms-%s", lo, fmtSec(hi))
	}
	return fmt.Sprintf("%.0f-%.0fms", lo, hi)
}

// DailyTrends returns real UTC day-by-day Zero-Lie time-series points.
func (e *Engine) DailyTrends(ctx context.Context, f storage.QueryFilter) ([]storage.DailyTrendPoint, error) {
	return e.store.QueryDailyTrends(ctx, f)
}

// DimensionSlice compares core observability health across slices (version, adapter, or scene).
type DimensionSlice struct {
	Dimension      string   `json:"dimension"` // version | adapter | scene
	Key            string   `json:"key"`
	StartupP90MS   *float64 `json:"startup_p90_ms"`
	StartupSamples int64    `json:"startup_samples"`
	JankRatePct    *float64 `json:"jank_rate_pct"`
	FluencyWindows int64    `json:"fluency_windows"`
	APISuccessPct  *float64 `json:"api_success_rate_pct"`
	APIP90MS       *float64 `json:"api_p90_ms"`
	APICalls       int64    `json:"api_calls"`
	AvgCPUPct      *float64 `json:"avg_cpu_pct"`
	MemoryP90MiB   *float64 `json:"memory_p90_mib"`
	IssueEvents    int64    `json:"issue_events"`
}

// Breakdown computes real multi-dimensional slices across versions, adapters (native vs react-native), or scenes.
func (e *Engine) Breakdown(ctx context.Context, meta storage.MetaStore, f storage.QueryFilter, dimension string) ([]DimensionSlice, error) {
	var keys []string
	switch dimension {
	case "adapter":
		keys = []string{"native", "react-native"}
	case "version":
		if meta != nil {
			rels, err := meta.ListReleases(ctx, f.AppID, f.Platform, f.Env)
			if err != nil {
				return nil, err
			}
			seen := map[string]struct{}{}
			for _, r := range rels {
				if _, ok := seen[r.AppVersion]; !ok {
					seen[r.AppVersion] = struct{}{}
					keys = append(keys, r.AppVersion)
				}
			}
			sort.Strings(keys)
		}
	case "scene":
		fl, err := e.store.QueryFluency(ctx, f)
		if err != nil {
			return nil, err
		}
		res, err := e.store.QueryResources(ctx, f)
		if err != nil {
			return nil, err
		}
		seen := map[string]struct{}{}
		for _, r := range fl {
			seen[r.Scene] = struct{}{}
		}
		for _, r := range res {
			seen[r.Scene] = struct{}{}
		}
		for k := range seen {
			keys = append(keys, k)
		}
		sort.Strings(keys)
	case "device", "os":
		type dimLister interface {
			ListDimensionValues(ctx context.Context, f storage.QueryFilter, dimension string) []string
		}
		if lister, ok := e.store.(dimLister); ok {
			keys = lister.ListDimensionValues(ctx, f, dimension)
		}
	default:
		return nil, fmt.Errorf("unsupported breakdown dimension %q (expected version, adapter, scene, device, or os)", dimension)
	}

	out := make([]DimensionSlice, 0, len(keys))
	for _, k := range keys {
		sub := f
		switch dimension {
		case "adapter":
			sub.Adapter = k
		case "version":
			sub.AppVersion = k
		case "scene":
			sub.Scene = k
		case "device":
			sub.DeviceModel = k
		case "os":
			sub.OSVersion = k
		}
		ov, err := e.Overview(ctx, sub)
		if err != nil {
			return nil, err
		}
		slice := DimensionSlice{
			Dimension:      dimension,
			Key:            k,
			StartupP90MS:   ov.Startup.TotalP90.Value,
			StartupSamples: ov.Startup.SuccessCount,
			APISuccessPct:  ov.Network.SuccessRatePct,
			APIP90MS:       ov.Network.DurationP90.Value,
			APICalls:       ov.Network.TotalCalls,
		}
		var validWin, totalFrames, jankFrames int64
		for _, fl := range ov.Fluency {
			if fl.ValidWindows > 0 {
				validWin += fl.ValidWindows
				totalFrames += fl.TotalFrames
				jankFrames += fl.JankFrames
			}
		}
		slice.FluencyWindows = validWin
		if validWin >= contract.DefaultDisplayMinSamples && totalFrames > 0 {
			jr := (float64(jankFrames) / float64(totalFrames)) * 100.0
			slice.JankRatePct = &jr
		}
		var resWin int64
		var cpuWeighted float64
		var maxMemP90 *float64
		for _, r := range ov.Resources {
			if r.AvgCPUPct != nil && r.ResourceWindows > 0 {
				cpuWeighted += (*r.AvgCPUPct) * float64(r.ResourceWindows)
				resWin += r.ResourceWindows
			}
			if r.MemoryPeakP90MiB.Value != nil {
				if maxMemP90 == nil || *r.MemoryPeakP90MiB.Value > *maxMemP90 {
					v := *r.MemoryPeakP90MiB.Value
					maxMemP90 = &v
				}
			}
		}
		if resWin >= contract.DefaultDisplayMinSamples {
			avg := cpuWeighted / float64(resWin)
			slice.AvgCPUPct = &avg
		}
		slice.MemoryP90MiB = maxMemP90
		for _, iss := range ov.TopIssues {
			slice.IssueEvents += iss.EventCount
		}
		out = append(out, slice)
	}
	return out, nil
}
