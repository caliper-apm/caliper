/**
 * TurboModule / JSI bridge contract connecting the optional RN adapter to
 * Caliper Native SDK (sdk-android / sdk-ios). All heavy persistence, queuing,
 * compression, and network upload stay strictly inside the Native SDK.
 */
export interface CaliperNativeBridgeSpec {
  setScene(scene: string): void;
  addBreadcrumb(category: string, message: string): void;
  notifyFirstFrameReady(jsEntryToFirstFrameMs: number): void;
  enqueueEvent(kind: string, scene: string, windowId: string | null, payloadJson: string): void;
}

export type FailureDomain = 'http' | 'business' | 'parse' | 'network' | 'timeout';

export interface ApiCallSample {
  startMs: number;
  endMs: number;
  failure: FailureDomain | null;
}

export interface NetworkSummaryPayload {
  window_duration_ms: number;
  call_count: number;
  failure_counts: Record<FailureDomain, number>;
  duration_buckets: number[];
  duration_sum_ms: number;
}

export interface JSLongTaskPayload {
  window_duration_ms: number;
  longtask_count: number;
  total_blocking_ms: number;
  max_task_ms: number;
  dropped_entries: number;
}
