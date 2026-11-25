package contract

import (
	"fmt"
	"math"
	"sort"
)

const (
	// DefaultSketchAlpha is the default maximum relative quantile error (2%).
	DefaultSketchAlpha = 0.02
	// DefaultSketchMinValue maps non-negative values <= 1e-6 into the exact zero bucket.
	DefaultSketchMinValue = 1e-6
	// SketchBinAbsent is the Int32 sentinel used in OLAP storage (ClickHouse) when no observation was recorded.
	SketchBinAbsent int32 = math.MinInt32
	// SketchBinZero is the Int32 sentinel used in OLAP storage for exact-zero observations (<= DefaultSketchMinValue).
	SketchBinZero int32 = math.MinInt32 + 1
)

// DDSketch is a clean-room, zero-dependency, fully-mergeable logarithmic quantile sketch
// guaranteeing relative error <= Alpha across arbitrary positive real values (durations, memory).
//
// Bucket k covers the half-open logarithmic interval (gamma^(k-1), gamma^k], where
// gamma = (1 + Alpha) / (1 - Alpha). The representative value 2*gamma^k / (gamma + 1)
// satisfies |estimate - true| / true <= Alpha for every value in the bucket.
type DDSketch struct {
	Alpha     float64         `json:"alpha"`
	ZeroCount int64           `json:"zero_count,omitempty"`
	Count     int64           `json:"count"`
	Bins      map[int32]int64 `json:"bins,omitempty"`

	gamma     float64
	logGamma  float64
	midFactor float64
}

// NewDDSketch creates an empty DDSketch with relative accuracy alpha in (0, 1).
// Invalid or zero alpha defaults to DefaultSketchAlpha (0.02 = 2%).
func NewDDSketch(alpha float64) *DDSketch {
	if math.IsNaN(alpha) || math.IsInf(alpha, 0) || alpha <= 0 || alpha >= 1 {
		alpha = DefaultSketchAlpha
	}
	s := &DDSketch{
		Alpha: alpha,
		Bins:  make(map[int32]int64),
	}
	s.ensureParams()
	return s
}

func (s *DDSketch) ensureParams() {
	if s.Alpha <= 0 || s.Alpha >= 1 || math.IsNaN(s.Alpha) || math.IsInf(s.Alpha, 0) {
		s.Alpha = DefaultSketchAlpha
	}
	if s.gamma <= 1 {
		s.gamma = (1.0 + s.Alpha) / (1.0 - s.Alpha)
		s.logGamma = math.Log(s.gamma)
		s.midFactor = 2.0 / (s.gamma + 1.0)
	}
}

// Total returns total recorded observations in the sketch.
func (s *DDSketch) Total() int64 {
	if s == nil {
		return 0
	}
	return s.Count
}

// Add records a single non-negative finite observation. Returns false if ignored (NaN/Inf/negative).
func (s *DDSketch) Add(value float64) bool {
	return s.AddWeighted(value, 1)
}

// AddWeighted records count observations of value. Returns false if ignored.
func (s *DDSketch) AddWeighted(value float64, count int64) bool {
	if s == nil || count <= 0 || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return false
	}
	s.ensureParams()
	if value <= DefaultSketchMinValue {
		s.ZeroCount += count
		s.Count += count
		return true
	}
	idx := int32(math.Ceil(math.Log(value) / s.logGamma))
	if s.Bins == nil {
		s.Bins = make(map[int32]int64)
	}
	s.Bins[idx] += count
	s.Count += count
	return true
}

// Merge folds all observations from other into s.
// Merging sketches with identical Alpha is associative, commutative, and lossless.
func (s *DDSketch) Merge(other *DDSketch) {
	if s == nil || other == nil || other.Count == 0 {
		return
	}
	s.ensureParams()
	other.ensureParams()

	if math.Abs(s.Alpha-other.Alpha) < 1e-9 {
		s.ZeroCount += other.ZeroCount
		s.Count += other.Count
		if len(other.Bins) > 0 {
			if s.Bins == nil {
				s.Bins = make(map[int32]int64, len(other.Bins))
			}
			for k, c := range other.Bins {
				if c > 0 {
					s.Bins[k] += c
				}
			}
		}
		return
	}

	// Cross-alpha defensive fallback: re-project each non-empty bucket at its representative midpoint.
	if other.ZeroCount > 0 {
		s.ZeroCount += other.ZeroCount
		s.Count += other.ZeroCount
	}
	for k, c := range other.Bins {
		if c > 0 {
			_, _, mid := other.BucketBounds(k)
			s.AddWeighted(mid, c)
		}
	}
}

// Clone returns an independent deep copy of s.
func (s *DDSketch) Clone() *DDSketch {
	if s == nil {
		return nil
	}
	s.ensureParams()
	cp := &DDSketch{
		Alpha:     s.Alpha,
		ZeroCount: s.ZeroCount,
		Count:     s.Count,
		gamma:     s.gamma,
		logGamma:  s.logGamma,
		midFactor: s.midFactor,
	}
	if len(s.Bins) > 0 {
		cp.Bins = make(map[int32]int64, len(s.Bins))
		for k, c := range s.Bins {
			cp.Bins[k] = c
		}
	}
	return cp
}

// BucketBounds returns (lower, upper, midpoint) for logarithmic bin index k.
func (s *DDSketch) BucketBounds(k int32) (lower, upper, midpoint float64) {
	s.ensureParams()
	lower = math.Pow(s.gamma, float64(k-1))
	upper = math.Pow(s.gamma, float64(k))
	midpoint = upper * s.midFactor
	return lower, upper, midpoint
}

// SortedKeys returns all positive non-empty bin indices in ascending order.
func (s *DDSketch) SortedKeys() []int32 {
	if s == nil || len(s.Bins) == 0 {
		return nil
	}
	keys := make([]int32, 0, len(s.Bins))
	for k, c := range s.Bins {
		if c > 0 {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func (s *DDSketch) precisionTag() string {
	s.ensureParams()
	if math.Abs(s.Alpha-DefaultSketchAlpha) < 1e-9 {
		return PrecisionDDSketch2Pct
	}
	return fmt.Sprintf("ddsketch_%gpct", s.Alpha*100.0)
}

// Quantile evaluates quantile q in [0, 1] with Zero-Lie minSamples gating and exact bucket interval.
func (s *DDSketch) Quantile(q float64, minSamples int64) QuantileResult {
	total := s.Total()
	if total == 0 {
		return QuantileResult{SampleSize: 0, MissingReason: MissingNoObservations}
	}
	if total < minSamples {
		return QuantileResult{SampleSize: total, MissingReason: MissingInsufficientSamples}
	}
	if math.IsNaN(q) || q < 0 {
		q = 0
	} else if q > 1 {
		q = 1
	}

	rank := float64(total) * q
	target := int64(math.Ceil(rank))
	if target < 1 {
		target = 1
	}

	prec := s.precisionTag()
	if s.ZeroCount >= target {
		zero := 0.0
		iv := [2]float64{0, 0}
		return QuantileResult{
			Value:      &zero,
			Interval:   &iv,
			SampleSize: total,
			Precision:  prec,
		}
	}

	keys := s.SortedKeys()
	cum := s.ZeroCount
	for _, k := range keys {
		cum += s.Bins[k]
		if cum >= target {
			lower, upper, mid := s.BucketBounds(k)
			val := roundSketchValue(mid)
			lo := floorSketchBound(lower)
			hi := ceilSketchBound(upper)
			if val < lo {
				lo = val
			}
			if val > hi {
				hi = val
			}
			iv := [2]float64{lo, hi}
			return QuantileResult{
				Value:      &val,
				Interval:   &iv,
				SampleSize: total,
				Precision:  prec,
			}
		}
	}

	return QuantileResult{SampleSize: total, MissingReason: MissingNoObservations}
}

// AddToDDSketch lazily allocates *dst on first valid observation and adds value.
func AddToDDSketch(dst **DDSketch, value float64) {
	if dst == nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return
	}
	if *dst == nil {
		*dst = NewDDSketch(DefaultSketchAlpha)
	}
	(*dst).Add(value)
}

// MergeDDSketch lazily allocates *dst and merges non-empty src into it.
func MergeDDSketch(dst **DDSketch, src *DDSketch) {
	if dst == nil || src == nil || src.Count == 0 {
		return
	}
	if *dst == nil {
		*dst = NewDDSketch(src.Alpha)
	}
	(*dst).Merge(src)
}

// QuantileDurationSketch prefers DDSketch when populated and transparently falls back
// to the 19-bucket duration histogram (Zero-Break compatibility for legacy / window-only rows).
func QuantileDurationSketch(sk *DDSketch, h Histogram, q float64, minSamples int64) QuantileResult {
	if sk != nil && sk.Total() > 0 {
		return sk.Quantile(q, minSamples)
	}
	return QuantileDuration(h, q, minSamples)
}

// QuantileMemoryMiBSketch prefers DDSketch when populated and transparently falls back
// to the 19-bucket memory MiB histogram.
func QuantileMemoryMiBSketch(sk *DDSketch, h Histogram, q float64, minSamples int64) QuantileResult {
	if sk != nil && sk.Total() > 0 {
		return sk.Quantile(q, minSamples)
	}
	return QuantileMemoryMiB(h, q, minSamples)
}

// DDSketchBinForValue projects a single non-negative observation into its compact Int32 DDSketch
// bin index for OLAP persistence (e.g. ClickHouse Int32 column aggregated via sumMapIf).
// Returns SketchBinAbsent for invalid/negative values and SketchBinZero for values <= DefaultSketchMinValue.
func DDSketchBinForValue(value, alpha float64) int32 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return SketchBinAbsent
	}
	if alpha <= 0 || alpha >= 1 || math.IsNaN(alpha) || math.IsInf(alpha, 0) {
		alpha = DefaultSketchAlpha
	}
	if value <= DefaultSketchMinValue {
		return SketchBinZero
	}
	gamma := (1.0 + alpha) / (1.0 - alpha)
	idx := int32(math.Ceil(math.Log(value) / math.Log(gamma)))
	if idx == SketchBinAbsent || idx == SketchBinZero {
		return SketchBinZero
	}
	return idx
}

// DDSketchFromBinMap reconstructs a DDSketch from sparse (bin_index -> count) pairs aggregated in OLAP storage.
// Returns nil if no valid positive counts exist so callers fall back cleanly to 19-bucket histograms.
func DDSketchFromBinMap(alpha float64, bins map[int32]int64) *DDSketch {
	if len(bins) == 0 {
		return nil
	}
	sk := NewDDSketch(alpha)
	for idx, cnt := range bins {
		if cnt <= 0 || idx == SketchBinAbsent {
			continue
		}
		if idx == SketchBinZero {
			sk.ZeroCount += cnt
			sk.Count += cnt
			continue
		}
		sk.Bins[idx] += cnt
		sk.Count += cnt
	}
	if sk.Count == 0 {
		return nil
	}
	return sk
}

func roundSketchValue(v float64) float64 {
	switch {
	case v < 10:
		return math.Round(v*1000) / 1000
	case v < 100:
		return math.Round(v*100) / 100
	default:
		return math.Round(v*10) / 10
	}
}

func floorSketchBound(v float64) float64 {
	switch {
	case v < 10:
		return math.Floor(v*1000) / 1000
	case v < 100:
		return math.Floor(v*100) / 100
	default:
		return math.Floor(v*10) / 10
	}
}

func ceilSketchBound(v float64) float64 {
	switch {
	case v < 10:
		return math.Ceil(v*1000) / 1000
	case v < 100:
		return math.Ceil(v*100) / 100
	default:
		return math.Ceil(v*10) / 10
	}
}
