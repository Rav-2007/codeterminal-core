import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseLine } from '../src/csv.js';

test('splits plain fields', () => {
  assert.deepEqual(parseLine('a,b,c'), ['a', 'b', 'c']);
});

test('keeps empty fields', () => {
  assert.deepEqual(parseLine('a,,c'), ['a', '', 'c']);
});

test('a quoted field may hold a comma', () => {
  assert.deepEqual(parseLine('a,"b,c",d'), ['a', 'b,c', 'd']);
});

test('two quotes inside a quoted field are one', () => {
  assert.deepEqual(parseLine('"say ""hi"""'), ['say "hi"']);
});

test('an unterminated quote throws', () => {
  assert.throws(() => parseLine('"abc'), Error);
});
