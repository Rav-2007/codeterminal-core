// runTest launches a real VS Code Extension Development Host via
// @vscode/test-electron: it downloads (and caches under .vscode-test/) a real
// VS Code build matching the extension's engines.vscode, then starts it with
// THIS extension loaded and points it at the compiled mocha suite. This is the
// in-repo runner the M1-M3 report said did not yet exist.
//
// If the environment cannot run a full Electron host (no display server,
// sandbox restrictions), @vscode/test-electron surfaces the real failure here
// and the process exits non-zero -- we do not silently fall back to a stub.

import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';

import { runTests } from '@vscode/test-electron';

// cachedVersion picks the newest VS Code build already under .vscode-test, so
// a local run does not download ~330 MB whenever a new stable ships -- measured:
// a run that should take 20s sat on "Downloading (326.00 MB)" with five builds
// already cached. MOCHIII_VSCODE_TEST_VERSION overrides it ("stable" for the
// latest release, or an exact version). With nothing cached -- a fresh
// checkout, CI -- the default is left to @vscode/test-electron, as before.
// Read before the VSCODE_* scrub below; the name avoids that prefix anyway.
function cachedVersion(root: string): string | undefined {
  const wanted = process.env.MOCHIII_VSCODE_TEST_VERSION;
  if (wanted) {
    return wanted;
  }
  const prefix = 'vscode-' + (process.platform === 'darwin' ? 'darwin' : process.platform) + '-';
  let names: string[] = [];
  try {
    // is-complete is @vscode/test-electron's own marker, written once an
    // install has fully unpacked. An interrupted download leaves a folder
    // without it -- measured: 355 MB, no bin/, unusable.
    names = fs
      .readdirSync(path.join(root, '.vscode-test'))
      .filter((n) => n.startsWith(prefix) && fs.existsSync(path.join(root, '.vscode-test', n, 'is-complete')));
  } catch {
    return undefined;
  }
  const versions = names
    .map((n) => n.slice(n.lastIndexOf('-') + 1))
    .filter((v) => /^\d+\.\d+\.\d+$/.test(v))
    .sort((a, b) => {
      const pa = a.split('.').map(Number);
      const pb = b.split('.').map(Number);
      return pa[0] - pb[0] || pa[1] - pb[1] || pa[2] - pb[2];
    });
  return versions.length > 0 ? versions[versions.length - 1] : undefined;
}

async function main(): Promise<void> {
  try {
    // Running this harness from inside VS Code's integrated terminal (or the
    // Claude Code VS Code extension) pollutes the environment with
    // ELECTRON_RUN_AS_NODE=1 and a set of VSCODE_* vars belonging to the OUTER
    // VS Code. @vscode/test-electron spawns the downloaded VS Code inheriting
    // that env, so ELECTRON_RUN_AS_NODE makes the nested Electron run as plain
    // Node -- it then treats the first launch arg as a script to execute and
    // dies with "Cannot find module <workspace>" (or loads the extension outside
    // the host and fails to resolve 'vscode'). Strip that pollution so the
    // nested VS Code launches as a real GUI extension host.
    delete process.env.ELECTRON_RUN_AS_NODE;
    for (const key of Object.keys(process.env)) {
      if (key.startsWith('VSCODE_')) {
        delete process.env[key];
      }
    }

    const extensionDevelopmentPath = path.resolve(__dirname, '../../');
    const version = cachedVersion(extensionDevelopmentPath);
    if (version) {
      console.log('runTest: using VS Code ' + version + ' (set MOCHIII_VSCODE_TEST_VERSION=stable for the latest)');
    }
    const extensionTestsPath = path.resolve(__dirname, './suite/index');

    // A throwaway workspace (NOT the extension dir itself) and a fresh user-data
    // dir keep the run hermetic and let the host open with a workspace folder
    // (some behaviors read workspaceFolders).
    const workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'ct-vscode-ws-'));
    const userDataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ct-vscode-user-'));

    try {
      await runTests({
        version,
        extensionDevelopmentPath,
        extensionTestsPath,
        launchArgs: [workspace, '--user-data-dir', userDataDir, '--disable-gpu'],
      });
    } finally {
      // THE HOST DOES NOT CLEAN UP AFTER ITSELF, and these are not small: a
      // user-data dir is tens of megabytes, and one is created per run. Ten
      // runs of this suite left 36 MB across 18 directories and a crashpad
      // handler still holding each one -- measured, on a working machine, while
      // verifying a feature that had nothing to do with any of it.
      //
      // In a finally so a failing suite cleans up too: the run that leaves
      // something behind is usually the one that went wrong.
      for (const dir of [workspace, userDataDir]) {
        try {
          fs.rmSync(dir, { recursive: true, force: true });
        } catch {
          /* best-effort: a live crashpad handler can hold a descriptor here */
        }
      }
    }
  } catch (err) {
    console.error('Failed to run E2E tests:', err);
    process.exit(1);
  }
}

void main();
