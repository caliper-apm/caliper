package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caliper-apm/caliper/server/internal/analytics"
	"github.com/caliper-apm/caliper/server/internal/auth"
	"github.com/caliper-apm/caliper/server/internal/storage"
	"github.com/caliper-apm/caliper/server/internal/symbolicator"
)

const (
	// ProtocolVersion is the default MCP JSON-RPC protocol revision negotiated during initialize.
	ProtocolVersion = "2025-03-26"
	ServerName      = "caliper-apm-mcp"
	ServerVersion   = "1.10.0"

	maxInspectFrames      = 32
	maxInspectBreadcrumbs = 30
	compactJSONThreshold  = 24 * 1024
)

// IsSupportedProtocolVersion reports whether v is a supported MCP protocol revision.
func IsSupportedProtocolVersion(v string) bool {
	switch strings.TrimSpace(v) {
	case "2025-06-18", "2025-03-26", "2024-11-05":
		return true
	default:
		return false
	}
}

// RPCRequest represents a JSON-RPC 2.0 MCP request or notification.
type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// RPCError represents a JSON-RPC 2.0 error payload.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// RPCResponse represents a JSON-RPC 2.0 MCP response.
type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// ToolDefinition describes one callable MCP tool using JSON Schema.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ContentItem is one structured text item inside an MCP tools/call result.
type ContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CallToolResult is the standard MCP tools/call response envelope.
type CallToolResult struct {
	Content []ContentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// Server exposes Caliper APM's Zero-Lie analytics, statistical release gate, and pure-Go
// symbolicated crash inspection over standard Model Context Protocol (MCP) JSON-RPC 2.0.
type Server struct {
	store  storage.AnalyticsStore
	meta   storage.MetaStore
	engine *analytics.Engine
	sym    *symbolicator.Service
}

// NewServer creates a read-only diagnostic MCP server wired to live Caliper storage & symbolicator.
func NewServer(store storage.AnalyticsStore, meta storage.MetaStore, engine *analytics.Engine, sym *symbolicator.Service) *Server {
	if engine == nil {
		engine = analytics.New(store)
	}
	if sym == nil && meta != nil {
		sym = symbolicator.NewService(meta)
	}
	return &Server{
		store:  store,
		meta:   meta,
		engine: engine,
		sym:    sym,
	}
}

// HandleRequest dispatches one MCP JSON-RPC 2.0 message with unscoped local credentials.
// Returns (nil, true) for JSON-RPC notifications that do not expect a response body.
func (s *Server) HandleRequest(ctx context.Context, req RPCRequest) (*RPCResponse, bool) {
	return s.HandleRequestWithPrincipal(ctx, req, auth.Principal{})
}

// HandleRequestWithPrincipal dispatches one MCP JSON-RPC 2.0 message while enforcing
// fail-closed RBAC workspace app_id scoping on every tool invocation.
func (s *Server) HandleRequestWithPrincipal(ctx context.Context, req RPCRequest, principal auth.Principal) (*RPCResponse, bool) {
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return errResp(req.ID, -32600, "jsonrpc must be 2.0"), false
	}

	// JSON-RPC notification (no id)
	if strings.HasPrefix(req.Method, "notifications/") && len(req.ID) == 0 {
		return nil, true
	}

	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		ver := ProtocolVersion
		if IsSupportedProtocolVersion(p.ProtocolVersion) {
			ver = strings.TrimSpace(p.ProtocolVersion)
		}
		return okResp(req.ID, map[string]any{
			"protocolVersion": ver,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{
				"name":    ServerName,
				"version": ServerVersion,
			},
		}), false

	case "ping":
		return okResp(req.ID, map[string]any{}), false

	case "tools/list":
		return okResp(req.ID, map[string]any{
			"tools": s.Tools(),
		}), false

	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.Name) == "" {
			return errResp(req.ID, -32602, "invalid tools/call params: tool name is required"), false
		}
		res := s.callTool(ctx, principal, params.Name, params.Arguments)
		return okResp(req.ID, res), false

	default:
		return errResp(req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method)), false
	}
}

// Tools returns the full catalog of read-only Caliper APM MCP tools.
func (s *Server) Tools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "caliper_overview",
			Description: "Fetch Zero-Lie mobile APM overview KPIs (DDSketch ±2% cold-start P50/P90/P95/P99, fluency jank rate with zero-frame isolation, per-scene CPU/memory peaks, network API denominator, and top stability issues).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"app_id":        map[string]any{"type": "string", "description": "Optional app identifier filter"},
					"platform":      map[string]any{"type": "string", "enum": []string{"android", "ios"}, "description": "Mobile platform (android or ios)"},
					"env":           map[string]any{"type": "string", "description": "Environment (default: prod)"},
					"app_version":   map[string]any{"type": "string", "description": "Optional app version filter (e.g. 1.1.0)"},
					"adapter":       map[string]any{"type": "string", "enum": []string{"native", "react-native"}, "description": "Optional tech stack adapter filter"},
					"scene":         map[string]any{"type": "string", "description": "Optional screen/scene filter"},
					"device_model":  map[string]any{"type": "string", "description": "Optional device model slice"},
					"os_version":    map[string]any{"type": "string", "description": "Optional OS version slice"},
					"start_day_utc": map[string]any{"type": "string", "description": "Optional inclusive UTC start date (YYYY-MM-DD)"},
					"end_day_utc":   map[string]any{"type": "string", "description": "Optional inclusive UTC end date (YYYY-MM-DD)"},
				},
				"required": []string{"platform"},
			},
		},
		{
			Name:        "caliper_compare_releases",
			Description: "Run a statistically gated Zero-Lie release comparison (DDSketch KS, cluster-adjusted fluency Z-test, Two-Proportion Z-test, Wilson 95% CIs, explicit sample gates) between baseline and candidate app versions.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"app_id":            map[string]any{"type": "string"},
					"platform":          map[string]any{"type": "string", "enum": []string{"android", "ios"}},
					"env":               map[string]any{"type": "string"},
					"baseline_version":  map[string]any{"type": "string", "description": "Baseline app version (e.g. 1.0.0)"},
					"candidate_version": map[string]any{"type": "string", "description": "Candidate app version (e.g. 1.1.0)"},
					"scene":             map[string]any{"type": "string"},
					"min_samples":       map[string]any{"type": "integer", "description": "Minimum samples per arm before declaring significance (default 100)"},
				},
				"required": []string{"platform", "baseline_version", "candidate_version"},
			},
		},
		{
			Name:        "caliper_evaluate_gate",
			Description: "Evaluate the CI/CD Release Quality Gate (PASS / BLOCK / GATED) with health score, symbol archive verification, blocking reasons, and PR-ready markdown summary.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"app_id":               map[string]any{"type": "string"},
					"platform":             map[string]any{"type": "string", "enum": []string{"android", "ios"}},
					"env":                  map[string]any{"type": "string"},
					"baseline_version":     map[string]any{"type": "string"},
					"candidate_version":    map[string]any{"type": "string"},
					"min_samples":          map[string]any{"type": "integer"},
					"require_symbol_bound": map[string]any{"type": "boolean"},
				},
				"required": []string{"platform", "baseline_version", "candidate_version"},
			},
		},
		{
			Name:        "caliper_scan_regressions",
			Description: "Scan global metrics plus every per-scene fluency and memory breakdown between two releases, returning Benjamini-Hochberg FDR-corrected findings ranked by impact score.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"app_id":            map[string]any{"type": "string"},
					"platform":          map[string]any{"type": "string", "enum": []string{"android", "ios"}},
					"env":               map[string]any{"type": "string"},
					"baseline_version":  map[string]any{"type": "string"},
					"candidate_version": map[string]any{"type": "string"},
					"min_samples":       map[string]any{"type": "integer"},
				},
				"required": []string{"platform", "baseline_version", "candidate_version"},
			},
		},
		{
			Name:        "caliper_list_issues",
			Description: "List top mobile stability issues (Native crashes, JS errors, ANR / Watchdog hangs, FOOM / LMK abnormal exits) ranked by event count and affected installs.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"app_id":       map[string]any{"type": "string"},
					"platform":     map[string]any{"type": "string", "enum": []string{"android", "ios"}},
					"env":          map[string]any{"type": "string"},
					"app_version":  map[string]any{"type": "string"},
					"device_model": map[string]any{"type": "string"},
					"os_version":   map[string]any{"type": "string"},
					"limit":        map[string]any{"type": "integer", "description": "Max issues to return (default 20, max 100)"},
				},
			},
		},
		{
			Name:        "caliper_inspect_issue",
			Description: "Inspect a specific stability issue fingerprint including automatically symbolicated stack frames (R8, Source Map v3, Mach-O .dSYM / ELF .so DWARF, Swift 5+ & Itanium C++ demangling), breadcrumbs, and device/OS/release impact breakdown.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"fingerprint": map[string]any{"type": "string", "description": "Issue fingerprint returned by caliper_list_issues"},
					"app_id":      map[string]any{"type": "string"},
					"platform":    map[string]any{"type": "string", "enum": []string{"android", "ios"}},
					"env":         map[string]any{"type": "string"},
					"app_version": map[string]any{"type": "string"},
				},
				"required": []string{"fingerprint"},
			},
		},
	}
}

func (s *Server) callTool(ctx context.Context, principal auth.Principal, name string, rawArgs json.RawMessage) CallToolResult {
	switch name {
	case "caliper_overview":
		var args struct {
			AppID       string `json:"app_id"`
			Platform    string `json:"platform"`
			Env         string `json:"env"`
			AppVersion  string `json:"app_version"`
			Adapter     string `json:"adapter"`
			Scene       string `json:"scene"`
			DeviceModel string `json:"device_model"`
			OSVersion   string `json:"os_version"`
			StartDayUTC string `json:"start_day_utc"`
			EndDayUTC   string `json:"end_day_utc"`
		}
		if err := unmarshalArgs(rawArgs, &args); err != nil {
			return toolErr(err.Error())
		}
		scopedAppID, status, code, msg := auth.ResolveAppScope(principal, args.AppID)
		if status != 0 {
			return toolErr(fmt.Sprintf("%s: %s", code, msg))
		}
		if args.Platform != "android" && args.Platform != "ios" {
			return toolErr("platform must be 'android' or 'ios'")
		}
		if args.Env == "" {
			args.Env = "prod"
		}
		rep, err := s.engine.Overview(ctx, storage.QueryFilter{
			AppID:       scopedAppID,
			Platform:    args.Platform,
			Env:         args.Env,
			AppVersion:  args.AppVersion,
			Adapter:     args.Adapter,
			Scene:       args.Scene,
			DeviceModel: args.DeviceModel,
			OSVersion:   args.OSVersion,
			StartDayUTC: args.StartDayUTC,
			EndDayUTC:   args.EndDayUTC,
		})
		if err != nil {
			return toolErr(err.Error())
		}
		return toolJSON(rep)

	case "caliper_compare_releases":
		var req analytics.CompareRequest
		if err := unmarshalArgs(rawArgs, &req); err != nil {
			return toolErr(err.Error())
		}
		scopedAppID, status, code, msg := auth.ResolveAppScope(principal, req.AppID)
		if status != 0 {
			return toolErr(fmt.Sprintf("%s: %s", code, msg))
		}
		req.AppID = scopedAppID
		if req.Platform != "android" && req.Platform != "ios" {
			return toolErr("platform must be 'android' or 'ios'")
		}
		if req.BaselineVersion == "" || req.CandidateVersion == "" {
			return toolErr("baseline_version and candidate_version are required")
		}
		rep, err := s.engine.Compare(ctx, req)
		if err != nil {
			return toolErr(err.Error())
		}
		return toolJSON(rep)

	case "caliper_evaluate_gate":
		var req analytics.ReleaseGateRequest
		if err := unmarshalArgs(rawArgs, &req); err != nil {
			return toolErr(err.Error())
		}
		scopedAppID, status, code, msg := auth.ResolveAppScope(principal, req.AppID)
		if status != 0 {
			return toolErr(fmt.Sprintf("%s: %s", code, msg))
		}
		req.AppID = scopedAppID
		if req.Platform != "android" && req.Platform != "ios" {
			return toolErr("platform must be 'android' or 'ios'")
		}
		if req.BaselineVersion == "" || req.CandidateVersion == "" {
			return toolErr("baseline_version and candidate_version are required")
		}
		rep, err := s.engine.EvaluateReleaseGate(ctx, s.meta, req)
		if err != nil {
			return toolErr(err.Error())
		}
		return toolJSON(rep)

	case "caliper_scan_regressions":
		var req analytics.CompareRequest
		if err := unmarshalArgs(rawArgs, &req); err != nil {
			return toolErr(err.Error())
		}
		scopedAppID, status, code, msg := auth.ResolveAppScope(principal, req.AppID)
		if status != 0 {
			return toolErr(fmt.Sprintf("%s: %s", code, msg))
		}
		req.AppID = scopedAppID
		if req.Platform != "android" && req.Platform != "ios" {
			return toolErr("platform must be 'android' or 'ios'")
		}
		if req.BaselineVersion == "" || req.CandidateVersion == "" {
			return toolErr("baseline_version and candidate_version are required")
		}
		rep, err := s.engine.ScanRegressions(ctx, req)
		if err != nil {
			return toolErr(err.Error())
		}
		return toolJSON(rep)

	case "caliper_list_issues":
		var args struct {
			AppID       string `json:"app_id"`
			Platform    string `json:"platform"`
			Env         string `json:"env"`
			AppVersion  string `json:"app_version"`
			DeviceModel string `json:"device_model"`
			OSVersion   string `json:"os_version"`
			Limit       int    `json:"limit"`
		}
		if err := unmarshalArgs(rawArgs, &args); err != nil {
			return toolErr(err.Error())
		}
		scopedAppID, status, code, msg := auth.ResolveAppScope(principal, args.AppID)
		if status != 0 {
			return toolErr(fmt.Sprintf("%s: %s", code, msg))
		}
		if args.Env == "" {
			args.Env = "prod"
		}
		if args.Limit <= 0 || args.Limit > 100 {
			args.Limit = 20
		}
		issues, err := s.store.QueryIssues(ctx, storage.QueryFilter{
			AppID:       scopedAppID,
			Platform:    args.Platform,
			Env:         args.Env,
			AppVersion:  args.AppVersion,
			DeviceModel: args.DeviceModel,
			OSVersion:   args.OSVersion,
		}, args.Limit)
		if err != nil {
			return toolErr(err.Error())
		}
		return toolJSON(map[string]any{
			"count":  len(issues),
			"issues": issues,
		})

	case "caliper_inspect_issue":
		var args struct {
			Fingerprint string `json:"fingerprint"`
			AppID       string `json:"app_id"`
			Platform    string `json:"platform"`
			Env         string `json:"env"`
			AppVersion  string `json:"app_version"`
		}
		if err := unmarshalArgs(rawArgs, &args); err != nil {
			return toolErr(err.Error())
		}
		scopedAppID, status, code, msg := auth.ResolveAppScope(principal, args.AppID)
		if status != 0 {
			return toolErr(fmt.Sprintf("%s: %s", code, msg))
		}
		if strings.TrimSpace(args.Fingerprint) == "" {
			return toolErr("fingerprint is required")
		}
		if args.Env == "" {
			args.Env = "prod"
		}
		filter := storage.QueryFilter{
			AppID:      scopedAppID,
			Platform:   args.Platform,
			Env:        args.Env,
			AppVersion: args.AppVersion,
		}
		issue, ok, err := s.store.GetIssue(ctx, args.Fingerprint, filter)
		if err != nil {
			return toolErr(err.Error())
		}
		if !ok || issue == nil {
			return toolErr(fmt.Sprintf("issue not found: %s", args.Fingerprint))
		}
		// Copy issue before bounding frames/breadcrumbs to protect coding agent context budgets
		// without mutating underlying store slices.
		boundedIssue := *issue
		truncatedFrames := 0
		if len(boundedIssue.SampleFrames) > maxInspectFrames {
			truncatedFrames = len(boundedIssue.SampleFrames) - maxInspectFrames
			boundedIssue.SampleFrames = boundedIssue.SampleFrames[:maxInspectFrames]
		}
		truncatedBreadcrumbs := 0
		if len(boundedIssue.SampleCrumbs) > maxInspectBreadcrumbs {
			truncatedBreadcrumbs = len(boundedIssue.SampleCrumbs) - maxInspectBreadcrumbs
			boundedIssue.SampleCrumbs = boundedIssue.SampleCrumbs[len(boundedIssue.SampleCrumbs)-maxInspectBreadcrumbs:]
		}
		// Automatically symbolicate stored frames via pure-Go symbolicator when symbols exist.
		var symRes *symbolicator.ResolveResult
		if s.sym != nil && len(boundedIssue.SampleFrames) > 0 {
			ver := args.AppVersion
			if ver == "" && len(boundedIssue.Releases) > 0 {
				ver = strings.Fields(boundedIssue.Releases[0])[0]
			}
			res := s.sym.ResolveFrames(ctx, symbolicator.LookupScope{
				AppID:      scopedAppID,
				Platform:   args.Platform,
				AppVersion: ver,
			}, boundedIssue.SampleFrames)
			symRes = &res
		}
		out := map[string]any{
			"issue": &boundedIssue,
		}
		if symRes != nil {
			out["symbolication"] = symRes
		}
		if truncatedFrames > 0 || truncatedBreadcrumbs > 0 {
			out["truncation"] = map[string]int{
				"omitted_frames":      truncatedFrames,
				"omitted_breadcrumbs": truncatedBreadcrumbs,
			}
		}
		return toolJSON(out)

	default:
		return toolErr(fmt.Sprintf("unknown tool: %s", name))
	}
}

func unmarshalArgs(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

func okResp(id json.RawMessage, result any) *RPCResponse {
	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
}

func errResp(id json.RawMessage, code int, msg string) *RPCResponse {
	return &RPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &RPCError{Code: code, Message: msg},
	}
}

func toolJSON(v any) CallToolResult {
	compact, err := json.Marshal(v)
	if err != nil {
		return toolErr(err.Error())
	}
	// Keep smaller tool responses human-readable while automatically switching to compact JSON
	// above compactJSONThreshold to preserve coding-agent context budgets.
	if len(compact) > compactJSONThreshold {
		return CallToolResult{
			Content: []ContentItem{{Type: "text", Text: string(compact)}},
		}
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return CallToolResult{
			Content: []ContentItem{{Type: "text", Text: string(compact)}},
		}
	}
	return CallToolResult{
		Content: []ContentItem{{Type: "text", Text: string(pretty)}},
	}
}

func toolErr(msg string) CallToolResult {
	return CallToolResult{
		Content: []ContentItem{{Type: "text", Text: msg}},
		IsError: true,
	}
}
