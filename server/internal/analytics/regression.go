package analytics

import (
	"context"
	"math"
	"sort"

	"github.com/caliper-apm/caliper/server/internal/contract"
	"github.com/caliper-apm/caliper/server/internal/storage"
)

const (
	// DefaultSignificanceAlpha is the two-sided p-value threshold required to avoid noisy false alarms.
	DefaultSignificanceAlpha = 0.01
	wilsonZ95                = 1.959963984540054
)

// WilsonCI95 computes the Wilson score 95% confidence interval (expressed in percentage [0, 100]).
func WilsonCI95(events, total int64) *[2]float64 {
	if total <= 0 || events < 0 || events > total {
		return nil
	}
	n := float64(total)
	p := float64(events) / n
	z2 := wilsonZ95 * wilsonZ95
	denom := 1.0 + z2/n
	center := (p + z2/(2.0*n)) / denom
	half := (wilsonZ95 * math.Sqrt((p*(1.0-p)+z2/(4.0*n))/n)) / denom
	low := math.Max(0.0, center-half) * 100.0
	high := math.Min(1.0, center+half) * 100.0
	ci := [2]float64{low, high}
	return &ci
}

// TwoProportionZTest computes a two-sided p-value comparing kA/nA vs kB/nB.
func TwoProportionZTest(kA, nA, kB, nB int64) (pValue float64, ok bool) {
	if nA <= 0 || nB <= 0 || kA < 0 || kB < 0 || kA > nA || kB > nB {
		return 1.0, false
	}
	pA := float64(kA) / float64(nA)
	pB := float64(kB) / float64(nB)
	pPool := float64(kA+kB) / float64(nA+nB)
	se := math.Sqrt(pPool * (1.0 - pPool) * (1.0/float64(nA) + 1.0/float64(nB)))
	if se == 0 {
		if pA == pB {
			return 1.0, true
		}
		return 0.0, true
	}
	z := math.Abs(pB-pA) / se
	p := math.Erfc(z / math.Sqrt2)
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	return p, true
}

// HistogramKSTest performs a two-sample Kolmogorov-Smirnov test over two 19-bucket histograms.
// Uses Stephens' finite-sample adjustment and the Smirnov survival series.
func HistogramKSTest(histA, histB contract.Histogram) (ksStat float64, pValue float64, ok bool) {
	nA := histA.Total()
	nB := histB.Total()
	if nA <= 0 || nB <= 0 {
		return 0, 1.0, false
	}
	var cumA, cumB int64
	var maxDiff float64
	for i := 0; i < contract.HistogramBucketCount; i++ {
		cumA += histA[i]
		cumB += histB[i]
		cdfA := float64(cumA) / float64(nA)
		cdfB := float64(cumB) / float64(nB)
		diff := math.Abs(cdfA - cdfB)
		if diff > maxDiff {
			maxDiff = diff
		}
	}
	nEff := (float64(nA) * float64(nB)) / float64(nA+nB)
	sqrtN := math.Sqrt(nEff)
	lambda := (sqrtN + 0.12 + 0.11/sqrtN) * maxDiff
	p := kolmogorovSurvival(lambda)
	return maxDiff, p, true
}

func kolmogorovSurvival(lambda float64) float64 {
	if lambda <= 0 {
		return 1.0
	}
	var sum float64
	for k := 1; k <= 100; k++ {
		term := math.Exp(-2.0 * float64(k*k) * lambda * lambda)
		if k%2 == 1 {
			sum += term
		} else {
			sum -= term
		}
		if term < 1e-12 {
			break
		}
	}
	p := 2.0 * sum
	if p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

// enrichWithProportionStats attaches Two-Proportion Z-test p-value + Wilson 95% CIs
// and downgrades noisy non-significant differences to neutral when p >= alpha.
func enrichWithProportionStats(
	m contract.MetricComparison,
	kBase, nBase, kCand, nCand int64,
	alpha float64,
) contract.MetricComparison {
	m.StatTest = "two_proportion_z"
	m.BaselineCI95 = WilsonCI95(kBase, nBase)
	m.CandidateCI95 = WilsonCI95(kCand, nCand)
	if p, ok := TwoProportionZTest(kBase, nBase, kCand, nCand); ok {
		m.PValue = &p
		sig := p < alpha
		m.StatSignificant = &sig
		if !sig && (m.Status == "regressed" || m.Status == "improved") {
			m.Status = "neutral"
			m.GateReason = "statistically_insignificant"
		}
	}
	return m
}

// enrichWithHistogramStats attaches Histogram KS p-value + quantile bucket intervals.
func enrichWithHistogramStats(
	m contract.MetricComparison,
	histBase, histCand contract.Histogram,
	baseQ, candQ contract.QuantileResult,
	alpha float64,
) contract.MetricComparison {
	m.StatTest = "histogram_ks"
	m.BaselineCI95 = baseQ.Interval
	m.CandidateCI95 = candQ.Interval
	if _, p, ok := HistogramKSTest(histBase, histCand); ok {
		m.PValue = &p
		sig := p < alpha
		m.StatSignificant = &sig
		if !sig && (m.Status == "regressed" || m.Status == "improved") {
			m.Status = "neutral"
			m.GateReason = "statistically_insignificant"
		}
	}
	return m
}

// RegressionFinding represents one ranked statistical finding from an automated release scan.
type RegressionFinding struct {
	Scope       string                    `json:"scope"` // "global" | "scene:<name>"
	Scene       string                    `json:"scene,omitempty"`
	Algorithm   string                    `json:"algorithm,omitempty"`
	ImpactScore float64                   `json:"impact_score"`
	Comparison  contract.MetricComparison `json:"comparison"`
}

// RegressionScanReport summarizes all ranked regressions across global + per-scene dimensions.
type RegressionScanReport struct {
	Platform         string              `json:"platform"`
	BaselineVersion  string              `json:"baseline_version"`
	CandidateVersion string              `json:"candidate_version"`
	Alpha            float64             `json:"alpha"`
	Verdict          string              `json:"verdict"`
	RegressedCount   int                 `json:"regressed_count"`
	Findings         []RegressionFinding `json:"findings"`
}

// ScanRegressions scans global metrics plus every scene's fluency and resource breakdown,
// returning statistically verified regressions ranked by impact score.
func (e *Engine) ScanRegressions(ctx context.Context, req CompareRequest) (RegressionScanReport, error) {
	globalCmp, err := e.Compare(ctx, req)
	if err != nil {
		return RegressionScanReport{}, err
	}

	minSamples := req.MinSamples
	if minSamples <= 0 {
		minSamples = contract.DefaultComparisonMinSamples
	}
	env := req.Env
	if env == "" {
		env = "prod"
	}

	findings := make([]RegressionFinding, 0, len(globalCmp.Metrics)+16)
	for _, m := range globalCmp.Metrics {
		if m.Status == "regressed" || m.Status == "improved" {
			findings = append(findings, RegressionFinding{
				Scope:       "global",
				ImpactScore: computeImpactScore(m),
				Comparison:  m,
			})
		}
	}

	baseFilter := storage.QueryFilter{
		AppID:       req.AppID,
		Platform:    req.Platform,
		Env:         env,
		AppVersion:  req.BaselineVersion,
		Algorithm:   req.Algorithm,
		StartDayUTC: req.StartDayUTC,
		EndDayUTC:   req.EndDayUTC,
	}
	candFilter := baseFilter
	candFilter.AppVersion = req.CandidateVersion

	// Per-scene fluency regression scan (isolated strictly by identical (scene, algorithm)).
	baseFl, err := e.store.QueryFluency(ctx, baseFilter)
	if err != nil {
		return RegressionScanReport{}, err
	}
	candFl, err := e.store.QueryFluency(ctx, candFilter)
	if err != nil {
		return RegressionScanReport{}, err
	}
	type flKey struct{ scene, algo string }
	baseFlMap := map[flKey]storage.FluencyAggregate{}
	for _, row := range baseFl {
		baseFlMap[flKey{row.Scene, row.Algorithm}] = row
	}
	for _, crow := range candFl {
		brow, ok := baseFlMap[flKey{crow.Scene, crow.Algorithm}]
		if !ok || brow.TotalFrames == 0 || crow.TotalFrames == 0 {
			continue
		}
		bRate := (float64(brow.JankFrames) / float64(brow.TotalFrames)) * 100.0
		cRate := (float64(crow.JankFrames) / float64(crow.TotalFrames)) * 100.0
		cmp := contract.CompareValues(
			"scene_fluency_jank_rate_pct", "%", "lower_is_better",
			&bRate, brow.ValidWindows, "",
			&cRate, crow.ValidWindows, "",
			minSamples, 0.5,
		)
		cmp = enrichWithProportionStats(cmp, brow.JankFrames, brow.TotalFrames, crow.JankFrames, crow.TotalFrames, DefaultSignificanceAlpha)
		if cmp.Status == "regressed" || cmp.Status == "improved" {
			findings = append(findings, RegressionFinding{
				Scope:       "scene:" + crow.Scene,
				Scene:       crow.Scene,
				Algorithm:   crow.Algorithm,
				ImpactScore: computeImpactScore(cmp),
				Comparison:  cmp,
			})
		}
	}

	// Per-scene memory P90 regression scan.
	baseRes, err := e.store.QueryResources(ctx, baseFilter)
	if err != nil {
		return RegressionScanReport{}, err
	}
	candRes, err := e.store.QueryResources(ctx, candFilter)
	if err != nil {
		return RegressionScanReport{}, err
	}
	baseResMap := map[string]storage.ResourceAggregate{}
	for _, row := range baseRes {
		baseResMap[row.Scene] = row
	}
	for _, crow := range candRes {
		brow, ok := baseResMap[crow.Scene]
		if !ok {
			continue
		}
		bMem := contract.QuantileMemoryMiB(brow.MemoryPeakHistMiB, 0.90, minSamples)
		cMem := contract.QuantileMemoryMiB(crow.MemoryPeakHistMiB, 0.90, minSamples)
		cmp := contract.CompareValues(
			"scene_memory_peak_p90_mib", "MiB", "lower_is_better",
			bMem.Value, bMem.SampleSize, bMem.MissingReason,
			cMem.Value, cMem.SampleSize, cMem.MissingReason,
			minSamples, 16.0,
		)
		cmp = enrichWithHistogramStats(cmp, brow.MemoryPeakHistMiB, crow.MemoryPeakHistMiB, bMem, cMem, DefaultSignificanceAlpha)
		if cmp.Status == "regressed" || cmp.Status == "improved" {
			findings = append(findings, RegressionFinding{
				Scope:       "scene:" + crow.Scene,
				Scene:       crow.Scene,
				ImpactScore: computeImpactScore(cmp),
				Comparison:  cmp,
			})
		}
	}

	regressedCount := 0
	for _, f := range findings {
		if f.Comparison.Status == "regressed" {
			regressedCount++
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		// Regressions strictly precede improvements, then highest impact score first.
		ri := findings[i].Comparison.Status == "regressed"
		rj := findings[j].Comparison.Status == "regressed"
		if ri != rj {
			return ri
		}
		if findings[i].ImpactScore != findings[j].ImpactScore {
			return findings[i].ImpactScore > findings[j].ImpactScore
		}
		return findings[i].Scope+findings[i].Comparison.MetricID < findings[j].Scope+findings[j].Comparison.MetricID
	})

	verdict := globalCmp.Verdict
	if regressedCount > 0 {
		verdict = "regressed"
	}

	return RegressionScanReport{
		Platform:         req.Platform,
		BaselineVersion:  req.BaselineVersion,
		CandidateVersion: req.CandidateVersion,
		Alpha:            DefaultSignificanceAlpha,
		Verdict:          verdict,
		RegressedCount:   regressedCount,
		Findings:         findings,
	}, nil
}

func computeImpactScore(m contract.MetricComparison) float64 {
	effect := 1.0
	if m.RelativePct != nil {
		effect = math.Min(math.Abs(*m.RelativePct), 500.0) / 10.0
	} else if m.AbsoluteDelta != nil {
		effect = math.Abs(*m.AbsoluteDelta)
	}
	confidence := 1.0
	if m.PValue != nil {
		p := math.Max(*m.PValue, 1e-12)
		confidence = math.Max(0.1, -math.Log10(p))
	}
	return math.Round(effect*confidence*100.0) / 100.0
}
