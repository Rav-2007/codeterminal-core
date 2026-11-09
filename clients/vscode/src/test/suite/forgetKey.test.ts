import * as assert from 'assert';

import { ChatPagesHost } from '../../chatPages/pagesHost';
import { ConnectResponse } from '../../daemonClient';
import { DECLINED, ForgetDeps, NOTHING_STORED, forgetStoredKey } from '../../forgetKey';

// "FORGET THE STORED KEY" (src/forgetKey.ts, pagesHost.reloadAfter).
//
// FOUND 2026-11-08 (UI report): it acted with no confirmation, did so with
// nothing stored, restarted the daemon anyway, and left the Account page on
// "Not connected" with a raw lockfile error until Refresh.
//
// NEUTER CHECK (2026-11-09): with forgetStoredKey doing what the command did
// on main (no show, no confirm, restart whenever the daemon answered) and
// reloadAfter as on main (load the page once), 5 of these 7 fail: nothing
// stored, declined, VS Code's key only, the daemon had none, and the restart
// recovery. "Both keys go after a yes" and "a daemon that never comes back"
// pass either way, as they should.

const DUMMY_MASKED = 'DUMM...0001';

function shown(masked: string): ConnectResponse {
  return { protocol_version: 1, ok: true, outcome: 'shown', detail: '', masked_key: masked, in_use: true };
}

function removed(had: boolean): ConnectResponse {
  return {
    protocol_version: 1,
    ok: true,
    outcome: 'removed',
    in_use: false,
    detail: had
      ? 'the stored key was removed; this daemon keeps the key it is already running with until it restarts'
      : 'no key was stored, so there was nothing to remove',
  };
}

interface Calls {
  confirm: string[][];
  forget: number;
  restart: number;
  clearOwn: number;
}

function deps(opts: { daemonKey?: string; daemonForgetHad?: boolean; own?: string; yes?: boolean; noDaemon?: boolean }): [ForgetDeps, Calls] {
  const calls: Calls = { confirm: [], forget: 0, restart: 0, clearOwn: 0 };
  const d: ForgetDeps = {
    daemon: opts.noDaemon
      ? undefined
      : {
          show: async () => shown(opts.daemonKey ?? '(none)'),
          forget: async () => {
            calls.forget++;
            return removed(opts.daemonForgetHad ?? opts.daemonKey !== undefined);
          },
          restart: async () => {
            calls.restart++;
          },
        },
    ownKey: async () => opts.own,
    clearOwnKey: async () => {
      calls.clearOwn++;
    },
    confirm: async (what) => {
      calls.confirm.push(what);
      return opts.yes === true;
    },
  };
  return [d, calls];
}

suite('Forget the stored key', () => {
  test('with nothing stored it does nothing: no question, no forget, no restart', async () => {
    const [d, calls] = deps({ yes: true });
    assert.strictEqual(await forgetStoredKey(d), NOTHING_STORED);
    assert.deepStrictEqual(calls, { confirm: [], forget: 0, restart: 0, clearOwn: 0 });

    const [noFolder, calls2] = deps({ yes: true, noDaemon: true });
    assert.strictEqual(await forgetStoredKey(noFolder), NOTHING_STORED);
    assert.deepStrictEqual(calls2, { confirm: [], forget: 0, restart: 0, clearOwn: 0 });
  });

  test('a stored key is forgotten only after a yes', async () => {
    const [d, calls] = deps({ daemonKey: DUMMY_MASKED, own: 'DUMMY-VSCODE-KEY-0002', yes: false });
    assert.strictEqual(await forgetStoredKey(d), DECLINED);
    assert.strictEqual(calls.confirm.length, 1, 'the user was not asked');
    assert.ok(calls.confirm[0].some((w) => w.includes(DUMMY_MASKED)), 'the question does not name the daemon key');
    assert.ok(calls.confirm[0].some((w) => w.includes('...0002')), "the question does not name VS Code's key");
    assert.ok(!calls.confirm[0].join(' ').includes('DUMMY-VSCODE-KEY'), 'the question shows the whole key');
    assert.deepStrictEqual([calls.forget, calls.restart, calls.clearOwn], [0, 0, 0], 'something was done after a no');
  });

  test('after a yes, both keys go and the daemon restarts once', async () => {
    const [d, calls] = deps({ daemonKey: DUMMY_MASKED, own: 'DUMMY-VSCODE-KEY-0002', yes: true });
    const said = await forgetStoredKey(d);
    assert.deepStrictEqual([calls.forget, calls.clearOwn, calls.restart], [1, 1, 1]);
    assert.match(said, /restarted/);
  });

  test("only VS Code's own key: it goes, the daemon is not asked to forget, and restarts to drop it", async () => {
    const [d, calls] = deps({ own: 'DUMMY-VSCODE-KEY-0003', yes: true });
    await forgetStoredKey(d);
    assert.deepStrictEqual([calls.confirm.length, calls.forget, calls.clearOwn, calls.restart], [1, 0, 1, 1]);
  });

  test('when the daemon had nothing after all, it is not restarted', async () => {
    const [d, calls] = deps({ daemonKey: DUMMY_MASKED, daemonForgetHad: false, yes: true });
    await forgetStoredKey(d);
    assert.deepStrictEqual([calls.forget, calls.restart], [1, 0]);
  });
});

suite('Account page after a key change', () => {
  test('waits for a restarting daemon instead of showing the lockfile error', async () => {
    const posts: any[] = [];
    const host = new ChatPagesHost({
      post: (m) => posts.push(m),
      currentTier: () => '',
      currentModel: () => '',
      selectTier: async () => undefined,
      providerNotes: () => [],
    });
    const lockErr = 'daemon not found (expected a lockfile at /run/user/1000/mochiii/daemon-test.lock): ENOENT';
    let asked = 0;
    const h = host as any;
    h.recoverEveryMs = 20;
    h.recoverMs = 5000;
    h.snapshot = async () => {
      asked++;
      return asked <= 3 ? { showError: lockErr, version: '' } : { show: shown('(none)'), version: 'test' };
    };
    try {
      await h.reloadAfter('forgetKey');
      const account = posts.filter((p) => p.type === 'page:account');
      assert.strictEqual(account.length, 1, `the page was posted ${account.length} times`);
      assert.ok(!JSON.stringify(account[0]).includes('lockfile'), 'the page shows the lockfile error: ' + JSON.stringify(account[0]));
      assert.ok(asked >= 4, 'the daemon was not asked again');
    } finally {
      host.dispose();
    }
  });

  test('a daemon that never comes back is still reported', async () => {
    const posts: any[] = [];
    const host = new ChatPagesHost({
      post: (m) => posts.push(m),
      currentTier: () => '',
      currentModel: () => '',
      selectTier: async () => undefined,
      providerNotes: () => [],
    });
    const h = host as any;
    h.recoverEveryMs = 20;
    h.recoverMs = 200;
    h.snapshot = async () => ({ showError: 'daemon not found: DUMMY-GONE', version: '' });
    try {
      await h.reloadAfter('forgetKey');
      const account = posts.filter((p) => p.type === 'page:account');
      assert.strictEqual(account.length, 1);
      assert.ok(JSON.stringify(account[0]).includes('DUMMY-GONE'), 'a daemon that stayed away was hidden');
    } finally {
      host.dispose();
    }
  });
});
