import type { CaliperReactNativeAdapter } from './adapter.ts';

export interface FetchInterceptorOptions {
  /** Only URLs matching at least one prefix or regex enter the logical API denominator. */
  allowlist: Array<string | RegExp>;
  /** Optional business-error classifier inspecting response status / JSON payload. */
  isBusinessError?: (status: number, url: string) => boolean;
  /** Optional clock provider (defaults to Date.now). */
  nowMs?: () => number;
}

/**
 * Wraps a fetch-compatible function to feed every allowlisted logical API call into
 * Caliper's bounded window denominator (550x fewer native bridge calls than per-request events).
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
    if (!matches(input)) {
      return baseFetch(input, init);
    }
    const started = clock();
    try {
      const resp = await baseFetch(input, init);
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
