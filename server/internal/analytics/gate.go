package analytics

import (
	"context"
	"fmt"
	"strings"

	"github.com/caliper-apm/caliper/server/internal/contract"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

// ReleaseGateRequest evaluates whether a candidate release is statistically safe to promote.
type ReleaseGateRequest struct {
	AppID               string `json:"app_id"`
	Platform            string `json:"platform"`
	Env                 string `json:"env"`
	BaselineVersion     string `json:"baseline_version"`
	CandidateVersion    string `json:"candidate_version"`
	Scene               string `json:"scene,omitempty"`
	StartDayUTC         string `json:"start_day_utc,omitempty"`
	EndDayUTC           string `json:"end_day_utc,omitempty"`
	MinSamples          int64  `json:"min_samples,omitempty"`
	MaxRegressedMetrics int    `json:"max_regressed_metrics,omitempty"`
	RequireSymbolBound  bool   `json:"require_symbol_bound,omitempty"`
}

// BinarySizeComparison reports optional Zero-Lie release binary size deltas.
// Sizes are only compared when both baseline and candidate report positive sizes of the
// exact same artifact kind (e.g. apk vs apk or ipa vs ipa; never mixing aab with apk).
type BinarySizeComparison struct {
	BaselineBytes  int64    `json:"baseline_bytes,omitempty"`
	CandidateBytes int64    `json:"candidate_bytes,omitempty"`
	BaselineKind   string   `json:"baseline_kind,omitempty"`
	CandidateKind  string   `json:"candidate_kind,omitempty"`
	DeltaBytes     *int64   `json:"delta_bytes,omitempty"`
	DeltaPct       *float64 `json:"delta_pct,omitempty"`
	Comparable     bool     `json:"comparable"`
	Reason         string   `json:"reason,omitempty"`
}

// ReleaseGateReport is the Zero-Lie CI/CD canary promotion decision artifact.
type ReleaseGateReport struct {
	Platform             string                      `json:"platform"`
	Env                  string                      `json:"env"`
	BaselineVersion      string                      `json:"baseline_version"`
	CandidateVersion     string                      `json:"candidate_version"`
	Verdict              string                      `json:"verdict"`   // "pass" | "block" | "gated"
	ExitCode             int                         `json:"exit_code"` // 0=pass, 2=block, 3=gated
	HealthScore          int                         `json:"health_score"`
	ScoreFormula         string                      `json:"score_formula"`
	CandidateSymbolBound bool                        `json:"candidate_symbol_bound"`
	BinarySize           *BinarySizeComparison       `json:"binary_size,omitempty"`
	RegressedCount       int                         `json:"regressed_count"`
	GatedCount           int                         `json:"gated_count"`
	EvaluatedCount       int                         `json:"evaluated_count"`
	BlockingReasons      []string                    `json:"blocking_reasons"`
	Warnings             []string                    `json:"warnings"`
	Metrics              []contract.MetricComparison `json:"metrics"`
	SceneFindings        []RegressionFinding         `json:"scene_findings"`
	MarkdownSummary      string                      `json:"markdown_summary"`
}

// EvaluateReleaseGate turns Caliper's Zero-Lie statistical comparison and per-scene scan
// into a deterministic CI/CD release promotion gate (pass / block / gated).
func (e *Engine) EvaluateReleaseGate(ctx context.Context, meta storage.MetaStore, req ReleaseGateRequest) (ReleaseGateReport, error) {
	if req.Env == "" {
		req.Env = "prod"
	}
	cmpReq := CompareRequest{
		AppID:            req.AppID,
		Platform:         req.Platform,
		Env:              req.Env,
		BaselineVersion:  req.BaselineVersion,
		CandidateVersion: req.CandidateVersion,
		Scene:            req.Scene,
		StartDayUTC:      req.StartDayUTC,
		EndDayUTC:        req.EndDayUTC,
		MinSamples:       req.MinSamples,
	}
	cmp, err := e.Compare(ctx, cmpReq)
	if err != nil {
		return ReleaseGateReport{}, err
	}
	scan, err := e.ScanRegressions(ctx, cmpReq)
	if err != nil {
		return ReleaseGateReport{}, err
	}

	symbolBound := false
	var binarySize *BinarySizeComparison
	if meta != nil {
		if syms, symErr := meta.ListSymbols(ctx, req.AppID, req.Platform, req.CandidateVersion, ""); symErr == nil && len(syms) > 0 {
			symbolBound = true
		}
		if rels, relErr := meta.ListReleases(ctx, req.AppID, req.Platform, req.Env); relErr == nil {
			var baseRel, candRel *storage.ReleaseRecord
			for i := range rels {
				r := &rels[i]
				if r.AppVersion == req.CandidateVersion && r.SymbolID != "" {
					symbolBound = true
				}
				if r.AppVersion == req.BaselineVersion && r.BinarySizeBytes > 0 && baseRel == nil {
					baseRel = r
				}
				if r.AppVersion == req.CandidateVersion && r.BinarySizeBytes > 0 && candRel == nil {
					candRel = r
				}
			}
			binarySize = compareReleaseBinarySize(baseRel, candRel)
		}
	}

	evaluated := 0
	blocking := make([]string, 0)
	warnings := make([]string, 0)
	for _, m := range cmp.Metrics {
		if m.Status == "gated" || m.Status == "insufficient_data" {
			warnings = append(warnings, fmt.Sprintf("%s gated (%s; baseline n=%d, candidate n=%d)",
				m.MetricID, m.GateReason, m.BaselineN, m.CandidateN))
			continue
		}
		evaluated++
		if m.Status == "regressed" {
			bVal := fmtOptFloat(m.BaselineValue)
			cVal := fmtOptFloat(m.CandidateValue)
			pStr := ""
			if m.PValue != nil {
				pStr = fmt.Sprintf(", %s p=%.4g", m.StatTest, *m.PValue)
			}
			blocking = append(blocking, fmt.Sprintf("%s regressed: %s -> %s (n=%d vs %d%s)",
				m.MetricID, bVal, cVal, m.BaselineN, m.CandidateN, pStr))
		}
	}

	sceneFindings := make([]RegressionFinding, 0)
	for _, f := range scan.Findings {
		if f.Scope != "global" && f.Comparison.Status == "regressed" {
			sceneFindings = append(sceneFindings, f)
		}
	}

	if !symbolBound {
		msg := fmt.Sprintf("candidate v%s (%s) has no bound build symbol archive (R8/dSYM/SourceMap)", req.CandidateVersion, req.Platform)
		if req.RequireSymbolBound {
			blocking = append(blocking, msg)
		} else {
			warnings = append(warnings, msg)
		}
	}

	score := 100 - cmp.RegressedCount*18 - len(sceneFindings)*6
	if !symbolBound {
		score -= 10
	}
	if score < 0 {
		score = 0
	}

	verdict := "pass"
	exitCode := 0
	if cmp.RegressedCount > req.MaxRegressedMetrics || (req.RequireSymbolBound && !symbolBound) {
		verdict = "block"
		exitCode = 2
	} else if evaluated == 0 || cmp.Verdict == "inconclusive" {
		verdict = "gated"
		exitCode = 3
		if len(warnings) == 0 {
			warnings = append(warnings, "insufficient samples to evaluate release safety gate")
		}
	}

	rep := ReleaseGateReport{
		Platform:             req.Platform,
		Env:                  req.Env,
		BaselineVersion:      req.BaselineVersion,
		CandidateVersion:     req.CandidateVersion,
		Verdict:              verdict,
		ExitCode:             exitCode,
		HealthScore:          score,
		ScoreFormula:         "100 - 18*global_regressions - 6*scene_regressions - 10*(missing_symbols)",
		CandidateSymbolBound: symbolBound,
		BinarySize:           binarySize,
		RegressedCount:       cmp.RegressedCount,
		GatedCount:           cmp.GatedCount,
		EvaluatedCount:       evaluated,
		BlockingReasons:      blocking,
		Warnings:             warnings,
		Metrics:              cmp.Metrics,
		SceneFindings:        sceneFindings,
	}
	rep.MarkdownSummary = renderGateMarkdown(rep)
	return rep, nil
}

func compareReleaseBinarySize(baseRel, candRel *storage.ReleaseRecord) *BinarySizeComparison {
	if baseRel == nil && candRel == nil {
		return nil
	}
	out := &BinarySizeComparison{}
	if baseRel != nil {
		out.BaselineBytes = baseRel.BinarySizeBytes
		out.BaselineKind = baseRel.BinaryArtifactKind
	}
	if candRel != nil {
		out.CandidateBytes = candRel.BinarySizeBytes
		out.CandidateKind = candRel.BinaryArtifactKind
	}
	if out.BaselineBytes <= 0 || out.CandidateBytes <= 0 {
		out.Comparable = false
		out.Reason = "missing_binary_size"
		return out
	}
	if out.BaselineKind == "" || out.CandidateKind == "" {
		out.Comparable = false
		out.Reason = "missing_artifact_kind"
		return out
	}
	if out.BaselineKind != out.CandidateKind {
		out.Comparable = false
		out.Reason = "artifact_kind_mismatch"
		return out
	}
	delta := out.CandidateBytes - out.BaselineBytes
	pct := (float64(delta) / float64(out.BaselineBytes)) * 100.0
	out.DeltaBytes = &delta
	out.DeltaPct = &pct
	out.Comparable = true
	return out
}

func fmtOptFloat(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f", *v)
}

func renderGateMarkdown(r ReleaseGateReport) string {
	var b strings.Builder
	badge := "PASS"
	switch r.Verdict {
	case "block":
		badge = "BLOCK"
	case "gated":
		badge = "GATED (Insufficient Samples)"
	}
	fmt.Fprintf(&b, "### Caliper Release Quality Gate: **%s**\n\n", badge)
	fmt.Fprintf(&b, "- **Platform / Env**: `%s` / `%s`\n", r.Platform, r.Env)
	fmt.Fprintf(&b, "- **Comparison**: `v%s` -> `v%s`\n", r.BaselineVersion, r.CandidateVersion)
	fmt.Fprintf(&b, "- **Health Score**: **%d / 100** (`%s`)\n", r.HealthScore, r.ScoreFormula)
	fmt.Fprintf(&b, "- **Symbols Bound**: `%v` | **Regressed**: `%d` | **Gated**: `%d`\n",
		r.CandidateSymbolBound, r.RegressedCount, r.GatedCount)
	if r.BinarySize != nil {
		if r.BinarySize.Comparable && r.BinarySize.DeltaBytes != nil && r.BinarySize.DeltaPct != nil {
			baseMiB := float64(r.BinarySize.BaselineBytes) / (1024.0 * 1024.0)
			candMiB := float64(r.BinarySize.CandidateBytes) / (1024.0 * 1024.0)
			deltaKiB := float64(*r.BinarySize.DeltaBytes) / 1024.0
			fmt.Fprintf(&b, "- **Binary Size (`%s`)**: `%.2f MiB` -> `%.2f MiB` (`%+.1f KiB` / `%+.2f%%`)\n",
				r.BinarySize.CandidateKind, baseMiB, candMiB, deltaKiB, *r.BinarySize.DeltaPct)
		} else {
			fmt.Fprintf(&b, "- **Binary Size**: not comparable (`%s`: baseline `%s`, candidate `%s`)\n",
				r.BinarySize.Reason, r.BinarySize.BaselineKind, r.BinarySize.CandidateKind)
		}
	}
	b.WriteString("\n")

	if len(r.BlockingReasons) > 0 {
		b.WriteString("#### Blocking Regressions\n")
		for _, reason := range r.BlockingReasons {
			fmt.Fprintf(&b, "- %s\n", reason)
		}
		b.WriteString("\n")
	}
	if len(r.Warnings) > 0 {
		b.WriteString("#### Gate Notices / Warnings\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
		b.WriteString("\n")
	}

	b.WriteString("| Metric | Baseline | Candidate | Samples (B / C) | Status | Stat Test |\n")
	b.WriteString("|---|---:|---:|---:|---|---|\n")
	for _, m := range r.Metrics {
		stat := "—"
		if m.StatTest != "" && m.PValue != nil {
			stat = fmt.Sprintf("%s (p=%.3g)", m.StatTest, *m.PValue)
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %d / %d | **%s** | %s |\n",
			m.MetricID, fmtOptFloat(m.BaselineValue), fmtOptFloat(m.CandidateValue),
			m.BaselineN, m.CandidateN, m.Status, stat)
	}
	return b.String()
}
