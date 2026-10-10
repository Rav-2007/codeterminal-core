import * as vscode from 'vscode';
import { AttachmentError, extractAttachmentFile } from './attachmentExtract';
import { resolveConfigPathSetting } from './daemonBinary';

// fs and path used to be imported here solely to build executable paths out of
// the workspace. Both uses were the vulnerability; there are now zero callers,
// and leaving the imports would invite the next one.
//
// child_process is gone for the same reason. Every local command that runs
// something now lives behind runLocalCommand, in a file that does not import
// vscode and can therefore be driven against a hostile workspace by
// src/test/suite/localCommandsHostile.test.ts. A spawn added back here would be
// outside that guard.
import { LocalCommandHost, runLocalCommand } from './localCommands';

import {
  APPROVAL_APPROVE_FOR_SPEC,
  APPROVAL_DENY,
  RESTART_HINT,
  ChatHistoryResponse,
  Degradation,
  EditBlockWire,
  EditRejectionWire,
  GroundingInfo,
  HistoryInfo,
  IncompleteInfo,
  ToolActivity,
  ToolApprovalRequest,
  Turn,
  applyEdit,
  fetchAvailableTiers,
  preflightSession,
  chatHistory,
  resetChat,
  streamPrompt,
  undoEdits,
} from './daemonClient';
import { parseModelCommand, parseSlash, parseTeamCommand, steeredPrompt } from './slashCommands';
import {
  SpecGrants,
  activatedText,
  appliedSpec,
  getActiveSpec,
  parseSpecCommand,
  setActiveSpec,
  specAction,
  specReportText,
  taskListText,
  workingCopyText,
} from './specWorkflow';

export class DiffContentProvider implements vscode.TextDocumentContentProvider {
  static scheme = 'mochiii-diff';
  private static contents = new Map<string, string>();

  static register(context: vscode.ExtensionContext) {
    context.subscriptions.push(
      vscode.workspace.registerTextDocumentContentProvider(this.scheme, new DiffContentProvider())
    );
  }

  static addContent(content: string, filename: string): vscode.Uri {
    const id = Math.random().toString(36).substring(2);
    this.contents.set(id, content);
    // Use filename in the path so the diff editor tab title looks better (e.g. Snippet.go)
    return vscode.Uri.parse(`${this.scheme}:${filename}?id=${id}`);
  }

  provideTextDocumentContent(uri: vscode.Uri): string {
    const id = uri.query.replace('id=', '');
    return DiffContentProvider.contents.get(id) || '';
  }
}

export const CLIENT_NAME = 'mochiii-vscode';
// The panel's view type: what VS Code records for a panel left open at exit,
// and what package.json's onWebviewPanel activation event and extension.ts's
// serializer are keyed on. All three must agree.
export const CHAT_VIEW_TYPE = 'mochiiiChat';

// How long the panel waits for the daemon to answer when it opens. Mochiii
// starts only when it is used (package.json activationEvents), so the command
// that opened this panel is usually what started the daemon a moment ago --
// and a panel VS Code restores after a restart always is.
const PREFLIGHT_WAIT_MS = 15_000;
const PREFLIGHT_RETRY_MS = 300;

// chatWebviewOptions is what the webview may do, for a new panel and for one
// VS Code restores: scripts on, and local files from media/ and nowhere else.
function chatWebviewOptions(extensionUri: vscode.Uri): vscode.WebviewOptions {
  return { enableScripts: true, localResourceRoots: [vscode.Uri.joinPath(extensionUri, 'media')] };
}

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
  // Ids for attachment chips, so a reply finds the chip it belongs to.
  private attachmentSeq = 0;
  // The composer's effort picker. '' is Auto: no reasoning_effort is sent and the
  // model uses its own default.
  private reasoningEffort: '' | 'low' | 'medium' | 'high' = '';
  private inFlight: AbortController | undefined;

  // preferredTier is the models.json tier name chosen via /model <name>.
  // Empty means default routing. Sent as PromptRequest.tier on every turn.
  private preferredTier = '';
  // The model id preferredTier (or the default tier) resolves to, for /context
  // and the switch confirmation; '' until the daemon has said.
  private currentModel = '';
  // The daemon's handshake said a prompt sent now would carry no key.
  private needsApiKey = false;

  // THE SHARED CHAT (daemon/chatsync.go). The workspace's chat is ONE
  // conversation, and the terminal client writes to it too; this panel used to
  // read it once, when it opened, so a question asked in the terminal never
  // reached this panel -- nor the history it sent next (FOUND 2026-10-08).
  // chatSync: the daemon keeps one (it gave a revision). chatRev: the revision
  // on screen, '' when not known, which the next catch-up answers by fetching
  // the chat whole. chatQuiet: that whole fetch says nothing, because the
  // change is this panel's own. resetPending: a "+" is on its way to the
  // daemon, and fetching meanwhile could bring the old chat back.
  private chatSync = false;
  private chatRev = '';
  private chatQuiet = false;
  private resetPending = false;
  private catchUpInFlight = false;
  // Set once the panel is closed, so work started before (the preflight's
  // wait for a starting daemon) stops instead of posting to a dead webview.
  private disposed = false;
  // lastGrounding is the most recent GroundingInfo for /context.
  private lastGrounding: GroundingInfo | undefined;

  // pendingApproval is the tool call the daemon is currently holding open,
  // together with the callback that answers it. Non-undefined ONLY while a turn
  // is suspended on a question, which is also the only time the webview has an
  // approval panel on screen.
  private pendingApproval:
    | {
        callId: string;
        respond: (decision: string) => void;
        // The spec grant this call offers, and the spec it would be held
        // under -- '' when it offers none (see onToolApprovalDecision).
        specGrant: string;
        spec: string;
        label: string;
      }
    | undefined;

  // specGrants are the commands approved "while this spec is active": in
  // memory, for one spec, and gone with the panel (specWorkflow.SpecGrants).
  private readonly specGrants = new SpecGrants();

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

  // turnMode is the latest turn's wire mode, and appliedPaths the files its
  // review applied -- so the spec a /spec turn wrote becomes active once the
  // user accepts it (specWorkflow.appliedSpec).
  private turnMode: string | undefined;
  private appliedPaths: string[] = [];

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

    const panel = vscode.window.createWebviewPanel(CHAT_VIEW_TYPE, 'Mochiii Chat', vscode.ViewColumn.Beside, {
      ...chatWebviewOptions(extensionUri),
      // KEPT ON PURPOSE, against the guideline's default. VS Code's webview
      // guide says this "has high memory overhead and should only be used when
      // other persistence techniques will not work" -- and here they will not.
      // Hidden without it, the webview is destroyed and every message posted
      // meanwhile is lost, and this panel can be in the middle of things that
      // cannot be rebuilt from saved state: a tool approval the daemon is HOLDING
      // OPEN until it is answered (dropping that panel stalls the turn until the
      // daemon's five-minute deadline denies it), an answer still streaming,
      // and an edit review one block in. Reloading from getState would show a
      // panel that looks ready while the daemon waits for a click it can no
      // longer receive. What a restart CAN rebuild -- the chat itself -- is
      // rebuilt from the daemon (revive, below).
      retainContextWhenHidden: true,
    });

    ChatPanel.current = new ChatPanel(panel, extensionUri);
  }

  // revive takes back a panel VS Code restored after a restart -- the one
  // that was open when the window closed -- through the serializer extension.ts
  // registers. Nothing needs carrying over from the old webview: the chat is
  // the daemon's, and the new panel hydrates from it like any panel that opens.
  static revive(panel: vscode.WebviewPanel, extensionUri: vscode.Uri): void {
    if (ChatPanel.current) {
      // One chat panel per window. A restored duplicate is closed, not kept.
      panel.dispose();
      ChatPanel.current.panel.reveal();
      return;
    }
    panel.webview.options = chatWebviewOptions(extensionUri);
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
    // Coming back to this panel -- from the terminal, another editor tab,
    // another app -- is when the other client may have added to the shared
    // chat. The webview reports its own focus too ('focused'), which covers
    // clicking in from VS Code's integrated terminal: neither event below fires
    // for that.
    this.panel.onDidChangeViewState(
      (e) => {
        if (e.webviewPanel.visible) {
          void this.catchUp();
        }
      },
      null,
      this.disposables,
    );
    vscode.window.onDidChangeWindowState(
      (state) => {
        if (state.focused && this.panel.visible) {
          void this.catchUp();
        }
      },
      null,
      this.disposables,
    );

    this.runPreflight();
  }

  // runPreflight hydrates persisted cross-session history into the panel
  // before the user ever types -- the ONLY place this ChatPanel reads
  // persisted_history. Subsequent per-prompt connections (streamPrompt)
  // never touch that field.
  //
  // It waits up to PREFLIGHT_WAIT_MS for a daemon that is still starting
  // before saying it could not reach one: opening this panel is usually what
  // started it.
  private async runPreflight(): Promise<void> {
    let session: Awaited<ReturnType<typeof preflightSession>>;
    const deadline = Date.now() + PREFLIGHT_WAIT_MS;
    for (;;) {
      try {
        session = await preflightSession(CLIENT_NAME);
        break;
      } catch (err) {
        if (this.disposed) {
          return;
        }
        if (Date.now() >= deadline) {
          this.panel.webview.postMessage({
            type: 'error',
            message: `Could not reach the daemon: ${(err as Error).message}`,
          });
          return;
        }
        await new Promise((r) => setTimeout(r, PREFLIGHT_RETRY_MS));
      }
    }
    if (this.disposed) {
      return;
    }
    this.needsApiKey = session.needsApiKey;
    this.chatSync = session.chatRevision !== '';
    this.chatRev = session.chatRevision;
    const persisted = turnsFromWire(session.turns);
    this.transcript = persisted;
    this.panel.webview.postMessage({ type: 'history', turns: persisted });
    this.postActiveSpec();
    this.postModelTier();
    // The header star shows whether the chat that was just restored is bookmarked.
    void this.onHistoryList('');
  }

  // knownModel: the caller already looked the tier up (applyTier), so there is
  // nothing to fetch. Otherwise the model is resolved from the daemon's list.
  private postModelTier(knownModel?: string): void {
    const tier = this.preferredTier || 'default';
    const post = (m: Record<string, unknown>): void => {
      try {
        void this.panel.webview.postMessage(m);
      } catch {
        // panel closed meanwhile
      }
    };
    if (knownModel) {
      this.currentModel = knownModel;
      post({ type: 'modelTier', tier, model: knownModel });
      return;
    }
    post({ type: 'modelTier', tier });
    // Then the model behind the tier, which is what the chip shows: "default"
    // alone told the user nothing about what was answering. Best effort -- with
    // no daemon yet the chip keeps the tier name.
    fetchAvailableTiers(CLIENT_NAME).then(
      (tiers) => {
        const current = this.preferredTier || 'default';
        if (current !== tier) {
          return; // the user picked another tier while this was in flight
        }
        const t = tiers.find((x) => (this.preferredTier ? x.name === this.preferredTier : x.default === true));
        if (t) {
          this.currentModel = t.slug;
          post({ type: 'modelTier', tier, model: t.slug });
        }
      },
      () => undefined,
    );
  }

  private handleMessage(msg: {
    type: string;
    text?: string;
    backupDir?: string;
    autoApply?: boolean;
    mode?: string;
    decision?: string;
    callId?: string;
    index?: number;
    remaining?: number;
    effort?: string;
    tier?: string;
    id?: string;
    on?: boolean;
  }): void {
    if (msg.type === 'pickAttachments' && typeof msg.remaining === 'number') {
      void this.onPickAttachments(msg.remaining);
    } else if (msg.type === 'listModels') {
      void this.onListModels();
    } else if (msg.type === 'selectModel' && typeof msg.tier === 'string') {
      void this.onSelectModel(msg.tier);
    } else if (msg.type === 'setEffort' && typeof msg.effort === 'string') {
      const e = msg.effort;
      this.reasoningEffort = e === 'low' || e === 'medium' || e === 'high' ? e : '';
    } else if (msg.type === 'toolApprovalDecision' && typeof msg.decision === 'string') {
      this.onToolApprovalDecision(msg.callId, msg.decision);
    } else if (msg.type === 'focused') {
      void this.catchUp();
    } else if (msg.type === 'prompt' && typeof msg.text === 'string') {
      this.onPrompt(msg.text, msg.autoApply === true, msg.mode);
    } else if (msg.type === 'applyEdit') {
      this.onApplyEdit();
    } else if (msg.type === 'skipEdit') {
      this.onSkipEdit();
    } else if (msg.type === 'undoEdit' && typeof msg.backupDir === 'string') {
      this.onUndoEdit(msg.backupDir);
    } else if (msg.type === 'viewDiff' && typeof msg.index === 'number') {
      this.onViewDiff(msg.index);
    } else if (msg.type === 'historyList') {
      void this.onHistoryList(typeof msg.text === 'string' ? msg.text : '');
    } else if (msg.type === 'historyResume' && typeof msg.id === 'string') {
      void this.onHistoryResume(msg.id);
    } else if (msg.type === 'historyBookmark' && typeof msg.id === 'string') {
      void this.onHistoryBookmark(msg.id, msg.on === true);
    } else if (msg.type === 'historyDelete' && typeof msg.id === 'string') {
      void this.onHistoryDelete(msg.id);
    } else if (msg.type === 'closePanel') {
      this.panel.dispose();
    } else if (msg.type === 'newChat') {
      void this.onNewChat();
    }
  }

  // The paperclip. The file dialog is the editor's, and the files are read here
  // from disk -- not by the webview, which has no Node and could only pass bytes
  // across the message channel as base64. Each file's text goes back to the
  // webview, which holds it until the prompt is sent.
  private async onPickAttachments(remaining: number): Promise<void> {
    const post = (m: Record<string, unknown>): void => {
      try {
        this.panel.webview.postMessage(m).then(undefined, () => undefined);
      } catch {
        // The panel was closed while a file was being read; nobody is waiting.
      }
    };
    if (remaining <= 0) {
      post({ type: 'attachmentNotice', text: 'No more files can be attached to this message.' });
      return;
    }
    const uris = await vscode.window.showOpenDialog({
      canSelectMany: true,
      openLabel: 'Attach',
      defaultUri: vscode.workspace.workspaceFolders?.[0]?.uri,
      filters: {
        'Documents, images, text and code': [
          'pdf', 'docx', 'xlsx', 'xlsm', 'pptx', 'png', 'jpg', 'jpeg', 'webp', 'gif', 'bmp',
          'txt', 'md', 'json', 'csv', 'log', 'js', 'ts', 'tsx', 'jsx', 'py', 'go', 'rs', 'java',
          'c', 'h', 'cpp', 'hpp', 'css', 'html', 'xml', 'yaml', 'yml', 'toml', 'sh', 'sql',
        ],
        'All files': ['*'],
      },
    });
    if (!uris || uris.length === 0) {
      return;
    }
    const files = uris.filter((u) => u.scheme === 'file').slice(0, remaining);
    if (uris.length > files.length) {
      post({ type: 'attachmentNotice', text: `Only ${files.length} of the ${uris.length} selected files were attached (at most 8 per message, local files only).` });
    }
    // Every chip appears at once; the files are then read one after another,
    // which is also the order OCR would serialise them in anyway.
    // The display name from the URI (always '/'-separated): this file keeps no
    // `path` import, per the note at its top; the file itself is opened by
    // extractAttachmentFile, which does not take a path from the workspace.
    const jobs = files.map((uri) => ({ uri, id: `att${++this.attachmentSeq}`, name: uri.path.slice(uri.path.lastIndexOf('/') + 1) }));
    for (const j of jobs) {
      post({ type: 'attachmentStarted', id: j.id, name: j.name });
    }
    for (const j of jobs) {
      try {
        const r = await extractAttachmentFile(j.uri.fsPath, (note) => post({ type: 'attachmentProgress', id: j.id, note }));
        post({ type: 'attachmentExtracted', id: j.id, name: j.name, kind: r.kind, text: r.text, note: r.note, desc: r.desc });
      } catch (err) {
        post({
          type: 'attachmentFailed',
          id: j.id,
          error: err instanceof AttachmentError ? err.message : `${j.name}: could not be read (${err instanceof Error ? err.message : String(err)}).`,
        });
      }
    }
  }

  // "+": keep the chat in history (auto-save), then start a new one on the
  // daemon too -- it used to clear only this panel, so the daemon went on
  // appending to one endless chat and the next window rehydrated all of it.
  private async onNewChat(): Promise<void> {
    this.inFlight?.abort();
    this.inFlight = undefined;
    this.clearPendingApproval();
    this.clearPendingReview();
    const saved = this.autoSave() ? await this.saveCurrentChat() : false;
    this.resetPending = true;
    try {
      // Level with the new, empty chat at once.
      this.chatRev = await resetChat(CLIENT_NAME);
      this.chatQuiet = false;
    } catch {
      // No daemon: the panel is cleared anyway; the next handshake starts fresh.
      // Where the chat stands is then unknown, and finding out is not news.
      this.chatRev = '';
      this.chatQuiet = true;
    } finally {
      this.resetPending = false;
    }
    this.transcript = [];
    this.lastGrounding = undefined;
    this.postSafe({ type: 'clearTranscript' });
    this.postSafe({ type: 'currentBookmark', on: false });
    if (saved) {
      this.postSafe({ type: 'info', text: 'New chat · the previous one is saved in History' });
    }
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

    // BEFORE parseSlash, exactly like /model above, because the catalog now
    // contains a 'team' entry and parseSlash would otherwise match it first and
    // route the turn as an ordinary steered command -- text on the wire, no
    // pipeline. parseSlash has a matching exclusion; both are needed, and
    // either alone is a silent regression.
    const team = parseTeamCommand(text);
    if (team.ok) {
      this.startModelTurn(text, team.prompt, undefined, autoApply, mode, team.pipeline);
      return;
    }

    // Also before parseSlash: /spec is local for some subcommands and a turn
    // (in its own mode) for others.
    const spec = parseSpecCommand(text);
    if (spec.ok) {
      void this.handleSpecCommand(spec.args, autoApply);
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
        this.startModelTurn(text, wirePrompt, slash.def.promptKind, autoApply, slash.def.mode ?? mode);
        return;
      }
    }

    this.startModelTurn(text, text, undefined, autoApply, mode);
  }

  private replyLocal(reply: string): void {
    this.transcript.push({ role: 'assistant', content: reply });
    // plain: command output is aligned text, not markdown. Rendered as markdown,
    // the "* " marking the current model in /model became a bullet point.
    this.panel.webview.postMessage({ type: 'token', text: reply, plain: true });
    this.panel.webview.postMessage({ type: 'done' });
  }

  // THE MODEL DROPDOWN. The chip used to send "/model" as a chat message, so a
  // click produced a reply in the transcript instead of a menu.
  private async onListModels(): Promise<void> {
    const post = (m: Record<string, unknown>): void => {
      try {
        this.panel.webview.postMessage(m).then(undefined, () => undefined);
      } catch {
        // panel closed meanwhile
      }
    };
    try {
      const tiers = await fetchAvailableTiers(CLIENT_NAME);
      post({
        type: 'modelMenu',
        current: this.preferredTier,
        tiers: tiers.map((t) => ({ name: t.name, slug: t.slug, active: t.active, isDefault: t.default === true })),
      });
    } catch (err) {
      post({ type: 'modelMenu', current: this.preferredTier, tiers: [], error: (err as Error).message });
    }
  }

  // Selecting from the dropdown: '' is the default. The same checks as /model.
  private async onSelectModel(tier: string): Promise<void> {
    const err = await this.applyTier(tier);
    // A switch made in the dropdown otherwise leaves no trace in the chat, so a
    // later look back cannot tell which model answered what. UI-only: this line
    // is not added to the transcript the model sees.
    const short = this.currentModel ? this.currentModel.slice(this.currentModel.lastIndexOf('/') + 1) : '';
    const text = err
      ? err
      : this.preferredTier
        ? `Model switched to ${short || this.preferredTier} · used from your next message`
        : `Model switched to Default${short ? ` (${short})` : ''} · used from your next message`;
    try {
      void this.panel.webview.postMessage({ type: err ? 'notice' : 'info', text });
    } catch {
      // panel closed meanwhile
    }
  }

  // Sets the tier for later turns, or returns why it cannot.
  private async applyTier(name: string): Promise<string | undefined> {
    const toDefault = name === '' || name === 'clear' || name === 'default';
    let tiers: Awaited<ReturnType<typeof fetchAvailableTiers>>;
    try {
      tiers = await fetchAvailableTiers(CLIENT_NAME);
    } catch (err) {
      if (toDefault) {
        // The default needs no checking; only its model name is unknown.
        this.preferredTier = '';
        this.currentModel = '';
        this.postModelTier();
        return undefined;
      }
      return `could not select model: ${(err as Error).message}`;
    }
    if (toDefault) {
      this.preferredTier = '';
      this.postModelTier(tiers.find((t) => t.default === true)?.slug);
      return undefined;
    }
    const found = tiers.find((t) => t.name === name);
    if (!found) {
      return `unknown model tier "${name}" — try /model for the list`;
    }
    if (!found.active) {
      return `tier "${name}" is currently inactive in models.json`;
    }
    this.preferredTier = found.name;
    this.postModelTier(found.slug);
    return undefined;
  }

  private async handleModelCommand(arg: string): Promise<void> {
    if (arg === '' || arg === 'list') {
      try {
        const tiers = await fetchAvailableTiers(CLIENT_NAME);
        const current = this.preferredTier || '(default)';
        const lines = [
          'models (/model <name> to select; /model clear to reset):',
          `current: ${current}`,
        ];
        for (const t of tiers) {
          const mark =
            t.name === this.preferredTier || (!this.preferredTier && t.default === true)
              ? '* '
              : '  ';
          const status = t.active ? '' : ' [inactive]';
          lines.push(`${mark}${t.name}  ${t.slug}${status}`);
        }
        this.replyLocal(lines.join('\n'));
      } catch (err) {
        this.replyLocal(`could not list models: ${(err as Error).message}`);
      }
      return;
    }
    if (arg === 'clear' || arg === 'default') {
      await this.applyTier('');
      this.replyLocal('model reset to default tier (models.json default_tier)');
      return;
    }
    const err = await this.applyTier(arg);
    this.replyLocal(err ?? `model set to ${this.currentModel || this.preferredTier}`);
  }

  // localHost adapts this panel to LocalCommandHost. The workspace is read here,
  // ONCE, and passed in -- rather than read inside the dispatcher from
  // module-level editor state, which is what used to make the local commands
  // impossible to test against a folder we control.
  private localHost(): LocalCommandHost {
    return {
      workspace: workspacePath(),
      extensionPath: this.extensionPath,
      // The machine-scoped config path the daemon was started with, so
      // /mcp-server reports that same config. Read here (this file imports
      // vscode) and resolved, never from the workspace. See localCommands.ts.
      configPath: resolveConfigPathSetting(vscode.workspace.getConfiguration('mochiii').get<string>('configPath')).path ?? '',
      transcript: this.transcript,
      preferredTier: this.preferredTier,
      currentModel: this.currentModel,
      lastGrounding: this.lastGrounding,
      replaceTranscript: (turns) => {
        this.transcript = turns;
      },
      forgetGrounding: () => {
        this.lastGrounding = undefined;
      },
      clearScreen: () => {
        void this.panel.webview.postMessage({ type: 'clearTranscript' });
      },
      close: () => this.panel.dispose(),
      // Delegates to the command that already owns this: it prompts with a
      // masked input box, stores the key in SecretStorage, and restarts the
      // daemon so the new key is actually in use. Reimplementing any of that
      // here would be a second credential path to keep correct.
      // The provider-aware flow in extension.ts (mochiii.connect): a masked
      // box, the daemon proves and stores the key and uses it at once.
      connectApiKey: async (base?: string) => {
        const r = await vscode.commands.executeCommand<{ message: string; inUse: boolean }>('mochiii.connect', { base });
        if (r?.inUse) {
          this.needsApiKey = false;
        }
        return r?.message ?? 'connect did not run.';
      },
      // Both return the sentence to print. The panel never reads or deletes a
      // credential itself -- it asks the extension, which owns SecretStorage.
      compactChat: () => this.compactChat(),
      showApiKey: async () => vscode.commands.executeCommand<string>('mochiii.showApiKey'),
      forgetApiKey: async () => vscode.commands.executeCommand<string>('mochiii.forgetApiKey'),
    };
  }

  private async handleSpecCommand(args: string, autoApply: boolean): Promise<void> {
    const root = workspacePath();
    const active = getActiveSpec(root);
    const action = specAction(root, active, args, this.specGrants.labelsFor(active));
    if (action.activate !== undefined) {
      if (action.activate !== active) {
        this.specGrants.clear(); // given under the old spec, not this one
      }
      await setActiveSpec(action.activate);
      this.postActiveSpec();
    }
    if (action.turn) {
      this.startModelTurn(action.turn.shown, action.turn.prompt, undefined, autoApply, action.turn.mode);
      return;
    }
    if (action.reply) {
      this.replyLocal(action.reply);
    }
  }

  // postActiveSpec tells the webview which spec every prompt now carries, for
  // the header chip.
  private postActiveSpec(): void {
    this.panel.webview.postMessage({ type: 'activeSpec', spec: getActiveSpec(workspacePath()) });
  }

  private async handleLocalSlash(name: string, args: string): Promise<void> {
    const reply = await runLocalCommand(this.localHost(), name, args);
    if (reply !== '') {
      this.replyLocal(reply);
    } else {
      // The webview locked the composer when the command was sent; a command
      // that answers with no reply (a successful /compact) must still unlock it.
      this.postSafe({ type: 'done' });
    }
    if (name === 'connect') {
      // A new provider brings its own models: the chip, and a selection that
      // named a model the new provider does not have, must follow.
      void this.refreshModelAfterConnect();
    }
  }

  private async refreshModelAfterConnect(): Promise<void> {
    try {
      const tiers = await fetchAvailableTiers(CLIENT_NAME);
      if (this.preferredTier && !tiers.some((t) => t.name === this.preferredTier)) {
        this.preferredTier = '';
      }
    } catch {
      // keep what is shown; the next turn will say if the daemon is gone
    }
    this.postModelTier();
  }

  // ASK FOR THE KEY WHEN A QUESTION NEEDS IT -- the terminal client's behaviour
  // (beginConnectForPrompt). The daemon said at the handshake that a prompt
  // would go out with no key; sending the question anyway would only turn it
  // into an authentication error. The connect box opens, and the question is
  // sent the moment a key is in use -- or handed back, unsent, if not.
  private async connectThenStart(run: () => void): Promise<void> {
    this.needsApiKey = false; // no second box while this one is open
    const r = await vscode.commands.executeCommand<{ message: string; inUse: boolean }>('mochiii.connect', {});
    if (r?.inUse) {
      void this.panel.webview.postMessage({ type: 'info', text: 'Key connected · sending your question' });
      void this.refreshModelAfterConnect();
      run();
      return;
    }
    this.needsApiKey = true;
    this.replyLocal(
      'Your question was not sent: Mochiii needs a model provider key first, and none is in use.\n\n' +
        (r?.message ?? '') +
        '\n\nRun /connect, then send the question again.',
    );
  }

  private startModelTurn(
    displayText: string,
    wirePrompt: string,
    promptKind: string | undefined,
    autoApply: boolean,
    mode?: string,
    pipeline?: string[]
  ): void {
    if (this.needsApiKey) {
      void this.connectThenStart(() => this.startModelTurn(displayText, wirePrompt, promptKind, autoApply, mode, pipeline));
      return;
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
    this.transcript.push({ role: 'user', content: displayText });

    const controller = new AbortController();
    this.inFlight = controller;
    this.turnMode = mode;
    let answer = '';
    // The reason slug for a cut-off answer, held here so onDone can attach it
    // to the transcript turn. The webview message onIncomplete posts is for the
    // user's eye and never comes back; this is the model's copy.
    let incompleteReason = '';

    const activeSpec = getActiveSpec(workspacePath());
    // The shared chat's revision this history was built from. Sent so the
    // daemon can answer from the stored chat if the terminal has added to it
    // since; '' when not known, which the daemon reads as "use what was sent".
    const sentRev = this.chatSync ? this.chatRev : '';
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
        // Fires independently of onEditProposals. A reply whose edits were ALL
        // malformed sends rejections and no proposals -- previously that reached
        // this panel as a completely ordinary answer, and the user watched a
        // reply that plainly contained edits produce nothing at all, with the
        // reason sitting in a daemon log they cannot see.
        onEditRejections: (rejections: EditRejectionWire[]) => {
          this.panel.webview.postMessage({
            type: 'editRejections',
            rejections,
          });
        },
        onIncomplete: (info: IncompleteInfo) => {
          incompleteReason = info.reason;
          this.panel.webview.postMessage({ type: 'incomplete', info });
        },
        onToolActivity: (activity: ToolActivity) => {
          this.panel.webview.postMessage({ type: 'toolActivity', activity });
        },
        // The spec workflow's reports, rendered as plain text by the webview.
        onWorkingCopy: (info) => {
          this.panel.webview.postMessage({ type: 'specNotice', kind: 'working-copy', text: workingCopyText(info) });
        },
        onSpecReport: (report) => {
          this.panel.webview.postMessage({ type: 'specNotice', kind: 'spec-report', text: specReportText(report) });
        },
        onTasks: (tasks) => {
          this.panel.webview.postMessage({ type: 'tasks', text: taskListText(tasks) });
        },
        // Providing this handler is what makes streamPrompt declare
        // CAP_TOOL_APPROVAL, and therefore what turns agent mode on for this
        // client -- see StreamHandlers.onToolApproval. The daemon suspends the
        // turn here, so the respond callback must eventually be called; it is
        // held until the webview reports what the user clicked.
        onToolApproval: (req: ToolApprovalRequest, respond: (decision: string) => void) => {
          // The spec answer is offered only where the daemon offered it AND a
          // spec is active to hold it; otherwise the webview never shows it.
          const spec = getActiveSpec(workspacePath());
          const specGrant = req.spec_grant && spec ? req.spec_grant : '';
          const label = `${req.server}__${req.tool} ${clip(req.arguments, 120)}`;
          this.pendingApproval = { callId: req.call_id, respond, specGrant, spec, label };
          this.panel.webview.postMessage({
            type: 'toolApproval',
            request: specGrant ? req : { ...req, spec_grant: undefined },
          });
        },
        // Level with the stored chat only if nobody else wrote to it first and
        // this turn was sent knowing where it stood; otherwise the chat is
        // fetched whole once the turn is over (onDone).
        onChatRevision: (revision: string, behind: boolean) => {
          this.chatRev = behind || !sentRev ? '' : revision;
        },
        onDone: () => {
          this.transcript.push(
            incompleteReason
              ? { role: 'assistant', content: answer, incomplete: incompleteReason }
              : { role: 'assistant', content: answer },
          );
          this.inFlight = undefined;
          this.clearPendingApproval();
          this.panel.webview.postMessage({ type: 'done' });
          if (this.chatSync && this.chatRev === '') {
            // Not under a review: catchUp waits for one to finish (canCatchUp).
            void this.catchUp();
          }
        },
        onError: (err: Error) => {
          // A stream that fails PARTWAY has already shown the user real text.
          // Dropping it here (which is what happened before) left the webview
          // displaying an answer the model would never see again, so a
          // follow-up like "expand on your second point" referred to something
          // absent from the conversation. Keeping it unmarked would be worse --
          // the model would read a reply that stops mid-sentence as a finished
          // one. So it is kept, and marked.
          //
          // Mirrors the TUI's streamErrMsg case (clients/tui/chat.go), and
          // 'provider_error' is protocol.IncompleteProviderError. onError and
          // onDone are mutually exclusive (daemonClient's `finished` guard), so
          // this cannot double-push.
          if (answer.trim()) {
            this.transcript.push({
              role: 'assistant',
              content: answer,
              incomplete: incompleteReason || 'provider_error',
            });
          }
          this.inFlight = undefined;
          this.clearPendingApproval();
          this.panel.webview.postMessage({ type: 'error', message: err.message });
        },
      },
      {
        promptKind,
        tier: this.preferredTier || undefined,
        mode,
        pipeline,
        spec: activeSpec || undefined,
        specGrants: this.specGrants.digestsFor(activeSpec),
        reasoningEffort: this.reasoningEffort || undefined,
        chatRevision: sentRev || undefined,
      }
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
    if (decision === APPROVAL_APPROVE_FOR_SPEC) {
      // Not offered: a message claiming this answer answers nothing -- the
      // same as a stray key in the TUI -- and the question stays open.
      if (!pending.specGrant) {
        return;
      }
      this.specGrants.remember(pending.spec, pending.specGrant, pending.label);
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
    this.appliedPaths = [];
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
    // A spec the user just accepted becomes the one they are working to.
    const accepted = appliedSpec(this.turnMode, this.appliedPaths);
    if (accepted) {
      void setActiveSpec(accepted).then(() => this.postActiveSpec());
      this.panel.webview.postMessage({ type: 'specNotice', kind: 'spec-active', text: activatedText(accepted) });
    }
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
        this.appliedPaths.push(edit.file_path);
      } else {
        this.refused++;
        this.refusalReasons.push(`${edit.file_path}: ${result.error ?? 'refused'}`);
      }
      this.panel.webview.postMessage({
        type: 'applyResult',
        applied: result.applied,
        error: result.error,
        backupDir: result.backup_dir,
        // What the gates learned, not what they decided. Absent on an older
        // daemon, which is why both are optional all the way down.
        syntaxNote: result.syntax_note,
        matchNote: result.match_note,
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

  private onViewDiff(index: number): void {
    if (index < 0 || index >= this.pendingBlocks.length) {
      return;
    }
    const edit = this.pendingBlocks[index];
    const filename = edit.file_path.split(/[/\\]/).pop() || 'Snippet';
    const originalUri = DiffContentProvider.addContent(edit.search, filename);
    const modifiedUri = DiffContentProvider.addContent(edit.replace, filename);
    vscode.commands.executeCommand('vscode.diff', originalUri, modifiedUri, `Proposed Edit: ${edit.file_path}`);
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

  // HISTORY, Claude Code style: every chat is kept (mochiii.history.autoSave,
  // on by default) and listed with its title and age; the search box matches
  // what was SAID, not only titles; bookmarked chats are pinned and never
  // pruned. The chats live in the daemon's archive (daemon/chatarchive.go),
  // shared with the terminal's /history.
  //
  // AUTO-SAVE REVERSES A RECORDED RULE, BY THE USER'S CHOICE (2026-10-06):
  // chatarchive.go writes only chats the user saves, "the owner's requirement
  // ... that disk use stays the user's choice". The setting is that choice, and
  // the archive's bounds (50 chats, 20 MB per workspace, bookmarks exempt) hold.
  private autoSave(): boolean {
    return vscode.workspace.getConfiguration('mochiii').get<boolean>('history.autoSave', true) !== false;
  }

  private postSafe(m: Record<string, unknown>): void {
    try {
      void this.panel.webview.postMessage(m);
    } catch {
      // panel closed meanwhile
    }
  }

  // Saves the current chat (or updates its saved copy). "Nothing to save" --
  // an empty chat, or one with no answer yet -- is not a failure.
  private async saveCurrentChat(): Promise<boolean> {
    if (this.transcript.length === 0) {
      return false;
    }
    try {
      const r = await chatHistory(CLIENT_NAME, 'save');
      return !r.error && !!r.entry;
    } catch {
      return false;
    }
  }

  private async onHistoryList(query: string): Promise<void> {
    try {
      const r = await chatHistory(CLIENT_NAME, 'list', { query: query.trim() || undefined });
      this.postSafe({ type: 'historyEntries', entries: r.entries ?? [], query, error: r.error });
      const current = (r.entries ?? []).find((e) => e.current);
      if (!query.trim()) {
        this.postSafe({ type: 'currentBookmark', on: current?.bookmarked === true });
      }
    } catch (err) {
      this.postSafe({ type: 'historyEntries', entries: [], query, error: (err as Error).message });
    }
  }

  // Renders turns as the whole chat, replacing what is on screen.
  private showTurns(turns: Turn[], info: string): void {
    this.transcript = turns;
    this.lastGrounding = undefined;
    this.postSafe({ type: 'clearTranscript' });
    this.postSafe({ type: 'history', turns });
    if (info) {
      this.postSafe({ type: 'info', text: info });
    }
  }

  // canCatchUp: the transcript may be changed under the user now -- the daemon
  // keeps a shared chat, no "+" is on its way to it, and nothing on screen holds
  // the turn in progress (a stream, a review, an approval).
  private canCatchUp(): boolean {
    return (
      this.chatSync &&
      !this.resetPending &&
      !this.catchUpInFlight &&
      !this.inFlight &&
      !this.autoApplyRunInFlight &&
      this.pendingBlocks.length === 0 &&
      !this.pendingApproval
    );
  }

  // catchUp asks the daemon what the shared chat gained since this panel last
  // looked, and shows it. A failure is not worth interrupting anyone for: the
  // daemon being away is reported by the next thing that needs it.
  private async catchUp(): Promise<void> {
    if (!this.canCatchUp()) {
      return;
    }
    const since = this.chatRev;
    this.catchUpInFlight = true;
    let r: ChatHistoryResponse;
    try {
      r = await chatHistory(CLIENT_NAME, 'current', { since });
    } catch {
      return;
    } finally {
      this.catchUpInFlight = false;
    }
    // Dropped if the panel moved on while it was asked: an answer to an older
    // question would undo what happened since.
    if (r.error || since !== this.chatRev || !this.canCatchUp()) {
      return;
    }
    this.adoptChat(r);
  }

  // adoptChat applies an answer to 'current': what the chat gained is appended
  // below what is here, so this panel's own turns stay as they were shown; a
  // chat that was replaced (a new chat, a resume, a compact in the other
  // window) is shown whole.
  private adoptChat(r: ChatHistoryResponse): void {
    if (!r.chat_revision || r.chat_revision === this.chatRev) {
      return;
    }
    const quiet = this.chatQuiet;
    this.chatRev = r.chat_revision;
    this.chatQuiet = false;
    const turns = turnsFromWire(r.turns);
    if (r.append) {
      if (turns.length === 0) {
        return;
      }
      if (!quiet) {
        const n = turns.length;
        this.postSafe({ type: 'info', text: `${n} message${n === 1 ? '' : 's'} from another Mochiii window on this workspace` });
      }
      this.transcript.push(...turns);
      this.postSafe({ type: 'history', turns });
      return;
    }
    let note = '';
    if (!quiet) {
      note = turns.length === 0 ? 'This chat was cleared in another Mochiii window' : 'This chat was changed in another Mochiii window · showing it as it is now';
    }
    this.showTurns(turns, note);
    // The header star belongs to whichever chat this now is.
    void this.onHistoryList('');
  }

  private async onHistoryResume(id: string): Promise<void> {
    if (this.inFlight || this.autoApplyRunInFlight) {
      this.postSafe({ type: 'notice', text: 'Wait for the current answer to finish before opening another chat.' });
      return;
    }
    if (this.autoSave()) {
      await this.saveCurrentChat();
    }
    try {
      const r = await chatHistory(CLIENT_NAME, 'resume', { id });
      if (r.error || !r.turns) {
        this.postSafe({ type: 'notice', text: r.error || 'That chat could not be opened.' });
        return;
      }
      this.clearPendingApproval();
      this.clearPendingReview();
      this.showTurns(turnsFromWire(r.turns), `Opened “${r.entry?.title ?? 'chat'}” from history`);
      // The resumed chat IS the shared chat now, as of this revision.
      this.chatRev = r.chat_revision ?? '';
      this.chatQuiet = false;
      this.postSafe({ type: 'currentBookmark', on: r.entry?.bookmarked === true });
    } catch (err) {
      this.postSafe({ type: 'notice', text: `Could not open that chat: ${(err as Error).message}` });
    }
  }

  // id '' is the current chat, which the daemon saves first.
  private async onHistoryBookmark(id: string, on: boolean): Promise<void> {
    try {
      const r = await chatHistory(CLIENT_NAME, on ? 'bookmark' : 'unbookmark', { id: id || undefined });
      if (r.error) {
        this.postSafe({ type: 'notice', text: r.error });
        return;
      }
      if (!id || r.entry?.current) {
        this.postSafe({ type: 'currentBookmark', on });
      }
      this.postSafe({ type: 'historyChanged' });
    } catch (err) {
      this.postSafe({ type: 'notice', text: `Could not bookmark that chat: ${(err as Error).message}` });
    }
  }

  private async onHistoryDelete(id: string): Promise<void> {
    const DELETE = 'Delete';
    const pick = await vscode.window.showWarningMessage('Delete this chat from history? This cannot be undone.', { modal: true }, DELETE);
    if (pick !== DELETE) {
      return;
    }
    try {
      const r = await chatHistory(CLIENT_NAME, 'delete', { id });
      if (r.error) {
        this.postSafe({ type: 'notice', text: r.error });
      }
      this.postSafe({ type: 'historyChanged' });
    } catch (err) {
      this.postSafe({ type: 'notice', text: `Could not delete that chat: ${(err as Error).message}` });
    }
  }

  // /compact: the daemon summarises the older turns with one model call, keeps
  // the recent ones, and saves the whole chat to history first.
  private async compactChat(): Promise<string> {
    if (this.transcript.length === 0) {
      return 'Nothing to compact: this chat is empty.';
    }
    this.postSafe({ type: 'info', text: 'Compacting: summarising the earlier part of this chat…' });
    try {
      const r = await chatHistory(CLIENT_NAME, 'compact', this.preferredTier ? { tier: this.preferredTier } : {});
      if (r.error || !r.turns) {
        return r.error ? r.error.charAt(0).toUpperCase() + r.error.slice(1) + '.' : 'compact did not return a chat.';
      }
      this.showTurns(
        turnsFromWire(r.turns),
        `Conversation compacted · ${r.compacted ?? 0} earlier messages summarised · the full chat is saved in History`,
      );
      this.chatRev = r.chat_revision ?? '';
      this.chatQuiet = false;
      this.postSafe({ type: 'currentBookmark', on: false });
      return '';
    } catch (err) {
      return `Could not compact: ${(err as Error).message}`;
    }
  }

  private dispose(): void {
    // Aborting the panel aborts the in-flight connection -- no orphaned
    // sockets left reading a dead webview.
    this.disposed = true;
    this.inFlight?.abort();
    if (ChatPanel.current === this) {
      ChatPanel.current = undefined;
    }
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
<title>Mochiii Chat</title>
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
    /* TWO COLOURS, TWO JOBS. Blue is everything you act on: buttons, focus,
       selection, links, the effort and context indicators, your own messages.
       Rose is the brand: the lotus, the header mark, Mochiii's avatar. Every
       surface comes from the VS Code theme, so a dark theme gets dark controls --
       hard-coded #fff here is what made the pills glare as white blobs. */
    --ct-bg: var(--vscode-editor-background, #f7f9fc);
    --ct-surface: var(--vscode-sideBar-background, #ffffff);
    --ct-rose: #e8919a;
    --ct-rose-deep: #d4727d;
    --ct-brand-gradient: linear-gradient(145deg, #e8919a, #d4727d);
    --ct-accent: #2563eb;
    --ct-accent-strong: #1d4ed8;
    --ct-accent-solid: #2563eb;
    --ct-accent-solid-hover: #1d4ed8;
    --ct-on-accent: #ffffff;
    --ct-accent-soft: color-mix(in srgb, var(--ct-accent) 13%, transparent);
    --ct-warn: #b45309;
    --ct-danger: #c0392b;
    --ct-ink: var(--vscode-editor-foreground, #1f2937);
    --ct-muted: var(--vscode-descriptionForeground, #5b6474);
    --ct-border: var(--vscode-widget-border, color-mix(in srgb, var(--ct-ink) 14%, transparent));
    --ct-control-bg: var(--vscode-input-background, #ffffff);
    --ct-control-border: color-mix(in srgb, var(--ct-ink) 20%, transparent);
    --ct-popover-bg: var(--vscode-editorWidget-background, #ffffff);
    --ct-shadow: 0 12px 32px color-mix(in srgb, #000 28%, transparent);
    --ct-code-bg: var(--vscode-textCodeBlock-background, var(--vscode-editorWidget-background, rgba(0, 0, 0, 0.04)));
    --ct-code-fg: var(--vscode-editorWidget-foreground, inherit);
    --ct-user-bg: color-mix(in srgb, var(--ct-accent) 9%, transparent);
    --ct-radius: 14px;
    --ct-composer-bg: var(--vscode-input-background, #ffffff);
    --ct-composer-border: color-mix(in srgb, var(--ct-ink) 18%, transparent);
    --ct-composer-glow: color-mix(in srgb, var(--ct-accent) 30%, transparent);
    --ct-composer-text: var(--vscode-input-foreground, inherit);
    --ct-composer-muted: var(--vscode-input-placeholderForeground, #8a93a6);
  }
  /* Dark and high-contrast themes: lighter blues, so text and rings keep
     WCAG contrast on a dark background (#5b9bff on #1e1e1e is about 6.4:1). */
  body.vscode-dark, body.vscode-high-contrast {
    --ct-accent: #5b9bff;
    --ct-accent-strong: #93bbff;
    --ct-accent-solid: #3574e8;
    --ct-accent-solid-hover: #4a86f0;
    --ct-warn: #f5a524;
    --ct-danger: #f26d6d;
  }
  /* The hidden attribute must win. A class that sets display (like .pill's
     inline-flex) otherwise overrides it, which is how an EMPTY spec chip showed
     as a blank circle in the composer. Two rules below already patched this one
     element at a time. */
  [hidden] { display: none !important; }
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
    background: var(--ct-brand-gradient);
    color: #fff;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    font-size: 13px;
    box-shadow: 0 4px 10px rgba(212, 114, 125, 0.28); /* brand */
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
  .icon-btn:hover { background: var(--ct-accent-soft); color: var(--ct-accent-strong); }
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
  .msg.user .avatar { background: var(--ct-accent-soft); color: var(--ct-accent-strong); border: 1px solid var(--ct-border); }
  .msg.assistant .avatar { background: var(--ct-brand-gradient); color: #fff; }
  .msg.error .avatar { background: #c0392b; }
  /* A one-line, neutral note in the transcript (a model switch). Not a bubble:
     it is about the conversation, not part of it. */
  .msg-info {
    display: flex;
    align-items: center;
    gap: 10px;
    margin: 4px 0 12px;
    color: var(--ct-muted);
    font-size: 11.5px;
  }
  .msg-info::before, .msg-info::after { content: ''; flex: 1; border-top: 1px solid var(--ct-border); }
  .msg-info .info-dot { width: 6px; height: 6px; border-radius: 50%; background: var(--ct-accent); flex-shrink: 0; }
  .msg .card {
    flex: 1;
    min-width: 0;
    background: var(--ct-surface);
    border: 1px solid var(--ct-border);
    border-radius: var(--ct-radius);
    padding: 12px 14px;
    box-shadow: 0 1px 2px color-mix(in srgb, var(--ct-accent-strong) 6%, transparent);
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
  /* Rendered markdown (renderMarkdown in media/main.js builds these nodes; it
     never sets innerHTML, so model output cannot inject markup). */
  .msg .body .md { white-space: normal; }
  .md > :first-child { margin-top: 0; }
  .md p { margin: 0 0 9px; white-space: pre-wrap; }
  .md h1, .md h2, .md h3, .md h4 { margin: 14px 0 6px; line-height: 1.3; color: var(--ct-ink); }
  .md h1 { font-size: 17px; }
  .md h2 { font-size: 15.5px; }
  .md h3 { font-size: 14.5px; }
  .md h4 { font-size: 13.5px; }
  .md h2, .md h3 { padding-bottom: 3px; border-bottom: 1px solid var(--ct-border); }
  .md ul, .md ol { margin: 4px 0 10px; padding-left: 22px; }
  .md li { margin: 2px 0; }
  .md li::marker { color: var(--ct-accent); }
  .md strong { font-weight: 650; }
  .md em { font-style: italic; }
  .md del { opacity: 0.7; }
  .md a { color: var(--ct-accent); text-decoration: none; border-bottom: 1px solid color-mix(in srgb, var(--ct-accent) 40%, transparent); }
  .md a:hover { border-bottom-color: var(--ct-accent); }
  .md code {
    font-family: var(--vscode-editor-font-family, monospace);
    font-size: 12.5px;
    padding: 1px 5px;
    border-radius: 5px;
    background: var(--ct-code-bg);
    border: 1px solid var(--ct-border);
  }
  .md blockquote {
    margin: 6px 0 10px;
    padding: 4px 10px;
    border-left: 3px solid var(--ct-accent);
    background: var(--ct-accent-soft);
    border-radius: 0 8px 8px 0;
  }
  .md hr { border: none; border-top: 1px solid var(--ct-border); margin: 12px 0; }
  .md .md-table-wrap { overflow-x: auto; margin: 6px 0 10px; }
  .md table { border-collapse: collapse; font-size: 12.5px; }
  .md th, .md td { border: 1px solid var(--ct-border); padding: 4px 8px; text-align: left; vertical-align: top; }
  .md th { background: var(--ct-accent-soft); font-weight: 600; }
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
    background: color-mix(in srgb, var(--ct-ink) 6%, var(--ct-code-bg));
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
  .msg-footer button:hover { border-color: var(--ct-accent); color: var(--ct-accent-strong); }
  .msg-footer button.active { background: var(--ct-accent-soft); border-color: var(--ct-accent); color: var(--ct-accent-strong); }
  .msg-footer .helpful { margin-left: auto; }
  .turn.reasoning {
    opacity: 0.7;
    font-style: italic;
    font-size: 12px;
    margin: 0 0 8px 38px;
    padding: 8px 12px;
    background: var(--ct-accent-soft);
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
      0 1px 2px color-mix(in srgb, var(--ct-accent) 8%, transparent),
      0 8px 24px color-mix(in srgb, var(--ct-accent) 12%, transparent);
    overflow: hidden;
    transition: border-color 180ms ease, box-shadow 220ms ease;
  }
  #composerShell.focused {
    border-color: var(--ct-accent);
    box-shadow:
      0 0 0 3px color-mix(in srgb, var(--ct-accent) 18%, transparent),
      0 10px 28px color-mix(in srgb, var(--ct-accent) 20%, transparent);
  }
  #composerShell.sending {
    animation: composerPulse 900ms ease;
  }
  @keyframes composerPulse {
    0% { box-shadow: 0 0 0 2px color-mix(in srgb, var(--ct-accent) 15%, transparent); }
    45% { box-shadow: 0 0 0 4px color-mix(in srgb, var(--ct-accent) 28%, transparent), 0 8px 28px color-mix(in srgb, var(--ct-accent) 25%, transparent); }
    100% { box-shadow: 0 1px 2px color-mix(in srgb, var(--ct-accent) 8%, transparent), 0 8px 24px color-mix(in srgb, var(--ct-accent) 12%, transparent); }
  }
  #slashMenu {
    display: none;
    position: absolute;
    bottom: calc(100% + 8px);
    left: 0;
    right: 0;
    max-height: 220px;
    overflow-y: auto;
    background: var(--ct-popover-bg);
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
    background: var(--ct-accent-soft);
  }
  #slashMenu .slash-name {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    color: var(--ct-accent-strong);
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
    background: var(--ct-accent-solid);
    color: var(--ct-on-accent);
    border: none;
    cursor: pointer;
    font-size: 16px;
    font-weight: 600;
    flex-shrink: 0;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    box-shadow: 0 4px 12px color-mix(in srgb, var(--ct-accent) 40%, transparent);
    transition: transform 120ms ease, background 120ms ease, box-shadow 160ms ease;
  }
  #sendBtn:hover:not(:disabled) {
    background: var(--ct-accent-solid-hover);
    box-shadow: 0 6px 16px color-mix(in srgb, var(--ct-accent-strong) 45%, transparent);
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
    border: 1px solid var(--ct-control-border);
    background: var(--ct-control-bg);
    color: var(--ct-ink);
    font: inherit;
    font-size: 12px;
    cursor: pointer;
    white-space: nowrap;
    max-width: 170px;
  }
  .pill:focus-visible, .composer-icon:focus-visible, #sendBtn:focus-visible {
    outline: 2px solid var(--ct-accent);
    outline-offset: 2px;
  }
  #modelChipLabel { overflow: hidden; text-overflow: ellipsis; }
  .pill:hover {
    border-color: var(--ct-accent);
    background: var(--ct-accent-soft);
    color: var(--ct-accent-strong);
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
    background: var(--ct-control-bg);
    color: var(--ct-ink);
    border-color: var(--ct-border);
  }
  #autoApplyToggle.on {
    background: var(--ct-accent-soft);
    color: var(--ct-accent-strong);
    border-color: var(--ct-accent);
  }

  /* Effort: one labelled pill that says what it is set to. It used to be five
     unlabelled dots that changed nothing -- the value was never sent. */
  .effort-pill { gap: 6px; }
  .effort-pill .effort-bars { display: block; flex-shrink: 0; }
  .effort-pill .effort-bars rect { fill: color-mix(in srgb, var(--ct-ink) 28%, transparent); transition: fill 140ms ease; }
  .effort-pill[data-level="1"] .effort-bars .b1,
  .effort-pill[data-level="2"] .effort-bars .b1, .effort-pill[data-level="2"] .effort-bars .b2,
  .effort-pill[data-level="3"] .effort-bars rect { fill: var(--ct-accent); }
  .effort-pill .effort-name { color: var(--ct-muted); }
  .effort-pill .effort-value { font-weight: 600; }
  .effort-pill:not([data-level="0"]) { border-color: color-mix(in srgb, var(--ct-accent) 55%, transparent); }
  .effort-pill:not([data-level="0"]) .effort-value { color: var(--ct-accent-strong); }

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
  .composer-icon:hover { background: var(--ct-accent-soft); color: var(--ct-accent-strong); }
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
    background: var(--ct-control-bg);
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
    background: var(--ct-accent-soft);
    color: var(--ct-accent-strong);
    cursor: pointer;
    font-size: 12px;
    line-height: 1;
    padding: 0;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .attach-chip .attach-remove:hover { background: var(--ct-accent); color: #fff; }

  /* Context usage: a ring that reads on any theme, plus the number. At 4% the
     old pale ring on a pale track was not visible at all. */
  .context-btn {
    position: relative;
    width: auto;
    gap: 4px;
    padding: 0 6px;
    color: var(--ct-muted);
    font-size: 11px;
    font-variant-numeric: tabular-nums;
  }
  .context-ring { width: 18px; height: 18px; display: block; }
  .context-ring-track { stroke: color-mix(in srgb, var(--ct-ink) 22%, transparent); }
  .context-ring-fill { stroke: var(--ct-accent); transition: stroke-dashoffset 200ms ease; }
  .context-btn.warn .context-ring-fill { stroke: var(--ct-warn); }
  .context-btn.warn .context-pct { color: var(--ct-warn); }
  .context-btn.hot .context-ring-fill { stroke: var(--ct-danger); }
  .context-btn.hot .context-pct { color: var(--ct-danger); font-weight: 600; }

  .context-popup {
    position: absolute;
    right: 12px;
    bottom: calc(100% + 10px);
    width: min(340px, calc(100% - 24px));
    background: var(--ct-popover-bg);
    border: 1px solid var(--ct-border);
    border-radius: 16px;
    box-shadow: var(--ct-shadow);
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
  .context-popup-close:hover { background: var(--ct-accent-soft); color: var(--ct-accent-strong); }
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
    background: var(--ct-popover-bg);
    border: 1px solid var(--ct-border);
    border-radius: 16px;
    box-shadow: var(--ct-shadow);
    padding: 8px;
    z-index: 20;
    color: var(--ct-ink);
  }
  .modes-popup[hidden] { display: none !important; }
  /* HISTORY PANEL (Claude Code style): under the header, a search box and the
     chats -- bookmarks first, then newest. */
  #appHeader { position: relative; }
  .history-popup {
    position: absolute;
    top: calc(100% + 4px);
    right: 12px;
    width: min(440px, calc(100% - 24px));
    max-height: min(480px, 70vh);
    display: flex;
    flex-direction: column;
    background: var(--ct-popover-bg);
    border: 1px solid var(--ct-border);
    border-radius: 14px;
    box-shadow: var(--ct-shadow);
    z-index: 30;
    overflow: hidden;
  }
  .history-head { padding: 10px; border-bottom: 1px solid var(--ct-border); }
  #searchInput {
    width: 100%;
    background: var(--ct-control-bg);
    color: var(--ct-ink);
    border: 1px solid var(--ct-control-border);
    border-radius: 9px;
    padding: 8px 10px;
    font: inherit;
    font-size: 13px;
  }
  #searchInput:focus { outline: 2px solid var(--ct-accent); outline-offset: -1px; border-color: transparent; }
  .history-list { overflow-y: auto; padding: 6px; }
  .history-section {
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--ct-muted);
    padding: 8px 8px 4px;
  }
  .history-row {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 8px;
    border: none;
    border-radius: 9px;
    background: transparent;
    color: var(--ct-ink);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .history-row:hover, .history-row:focus-visible { background: var(--ct-accent-soft); outline: none; }
  .history-row.current { box-shadow: inset 2px 0 0 var(--ct-accent); }
  .history-row .h-main { flex: 1; min-width: 0; }
  .history-row .h-title { display: block; font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .history-row .h-meta { display: block; font-size: 11px; color: var(--ct-muted); margin-top: 2px; }
  .history-row .h-age { font-size: 11px; color: var(--ct-muted); flex-shrink: 0; font-variant-numeric: tabular-nums; }
  .history-row .h-act {
    width: 26px; height: 26px; border: none; border-radius: 7px; background: transparent;
    color: var(--ct-muted); cursor: pointer; font-size: 14px; flex-shrink: 0; opacity: 0; display: inline-flex;
    align-items: center; justify-content: center;
  }
  .history-row:hover .h-act, .history-row:focus-within .h-act, .history-row .h-act.on { opacity: 1; }
  .history-row .h-act:hover { background: var(--ct-accent-soft); color: var(--ct-accent-strong); }
  .history-row .h-act.on { color: var(--ct-accent); }
  .history-row .h-del:hover { color: var(--ct-danger); }
  .history-empty { padding: 18px 10px; color: var(--ct-muted); font-size: 12.5px; text-align: center; }
  #bookmarkBtn[aria-pressed="true"] { color: var(--ct-accent); }

  /* NOTICES: collapsed, the strips are visually hidden but still announced
     (the sr-only technique, not display:none, which would silence them). */
  .notices.collapsed > div {
    position: absolute !important;
    width: 1px; height: 1px; margin: -1px; padding: 0; border: 0;
    overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap;
  }
  .notices-chip { gap: 4px; color: var(--ct-warn); border-color: color-mix(in srgb, var(--ct-warn) 45%, transparent); }
  .notices-chip[aria-expanded="true"] { background: color-mix(in srgb, var(--ct-warn) 12%, transparent); }

  /* The model dropdown reuses the mode menu's look, anchored under the chip on
     the left, and scrolls when a provider lists many models. */
  .model-popup { left: 12px; right: auto; max-height: min(360px, 60vh); overflow-y: auto; }
  .model-popup .mode-item[aria-disabled="true"] { opacity: 0.5; cursor: not-allowed; }
  .mode-item:focus-visible { outline: 2px solid var(--ct-accent); outline-offset: -2px; }
  .model-popup .model-tag {
    font-size: 10px;
    padding: 1px 6px;
    margin-left: 6px;
    border-radius: 999px;
    background: var(--ct-accent-soft);
    color: var(--ct-accent-strong);
    vertical-align: 1px;
  }
  .model-popup .model-empty { padding: 10px; color: var(--ct-muted); font-size: 12px; }
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
  .mode-item[aria-selected="true"] { background: var(--ct-accent-soft); }
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
  .mode-item[aria-selected="true"] .mode-name { color: var(--ct-accent-strong); }
  .mode-check {
    font-size: 14px;
    color: var(--ct-accent-strong);
    opacity: 0;
    font-weight: bold;
    margin-top: 1px;
  }
  .mode-item[aria-selected="true"] .mode-check { opacity: 1; }

  .composer-mic, .composer-divider { display: none; }

  #toolbar { display: none; }
  .chip { display: none; }


  button {
    background: var(--ct-accent-solid);
    color: var(--ct-on-accent);
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
  .edit-proposal .view-diff-btn {
    display: inline-block;
    margin: 4px 0 10px;
    background: transparent;
    color: var(--ct-ink);
    border: 1px solid var(--ct-border);
    border-radius: 6px;
    padding: 6px 10px;
    font-size: 11px;
  }
  .edit-proposal .view-diff-btn:hover { background: var(--ct-accent-soft); border-color: var(--ct-accent); }
  .edit-proposal .actions { display: flex; gap: 6px; }
  .edit-proposal .result { margin-top: 6px; font-style: italic; }
  .edit-proposal .result.ok { color: #1e7a3a; }
  .edit-proposal .result.refused { color: #c0392b; }
  .edit-proposal .result.auto-pending { opacity: 0.65; }
  /* What the gates learned, under what they decided. Muted deliberately: a
     Tier B delimiter advisory qualifies an edit that APPLIED, and styling it
     like the refusal above would read as a failure. */
  .edit-proposal .result-notes {
    margin-top: 4px;
    font-size: 0.9em;
    opacity: 0.75;
  }
  /* Blocks the parser could not read. These have no proposal bubble to live
     in -- the case that matters most is when there are no proposals at all. */
  .edit-rejections {
    margin: 4px 0 14px 38px;
    padding: 10px 12px;
    border-left: 3px solid var(--ct-danger);
    background: color-mix(in srgb, var(--ct-danger) 9%, transparent);
    border-radius: 4px;
    font-size: 0.92em;
  }
  .edit-rejections-heading { font-weight: 600; margin-bottom: 4px; }
  /* The spec workflow's reports: plain text, line breaks kept. */
  .spec-notice {
    margin: 4px 0 14px 38px;
    padding: 10px 12px;
    border-left: 3px solid var(--vscode-focusBorder, #3794ff);
    background: var(--vscode-textBlockQuote-background, rgba(127, 127, 127, 0.08));
    border-radius: 4px;
    font-size: 0.92em;
    white-space: pre-wrap;
  }
  .spec-chip { cursor: default; opacity: 0.9; }
  .edit-rejection { opacity: 0.85; font-family: var(--vscode-editor-font-family, monospace); }
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
    outline: 2px solid var(--ct-accent);
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
      <button type="button" id="bookmarkBtn" class="icon-btn" title="Bookmark this chat" aria-label="Bookmark this chat" aria-pressed="false">☆</button>
      <button type="button" id="historyBtn" class="icon-btn" title="History" aria-label="Chat history"
        aria-haspopup="dialog" aria-expanded="false" aria-controls="historyPopup">◷</button>
    </div>
    <!-- History, Claude Code style: every chat, newest first, bookmarks pinned
         on top; the search matches what was said, not only titles. -->
    <div id="historyPopup" class="history-popup" hidden role="dialog" aria-label="Chat history">
      <div class="history-head" role="search">
        <label for="searchInput" class="sr-only">Search chats</label>
        <input id="searchInput" type="text" placeholder="Search chats…" autocomplete="off" spellcheck="false" />
      </div>
      <div id="historyList" class="history-list" role="listbox" aria-label="Chats" aria-live="polite"></div>
    </div>
  </header>
  <!-- First-run text, rendered INSIDE the empty transcript: a new user's very
       first sight of this panel was a blank rectangle that never said what it
       does. Static markup, no state and no settings; main.js removes it as soon
       as a real conversation turn is added (restored history included).

       THIS USED TO TELL THE USER TO START THE DAEMON BY HAND, in a <pre> block
       naming ./daemon/mochiii-daemon. That was true when written and is
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
  <!-- THE NOTICES, COLLAPSED. Every strip is still a live region, so a screen
       reader still announces each one; visually they fold behind the notices
       chip in the composer, and a notice is shown in full only the first time
       its text appears in a session (main.js: noticesObserver). -->
  <div id="notices" class="notices collapsed">
  <div id="grounding" role="status" aria-live="polite" aria-label="Grounding"></div>
  <div id="historyNotice" role="status" aria-live="polite" aria-label="Conversation history notice"></div>
  <!-- Redaction and degradation strips carry information the user needs to
       decide whether to trust the answer (a secret was scrubbed; a subsystem is
       unavailable), so they are announced rather than merely displayed. -->
  <div id="redactions" role="status" aria-live="polite" aria-label="Redaction notice"></div>
  <div id="degraded" role="status" aria-live="polite" aria-label="Degraded functionality notice"></div>
  <div id="provider" role="status" aria-live="polite" aria-label="Serving provider"></div>
  </div>
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
          <button type="button" class="pill" id="modelChip" title="Choose model" aria-label="Choose model"
            aria-haspopup="listbox" aria-expanded="false" aria-controls="modelPopup">
            <span id="modelChipLabel">Model</span><span class="chev" aria-hidden="true">▾</span>
          </button>
          <span class="pill spec-chip" id="specChip" hidden title="Active spec: every prompt works to it (/spec show, /spec off)"></span>
          <button type="button" class="pill effort-pill" id="effortBtn" data-level="0"
            aria-label="Thinking effort: Auto. Click to change."
            title="Thinking effort: how long the model reasons before it answers. Auto uses the model's default. Higher is slower and often more careful. Click to cycle Auto, Low, Medium, High.">
            <svg class="effort-bars" width="14" height="12" viewBox="0 0 14 12" aria-hidden="true">
              <rect class="b1" x="0" y="7" width="3.2" height="5" rx="1"/>
              <rect class="b2" x="5.4" y="4" width="3.2" height="8" rx="1"/>
              <rect class="b3" x="10.8" y="0" width="3.2" height="12" rx="1"/>
            </svg>
            <span class="effort-name">Effort</span><span class="effort-value" id="effortValue">Auto</span>
          </button>
        </div>
        <div class="composer-right">
          <button type="button" class="pill notices-chip" id="noticesChip" hidden aria-expanded="false" aria-controls="notices"
            title="Notices about the last answer">
            <span aria-hidden="true">ⓘ</span><span id="noticesCount">0</span>
          </button>
          <button type="button" class="composer-icon context-btn" id="contextBtn" title="Context usage" aria-label="Open context usage" aria-expanded="false" aria-controls="contextPopup">
            <svg class="context-ring" viewBox="0 0 24 24" aria-hidden="true">
              <circle class="context-ring-track" cx="12" cy="12" r="8" fill="none" stroke-width="2.5"/>
              <circle class="context-ring-fill" id="contextRingFill" cx="12" cy="12" r="8" fill="none" stroke-width="2.5"
                stroke-linecap="round" transform="rotate(-90 12 12)"
                stroke-dasharray="50.27" stroke-dashoffset="50.27"/>
            </svg>
            <span class="context-pct" id="contextPctShort" aria-hidden="true">0%</span>
          </button>
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
    <div id="modelPopup" class="modes-popup model-popup" hidden role="dialog" aria-label="Choose model">
      <div class="modes-popup-head">
        <div class="modes-popup-title">Model</div>
      </div>
      <div class="modes-list" id="modelList" role="listbox" aria-label="Models"></div>
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

// clip shortens s to at most n characters for a label, marking the cut.
function clip(s: string, n: number): string {
  return s.length <= n ? s : s.slice(0, n) + '…';
}
