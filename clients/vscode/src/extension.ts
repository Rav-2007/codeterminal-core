import * as vscode from 'vscode';
import { shutdownExtractors } from './attachmentExtract';
import * as cp from 'child_process';
import * as path from 'path';
import * as os from 'os';
import * as fs from 'fs';

import { CHAT_VIEW_ID, CHAT_VIEW_IDS, CHAT_VIEW_TYPE, CLIENT_NAME, ChatPanel, DiffContentProvider } from './chatPanel';
import { MOVE_CHAT_HERE_COMMAND } from './chatElsewhere';
import { bundledDaemonDir, daemonBinaryName, resolveConfigPathSetting } from './daemonBinary';
import { ConnectResponse, probeDaemon, resolvedWorkspaceRoot, sendConnect, setWorkspaceRoot } from './daemonClient';
import { ensureModelAvailable } from './modelSetup';
import { API_KEY_SECRET, clearApiKey, ensureApiKey, getApiKey, promptForApiKey } from './apiKey';
import { ANY_KEY_CHOICE, formatConnectResult, keyMismatch, providerByName, providerChoices, providerForBase, providerLabel } from './connectFlow';
import { ConnectOutcome, setConnectKey } from './connectBridge';
import { DaemonHandle, DaemonSupervisor } from './daemonSupervisor';
import {
  CREDENTIALS_FROM_STDIN_FLAG,
  DaemonCredentials,
  credentialsLine,
  daemonCredentials,
  withoutCredentials,
} from './daemonCredentials';
import { cachedMcpEnv, loadMcpEnv } from './mcpSecrets';
import { useSpecStore } from './specWorkflow';
import { McpServersProvider, registerMcpServers } from './mcpServersView';
import { McpMarketplace, handleMarketplaceUri } from './mcpMarketplace';
import { FOLDERS_NOTED_KEY, WORKING_FOLDER_KEY, chooseWorkingFolder, otherFoldersNotice, setActiveWorkingFolder } from './workingFolder';
import { setPagesContext } from './chatPages/pagesHost';

let supervisor: DaemonSupervisor | undefined;
let output: vscode.OutputChannel | undefined;

// The API key, read once from SecretStorage and held for spawnDaemon, which
// hands it to the daemon on stdin (daemonCredentials.ts) -- never in its
// environment.
//
// A CACHE, AND IT HAS TO BE ONE. context.secrets.get() is async; spawnDaemon is
// called synchronously by the supervisor, which owns when a spawn happens and
// cannot be made to await a keychain. So the key is read before the first
// ensure() and refreshed whenever it changes, rather than fetched at spawn time.
//
// Every write to this is followed by a daemon restart, because the daemon reads
// its credentials once, at start. A key stored without a restart is a key the
// running daemon will never see, which would look exactly like the key not
// working.
let cachedApiKey: string | undefined;

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  // The active spec (/spec use) is remembered per workspace.
  useSpecStore(context.workspaceState);
  DiffContentProvider.register(context);

  // The bundled location, defined once in daemonBinary.ts so the spawn path and
  // /mcp-server cannot drift apart on where the runtime lives. This path was
  // always safe -- it is anchored to extensionPath -- but it was a second
  // spelling of the same layout, and the two spellings are how /mcp-server was
  // able to grow a workspace-relative one without anyone noticing.
  //
  // Deliberately NOT resolveDaemonBin: that falls back to PATH, which is right
  // for a diagnostic command and wrong for the daemon this window MANAGES.
  // Silently supervising some other daemon found on PATH is not a lifecycle
  // anyone asked for.
  const binaryPath = path.join(bundledDaemonDir(context.extensionPath), daemonBinaryName());

  const workspaceFolders = vscode.workspace.workspaceFolders;
  // '' when no folder is open, NOT '.'.
  //
  // '.' resolved to the extension host's current directory -- whatever
  // directory VS Code was launched from. The daemon was then started with that
  // as its workspace and indexed it, so a VS Code launched from $HOME read the
  // home directory into the retrieval index, and index content becomes prompt
  // context. It also wrote .mochiii/logs/ there, outside any project.
  // setWorkspaceRoot refuses a relative path for the same reason.
  //
  // ONE folder, chosen among the open ones (workingFolder.ts): the first, or
  // the one picked with "Mochiii: Choose Working Folder".
  const folderChoices = (workspaceFolders ?? []).map((f) => ({ name: f.name, fsPath: f.uri.fsPath }));
  const working = chooseWorkingFolder(folderChoices, context.workspaceState.get<string>(WORKING_FOLDER_KEY));
  const workspacePath = working.fsPath;
  setActiveWorkingFolder(workspacePath);
  if (working.warning) {
    void vscode.window.showWarningMessage(working.warning);
  }
  // The other folders open in this window, said once each (workingFolder.ts).
  const sayOtherFolders = (): void => {
    const now = (vscode.workspace.workspaceFolders ?? []).map((f) => ({ name: f.name, fsPath: f.uri.fsPath }));
    const notice = otherFoldersNotice(now, workspacePath, context.workspaceState.get<string[]>(FOLDERS_NOTED_KEY) ?? []);
    if (!notice.message) {
      return;
    }
    void context.workspaceState.update(FOLDERS_NOTED_KEY, notice.noted);
    const choose = 'Choose Folder';
    void vscode.window.showInformationMessage(notice.message, choose).then((a) => {
      if (a === choose) {
        void vscode.commands.executeCommand('mochiii.chooseWorkingFolder');
      }
    });
  };
  sayOtherFolders();

  // BEFORE anything connects, including the supervisor's first probe. The
  // daemon publishes its lockfile under a name derived from this workspace, so
  // a client that has not recorded the workspace looks for the old per-user
  // name and finds nothing -- which would make the probe answer "no daemon"
  // every time and put the unconditional spawn straight back.
  setWorkspaceRoot(workspacePath);

  output = vscode.window.createOutputChannel('Mochiii');
  context.subscriptions.push(output);

  // WHERE THE DAEMON'S LOG GOES, and why it is unconditional.
  //
  // The daemon is spawned detached with stdio 'ignore', so its stderr goes
  // nowhere. That is deliberate and stays: piping into this extension host
  // would wedge the daemon the moment the host died or stopped draining, which
  // is the same unbounded-write failure the Windows named-pipe buffer already
  // taught us. A file is the durable answer, and the daemon already knows how
  // to write one, size-rotated, teeing rather than redirecting.
  //
  // It is passed on EVERY spawn rather than only when debugging, because the
  // window that most needs the log is the one that did NOT spawn: an adopting
  // window has no pipe to the daemon by construction, so a file is the only
  // channel it can ever read. Same directory as the warn sink and the tool
  // audit log, and already gitignored.
  // Derived from the RESOLVED root, exactly as the lockfile name is. Built from
  // the raw path it disagreed with the socket whenever a workspace was reached
  // through a symlink, so a window that had ADOPTED a daemon looked for a log
  // its owner never wrote.
  //
  // Empty when no folder is open: there is then no per-workspace daemon and
  // nowhere predictable to put a log, and a relative path would land wherever
  // the extension host happened to be started. Better to pass nothing and say so
  // than to write somewhere nobody will find.
  const root = resolvedWorkspaceRoot();
  const daemonLogPath = root ? path.join(root, '.mochiii', 'logs', 'daemon.log') : '';

  // NO FOLDER, NO DAEMON.
  //
  // This daemon is bound to one workspace at startup, in immutable Server
  // fields: it indexes that directory, grounds every answer in it, and writes
  // its state under it. There is no such directory here, and the previous
  // answer -- start one anyway, rooted at whatever directory VS Code was
  // launched from -- indexed a directory nobody chose.
  //
  // Declining is not a degradation of the product, it is the product's scope.
  // Every command below says so plainly when it is used, which beats the
  // "daemon not found (expected a lockfile at ...)" a connection attempt would
  // otherwise produce.
  if (root) {
    // BEFORE the first spawn, not after. The daemon takes its environment at
    // exec, so a key read later would mean the first daemon of every session
    // starts without one and has to be restarted to pick it up -- a restart the
    // user did not ask for, to fix a problem that did not need to exist.
    //
    // Awaited, and activation is async for this one read. A keychain lookup is
    // milliseconds, and it is the difference between the daemon being able to
    // answer the first question and not.
    cachedApiKey = await getApiKey(context);
    // The keys the user's MCP servers read, for the same reason (mcpSecrets.ts).
    await loadMcpEnv(context);

    supervisor = new DaemonSupervisor({
      probe: probeDaemon,
      spawn: () => spawnDaemon(binaryPath, root, daemonLogPath),
      warn: (m) => vscode.window.showWarningMessage(m),
      // The daemon's own last words, appended to any error about it. See
      // lastDaemonLogLines: without this the user is told an exit code and
      // nothing else, for failures that are usually one line to fix.
      error: (m) => {
        const detail = lastDaemonLogLines(daemonLogPath);
        output?.appendLine(`[daemon] ${m}`);
        if (detail) {
          output?.appendLine(`[daemon] last log lines:\n${detail}`);
        }
        void vscode.window.showErrorMessage(detail ? `${m} The daemon reported: ${detail}` : m);
      },
      // Adoption is the ordinary case and must be SILENT. It goes to the output
      // channel, where someone debugging can find it, and nowhere a user who did
      // nothing wrong has to dismiss it.
      log: (m) => output?.appendLine(`[daemon] ${m}`),
      schedule: (ms, fn) => {
        const t = setTimeout(fn, ms);
        return { cancel: () => clearTimeout(t) };
      },
    });
    void supervisor.ensure();

    // AFTER ensure(), and deliberately not awaited.
    //
    // The daemon must come up on its own schedule: it works without the model
    // (ungrounded), and making its start wait on a filesystem probe -- let alone
    // on a user answering a dialog -- would trade a working product for a
    // prompt. This only ever tells the user something true and offers to fix it.
    void ensureModelAvailable(binaryPath, context, output);

    // Same rule, same reason: tell the user something true and offer to fix it,
    // without making the daemon's start wait on an answer. A daemon with no key
    // still starts, still indexes, and still serves everything that does not
    // need the model -- it just cannot answer, which is what the prompt says.
    void ensureApiKey(context, output, async (key) => {
      cachedApiKey = key;
      await vscode.commands.executeCommand('mochiii.restartDaemon');
    });

    // A key set in ANOTHER window is a key this window's daemon does not have.
    // Both windows supervise a daemon for their own workspace, and SecretStorage
    // is shared between them, so without this the second window keeps a stale
    // cache until it is reloaded.
    //
    // The cache is refreshed but NO restart is triggered here: the window that
    // set the key restarts its own daemon, and a change notification is not a
    // reason to interrupt a turn in progress somewhere else. The refreshed value
    // is picked up by the next spawn.
    context.subscriptions.push(
      context.secrets.onDidChange(async (e) => {
        if (e.key === 'mochiii.apiKey') {
          cachedApiKey = await getApiKey(context);
        }
      })
    );
  } else {
    output.appendLine(
      '[daemon] no folder is open, so there is no workspace to ground answers in and no daemon was started'
    );
  }

  // NO_WORKSPACE is what every command says instead of failing obscurely. One
  // string, because three commands must not drift into three explanations of
  // the same fact.
  const NO_WORKSPACE =
    'Mochiii grounds its answers in an open folder, and this window has none. ' +
    'Open a folder or workspace, then try again.';

  // THE MCP SERVERS VIEW, THE MARKETPLACE, AND mochiii.in's "OPEN IN VS CODE"
  // (Stage 7, docs/VSCODE_UX_OVERHAUL_2026-10-12.md). Every one is a client of
  // the daemon's own MCPRequest -- nothing here decides what may run -- and each
  // says NO_WORKSPACE rather than asking a daemon this window never started.
  const mcpServers = new McpServersProvider(CLIENT_NAME, root ? undefined : NO_WORKSPACE);
  registerMcpServers(context, mcpServers, CLIENT_NAME);
  // The chat's pages (Customize, Account, Settings) reach the same MCP state
  // the tree view shows.
  setPagesContext({
    clientName: CLIENT_NAME,
    extensionVersion: String(context.extension.packageJSON?.version ?? ''),
    extension: context,
    mcpServers,
  });
  context.subscriptions.push(
    // The chat's /mcp-server passes the words after it, which only fill the
    // search box; the palette and the view pass nothing. Resolves whether the
    // marketplace opened, which the chat reports.
    vscode.commands.registerCommand('mochiii.mcp.browse', async (search?: unknown) => {
      if (!root) {
        vscode.window.showInformationMessage(NO_WORKSPACE);
        return false;
      }
      const q = typeof search === 'string' ? search.trim().slice(0, 200) : '';
      return McpMarketplace.show(context, mcpServers, CLIENT_NAME, undefined, q || undefined);
    }),
    // vscode://MochiiiAIAgent.mochiii-vscode/mcp/install?id=<name>. The link
    // carries a name and nothing else; the marketplace looks it up in the
    // catalog it checked itself and shows it -- it never installs from a link.
    vscode.window.registerUriHandler({
      handleUri: async (uri: vscode.Uri) => {
        if (!root) {
          vscode.window.showInformationMessage(NO_WORKSPACE);
          return;
        }
        await handleMarketplaceUri(uri, context, mcpServers, CLIENT_NAME);
      },
    })
  );

  // THE SIDEBAR VIEW: the chat's home as of 0.2.0.
  //
  // 0.1.1 had no `views` and no `viewsContainers` in package.json at all, so
  // there was no activity-bar icon and no sidebar presence -- the only way in
  // was a command that took over an editor column. Claude Code and Cline both
  // register a WebviewViewProvider in their own container, and the difference is
  // most of why this extension felt like a document that had opened itself
  // rather than part of the editor.
  //
  // Its container is in the SECONDARY side bar (package.json), beside the
  // files rather than instead of them: in the activity bar, opening the
  // Explorer hid the chat and opening the chat hid the Explorer.
  //
  // retainContextWhenHidden for the same reason the panel keeps it: this view
  // can be in the middle of a tool approval the daemon is HOLDING OPEN, an
  // answer still streaming, or an edit review one block in. Collapsing the
  // sidebar must not drop any of those.
  //
  // TWO SIDEBAR HOMES since 2026-10-15 (CHAT_VIEW_IDS): the secondary side bar
  // above, and a container of Mochiii's own in the Activity Bar, so the
  // product has an icon beside Explorer and the other agents the way Claude
  // Code and Codex do. The chat is one conversation with one live host;
  // whichever view is not hosting says where the chat is (chatElsewhere.ts).
  const chatViews = new Map<string, vscode.WebviewView>();
  // A move asked for before its view had ever been resolved: honoured when
  // VS Code resolves it, which focusing the view makes it do.
  let pendingMove: string | undefined;
  const moveChatInto = (viewId: string): boolean => {
    const view = chatViews.get(viewId);
    if (!view) {
      return false;
    }
    if (!ChatPanel.hostsWebview(view.webview)) {
      ChatPanel.resolveSidebarView(view, context.extensionUri, viewId);
    }
    return true;
  };
  for (const viewId of CHAT_VIEW_IDS) {
    context.subscriptions.push(
      vscode.window.registerWebviewViewProvider(
        viewId,
        {
          resolveWebviewView: (view: vscode.WebviewView) => {
            if (!root) {
              // No folder means no daemon to ground against. Say so IN the view:
              // a notification is dismissed and forgotten, and an empty panel with
              // no explanation is how 0.1.1 greeted people.
              view.webview.options = { enableScripts: false };
              view.webview.html = noWorkspaceHtml(NO_WORKSPACE);
              return;
            }
            chatViews.set(viewId, view);
            view.onDidDispose(
              () => {
                if (chatViews.get(viewId) === view) {
                  chatViews.delete(viewId);
                }
              },
              null,
              context.subscriptions
            );
            // A fresh webview for the view that hosts the chat, or a move that
            // was waiting for this view: take the chat. Anything else offers.
            if (pendingMove === viewId || ChatPanel.hostViewId() === viewId) {
              pendingMove = undefined;
              ChatPanel.resolveSidebarView(view, context.extensionUri, viewId);
            } else {
              ChatPanel.offerSidebarView(view, context.extensionUri, viewId);
            }

            // THE SIDEBAR TAKES THE CHAT BACK when the user returns to it.
            //
            // "Open Chat in Editor" moves the chat to a tab, and the other side
            // bar can take it too, which leaves this view holding a webview
            // nothing posts to. VS Code does not re-resolve an already-resolved
            // view, so without this the view would sit there frozen. It takes
            // the chat back only when nothing needs it where it is
            // (offerSidebarView); otherwise it says where the chat is.
            //
            // Registered on the extension's subscriptions rather than the chat's,
            // deliberately: the ChatPanel this listener has to outlive is exactly
            // the one that gets disposed when another host takes over.
            view.onDidChangeVisibility(
              () => {
                if (view.visible && !ChatPanel.hostsWebview(view.webview)) {
                  ChatPanel.offerSidebarView(view, context.extensionUri, viewId);
                }
              },
              null,
              context.subscriptions
            );
          },
        },
        { webviewOptions: { retainContextWhenHidden: true } }
      )
    );
  }

  context.subscriptions.push(
    // The placeholder's "Move the chat here", and openChat's way of bringing a
    // chat out of an editor tab. Not in the palette: it needs a view id.
    vscode.commands.registerCommand(MOVE_CHAT_HERE_COMMAND, async (viewId?: unknown) => {
      if (!root) {
        vscode.window.showInformationMessage(NO_WORKSPACE);
        return;
      }
      const target = typeof viewId === 'string' && CHAT_VIEW_IDS.includes(viewId) ? viewId : CHAT_VIEW_ID;
      if (!moveChatInto(target)) {
        pendingMove = target;
      }
      await vscode.commands.executeCommand(`${target}.focus`).then(undefined, () => undefined);
    })
  );

  context.subscriptions.push(
    // mochiii.openChat now FOCUSES THE SIDEBAR rather than opening a tab, so the
    // command palette entry and Mochiii's side-bar tab lead to the same place.
    // The editor-tab route kept its own command below.
    vscode.commands.registerCommand('mochiii.openChat', () => {
      if (!root) {
        vscode.window.showInformationMessage(NO_WORKSPACE);
        return;
      }
      // Where the chat already is, if that is a side bar; otherwise into the
      // default side bar, bringing it out of an editor tab as before.
      const hosting = ChatPanel.hostViewId();
      if (hosting) {
        void vscode.commands.executeCommand(`${hosting}.focus`);
      } else {
        void vscode.commands.executeCommand(MOVE_CHAT_HERE_COMMAND, CHAT_VIEW_ID);
      }
    })
  );

  context.subscriptions.push(
    vscode.commands.registerCommand('mochiii.openChatInEditor', () => {
      if (!root) {
        vscode.window.showInformationMessage(NO_WORKSPACE);
        return;
      }
      ChatPanel.createOrShow(context.extensionUri);
    })
  );

  // THE CHAT PANEL COMES BACK AFTER A RESTART. VS Code restores a webview
  // panel that was open at exit only for an extension that registers a
  // serializer for its view type, and activates the extension for it through
  // package.json's onWebviewPanel event. Without one, a chat left open at exit
  // was simply gone after a restart. A window with no folder has no daemon to
  // fill it from, so the restored panel is closed there, as openChat declines.
  context.subscriptions.push(
    vscode.window.registerWebviewPanelSerializer(CHAT_VIEW_TYPE, {
      deserializeWebviewPanel: async (panel: vscode.WebviewPanel) => {
        if (!root) {
          panel.dispose();
          return;
        }
        ChatPanel.revive(panel, context.extensionUri);
      },
    })
  );

  context.subscriptions.push(
    // Which open folder the daemon works in. Saved per workspace and applied
    // by reloading the window: the daemon, its index, its lockfile and the
    // log path are all keyed on the one folder chosen at activation.
    vscode.commands.registerCommand('mochiii.chooseWorkingFolder', async () => {
      const folders = vscode.workspace.workspaceFolders ?? [];
      if (folders.length === 0) {
        vscode.window.showInformationMessage(NO_WORKSPACE);
        return;
      }
      if (folders.length === 1) {
        vscode.window.showInformationMessage(`Mochiii works in ${folders[0].name}, the only folder open in this window.`);
        return;
      }
      const pick = await vscode.window.showQuickPick(
        folders.map((f) => ({
          label: f.name,
          description: f.uri.fsPath === workspacePath ? 'current' : '',
          detail: f.uri.fsPath,
          fsPath: f.uri.fsPath,
        })),
        { title: 'Mochiii: Choose Working Folder', placeHolder: 'The folder Mochiii reads, searches and edits' },
      );
      if (!pick || pick.fsPath === workspacePath) {
        return;
      }
      await context.workspaceState.update(WORKING_FOLDER_KEY, pick.fsPath);
      const reload = 'Reload Window';
      const answer = await vscode.window.showInformationMessage(
        `Mochiii will work in ${pick.label} after the window reloads.`,
        reload,
      );
      if (answer === reload) {
        await vscode.commands.executeCommand('workbench.action.reloadWindow');
      }
    }),
    // A folder added to the window is not one Mochiii can reach until it is
    // chosen: say so when it happens, instead of letting a tool fail on it.
    vscode.workspace.onDidChangeWorkspaceFolders((e) => {
      if (e.added.length > 0) {
        sayOtherFolders();
      }
    }),
    vscode.commands.registerCommand('mochiii.restartDaemon', async () => {
      if (!supervisor) {
        vscode.window.showInformationMessage(NO_WORKSPACE);
        return;
      }
      vscode.window.showInformationMessage('Restarting Mochiii daemon...');
      await supervisor.restart();
    })
  );

  // /connect, THROUGH THE DAEMON -- as the terminal client has done since
  // 2026-10-05. The daemon tells which provider a key is for (Groq, NVIDIA,
  // OpenAI, Anthropic, Gemini, OpenRouter and 11 more), proves it, finds a model
  // that answers, stores it (~/.mochiii/credentials.json, 0600) and uses it at
  // once, without a restart. See connectFlow.ts for why VS Code's old path --
  // its own key in SecretStorage, passed as MOCHIII_API_KEY -- could not.
  //
  // The key lives in this function's locals and goes nowhere but the daemon's
  // peer-authenticated local socket: not the transcript, not a log, not a
  // setting. Returned to the chat is the daemon's MASKED report.
  //
  // With no folder open there is no daemon to prove a key with, so the old path
  // stays for that case: stored in SecretStorage for the next window.
  async function connectThroughDaemon(base?: string): Promise<{ message: string; inUse: boolean }> {
    if (!supervisor) {
      const saved = await promptForApiKey(context);
      if (!saved) {
        return { message: 'Cancelled: no key was stored.', inUse: false };
      }
      cachedApiKey = saved;
      return { message: 'Key stored in VS Code. Open a folder to start asking questions.', inUse: false };
    }
    // Step 1: the provider. Step 2: its key. The command palette's path; the
    // chat asks the same two things in its own popup (ChatPanel.openConnect).
    if (!base) {
      const pick = await vscode.window.showQuickPick(
        [ANY_KEY_CHOICE, ...providerChoices()].map((p) => ({ label: p.name, description: p.hint, detail: p.apiBase, apiBase: p.apiBase })),
        {
          title: 'Mochiii: connect a model provider (1/2)',
          placeHolder: 'Which provider is the key from?',
          matchOnDescription: true,
          ignoreFocusOut: true,
        },
      );
      if (!pick) {
        return { message: 'Cancelled: no key was sent.', inUse: false };
      }
      base = pick.apiBase;
    }
    const named = base ? providerForBase(base) : undefined;
    const key = await vscode.window.showInputBox({
      title: `Mochiii: API key${named ? ` for ${named.name}` : base ? ` for ${base}` : ''} (2/2)`,
      prompt:
        'Paste the API key. It is checked with the provider and stored for this machine. It never appears in the chat.',
      password: true,
      ignoreFocusOut: true,
      validateInput: (v) => (v.trim() === '' ? 'Paste a key, or press Escape to cancel.' : base ? keyMismatch(v, base) : undefined),
    });
    if (!key || key.trim() === '') {
      return { message: 'Cancelled: no key was sent.', inUse: false };
    }
    return connectKey(key.trim(), base || undefined, true);
  }

  // The key path itself, shared by the palette above and the chat's popup
  // (connectBridge.ts). The daemon proves the key with the provider at apiBase,
  // stores it and uses it at once.
  async function connectKey(key: string, base: string | undefined, notify: boolean): Promise<ConnectOutcome> {
    if (!supervisor) {
      return { message: 'Open a folder first: there is no daemon to check the key with.', inUse: false };
    }
    if (base) {
      const mismatch = keyMismatch(key, base);
      if (mismatch) {
        return { message: mismatch, inUse: false };
      }
    }
    const ask = (apiBase?: string): Thenable<ConnectResponse> =>
      notify
        ? vscode.window.withProgress(
            {
              location: vscode.ProgressLocation.Notification,
              title: `Mochiii: checking the key with ${apiBase ? providerLabel(apiBase) : 'its provider'}…`,
            },
            () => sendConnect(CLIENT_NAME, { api_key: key, api_base: apiBase }),
          )
        : sendConnect(CLIENT_NAME, { api_key: key, api_base: apiBase });
    let resp: ConnectResponse;
    try {
      resp = await ask(base);
      // A key whose prefix several providers share is sent NOWHERE until the
      // user says whose it is. The terminal asks them to paste it again; here
      // the key is still in hand, so they pick from the daemon's candidates.
      if (resp.outcome === 'needs_provider' && resp.candidates && resp.candidates.length > 0) {
        // The chat's popup asks with its own list (connect.js), the key still
        // in its field; only the palette path asks here.
        if (!notify) {
          const candidates = resp.candidates.map((id) => providerByName(id)?.apiBase).filter((b): b is string => !!b);
          return { message: `That key was sent nowhere: ${resp.detail}. Pick its provider, then press Connect again.`, inUse: false, candidates };
        }
        const items = resp.candidates.map((id) => {
          const p = providerByName(id);
          return { label: p ? p.name : id, description: p ? p.apiBase : '', id };
        });
        const pick = await vscode.window.showQuickPick(items, {
          title: 'Mochiii: which provider issued this key?',
          placeHolder: 'Nothing has been sent yet. The key goes only to the provider you pick.',
          ignoreFocusOut: true,
        });
        if (!pick) {
          return { message: formatConnectResult(resp), inUse: false };
        }
        const chosen = providerByName(pick.id);
        resp = await ask(chosen ? chosen.apiBase : undefined);
      }
    } catch (err) {
      return { message: `connect failed: could not reach the daemon (${(err as Error).message}).`, inUse: false };
    }

    const extra: string[] = [];
    const stored = resp.outcome === 'accepted' || resp.outcome === 'unverified';
    // VS Code's own SecretStorage key reaches the daemon as MOCHIII_API_KEY,
    // which outranks the key just stored -- so connecting would otherwise
    // "succeed" and change nothing. Move the user onto the daemon's store. A
    // MOCHIII_API_KEY from the user's own environment is theirs, and only named.
    if (stored && resp.env_override && cachedApiKey && !process.env.MOCHIII_API_KEY) {
      await clearApiKey(context);
      cachedApiKey = undefined;
      await supervisor.restart();
      resp = { ...resp, env_override: false, in_use: true };
      extra.push(
        'VS Code had a key of its own, which it passed to the daemon at start and which outranked this one. ' +
          'It was removed, and the daemon restarted using the key you just connected.',
      );
    }
    return { message: formatConnectResult(resp, extra), inUse: stored && resp.in_use };
  }

  context.subscriptions.push(
    vscode.commands.registerCommand('mochiii.connect', (opts?: { base?: string }) => connectThroughDaemon(opts?.base))
  );
  setConnectKey((key, apiBase) => connectKey(key, apiBase || undefined, false));
  context.subscriptions.push({ dispose: () => setConnectKey(undefined) });

  // /connect show: what the daemon has stored and is using -- masked by the
  // daemon. A key VS Code still keeps for itself is named, since it wins.
  context.subscriptions.push(
    vscode.commands.registerCommand('mochiii.showApiKey', async (): Promise<string> => {
      const own = await getApiKey(context);
      const ownLine = own
        ? `VS Code also keeps a key of its own (...${own.slice(-4)}), passed to the daemon at start; it outranks the stored one. /connect replaces it.`
        : '';
      if (!supervisor) {
        return own ? `No folder is open, so there is no daemon to ask. ${ownLine}` : 'No folder is open, so there is no daemon to ask, and VS Code holds no key.';
      }
      try {
        const r = await sendConnect(CLIENT_NAME, { show: true });
        return own ? formatConnectResult({ ...r, env_override: false }, [ownLine]) : formatConnectResult(r);
      } catch (err) {
        return `could not ask the daemon: ${(err as Error).message}`;
      }
    })
  );

  // /connect forget: removes the daemon's stored key AND any key VS Code kept
  // for itself -- "forget" leaving a second key in force would be a lie.
  context.subscriptions.push(
    vscode.commands.registerCommand('mochiii.forgetApiKey', async (): Promise<string> => {
      const parts: string[] = [];
      if (supervisor) {
        try {
          parts.push(formatConnectResult(await sendConnect(CLIENT_NAME, { forget: true })));
        } catch (err) {
          parts.push(`could not ask the daemon to forget its key: ${(err as Error).message}`);
        }
      }
      const own = await getApiKey(context);
      if (own) {
        await clearApiKey(context);
        cachedApiKey = undefined;
        parts.push('The key VS Code kept for itself was removed as well.');
      }
      // The running daemon still holds a key in memory until it restarts.
      if (supervisor && parts.length > 0) {
        await supervisor.restart();
        parts.push('The daemon was restarted, so it is no longer using a removed key.');
      }
      return parts.length > 0 ? parts.join('\n') : 'No key was stored, so there was nothing to remove.';
    })
  );

  // DELIBERATELY NOT GATED ON A WORKSPACE, unlike the three commands around it.
  //
  // Those three act on a daemon, and there is no daemon without a folder. This
  // one stores a credential for the machine, which is worth doing from any
  // window -- including the empty one a user lands in right after installing,
  // which is exactly when they have the key in their clipboard.
  context.subscriptions.push(
    vscode.commands.registerCommand('mochiii.setApiKey', async () => {
      // With a folder open, the same provider-aware path as /connect.
      if (supervisor) {
        const r = await connectThroughDaemon();
        output?.appendLine('[connect] ' + r.message.split('\n').join('\n[connect] '));
        const first = r.message.split('\n')[0];
        if (r.inUse) {
          void vscode.window.showInformationMessage(`Mochiii: ${first}`);
        } else {
          void vscode.window.showWarningMessage(`Mochiii: ${first} (details in the Mochiii output)`);
        }
        return;
      }
      const existing = await getApiKey(context);

      if (existing) {
        const REPLACE = 'Replace';
        const REMOVE = 'Remove';
        const pick = await vscode.window.showQuickPick([REPLACE, REMOVE], {
          title: 'Mochiii: a key is already stored',
          placeHolder: 'Replace it with a new one, or remove it.',
        });
        if (pick === undefined) {
          return;
        }
        if (pick === REMOVE) {
          await clearApiKey(context);
          cachedApiKey = undefined;
          // No folder, so no daemon is running that could still hold it.
          vscode.window.showInformationMessage('Mochiii: API key removed.');
          return;
        }
      }

      const key = await promptForApiKey(context);
      if (!key) {
        return; // cancelled
      }
      cachedApiKey = key;

      // No workspace, so no daemon to restart -- the key is stored and the next
      // window that opens a folder will start its daemon with it.
      vscode.window.showInformationMessage('Mochiii: API key saved. Open a folder to start asking questions.');
    })
  );

  // TEST MODE ONLY: put a key into SecretStorage the way a user's would arrive,
  // without an input box a test cannot type into. Registered only when VS Code
  // runs this extension under @vscode/test-electron (ExtensionMode.Test) -- never
  // in a user's session and never under F5 (ExtensionMode.Development) -- so no
  // other extension can ever reach it. credentialHandoff.test.ts drives it.
  if (context.extensionMode === vscode.ExtensionMode.Test) {
    context.subscriptions.push(
      vscode.commands.registerCommand('mochiii.test.storeApiKey', async (key?: string) => {
        if (key && key.trim() !== '') {
          await context.secrets.store(API_KEY_SECRET, key.trim());
        } else {
          await clearApiKey(context);
        }
        cachedApiKey = await getApiKey(context);
      })
    );
  }

  // Reachable from a window that ADOPTED the daemon and therefore has no pipe,
  // no child process, and nothing else to show. Opening the file rather than
  // streaming it into the OutputChannel keeps one copy of the truth: the log is
  // whatever the daemon wrote, size-rotated by the daemon, and this command is
  // only a way to look at it.
  context.subscriptions.push(
    vscode.commands.registerCommand('mochiii.showDaemonLog', async () => {
      if (!daemonLogPath) {
        vscode.window.showInformationMessage(NO_WORKSPACE);
        return;
      }
      try {
        const doc = await vscode.workspace.openTextDocument(vscode.Uri.file(daemonLogPath));
        await vscode.window.showTextDocument(doc, { preview: false });
      } catch {
        // Absent is a normal state, not an error: no daemon has started in this
        // workspace yet. Say which file was looked for, so it is actionable.
        vscode.window.showInformationMessage(
          `No Mochiii daemon log yet at ${daemonLogPath}. It is written once a daemon starts in this workspace.`
        );
      }
    })
  );
}

// spawnDaemon starts one daemon process and adapts it to the supervisor's
// DaemonHandle. It contains no policy: whether to spawn at all, and what a
// given exit means, are decisions that live in DaemonSupervisor.

// lastDaemonLogLines returns the tail of the daemon's own log, for attaching to
// an error the user would otherwise be unable to act on.
//
// THE MESSAGE WITHOUT THIS IS A DEAD END. When the daemon cannot start, the
// supervisor knows only an exit code, so the user was told "Mochiii daemon
// exited 1 5 times and will not be restarted again" -- which names neither the
// cause nor the cure. The daemon meanwhile wrote the exact reason to its log
// one line before dying, e.g. `MOCHIII_API_BASE "not-a-url" has no
// scheme`. That is a problem the user can fix in a minute and could not
// previously see.
//
// The example used to be "MOCHIII_API_BASE must be set", which no longer
// happens: an ABSENT base now takes a default rather than killing the daemon.
// A typo'd one is still fatal, by startup_validate.go's design.
//
// Bounded and defensive on purpose: this runs on a path that is ALREADY
// failing, so it must not add a second failure. Any error reading the log is
// swallowed and the original message is shown alone.
function lastDaemonLogLines(logPath: string, max = 3): string {
  if (!logPath) {
    return '';
  }
  try {
    const text = fs.readFileSync(logPath, 'utf8');
    const lines = text.split('\n').filter((l) => l.trim() !== '');
    if (lines.length === 0) {
      return '';
    }
    return lines.slice(-max).join('\n');
  } catch {
    return '';
  }
}

// daemonEnvironment is the environment the daemon is STARTED with, rather than
// whatever the extension host happened to inherit.
//
// THE FIRST-RUN DEFECT LIVED IN THE ABSENCE OF THIS FUNCTION. spawnDaemon passed
// no env, so the child got the host's; a VS Code launched from a desktop icon
// has no MOCHIII_API_BASE in it, the daemon called Fatal, and the
// supervisor reported "exited 1 5 times and will not be restarted again". Every
// test supplied the variable by hand, so nothing ever saw what an install sees.
//
// The daemon now defaults its own API base, so this function's job is narrower
// and worth stating: it DELIVERS the user's setting when there is one. A
// setting nobody passes to the process that needs it is not a setting.
//
// `machine` scope on the contributed setting is what stops a workspace's
// .vscode/settings.json supplying this value; see package.json, and
// daemonBinary.test.ts for the same boundary on the config file.
// EXPORTED so it can be tested: a pure function of process.env and
// configuration.
//
// IT NEVER CARRIES A KEY, and it takes no key argument so that it cannot be
// handed one. It used to: the SecretStorage key went in as MOCHIII_API_KEY,
// which left it readable in /proc/<daemon-pid>/environ for the daemon's whole
// life (os.Unsetenv in the daemon cannot take that back). The key now goes on
// the daemon's stdin -- see daemonCredentials.ts -- and MOCHIII_API_KEY and
// MOCHIII_PROXY_KEY are REMOVED here even when VS Code inherited them from a
// shell; spawnDaemon hands an inherited key over on stdin instead, so the
// from-source workflow the README documents keeps working.
export function daemonEnvironment(): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = withoutCredentials(process.env);
  const configured = vscode.workspace.getConfiguration('mochiii').get<string>('apiBase');
  if (typeof configured === 'string' && configured.trim() !== '') {
    env.MOCHIII_API_BASE = configured.trim();
  }
  // THE MODEL, when one is named: a local server (Ollama, LM Studio) knows its
  // models by its own names, none of which are in models.json. Same delivery
  // path as the base, for the same reason -- a setting that never reached the
  // daemon would look configured and change nothing.
  const model = vscode.workspace.getConfiguration('mochiii').get<string>('model');
  if (typeof model === 'string' && model.trim() !== '') {
    env.MOCHIII_MODEL = model.trim();
  }
  return env;
}

// What the last spawnDaemon call was GIVEN, as names only -- argument list,
// environment variable names, and the fields of the stdin line -- so a test can
// assert that a key went down the pipe and not into the environment without any
// value ever being held here. See lastDaemonSpawnForTest.
export interface DaemonSpawnRecord {
  args: string[];
  envNames: string[];
  stdinFields: string[];
  stdin: string;
}
let lastSpawn: DaemonSpawnRecord | undefined;

export function lastDaemonSpawnForTest(): DaemonSpawnRecord | undefined {
  return lastSpawn;
}

function spawnDaemon(binaryPath: string, workspacePath: string, logPath: string): DaemonHandle {
  try {
    // Ensure binary is executable on Unix-like systems
    if (os.platform() !== 'win32' && fs.existsSync(binaryPath)) {
      fs.chmodSync(binaryPath, 0o755);
    }
  } catch (err) {
    vscode.window.showWarningMessage(`Failed to set execution permissions on the Mochiii daemon: ${err}`);
  }

  // -log-file only when there is a resolved workspace to anchor it to; see
  // daemonLogPath in activate(). Without it the daemon logs to stderr, which for
  // a detached child means nowhere -- the honest outcome when there is no
  // workspace, rather than a file written to an unpredictable cwd.
  const args = ['--workspace', workspacePath, CREDENTIALS_FROM_STDIN_FLAG];
  if (logPath) {
    args.push('-log-file', logPath);
  }

  // mochiii.configPath, when set, names the models.json the daemon loads --
  // the supported way to turn on agent mode, whose bundled config has it off.
  // It is read HERE from the machine-scoped setting (a workspace cannot set
  // it) and resolved to an absolute path (the daemon's cwd is the workspace,
  // so a relative --config would resolve against the opened folder). See
  // resolveConfigPathSetting for why both properties are load-bearing.
  const { path: configPath, warning: configWarning } = resolveConfigPathSetting(
    vscode.workspace.getConfiguration('mochiii').get<string>('configPath'),
  );
  if (configWarning) {
    vscode.window.showWarningMessage(`Mochiii: ${configWarning}`);
  }
  if (configPath) {
    args.push('--config', configPath);
  }

  // EXPLICIT, because inheriting was the first-run defect: process.env alone is
  // whatever launched VS Code, and a desktop icon carries none of a login
  // shell's exports. And KEYLESS: see daemonEnvironment.
  const env = daemonEnvironment();
  const credentials: DaemonCredentials = daemonCredentials(cachedApiKey, process.env, cachedMcpEnv());
  const child = cp.spawn(binaryPath, args, {
    cwd: workspacePath,
    detached: true,
    env,
    // stdin IS a pipe, and the only one: the daemon reads one line from it at
    // start and never again, and this side closes it straight after writing,
    // so nothing can block on it.
    //
    // stdout and stderr are NOT pipes. See daemonLogPath in activate(): a
    // detached child that outlives this host would block on a full pipe nobody
    // is draining. The -log-file above is the durable channel, and it works for
    // an adopting window too, which has no pipe at all.
    stdio: ['pipe', 'ignore', 'ignore'],
  });
  lastSpawn = {
    args: [...args],
    envNames: Object.keys(env),
    stdinFields: Object.keys(credentials),
    stdin: 'pipe',
  };
  // An 'error' on stdin with no listener would crash the extension host. It
  // happens when the daemon is gone before reading -- a binary too old to know
  // the flag exits at once -- and the daemon's own exit is what the supervisor
  // reports; this only records why the handover failed.
  child.stdin?.on('error', (err) => {
    output?.appendLine(`[daemon] could not hand the credentials over on stdin: ${err.message}`);
  });
  child.stdin?.end(credentialsLine(credentials));
  child.unref();

  return {
    onExit: (cb) => child.on('exit', cb),
    onError: (cb) => child.on('error', cb),
    kill: () => child.kill('SIGTERM'),
  };
}

export function deactivate(): Thenable<void> {
  // Kills only a daemon THIS window started; an adopted one belongs to another
  // window that is still using it. See DaemonSupervisor.dispose.
  supervisor?.dispose();
  supervisor = undefined;
  // Stops the OCR worker thread if an image was ever read; a no-op otherwise.
  return shutdownExtractors();
}

// noWorkspaceHtml is what the sidebar view shows in a window with no folder
// open.
//
// IN THE VIEW, NOT A NOTIFICATION. A toast is dismissed and gone, and the view
// it was explaining is still sitting there blank -- which is how 0.1.1 greeted
// anyone whose first click was the chat icon. The view is the thing the user is
// looking at, so the reason goes in it.
//
// SCRIPTS OFF, and the CSP says so: this page has no behaviour and nothing to
// post, so it should not be able to. The theme variables still apply -- a
// stylesheet needs no script -- so it looks like part of the editor rather than
// a white rectangle in a dark window.
function noWorkspaceHtml(message: string): string {
  const escaped = message.replace(/[&<>"']/g, (c) =>
    ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c] as string
  );
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
  p { margin: 0; line-height: 1.5; color: var(--vscode-descriptionForeground); }
</style>
</head>
<body>
  <h2>No folder open</h2>
  <p>${escaped}</p>
</body>
</html>`;
}
