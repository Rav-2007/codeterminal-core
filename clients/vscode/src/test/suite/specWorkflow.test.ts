import * as assert from 'assert';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';

import {
  MODE_BUILD,
  MODE_CHECK,
  MODE_SPEC,
  appliedSpec,
  getActiveSpec,
  listSpecs,
  parseSpecCommand,
  resolveSpecArg,
  setActiveSpec,
  specAction,
  specReportText,
  taskListText,
  useSpecStore,
  workingCopyText,
} from '../../specWorkflow';

// The extension's half of the spec workflow. The TUI's twin is
// clients/tui/spec.go and its tests (clients/tui/spec_test.go); these restate
// the same cases so the two clients cannot drift apart in behaviour.

function workspace(): string {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'mochiii-spec-'));
  for (const rel of ['specs/verbose-flag.md', 'specs/api/paging.md']) {
    fs.mkdirSync(path.dirname(path.join(root, rel)), { recursive: true });
    fs.writeFileSync(path.join(root, rel), '# spec\n');
  }
  return root;
}

class MemoryStore {
  private readonly m = new Map<string, unknown>();
  get<T>(key: string): T | undefined {
    return this.m.get(key) as T | undefined;
  }
  async update(key: string, value: unknown): Promise<void> {
    if (value === undefined) {
      this.m.delete(key);
    } else {
      this.m.set(key, value);
    }
  }
}

suite('spec workflow', () => {
  test('a spec can be named the way a person would', () => {
    const root = workspace();
    for (const arg of ['verbose-flag', 'verbose-flag.md', 'specs/verbose-flag.md']) {
      assert.strictEqual(resolveSpecArg(root, arg).rel, 'specs/verbose-flag.md', arg);
    }
    for (const bad of ['', 'missing', '../etc/passwd', 'specs/../README']) {
      assert.ok(resolveSpecArg(root, bad).error, `accepted ${bad}`);
    }
    assert.deepStrictEqual(listSpecs(root), ['specs/api/paging.md', 'specs/verbose-flag.md']);
  });

  test('the active spec is remembered, and forgotten when its file goes', async () => {
    const root = workspace();
    useSpecStore(new MemoryStore());
    await setActiveSpec('specs/verbose-flag.md');
    assert.strictEqual(getActiveSpec(root), 'specs/verbose-flag.md');
    fs.rmSync(path.join(root, 'specs', 'verbose-flag.md'));
    assert.strictEqual(getActiveSpec(root), '', 'a deleted spec is still active');
    await setActiveSpec('');
    assert.strictEqual(getActiveSpec(root), '');
  });

  test('/spec subcommands ask for the right thing', () => {
    const root = workspace();
    assert.deepStrictEqual(parseSpecCommand('/spec'), { ok: true, args: '' });
    assert.strictEqual(parseSpecCommand('/special').ok, false);

    assert.strictEqual(specAction(root, '', 'use verbose-flag').activate, 'specs/verbose-flag.md');
    assert.strictEqual(specAction(root, 'specs/verbose-flag.md', 'off').activate, '');
    assert.strictEqual(specAction(root, '', 'check').turn, undefined, 'check ran with no spec');
    assert.strictEqual(specAction(root, '', 'build').turn, undefined, 'build ran with no spec');
    assert.strictEqual(specAction(root, 'specs/v.md', 'check').turn?.mode, MODE_CHECK);
    assert.strictEqual(specAction(root, 'specs/v.md', 'build').turn?.mode, MODE_BUILD);
    const goal = specAction(root, '', 'add a --verbose flag').turn;
    assert.strictEqual(goal?.mode, MODE_SPEC);
    assert.strictEqual(goal?.prompt, 'add a --verbose flag');
    assert.ok(specAction(root, 'specs/verbose-flag.md', 'show').reply?.includes('active spec: specs/verbose-flag.md'));
  });

  test('an accepted spec becomes active only after a /spec turn', () => {
    assert.strictEqual(appliedSpec(MODE_SPEC, ['specs/new.md']), 'specs/new.md');
    assert.strictEqual(appliedSpec('', ['specs/new.md']), '', 'an ordinary edit to a spec switched specs');
    assert.strictEqual(appliedSpec(MODE_SPEC, ['src/a.go']), '');
  });

  test('the reports read at a glance', () => {
    assert.strictEqual(workingCopyText({ checked: 'go test ./...', passed: true }), '✓ checked: go test ./... passed');
    assert.ok(workingCopyText({}).startsWith('not checked'));
    assert.ok(
      workingCopyText({ checked: 'go test ./...', output: '--- FAIL: TestX' }).includes('  --- FAIL: TestX'),
    );
    const rep = specReportText({
      spec: 'specs/v.md',
      criteria: [
        { id: 'C1', text: '-v prints files', status: 'met', evidence: 'go test ./cmd passed' },
        { id: 'C2', text: 'quiet by default', status: 'unmet' },
        { id: 'C3', text: 'documented', status: 'unknown', note: 'not checked' },
      ],
    });
    for (const want of ['1 of 3 criteria met', '✓ C1: -v prints files', '✗ C2', '? C3: documented', '    not checked']) {
      assert.ok(rep.includes(want), `report lacks ${want}:\n${rep}`);
    }
    assert.strictEqual(
      taskListText([
        { id: '1', title: 'write the test', status: 'done' },
        { id: '2', title: 'add the flag', status: 'active' },
      ]),
      'tasks: 1 of 2 done\n☑ write the test\n▸ add the flag',
    );
  });
});
