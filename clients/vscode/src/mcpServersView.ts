// THE "MCP SERVERS" VIEW: the servers this window's daemon will start, in the
// sidebar beside the chat, with add / change / pause / remove on each.
//
// Before this the only way to give Mochiii an MCP server from VS Code was to
// find mochiii.configPath, write a models.json by hand and restart the daemon
// (docs/VSCODE_UX_OVERHAUL_2026-10-12.md, P1-4). Everything here is the daemon's
// own MCPRequest -- the view decides nothing about what may run:
//
//   * The list is MCPActionServers, which reads the config and the user's file
//     and STARTS NOTHING. MCPActionList would launch every third-party program
//     to ask what it offers, every time the sidebar redraws.
//   * Add and change go through storeServer (mcpInstall.ts): the daemon's
//     warning, then the server's name typed.
//   * Pause and resume do not ask again -- the program does not change -- and
//     the daemon is what refuses them for a server the config file defines.
//
// A NATIVE TREE, NOT A WEBVIEW. It is a list with actions on each row, which is
// what a tree is for; it inherits the theme, the keyboard and the screen reader
// for free, and it has no content-security policy to get wrong. The marketplace,
// which is browsing rather than managing, is the webview (mcpMarketplace.ts).

import * as vscode from 'vscode';
import {
  FEATURE_MCP_ENV,
  FEATURE_MCP_MANAGE,
  FEATURE_MCP_CHECK,
  FEATURE_MCP_SECRETS,
  MCP_CWD_WORKSPACE,
  MCP_SOURCE_CONFIG,
  MCP_STATE_FAILED,
  MCP_STATE_RUNNING,
  MCPServerInfo,
  RESTART_HINT,
  sendMCP,
} from './daemonClient';
import { validEnvName, validServerName } from './mcpCatalog';
import { ServerSpec, splitArgs, storeServer } from './mcpInstall';
import { cachedMcpEnv, storeMcpEnv, storedMcpEnvNames } from './mcpSecrets';
import { McpPopupHost, mcpPopupHost } from './mcpPopup';

/**
 * popup is the chat's MCP pop-up, shown: the way the MCP commands ask now,
 * in place of input boxes at the top of the window. Undefined when there is no
 * chat panel to show it in -- then the commands fall back to the input boxes.
 */
async function popup(): Promise<McpPopupHost | undefined> {
  const host = mcpPopupHost();
  if (host) {
    await vscode.commands.executeCommand('mochiii.chatView.focus').then(undefined, () => undefined);
  }
  return host;
}

export const MCP_SERVERS_VIEW_ID = 'mochiii.mcpServers';

/** A row: a server, or a line that says something about the list. */
export type McpNode =
  | { kind: 'server'; server: MCPServerInfo }
  | { kind: 'notice'; label: string; detail: string; icon: string; command?: vscode.Command };

/** What the daemon said, as the view holds it. */
export interface McpServersState {
  servers: MCPServerInfo[];
  agentMode: boolean;
  /** Whether the daemon takes update/enable/disable/servers (FEATURE_MCP_MANAGE). */
  manage: boolean;
  /** Whether the daemon takes a server's key and reports its state (FEATURE_MCP_ENV). */
  setEnv?: boolean;
  /** Whether the daemon keeps keys in its own file (FEATURE_MCP_SECRETS). */
  secrets?: boolean;
  /** Whether the daemon takes check, get and add_json (FEATURE_MCP_CHECK). */
  check?: boolean;
  notes: string[];
  /** Set when the daemon could not be asked. */
  unreachable?: string;
  /** Set when no folder is open: there is no daemon to ask at all. */
  noFolder?: string;
}

export class McpServersProvider implements vscode.TreeDataProvider<McpNode> {
  private readonly changed = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData = this.changed.event;
  private readonly stateChanged = new vscode.EventEmitter<McpServersState>();
  /** Fires after every refresh, so the marketplace can mark what is installed. */
  readonly onDidChangeState = this.stateChanged.event;

  private state: McpServersState = { servers: [], agentMode: true, manage: false, notes: [] };
  private loaded = false;
  private retries = 0;
  private retryTimer: ReturnType<typeof setTimeout> | undefined;

  /** noFolder, when set, is what the view says instead of asking a daemon that cannot exist. */
  constructor(
    private readonly clientName: string,
    private readonly noFolder?: string,
  ) {}

  get current(): McpServersState {
    return this.state;
  }

  /** refresh asks the daemon again. Safe to call often: it starts nothing. */
  async refresh(): Promise<McpServersState> {
    if (this.retryTimer) {
      clearTimeout(this.retryTimer);
      this.retryTimer = undefined;
    }
    if (this.noFolder) {
      this.state = { servers: [], agentMode: true, manage: false, notes: [], noFolder: this.noFolder };
      this.loaded = true;
      this.changed.fire();
      this.stateChanged.fire(this.state);
      return this.state;
    }
    try {
      const { resp, features } = await sendMCP(this.clientName, { action: 'servers' });
      const manage = features.includes(FEATURE_MCP_MANAGE);
      const setEnv = features.includes(FEATURE_MCP_ENV);
      const secrets = features.includes(FEATURE_MCP_SECRETS);
      const check = features.includes(FEATURE_MCP_CHECK);
      if (resp.error) {
        this.state = { servers: [], agentMode: true, manage, setEnv, secrets, check, notes: [], unreachable: resp.error };
      } else {
        this.state = { servers: resp.servers ?? [], agentMode: resp.agent_mode, manage, setEnv, secrets, check, notes: resp.notes ?? [] };
      }
      this.retries = 0;
    } catch (err) {
      this.state = { servers: [], agentMode: true, manage: false, notes: [], unreachable: (err as Error).message };
      // The daemon starts when the extension does, and is not waited for. A view
      // opened in that second would otherwise say "not running" about a daemon
      // that is about to be.
      if (this.retries < 5) {
        this.retries++;
        this.retryTimer = setTimeout(() => void this.refresh(), 1500 * this.retries);
      }
    }
    this.loaded = true;
    this.changed.fire();
    this.stateChanged.fire(this.state);
    return this.state;
  }

  dispose(): void {
    if (this.retryTimer) {
      clearTimeout(this.retryTimer);
    }
    this.changed.dispose();
    this.stateChanged.dispose();
  }

  getChildren(node?: McpNode): McpNode[] {
    if (node) {
      return [];
    }
    if (!this.loaded) {
      void this.refresh();
      return [];
    }
    return nodesFor(this.state);
  }

  getTreeItem(node: McpNode): vscode.TreeItem {
    if (node.kind === 'notice') {
      const item = new vscode.TreeItem(node.label, vscode.TreeItemCollapsibleState.None);
      item.tooltip = node.detail;
      item.iconPath = new vscode.ThemeIcon(node.icon);
      item.contextValue = 'mcpNotice';
      if (node.command) {
        item.command = node.command;
      }
      return item;
    }
    const s = node.server;
    const item = new vscode.TreeItem(s.name, vscode.TreeItemCollapsibleState.None);
    const fromConfig = s.source === MCP_SOURCE_CONFIG;
    item.description = s.disabled
      ? 'paused'
      : s.status_line || serverStatus(s).short || (fromConfig ? 'from models.json' : s.program ?? '');
    item.iconPath = new vscode.ThemeIcon(
      s.disabled ? 'debug-pause' : s.state === MCP_STATE_FAILED ? 'error' : fromConfig ? 'settings-gear' : 'server-environment',
    );
    item.contextValue = fromConfig ? 'mcpServer.config' : s.disabled ? 'mcpServer.user.disabled' : 'mcpServer.user.enabled';
    item.tooltip = serverTooltip(s);
    item.accessibilityInformation = {
      label: `MCP server ${s.name}${s.disabled ? ', disabled' : ''}${fromConfig ? ', defined in models.json' : ''}`,
    };
    return item;
  }
}

/** nodesFor is the rows for a state: notices first, then the servers. Exported for tests. */
export function nodesFor(state: McpServersState): McpNode[] {
  const nodes: McpNode[] = [];
  if (state.noFolder) {
    return [{ kind: 'notice', label: 'Open a folder to use MCP servers', detail: state.noFolder, icon: 'folder-opened' }];
  }
  if (state.unreachable) {
    nodes.push({
      kind: 'notice',
      label: "Mochiii's daemon is not answering",
      detail: `${state.unreachable}\n\nIt starts with the chat. Open it, or ${RESTART_HINT}, then refresh this list.`,
      icon: 'warning',
      command: { command: 'mochiii.mcp.refresh', title: 'Refresh' },
    });
    return nodes;
  }
  if (!state.agentMode) {
    nodes.push({
      kind: 'notice',
      label: 'Agent mode is off: no server runs',
      detail:
        "The daemon's models.json does not set mcp.enabled, so no MCP server is started and no tool is offered. " +
        'Servers added here are kept and start once agent mode is on.',
      icon: 'info',
    });
  }
  if (!state.manage && state.servers.length > 0) {
    nodes.push({
      kind: 'notice',
      label: 'Restart the daemon to change servers here',
      detail: `This daemon can add and remove servers but not change or pause them. ${RESTART_HINT}.`,
      icon: 'info',
    });
  }
  for (const note of state.notes) {
    nodes.push({ kind: 'notice', label: note.length > 80 ? note.slice(0, 79) + '…' : note, detail: note, icon: 'warning' });
  }
  for (const server of state.servers) {
    nodes.push({ kind: 'server', server });
  }
  // Nothing at all: the view's welcome content (package.json viewsWelcome) is
  // what shows, with the two ways to add one.
  return state.servers.length === 0 && nodes.length === 0 ? [] : nodes;
}

/**
 * serverStatus is what a server's row says about it beyond on/off: whether its
 * last start failed (and why, in its own words), and which keys it has no value
 * for. A server marked "Active" that exits on every start was the bug this is for
 * (2026-10-14). Exported for the chat's Customize page and for tests.
 */
export function serverStatus(s: MCPServerInfo): { short: string; detail: string; needsKey: boolean } {
  const missing = s.env_missing ?? [];
  const needsKey = missing.length > 0;
  if (s.disabled) {
    return { short: '', detail: '', needsKey };
  }
  if (s.state === MCP_STATE_FAILED) {
    const why = s.state_detail ? `: ${s.state_detail}` : '';
    const hint = needsKey ? ` It has no value for ${missing.join(', ')}; use Set key.` : '';
    return { short: needsKey ? `needs ${missing.join(', ')}` : 'failed to start', detail: `Failed to start${why}.${hint}`, needsKey };
  }
  if (needsKey) {
    return {
      short: `needs ${missing.join(', ')}`,
      detail: `Mochiii has no value for ${missing.join(', ')}, which this server reads; most servers exit without their key. Use Set key.`,
      needsKey,
    };
  }
  if (s.state === MCP_STATE_RUNNING) {
    return { short: 'running', detail: 'Started and answered the last time a turn used it.', needsKey };
  }
  return { short: '', detail: 'Not started yet: it starts with the next question in agent mode.', needsKey };
}

function serverTooltip(s: MCPServerInfo): vscode.MarkdownString {
  const md = new vscode.MarkdownString(undefined, true);
  md.isTrusted = false;
  md.appendMarkdown(`**${escapeMd(s.name)}**${s.disabled ? ' — paused' : ''}\n\n`);
  const status = serverStatus(s);
  if (status.detail) {
    md.appendMarkdown(escapeMd(status.detail) + '\n\n');
  }
  md.appendCodeblock(s.command, 'text');
  md.appendMarkdown(
    s.source === MCP_SOURCE_CONFIG
      ? "Defined in the daemon's models.json; change it there.\n\n"
      : 'Added by you; kept in ~/.mochiii/mcp-servers.json.\n\n',
  );
  if (s.env && s.env.length > 0) {
    md.appendMarkdown(`Given the variables: ${s.env.map((e) => '`' + e + '`').join(', ')}\n\n`);
    const kept = s.env_stored ?? storedMcpEnvNames().filter((n) => s.env?.includes(n));
    if (kept.length > 0) {
      md.appendMarkdown(`Values kept by Mochiii: ${kept.map((e) => '`' + e + '`').join(', ')}\n\n`);
    }
  }
  md.appendMarkdown(s.cwd === MCP_CWD_WORKSPACE ? 'Starts inside the project.' : 'Starts in a private folder of its own.');
  md.appendMarkdown('\n\nRuns with your full privileges; each of its tools asks before it runs.');
  return md;
}

function escapeMd(s: string): string {
  return s.replace(/[\\`*_{}[\]()#+\-.!|<>]/g, '\\$&');
}

/** askForServer collects a server by hand, prefilled when changing one. Undefined is cancel. Exported for tests. */
export async function askForServer(existing?: MCPServerInfo): Promise<ServerSpec | undefined> {
  const name = existing
    ? existing.name
    : await vscode.window.showInputBox({
        title: 'Add an MCP server (1/4): its name',
        prompt: "The name approval prompts will call it. Lower-case letters, digits, '-' and '_'.",
        placeHolder: 'github',
        ignoreFocusOut: true,
        validateInput: (v) =>
          validServerName(v.trim()) ? undefined : "1-32 of a-z, 0-9, '-' and '_', starting with a letter or digit; not \"builtin\"",
      });
  if (name === undefined) {
    return undefined;
  }
  const step = (n: number) => `${existing ? 'Change ' + existing.name : 'Add an MCP server'} (${n}/4)`;

  const current = existing ? [existing.program ?? '', ...(existing.args ?? [])].map(quoteArg).join(' ') : '';
  const line = await vscode.window.showInputBox({
    title: `${step(2)}: the command`,
    prompt:
      'The program and its arguments, run directly (no shell). A program name found on PATH, or an absolute path. ' +
      'Never put a secret here: the command line is stored and shown.',
    value: current,
    placeHolder: 'npx -y some-mcp-server@1.2.3',
    ignoreFocusOut: true,
    validateInput: (v) => {
      const { args, error } = splitArgs(v);
      if (error) {
        return error;
      }
      if (args.length === 0) {
        return 'Give the program to run';
      }
      if (/[\\/]/.test(args[0]) && !isAbsolute(args[0])) {
        return 'A relative path would mean a different program depending on where the daemon started. Give a name on PATH or an absolute path';
      }
      return undefined;
    },
  });
  if (line === undefined) {
    return undefined;
  }
  const [command, ...args] = splitArgs(line).args;

  const envLine = await vscode.window.showInputBox({
    title: `${step(3)}: variables it reads (optional)`,
    prompt:
      "NAMES of environment variables to pass through from the daemon's environment, separated by spaces or commas. " +
      'Never a value: GITHUB_TOKEN, not GITHUB_TOKEN=…',
    value: (existing?.env ?? []).join(' '),
    placeHolder: 'GITHUB_TOKEN',
    ignoreFocusOut: true,
    validateInput: (v) => {
      for (const n of v.split(/[\s,]+/).filter(Boolean)) {
        if (n.includes('=')) {
          // The value is not repeated back: this box may be on a shared screen.
          return `${n.split('=')[0]} carries a value. Give the variable's NAME only; its value stays in your environment`;
        }
        if (!validEnvName(n)) {
          return `${n} is not a variable Mochiii will pass to a server`;
        }
      }
      return undefined;
    },
  });
  if (envLine === undefined) {
    return undefined;
  }
  const env = [...new Set(envLine.split(/[\s,]+/).filter(Boolean))];

  const where = await vscode.window.showQuickPick(
    [
      { label: 'A private folder of its own', description: 'recommended', value: '' },
      {
        label: 'Inside the project',
        description: "only if it needs the project's files as its working folder",
        value: MCP_CWD_WORKSPACE,
      },
    ],
    {
      title: `${step(4)}: where it starts`,
      placeHolder: 'A launcher like npx or uvx reads the folder it starts in; a private one keeps a project from choosing what it runs',
      ignoreFocusOut: true,
    },
  );
  if (!where) {
    return undefined;
  }
  return { name: name.trim(), command, args, env, cwd: where.value };
}

function isAbsolute(p: string): boolean {
  return p.startsWith('/') || /^[A-Za-z]:[\\/]/.test(p) || p.startsWith('\\\\');
}

function quoteArg(a: string): string {
  return /^[A-Za-z0-9@%+=:,./_-]+$/.test(a) ? a : `"${a.replace(/(["\\])/g, '\\$1')}"`;
}

/** maxKeyBytes is the daemon's bound on one value (daemon/mcpenv.go). */
const MAX_KEY_BYTES = 8 << 10;

/**
 * setServerKey asks for the value of a variable a server reads, keeps it in
 * SecretStorage and gives it to the running daemon. Undefined name asks which,
 * when the server reads more than one.
 */
async function setServerKey(
  context: vscode.ExtensionContext,
  clientName: string,
  provider: McpServersProvider,
  server: MCPServerInfo,
  name?: string,
): Promise<void> {
  const names = server.env ?? [];
  if (names.length === 0) {
    void vscode.window.showInformationMessage(
      `${server.name} reads no variables. If it needs a key, add the variable's name with Change… (step 3), then set it here.`,
    );
    return;
  }
  let chosen = name && names.includes(name) ? name : undefined;
  if (!chosen) {
    if (names.length === 1) {
      chosen = names[0];
    } else {
      const kept = new Set(server.env_stored ?? storedMcpEnvNames());
      const missing = new Set(server.env_missing ?? []);
      const pick = await vscode.window.showQuickPick(
        names.map((n) => ({
          label: n,
          description: missing.has(n) ? 'no value yet' : kept.has(n) ? 'kept by Mochiii' : 'from VS Code’s environment',
        })),
        { title: `Set a key for ${server.name}`, placeHolder: 'Which variable?', ignoreFocusOut: true },
      );
      chosen = pick?.label;
    }
  }
  if (!chosen) {
    return;
  }
  const daemonKeeps = !!provider.current.secrets;
  const kept = (server.env_stored ?? storedMcpEnvNames()).includes(chosen);
  const value = await vscode.window.showInputBox({
    title: `${chosen} for ${server.name}`,
    prompt:
      (daemonKeeps
        ? `Kept for ${server.name} in ~/.mochiii/mcp-secrets.json, readable only by you, and never shown to the model. `
        : `Kept in VS Code's secret storage on this machine and given only to servers that read ${chosen}. `) +
      (kept ? 'Leave it empty to forget the stored value.' : 'Paste the value; it is not shown.'),
    password: true,
    ignoreFocusOut: true,
    validateInput: (v) => {
      if (v.length > MAX_KEY_BYTES) {
        return `Longer than ${MAX_KEY_BYTES} characters`;
      }
      // eslint-disable-next-line no-control-regex
      if (/[\u0000-\u001f\u007f]/.test(v)) {
        return 'Contains a line break or another control character';
      }
      if (!v.trim() && !kept) {
        return 'Paste the value, or press Escape to cancel';
      }
      return undefined;
    },
  });
  if (value === undefined) {
    return;
  }
  const trimmed = value.trim();
  if (daemonKeeps) {
    // The daemon keeps it, for this server only, where every client sees it.
    try {
      const { resp } = await sendMCP(clientName, { action: 'set_env', name: server.name, env_name: chosen, env_value: trimmed });
      if (resp.error) {
        void vscode.window.showErrorMessage(resp.error);
      } else if (resp.detail) {
        void vscode.window.showInformationMessage(resp.detail);
      }
    } catch (err) {
      void vscode.window.showErrorMessage(`Could not reach the Mochiii daemon, so ${chosen} was not saved: ${(err as Error).message}`);
    }
    await provider.refresh();
    return;
  }
  await storeMcpEnv(context, chosen, trimmed);
  if (!provider.current.setEnv) {
    void vscode.window.showInformationMessage(
      `${trimmed ? 'Saved' : 'Forgot'} ${chosen}. This daemon predates server keys, so ${RESTART_HINT} to give it to ${server.name}.`,
    );
    return;
  }
  try {
    const { resp } = await sendMCP(clientName, { action: 'set_env', env_name: chosen, env_value: trimmed });
    if (resp.error) {
      void vscode.window.showErrorMessage(resp.error);
    } else if (resp.detail) {
      void vscode.window.showInformationMessage(resp.detail);
    }
  } catch (err) {
    void vscode.window.showErrorMessage(`Saved ${chosen}, but could not reach the Mochiii daemon: ${(err as Error).message}`);
  }
  await provider.refresh();
}

/**
 * offerKeyFor is said after a server is added or changed: when it reads a key
 * Mochiii has no value for, it will exit on its first start, so the user is
 * offered the one step that fixes it rather than finding out from a failed turn.
 */
async function offerKeyFor(
  context: vscode.ExtensionContext,
  clientName: string,
  provider: McpServersProvider,
  name: string,
): Promise<void> {
  const server = provider.current.servers.find((s) => s.name === name);
  const missing = server?.env_missing ?? [];
  if (!server || missing.length === 0 || !provider.current.setEnv) {
    return;
  }
  const SET = 'Set key…';
  const pick = await vscode.window.showWarningMessage(
    `${name} reads ${missing.join(', ')}, which Mochiii has no value for. Most servers exit without their key.`,
    SET,
  );
  if (pick === SET) {
    await setServerKey(context, clientName, provider, server, missing[0]);
  }
}

const MIGRATED_KEY = 'mochiii.mcpEnvMigrated';

/**
 * migrateKeysToDaemon moves the keys this extension kept in SecretStorage (before
 * the daemon kept them) into the daemon's key file, once. Each goes as a value
 * every server reading that variable gets -- what it meant here. Only when every
 * one is stored is the SecretStorage copy deleted; a failure leaves it for the
 * next try, so a key is never lost on the way.
 */
export async function migrateKeysToDaemon(
  context: vscode.ExtensionContext,
  send: (name: string, value: string) => Promise<string | undefined>,
): Promise<number> {
  if (context.globalState.get<boolean>(MIGRATED_KEY)) {
    return 0;
  }
  const held = cachedMcpEnv();
  const names = Object.keys(held);
  for (const name of names) {
    const error = await send(name, held[name]);
    if (error) {
      return 0;
    }
  }
  for (const name of names) {
    await storeMcpEnv(context, name, '');
  }
  await context.globalState.update(MIGRATED_KEY, true);
  return names.length;
}

/** registerMcpServers contributes the view and its commands. */
export function registerMcpServers(
  context: vscode.ExtensionContext,
  provider: McpServersProvider,
  clientName: string,
): void {
  const view = vscode.window.createTreeView(MCP_SERVERS_VIEW_ID, { treeDataProvider: provider, showCollapseAll: false });
  // A row in this tree passes its node; the chat's Customize page passes the
  // server's NAME, which is looked up in what the daemon last listed -- so a
  // name no listed server has does nothing.
  const serverOf = (node?: McpNode | string): MCPServerInfo | undefined =>
    typeof node === 'string'
      ? provider.current.servers.find((s) => s.name === node)
      : node?.kind === 'server'
        ? node.server
        : undefined;

  const toggle = async (node: McpNode | string | undefined, action: 'enable' | 'disable') => {
    const server = serverOf(node);
    if (!server) {
      return;
    }
    try {
      const { resp } = await sendMCP(clientName, { action, name: server.name });
      if (resp.error) {
        void vscode.window.showErrorMessage(resp.error);
      } else {
        void vscode.window.showInformationMessage([resp.detail, ...(resp.notes ?? [])].filter(Boolean).join(' '));
      }
    } catch (err) {
      void vscode.window.showErrorMessage(`Could not reach the Mochiii daemon: ${(err as Error).message}`);
    }
    await provider.refresh();
  };

  // Once a daemon that keeps keys answers, the keys kept here move to it.
  let migrating = false;
  context.subscriptions.push(
    provider.onDidChangeState((state) => {
      if (!state.secrets || migrating || context.globalState.get<boolean>(MIGRATED_KEY)) {
        return;
      }
      migrating = true;
      void migrateKeysToDaemon(context, async (name, value) => {
        try {
          const { resp } = await sendMCP(clientName, { action: 'set_env', env_name: name, env_value: value });
          return resp.error;
        } catch (err) {
          return (err as Error).message;
        }
      })
        .then((n) => (n > 0 ? provider.refresh() : undefined))
        .finally(() => {
          migrating = false;
        });
    }),
  );

  context.subscriptions.push(
    view,
    provider,
    vscode.commands.registerCommand('mochiii.mcp.refresh', () => provider.refresh()),
    vscode.commands.registerCommand('mochiii.mcp.add', async () => {
      if (provider.current.noFolder) {
        void vscode.window.showInformationMessage(provider.current.noFolder);
        return;
      }
      const host = await popup();
      if (host) {
        host.openAdd();
        return;
      }
      const spec = await askForServer();
      if (spec) {
        const done = await storeServer(clientName, spec, { action: 'add' });
        await provider.refresh();
        if (done) {
          await offerKeyFor(context, clientName, provider, spec.name);
        }
      }
    }),
    vscode.commands.registerCommand('mochiii.mcp.edit', async (node?: McpNode | string) => {
      const server = serverOf(node);
      if (!server) {
        return;
      }
      const host = await popup();
      if (host) {
        host.openEdit(server);
        return;
      }
      const spec = await askForServer(server);
      if (spec) {
        const done = await storeServer(clientName, spec, { action: 'update' });
        await provider.refresh();
        if (done) {
          await offerKeyFor(context, clientName, provider, spec.name);
        }
      }
    }),
    // Not in the palette: the marketplace runs it after an add.
    vscode.commands.registerCommand('mochiii.mcp.offerKey', (name: unknown) =>
      typeof name === 'string' ? offerKeyFor(context, clientName, provider, name) : undefined,
    ),
    vscode.commands.registerCommand('mochiii.mcp.setKey', async (node?: McpNode | string, envName?: string) => {
      const server = serverOf(node);
      const reads = (server?.env ?? []).length + (server?.headers ?? []).length;
      const host = server && reads > 0 && provider.current.setEnv ? await popup() : undefined;
      if (server && host) {
        host.openKey(server, typeof envName === 'string' ? envName : undefined);
        return;
      }
      if (server) {
        await setServerKey(context, clientName, provider, server, typeof envName === 'string' ? envName : undefined);
      }
    }),
    // Start one server now and say how it fared, as `claude mcp list` does.
    vscode.commands.registerCommand('mochiii.mcp.check', async (node?: McpNode | string) => {
      const server = serverOf(node);
      if (!server) {
        return;
      }
      await vscode.window.withProgress(
        { location: vscode.ProgressLocation.Notification, title: `Starting ${server.name} to see whether it works…` },
        async () => {
          try {
            const { resp } = await sendMCP(clientName, { action: 'check', name: server.name });
            const text = resp.error ?? [resp.detail ?? '', ...(resp.notes ?? [])].filter(Boolean).join(' ');
            if (resp.error || (resp.servers ?? []).some((s) => s.name === server.name && s.state === MCP_STATE_FAILED)) {
              void vscode.window.showWarningMessage(text);
            } else {
              void vscode.window.showInformationMessage(text);
            }
          } catch (err) {
            void vscode.window.showErrorMessage(`Could not reach the Mochiii daemon: ${(err as Error).message}`);
          }
        },
      );
      await provider.refresh();
    }),
    vscode.commands.registerCommand('mochiii.mcp.enable', (node?: McpNode | string) => toggle(node, 'enable')),
    vscode.commands.registerCommand('mochiii.mcp.disable', (node?: McpNode | string) => toggle(node, 'disable')),
    vscode.commands.registerCommand('mochiii.mcp.remove', async (node?: McpNode | string) => {
      const server = serverOf(node);
      if (!server) {
        return;
      }
      const ok = await vscode.window.showWarningMessage(
        `Remove the MCP server "${server.name}"?`,
        { modal: true, detail: `${server.command}\n\nIt is not started again. A turn already running keeps its copy until it ends.` },
        'Remove',
      );
      if (ok !== 'Remove') {
        return;
      }
      try {
        const { resp } = await sendMCP(clientName, { action: 'remove', name: server.name });
        if (resp.error) {
          void vscode.window.showErrorMessage(resp.error);
        } else if (resp.detail) {
          void vscode.window.showInformationMessage(resp.detail);
        }
      } catch (err) {
        void vscode.window.showErrorMessage(`Could not reach the Mochiii daemon: ${(err as Error).message}`);
      }
      await provider.refresh();
    }),
    // The tool report starts every server to ask it, so it is a command the
    // user runs, never something the view does on its own.
    vscode.commands.registerCommand('mochiii.mcp.showTools', async () => {
      try {
        const { resp } = await sendMCP(clientName, { action: 'list' });
        const text = resp.error ? resp.error : [resp.report ?? '', ...(resp.notes ?? []).map((n) => 'NOTE: ' + n)].join('\n');
        const doc = await vscode.workspace.openTextDocument({ content: text, language: 'plaintext' });
        await vscode.window.showTextDocument(doc, { preview: true });
      } catch (err) {
        void vscode.window.showErrorMessage(`Could not reach the Mochiii daemon: ${(err as Error).message}`);
      }
    }),
  );
}
