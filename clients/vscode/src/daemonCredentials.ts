// HOW A KEY REACHES THE DAEMON: on its stdin, never in its environment.
//
// A variable a process is started with stays readable in /proc/<pid>/environ for
// that process's whole life, by anything running as the same user -- and nothing
// the daemon does later can take it back (os.Unsetenv does not rewrite that
// file). This extension used to start the daemon with its SecretStorage key as
// MOCHIII_API_KEY, so a key the user had put in the OS keychain sat in plain
// text in procfs from the first second the daemon ran.
//
// Now the daemon is started with --credentials-from-stdin and an environment
// holding no key, and the key goes down a pipe as one JSON line that the daemon
// reads once and the extension then closes (daemon/launchcred.go).
//
// No vscode import: everything here is a pure function of its arguments, so the
// rule can be tested without activating the extension.

/** The daemon flag that makes it read one credentials line from stdin. */
export const CREDENTIALS_FROM_STDIN_FLAG = '--credentials-from-stdin';

/**
 * The environment names that hold a credential. Removed from EVERY process this
 * extension starts -- even when VS Code itself inherited them from a shell --
 * because none of those processes needs one in its environment: the daemon gets
 * its key on stdin, and `mcp list`, `download-model` and git need none at all.
 */
export const CREDENTIAL_ENV_NAMES: readonly string[] = ['MOCHIII_API_KEY', 'MOCHIII_PROXY_KEY'];

/** A copy of env with every credential variable removed. */
export function withoutCredentials(env: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  const out: NodeJS.ProcessEnv = { ...env };
  for (const name of CREDENTIAL_ENV_NAMES) {
    delete out[name];
  }
  return out;
}

/** The line written to the daemon's stdin. Fields are omitted, never empty. */
export interface DaemonCredentials {
  api_key?: string;
  proxy_key?: string;
}

/**
 * What the daemon is handed at start.
 *
 * The key is the one this extension keeps in SecretStorage, or -- when it keeps
 * none -- a MOCHIII_API_KEY VS Code inherited from the shell that launched it.
 * That is the order the old environment path had (a stored key overwrote an
 * inherited one, and an inherited one was left alone when nothing was stored),
 * so moving the key off the environment changes WHERE it travels and nothing
 * about WHICH key is used.
 *
 * The API base is deliberately NOT sent here. It is not a secret, it still
 * travels as MOCHIII_API_BASE, and a key sent without a base is used with
 * whatever base is in force -- exactly MOCHIII_API_KEY's semantics -- so every
 * existing setup resolves the same base, with the same error messages, as before.
 *
 * The proxy key travels in a field of its own, never as api_key: the daemon
 * keeps provider and proxy keys apart so neither is sent to the other's host.
 */
export function daemonCredentials(storedKey: string | undefined, inherited: NodeJS.ProcessEnv): DaemonCredentials {
  const out: DaemonCredentials = {};
  const key = (storedKey ?? '').trim() || (inherited.MOCHIII_API_KEY ?? '').trim();
  if (key) {
    out.api_key = key;
  }
  const proxy = (inherited.MOCHIII_PROXY_KEY ?? '').trim();
  if (proxy) {
    out.proxy_key = proxy;
  }
  return out;
}

/** The exact bytes written to stdin: one JSON object and a newline ("{}" for none). */
export function credentialsLine(c: DaemonCredentials): string {
  return JSON.stringify(c) + '\n';
}
