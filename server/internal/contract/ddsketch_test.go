package contract

import (
	"math"
	"math/rand"
	"sort"
	"testing"
)

func TestDDSketchRelativeAccuracyLogNormal(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	const n = 10000
	values := make([]float64, n)
	sk := NewDDSketch(DefaultSketchAlpha)

	// Log-normal latency spanning ~25ms to ~45,000ms (realistic heavy-tailed mobile APM distribution)
	for i := 0; i < n; i++ {
		v := math.Exp(6.6 + 0.72*rng.NormFloat64()) // median ~735ms, heavy right tail
		values[i] = v
		if !sk.Add(v) {
			t.Fatalf("Add(%v) returned false", v)
		}
	}
	sort.Float64s(values)

	quantiles := []float64{0.50, 0.75, 0.90, 0.95, 0.99, 0.999}
	var prev float64
	for _, q := range quantiles {
		got := sk.Quantile(q, DefaultDisplayMinSamples)
		if got.Value == nil || got.Interval == nil {
			t.Fatalf("q=%.3f returned nil value/interval: %+v", q, got)
		}
		if got.Precision != PrecisionDDSketch2Pct {
			t.Fatalf("q=%.3f precision=%q want %q", q, got.Precision, PrecisionDDSketch2Pct)
		}
		if got.SampleSize != n {
			t.Fatalf("q=%.3f sample_size=%d want %d", q, got.SampleSize, n)
		}

		idx := int(math.Ceil(float64(n)*q)) - 1
		if idx < 0 {
			idx = 0
		}
		exact := values[idx]
		relErr := math.Abs(*got.Value-exact) / exact
		// Allow tiny 0.001 margin for decimal rounding on top of theoretical 2% bound.
		if relErr > DefaultSketchAlpha+0.001 {
			t.Errorf("q=%.3f: got=%.2f exact=%.2f relErr=%.4f (> %.3f)", q, *got.Value, exact, relErr, DefaultSketchAlpha)
		}
		if exact < (*got.Interval)[0]-0.1 || exact > (*got.Interval)[1]+0.1 {
			t.Errorf("q=%.3f: exact=%.2f outside Zero-Lie interval [%.2f, %.2f]", q, exact, (*got.Interval)[0], (*got.Interval)[1])
		}
		if *got.Value < prev {
			t.Errorf("non-monotonic quantiles: q=%.3f val=%.2f < prev=%.2f", q, *got.Value, prev)
		}
		prev = *got.Value
	}
}

func TestDDSketchMergeAssociativityAndLosslessness(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	combined := NewDDSketch(DefaultSketchAlpha)
	partA := NewDDSketch(DefaultSketchAlpha)
	partB := NewDDSketch(DefaultSketchAlpha)
	partC := NewDDSketch(DefaultSketchAlpha)

	for i := 0; i < 3000; i++ {
		var v float64
		if i%30 == 0 {
			v = 0.0
		} else {
			v = 0.5 + math.Exp(5.8+0.8*rng.NormFloat64())
		}
		combined.Add(v)
		switch i % 3 {
		case 0:
			partA.Add(v)
		case 1:
			partB.Add(v)
		case 2:
			partC.Add(v)
		}
	}

	merged := partA.Clone()
	merged.Merge(partB)
	merged.Merge(partC)

	if merged.Total() != combined.Total() || merged.ZeroCount != combined.ZeroCount {
		t.Fatalf("merged totals mismatch: got (%d,%d) want (%d,%d)", merged.Total(), merged.ZeroCount, combined.Total(), combined.ZeroCount)
	}
	for _, q := range []float64{0.50, 0.90, 0.95, 0.99} {
		qC := combined.Quantile(q, DefaultDisplayMinSamples)
		qM := merged.Quantile(q, DefaultDisplayMinSamples)
		if *qC.Value != *qM.Value || *qC.Interval != *qM.Interval {
			t.Fatalf("q=%.2f mismatch after merge: combined=%+v merged=%+v", q, qC, qM)
		}
	}
}

func TestDDSketchZeroLieGatingAndInvalidInputs(t *testing.T) {
	var nilSketch *DDSketch
	if res := nilSketch.Quantile(0.90, DefaultDisplayMinSamples); res.MissingReason != MissingNoObservations || res.Value != nil {
		t.Fatalf("nil sketch must report no_observations, got %+v", res)
	}

	sk := NewDDSketch(DefaultSketchAlpha)
	// Invalid values MUST be rejected without incrementing Count.
	if sk.Add(-5) || sk.Add(math.NaN()) || sk.Add(math.Inf(1)) || sk.Add(math.Inf(-1)) {
		t.Fatal("invalid inputs must be rejected")
	}
	if sk.Total() != 0 {
		t.Fatalf("expected 0 count after invalid adds, got %d", sk.Total())
	}

	// Below minSamples gate -> insufficient_samples
	for i := 0; i < 5; i++ {
		sk.Add(500)
	}
	if res := sk.Quantile(0.90, DefaultDisplayMinSamples); res.MissingReason != MissingInsufficientSamples || res.Value != nil || res.SampleSize != 5 {
		t.Fatalf("under-sampled sketch must gate, got %+v", res)
	}

	// All-zero sketch -> exact 0
	zsk := NewDDSketch(DefaultSketchAlpha)
	for i := 0; i < 15; i++ {
		zsk.Add(0)
	}
	zres := zsk.Quantile(0.95, DefaultDisplayMinSamples)
	if zres.Value == nil || *zres.Value != 0 || (*zres.Interval)[0] != 0 || (*zres.Interval)[1] != 0 {
		t.Fatalf("all-zero sketch should yield exact 0, got %+v", zres)
	}
}

func TestQuantileSketchFallbackToHistogram(t *testing.T) {
	var h Histogram
	for i := 0; i < 100; i++ {
		h[DurationBucketIndex(850)]++
	}
	// When sketch is nil, transparently falls back to 19-bucket histogram.
	resHist := QuantileDurationSketch(nil, h, 0.90, DefaultDisplayMinSamples)
	if resHist.Value == nil || resHist.Precision != PrecisionHist19Bucket {
		t.Fatalf("expected hist19 fallback, got %+v", resHist)
	}

	// When sketch is populated (e.g. clustered at 850ms inside [750, 1000]), DDSketch gives ~850ms ±2%!
	sk := NewDDSketch(DefaultSketchAlpha)
	for i := 0; i < 100; i++ {
		sk.Add(850)
	}
	resSketch := QuantileDurationSketch(sk, h, 0.90, DefaultDisplayMinSamples)
	if resSketch.Value == nil || resSketch.Precision != PrecisionDDSketch2Pct {
		t.Fatalf("expected ddsketch_2pct, got %+v", resSketch)
	}
	if math.Abs(*resSketch.Value-850.0)/850.0 > DefaultSketchAlpha+0.001 {
		t.Fatalf("expected ~850ms within 2%%, got %v", *resSketch.Value)
	}
}
