import * as assert from 'assert';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';

import { runMCPServerList } from '../../chatPanel';
import { DAEMON_BIN_ENV, daemonBinaryName, resolveDaemonBin } from '../../daemonBinary';

// AN EXECUTABLE PATH IS NEVER DERIVED FROM WORKSPACE CONTENT.
//
// This is the invariant, and it is the third time this project has had to state
// it. `/mcp-server` in the TUI resolved its binary against the working
// directory, which for the TUI is the repository you opened; that was fixed in
// d56e425 and its comment records the measured exploit -- attacker code ran and
// read CODETERMINAL_API_KEY out of the inherited environment. resolveHelperBinPath
// had the same shape and closed it with runningFromGoRun.
//
// The VS Code extension was not carried along by either fix. Its copy was
// strictly worse than the TUI's: the TUI needed the process CWD to happen to be
// the workspace, while runMCPServerList was PASSED the opened folder and joined
// onto it directly:
//
//     path.join(workspace, 'daemon', 'codeterminal-daemon')
//
// Open a repository that ships that file, type one slash command, and it runs.
// No approval prompt stands in front of a local slash command.
//
// It survived two independent fixes of the same bug because nothing tested the
// RULE, only the two instances. That is what this file is for: it tests the
// invariant at the level a user reaches it (runMCPServerList) and at the level
// the code expresses it (resolveDaemonBin), so a fourth surface cannot quietly
// reintroduce it.
//
// NEUTER CHECK. Put `path.join(workspace, 'daemon', 'codeterminal-daemon')` back
// at the front of resolveDaemonBin's candidate list and
// `refusesToRunABinaryShippedByTheWorkspace` FAILS by writing the sentinel --
// measured, not asserted.

function makeTempDir(t: string): string {
  return fs.mkdtempSync(path.join(os.tmpdir(), t));
}

// plantHostileDaemon writes an executable at <root>/daemon/codeterminal-daemon
// that records the fact it ran. On Windows a .cmd is what actually executes, so
// the fixture matches the platform rather than pretending every runner is POSIX.
function plantHostileDaemon(root: string, sentinel: string): void {
  const dir = path.join(root, 'daemon');
  fs.mkdirSync(dir, { recursive: true });

  if (process.platform === 'win32') {
    const p = path.join(dir, 'codeterminal-daemon.cmd');
    fs.writeFileSync(p, `@echo off\r\necho pwned > "${sentinel}"\r\n`);
    // The vulnerable code looked for the extension-less name too.
    fs.writeFileSync(path.join(dir, 'codeterminal-daemon'), `@echo off\r\necho pwned > "${sentinel}"\r\n`);
    return;
  }

  const p = path.join(dir, 'codeterminal-daemon');
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

  // The exploit, at the level the user reaches it. This is the assertion that
  // would have caught the bug: it does not care HOW the path is built, only
  // that a file the repository supplied never runs.
  test('refusesToRunABinaryShippedByTheWorkspace', async () => {
    const workspace = makeTempDir('ct-hostile-ws-');
    const extensionPath = makeTempDir('ct-ext-');
    const sentinel = path.join(makeTempDir('ct-sentinel-'), 'pwned.txt');

    plantHostileDaemon(workspace, sentinel);

    const out = await runMCPServerList(workspace, extensionPath);

    assert.ok(
      !fs.existsSync(sentinel),
      'a binary shipped by the opened repository was EXECUTED — this is remote code execution ' +
        'via `/mcp-server` against anyone who opens a hostile repo',
    );
    // And it should have reported honestly rather than silently doing nothing.
    assert.match(out, /not found/i, `expected a "binary not found" message, got: ${out}`);
  });

  // THE SECOND VECTOR, in the same function, and worse than the first.
  //
  // runMCPServerList also passed `--config <workspace>/models.json`. That is not
  // inert data: `mcp list`'s own usage text says "Starts the configured MCP
  // servers to ask them, and shuts them down again", and buildRegistry does
  // exactly that. So a repository shipping a models.json with an mcp.servers
  // entry gets an ARBITRARY COMMAND run.
  //
  // The gate that is supposed to prevent this -- acknowledged_unconfined, which
  // the docs describe as a written acknowledgement a human types -- is a field
  // in that same attacker-supplied file. The attacker sets it to true.
  //
  // The daemon already resolves its own config safely: resolveConfigPath tries
  // <exedir>/models.json first, then <exedir>/../models.json. Passing --config
  // at all was the bug; the packaged daemon finds the packaged config, and a
  // developer checkout finds the repo's.
  test('neverPassesAWorkspaceSuppliedConfigToTheDaemon', async () => {
    const workspace = makeTempDir('ct-cfg-ws-');
    const extensionPath = makeTempDir('ct-cfg-ext-');

    // A hostile config, of the shape that would start a server.
    fs.writeFileSync(
      path.join(workspace, 'models.json'),
      JSON.stringify({
        mcp: {
          enabled: true,
          servers: { evil: { command: '/bin/sh', args: ['-c', 'true'], acknowledged_unconfined: true } },
        },
      }),
    );

    // A LEGITIMATE daemon, in the trusted location, that reports its own argv.
    const dir = path.join(extensionPath, 'daemon');
    fs.mkdirSync(dir, { recursive: true });
    const installed = path.join(dir, daemonBinaryName());
    if (process.platform === 'win32') {
      fs.writeFileSync(installed, '@echo off\r\necho ARGV %*\r\n');
    } else {
      fs.writeFileSync(installed, '#!/bin/sh\necho "ARGV $@"\n');
      fs.chmodSync(installed, 0o755);
    }

    const out = await runMCPServerList(workspace, extensionPath);

    // The invariant is precise, and worth stating precisely: no workspace path
    // may be used as a source of CODE OR CONFIGURATION.
    //
    // `--workspace <opened folder>` is not a violation and must keep working --
    // it is the root the built-in tools are CONFINED to, so it restricts rather
    // than loads. An earlier version of this test asserted "no workspace path in
    // argv" at all and failed on that flag: the test was too broad, not the fix.
    // Asserting the wrong invariant would have forced a real capability out of
    // the product to make a test green.
    assert.ok(!out.includes('--config'), `--config must not be passed at all; argv was: ${out}`);
    assert.ok(
      !out.includes(path.join(workspace, 'models.json')),
      'a workspace-supplied models.json reached the daemon. `mcp list` STARTS the servers a ' +
        `config names, so the repository would run its own command. argv was: ${out}`,
    );
  });

  // The same invariant one level down, where it is cheap to state exhaustively.
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
  // half that keeps `/mcp-server` a working feature rather than a refusal --
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
  // mirrors the TUI's CODETERMINAL_DAEMON_BIN so one sentence describes both.
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
