// The one door from the chat's connect popup to the key path in extension.ts.
//
// The popup collects the provider and the key inside the chat (apiBase '' is
// "Any key": the daemon tells the provider from the key); extension.ts
// owns everything after that -- the daemon's check, the store, the restart --
// and installs it here at activation. A function, not a command: a command
// taking a key as an argument would be callable by every other extension.

export interface ConnectOutcome {
  message: string;
  inUse: boolean;
  // needs_provider: the key could be any of these providers' (their API
  // addresses), and it was sent nowhere. Only from the popup's path.
  candidates?: string[];
}

type ConnectKey = (key: string, apiBase: string) => Promise<ConnectOutcome>;

let impl: ConnectKey | undefined;

export function setConnectKey(f: ConnectKey | undefined): void {
  impl = f;
}

export function hasConnectKey(): boolean {
  return impl !== undefined;
}

export async function connectKey(key: string, apiBase: string): Promise<ConnectOutcome> {
  if (!impl) {
    return { message: 'Mochiii is still starting. Try again in a moment.', inUse: false };
  }
  return impl(key, apiBase);
}
