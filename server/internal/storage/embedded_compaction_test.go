package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/caliper-apm/caliper/server/internal/contract"
	"github.com/caliper-apm/caliper/server/internal/protocol"
)

func TestEmbeddedBoundedSeenEventsFIFO(t *testing.T) {
	store, err := OpenEmbedded("")
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	defer store.Close()
	store.SetMaxSeenEvents(5)

	ctx := context.Background()
	makeBatch := func(id string, ms float64) *protocol.BatchEnvelope {
		return &protocol.BatchEnvelope{
			SchemaVersion: 1,
			SentAtMS:      1763540000000,
			Resource: protocol.Resource{
				AppID:       "dev.caliper.test",
				Platform:    "android",
				Env:         "prod",
				AppVersion:  "1.0.0",
				BuildNumber: "100",
				Adapter:     "native",
				SDKName:     "caliper-android",
				SDKVersion:  "1.10.0",
				InstallHash: "0123456789abcdef",
			},
			Events: []protocol.Event{
				{
					EventID:         id,
					Kind:            protocol.KindStartup,
					OccurredAtMS:    1763540000000,
					ContractVersion: 1,
					Scene:           "startup",
					Payload:         json.RawMessage(fmt.Sprintf(`{"total_duration_ms":%.1f,"cold_start":true,"start_anchor":"process_start","end_anchor":"first_content_frame","result":"success"}`, ms)),
				},
			},
		}
	}

	// Insert 8 distinct events into a bounded ring of capacity 5.
	for i := 1; i <= 8; i++ {
		res, err := store.IngestBatch(ctx, makeBatch(fmt.Sprintf("evt-fifo-%03d", i), float64(400+i*10)))
		if err != nil || res.Accepted != 1 {
			t.Fatalf("ingest evt-fifo-%03d failed: res=%+v err=%v", i, res, err)
		}
	}

	if len(store.seenEvents) != 5 {
		t.Fatalf("expected bounded seenEvents len=5, got %d", len(store.seenEvents))
	}
	// Recent event evt-fifo-008 must still be deduplicated.
	dupRes, err := store.IngestBatch(ctx, makeBatch("evt-fifo-008", 480))
	if err != nil || dupRes.Duplicates != 1 || dupRes.Accepted != 0 {
		t.Fatalf("expected evt-fifo-008 to be deduplicated, got res=%+v err=%v", dupRes, err)
	}
	// Evicted oldest event evt-fifo-001 is no longer retained in bounded dedup ring.
	if _, ok := store.seenEvents["evt-fifo-001"]; ok {
		t.Fatalf("expected oldest evt-fifo-001 to be evicted from bounded dedup map")
	}
}

func TestEmbeddedSnapshotCompactionAndReloadParity(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenEmbedded(dir)
	if err != nil {
		t.Fatalf("open embedded store: %v", err)
	}
	store.SetAutoCompactBytes(0) // test explicit compaction first

	ctx := context.Background()
	ingestStartups := func(prefix string, count int, baseMS float64) {
		events := make([]protocol.Event, 0, count)
		for i := 0; i < count; i++ {
			events = append(events, protocol.Event{
				EventID:         fmt.Sprintf("%s-%03d", prefix, i),
				Kind:            protocol.KindStartup,
				OccurredAtMS:    1763540000000 + int64(i*1000),
				ContractVersion: 1,
				Scene:           "startup",
				Payload:         json.RawMessage(fmt.Sprintf(`{"total_duration_ms":%.1f,"native_init_ms":120,"first_frame_ms":200,"cold_start":true,"start_anchor":"process_start","end_anchor":"first_content_frame","result":"success"}`, baseMS+float64(i))),
			})
		}
		_, err := store.IngestBatch(ctx, &protocol.BatchEnvelope{
			SchemaVersion: 1,
			SentAtMS:      1763540000000,
			Resource: protocol.Resource{
				AppID:       "dev.caliper.compact",
				Platform:    "ios",
				Env:         "prod",
				AppVersion:  "1.10.0",
				BuildNumber: "1100",
				Adapter:     "native",
				SDKName:     "caliper-ios",
				SDKVersion:  "1.10.0",
				InstallHash: "abcdef0123456789",
			},
			Events: events,
		})
		if err != nil {
			t.Fatalf("ingest %s: %v", prefix, err)
		}
	}

	ingestStartups("evt-pre", 40, 600)
	walPath := filepath.Join(dir, "events.wal.jsonl")
	stBefore, err := os.Stat(walPath)
	if err != nil || stBefore.Size() == 0 {
		t.Fatalf("expected non-empty WAL before compaction, err=%v", err)
	}

	if err := store.Compact(); err != nil {
		t.Fatalf("compact failed: %v", err)
	}
	stAfter, err := os.Stat(walPath)
	if err != nil || stAfter.Size() != 0 {
		t.Fatalf("expected WAL truncated to 0 bytes after compaction, got size=%d err=%v", stAfter.Size(), err)
	}
	if _, err := os.Stat(filepath.Join(dir, "rollup.snapshot.json.gz")); err != nil {
		t.Fatalf("expected rollup.snapshot.json.gz to exist: %v", err)
	}

	// Write additional tail events AFTER snapshot compaction.
	ingestStartups("evt-post", 20, 900)
	beforeAgg, err := store.QueryStartup(ctx, QueryFilter{AppID: "dev.caliper.compact", Platform: "ios", AppVersion: "1.10.0"})
	if err != nil {
		t.Fatalf("query before close: %v", err)
	}
	beforeP95 := contract.QuantileDurationSketch(beforeAgg.TotalSketch, beforeAgg.TotalHist, 0.95, 10)
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Reopen from snapshot + post-compact WAL tail.
	reopened, err := OpenEmbedded(dir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()

	afterAgg, err := reopened.QueryStartup(ctx, QueryFilter{AppID: "dev.caliper.compact", Platform: "ios", AppVersion: "1.10.0"})
	if err != nil {
		t.Fatalf("query after reopen: %v", err)
	}
	afterP95 := contract.QuantileDurationSketch(afterAgg.TotalSketch, afterAgg.TotalHist, 0.95, 10)
	if afterAgg.SuccessCount != 60 || afterAgg.TotalSketch == nil || afterAgg.TotalSketch.Total() != 60 {
		t.Fatalf("expected 60 compacted+replayed samples, got %+v", afterAgg)
	}
	if beforeP95.Value == nil || afterP95.Value == nil || *beforeP95.Value != *afterP95.Value {
		t.Fatalf("DDSketch P95 mismatch across compaction+reload: before=%+v after=%+v", beforeP95, afterP95)
	}

	// Verify pre-compact event IDs are still deduplicated after reload.
	dup, err := reopened.IngestBatch(ctx, &protocol.BatchEnvelope{
		SchemaVersion: 1,
		SentAtMS:      1763540000000,
		Resource: protocol.Resource{
			AppID:       "dev.caliper.compact",
			Platform:    "ios",
			Env:         "prod",
			AppVersion:  "1.10.0",
			BuildNumber: "1100",
			Adapter:     "native",
			SDKName:     "caliper-ios",
			SDKVersion:  "1.10.0",
			InstallHash: "abcdef0123456789",
		},
		Events: []protocol.Event{
			{
				EventID:         "evt-pre-000",
				Kind:            protocol.KindStartup,
				OccurredAtMS:    1763540000000,
				ContractVersion: 1,
				Scene:           "startup",
				Payload:         json.RawMessage(`{"total_duration_ms":600,"cold_start":true,"start_anchor":"process_start","end_anchor":"first_content_frame","result":"success"}`),
			},
		},
	})
	if err != nil || dup.Duplicates != 1 {
		t.Fatalf("expected pre-compact event ID to remain deduplicated after snapshot reload, got res=%+v err=%v", dup, err)
	}
}
