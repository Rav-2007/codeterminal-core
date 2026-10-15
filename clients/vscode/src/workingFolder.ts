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

/** The workspaceState key for the other folders the user has been told about. */
export const FOLDERS_NOTED_KEY = 'mochiii.otherFoldersNoted';

/**
 * otherFoldersNotice is what to tell the user about folders open beside the
 * working one that they have not been told about yet, and the updated list of
 * told folders; message is '' when there is nothing new to say.
 *
 * AT STARTUP, NOT ONLY ON CHANGE (2026-10-15). Adding a second folder to a
 * one-folder window turns it into a workspace and restarts the extension, so
 * onDidChangeWorkspaceFolders never fired for the case it was written for:
 * Mochiii came back with two folders, said nothing, and answered "there are no
 * other folders" about the one it could see. Said once per folder per
 * workspace, so a window that is meant to hold several is not nagged.
 */
export function otherFoldersNotice(
  folders: readonly FolderChoice[],
  working: string,
  noted: readonly string[]
): { message: string; noted: string[] } {
  const told = new Set(noted);
  const fresh = folders.filter((f) => f.fsPath !== working && !told.has(f.fsPath));
  if (!working || fresh.length === 0) {
    return { message: '', noted: [...noted] };
  }
  const current = folders.find((f) => f.fsPath === working)?.name ?? working;
  const names = fresh.map((f) => f.name).join(', ');
  const verb = fresh.length === 1 ? 'is' : 'are';
  return {
    message:
      `Mochiii works in one folder: ${current}. ${names} ${verb} open too -- Mochiii can look inside when you ask ` +
      '(it asks first), or you can make it the working folder.',
    noted: [...noted, ...fresh.map((f) => f.fsPath)],
  };
}
