/**
 * Zero-Lie cross-platform contract primitives (byte-for-byte parity with contract-vectors.json).
 */
export const MAX_SAMPLING_RATE_PPM = 1_000_000;
export const HISTOGRAM_BUCKET_COUNT = 19;

export const DURATION_BOUNDS_MS: readonly number[] = [
  16, 33, 50, 100, 250, 500, 750, 1000, 1500, 2000,
  3000, 5000, 8000, 10000, 15000, 30000, 60000, 120000,
];

export function fnv1a32(input: string): number {
  let hash = 2166136261 >>> 0;
  for (let i = 0; i < input.length; i += 1) {
    hash ^= input.charCodeAt(i) & 0xff;
    hash = Math.imul(hash, 16777619) >>> 0;
  }
  return hash >>> 0;
}

export function samplingBucketPPM(installSeed: string, policyId: string): number {
  return fnv1a32(`${installSeed}|${policyId}`) % MAX_SAMPLING_RATE_PPM;
}

export function isAdmitted(installSeed: string, policyId: string, ratePPM: number): boolean {
  if (ratePPM <= 0) return false;
  if (ratePPM >= MAX_SAMPLING_RATE_PPM) return true;
  return samplingBucketPPM(installSeed, policyId) < ratePPM;
}

export function durationBucketIndex(ms: number): number {
  if (!Number.isFinite(ms) || ms < 0) return DURATION_BOUNDS_MS.length;
  for (let i = 0; i < DURATION_BOUNDS_MS.length; i += 1) {
    if (ms <= DURATION_BOUNDS_MS[i]) return i;
  }
  return DURATION_BOUNDS_MS.length;
}

export interface FluencyAssessment {
  jankRatio: number | null;
  severity: 'unknown' | 'healthy' | 'degraded' | 'critical';
}

export function classifyFluency(frameCount: number, jankFrameCount: number): FluencyAssessment {
  if (frameCount <= 0 || jankFrameCount < 0 || jankFrameCount > frameCount) {
    return { jankRatio: null, severity: 'unknown' };
  }
  const ratio = jankFrameCount / frameCount;
  const severity = ratio >= 0.10 ? 'critical' : ratio >= 0.03 ? 'degraded' : 'healthy';
  return { jankRatio: ratio, severity };
}

export class SlidingQuota {
  private readonly windowMs: number;
  private readonly cap: number;
  private timestamps: number[] = [];

  constructor(windowMs: number, cap: number) {
    this.windowMs = windowMs;
    this.cap = cap;
  }

  tryConsume(timestampMs: number): boolean {
    if (this.cap <= 0 || this.windowMs <= 0) return false;
    const cutoff = timestampMs - this.windowMs;
    this.timestamps = this.timestamps.filter(ts => ts > cutoff);
    if (this.timestamps.length >= this.cap) return false;
    this.timestamps.push(timestampMs);
    return true;
  }
}
