package otlp

import (
	"encoding/json"
	"testing"

	"github.com/caliper-apm/caliper/server/internal/analytics"
	"github.com/caliper-apm/caliper/server/internal/contract"
	"github.com/caliper-apm/caliper/server/internal/protocol"
)

func TestTranslateOTLPTracesLogsAndExportMetrics(t *testing.T) {
	rawTrace := `{
	  "resourceSpans": [{
	    "resource": {
	      "attributes": [
	        {"key": "service.name", "value": {"stringValue": "dev.caliper.demo"}},
	        {"key": "os.name", "value": {"stringValue": "ios"}},
	        {"key": "service.version", "value": {"stringValue": "1.1.0"}},
	        {"key": "app.build_id", "value": {"stringValue": "110"}}
	      ]
	    },
	    "scopeSpans": [{
	      "spans": [
	        {
	          "traceId": "4bf92f3577b34da6a3ce929d0e0e4736",
	          "spanId": "00f067aa0ba902b7",
	          "name": "app.startup.cold",
	          "startTimeUnixNano": "1763540000000000000",
	          "endTimeUnixNano":   "1763540000650000000"
	        },
	        {
	          "traceId": "4bf92f3577b34da6a3ce929d0e0e4736",
	          "spanId": "11f067aa0ba902b8",
	          "name": "GET /api/v1/feed",
	          "startTimeUnixNano": "1763540001000000000",
	          "endTimeUnixNano":   "1763540001320000000",
	          "attributes": [
	            {"key": "http.request.method", "value": {"stringValue": "GET"}},
	            {"key": "http.route", "value": {"stringValue": "/api/v1/feed"}},
	            {"key": "http.response.status_code", "value": {"intValue": "502"}}
	          ],
	          "events": [{
	            "name": "exception",
	            "timeUnixNano": "1763540001310000000",
	            "attributes": [
	              {"key": "exception.type", "value": {"stringValue": "URLError"}},
	              {"key": "exception.message", "value": {"stringValue": "Bad Gateway 502"}},
	              {"key": "exception.escaped", "value": {"boolValue": true}}
	            ]
	          }]
	        }
	      ]
	    }]
	  }]
	}`

	var req ExportTraceServiceRequest
	if err := json.Unmarshal([]byte(rawTrace), &req); err != nil {
		t.Fatalf("unmarshal otlp trace: %v", err)
	}
	batches, err := TranslateTraces(req)
	if err != nil || len(batches) != 1 {
		t.Fatalf("expected 1 batch, got len=%d err=%v", len(batches), err)
	}
	if err := batches[0].Validate(); err != nil {
		t.Fatalf("translated batch failed validation: %v", err)
	}
	if len(batches[0].Events) != 3 {
		t.Fatalf("expected 3 events (startup + network_transport + exception crash), got %d", len(batches[0].Events))
	}
	for i := range batches[0].Events {
		if _, err := protocol.ValidateEvent(&batches[0].Events[i]); err != nil {
			t.Fatalf("event[%d] invalid: %v", i, err)
		}
	}

	rawLogs := `{
	  "resourceLogs": [{
	    "resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "dev.caliper.demo"}}, {"key": "os.name", "value": {"stringValue": "android"}}]},
	    "scopeLogs": [{
	      "logRecords": [
	        {"severityNumber": 9, "severityText": "INFO", "body": {"stringValue": "ignored info"}},
	        {"severityNumber": 21, "severityText": "FATAL", "body": {"stringValue": "OOM crash in BitmapPool"}}
	      ]
	    }]
	  }]
	}`
	var lReq ExportLogsServiceRequest
	if err := json.Unmarshal([]byte(rawLogs), &lReq); err != nil {
		t.Fatalf("unmarshal otlp logs: %v", err)
	}
	lBatches, err := TranslateLogs(lReq)
	if err != nil || len(lBatches) != 1 || len(lBatches[0].Events) != 1 {
		t.Fatalf("expected 1 fatal log event, got %+v err=%v", lBatches, err)
	}

	p90 := 520.0
	sr := 99.4
	exp := ExportOverviewAsOTLPMetrics("dev.caliper.demo", analytics.OverviewReport{
		Platform:   "ios",
		AppVersion: "1.1.0",
		Startup:    analytics.StartupReport{TotalP90: contract.QuantileResult{Value: &p90}},
		Network:    analytics.NetworkReport{SuccessRatePct: &sr},
	})
	rawOut, err := json.Marshal(exp)
	if err != nil || len(rawOut) < 50 {
		t.Fatalf("unexpected otlp export: %s err=%v", string(rawOut), err)
	}
}
