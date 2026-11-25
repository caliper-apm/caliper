package contract

import (
	"errors"
	"math"
)

const (
	// DefaultDisplayMinSamples is the minimum observations required to show a point estimate.
	DefaultDisplayMinSamples = 10
	// DefaultComparisonMinSamples is the minimum observations per arm required to compute a delta.
	DefaultComparisonMinSamples = 100
	// MaxSamplingRatePPM is 100% in parts-per-million.
	MaxSamplingRatePPM = 1_000_000
	// HistogramBucketCount is len(DurationBoundsMS) + 1 (final bucket is overflow > 120000 ms).
	HistogramBucketCount = 19
)

// DurationBoundsMS defines inclusive upper bounds in milliseconds.
// Bucket i covers (bounds[i-1], bounds[i]]; bucket 0 covers [0, 16]; bucket 18 covers (120000, +Inf).
var DurationBoundsMS = [18]float64{
	16, 33, 50, 100, 250, 500, 750, 1000, 1500, 2000,
	3000, 5000, 8000, 10000, 15000, 30000, 60000, 120000,
}

// MemoryBoundsMiB defines inclusive upper bounds in MiB for mergeable memory footprint histograms.
var MemoryBoundsMiB = [18]float64{
	64, 96, 128, 160, 192, 224, 256, 320, 384, 448,
	512, 640, 768, 896, 1024, 1536, 2048, 4096,
}

// MissingReason codes strictly enforce Zero-Lie semantics.
const (
	MissingNoObservations      = "no_observations"
	MissingInsufficientSamples = "insufficient_samples"
	MissingDenominator         = "missing_denominator"
	MissingHistogramOverflow   = "histogram_overflow"
	MissingIncompatiblePolicy  = "incompatible_policy"
	MissingIncompatibleAlgo    = "incompatible_algorithm"
)

// FNV1a32 computes the exact unsigned 32-bit FNV-1a hash over ASCII bytes.
func FNV1a32(input string) uint32 {
	const (
		offset32 uint32 = 2166136261
		prime32  uint32 = 16777619
	)
	h := offset32
	for i := 0; i < len(input); i++ {
		h ^= uint32(input[i])
		h *= prime32
	}
	return h
}

// SamplingBucketPPM deterministically maps (installSeed, policyID) to [0, 999999].
func SamplingBucketPPM(installSeed, policyID string) uint32 {
	return FNV1a32(installSeed+"|"+policyID) % MaxSamplingRatePPM
}

// IsAdmitted evaluates deterministic installation sampling at ratePPM in [0, 1_000_000].
func IsAdmitted(installSeed, policyID string, ratePPM uint32) bool {
	if ratePPM == 0 {
		return false
	}
	if ratePPM >= MaxSamplingRatePPM {
		return true
	}
	return SamplingBucketPPM(installSeed, policyID) < ratePPM
}

// DurationBucketIndex returns the histogram bucket index in [0, 18] for a non-negative duration in ms.
func DurationBucketIndex(ms float64) int {
	return bucketIndex(DurationBoundsMS[:], ms)
}

// MemoryBucketIndex returns the histogram bucket index in [0, 18] for a non-negative memory value in MiB.
func MemoryBucketIndex(mib float64) int {
	return bucketIndex(MemoryBoundsMiB[:], mib)
}

func bucketIndex(bounds []float64, value float64) int {
	if math.IsNaN(value) || value < 0 {
		return len(bounds)
	}
	for i, upper := range bounds {
		if value <= upper {
			return i
		}
	}
	return len(bounds)
}

// Histogram stores fixed-bound non-cumulative bucket counts (18 bounded + 1 overflow).
type Histogram [HistogramBucketCount]int64

// Add merges another histogram into h.
func (h *Histogram) Add(other Histogram) {
	for i := range h {
		h[i] += other[i]
	}
}

// AddSlice merges a 19-element slice into h.
func (h *Histogram) AddSlice(counts []int64) error {
	if len(counts) != HistogramBucketCount {
		return errors.New("histogram slice length must be 19")
	}
	for i, c := range counts {
		if c < 0 {
			return errors.New("histogram bucket count must be non-negative")
		}
		h[i] += c
	}
	return nil
}

// Total returns total observed count across all buckets including overflow.
func (h Histogram) Total() int64 {
	var total int64
	for _, c := range h {
		total += c
	}
	return total
}

// QuantilePrecision identifies how a quantile estimate was computed.
const (
	PrecisionDDSketch2Pct = "ddsketch_2pct"
	PrecisionHist19Bucket = "hist19_interp"
)

// QuantileResult carries both the interval and explicit missing reason (Zero-Lie contract).
type QuantileResult struct {
	Value         *float64    `json:"value"`
	Interval      *[2]float64 `json:"interval,omitempty"`
	SampleSize    int64       `json:"sample_size"`
	Precision     string      `json:"precision,omitempty"`
	MissingReason string      `json:"missing_reason,omitempty"`
}

// QuantileDuration computes quantile q in (0, 1] from a duration histogram with minSamples gate.
func QuantileDuration(h Histogram, q float64, minSamples int64) QuantileResult {
	return quantileFromBounds(DurationBoundsMS[:], h, q, minSamples)
}

// QuantileMemoryMiB computes quantile q in (0, 1] from a memory MiB histogram with minSamples gate.
func QuantileMemoryMiB(h Histogram, q float64, minSamples int64) QuantileResult {
	return quantileFromBounds(MemoryBoundsMiB[:], h, q, minSamples)
}

func quantileFromBounds(bounds []float64, h Histogram, q float64, minSamples int64) QuantileResult {
	total := h.Total()
	if total == 0 {
		return QuantileResult{SampleSize: 0, MissingReason: MissingNoObservations}
	}
	if total < minSamples {
		return QuantileResult{SampleSize: total, MissingReason: MissingInsufficientSamples}
	}
	rank := float64(total) * q
	target := int64(math.Ceil(rank))
	if target < 1 {
		target = 1
	}
	var cumulative int64
	for i, count := range h {
		prevCum := cumulative
		cumulative += count
		if cumulative >= target {
			if i == len(bounds) {
				// Overflow bucket: MUST NOT clamp or fabricate a numeric estimate.
				return QuantileResult{SampleSize: total, MissingReason: MissingHistogramOverflow}
			}
			lower := 0.0
			if i > 0 {
				lower = bounds[i-1]
			}
			upper := bounds[i]
			// Standard intra-bucket linear interpolation (Prometheus histogram_quantile semantics)
			// while preserving the exact Zero-Lie [lower, upper] bucket interval.
			frac := 1.0
			if count > 0 {
				frac = (rank - float64(prevCum)) / float64(count)
				if frac < 0 {
					frac = 0
				} else if frac > 1 {
					frac = 1
				}
			}
			val := math.Round((lower+frac*(upper-lower))*10) / 10
			interval := [2]float64{lower, upper}
			return QuantileResult{
				Value:      &val,
				Interval:   &interval,
				SampleSize: total,
				Precision:  PrecisionHist19Bucket,
			}
		}
	}
	return QuantileResult{SampleSize: total, MissingReason: MissingNoObservations}
}

// FluencyAssessment represents Zero-Lie frame window classification.
type FluencyAssessment struct {
	JankRatio *float64 `json:"jank_ratio"`
	Severity  string   `json:"severity"`
}

// ClassifyFluency classifies a fluency window. frameCount == 0 MUST yield unknown, never 0% jank.
func ClassifyFluency(frameCount, jankFrameCount int64) FluencyAssessment {
	if frameCount <= 0 || jankFrameCount < 0 || jankFrameCount > frameCount {
		return FluencyAssessment{JankRatio: nil, Severity: "unknown"}
	}
	ratio := float64(jankFrameCount) / float64(frameCount)
	severity := "healthy"
	switch {
	case ratio >= 0.10:
		severity = "critical"
	case ratio >= 0.03:
		severity = "degraded"
	}
	return FluencyAssessment{JankRatio: &ratio, Severity: severity}
}

// ValidateStartupStages enforces stage conservation: native + firstFrame <= total.
func ValidateStartupStages(totalMS float64, nativeInitMS, runtimeInitMS, firstFrameMS *float64) bool {
	if math.IsNaN(totalMS) || math.IsInf(totalMS, 0) || totalMS < 0 {
		return false
	}
	var sum float64
	for _, stage := range []*float64{nativeInitMS, runtimeInitMS, firstFrameMS} {
		if stage == nil {
			continue
		}
		if math.IsNaN(*stage) || math.IsInf(*stage, 0) || *stage < 0 {
			return false
		}
		sum += *stage
	}
	return sum <= totalMS+1e-6
}

// SlidingQuota enforces a strict rolling window event cap.
type SlidingQuota struct {
	WindowMS   int64
	Cap        int
	timestamps []int64
}

// TryConsume records timestampMS if within cap; returns true if admitted.
func (q *SlidingQuota) TryConsume(timestampMS int64) bool {
	if q.Cap <= 0 || q.WindowMS <= 0 {
		return false
	}
	cutoff := timestampMS - q.WindowMS
	kept := q.timestamps[:0]
	for _, ts := range q.timestamps {
		if ts > cutoff {
			kept = append(kept, ts)
		}
	}
	q.timestamps = kept
	if len(q.timestamps) >= q.Cap {
		return false
	}
	q.timestamps = append(q.timestamps, timestampMS)
	return true
}

// MetricComparison holds gated baseline vs candidate comparison outputs.
type MetricComparison struct {
	MetricID       string   `json:"metric_id"`
	Unit           string   `json:"unit"`
	Direction      string   `json:"direction"` // "lower_is_better" | "higher_is_better"
	BaselineValue  *float64 `json:"baseline_value"`
	BaselineN      int64    `json:"baseline_sample_size"`
	CandidateValue *float64 `json:"candidate_value"`
	CandidateN     int64    `json:"candidate_sample_size"`
	AbsoluteDelta   *float64    `json:"absolute_delta"` // candidate - baseline (pp for percentages)
	RelativePct     *float64    `json:"relative_pct"`   // nil when baseline == 0
	Status          string      `json:"status"`         // "regressed" | "improved" | "neutral" | "gated"
	GateReason      string      `json:"gate_reason,omitempty"`
	StatTest        string      `json:"stat_test,omitempty"` // "two_proportion_z" | "cluster_proportion_z" | "ddsketch_ks" | "histogram_ks" | "threshold_only"
	PValue          *float64    `json:"p_value,omitempty"`
	AdjustedPValue  *float64    `json:"adjusted_p_value,omitempty"` // Benjamini-Hochberg FDR q-value in multi-scene scans
	StatSignificant *bool       `json:"statistically_significant,omitempty"`
	BaselineCI95    *[2]float64 `json:"baseline_ci95,omitempty"`
	CandidateCI95   *[2]float64 `json:"candidate_ci95,omitempty"`
}

// CompareValues applies sample-size gating, zero-denominator protection, and regression classification.
func CompareValues(
	metricID, unit, direction string,
	baselineVal *float64, baselineN int64, baselineMissing string,
	candidateVal *float64, candidateN int64, candidateMissing string,
	minSamples int64,
	materialThreshold float64,
) MetricComparison {
	res := MetricComparison{
		MetricID:       metricID,
		Unit:           unit,
		Direction:      direction,
		BaselineValue:  baselineVal,
		BaselineN:      baselineN,
		CandidateValue: candidateVal,
		CandidateN:     candidateN,
	}

	if baselineMissing != "" {
		res.Status = "gated"
		res.GateReason = "baseline_" + baselineMissing
		return res
	}
	if candidateMissing != "" {
		res.Status = "gated"
		res.GateReason = "candidate_" + candidateMissing
		return res
	}
	if baselineVal == nil || candidateVal == nil {
		res.Status = "gated"
		res.GateReason = MissingNoObservations
		return res
	}
	if baselineN < minSamples || candidateN < minSamples {
		res.Status = "gated"
		res.GateReason = MissingInsufficientSamples
		return res
	}

	delta := *candidateVal - *baselineVal
	res.AbsoluteDelta = &delta
	if *baselineVal != 0 {
		rel := (delta / *baselineVal) * 100.0
		res.RelativePct = &rel
	}

	if math.Abs(delta) < materialThreshold {
		res.Status = "neutral"
		return res
	}
	if direction == "higher_is_better" {
		if delta < 0 {
			res.Status = "regressed"
		} else {
			res.Status = "improved"
		}
	} else {
		if delta > 0 {
			res.Status = "regressed"
		} else {
			res.Status = "improved"
		}
	}
	return res
}
