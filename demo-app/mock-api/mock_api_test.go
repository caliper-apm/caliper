package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMockAPIFeedStreamAndChaosConfig(t *testing.T) {
	ts := httptest.NewServer(NewServer())
	defer ts.Close()

	// Healthy feed
	resp, err := http.Get(ts.URL + "/api/v1/feed")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("healthy feed failed: status=%v err=%v", resp.StatusCode, err)
	}
	resp.Body.Close()

	// Streaming SSE article
	respSSE, err := http.Get(ts.URL + "/api/v1/stream/article")
	if err != nil || respSSE.StatusCode != http.StatusOK {
		t.Fatalf("sse failed: status=%v err=%v", respSSE.StatusCode, err)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(respSSE.Body)
	respSSE.Body.Close()
	if !strings.Contains(buf.String(), "[DONE]") {
		t.Fatalf("expected SSE [DONE] token, got %q", buf.String())
	}

	// Inject 100% error rate via Chaos endpoint
	respCfg, err := http.Post(ts.URL+"/chaos/config", "application/json", bytes.NewBufferString(`{"latency_ms":10,"error_rate":1.0,"ttft_delay_ms":5,"token_gap_ms":5}`))
	if err != nil || respCfg.StatusCode != http.StatusOK {
		t.Fatalf("set chaos config failed: %v", err)
	}
	respCfg.Body.Close()

	respFail, err := http.Get(ts.URL + "/api/v1/feed")
	if err != nil || respFail.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected injected 502, got status=%v err=%v", respFail.StatusCode, err)
	}
	respFail.Body.Close()
}
