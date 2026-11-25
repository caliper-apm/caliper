export const NATIVE_MODULE_NAME = 'CaliperReactNativeBridge';

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
  flush?(): void;
}

/**
 * Resolves the registered Caliper NativeModule / TurboModule from React Native without
 * introducing a hard runtime dependency on `react-native` in pure-TS/Node test environments.
 */
export function resolveNativeBridge(reactNativeRuntime?: {
  TurboModuleRegistry?: { get?: (name: string) => unknown };
  NativeModules?: Record<string, unknown>;
}): CaliperNativeBridgeSpec | null {
  const fromTurbo = reactNativeRuntime?.TurboModuleRegistry?.get?.(NATIVE_MODULE_NAME);
  const candidate = (fromTurbo ?? reactNativeRuntime?.NativeModules?.[NATIVE_MODULE_NAME]) as
    | Partial<CaliperNativeBridgeSpec>
    | undefined;
  if (
    candidate &&
    typeof candidate.setScene === 'function' &&
    typeof candidate.addBreadcrumb === 'function' &&
    typeof candidate.notifyFirstFrameReady === 'function' &&
    typeof candidate.enqueueEvent === 'function'
  ) {
    return candidate as CaliperNativeBridgeSpec;
  }
  return null;
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
