package collector

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caliper-apm/caliper/server/internal/auth"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

func TestHTTPGzipIngestAndQueryLifecycle(t *testing.T) {
	store, err := storage.OpenEmbedded(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	h := NewHandler(store, store, Config{APIKey: "test-secret"})
	ts := httptest.NewServer(h)
	defer ts.Close()

	payload := `{
	  "schema_version": 1,
	  "sent_at_ms": 1763540000000,
	  "resource": {
	    "app_id": "dev.caliper.demo",
	    "platform": "ios",
	    "env": "prod",
	    "app_version": "1.0.0",
	    "build_number": "10",
	    "adapter": "native",
	    "sdk_name": "caliper-ios",
	    "sdk_version": "0.1.0",
	    "install_hash": "fedcba9876543210"
	  },
	  "events": [
	    {
	      "event_id": "ios-evt-0001",
	      "kind": "crash",
	      "occurred_at_ms": 1763539990000,
	      "contract_version": 1,
	      "scene": "media_gallery",
	      "payload": {
	        "fingerprint": "fp-ios-null-deref",
	        "title": "EXC_BAD_ACCESS in ImageDecoder.decodeFrame",
	        "fatal": true,
	        "frames": [{"module": "DemoApp", "symbol": "ImageDecoder.decodeFrame", "file": "ImageDecoder.swift", "line": 84, "in_app": true}]
	      }
	    }
	  ]
	}`

	// Unauthorized request must be rejected.
	reqBad, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/ingest", bytes.NewBufferString(payload))
	respBad, err := http.DefaultClient.Do(reqBad)
	if err != nil || respBad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without API key, got status=%v err=%v", respBad.StatusCode, err)
	}
	respBad.Body.Close()

	// Gzipped authorized request must succeed.
	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	_, _ = gz.Write([]byte(payload))
	_ = gz.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/ingest", &gzBuf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("X-Caliper-Key", "test-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post ingest: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	// Read back stability issue.
	respIssue, err := http.Get(ts.URL + "/v1/stability/issues/fp-ios-null-deref?platform=ios")
	if err != nil {
		t.Fatalf("get issue: %v", err)
	}
	defer respIssue.Body.Close()
	if respIssue.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for issue detail, got %d", respIssue.StatusCode)
	}
	var issue storage.IssueSummary
	if err := json.NewDecoder(respIssue.Body).Decode(&issue); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	if issue.EventCount != 1 || issue.AffectedInstalls != 1 || len(issue.SampleFrames) != 1 {
		t.Fatalf("unexpected issue summary: %+v", issue)
	}
}

func TestConsoleStaticServingAndSecurity(t *testing.T) {
	store, err := storage.OpenEmbedded(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	dist := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<!doctype html><title>Caliper APM</title>"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dist, "assets", "app.js"), []byte("console.log('ok');"), 0o644); err != nil {
		t.Fatalf("write app.js: %v", err)
	}

	h := NewHandler(store, store, Config{ConsoleDistDir: dist})
	ts := httptest.NewServer(h)
	defer ts.Close()

	// 1. Root serves index.html.
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Caliper APM") {
		t.Fatalf("expected index.html, got status=%d body=%q", resp.StatusCode, string(body))
	}

	// 2. Hashed asset gets immutable cache header.
	respAsset, err := http.Get(ts.URL + "/assets/app.js")
	if err != nil {
		t.Fatalf("GET /assets/app.js: %v", err)
	}
	respAsset.Body.Close()
	if respAsset.StatusCode != http.StatusOK || !strings.Contains(respAsset.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("expected immutable asset cache, got status=%d cc=%q", respAsset.StatusCode, respAsset.Header.Get("Cache-Control"))
	}

	// 3. SPA route falls back to index.html.
	respSPA, err := http.Get(ts.URL + "/stability/issues")
	if err != nil {
		t.Fatalf("GET /stability/issues: %v", err)
	}
	spaBody, _ := io.ReadAll(respSPA.Body)
	respSPA.Body.Close()
	if respSPA.StatusCode != http.StatusOK || !strings.Contains(string(spaBody), "Caliper APM") {
		t.Fatalf("expected SPA fallback, got status=%d", respSPA.StatusCode)
	}

	// 4. Unknown /v1/* route returns JSON 404, not SPA HTML.
	respAPI404, err := http.Get(ts.URL + "/v1/does-not-exist")
	if err != nil {
		t.Fatalf("GET /v1/does-not-exist: %v", err)
	}
	respAPI404.Body.Close()
	if respAPI404.StatusCode != http.StatusNotFound || !strings.Contains(respAPI404.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("expected JSON 404 for unknown /v1 route, got status=%d ct=%q", respAPI404.StatusCode, respAPI404.Header.Get("Content-Type"))
	}

	// 5. Direct traversal call is rejected with 400.
	rec := httptest.NewRecorder()
	reqTrav := httptest.NewRequest(http.MethodGet, "/assets/%2e%2e/%2e%2e/etc/passwd", nil)
	h.handleConsoleStatic(rec, reqTrav)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on traversal path, got %d", rec.Code)
	}
}

func TestRBACAuditedSessionLookupAndAuditLogs(t *testing.T) {
	store, err := storage.OpenEmbedded(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	authz, err := auth.NewRBAC([]auth.KeySpec{
		{Key: "ingest-key", Actor: "sdk-client", Role: auth.RoleIngestOnly, AppIDs: []string{"dev.caliper.demo"}},
		{Key: "viewer-key", Actor: "analyst-1", Role: auth.RoleViewer},
		{Key: "operator-key", Actor: "oncall-1", Role: auth.RoleOperator},
		{Key: "admin-key", Actor: "security-1", Role: auth.RoleAdmin},
	})
	if err != nil {
		t.Fatalf("new rbac: %v", err)
	}

	h := NewHandler(store, store, Config{Authorizer: authz})
	ts := httptest.NewServer(h)
	defer ts.Close()

	// 1. Ingest event with hashed user_hash using ingest-key
	payload := `{
	  "schema_version": 1,
	  "sent_at_ms": 1763540000000,
	  "resource": {
	    "app_id": "dev.caliper.demo",
	    "platform": "android",
	    "env": "prod",
	    "app_version": "1.1.0",
	    "build_number": "110",
	    "adapter": "native",
	    "sdk_name": "caliper-android",
	    "sdk_version": "0.5.0",
	    "install_hash": "1111222233334444",
	    "user_hash": "aaaabbbbccccdddd"
	  },
	  "events": [
	    {
	      "event_id": "rbac-evt-0001",
	      "kind": "startup",
	      "occurred_at_ms": 1763540000000,
	      "contract_version": 1,
	      "scene": "home_feed",
	      "payload": {
	        "total_duration_ms": 1450,
	        "cold_start": true,
	        "start_anchor": "process_start",
	        "end_anchor": "first_content_frame",
	        "result": "success"
	      }
	    }
	  ]
	}`
	reqIng, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/ingest", bytes.NewBufferString(payload))
	reqIng.Header.Set("Authorization", "Bearer ingest-key")
	respIng, err := http.DefaultClient.Do(reqIng)
	if err != nil || respIng.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 ingest, got status=%d err=%v", respIng.StatusCode, err)
	}
	respIng.Body.Close()

	// 2. Viewer is forbidden (403) from session lookup
	reqViewer, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/sessions/lookup?user_hash=aaaabbbbccccdddd&reason=INC-42", nil)
	reqViewer.Header.Set("Authorization", "Bearer viewer-key")
	respViewer, _ := http.DefaultClient.Do(reqViewer)
	respViewer.Body.Close()
	if respViewer.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for viewer session lookup, got %d", respViewer.StatusCode)
	}

	// 3. Operator without audit reason is rejected (400)
	reqNoReason, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/sessions/lookup?user_hash=aaaabbbbccccdddd", nil)
	reqNoReason.Header.Set("Authorization", "Bearer operator-key")
	respNoReason, _ := http.DefaultClient.Do(reqNoReason)
	respNoReason.Body.Close()
	if respNoReason.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 without reason, got %d", respNoReason.StatusCode)
	}

	// 4. Operator with ticket reason succeeds (200) and records audit entry
	reqOp, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/sessions/lookup?user_hash=aaaabbbbccccdddd&reason=INC-4242-vip-freeze", nil)
	reqOp.Header.Set("Authorization", "Bearer operator-key")
	respOp, err := http.DefaultClient.Do(reqOp)
	if err != nil || respOp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for operator lookup, got status=%d err=%v", respOp.StatusCode, err)
	}
	var lookupResp struct {
		Actor  string                         `json:"actor"`
		Events []storage.SessionTimelineEvent `json:"events"`
	}
	if err := json.NewDecoder(respOp.Body).Decode(&lookupResp); err != nil {
		t.Fatalf("decode lookup: %v", err)
	}
	respOp.Body.Close()
	if lookupResp.Actor != "oncall-1" || len(lookupResp.Events) != 1 || lookupResp.Events[0].EventID != "rbac-evt-0001" {
		t.Fatalf("unexpected lookup response: %+v", lookupResp)
	}

	// 5. Operator is forbidden (403) from reading audit logs; Admin succeeds (200) and sees the exact entry
	reqOpAudit, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/audit/logs", nil)
	reqOpAudit.Header.Set("Authorization", "Bearer operator-key")
	respOpAudit, _ := http.DefaultClient.Do(reqOpAudit)
	respOpAudit.Body.Close()
	if respOpAudit.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for operator reading audit logs, got %d", respOpAudit.StatusCode)
	}

	reqAdminAudit, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/audit/logs", nil)
	reqAdminAudit.Header.Set("X-Caliper-Key", "admin-key")
	respAdminAudit, err := http.DefaultClient.Do(reqAdminAudit)
	if err != nil || respAdminAudit.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for admin audit logs, got status=%d err=%v", respAdminAudit.StatusCode, err)
	}
	var auditResp struct {
		AuditLogs []storage.AuditLogEntry `json:"audit_logs"`
	}
	if err := json.NewDecoder(respAdminAudit.Body).Decode(&auditResp); err != nil {
		t.Fatalf("decode audit logs: %v", err)
	}
	respAdminAudit.Body.Close()
	if len(auditResp.AuditLogs) != 1 || auditResp.AuditLogs[0].Actor != "oncall-1" || auditResp.AuditLogs[0].Reason != "INC-4242-vip-freeze" || auditResp.AuditLogs[0].ResultCount != 1 {
		t.Fatalf("unexpected audit log entry: %+v", auditResp.AuditLogs)
	}
}
