// THE MCP MARKETPLACE: browse the mochiii.in catalog and add a server to the
// daemon, from an editor tab.
//
// The sidebar's "MCP Servers" view manages what is installed; this is where a
// user goes to find more ("Browse for more"). It is a webview because browsing
// thousands of cards with search and tiers is what a webview is for -- and
// because it is a webview, three rules keep it from becoming a way around the
// daemon's own:
//
//   1. THE WEBVIEW NEVER HOLDS THE CATALOG. The extension host fetches it,
//      checks every entry (mcpCatalog.ts), keeps it, and answers the webview's
//      searches a page at a time. The webview has no connect-src at all.
//   2. "ADD" CARRIES AN ID, NOTHING ELSE. The host looks the server up in its
//      own checked copy and builds the request from that, so nothing the
//      webview says -- and nothing a vscode:// link says -- chooses a program.
//   3. THE INSTALL IS THE SAME TWO STEPS AS EVERYWHERE ELSE (mcpInstall.ts):
//      the daemon's warning, then the server's name typed. A marketplace puts
//      every listed server one click away, which is exactly why the click is
//      not the confirmation.
//
// A one-time notice before the first open says what the marketplace is, in the
// words of docs/MCP_LANE_B_THREAT_MODEL.md: these programs run unconfined.

import * as fs from 'fs/promises';
import * as path from 'path';
import * as vscode from 'vscode';
import { MCP_SOURCE_USER } from './daemonClient';
import {
  Catalog,
  CatalogServer,
  MAX_CATALOG_BYTES,
  MAX_COMMUNITY_BYTES,
  catalogUrlFrom,
  fetchCatalogText,
  parseCatalog,
  validServerName,
} from './mcpCatalog';
import { storeServer } from './mcpInstall';
import { MCP_SERVERS_VIEW_ID, McpServersProvider, McpServersState } from './mcpServersView';

export const MARKETPLACE_VIEW_TYPE = 'mochiiiMcpMarketplace';
export const NOTICE_ACCEPTED_KEY = 'mochiii.mcp.marketplaceNoticeAccepted';
const PAGE = 40;

/** One card as the webview draws it: display fields only, never the means to run it. */
export interface CardView {
  id: string;
  name: string;
  description: string;
  publisher: string;
  namespace?: string;
  tier: 'verified' | 'community';
  categories: string[];
  pkg: string;
  runtime: string;
  commandLine: string;
  env: { name: string; required: boolean; secret: boolean; description?: string }[];
  hasHomepage: boolean;
  hasPackageUrl: boolean;
  /** '' (not added), 'added', 'disabled', or 'taken' (a config server has the name). */
  installed: '' | 'added' | 'disabled' | 'taken';
}

const RUNTIME: Record<CatalogServer['package']['registry'], string> = {
  npm: 'Node.js (npx)',
  pypi: 'uv (uvx)',
  oci: 'Docker',
};

function quote(a: string): string {
  return /^[A-Za-z0-9@%+=:,./_-]+$/.test(a) ? a : `'${a.replace(/'/g, `'\\''`)}'`;
}

export function cardFor(s: CatalogServer, state: McpServersState): CardView {
  const mine = state.servers.find((x) => x.name === s.id);
  const installed: CardView['installed'] = !mine ? '' : mine.source !== MCP_SOURCE_USER ? 'taken' : mine.disabled ? 'disabled' : 'added';
  return {
    id: s.id,
    name: s.name,
    description: s.description,
    publisher: s.publisher,
    namespace: s.namespace,
    tier: s.tier,
    categories: s.categories,
    pkg: `${s.package.registry} · ${s.package.identifier} ${s.package.version}`,
    runtime: RUNTIME[s.package.registry],
    commandLine: [s.launch.command, ...s.launch.args].map(quote).join(' '),
    env: s.env,
    hasHomepage: !!s.homepage,
    hasPackageUrl: !!s.package.url,
    installed,
  };
}

/** matches is the search: every word somewhere in what a person would search by. */
export function matches(s: CatalogServer, query: string, category: string): boolean {
  if (category && !s.categories.includes(category)) {
    return false;
  }
  const hay = `${s.name} ${s.id} ${s.publisher} ${s.namespace ?? ''} ${s.description} ${s.package.identifier}`.toLowerCase();
  return query
    .toLowerCase()
    .split(/\s+/)
    .filter(Boolean)
    .every((w) => hay.includes(w));
}

/** The provenance line said before the daemon's own warning. */
export function provenanceOf(s: CatalogServer): string {
  const pkg = `${s.package.registry} package ${s.package.identifier} ${s.package.version}`;
  if (s.tier === 'verified') {
    return `From the Mochiii MCP marketplace: ${s.name} by ${s.publisher}, an official server Mochiii chose and reviewed (${pkg}).`;
  }
  return (
    `From the Mochiii MCP marketplace, COMMUNITY tier: ${s.name} is listed in the public MCP Registry` +
    (s.namespace ? ` as ${s.namespace}` : '') +
    ` and has NOT been reviewed by Mochiii (${pkg}). Check the package before you go on.`
  );
}

/** acceptNotice shows the one-time notice; true when the user chose to go on. */
async function acceptNotice(context: vscode.ExtensionContext): Promise<boolean> {
  if (context.globalState.get<boolean>(NOTICE_ACCEPTED_KEY)) {
    return true;
  }
  const open = 'Open the Marketplace';
  const choice = await vscode.window.showInformationMessage(
    'The MCP marketplace lists servers made by other people.',
    {
      modal: true,
      detail:
        'A server you add runs on your machine with your full privileges: Mochiii cannot confine it. Before anything is ' +
        "added, Mochiii shows you exactly what will run and asks you to type the server's name; after that, each of its " +
        'tools asks before it runs, and every call is audited.\n\n' +
        'Verified servers are official ones Mochiii chose. Community servers come from the public MCP Registry and have ' +
        'not been reviewed.',
    },
    open,
  );
  if (choice !== open) {
    return false;
  }
  await context.globalState.update(NOTICE_ACCEPTED_KEY, true);
  return true;
}

function nonce(): string {
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
  let out = '';
  for (let i = 0; i < 32; i++) {
    out += chars.charAt(Math.floor(Math.random() * chars.length));
  }
  return out;
}

interface Query {
  q: string;
  tier: 'verified' | 'community' | 'all';
  category: string;
  offset: number;
  /** How many from offset; the webview asks for everything it shows when it redraws. */
  limit: number;
}

export class McpMarketplace {
  private static current: McpMarketplace | undefined;

  private catalog: Catalog | undefined;
  private community: Catalog | undefined;
  private communityLoad: Promise<void> | undefined;
  private lastQuery: Query = { q: '', tier: 'verified', category: '', offset: 0, limit: PAGE };
  private readonly disposables: vscode.Disposable[] = [];

  /** show opens (or reveals) the marketplace; focusId puts one server first. */
  static async show(
    context: vscode.ExtensionContext,
    provider: McpServersProvider,
    clientName: string,
    focusId?: string,
  ): Promise<void> {
    if (!(await acceptNotice(context))) {
      return;
    }
    if (McpMarketplace.current) {
      McpMarketplace.current.panel.reveal();
      if (focusId) {
        await McpMarketplace.current.focus(focusId);
      }
      return;
    }
    const panel = vscode.window.createWebviewPanel(MARKETPLACE_VIEW_TYPE, 'MCP Marketplace', vscode.ViewColumn.Active, {
      enableScripts: true,
      localResourceRoots: [vscode.Uri.joinPath(context.extensionUri, 'media')],
      retainContextWhenHidden: true,
    });
    McpMarketplace.current = new McpMarketplace(panel, context, provider, clientName, focusId);
  }

  private constructor(
    private readonly panel: vscode.WebviewPanel,
    private readonly context: vscode.ExtensionContext,
    private readonly provider: McpServersProvider,
    private readonly clientName: string,
    private pendingFocus: string | undefined,
  ) {
    panel.iconPath = vscode.Uri.joinPath(context.extensionUri, 'media', 'activity-icon.svg');
    panel.webview.html = marketplaceHtml(panel.webview, context.extensionUri);
    this.disposables.push(
      panel.webview.onDidReceiveMessage((msg) => void this.onMessage(msg)),
      // What is installed changed: the webview re-asks for what it shows, so
      // each card's Add button says the truth.
      provider.onDidChangeState(() => this.post({ type: 'stateChanged' })),
      panel.onDidDispose(() => this.dispose()),
    );
  }

  private dispose(): void {
    McpMarketplace.current = undefined;
    for (const d of this.disposables) {
      d.dispose();
    }
  }

  private post(msg: unknown): void {
    void this.panel.webview.postMessage(msg);
  }

  private async onMessage(msg: any): Promise<void> {
    switch (msg?.type) {
      case 'ready':
        await this.loadCatalog();
        void this.provider.refresh();
        return;
      case 'refresh':
        this.community = undefined;
        this.communityLoad = undefined;
        await this.loadCatalog();
        return;
      case 'query':
        await this.answer({
          q: String(msg.q ?? '').slice(0, 200),
          tier: msg.tier === 'community' || msg.tier === 'all' ? msg.tier : 'verified',
          category: String(msg.category ?? '').slice(0, 40),
          offset: Number.isInteger(msg.offset) && msg.offset >= 0 ? msg.offset : 0,
          limit: Number.isInteger(msg.limit) ? Math.min(Math.max(msg.limit, 1), 400) : PAGE,
        });
        return;
      case 'install':
        await this.install(String(msg.id ?? ''));
        return;
      case 'open':
        await this.openLink(String(msg.id ?? ''), msg.which === 'package' ? 'package' : 'homepage');
        return;
      case 'manage':
        await vscode.commands.executeCommand(`${MCP_SERVERS_VIEW_ID}.focus`);
        return;
    }
  }

  private lookup(id: string): CatalogServer | undefined {
    return this.catalog?.servers.find((s) => s.id === id) ?? this.community?.servers.find((s) => s.id === id);
  }

  private cachePath(file: string): string {
    return path.join(this.context.globalStorageUri.fsPath, file);
  }

  /** load fetches one catalog file, falling back to the last copy that checked out. */
  private async load(url: string, file: string, tier: CatalogServer['tier'], maxBytes: number): Promise<Catalog> {
    try {
      const text = await fetchCatalogText(url, { maxBytes });
      const catalog = parseCatalog(text, url, { tier, baseUrl: url });
      if (catalog.servers.length === 0) {
        throw new Error(`the catalog at ${url} held no server this extension could offer`);
      }
      await fs.mkdir(this.context.globalStorageUri.fsPath, { recursive: true });
      await fs.writeFile(this.cachePath(file), text, 'utf8');
      return catalog;
    } catch (err) {
      try {
        const text = await fs.readFile(this.cachePath(file), 'utf8');
        const stat = await fs.stat(this.cachePath(file));
        const saved = parseCatalog(text, `a saved copy from ${stat.mtime.toLocaleString()}`, { tier, baseUrl: url });
        this.post({ type: 'notice', kind: 'stale', text: `Showing ${saved.source}: ${(err as Error).message}` });
        return saved;
      } catch {
        throw err;
      }
    }
  }

  private async loadCatalog(): Promise<void> {
    const { url, warning } = catalogUrlFrom(vscode.workspace.getConfiguration('mochiii').get<string>('mcp.catalogUrl'));
    if (warning) {
      this.post({ type: 'notice', kind: 'warning', text: warning });
    }
    this.post({ type: 'loading', what: 'catalog' });
    try {
      this.catalog = await this.load(url, 'mcp-catalog.json', 'verified', MAX_CATALOG_BYTES);
    } catch (err) {
      this.post({ type: 'notice', kind: 'error', text: `The MCP catalog could not be loaded: ${(err as Error).message}` });
      return;
    }
    const c = this.catalog;
    this.post({
      type: 'catalog',
      generatedAt: c.generatedAt,
      source: c.source,
      rejected: c.rejected,
      verifiedCount: c.servers.length,
      communityCount: c.community?.count ?? 0,
      categories: [...new Set(c.servers.flatMap((s) => s.categories))].sort(),
    });
    if (this.pendingFocus) {
      const id = this.pendingFocus;
      this.pendingFocus = undefined;
      await this.focus(id);
    }
    await this.answer(this.lastQuery);
  }

  private loadCommunity(): Promise<void> {
    if (this.community || !this.catalog?.community) {
      return Promise.resolve();
    }
    if (!this.communityLoad) {
      const { url } = this.catalog.community;
      this.post({ type: 'loading', what: 'community' });
      this.communityLoad = this.load(url, 'mcp-community.json', 'community', MAX_COMMUNITY_BYTES).then(
        (c) => {
          this.community = c;
        },
        (err: Error) => {
          this.communityLoad = undefined;
          this.post({ type: 'notice', kind: 'error', text: `The community list could not be loaded: ${err.message}` });
        },
      );
    }
    return this.communityLoad;
  }

  private async answer(query: Query): Promise<void> {
    this.lastQuery = query;
    if (!this.catalog) {
      return;
    }
    if (query.tier !== 'verified') {
      await this.loadCommunity();
    }
    const pool =
      query.tier === 'verified'
        ? this.catalog.servers
        : query.tier === 'community'
          ? this.community?.servers ?? []
          : [...this.catalog.servers, ...(this.community?.servers ?? [])];
    const hits = pool.filter((s) => matches(s, query.q, query.category));
    const state = this.provider.current;
    this.post({
      type: 'results',
      query,
      total: hits.length,
      items: hits.slice(query.offset, query.offset + query.limit).map((s) => cardFor(s, state)),
      communityReady: !!this.community,
    });
  }

  /** focus puts one server at the top of the page -- what a mochiii.in link asks for. It never installs. */
  private async focus(id: string): Promise<void> {
    if (!this.catalog) {
      this.pendingFocus = id;
      return;
    }
    let server = this.lookup(id);
    if (!server && this.catalog.community) {
      await this.loadCommunity();
      server = this.lookup(id);
    }
    this.post(
      server
        ? { type: 'focus', item: cardFor(server, this.provider.current) }
        : { type: 'notice', kind: 'warning', text: `The link asked for a server named "${id}", which this catalog does not list.` },
    );
  }

  private async install(id: string): Promise<void> {
    const server = this.lookup(id);
    if (!server) {
      void vscode.window.showErrorMessage(`The marketplace does not list a server named "${id}".`);
      return;
    }
    const existing = this.provider.current.servers.find((s) => s.name === server.id);
    if (existing) {
      void vscode.window.showInformationMessage(
        existing.source === MCP_SOURCE_USER
          ? `${server.id} is already added. Change or remove it in the MCP Servers view.`
          : `A server named ${server.id} is already defined in the daemon's models.json.`,
      );
      await vscode.commands.executeCommand(`${MCP_SERVERS_VIEW_ID}.focus`);
      return;
    }
    this.post({ type: 'busy', id, busy: true });
    try {
      await storeServer(
        this.clientName,
        {
          name: server.id,
          command: server.launch.command,
          args: server.launch.args,
          env: server.env.map((e) => e.name),
          cwd: server.cwd ?? '',
        },
        { action: 'add', provenance: provenanceOf(server), envHelp: server.env },
      );
    } finally {
      this.post({ type: 'busy', id, busy: false });
      await this.provider.refresh();
    }
  }

  private async openLink(id: string, which: 'homepage' | 'package'): Promise<void> {
    const server = this.lookup(id);
    const url = which === 'package' ? server?.package.url : server?.homepage;
    if (url) {
      await vscode.env.openExternal(vscode.Uri.parse(url));
    }
  }
}

/** handleMarketplaceUri answers vscode://<this extension>/mcp/install?id=<name>, from mochiii.in. */
export async function handleMarketplaceUri(
  uri: vscode.Uri,
  context: vscode.ExtensionContext,
  provider: McpServersProvider,
  clientName: string,
): Promise<void> {
  if (uri.path !== '/mcp/install') {
    return;
  }
  const id = new URLSearchParams(uri.query).get('id') ?? '';
  // Only a name the daemon would accept is even looked up; anything else in the
  // link is not read at all.
  if (!validServerName(id)) {
    void vscode.window.showWarningMessage('That link does not name an MCP server Mochiii can add.');
    return;
  }
  await McpMarketplace.show(context, provider, clientName, id);
}

function marketplaceHtml(webview: vscode.Webview, extensionUri: vscode.Uri): string {
  const script = webview.asWebviewUri(vscode.Uri.joinPath(extensionUri, 'media', 'marketplace.js'));
  const n = nonce();
  return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src ${webview.cspSource}; style-src ${webview.cspSource} 'unsafe-inline'; script-src 'nonce-${n}';">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>MCP Marketplace</title>
<style>${mcpMarketplaceStyles()}</style>
</head>
<body>
<main id="app" aria-live="polite"></main>
<script nonce="${n}" src="${script}"></script>
</body>
</html>`;
}

/** The marketplace's stylesheet: every colour from the user's theme (scripts/theme-check.js scans this). */
export function mcpMarketplaceStyles(): string {
  return `
  :root {
    --mk-bg: var(--vscode-editor-background);
    --mk-ink: var(--vscode-editor-foreground);
    --mk-muted: var(--vscode-descriptionForeground);
    --mk-border: var(--vscode-widget-border, var(--vscode-panel-border));
    --mk-card: var(--vscode-sideBar-background, var(--vscode-editor-background));
    --mk-input-bg: var(--vscode-input-background);
    --mk-input-fg: var(--vscode-input-foreground);
    --mk-input-border: var(--vscode-input-border, var(--vscode-widget-border));
    --mk-focus: var(--vscode-focusBorder);
    --mk-btn-bg: var(--vscode-button-background);
    --mk-btn-fg: var(--vscode-button-foreground);
    --mk-btn-hover: var(--vscode-button-hoverBackground);
    --mk-btn2-bg: var(--vscode-button-secondaryBackground);
    --mk-btn2-fg: var(--vscode-button-secondaryForeground);
    --mk-btn2-hover: var(--vscode-button-secondaryHoverBackground);
    --mk-code-bg: var(--vscode-textCodeBlock-background);
    --mk-ok: var(--vscode-testing-iconPassed);
    --mk-warn: var(--vscode-editorWarning-foreground);
    --mk-err: var(--vscode-errorForeground);
    --mk-badge-bg: var(--vscode-badge-background);
    --mk-badge-fg: var(--vscode-badge-foreground);
    --mk-link: var(--vscode-textLink-foreground);
  }
  body { margin: 0; padding: 0 20px 40px; background: var(--mk-bg); color: var(--mk-ink);
    font-family: var(--vscode-font-family); font-size: var(--vscode-font-size); }
  h1 { font-size: 1.6em; margin: 20px 0 4px; font-weight: 600; }
  .sub { color: var(--mk-muted); margin: 0 0 14px; max-width: 760px; line-height: 1.5; }
  .bar { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin: 12px 0; }
  .bar input[type=search] { flex: 1 1 260px; min-width: 0; padding: 6px 8px; background: var(--mk-input-bg);
    color: var(--mk-input-fg); border: 1px solid var(--mk-input-border); border-radius: 2px; font: inherit; }
  .bar input:focus, .bar select:focus, button:focus-visible { outline: 1px solid var(--mk-focus); outline-offset: 1px; }
  .bar select { padding: 5px 6px; background: var(--mk-input-bg); color: var(--mk-input-fg);
    border: 1px solid var(--mk-input-border); font: inherit; }
  .tabs { display: inline-flex; border: 1px solid var(--mk-border); border-radius: 3px; overflow: hidden; }
  .tabs button { border: none; background: transparent; color: var(--mk-muted); padding: 5px 12px; cursor: pointer; font: inherit; }
  .tabs button[aria-selected=true] { background: var(--mk-btn2-bg); color: var(--mk-btn2-fg); }
  button.btn { border: 1px solid transparent; padding: 4px 12px; border-radius: 2px; cursor: pointer; font: inherit;
    background: var(--mk-btn-bg); color: var(--mk-btn-fg); }
  button.btn:hover { background: var(--mk-btn-hover); }
  button.btn.secondary { background: var(--mk-btn2-bg); color: var(--mk-btn2-fg); }
  button.btn.secondary:hover { background: var(--mk-btn2-hover); }
  button.btn:disabled { opacity: 0.6; cursor: default; }
  button.link { border: none; background: none; color: var(--mk-link); cursor: pointer; padding: 0; font: inherit; }
  .notice { padding: 8px 12px; border-left: 3px solid var(--mk-warn); background: var(--mk-code-bg); margin: 8px 0; }
  .notice.error { border-left-color: var(--mk-err); }
  .safety { color: var(--mk-muted); font-size: 0.92em; margin: 6px 0 12px; line-height: 1.5; }
  .count { color: var(--mk-muted); font-size: 0.9em; margin: 4px 0 10px; }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: 12px; }
  .card { border: 1px solid var(--mk-border); background: var(--mk-card); border-radius: 4px; padding: 14px;
    display: flex; flex-direction: column; gap: 8px; min-width: 0; }
  .card.focused { border-color: var(--mk-focus); }
  .head { display: flex; justify-content: space-between; gap: 8px; align-items: flex-start; }
  .head h2 { font-size: 1.1em; margin: 0; overflow-wrap: anywhere; }
  .pub { color: var(--mk-muted); font-size: 0.88em; }
  .tier { flex: none; font-size: 0.78em; padding: 1px 7px; border-radius: 10px; background: var(--mk-badge-bg); color: var(--mk-badge-fg); }
  .tier.verified { background: transparent; color: var(--mk-ok); border: 1px solid var(--mk-ok); }
  .desc { margin: 0; line-height: 1.45; }
  .meta { color: var(--mk-muted); font-size: 0.85em; overflow-wrap: anywhere; }
  .env { display: flex; flex-wrap: wrap; gap: 4px; }
  .env span { font-family: var(--vscode-editor-font-family); font-size: 0.85em; border: 1px solid var(--mk-border);
    border-radius: 3px; padding: 0 5px; }
  .env span.req { border-color: var(--mk-warn); }
  code.cmd { display: block; font-family: var(--vscode-editor-font-family); font-size: 0.85em; background: var(--mk-code-bg);
    padding: 6px 8px; border-radius: 3px; overflow-x: auto; white-space: nowrap; }
  .prov { color: var(--mk-muted); font-size: 0.85em; margin: 0; overflow-wrap: anywhere; }
  .actions { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-top: auto; }
  .more { display: flex; justify-content: center; margin: 16px 0; }
  .foot { color: var(--mk-muted); font-size: 0.85em; margin-top: 18px; }
  .focusbox { margin: 10px 0 16px; }
  .focusbox h3 { font-size: 0.95em; margin: 0 0 6px; color: var(--mk-muted); font-weight: 500; }
  `;
}
