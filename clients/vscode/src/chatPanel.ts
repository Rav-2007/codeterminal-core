import * as vscode from 'vscode';

import { EditBlockWire, GroundingInfo, Turn, applyEdit, preflightHandshake, streamPrompt } from './daemonClient';

const CLIENT_NAME = 'codeterminal-vscode';
const VIEW_TYPE = 'codeterminalChat';

// turnsFromWire keeps only "user"/"assistant" roles, mirroring
// clients/tui/chat.go's turnsFromProtocol -- defense in depth even though
// the daemon has already re-validated these roles server-side.
function turnsFromWire(turns: Turn[] | undefined): Turn[] {
  if (!turns) {
    return [];
  }
  return turns.filter((t) => t.role === 'user' || t.role === 'assistant');
}

// ChatPanel owns exactly one webview panel and the extension-host side of
// one conversation: the transcript (source of truth for building the next
// PromptRequest.History, mirroring buildHistory in clients/tui/chat.go) and
// the in-flight connection, if any.
export class ChatPanel {
  private static current: ChatPanel | undefined;

  private readonly panel: vscode.WebviewPanel;
  private readonly disposables: vscode.Disposable[] = [];
  private transcript: Turn[] = [];
  private inFlight: AbortController | undefined;

  // pendingEdit is the FIRST edit block from the most recent response's
  // edit_proposals, if any and if not yet applied/skipped -- this slice
  // only ever acts on one block at a time (see MULTI-BLOCK note in
  // onEditProposals below). Cleared on apply, skip, or a new prompt.
  private pendingEdit: EditBlockWire | undefined;
  private applyInFlight = false;

  static createOrShow(extensionUri: vscode.Uri): void {
    if (ChatPanel.current) {
      ChatPanel.current.panel.reveal();
      return;
    }

    const panel = vscode.window.createWebviewPanel(VIEW_TYPE, 'CodeTerminal Chat', vscode.ViewColumn.Beside, {
      enableScripts: true,
      retainContextWhenHidden: true,
      localResourceRoots: [vscode.Uri.joinPath(extensionUri, 'media')],
    });

    ChatPanel.current = new ChatPanel(panel, extensionUri);
  }

  private constructor(panel: vscode.WebviewPanel, extensionUri: vscode.Uri) {
    this.panel = panel;
    this.panel.webview.html = this.getHtml(extensionUri);

    this.panel.webview.onDidReceiveMessage((msg) => this.handleMessage(msg), null, this.disposables);
    this.panel.onDidDispose(() => this.dispose(), null, this.disposables);

    this.runPreflight();
  }

  // runPreflight hydrates persisted cross-session history into the panel
  // before the user ever types -- the ONLY place this ChatPanel reads
  // persisted_history. Subsequent per-prompt connections (streamPrompt)
  // never touch that field.
  private async runPreflight(): Promise<void> {
    try {
      const persisted = turnsFromWire(await preflightHandshake(CLIENT_NAME));
      this.transcript = persisted;
      this.panel.webview.postMessage({ type: 'history', turns: persisted });
    } catch (err) {
      this.panel.webview.postMessage({
        type: 'error',
        message: `Could not reach the daemon: ${(err as Error).message}`,
      });
    }
  }

  private handleMessage(msg: { type: string; text?: string }): void {
    if (msg.type === 'prompt' && typeof msg.text === 'string') {
      this.onPrompt(msg.text);
    } else if (msg.type === 'applyEdit') {
      this.onApplyEdit();
    } else if (msg.type === 'skipEdit') {
      this.pendingEdit = undefined;
    }
  }

  private onPrompt(text: string): void {
    if (this.inFlight) {
      return; // a turn is already in flight; the webview disables input while streaming
    }

    // A new turn makes any not-yet-actioned proposal from the PREVIOUS
    // answer stale -- clear it so a lagging Apply click can never target
    // the wrong block. The webview already hid its panel locally on send;
    // this only guards the extension-host state applyEdit reads from.
    this.pendingEdit = undefined;

    // Snapshot BEFORE appending this turn, so the not-yet-answered prompt
    // never ends up in its own History -- same ordering as chat.go's
    // startTurn.
    const history = [...this.transcript];
    this.transcript.push({ role: 'user', content: text });

    const controller = new AbortController();
    this.inFlight = controller;
    let answer = '';

    streamPrompt(CLIENT_NAME, text, workspacePath(), history, controller.signal, {
      onGrounding: (info: GroundingInfo) => {
        this.panel.webview.postMessage({ type: 'grounding', info });
      },
      onToken: (token: string) => {
        answer += token;
        this.panel.webview.postMessage({ type: 'token', text: token });
      },
      onEditProposals: (proposals: EditBlockWire[]) => {
        // OUT OF SCOPE for this slice: acting on more than the first block.
        // Still show the count so extra proposals read as "not shown yet",
        // not as a bug that silently dropped them.
        this.pendingEdit = proposals[0];
        this.panel.webview.postMessage({
          type: 'editProposal',
          edit: proposals[0],
          moreCount: proposals.length - 1,
        });
      },
      onDone: () => {
        this.transcript.push({ role: 'assistant', content: answer });
        this.inFlight = undefined;
        this.panel.webview.postMessage({ type: 'done' });
      },
      onError: (err: Error) => {
        this.inFlight = undefined;
        this.panel.webview.postMessage({ type: 'error', message: err.message });
      },
    });
  }

  // onApplyEdit sends the pending block to the daemon exactly as received
  // -- no re-parsing, no re-diffing. All safety gates (exact-match,
  // ambiguity-refuse, workspace confinement, secret-file refusal, syntax
  // gate) run daemon-side in editapply.PrepareEdit; this only relays the
  // verbatim result, success or refusal, back to the webview.
  private async onApplyEdit(): Promise<void> {
    if (this.applyInFlight || !this.pendingEdit) {
      return;
    }
    const edit = this.pendingEdit;
    this.applyInFlight = true;
    try {
      const result = await applyEdit(CLIENT_NAME, workspacePath(), edit);
      this.panel.webview.postMessage({
        type: 'applyResult',
        applied: result.applied,
        error: result.error,
        backupDir: result.backup_dir,
      });
    } catch (err) {
      this.panel.webview.postMessage({
        type: 'applyResult',
        applied: false,
        error: (err as Error).message,
      });
    } finally {
      this.applyInFlight = false;
      this.pendingEdit = undefined;
    }
  }

  private dispose(): void {
    // Aborting the panel aborts the in-flight connection -- no orphaned
    // sockets left reading a dead webview.
    this.inFlight?.abort();
    ChatPanel.current = undefined;
    while (this.disposables.length) {
      this.disposables.pop()?.dispose();
    }
    this.panel.dispose();
  }

  private getHtml(extensionUri: vscode.Uri): string {
    const webview = this.panel.webview;
    const scriptUri = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, 'media', 'main.js'));
    const nonce = getNonce();

    return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}';">
<title>CodeTerminal Chat</title>
<style>
  body {
    font-family: var(--vscode-font-family);
    color: var(--vscode-foreground);
    background: var(--vscode-editor-background);
    margin: 0;
    display: flex;
    flex-direction: column;
    height: 100vh;
  }
  #transcript { flex: 1; overflow-y: auto; padding: 8px 12px; }
  .turn { margin-bottom: 14px; white-space: pre-wrap; line-height: 1.4; }
  .turn .role { display: block; font-size: 11px; opacity: 0.6; margin-bottom: 2px; }
  .turn.user .role { color: var(--vscode-textLink-foreground); }
  .turn.error { color: var(--vscode-errorForeground); }
  #grounding { font-size: 11px; opacity: 0.7; padding: 0 12px 6px; min-height: 14px; }
  #inputRow { display: flex; gap: 6px; padding: 8px 12px; border-top: 1px solid var(--vscode-panel-border, transparent); }
  #promptInput {
    flex: 1;
    background: var(--vscode-input-background);
    color: var(--vscode-input-foreground);
    border: 1px solid var(--vscode-input-border, transparent);
    padding: 6px 8px;
    font-family: inherit;
    font-size: inherit;
  }
  button {
    background: var(--vscode-button-background);
    color: var(--vscode-button-foreground);
    border: none;
    padding: 6px 14px;
    cursor: pointer;
  }
  button:disabled { opacity: 0.5; cursor: default; }
  .edit-proposal {
    margin: 4px 12px 14px;
    padding: 8px 10px;
    border: 1px solid var(--vscode-panel-border, #444);
    border-radius: 4px;
    font-size: 12px;
  }
  .edit-proposal .file-path {
    font-weight: 600;
    margin-bottom: 6px;
  }
  .edit-proposal pre {
    margin: 0 0 8px;
    white-space: pre-wrap;
    font-family: var(--vscode-editor-font-family, monospace);
  }
  .edit-proposal .diff-line { display: block; padding: 0 4px; }
  .edit-proposal .diff-line.removed {
    color: var(--vscode-gitDecoration-deletedResourceForeground, #f14c4c);
    background: rgba(241, 76, 76, 0.08);
  }
  .edit-proposal .diff-line.added {
    color: var(--vscode-gitDecoration-addedResourceForeground, #2ea043);
    background: rgba(46, 160, 67, 0.08);
  }
  .edit-proposal .more-note {
    opacity: 0.7;
    font-style: italic;
    margin-bottom: 8px;
  }
  .edit-proposal .actions { display: flex; gap: 6px; }
  .edit-proposal .result {
    margin-top: 6px;
    font-style: italic;
  }
  .edit-proposal .result.ok { color: var(--vscode-gitDecoration-addedResourceForeground, #2ea043); }
  .edit-proposal .result.refused { color: var(--vscode-errorForeground); }
</style>
</head>
<body>
  <div id="transcript"></div>
  <div id="grounding"></div>
  <div id="inputRow">
    <input id="promptInput" type="text" placeholder="Ask something…" autofocus />
    <button id="sendBtn">Send</button>
  </div>
  <script nonce="${nonce}" src="${scriptUri}"></script>
</body>
</html>`;
  }
}

// workspacePath sends the first open VS Code workspace folder as
// PromptRequest.Workspace -- purely advisory (see protocol.go), lets the
// daemon report a mismatch if this doesn't match its own --workspace.
function workspacePath(): string {
  return vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? '';
}

function getNonce(): string {
  let text = '';
  const possible = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
  for (let i = 0; i < 32; i++) {
    text += possible.charAt(Math.floor(Math.random() * possible.length));
  }
  return text;
}
