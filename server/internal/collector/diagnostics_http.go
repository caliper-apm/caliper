package collector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/caliper-apm/caliper/server/internal/auth"
	"github.com/caliper-apm/caliper/server/internal/otlp"
	"github.com/caliper-apm/caliper/server/internal/profiler"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

func (h *Handler) registerV1DiagnosticsRoutes() {
	// CPU Flamegraphs & Trimmed Heap / OOM Deep Diagnostics
	h.mux.HandleFunc("GET /v1/diagnostics/profiles", h.withPerm(auth.PermReadAnalytics, h.handleListCPUProfiles))
	h.mux.HandleFunc("GET /v1/diagnostics/profiles/{profile_id}/flamegraph", h.withPerm(auth.PermReadAnalytics, h.handleGetFlamegraph))
	h.mux.HandleFunc("GET /v1/diagnostics/heap", h.withPerm(auth.PermReadAnalytics, h.handleListHeapSnapshots))
	h.mux.HandleFunc("POST /v1/diagnostics/heap/compare", h.withPerm(auth.PermReadAnalytics, h.handleCompareHeapSnapshots))

	// Audited Remote Log Retrieval Tasks
	h.mux.HandleFunc("POST /v1/diagnostics/log-tasks", h.handleCreateRemoteLogTask)
	h.mux.HandleFunc("GET /v1/diagnostics/log-tasks", h.withPerm(auth.PermReadAnalytics, h.handleListRemoteLogTasks))
	h.mux.HandleFunc("GET /v1/diagnostics/log-tasks/pending", h.withPerm(auth.PermIngest, h.handlePollPendingLogTasks))
	h.mux.HandleFunc("POST /v1/diagnostics/log-tasks/{task_id}/upload", h.withPerm(auth.PermIngest, h.handleUploadRemoteLog))
	h.mux.HandleFunc("GET /v1/diagnostics/log-tasks/{task_id}/content", h.handleGetRemoteLogContent)

	// Full OTLP/HTTP JSON Interoperability
	h.mux.HandleFunc("POST /v1/otlp/v1/traces", h.withPerm(auth.PermIngest, h.handleOTLPTraces))
	h.mux.HandleFunc("POST /v1/otlp/v1/logs", h.withPerm(auth.PermIngest, h.handleOTLPLogs))
	h.mux.HandleFunc("GET /v1/otlp/export", h.withPerm(auth.PermReadAnalytics, h.handleOTLPExportMetrics))
	h.mux.HandleFunc("GET /v1/otlp/export/traces", h.withPerm(auth.PermReadAnalytics, h.handleOTLPExportTraces))
}

func (h *Handler) handleListCPUProfiles(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	runtime := strings.TrimSpace(r.URL.Query().Get("runtime"))
	limit := 25
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	list, err := h.store.QueryCPUProfiles(r.Context(), f, runtime, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": list})
}

func (h *Handler) handleGetFlamegraph(w http.ResponseWriter, r *http.Request) {
	profileID := r.PathValue("profile_id")
	rec, ok, err := h.store.GetCPUProfile(r.Context(), profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "profile_not_found", "cpu profile not found")
		return
	}
	fg := profiler.BuildFlamegraph(r.Context(), *rec, h.symbolicator)
	writeJSON(w, http.StatusOK, fg)
}

func (h *Handler) handleListHeapSnapshots(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	list, err := h.store.QueryHeapSnapshots(r.Context(), f, 25)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"snapshots": list})
}

func (h *Handler) handleCompareHeapSnapshots(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AppID            string `json:"app_id"`
		Platform         string `json:"platform"`
		Env              string `json:"env"`
		BaselineVersion  string `json:"baseline_version"`
		CandidateVersion string `json:"candidate_version"`
		Scene            string `json:"scene,omitempty"`
	}
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
	if req.Env == "" {
		req.Env = "prod"
	}
	baseSnaps, err := h.store.QueryHeapSnapshots(r.Context(), storage.QueryFilter{
		AppID: req.AppID, Platform: req.Platform, Env: req.Env, AppVersion: req.BaselineVersion, Scene: req.Scene,
	}, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	candSnaps, err := h.store.QueryHeapSnapshots(r.Context(), storage.QueryFilter{
		AppID: req.AppID, Platform: req.Platform, Env: req.Env, AppVersion: req.CandidateVersion, Scene: req.Scene,
	}, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	diff := profiler.CompareHeapSnapshots(r.Context(), req.Platform, req.BaselineVersion, req.CandidateVersion, baseSnaps, candSnaps, h.symbolicator)
	writeJSON(w, http.StatusOK, diff)
}

func (h *Handler) handleCreateRemoteLogTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AppID      string `json:"app_id"`
		Platform   string `json:"platform"`
		TargetType string `json:"target_type"`
		TargetHash string `json:"target_hash"`
		Reason     string `json:"reason"`
		MaxBytes   int64  `json:"max_bytes,omitempty"`
		TTLSec     int64  `json:"ttl_sec,omitempty"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	principal, status, code, msg := h.authz.Authorize(r, auth.PermSessionLookup, req.AppID)
	if status != 0 {
		writeError(w, status, code, msg)
		return
	}
	if len(strings.TrimSpace(req.Reason)) < 4 {
		writeError(w, http.StatusBadRequest, "missing_audit_reason", "audit reason (min 4 chars) is required to create a remote log task")
		return
	}
	now := time.Now().UTC().UnixMilli()
	ttlMS := req.TTLSec * 1000
	if ttlMS <= 0 || ttlMS > 24*3600*1000 {
		ttlMS = 3600_000
	}
	// Fail-closed audit log recorded before creating task.
	if err := h.meta.AppendAuditLog(r.Context(), storage.AuditLogEntry{
		TimestampMS: now,
		Actor:       principal.Actor,
		Role:        string(principal.Role),
		Action:      "remote_log_request",
		TargetType:  req.TargetType,
		TargetHash:  req.TargetHash,
		Reason:      strings.TrimSpace(req.Reason),
		RemoteIP:    r.RemoteAddr,
		ResultCount: 1,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "audit_log_failed", err.Error())
		return
	}

	task, err := h.meta.CreateRemoteLogTask(r.Context(), storage.RemoteLogTask{
		AppID:       req.AppID,
		Platform:    req.Platform,
		TargetType:  req.TargetType,
		TargetHash:  strings.TrimSpace(req.TargetHash),
		Reason:      strings.TrimSpace(req.Reason),
		RequestedBy: principal.Actor,
		MaxBytes:    req.MaxBytes,
		CreatedAtMS: now,
		ExpiresAtMS: now + ttlMS,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_task", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

func (h *Handler) handleListRemoteLogTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tasks, err := h.meta.ListRemoteLogTasks(r.Context(), q.Get("app_id"), q.Get("platform"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (h *Handler) handlePollPendingLogTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tasks, err := h.meta.PollPendingLogTasks(
		r.Context(),
		q.Get("app_id"),
		q.Get("platform"),
		q.Get("install_hash"),
		q.Get("user_hash"),
		time.Now().UTC().UnixMilli(),
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "poll_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (h *Handler) handleUploadRemoteLog(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("task_id")
	reader, closer, err := openBoundedBody(r, h.cfg.MaxPayloadSize)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_encoding", err.Error())
		return
	}
	defer closer()

	raw, err := io.ReadAll(io.LimitReader(reader, h.cfg.MaxPayloadSize+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read_failed", err.Error())
		return
	}
	if int64(len(raw)) > h.cfg.MaxPayloadSize {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "remote log payload exceeds limit")
		return
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])

	completed, err := h.meta.CompleteRemoteLogTask(r.Context(), taskID, raw, digest, time.Now().UTC().UnixMilli())
	if err != nil {
		writeError(w, http.StatusBadRequest, "upload_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, completed)
}

func (h *Handler) handleGetRemoteLogContent(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("task_id")
	reason := strings.TrimSpace(r.URL.Query().Get("reason"))
	principal, status, code, msg := h.authz.Authorize(r, auth.PermSessionLookup, r.URL.Query().Get("app_id"))
	if status != 0 {
		writeError(w, status, code, msg)
		return
	}
	if len(reason) < 4 {
		writeError(w, http.StatusBadRequest, "missing_audit_reason", "audit reason (min 4 chars) is required to inspect remote logs")
		return
	}
	task, content, ok, err := h.meta.GetRemoteLogContent(r.Context(), taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "task_not_found", "remote log task not found")
		return
	}
	if err := h.meta.AppendAuditLog(r.Context(), storage.AuditLogEntry{
		TimestampMS: time.Now().UTC().UnixMilli(),
		Actor:       principal.Actor,
		Role:        string(principal.Role),
		Action:      "remote_log_read",
		TargetType:  task.TargetType,
		TargetHash:  task.TargetHash,
		Reason:      reason,
		RemoteIP:    r.RemoteAddr,
		ResultCount: len(content),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "audit_log_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task":    task,
		"content": string(content),
	})
}

func (h *Handler) handleOTLPTraces(w http.ResponseWriter, r *http.Request) {
	reader, closer, err := openBoundedBody(r, h.cfg.MaxPayloadSize)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_encoding", err.Error())
		return
	}
	defer closer()

	var req otlp.ExportTraceServiceRequest
	if err := json.NewDecoder(io.LimitReader(reader, h.cfg.MaxPayloadSize)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_otlp_json", err.Error())
		return
	}
	batches, err := otlp.TranslateTraces(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "translate_failed", err.Error())
		return
	}
	accepted, duplicates, rejected := 0, 0, 0
	for _, b := range batches {
		if _, status, code, msg := h.authz.Authorize(r, auth.PermIngest, b.Resource.AppID); status != 0 {
			writeError(w, status, code, msg)
			return
		}
		res, err := h.store.IngestBatch(r.Context(), b)
		if err != nil {
			rejected += len(b.Events)
			continue
		}
		accepted += res.Accepted
		duplicates += res.Duplicates
		rejected += res.Rejected
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"partialSuccess": map[string]any{"rejectedSpans": rejected},
		"caliper":        map[string]any{"accepted": accepted, "duplicates": duplicates, "rejected": rejected},
	})
}

func (h *Handler) handleOTLPLogs(w http.ResponseWriter, r *http.Request) {
	reader, closer, err := openBoundedBody(r, h.cfg.MaxPayloadSize)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_encoding", err.Error())
		return
	}
	defer closer()

	var req otlp.ExportLogsServiceRequest
	if err := json.NewDecoder(io.LimitReader(reader, h.cfg.MaxPayloadSize)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_otlp_json", err.Error())
		return
	}
	batches, err := otlp.TranslateLogs(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "translate_failed", err.Error())
		return
	}
	accepted, rejected := 0, 0
	for _, b := range batches {
		if _, status, code, msg := h.authz.Authorize(r, auth.PermIngest, b.Resource.AppID); status != 0 {
			writeError(w, status, code, msg)
			return
		}
		res, err := h.store.IngestBatch(r.Context(), b)
		if err != nil {
			rejected += len(b.Events)
			continue
		}
		accepted += res.Accepted
		rejected += res.Rejected
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"partialSuccess": map[string]any{"rejectedLogRecords": rejected},
		"caliper":        map[string]any{"accepted": accepted, "rejected": rejected},
	})
}

func (h *Handler) handleOTLPExportMetrics(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	ov, err := h.engine.Overview(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, otlp.ExportOverviewAsOTLPMetrics(f.AppID, ov))
}

func (h *Handler) handleOTLPExportTraces(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	q := r.URL.Query()
	op := q.Get("operation")
	traceID := strings.ToLower(strings.TrimSpace(q.Get("trace_id")))
	limit := 100
	if raw := q.Get("limit"); raw != "" {
		if parsed, convErr := strconv.Atoi(raw); convErr == nil && parsed > 0 && parsed <= 200 {
			limit = parsed
		}
	}
	samples, err := h.store.QueryNetworkSamples(r.Context(), f, op, false, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
		return
	}
	if traceID != "" {
		filtered := make([]storage.NetworkTransportSample, 0, len(samples))
		for _, smp := range samples {
			if strings.EqualFold(smp.TraceID, traceID) {
				filtered = append(filtered, smp)
			}
		}
		samples = filtered
	}
	writeJSON(w, http.StatusOK, otlp.ExportNetworkSamplesAsOTLPTraces(f.AppID, samples))
}
