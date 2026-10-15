// THE HOST SIDE OF THE CHAT'S PAGES (media/pages.js is the other side).
//
// Every request a page makes arrives here as a message, and is answered from a
// table in rules.ts or from the daemon -- never from the message itself. A
// page asks for the command named "connect", not for "mochiii.connect"; for
// the setting "toolActivity" with a value the table accepts, not for any key;
// for the link "issues", not for a URL.

import * as vscode from 'vscode';

import { ConnectResponse, FEATURE_MCP_TOOLS, daemonInfo, fetchAvailableTiers, sendConnect, sendMCP, spendStatus, SpendStatus } from '../daemonClient';
import { MarketplaceCore, acceptNotice } from '../mcpMarketplace';
import { McpServersProvider, serverStatus } from '../mcpServersView';
import { getActiveSpec, listSpecs } from '../specWorkflow';
import { activeWorkingFolder } from '../workingFolder';
import { accountView, providerName } from './accountView';
import { MACHINE_SETTINGS, MCP_ROW_COMMANDS, checkSettingUpdate, isMachineSetting, pageCommand, pageLink } from './rules';

/** What the pages need from the extension as a whole, set once at activation. */
export interface PagesContext {
  clientName: string;
  extensionVersion: string;
  extension: vscode.ExtensionContext;
  mcpServers?: McpServersProvider;
}

let pagesContext: PagesContext | undefined;

/** setPagesContext is called by extension.ts once the MCP view exists. */
export function setPagesContext(ctx: PagesContext): void {
  pagesContext = ctx;
}

/** currentPagesContext is what setPagesContext was given, for the MCP pop-up. */
export function currentPagesContext(): PagesContext | undefined {
  return pagesContext;
}

/** What the pages need from the one chat panel they live in. */
export interface PagesPanel {
  post(msg: Record<string, unknown>): void;
  /** The tier this panel sends ('' for the daemon's default) and its model id. */
  currentTier(): string;
  currentModel(): string;
  /** Use a tier for this panel's next questions, as the model menu does. */
  selectTier(tier: string): Promise<void>;
  /** Provider-level notices this panel has seen (degradedOnce.ts). */
  providerNotes(): string[];
  /** The connect popup over the composer; settles when it closes. */
  connect?(): Promise<unknown>;
}

// 'mcp' is the /mcp dialog, which loads the servers only.
type PageName = 'customize' | 'account' | 'settings' | 'mcp';

export class ChatPagesHost implements vscode.Disposable {
  private readonly loaded = new Set<PageName>();
  private readonly disposables: vscode.Disposable[] = [];
  // The Customize page's marketplace, made on its first request. Its replies
  // go back prefixed "mk:" (pages.js strips it), so a catalog message cannot be
  // mistaken for one of the chat's own.
  private market: MarketplaceCore | undefined;

  constructor(private readonly panel: PagesPanel) {
    const mcp = pagesContext?.mcpServers;
    if (mcp) {
      // The Installed list follows the daemon, as the tree view does.
      this.disposables.push(
        mcp.onDidChangeState(() => {
          this.postToolsCount();
          if (this.loaded.has('customize') || this.loaded.has('mcp')) {
            this.postMcp();
          }
        }),
      );
    }
  }

  dispose(): void {
    this.market?.dispose();
    this.disposables.forEach((d) => d.dispose());
  }

  private async onMarket(msg: Record<string, unknown>): Promise<void> {
    const ctx = pagesContext;
    if (!ctx?.mcpServers) {
      return;
    }
    const inner = { ...msg, type: String(msg.type).slice(3) };
    if (!this.market) {
      // THE SAME ONE-TIME NOTICE the editor tab shows, before any catalog is
      // fetched: these are third-party programs that run as the user.
      if (inner.type !== 'ready' || !(await acceptNotice(ctx.extension))) {
        this.panel.post({ type: 'page:marketDeclined' });
        return;
      }
      this.market = new MarketplaceCore(
        (m) => this.panel.post({ ...(m as Record<string, unknown>), type: 'mk:' + String((m as { type?: unknown }).type) }),
        ctx.extension,
        ctx.mcpServers,
        ctx.clientName,
      );
    }
    await this.market.onMessage(inner);
  }

  /**
   * announceTools gives the composer's plug button its count, asking the
   * daemon first (the servers action starts nothing).
   */
  announceTools(): void {
    const mcp = pagesContext?.mcpServers;
    if (!mcp) {
      return;
    }
    this.postToolsCount();
    void mcp.refresh();
  }

  private postToolsCount(): void {
    const st = pagesContext?.mcpServers?.current;
    if (!st) {
      return;
    }
    const on = st.servers.filter((s) => !s.disabled).length;
    this.panel.post({ type: 'mcpCount', on, total: st.servers.length, agentMode: st.agentMode });
  }

  /** handle answers one page message. Returns false for a message that is not a page's. */
  handle(msg: Record<string, unknown>): boolean {
    const type = typeof msg.type === 'string' ? msg.type : '';
    if (type.startsWith('mk:')) {
      void this.onMarket(msg);
      return true;
    }
    switch (type) {
      case 'page:load':
        void this.load(msg.page);
        return true;
      case 'page:command': {
        if (msg.name === 'connect' && this.panel.connect) {
          void this.panel.connect().then(() => this.reloadAfter(msg.name));
          return true;
        }
        const cmd = pageCommand(msg.name);
        if (cmd) {
          void Promise.resolve(vscode.commands.executeCommand(cmd)).then(() => this.reloadAfter(msg.name));
        }
        return true;
      }
      case 'page:link': {
        const url = pageLink(msg.name);
        if (url) {
          void vscode.env.openExternal(vscode.Uri.parse(url));
        }
        return true;
      }
      case 'page:setSetting': {
        const check = checkSettingUpdate(msg.key, msg.value);
        if (!check.ok) {
          this.panel.post({ type: 'page:notice', page: 'settings', text: check.reason });
          return true;
        }
        void vscode.workspace
          .getConfiguration('mochiii')
          .update(check.key, msg.value, vscode.ConfigurationTarget.Global)
          .then(undefined, (err: Error) => this.panel.post({ type: 'page:notice', page: 'settings', text: err.message }));
        return true;
      }
      case 'page:openSetting':
        if (isMachineSetting(msg.key)) {
          void vscode.commands.executeCommand('workbench.action.openSettings', `mochiii.${msg.key}`);
        }
        return true;
      case 'page:chooseModel':
        void this.chooseModel(msg.tier);
        return true;
      case 'mcp:row': {
        const cmd = typeof msg.action === 'string' ? MCP_ROW_COMMANDS[msg.action] : undefined;
        if (cmd && typeof msg.name === 'string') {
          void vscode.commands.executeCommand(cmd, msg.name);
        }
        return true;
      }
      default:
        return false;
    }
  }

  private async reloadAfter(name: unknown): Promise<void> {
    if (name === 'connect' || name === 'forgetKey') {
      await this.load('account');
    } else if (name === 'mcpAdd') {
      await pagesContext?.mcpServers?.refresh();
    }
  }

  private async load(page: unknown): Promise<void> {
    if (page === 'account') {
      this.loaded.add('account');
      await this.postAccount();
    } else if (page === 'settings') {
      this.loaded.add('settings');
      await this.postSettings();
    } else if (page === 'mcp') {
      // The /mcp dialog: the servers only, not the rest of Customize.
      this.loaded.add('mcp');
      await pagesContext?.mcpServers?.refresh();
      this.postMcp();
    } else if (page === 'customize') {
      this.loaded.add('customize');
      await pagesContext?.mcpServers?.refresh();
      this.postMcp();
      this.postSpecs();
      await this.postTools();
    }
  }

  private clientName(): string {
    return pagesContext?.clientName ?? 'vscode';
  }

  // ---- Account --------------------------------------------------------------

  private async snapshot(): Promise<{ show?: ConnectResponse; showError?: string; version: string; spend?: SpendStatus }> {
    const name = this.clientName();
    const [show, info, spend] = await Promise.allSettled([sendConnect(name, { show: true }), daemonInfo(name), spendStatus(name)]);
    return {
      show: show.status === 'fulfilled' ? show.value : undefined,
      showError: show.status === 'rejected' ? (show.reason as Error).message : undefined,
      version: info.status === 'fulfilled' ? info.value.version : '',
      spend: spend.status === 'fulfilled' ? spend.value : undefined,
    };
  }

  private async postAccount(): Promise<void> {
    const snap = await this.snapshot();
    this.panel.post({
      type: 'page:account',
      view: accountView({
        show: snap.show,
        showError: snap.showError,
        daemonVersion: snap.version,
        spend: snap.spend,
        currentModel: this.panel.currentModel(),
        providerNotes: this.panel.providerNotes(),
      }),
    });
  }

  // ---- Settings -------------------------------------------------------------

  private async postSettings(): Promise<void> {
    const name = this.clientName();
    const cfg = vscode.workspace.getConfiguration('mochiii');
    const [snap, tiers] = await Promise.all([this.snapshot(), fetchAvailableTiers(name).catch(() => [])]);
    const folders = vscode.workspace.workspaceFolders ?? [];
    const folder = activeWorkingFolder();
    const base = snap.show?.active_api_base || snap.show?.api_base || '';
    this.panel.post({
      type: 'page:settings',
      provider: snap.show ? providerName(base) : 'not reachable',
      tiers: tiers.filter((t) => t.active).map((t) => ({ name: t.name, slug: t.slug, isDefault: !!t.default })),
      currentTier: this.panel.currentTier(),
      daemonDefault: snap.show?.model || '',
      values: {
        toolActivity: cfg.get<string>('toolActivity') === 'expanded' ? 'expanded' : 'compact',
        defaultMode: cfg.get<string>('defaultMode') ?? 'auto',
        sendEditorContext: cfg.get<boolean>('sendEditorContext') !== false,
        'history.autoSave': cfg.get<boolean>('history.autoSave') !== false,
      },
      folder: folders.find((f) => f.uri.fsPath === folder)?.name ?? folder,
      folderPath: folder,
      folderCount: folders.length,
      machine: MACHINE_SETTINGS.map((m) => ({ ...m, value: String(cfg.get(m.key) ?? '') })),
      version: this.extensionVersion(),
      daemonVersion: snap.version,
      privacy: this.panel.providerNotes(),
    });
  }

  private extensionVersion(): string {
    return pagesContext?.extensionVersion ?? '';
  }

  // The default model: this panel uses it from the next question, and the
  // daemon saves it beside the key in force so a restart keeps it.
  private async chooseModel(tier: unknown): Promise<void> {
    if (typeof tier !== 'string') {
      return;
    }
    let note = '';
    try {
      const resp = tier === '' ? await sendConnect(this.clientName(), { clear_model: true }) : await sendConnect(this.clientName(), { choose_model: tier });
      note = resp.error ? resp.error : resp.detail;
      if (!resp.error) {
        await this.panel.selectTier(tier);
      }
    } catch (err) {
      note = `Could not reach the Mochiii daemon: ${(err as Error).message}`;
    }
    this.panel.post({ type: 'page:notice', page: 'settings', text: note });
  }

  // ---- Customize ------------------------------------------------------------

  private postMcp(): void {
    const mcp = pagesContext?.mcpServers;
    if (!mcp) {
      return;
    }
    const st = mcp.current;
    this.panel.post({
      type: 'page:mcp',
      servers: st.servers.map((s) => ({
        name: s.name,
        // The daemon's command is already the program and its arguments.
        command: s.command,
        source: s.source,
        disabled: !!s.disabled,
        // A remote server's address and transport, for the /mcp dialog.
        url: s.url ?? '',
        transport: s.transport ?? '',
        env: s.env ?? [],
        // Whether it really starts, not only whether it is switched on.
        status: serverStatus(s),
        // The daemon's own line ("✔ Connected (5 tools)"), when it says one.
        statusLine: s.status_line ?? '',
        warnings: s.warnings ?? [],
        canSetKey: !!st.setEnv && (s.env ?? []).length + (s.headers ?? []).length > 0,
        canCheck: !!st.check && !s.disabled,
        // A remote server can be signed in to, or out of (FEATURE_MCP_OAUTH).
        canSignIn: !!st.oauth && !!s.url && !s.disabled,
        signedIn: !!s.signed_in,
        // A server the project proposes: its approval, and whether this
        // folder is trusted enough to approve one at all.
        approval: s.approval ?? '',
        canApprove: vscode.workspace.isTrusted,
      })),
      agentMode: st.agentMode,
      notice: st.noFolder || st.unreachable || (st.servers.length > 0 && !st.agentMode ? 'Agent mode is off in the config the daemon loaded, so no server is started.' : ''),
    });
  }

  private postSpecs(): void {
    const root = activeWorkingFolder();
    const specs = root ? listSpecs(root) : [];
    this.panel.post({ type: 'page:specs', specs, active: root ? getActiveSpec(root) : '' });
  }

  private async postTools(): Promise<void> {
    try {
      const { resp, features } = await sendMCP(this.clientName(), { action: 'tools' });
      if (!features.includes(FEATURE_MCP_TOOLS)) {
        this.panel.post({ type: 'page:tools', tools: [], note: 'This daemon cannot list its tools without starting servers. Run Show MCP tools instead.' });
        return;
      }
      this.panel.post({ type: 'page:tools', tools: resp.tools ?? [], note: resp.error ?? (resp.notes ?? []).join(' ') });
    } catch (err) {
      this.panel.post({ type: 'page:tools', tools: [], note: `Could not reach the Mochiii daemon: ${(err as Error).message}` });
    }
  }
}
