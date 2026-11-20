import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  DURATION_BOUNDS_MS,
  fnv1a32,
  samplingBucketPPM,
  isAdmitted,
  classifyFluency,
  SlidingQuota,
  CaliperReactNativeAdapter,
} from '../src/index.ts';
import type { CaliperNativeBridgeSpec } from '../src/index.ts';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const fixturePath = path.resolve(__dirname, '../../sdk-core/fixtures/contract-vectors.json');
const fixture = JSON.parse(fs.readFileSync(fixturePath, 'utf8'));

test('golden contract parity (TypeScript vs contract-vectors.json)', () => {
  assert.deepEqual([...DURATION_BOUNDS_MS], fixture.duration_histogram_bounds_ms);

  for (const tc of fixture.deterministic_sampling_fnv1a32) {
    assert.equal(fnv1a32(`${tc.install_seed}|${tc.policy_id}`), tc.expected_hash_u32);
    assert.equal(samplingBucketPPM(tc.install_seed, tc.policy_id), tc.expected_bucket_ppm);
    assert.equal(isAdmitted(tc.install_seed, tc.policy_id, tc.expected_bucket_ppm), false);
    assert.equal(isAdmitted(tc.install_seed, tc.policy_id, tc.expected_bucket_ppm + 1), true);
  }

  for (const tc of fixture.fluency_window_cases) {
    const got = classifyFluency(tc.frame_count, tc.jank_frame_count);
    assert.equal(got.severity, tc.expected_severity);
    assert.equal(got.jankRatio, tc.expected_jank_ratio);
  }

  for (const tc of fixture.sliding_quota_24h_cases) {
    const quota = new SlidingQuota(tc.window_ms, tc.cap);
    tc.timestamps_ms.forEach((ts: number, i: number) => {
      assert.equal(quota.tryConsume(ts), tc.expected_admitted[i]);
    });
  }
});

test('RN adapter correlates LongTask + API denominator windows with strict bucket conservation', () => {
  let clock = 10_000;
  const sent: Array<{ kind: string; scene: string; windowId: string | null; payload: Record<string, unknown> }> = [];
  const scenes: string[] = [];

  const bridge: CaliperNativeBridgeSpec = {
    setScene: s => scenes.push(s),
    addBreadcrumb: () => {},
    notifyFirstFrameReady: () => {},
    enqueueEvent: (kind, scene, windowId, payloadJson) => {
      sent.push({ kind, scene, windowId, payload: JSON.parse(payloadJson) });
    },
  };

  const adapter = new CaliperReactNativeAdapter(bridge, () => clock);
  adapter.setRouteScene('streaming_reader');
  assert.deepEqual(scenes, ['streaming_reader']);

  // Record 2 long tasks and 3 API calls (1 started before observation -> must be excluded!)
  adapter.recordLongTaskEntries([{ duration: 82 }, { duration: 145 }], 1);
  adapter.recordApiCall({ startMs: 9_000, endMs: 10_500, failure: null }); // pre-observation -> excluded
  adapter.recordApiCall({ startMs: 10_100, endMs: 10_280, failure: null });
  adapter.recordApiCall({ startMs: 10_300, endMs: 11_150, failure: 'timeout' });

  clock = 40_000;
  const flushed = adapter.flushWindow();
  assert.ok(flushed);
  assert.equal(sent.length, 2);
  assert.equal(sent[0].kind, 'js_longtask_window');
  assert.equal(sent[1].kind, 'network_summary');
  assert.equal(sent[0].windowId, sent[1].windowId);

  const net = sent[1].payload as { call_count: number; duration_buckets: number[]; failure_counts: { timeout: number } };
  assert.equal(net.call_count, 2);
  assert.equal(net.failure_counts.timeout, 1);
  assert.equal(net.duration_buckets.reduce((a, b) => a + b, 0), net.call_count);

  // Capture JS error
  adapter.captureJSError(new TypeError('Cannot read properties of undefined'), true);
  assert.equal(sent.length, 3);
  assert.equal(sent[2].kind, 'crash');
});
