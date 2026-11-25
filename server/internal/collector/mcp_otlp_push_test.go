package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caliper-apm/caliper/server/internal/analytics"
	"github.com/caliper-apm/caliper/server/internal/auth"
	"github.com/caliper-apm/caliper/server/internal/otlp"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

func TestCollectorMCPAndOTLPTracePushE2E(t *testing.T) {
	var (
		mu          sync.Mutex
		pushedSpans int
	)
	otlpCollector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req otlp.ExportTraceServiceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				pushedSpans += len(ss.Spans)
			}
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer otlpCollector.Close()

	store, err := storage.OpenEmbedded("")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	exporter := otlp.NewTracePushExporter(otlp.PushConfig{
		Endpoint:      otlpCollector.URL,
		BatchSize:     1,
		FlushInterval: 20 * time.Millisecond,
	})
	handler := NewHandler(store, store, Config{TraceExporter: exporter})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	// 1. Ingest a batch with W3C trace_id/span_id and verify automatic push export.
	batchJSON := `{
		"schema_version": 1,
		"sent_at_ms": 1763540000000,
		"resource": {
			"app_id": "dev.caliper.e2e",
			"platform": "android",
			"env": "prod",
			"app_version": "1.10.0",
			"build_number": "1100",
			"adapter": "native",
			"sdk_name": "caliper-android",
			"sdk_version": "1.10.0",
			"install_hash": "0123456789abcdef"
		},
		"events": [{
			"event_id": "evt-otlp-push-1",
			"kind": "network_transport",
			"occurred_at_ms": 1763540000000,
			"contract_version": 1,
			"scene": "checkout",
			"payload": {
				"operation": "/api/v1/pay",
				"method": "POST",
				"status_code": 200,
				"result": "success",
				"total_ms": 210,
				"trace_id": "abcdef0123456789abcdef0123456789",
				"span_id": "abcdef0123456789"
			}
		}]
	}`
	ingResp, err := http.Post(ts.URL+"/v1/ingest", "application/json", strings.NewReader(batchJSON))
	if err != nil || ingResp.StatusCode != http.StatusAccepted {
		t.Fatalf("ingest failed: status=%v err=%v", ingResp, err)
	}
	_ = ingResp.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := exporter.Close(ctx); err != nil {
		t.Fatalf("close exporter: %v", err)
	}

	mu.Lock()
	gotSpans := pushedSpans
	mu.Unlock()
	if gotSpans != 1 {
		t.Fatalf("expected 1 pushed OTLP span from ingest, got %d", gotSpans)
	}

	// 2. Call POST /v1/mcp initialize + tools/call (caliper_overview)
	mcpBody := bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"caliper_overview","arguments":{"app_id":"dev.caliper.e2e","platform":"android"}}}`)
	mcpResp, err := http.Post(ts.URL+"/v1/mcp", "application/json", mcpBody)
	if err != nil || mcpResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/mcp failed: resp=%v err=%v", mcpResp, err)
	}
	defer mcpResp.Body.Close()
	var rpcOut map[string]any
	if err := json.NewDecoder(mcpResp.Body).Decode(&rpcOut); err != nil {
		t.Fatalf("decode mcp response: %v", err)
	}
	if rpcOut["error"] != nil {
		t.Fatalf("unexpected MCP RPC error: %+v", rpcOut["error"])
	}
}

func TestMCPRBACTenantScopingOriginGuardAndBinarySizeGate(t *testing.T) {
	store, err := storage.OpenEmbedded("")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	authz, err := auth.NewRBAC([]auth.KeySpec{
		{Key: "k-alpha-viewer", Actor: "viewer-alpha", Role: auth.RoleViewer, AppIDs: []string{"app.alpha"}},
		{Key: "k-admin", Actor: "admin", Role: auth.RoleAdmin},
	})
	if err != nil {
		t.Fatalf("new rbac: %v", err)
	}
	ts := httptest.NewServer(NewHandler(store, store, Config{Authorizer: authz}))
	defer ts.Close()

	doReq := func(method, path, key, origin, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("X-Caliper-Key", key)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do %s %s: %v", method, path, err)
		}
		return resp
	}

	// 1. Cross-origin browser request to /v1/mcp is blocked with 403 forbidden_origin.
	evilResp := doReq(http.MethodPost, "/v1/mcp", "k-admin", "https://evil.example", `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	evilResp.Body.Close()
	if evilResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for untrusted Origin on /v1/mcp, got %d", evilResp.StatusCode)
	}

	// 2. Notification on /v1/mcp returns 202 Accepted + MCP-Protocol-Version header.
	notifResp := doReq(http.MethodPost, "/v1/mcp", "k-admin", "http://localhost:3000", `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	notifResp.Body.Close()
	if notifResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted for MCP notification, got %d", notifResp.StatusCode)
	}
	if notifResp.Header.Get("MCP-Protocol-Version") == "" {
		t.Fatalf("expected MCP-Protocol-Version response header")
	}

	// 3. Tenant-scoped RBAC key cannot read app.beta via JSON body on /v1/mcp, /v1/analytics/compare, or /v1/analytics/release-gate.
	crossMCP := doReq(http.MethodPost, "/v1/mcp", "k-alpha-viewer", "", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"caliper_overview","arguments":{"app_id":"app.beta","platform":"android"}}}`)
	var mcpPayload struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	_ = json.NewDecoder(crossMCP.Body).Decode(&mcpPayload)
	crossMCP.Body.Close()
	if !mcpPayload.Result.IsError || len(mcpPayload.Result.Content) == 0 || !strings.Contains(mcpPayload.Result.Content[0].Text, "forbidden_app_scope") {
		t.Fatalf("expected MCP forbidden_app_scope for cross-tenant call, got %+v", mcpPayload)
	}

	cmpResp := doReq(http.MethodPost, "/v1/analytics/compare", "k-alpha-viewer", "", `{"app_id":"app.beta","platform":"android","baseline_version":"1.0.0","candidate_version":"1.1.0"}`)
	cmpResp.Body.Close()
	if cmpResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 on cross-tenant /v1/analytics/compare, got %d", cmpResp.StatusCode)
	}

	gateResp := doReq(http.MethodPost, "/v1/analytics/release-gate", "k-alpha-viewer", "", `{"app_id":"app.beta","platform":"android","baseline_version":"1.0.0","candidate_version":"1.1.0"}`)
	gateResp.Body.Close()
	if gateResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 on cross-tenant /v1/analytics/release-gate, got %d", gateResp.StatusCode)
	}

	// 4. Release Binary Size registration validation + Zero-Lie same-kind vs mismatched-kind comparison.
	badKind := doReq(http.MethodPost, "/v1/releases/register", "k-admin", "", `{"app_id":"app.alpha","platform":"android","env":"prod","app_version":"1.0.0","build_number":"100","binary_size_bytes":1000,"binary_artifact_kind":"exe"}`)
	badKind.Body.Close()
	if badKind.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid binary_artifact_kind, got %d", badKind.StatusCode)
	}

	for _, body := range []string{
		`{"app_id":"app.alpha","platform":"android","env":"prod","app_version":"1.0.0","build_number":"100","binary_size_bytes":25000000,"binary_artifact_kind":"apk"}`,
		`{"app_id":"app.alpha","platform":"android","env":"prod","app_version":"1.1.0","build_number":"110","binary_size_bytes":27500000,"binary_artifact_kind":"apk"}`,
		`{"app_id":"app.alpha","platform":"android","env":"prod","app_version":"1.2.0","build_number":"120","binary_size_bytes":19000000,"binary_artifact_kind":"aab"}`,
	} {
		r := doReq(http.MethodPost, "/v1/releases/register", "k-admin", "", body)
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatalf("register release failed: status=%d body=%s", r.StatusCode, body)
		}
	}

	// Same artifact kind (apk vs apk) -> comparable=true with exact +2.5 MB delta.
	okGate := doReq(http.MethodPost, "/v1/analytics/release-gate", "k-alpha-viewer", "", `{"app_id":"app.alpha","platform":"android","baseline_version":"1.0.0","candidate_version":"1.1.0"}`)
	var gateRep analytics.ReleaseGateReport
	_ = json.NewDecoder(okGate.Body).Decode(&gateRep)
	okGate.Body.Close()
	if gateRep.BinarySize == nil || !gateRep.BinarySize.Comparable || gateRep.BinarySize.DeltaBytes == nil || *gateRep.BinarySize.DeltaBytes != 2500000 {
		t.Fatalf("expected comparable +2500000 byte apk delta, got %+v", gateRep.BinarySize)
	}

	// Mismatched artifact kind (apk vs aab) -> Zero-Lie comparable=false, reason=artifact_kind_mismatch!
	misGate := doReq(http.MethodPost, "/v1/analytics/release-gate", "k-alpha-viewer", "", `{"app_id":"app.alpha","platform":"android","baseline_version":"1.0.0","candidate_version":"1.2.0"}`)
	var misRep analytics.ReleaseGateReport
	_ = json.NewDecoder(misGate.Body).Decode(&misRep)
	misGate.Body.Close()
	if misRep.BinarySize == nil || misRep.BinarySize.Comparable || misRep.BinarySize.Reason != "artifact_kind_mismatch" {
		t.Fatalf("expected Zero-Lie artifact_kind_mismatch for apk vs aab, got %+v", misRep.BinarySize)
	}
}
