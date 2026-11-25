package collector

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/caliper-apm/caliper/server/internal/alerts"
	"github.com/caliper-apm/caliper/server/internal/analytics"
	"github.com/caliper-apm/caliper/server/internal/auth"
	"github.com/caliper-apm/caliper/server/internal/mcp"
	"github.com/caliper-apm/caliper/server/internal/otlp"
	"github.com/caliper-apm/caliper/server/internal/protocol"
	"github.com/caliper-apm/caliper/server/internal/storage"
	"github.com/caliper-apm/caliper/server/internal/symbolicator"
)

// Config controls collector HTTP security and operational boundaries.
type Config struct {
	APIKey         string
	Authorizer     *auth.Authorizer
	MaxPayloadSize int64
	ConsoleDistDir string
	MetaEngine     string // "embedded" | "postgres"
	TraceExporter  *otlp.TracePushExporter
}

// Handler serves ingestion, policy, release registration, symbolication, alerts, RBAC, MCP, and Zero-Lie analytics endpoints.
type Handler struct {
	store             storage.AnalyticsStore
	meta              storage.MetaStore
	engine            *analytics.Engine
	symbolicator      *symbolicator.Service
	alerts            *alerts.Engine
	mcpServer         *mcp.Server
	traceExporter     *otlp.TracePushExporter
	authz             *auth.Authorizer
	cfg               Config
	startedAt         time.Time
	mux               *http.ServeMux
	ingestedAccepted  atomic.Int64
	ingestedDuplicate atomic.Int64
	ingestedRejected  atomic.Int64
}

// NewHandler wires all v0.1 + v0.5 REST endpoints onto a clean stdlib ServeMux.
func NewHandler(store storage.AnalyticsStore, meta storage.MetaStore, cfg Config) *Handler {
	if cfg.MaxPayloadSize <= 0 {
		cfg.MaxPayloadSize = protocol.MaxPayloadBytes
	}
	authz := cfg.Authorizer
	if authz == nil {
		authz = auth.NewSingleAPIKey(cfg.APIKey)
	}
	eng := analytics.New(store)
	sym := symbolicator.NewService(meta)
	h := &Handler{
		store:         store,
		meta:          meta,
		engine:        eng,
		symbolicator:  sym,
		alerts:        alerts.NewEngine(store, meta, eng, alerts.NewWebhookDispatcher(5*time.Second)),
		mcpServer:     mcp.NewServer(store, meta, eng, sym),
		traceExporter: cfg.TraceExporter,
		authz:         authz,
		cfg:           cfg,
		startedAt:     time.Now().UTC(),
		mux:           http.NewServeMux(),
	}
	h.routes()
	return h
}

// AlertEngine exposes the wired alert evaluator for background scheduling in cmd/caliper.
func (h *Handler) AlertEngine() *alerts.Engine {
	return h.alerts
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Encoding, X-Caliper-Key, Authorization, MCP-Protocol-Version")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) routes() {
	h.mux.HandleFunc("GET /healthz", h.handleHealth)
	h.mux.HandleFunc("GET /v1/auth/whoami", h.handleWhoAmI)
	h.mux.HandleFunc("POST /v1/ingest", h.withPerm(auth.PermIngest, h.handleIngest))
	h.mux.HandleFunc("GET /v1/policy", h.handleGetPolicy)
	h.mux.HandleFunc("POST /v1/releases/register", h.withPerm(auth.PermWriteRelease, h.handleRegisterRelease))
	h.mux.HandleFunc("GET /v1/releases", h.withPerm(auth.PermReadAnalytics, h.handleListReleases))
	h.mux.HandleFunc("POST /v1/symbols/upload", h.withPerm(auth.PermWriteRelease, h.handleUploadSymbol))
	h.mux.HandleFunc("GET /v1/symbols", h.withPerm(auth.PermReadAnalytics, h.handleListSymbols))
	h.mux.HandleFunc("POST /v1/symbols/resolve", h.withPerm(auth.PermReadAnalytics, h.handleResolveSymbols))
	h.mux.HandleFunc("GET /v1/alerts/rules", h.withPerm(auth.PermReadAnalytics, h.handleListAlertRules))
	h.mux.HandleFunc("POST /v1/alerts/rules", h.withPerm(auth.PermManageAlerts, h.handleUpsertAlertRule))
	h.mux.HandleFunc("GET /v1/alerts/incidents", h.withPerm(auth.PermReadAnalytics, h.handleListAlertIncidents))
	h.mux.HandleFunc("POST /v1/alerts/evaluate", h.withPerm(auth.PermManageAlerts, h.handleEvaluateAlerts))
	h.mux.HandleFunc("POST /v1/alerts/rules/{rule_id}/evaluate", h.withPerm(auth.PermManageAlerts, h.handleEvaluateSingleAlert))
	h.mux.HandleFunc("GET /v1/analytics/overview", h.withPerm(auth.PermReadAnalytics, h.handleOverview))
	h.mux.HandleFunc("GET /v1/analytics/timeseries", h.withPerm(auth.PermReadAnalytics, h.handleTimeseries))
	h.mux.HandleFunc("GET /v1/analytics/breakdown", h.withPerm(auth.PermReadAnalytics, h.handleBreakdown))
	h.mux.HandleFunc("POST /v1/analytics/compare", h.withPerm(auth.PermReadAnalytics, h.handleCompare))
	h.mux.HandleFunc("POST /v1/analytics/regressions/scan", h.withPerm(auth.PermReadAnalytics, h.handleScanRegressions))
	h.mux.HandleFunc("GET /v1/analytics/startup", h.withPerm(auth.PermReadAnalytics, h.handleStartup))
	h.mux.HandleFunc("GET /v1/analytics/fluency", h.withPerm(auth.PermReadAnalytics, h.handleFluency))
	h.mux.HandleFunc("GET /v1/analytics/resources", h.withPerm(auth.PermReadAnalytics, h.handleResources))
	h.mux.HandleFunc("GET /v1/analytics/network", h.withPerm(auth.PermReadAnalytics, h.handleNetwork))
	h.mux.HandleFunc("GET /v1/stability/issues", h.withPerm(auth.PermReadAnalytics, h.handleIssues))
	h.mux.HandleFunc("GET /v1/stability/issues/{fingerprint}", h.withPerm(auth.PermReadAnalytics, h.handleIssueDetail))
	h.mux.HandleFunc("GET /v1/sessions/lookup", h.handleSessionLookup)
	h.mux.HandleFunc("GET /v1/audit/logs", h.withPerm(auth.PermReadAuditLogs, h.handleListAuditLogs))
	h.mux.HandleFunc("POST /v1/mcp", h.handleMCP)
	h.registerV1DiagnosticsRoutes()
	h.registerOpsRoutes()
	if h.cfg.ConsoleDistDir != "" {
		h.mux.HandleFunc("GET /", h.handleConsoleStatic)
	}
}

func (h *Handler) handleConsoleStatic(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == "/healthz" {
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	clean := filepath.ToSlash(filepath.Clean("/" + r.URL.Path))
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || clean == "." {
		clean = "index.html"
	}
	if strings.Contains(r.URL.Path, "..") || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		writeError(w, http.StatusBadRequest, "invalid_path", "invalid asset path")
		return
	}
	absDist, err := filepath.Abs(h.cfg.ConsoleDistDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "console_error", err.Error())
		return
	}
	target := filepath.Join(absDist, filepath.FromSlash(clean))
	rel, err := filepath.Rel(absDist, target)
	if err != nil || strings.HasPrefix(rel, "..") {
		writeError(w, http.StatusBadRequest, "invalid_path", "invalid asset path")
		return
	}
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		if strings.HasPrefix(clean, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeFile(w, r, target)
		return
	}
	// SPA fallback to index.html
	indexPath := filepath.Join(h.cfg.ConsoleDistDir, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		writeError(w, http.StatusNotFound, "console_not_built", "console dist not found; run npm run build in console/")
		return
	}
	http.ServeFile(w, r, indexPath)
}

type principalCtxKey struct{}

func principalFromContext(r *http.Request) auth.Principal {
	if p, ok := r.Context().Value(principalCtxKey{}).(auth.Principal); ok {
		return p
	}
	return auth.Principal{}
}

func (h *Handler) withPerm(perm auth.Permission, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		appID := r.URL.Query().Get("app_id")
		p, status, code, msg := h.authz.Authorize(r, perm, appID)
		if status != 0 {
			writeError(w, status, code, msg)
			return
		}
		ctx := context.WithValue(r.Context(), principalCtxKey{}, p)
		next(w, r.WithContext(ctx))
	}
}

func (h *Handler) resolveAppScopeOrWriteError(w http.ResponseWriter, r *http.Request, requestedAppID string) (string, bool) {
	resolved, status, code, msg := auth.ResolveAppScope(principalFromContext(r), requestedAppID)
	if status != 0 {
		writeError(w, status, code, msg)
		return "", false
	}
	return resolved, true
}

func (h *Handler) parseFilterOrWriteError(w http.ResponseWriter, r *http.Request) (storage.QueryFilter, bool) {
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return storage.QueryFilter{}, false
	}
	scopedAppID, ok := h.resolveAppScopeOrWriteError(w, r, f.AppID)
	if !ok {
		return storage.QueryFilter{}, false
	}
	f.AppID = scopedAppID
	return f, true
}

func (h *Handler) handleWhoAmI(w http.ResponseWriter, r *http.Request) {
	p, ok := h.authz.Authenticate(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid credentials")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	stats := h.store.Stats(r.Context())
	metaEngine := h.cfg.MetaEngine
	if metaEngine == "" {
		metaEngine = "embedded"
	}
	type pinger interface {
		Ping(ctx context.Context) error
	}
	if p, ok := h.meta.(pinger); ok {
		pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		err := p.Ping(pingCtx)
		cancel()
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status":         "degraded",
				"meta_engine":    metaEngine,
				"meta_error":     err.Error(),
				"uptime_seconds": int64(time.Since(h.startedAt).Seconds()),
				"storage":        stats,
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"meta_engine":    metaEngine,
		"uptime_seconds": int64(time.Since(h.startedAt).Seconds()),
		"storage":        stats,
	})
}

func (h *Handler) handleIngest(w http.ResponseWriter, r *http.Request) {
	bodyReader, closer, err := openBoundedBody(r, h.cfg.MaxPayloadSize)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_encoding", err.Error())
		return
	}
	defer closer()

	raw, err := io.ReadAll(io.LimitReader(bodyReader, h.cfg.MaxPayloadSize+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read_failed", err.Error())
		return
	}
	if int64(len(raw)) > h.cfg.MaxPayloadSize {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "uncompressed body exceeds maximum allowed bytes")
		return
	}

	var batch protocol.BatchEnvelope
	if err := json.Unmarshal(raw, &batch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if _, status, code, msg := h.authz.Authorize(r, auth.PermIngest, batch.Resource.AppID); status != 0 {
		writeError(w, status, code, msg)
		return
	}
	res, err := h.store.IngestBatch(r.Context(), &batch)
	h.ingestedAccepted.Add(int64(res.Accepted))
	h.ingestedDuplicate.Add(int64(res.Duplicates))
	h.ingestedRejected.Add(int64(res.Rejected))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_batch", err.Error())
		return
	}
	if h.traceExporter != nil && res.Accepted > 0 {
		h.traceExporter.EnqueueBatch(&batch)
	}
	writeJSON(w, http.StatusAccepted, res)
}

// isAllowedMCPOrigin enforces MCP Streamable HTTP Origin validation to prevent browser DNS-rebinding attacks.
// CLI / stdio bridges / curl requests without an Origin header are allowed; browser origins must match loopback or r.Host.
func isAllowedMCPOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	reqHost := strings.TrimSpace(r.Host)
	if strings.EqualFold(u.Host, reqHost) {
		return true
	}
	if rh, _, splitErr := net.SplitHostPort(reqHost); splitErr == nil && strings.EqualFold(host, rh) {
		return true
	}
	return strings.EqualFold(host, reqHost)
}

func (h *Handler) handleMCP(w http.ResponseWriter, r *http.Request) {
	if !isAllowedMCPOrigin(r) {
		writeError(w, http.StatusForbidden, "forbidden_origin", "cross-origin browser requests to /v1/mcp are blocked")
		return
	}
	principal, status, code, msg := h.authz.Authorize(r, auth.PermReadAnalytics, "")
	if status != 0 {
		writeError(w, status, code, msg)
		return
	}
	protoVer := mcp.ProtocolVersion
	if reqVer := strings.TrimSpace(r.Header.Get("MCP-Protocol-Version")); mcp.IsSupportedProtocolVersion(reqVer) {
		protoVer = reqVer
	}
	w.Header().Set("MCP-Protocol-Version", protoVer)

	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusOK, mcp.RPCResponse{
			JSONRPC: "2.0",
			Error:   &mcp.RPCError{Code: -32700, Message: "read error: " + err.Error()},
		})
		return
	}
	var req mcp.RPCRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, http.StatusOK, mcp.RPCResponse{
			JSONRPC: "2.0",
			Error:   &mcp.RPCError{Code: -32700, Message: "parse error: " + err.Error()},
		})
		return
	}
	resp, isNotif := h.mcpServer.HandleRequestWithPrincipal(r.Context(), req, principal)
	if isNotif || resp == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleGetPolicy(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	appID := q.Get("app_id")
	platform := q.Get("platform")
	env := q.Get("env")
	if env == "" {
		env = "prod"
	}
	if appID == "" || (platform != "android" && platform != "ios") {
		writeError(w, http.StatusBadRequest, "invalid_query", "app_id and platform (android|ios) are required")
		return
	}
	p, err := h.meta.GetPolicy(r.Context(), appID, platform, env)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "policy_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handler) handleRegisterRelease(w http.ResponseWriter, r *http.Request) {
	var rec storage.ReleaseRecord
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&rec); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if rec.AppID == "" || rec.AppVersion == "" || rec.BuildNumber == "" || (rec.Platform != "android" && rec.Platform != "ios") {
		writeError(w, http.StatusBadRequest, "invalid_release", "app_id, platform, app_version, and build_number are required")
		return
	}
	scopedAppID, ok := h.resolveAppScopeOrWriteError(w, r, rec.AppID)
	if !ok {
		return
	}
	rec.AppID = scopedAppID
	if rec.BinarySizeBytes < 0 {
		writeError(w, http.StatusBadRequest, "invalid_release", "binary_size_bytes must be non-negative")
		return
	}
	rec.BinaryArtifactKind = strings.ToLower(strings.TrimSpace(rec.BinaryArtifactKind))
	if !storage.IsValidBinaryArtifactKind(rec.BinaryArtifactKind) {
		writeError(w, http.StatusBadRequest, "invalid_release", "binary_artifact_kind must be one of apk, aab, ipa, xcarchive, bundle")
		return
	}
	if rec.Env == "" {
		rec.Env = "prod"
	}
	if err := h.meta.RegisterRelease(r.Context(), rec); err != nil {
		writeError(w, http.StatusInternalServerError, "register_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "registered"})
}

func (h *Handler) handleListReleases(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	appID, ok := h.resolveAppScopeOrWriteError(w, r, q.Get("app_id"))
	if !ok {
		return
	}
	rels, err := h.meta.ListReleases(r.Context(), appID, q.Get("platform"), q.Get("env"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": rels})
}

func (h *Handler) handleUploadSymbol(w http.ResponseWriter, r *http.Request) {
	const maxSymbolBytes = 128 * 1024 * 1024 // 128 MiB cap supporting native .dSYM / .so DWARF binaries
	q := r.URL.Query()
	kind := q.Get("kind")
	switch kind {
	case "r8_mapping", "dsym", "hermes_sourcemap", "breakpad", "native_elf":
	default:
		writeError(w, http.StatusBadRequest, "invalid_symbol_kind", "kind must be r8_mapping, dsym, hermes_sourcemap, breakpad, or native_elf")
		return
	}
	platform := q.Get("platform")
	if platform != "android" && platform != "ios" {
		writeError(w, http.StatusBadRequest, "invalid_platform", "platform must be android or ios")
		return
	}
	appID, appVer, build := q.Get("app_id"), q.Get("app_version"), q.Get("build_number")
	if appID == "" || appVer == "" || build == "" {
		writeError(w, http.StatusBadRequest, "missing_metadata", "app_id, app_version, and build_number query params are required")
		return
	}
	env := q.Get("env")
	if env == "" {
		env = "prod"
	}

	bodyReader, closer, err := openBoundedBody(r, maxSymbolBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_encoding", err.Error())
		return
	}
	defer closer()

	content, err := io.ReadAll(io.LimitReader(bodyReader, maxSymbolBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read_failed", err.Error())
		return
	}
	if int64(len(content)) > maxSymbolBytes || len(content) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_symbol_size", "symbol artifact must be non-empty and <= 128 MiB")
		return
	}

	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	if claimed := strings.ToLower(strings.TrimSpace(q.Get("sha256"))); claimed != "" && claimed != digest {
		writeError(w, http.StatusBadRequest, "sha256_mismatch", "uploaded content SHA-256 does not match claimed sha256")
		return
	}

	symbolID := q.Get("symbol_id")
	if symbolID == "" {
		symbolID = "sym-" + platform + "-" + appVer + "-" + build + "-" + digest[:8]
	}

	saved, err := h.meta.SaveSymbol(r.Context(), storage.SymbolRecord{
		SymbolID:    symbolID,
		AppID:       appID,
		Platform:    platform,
		Env:         env,
		AppVersion:  appVer,
		BuildNumber: build,
		GitSHA:      q.Get("git_sha"),
		Kind:        kind,
		SHA256:      digest,
	}, content)
	if err != nil {
		writeError(w, http.StatusBadRequest, "symbol_save_failed", err.Error())
		return
	}
	// Never expose host filesystem path in API response.
	saved.StoragePath = ""
	writeJSON(w, http.StatusCreated, saved)
}

func (h *Handler) handleListSymbols(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	appID, ok := h.resolveAppScopeOrWriteError(w, r, q.Get("app_id"))
	if !ok {
		return
	}
	syms, err := h.meta.ListSymbols(r.Context(), appID, q.Get("platform"), q.Get("app_version"), q.Get("build_number"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	for i := range syms {
		syms[i].StoragePath = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"symbols": syms})
}

func parseFilter(r *http.Request) (storage.QueryFilter, error) {
	q := r.URL.Query()
	f := storage.QueryFilter{
		AppID:       q.Get("app_id"),
		Platform:    q.Get("platform"),
		Env:         q.Get("env"),
		AppVersion:  q.Get("app_version"),
		BuildNumber: q.Get("build_number"),
		Adapter:     q.Get("adapter"),
		Scene:       q.Get("scene"),
		DeviceModel: q.Get("device_model"),
		OSVersion:   q.Get("os_version"),
		PolicyID:    q.Get("policy_id"),
		Algorithm:   q.Get("algorithm"),
		StartDayUTC: q.Get("start_day_utc"),
		EndDayUTC:   q.Get("end_day_utc"),
	}
	if f.Env == "" {
		f.Env = "prod"
	}
	if f.Platform != "android" && f.Platform != "ios" {
		return f, errors.New("platform query param must be android or ios")
	}
	return f, nil
}

func (h *Handler) handleOverview(w http.ResponseWriter, r *http.Request) {
	f, ok := h.parseFilterOrWriteError(w, r)
	if !ok {
		return
	}
	rep, err := h.engine.Overview(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *Handler) handleTimeseries(w http.ResponseWriter, r *http.Request) {
	f, ok := h.parseFilterOrWriteError(w, r)
	if !ok {
		return
	}
	points, err := h.engine.DailyTrends(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"points": points})
}

func (h *Handler) handleBreakdown(w http.ResponseWriter, r *http.Request) {
	f, ok := h.parseFilterOrWriteError(w, r)
	if !ok {
		return
	}
	dim := strings.TrimSpace(r.URL.Query().Get("dimension"))
	if dim == "" {
		dim = "version"
	}
	slices, err := h.engine.Breakdown(r.Context(), h.meta, f, dim)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_dimension", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dimension": dim, "slices": slices})
}

func (h *Handler) handleStartup(w http.ResponseWriter, r *http.Request) {
	f, ok := h.parseFilterOrWriteError(w, r)
	if !ok {
		return
	}
	rep, err := h.engine.Startup(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *Handler) handleFluency(w http.ResponseWriter, r *http.Request) {
	f, ok := h.parseFilterOrWriteError(w, r)
	if !ok {
		return
	}
	rep, err := h.engine.Fluency(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	scope := symbolicator.LookupScope{
		AppID:       f.AppID,
		Platform:    f.Platform,
		AppVersion:  f.AppVersion,
		BuildNumber: f.BuildNumber,
	}
	for i := range rep {
		for sIdx, stack := range rep[i].SampleStallStacks {
			resolved, _ := h.symbolicator.ResolveLines(r.Context(), scope, stack)
			rep[i].SampleStallStacks[sIdx] = resolved
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"scenes": rep})
}

func (h *Handler) handleResources(w http.ResponseWriter, r *http.Request) {
	f, ok := h.parseFilterOrWriteError(w, r)
	if !ok {
		return
	}
	rep, err := h.engine.Resources(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scenes": rep})
}

func (h *Handler) handleNetwork(w http.ResponseWriter, r *http.Request) {
	f, ok := h.parseFilterOrWriteError(w, r)
	if !ok {
		return
	}
	rep, err := h.engine.Network(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *Handler) handleCompare(w http.ResponseWriter, r *http.Request) {
	var req analytics.CompareRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if (req.Platform != "android" && req.Platform != "ios") || req.BaselineVersion == "" || req.CandidateVersion == "" {
		writeError(w, http.StatusBadRequest, "invalid_compare", "platform, baseline_version, and candidate_version are required")
		return
	}
	scopedAppID, ok := h.resolveAppScopeOrWriteError(w, r, req.AppID)
	if !ok {
		return
	}
	req.AppID = scopedAppID
	rep, err := h.engine.Compare(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "compare_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *Handler) handleIssues(w http.ResponseWriter, r *http.Request) {
	f, ok := h.parseFilterOrWriteError(w, r)
	if !ok {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, convErr := strconv.Atoi(raw); convErr == nil {
			limit = parsed
		}
	}
	issues, err := h.store.QueryIssues(r.Context(), f, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	if states, stErr := h.meta.ListIssueStates(r.Context()); stErr == nil && len(states) > 0 {
		byFP := make(map[string]storage.IssueState, len(states))
		for _, st := range states {
			byFP[st.Fingerprint] = st
		}
		for i := range issues {
			if issues[i].Status == "" {
				issues[i].Status = "open"
			}
			if st, ok := byFP[issues[i].Fingerprint]; ok {
				if st.Status != "" {
					issues[i].Status = st.Status
				}
				issues[i].Assignee = st.Assignee
				issues[i].Note = st.Note
				issues[i].ResolvedInVersion = st.ResolvedInVersion
				issues[i].StateUpdatedAtMS = st.UpdatedAtMS
			}
		}
	} else {
		for i := range issues {
			if issues[i].Status == "" {
				issues[i].Status = "open"
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"issues": issues})
}

func (h *Handler) handleIssueDetail(w http.ResponseWriter, r *http.Request) {
	fp := r.PathValue("fingerprint")
	q := r.URL.Query()
	appID, ok := h.resolveAppScopeOrWriteError(w, r, q.Get("app_id"))
	if !ok {
		return
	}
	f := storage.QueryFilter{
		AppID:       appID,
		Platform:    q.Get("platform"),
		AppVersion:  q.Get("app_version"),
		BuildNumber: q.Get("build_number"),
	}
	issue, ok, err := h.store.GetIssue(r.Context(), fp, f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "issue_not_found", "issue fingerprint not found")
		return
	}
	if issue.Status == "" {
		issue.Status = "open"
	}
	if st, found, stErr := h.meta.GetIssueState(r.Context(), fp); stErr == nil && found {
		if st.Status != "" {
			issue.Status = st.Status
		}
		issue.Assignee = st.Assignee
		issue.Note = st.Note
		issue.ResolvedInVersion = st.ResolvedInVersion
		issue.StateUpdatedAtMS = st.UpdatedAtMS
	}

	// Auto-symbolicate sample frames when matching uploaded symbols exist.
	scope := symbolicator.LookupScope{
		AppID:       f.AppID,
		Platform:    f.Platform,
		AppVersion:  f.AppVersion,
		BuildNumber: f.BuildNumber,
		SymbolID:    q.Get("symbol_id"),
	}
	if scope.AppVersion == "" && len(issue.Releases) > 0 {
		// Releases are formatted as "1.1.0(110)"
		rel := issue.Releases[0]
		if open := strings.IndexByte(rel, '('); open > 0 && strings.HasSuffix(rel, ")") {
			scope.AppVersion = rel[:open]
			scope.BuildNumber = rel[open+1 : len(rel)-1]
		}
	}
	symRes := h.symbolicator.ResolveFrames(r.Context(), scope, issue.SampleFrames)
	issue.SampleFrames = symRes.Frames

	writeJSON(w, http.StatusOK, map[string]any{
		"fingerprint":         issue.Fingerprint,
		"kind":                issue.Kind,
		"title":               issue.Title,
		"fatal":               issue.Fatal,
		"exception_type":      issue.ExceptionType,
		"exit_reason":         issue.ExitReason,
		"event_count":         issue.EventCount,
		"affected_installs":   issue.AffectedInstalls,
		"first_seen_ms":       issue.FirstSeenMS,
		"last_seen_ms":        issue.LastSeenMS,
		"releases":            issue.Releases,
		"device_breakdown":    issue.DeviceBreakdown,
		"os_breakdown":        issue.OSBreakdown,
		"release_breakdown":   issue.ReleaseBreakdown,
		"sample_user_hash":    issue.SampleUserHash,
		"sample_install_hash": issue.SampleInstallHash,
		"status":              issue.Status,
		"assignee":            issue.Assignee,
		"note":                issue.Note,
		"resolved_in_version": issue.ResolvedInVersion,
		"state_updated_at_ms": issue.StateUpdatedAtMS,
		"sample_frames":       issue.SampleFrames,
		"sample_breadcrumbs":  issue.SampleCrumbs,
		"symbol_status":       symRes.Status,
		"resolved_frames":     symRes.ResolvedCount,
		"matched_symbols":     symRes.MatchedSymbolID,
	})
}

func (h *Handler) handleResolveSymbols(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AppID       string                `json:"app_id"`
		Platform    string                `json:"platform"`
		AppVersion  string                `json:"app_version"`
		BuildNumber string                `json:"build_number"`
		SymbolID    string                `json:"symbol_id"`
		Frames      []protocol.StackFrame `json:"frames"`
		Lines       []string              `json:"lines"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256*1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	scope := symbolicator.LookupScope{
		AppID:       req.AppID,
		Platform:    req.Platform,
		AppVersion:  req.AppVersion,
		BuildNumber: req.BuildNumber,
		SymbolID:    req.SymbolID,
	}
	if len(req.Lines) > 0 {
		resolved, cnt := h.symbolicator.ResolveLines(r.Context(), scope, req.Lines)
		writeJSON(w, http.StatusOK, map[string]any{
			"resolved_count": cnt,
			"total_lines":    len(req.Lines),
			"lines":          resolved,
		})
		return
	}
	res := h.symbolicator.ResolveFrames(r.Context(), scope, req.Frames)
	writeJSON(w, http.StatusOK, res)
}

func (h *Handler) handleListAlertRules(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rules, err := h.meta.ListAlertRules(r.Context(), q.Get("app_id"), q.Get("platform"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

func (h *Handler) handleUpsertAlertRule(w http.ResponseWriter, r *http.Request) {
	var rule storage.AlertRule
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&rule); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	saved, err := h.meta.UpsertAlertRule(r.Context(), rule)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_rule", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (h *Handler) handleListAlertIncidents(w http.ResponseWriter, r *http.Request) {
	incs, err := h.meta.ListAlertIncidents(r.Context(), r.URL.Query().Get("rule_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"incidents": incs})
}

func (h *Handler) handleEvaluateAlerts(w http.ResponseWriter, r *http.Request) {
	outcomes, err := h.alerts.EvaluateAll(r.Context(), time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "evaluate_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"outcomes": outcomes})
}

func (h *Handler) handleEvaluateSingleAlert(w http.ResponseWriter, r *http.Request) {
	ruleID := r.PathValue("rule_id")
	outcome, err := h.alerts.EvaluateRule(r.Context(), ruleID, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusNotFound, "rule_not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, outcome)
}

func (h *Handler) handleScanRegressions(w http.ResponseWriter, r *http.Request) {
	var req analytics.CompareRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.Platform != "android" && req.Platform != "ios" {
		writeError(w, http.StatusBadRequest, "invalid_request", "platform must be android or ios")
		return
	}
	if req.BaselineVersion == "" || req.CandidateVersion == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "baseline_version and candidate_version are required")
		return
	}
	scopedAppID, ok := h.resolveAppScopeOrWriteError(w, r, req.AppID)
	if !ok {
		return
	}
	req.AppID = scopedAppID
	rep, err := h.engine.ScanRegressions(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "scan_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *Handler) handleSessionLookup(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	appID := strings.TrimSpace(q.Get("app_id"))
	principal, scopedAppID, status, code, msg := h.authz.AuthorizeScoped(r, auth.PermSessionLookup, appID)
	if status != 0 {
		writeError(w, status, code, msg)
		return
	}
	appID = scopedAppID

	installHash := strings.TrimSpace(q.Get("install_hash"))
	userHash := strings.TrimSpace(q.Get("user_hash"))
	reason := strings.TrimSpace(q.Get("reason"))
	if installHash == "" && userHash == "" {
		writeError(w, http.StatusBadRequest, "missing_target", "install_hash or user_hash is required")
		return
	}
	if len(reason) < 4 {
		writeError(w, http.StatusBadRequest, "missing_audit_reason", "a non-empty audit reason (e.g. ticket ID, min 4 chars) is strictly required for session lookup")
		return
	}
	limit := 100
	if rawLimit := q.Get("limit"); rawLimit != "" {
		if n, err := strconv.Atoi(rawLimit); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	events, err := h.store.QuerySessionTimeline(r.Context(), storage.SessionLookupFilter{
		AppID:       appID,
		Platform:    strings.TrimSpace(q.Get("platform")),
		InstallHash: installHash,
		UserHash:    userHash,
		Limit:       limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "lookup_failed", err.Error())
		return
	}

	targetType := "install_hash"
	targetHash := installHash
	if userHash != "" {
		targetType = "user_hash"
		targetHash = userHash
	}
	auditEntry := storage.AuditLogEntry{
		TimestampMS: time.Now().UTC().UnixMilli(),
		Actor:       principal.Actor,
		Role:        string(principal.Role),
		Action:      "session_lookup",
		TargetType:  targetType,
		TargetHash:  targetHash,
		Reason:      reason,
		RemoteIP:    r.RemoteAddr,
		ResultCount: len(events),
	}
	if err := h.meta.AppendAuditLog(r.Context(), auditEntry); err != nil {
		writeError(w, http.StatusInternalServerError, "audit_log_failed", "refusing to return session lookup without persisted audit entry: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"target_type": targetType,
		"target_hash": targetHash,
		"reason":      reason,
		"actor":       principal.Actor,
		"events":      events,
	})
}

func (h *Handler) handleListAuditLogs(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		if n, err := strconv.Atoi(rawLimit); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	logs, err := h.meta.ListAuditLogs(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit_logs": logs})
}

func openBoundedBody(r *http.Request, maxBytes int64) (io.Reader, func(), error) {
	enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	if enc == "" || enc == "identity" {
		return r.Body, func() { _ = r.Body.Close() }, nil
	}
	if enc == "gzip" {
		gz, err := gzip.NewReader(io.LimitReader(r.Body, maxBytes))
		if err != nil {
			return nil, nil, err
		}
		return gz, func() { _ = gz.Close(); _ = r.Body.Close() }, nil
	}
	return nil, nil, errors.New("unsupported Content-Encoding: " + enc)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
