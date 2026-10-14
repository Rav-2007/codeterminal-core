// WHAT THE CHAT'S PAGES MAY ASK THE EXTENSION TO DO, written down in one place.
//
// The pages run in the webview, the least trusted part of this extension: it
// renders model output and third-party catalog text. So a page never names a
// VS Code command, a setting key or a URL that the host then uses as given.
// It names an entry in one of the tables below, and the host looks it up. A
// name that is not in a table does nothing.
//
// No vscode import, so the tables and their checks are tested directly
// (src/test/suite/chatPagesRules.test.ts).

/**
 * Settings a page may change, and the values each accepts. Every one is about
 * how the panel looks or what the user sends -- none names where a question
 * goes or what may run. Those (mochiii.apiBase, mochiii.model,
 * mochiii.configPath, mochiii.mcp.catalogUrl) are machine-scoped so that a
 * repository cannot set them, and a page cannot either: see MACHINE_SETTINGS.
 */
export const PAGE_SETTINGS: Record<string, (v: unknown) => boolean> = {
  toolActivity: (v) => v === 'compact' || v === 'expanded',
  defaultMode: (v) => v === 'auto' || v === 'manual' || v === 'plan',
  sendEditorContext: (v) => typeof v === 'boolean',
  'history.autoSave': (v) => typeof v === 'boolean',
};

/** checkSettingUpdate says whether a page may set `key` (under "mochiii.") to `value`. */
export function checkSettingUpdate(key: unknown, value: unknown): { ok: true; key: string } | { ok: false; reason: string } {
  if (typeof key !== 'string' || !Object.prototype.hasOwnProperty.call(PAGE_SETTINGS, key)) {
    return { ok: false, reason: `the settings page may not change ${JSON.stringify(key)}` };
  }
  if (!PAGE_SETTINGS[key](value)) {
    return { ok: false, reason: `${JSON.stringify(value)} is not a value mochiii.${key} accepts` };
  }
  return { ok: true, key };
}

/**
 * The machine-scoped settings the Advanced section SHOWS. Each opens in VS
 * Code's own Settings editor; none is ever written from a page.
 */
export const MACHINE_SETTINGS: { key: string; label: string; what: string }[] = [
  { key: 'apiBase', label: 'API address', what: 'where questions are sent; empty means the provider you connected' },
  { key: 'model', label: 'Model override', what: 'one model id that replaces every configured model' },
  { key: 'configPath', label: 'Config file', what: 'a models.json of your own, for agent tools and MCP servers' },
  { key: 'mcp.catalogUrl', label: 'MCP catalog', what: 'where the marketplace’s list comes from' },
];

/** isMachineSetting says whether `key` is one of MACHINE_SETTINGS. */
export function isMachineSetting(key: unknown): key is string {
  return typeof key === 'string' && MACHINE_SETTINGS.some((m) => m.key === key);
}

/** Commands a page may run, by the name a page uses for them. */
export const PAGE_COMMANDS: Record<string, string> = {
  connect: 'mochiii.connect',
  forgetKey: 'mochiii.forgetApiKey',
  mcpAdd: 'mochiii.mcp.add',
  mcpShowTools: 'mochiii.mcp.showTools',
  marketInEditor: 'mochiii.mcp.browse',
  chooseFolder: 'mochiii.chooseWorkingFolder',
  daemonLog: 'mochiii.showDaemonLog',
  restartDaemon: 'mochiii.restartDaemon',
};

/** pageCommand maps a page's name for a command to the VS Code command, or undefined. */
export function pageCommand(name: unknown): string | undefined {
  return typeof name === 'string' && Object.prototype.hasOwnProperty.call(PAGE_COMMANDS, name) ? PAGE_COMMANDS[name] : undefined;
}

/** Links a page may open, by name. Fixed: a page never supplies a URL. */
export const PAGE_LINKS: Record<string, string> = {
  site: 'https://www.mochiii.in/',
  issues: 'https://github.com/Rav-2007/codeterminal-core/issues',
  docs: 'https://github.com/Rav-2007/codeterminal-core/tree/main/clients/vscode#readme',
};

/** pageLink maps a page's link name to its URL, or undefined. */
export function pageLink(name: unknown): string | undefined {
  return typeof name === 'string' && Object.prototype.hasOwnProperty.call(PAGE_LINKS, name) ? PAGE_LINKS[name] : undefined;
}

/** What a page's MCP row may do to a user server, mapped to the view's commands. */
export const MCP_ROW_COMMANDS: Record<string, string> = {
  enable: 'mochiii.mcp.enable',
  disable: 'mochiii.mcp.disable',
  edit: 'mochiii.mcp.edit',
  remove: 'mochiii.mcp.remove',
  setKey: 'mochiii.mcp.setKey',
  check: 'mochiii.mcp.check',
  signIn: 'mochiii.mcp.signIn',
  signOut: 'mochiii.mcp.signOut',
};


// The key-source wording, kept in step with the terminal's /account
// (clients/tui/account.go) so the two clients describe one key the same way.
export function keySourceText(source: string | undefined, localBase: boolean): string {
  switch (source) {
    case 'stored':
      return 'stored on this machine';
    case 'environment':
      return 'from MOCHIII_API_KEY in the daemon’s environment';
    case 'launcher':
      return 'handed over by VS Code from its own key store';
    case 'proxy':
      return 'proxy mode: the proxy holds the provider’s key';
    default:
      return localBase ? 'a model server on this machine asks for none' : 'none yet: the next question will ask for one';
  }
}
