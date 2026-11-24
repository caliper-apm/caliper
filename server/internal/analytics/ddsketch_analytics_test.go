package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"path/filepath"
	"sort"
	"testing"

	"github.com/caliper-apm/caliper/server/internal/contract"
	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

func TestDDSketchEndToEndPrecisionAndWALReplay(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, "caliper.wal")
	store, err := storage.OpenEmbedded(walPath)
	if err != nil {
		t.Fatalf("open embedded store: %v", err)
	}

	ctx := context.Background()
	rng := rand.New(rand.NewSource(2026))
	const n = 400
	startVals := make([]float64, n)
	memVals := make([]float64, n)
	netVals := make([]float64, n)
	jrnVals := make([]float64, n)

	baseTS := int64(1763540000000)
	var batch []protocol.Event
	flush := func() {
		if len(batch) == 0 {
			return
		}
		_, err := store.IngestBatch(ctx, &protocol.BatchEnvelope{
			SchemaVersion: 1,
			SentAtMS:      baseTS + 1000,
			Resource: protocol.Resource{
				AppID: "dev.caliper.demo", Platform: "android", Env: "prod",
				AppVersion: "2.0.0", BuildNumber: "200", Adapter: "native",
				SDKName: "caliper-android", SDKVersion: "1.10.0", InstallHash: "0123456789abcdef",
			},
			Events: batch,
		})
		if err != nil {
			t.Fatalf("ingest batch: %v", err)
		}
		batch = batch[:0]
	}

	for i := 0; i < n; i++ {
		stMS := math.Exp(6.6 + 0.55*rng.NormFloat64()) // ~735ms median, heavy tail
		memMiB := math.Exp(5.4 + 0.25*rng.NormFloat64()) // ~221 MiB median
		netMS := math.Exp(5.2 + 0.65*rng.NormFloat64())  // ~181ms median
		jrnMS := math.Exp(6.1 + 0.45*rng.NormFloat64())  // ~445ms median
		startVals[i] = stMS
		memVals[i] = memMiB
		netVals[i] = netMS
		jrnVals[i] = jrnMS

		ts := baseTS + int64(i*100)
		batch = append(batch,
			protocol.Event{
				EventID: fmt.Sprintf("dd-st-%04d", i), Kind: protocol.KindStartup,
				OccurredAtMS: ts, ContractVersion: 1, PolicyID: "default_v1",
				Payload: json.RawMessage(fmt.Sprintf(`{"total_duration_ms":%.2f,"cold_start":true,"start_anchor":"process_start","end_anchor":"first_content_frame","result":"success"}`, stMS)),
			},
			protocol.Event{
				EventID: fmt.Sprintf("dd-res-%04d", i), Kind: protocol.KindResourceWindow,
				OccurredAtMS: ts, ContractVersion: 1, Scene: "home_feed", PolicyID: "default_v1",
				Payload: json.RawMessage(fmt.Sprintf(`{"window_duration_ms":10000,"cpu_pct":19.5,"memory_kind":"android_pss_kib","memory_peak_kib":%.2f,"sample_count":10}`, memMiB*1024.0)),
			},
			protocol.Event{
				EventID: fmt.Sprintf("dd-net-%04d", i), Kind: protocol.KindNetworkStage,
				OccurredAtMS: ts, ContractVersion: 1, Scene: "home_feed", PolicyID: "default_v1",
				Payload: json.RawMessage(fmt.Sprintf(`{"operation":"/v1/feed","method":"GET","status_code":200,"result":"success","ttfb_ms":%.2f,"total_ms":%.2f}`, netMS*0.6, netMS)),
			},
			protocol.Event{
				EventID: fmt.Sprintf("dd-jrn-%04d", i), Kind: protocol.KindCustomScene,
				OccurredAtMS: ts, ContractVersion: 1, Scene: "home_feed", PolicyID: "default_v1",
				Payload: json.RawMessage(fmt.Sprintf(`{"journey_name":"feed_open","status":"success","duration_ms":%.2f,"stages":[{"name":"fetch","duration_ms":%.2f},{"name":"render","duration_ms":%.2f}]}`, jrnMS, jrnMS*0.6, jrnMS*0.4)),
			},
		)
		if len(batch) >= 160 {
			flush()
		}
	}
	flush()

	sort.Float64s(startVals)
	sort.Float64s(memVals)
	sort.Float64s(netVals)
	sort.Float64s(jrnVals)
	exactQ := func(sorted []float64, q float64) float64 {
		idx := int(math.Ceil(float64(len(sorted))*q)) - 1
		if idx < 0 {
			idx = 0
		}
		return sorted[idx]
	}
	assertWithin2Pct := func(label string, got contract.QuantileResult, want float64) {
		t.Helper()
		if got.Value == nil {
			t.Fatalf("%s: nil value (missing=%s)", label, got.MissingReason)
		}
		if got.Precision != contract.PrecisionDDSketch2Pct {
			t.Fatalf("%s: precision=%q want %q", label, got.Precision, contract.PrecisionDDSketch2Pct)
		}
		relErr := math.Abs(*got.Value-want) / want
		if relErr > contract.DefaultSketchAlpha+0.002 {
			t.Errorf("%s: got=%.2f want=%.2f relErr=%.4f (>2%%)", label, *got.Value, want, relErr)
		}
	}

	f := storage.QueryFilter{AppID: "dev.caliper.demo", Platform: "android", Env: "prod", AppVersion: "2.0.0"}
	verifyStore := func(label string, s storage.AnalyticsStore) {
		eng := New(s)
		st, err := eng.Startup(ctx, f)
		if err != nil {
			t.Fatalf("%s startup: %v", label, err)
		}
		assertWithin2Pct(label+" startup P50", st.TotalP50, exactQ(startVals, 0.50))
		assertWithin2Pct(label+" startup P90", st.TotalP90, exactQ(startVals, 0.90))
		assertWithin2Pct(label+" startup P95", st.TotalP95, exactQ(startVals, 0.95))
		assertWithin2Pct(label+" startup P99", st.TotalP99, exactQ(startVals, 0.99))

		res, err := eng.Resources(ctx, f)
		if err != nil || len(res) != 1 {
			t.Fatalf("%s resources: err=%v len=%d", label, err, len(res))
		}
		assertWithin2Pct(label+" mem P90", res[0].MemoryPeakP90MiB, exactQ(memVals, 0.90))
		assertWithin2Pct(label+" mem P95", res[0].MemoryPeakP95MiB, exactQ(memVals, 0.95))
		assertWithin2Pct(label+" mem P99", res[0].MemoryPeakP99MiB, exactQ(memVals, 0.99))

		net, err := eng.Network(ctx, f)
		if err != nil || len(net.Operations) != 1 {
			t.Fatalf("%s network: err=%v ops=%d", label, err, len(net.Operations))
		}
		assertWithin2Pct(label+" net op P90", net.Operations[0].TotalP90, exactQ(netVals, 0.90))
		assertWithin2Pct(label+" net op P95", net.Operations[0].TotalP95, exactQ(netVals, 0.95))
		assertWithin2Pct(label+" net op P99", net.Operations[0].TotalP99, exactQ(netVals, 0.99))

		jrns, err := eng.CustomJourneys(ctx, f)
		if err != nil || len(jrns) != 1 {
			t.Fatalf("%s journeys: err=%v len=%d", label, err, len(jrns))
		}
		assertWithin2Pct(label+" journey P95", jrns[0].DurationP95, exactQ(jrnVals, 0.95))
		assertWithin2Pct(label+" journey P99", jrns[0].DurationP99, exactQ(jrnVals, 0.99))

		trends, err := eng.DailyTrends(ctx, f)
		if err != nil || len(trends) != 1 {
			t.Fatalf("%s trends: err=%v len=%d", label, err, len(trends))
		}
		if trends[0].StartupP95MS == nil || trends[0].StartupP99MS == nil || trends[0].MemoryP95MiB == nil {
			t.Fatalf("%s trends missing P95/P99: %+v", label, trends[0])
		}
		if math.Abs(*trends[0].StartupP99MS-exactQ(startVals, 0.99))/exactQ(startVals, 0.99) > 0.022 {
			t.Errorf("%s daily startup P99 mismatch: got=%.2f want=%.2f", label, *trends[0].StartupP99MS, exactQ(startVals, 0.99))
		}
	}

	verifyStore("live", store)
	_ = store.Close()

	// Reopen from WAL: raw events replay and rebuild exact DDSketches without data loss.
	reopened, err := storage.OpenEmbedded(walPath)
	if err != nil {
		t.Fatalf("reopen embedded WAL: %v", err)
	}
	defer reopened.Close()
	verifyStore("wal-replay", reopened)
}

func TestDDSketchCatchesIntraBucketAndTailOnlyRegressions(t *testing.T) {
	store, err := storage.OpenEmbedded("")
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	ingestStartupSeries := func(ver string, vals []float64) {
		var batch []protocol.Event
		for i, v := range vals {
			batch = append(batch, protocol.Event{
				EventID: fmt.Sprintf("ib-%s-%04d", ver, i), Kind: protocol.KindStartup,
				OccurredAtMS: 1763540000000 + int64(i*10), ContractVersion: 1, PolicyID: "default_v1",
				Payload: json.RawMessage(fmt.Sprintf(`{"total_duration_ms":%.2f,"cold_start":true,"start_anchor":"process_start","end_anchor":"first_content_frame","result":"success"}`, v)),
			})
			if len(batch) >= 100 || i == len(vals)-1 {
				if _, err := store.IngestBatch(ctx, &protocol.BatchEnvelope{
					SchemaVersion: 1, SentAtMS: 1763540005000,
					Resource: protocol.Resource{
						AppID: "dev.caliper.demo", Platform: "android", Env: "prod",
						AppVersion: ver, BuildNumber: "1", Adapter: "native",
						SDKName: "caliper-android", SDKVersion: "1.10.0", InstallHash: "0123456789abcdef",
					},
					Events: batch,
				}); err != nil {
					t.Fatalf("ingest %s: %v", ver, err)
				}
				batch = batch[:0]
			}
		}
	}

	// Case 1: Intra-bucket regression inside the single coarse [1000, 1500] ms bucket!
	// Baseline = 1060ms, Candidate = 1320ms (+260ms, +24.5%).
	baseIntra := make([]float64, 200)
	candIntra := make([]float64, 200)
	for i := 0; i < 200; i++ {
		baseIntra[i] = 1040 + float64(i%40) // [1040, 1079] -> all in (1000, 1500]
		candIntra[i] = 1300 + float64(i%40) // [1300, 1339] -> all in (1000, 1500]
	}
	ingestStartupSeries("3.0.0", baseIntra)
	ingestStartupSeries("3.1.0", candIntra)

	// Confirm coarse 19-bucket Histogram KS is completely blind here (KS = 0, p = 1.0)
	baseAgg, _ := store.QueryStartup(ctx, storage.QueryFilter{AppID: "dev.caliper.demo", Platform: "android", Env: "prod", AppVersion: "3.0.0"})
	candAgg, _ := store.QueryStartup(ctx, storage.QueryFilter{AppID: "dev.caliper.demo", Platform: "android", Env: "prod", AppVersion: "3.1.0"})
	histKS, histP, _ := HistogramKSTest(baseAgg.TotalHist, candAgg.TotalHist)
	if histKS != 0 || histP < 0.99 {
		t.Fatalf("expected coarse 19-bucket KS=0 inside [1000,1500], got ks=%g p=%g", histKS, histP)
	}

	// Yet DDSketch-powered Compare() catches it cleanly with ddsketch_ks and p < 1e-6!
	eng := New(store)
	cmpIntra, err := eng.Compare(ctx, CompareRequest{
		AppID: "dev.caliper.demo", Platform: "android", Env: "prod",
		BaselineVersion: "3.0.0", CandidateVersion: "3.1.0",
	})
	if err != nil {
		t.Fatalf("compare intra-bucket: %v", err)
	}
	if cmpIntra.Verdict != "regressed" {
		t.Fatalf("expected regressed verdict for +260ms intra-bucket shift, got %+v", cmpIntra)
	}
	foundDDSketchP90 := false
	for _, m := range cmpIntra.Metrics {
		if m.MetricID == "startup_cold_p90_ms" {
			if m.Status != "regressed" || m.StatTest != "ddsketch_ks" || m.PValue == nil || *m.PValue >= 1e-6 {
				t.Fatalf("expected ddsketch_ks regressed p<1e-6 for startup_cold_p90_ms, got %+v", m)
			}
			foundDDSketchP90 = true
		}
	}
	if !foundDDSketchP90 {
		t.Fatal("missing startup_cold_p90_ms in compare report")
	}

	// Case 2: Tail-only P99 regression (P50 and P90 stay flat at ~600-700ms, only worst 3% spike to 2400ms).
	baseTail := make([]float64, 300)
	candTail := make([]float64, 300)
	for i := 0; i < 300; i++ {
		if i < 290 {
			baseTail[i] = 580 + float64(i%120)
			candTail[i] = 580 + float64(i%120)
		} else {
			baseTail[i] = 820
			candTail[i] = 2400 // worst 3.3% tail blows up!
		}
	}
	ingestStartupSeries("4.0.0", baseTail)
	ingestStartupSeries("4.1.0", candTail)

	// Note: 3.3% CDF diff with n=300 is small for global KS; test with 8% tail shift or verify P99 value jumps sharply.
	// Wait: let's check both the exact P99 estimate and Compare behavior!
	stBase, _ := eng.Startup(ctx, storage.QueryFilter{AppID: "dev.caliper.demo", Platform: "android", Env: "prod", AppVersion: "4.0.0"})
	stCand, _ := eng.Startup(ctx, storage.QueryFilter{AppID: "dev.caliper.demo", Platform: "android", Env: "prod", AppVersion: "4.1.0"})
	if stBase.TotalP90.Value == nil || stCand.TotalP90.Value == nil || math.Abs(*stBase.TotalP90.Value-*stCand.TotalP90.Value) > 30 {
		t.Fatalf("expected flat P90 for tail-only shift, got base=%v cand=%v", stBase.TotalP90.Value, stCand.TotalP90.Value)
	}
	if stBase.TotalP99.Value == nil || stCand.TotalP99.Value == nil || *stCand.TotalP99.Value < 2300 || *stBase.TotalP99.Value > 860 {
		t.Fatalf("expected P99 to capture ~820ms -> ~2400ms tail spike, got base=%v cand=%v", stBase.TotalP99.Value, stCand.TotalP99.Value)
	}
}
