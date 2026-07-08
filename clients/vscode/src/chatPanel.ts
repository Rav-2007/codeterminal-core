import * as vscode from 'vscode';

import { EditBlockWire, GroundingInfo, Turn, applyEdit, preflightHandshake, streamPrompt, undoEdits } from './daemonClient';

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

  // Sequential edit-review state: set from the most recent response's
  // edit_proposals (the FULL list -- the daemon already sends all parsed
  // blocks, see onEditProposals below), reviewed one block at a time,
  // mirroring reviewBlocks/reviewIndex/reviewBackupDir in
  // clients/tui/chat.go. currentIndex only ever advances forward; there is
  // no pre-filtering of gate-refused blocks (unlike the TUI) -- a refusal
  // is discovered when the user clicks Apply for that block (see
  // onApplyEdit), not before it's shown. All of this resets on a new
  // prompt, same lifecycle pendingEdit used to follow, so a lagging Apply/
  // Skip click from a stale review can never act on the wrong block.
  private pendingBlocks: EditBlockWire[] = [];
  private currentIndex = 0;
  private runBackupDir: string | undefined;
  private applied = 0;
  private skipped = 0;
  private refused = 0;
  private refusalReasons: string[] = [];
  private applyInFlight = false;
  private undoInFlight = false;

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

  private handleMessage(msg: { type: string; text?: string; backupDir?: string }): void {
    if (msg.type === 'prompt' && typeof msg.text === 'string') {
      this.onPrompt(msg.text);
    } else if (msg.type === 'applyEdit') {
      this.onApplyEdit();
    } else if (msg.type === 'skipEdit') {
      this.onSkipEdit();
    } else if (msg.type === 'undoEdit' && typeof msg.backupDir === 'string') {
      this.onUndoEdit(msg.backupDir);
    }
  }

  private onPrompt(text: string): void {
    if (this.inFlight) {
      return; // a turn is already in flight; the webview disables input while streaming
    }

    // A new turn makes any not-yet-finished review from the PREVIOUS answer
    // stale -- clear it so a lagging Apply/Skip click can never target the
    // wrong block. The webview already hid its panel locally on send; this
    // only guards the extension-host state onApplyEdit/onSkipEdit read from.
    this.clearPendingReview();

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
        this.startEditReview(proposals);
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

  // clearPendingReview resets all sequential edit-review state -- called on
  // a new prompt (a safety net; the webview already hides a stale panel
  // locally on send) and again once a review's final summary has been
  // posted, so a lagging click after the review ended is a no-op.
  private clearPendingReview(): void {
    this.pendingBlocks = [];
    this.currentIndex = 0;
    this.runBackupDir = undefined;
    this.applied = 0;
    this.skipped = 0;
    this.refused = 0;
    this.refusalReasons = [];
  }

  // startEditReview begins reviewing every block the daemon parsed out of
  // the just-completed response, one at a time -- mirroring
  // checkForEditBlocks/advanceReview in clients/tui/chat.go, except there is
  // no local pre-gating here: the daemon already sent the full, ungated
  // parse result (see editProposalsFromBlocks in daemon/server.go), and
  // each block is only ever validated when the user actually clicks Apply
  // for it (see onApplyEdit).
  private startEditReview(blocks: EditBlockWire[]): void {
    this.clearPendingReview();
    this.pendingBlocks = blocks;
    this.postCurrentBlockOrSummary();
  }

  // postCurrentBlockOrSummary sends the webview whatever comes next: the
  // not-yet-processed block at currentIndex, or, once every block has been
  // applied/skipped/refused, a one-time end-of-run summary -- mirroring
  // finishReview's system-turn text in clients/tui/chat.go.
  private postCurrentBlockOrSummary(): void {
    if (this.currentIndex < this.pendingBlocks.length) {
      this.panel.webview.postMessage({
        type: 'editProposal',
        edit: this.pendingBlocks[this.currentIndex],
        index: this.currentIndex,
        total: this.pendingBlocks.length,
      });
      return;
    }
    this.panel.webview.postMessage({
      type: 'editSummary',
      total: this.pendingBlocks.length,
      applied: this.applied,
      skipped: this.skipped,
      refused: this.refused,
      refusalReasons: this.refusalReasons,
      backupDir: this.runBackupDir,
    });
    this.clearPendingReview();
  }

  // onApplyEdit sends the current block to the daemon exactly as received
  // -- no re-parsing, no re-diffing. All safety gates (exact-match,
  // ambiguity-refuse, workspace confinement, secret-file refusal, syntax
  // gate) run daemon-side in editapply.PrepareEdit; this only relays the
  // verbatim result, success or refusal, back to the webview and advances
  // to the next block either way -- a gate refusal is discovered here, at
  // apply-click time, not pre-filtered before display (see the class-level
  // doc comment on pendingBlocks).
  //
  // runBackupDir is threaded through as ApplyEditRequest.BackupSessionDir
  // once the FIRST block in this run applies successfully, so every
  // subsequent block in the same run reuses that one backup session dir --
  // this is what lets `edits undo` revert the whole batch together.
  private async onApplyEdit(): Promise<void> {
    if (this.applyInFlight || this.currentIndex >= this.pendingBlocks.length) {
      return;
    }
    const edit = this.pendingBlocks[this.currentIndex];
    this.applyInFlight = true;
    try {
      const result = await applyEdit(CLIENT_NAME, workspacePath(), edit, this.runBackupDir);
      if (result.applied) {
        if (!this.runBackupDir) {
          this.runBackupDir = result.backup_dir;
        }
        this.applied++;
      } else {
        this.refused++;
        this.refusalReasons.push(`${edit.file_path}: ${result.error ?? 'refused'}`);
      }
      this.panel.webview.postMessage({
        type: 'applyResult',
        applied: result.applied,
        error: result.error,
        backupDir: result.backup_dir,
      });
    } catch (err) {
      const message = (err as Error).message;
      this.refused++;
      this.refusalReasons.push(`${edit.file_path}: ${message}`);
      this.panel.webview.postMessage({
        type: 'applyResult',
        applied: false,
        error: message,
      });
    } finally {
      this.applyInFlight = false;
      this.currentIndex++;
      this.postCurrentBlockOrSummary();
    }
  }

  // onSkipEdit skips the current block with no daemon call, then advances
  // -- same "no confirm prompt needed, just move on" shape as Apply's
  // advance, but nothing is sent over the wire.
  private onSkipEdit(): void {
    if (this.currentIndex >= this.pendingBlocks.length) {
      return;
    }
    this.skipped++;
    this.currentIndex++;
    this.postCurrentBlockOrSummary();
  }

  // onUndoEdit reverts a completed run's shared backup session by ID
  // (backupDir), triggering the exact same runUndoSession logic the CLI's
  // `edits undo` uses -- no restore logic lives here or anywhere else in
  // the extension. backupDir comes from the WEBVIEW, which already
  // received and rendered it in the run's editSummary message -- not from
  // this.runBackupDir, which postCurrentBlockOrSummary already clears via
  // clearPendingReview() the moment that summary is posted (see Phase 0's
  // finding on this). The webview is therefore the only reliable holder of
  // "which session to undo" by the time a user actually clicks the button.
  //
  // guarded (paths left untouched because they changed since the apply
  // run) is relayed to the webview verbatim -- this method never collapses
  // it into a bare success/failure so the webview can render an honest
  // partial-undo message rather than imply a full revert happened.
  private async onUndoEdit(backupDir: string): Promise<void> {
    if (this.undoInFlight) {
      return;
    }
    this.undoInFlight = true;
    try {
      const result = await undoEdits(CLIENT_NAME, workspacePath(), backupDir);
      this.panel.webview.postMessage({
        type: 'undoResult',
        restored: result.restored,
        guarded: result.guarded,
        error: result.error,
      });
    } catch (err) {
      this.panel.webview.postMessage({
        type: 'undoResult',
        restored: 0,
        error: (err as Error).message,
      });
    } finally {
      this.undoInFlight = false;
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
  .edit-proposal .edit-index {
    font-size: 11px;
    opacity: 0.6;
    margin-bottom: 2px;
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
  .edit-proposal .actions { display: flex; gap: 6px; }
  .edit-proposal .result {
    margin-top: 6px;
    font-style: italic;
  }
  .edit-proposal .result.ok { color: var(--vscode-gitDecoration-addedResourceForeground, #2ea043); }
  .edit-proposal .result.refused { color: var(--vscode-errorForeground); }
  .edit-summary {
    margin: 4px 12px 14px;
    padding: 8px 10px;
    border: 1px solid var(--vscode-panel-border, #444);
    border-radius: 4px;
    font-size: 12px;
  }
  .edit-summary .summary-line { font-weight: 600; margin-bottom: 4px; }
  .edit-summary .summary-refusal { color: var(--vscode-errorForeground); margin-bottom: 2px; }
  .edit-summary .summary-backup { opacity: 0.7; font-style: italic; margin-top: 4px; }
  .edit-summary .undo-btn { margin-top: 6px; }
  .edit-summary .summary-undo-result { margin-top: 6px; font-style: italic; }
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
