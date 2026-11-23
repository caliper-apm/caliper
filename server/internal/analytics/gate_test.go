package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

func TestEvaluateReleaseGatePassBlockGated(t *testing.T) {
	ctx := context.Background()
	st, err := storage.OpenEmbedded("")
	if err != nil {
		t.Fatalf("open embedded: %v", err)
	}
	defer st.Close()

	now := time.Now().UTC().UnixMilli()
	ingestStartups := func(ver, build string, count int, durMS float64) {
		evs := make([]protocol.Event, 0, count)
		for i := 0; i < count; i++ {
			payload, _ := json.Marshal(protocol.StartupPayload{
				TotalDurationMS: durMS,
				ColdStart:       true,
				StartAnchor:     "application_on_create",
				EndAnchor:       "first_content_frame",
				Result:          "success",
			})
			evs = append(evs, protocol.Event{
				EventID:         fmt.Sprintf("st-%s-%d", ver, i),
				Kind:            protocol.KindStartup,
				OccurredAtMS:    now,
				ContractVersion: 1,
				PolicyID:        "default_v1",
				Payload:         payload,
			})
		}
		if _, err := st.IngestBatch(ctx, &protocol.BatchEnvelope{
			SchemaVersion: 1,
			SentAtMS:      now,
			Resource: protocol.Resource{
				AppID:       "dev.caliper.demo",
				Platform:    "android",
				Env:         "prod",
				AppVersion:  ver,
				BuildNumber: build,
				SDKName:     "caliper-android",
				SDKVersion:  "1.3.0",
				InstallHash: "aabbccdd11223344",
			},
			Events: evs,
		}); err != nil {
			t.Fatalf("ingest %s: %v", ver, err)
		}
	}

	// Under-sampled candidate -> must be GATED (exit code 3), never silently PASS!
	ingestStartups("1.0.0", "100", 120, 500)
	ingestStartups("1.0.1", "101", 10, 520)

	eng := New(st)
	gatedRep, err := eng.EvaluateReleaseGate(ctx, st, ReleaseGateRequest{
		AppID:            "dev.caliper.demo",
		Platform:         "android",
		Env:              "prod",
		BaselineVersion:  "1.0.0",
		CandidateVersion: "1.0.1",
	})
	if err != nil {
		t.Fatalf("gate gated: %v", err)
	}
	if gatedRep.Verdict != "gated" || gatedRep.ExitCode != 3 {
		t.Fatalf("expected gated (exit 3), got verdict=%s exit=%d", gatedRep.Verdict, gatedRep.ExitCode)
	}

	// Regressed candidate -> must BLOCK (exit code 2)
	ingestStartups("1.1.0", "110", 120, 2200)
	blockRep, err := eng.EvaluateReleaseGate(ctx, st, ReleaseGateRequest{
		AppID:            "dev.caliper.demo",
		Platform:         "android",
		Env:              "prod",
		BaselineVersion:  "1.0.0",
		CandidateVersion: "1.1.0",
	})
	if err != nil {
		t.Fatalf("gate block: %v", err)
	}
	if blockRep.Verdict != "block" || blockRep.ExitCode != 2 || blockRep.RegressedCount == 0 {
		t.Fatalf("expected block (exit 2), got verdict=%s exit=%d regressed=%d", blockRep.Verdict, blockRep.ExitCode, blockRep.RegressedCount)
	}
	if !strings.Contains(blockRep.MarkdownSummary, "BLOCK") {
		t.Fatalf("expected BLOCK in markdown summary, got:\n%s", blockRep.MarkdownSummary)
	}

	// Recovered candidate with bound symbol -> must PASS (exit code 0) with score 100
	ingestStartups("1.2.0", "120", 120, 480)
	if _, err := st.SaveSymbol(ctx, storage.SymbolRecord{
		SymbolID:    "sym-android-1.2.0-120",
		AppID:       "dev.caliper.demo",
		Platform:    "android",
		Env:         "prod",
		AppVersion:  "1.2.0",
		BuildNumber: "120",
		Kind:        "r8_mapping",
		SHA256:      "deadbeef",
	}, []byte("com.caliper.App -> a.b:\n")); err != nil {
		t.Fatalf("save symbol: %v", err)
	}
	passRep, err := eng.EvaluateReleaseGate(ctx, st, ReleaseGateRequest{
		AppID:              "dev.caliper.demo",
		Platform:           "android",
		Env:                "prod",
		BaselineVersion:    "1.0.0",
		CandidateVersion:   "1.2.0",
		RequireSymbolBound: true,
	})
	if err != nil {
		t.Fatalf("gate pass: %v", err)
	}
	if passRep.Verdict != "pass" || passRep.ExitCode != 0 || !passRep.CandidateSymbolBound || passRep.HealthScore != 100 {
		t.Fatalf("expected pass score=100 symbolBound=true, got %+v", passRep)
	}
}
