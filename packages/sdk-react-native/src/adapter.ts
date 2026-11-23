import type { CaliperNativeBridgeSpec, JSLongTaskPayload, ApiCallSample } from './bridge.ts';
import { aggregateApiWindow } from './apiWindow.ts';

export interface LongTaskEntry {
  duration: number;
}

/**
 * First-class React Native adapter attaching JS LongTask windows, logical API denominator windows,
 * route scene boundaries, and uncaught JS errors onto the Native SDK timeline.
 * Zero per-call native bridge chatter: aggregates in JS memory and flushes once per window.
 */
export class CaliperReactNativeAdapter {
  private readonly bridge: CaliperNativeBridgeSpec;
  private readonly now: () => number;
  private scene = 'unknown';
  private observationStartMs: number;
  private windowStartMs: number;
  private longTaskCount = 0;
  private totalBlockingMs = 0;
  private maxTaskMs = 0;
  private droppedEntries = 0;
  private apiSamples: ApiCallSample[] = [];
  private windowSeq = 0;

  constructor(bridge: CaliperNativeBridgeSpec, now: () => number = () => Date.now()) {
    this.bridge = bridge;
    this.now = now;
    const t = this.now();
    this.observationStartMs = t;
    this.windowStartMs = t;
  }

  setRouteScene(scene: string): void {
    const clean = scene.trim() || 'unknown';
    if (clean === this.scene) return;
    // Discard in-progress JS window synchronously before switching scene boundary.
    this.resetObservation();
    this.scene = clean;
    this.bridge.setScene(clean);
  }

  recordLongTaskEntries(entries: readonly LongTaskEntry[], dropped = 0): void {
    this.droppedEntries += Math.max(0, dropped);
    for (const e of entries) {
      if (!Number.isFinite(e.duration) || e.duration < 0) continue;
      this.longTaskCount += 1;
      this.totalBlockingMs += e.duration;
      if (e.duration > this.maxTaskMs) this.maxTaskMs = e.duration;
    }
  }

  recordApiCall(sample: ApiCallSample): void {
    this.apiSamples.push(sample);
  }

  /** Emits both js_longtask_window and network_summary with a shared window_id for correlation. */
  flushWindow(): { windowId: string; longTask: JSLongTaskPayload; network: ReturnType<typeof aggregateApiWindow> } | null {
    const endMs = this.now();
    if (endMs <= this.windowStartMs) return null;

    this.windowSeq += 1;
    const windowId = `rn-win-${this.windowSeq}`;
    const longTask: JSLongTaskPayload = {
      window_duration_ms: Math.round(endMs - this.windowStartMs),
      longtask_count: this.longTaskCount,
      total_blocking_ms: Math.round(this.totalBlockingMs),
      max_task_ms: Math.round(this.maxTaskMs),
      dropped_entries: this.droppedEntries,
    };

    const network = aggregateApiWindow(this.apiSamples, {
      observationStartMs: this.observationStartMs,
      windowStartMs: this.windowStartMs,
      windowEndMs: endMs,
    });

    this.bridge.enqueueEvent('js_longtask_window', this.scene, windowId, JSON.stringify(longTask));
    if (network && network.call_count > 0) {
      this.bridge.enqueueEvent('network_summary', this.scene, windowId, JSON.stringify(network));
    }

    this.windowStartMs = endMs;
    this.longTaskCount = 0;
    this.totalBlockingMs = 0;
    this.maxTaskMs = 0;
    this.droppedEntries = 0;
    this.apiSamples = [];
    return { windowId, longTask, network };
  }

  captureJSError(error: Error, fatal: boolean): void {
    const frames = (error.stack ?? '')
      .split('\n')
      .slice(1, 33)
      .map(line => line.trim())
      .filter(Boolean)
      .map(line => ({ symbol: line, in_app: true }));

    const fingerprint = `js-${error.name}-${(error.message || 'error').slice(0, 48).replace(/[^a-zA-Z0-9_-]/g, '_')}`;
    const payload = {
      fingerprint,
      title: `${error.name}: ${error.message}`.slice(0, 256),
      fatal,
      exception_type: error.name,
      thread_name: 'js',
      frames: frames.length > 0 ? frames : [{ symbol: error.name, in_app: true }],
    };
    this.bridge.enqueueEvent('crash', this.scene, null, JSON.stringify(payload));
  }

  /** Records a custom multi-stage business journey trace via the Native bridge. */
  recordCustomJourney(params: {
    journeyName: string;
    status?: 'success' | 'failure' | 'cancelled' | 'timeout';
    durationMs?: number;
    stages?: Array<{ name: string; duration_ms: number }>;
    errorCode?: string;
  }): void {
    const payload: Record<string, unknown> = {
      journey_name: params.journeyName,
      status: params.status ?? 'success',
    };
    if (typeof params.durationMs === 'number' && Number.isFinite(params.durationMs) && params.durationMs >= 0) {
      payload.duration_ms = params.durationMs;
    }
    if (params.errorCode) {
      payload.error_code = params.errorCode;
    }
    if (params.stages && params.stages.length > 0) {
      payload.stages = params.stages.slice(0, 16);
    }
    this.bridge.enqueueEvent('custom_scene', this.scene, null, JSON.stringify(payload));
  }

  private resetObservation(): void {
    const t = this.now();
    this.observationStartMs = t;
    this.windowStartMs = t;
    this.longTaskCount = 0;
    this.totalBlockingMs = 0;
    this.maxTaskMs = 0;
    this.droppedEntries = 0;
    this.apiSamples = [];
  }
}
