import type { CaliperReactNativeAdapter } from './adapter.ts';

export interface FetchInterceptorOptions {
  /** Only URLs matching at least one prefix or regex enter the logical API denominator. */
  allowlist: Array<string | RegExp>;
  /** Optional host/URL patterns permitted to receive W3C `traceparent` headers for end-to-end backend tracing. */
  tracePropagationTargets?: Array<string | RegExp>;
  /** Optional business-error classifier inspecting response status / JSON payload. */
  isBusinessError?: (status: number, url: string) => boolean;
  /** Optional clock provider (defaults to Date.now). */
  nowMs?: () => number;
}

function randomHex(byteLen: number): string {
  const bytes = new Uint8Array(byteLen);
  if (typeof globalThis.crypto?.getRandomValues === 'function') {
    globalThis.crypto.getRandomValues(bytes);
  } else {
    for (let i = 0; i < byteLen; i++) {
      bytes[i] = Math.floor(Math.random() * 256);
    }
  }
  let nonZero = false;
  let hex = '';
  for (let i = 0; i < byteLen; i++) {
    if (bytes[i] !== 0) nonZero = true;
    hex += bytes[i].toString(16).padStart(2, '0');
  }
  return nonZero ? hex : `${'0'.repeat(byteLen * 2 - 1)}1`;
}

export function generateW3CTraceparent(sampled = true): { traceId: string; spanId: string; traceparent: string } {
  const traceId = randomHex(16);
  const spanId = randomHex(8);
  return { traceId, spanId, traceparent: `00-${traceId}-${spanId}-${sampled ? '01' : '00'}` };
}

function shouldPropagateTrace(url: string, targets?: Array<string | RegExp>): boolean {
  if (!targets || targets.length === 0) return false;
  return targets.some(rule => {
    if (rule === '*') return true;
    return typeof rule === 'string' ? url.includes(rule) : rule.test(url);
  });
}

function hasHeaderCaseInsensitive(headers: Record<string, string>, key: string): boolean {
  const target = key.toLowerCase();
  return Object.keys(headers).some(k => k.toLowerCase() === target);
}

/**
 * Wraps a fetch-compatible function to feed every allowlisted logical API call into
 * Caliper's bounded window denominator (550x fewer native bridge calls than per-request events),
 * and optionally injects W3C `traceparent` headers on matching backend origins.
 */
export function wrapFetchForDenominator<TResp extends { status: number; ok: boolean }>(
  baseFetch: (input: string, init?: Record<string, unknown>) => Promise<TResp>,
  adapter: CaliperReactNativeAdapter,
  options: FetchInterceptorOptions,
): (input: string, init?: Record<string, unknown>) => Promise<TResp> {
  const clock = options.nowMs ?? (() => Date.now());
  const matches = (url: string): boolean =>
    options.allowlist.some(rule => (typeof rule === 'string' ? url.includes(rule) : rule.test(url)));

  return async (input: string, init?: Record<string, unknown>): Promise<TResp> => {
    let effectiveInit = init;
    if (shouldPropagateTrace(input, options.tracePropagationTargets)) {
      const existingHeaders = (init?.headers && typeof init.headers === 'object' && !Array.isArray(init.headers))
        ? { ...(init.headers as Record<string, string>) }
        : {};
      if (!hasHeaderCaseInsensitive(existingHeaders, 'traceparent')) {
        existingHeaders.traceparent = generateW3CTraceparent(true).traceparent;
        effectiveInit = { ...(init ?? {}), headers: existingHeaders };
      }
    }

    if (!matches(input)) {
      return baseFetch(input, effectiveInit);
    }
    const started = clock();
    try {
      const resp = await baseFetch(input, effectiveInit);
      const ended = Math.max(started, clock());
      if (!resp.ok || resp.status >= 400) {
        adapter.recordApiCall({ startMs: started, endMs: ended, failure: 'http' });
      } else if (options.isBusinessError?.(resp.status, input)) {
        adapter.recordApiCall({ startMs: started, endMs: ended, failure: 'business' });
      } else {
        adapter.recordApiCall({ startMs: started, endMs: ended, failure: null });
      }
      return resp;
    } catch (err: unknown) {
      const ended = Math.max(started, clock());
      const msg = String(err ?? '');
      const failure = msg.toLowerCase().includes('timeout') || msg.toLowerCase().includes('abort') ? 'timeout' : 'network';
      adapter.recordApiCall({ startMs: started, endMs: ended, failure });
      throw err;
    }
  };
}
