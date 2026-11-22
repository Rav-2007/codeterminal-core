import * as assert from 'assert';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import * as vscode from 'vscode';

import { DaemonHandle, DaemonSupervisor, SupervisorDeps } from '../../daemonSupervisor';
import { INDEX_STAMP, folderIndex, markAsked, wasAsked } from '../../indexSetup';

function harness(): { sup: DaemonSupervisor; log: string[]; state: { up: boolean } } {
  const log: string[] = [];
  const state = { up: false };
  let n = 0;
  const deps: SupervisorDeps = {
    probe: async () => state.up,
    spawn: (): DaemonHandle => {
      const id = ++n;
      let exited: (code: number | null, signal: string | null) => void = () => undefined;
      log.push(`start ${id}`);
      return {
        onExit: (cb) => { exited = cb; },
        onError: () => undefined,
        kill: () => {
          log.push(`stop ${id}`);
          setImmediate(() => exited(0, null));
        },
      };
    },
    warn: (m) => log.push(`warn ${m}`),
    error: (m) => log.push(`error ${m}`),
    log: () => undefined,
    schedule: (ms, fn) => {
      const t = setTimeout(fn, ms);
      return { cancel: () => clearTimeout(t) };
    },
  };
  return { sup: new DaemonSupervisor(deps), log, state };
}

suite('the index offer', () => {
  test('the service is stopped, the index built, the service started again', async () => {
    const { sup, log } = harness();
    await sup.ensure();
    const r = await sup.whileStopped(async () => { log.push('index'); return 'done'; });
    assert.deepStrictEqual(r, { ran: true, value: 'done' });
    assert.deepStrictEqual(log, ['start 1', 'stop 1', 'index', 'start 2']);
    sup.dispose();
  });

  test('nothing starts a service under the indexer', async () => {
    const { sup, log } = harness();
    await sup.ensure();
    await sup.whileStopped(async () => {
      assert.strictEqual(await sup.ensure(), 'paused');
      assert.strictEqual(await sup.restart(), 'paused');
      log.push('index');
    });
    assert.deepStrictEqual(log, ['start 1', 'stop 1', 'index', 'start 2']);
    sup.dispose();
  });

  test('a failed index still gets its service back', async () => {
    const { sup, log } = harness();
    await sup.ensure();
    await assert.rejects(sup.whileStopped(async () => { throw new Error('failed'); }));
    assert.strictEqual(log[log.length - 1], 'start 2');
    sup.dispose();
  });

  test("another window's service is never indexed under", async () => {
    const { sup, log, state } = harness();
    state.up = true;
    await sup.ensure();
    const r = await sup.whileStopped(async () => { log.push('index'); });
    assert.deepStrictEqual(r, { ran: false });
    assert.deepStrictEqual(log, []);
    sup.dispose();
  });

  test('which folders are offered', () => {
    const home = fs.mkdtempSync(path.join(os.tmpdir(), 'mochiii-home-'));
    const root = path.join(home, 'project');
    fs.mkdirSync(path.join(root, '.mochiii', 'index'), { recursive: true });
    assert.strictEqual(folderIndex('', home), 'notAProject');
    assert.strictEqual(folderIndex(home, home), 'notAProject');
    assert.strictEqual(folderIndex(path.parse(root).root, home), 'notAProject');
    assert.strictEqual(folderIndex(root, home), 'none');
    fs.writeFileSync(path.join(root, '.mochiii', 'index', INDEX_STAMP), '{}');
    assert.strictEqual(folderIndex(root, home), 'indexed');
    fs.writeFileSync(path.join(root, '.mochiii', 'index', 'TOO_LARGE'), '{}');
    assert.strictEqual(folderIndex(root, home), 'tooLarge');
  });

  test("the index stamp is the terminal app's", () => {
    const tui = fs.readFileSync(path.resolve(__dirname, '../../../../tui/firstrun.go'), 'utf8');
    assert.ok(tui.includes(`indexStampFile = "${INDEX_STAMP}"`));
  });

  test('asked once, per folder', async () => {
    let value: unknown;
    const store = {
      keys: () => [], get: () => value, update: async (_: string, next: unknown) => { value = next; },
    } as unknown as vscode.Memento;
    const a = fs.mkdtempSync(path.join(os.tmpdir(), 'mochiii-a-'));
    const b = fs.mkdtempSync(path.join(os.tmpdir(), 'mochiii-b-'));
    assert.ok(!wasAsked(store, a));
    await markAsked(store, a);
    assert.ok(wasAsked(store, a));
    assert.ok(!wasAsked(store, b));
  });
});
