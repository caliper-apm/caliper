package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type goldenVectors struct {
	DeterministicSampling []struct {
		InstallSeed       string `json:"install_seed"`
		PolicyID          string `json:"policy_id"`
		ExpectedHashU32   uint32 `json:"expected_hash_u32"`
		ExpectedBucketPPM uint32 `json:"expected_bucket_ppm"`
		Admitted100k      *bool  `json:"admitted_at_100000_ppm"`
		Admitted132696    *bool  `json:"admitted_at_132696_ppm"`
		Admitted132697    *bool  `json:"admitted_at_132697_ppm"`
		Admitted500k      *bool  `json:"admitted_at_500000_ppm"`
		Admitted717152    *bool  `json:"admitted_at_717152_ppm"`
		Admitted30k       *bool  `json:"admitted_at_30000_ppm"`
		Admitted600k      *bool  `json:"admitted_at_600000_ppm"`
		Admitted436213    *bool  `json:"admitted_at_436213_ppm"`
		Admitted436214    *bool  `json:"admitted_at_436214_ppm"`
	} `json:"deterministic_sampling_fnv1a32"`
	DurationBoundsMS []float64 `json:"duration_histogram_bounds_ms"`
	HistogramCases   []struct {
		Name                  string      `json:"name"`
		Counts                []int64     `json:"counts"`
		ExpectedSampleSize    int64       `json:"expected_sample_size"`
		ExpectedP50IntervalMS *[2]float64 `json:"expected_p50_interval_ms"`
		ExpectedP90IntervalMS *[2]float64 `json:"expected_p90_interval_ms"`
		ExpectedMissingReason string      `json:"expected_missing_reason"`
		ExpectedP90Missing    string      `json:"expected_p90_missing_reason"`
	} `json:"histogram_quantile_cases"`
	FluencyCases []struct {
		Name              string   `json:"name"`
		FrameCount        int64    `json:"frame_count"`
		JankFrameCount    int64    `json:"jank_frame_count"`
		ExpectedJankRatio *float64 `json:"expected_jank_ratio"`
		ExpectedSeverity  string   `json:"expected_severity"`
	} `json:"fluency_window_cases"`
	SlidingQuotaCases []struct {
		Name             string  `json:"name"`
		WindowMS         int64   `json:"window_ms"`
		Cap              int     `json:"cap"`
		TimestampsMS     []int64 `json:"timestamps_ms"`
		ExpectedAdmitted []bool  `json:"expected_admitted"`
	} `json:"sliding_quota_24h_cases"`
}

func TestGoldenContractVectors(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "..", "packages", "sdk-core", "fixtures", "contract-vectors.json")
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	var vectors goldenVectors
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("parse golden fixture: %v", err)
	}

	// 1. Duration bounds parity
	if len(vectors.DurationBoundsMS) != len(DurationBoundsMS) {
		t.Fatalf("bounds length mismatch: got %d want %d", len(DurationBoundsMS), len(vectors.DurationBoundsMS))
	}
	for i, want := range vectors.DurationBoundsMS {
		if DurationBoundsMS[i] != want {
			t.Fatalf("bound[%d]=%v want %v", i, DurationBoundsMS[i], want)
		}
	}

	// 2. Deterministic FNV-1a sampling parity
	for _, tc := range vectors.DeterministicSampling {
		gotHash := FNV1a32(tc.InstallSeed + "|" + tc.PolicyID)
		if gotHash != tc.ExpectedHashU32 {
			t.Errorf("FNV1a32(%s|%s)=%d want %d", tc.InstallSeed, tc.PolicyID, gotHash, tc.ExpectedHashU32)
		}
		gotBucket := SamplingBucketPPM(tc.InstallSeed, tc.PolicyID)
		if gotBucket != tc.ExpectedBucketPPM {
			t.Errorf("bucket=%d want %d", gotBucket, tc.ExpectedBucketPPM)
		}
		checkAdmit := func(rate uint32, want *bool) {
			if want == nil {
				return
			}
			if got := IsAdmitted(tc.InstallSeed, tc.PolicyID, rate); got != *want {
				t.Errorf("IsAdmitted(%s, %d)=%v want %v", tc.InstallSeed, rate, got, *want)
			}
		}
		checkAdmit(100000, tc.Admitted100k)
		checkAdmit(132696, tc.Admitted132696)
		checkAdmit(132697, tc.Admitted132697)
		checkAdmit(500000, tc.Admitted500k)
		checkAdmit(717152, tc.Admitted717152)
		checkAdmit(30000, tc.Admitted30k)
		checkAdmit(600000, tc.Admitted600k)
		checkAdmit(436213, tc.Admitted436213)
		checkAdmit(436214, tc.Admitted436214)
	}

	// 3. Histogram quantile & overflow parity
	for _, tc := range vectors.HistogramCases {
		var h Histogram
		if err := h.AddSlice(tc.Counts); err != nil {
			t.Fatalf("%s: AddSlice error: %v", tc.Name, err)
		}
		p50 := QuantileDuration(h, 0.50, DefaultDisplayMinSamples)
		p90 := QuantileDuration(h, 0.90, DefaultDisplayMinSamples)
		if p50.SampleSize != tc.ExpectedSampleSize {
			t.Errorf("%s: sample_size=%d want %d", tc.Name, p50.SampleSize, tc.ExpectedSampleSize)
		}
		if tc.ExpectedP50IntervalMS == nil {
			if p50.Interval != nil || p50.Value != nil {
				t.Errorf("%s: expected nil p50, got %+v", tc.Name, p50)
			}
			if tc.ExpectedMissingReason != "" && p50.MissingReason != tc.ExpectedMissingReason {
				t.Errorf("%s: p50 missing=%q want %q", tc.Name, p50.MissingReason, tc.ExpectedMissingReason)
			}
		} else if p50.Interval == nil || *p50.Interval != *tc.ExpectedP50IntervalMS {
			t.Errorf("%s: p50 interval=%v want %v", tc.Name, p50.Interval, tc.ExpectedP50IntervalMS)
		}

		if tc.ExpectedP90IntervalMS == nil {
			if p90.Interval != nil || p90.Value != nil {
				t.Errorf("%s: expected nil p90, got %+v", tc.Name, p90)
			}
			wantReason := tc.ExpectedP90Missing
			if wantReason == "" {
				wantReason = tc.ExpectedMissingReason
			}
			if wantReason != "" && p90.MissingReason != wantReason {
				t.Errorf("%s: p90 missing=%q want %q", tc.Name, p90.MissingReason, wantReason)
			}
		} else if p90.Interval == nil || *p90.Interval != *tc.ExpectedP90IntervalMS {
			t.Errorf("%s: p90 interval=%v want %v", tc.Name, p90.Interval, tc.ExpectedP90IntervalMS)
		}
	}

	// 4. Fluency zero-frame contract
	for _, tc := range vectors.FluencyCases {
		got := ClassifyFluency(tc.FrameCount, tc.JankFrameCount)
		if got.Severity != tc.ExpectedSeverity {
			t.Errorf("%s: severity=%q want %q", tc.Name, got.Severity, tc.ExpectedSeverity)
		}
		if tc.ExpectedJankRatio == nil {
			if got.JankRatio != nil {
				t.Errorf("%s: expected nil jank_ratio, got %v", tc.Name, *got.JankRatio)
			}
		} else if got.JankRatio == nil || *got.JankRatio != *tc.ExpectedJankRatio {
			t.Errorf("%s: jank_ratio=%v want %v", tc.Name, got.JankRatio, *tc.ExpectedJankRatio)
		}
	}

	// 5. Sliding 24h quota
	for _, tc := range vectors.SlidingQuotaCases {
		q := SlidingQuota{WindowMS: tc.WindowMS, Cap: tc.Cap}
		for i, ts := range tc.TimestampsMS {
			if got := q.TryConsume(ts); got != tc.ExpectedAdmitted[i] {
				t.Errorf("%s[%d] ts=%d: admitted=%v want %v", tc.Name, i, ts, got, tc.ExpectedAdmitted[i])
			}
		}
	}
}

func TestCompareValuesGatesAndZeroBaseline(t *testing.T) {
	zero := 0.0
	two := 2.0
	c := CompareValues("jank_pct", "%", "lower_is_better", &zero, 150, "", &two, 150, "", DefaultComparisonMinSamples, 0.5)
	if c.Status != "regressed" {
		t.Fatalf("expected regressed, got %s", c.Status)
	}
	if c.AbsoluteDelta == nil || *c.AbsoluteDelta != 2.0 {
		t.Fatalf("expected +2.0 pp delta, got %v", c.AbsoluteDelta)
	}
	if c.RelativePct != nil {
		t.Fatalf("expected nil relative_pct when baseline==0, got %v", *c.RelativePct)
	}

	gated := CompareValues("jank_pct", "%", "lower_is_better", &zero, 50, "", &two, 150, "", DefaultComparisonMinSamples, 0.5)
	if gated.Status != "gated" || gated.GateReason != MissingInsufficientSamples {
		t.Fatalf("expected gated insufficient_samples, got %+v", gated)
	}
}

func TestValidateStartupStages(t *testing.T) {
	n, r, f := 180.0, 220.0, 140.0
	if !ValidateStartupStages(600, &n, &r, &f) {
		t.Fatal("valid startup stages rejected")
	}
	if ValidateStartupStages(500, &n, &r, &f) {
		t.Fatal("over-budget startup stages must be rejected")
	}
}
