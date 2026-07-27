import * as vscode from 'vscode';

import {
  DAEMON_LAUNCH_COMMAND,
  Degradation,
  EditBlockWire,
  GroundingInfo,
  HistoryInfo,
  IncompleteInfo,
  Turn,
  applyEdit,
  preflightHandshake,
  searchConversations,
  streamPrompt,
  undoEdits,
} from './daemonClient';

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
  private searchInFlight = false;

  // currentRunAutoApply is captured ONCE, from the 'prompt' message's
  // autoApply field, at the moment a prompt is sent -- mirroring the
  // webview's own autoApplyEnabled, which is read at that same instant (see
  // send() in media/main.js). It is never re-read from the webview's live
  // toggle state after that point, so flipping the toggle mid-run cannot
  // retroactively change a run already in flight (Phase 0 C).
  private currentRunAutoApply = false;

  // autoApplyRunInFlight is true for exactly the duration of runAutoApply
  // (see below) -- i.e. from right after edit proposals arrive until every
  // block has been applied/refused and the summary posted. onEditProposals
  // fires (and startEditReview kicks off the auto loop) BEFORE onDone clears
  // this.inFlight, so without this separate flag a fast second prompt could
  // slip in while the auto loop is still running and clobber pendingBlocks/
  // currentIndex out from under it (clearPendingReview resets both). This
  // flag closes that window: onPrompt refuses a new turn until the previous
  // run's auto-apply loop has actually finished, not just until the model's
  // response finished streaming.
  private autoApplyRunInFlight = false;

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

  private handleMessage(msg: { type: string; text?: string; backupDir?: string; autoApply?: boolean }): void {
    if (msg.type === 'prompt' && typeof msg.text === 'string') {
      this.onPrompt(msg.text, msg.autoApply === true);
    } else if (msg.type === 'applyEdit') {
      this.onApplyEdit();
    } else if (msg.type === 'skipEdit') {
      this.onSkipEdit();
    } else if (msg.type === 'undoEdit' && typeof msg.backupDir === 'string') {
      this.onUndoEdit(msg.backupDir);
    } else if (msg.type === 'search' && typeof msg.text === 'string') {
      this.onSearch(msg.text);
    }
  }

  private onPrompt(text: string, autoApply: boolean): void {
    if (this.inFlight || this.autoApplyRunInFlight) {
      return; // a turn is already in flight, or a previous run's auto-apply loop hasn't finished yet
    }

    // Captured once, for this run only -- see the currentRunAutoApply field
    // doc comment for why this must not be re-read later.
    this.currentRunAutoApply = autoApply;

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
      // 'historyInfo', NOT 'history' -- 'history' is already the persisted-turn
      // hydration message (see runPreflight). This one carries HistoryInfo, whose
      // truncated flag the webview renders as a dropped-turns warning.
      onHistory: (info: HistoryInfo) => {
        this.panel.webview.postMessage({ type: 'historyInfo', info });
      },
      onRedactions: (kinds: string[]) => {
        this.panel.webview.postMessage({ type: 'redactions', kinds });
      },
      onReasoning: (text: string) => {
        this.panel.webview.postMessage({ type: 'reasoning', text });
      },
      onDegraded: (items: Degradation[]) => {
        this.panel.webview.postMessage({ type: 'degraded', items });
      },
      onProvider: (provider: string) => {
        this.panel.webview.postMessage({ type: 'provider', provider });
      },
      onToken: (token: string) => {
        answer += token;
        this.panel.webview.postMessage({ type: 'token', text: token });
      },
      onEditProposals: (proposals: EditBlockWire[]) => {
        this.startEditReview(proposals);
      },
      onIncomplete: (info: IncompleteInfo) => {
        this.panel.webview.postMessage({ type: 'incomplete', info });
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
  // for it (see onApplyEdit) -- or, in an auto-apply run, when runAutoApply
  // drives that same validation itself (see below).
  //
  // The initial postCurrentBlockOrSummary() call always posts block 0's
  // editProposal first, whether or not this run is auto -- this keeps the
  // message sequence identical in both modes (one editProposal per block,
  // always immediately before that block's applyResult); auto mode differs
  // only in WHO triggers the apply that follows it (runAutoApply calling
  // onApplyEdit itself, instead of waiting for a webview 'applyEdit'
  // message).
  private startEditReview(blocks: EditBlockWire[]): void {
    this.clearPendingReview();
    this.pendingBlocks = blocks;
    this.postCurrentBlockOrSummary();
    if (this.currentRunAutoApply) {
      void this.runAutoApply();
    }
  }

  // runAutoApply self-drives the SAME per-block apply path a manual click
  // would (onApplyEdit): apply the block at currentIndex, capture
  // runBackupDir on first success, tally applied/refused, post applyResult,
  // advance, post the next block's editProposal (or, once done, the
  // editSummary) -- all of that already lives in onApplyEdit/
  // postCurrentBlockOrSummary, so this loop reuses it completely rather
  // than reimplementing any part of it. Each iteration is awaited before
  // the next begins (no Promise.all/batching), which is what preserves
  // stale-edit safety: block i+1 is only ever applied against whatever is
  // actually on disk after block i's apply (or refusal) has already
  // happened, identical to the manual one-click-at-a-time property. There
  // is no artificial delay between iterations -- it fires as fast as each
  // daemon round trip allows.
  //
  // A gate refusal requires no special handling here: onApplyEdit already
  // records refused/refusalReasons and moves on regardless of who called
  // it, so a refused block is simply skipped over and the loop continues
  // to the next block -- fully auto, no fallback to a manual prompt.
  private async runAutoApply(): Promise<void> {
    this.autoApplyRunInFlight = true;
    try {
      while (this.currentIndex < this.pendingBlocks.length) {
        await this.onApplyEdit();
      }
    } finally {
      this.autoApplyRunInFlight = false;
    }
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

  // onSearch runs a lexical (FTS5) search over cross-session conversation
  // memory for the current workspace, entirely separate from the chat
  // transcript above -- searching never touches this.transcript or
  // interrupts an in-flight prompt/apply/undo, and a search in flight
  // doesn't block those either; searchInFlight only guards against a second
  // search racing the first. Results (or the empty-not-error / actual-error
  // outcome) are relayed to the webview verbatim, exactly as they arrived
  // over the wire -- no re-sorting (results already arrive bm25-ranked) and
  // no collapsing "no results" and "search failed" into the same message,
  // mirroring how onUndoEdit relays guarded/error without collapsing them.
  private async onSearch(query: string): Promise<void> {
    if (this.searchInFlight) {
      return;
    }
    this.searchInFlight = true;
    try {
      const result = await searchConversations(CLIENT_NAME, workspacePath(), query);
      this.panel.webview.postMessage({ type: 'searchResults', results: result.results, error: result.error });
    } catch (err) {
      this.panel.webview.postMessage({ type: 'searchResults', error: (err as Error).message });
    } finally {
      this.searchInFlight = false;
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
  #searchRow { display: flex; gap: 6px; padding: 8px 12px; border-bottom: 1px solid var(--vscode-panel-border, transparent); }
  #searchInput {
    flex: 1;
    background: var(--vscode-input-background);
    color: var(--vscode-input-foreground);
    border: 1px solid var(--vscode-input-border, transparent);
    padding: 6px 8px;
    font-family: inherit;
    font-size: inherit;
  }
  #searchResults { padding: 0 12px; }
  .search-result {
    margin-bottom: 10px;
    padding: 8px 10px;
    border: 1px solid var(--vscode-panel-border, #444);
    border-radius: 4px;
    font-size: 12px;
  }
  .search-result .search-result-meta { font-size: 11px; opacity: 0.6; margin-bottom: 4px; }
  .search-result .search-result-snippet { white-space: pre-wrap; line-height: 1.4; }
  .search-result mark {
    background: var(--vscode-editor-findMatchHighlightBackground, #ffd33d55);
    color: inherit;
  }
  .search-empty { padding: 4px 0 10px; font-size: 12px; opacity: 0.7; }
  .search-error { padding: 4px 0 10px; font-size: 12px; color: var(--vscode-errorForeground); }
  #transcript { flex: 1; overflow-y: auto; padding: 8px 12px; }
  /* Guidance, not conversation: dimmer than a turn so it never reads as a
     message, and gone the moment the transcript has real content. */
  .first-run { font-size: 12px; opacity: 0.75; line-height: 1.5; max-width: 60ch; }
  .first-run p { margin: 0 0 8px; }
  .first-run pre {
    margin: 0;
    padding: 6px 8px;
    white-space: pre-wrap;
    font-family: var(--vscode-editor-font-family, monospace);
    background: var(--vscode-textCodeBlock-background, #00000033);
    border: 1px solid var(--vscode-panel-border, #444);
  }
  .turn { margin-bottom: 14px; white-space: pre-wrap; line-height: 1.4; }
  .turn .role { display: block; font-size: 11px; opacity: 0.6; margin-bottom: 2px; }
  .turn.user .role { color: var(--vscode-textLink-foreground); }
  .turn.error { color: var(--vscode-errorForeground); }
  /* Thinking is commentary, not the answer: dim + italic, visibly a separate
     block above the reply, never styled like the answer it precedes. */
  .turn.reasoning { opacity: 0.6; font-style: italic; font-size: 12px; }
  .turn.reasoning .role { color: var(--vscode-descriptionForeground, inherit); }
  #historyNotice {
    font-size: 11px;
    padding: 0 12px 6px;
    color: var(--vscode-editorWarning-foreground, #cca700);
  }
  #historyNotice:empty { padding: 0; }
  /* A cut-off answer is a warning, not an error: the reply is partly valid, the
     user just needs to know it stopped early. Warning color, marked, persistent
     in scrollback -- distinct from both a normal turn and a red error. */
  .turn.incomplete-notice {
    color: var(--vscode-editorWarning-foreground, #cca700);
    font-size: 12px;
    font-style: italic;
  }
  #grounding { font-size: 11px; opacity: 0.7; padding: 0 12px 6px; min-height: 14px; }
  #redactions {
    font-size: 11px;
    padding: 0 12px 6px;
    color: var(--vscode-editorWarning-foreground, #cca700);
  }
  #redactions:empty { padding: 0; }
  #degraded {
    font-size: 11px;
    padding: 0 12px 6px;
    color: var(--vscode-editorWarning-foreground, #cca700);
  }
  #degraded:empty { padding: 0; }
  #degraded .item { display: block; }
  /* Provider is a plain fact, not a warning: neutral #grounding-style dimming,
     not the warning color #degraded/#redactions use. :empty removes its padding
     so an absent provider (the common case) occupies no space. */
  #provider { font-size: 11px; opacity: 0.7; padding: 0 12px 6px; }
  #provider:empty { padding: 0; }
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
  .edit-proposal .result.auto-pending { opacity: 0.65; }
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
  .auto-apply-toggle {
    font-weight: 600;
    border: 1px solid var(--vscode-panel-border, #444);
  }
  .auto-apply-toggle.off {
    background: transparent;
    color: var(--vscode-foreground);
    opacity: 0.7;
  }
  .auto-apply-toggle.on {
    background: var(--vscode-inputValidation-warningBackground, #7a5c00);
    color: var(--vscode-inputValidation-warningForeground, #fff);
    border-color: var(--vscode-inputValidation-warningBorder, #b89500);
    opacity: 1;
  }
</style>
</head>
<body>
  <div id="searchRow">
    <input id="searchInput" type="text" placeholder="Search past conversations…" />
    <button id="searchBtn">Search</button>
  </div>
  <div id="searchResults"></div>
  <!-- First-run text, rendered INSIDE the empty transcript: a new user's very
       first sight of this panel was a blank rectangle that never stated the one
       thing it cannot work without -- a separately launched daemon. Static
       markup, no state and no settings; main.js removes it as soon as a real
       conversation turn is added (restored history included). The command must
       stay identical to daemonClient.ts's DAEMON_LAUNCH_COMMAND, which is why
       it is interpolated from there rather than written out again. -->
  <div id="transcript">
    <div id="firstRun" class="first-run">
      <p>CodeTerminal answers questions about the code in your workspace, grounded in a local index, and can propose edits you apply from here.</p>
      <p><strong>It needs the CodeTerminal daemon already running</strong> — this panel talks to it over a local socket and does not start it for you.</p>
      <p>Start it in a terminal from the repo root, then send a prompt:</p>
      <pre>${DAEMON_LAUNCH_COMMAND}</pre>
    </div>
  </div>
  <div id="grounding"></div>
  <div id="historyNotice"></div>
  <div id="redactions"></div>
  <div id="degraded"></div>
  <div id="provider"></div>
  <div id="inputRow">
    <button id="autoApplyToggle" class="auto-apply-toggle off" title="When ON, proposed edits apply automatically without a per-edit confirmation"></button>
    <input id="promptInput" type="text" placeholder="Ask something…" />
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
