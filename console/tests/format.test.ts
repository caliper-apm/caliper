import test from 'node:test';
import assert from 'node:assert/strict';
import { formatDelta, formatPValue, formatQuantile, formatValue, statTestLabel } from '../src/format.ts';

test('Zero-Lie formatting never turns null or overflow into zero', () => {
  assert.equal(formatValue(null, '%', 'no_observations'), '— (No observations)');
  assert.equal(formatValue(undefined, 'ms'), '—');
  assert.equal(formatValue(0, '%'), '0.00%');

  const overflow = formatQuantile({ value: null, sample_size: 100, missing_reason: 'histogram_overflow' }, 'ms');
  assert.equal(overflow.main, '—');
  assert.match(overflow.sub, /Overflow/);

  const valid = formatQuantile({ value: 500, interval: [250, 500], sample_size: 240 }, 'ms');
  assert.equal(valid.main, '500 ms');
  assert.equal(valid.sub, '(250–500 ms] · n=240');
});

test('Percentage delta omits relative % when baseline is zero', () => {
  assert.equal(formatDelta(2.5, '%', null), '+2.50 pp');
  assert.equal(formatDelta(-120, 'ms', -24.0), '-120 ms (-24.0%)');
});

test('Statistical p-value and test label formatting', () => {
  assert.equal(formatPValue(0.00001), 'p < 0.0001');
  assert.equal(formatPValue(0.0342), 'p = 0.0342');
  assert.equal(formatPValue(undefined), '—');
  assert.equal(statTestLabel('two_proportion_z'), '2-Prop Z-test');
  assert.equal(statTestLabel('histogram_ks'), 'Histogram KS');
});
