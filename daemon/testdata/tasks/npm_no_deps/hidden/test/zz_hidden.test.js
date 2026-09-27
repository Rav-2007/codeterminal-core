import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseLine } from '../src/csv.js';
import { formatLine } from '../src/format.js';

test('hidden: a quoted field keeps its comma', () => {
  assert.deepEqual(parseLine('a,"b,c",d'), ['a', 'b,c', 'd']);
});

test('hidden: a doubled quote is one quote', () => {
  assert.deepEqual(parseLine('"say ""hi""",x'), ['say "hi"', 'x']);
});

test('hidden: empty and trailing fields', () => {
  assert.deepEqual(parseLine('a,,b'), ['a', '', 'b']);
  assert.deepEqual(parseLine('a,'), ['a', '']);
  assert.deepEqual(parseLine(''), ['']);
  assert.deepEqual(parseLine('""'), ['']);
  assert.deepEqual(parseLine('"",""'), ['', '']);
});

test('hidden: spaces are kept', () => {
  assert.deepEqual(parseLine(' a ,"b"'), [' a ', 'b']);
  assert.deepEqual(parseLine(' x ,y'), [' x ', 'y']);
});

test('hidden: an unterminated quote throws', () => {
  assert.throws(() => parseLine('a,"b'), Error);
  assert.throws(() => parseLine('"abc'), Error);
});

test('hidden: parseLine undoes formatLine', () => {
  const rows = [
    ['plain', 'with,comma', 'with "quotes"', '', ' spaced '],
    ['""', ',', '"'],
    [''],
  ];
  for (const row of rows) {
    assert.deepEqual(parseLine(formatLine(row)), row);
  }
});
