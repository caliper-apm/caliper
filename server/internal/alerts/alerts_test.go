package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/caliper-apm/caliper/server/internal/analytics"
	"github.com/caliper-apm/caliper/server/internal/contract"
	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

func TestAlertEngineGatingCooldownWebhookAndResolution(t *testing.T) {
	store, err := storage.OpenEmbedded("")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	var mu sync.Mutex
	var received []map[string]any
	hookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var doc map[string]any
		_ = json.Unmarshal(raw, &doc)
		mu.Lock()
		received = append(received, doc)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer hookSrv.Close()

	ctx := context.Background()
	eng := analytics.New(store)
	alertEng := NewEngine(store, store, eng, NewWebhookDispatcher(3*time.Second))

	_, err = store.UpsertAlertRule(ctx, storage.AlertRule{
		RuleID:        "rule-feed-jank",
		Name:          "Feed Jank Spike",
		AppID:         "dev.caliper.demo",
		Platform:      "android",
		Env:           "prod",
		AppVersion:    "1.1.0",
		Scene:         "home_feed",
		MetricID:      "fluency_jank_rate_pct",
		Operator:      "gt",
		Threshold:     5.0,
		MinSamples:    20,
		WebhookURL:    hookSrv.URL,
		WebhookFormat: "feishu",
		CooldownSec:   3600,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("upsert rule: %v", err)
	}

	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	// 1. No observations -> MUST be gated, zero webhooks!
	o1, err := alertEng.EvaluateRule(ctx, "rule-feed-jank", t0)
	if err != nil || o1.State != "gated" || o1.GateReason != contract.MissingNoObservations {
		t.Fatalf("expected gated no_observations, got %+v err=%v", o1, err)
	}

	// 2. Ingest only 10 degraded windows (< MinSamples=20) -> MUST stay gated!
	ingestFluency(t, store, "1.1.0", 10, 1000, 95, "batch-low")
	o2, err := alertEng.EvaluateRule(ctx, "rule-feed-jank", t0.Add(time.Minute))
	if err != nil || o2.State != "gated" || o2.GateReason != contract.MissingInsufficientSamples {
		t.Fatalf("expected gated insufficient_samples, got %+v err=%v", o2, err)
	}

	// 3. Ingest 15 more degraded windows (total 25 >= 20, jank=9.5% > 5.0%) -> MUST fire & deliver Feishu card!
	ingestFluency(t, store, "1.1.0", 15, 1000, 95, "batch-high")
	o3, err := alertEng.EvaluateRule(ctx, "rule-feed-jank", t0.Add(2*time.Minute))
	if err != nil || o3.State != "firing" || o3.Incident == nil || o3.Incident.DeliveryStatus != "delivered" {
		t.Fatalf("expected firing + delivered, got %+v err=%v", o3, err)
	}

	// 4. Re-evaluate within cooldown -> MUST suppress duplicate webhook!
	o4, err := alertEng.EvaluateRule(ctx, "rule-feed-jank", t0.Add(5*time.Minute))
	if err != nil || o4.State != "firing" || o4.Incident == nil || o4.Incident.DeliveryStatus != "skipped_cooldown" {
		t.Fatalf("expected skipped_cooldown, got %+v err=%v", o4, err)
	}

	// 5. Flood healthy windows to bring rate below 5% -> MUST transition to resolved & send recovery webhook!
	ingestFluency(t, store, "1.1.0", 100, 1000, 5, "batch-recover")
	o5, err := alertEng.EvaluateRule(ctx, "rule-feed-jank", t0.Add(10*time.Minute))
	if err != nil || o5.State != "resolved" || o5.Incident == nil || o5.Incident.DeliveryStatus != "delivered" {
		t.Fatalf("expected resolved + delivered, got %+v err=%v", o5, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 2 {
		t.Fatalf("expected exactly 2 webhook deliveries (firing + resolved), got %d", len(received))
	}
	if received[0]["msg_type"] != "interactive" || received[1]["msg_type"] != "interactive" {
		t.Fatalf("expected Feishu interactive card payload, got %+v", received)
	}
}

func ingestFluency(t *testing.T, store *storage.EmbeddedStore, ver string, n int, frames, jank int64, prefix string) {
	t.Helper()
	events := make([]protocol.Event, 0, n)
	for i := 0; i < n; i++ {
		events = append(events, protocol.Event{
			EventID:         fmt.Sprintf("%s-%d", prefix, i),
			Kind:            protocol.KindFluencyWindow,
			OccurredAtMS:    1763540000000 + int64(i),
			ContractVersion: 1,
			Scene:           "home_feed",
			PolicyID:        "default_v1",
			Payload:         json.RawMessage(fmt.Sprintf(`{"algorithm":"android_frame_metrics_deadline","window_duration_ms":30000,"frame_count":%d,"jank_frame_count":%d}`, frames, jank)),
		})
	}
	_, err := store.IngestBatch(context.Background(), &protocol.BatchEnvelope{
		SchemaVersion: 1,
		SentAtMS:      1763540000000,
		Resource: protocol.Resource{
			AppID:       "dev.caliper.demo",
			Platform:    "android",
			Env:         "prod",
			AppVersion:  ver,
			BuildNumber: "110",
			Adapter:     "native",
			SDKName:     "caliper-android",
			SDKVersion:  "0.1.0",
			InstallHash: "0123456789abcdef",
		},
		Events: events,
	})
	if err != nil {
		t.Fatalf("ingest fluency: %v", err)
	}
}
