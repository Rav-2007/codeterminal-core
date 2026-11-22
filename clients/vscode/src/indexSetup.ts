// "INDEX THIS FOLDER FOR CODE SEARCH?", asked once per folder (owner, 2026-11-22).
//
// The extension never built an index, so every start logged "retrieval
// disabled: no index found" and the model read whole files. The order is the
// terminal's (clients/tui/firstrun.go): stop the service, have the model,
// index, start the service. A service that started without an index never
// picks one up.

import * as cp from 'child_process';
import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import * as vscode from 'vscode';

import { withoutCredentials } from './daemonCredentials';
import { DaemonSupervisor } from './daemonSupervisor';
import { ensureModelAvailable, humanMB, queryModelStatus, runDownload } from './modelSetup';

const ASKED_KEY = 'mochiii.indexOffer.asked';
// clients/tui/firstrun.go's indexStampFile; indexSetup.test.ts holds the two to it.
export const INDEX_STAMP = 'embedder_stamp.json';

export type FolderIndex = 'indexed' | 'tooLarge' | 'notAProject' | 'none';

function real(p: string): string {
  try {
    return fs.realpathSync(p);
  } catch {
    return path.resolve(p);
  }
}

export function folderIndex(root: string, home = os.homedir()): FolderIndex {
  if (!root) return 'notAProject';
  const clean = real(root);
  const fold = (p: string): string => (process.platform === 'win32' ? p.toLowerCase() : p);
  if (path.dirname(clean) === clean || fold(clean) === fold(real(home))) return 'notAProject';
  const dir = path.join(clean, '.mochiii', 'index');
  if (fs.existsSync(path.join(dir, 'TOO_LARGE'))) return 'tooLarge';
  return fs.existsSync(path.join(dir, INDEX_STAMP)) ? 'indexed' : 'none';
}

export interface IndexDeps {
  root: string;
  binaryPath: string;
  context: vscode.ExtensionContext;
  output: vscode.OutputChannel;
  supervisor: Pick<DaemonSupervisor, 'whileStopped'>;
  // A task in this window: stopping the service would end it.
  busy: () => boolean;
}

export function wasAsked(memento: vscode.Memento, root: string): boolean {
  return (memento.get<string[]>(ASKED_KEY) ?? []).includes(real(root));
}

export async function markAsked(memento: vscode.Memento, root: string): Promise<void> {
  if (wasAsked(memento, root)) return;
  await memento.update(ASKED_KEY, [...(memento.get<string[]>(ASKED_KEY) ?? []), real(root)]);
}

// offerIndex never throws and never blocks activation.
export async function offerIndex(deps: IndexDeps): Promise<void> {
  const { root, binaryPath, context, output } = deps;
  try {
    const state = folderIndex(root);
    if (state === 'indexed') return await ensureModelAvailable(binaryPath, context, output);
    if (state !== 'none' || wasAsked(context.globalState, root)) return;
    const model = await queryModelStatus(binaryPath);
    if (!model) return; // no bundled service to index with

    const INDEX = 'Index';
    const download = model.cached ? '' : ` It first downloads a search model (${humanMB(model.downloadBytes)}, once).`;
    // MODAL, because it is asked once: VS Code drops any other notification
    // into the bell after 10 seconds, buttons or not, and this one was missed.
    const picked = await vscode.window.showInformationMessage(
      `Index "${path.basename(root)}" for code search?`,
      {
        modal: true,
        detail:
          `It runs on this computer, takes a minute or two for most projects, and adds its folder to .gitignore.${download}\n\n` +
          'Mochiii asks once per folder. Later: "Mochiii: Index Folder for Code Search".',
      },
      { title: INDEX },
      { title: 'Not now', isCloseAffordance: true },
    );
    await markAsked(context.globalState, root);
    if (picked?.title === INDEX) await buildIndex(deps);
  } catch (err) {
    output.appendLine(`index setup: ${(err as Error).message}`);
  }
}

let building = false;

export async function buildIndex(deps: IndexDeps): Promise<void> {
  const { root, binaryPath, output } = deps;
  if (building) return;
  if (folderIndex(root) === 'notAProject') {
    void vscode.window.showInformationMessage('Mochiii does not index your home folder or a whole drive. Open a project folder.');
    return;
  }
  building = true;
  try {
    const model = await queryModelStatus(binaryPath);
    if (!model) {
      void vscode.window.showWarningMessage('Mochiii could not check its code-search model. See the Mochiii output channel.');
      return;
    }
    if (!model.cached && !(await runDownload(binaryPath, output))) {
      void vscode.window.showWarningMessage(
        'The code-search model was not downloaded, so this folder was not indexed. To try again: "Mochiii: Index Folder for Code Search".',
      );
      return;
    }

    const result = await vscode.window.withProgress(
      { location: vscode.ProgressLocation.Notification, title: 'Mochiii: indexing for code search', cancellable: true },
      async (progress, token) => {
        while (deps.busy() && !token.isCancellationRequested) {
          progress.report({ message: 'starts when the current task ends' });
          await new Promise((resolve) => setTimeout(resolve, 1000));
        }
        if (token.isCancellationRequested) return 'cancelled';
        progress.report({ message: '' });
        const ran = await deps.supervisor.whileStopped(() => runIndex(binaryPath, root, output, progress, token));
        return ran.ran ? ran.value : 'elsewhere';
      },
    );

    if (result === 'done' && folderIndex(root) === 'tooLarge') {
      void vscode.window.showWarningMessage('This folder is too large to index, so code search stays off. Search and file reading still work.');
    } else if (result === 'done') {
      void vscode.window.showInformationMessage('Indexed. Code search is on for this folder.');
    } else if (result === 'elsewhere') {
      void vscode.window.showWarningMessage(
        'Another window or the terminal app is running Mochiii for this folder, so it was not indexed. Close it, then run "Mochiii: Index Folder for Code Search".',
      );
    } else if (result === 'failed') {
      void vscode.window.showWarningMessage('Indexing did not finish. Mochiii works without code search; see the Mochiii output channel.');
    }
  } finally {
    building = false;
  }
}

function runIndex(
  binaryPath: string,
  root: string,
  output: vscode.OutputChannel,
  progress: vscode.Progress<{ message?: string }>,
  token: vscode.CancellationToken,
): Promise<'done' | 'failed' | 'cancelled'> {
  return new Promise((resolve) => {
    if (token.isCancellationRequested) return resolve('cancelled');
    const child = cp.spawn(binaryPath, ['index', root], {
      stdio: ['ignore', 'ignore', 'pipe'],
      cwd: root,
      env: withoutCredentials(process.env),
    });
    token.onCancellationRequested(() => child.kill());
    child.stderr?.on('data', (chunk: Buffer) => {
      const text = chunk.toString();
      output.append(text);
      const last = text.trim().split('\n').pop();
      if (last) progress.report({ message: last.replace(/^.*?index:\s*/, '').slice(0, 100) });
    });
    child.on('error', () => resolve('failed'));
    child.on('close', (code) => resolve(token.isCancellationRequested ? 'cancelled' : code === 0 ? 'done' : 'failed'));
  });
}
