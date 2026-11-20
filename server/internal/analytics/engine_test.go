package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/caliper-apm/caliper/server/internal/contract"
	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

func TestCompareDetectsRegressionAndGuardsMixedAlgorithms(t *testing.T) {
	store, err := storage.OpenEmbedded("")
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	ingestVersion := func(ver string, startupMS float64, jankFrames int64, algo string, n int) {
		events := make([]protocol.Event, 0, n*2)
		for i := 0; i < n; i++ {
			events = append(events,
				protocol.Event{
					EventID:         fmt.Sprintf("%s-%s-st-%d", ver, algo, i),
					Kind:            protocol.KindStartup,
					OccurredAtMS:    1763539990000 + int64(i),
					ContractVersion: 1,
					PolicyID:        "default_v1",
					Payload:         json.RawMessage(fmt.Sprintf(`{"total_duration_ms":%.1f,"native_init_ms":150,"first_frame_ms":120,"cold_start":true,"start_anchor":"application_on_create","end_anchor":"first_content_frame","result":"success"}`, startupMS)),
				},
				protocol.Event{
					EventID:         fmt.Sprintf("%s-%s-fl-%d", ver, algo, i),
					Kind:            protocol.KindFluencyWindow,
					OccurredAtMS:    1763539990000 + int64(i),
					ContractVersion: 1,
					Scene:           "home_feed",
					PolicyID:        "default_v1",
					Payload:         json.RawMessage(fmt.Sprintf(`{"algorithm":%q,"window_duration_ms":30000,"frame_count":1000,"jank_frame_count":%d}`, algo, jankFrames)),
				},
			)
		}
		for start := 0; start < len(events); start += 100 {
			end := start + 100
			if end > len(events) {
				end = len(events)
			}
			_, err := store.IngestBatch(ctx, &protocol.BatchEnvelope{
				SchemaVersion: 1,
				SentAtMS:      1763540000000,
				Resource: protocol.Resource{
					AppID:       "dev.caliper.demo",
					Platform:    "android",
					Env:         "prod",
					AppVersion:  ver,
					BuildNumber: "1",
					Adapter:     "native",
					SDKName:     "caliper-android",
					SDKVersion:  "0.1.0",
					InstallHash: "0123456789abcdef",
				},
				Events: events[start:end],
			})
			if err != nil {
				t.Fatalf("ingest %s: %v", ver, err)
			}
		}
	}

	ingestVersion("1.0.0", 480, 12, "android_frame_metrics_deadline", 120)
	ingestVersion("1.1.0", 1450, 95, "android_frame_metrics_deadline", 120)

	eng := New(store)
	rep, err := eng.Compare(ctx, CompareRequest{
		AppID:            "dev.caliper.demo",
		Platform:         "android",
		Env:              "prod",
		BaselineVersion:  "1.0.0",
		CandidateVersion: "1.1.0",
	})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if rep.Verdict != "regressed" || rep.RegressedCount < 3 {
		t.Fatalf("expected regressed verdict with >=3 regressed metrics, got %+v", rep)
	}

	// Inject a heuristic fluency event into 1.1.0 -> unfiltered fluency comparison MUST gate on incompatible_algorithm!
	ingestVersion("1.1.0", 1450, 20, "android_frame_metrics_heuristic", 5)
	repMixed, err := eng.Compare(ctx, CompareRequest{
		AppID:            "dev.caliper.demo",
		Platform:         "android",
		Env:              "prod",
		BaselineVersion:  "1.0.0",
		CandidateVersion: "1.1.0",
	})
	if err != nil {
		t.Fatalf("compare mixed: %v", err)
	}
	foundGated := false
	for _, m := range repMixed.Metrics {
		if m.MetricID == "fluency_jank_rate_pct" && m.Status == "gated" && m.GateReason == "candidate_"+contract.MissingIncompatibleAlgo {
			foundGated = true
		}
	}
	if !foundGated {
		t.Fatalf("expected fluency metric to be gated on mixed algorithms, got %+v", repMixed.Metrics)
	}
}
