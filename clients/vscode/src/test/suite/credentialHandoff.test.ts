import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';
import * as vscode from 'vscode';

import { CREDENTIALS_FROM_STDIN_FLAG } from '../../daemonCredentials';
import { lastDaemonSpawnForTest } from '../../extension';

// THE ACTIVATED EXTENSION HANDS ITS KEY TO THE DAEMON ON STDIN, AND THE DAEMON'S
// ENVIRONMENT HOLDS NO KEY -- not the SecretStorage one, and not one VS Code
// inherited from a shell.
//
// The extension used to start the daemon with its SecretStorage key as
// MOCHIII_API_KEY, and copied process.env wholesale, so any MOCHIII_API_KEY or
// MOCHIII_PROXY_KEY VS Code had inherited went along too. A variable a process
// starts with is readable in /proc/<pid>/environ for its whole life.
//
// This drives the REAL activated extension: a key goes into real SecretStorage
// (through mochiii.test.storeApiKey, registered only in ExtensionMode.Test),
// two more are planted in process.env as a shell would have, and the real
// mochiii.restartDaemon command spawns a real daemon. Asserted:
//
//   1. what the spawn call was GIVEN (lastDaemonSpawnForTest -- names only): the
//      flag, no credential variable in the environment, both fields on stdin;
//   2. that the daemon took its key from stdin (its own log says so, masked);
//   3. on Linux, the daemon process's actual /proc/<pid>/environ.

const EXT_ID = 'mochiii.mochiii-vscode';
const STORED = 'FAKE-vscode-secretstorage-0451-not-real';
const SHELL_KEY = 'FAKE-inherited-shell-0451-not-real';
const SHELL_PROXY = 'FAKE-inherited-proxy-0451-not-real';
const CREDENTIAL_NAMES = ['MOCHIII_API_KEY', 'MOCHIII_PROXY_KEY'];

function workspaceRoot(): string {
  const folder = vscode.workspace.workspaceFolders?.[0];
  assert.ok(folder, 'the test host opened no workspace folder, so no daemon is supervised');
  return fs.realpathSync(folder.uri.fsPath);
}

async function waitFor<T>(what: string, ms: number, probe: () => T | undefined): Promise<T> {
  const deadline = Date.now() + ms;
  for (;;) {
    const v = probe();
    if (v !== undefined) {
      return v;
    }
    if (Date.now() > deadline) {
      throw new Error(`timed out after ${ms}ms waiting for ${what}`);
    }
    await new Promise((r) => setTimeout(r, 100));
  }
}

// Linux only: the pids whose command line is a daemon for root started with the
// flag. Read from /proc, so it finds the process the extension really started.
function daemonPids(root: string): number[] {
  const pids: number[] = [];
  for (const entry of fs.readdirSync('/proc')) {
    if (!/^\d+$/.test(entry)) {
      continue;
    }
    try {
      const argv = fs.readFileSync(`/proc/${entry}/cmdline`, 'utf8').split('\0');
      if (argv.includes(CREDENTIALS_FROM_STDIN_FLAG) && argv.includes(root)) {
        pids.push(Number(entry));
      }
    } catch {
      /* gone, or not ours */
    }
  }
  return pids;
}

// Variable NAMES in /proc/<pid>/environ, waiting for a populated block: Linux
// releases a vfork parent during exec before the new environment is in place,
// and an empty read would make an absence check pass for anything.
async function environNames(pid: number): Promise<string[]> {
  const raw = await waitFor(`a populated /proc/${pid}/environ`, 5000, () => {
    const b = fs.readFileSync(`/proc/${pid}/environ`);
    return b.length > 0 ? b : undefined;
  });
  return raw
    .toString('utf8')
    .split('\0')
    .filter((kv) => kv !== '')
    .map((kv) => kv.slice(0, kv.indexOf('=') < 0 ? kv.length : kv.indexOf('=')));
}

suite('the daemon is handed its key on stdin, never in its environment', function () {
  // A real daemon, restarted twice. Nothing here is fast.
  this.timeout(120000);

  const saved: Record<string, string | undefined> = {};

  suiteSetup(async () => {
    const ext = vscode.extensions.getExtension(EXT_ID);
    assert.ok(ext, `${EXT_ID} is not installed in the test host`);
    await ext.activate();
    for (const name of CREDENTIAL_NAMES) {
      saved[name] = process.env[name];
    }
  });

  suiteTeardown(async () => {
    for (const name of CREDENTIAL_NAMES) {
      if (saved[name] === undefined) {
        delete process.env[name];
      } else {
        process.env[name] = saved[name];
      }
    }
    // Leave the host as found: no stored key, and a daemon started without one.
    await vscode.commands.executeCommand('mochiii.test.storeApiKey', undefined);
    await vscode.commands.executeCommand('mochiii.restartDaemon');
  });

  test('after activation with a key in SecretStorage, the spawned daemon is given no key in its environment', async () => {
    const root = workspaceRoot();
    await vscode.commands.executeCommand('mochiii.test.storeApiKey', STORED);
    // As a shell that launched VS Code would have left them.
    process.env.MOCHIII_API_KEY = SHELL_KEY;
    process.env.MOCHIII_PROXY_KEY = SHELL_PROXY;

    const before = lastDaemonSpawnForTest();
    await vscode.commands.executeCommand('mochiii.restartDaemon');
    const rec = lastDaemonSpawnForTest();

    // 1. WHAT THE SPAWN CALL WAS GIVEN.
    assert.ok(rec && rec !== before, 'restarting did not spawn a daemon, so nothing about a spawn was shown');
    assert.ok(rec.args.includes(CREDENTIALS_FROM_STDIN_FLAG), `spawned without ${CREDENTIALS_FROM_STDIN_FLAG}: ${rec.args.join(' ')}`);
    for (const name of CREDENTIAL_NAMES) {
      assert.ok(!rec.envNames.includes(name), `the daemon was spawned with ${name} in its environment`);
    }
    assert.ok(rec.envNames.length > 0, 'the spawn was given an empty environment, so its lack of a key proves nothing');
    assert.strictEqual(rec.stdin, 'pipe');
    assert.deepStrictEqual([...rec.stdinFields].sort(), ['api_key', 'proxy_key']);
    const recorded = JSON.stringify(rec);
    for (const v of [STORED, SHELL_KEY, SHELL_PROXY]) {
      assert.ok(!recorded.includes(v), 'the spawn record holds a key value; it must hold names only');
    }

    // 2. THE DAEMON TOOK THE KEY FROM STDIN, and logged it masked.
    const logPath = path.join(root, '.mochiii', 'logs', 'daemon.log');
    const log = await waitFor('the daemon to log where its key came from', 30000, () => {
      const text = fs.existsSync(logPath) ? fs.readFileSync(logPath, 'utf8') : '';
      return text.includes('using the key supplied on stdin') ? text : undefined;
    });
    for (const v of [STORED, SHELL_KEY, SHELL_PROXY]) {
      assert.ok(!log.includes(v), 'the daemon log carries a raw key');
    }

    // 3. THE PROCESS ITSELF, where procfs exists.
    if (process.platform !== 'linux') {
      return;
    }
    const pids = await waitFor('the restarted daemon in /proc', 20000, () => {
      const p = daemonPids(root);
      return p.length > 0 ? p : undefined;
    });
    for (const pid of pids) {
      const names = await environNames(pid);
      for (const name of CREDENTIAL_NAMES) {
        assert.ok(!names.includes(name), `/proc/${pid}/environ holds ${name}`);
      }
      const argv = fs.readFileSync(`/proc/${pid}/cmdline`, 'utf8');
      for (const v of [STORED, SHELL_KEY, SHELL_PROXY]) {
        assert.ok(!argv.includes(v), `a key is on /proc/${pid}/cmdline`);
      }
    }
  });
});
