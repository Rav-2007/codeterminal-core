import * as assert from 'assert';
import * as cp from 'child_process';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';

import { probeDaemon, setWorkspaceRoot } from '../../daemonClient';
import { DaemonHandle, DaemonSupervisor, SupervisorDeps } from '../../daemonSupervisor';

// THE SEAM NOTHING TESTED: the extension and a REAL daemon process.
//
// The suite around this file is thorough on each side of that seam and covers
// neither crossing it. daemonClientE2E drives the real compiled client over a
// real socket, but the thing answering is a StubDaemon. daemonSupervisor tests
// the spawn/adopt/restart POLICY against an injected FakeHandle that never
// execs anything. daemonBinary tests which PATH gets chosen and never runs it.
//
// So every test could pass while the extension was incapable of starting the
// daemon it ships -- which is not hypothetical in this project: the TUI could
// not reach the daemon at all for three weeks in August, and a lost startup
// race once orphaned an 81 MB helper. Both live exactly here.
//
// This spawns the real binary the way extension.ts's spawnDaemon does, probes
// it with the real exported probeDaemon, and asserts the two contracts that
// only a real process can demonstrate:
//
//   1. a cold workspace SPAWNS, and the daemon then answers a real handshake
//   2. a second supervisor on the same workspace ADOPTS rather than spawning
//
// (2) is the exit-3 contract. Against a fake handle it is a branch; against a
// real daemon holding a real lockfile it is the behaviour a second VS Code
// window actually gets.

const daemonBin = path.resolve(__dirname, '../../../daemon/codeterminal-daemon');

function spawnRealDaemon(workspace: string, logPath: string): DaemonHandle {
  fs.chmodSync(daemonBin, 0o755);
  // The env the daemon needs to reach ITS start. extension.ts's spawnDaemon
  // passes no env at all and the child inherits the extension host's, which is
  // exactly the defect this file's second suite is about; here we supply it so
  // the SEAM -- supervisor policy against a real process and a real socket --
  // is what is under test rather than the missing variable.
  const child = cp.spawn(daemonBin, ['--workspace', workspace, '-log-file', logPath], {
    cwd: workspace,
    detached: true,
    stdio: 'ignore',
    env: { ...process.env, CODETERMINAL_API_BASE: 'http://127.0.0.1:9', CODETERMINAL_API_KEY: 'test' },
  });
  child.unref();
  return {
    onExit: (cb) => child.on('exit', cb),
    onError: (cb) => child.on('error', cb),
    kill: () => child.kill('SIGTERM'),
  };
}

function deps(workspace: string, logPath: string, log: string[]): SupervisorDeps {
  return {
    probe: probeDaemon,
    spawn: () => spawnRealDaemon(workspace, logPath),
    warn: (m) => log.push('warn: ' + m),
    error: (m) => log.push('error: ' + m),
    log: (m) => log.push('log: ' + m),
    schedule: (ms, fn) => {
      const t = setTimeout(fn, ms);
      return { cancel: () => clearTimeout(t) };
    },
  };
}

async function probeUntil(want: boolean, attempts = 40): Promise<boolean> {
  for (let i = 0; i < attempts; i++) {
    if ((await probeDaemon()) === want) return true;
    await new Promise((r) => setTimeout(r, 250));
  }
  return false;
}

suite('the extension can start the daemon it ships', function () {
  // A real process, a real socket and a real handshake. Nothing here is fast.
  this.timeout(60000);

  let workspace: string;
  let logPath: string;
  const supervisors: DaemonSupervisor[] = [];

  setup(() => {
    // Actionable rather than skipped. A test that silently disappears when a
    // build step was missed is the failure mode this repo keeps finding in its
    // own gates; CI runs `npm run compile`, which builds this binary.
    assert.ok(
      fs.existsSync(daemonBin),
      `the daemon binary is not at ${daemonBin}. Run \`npm run compile\` (or \`npm run build:daemon\`) first`,
    );
    // AND ITS CONFIG, WHICH `npm run compile` DOES NOT STAGE.
    //
    // `compile` is build:daemon + tsc; only `build:runtime` runs
    // stage-runtime.js, and that is what copies the repo-root models.json to
    // <exedir>. resolveConfigPath looks beside the executable, one directory
    // up, then falls back to ./models.json relative to CWD -- which here is a
    // fresh temp workspace. So the daemon exited 1 with "reading config
    // ./models.json: no such file or directory", three times, and the
    // supervisor reported only that it kept restarting.
    //
    // IT PASSED LOCALLY AND FAILED ON A RUNNER for the oldest reason there is:
    // a developer tree has a models.json left beside the binary from some
    // earlier `build:runtime`, and a clean checkout does not. Staged here
    // rather than in `pretest`, because adding build:runtime there would drag
    // in build:helper, which needs CGO and onnxruntime and would trade this
    // failure for a worse one.
    const repoRoot = path.resolve(__dirname, '../../../../..');
    const stagedConfig = path.join(path.dirname(daemonBin), 'models.json');
    if (!fs.existsSync(stagedConfig)) {
      const source = path.join(repoRoot, 'models.json');
      assert.ok(fs.existsSync(source), `models.json is not at the repo root (${source})`);
      fs.copyFileSync(source, stagedConfig);
    }
    workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'mochiii-realspawn-'));
    logPath = path.join(workspace, 'daemon.log');
    setWorkspaceRoot(workspace);
  });

  teardown(async () => {
    for (const s of supervisors) s.dispose();
    supervisors.length = 0;
    await probeUntil(false, 20);
    setWorkspaceRoot('');
    try {
      fs.rmSync(workspace, { recursive: true, force: true });
    } catch {
      /* a daemon may still be releasing it; the temp dir is not the assertion */
    }
  });

  test('a cold workspace spawns a daemon that answers a real handshake', async () => {
    const log: string[] = [];
    const supervisor = new DaemonSupervisor(deps(workspace, logPath, log));
    supervisors.push(supervisor);

    assert.strictEqual(await supervisor.ensure(), 'spawned', log.join('\n'));
    assert.ok(
      await probeUntil(true),
      `no daemon answered a handshake for ${workspace}.\nsupervisor log:\n${log.join('\n')}\n` +
        `daemon log:\n${fs.existsSync(logPath) ? fs.readFileSync(logPath, 'utf8') : '(none)'}`,
    );
  });

  test('a second window adopts the running daemon instead of spawning another', async () => {
    const firstLog: string[] = [];
    const first = new DaemonSupervisor(deps(workspace, logPath, firstLog));
    supervisors.push(first);
    assert.strictEqual(await first.ensure(), 'spawned', firstLog.join('\n'));
    assert.ok(await probeUntil(true), firstLog.join('\n'));

    // The second supervisor must NOT exec anything: if it does, that is the
    // lost race that exit 3 exists to make cheap, and this asserts we never
    // pay it in the first place.
    const secondLog: string[] = [];
    let spawnedAgain = false;
    const second = new DaemonSupervisor({
      ...deps(workspace, logPath, secondLog),
      spawn: () => {
        spawnedAgain = true;
        return spawnRealDaemon(workspace, logPath);
      },
    });
    supervisors.push(second);

    assert.strictEqual(await second.ensure(), 'adopted', secondLog.join('\n'));
    assert.strictEqual(spawnedAgain, false, 'the second window started a redundant daemon');
  });
});

// THE FIRST-RUN DEFECT, and the reason the suite above supplies an env.
//
// The daemon requires CODETERMINAL_API_BASE from the ENVIRONMENT
// (daemon/main.go: `apiBase := os.Getenv(...)`, then logger.Fatal). The
// extension's spawnDaemon passes no env, so the child inherits the extension
// host's -- and a VS Code launched from a desktop icon does not carry a shell's
// exports. There is no setting to supply it (package.json contributes only
// commands), no config field, and no .env read by the daemon.
//
// So on an ordinary install the daemon exits 1 five times. What the user was
// told was "Mochiii daemon exited 1 5 times and will not be restarted again":
// an exit code, for a problem that is one line to fix, with the actual reason
// sitting in a log file they were not pointed at.
//
// This asserts the daemon still writes that reason where the extension can find
// it, which is what extension.ts's lastDaemonLogLines now attaches to the error.
suite('a daemon that cannot start says why, somewhere the extension can read it', function () {
  this.timeout(30000);

  test('the failure reason reaches the daemon log file', async () => {
    assert.ok(fs.existsSync(daemonBin), `run \`npm run compile\` first (${daemonBin})`);
    const ws = fs.mkdtempSync(path.join(os.tmpdir(), 'mochiii-nobase-'));
    const log = path.join(ws, 'daemon.log');
    try {
      const env = { ...process.env };
      delete env.CODETERMINAL_API_BASE;

      await new Promise<void>((resolve) => {
        const child = cp.spawn(daemonBin, ['--workspace', ws, '-log-file', log], {
          cwd: ws,
          env,
          stdio: 'ignore',
        });
        child.on('exit', () => resolve());
        child.on('error', () => resolve());
      });

      assert.ok(fs.existsSync(log), 'the daemon died without writing a log file at all');
      const text = fs.readFileSync(log, 'utf8');
      assert.match(
        text,
        /CODETERMINAL_API_BASE must be set/,
        `the daemon exited without recording why. The extension can only tell a user ` +
          `what the daemon wrote, so a silent failure here is an unactionable error there. Log:\n${text}`,
      );
    } finally {
      fs.rmSync(ws, { recursive: true, force: true });
    }
  });
});

