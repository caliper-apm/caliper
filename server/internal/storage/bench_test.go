package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/caliper-apm/caliper/server/internal/protocol"
)

func BenchmarkEmbeddedIngest100Batch(b *testing.B) {
	store, err := OpenEmbedded("")
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		events := make([]protocol.Event, 100)
		for j := 0; j < 100; j++ {
			events[j] = protocol.Event{
				EventID:         fmt.Sprintf("bench-%d-%d", i, j),
				Kind:            protocol.KindFluencyWindow,
				OccurredAtMS:    1763540000000 + int64(j),
				ContractVersion: 1,
				Scene:           "home_feed",
				PolicyID:        "default_v1",
				Payload:         json.RawMessage(`{"algorithm":"android_frame_metrics_deadline","window_duration_ms":30000,"frame_count":900,"jank_frame_count":18}`),
			}
		}
		_, err := store.IngestBatch(ctx, &protocol.BatchEnvelope{
			SchemaVersion: 1,
			SentAtMS:      1763540000000,
			Resource: protocol.Resource{
				AppID: "dev.caliper.demo", Platform: "android", Env: "prod",
				AppVersion: "1.0.0", BuildNumber: "100", Adapter: "native",
				SDKName: "caliper-android", SDKVersion: "0.1.0", InstallHash: "0123456789abcdef",
			},
			Events: events,
		})
		if err != nil {
			b.Fatalf("ingest: %v", err)
		}
	}
}
