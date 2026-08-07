// DaemonSupervisor decides, on every window, whether this extension host should
// START a daemon or USE one that is already there -- and what to do when one it
// started stops.
//
// It used to be four closures inside activate(): spawn unconditionally, and
// restart on any non-zero exit. That was wrong in both halves.
//
// SPAWNING UNCONDITIONALLY. Two VS Code windows on the same repository SHOULD
// share one daemon -- one root, one index, one set of answers -- and the client
// half already did, because both derive the same per-workspace lockfile. Only
// the spawn half did not: the second window started a daemon that could not
// bind, and every one of those cost an 81 MB embedder helper that os.Exit
// orphaned (see daemon/startuprace_test.go). Probing first is what makes the
// two halves agree.
//
// RESTARTING ON ANY NON-ZERO EXIT. "The daemon stopped" is not one event. A
// daemon that cannot read models.json will fail identically forever and must be
// reported; a daemon that lost a startup race did not fail at all and must be
// adopted in silence. The daemon now says which (daemon/exitcodes.go), and this
// is the consumer of that contract.
//
// EVERYTHING IS INJECTED so the policy can be tested as policy. The interesting
// assertions here are about what does NOT happen -- no spawn when a daemon is
// already up, no budget spent on a benign exit, no unbounded retry -- and those
// are unobservable through a real spawn without waiting on wall-clock time and
// a real subprocess. See test/suite/daemonSupervisor.test.ts.

// EXIT_ALREADY_RUNNING mirrors exitAlreadyRunning in daemon/exitcodes.go. It
// means another daemon already serves this exact workspace and this one stopped
// because it was redundant -- a NORMAL outcome, not a failure. The numbers are a
// contract; change them on both sides or not at all.
export const EXIT_ALREADY_RUNNING = 3;

// The restart budget. A daemon that cannot start does not become able to start
// by being started again -- a missing binary, an unreadable models.json and a
// bad API base all fail identically every time -- so the retry is bounded and
// backs off. Five attempts spans ~30 s: long enough to ride out a transient
// failure, short enough that a real one is reported while the user still
// remembers opening the folder.
export const MAX_RESTARTS = 5;
export const RESTART_BASE_MS = 2000;

// How long to keep looking for the winner after losing the race.
//
// Exit 3 is POSITIVE EVIDENCE that a daemon holds the address -- ours tried to
// bind and was refused. But the winner publishes its lockfile only at the END of
// startup: daemon/main.go binds, then runs setupRetrieval and setupMemoryStore,
// and only then writes the lockfile. So there is a window in which the winner
// provably exists and is provably undiscoverable, and asking once lands in it.
//
// Left unhandled, that turned an ordinary second window into an error: probe
// misses, budget charged, retry spawns another daemon, that one loses too --
// repeat until the budget is spent and the user is told the daemon "will not be
// restarted again", about a daemon that was starting perfectly well.
//
// SIZING, MEASURED RATHER THAN GUESSED. On this machine, warm, with retrieval
// enabled, spawn-to-lockfile is 0.45-0.49 s over three runs -- NOT the several
// seconds an earlier version of this comment asserted. The embedding model is
// loaded inside the helper subprocess and `waitReady` returns well before the
// window that matters.
//
// 15 s is therefore ~30x the measured figure, and the headroom is deliberate
// rather than padding: setupRetrieval also opens the chromem collection and the
// FTS index, and both scale with the size of the workspace's index, which this
// repository's is not a large example of. A cold page cache, a network home
// directory, or a first-ever start pay more than a warm local one.
//
// The cost of the headroom is bounded and falls only on the genuinely-broken
// case: a winner that really died delays the first retry by 15 s. The cost of
// being too tight is the bug above, which was silent and reached the user.
// Nothing is charged to the restart budget during the wait, because nothing has
// failed.
export const ADOPT_PROBE_ATTEMPTS = 10;
export const ADOPT_PROBE_INTERVAL_MS = 1500;

// How long restart() waits for the daemon it just asked to stop.
//
// kill() DELIVERS a signal; it does not wait for the process to die, and the
// daemon deliberately drains in-flight work before it goes. Deciding anything
// during that window gets both possible answers wrong: probe while it is still
// answering handshakes and restart "adopts" the corpse it just made, owning
// nothing; spawn before the socket is released and the replacement loses the
// bind, exits 3, and waits out a 15 s adoption window for a winner that is
// never coming.
//
// Bounded, because the daemon a user is restarting is disproportionately likely
// to be one that is wedged -- and a wedged daemon must not wedge the command
// reached for BECAUSE it is wedged. Past this we give up on it and start a new
// one; if the old one is somehow still holding the address, the new one exits 3
// and the ordinary adoption path takes over, which is the correct behaviour
// rather than a special case.
export const RESTART_EXIT_GRACE_MS = 5000;

/** A daemon process this supervisor started. */
export interface DaemonHandle {
  onExit(cb: (code: number | null, signal: string | null) => void): void;
  onError(cb: (err: Error) => void): void;
  kill(): void;
}

export interface Timer {
  cancel(): void;
}

export interface SupervisorDeps {
  /** Is a daemon already serving this workspace? Proven by handshake. */
  probe: () => Promise<boolean>;
  spawn: () => DaemonHandle;
  /** User-visible. Reserved for things a user can act on. */
  warn: (message: string) => void;
  error: (message: string) => void;
  /** Diagnostic only; never a toast. Ordinary adoption must be silent. */
  log: (message: string) => void;
  schedule: (ms: number, fn: () => void) => Timer;
}

export type EnsureOutcome = 'adopted' | 'spawned' | 'disposed';

/**
 * A daemon this supervisor started, and the promise of its death.
 *
 * `exited` is settled by the handle's own exit/error callbacks and is what
 * restart() waits on. It is deliberately settled even for a handle that is no
 * longer `own`: the whole reason to wait is that we have just stopped owning it.
 */
interface OwnedDaemon {
  handle: DaemonHandle;
  exited: Promise<void>;
}

export class DaemonSupervisor {
  private restarts = 0;
  private timer: Timer | undefined;
  private disposed = false;
  // Set ONLY when this supervisor spawned the process. An adopted daemon
  // belongs to another window and is never held here -- which is what stops
  // dispose() from killing it. See dispose().
  private own: OwnedDaemon | undefined;
  // The ensure() currently in flight, so two callers get one daemon. See
  // ensure().
  private ensuring: Promise<EnsureOutcome> | undefined;

  constructor(private readonly deps: SupervisorDeps) {}

  /**
   * Make sure a daemon is serving this workspace: adopt one, or start one.
   *
   * Probe first, always. This is also the retry path, so a retry that happens
   * to race another window's successful start adopts it rather than spawning a
   * third process to lose a second race.
   */
  async ensure(): Promise<EnsureOutcome> {
    // ONE AT A TIME. Two ensure() calls genuinely overlap: activate() fires one
    // without awaiting it, and the probe it waits on has a two-second budget,
    // during which the user can invoke Restart Daemon. Both would see "nothing
    // there" and both would spawn -- and startOwn keeps only the last handle, so
    // the first daemon is unkillable by dispose() from the moment it exists.
    //
    // Joining the in-flight call rather than queueing a second one is the right
    // answer to the question actually being asked, which is "is a daemon
    // serving this workspace" and not "start one now".
    if (this.ensuring) {
      return this.ensuring;
    }
    const run = this.ensureOnce();
    this.ensuring = run;
    try {
      return await run;
    } finally {
      if (this.ensuring === run) {
        this.ensuring = undefined;
      }
    }
  }

  private async ensureOnce(): Promise<EnsureOutcome> {
    if (this.disposed) {
      return 'disposed';
    }
    if (await this.deps.probe()) {
      this.deps.log('a daemon is already serving this workspace; adopted it');
      return 'adopted';
    }
    // Re-checked after the await: dispose() can land while the probe is in
    // flight, and spawning a daemon for a window that is closing would leak the
    // exact process this class exists to stop leaking.
    if (this.disposed) {
      return 'disposed';
    }
    this.startOwn();
    return 'spawned';
  }

  /**
   * A user asking for a restart, which is a deliberate act and usually follows
   * fixing whatever exhausted the budget. It gets a fresh one, or the command
   * silently does nothing after the fifth crash -- precisely when a user
   * reaches for it.
   */
  async restart(): Promise<EnsureOutcome> {
    this.restarts = 0;
    this.cancelTimer();

    // WAIT FOR IT TO ACTUALLY GO before deciding anything. kill() delivers a
    // signal and returns; the daemon drains in-flight work before exiting. See
    // RESTART_EXIT_GRACE_MS for both ways this used to go wrong.
    const dying = this.killOwn();
    if (dying) {
      await this.settleOrGiveUp(dying, RESTART_EXIT_GRACE_MS);
      return this.ensure();
    }

    if (await this.deps.probe()) {
      // Nothing of ours to restart, and the daemon serving this workspace is
      // still there: it belongs to another window. Killing another window's
      // daemon out from under it would be worse than declining, and this
      // extension has no way to ask it to stop -- the shutdown RPC that would
      // make this possible is not built yet. Say so rather than appear to work.
      this.deps.warn(
        'The daemon serving this workspace was started by another VS Code window, so this ' +
          'window cannot restart it. Restart it from that window, or close it and try again.'
      );
      return 'adopted';
    }
    return this.ensure();
  }

  /**
   * Stop supervising. Kills only a daemon THIS supervisor started.
   *
   * An adopted daemon is another window's, and that window is still using it.
   * The old code could not tell the difference because it never adopted; now
   * that it does, "kill the daemon on deactivate" has to mean "kill mine".
   */
  dispose(): void {
    this.disposed = true;
    this.cancelTimer();
    this.killOwn();
  }

  private startOwn(): void {
    const handle = this.deps.spawn();
    let settle: () => void = () => undefined;
    const owned: OwnedDaemon = {
      handle,
      exited: new Promise<void>((resolve) => {
        settle = resolve;
      }),
    };
    this.own = owned;

    // `this.own !== owned` IS THE WHOLE FIX FOR A REAL ORPHAN.
    //
    // kill() delivers a signal; the exit event arrives later, by which time
    // restart() has already spawned the replacement. Both callbacks used to
    // clear this.own unconditionally, so the dead daemon's exit un-tracked the
    // LIVE one -- and dispose() then had nothing to kill. That is a daemon
    // nobody owns, holding the socket and its 81 MB embedder helper, until the
    // machine is rebooted: precisely the leak this class was written to stop,
    // reached through the back door.
    //
    // settle() runs FIRST and unconditionally, before the ownership check,
    // because restart() is waiting on exactly this promise for a handle it has
    // deliberately stopped owning.
    const stale = (): boolean => this.disposed || this.own !== owned;

    handle.onError((err) => {
      settle();
      if (stale()) {
        return;
      }
      this.own = undefined;
      this.deps.error(`Mochiii daemon failed to start: ${err.message}. Please restart VS Code.`);
    });

    handle.onExit((code, signal) => {
      settle();
      if (stale()) {
        return;
      }
      this.own = undefined;

      // A clean stop, or one we asked for.
      if (code === 0 || signal === 'SIGTERM' || signal === 'SIGKILL') {
        return;
      }

      if (code === EXIT_ALREADY_RUNNING) {
        void this.adoptAfterLosingTheRace();
        return;
      }

      this.scheduleRetry(code);
    });
  }

  /**
   * The daemon reported that someone else already serves this workspace.
   *
   * That is the ordinary outcome of opening a second window on one repository
   * and it is NOT charged to the restart budget -- nothing failed. It is also
   * not announced: a user who opened a second window did not do anything wrong
   * and has nothing to act on.
   */
  private async adoptAfterLosingTheRace(attempt = 1): Promise<void> {
    if (this.disposed) {
      return;
    }
    if (await this.deps.probe()) {
      this.deps.log('another window started the daemon first; adopted it');
      return;
    }
    if (this.disposed) {
      return;
    }

    // Not there YET. The winner publishes its lockfile only once it has
    // finished starting, and starting includes loading the embedding model, so
    // an immediate miss says nothing. Wait and look again -- see
    // ADOPT_PROBE_ATTEMPTS. No budget is charged here; nothing has failed.
    if (attempt < ADOPT_PROBE_ATTEMPTS) {
      this.setTimer(ADOPT_PROBE_INTERVAL_MS, () => {
        void this.adoptAfterLosingTheRace(attempt + 1);
      });
      return;
    }

    // It said someone else had the address, and after waiting out a cold start,
    // nobody does -- the winner died between its bind and now. Something is
    // genuinely wrong, so this one DOES spend budget. Without that, two daemons
    // dying alternately would hand the race back and forth forever, which is
    // the unbounded loop this whole design is meant to be free of.
    this.deps.log('lost the startup race, but the winner never appeared; retrying');
    this.scheduleRetry(EXIT_ALREADY_RUNNING);
  }

  private scheduleRetry(code: number | null): void {
    this.restarts++;
    if (this.restarts > MAX_RESTARTS) {
      this.deps.error(
        `Mochiii daemon exited ${code} ${MAX_RESTARTS} times and will not be restarted again. ` +
          `Check the daemon log, or run "Mochiii: Restart Daemon" once the cause is fixed.`
      );
      return;
    }

    const delay = RESTART_BASE_MS * 2 ** (this.restarts - 1);
    this.deps.warn(`Mochiii daemon exited ${code}; restarting (${this.restarts}/${MAX_RESTARTS})...`);
    this.setTimer(delay, () => {
      void this.ensure();
    });
  }

  /**
   * Record a pending timer, replacing (and cancelling) any previous one.
   *
   * There is only ever one thing this supervisor is waiting to do next, so two
   * live timers means one of them is a leak: it fires later, calls ensure(), and
   * spends budget on behalf of a decision that was superseded. Assigning
   * `this.timer` directly is what let that happen.
   */
  private setTimer(ms: number, fn: () => void): void {
    this.cancelTimer();
    this.timer = this.deps.schedule(ms, () => {
      this.timer = undefined;
      fn();
    });
  }

  private cancelTimer(): void {
    this.timer?.cancel();
    this.timer = undefined;
  }

  /**
   * Wait for `p`, but not past `ms`.
   *
   * The deadline goes through deps.schedule like every other wait in this
   * class, so it is a fake clock in tests rather than a real second of wall
   * time. It is deliberately NOT stored in this.timer: cancelTimer() means
   * "abandon the next scheduled action", and this is a caller blocked on an
   * answer, not an action to abandon.
   */
  private settleOrGiveUp(p: Promise<void>, ms: number): Promise<void> {
    return new Promise<void>((resolve) => {
      let done = false;
      const finish = (): void => {
        if (!done) {
          done = true;
          resolve();
        }
      };
      const deadline = this.deps.schedule(ms, () => {
        this.deps.log(
          `the daemon did not exit within ${ms}ms of being asked to; starting a replacement anyway`
        );
        finish();
      });
      void p.then(() => {
        deadline.cancel();
        finish();
      });
    });
  }

  /**
   * Stop the daemon this supervisor started, and hand back the promise of its
   * death so a caller can wait for it. Undefined when we own nothing.
   */
  private killOwn(): Promise<void> | undefined {
    const owned = this.own;
    if (!owned) {
      return undefined;
    }
    this.own = undefined;
    try {
      owned.handle.kill();
    } catch {
      // Already dead. The only goal was that it not be running.
    }
    return owned.exited;
  }
}
