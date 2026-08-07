import * as assert from 'assert';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';

import { resolvedWorkspaceRoot, setWorkspaceRoot } from '../../daemonClient';

import {
  ADOPT_PROBE_ATTEMPTS,
  DaemonHandle,
  DaemonSupervisor,
  EXIT_ALREADY_RUNNING,
  MAX_RESTARTS,
  RESTART_BASE_MS,
  RESTART_EXIT_GRACE_MS,
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

  // THE COLD-START WINDOW, and the reason a single probe was not enough.
  //
  // The winner does not publish its lockfile until it has finished starting, and
  // starting includes loading the embedding model -- seconds. So the loser's
  // probe fires at the worst possible moment: the winner provably exists (we
  // were refused the bind) and is provably unreachable.
  //
  // Asking once turned an ordinary second window into an error: miss, charge
  // budget, retry, spawn, lose again, repeat until the budget was spent and the
  // user was told the daemon "will not be restarted again" -- about a daemon
  // that was starting perfectly well the whole time.
  test('losing the race waits out the winner cold start before charging anything', async () => {
    const h = new Harness();
    const sup = new DaemonSupervisor(h.deps);

    await sup.ensure();
    h.daemonUp = false; // the winner is up but still loading its model
    h.last.exit(EXIT_ALREADY_RUNNING);
    await tick();
    await tick();

    // Three misses in a row: still nothing said, still nothing charged.
    for (let i = 0; i < 3; i++) {
      assert.deepStrictEqual(h.userVisible, [], `attempt ${i + 1} must stay silent`);
      assert.strictEqual(h.spawns.length, 1, 'and must not spawn another daemon to lose again');
      await h.fire();
    }

    // The winner finishes starting and publishes its lockfile.
    h.daemonUp = true;
    await h.fire();

    assert.deepStrictEqual(h.userVisible, [], 'adopting a slow starter is still adoption');
    assert.strictEqual(h.spawns.length, 1);
    assert.ok(
      h.logs.some((l) => /adopted/.test(l)),
      'the adoption should be recorded where someone debugging can see it'
    );
  });

  // The bound on the wait: it cannot be indefinite, or a winner that really did
  // die leaves the window with no daemon and no message, forever.
  test('a winner that never appears eventually spends budget', async () => {
    const h = new Harness();
    const sup = new DaemonSupervisor(h.deps);

    await sup.ensure();
    h.daemonUp = false;
    h.last.exit(EXIT_ALREADY_RUNNING);
    await tick();
    await tick();

    // Exhaust every adoption attempt.
    for (let i = 1; i < ADOPT_PROBE_ATTEMPTS; i++) {
      assert.deepStrictEqual(h.userVisible, [], `attempt ${i} must stay silent`);
      await h.fire();
    }

    assert.match(
      h.warns[h.warns.length - 1],
      new RegExp(`\\(1/${MAX_RESTARTS}\\)`),
      'two daemons dying alternately would hand the race back and forth forever if this ' +
        'path were free; once the wait is exhausted it must be charged like any other failure'
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

  // THE ORPHAN THIS CLASS EXISTS TO PREVENT, COMING BACK THROUGH THE BACK DOOR.
  //
  // kill() DELIVERS a signal; it does not wait for the process to die. The exit
  // event therefore arrives some time later -- by which point restart() has
  // already spawned the replacement. Both callbacks cleared this.own
  // unconditionally, so the dead daemon's exit un-tracked the LIVE one, and
  // dispose() then had nothing to kill.
  //
  // That is a daemon nobody owns, holding the socket, with its 81 MB embedder
  // helper, for as long as the machine is up.
  //
  // Neuter check: drop the `this.own !== owned` guard from onExit and this fails.
  test('a killed daemon\'s late exit does not un-track the one that replaced it', async () => {
    const h = new Harness();
    const sup = new DaemonSupervisor(h.deps);

    await sup.ensure();
    const dead = h.last;

    // Wedged badly enough to outlast the grace period -- which is exactly the
    // daemon a user reaches for Restart Daemon about. The replacement therefore
    // starts while the old process is still, formally, alive.
    const restarting = sup.restart();
    await tick();
    const grace = h.timers.filter((t) => !t.cancelled);
    assert.strictEqual(grace.length, 1);
    grace[0].cancelled = true;
    grace[0].fn();
    await restarting;

    const live = h.last;
    assert.notStrictEqual(live, dead, 'restart must have spawned a replacement');

    // And NOW the old one finally goes, long after its replacement is up.
    dead.exit(null, 'SIGTERM');
    await tick();

    sup.dispose();
    assert.strictEqual(
      live.killed,
      true,
      'the replacement was left untracked by the dead one\'s exit, so dispose() killed ' +
        'nothing -- a daemon nobody owns, holding the socket and an 81 MB helper, until reboot'
    );
  });

  // ...and the same clobber via onError, which has the identical shape.
  test('a killed daemon\'s late error does not un-track the one that replaced it', async () => {
    const h = new Harness();
    const sup = new DaemonSupervisor(h.deps);

    await sup.ensure();
    const dead = h.last;

    const restarting = sup.restart();
    dead.exit(null, 'SIGTERM');
    await restarting;
    const live = h.last;

    dead.fail('ECONNRESET on a process that is already gone');
    await tick();

    sup.dispose();
    assert.strictEqual(live.killed, true, 'a dead handle must not speak for a live one');
    assert.deepStrictEqual(
      h.errors,
      [],
      'and it must not report a failure about a daemon that was replaced on purpose'
    );
  });

  // RESTART MUST NOT ADOPT THE CORPSE IT JUST MADE.
  //
  // restart() killed its own daemon and went straight to ensure(), which probes
  // first. A daemon that has been signalled is still answering handshakes while
  // it drains, so the probe said "yes" -- and restart returned 'adopted', owning
  // nothing, about a process that was in the middle of exiting. Seconds later
  // the window had no daemon at all and no supervisor state saying so.
  //
  // The other half is just as bad: if the probe misses but the socket has not
  // been released yet, the replacement loses the bind, exits 3, and waits out a
  // 15s adoption window for a winner that is never coming.
  //
  // Neuter check: drop the await on the exit and this fails as 'adopted'.
  test('restart waits for its own daemon to go before deciding anything', async () => {
    const h = new Harness();
    const sup = new DaemonSupervisor(h.deps);

    await sup.ensure();
    const dying = h.last;

    // Still answering handshakes while it drains -- which is what a daemon with
    // a shutdown grace period does.
    h.daemonUp = true;

    const restarting = sup.restart();
    await tick();
    assert.strictEqual(
      h.spawns.length,
      1,
      'restart must not have decided anything yet: the daemon it killed has not exited'
    );

    dying.exit(null, 'SIGTERM');
    h.daemonUp = false; // the socket is released as it goes
    const outcome = await restarting;

    assert.strictEqual(
      outcome,
      'spawned',
      'restart adopted the corpse of the daemon it had just killed; the window ends up with ' +
        'no daemon and a supervisor that believes one is running'
    );
    assert.strictEqual(h.spawns.length, 2);
  });

  // A daemon wedged badly enough to ignore SIGTERM must not wedge the command
  // the user reached for BECAUSE it is wedged.
  test('restart does not wait forever for a daemon that will not die', async () => {
    const h = new Harness();
    const sup = new DaemonSupervisor(h.deps);

    await sup.ensure();

    const restarting = sup.restart();
    await tick();

    const waiting = h.timers.filter((t) => !t.cancelled);
    assert.strictEqual(waiting.length, 1, 'the wait must be bounded by a timer, not open-ended');
    assert.strictEqual(waiting[0].ms, RESTART_EXIT_GRACE_MS);
    waiting[0].cancelled = true;
    waiting[0].fn();

    assert.strictEqual(await restarting, 'spawned');
    assert.strictEqual(h.spawns.length, 2, 'it gives up on the old one and starts a new one');
  });

  // Two ensure() calls can genuinely overlap: activate() fires one without
  // awaiting it, and the probe it is waiting on has a two-second budget, during
  // which the user can invoke Restart Daemon. Both would see "nothing there" and
  // both would spawn -- and only the second handle is kept, so the first is an
  // orphan from the moment it starts.
  test('concurrent ensure() calls start one daemon, not two', async () => {
    const h = new Harness();
    let release: (v: boolean) => void = () => undefined;
    h.deps.probe = () =>
      new Promise<boolean>((r) => {
        release = r;
      });
    const sup = new DaemonSupervisor(h.deps);

    const first = sup.ensure();
    const second = sup.ensure();
    release(false);
    await Promise.all([first, second]);

    assert.strictEqual(
      h.spawns.length,
      1,
      'two overlapping ensures each spawned a daemon, and only the last handle is tracked -- ' +
        'the first is unkillable by dispose() the moment it exists'
    );
  });

  // TWO CONTRACTS THAT LIVE IN DIFFERENT FILES AND MUST AGREE.
  //
  // Both fail silently and only in front of a user, which is why they are pinned
  // here rather than left to review.
  test('every user-facing hint names a command that actually exists', () => {
    const pkg = JSON.parse(
      fs.readFileSync(path.join(__dirname, '..', '..', '..', 'package.json'), 'utf8')
    );
    const titles: string[] = pkg.contributes.commands.map((c: { title: string }) => c.title);

    const src = (f: string) =>
      fs.readFileSync(path.join(__dirname, '..', '..', '..', 'src', f), 'utf8');
    const hints = [...src('daemonClient.ts').matchAll(/run "([^"]+)" from the Command Palette/g)]
      .map((m) => m[1])
      .concat(
        [...src('daemonSupervisor.ts').matchAll(/run "([^"]+)"/g)].map((m) => m[1])
      );

    assert.ok(hints.length >= 2, 'expected the restart hint to appear in both files');
    for (const hint of hints) {
      assert.ok(
        titles.includes(hint),
        `a message tells the user to run "${hint}", but package.json contributes ` +
          `[${titles.join(', ')}]. Searching the palette for that name finds NOTHING — and this ` +
          `message is shown at exactly the moment it is the user's only way out. ` +
          `(This really happened: titles said "CodeTerminal: ..." while every toast said "Mochiii".)`
      );
    }
  });

  // THE INVARIANT THAT BROKE. The log path and the socket must be derived from
  // the SAME string, or a window that adopted a daemon looks for a log its owner
  // never wrote.
  //
  // It was built from the extension host's raw workspace path while the lockfile
  // was keyed on the realpath'd one. Those agree until a workspace is reached
  // through a symlink -- /tmp on macOS is /private/tmp, ~/work -> /mnt/data/work
  // on Linux -- and then one daemon serves two windows that disagree about where
  // its log is. Silent: the adopting window simply reports "no log yet".
  test('the daemon log is keyed on the same resolved root as the lockfile', function () {
    const real = fs.mkdtempSync(path.join(os.tmpdir(), 'logreal'));
    const link = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'loglink')), 'ws');
    try {
      fs.symlinkSync(real, link);
    } catch {
      this.skip(); // NOT RUN: this platform will not create a symlink
      return;
    }

    const logPathFor = (root: string): string => {
      setWorkspaceRoot(root);
      return path.join(resolvedWorkspaceRoot(), '.codeterminal', 'logs', 'daemon.log');
    };

    assert.strictEqual(
      logPathFor(link),
      logPathFor(real),
      'the same directory reached through a symlink must yield ONE log path, or the window ' +
        'that adopted the daemon opens a file the owner never writes'
    );
  });

  test('no workspace means no log path, rather than one written somewhere unpredictable', () => {
    setWorkspaceRoot('');
    assert.strictEqual(
      resolvedWorkspaceRoot(),
      '',
      'with no folder open there is no per-workspace daemon and nowhere predictable for its ' +
        'log; a relative path would land wherever the extension host was started'
    );
  });

  test('the daemon is always given a log file, because an adopting window has no pipe', () => {
    const src = fs.readFileSync(
      path.join(__dirname, '..', '..', '..', 'src', 'extension.ts'),
      'utf8'
    );
    assert.match(
      src,
      /'-log-file'/,
      'spawnDaemon must pass -log-file unconditionally: stdio is `ignore` (deliberately — a ' +
        'detached child would wedge on a pipe nobody drains), so a file is the ONLY channel, ' +
        'and the window that most needs it is the one that adopted rather than spawned'
    );
    assert.match(
      src,
      /codeterminal\.showDaemonLog/,
      'and there must be a way to read it from a window that has no child process at all'
    );
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
