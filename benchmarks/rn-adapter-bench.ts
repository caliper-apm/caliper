import { performance } from 'node:perf_hooks';
import { CaliperReactNativeAdapter } from '../packages/sdk-react-native/src/index.ts';

const ITERATIONS = 100_000;

// Baseline OFF: simulate host work without SDK recording.
const tOffStart = performance.now();
let sinkOff = 0;
for (let i = 0; i < ITERATIONS; i += 1) {
  sinkOff += (i % 7) + 1;
}
const offMs = performance.now() - tOffStart;

// Candidate ON: record 100,000 API calls & LongTask samples in memory window + 100 window flushes.
let clock = 10_000;
let flushedEvents = 0;
const adapter = new CaliperReactNativeAdapter(
  {
    setScene: () => {},
    addBreadcrumb: () => {},
    notifyFirstFrameReady: () => {},
    enqueueEvent: () => {
      flushedEvents += 1;
    },
  },
  () => clock,
);
adapter.setRouteScene('streaming_reader');

const memBefore = process.memoryUsage().heapUsed;
const tOnStart = performance.now();
for (let i = 0; i < ITERATIONS; i += 1) {
  adapter.recordApiCall({ startMs: clock + 1, endMs: clock + 120, failure: i % 50 === 0 ? 'http' : null });
  if (i % 10 === 0) {
    adapter.recordLongTaskEntries([{ duration: 68 }]);
  }
  if ((i + 1) % 1000 === 0) {
    clock += 30_000;
    adapter.flushWindow();
  }
}
const onMs = performance.now() - tOnStart;
const memDeltaKiB = (process.memoryUsage().heapUsed - memBefore) / 1024;

console.log(JSON.stringify({
  benchmark: 'sdk-react-native-window-aggregation',
  iterations: ITERATIONS,
  off_ms: Number(offMs.toFixed(3)),
  on_ms: Number(onMs.toFixed(3)),
  per_call_overhead_us: Number((((onMs - offMs) * 1000) / ITERATIONS).toFixed(3)),
  flushed_bridge_events: flushedEvents,
  heap_delta_kib: Number(memDeltaKiB.toFixed(1)),
  sink_check: sinkOff > 0,
}, null, 2));
