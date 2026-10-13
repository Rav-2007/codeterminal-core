import * as assert from 'assert';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';

import { DAEMON_BIN_ENV, daemonBinaryName, resolveConfigPathSetting, resolveDaemonBin } from '../../daemonBinary';

// AN EXECUTABLE PATH IS NEVER DERIVED FROM WORKSPACE CONTENT.
//
// This is the invariant, and it is the third time this project has had to state
// it. `/mcp-server` in the TUI resolved its binary against the working
// directory, which for the TUI is the repository you opened; that was fixed in
// d56e425 and its comment records the measured exploit -- attacker code ran and
// read MOCHIII_API_KEY out of the inherited environment. resolveHelperBinPath
// had the same shape and closed it with runningFromGoRun.
//
// The VS Code extension was not carried along by either fix. Its copy was
// strictly worse than the TUI's: the TUI needed the process CWD to happen to be
// the workspace, while runMCPServerList was PASSED the opened folder and joined
// onto it directly:
//
//     path.join(workspace, 'daemon', 'mochiii-daemon')
//
// Open a repository that ships that file, type one slash command, and it runs.
// No approval prompt stands in front of a local slash command.
//
// It survived two independent fixes of the same bug because nothing tested the
// RULE, only the two instances. That is what this file is for: it tests the
// invariant where the code expresses it (resolveDaemonBin), so a fourth surface
// cannot quietly reintroduce it.
//
// The door a user reached it through is gone: runMCPServerList was retired on
// 2026-10-13, when /mcp-server began opening the MCP marketplace, which asks
// the RUNNING daemon over its socket and starts no program. Its tests went with
// it. resolveDaemonBin still chooses the binary the extension spawns as its
// daemon, which is why everything below stays.
//
// NEUTER CHECK. Add a candidate relative to the working directory --
// `path.join('daemon', daemonBinaryName())` -- to resolveDaemonBin and
// `resolverIgnoresTheCurrentWorkingDirectory` FAILS.

function makeTempDir(t: string): string {
  return fs.mkdtempSync(path.join(os.tmpdir(), t));
}

// plantHostileDaemon writes an executable at <root>/daemon/mochiii-daemon
// that records the fact it ran. On Windows a .cmd is what actually executes, so
// the fixture matches the platform rather than pretending every runner is POSIX.
function plantHostileDaemon(root: string, sentinel: string): void {
  const dir = path.join(root, 'daemon');
  fs.mkdirSync(dir, { recursive: true });

  if (process.platform === 'win32') {
    const p = path.join(dir, 'mochiii-daemon.cmd');
    fs.writeFileSync(p, `@echo off\r\necho pwned > "${sentinel}"\r\n`);
    // The vulnerable code looked for the extension-less name too.
    fs.writeFileSync(path.join(dir, 'mochiii-daemon'), `@echo off\r\necho pwned > "${sentinel}"\r\n`);
    return;
  }

  const p = path.join(dir, 'mochiii-daemon');
  fs.writeFileSync(p, `#!/bin/sh\necho pwned > "${sentinel}"\n`);
  fs.chmodSync(p, 0o755);
}

suite('daemon binary resolution — workspace content is never executable', () => {
  let savedEnv: string | undefined;

  setup(() => {
    savedEnv = process.env[DAEMON_BIN_ENV];
    delete process.env[DAEMON_BIN_ENV];
  });

  teardown(() => {
    if (savedEnv === undefined) {
      delete process.env[DAEMON_BIN_ENV];
    } else {
      process.env[DAEMON_BIN_ENV] = savedEnv;
    }
  });

  // The invariant where the code expresses it, and where it is cheap to state
  // exhaustively.
  test('resolverNeverReturnsAPathInsideTheWorkspace', () => {
    const workspace = makeTempDir('ct-hostile-ws2-');
    const extensionPath = makeTempDir('ct-ext2-');
    plantHostileDaemon(workspace, path.join(workspace, 'unused'));

    const resolved = resolveDaemonBin(extensionPath);
    if (resolved !== undefined) {
      assert.ok(
        !path.resolve(resolved).startsWith(path.resolve(workspace)),
        `resolver returned a path inside the workspace: ${resolved}`,
      );
    }
  });

  // The CWD is not a trusted input either. The TUI's exploit arrived through
  // exactly this door, and Node makes it easy to reintroduce, because
  // fs.existsSync('name') consults the CWD while execFile('name') consults PATH
  // -- so a bare-name candidate can be CHECKED in one place and RUN from
  // another.
  test('resolverIgnoresTheCurrentWorkingDirectory', () => {
    const workspace = makeTempDir('ct-hostile-cwd-');
    const extensionPath = makeTempDir('ct-ext3-');
    plantHostileDaemon(workspace, path.join(workspace, 'unused'));

    const originalCwd = process.cwd();
    try {
      process.chdir(workspace);
      const resolved = resolveDaemonBin(extensionPath);
      if (resolved !== undefined) {
        assert.ok(
          !path.resolve(resolved).startsWith(path.resolve(workspace)),
          `resolver trusted the CWD: ${resolved}`,
        );
      }
    } finally {
      process.chdir(originalCwd);
    }
  });

  // The installed layout must still work, or the fix is an outage. This is the
  // half that keeps the extension starting its daemon rather than refusing to --
  // the macOS peer-credential outage is the standing reminder that a
  // fail-closed change with no positive test is how you ship one.
  test('findsTheDaemonShippedInsideTheExtension', () => {
    const extensionPath = makeTempDir('ct-ext4-');
    const dir = path.join(extensionPath, 'daemon');
    fs.mkdirSync(dir, { recursive: true });
    const installed = path.join(dir, daemonBinaryName());
    fs.writeFileSync(installed, '');
    fs.chmodSync(installed, 0o755);

    assert.strictEqual(resolveDaemonBin(extensionPath), installed);
  });

  // An explicit environment variable IS a trusted input: the user set it. This
  // mirrors the TUI's MOCHIII_DAEMON_BIN so one sentence describes both.
  test('honoursTheExplicitEnvironmentOverride', () => {
    const extensionPath = makeTempDir('ct-ext5-');
    const elsewhere = path.join(makeTempDir('ct-override-'), 'my-daemon');
    fs.writeFileSync(elsewhere, '');
    fs.chmodSync(elsewhere, 0o755);

    process.env[DAEMON_BIN_ENV] = elsewhere;
    assert.strictEqual(resolveDaemonBin(extensionPath), elsewhere);
  });

  // A directory named like the binary must not satisfy the search, or the
  // resolver returns something execFile cannot run and the failure surfaces as
  // a confusing EACCES instead of an honest "not found".
  test('ignoresADirectoryWithTheBinarysName', () => {
    const extensionPath = makeTempDir('ct-ext6-');
    fs.mkdirSync(path.join(extensionPath, 'daemon', daemonBinaryName()), { recursive: true });

    const resolved = resolveDaemonBin(extensionPath);
    assert.ok(
      resolved === undefined || !resolved.startsWith(path.join(extensionPath, 'daemon', daemonBinaryName())),
      `resolver returned a directory: ${resolved}`,
    );
  });
});

// mochiii.configPath -> --config. The capability added when the extension
// learned to start the daemon in agent mode. Safe only because the value is
// absolute (a relative --config resolves against the daemon's cwd, the
// workspace) and machine-scoped (a workspace cannot set it). resolveConfigPathSetting
// enforces the first; VS Code the second (pinned in localCommandsHostile.test.ts).
suite('mochiii.configPath resolves to an absolute --config', () => {
  test('an absolute path passes through', () => {
    assert.strictEqual(resolveConfigPathSetting('/etc/mochiii/models.json').path, '/etc/mochiii/models.json');
  });
  test('~ expands to the home directory', () => {
    assert.strictEqual(resolveConfigPathSetting('~/cfg/models.json').path, path.join(os.homedir(), 'cfg', 'models.json'));
  });
  test('empty or whitespace yields nothing to pass', () => {
    assert.deepStrictEqual(resolveConfigPathSetting(''), {});
    assert.deepStrictEqual(resolveConfigPathSetting('   '), {});
    assert.deepStrictEqual(resolveConfigPathSetting(undefined), {});
  });
  test('a relative path is refused, not resolved against the cwd', () => {
    const r = resolveConfigPathSetting('models.json');
    assert.strictEqual(r.path, undefined, 'a relative config path must not be passed');
    assert.ok(r.warning && /absolute/.test(r.warning), `expected an absolute-path warning, got: ${r.warning}`);
  });
});
