package analytics

import (
	"context"

	"github.com/caliper-apm/caliper/server/internal/contract"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

// JourneyStageReport summarizes one ordered stage inside a custom business journey trace.
type JourneyStageReport struct {
	Name        string                  `json:"name"`
	SampleCount int64                   `json:"sample_count"`
	P50MS       contract.QuantileResult `json:"p50_ms"`
	P90MS       contract.QuantileResult `json:"p90_ms"`
}

// CustomJourneyReport exposes Zero-Lie duration quantiles, success rates, and child stage waterfalls
// for custom business transactions (e.g. article_open_to_readable, feed_refresh_pipeline).
type CustomJourneyReport struct {
	JourneyName     string                  `json:"journey_name"`
	TotalCount      int64                   `json:"total_count"`
	SuccessCount    int64                   `json:"success_count"`
	FailureCount    int64                   `json:"failure_count"`
	TimeoutCount    int64                   `json:"timeout_count"`
	CancelledCount  int64                   `json:"cancelled_count"`
	SuccessRatePct  *float64                `json:"success_rate_pct"`
	MissingReason   string                  `json:"missing_reason,omitempty"`
	DurationP50     contract.QuantileResult `json:"duration_p50_ms"`
	DurationP90     contract.QuantileResult `json:"duration_p90_ms"`
	DurationBuckets []HistogramBucket       `json:"duration_buckets,omitempty"`
	Stages          []JourneyStageReport    `json:"stages,omitempty"`
	ErrorCodes      map[string]int64        `json:"error_codes,omitempty"`
}

// CustomJourneys computes Zero-Lie aggregates for all custom_scene business journeys in scope.
func (e *Engine) CustomJourneys(ctx context.Context, f storage.QueryFilter) ([]CustomJourneyReport, error) {
	aggs, err := e.store.QueryCustomScenes(ctx, f)
	if err != nil {
		return nil, err
	}
	out := make([]CustomJourneyReport, 0, len(aggs))
	for _, a := range aggs {
		rep := CustomJourneyReport{
			JourneyName:     a.JourneyName,
			TotalCount:      a.TotalCount,
			SuccessCount:    a.SuccessCount,
			FailureCount:    a.FailureCount,
			TimeoutCount:    a.TimeoutCount,
			CancelledCount:  a.CancelledCount,
			DurationP50:     contract.QuantileDuration(a.DurationHist, 0.50, contract.DefaultDisplayMinSamples),
			DurationP90:     contract.QuantileDuration(a.DurationHist, 0.90, contract.DefaultDisplayMinSamples),
			DurationBuckets: DurationBucketsSlice(a.DurationHist),
			ErrorCodes:      a.ErrorCodes,
		}
		if a.TotalCount == 0 {
			rep.MissingReason = contract.MissingNoObservations
		} else if a.TotalCount < contract.DefaultDisplayMinSamples {
			rep.MissingReason = contract.MissingInsufficientSamples
		} else {
			sr := (float64(a.SuccessCount) / float64(a.TotalCount)) * 100.0
			rep.SuccessRatePct = &sr
		}
		for _, stName := range a.StageOrder {
			h := a.StageHists[stName]
			rep.Stages = append(rep.Stages, JourneyStageReport{
				Name:        stName,
				SampleCount: h.Total(),
				P50MS:       contract.QuantileDuration(h, 0.50, contract.DefaultDisplayMinSamples),
				P90MS:       contract.QuantileDuration(h, 0.90, contract.DefaultDisplayMinSamples),
			})
		}
		out = append(out, rep)
	}
	return out, nil
}
