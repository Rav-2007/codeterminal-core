import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';

import {
  CONTINUATION_MARK, LIMIT_STOPS, NextStep, RETRY_WAITS_MS, SEGMENT_MAX_STEPS, SegmentEnd, TASK_MAX_STEPS, TaskRun,
  progressText, stopKind,
} from '../../taskRun';

const ROOT = path.resolve(__dirname, '../../..');
const budget = (detail: string) => ({ reason: 'agent_budget', detail });
const READ_LIMIT = budget('this task read as much as it is allowed to send to the model in one go');

const segment = (over: Partial<SegmentEnd> = {}): SegmentEnd =>
  ({ answer: 'Working on it.', steps: 5, tokens: 1000, saved: [], notSaved: [], ...over });

function continued(next: NextStep): string {
  assert.strictEqual(next.kind, 'continue', JSON.stringify(next));
  return next.kind === 'continue' ? next.prompt : '';
}

suite('long tasks: what continues', () => {
  test("every limit continues, by the daemon's own words", () => {
    for (const detail of [
      'this task reached its limit of 25 steps for the whole turn before finishing',
      'this task reached its limit of 25 steps before finishing',
      'this task reached its token limit for one turn',
      'this task ran out of time before finishing',
      'this task read as much as it is allowed to send to the model in one go — what you see above is everything',
    ]) {
      assert.strictEqual(stopKind(budget(detail)), 'limit', detail);
    }
    assert.strictEqual(stopKind({ reason: 'length' }), 'limit');
  });

  test('the stops that must never continue do not', () => {
    for (const info of [
      budget('this task stopped because it reached the day limit of 15000000 tokens'),
      budget('this task stopped because the model repeated the same step 3 times'),
      budget('this task stopped because its last several steps found nothing new'),
      budget('a sentence this version does not know'),
      { reason: 'agent_budget' },
      { reason: 'user_cancelled' },
      { reason: 'content_filter' },
      { reason: 'budget_exceeded' },
      { reason: 'a_reason_added_later' },
    ]) {
      assert.strictEqual(stopKind(info), 'final', JSON.stringify(info));
      const t = new TaskRun('goal', true);
      assert.strictEqual(t.end(segment({ stop: info })).kind, 'stopped', JSON.stringify(info));
    }
  });

  test("the daemon still says each limit's words", () => {
    const source = fs.readFileSync(path.resolve(ROOT, '../../daemon/agentloop.go'), 'utf8');
    const body = /\nfunc (?:\([^)]*\) )?budgetStop[\s\S]*?\n}/.exec(source)?.[0] ?? '';
    assert.ok(body.length > 500, 'budgetStop was not found in the daemon source');
    for (const words of LIMIT_STOPS) assert.ok(body.includes(words), `the daemon no longer says: ${words}`);
    // And every other stop there starts differently.
    for (const m of body.matchAll(/this task stopped because[^"]*/g)) {
      assert.strictEqual(stopKind(budget(m[0])), 'final', m[0]);
    }
  });

  test('a 400-step task is sixteen segments, then Resume', () => {
    const t = new TaskRun('goal', true);
    let parts = 0;
    let next: NextStep;
    do {
      next = t.end(segment({ steps: SEGMENT_MAX_STEPS, stop: READ_LIMIT }));
      parts++;
    } while (next.kind === 'continue' && parts < 40);
    assert.strictEqual(next.kind, 'paused');
    assert.strictEqual(parts, 16);
    assert.strictEqual(t.steps, TASK_MAX_STEPS);
    assert.ok(t.resume().includes('goal'));
    assert.strictEqual(t.end(segment({ stop: READ_LIMIT })).kind, 'continue', 'Resume gave no new allowance');
  });

  test('no segment starts that could pass the hour', () => {
    let now = 0;
    const t = new TaskRun('goal', true, () => now);
    now = 49 * 60_000;
    assert.strictEqual(t.end(segment({ stop: READ_LIMIT })).kind, 'continue');
    now = 51 * 60_000;
    assert.strictEqual(t.end(segment({ stop: READ_LIMIT })).kind, 'paused');
  });

  test("waiting for the user's answer is not the task's time", () => {
    let now = 0;
    const t = new TaskRun('goal', true, () => now);
    now = 60_000;
    t.waiting(true);
    now = 3 * 3_600_000;
    t.waiting(false);
    assert.strictEqual(t.elapsedMs(), 60_000);
  });

  test('a busy provider is retried twice, and only in a row', () => {
    const busy = { reason: 'provider_error', detail: 'busy' };
    const t = new TaskRun('goal', true);
    for (const wait of RETRY_WAITS_MS) {
      const next = t.end(segment({ stop: busy }));
      assert.ok(next.kind === 'continue' && next.waitMs === wait, JSON.stringify(next));
    }
    assert.strictEqual(t.end(segment({ stop: busy })).kind, 'paused');

    const u = new TaskRun('goal', true);
    u.end(segment({ stop: busy }));
    u.end(segment({ stop: READ_LIMIT }));
    assert.strictEqual(u.end(segment({ stop: busy })).kind, 'continue', 'a good segment did not reset the retries');
  });

  test('Manual pauses with Resume and never continues by itself', () => {
    assert.strictEqual(new TaskRun('goal', false).end(segment({ stop: READ_LIMIT })).kind, 'paused');
    assert.strictEqual(new TaskRun('goal', false).end(segment({ saved: ['a.ts'] })).kind, 'done');
  });

  test('a segment that did nothing is not repeated', () => {
    assert.strictEqual(new TaskRun('goal', true).end(segment({ steps: 0, answer: '', stop: READ_LIMIT })).kind, 'paused');
  });
});

suite('long tasks: the handoff and the finish check', () => {
  test('the handoff carries what history drops', () => {
    const t = new TaskRun('Fix the six bugs', true);
    t.setPlan([{ title: 'config', status: 'done' }, { title: 'cli', status: 'active' }]);
    const prompt = continued(t.end(segment({
      stop: READ_LIMIT, answer: 'config is fixed; cli is next', saved: ['src/config.py'],
      notSaved: [{ file: 'src/cli.py', why: 'search text not found' }],
      check: { checked: 'pytest -q', passed: false },
    })));
    for (const want of [CONTINUATION_MARK, 'Fix the six bugs', '[active] cli', 'src/config.py', 'src/cli.py: search text not found',
      '`pytest -q` failed', 'cli is next', 'relative paths']) {
      assert.ok(prompt.includes(want), `the handoff is missing ${want}`);
    }
  });

  test('files saved earlier stay in later handoffs', () => {
    const t = new TaskRun('goal', true);
    t.end(segment({ stop: READ_LIMIT, saved: ['first.ts'] }));
    assert.ok(continued(t.end(segment({ stop: READ_LIMIT, saved: ['second.ts'] }))).includes('first.ts'));
  });

  test('code changed with no passing check gets one look back, never two', () => {
    const t = new TaskRun('goal', true);
    assert.ok(continued(t.end(segment({ answer: 'All done.', saved: ['a.ts'] }))).includes('no check has passed'));
    assert.strictEqual(t.end(segment({ answer: 'All done.', saved: ['b.ts'] })).kind, 'done');
  });

  test('an open plan item gets the look back', () => {
    const t = new TaskRun('goal', true);
    t.setPlan([{ title: 'write the tests', status: 'pending' }]);
    assert.ok(continued(t.end(segment({ answer: 'All done.' }))).includes('write the tests'));
  });

  test('no look back after a passing check, for documents only, or when the answer asks', () => {
    const rows: Partial<SegmentEnd>[] = [
      { saved: ['a.ts'], check: { checked: 'npm test', passed: true } },
      { saved: ['README.md'] },
      { answer: 'Which database do you use?', saved: ['a.ts'] },
      {},
    ];
    for (const row of rows) {
      assert.strictEqual(new TaskRun('goal', true).end(segment({ answer: 'Done.', ...row })).kind, 'done', JSON.stringify(row));
    }
  });

  test('a stale pass is not a pass', () => {
    const end = segment({ answer: 'Done.', saved: ['a.ts'], check: { checked: 'npm test', passed: true, stale: true } });
    assert.strictEqual(new TaskRun('goal', true).end(end).kind, 'continue');
  });

  test('the line reads as designed', () => {
    assert.strictEqual(progressText({ state: 'working', ms: 252_000, steps: 38, tokens: 412_600 }),
      'Working… 4m 12s · 38 steps · 412.6k tokens');
    assert.strictEqual(progressText({ state: 'done', ms: 5_000, steps: 1, tokens: 0 }), 'Done 5s · 1 step');
  });

  test("the staged budget makes a segment's step limit the ordinary one", () => {
    // eslint-disable-next-line @typescript-eslint/no-require-imports
    const { withSegmentBudget } = require(path.join(ROOT, 'scripts/segmentBudget.js'));
    const root = fs.readFileSync(path.resolve(ROOT, '../../models.json'), 'utf8');
    const staged = JSON.parse(withSegmentBudget(root));
    const b = staged.mcp.budget;
    assert.strictEqual(b.max_iterations, SEGMENT_MAX_STEPS);
    assert.strictEqual(b.turn_timeout_seconds, 600);
    assert.strictEqual(b.spend.session_tokens, b.spend.day_tokens);
    const original = JSON.parse(root);
    original.mcp.budget = staged.mcp.budget;
    assert.deepStrictEqual(staged, original, 'the staged file differs in more than its budgets');
  });
});
