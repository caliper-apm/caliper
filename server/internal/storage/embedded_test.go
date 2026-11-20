package storage

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/caliper-apm/caliper/server/internal/contract"
	"github.com/caliper-apm/caliper/server/internal/protocol"
)

func TestEmbeddedStoreWALPersistenceAndDeduplication(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := OpenEmbedded(dir)
	if err != nil {
		t.Fatalf("open embedded store: %v", err)
	}

	batch := &protocol.BatchEnvelope{
		SchemaVersion: 1,
		SentAtMS:      1763540000000,
		Resource: protocol.Resource{
			AppID:       "dev.caliper.demo",
			Platform:    "android",
			Env:         "prod",
			AppVersion:  "1.0.0",
			BuildNumber: "100",
			Adapter:     "native",
			SDKName:     "caliper-android",
			SDKVersion:  "0.1.0",
			InstallHash: "0123456789abcdef",
		},
		Events: []protocol.Event{
			{
				EventID:         "evt-persist-001",
				Kind:            protocol.KindStartup,
				OccurredAtMS:    1763539990000,
				ContractVersion: 1,
				PolicyID:        "default_v1",
				Payload:         json.RawMessage(`{"total_duration_ms":420,"native_init_ms":120,"first_frame_ms":90,"cold_start":true,"start_anchor":"application_on_create","end_anchor":"first_content_frame","result":"success"}`),
			},
			{
				EventID:         "evt-persist-002",
				Kind:            protocol.KindFluencyWindow,
				OccurredAtMS:    1763539991000,
				ContractVersion: 1,
				Scene:           "home_feed",
				PolicyID:        "default_v1",
				Payload:         json.RawMessage(`{"algorithm":"android_frame_metrics_deadline","window_duration_ms":30000,"frame_count":0,"jank_frame_count":0}`),
			},
		},
	}

	r1, err := store.IngestBatch(ctx, batch)
	if err != nil {
		t.Fatalf("ingest batch: %v", err)
	}
	if r1.Accepted != 2 || r1.Duplicates != 0 {
		t.Fatalf("unexpected first ingest result: %+v", r1)
	}

	// Re-ingest exact same batch: must deduplicate cleanly.
	r2, err := store.IngestBatch(ctx, batch)
	if err != nil {
		t.Fatalf("re-ingest batch: %v", err)
	}
	if r2.Accepted != 0 || r2.Duplicates != 2 {
		t.Fatalf("expected 2 duplicates, got %+v", r2)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Reopen from disk WAL and verify full recovery.
	reopened, err := OpenEmbedded(dir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()

	st, err := reopened.QueryStartup(ctx, QueryFilter{AppID: "dev.caliper.demo", Platform: "android", Env: "prod"})
	if err != nil {
		t.Fatalf("query startup: %v", err)
	}
	if st.SuccessCount != 1 || st.StagedCount != 1 {
		t.Fatalf("expected 1 recovered startup event, got %+v", st)
	}
	if st.TotalHist[contract.DurationBucketIndex(420)] != 1 {
		t.Fatalf("startup histogram bucket mismatch after WAL recovery")
	}

	fl, err := reopened.QueryFluency(ctx, QueryFilter{AppID: "dev.caliper.demo", Platform: "android", Env: "prod"})
	if err != nil || len(fl) != 1 || fl[0].ZeroFrameWindows != 1 || fl[0].ValidWindows != 0 {
		t.Fatalf("expected 1 zero-frame fluency window after WAL recovery, got %+v (err=%v)", fl, err)
	}

	rels, err := reopened.ListReleases(ctx, "dev.caliper.demo", "android", "prod")
	if err != nil || len(rels) != 1 || rels[0].AppVersion != "1.0.0" {
		t.Fatalf("expected auto-discovered release 1.0.0, got %+v (err=%v)", rels, err)
	}
}
