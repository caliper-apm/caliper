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
  wrapFetchForDenominator,
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

test('wrapFetchForDenominator filters allowlist and classifies http/network/timeout failures', async () => {
  let clock = 50_000;
  const sent: Array<{ kind: string; payload: Record<string, unknown> }> = [];
  const bridge: CaliperNativeBridgeSpec = {
    setScene: () => {},
    addBreadcrumb: () => {},
    notifyFirstFrameReady: () => {},
    enqueueEvent: (kind, _s, _w, payloadJson) => { sent.push({ kind, payload: JSON.parse(payloadJson) }); },
  };
  const adapter = new CaliperReactNativeAdapter(bridge, () => clock);
  const wrapped = wrapFetchForDenominator(
    async (url: string) => {
      clock += 120;
      if (url.includes('502')) return { status: 502, ok: false };
      if (url.includes('timeout')) throw new Error('AbortError: request timeout');
      return { status: 200, ok: true };
    },
    adapter,
    { allowlist: ['/api/v1/'], nowMs: () => clock },
  );

  await wrapped('https://cdn.example.com/static.png'); // ignored (not in allowlist)
  await wrapped('https://api.example.com/api/v1/feed'); // ok
  await wrapped('https://api.example.com/api/v1/502');  // http error
  await assert.rejects(() => wrapped('https://api.example.com/api/v1/timeout'));

  clock += 10_000;
  adapter.flushWindow();
  const netEv = sent.find(e => e.kind === 'network_summary');
  assert.ok(netEv);
  const net = netEv.payload as { call_count: number; failure_counts: { http: number; timeout: number } };
  assert.equal(net.call_count, 3);
  assert.equal(net.failure_counts.http, 1);
  assert.equal(net.failure_counts.timeout, 1);
});

test('wrapFetchForDenominator injects W3C traceparent only on configured targets and preserves existing headers', async () => {
  const seenHeaders: Array<{ url: string; headers?: Record<string, string> }> = [];
  const bridge: CaliperNativeBridgeSpec = {
    setScene: () => {},
    addBreadcrumb: () => {},
    notifyFirstFrameReady: () => {},
    enqueueEvent: () => {},
  };
  const adapter = new CaliperReactNativeAdapter(bridge, () => 100_000);
  const wrapped = wrapFetchForDenominator(
    async (url: string, init?: Record<string, unknown>) => {
      seenHeaders.push({ url, headers: init?.headers as Record<string, string> | undefined });
      return { status: 200, ok: true };
    },
    adapter,
    {
      allowlist: ['/api/v1/'],
      tracePropagationTargets: ['api.mycorp.com'],
      nowMs: () => 100_000,
    },
  );

  await wrapped('https://api.mycorp.com/api/v1/orders');
  await wrapped('https://thirdparty.example.org/ping');
  const preset = '00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01';
  await wrapped('https://api.mycorp.com/api/v1/preset', { headers: { Traceparent: preset } });

  assert.match(seenHeaders[0].headers?.traceparent ?? '', /^00-[a-f0-9]{32}-[a-f0-9]{16}-01$/);
  assert.equal(seenHeaders[1].headers?.traceparent, undefined);
  assert.equal(seenHeaders[2].headers?.Traceparent, preset);
});
