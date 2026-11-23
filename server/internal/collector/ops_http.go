package collector

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/caliper-apm/caliper/server/internal/alerts"
	"github.com/caliper-apm/caliper/server/internal/analytics"
	"github.com/caliper-apm/caliper/server/internal/auth"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

func (h *Handler) registerOpsRoutes() {
	h.mux.HandleFunc("GET /metrics", h.handlePrometheusMetrics)
	h.mux.HandleFunc("GET /v1/ops/source-health", h.withPerm(auth.PermReadAnalytics, h.handleSourceHealth))
	h.mux.HandleFunc("PUT /v1/policy", h.handleUpsertPolicy)
	h.mux.HandleFunc("DELETE /v1/alerts/rules/{rule_id}", h.withPerm(auth.PermManageAlerts, h.handleDeleteAlertRule))
	h.mux.HandleFunc("POST /v1/alerts/rules/{rule_id}/test", h.withPerm(auth.PermManageAlerts, h.handleTestAlertWebhook))
	h.mux.HandleFunc("PATCH /v1/stability/issues/{fingerprint}", h.handleUpdateIssueState)
	h.mux.HandleFunc("POST /v1/analytics/release-gate", h.withPerm(auth.PermReadAnalytics, h.handleReleaseGate))
	h.mux.HandleFunc("GET /v1/analytics/network/samples", h.withPerm(auth.PermReadAnalytics, h.handleNetworkSamples))
	h.mux.HandleFunc("GET /v1/analytics/journeys", h.withPerm(auth.PermReadAnalytics, h.handleCustomJourneys))
}

// handlePrometheusMetrics exposes standard OpenMetrics/Prometheus text counters for self-monitoring.
func (h *Handler) handlePrometheusMetrics(w http.ResponseWriter, r *http.Request) {
	stats := h.store.Stats(r.Context())
	uptime := int64(time.Since(h.startedAt).Seconds())

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "# HELP caliper_server_uptime_seconds Server uptime in seconds.\n")
	fmt.Fprintf(w, "# TYPE caliper_server_uptime_seconds gauge\n")
	fmt.Fprintf(w, "caliper_server_uptime_seconds %d\n", uptime)
	fmt.Fprintf(w, "# HELP caliper_ingest_events_accepted_total Total telemetry events accepted.\n")
	fmt.Fprintf(w, "# TYPE caliper_ingest_events_accepted_total counter\n")
	fmt.Fprintf(w, "caliper_ingest_events_accepted_total %d\n", h.ingestedAccepted.Load())
	fmt.Fprintf(w, "# HELP caliper_ingest_events_duplicate_total Total deduplicated telemetry events.\n")
	fmt.Fprintf(w, "# TYPE caliper_ingest_events_duplicate_total counter\n")
	fmt.Fprintf(w, "caliper_ingest_events_duplicate_total %d\n", h.ingestedDuplicate.Load())
	fmt.Fprintf(w, "# HELP caliper_ingest_events_rejected_total Total schema-rejected telemetry events.\n")
	fmt.Fprintf(w, "# TYPE caliper_ingest_events_rejected_total counter\n")
	fmt.Fprintf(w, "caliper_ingest_events_rejected_total %d\n", h.ingestedRejected.Load())
	fmt.Fprintf(w, "# HELP caliper_storage_total_events Total stored telemetry events.\n")
	fmt.Fprintf(w, "# TYPE caliper_storage_total_events gauge\n")
	fmt.Fprintf(w, "caliper_storage_total_events %d\n", stats.TotalEvents)
	fmt.Fprintf(w, "# HELP caliper_storage_releases_total Total discovered or registered app releases.\n")
	fmt.Fprintf(w, "# TYPE caliper_storage_releases_total gauge\n")
	fmt.Fprintf(w, "caliper_storage_releases_total %d\n", stats.ReleaseCount)
	fmt.Fprintf(w, "# HELP caliper_storage_issues_total Total fingerprinted stability issues.\n")
	fmt.Fprintf(w, "# TYPE caliper_storage_issues_total gauge\n")
	fmt.Fprintf(w, "caliper_storage_issues_total %d\n", stats.IssueCount)
}

// handleSourceHealth reports telemetry data quality, symbol coverage, and zero-frame isolation integrity.
func (h *Handler) handleSourceHealth(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	stats := h.store.Stats(r.Context())
	rels, _ := h.meta.ListReleases(r.Context(), f.AppID, f.Platform, f.Env)
	syms, _ := h.meta.ListSymbols(r.Context(), f.AppID, f.Platform, "", "")
	fl, _ := h.engine.Fluency(r.Context(), f)

	mappedBuilds := map[string]struct{}{}
	for _, s := range syms {
		mappedBuilds[s.AppVersion+"|"+s.BuildNumber] = struct{}{}
	}
	mappedRelCount := 0
	for _, rel := range rels {
		if _, ok := mappedBuilds[rel.AppVersion+"|"+rel.BuildNumber]; ok || rel.SymbolID != "" {
			mappedRelCount++
		}
	}
	var symbolCovPct *float64
	if len(rels) > 0 {
		v := (float64(mappedRelCount) / float64(len(rels))) * 100.0
		symbolCovPct = &v
	}

	var totalFlWin, zeroFlWin int64
	for _, row := range fl {
		totalFlWin += row.TotalWindows
		zeroFlWin += row.ZeroFrameWindows
	}
	var zeroFramePct *float64
	if totalFlWin > 0 {
		v := (float64(zeroFlWin) / float64(totalFlWin)) * 100.0
		zeroFramePct = &v
	}

	acc := h.ingestedAccepted.Load()
	dup := h.ingestedDuplicate.Load()
	rej := h.ingestedRejected.Load()
	totalAttempted := acc + dup + rej
	acceptPct := 100.0
	if totalAttempted > 0 {
		acceptPct = (float64(acc+dup) / float64(totalAttempted)) * 100.0
	}

	status := "healthy"
	if rej > 0 && acceptPct < 98.0 {
		status = "degraded"
	} else if symbolCovPct != nil && *symbolCovPct < 50.0 {
		status = "warning"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":                status,
		"platform":              f.Platform,
		"storage_engine":        stats.Engine,
		"total_stored_events":   stats.TotalEvents,
		"ingest_accepted":       acc,
		"ingest_duplicates":     dup,
		"ingest_rejected":       rej,
		"ingest_accept_pct":     acceptPct,
		"total_releases":        len(rels),
		"symbol_mapped_releases": mappedRelCount,
		"symbol_coverage_pct":   symbolCovPct,
		"symbol_bundles_count":  len(syms),
		"fluency_total_windows": totalFlWin,
		"fluency_zero_windows":  zeroFlWin,
		"zero_frame_ratio_pct":  zeroFramePct,
		"uptime_seconds":        int64(time.Since(h.startedAt).Seconds()),
	})
}

// handleUpsertPolicy allows operators to update remote sampling & collector policies live.
func (h *Handler) handleUpsertPolicy(w http.ResponseWriter, r *http.Request) {
	var p storage.SamplingPolicy
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if p.AppID == "" || (p.Platform != "android" && p.Platform != "ios") {
		writeError(w, http.StatusBadRequest, "invalid_policy", "app_id and platform (android|ios) are required")
		return
	}
	if p.Env == "" {
		p.Env = "prod"
	}
	if p.RatePPM > 1_000_000 {
		writeError(w, http.StatusBadRequest, "invalid_policy", "rate_ppm must be in [0, 1000000]")
		return
	}
	if p.LeaseTTLMS < 60_000 {
		p.LeaseTTLMS = 60_000
	}
	if p.MaxEvents24h < 10 {
		p.MaxEvents24h = 10
	}
	if p.PolicyID == "" {
		p.PolicyID = fmt.Sprintf("pol-%s-%s-%d", p.Platform, p.Env, time.Now().Unix())
	}
	principal, status, code, msg := h.authz.Authorize(r, auth.PermManageAlerts, p.AppID)
	if status != 0 {
		writeError(w, status, code, msg)
		return
	}
	if err := h.meta.UpsertPolicy(r.Context(), p); err != nil {
		writeError(w, http.StatusInternalServerError, "policy_save_failed", err.Error())
		return
	}
	_ = h.meta.AppendAuditLog(r.Context(), storage.AuditLogEntry{
		AuditID:     fmt.Sprintf("aud-pol-%d", time.Now().UnixNano()),
		TimestampMS: time.Now().UTC().UnixMilli(),
		Actor:       principal.Actor,
		Role:        string(principal.Role),
		Action:      "policy_update",
		TargetType:  "policy",
		TargetHash:  p.PolicyID,
		Reason:      fmt.Sprintf("%s/%s rate_ppm=%d max_24h=%d collectors=%d", p.AppID, p.Platform, p.RatePPM, p.MaxEvents24h, len(p.EnabledCollectors)),
		RemoteIP:    r.RemoteAddr,
		ResultCount: len(p.EnabledCollectors),
	})
	saved, _ := h.meta.GetPolicy(r.Context(), p.AppID, p.Platform, p.Env)
	writeJSON(w, http.StatusOK, saved)
}

func (h *Handler) handleDeleteAlertRule(w http.ResponseWriter, r *http.Request) {
	ruleID := r.PathValue("rule_id")
	if err := h.meta.DeleteAlertRule(r.Context(), ruleID); err != nil {
		writeError(w, http.StatusNotFound, "rule_not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "rule_id": ruleID})
}

func (h *Handler) handleTestAlertWebhook(w http.ResponseWriter, r *http.Request) {
	ruleID := r.PathValue("rule_id")
	rules, err := h.meta.ListAlertRules(r.Context(), "", "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	var target *storage.AlertRule
	for i := range rules {
		if rules[i].RuleID == ruleID {
			target = &rules[i]
			break
		}
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "rule_not_found", "alert rule not found")
		return
	}
	if strings.TrimSpace(target.WebhookURL) == "" {
		writeError(w, http.StatusBadRequest, "webhook_missing", "rule has no webhook_url configured")
		return
	}
	nowMS := time.Now().UTC().UnixMilli()
	obs := target.Threshold
	if target.Operator == "gt" {
		obs = target.Threshold * 1.15
	} else {
		obs = target.Threshold * 0.85
	}
	inc := storage.AlertIncident{
		IncidentID:      fmt.Sprintf("test-%s-%d", ruleID, nowMS),
		RuleID:          target.RuleID,
		RuleName:        "[TEST] " + target.Name,
		AppID:           target.AppID,
		Platform:        target.Platform,
		MetricID:        target.MetricID,
		Status:          "firing",
		ObservedValue:   &obs,
		Threshold:       target.Threshold,
		SampleSize:      int64(target.MinSamples),
		TriggeredAtMS:   nowMS,
		LastEvaluatedMS: nowMS,
	}
	dispatcher := alerts.NewWebhookDispatcher(5 * time.Second)
	receipt := dispatcher.Send(r.Context(), *target, inc)
	writeJSON(w, http.StatusOK, map[string]any{
		"rule_id":  target.RuleID,
		"format":   target.WebhookFormat,
		"receipt":  receipt,
		"sent_at":  nowMS,
	})
}

func (h *Handler) handleUpdateIssueState(w http.ResponseWriter, r *http.Request) {
	fp := r.PathValue("fingerprint")
	if fp == "" {
		writeError(w, http.StatusBadRequest, "invalid_fingerprint", "fingerprint is required")
		return
	}
	var body struct {
		Status            string `json:"status"`
		Assignee          string `json:"assignee"`
		Note              string `json:"note"`
		ResolvedInVersion string `json:"resolved_in_version"`
		AppID             string `json:"app_id"`
		Platform          string `json:"platform"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 32*1024)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	principal, status, code, msg := h.authz.Authorize(r, auth.PermManageAlerts, body.AppID)
	if status != 0 {
		writeError(w, status, code, msg)
		return
	}
	saved, err := h.meta.UpsertIssueState(r.Context(), storage.IssueState{
		Fingerprint:       fp,
		Status:            body.Status,
		Assignee:          strings.TrimSpace(body.Assignee),
		Note:              strings.TrimSpace(body.Note),
		ResolvedInVersion: strings.TrimSpace(body.ResolvedInVersion),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_issue_state", err.Error())
		return
	}
	_ = h.meta.AppendAuditLog(r.Context(), storage.AuditLogEntry{
		AuditID:     fmt.Sprintf("aud-iss-%d", time.Now().UnixNano()),
		TimestampMS: time.Now().UTC().UnixMilli(),
		Actor:       principal.Actor,
		Role:        string(principal.Role),
		Action:      "issue_triage",
		TargetType:  "issue",
		TargetHash:  fp,
		Reason:      fmt.Sprintf("status=%s assignee=%s", saved.Status, saved.Assignee),
		RemoteIP:    r.RemoteAddr,
		ResultCount: 1,
	})
	writeJSON(w, http.StatusOK, saved)
}

func (h *Handler) handleReleaseGate(w http.ResponseWriter, r *http.Request) {
	var req analytics.ReleaseGateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if (req.Platform != "android" && req.Platform != "ios") || req.BaselineVersion == "" || req.CandidateVersion == "" {
		writeError(w, http.StatusBadRequest, "invalid_gate_request", "platform (android|ios), baseline_version, and candidate_version are required")
		return
	}
	rep, err := h.engine.EvaluateReleaseGate(r.Context(), h.meta, req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "gate_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *Handler) handleNetworkSamples(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	q := r.URL.Query()
	op := q.Get("operation")
	onlyIssues := q.Get("only_issues") == "true" || q.Get("only_issues") == "1"
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		if parsed, convErr := strconv.Atoi(raw); convErr == nil {
			limit = parsed
		}
	}
	samples, err := h.store.QueryNetworkSamples(r.Context(), f, op, onlyIssues, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"samples": samples})
}

func (h *Handler) handleCustomJourneys(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	journeys, err := h.engine.CustomJourneys(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"journeys": journeys})
}
