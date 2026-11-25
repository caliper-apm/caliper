package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/caliper-apm/caliper/server/internal/analytics"
	"github.com/caliper-apm/caliper/server/internal/auth"
	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage"
	"github.com/caliper-apm/caliper/server/internal/symbolicator"
)

func TestMCPServerInitializeListAndTools(t *testing.T) {
	store, err := storage.OpenEmbedded("")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	ingestVersion := func(ver string, startupMS float64) {
		events := make([]protocol.Event, 0, 110)
		for i := 0; i < 110; i++ {
			events = append(events, protocol.Event{
				EventID:         fmt.Sprintf("mcp-st-%s-%03d", ver, i),
				Kind:            protocol.KindStartup,
				OccurredAtMS:    1763540000000 + int64(i*1000),
				ContractVersion: 1,
				Scene:           "startup",
				Payload:         json.RawMessage(fmt.Sprintf(`{"total_duration_ms":%.1f,"cold_start":true,"start_anchor":"process_start","end_anchor":"first_content_frame","result":"success"}`, startupMS+float64(i%5))),
			})
		}
		if ver == "1.1.0" {
			events = append(events, protocol.Event{
				EventID:         "mcp-crash-0001",
				Kind:            protocol.KindCrash,
				OccurredAtMS:    1763540500000,
				ContractVersion: 1,
				Scene:           "home_feed",
				Payload:         json.RawMessage(`{"fingerprint":"fp-npe-feed","title":"NullPointerException in FeedBinder","fatal":true,"exception_type":"java.lang.NullPointerException","frames":[{"symbol":"a.b.c","line":14,"in_app":true}]}`),
			})
		}
		if _, err := store.IngestBatch(ctx, &protocol.BatchEnvelope{
			SchemaVersion: 1,
			SentAtMS:      1763540000000,
			Resource: protocol.Resource{
				AppID: "dev.caliper.mcp", Platform: "android", Env: "prod",
				AppVersion: ver, BuildNumber: "100", Adapter: "native",
				SDKName: "caliper-android", SDKVersion: "1.10.0", InstallHash: "0123456789abcdef",
			},
			Events: events,
		}); err != nil {
			t.Fatalf("ingest %s: %v", ver, err)
		}
	}
	ingestVersion("1.0.0", 520)
	ingestVersion("1.1.0", 1420)

	// Upload a tiny R8 mapping so caliper_inspect_issue proves live symbolication!
	r8Mapping := []byte("com.example.FeedBinder -> a.b:\n    14:14:void bindCard(int):42:42 -> c\n")
	if _, err := store.SaveSymbol(ctx, storage.SymbolRecord{
		SymbolID:    "sym-r8-110",
		AppID:       "dev.caliper.mcp",
		Platform:    "android",
		Env:         "prod",
		AppVersion:  "1.1.0",
		BuildNumber: "100",
		Kind:        "r8_mapping",
	}, r8Mapping); err != nil {
		t.Fatalf("save symbol: %v", err)
	}

	srv := NewServer(store, store, analytics.New(store), symbolicator.NewService(store))

	// 1. initialize
	initResp, notif := srv.HandleRequest(ctx, RPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2025-03-26"}`),
	})
	if notif || initResp == nil || initResp.Error != nil {
		t.Fatalf("initialize failed: resp=%+v notif=%v", initResp, notif)
	}

	// 2. notification returns no body
	nResp, isNotif := srv.HandleRequest(ctx, RPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	if !isNotif || nResp != nil {
		t.Fatalf("expected notification no-op, got resp=%+v isNotif=%v", nResp, isNotif)
	}

	// 3. tools/list
	listResp, _ := srv.HandleRequest(ctx, RPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`2`),
		Method:  "tools/list",
	})
	if listResp == nil || listResp.Error != nil {
		t.Fatalf("tools/list failed: %+v", listResp)
	}
	listMap := listResp.Result.(map[string]any)
	tools := listMap["tools"].([]ToolDefinition)
	if len(tools) != 6 {
		t.Fatalf("expected 6 MCP tools, got %d", len(tools))
	}

	// 4. caliper_evaluate_gate should BLOCK 1.1.0 vs 1.0.0
	gateResp, _ := srv.HandleRequest(ctx, RPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`3`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"caliper_evaluate_gate","arguments":{"app_id":"dev.caliper.mcp","platform":"android","baseline_version":"1.0.0","candidate_version":"1.1.0","min_samples":100}}`),
	})
	gateRes := gateResp.Result.(CallToolResult)
	if gateRes.IsError || !strings.Contains(gateRes.Content[0].Text, `"verdict": "block"`) {
		t.Fatalf("expected BLOCK verdict from caliper_evaluate_gate, got %+v", gateRes)
	}

	// 5. caliper_inspect_issue should return symbolicated FeedBinder.bindCard
	inspResp, _ := srv.HandleRequest(ctx, RPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`4`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"caliper_inspect_issue","arguments":{"fingerprint":"fp-npe-feed","app_id":"dev.caliper.mcp","platform":"android","app_version":"1.1.0"}}`),
	})
	inspRes := inspResp.Result.(CallToolResult)
	if inspRes.IsError || !strings.Contains(inspRes.Content[0].Text, "com.example.FeedBinder.bindCard") {
		t.Fatalf("expected symbolicated FeedBinder.bindCard in inspect response, got %+v", inspRes)
	}
}

func TestMCPServerRBACScopeAndPayloadBounding(t *testing.T) {
	store, err := storage.OpenEmbedded("")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	ingestApp := func(appID, fp string, extraFrames int) {
		frames := make([]protocol.StackFrame, 0, extraFrames)
		for i := 0; i < extraFrames; i++ {
			frames = append(frames, protocol.StackFrame{Symbol: fmt.Sprintf("com.app.Frame%02d.run", i), Line: i + 1, InApp: true})
		}
		crumbs := make([]protocol.Breadcrumb, 0, 32)
		for i := 0; i < 32; i++ {
			crumbs = append(crumbs, protocol.Breadcrumb{TimestampMS: 1763540000000 + int64(i*100), Category: "ui", Message: fmt.Sprintf("tap-%d", i)})
		}
		payload, _ := json.Marshal(protocol.StabilityPayload{
			Fingerprint:   fp,
			Title:         "Crash in " + appID,
			Fatal:         true,
			ExceptionType: "RuntimeException",
			Frames:        frames,
			Breadcrumbs:   crumbs,
		})
		if _, err := store.IngestBatch(ctx, &protocol.BatchEnvelope{
			SchemaVersion: 1,
			SentAtMS:      1763540000000,
			Resource: protocol.Resource{
				AppID: appID, Platform: "android", Env: "prod",
				AppVersion: "1.0.0", BuildNumber: "100", Adapter: "native",
				SDKName: "caliper-android", SDKVersion: "1.10.0", InstallHash: "0123456789abcdef",
			},
			Events: []protocol.Event{{
				EventID: "ev-" + appID, Kind: protocol.KindCrash, OccurredAtMS: 1763540000000,
				ContractVersion: 1, Scene: "home", Payload: payload,
			}},
		}); err != nil {
			t.Fatalf("ingest %s: %v", appID, err)
		}
	}
	ingestApp("app.alpha", "fp-alpha", 48)
	ingestApp("app.beta", "fp-beta", 4)

	srv := NewServer(store, store, analytics.New(store), symbolicator.NewService(store))

	// 1. Negotiate 2025-06-18 protocol version cleanly.
	initResp, _ := srv.HandleRequest(ctx, RPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
		Params:  json.RawMessage(`{"protocolVersion":"2025-06-18"}`),
	})
	if initResp == nil || initResp.Result.(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatalf("expected negotiated protocolVersion 2025-06-18, got %+v", initResp)
	}

	// 2. Single-app scoped principal calling caliper_list_issues with empty app_id auto-scopes to app.alpha only.
	alphaPrincipal := auth.Principal{Actor: "alpha-viewer", Role: auth.RoleViewer, AppIDs: []string{"app.alpha"}, Mode: "rbac"}
	listResp, _ := srv.HandleRequestWithPrincipal(ctx, RPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`2`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"caliper_list_issues","arguments":{"platform":"android"}}`),
	}, alphaPrincipal)
	listRes := listResp.Result.(CallToolResult)
	if listRes.IsError || !strings.Contains(listRes.Content[0].Text, "fp-alpha") || strings.Contains(listRes.Content[0].Text, "fp-beta") {
		t.Fatalf("expected auto-scoped app.alpha issues only, got %+v", listRes)
	}

	// 3. Cross-tenant access attempt to app.beta MUST be rejected with forbidden_app_scope.
	crossResp, _ := srv.HandleRequestWithPrincipal(ctx, RPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`3`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"caliper_overview","arguments":{"app_id":"app.beta","platform":"android"}}`),
	}, alphaPrincipal)
	crossRes := crossResp.Result.(CallToolResult)
	if !crossRes.IsError || !strings.Contains(crossRes.Content[0].Text, "forbidden_app_scope") {
		t.Fatalf("expected forbidden_app_scope for cross-app MCP call, got %+v", crossRes)
	}

	// 4. Multi-app scoped principal omitting app_id MUST be rejected (no cross-tenant wildcard).
	multiPrincipal := auth.Principal{Actor: "multi-viewer", Role: auth.RoleViewer, AppIDs: []string{"app.alpha", "app.beta"}, Mode: "rbac"}
	wildResp, _ := srv.HandleRequestWithPrincipal(ctx, RPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`4`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"caliper_overview","arguments":{"platform":"android"}}`),
	}, multiPrincipal)
	wildRes := wildResp.Result.(CallToolResult)
	if !wildRes.IsError || !strings.Contains(wildRes.Content[0].Text, "forbidden_app_scope") {
		t.Fatalf("expected forbidden_app_scope for multi-app wildcard MCP call, got %+v", wildRes)
	}

	// 5. Inspect issue enforces frame/breadcrumb bounding and reports truncation honestly.
	inspResp, _ := srv.HandleRequestWithPrincipal(ctx, RPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`5`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"caliper_inspect_issue","arguments":{"fingerprint":"fp-alpha","platform":"android"}}`),
	}, alphaPrincipal)
	inspRes := inspResp.Result.(CallToolResult)
	if inspRes.IsError || !strings.Contains(inspRes.Content[0].Text, `"omitted_frames": 16`) || !strings.Contains(inspRes.Content[0].Text, `"omitted_breadcrumbs": 2`) {
		t.Fatalf("expected bounded frames/breadcrumbs with honest truncation counts, got %+v", inspRes)
	}
}
