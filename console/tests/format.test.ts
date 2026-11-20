import test from 'node:test';
import assert from 'node:assert/strict';
import { formatDelta, formatQuantile, formatValue } from '../src/format.ts';

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
