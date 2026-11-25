import test from 'node:test';
import assert from 'node:assert/strict';
import { DICT, detectInitialLang, SUPPORTED_LANGS, t, type DictKey } from '../src/i18n.ts';

test('All 6 Console locales implement 100% of dictionary keys with non-empty strings', () => {
  const baseKeys = Object.keys(DICT.en) as DictKey[];
  assert.ok(baseKeys.length >= 60, 'expected full APM dictionary');
  assert.equal(SUPPORTED_LANGS.length, 6);

  for (const { code } of SUPPORTED_LANGS) {
    const locale = DICT[code];
    assert.ok(locale, `missing locale ${code}`);
    assert.equal(Object.keys(locale).length, baseKeys.length, `key count mismatch for ${code}`);
    for (const k of baseKeys) {
      const val = t(code, k);
      assert.equal(typeof val, 'string');
      assert.ok(val.trim().length > 0, `empty translation for ${code}.${k}`);
    }
  }
});

test('detectInitialLang resolves valid query overrides and falls back safely to English', () => {
  assert.equal(detectInitialLang('ja'), 'ja');
  assert.equal(detectInitialLang('ko'), 'ko');
  assert.equal(detectInitialLang('es'), 'es');
  assert.equal(detectInitialLang('de'), 'de');
  assert.equal(detectInitialLang('zh'), 'zh');
  assert.equal(detectInitialLang('unknown'), 'en');
  assert.equal(detectInitialLang(null), 'en');
});
