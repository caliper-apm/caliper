import { durationBucketIndex, HISTOGRAM_BUCKET_COUNT } from './contract.ts';
import type { ApiCallSample, FailureDomain, NetworkSummaryPayload } from './bridge.ts';

const MAX_CALLS_PER_WINDOW = 100_000;
const MAX_CALL_DURATION_MS = 600_000;

/**
 * Aggregates allowlisted logical API calls inside a single uninterrupted observation window.
 * Calls that started before observationStartMs are excluded (may span a background pause).
 * Strict conservation: sum(duration_buckets) === call_count.
 */
export function aggregateApiWindow(
  samples: readonly ApiCallSample[],
  bounds: { observationStartMs: number; windowStartMs: number; windowEndMs: number },
): NetworkSummaryPayload | null {
  const { observationStartMs, windowStartMs, windowEndMs } = bounds;
  if (
    !Number.isFinite(observationStartMs) ||
    !Number.isFinite(windowStartMs) ||
    !Number.isFinite(windowEndMs) ||
    windowEndMs <= windowStartMs ||
    windowStartMs < observationStartMs
  ) {
    return null;
  }

  const buckets = new Array<number>(HISTOGRAM_BUCKET_COUNT).fill(0);
  const failures: Record<FailureDomain, number> = {
    http: 0,
    business: 0,
    parse: 0,
    network: 0,
    timeout: 0,
  };
  let calls = 0;
  let sumMs = 0;

  for (const s of samples) {
    if (s.endMs <= windowStartMs || s.endMs > windowEndMs) continue;
    if (!Number.isFinite(s.startMs) || !Number.isFinite(s.endMs) || s.endMs < s.startMs) {
      return null;
    }
    if (s.startMs < observationStartMs) continue;
    const dur = Math.round(s.endMs - s.startMs);
    if (dur > MAX_CALL_DURATION_MS) continue;
    calls += 1;
    if (calls > MAX_CALLS_PER_WINDOW) return null;
    sumMs += dur;
    buckets[durationBucketIndex(dur)] += 1;
    if (s.failure) {
      failures[s.failure] += 1;
    }
  }

  return {
    window_duration_ms: Math.round(windowEndMs - windowStartMs),
    call_count: calls,
    failure_counts: failures,
    duration_buckets: buckets,
    duration_sum_ms: sumMs,
  };
}
