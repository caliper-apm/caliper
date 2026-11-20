import type { QuantileResult } from './types.ts';

const MISSING_LABELS: Record<string, string> = {
  no_observations: 'No observations',
  insufficient_samples: 'Insufficient samples',
  missing_denominator: 'Missing denominator',
  histogram_overflow: 'Overflow (>120s)',
  incompatible_policy: 'Incompatible policy',
  incompatible_algorithm: 'Mixed algorithms',
  baseline_no_observations: 'Baseline missing',
  candidate_no_observations: 'Candidate missing',
  baseline_insufficient_samples: 'Baseline n < gate',
  candidate_insufficient_samples: 'Candidate n < gate',
  baseline_missing_denominator: 'Baseline missing denom',
  candidate_missing_denominator: 'Candidate missing denom',
  baseline_incompatible_algorithm: 'Baseline mixed algo',
  candidate_incompatible_algorithm: 'Candidate mixed algo',
  statistically_insignificant: 'Not statistically significant (p ≥ 0.01)',
};

const METRIC_LABELS: Record<string, string> = {
  startup_cold_p50_ms: 'Cold Start P50',
  startup_cold_p90_ms: 'Cold Start P90',
  fluency_jank_rate_pct: 'Fluency Jank Rate',
  scene_fluency_jank_rate_pct: 'Scene Jank Rate',
  resource_cpu_avg_pct: 'Avg Process CPU',
  resource_memory_peak_p90_mib: 'Peak Memory P90',
  scene_memory_peak_p90_mib: 'Scene Peak Memory P90',
  network_api_success_rate_pct: 'API Success Rate',
  network_api_p90_ms: 'API Duration P90',
};

export function missingLabel(reason?: string): string {
  if (!reason) return 'Unavailable';
  return MISSING_LABELS[reason] ?? reason.replace(/_/g, ' ');
}

export function metricLabel(metricId: string): string {
  return METRIC_LABELS[metricId] ?? metricId;
}

/** Zero-Lie formatting: null MUST never render as "0" or "0.0%". */
export function formatValue(value: number | null | undefined, unit: string, missingReason?: string): string {
  if (value === null || value === undefined || !Number.isFinite(value)) {
    return missingReason ? `— (${missingLabel(missingReason)})` : '—';
  }
  if (unit === '%') return `${value.toFixed(2)}%`;
  if (unit === 'ms') return `${Math.round(value).toLocaleString('en-US')} ms`;
  if (unit === 'MiB') return `${Math.round(value).toLocaleString('en-US')} MiB`;
  return `${value.toFixed(2)} ${unit}`.trim();
}

export function formatQuantile(q: QuantileResult | undefined, unit: string): { main: string; sub: string } {
  if (!q) return { main: '—', sub: 'No data' };
  if (q.value === null || q.missing_reason) {
    return {
      main: '—',
      sub: `${missingLabel(q.missing_reason)} · n=${q.sample_size.toLocaleString('en-US')}`,
    };
  }
  const interval = q.interval ? `(${q.interval[0]}–${q.interval[1]} ${unit}] · ` : '';
  return {
    main: formatValue(q.value, unit),
    sub: `${interval}n=${q.sample_size.toLocaleString('en-US')}`,
  };
}

export function formatDelta(delta: number | null, unit: string, relPct: number | null): string {
  if (delta === null || !Number.isFinite(delta)) return '—';
  const sign = delta > 0 ? '+' : '';
  const absPart = unit === '%' ? `${sign}${delta.toFixed(2)} pp` : `${sign}${Math.round(delta)} ${unit}`;
  if (relPct === null || !Number.isFinite(relPct)) {
    return absPart;
  }
  const relSign = relPct > 0 ? '+' : '';
  return `${absPart} (${relSign}${relPct.toFixed(1)}%)`;
}

export function formatTime(ms?: number): string {
  if (!ms) return '—';
  return new Date(ms).toISOString().replace('T', ' ').slice(0, 19) + ' UTC';
}

export function formatPValue(p?: number): string {
  if (p === undefined || p === null || !Number.isFinite(p)) return '—';
  if (p < 0.0001) return 'p < 0.0001';
  return `p = ${p.toFixed(4)}`;
}

export function statTestLabel(test?: string): string {
  if (test === 'two_proportion_z') return '2-Prop Z-test';
  if (test === 'histogram_ks') return 'Histogram KS';
  if (test === 'threshold_only') return 'Threshold gate';
  return '—';
}
