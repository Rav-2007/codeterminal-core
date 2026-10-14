import * as vscode from 'vscode';
import { MCPRequest, MCPServerInfo, RESTART_HINT, sendMCP } from './daemonClient';
import { validEnvName, validServerName } from './mcpCatalog';
import { splitArgs } from './mcpInstall';
import type { McpServersProvider } from './mcpServersView';

// THE MCP POP-UP'S HOST SIDE (media/mcpPopup.js draws it).
//
// THE SAME TWO STEPS AS EVERY OTHER WAY IN. The form's request goes to the
// daemon without an acknowledgement; the daemon stores nothing and answers with
// its warning, which the pop-up shows; the user types the server's name, and
// only then is the same request sent acknowledged. The request -- values
// included -- is held here, in memory, between the two, and dropped on Back,
// Cancel, close, or the next form.
//
// A value never travels further than the daemon: it is not logged, not shown in
// a message, and not kept here after the request is answered.

export interface McpPopupDeps {
  post(msg: Record<string, unknown>): void;
  clientName(): string;
  provider(): McpServersProvider | undefined;
}

type Request = Omit<MCPRequest, 'protocol_version' | 'mcp'>;

/** The fields of a form the pop-up sent. */
export interface McpForm {
  mode: 'add' | 'update';
  tab: 'command' | 'json';
  name: string;
  commandLine: string;
  env: { name: string; value: string }[];
  cwd: string;
  json: string;
}

function isAbsolute(p: string): boolean {
  return p.startsWith('/') || /^[A-Za-z]:[\\/]/.test(p) || p.startsWith('\\\\');
}

/**
 * requestFromForm is the daemon request a form describes, or why it cannot be
 * sent. Pure, for tests. Errors name a field or variable, never a value.
 */
export function requestFromForm(form: McpForm): { req?: Request; error?: string } {
  const name = form.name.trim();
  if (!validServerName(name)) {
    return { error: "The name is 1-32 of a-z, 0-9, '-' and '_', starting with a letter or digit, and not \"builtin\"." };
  }
  if (form.tab === 'json') {
    if (!form.json.trim()) {
      return { error: "Paste the server's JSON entry." };
    }
    let json: unknown;
    try {
      json = JSON.parse(form.json);
    } catch {
      // Not quoted back: what was pasted may hold a key.
      return { error: 'That is not valid JSON. Paste one server\'s entry, such as {"command": "npx", "args": ["-y", "some-server"]}.' };
    }
    return { req: { action: 'add_json', name, json } };
  }
  const { args, error } = splitArgs(form.commandLine);
  if (error) {
    return { error: 'The command: ' + error + '.' };
  }
  if (args.length === 0) {
    return { error: 'Give the program to run.' };
  }
  if (/[\\/]/.test(args[0]) && !isAbsolute(args[0])) {
    return { error: 'The program is a relative path; give a name on PATH or an absolute path.' };
  }
  const env: string[] = [];
  const values: Record<string, string> = {};
  for (const row of form.env) {
    const n = row.name.trim();
    if (!n) {
      continue;
    }
    if (!validEnvName(n)) {
      return { error: `${n} is not a variable Mochiii will pass to a server.` };
    }
    if (!env.includes(n)) {
      env.push(n);
    }
    if (row.value.trim()) {
      values[n] = row.value.trim();
    }
  }
  const req: Request = {
    action: form.mode === 'update' ? 'update' : 'add',
    name,
    command: args[0],
    args: args.slice(1),
    env,
    ...(Object.keys(values).length > 0 ? { env_values: values } : {}),
    ...(form.cwd === 'workspace' ? { cwd: 'workspace' } : {}),
  };
  return { req };
}

function quoteArg(a: string): string {
  return /^[A-Za-z0-9@%+=:,./_-]+$/.test(a) ? a : `"${a.replace(/(["\\])/g, '\\$1')}"`;
}

export class McpPopupHost {
  private pending: Request | undefined;

  constructor(private readonly deps: McpPopupDeps) {}

  /** openAdd shows the empty form. */
  openAdd(): void {
    this.pending = undefined;
    this.deps.post({ type: 'mcpPopupOpen', mode: 'add' });
  }

  /** openEdit shows the form filled in with a server the user added. */
  openEdit(s: MCPServerInfo): void {
    this.pending = undefined;
    const stored = new Set(s.env_stored ?? []);
    this.deps.post({
      type: 'mcpPopupOpen',
      mode: 'update',
      name: s.name,
      commandLine: [s.program ?? '', ...(s.args ?? [])].map(quoteArg).join(' '),
      env: (s.env ?? []).map((n) => ({ name: n, stored: stored.has(n) })),
      cwd: s.cwd ?? '',
    });
  }

  /** openKey shows the key step for one server, on the variable named or the first missing. */
  openKey(s: MCPServerInfo, name?: string): void {
    const stored = new Set(s.env_stored ?? []);
    const missing = new Set(s.env_missing ?? []);
    this.deps.post({
      type: 'mcpPopupKeyOpen',
      server: s.name,
      name: name ?? '',
      vars: (s.env ?? []).map((n) => ({ name: n, stored: stored.has(n), missing: missing.has(n) })),
    });
  }

  /** handle answers the pop-up's messages; false for anything else. */
  handle(msg: Record<string, unknown>): boolean {
    switch (msg.type) {
      case 'mcpPopupSubmit':
        void this.submit(msg);
        return true;
      case 'mcpPopupAgree':
        void this.agree(typeof msg.typed === 'string' ? msg.typed : '');
        return true;
      case 'mcpPopupBack':
      case 'mcpPopupCancel':
        this.pending = undefined;
        return true;
      case 'mcpPopupKeySave':
        void this.saveKey(String(msg.server ?? ''), String(msg.name ?? ''), typeof msg.value === 'string' ? msg.value : '');
        return true;
    }
    return false;
  }

  private error(text: string): void {
    this.deps.post({ type: 'mcpPopupError', text });
  }

  private async submit(msg: Record<string, unknown>): Promise<void> {
    this.pending = undefined;
    const form: McpForm = {
      mode: msg.mode === 'update' ? 'update' : 'add',
      tab: msg.tab === 'json' ? 'json' : 'command',
      name: String(msg.name ?? ''),
      commandLine: String(msg.commandLine ?? ''),
      env: Array.isArray(msg.env)
        ? (msg.env as unknown[]).map((r) => {
            const row = r as Record<string, unknown>;
            return { name: String(row.name ?? ''), value: typeof row.value === 'string' ? row.value : '' };
          })
        : [],
      cwd: String(msg.cwd ?? ''),
      json: String(msg.json ?? ''),
    };
    const { req, error } = requestFromForm(form);
    if (!req) {
      this.error(error ?? 'The form is not complete.');
      return;
    }
    if (form.tab === 'json' && !this.deps.provider()?.current.check) {
      this.error(`This daemon cannot read another client's JSON yet; ${RESTART_HINT}.`);
      return;
    }
    if (req.env_values && !this.deps.provider()?.current.secrets) {
      this.error(`This daemon cannot keep keys yet; ${RESTART_HINT}, or leave the values empty and set them later.`);
      return;
    }
    try {
      const { resp } = await sendMCP(this.deps.clientName(), req);
      if (resp.error) {
        this.error(resp.error);
        return;
      }
      if (!resp.needs_acknowledgement || !resp.detail) {
        // A daemon that stored the server without asking broke the contract
        // this flow depends on (mcpInstall.ts says the same).
        this.error(`The daemon did not ask for the acknowledgement an MCP server needs; ${RESTART_HINT}.`);
        return;
      }
      this.pending = req;
      const notes = (resp.notes ?? []).map((n) => '\n\nNote: ' + n).join('');
      this.deps.post({ type: 'mcpPopupConfirm', name: req.name, detail: resp.detail + notes });
    } catch (err) {
      this.error(`Could not reach the Mochiii daemon: ${(err as Error).message}`);
    }
  }

  private async agree(typed: string): Promise<void> {
    const req = this.pending;
    if (!req || typed !== req.name) {
      this.error("Type the server's name exactly.");
      return;
    }
    this.pending = undefined;
    try {
      const { resp } = await sendMCP(this.deps.clientName(), { ...req, acknowledged_unconfined: true });
      if (resp.error || !resp.ok) {
        this.error(resp.error || resp.detail || 'The daemon did not store the server.');
        return;
      }
      this.deps.post({ type: 'mcpPopupClose' });
      void vscode.window.showInformationMessage([resp.detail ?? 'Done.', ...(resp.notes ?? [])].join(' '));
      const provider = this.deps.provider();
      await provider?.refresh();
      // A server that reads a key Mochiii has no value for exits on its first
      // start: the key step comes straight after.
      const server = provider?.current.servers.find((s) => s.name === req.name);
      if (server && (server.env_missing ?? []).length > 0 && provider?.current.setEnv) {
        this.openKey(server);
      }
    } catch (err) {
      this.error(`Could not reach the Mochiii daemon: ${(err as Error).message}`);
    }
  }

  private async saveKey(server: string, name: string, value: string): Promise<void> {
    if (!server || !name) {
      this.error('Choose the variable.');
      return;
    }
    const provider = this.deps.provider();
    // Per server when the daemon keeps keys itself; otherwise as before.
    const req: Request = provider?.current.secrets
      ? { action: 'set_env', name: server, env_name: name, env_value: value.trim() }
      : { action: 'set_env', env_name: name, env_value: value.trim() };
    try {
      const { resp } = await sendMCP(this.deps.clientName(), req);
      if (resp.error) {
        this.error(resp.error);
        return;
      }
      this.deps.post({ type: 'mcpPopupClose' });
      if (resp.detail) {
        void vscode.window.showInformationMessage(resp.detail);
      }
      await provider?.refresh();
    } catch (err) {
      this.error(`Could not reach the Mochiii daemon, so ${name} was not saved: ${(err as Error).message}`);
    }
  }
}

// THE WAY IN FROM A COMMAND. The sidebar's MCP list and the Customize page run
// mochiii.mcp.add / edit / setKey; the chat panel registers its pop-up here so
// those commands open it. A function hook, not a command that takes a value:
// nothing another extension can call carries a key in (connectBridge.ts).
let current: McpPopupHost | undefined;

export function setMcpPopupHost(host: McpPopupHost | undefined): void {
  current = host;
}

export function mcpPopupHost(): McpPopupHost | undefined {
  return current;
}
