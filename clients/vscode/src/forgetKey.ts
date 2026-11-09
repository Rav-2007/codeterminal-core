import { ConnectResponse } from './daemonClient';
import { formatConnectResult } from './connectFlow';

// FORGET THE STORED KEY: ASK FIRST, AND ONLY WHEN THERE IS SOMETHING TO FORGET.
//
// FOUND 2026-11-08 (UI report): the Account page's "Forget the stored key"
// acted at once, with no confirmation, and did so with nothing stored -- it
// still restarted the daemon, because the daemon's "nothing was stored" answer
// counted as something done. The page then showed "Not connected" with a raw
// lockfile error until Refresh (pagesHost.reloadAfter now waits for the daemon).
//
// Two keys can be stored: the daemon's key file, and a key VS Code kept in its
// own secret storage. Forget removes both (a "forget" that left one in force
// would be a lie). A key in the environment is not stored and is not touched.
//
// Pure, with its effects passed in, so the decisions are tested without VS
// Code's dialogs (a test host accepts every modal dialog on its own).

export interface ForgetDeps {
  /** The daemon, when a folder is open and there is one to ask. */
  daemon?: {
    show(): Promise<ConnectResponse>;
    forget(): Promise<ConnectResponse>;
    restart(): Promise<unknown>;
  };
  ownKey(): Promise<string | undefined>;
  clearOwnKey(): Promise<void>;
  /** Asks the user; true only on an explicit yes. `what` names each key that would go. */
  confirm(what: string[]): Promise<boolean>;
}

/** Whether a show answer says the daemon has a key file (daemon maskKey: "(none)" when not). */
export function daemonHasStoredKey(r: ConnectResponse): boolean {
  return !!r.masked_key && r.masked_key !== '(none)';
}

export const NOTHING_STORED = 'No key is stored, so there is nothing to forget. Nothing was changed.';
export const DECLINED = 'Nothing was forgotten.';

/**
 * forgetDidNothing says whether the answer forgetStoredKey gave means nothing
 * was changed: nothing was stored, or the user said no. Neither touched the
 * daemon, so neither has anything for the Account page to wait for.
 */
export function forgetDidNothing(said: unknown): boolean {
  return said === NOTHING_STORED || said === DECLINED;
}

export async function forgetStoredKey(deps: ForgetDeps): Promise<string> {
  const what: string[] = [];
  let askDaemon = false;
  if (deps.daemon) {
    try {
      const shown = await deps.daemon.show();
      if (daemonHasStoredKey(shown)) {
        askDaemon = true;
        what.push(`the key stored for the daemon (${shown.masked_key})`);
      }
    } catch {
      // Cannot tell: offer it, and let the daemon say whether it had one.
      askDaemon = true;
      what.push('the key stored for the daemon, if there is one (the daemon did not answer)');
    }
  }
  const own = await deps.ownKey();
  if (own) {
    what.push(`the key VS Code kept for itself (...${own.slice(-4)})`);
  }
  if (what.length === 0) {
    return NOTHING_STORED;
  }
  if (!(await deps.confirm(what))) {
    return DECLINED;
  }

  const parts: string[] = [];
  let removed = false;
  if (askDaemon && deps.daemon) {
    try {
      const r = await deps.daemon.forget();
      parts.push(formatConnectResult(r));
      removed = removed || (r.ok && !/^no key was stored/.test(r.detail || ''));
    } catch (err) {
      parts.push(`could not ask the daemon to forget its key: ${(err as Error).message}`);
    }
  }
  if (own) {
    await deps.clearOwnKey();
    removed = true;
    parts.push('The key VS Code kept for itself was removed as well.');
  }
  // The running daemon still holds a removed key in memory until it restarts.
  // Only then: a restart with nothing removed is an outage for nothing.
  if (removed && deps.daemon) {
    await deps.daemon.restart();
    parts.push('The daemon was restarted, so it is no longer using a removed key.');
  }
  return parts.join('\n');
}
