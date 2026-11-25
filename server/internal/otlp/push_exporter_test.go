package otlp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/caliper-apm/caliper/server/internal/protocol"
)

func TestTracePushExporterBatchesAndPushesW3CSpans(t *testing.T) {
	var (
		mu       sync.Mutex
		received []ExportTraceServiceRequest
		gotAuth  string
		gotPath  string
	)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var req ExportTraceServiceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		received = append(received, req)
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	exp := NewTracePushExporter(PushConfig{
		Endpoint:      collector.URL, // should auto-append /v1/traces
		Headers:       ParseHeaderList("Authorization=Bearer tempo-secret"),
		BatchSize:     2,
		FlushInterval: 50 * time.Millisecond,
	})
	if exp == nil {
		t.Fatalf("expected non-nil exporter")
	}

	traceID := "0123456789abcdef0123456789abcdef"
	spanID := "0123456789abcdef"
	exp.EnqueueBatch(&protocol.BatchEnvelope{
		SchemaVersion: 1,
		SentAtMS:      1763540000000,
		Resource: protocol.Resource{
			AppID:       "dev.caliper.mobile",
			Platform:    "ios",
			Env:         "prod",
			AppVersion:  "1.10.0",
			BuildNumber: "1100",
			InstallHash: "abcdef0123456789",
		},
		Events: []protocol.Event{
			{
				EventID:      "evt-push-001",
				Kind:         protocol.KindNetworkStage,
				OccurredAtMS: 1763540000000,
				Scene:        "checkout",
				Payload:      json.RawMessage(`{"operation":"/api/v1/order","method":"POST","status_code":200,"result":"success","total_ms":145.5,"ttfb_ms":92.0,"trace_id":"` + traceID + `","span_id":"` + spanID + `"}`),
			},
			{
				// Missing trace_id -> ignored by trace exporter
				EventID:      "evt-push-002",
				Kind:         protocol.KindNetworkStage,
				OccurredAtMS: 1763540001000,
				Scene:        "checkout",
				Payload:      json.RawMessage(`{"operation":"/api/v1/health","method":"GET","status_code":200,"result":"success","total_ms":12}`),
			},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := exp.Close(ctx); err != nil {
		t.Fatalf("close exporter: %v", err)
	}

	st := exp.Stats()
	if st.Enqueued != 1 || st.Exported != 1 || st.Errors != 0 || st.Dropped != 0 {
		t.Fatalf("unexpected exporter stats: %+v", st)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/traces" || gotAuth != "Bearer tempo-secret" {
		t.Fatalf("unexpected OTLP request path=%q auth=%q", gotPath, gotAuth)
	}
	if len(received) != 1 || len(received[0].ResourceSpans) != 1 || len(received[0].ResourceSpans[0].ScopeSpans[0].Spans) != 1 {
		t.Fatalf("expected 1 pushed OTLP CLIENT span, got %+v", received)
	}
	sp := received[0].ResourceSpans[0].ScopeSpans[0].Spans[0]
	if sp.TraceID != traceID || sp.SpanID != spanID || sp.Kind != 3 {
		t.Fatalf("unexpected pushed span: %+v", sp)
	}
}
