// ADDING OR CHANGING AN MCP SERVER FROM VS CODE: the same two steps the
// terminal's /mcp-server add takes, and the daemon insists on.
//
// 1. The request goes WITHOUT an acknowledgement. The daemon validates it and
//    stores nothing; its answer (MCPResponse.detail) is what the user is about
//    to agree to -- the exact command line, "YOUR full privileges", which
//    variables it is given and where it starts.
// 2. That text is shown as it came, in a modal, and the user TYPES THE SERVER'S
//    NAME to go on. models.json's rule is that nobody arrives at an unconfined
//    server by copying a snippet; they arrive by typing the word. A button
//    alone would have repealed that for every server one click away in a
//    marketplace, which is the whole risk a marketplace adds.
// 3. Only then is the acknowledged request sent, and the daemon tries the
//    server and says whether it works.
//
// The warning is the DAEMON'S, not this file's: it describes the request the
// daemon actually validated, so what is shown cannot drift from what is stored.
// What this file adds is provenance -- which tier of the marketplace it came
// from -- and, for the variables a catalog says a server reads, where in VS
// Code's world they have to be set.

import * as vscode from 'vscode';
import { MCPAction, MCPResponse, RESTART_HINT, sendMCP } from './daemonClient';
import { CatalogEnv } from './mcpCatalog';

export interface ServerSpec {
  name: string;
  command: string;
  args: string[];
  /** Variable NAMES, passed through from the daemon's environment. */
  env: string[];
  /** The names in env the server runs without (a catalog's required: false). */
  envOptional?: string[];
  /** '' for a private folder of its own, 'workspace' for the project. */
  cwd: string;
}

export interface StoreOptions {
  action: Extract<MCPAction, 'add' | 'update'>;
  /** A line said before the daemon's warning: where this server came from. */
  provenance?: string;
  /** What a catalog says about the variables, for the after-add guidance. */
  envHelp?: CatalogEnv[];
}

/** The UI the flow needs, so a test can answer for the user. */
export interface StoreUi {
  /** Show the warning; true only when the user chose to go on. */
  confirm(message: string, detail: string, goOn: string): Promise<boolean>;
  /** Ask for the server's name to be typed; true only when it was, exactly. */
  typeName(name: string, prompt: string): Promise<boolean>;
  info(message: string): void;
  error(message: string): void;
}

export const vscodeStoreUi: StoreUi = {
  async confirm(message, detail, goOn) {
    return (await vscode.window.showWarningMessage(message, { modal: true, detail }, goOn)) === goOn;
  },
  async typeName(name, prompt) {
    const typed = await vscode.window.showInputBox({
      title: `Type ${name} to confirm`,
      prompt,
      placeHolder: name,
      ignoreFocusOut: true,
      validateInput: (v) => (v === name || v === '' ? undefined : `Type exactly ${name}, or press Escape to cancel`),
    });
    return typed === name;
  },
  info(message) {
    void vscode.window.showInformationMessage(message);
  },
  error(message) {
    void vscode.window.showErrorMessage(message);
  },
};

/**
 * storeServer runs the whole add (or update) flow and returns the daemon's final
 * answer, or undefined when nothing was stored -- cancelled, refused or failed,
 * each already said to the user.
 */
export async function storeServer(
  clientName: string,
  spec: ServerSpec,
  opts: StoreOptions,
  ui: StoreUi = vscodeStoreUi,
): Promise<MCPResponse | undefined> {
  const verb = opts.action === 'add' ? 'add' : 'change';
  const request = {
    action: opts.action,
    name: spec.name,
    command: spec.command,
    args: spec.args,
    env: spec.env,
    ...(spec.envOptional?.length ? { env_optional: spec.envOptional } : {}),
    ...(spec.cwd ? { cwd: spec.cwd } : {}),
  };

  let first: MCPResponse;
  try {
    first = (await sendMCP(clientName, request)).resp;
  } catch (err) {
    ui.error(`Could not reach the Mochiii daemon: ${(err as Error).message}`);
    return undefined;
  }
  if (first.error) {
    ui.error(first.error);
    return undefined;
  }
  if (!first.needs_acknowledgement || !first.detail) {
    // A daemon that stored the server without asking would have broken the
    // contract this flow depends on; say so rather than carry on as if it asked.
    ui.error('The daemon did not ask for the acknowledgement an MCP server needs, so nothing was confirmed here. ' + RESTART_HINT + '.');
    return undefined;
  }

  const detail = (opts.provenance ? opts.provenance + '\n\n' : '') + first.detail;
  if (!(await ui.confirm(`${verb === 'add' ? 'Add' : 'Change'} the MCP server "${spec.name}"?`, detail, 'Continue…'))) {
    return undefined;
  }
  if (
    !(await ui.typeName(
      spec.name,
      `This server will run ${spec.command} with your full privileges, and each of its tools will ask before it runs. ` +
        `Type its name to ${verb} it; Escape cancels and nothing is ${verb === 'add' ? 'added' : 'changed'}.`,
    ))
  ) {
    return undefined;
  }

  let done: MCPResponse;
  try {
    done = (await sendMCP(clientName, { ...request, acknowledged_unconfined: true })).resp;
  } catch (err) {
    ui.error(`Could not reach the Mochiii daemon: ${(err as Error).message}`);
    return undefined;
  }
  if (done.error || !done.ok) {
    ui.error(done.error || done.detail || `The daemon did not ${verb} ${spec.name}.`);
    return undefined;
  }
  ui.info(resultMessage(done, opts.envHelp ?? []));
  return done;
}

/**
 * resultMessage is the daemon's answer, plus -- for variables the server needs --
 * where VS Code's daemon reads them from. The daemon says "export it"; in an
 * editor launched from a desktop icon there is no shell to export it in, and the
 * daemon inherits VS Code's own environment, so that is what the user is told.
 */
export function resultMessage(resp: MCPResponse, envHelp: CatalogEnv[]): string {
  const parts = [resp.detail ?? 'Done.'];
  for (const note of resp.notes ?? []) {
    parts.push(note);
  }
  const needed = envHelp.filter((e) => e.required).map((e) => e.name);
  if (needed.length > 0) {
    // The key is given with Set key (mcpSecrets.ts) and kept in VS Code's
    // secret storage. It used to be "set it in the environment VS Code was
    // started in", which from a desktop icon has no shell to set it in.
    parts.push(
      `It needs ${needed.join(', ')}: set ${needed.length === 1 ? 'it' : 'them'} with Set key on the server ` +
        `(Customize > MCP in the chat, or the MCP Servers list). Mochiii keeps ${needed.length === 1 ? 'it' : 'them'} ` +
        `in VS Code's secret storage. A variable in the environment VS Code was started in works too.`,
    );
  }
  return parts.join(' ');
}

/** splitArgs splits an arguments line the way a shell would quote it, without a shell. */
export function splitArgs(line: string): { args: string[]; error?: string } {
  const args: string[] = [];
  let cur = '';
  let quote: '"' | "'" | '' = '';
  let have = false;
  for (let i = 0; i < line.length; i++) {
    const c = line[i];
    if (quote) {
      if (c === quote) {
        quote = '';
      } else if (c === '\\' && quote === '"' && i + 1 < line.length) {
        cur += line[++i];
      } else {
        cur += c;
      }
    } else if (c === '"' || c === "'") {
      quote = c;
      have = true;
    } else if (c === '\\' && i + 1 < line.length) {
      cur += line[++i];
      have = true;
    } else if (/\s/.test(c)) {
      if (have) {
        args.push(cur);
        cur = '';
        have = false;
      }
    } else {
      cur += c;
      have = true;
    }
  }
  if (quote) {
    return { args: [], error: 'a quote is not closed' };
  }
  if (have) {
    args.push(cur);
  }
  return { args };
}
