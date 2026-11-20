package collector

import (
	"fmt"
	"net/http"
	"time"

	"github.com/caliper-apm/caliper/server/internal/auth"
)

func (h *Handler) registerOpsRoutes() {
	h.mux.HandleFunc("GET /metrics", h.handlePrometheusMetrics)
	h.mux.HandleFunc("GET /v1/ops/source-health", h.withPerm(auth.PermReadAnalytics, h.handleSourceHealth))
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
