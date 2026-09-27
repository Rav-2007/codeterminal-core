import { test } from 'node:test';
import assert from 'node:assert/strict';
import { formatLine } from '../src/format.js';

test('quotes only what needs quoting', () => {
  assert.equal(formatLine(['a', 'b,c', 'say "hi"']), 'a,"b,c","say ""hi"""');
});
