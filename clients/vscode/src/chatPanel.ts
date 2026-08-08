import * as vscode from 'vscode';
import { execFile } from 'child_process';
import { promisify } from 'util';

// fs and path used to be imported here solely to build executable paths out of
// the workspace. Both uses were the vulnerability; there are now zero callers,
// and leaving the imports would invite the next one.
import { DAEMON_BIN_ENV, resolveDaemonBin } from './daemonBinary';
import { runGitStatus } from './safeGit';

import {
  APPROVAL_DENY,
  RESTART_HINT,
  Degradation,
  EditBlockWire,
  GroundingInfo,
  HistoryInfo,
  IncompleteInfo,
  ToolActivity,
  ToolApprovalRequest,
  Turn,
  applyEdit,
  fetchAvailableTiers,
  preflightHandshake,
  searchConversations,
  streamPrompt,
  undoEdits,
} from './daemonClient';
import {
  formatInitChecklist,
  formatSlashHelp,
  parseModelCommand,
  parseSlash,
  steeredPrompt,
} from './slashCommands';

const execFileAsync = promisify(execFile);

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

  // preferredTier is the models.json tier name chosen via /model <name>.
  // Empty means default routing. Sent as PromptRequest.tier on every turn.
  private preferredTier = '';
  // lastGrounding is the most recent GroundingInfo for /context.
  private lastGrounding: GroundingInfo | undefined;

  // pendingApproval is the tool call the daemon is currently holding open,
  // together with the callback that answers it. Non-undefined ONLY while a turn
  // is suspended on a question, which is also the only time the webview has an
  // approval panel on screen.
  private pendingApproval: { callId: string; respond: (decision: string) => void } | undefined;

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

  // Kept as a field because /mcp-server needs it: the daemon it runs must be
  // resolved against OUR installation directory, never against the workspace.
  // See daemonBinary.ts for why that distinction is load-bearing.
  private readonly extensionPath: string;

  private constructor(panel: vscode.WebviewPanel, extensionUri: vscode.Uri) {
    this.panel = panel;
    this.extensionPath = extensionUri.fsPath;
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
      this.postModelTier();
    } catch (err) {
      this.panel.webview.postMessage({
        type: 'error',
        message: `Could not reach the daemon: ${(err as Error).message}`,
      });
    }
  }

  private postModelTier(): void {
    this.panel.webview.postMessage({
      type: 'modelTier',
      tier: this.preferredTier || 'default',
    });
  }

  private handleMessage(msg: {
    type: string;
    text?: string;
    backupDir?: string;
    autoApply?: boolean;
    mode?: string;
    decision?: string;
    callId?: string;
  }): void {
    if (msg.type === 'toolApprovalDecision' && typeof msg.decision === 'string') {
      this.onToolApprovalDecision(msg.callId, msg.decision);
    } else if (msg.type === 'prompt' && typeof msg.text === 'string') {
      this.onPrompt(msg.text, msg.autoApply === true, msg.mode);
    } else if (msg.type === 'applyEdit') {
      this.onApplyEdit();
    } else if (msg.type === 'skipEdit') {
      this.onSkipEdit();
    } else if (msg.type === 'undoEdit' && typeof msg.backupDir === 'string') {
      this.onUndoEdit(msg.backupDir);
    } else if (msg.type === 'search' && typeof msg.text === 'string') {
      this.onSearch(msg.text);
    } else if (msg.type === 'closePanel') {
      this.panel.dispose();
    } else if (msg.type === 'newChat') {
      this.onNewChat();
    }
  }

  private onNewChat(): void {
    this.inFlight?.abort();
    this.inFlight = undefined;
    this.clearPendingApproval();
    this.clearPendingReview();
    this.transcript = [];
    this.lastGrounding = undefined;
    this.panel.webview.postMessage({ type: 'clearTranscript' });
  }

  private onPrompt(text: string, autoApply: boolean, mode?: string): void {
    if (this.inFlight || this.autoApplyRunInFlight) {
      return; // a turn is already in flight, or a previous run's auto-apply loop hasn't finished yet
    }

    const model = parseModelCommand(text);
    if (model.ok) {
      void this.handleModelCommand(model.arg);
      return;
    }

    const slash = parseSlash(text);
    if (!slash.rawPassthrough) {
      if (slash.usageOnly && slash.def) {
        this.replyLocal(`usage: /${slash.def.name} <args…>`);
        return;
      }
      if (slash.def?.kind === 'local') {
        void this.handleLocalSlash(slash.def.name, slash.args);
        return;
      }
      if (slash.def?.kind === 'steered') {
        const wirePrompt = steeredPrompt(slash.def, slash.args);
        this.startModelTurn(text, wirePrompt, slash.def.promptKind, autoApply, mode);
        return;
      }
    }

    this.startModelTurn(text, text, undefined, autoApply, mode);
  }

  private replyLocal(reply: string): void {
    this.transcript.push({ role: 'assistant', content: reply });
    this.panel.webview.postMessage({ type: 'token', text: reply });
    this.panel.webview.postMessage({ type: 'done' });
  }

  private async handleModelCommand(arg: string): Promise<void> {
    if (arg === '' || arg === 'list') {
      try {
        const tiers = await fetchAvailableTiers(CLIENT_NAME);
        const current = this.preferredTier || '(default)';
        const lines = [
          'models (active only — /model <name> to select; /model clear to reset):',
          `current: ${current}`,
        ];
        for (const t of tiers) {
          if (!t.active) {
            continue;
          }
          const mark =
            t.name === this.preferredTier || (!this.preferredTier && t.name === 'primary')
              ? '* '
              : '  ';
          lines.push(`${mark}${t.name}  ${t.slug}`);
        }
        this.replyLocal(lines.join('\n'));
      } catch (err) {
        this.replyLocal(`could not list models: ${(err as Error).message}`);
      }
      return;
    }
    if (arg === 'clear' || arg === 'default') {
      this.preferredTier = '';
      this.postModelTier();
      this.replyLocal('model reset to default tier (models.json default_tier)');
      return;
    }
    try {
      const tiers = await fetchAvailableTiers(CLIENT_NAME);
      const found = tiers.find((t) => t.active && t.name === arg);
      if (!found) {
        this.replyLocal(`unknown model tier "${arg}" — try /model for the list`);
        return;
      }
      this.preferredTier = found.name;
      this.postModelTier();
      this.replyLocal(`model set to ${found.name} (${found.slug})`);
    } catch (err) {
      this.replyLocal(`could not select model: ${(err as Error).message}`);
    }
  }

  private async handleLocalSlash(name: string, args: string): Promise<void> {
    const ws = workspacePath();
    switch (name) {
      case 'help':
        this.replyLocal(formatSlashHelp());
        return;
      case 'clear':
        this.transcript = [];
        this.lastGrounding = undefined;
        this.panel.webview.postMessage({ type: 'clearTranscript' });
        this.replyLocal('transcript cleared');
        return;
      case 'compact': {
        const keep = 8;
        if (this.transcript.length > keep) {
          this.transcript = this.transcript.slice(-keep);
          this.replyLocal(`kept last ${keep} turns`);
        } else {
          this.replyLocal('transcript already compact');
        }
        return;
      }
      case 'context': {
        const tier = this.preferredTier || '(default)';
        let g = '(none this turn)';
        if (this.lastGrounding) {
          g = `chunks=${this.lastGrounding.chunks ?? 0} truncated=${!!this.lastGrounding.truncated} mismatch=${!!this.lastGrounding.workspace_mismatch}`;
        }
        this.replyLocal(`workspace: ${ws}\nmodel tier: ${tier}\ngrounding: ${g}`);
        return;
      }
      case 'git':
        this.replyLocal(await runGitStatus(ws));
        return;
      case 'init':
        this.replyLocal(formatInitChecklist(ws));
        return;
      case 'mcp-server':
        this.replyLocal(await runMCPServerList(ws, this.extensionPath));
        return;
      case 'search': {
        const q = args.toLowerCase();
        const hits: string[] = [];
        this.transcript.forEach((t, i) => {
          if (t.content.toLowerCase().includes(q)) {
            const snippet = t.content.length > 120 ? t.content.slice(0, 120) + '…' : t.content;
            hits.push(`${i + 1}. [${t.role}] ${snippet}`);
          }
        });
        this.replyLocal(hits.length === 0 ? 'no matching turns' : 'matches:\n' + hits.join('\n'));
        return;
      }
      case 'exit':
        this.panel.dispose();
        return;
      default:
        this.replyLocal('unknown local command');
    }
  }

  private startModelTurn(
    displayText: string,
    wirePrompt: string,
    promptKind: string | undefined,
    autoApply: boolean,
    mode?: string
  ): void {
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
    this.transcript.push({ role: 'user', content: displayText });

    const controller = new AbortController();
    this.inFlight = controller;
    let answer = '';

    streamPrompt(
      CLIENT_NAME,
      wirePrompt,
      workspacePath(),
      history,
      controller.signal,
      {
        onGrounding: (info: GroundingInfo) => {
          this.lastGrounding = info;
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
        onToolActivity: (activity: ToolActivity) => {
          this.panel.webview.postMessage({ type: 'toolActivity', activity });
        },
        // Providing this handler is what makes streamPrompt declare
        // CAP_TOOL_APPROVAL, and therefore what turns agent mode on for this
        // client -- see StreamHandlers.onToolApproval. The daemon suspends the
        // turn here, so the respond callback must eventually be called; it is
        // held until the webview reports what the user clicked.
        onToolApproval: (req: ToolApprovalRequest, respond: (decision: string) => void) => {
          this.pendingApproval = { callId: req.call_id, respond };
          this.panel.webview.postMessage({ type: 'toolApproval', request: req });
        },
        onDone: () => {
          this.transcript.push({ role: 'assistant', content: answer });
          this.inFlight = undefined;
          this.clearPendingApproval();
          this.panel.webview.postMessage({ type: 'done' });
        },
        onError: (err: Error) => {
          this.inFlight = undefined;
          this.clearPendingApproval();
          this.panel.webview.postMessage({ type: 'error', message: err.message });
        },
      },
      { promptKind, tier: this.preferredTier || undefined, mode }
    );
  }

  // onToolApprovalDecision relays what the user clicked back to the waiting
  // daemon.
  //
  // The call id is checked, not trusted. A click that arrives after the turn
  // moved on -- a double-click, a lagging renderer, a stale panel -- must not
  // answer whatever question happens to be pending NOW, which is the one way a
  // click on one prompt could authorise a call the user never saw. A mismatch
  // is dropped, and the daemon's own re-check of the id and argument digest is
  // the second lock behind this one.
  private onToolApprovalDecision(callId: string | undefined, decision: string): void {
    const pending = this.pendingApproval;
    if (!pending || (callId !== undefined && callId !== pending.callId)) {
      return;
    }
    this.pendingApproval = undefined;
    pending.respond(decision);
  }

  // clearPendingApproval denies anything still waiting when a turn ends.
  //
  // Nothing should be: the daemon does not send done while it is waiting for an
  // answer. It exists because the alternative to a stale pending approval is a
  // closure holding a dead socket that a later click would fire into, and
  // denying costs nothing when there is nothing left to deny.
  private clearPendingApproval(): void {
    const pending = this.pendingApproval;
    this.pendingApproval = undefined;
    pending?.respond(APPROVAL_DENY);
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
    const logoUri = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, 'media', 'logo.png'));
    const nonce = getNonce();

    return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src ${webview.cspSource} https: data:; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${nonce}';">
<title>CodeTerminal Chat</title>
<style>
${chatPanelStyles()}
</style>
</head>
<body>
${chatPanelBodyMarkup(logoUri.toString())}  <script nonce="${nonce}" src="${scriptUri}"></script>
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

export async function runMCPServerList(workspace: string, extensionPath: string): Promise<string> {
  const bin = resolveDaemonBin(extensionPath);
  if (!bin) {
    return (
      'codeterminal-daemon binary not found.\n' +
      'It ships inside this extension; a development checkout needs it built:\n' +
      '  (cd daemon && go build -o codeterminal-daemon .)\n' +
      `then put it on PATH, or set ${DAEMON_BIN_ENV} to its path, and retry /mcp-server`
    );
  }
  try {
    const args = ['mcp', 'list'];
    if (workspace) {
      args.push('--workspace', workspace);
    }
    // cwd is deliberately NOT the workspace. Nothing below resolves a relative
    // path, and leaving it unset keeps that true if someone later adds one.
    const { stdout, stderr } = await execFileAsync(bin, args, { cwd: extensionPath });
    const s = (stdout + stderr).trim();
    return s || 'no MCP servers configured (agent mode off or empty mcp.servers)';
  } catch (err) {
    const e = err as { message: string; stdout?: string; stderr?: string };
    return `mcp list failed: ${e.message}\n${(e.stdout || '') + (e.stderr || '')}`.trim();
  }
}

function getNonce(): string {
  let text = '';
  const possible = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
  for (let i = 0; i < 32; i++) {
    text += possible.charAt(Math.floor(Math.random() * possible.length));
  }
  return text;
}

export function chatPanelStyles(): string {
  return `  :root {
    --ct-bg: #fff8f9;
    --ct-surface: #ffffff;
    --ct-rose: #e8919a;
    --ct-rose-deep: #d4727d;
    --ct-rose-soft: #fce8eb;
    --ct-ink: #3d2c2e;
    --ct-muted: #8a6f73;
    --ct-border: #f0d6da;
    --ct-code-bg: #fff6f7;
    --ct-code-fg: #5b3a44;
    --ct-user-bg: #fff0f2;
    --ct-radius: 14px;
    --ct-composer-bg: #fff5f7;
    --ct-composer-border: #f0c8d0;
    --ct-composer-glow: rgba(232, 145, 154, 0.28);
    --ct-composer-text: #4a3438;
    --ct-composer-muted: #a88a90;
  }
  * { box-sizing: border-box; }
  body {
    font-family: "Segoe UI", "Helvetica Neue", sans-serif;
    color: var(--ct-ink);
    background: var(--ct-bg);
    margin: 0;
    display: flex;
    flex-direction: column;
    height: 100vh;
  }
  #appHeader {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 10px 14px;
    background: var(--ct-surface);
    border-bottom: 1px solid var(--ct-border);
  }
  #appHeader .brand {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: 700;
    font-size: 15px;
    letter-spacing: 0.01em;
    color: var(--ct-ink);
  }
  #appHeader .brand-mark {
    width: 26px;
    height: 26px;
    border-radius: 8px;
    background: linear-gradient(145deg, var(--ct-rose), var(--ct-rose-deep));
    color: #fff;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-size: 13px;
    box-shadow: 0 4px 10px rgba(212, 114, 125, 0.28);
  }
  #appHeader .brand-logo {
    width: 44px;
    height: 44px;
    object-fit: contain;
    transform: scale(2.5);
    transform-origin: center;
  }
  #appHeader .header-actions { display: flex; gap: 4px; }
  .icon-btn {
    width: 32px;
    height: 32px;
    border: none;
    border-radius: 8px;
    background: transparent;
    color: var(--ct-muted);
    cursor: pointer;
    font-size: 16px;
    line-height: 1;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .icon-btn:hover { background: var(--ct-rose-soft); color: var(--ct-rose-deep); }
  #searchRow {
    display: none;
    gap: 6px;
    padding: 8px 12px;
    border-bottom: 1px solid var(--ct-border);
    background: var(--ct-surface);
  }
  #searchRow.visible { display: flex; }
  #searchInput {
    flex: 1;
    background: var(--ct-bg);
    color: var(--ct-ink);
    border: 1px solid var(--ct-border);
    border-radius: 10px;
    padding: 8px 10px;
    font-family: inherit;
    font-size: 13px;
  }
  #searchBtn {
    background: var(--ct-rose);
    color: #fff;
    border: none;
    border-radius: 10px;
    padding: 8px 14px;
    cursor: pointer;
  }
  #searchResults { padding: 0 12px; }
  .search-result {
    margin-bottom: 10px;
    padding: 10px 12px;
    border: 1px solid var(--ct-border);
    border-radius: var(--ct-radius);
    background: var(--ct-surface);
    font-size: 12px;
  }
  .search-result .search-result-meta { font-size: 11px; color: var(--ct-muted); margin-bottom: 4px; }
  .search-result .search-result-snippet { white-space: pre-wrap; line-height: 1.4; }
  .search-result mark {
    background: #ffe0e4;
    color: inherit;
  }
  .search-empty { padding: 4px 0 10px; font-size: 12px; color: var(--ct-muted); }
  .search-error { padding: 4px 0 10px; font-size: 12px; color: #c0392b; }
  #transcript { flex: 1; overflow-y: auto; padding: 14px 16px; }
  .first-run {
    font-size: 13px;
    color: var(--ct-muted);
    line-height: 1.55;
    max-width: 62ch;
    background: var(--ct-surface);
    border: 1px solid var(--ct-border);
    border-radius: var(--ct-radius);
    padding: 14px 16px;
  }
  .first-run p { margin: 0 0 8px; color: var(--ct-ink); }
  .first-run p:last-child { margin-bottom: 0; }
  /* The recovery hint is secondary: it matters only if the common case fails,
     so it must not compete with the sentence telling the user to just type. */
  .first-run .first-run-quiet { color: var(--ct-muted); font-size: 12px; }
  .msg {
    display: flex;
    gap: 10px;
    margin-bottom: 16px;
    align-items: flex-start;
  }
  .msg .avatar {
    width: 28px;
    height: 28px;
    border-radius: 50%;
    flex-shrink: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 12px;
    font-weight: 700;
    color: #fff;
  }
  .msg.user .avatar { background: var(--ct-rose-soft); color: var(--ct-rose-deep); border: 1px solid var(--ct-border); }
  .msg.assistant .avatar { background: linear-gradient(145deg, var(--ct-rose), var(--ct-rose-deep)); color: #fff; }
  .msg.error .avatar { background: #c0392b; }
  .msg .card {
    flex: 1;
    min-width: 0;
    background: var(--ct-surface);
    border: 1px solid var(--ct-border);
    border-radius: var(--ct-radius);
    padding: 12px 14px;
    box-shadow: 0 1px 2px rgba(212, 114, 125, 0.06);
  }
  .msg.user .card { background: var(--ct-user-bg); }
  .msg .role {
    display: block;
    font-size: 11px;
    font-weight: 600;
    color: var(--ct-muted);
    margin-bottom: 6px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .msg .body { white-space: pre-wrap; line-height: 1.5; font-size: 13.5px; }
  .msg .body .md-p { margin: 0 0 8px; white-space: pre-wrap; }
  .msg .code-block {
    margin: 8px 0;
    border-radius: 10px;
    overflow: hidden;
    background: var(--ct-code-bg);
    color: var(--ct-code-fg);
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 12px;
  }
  .msg .code-block .code-lang {
    padding: 4px 10px;
    font-size: 10px;
    color: var(--ct-muted);
    background: #fff;
    border-bottom: 1px solid var(--ct-border);
  }
  .msg .code-block .code-row {
    display: flex;
    line-height: 1.45;
  }
  .msg .code-block .ln {
    width: 36px;
    flex-shrink: 0;
    text-align: right;
    padding: 0 8px;
    color: #c4a4ad;
    background: #fffafb;
    border-right: 1px solid var(--ct-border);
    user-select: none;
  }
  .msg .code-block .lc {
    flex: 1;
    padding-right: 10px;
    white-space: pre;
    overflow-x: auto;
  }
  .msg-footer {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 10px;
    padding-top: 8px;
    border-top: 1px solid var(--ct-border);
    font-size: 12px;
    color: var(--ct-muted);
  }
  .msg-footer button {
    background: transparent;
    border: 1px solid var(--ct-border);
    border-radius: 8px;
    color: var(--ct-muted);
    padding: 3px 8px;
    cursor: pointer;
    font-size: 12px;
  }
  .msg-footer button:hover { border-color: var(--ct-rose); color: var(--ct-rose-deep); }
  .msg-footer button.active { background: var(--ct-rose-soft); border-color: var(--ct-rose); color: var(--ct-rose-deep); }
  .msg-footer .helpful { margin-left: auto; }
  .turn.reasoning {
    opacity: 0.7;
    font-style: italic;
    font-size: 12px;
    margin: 0 0 8px 38px;
    padding: 8px 12px;
    background: var(--ct-rose-soft);
    border-radius: 10px;
    color: var(--ct-muted);
  }
  .turn.reasoning .role { color: var(--ct-muted); display: block; margin-bottom: 2px; font-style: normal; font-size: 11px; }
  .turn.incomplete-notice {
    color: #b8860b;
    font-size: 12px;
    font-style: italic;
    margin: 4px 0 12px 38px;
  }
  .turn.error-inline {
    color: #c0392b;
    margin-bottom: 14px;
    white-space: pre-wrap;
  }
  #historyNotice {
    font-size: 11px;
    padding: 0 12px 6px;
    color: #b8860b;
  }
  #historyNotice:empty { padding: 0; }
  #grounding { font-size: 11px; color: var(--ct-muted); padding: 0 12px 6px; min-height: 14px; }
  #redactions, #degraded {
    font-size: 11px;
    padding: 0 12px 6px;
    color: #b8860b;
  }
  #redactions:empty, #degraded:empty { padding: 0; }
  #degraded .item { display: block; }
  #provider { font-size: 11px; color: var(--ct-muted); padding: 0 12px 6px; }
  #provider:empty { padding: 0; }
  #composer {
    padding: 12px 14px 14px;
    background: transparent;
    border-top: none;
    position: relative;
  }
  #composerShell {
    position: relative;
    background: var(--ct-composer-bg);
    border: 1px solid var(--ct-composer-border);
    border-radius: 18px;
    box-shadow:
      0 1px 2px rgba(232, 145, 154, 0.08),
      0 8px 24px rgba(232, 145, 154, 0.12);
    overflow: hidden;
    transition: border-color 180ms ease, box-shadow 220ms ease;
  }
  #composerShell.focused {
    border-color: var(--ct-rose);
    box-shadow:
      0 0 0 3px rgba(232, 145, 154, 0.18),
      0 10px 28px rgba(232, 145, 154, 0.2);
  }
  #composerShell.sending {
    animation: composerPulse 900ms ease;
  }
  @keyframes composerPulse {
    0% { box-shadow: 0 0 0 2px rgba(232, 145, 154, 0.15); }
    45% { box-shadow: 0 0 0 4px rgba(232, 145, 154, 0.28), 0 8px 28px rgba(232, 145, 154, 0.25); }
    100% { box-shadow: 0 1px 2px rgba(232, 145, 154, 0.08), 0 8px 24px rgba(232, 145, 154, 0.12); }
  }
  #slashMenu {
    display: none;
    position: absolute;
    bottom: calc(100% + 8px);
    left: 0;
    right: 0;
    max-height: 220px;
    overflow-y: auto;
    background: #fff;
    color: var(--ct-ink);
    border: 1px solid var(--ct-border);
    border-radius: 14px;
    box-shadow: 0 12px 32px rgba(61, 44, 46, 0.12);
    z-index: 8;
  }
  #slashMenu.visible { display: block; }
  #slashMenu button {
    display: flex;
    width: 100%;
    text-align: left;
    background: transparent;
    color: inherit;
    border: 0;
    padding: 9px 12px;
    font: inherit;
    cursor: pointer;
    gap: 8px;
    align-items: baseline;
  }
  #slashMenu button:hover, #slashMenu button.active {
    background: var(--ct-rose-soft);
  }
  #slashMenu .slash-name {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    color: var(--ct-rose-deep);
  }
  #slashMenu .slash-summary { color: var(--ct-muted); font-size: 12px; }

  .composer-top {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 14px 14px 8px;
  }
  #promptInput {
    flex: 1;
    min-height: 52px;
    max-height: 160px;
    resize: none;
    background: transparent;
    color: var(--ct-composer-text);
    border: none;
    border-radius: 0;
    padding: 4px 0;
    font-family: inherit;
    font-size: 14px;
    line-height: 1.45;
    box-shadow: none;
  }
  #promptInput::placeholder { color: var(--ct-composer-muted); }
  #promptInput:focus {
    outline: none;
    border: none;
    box-shadow: none;
  }

  #sendBtn {
    width: 36px;
    height: 36px;
    margin-top: 2px;
    border-radius: 11px;
    background: var(--ct-rose);
    color: #fff;
    border: none;
    cursor: pointer;
    font-size: 16px;
    font-weight: 600;
    flex-shrink: 0;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    box-shadow: 0 4px 12px rgba(232, 145, 154, 0.4);
    transition: transform 120ms ease, background 120ms ease, box-shadow 160ms ease;
  }
  #sendBtn:hover:not(:disabled) {
    background: var(--ct-rose-deep);
    box-shadow: 0 6px 16px rgba(212, 114, 125, 0.45);
    transform: translateY(-1px);
  }
  #sendBtn:disabled { opacity: 0.4; cursor: default; box-shadow: none; transform: none; }

  .composer-bottom {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 6px 12px 12px;
  }
  .composer-left, .composer-right {
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
  .composer-right { margin-left: auto; gap: 6px; }

  .pill {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    height: 28px;
    padding: 0 10px;
    border-radius: 999px;
    border: 1px solid var(--ct-border);
    background: #fff;
    color: var(--ct-ink);
    font: inherit;
    font-size: 12px;
    cursor: pointer;
    white-space: nowrap;
    max-width: 140px;
  }
  .pill:hover {
    border-color: var(--ct-rose);
    background: var(--ct-rose-soft);
    color: var(--ct-rose-deep);
  }
  .pill .chev { opacity: 0.5; font-size: 10px; }
  .pill .bolt { color: inherit; display: flex; align-items: center; justify-content: center; margin-right: 2px; }
  .mode-svg { display: block; }
  #autoApplyToggle.on .bolt { color: #e6a317; }
  #autoApplyToggle.off .bolt { color: inherit; }

  #modelChip, #autoApplyToggle {
    /* pills */
  }
  #modelChip {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  #autoApplyToggle.mode-btn,
  #autoApplyToggle.pill {
    display: inline-flex;
    font-weight: 500;
  }
  #autoApplyToggle.off {
    background: #fff;
    color: var(--ct-ink);
    border-color: var(--ct-border);
  }
  #autoApplyToggle.on {
    background: var(--ct-rose-soft);
    color: var(--ct-rose-deep);
    border-color: var(--ct-rose);
  }

  #effortMeter {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    height: 28px;
    padding: 0 8px;
    border-radius: 999px;
    background: #fff;
    border: 1px solid var(--ct-border);
    cursor: pointer;
  }
  #effortMeter:hover { border-color: var(--ct-rose); background: var(--ct-rose-soft); }
  #effortMeter .effort-label {
    font-size: 10px;
    color: var(--ct-muted);
    margin-right: 2px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  #effortMeter .dot {
    width: 5px;
    height: 5px;
    border-radius: 50%;
    background: #e8d0d4;
    transition: transform 160ms ease, background 160ms ease, box-shadow 160ms ease;
  }
  #effortMeter .dot.on {
    background: #f0a0ac;
  }
  #effortMeter .dot.active {
    width: 9px;
    height: 9px;
    background: radial-gradient(circle at 35% 35%, #fff, #f5c0ca 45%, #e8919a 100%);
    box-shadow: 0 0 10px rgba(232, 145, 154, 0.75), 0 0 2px #fff;
    animation: effortGlow 1.8s ease-in-out infinite;
  }
  @keyframes effortGlow {
    0%, 100% { box-shadow: 0 0 8px rgba(232, 145, 154, 0.45), 0 0 1px #fff; }
    50% { box-shadow: 0 0 14px rgba(232, 145, 154, 0.85), 0 0 3px #fff; }
  }

  .composer-icon {
    width: 30px;
    height: 30px;
    border-radius: 8px;
    border: none;
    background: transparent;
    color: var(--ct-muted);
    cursor: pointer;
    font: inherit;
    font-size: 15px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .composer-icon:hover { background: var(--ct-rose-soft); color: var(--ct-rose-deep); }
  .composer-icon:disabled { opacity: 0.35; cursor: default; }

  .attach-list {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    padding: 0 12px 12px;
  }
  .attach-list[hidden] { display: none !important; }
  .attach-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    max-width: 100%;
    padding: 4px 8px 4px 10px;
    border-radius: 999px;
    border: 1px solid var(--ct-border);
    background: #fff;
    color: var(--ct-ink);
    font-size: 11px;
  }
  .attach-chip .attach-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 180px;
  }
  .attach-chip .attach-remove {
    width: 18px;
    height: 18px;
    border: none;
    border-radius: 50%;
    background: var(--ct-rose-soft);
    color: var(--ct-rose-deep);
    cursor: pointer;
    font-size: 12px;
    line-height: 1;
    padding: 0;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .attach-chip .attach-remove:hover { background: var(--ct-rose); color: #fff; }

  .context-btn {
    position: relative;
  }
  .context-ring {
    width: 18px;
    height: 18px;
    display: block;
  }
  .context-ring-track {
    stroke: var(--ct-border);
  }
  .context-ring-fill {
    stroke: var(--ct-rose);
    transition: stroke-dashoffset 200ms ease;
  }
  .context-btn.hot .context-ring-fill { stroke: var(--ct-rose-deep); }

  .context-popup {
    position: absolute;
    right: 12px;
    bottom: calc(100% + 10px);
    width: min(340px, calc(100% - 24px));
    background: #fff;
    border: 1px solid var(--ct-border);
    border-radius: 16px;
    box-shadow: 0 12px 36px rgba(212, 114, 125, 0.18);
    padding: 14px;
    z-index: 20;
    color: var(--ct-ink);
  }
  .context-popup[hidden] { display: none !important; }
  .context-popup-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 6px;
  }
  .context-popup-title {
    font-weight: 700;
    font-size: 14px;
  }
  .context-popup-close {
    width: 26px;
    height: 26px;
    border: none;
    border-radius: 8px;
    background: transparent;
    color: var(--ct-muted);
    cursor: pointer;
    font-size: 14px;
  }
  .context-popup-close:hover { background: var(--ct-rose-soft); color: var(--ct-rose-deep); }
  .context-popup-summary {
    display: flex;
    justify-content: space-between;
    gap: 8px;
    font-size: 12px;
    color: var(--ct-muted);
    margin-bottom: 10px;
  }
  .context-popup-summary #contextPct {
    color: var(--ct-ink);
    font-weight: 650;
  }
  .context-bar {
    display: flex;
    height: 8px;
    border-radius: 999px;
    overflow: hidden;
    background: #f3e8ea;
    margin-bottom: 12px;
  }
  .context-bar .seg {
    height: 100%;
    min-width: 0;
  }
  .context-rows {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .context-row {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 12.5px;
  }
  .context-row .swatch {
    width: 10px;
    height: 10px;
    border-radius: 3px;
    flex-shrink: 0;
  }
  .context-row .label { flex: 1; color: var(--ct-ink); }
  .context-row .count {
    color: var(--ct-muted);
    font-variant-numeric: tabular-nums;
  }
  .context-note {
    margin-top: 12px;
    font-size: 11px;
    color: var(--ct-muted);
    line-height: 1.4;
  }

  .modes-popup {
    position: absolute;
    right: 12px;
    bottom: calc(100% + 10px);
    width: min(340px, calc(100% - 24px));
    background: #fff;
    border: 1px solid var(--ct-border);
    border-radius: 16px;
    box-shadow: 0 12px 36px rgba(212, 114, 125, 0.18);
    padding: 8px;
    z-index: 20;
    color: var(--ct-ink);
  }
  .modes-popup[hidden] { display: none !important; }
  .modes-popup-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 6px 12px;
    margin-bottom: 4px;
  }
  .modes-popup-title {
    font-weight: 600;
    font-size: 11px;
    color: var(--ct-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .modes-list {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .mode-item {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    width: 100%;
    text-align: left;
    background: transparent;
    border: none;
    padding: 10px 12px;
    border-radius: 10px;
    cursor: pointer;
    color: inherit;
  }
  .mode-item:hover { background: #f9f5f6; }
  .mode-item[aria-selected="true"] { background: var(--ct-rose-soft); }
  .mode-icon {
    display: flex;
    align-items: center;
    justify-content: center;
    width: 16px;
    height: 16px;
    margin-top: 1px;
    flex-shrink: 0;
  }
  .mode-text { flex: 1; min-width: 0; }
  .mode-name {
    font-weight: 600;
    font-size: 13px;
    margin-bottom: 2px;
    color: var(--ct-ink);
  }
  .mode-desc {
    font-size: 11px;
    color: var(--ct-muted);
    line-height: 1.35;
  }
  .mode-item[aria-selected="true"] .mode-name { color: var(--ct-rose-deep); }
  .mode-check {
    font-size: 14px;
    color: var(--ct-rose-deep);
    opacity: 0;
    font-weight: bold;
    margin-top: 1px;
  }
  .mode-item[aria-selected="true"] .mode-check { opacity: 1; }

  .composer-mic, .composer-divider { display: none; }

  #toolbar { display: none; }
  .chip { display: none; }


  button {
    background: var(--ct-rose);
    color: #fff;
    border: none;
    padding: 6px 14px;
    border-radius: 8px;
    cursor: pointer;
  }
  button:disabled { opacity: 0.5; cursor: default; }
  .edit-proposal {
    margin: 4px 0 14px 38px;
    padding: 10px 12px;
    border: 1px solid var(--ct-border);
    border-radius: var(--ct-radius);
    background: var(--ct-surface);
    font-size: 12px;
  }
  .edit-proposal .edit-index { font-size: 11px; color: var(--ct-muted); margin-bottom: 2px; }
  .edit-proposal .file-path { font-weight: 600; margin-bottom: 6px; }
  .edit-proposal pre {
    margin: 0 0 8px;
    white-space: pre-wrap;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  }
  .edit-proposal .diff-line { display: block; padding: 0 4px; }
  .edit-proposal .diff-line.removed { color: #c0392b; background: rgba(192, 57, 43, 0.08); }
  .edit-proposal .diff-line.added { color: #1e7a3a; background: rgba(30, 122, 58, 0.08); }
  .edit-proposal .actions { display: flex; gap: 6px; }
  .edit-proposal .result { margin-top: 6px; font-style: italic; }
  .edit-proposal .result.ok { color: #1e7a3a; }
  .edit-proposal .result.refused { color: #c0392b; }
  .edit-proposal .result.auto-pending { opacity: 0.65; }
  .tool-approval {
    margin: 4px 0 14px 38px;
    padding: 12px;
    border: 2px solid #d4a017;
    border-radius: var(--ct-radius);
    background: var(--ct-surface);
    font-size: 12px;
  }
  .tool-approval .approval-heading { font-weight: 600; margin-bottom: 6px; }
  .tool-approval .approval-args-label { font-size: 11px; color: var(--ct-muted); }
  .tool-approval .approval-args {
    margin: 2px 0 8px;
    padding: 6px 8px;
    white-space: pre-wrap;
    word-break: break-all;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    background: var(--ct-bg);
    border-radius: 8px;
  }
  .tool-approval .approval-lane { margin-bottom: 8px; }
  .tool-approval .approval-lane.unconfined {
    color: #c0392b;
    font-weight: 600;
  }
  .tool-approval .approval-lane.confined { color: var(--ct-muted); }
  .tool-approval .approval-actions { display: flex; gap: 6px; flex-wrap: wrap; }
  .tool-approval .approval-btn:focus-visible {
    outline: 2px solid var(--ct-rose);
    outline-offset: 1px;
  }
  .tool-activity {
    margin: 2px 0 2px 38px;
    font-size: 11px;
    color: var(--ct-muted);
    font-style: italic;
  }
  .approval-record {
    margin: 2px 0 8px 38px;
    font-size: 11px;
    color: var(--ct-muted);
  }
  .edit-summary {
    margin: 4px 0 14px 38px;
    padding: 10px 12px;
    border: 1px solid var(--ct-border);
    border-radius: var(--ct-radius);
    background: var(--ct-surface);
    font-size: 12px;
  }
  .edit-summary .summary-line { font-weight: 600; margin-bottom: 4px; }
  .edit-summary .summary-refusal { color: #c0392b; margin-bottom: 2px; }
  .edit-summary .summary-backup { color: var(--ct-muted); font-style: italic; margin-top: 4px; }
  .edit-summary .undo-btn { margin-top: 6px; }
  .edit-summary .summary-undo-result { margin-top: 6px; font-style: italic; }
  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }`;
}

// chatPanelBodyMarkup is the panel's static body markup, extracted from getHtml
// so it can be asserted directly. getHtml needs a live webview (for cspSource
// and asWebviewUri) and a fresh nonce, neither of which a test can supply, and
// VS Code exposes no webview DOM to the test host -- so without this the
// accessibility structure below would be unassertable and free to rot.
//
// See accessibility.test.ts, which pins the roles and labels this returns.
export function chatPanelBodyMarkup(logoUri: string = ''): string {
  return `  <!-- ACCESSIBILITY. The webview had no aria-*, role= or tabindex anywhere: a
       screen-reader user could not follow a streaming answer (nothing announced
       it), could not read the Auto-apply toggle's state (it lived only in
       textContent), and could not operate the diff-approval flow -- Gate 4 of
       the safety pipeline, where the user authorizes writes to their own files,
       which presumed sight.

       Everything below is ADDITIVE: roles, labels and live regions only. Every
       renderer still writes through textContent and never innerHTML, so the CSP
       + no-dangerous-sinks posture the Phase 3 review verified is untouched. -->
  <header id="appHeader">
    <div class="brand">
      ${logoUri ? `<img src="${logoUri}" alt="Mochiii Logo" class="brand-logo" />` : `<span class="brand-mark" aria-hidden="true">✦</span>`}
      Mochiii
    </div>
    <div class="header-actions">
      <button type="button" id="newChatBtn" class="icon-btn" title="New chat" aria-label="New chat">＋</button>
      <button type="button" id="historyBtn" class="icon-btn" title="History / search" aria-label="History and search">◷</button>
    </div>
  </header>
  <div id="searchRow" role="search">
    <label for="searchInput" class="sr-only">Search past conversations</label>
    <input id="searchInput" type="text" placeholder="Search past conversations…" />
    <button id="searchBtn">Search</button>
  </div>
  <!-- Results arrive asynchronously, so they are announced. polite, not
       assertive: a search result should not interrupt a streaming answer. -->
  <div id="searchResults" role="region" aria-label="Search results" aria-live="polite"></div>
  <!-- First-run text, rendered INSIDE the empty transcript: a new user's very
       first sight of this panel was a blank rectangle that never said what it
       does. Static markup, no state and no settings; main.js removes it as soon
       as a real conversation turn is added (restored history included).

       THIS USED TO TELL THE USER TO START THE DAEMON BY HAND, in a <pre> block
       naming ./daemon/codeterminal-daemon. That was true when written and is
       now the opposite of the truth: the extension starts and supervises the
       daemon (daemonSupervisor.ts), so following the old instruction started a
       SECOND daemon which could only lose the bind and exit. The recovery hint
       is interpolated from daemonClient.ts's RESTART_HINT rather than written
       out again, so it cannot drift from the palette entry it names. -->
  <!-- role="log" + aria-live="polite" + aria-atomic="false" is the combination
       that makes a STREAMED answer followable: the reader announces each
       appended token as it arrives instead of re-reading the entire transcript
       on every mutation (which aria-atomic="true" would do, and which is
       unusable at streaming rates). -->
  <div id="transcript" role="log" aria-live="polite" aria-atomic="false" aria-label="Conversation transcript">
    <div id="firstRun" class="first-run">
      <p>Mochiii answers questions about the code in your workspace, grounded in a local index, and can propose edits you apply from here.</p>
      <p>The Mochiii daemon starts automatically and is shared with any other window open on this same folder. Just send a prompt.</p>
      <p class="first-run-quiet">First answer slow? It is loading the local embedding model. If it never arrives, ${RESTART_HINT}.</p>
    </div>
  </div>
  <div id="grounding" role="status" aria-live="polite" aria-label="Grounding"></div>
  <div id="historyNotice" role="status" aria-live="polite" aria-label="Conversation history notice"></div>
  <!-- Redaction and degradation strips carry information the user needs to
       decide whether to trust the answer (a secret was scrubbed; a subsystem is
       unavailable), so they are announced rather than merely displayed. -->
  <div id="redactions" role="status" aria-live="polite" aria-label="Redaction notice"></div>
  <div id="degraded" role="status" aria-live="polite" aria-label="Degraded functionality notice"></div>
  <div id="provider" role="status" aria-live="polite" aria-label="Serving provider"></div>
  <div id="composer">
    <div id="slashMenu" role="listbox" aria-label="Slash commands"></div>
    <div id="composerShell">
      <div class="composer-top">
        <label for="promptInput" class="sr-only">Ask a question about your code</label>
        <textarea id="promptInput" rows="2" placeholder="Ask Mochiii anything… or type /"></textarea>
        <button type="button" id="sendBtn" title="Send" aria-label="Send">✈</button>
      </div>
      <div class="composer-bottom">
        <div class="composer-left">
          <button type="button" class="pill" id="modelChip" title="Choose model (/model)" aria-label="Choose model">
            <span id="modelChipLabel">Model</span><span class="chev" aria-hidden="true">▾</span>
          </button>
          <div id="effortMeter" role="slider" aria-valuemin="1" aria-valuemax="5" aria-valuenow="3" aria-label="Effort level" title="Effort">
            <span class="effort-label">Effort</span>
            <span class="dot" data-level="1"></span>
            <span class="dot" data-level="2"></span>
            <span class="dot" data-level="3"></span>
            <span class="dot" data-level="4"></span>
            <span class="dot" data-level="5"></span>
          </div>
        </div>
        <div class="composer-right">
          <button type="button" class="composer-icon context-btn" id="contextBtn" title="Context usage" aria-label="Open context usage" aria-expanded="false" aria-controls="contextPopup">
            <svg class="context-ring" viewBox="0 0 24 24" aria-hidden="true">
              <circle class="context-ring-track" cx="12" cy="12" r="8" fill="none" stroke-width="2.5"/>
              <circle class="context-ring-fill" id="contextRingFill" cx="12" cy="12" r="8" fill="none" stroke-width="2.5"
                stroke-linecap="round" transform="rotate(-90 12 12)"
                stroke-dasharray="50.27" stroke-dashoffset="50.27"/>
            </svg>
          </button>
          <input type="file" id="fileInput" multiple hidden
            accept=".txt,.md,.json,.js,.ts,.tsx,.jsx,.py,.go,.rs,.java,.c,.h,.cpp,.hpp,.css,.html,.xml,.yaml,.yml,.toml,.sh,.sql,.csv,.log,.env.example,image/*,.png,.jpg,.jpeg,.webp,.gif" />
          <button type="button" class="composer-icon" id="attachBtn" title="Attach files" aria-label="Attach files">📎</button>
          <button id="autoApplyToggle" class="pill mode-btn on" aria-haspopup="listbox" aria-expanded="false"
            aria-label="Select mode"
            title="Select agent mode (Manual, Plan, Auto)">
            <span class="bolt" aria-hidden="true" id="modeBtnIcon"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="mode-svg auto-icon"><path d="m21.64 3.64-1.28-1.28a1.21 1.21 0 0 0-1.72 0L2.36 18.64a1.21 1.21 0 0 0 0 1.72l1.28 1.28a1.2 1.2 0 0 0 1.72 0L21.64 5.36a1.2 1.2 0 0 0 0-1.72Z"/><path d="m14 7 3 3"/><path d="M5 6v4"/><path d="M19 14v4"/><path d="M10 2v2"/><path d="M7 8H3"/><path d="M21 16h-4"/><path d="M11 3H9"/></svg></span><span id="modeBtnLabel">Auto</span>
          </button>
        </div>
      </div>
      <div id="attachList" class="attach-list" hidden></div>
    </div>
    <div id="contextPopup" class="context-popup" hidden role="dialog" aria-label="Context usage">
      <div class="context-popup-head">
        <div class="context-popup-title">Context Usage</div>
        <button type="button" class="context-popup-close" id="contextPopupClose" aria-label="Close context usage">✕</button>
      </div>
      <div class="context-popup-summary">
        <span id="contextPct">0% Full</span>
        <span id="contextTotals">~0 / 128K Tokens</span>
      </div>
      <div class="context-bar" id="contextBar" aria-hidden="true"></div>
      <div class="context-rows" id="contextRows"></div>
      <div class="context-note" id="contextNote">Estimates from this chat panel (chars÷4). Not exact provider billing.</div>
    </div>
    <div id="modesPopup" class="modes-popup" hidden role="dialog" aria-label="Select mode">
      <div class="modes-popup-head">
        <div class="modes-popup-title">Modes</div>
      </div>
      <div class="modes-list" id="modesList" role="listbox">
        <button type="button" class="mode-item" role="option" aria-selected="false" data-mode="manual">
          <div class="mode-icon" aria-hidden="true"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="mode-svg manual-icon"><path d="M12 19l7-7 3 3-7 7-3-3z"/><path d="M18 13l-1.5-7.5L2 2l3.5 14.5L13 18l5-5z"/><path d="M2 2l7.586 7.586"/><circle cx="11" cy="11" r="2"/><path d="M4 22h16"/></svg></div>
          <div class="mode-text">
            <div class="mode-name">Manual</div>
            <div class="mode-desc">Mochiii will ask for approval before making each edit</div>
          </div>
          <div class="mode-check" aria-hidden="true">✓</div>
        </button>
        <button type="button" class="mode-item" role="option" aria-selected="false" data-mode="plan">
          <div class="mode-icon" aria-hidden="true"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="mode-svg plan-icon"><rect width="8" height="4" x="8" y="2" rx="1" ry="1"/><path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/><path d="m7 11 2 2 3-3"/><path d="M14 11h3"/><path d="m7 16 2 2 3-3"/><path d="M14 16h3"/></svg></div>
          <div class="mode-text">
            <div class="mode-name">Plan</div>
            <div class="mode-desc">Mochiii will explore the code and present a plan before editing</div>
          </div>
          <div class="mode-check" aria-hidden="true">✓</div>
        </button>
        <button type="button" class="mode-item" role="option" aria-selected="true" data-mode="auto">
          <div class="mode-icon" aria-hidden="true"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="mode-svg auto-icon"><path d="m21.64 3.64-1.28-1.28a1.21 1.21 0 0 0-1.72 0L2.36 18.64a1.21 1.21 0 0 0 0 1.72l1.28 1.28a1.2 1.2 0 0 0 1.72 0L21.64 5.36a1.2 1.2 0 0 0 0-1.72Z"/><path d="m14 7 3 3"/><path d="M5 6v4"/><path d="M19 14v4"/><path d="M10 2v2"/><path d="M7 8H3"/><path d="M21 16h-4"/><path d="M11 3H9"/></svg></div>
          <div class="mode-text">
            <div class="mode-name">Auto</div>
            <div class="mode-desc">Mochiii will approve actions that pass a safety check</div>
          </div>
          <div class="mode-check" aria-hidden="true">✓</div>
        </button>
      </div>
    </div>
  </div>`;
}
