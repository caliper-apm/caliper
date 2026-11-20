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

func TestStatisticalTestsMath(t *testing.T) {
	// 1. Two-proportion Z-test: large jank jump (20/2000 = 1% -> 200/2000 = 10%)
	pSig, ok := TwoProportionZTest(20, 2000, 200, 2000)
	if !ok || pSig >= 1e-6 {
		t.Fatalf("expected p < 1e-6 for 1%% vs 10%%, got p=%g ok=%v", pSig, ok)
	}
	// Identical proportions must give p = 1.0
	pSame, ok := TwoProportionZTest(50, 1000, 50, 1000)
	if !ok || pSame < 0.99 {
		t.Fatalf("expected p ~ 1.0 for equal proportions, got p=%g", pSame)
	}

	// Wilson 95% CI sanity
	ci := WilsonCI95(50, 1000)
	if ci == nil || ci[0] >= 5.0 || ci[1] <= 5.0 {
		t.Fatalf("expected Wilson CI around 5%%, got %+v", ci)
	}

	// 2. Histogram KS test: shifted startup latency
	var hBase, hCand contract.Histogram
	hBase[5] = 200 // 500ms bucket
	hCand[9] = 200 // 2000ms bucket
	ks, pKS, ok := HistogramKSTest(hBase, hCand)
	if !ok || ks < 0.99 || pKS >= 1e-6 {
		t.Fatalf("expected KS~1.0 and p<1e-6, got ks=%g p=%g ok=%v", ks, pKS, ok)
	}
	ks0, p0, ok := HistogramKSTest(hBase, hBase)
	if !ok || ks0 != 0 || p0 < 0.99 {
		t.Fatalf("expected KS=0 and p=1.0 for identical histograms, got ks=%g p=%g", ks0, p0)
	}
}

func TestScanRegressionsRanksPerSceneFindings(t *testing.T) {
	store, err := storage.OpenEmbedded("")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	ingestScene := func(ver, scene string, jankFrames int64, memKiB float64, startupMS float64) {
		var events []protocol.Event
		for i := 0; i < 120; i++ {
			events = append(events,
				protocol.Event{
					EventID:         fmt.Sprintf("st-%s-%s-%03d", ver, scene, i),
					Kind:            protocol.KindStartup,
					OccurredAtMS:    1763540000000 + int64(i*1000),
					ContractVersion: 1,
					Scene:           scene,
					Payload:         json.RawMessage(fmt.Sprintf(`{"total_duration_ms":%.0f,"cold_start":true,"start_anchor":"process_start","end_anchor":"first_content_frame","result":"success"}`, startupMS)),
				},
				protocol.Event{
					EventID:         fmt.Sprintf("fl-%s-%s-%03d", ver, scene, i),
					Kind:            protocol.KindFluencyWindow,
					OccurredAtMS:    1763540000000 + int64(i*1000),
					ContractVersion: 1,
					Scene:           scene,
					Payload:         json.RawMessage(fmt.Sprintf(`{"algorithm":"android_frame_metrics_deadline","window_duration_ms":10000,"frame_count":600,"jank_frame_count":%d}`, jankFrames)),
				},
				protocol.Event{
					EventID:         fmt.Sprintf("res-%s-%s-%03d", ver, scene, i),
					Kind:            protocol.KindResourceWindow,
					OccurredAtMS:    1763540000000 + int64(i*1000),
					ContractVersion: 1,
					Scene:           scene,
					Payload:         json.RawMessage(fmt.Sprintf(`{"window_duration_ms":10000,"cpu_pct":18,"memory_kind":"android_pss_kib","memory_peak_kib":%.0f,"sample_count":10}`, memKiB)),
				},
			)
			if len(events) >= 180 {
				_, err := store.IngestBatch(ctx, &protocol.BatchEnvelope{
					SchemaVersion: 1,
					SentAtMS:      1763540000000,
					Resource: protocol.Resource{
						AppID: "dev.caliper.demo", Platform: "android", Env: "prod",
						AppVersion: ver, BuildNumber: "100", Adapter: "native",
						SDKName: "caliper-android", SDKVersion: "0.5.0", InstallHash: "0123456789abcdef",
					},
					Events: events,
				})
				if err != nil {
					t.Fatalf("ingest %s: %v", ver, err)
				}
				events = events[:0]
			}
		}
		if len(events) > 0 {
			_, err := store.IngestBatch(ctx, &protocol.BatchEnvelope{
				SchemaVersion: 1,
				SentAtMS:      1763540000000,
				Resource: protocol.Resource{
					AppID: "dev.caliper.demo", Platform: "android", Env: "prod",
					AppVersion: ver, BuildNumber: "100", Adapter: "native",
					SDKName: "caliper-android", SDKVersion: "0.5.0", InstallHash: "0123456789abcdef",
				},
				Events: events,
			})
			if err != nil {
				t.Fatalf("ingest flush %s: %v", ver, err)
			}
		}
	}

	// Baseline 1.0.0: healthy home_feed and healthy settings
	ingestScene("1.0.0", "home_feed", 6, 180*1024, 520)
	ingestScene("1.0.0", "settings", 6, 170*1024, 520)

	// Candidate 1.1.0: regressed home_feed (jank 1% -> 12%, memory 180 -> 420 MiB, startup 520 -> 1800ms), settings unchanged
	ingestScene("1.1.0", "home_feed", 72, 420*1024, 1800)
	ingestScene("1.1.0", "settings", 6, 170*1024, 520)

	eng := New(store)
	scan, err := eng.ScanRegressions(ctx, CompareRequest{
		AppID:            "dev.caliper.demo",
		Platform:         "android",
		Env:              "prod",
		BaselineVersion:  "1.0.0",
		CandidateVersion: "1.1.0",
		MinSamples:       100,
	})
	if err != nil {
		t.Fatalf("scan regressions: %v", err)
	}
	if scan.Verdict != "regressed" || scan.RegressedCount < 3 {
		t.Fatalf("expected regressed with >=3 findings, got verdict=%s count=%d", scan.Verdict, scan.RegressedCount)
	}
	foundHomeFeedScene := false
	for _, f := range scan.Findings {
		if f.Scope == "scene:home_feed" && f.Comparison.Status == "regressed" && f.Comparison.PValue != nil && *f.Comparison.PValue < 0.01 {
			foundHomeFeedScene = true
		}
		if f.Scope == "scene:settings" && f.Comparison.Status == "regressed" {
			t.Fatalf("healthy settings scene should not be flagged as regressed: %+v", f)
		}
	}
	if !foundHomeFeedScene {
		t.Fatalf("expected statistically significant scene:home_feed finding, got %+v", scan.Findings)
	}
}
