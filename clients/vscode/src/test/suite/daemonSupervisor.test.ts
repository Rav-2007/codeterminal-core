import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';

import {
  DaemonHandle,
  DaemonSupervisor,
  EXIT_ALREADY_RUNNING,
  MAX_RESTARTS,
  RESTART_BASE_MS,
  SupervisorDeps,
  Timer,
} from '../../daemonSupervisor';

// THE SUPERVISION POLICY, TESTED AS POLICY.
//
// This file replaces four source-scraping checks in daemonRestart.test.ts.
// Those asserted that certain TEXT appeared in extension.ts, which is the
// weakest form of evidence available -- it cannot tell a working bound from a
// constant that is declared and never consulted. They existed because the logic
// was four closures inside activate(), reachable only by spawning a real
// subprocess inside the Extension Development Host and waiting on a wall clock.
//
// Moving the policy into DaemonSupervisor with injected probe/spawn/schedule
// made it directly drivable, and the assertions that matter are all about
// things that must NOT happen:
//
//   - no spawn at all when a daemon is already serving this workspace
//   - no restart budget spent on an exit that means "someone else got here first"
//   - no unbounded retry
//   - no killing a daemon this window did not start
//
// Every one of those is invisible to a source check.

/** setImmediate, awaited: lets one round of promise callbacks run. */
const tick = (): Promise<void> => new Promise((r) => setImmediate(r));

class FakeHandle implements DaemonHandle {
  private exitCb?: (code: number | null, signal: string | null) => void;
  private errorCb?: (err: Error) => void;
  killed = false;

  onExit(cb: (code: number | null, signal: string | null) => void): void {
    this.exitCb = cb;
  }
  onError(cb: (err: Error) => void): void {
    this.errorCb = cb;
  }
  kill(): void {
    this.killed = true;
  }

  /** Drive an exit the way the real child process would. */
  exit(code: number | null, signal: string | null = null): void {
    this.exitCb?.(code, signal);
  }
  fail(message: string): void {
    this.errorCb?.(new Error(message));
  }
}

interface FakeTimer {
  ms: number;
  fn: () => void;
  cancelled: boolean;
}

class Harness {
  /** What probe() answers. The single fact the supervisor branches on. */
  daemonUp = false;
  spawns: FakeHandle[] = [];
  warns: string[] = [];
  errors: string[] = [];
  logs: string[] = [];
  timers: FakeTimer[] = [];
  probes = 0;
  readonly deps: SupervisorDeps;

  constructor() {
    this.deps = {
      probe: async () => {
        this.probes++;
        return this.daemonUp;
      },
      spawn: (): DaemonHandle => {
        const h = new FakeHandle();
        this.spawns.push(h);
        return h;
      },
      warn: (m) => this.warns.push(m),
      error: (m) => this.errors.push(m),
      log: (m) => this.logs.push(m),
      schedule: (ms: number, fn: () => void): Timer => {
        const t: FakeTimer = { ms, fn, cancelled: false };
        this.timers.push(t);
        return {
          cancel: () => {
            t.cancelled = true;
          },
        };
      },
    };
  }

  get last(): FakeHandle {
    assert.ok(this.spawns.length > 0, 'nothing has been spawned');
    return this.spawns[this.spawns.length - 1];
  }

  /** The clock, advanced deliberately: fire the one pending retry. */
  async fire(): Promise<void> {
    const pending = this.timers.filter((t) => !t.cancelled);
    assert.strictEqual(pending.length, 1, `expected exactly 1 pending retry, got ${pending.length}`);
    const t = pending[0];
    t.cancelled = true;
    t.fn();
    await tick();
    await tick();
  }

  /** Toasts a user would actually see. Adoption must produce none. */
  get userVisible(): string[] {
    return [...this.warns, ...this.errors];
  }
}

suite('daemon supervisor', () => {
  test('adopts a running daemon instead of starting a second one', async () => {
    const h = new Harness();
    h.daemonUp = true;
    const sup = new DaemonSupervisor(h.deps);

    const outcome = await sup.ensure();

    assert.strictEqual(outcome, 'adopted');
    assert.strictEqual(
      h.spawns.length,
      0,
      'a second VS Code window on the same repository must not start a daemon that can only ' +
        'lose the bind -- every one of those orphaned an 81 MB embedder helper'
    );
    assert.deepStrictEqual(
      h.userVisible,
      [],
      'adoption is the ORDINARY case; a user who opened a second window did nothing wrong ' +
        'and has nothing to act on'
    );
  });

  test('starts one when nothing is serving the workspace', async () => {
    const h = new Harness();
    h.daemonUp = false;
    const sup = new DaemonSupervisor(h.deps);

    assert.strictEqual(await sup.ensure(), 'spawned');
    assert.strictEqual(h.spawns.length, 1);
  });

  // The exit-code contract with daemon/exitcodes.go, and the reason it exists.
  test('losing the startup race adopts silently and costs no restart budget', async () => {
    const h = new Harness();
    const sup = new DaemonSupervisor(h.deps);

    await sup.ensure(); // spawn #1
    h.daemonUp = true; // the winner is up by the time we look
    h.last.exit(EXIT_ALREADY_RUNNING);
    await tick();
    await tick();

    assert.deepStrictEqual(
      h.userVisible,
      [],
      'exit 3 means another window already serves this repo -- a normal outcome, not a failure'
    );
    assert.strictEqual(h.timers.length, 0, 'nothing should be scheduled; there is nothing to retry');

    // THE BUDGET IS PROVABLY UNTOUCHED: force a genuine failure now and read the
    // counter off the message. "1/5" means the exit above was not charged.
    h.daemonUp = false;
    await sup.ensure(); // spawn #2
    h.last.exit(1);
    await tick();

    assert.match(
      h.warns[h.warns.length - 1],
      new RegExp(`\\(1/${MAX_RESTARTS}\\)`),
      'a benign exit 3 must not consume an attempt from the budget reserved for real failures'
    );
  });

  // The bound on the bound: exit 3 is only free if somebody really is there.
  test('losing to a winner that then vanishes does spend budget', async () => {
    const h = new Harness();
    const sup = new DaemonSupervisor(h.deps);

    await sup.ensure();
    h.daemonUp = false; // it claimed to lose, and nobody holds the address
    h.last.exit(EXIT_ALREADY_RUNNING);
    await tick();
    await tick();

    assert.match(
      h.warns[h.warns.length - 1],
      new RegExp(`\\(1/${MAX_RESTARTS}\\)`),
      'two daemons dying alternately would hand the race back and forth forever if this ' +
        'path were free; it must be charged like any other failure'
    );
  });

  test('a daemon that keeps failing is retried a bounded number of times, then reported', async () => {
    const h = new Harness();
    const sup = new DaemonSupervisor(h.deps);

    await sup.ensure();
    for (let i = 0; i < MAX_RESTARTS; i++) {
      h.last.exit(1);
      await tick();
      assert.match(h.warns[i], new RegExp(`\\(${i + 1}/${MAX_RESTARTS}\\)`));
      await h.fire();
    }

    // The attempt that exhausts the budget.
    h.last.exit(1);
    await tick();

    assert.strictEqual(h.warns.length, MAX_RESTARTS, 'exactly the budget, no more');
    assert.strictEqual(h.errors.length, 1, 'exhausting the budget must be reported once');
    assert.match(h.errors[0], /will not be restarted again/);
    assert.match(h.errors[0], /Restart Daemon/, 'the message must name the way out');
    assert.strictEqual(
      h.timers.filter((t) => !t.cancelled).length,
      0,
      'nothing may remain scheduled after the budget is spent -- that is the infinite loop'
    );
    assert.strictEqual(h.spawns.length, MAX_RESTARTS + 1);
  });

  test('the retry delay backs off instead of hammering at a fixed rate', async () => {
    const h = new Harness();
    const sup = new DaemonSupervisor(h.deps);

    await sup.ensure();
    const delays: number[] = [];
    for (let i = 0; i < 3; i++) {
      h.last.exit(1);
      await tick();
      delays.push(h.timers[h.timers.length - 1].ms);
      await h.fire();
    }

    assert.deepStrictEqual(delays, [RESTART_BASE_MS, RESTART_BASE_MS * 2, RESTART_BASE_MS * 4]);
  });

  test('a retry adopts if another window won in the meantime', async () => {
    const h = new Harness();
    const sup = new DaemonSupervisor(h.deps);

    await sup.ensure(); // spawn #1
    h.last.exit(1);
    await tick();

    h.daemonUp = true; // another window started one while we waited
    await h.fire();

    assert.strictEqual(
      h.spawns.length,
      1,
      'the retry path probes too; a retry that races another window must adopt rather than ' +
        'spawn a third process to lose a second race'
    );
  });

  test('a clean stop is not a crash', async () => {
    const h = new Harness();
    const sup = new DaemonSupervisor(h.deps);

    await sup.ensure();
    h.last.exit(0);
    await tick();
    assert.deepStrictEqual(h.userVisible, []);
    assert.strictEqual(h.timers.length, 0);

    await sup.ensure(); // a second one, stopped by signal
    h.last.exit(null, 'SIGTERM');
    await tick();
    assert.deepStrictEqual(h.userVisible, [], 'a daemon we asked to stop is not a failure');
    assert.strictEqual(h.timers.length, 0);
  });

  test('dispose kills a daemon this window started', async () => {
    const h = new Harness();
    const sup = new DaemonSupervisor(h.deps);

    await sup.ensure();
    const own = h.last;
    sup.dispose();

    assert.strictEqual(own.killed, true);
  });

  // The half that adoption makes newly possible to get wrong.
  test('dispose does NOT kill a daemon this window adopted', async () => {
    const h = new Harness();
    h.daemonUp = true;
    const sup = new DaemonSupervisor(h.deps);

    await sup.ensure();
    sup.dispose();

    assert.strictEqual(
      h.spawns.length,
      0,
      'nothing was spawned, so there is nothing this window owns -- the daemon belongs to ' +
        'another window that is still using it'
    );
  });

  test('dispose stops a pending retry', async () => {
    const h = new Harness();
    const sup = new DaemonSupervisor(h.deps);

    await sup.ensure();
    h.last.exit(1);
    await tick();
    assert.strictEqual(h.timers.filter((t) => !t.cancelled).length, 1);

    sup.dispose();
    assert.strictEqual(
      h.timers.filter((t) => !t.cancelled).length,
      0,
      'a window that is closing must not start a daemon two seconds later'
    );
  });

  // dispose() can land while the probe is still in flight, and the check after
  // the await is what catches it. Without it, closing a window during startup
  // spawns a daemon nothing will ever stop -- the exact leak this class exists
  // to prevent, reintroduced through the back door.
  test('disposing during an in-flight probe does not spawn', async () => {
    const h = new Harness();
    let release: (v: boolean) => void = () => undefined;
    h.deps.probe = () =>
      new Promise<boolean>((r) => {
        release = r;
      });
    const sup = new DaemonSupervisor(h.deps);

    const pending = sup.ensure();
    sup.dispose();
    release(false); // the probe finally answers "nothing there"

    assert.strictEqual(await pending, 'disposed');
    assert.strictEqual(h.spawns.length, 0);
  });

  test('a user-requested restart gets a fresh budget', async () => {
    const h = new Harness();
    const sup = new DaemonSupervisor(h.deps);

    await sup.ensure();
    for (let i = 0; i < MAX_RESTARTS; i++) {
      h.last.exit(1);
      await tick();
      await h.fire();
    }
    h.last.exit(1);
    await tick();
    assert.strictEqual(h.errors.length, 1, 'budget spent');

    await sup.restart();
    h.last.exit(1);
    await tick();

    assert.match(
      h.warns[h.warns.length - 1],
      new RegExp(`\\(1/${MAX_RESTARTS}\\)`),
      'the Restart Daemon command must clear the budget, or it silently does nothing once the ' +
        'budget is spent -- which is precisely when a user reaches for it'
    );
  });

  test('restarting a daemon owned by another window says so rather than appearing to work', async () => {
    const h = new Harness();
    h.daemonUp = true;
    const sup = new DaemonSupervisor(h.deps);

    await sup.ensure(); // adopted; we own nothing
    await sup.restart();

    assert.strictEqual(h.spawns.length, 0, 'we cannot restart what we did not start');
    assert.strictEqual(h.warns.length, 1);
    assert.match(h.warns[0], /another VS Code window/);
  });

  // The one fact that still belongs to extension.ts rather than to the policy:
  // ORDER. The probe reads a lockfile whose name is derived from the workspace,
  // so a supervisor that runs before setWorkspaceRoot probes the wrong path,
  // always answers "no daemon", and puts the unconditional spawn straight back.
  test('extension.ts records the workspace before the supervisor probes', () => {
    const source = fs.readFileSync(
      path.join(__dirname, '..', '..', '..', 'src', 'extension.ts'),
      'utf8'
    );
    const setIdx = source.indexOf('setWorkspaceRoot(workspacePath)');
    const ensureIdx = source.indexOf('supervisor.ensure()');
    assert.ok(setIdx > 0, 'activate() must call setWorkspaceRoot');
    assert.ok(ensureIdx > 0, 'activate() must ensure a daemon');
    assert.ok(
      setIdx < ensureIdx,
      'setWorkspaceRoot must run first, or the probe looks for a lockfile under the old ' +
        'per-user name, never finds one, and the adopt path is dead code'
    );
  });
});
