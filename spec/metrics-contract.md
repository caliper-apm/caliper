# Caliper APM — Zero-Lie Metrics & Statistical Contract (v0.1)

Every backend query, console visualization, and alert evaluation MUST obey these mathematical invariants.

---

## 1. Missing Data & Zero Semantics
- **Missing ≠ Zero**: Absent observations MUST return `value: null` with an explicit `missing_reason` (`no_observations`, `insufficient_samples`, `missing_denominator`, `histogram_overflow`, `pre_release`, `incompatible_policy`).
- **Valid Zero**: A numeric `0` or `0.0%` is permitted ONLY when backed by an observed non-zero denominator (`denominator > 0` or `valid_windows > 0`).

---

## 2. Mergeable Histograms & Quantiles (P50 / P90 / P95)
- **Never average daily percentiles.** Multi-day and multi-release quantiles MUST be computed by summing bucket counts across compatible fixed-bound histograms (or mergeable sketches).
- **Fixed Duration Buckets (ms, inclusive upper bounds)**:
  `[16, 33, 50, 100, 250, 500, 750, 1000, 1500, 2000, 3000, 5000, 8000, 10000, 15000, 30000, 60000, 120000, +Inf]`
- **Bucket Count Conservation**: `len(buckets) == len(bounds) + 1`, where the final bucket is strictly `> max_bound` (overflow).
- **Overflow Rule**: If the target quantile rank falls inside the overflow bucket (`> 120000 ms`), the quantile point estimate MUST be `null` with `missing_reason = "histogram_overflow"` (never clamp to 120000 ms).
- **Interval Transparency**: Quantiles derived from fixed buckets expose `[lower_bound, upper_bound]` alongside the upper-bound readout so readers know bucket resolution.

---

## 3. Fluency & Zero-Frame Windows
- A fluency window with `frame_count == 0` MUST NOT be counted as 0% jank or "smooth".
- Classification rule:
  - `frame_count == 0` → `jank_ratio = null`, `severity = "unknown"`, excluded from valid jank-rate numerator/denominator while preserved in quality diagnostics (`zero_frame_windows`).
- **Algorithm Source Isolation**:
  - Android API 31+ (`android_frame_metrics_deadline`) and Android API 26–30 (`android_frame_metrics_heuristic`) and iOS (`ios_display_link`) are distinct metric sources and MUST NOT be blindly merged into a single cross-algorithm comparison delta.

---

## 4. Platform & Stage Honesty
- **Cold Start Anchors**:
  - Android anchor: `Application.onCreate` → first content frame.
  - iOS anchor: `process_main` / `didFinishLaunching` → first content frame.
  - Cross-platform raw comparisons are disallowed; always compare within the same platform and contract version.
- **Stage Conservation**: When optional sub-stages (`native_init_ms`, `first_frame_ms`) are present, they are accepted only if `native_init_ms >= 0`, `first_frame_ms >= 0`, and `native_init_ms + first_frame_ms <= total_duration_ms`.

---

## 5. Sample Size & Regression Comparison Gates
- **Display Gate**: Point readout requires `sample_size >= 10` (configurable per metric; below gate shows `insufficient_samples`).
- **Comparison Gate**: Release-vs-Release or Period-vs-Period delta requires **both** baseline and candidate to satisfy:
  1. `sample_size >= 100` (default comparison minimum);
  2. Matching `platform`, `contract_version`, and compatible `sampling_policy`;
  3. Non-overlapping or explicitly flagged period windows.
- **Percentage Delta Reporting**: Rate metrics always report both **percentage-point difference (`pp`)** and **relative change (`%`)**; when `baseline == 0`, relative change MUST be `null` (not `Infinity` or `0%`).

---

## 6. Sampling, Lease & Budget Contract (Client Core)
- **Deterministic Installation Sampling**: `fnv1a32(install_seed + "|" + policy_id) % 1_000_000 < rate_ppm`.
- **Lease Expiration**: Remote policies carry an explicit `lease_ttl_ms`; once expired without a fresh server refresh, collectors auto-disable (fail-closed).
- **Sliding 24h Quota**: Each installation enforces a strict maximum event count per rolling 24-hour window (`max_events_24h`), persisted locally off the main thread.
