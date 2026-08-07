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

export class DaemonSupervisor {
  private restarts = 0;
  private timer: Timer | undefined;
  private disposed = false;
  // Set ONLY when this supervisor spawned the process. An adopted daemon
  // belongs to another window and is never held here -- which is what stops
  // dispose() from killing it. See dispose().
  private own: DaemonHandle | undefined;

  constructor(private readonly deps: SupervisorDeps) {}

  /**
   * Make sure a daemon is serving this workspace: adopt one, or start one.
   *
   * Probe first, always. This is also the retry path, so a retry that happens
   * to race another window's successful start adopts it rather than spawning a
   * third process to lose a second race.
   */
  async ensure(): Promise<EnsureOutcome> {
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

    const hadOwn = this.own !== undefined;
    this.killOwn();

    if (!hadOwn && (await this.deps.probe())) {
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
    this.own = handle;

    handle.onError((err) => {
      if (this.disposed) {
        return;
      }
      this.own = undefined;
      this.deps.error(`Mochiii daemon failed to start: ${err.message}. Please restart VS Code.`);
    });

    handle.onExit((code, signal) => {
      if (this.disposed) {
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
      this.timer = this.deps.schedule(ADOPT_PROBE_INTERVAL_MS, () => {
        this.timer = undefined;
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
    this.timer = this.deps.schedule(delay, () => {
      this.timer = undefined;
      void this.ensure();
    });
  }

  private cancelTimer(): void {
    this.timer?.cancel();
    this.timer = undefined;
  }

  private killOwn(): void {
    if (!this.own) {
      return;
    }
    const handle = this.own;
    this.own = undefined;
    try {
      handle.kill();
    } catch {
      // Already dead. The only goal was that it not be running.
    }
  }
}
