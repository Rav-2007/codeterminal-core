import * as assert from 'assert';

import { freshDegradations } from '../../degradedOnce';

// "zero-data-retention routing is not available with Groq" arrived on EVERY
// turn of the first Stage 7 walkthrough. A provider-level fact is said once
// per panel; a turn-level one is said every time it happens.
suite('provider notices are said once', () => {
  const routing = { component: 'provider_routing', detail: 'zero-data-retention routing is not available with Groq' };
  const effort = { component: 'reasoning_effort', detail: 'the provider refused it' };

  test('a routing note is shown the first time and not again', () => {
    const seen = new Set<string>();
    assert.deepStrictEqual(freshDegradations([routing], seen), [routing]);
    assert.deepStrictEqual(freshDegradations([routing], seen), []);
  });

  test('a different routing note (another provider) is new', () => {
    const seen = new Set<string>();
    freshDegradations([routing], seen);
    const other = { ...routing, detail: 'zero-data-retention routing is not available with NVIDIA' };
    assert.deepStrictEqual(freshDegradations([other], seen), [other]);
  });

  test('a turn-level item is shown every turn, or the effort pill would un-grey', () => {
    const seen = new Set<string>();
    assert.deepStrictEqual(freshDegradations([routing, effort], seen), [routing, effort]);
    assert.deepStrictEqual(freshDegradations([routing, effort], seen), [effort]);
  });
});
