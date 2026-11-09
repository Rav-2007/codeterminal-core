import * as assert from 'assert';

import { ChatPagesHost } from '../../chatPages/pagesHost';
import { ConnectResponse, sendConnect } from '../../daemonClient';
import { DECLINED, ForgetDeps, NOTHING_STORED, forgetDidNothing, forgetStoredKey } from '../../forgetKey';
import { StubDaemon, pointAtEmptyRuntimeDir, writeLine } from '../stubDaemon';

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

suite('Forget: what counts as nothing done', () => {
  test('nothing stored and a No are nothing; anything else is not', () => {
    assert.strictEqual(forgetDidNothing(NOTHING_STORED), true);
    assert.strictEqual(forgetDidNothing(DECLINED), true);
    assert.strictEqual(forgetDidNothing('The stored key was removed.\nThe daemon was restarted.'), false);
    assert.strictEqual(forgetDidNothing(''), false);
    assert.strictEqual(forgetDidNothing(undefined), false, 'an answer that is not one is not "nothing was done"');
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
      // One "Waiting for the daemon…" while it was away, then the real page.
      assert.strictEqual(account.length, 2, `the page was posted ${account.length} times`);
      assert.strictEqual(account[0].view.waiting, true, 'the first post was not the waiting state');
      assert.notStrictEqual(account[1].view.waiting, true);
      assert.strictEqual(account[1].view.reachable, true);
      assert.ok(!JSON.stringify(account).includes('lockfile'), 'the page shows the lockfile error: ' + JSON.stringify(account));
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
      assert.strictEqual(account.length, 2, 'the waiting state, then the result');
      assert.strictEqual(account[0].view.waiting, true);
      assert.ok(JSON.stringify(account[1]).includes('DUMMY-GONE'), 'a daemon that stayed away was hidden');
    } finally {
      host.dispose();
    }
  });
});


// THE ACCOUNT PAGE WHEN THE DAEMON IS DOWN (2026-11-09, against a real stopped
// and restarted daemon: the stub below is a socket and a lockfile, and
// "stopped" is no lockfile at all, which is what the page's raw error says).
//
//   a  the 20 s wait after Forget or connect says it is waiting
//   b  when it runs out the page says so and offers Retry, keeping the raw error
//   c  a page opened while the daemon is down finds it again by itself, and
//      stops looking when the page closes
//   d  Cancel on the Forget question starts no wait
suite('Account page when the daemon is down', () => {
  const answer: ConnectResponse = {
    protocol_version: 1,
    ok: true,
    outcome: 'shown',
    detail: '',
    in_use: true,
    active_api_base: 'http://192.0.2.1:9/v1',
    key_source: 'stored',
    active_masked_key: 'DUMM...0001',
    model_slug: 'dummy/model',
  };

  interface Rig {
    posts: any[];
    host: ChatPagesHost;
    h: any;
    stub: StubDaemon;
    /** Start the daemon: a socket and its lockfile appear. */
    start(): Promise<void>;
    /** The Account views posted so far. */
    views(): any[];
    /**
     * How many times THIS host asked the daemon. Not the stub's handshakes: in
     * the full suite the rest of the activated extension polls whatever daemon
     * the environment names, and those connections are not this host's.
     */
    asked(): number;
  }

  async function rig(fn: (r: Rig) => Promise<void>): Promise<void> {
    const prev = process.env.XDG_RUNTIME_DIR;
    const restoreEmpty = pointAtEmptyRuntimeDir(); // the daemon is stopped: no lockfile
    const stub = new StubDaemon();
    let started = false;
    const posts: any[] = [];
    const host = new ChatPagesHost({
      post: (m) => posts.push(m),
      currentTier: () => '',
      currentModel: () => '',
      selectTier: async () => undefined,
      providerNotes: () => [],
    });
    const h = host as any;
    h.recoverEveryMs = 15;
    h.watchEveryMs = 30;
    let asked = 0;
    const snapshot = h.snapshot.bind(h);
    h.snapshot = async () => {
      asked++;
      return snapshot();
    };
    try {
      await fn({
        asked: () => asked,
        posts,
        host,
        h,
        stub,
        start: async () => {
          await stub.start((_req, socket) => {
            writeLine(socket, answer);
            socket.end();
          });
          started = true;
        },
        views: () => posts.filter((p) => p.type === 'page:account').map((p) => p.view),
      });
    } finally {
      host.dispose();
      if (started) {
        await stub.stop();
      }
      restoreEmpty();
      if (prev === undefined) {
        delete process.env.XDG_RUNTIME_DIR;
      } else {
        process.env.XDG_RUNTIME_DIR = prev;
      }
    }
  }

  async function until(what: string, cond: () => boolean, ms = 4000): Promise<void> {
    const end = Date.now() + ms;
    while (!cond()) {
      if (Date.now() > end) {
        assert.fail('timed out waiting for ' + what);
      }
      await new Promise((r) => setTimeout(r, 5));
    }
  }
  const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));

  // ---- a ---------------------------------------------------------------------

  test('a: during the wait the page says it is waiting, without the raw error, and the daemon coming back ends it', async () => {
    await rig(async (r) => {
      r.h.recoverMs = 10000;
      const done = r.h.reloadAfter('forgetKey');
      await until('the waiting state', () => r.views().length >= 1);
      const waiting = r.views()[0];
      assert.strictEqual(waiting.waiting, true);
      assert.strictEqual(waiting.provider, 'Waiting for the daemon…');
      assert.ok(!JSON.stringify(waiting).includes('lockfile'), 'the waiting state shows the raw error');
      assert.ok(!waiting.note, 'a wait that is still going has not given up');
      await sleep(120); // still waiting: it does not repeat itself or settle
      assert.strictEqual(r.views().length, 1, 'the waiting state was posted again');
      await r.start();
      await done;
      const views = r.views();
      assert.strictEqual(views.length, 2, JSON.stringify(views.map((v) => v.provider)));
      assert.strictEqual(views[1].reachable, true);
      assert.strictEqual(views[1].provider, '192.0.2.1');
      assert.ok(!views[1].waiting && !views[1].note);
    });
  });

  test('a: a daemon that is back at once never shows the waiting state', async () => {
    await rig(async (r) => {
      await r.start();
      await r.h.reloadAfter('forgetKey');
      const views = r.views();
      assert.strictEqual(views.length, 1);
      assert.strictEqual(views[0].reachable, true);
    });
  });

  // ---- b ---------------------------------------------------------------------

  test('b: when the wait runs out the page keeps the raw error, says so, and Retry finds a daemon that came back', async () => {
    await rig(async (r) => {
      // What the raw error is, asked of the real client: the page must show it unchanged.
      const raw = await sendConnect('vscode', { show: true }).then(
        () => assert.fail('a daemon answered'),
        (e: Error) => e.message,
      );
      r.h.recoverMs = 150;
      await r.h.reloadAfter('forgetKey');
      const views = r.views();
      assert.strictEqual(views.length, 2, 'the waiting state, then the result');
      assert.strictEqual(views[0].waiting, true);
      const last = views[1];
      assert.strictEqual(last.reachable, false);
      assert.strictEqual(last.base, raw, 'the raw error was reworded');
      assert.match(last.note, /did not answer within \d+ seconds/);
      assert.match(last.note, /Retry/);

      // Retry is the page asking again (page:load). Still down: the note stays.
      r.host.handle({ type: 'page:load', page: 'account' });
      await until('the answer to Retry', () => r.views().length >= 3);
      assert.strictEqual(r.views()[2].reachable, false);
      assert.ok(r.views()[2].note, 'pressing Retry against a daemon that is still down dropped the note');
      assert.strictEqual(r.views()[2].base, raw);

      // The daemon returns, and Retry finds it; the note goes with the failure.
      await r.start();
      r.host.handle({ type: 'page:load', page: 'account' });
      await until('the page after Retry', () => r.views().some((v) => v.reachable));
      const ok = r.views().find((v) => v.reachable);
      assert.ok(!ok.note && !ok.waiting);
    });
  });

  // ---- c ---------------------------------------------------------------------

  test('c: a page opened while the daemon is down refreshes itself when the daemon returns', async () => {
    await rig(async (r) => {
      r.host.handle({ type: 'page:load', page: 'account' });
      await until('the first view', () => r.views().length === 1);
      assert.strictEqual(r.views()[0].reachable, false);
      assert.ok(!r.views()[0].waiting && !r.views()[0].note, 'a plain open has not waited for anything');
      await sleep(200); // several checks, all finding no daemon: nothing new is said
      assert.strictEqual(r.views().length, 1, 'the page was re-posted with nothing new in it');
      await r.start();
      await until('the page to refresh itself', () => r.views().length >= 2);
      assert.strictEqual(r.views()[1].reachable, true);
      assert.strictEqual(r.views()[1].provider, '192.0.2.1');
    });
  });

  test('c: once the daemon answers the checking stops', async () => {
    await rig(async (r) => {
      await r.start();
      r.host.handle({ type: 'page:load', page: 'account' });
      await until('the view', () => r.views().length === 1);
      assert.strictEqual(r.views()[0].reachable, true);
      const asked = r.asked();
      await sleep(200);
      assert.strictEqual(r.asked(), asked, 'the daemon was asked again although it was up');
    });
  });

  test('c: closing the page stops the checking, and nothing is posted for it afterwards', async () => {
    await rig(async (r) => {
      r.host.handle({ type: 'page:load', page: 'account' });
      await until('the first view', () => r.views().length === 1);
      assert.strictEqual(r.host.handle({ type: 'page:hidden', page: 'account' }), true);
      const atClose = r.asked();
      await r.start();
      await sleep(250);
      assert.strictEqual(r.views().length, 1, 'a closed page was refreshed');
      assert.strictEqual(r.asked(), atClose, 'the daemon was still being asked after the page closed');
    });
  });

  test('c: closing another page does not stop the Account page checking', async () => {
    await rig(async (r) => {
      r.host.handle({ type: 'page:load', page: 'account' });
      await until('the first view', () => r.views().length === 1);
      r.host.handle({ type: 'page:hidden', page: 'settings' });
      await r.start();
      await until('the page to refresh itself', () => r.views().length >= 2);
    });
  });

  test('c: reopening the page after closing it checks again', async () => {
    await rig(async (r) => {
      r.host.handle({ type: 'page:load', page: 'account' });
      await until('the first view', () => r.views().length === 1);
      r.host.handle({ type: 'page:hidden', page: 'account' });
      r.host.handle({ type: 'page:load', page: 'account' });
      await until('the second view', () => r.views().length === 2);
      await r.start();
      await until('the page to refresh itself', () => r.views().length >= 3);
      assert.strictEqual(r.views()[2].reachable, true);
    });
  });

  test('c: the page being closed during the 20 s wait ends the wait', async () => {
    await rig(async (r) => {
      r.h.recoverMs = 60000;
      const t0 = Date.now();
      const done = r.h.reloadAfter('forgetKey');
      await until('the waiting state', () => r.views().length === 1);
      r.host.handle({ type: 'page:hidden', page: 'account' });
      await done;
      assert.ok(Date.now() - t0 < 5000, 'the wait went on after the page was closed');
      assert.strictEqual(r.views().length, 1, 'a result was posted to a closed page');
    });
  });

  test('c: a wait that ends with the page not open starts no watch', async () => {
    await rig(async (r) => {
      // The connect popup hides the Account page, then the wait runs with nothing open.
      r.h.recoverMs = 100;
      await r.h.reloadAfter('connect');
      const before = r.views().length;
      const asked = r.asked();
      await r.start();
      await sleep(250);
      assert.strictEqual(r.views().length, before, 'a page that is not open was refreshed');
      assert.strictEqual(r.asked(), asked, 'the daemon was being asked for a page that is not open');
    });
  });

  test('a: a page opened again in the middle of the wait shows the wait, not the failure', async () => {
    await rig(async (r) => {
      r.h.recoverMs = 10000;
      const done = r.h.reloadAfter('connect');
      await until('the waiting state', () => r.views().length === 1);
      r.host.handle({ type: 'page:load', page: 'account' }); // the user opens Account again
      await until('the page to be posted again', () => r.views().length === 2);
      assert.strictEqual(r.views()[1].waiting, true, 'a reopened page showed the failed state: ' + JSON.stringify(r.views()[1]));
      assert.ok(!JSON.stringify(r.views()[1]).includes('lockfile'));
      await r.start();
      await done;
      assert.strictEqual(r.views()[r.views().length - 1].reachable, true);
    });
  });

  test('c: the watch leaves a running wait alone, and takes over after it', async () => {
    await rig(async (r) => {
      r.h.recoverMs = 200;
      r.host.handle({ type: 'page:load', page: 'account' }); // arms the watch
      await until('the first view', () => r.views().length === 1);
      const done = r.h.reloadAfter('forgetKey'); // the wait takes over
      await done;
      assert.deepStrictEqual(
        r.views().map((v) => (v.waiting ? 'waiting' : v.note ? 'gave up' : 'down')),
        ['down', 'waiting', 'gave up'],
      );
      await r.start(); // after the wait gave up, the watch still finds the daemon
      await until('the page to refresh itself', () => r.views().length >= 4);
      assert.strictEqual(r.views()[3].reachable, true);
      assert.ok(!r.views()[3].note);
    });
  });

  // ---- d ---------------------------------------------------------------------

  test('d: Cancel on the Forget question, or nothing stored, starts no wait', async () => {
    await rig(async (r) => {
      r.h.recoverMs = 60000;
      let asked = 0;
      const snapshot = r.h.snapshot.bind(r.h);
      r.h.snapshot = async () => {
        asked++;
        return snapshot();
      };
      await r.h.commandDone('forgetKey', DECLINED);
      await r.h.commandDone('forgetKey', NOTHING_STORED);
      await sleep(100);
      assert.strictEqual(asked, 0, 'the daemon was asked after a Cancel');
      assert.strictEqual(r.posts.length, 0, 'the page was changed by a Cancel');
    });
  });

  test('d: an answer that did change something still reloads the page and waits', async () => {
    await rig(async (r) => {
      r.h.recoverMs = 100;
      await r.h.commandDone('forgetKey', 'The stored key was removed.\nThe daemon was restarted, so it is no longer using a removed key.');
      const views = r.views();
      assert.strictEqual(views[0].waiting, true);
      assert.ok(views[views.length - 1].note, 'a daemon that did not come back was not reported');
    });
  });

  test('d: Cancel does not touch other commands', async () => {
    await rig(async (r) => {
      r.h.recoverMs = 100;
      await r.h.commandDone('connect', DECLINED); // a connect that returned the same words is still a connect
      assert.ok(r.views().length >= 1, 'a command other than Forget was skipped');
    });
  });
});
