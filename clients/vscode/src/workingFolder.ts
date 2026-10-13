// THE ONE FOLDER MOCHIII WORKS IN, when the window has several.
//
// The daemon takes exactly one --workspace: it indexes that folder, confines
// its tools and edits to it, and names its lockfile after it. Until Stage 8 it
// was always workspaceFolders[0], with nothing saying so -- found in the first
// Stage 7 walkthrough, where "list the new_file folder" failed twice because
// new_file was the SECOND folder of the workspace.
//
// The choice is the user's, made with "Mochiii: Choose Working Folder", and
// kept in the extension's per-workspace storage -- NOT in a setting. A setting
// can be written by a repository's .vscode/settings.json or a .code-workspace
// file, and which folder the daemon indexes and may edit is not something a
// repository should choose. Whatever is saved can only select among folders
// already open in the window; anything else falls back to the first.

/** A workspace folder as this module needs it: plain data, no vscode import. */
export interface FolderChoice {
  name: string;
  fsPath: string;
}

/** The workspaceState key the choice is saved under. */
export const WORKING_FOLDER_KEY = 'mochiii.workingFolder';

/**
 * chooseWorkingFolder picks the folder the daemon works in: the saved one if
 * it is still open in this window, else the first. `warning` says so when a
 * saved choice is no longer open. '' when no folder is open.
 */
export function chooseWorkingFolder(folders: readonly FolderChoice[], saved: string | undefined): { fsPath: string; warning?: string } {
  if (folders.length === 0) {
    return { fsPath: '' };
  }
  if (saved) {
    const hit = folders.find((f) => f.fsPath === saved);
    if (hit) {
      return { fsPath: hit.fsPath };
    }
    return {
      fsPath: folders[0].fsPath,
      warning: `Mochiii's working folder (${saved}) is not open in this window, so it works in ${folders[0].name}.`,
    };
  }
  return { fsPath: folders[0].fsPath };
}

let active = '';

/** Record the folder this window's daemon was started on (extension.ts, at activation). */
export function setActiveWorkingFolder(fsPath: string): void {
  active = fsPath;
}

/** The folder this window's daemon works in, or '' before activation decided one. */
export function activeWorkingFolder(): string {
  return active;
}
