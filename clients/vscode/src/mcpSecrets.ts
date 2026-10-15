import * as vscode from 'vscode';

// THE KEYS MCP SERVERS READ, KEPT WHERE THE MODEL KEY IS KEPT.
//
// A server names the variables it reads (perplexity's PERPLEXITY_API_KEY), and
// until this file the daemon could only find their values in the environment
// VS Code was started in. From a desktop icon that environment has none, so a
// server the user added exited on every start and the list still said "Active"
// (FOUND 2026-10-14).
//
// The values live in SecretStorage, for apiKey.ts's reasons: not a setting a
// workspace or Settings Sync can see. They reach the daemon on its stdin at
// start (daemonCredentials.ts, mcp_env) and, when set while it runs, in an
// MCPActionSetEnv request -- never in its environment, where any process of the
// same user could read them from /proc.
//
// Keyed by VARIABLE name, not server name: a variable means the same thing to
// every server that reads it, as it would in a shell.

const SECRET_KEY = 'mochiii.mcpServerEnv';

/** The variable names this file will store a value for (the daemon's rule). */
export const MCP_ENV_NAME = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/;

let cache: Record<string, string> | undefined;

function parse(raw: string | undefined): Record<string, string> {
  if (!raw) {
    return {};
  }
  try {
    const v: unknown = JSON.parse(raw);
    if (!v || typeof v !== 'object' || Array.isArray(v)) {
      return {};
    }
    const out: Record<string, string> = {};
    for (const [k, val] of Object.entries(v as Record<string, unknown>)) {
      if (MCP_ENV_NAME.test(k) && typeof val === 'string' && val !== '') {
        out[k] = val;
      }
    }
    return out;
  } catch {
    return {};
  }
}

/** loadMcpEnv reads the stored values once; later reads come from memory. */
export async function loadMcpEnv(context: vscode.ExtensionContext): Promise<Record<string, string>> {
  cache = parse(await context.secrets.get(SECRET_KEY));
  return { ...cache };
}

/** cachedMcpEnv is what loadMcpEnv last read, for the synchronous daemon spawn. */
export function cachedMcpEnv(): Record<string, string> {
  return { ...(cache ?? {}) };
}

/** storeMcpEnv sets one value, or forgets it for ''. */
export async function storeMcpEnv(context: vscode.ExtensionContext, name: string, value: string): Promise<void> {
  const all = parse(await context.secrets.get(SECRET_KEY));
  if (value) {
    all[name] = value;
  } else {
    delete all[name];
  }
  cache = all;
  if (Object.keys(all).length === 0) {
    await context.secrets.delete(SECRET_KEY);
  } else {
    await context.secrets.store(SECRET_KEY, JSON.stringify(all));
  }
}

/** storedMcpEnvNames is the names a value is kept for. Never the values. */
export function storedMcpEnvNames(): string[] {
  return Object.keys(cache ?? {}).sort();
}
