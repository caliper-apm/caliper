import type { CaliperReactNativeAdapter } from '../../../packages/sdk-react-native/src/index.ts';

export interface StreamingReaderProps {
  adapter: CaliperReactNativeAdapter;
  apiBaseUrl: string;
}

/**
 * Embedded React Native feature screen controller inside Atlas Reader Hybrid Demo App.
 * Hooks into CaliperReactNativeAdapter to correlate JS LongTasks, API windows, and JS errors
 * onto the exact same Native SDK timeline.
 */
export class StreamingReaderController {
  private readonly adapter: CaliperReactNativeAdapter;
  private readonly apiBaseUrl: string;
  private readonly retainedCallbacks: Array<() => void> = [];

  constructor(props: StreamingReaderProps) {
    this.adapter = props.adapter;
    this.apiBaseUrl = props.apiBaseUrl.replace(/\/$/, '');
  }

  onMount(): void {
    this.adapter.setRouteScene('streaming_reader');
  }

  /** Chaos Lab #3: Simulates synchronous JS LongTask storm during token streaming. */
  injectJSLongTaskStorm(iterations = 4): void {
    const entries = Array.from({ length: iterations }, (_, i) => ({
      duration: 95 + i * 35,
    }));
    this.adapter.recordLongTaskEntries(entries, 0);
  }

  /** Chaos Lab #4 (RN side): Retains screen closure callback across unmounts. */
  injectCallbackRetentionLeak(payloadSizeKB = 512): void {
    const heavyBuffer = 'x'.repeat(payloadSizeKB * 1024);
    this.retainedCallbacks.push(() => {
      void heavyBuffer.length;
    });
  }

  /** Chaos Lab #5 (RN side): Emits minified bundle JS error resolvable via Hermes SourceMap v3. */
  injectMinifiedJSError(): void {
    const err = new TypeError('Cannot read properties of undefined (reading "tokens")');
    err.stack = [
      'TypeError: Cannot read properties of undefined (reading "tokens")',
      '    at r (index.android.bundle:1:0)',
      '    at p (index.android.bundle:1:24)',
    ].join('\n');
    this.adapter.captureJSError(err, true);
  }

  async fetchFeedWithObservation(): Promise<{ ok: boolean; status: number; durationMs: number }> {
    const startMs = Date.now();
    try {
      const resp = await fetch(`${this.apiBaseUrl}/api/v1/feed`);
      const endMs = Date.now();
      this.adapter.recordApiCall({
        startMs,
        endMs,
        failure: resp.ok ? null : 'http',
      });
      return { ok: resp.ok, status: resp.status, durationMs: endMs - startMs };
    } catch {
      const endMs = Date.now();
      this.adapter.recordApiCall({ startMs, endMs, failure: 'network' });
      return { ok: false, status: 0, durationMs: endMs - startMs };
    }
  }
}
