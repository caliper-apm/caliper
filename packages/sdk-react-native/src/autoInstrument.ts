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

export interface ErrorUtilsLike {
  getGlobalHandler?: () => ((error: Error, isFatal?: boolean) => void) | undefined;
  setGlobalHandler?: (handler: (error: Error, isFatal?: boolean) => void) => void;
}

export interface PerformanceObserverEntryListLike {
  getEntries(): Array<{ duration: number }>;
}

export interface PerformanceObserverLike {
  observe(options: { type: string; buffered?: boolean }): void;
  disconnect(): void;
}

export interface PerformanceObserverCtorLike {
  new (callback: (list: PerformanceObserverEntryListLike) => void): PerformanceObserverLike;
}

export interface ReactNativeAutoHooksOptions {
  /** Chain React Native `ErrorUtils.setGlobalHandler` without swallowing host handlers (default: true). */
  captureGlobalErrors?: boolean;
  /** Subscribe to W3C / RN `PerformanceObserver({ type: 'longtask' })` when available (default: true). */
  observeLongTasks?: boolean;
  /** Periodic JS window flush interval in ms (default: 30000; <= 0 disables timer). */
  flushIntervalMs?: number;
  /** Optional `globalThis.fetch` interception config (allowlist + W3C traceparent targets). */
  fetch?: FetchInterceptorOptions;
  /** Optional injectable runtime globals for deterministic unit tests. */
  runtime?: {
    ErrorUtils?: ErrorUtilsLike;
    PerformanceObserver?: PerformanceObserverCtorLike;
    fetch?: (input: string, init?: Record<string, unknown>) => Promise<{ status: number; ok: boolean }>;
  };
}

/**
 * Installs drop-in React Native auto-instrumentation hooks on top of [CaliperReactNativeAdapter]:
 * - Chains `ErrorUtils.setGlobalHandler` (never swallows host error handlers)
 * - Subscribes to Hermes / RN `PerformanceObserver` `longtask` entries when supported
 * - Optionally wraps `fetch` for bounded window denominator & W3C `traceparent` injection
 * - Schedules periodic window flushing (with clean `uninstall()` teardown)
 */
export function installReactNativeAutoHooks(
  adapter: CaliperReactNativeAdapter,
  options: ReactNativeAutoHooksOptions = {},
): { uninstall: () => void } {
  const g = (options.runtime ?? globalThis) as {
    ErrorUtils?: ErrorUtilsLike;
    PerformanceObserver?: PerformanceObserverCtorLike;
    fetch?: (input: string, init?: Record<string, unknown>) => Promise<{ status: number; ok: boolean }>;
  };

  let prevErrorHandler: ((error: Error, isFatal?: boolean) => void) | undefined;
  let errorHookInstalled = false;
  if (options.captureGlobalErrors !== false && g.ErrorUtils?.setGlobalHandler) {
    prevErrorHandler = g.ErrorUtils.getGlobalHandler?.();
    const chainedHandler = (error: Error, isFatal?: boolean): void => {
      try {
        adapter.captureJSError(error instanceof Error ? error : new Error(String(error)), Boolean(isFatal));
        if (isFatal) {
          adapter.flushWindow();
        }
      } catch {
        // Fail-closed: never break host error handling.
      }
      prevErrorHandler?.(error, isFatal);
    };
    g.ErrorUtils.setGlobalHandler(chainedHandler);
    errorHookInstalled = true;
  }

  let longTaskObserver: PerformanceObserverLike | null = null;
  if (options.observeLongTasks !== false && typeof g.PerformanceObserver === 'function') {
    try {
      const obs = new g.PerformanceObserver(list => {
        try {
          const entries = list.getEntries();
          if (entries.length > 0) {
            adapter.recordLongTaskEntries(entries);
          }
        } catch {
          // Fail-closed
        }
      });
      obs.observe({ type: 'longtask', buffered: true });
      longTaskObserver = obs;
    } catch {
      longTaskObserver = null;
    }
  }

  let origFetch: ((input: string, init?: Record<string, unknown>) => Promise<{ status: number; ok: boolean }>) | undefined;
  if (options.fetch && typeof g.fetch === 'function') {
    origFetch = g.fetch;
    g.fetch = wrapFetchForDenominator(origFetch, adapter, options.fetch);
  }

  const intervalMs = options.flushIntervalMs ?? 30_000;
  let timer: ReturnType<typeof setInterval> | null = null;
  if (intervalMs > 0) {
    timer = setInterval(() => {
      try {
        adapter.flushWindow();
      } catch {
        // Fail-closed
      }
    }, intervalMs);
    if (typeof (timer as { unref?: () => void }).unref === 'function') {
      (timer as { unref: () => void }).unref();
    }
  }

  let uninstalled = false;
  return {
    uninstall: () => {
      if (uninstalled) return;
      uninstalled = true;
      if (timer !== null) {
        clearInterval(timer);
        timer = null;
      }
      if (longTaskObserver) {
        try {
          longTaskObserver.disconnect();
        } catch {
          // ignore
        }
        longTaskObserver = null;
      }
      if (origFetch) {
        g.fetch = origFetch;
        origFetch = undefined;
      }
      if (errorHookInstalled && g.ErrorUtils?.setGlobalHandler && prevErrorHandler) {
        g.ErrorUtils.setGlobalHandler(prevErrorHandler);
      }
      try {
        adapter.flushWindow();
      } catch {
        // ignore
      }
    },
  };
}
