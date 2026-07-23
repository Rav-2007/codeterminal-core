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
    const extensionTestsPath = path.resolve(__dirname, './suite/index');

    // A throwaway workspace (NOT the extension dir itself) and a fresh user-data
    // dir keep the run hermetic and let the host open with a workspace folder
    // (some behaviors read workspaceFolders).
    const workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'ct-vscode-ws-'));
    const userDataDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ct-vscode-user-'));

    await runTests({
      extensionDevelopmentPath,
      extensionTestsPath,
      launchArgs: [workspace, '--user-data-dir', userDataDir, '--disable-gpu'],
    });
  } catch (err) {
    console.error('Failed to run E2E tests:', err);
    process.exit(1);
  }
}

void main();
