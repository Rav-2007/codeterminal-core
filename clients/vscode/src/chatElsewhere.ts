// THE CHAT HAS MORE THAN ONE HOME, AND ONLY ONE IS LIVE.
//
// Mochiii's chat can sit in the Activity Bar's side bar (an icon beside
// Explorer, Search and the other agents), in the secondary side bar beside the
// files, or in an editor tab. The conversation is one -- it lives in the daemon
// -- but a ChatPanel holds work that cannot be split or copied: a tool approval
// the daemon is holding open, an answer still streaming, an edit review one
// block in. So exactly one host renders it, and every other host says where it
// is and offers to bring it over, instead of showing a frozen transcript whose
// Send goes nowhere.

/** The command a placeholder's link runs, with the view id to move into. */
export const MOVE_CHAT_HERE_COMMAND = 'mochiii.moveChatHere';

/** Where the live chat is, in the words the placeholder uses. */
export type ChatLocation = 'activity' | 'secondary' | 'editor';

function describe(where: ChatLocation): string {
  switch (where) {
    case 'activity':
      return "Mochiii's side bar (its icon in the Activity Bar)";
    case 'secondary':
      return 'the secondary side bar';
    default:
      return 'an editor tab';
  }
}

/**
 * chatElsewhereHtml is the placeholder a chat host shows while the chat is
 * live somewhere else. No script: the one action is a command link, and the
 * webview is opened with only that command allowed (see chatElsewhereOptions).
 */
export function chatElsewhereHtml(viewId: string, where: ChatLocation, busy: boolean): string {
  const args = encodeURIComponent(JSON.stringify([viewId]));
  const note = busy
    ? '<p class="warn">Mochiii is working there right now. Moving the chat stops the answer in progress.</p>'
    : '';
  return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline';">
<style>
  body {
    margin: 0;
    padding: 24px 16px;
    font-family: var(--vscode-font-family);
    font-size: var(--vscode-font-size);
    color: var(--vscode-foreground);
    background: var(--vscode-sideBar-background, var(--vscode-editor-background));
  }
  h2 { font-size: 13px; font-weight: 600; margin: 0 0 8px; }
  p { margin: 0 0 12px; line-height: 1.5; color: var(--vscode-descriptionForeground); }
  p.warn { color: var(--vscode-editorWarning-foreground, var(--vscode-foreground)); }
  a.button {
    display: inline-block;
    padding: 4px 12px;
    border-radius: 2px;
    text-decoration: none;
    color: var(--vscode-button-foreground);
    background: var(--vscode-button-background);
  }
  a.button:hover { background: var(--vscode-button-hoverBackground); }
  a.button:focus-visible { outline: 1px solid var(--vscode-focusBorder); outline-offset: 2px; }
</style>
</head>
<body>
  <h2>The chat is open elsewhere</h2>
  <p>Mochiii's chat is open in ${describe(where)}. It is one conversation, shown in one place at a time.</p>
  ${note}
  <a class="button" role="button" href="command:${MOVE_CHAT_HERE_COMMAND}?${args}">Move the chat here</a>
</body>
</html>`;
}

/** The webview options for a placeholder: no script, one command. */
export function chatElsewhereOptions(): { enableScripts: false; enableCommandUris: readonly string[]; localResourceRoots: [] } {
  return { enableScripts: false, enableCommandUris: [MOVE_CHAT_HERE_COMMAND], localResourceRoots: [] };
}
