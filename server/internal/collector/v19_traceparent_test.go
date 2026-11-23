package collector

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/caliper-apm/caliper/server/internal/otlp"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

func TestV19W3CTraceparentEndToEndCorrelationAndOTLPExport(t *testing.T) {
	store, err := storage.OpenEmbedded("")
	if err != nil {
		t.Fatalf("open embedded: %v", err)
	}
	defer store.Close()

	h := NewHandler(store, store, Config{})
	ts := httptest.NewServer(h)
	defer ts.Close()

	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	spanID := "00f067aa0ba902b7"

	// 1. Ingest a mobile network_transport sample carrying W3C trace_id & span_id.
	batchBody := `{
		"schema_version": 1,
		"sent_at_ms": 1763550000000,
		"resource": {
			"app_id": "dev.caliper.trace",
			"platform": "android",
			"env": "prod",
			"app_version": "1.9.0",
			"build_number": "190",
			"sdk_name": "caliper-android",
			"sdk_version": "1.9.0",
			"install_hash": "0123456789abcdef0123456789abcdef",
			"device_model": "V2343A",
			"os_version": "14"
		},
		"events": [
			{
				"event_id": "evt-trace-ok-01",
				"kind": "network_transport",
				"occurred_at_ms": 1763550000000,
				"contract_version": 1,
				"scene": "CheckoutActivity",
				"payload": {
					"operation": "/v1/orders/checkout",
					"method": "POST",
					"status_code": 200,
					"result": "success",
					"dns_ms": 4.2,
					"tcp_ms": 12.1,
					"tls_ms": 18.5,
					"ttfb_ms": 65.0,
					"body_ms": 9.4,
					"total_ms": 109.2,
					"protocol": "h2",
					"trace_id": "` + traceID + `",
					"span_id": "` + spanID + `"
				}
			}
		]
	}`

	resp, err := http.Post(ts.URL+"/v1/ingest", "application/json", strings.NewReader(batchBody))
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d", resp.StatusCode)
	}

	// 2. Reject invalid all-zero W3C trace_id strictly.
	badBody := strings.Replace(batchBody, traceID, "00000000000000000000000000000000", 1)
	badBody = strings.Replace(badBody, "evt-trace-ok-01", "evt-trace-bad-01", 1)
	badResp, err := http.Post(ts.URL+"/v1/ingest", "application/json", strings.NewReader(badBody))
	if err != nil {
		t.Fatalf("bad ingest request failed: %v", err)
	}
	defer badResp.Body.Close()
	var badAck struct {
		Accepted int `json:"accepted"`
		Rejected int `json:"rejected"`
	}
	_ = json.NewDecoder(badResp.Body).Decode(&badAck)
	if badAck.Rejected != 1 || badAck.Accepted != 0 {
		t.Fatalf("expected all-zero trace_id to be rejected, got accepted=%d rejected=%d", badAck.Accepted, badAck.Rejected)
	}

	// 3. Query network samples filtered by trace_id.
	qResp, err := http.Get(ts.URL + "/v1/analytics/network/samples?app_id=dev.caliper.trace&platform=android&env=prod&trace_id=" + traceID)
	if err != nil {
		t.Fatalf("query samples by trace_id failed: %v", err)
	}
	defer qResp.Body.Close()
	var qData struct {
		Samples []storage.NetworkTransportSample `json:"samples"`
	}
	if err := json.NewDecoder(qResp.Body).Decode(&qData); err != nil {
		t.Fatalf("decode samples failed: %v", err)
	}
	if len(qData.Samples) != 1 || qData.Samples[0].TraceID != traceID || qData.Samples[0].SpanID != spanID {
		t.Fatalf("expected 1 sample matching W3C trace_id/span_id, got %+v", qData.Samples)
	}

	// 4. Export as standard OTLP/HTTP JSON CLIENT trace spans for Jaeger/Tempo/SkyWalking correlation.
	expResp, err := http.Get(ts.URL + "/v1/otlp/export/traces?app_id=dev.caliper.trace&platform=android&env=prod&trace_id=" + traceID)
	if err != nil {
		t.Fatalf("otlp trace export failed: %v", err)
	}
	defer expResp.Body.Close()
	if expResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on OTLP trace export, got %d", expResp.StatusCode)
	}
	var otlpTraces otlp.ExportTraceServiceRequest
	if err := json.NewDecoder(expResp.Body).Decode(&otlpTraces); err != nil {
		t.Fatalf("decode OTLP traces failed: %v", err)
	}
	if len(otlpTraces.ResourceSpans) != 1 || len(otlpTraces.ResourceSpans[0].ScopeSpans) != 1 || len(otlpTraces.ResourceSpans[0].ScopeSpans[0].Spans) != 1 {
		t.Fatalf("unexpected OTLP trace export structure: %+v", otlpTraces)
	}
	sp := otlpTraces.ResourceSpans[0].ScopeSpans[0].Spans[0]
	if sp.TraceID != traceID || sp.SpanID != spanID || sp.Kind != 3 || sp.Name != "POST /v1/orders/checkout" {
		t.Fatalf("unexpected exported OTLP CLIENT span: %+v", sp)
	}

	// 5. Round-trip exported OTLP trace back into POST /v1/otlp/v1/traces and verify trace_id preserved.
	roundTripBytes, _ := json.Marshal(otlpTraces)
	rtResp, err := http.Post(ts.URL+"/v1/otlp/v1/traces", "application/json", bytes.NewReader(roundTripBytes))
	if err != nil {
		t.Fatalf("otlp round-trip ingest failed: %v", err)
	}
	defer rtResp.Body.Close()
	if rtResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on OTLP round-trip ingest, got %d", rtResp.StatusCode)
	}
}
