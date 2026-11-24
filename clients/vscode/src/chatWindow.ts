// "MOCHIII: OPEN CHAT IN NEW WINDOW" (2026-11-24).
//
// The chat as an editor tab (mochiii.openChatInEditor), then moved into a
// window of its own with VS Code's own command -- the same thing as dragging
// the tab out of the window. It is still the one chat of this VS Code window:
// the same panel, the same host and daemon, so it sends, streams, asks and
// applies edits as the side bar's chat does, and closing it closes a tab, not
// the daemon.
//
// workbench.action.moveEditorToNewWindow is in VS Code's source from 1.85.0
// (src/vs/workbench/browser/parts/editor/editorCommands.ts), the oldest
// version engines.vscode allows. A build without it -- an older fork -- gets
// the tab and one line saying why.
//
// Kept free of `vscode` so the order of the steps can be tested on its own.

export const OPEN_CHAT_IN_NEW_WINDOW_COMMAND = 'mochiii.openChatInNewWindow';
export const MOVE_EDITOR_TO_NEW_WINDOW = 'workbench.action.moveEditorToNewWindow';

export function tooOldForWindows(version: string): string {
  return `VS Code ${version} cannot open the chat in a window of its own, so it is open as an editor tab.`;
}

export const COULD_NOT_MOVE = 'The chat is open as an editor tab: VS Code did not make it the active editor, so it was not moved to a window of its own.';

export interface NewWindowSteps {
  /** The ids of every command VS Code has (vscode.commands.getCommands). */
  commands(): Thenable<string[]>;
  /** Opens the chat as an editor tab, or shows the tab it is already in. */
  openInEditor(): void | Thenable<unknown>;
  /** Whether the chat's tab is the active editor: the move takes the active one. */
  chatIsActiveEditor(): boolean;
  execute(command: string): Thenable<unknown>;
  /** One line to the user. */
  say(line: string): void;
  version: string;
  wait?(ms: number): Promise<void>;
}

export async function openChatInNewWindow(steps: NewWindowSteps): Promise<'window' | 'tab'> {
  const canMove = (await steps.commands()).includes(MOVE_EDITOR_TO_NEW_WINDOW);
  await steps.openInEditor();
  if (!canMove) {
    steps.say(tooOldForWindows(steps.version));
    return 'tab';
  }
  const wait = steps.wait ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
  // A tab just created, or just revealed, is the active editor a moment later.
  for (let i = 0; i < 40 && !steps.chatIsActiveEditor(); i++) {
    await wait(50);
  }
  if (!steps.chatIsActiveEditor()) {
    // Moving whatever else is active would move the user's file, not the chat.
    steps.say(COULD_NOT_MOVE);
    return 'tab';
  }
  await steps.execute(MOVE_EDITOR_TO_NEW_WINDOW);
  return 'window';
}
