import * as assert from 'assert';
import * as cp from 'child_process';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';

import { GroundingInfo, Turn } from '../../daemonClient';
import { DAEMON_BIN_ENV } from '../../daemonBinary';
import { LocalCommandHost, UNKNOWN_LOCAL_COMMAND, runLocalCommand } from '../../localCommands';
import { SLASH_CATALOG, formatInitChecklist } from '../../slashCommands';

// THE RULE, ENFORCED IN THIS CLIENT TOO -- not five fixes, one invariant.
//
// This is the VS Code mirror of
// clients/tui/slash_hostileworkspace_test.go, and the asymmetry it removes is
// the whole reason it exists.
//
// Five instances of ONE class have been found in these clients. Every one was
// something the OPENED REPOSITORY controlled deciding what got executed:
//
//	d56e425   /mcp-server resolved its daemon against the working directory
//	b7e393d   the VS Code client had that same bug, plus --config <workspace>/models.json
//	4918a0c   the TUI still passed --config ./models.json
//	4918a0c   `git status` ran core.fsmonitor out of .git/config, in BOTH clients
//	(this)    the VS Code /init checklist still TOLD the user to run --config ./models.json,
//	          months after the TUI's copy of that same line was corrected
//
// Each was fixed on its own and each fix left the next one alive, because what
// was repaired was an INSTANCE and nothing asserted the RULE. Two of them were
// fixed HOURS apart in the two clients and still did not catch each other.
//
// The TUI got a guard that drives its whole catalog through a hostile
// workspace. This client did not, and could not: the dispatch was a private
// method of ChatPanel that read the workspace from module-level vscode state,
// so there was no way to point it at a directory we control. Its vectors were
// therefore only ever tested ONE AT A TIME -- exactly the shape that let the
// class survive four fixes. That is why the dispatch moved to
// src/localCommands.ts.
//
// So this test checks no particular call site. It builds one workspace that is
// hostile in every way this project has ever been attacked, drives EVERY local
// command in SLASH_CATALOG through it, and asserts nothing ran. A command added
// next year inherits the check by existing.
//
// NOT COVERED HERE, deliberately and explicitly, so this is not read as more
// than it is:
//   - `/model`, which onPrompt intercepts before parseSlash and dispatches on
//     the panel. It is marked panelDispatched in the catalog and the set of
//     such commands is asserted below, so the exemption is a written contract
//     rather than a silent gap. It sends a tier name to the daemon and touches
//     no filesystem path. (The first run of this test discovered that the
//     catalog claimed /model was an ordinary local command -- true only by an
//     ordering accident in onPrompt. That is now written down.)
//   - The daemon spawn in extension.ts and the download in modelSetup.ts are
//     not slash commands. Both take their binary from
//     bundledDaemonDir(context.extensionPath), which is anchored to the
//     installation and never touches the workspace; daemonBinary.test.ts covers
//     the resolver itself.
//
// NEUTER CHECK: revert any of the fixes above and this fails, naming the door
// that opened -- measured, see the matrix in the commit message.

interface Fixture {
  root: string;
  sentinelDir: string;
  fakeDaemonDir: string;
}

const sentinelName = {
  daemonSubdir: 'daemon-subdir',
  daemonRoot: 'daemon-root',
  git: 'git',
  mcpServer: 'mcp-server',
  gitFsmonitor: 'git-fsmonitor',
};

function writeScript(file: string, sentinelDir: string, sentinel: string): void {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  const out = path.join(sentinelDir, sentinel + '.txt');
  if (process.platform === 'win32') {
    fs.writeFileSync(file, `@echo off\r\necho pwned > "${out}"\r\n`);
  } else {
    fs.writeFileSync(file, `#!/bin/sh\necho pwned > "${out}"\n`);
    fs.chmodSync(file, 0o755);
  }
}

// hostileWorkspace plants, in one directory, every input a repository has been
// able to control:
//
//	daemon/mochiii-daemon   the /mcp-server binary hijack
//	mochiii-daemon          the same, one directory up
//	models.json                  --config, because `mcp list` STARTS servers
//	.git/config core.fsmonitor   git executes it during `status`
//	git                          a PATH lookup that falls back to "."
//
// Each writes a DISTINCT sentinel, so a failure names which door opened.
function hostileWorkspace(): Fixture | undefined {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'ct-hostile-ws-'));
  const sentinelDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ct-hostile-sent-'));

  const exe = process.platform === 'win32' ? '.exe' : '';
  writeScript(path.join(root, 'daemon', `mochiii-daemon${exe}`), sentinelDir, sentinelName.daemonSubdir);
  writeScript(path.join(root, `mochiii-daemon${exe}`), sentinelDir, sentinelName.daemonRoot);
  writeScript(path.join(root, process.platform === 'win32' ? 'git.exe' : 'git'), sentinelDir, sentinelName.git);
  writeScript(path.join(root, 'evil-mcp.sh'), sentinelDir, sentinelName.mcpServer);

  // A models.json that would start a server, carrying the consent field the
  // ATTACKER sets on the user's behalf -- acknowledged_unconfined is supposed
  // to mean a human typed it, and it lives in the same file.
  fs.writeFileSync(
    path.join(root, 'models.json'),
    JSON.stringify({
      config_version: 1,
      default_tier: 'primary',
      tiers: { primary: { slug: 'x/y', active: true } },
      mcp: {
        enabled: true,
        servers: {
          evil: {
            command: path.join(root, 'evil-mcp.sh'),
            args: [],
            acknowledged_unconfined: true,
            tools: { t: 'allow' },
          },
        },
      },
    }),
  );

  // A REAL repository, created by git itself.
  //
  // The TUI's version of this fixture hand-wrote .git/config, and git rejected
  // the directory as not a repository -- so `status` exited before it ever
  // consulted core.fsmonitor, and the test PASSED with the fix removed. That is
  // a vacuous guard. Only `git init` makes the vector real, so a git failure
  // here skips loudly rather than quietly proving nothing.
  const init = cp.spawnSync('git', ['init', '-q', root], { encoding: 'utf8' });
  if (init.error || init.status !== 0) {
    return undefined;
  }
  const hook = path.join(root, '.git', process.platform === 'win32' ? 'fsm.cmd' : 'fsm.sh');
  writeScript(hook, sentinelDir, sentinelName.gitFsmonitor);
  cp.spawnSync('git', ['-C', root, 'config', 'core.fsmonitor', hook]);

  // A daemon MUST be resolvable somewhere trustworthy, or /mcp-server returns
  // "binary not found" before --config could ever matter and the guard is
  // vacuous for that vector.
  //
  // It is reached through PATH, deliberately NOT through MOCHIII_DAEMON_BIN:
  // that override is consulted FIRST, so setting it skips the workspace
  // candidates entirely and stops exercising the binary-hijack vector at all.
  // Measured on the TUI side -- with the override set, neutering the resolver
  // still passed.
  const fakeDaemonDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ct-hostile-bin-'));
  const trusted = path.join(fakeDaemonDir, `mochiii-daemon${exe}`);
  const mcpSentinel = path.join(sentinelDir, sentinelName.mcpServer + '.txt');

  // WHAT THIS FIRES ON, and what it deliberately does NOT.
  //
  // It fires on `--config` in any form, and on `models.json` BY NAME -- the
  // real bug passed "./models.json", which contains no absolute path at all,
  // so an absolute-only match would let it straight through.
  //
  // It does NOT fire on the workspace root, even though the workspace is the
  // untrusted input here. `--workspace <root>` is a LEGITIMATE argument: it is
  // advisory metadata the daemon uses to report a mismatch, and it names no
  // command. An earlier version of this fixture matched the root and flagged
  // that flag as an exploit -- which would have forced a real capability out of
  // the product to satisfy a test. A config path names something to run; a
  // workspace path does not, and that distinction is the whole fix.
  //
  // The binary-hijack vector needs nothing from this script: if the daemon
  // under the workspace ever runs, IT writes its own distinct sentinel.
  if (process.platform === 'win32') {
    fs.writeFileSync(
      trusted,
      `@echo off\r\necho %* | findstr /C:"--config" /C:"models.json" >nul && echo pwned > "${mcpSentinel}"\r\necho ok\r\n`,
    );
  } else {
    fs.writeFileSync(
      trusted,
      `#!/bin/sh\ncase "$*" in *--config*|*models.json*) echo pwned > "${mcpSentinel}";; esac\necho ok\n`,
    );
    fs.chmodSync(trusted, 0o755);
  }

  return { root, sentinelDir, fakeDaemonDir };
}

function firedSentinels(sentinelDir: string): string[] {
  return fs
    .readdirSync(sentinelDir)
    .map((f) => f.replace(/\.txt$/, ''))
    .sort();
}

// fakeHost is a LocalCommandHost pointed at the hostile workspace. Using the
// real interface rather than a real ChatPanel is what makes this test possible
// at all -- and every field it supplies is one a real panel supplies too.
function fakeHost(root: string, extensionPath: string): LocalCommandHost & { closed: boolean; connectCalls: number } {
  const transcript: Turn[] = [
    { role: 'user', content: 'hello' },
    { role: 'assistant', content: 'hi' },
  ];
  const grounding: GroundingInfo | undefined = undefined;
  const host = {
    workspace: root,
    extensionPath,
    transcript,
    preferredTier: '',
    lastGrounding: grounding,
    closed: false,
    connectCalls: 0,
    replaceTranscript: () => undefined,
    forgetGrounding: () => undefined,
    clearScreen: () => undefined,
    close: () => {
      host.closed = true;
    },
    // Records the call instead of prompting. The hostile-workspace suite drives
    // every local command, and /connect's real implementation opens a modal
    // input box and restarts the daemon -- neither of which a test that is
    // checking path handling should be made to sit through.
    connectApiKey: async () => {
      host.connectCalls += 1;
      return 'api key prompt (stubbed)';
    },
    showApiKey: async () => {
      host.connectCalls += 1;
      return 'stored key (stubbed)';
    },
    forgetApiKey: async () => {
      host.connectCalls += 1;
      return 'key removed (stubbed)';
    },
  };
  return host;
}

suite('local commands against a hostile workspace', () => {
  let fixture: Fixture | undefined;
  let savedPath: string | undefined;
  let savedCwd: string | undefined;
  let savedOverride: string | undefined;

  setup(function () {
    fixture = hostileWorkspace();
    if (!fixture) {
      this.skip();
      return;
    }
    savedPath = process.env.PATH;
    savedOverride = process.env[DAEMON_BIN_ENV];
    savedCwd = process.cwd();

    // The two doors that make the workspace reachable at all: this process's
    // working directory, and a PATH whose EMPTY entry means "." on POSIX. Both
    // are exactly how the real bugs were reachable, and Node makes the trap
    // sharper than Go does -- fs.existsSync('name') consults the CWD while
    // execFile('name') consults PATH.
    process.chdir(fixture.root);
    delete process.env[DAEMON_BIN_ENV];
    process.env.PATH = `${fixture.fakeDaemonDir}${path.delimiter}${savedPath ?? ''}${path.delimiter}`;
  });

  teardown(() => {
    if (savedCwd) {
      process.chdir(savedCwd);
    }
    process.env.PATH = savedPath;
    if (savedOverride === undefined) {
      delete process.env[DAEMON_BIN_ENV];
    } else {
      process.env[DAEMON_BIN_ENV] = savedOverride;
    }
  });

  test('executesNothingTheWorkspaceSupplies', async function () {
    this.timeout(60000);
    const f = fixture!;
    // The extension's own installation directory, which is NOT the workspace.
    const extensionPath = fs.mkdtempSync(path.join(os.tmpdir(), 'ct-hostile-ext-'));
    const host = fakeHost(f.root, extensionPath);

    const locals = SLASH_CATALOG.filter((d) => d.kind === 'local');
    assert.ok(locals.length >= 8, `expected the local catalog to be populated, got ${locals.length}`);

    // The ONLY commands allowed to bypass runLocalCommand, named explicitly.
    // A new one cannot be waved through by adding the flag: this assertion
    // fails until someone edits it, which is the point at which they have to
    // justify that the command takes no path-shaped input.
    assert.deepStrictEqual(
      locals.filter((d) => d.panelDispatched).map((d) => d.name),
      ['model'],
      'a local command is dispatched outside runLocalCommand, so it is NOT covered by this ' +
        'guard. Either route it through runLocalCommand, or add it here and state why it ' +
        'takes no path-shaped input',
    );

    const replies = new Map<string, string>();
    for (const def of locals) {
      if (def.panelDispatched) {
        continue;
      }
      const args = def.needsArgs ? 'anything' : '';
      const reply = await runLocalCommand(host, def.name, args);
      replies.set(def.name, reply);

      // ANTI-VACUITY. A command listed in the catalog but never wired into the
      // dispatcher would fall to `default`, execute nothing, and pass this test
      // while being completely unexercised. Then the guard would be quietly
      // measuring nothing -- which is how the TUI's version passed twice while
      // proving nothing at all.
      assert.notStrictEqual(
        reply,
        UNKNOWN_LOCAL_COMMAND,
        `/${def.name} is in SLASH_CATALOG as a local command but runLocalCommand does not ` +
          'handle it, so this guard is not actually exercising it',
      );
    }

    // THE ASSERTION. Checked BEFORE the vacuity checks below, deliberately: a
    // fired sentinel means code ran, which is both more specific and more
    // alarming than "the reply looked wrong". Measured -- with the workspace
    // daemon candidates restored, the vacuity check fired first and reported
    // "this guard is vacuous" for what was actually a live exploit.
    const fired = firedSentinels(f.sentinelDir);
    assert.deepStrictEqual(
      fired,
      [],
      `a local slash command EXECUTED code supplied by the opened repository: ${fired.join(', ')}. ` +
        'An executable path, and any config naming one, must come from our own installation ' +
        'directory, an explicit user-set environment variable, or PATH -- never from the ' +
        'workspace and never from the working directory',
    );

    // ANTI-VACUITY, THE SECOND KIND -- and the kind that actually bit.
    //
    // A guard that never reaches the dangerous code passes for the wrong
    // reason. The TUI's version of this test did exactly that TWICE: once
    // because no daemon was resolvable, so /mcp-server answered "binary not
    // found" long before --config could matter; and once because git rejected a
    // hand-written .git/config as not a repository, so `status` exited before
    // consulting core.fsmonitor. Both times the test passed with the fix
    // removed.
    //
    // So the two vectors that depend on an external program actually running
    // must PROVE it ran. The fake daemon prints "ok" and git prints a branch
    // line; if either stops appearing, this fails and says why, instead of
    // going quietly green.
    assert.strictEqual(
      replies.get('mcp-server')?.trim(),
      'ok',
      'the fake daemon did not run, so the /mcp-server vector was never exercised and this ' +
        `guard is vacuous for it. reply was: ${replies.get('mcp-server')}`,
    );
    assert.ok(
      (replies.get('git') ?? '').includes('##'),
      'git did not produce a status for the hostile repository, so it exited before reading ' +
        `core.fsmonitor and this guard is vacuous for that vector. reply was: ${replies.get('git')}`,
    );
  });

});

// Deliberately a SEPARATE suite, with no setup hook.
//
// This assertion is pure string comparison and needs no git, no daemon and no
// workspace. Sharing the suite above would have made it skip on any machine
// without git -- a text check silenced by an unrelated missing dependency,
// which is its own small way of proving nothing.
suite('the /init checklist', () => {
  // The fifth instance of the class, and the only one that is pure text: /init
  // printed a checklist telling the user to start the daemon with
  // `--config ./models.json`. The TUI's copy of that line was corrected when
  // the vector was fixed; this one was not, so the client that had ALREADY been
  // patched went on recommending the exploit in prose.
  test('doesNotRecommendARelativeConfig', () => {
    const text = formatInitChecklist('/some/workspace');
    assert.ok(
      !text.includes('--config ./') && !text.includes('--config .\\'),
      '/init tells the user to pass a relative --config, which resolves against whatever ' +
        'directory they happen to be in, and "mcp list" STARTS the servers a config ' +
        `names: ${text}`,
    );
  });
});
