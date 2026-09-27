import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseLine } from '../src/csv.js';

test('splits plain fields', () => {
  assert.deepEqual(parseLine('a,b,c'), ['a', 'b', 'c']);
});

test('keeps empty fields', () => {
  assert.deepEqual(parseLine('a,,c'), ['a', '', 'c']);
});
