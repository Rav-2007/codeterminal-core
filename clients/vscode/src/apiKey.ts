import * as vscode from 'vscode';

// THE CREDENTIAL THE PACKAGED EXTENSION COULD NOT RECEIVE.
//
// The daemon reads its model-provider key from exactly one place --
// CODETERMINAL_API_KEY in its environment (daemon/main.go:100) -- and
// daemon/config.go:23 states the rule that keeps it there: models.json holds
// "model slugs and metadata only -- never credentials".
//
// Until this file existed, nothing put that variable in the daemon's
// environment. daemonEnvironment() in extension.ts bridged the API BASE from a
// contributed setting and nothing else, and package.json contributed no key
// setting at all. So the key had to already be in the environment the extension
// host inherited -- which, for a VS Code started from a desktop icon, is empty.
//
// THIS IS ITEM 41'S DEFECT, ONE VALUE OVER. That item ("the extension cannot
// start its daemon on an ordinary install") was the same shape: a value the
// daemon needs, with no path from a user to the process that needs it. It took
// 72 days to find because every test supplied the variable by hand, so nothing
// ever saw what an install sees. extension.ts's own comment on the fix states
// the lesson -- "a setting nobody passes to the process that needs it is not a
// setting" -- and it was applied to the base only. The key, without which every
// request goes out with no Authorization header and every answer fails, kept
// the defect.
//
// WHY SecretStorage AND NOT A SETTING. apiBase is a contributed setting scoped
// `machine` precisely so a workspace's .vscode/settings.json cannot redirect a
// user's prompts to a host the repository chooses. A live provider key in
// settings.json would be a worse version of that exposure: readable by any
// extension that can read configuration, replicated by Settings Sync, and easy
// to commit by accident. SecretStorage is none of those things -- it is
// per-machine, not synced, and not visible to configuration readers.
//
// WHAT IS **NOT** CLAIMED, and this paragraph exists because the sentence above
// used to claim it: that the key is "encrypted by the OS keychain". That is
// true on macOS and Windows. On Linux -- the platform this release actually
// ships -- VS Code's SecretStorage sits on Electron's safeStorage, which uses a
// keyring when one is available (gnome-keyring, kwallet) and falls back to a
// basic scheme when none is, which is not equivalent to a keychain. Stating the
// strong version everywhere would be telling a Linux user their key is
// protected by something that may not be running.
//
// Nor does any of it protect the key in USE: it still reaches the daemon as an
// environment variable on the spawn, because that is the only interface the
// daemon has, and /proc/<pid>/environ is readable by the same user. What is
// protected is the key at rest, and the specific, likely ways a credential in
// settings.json gets away from you -- a sync, a screen-share, a commit.

export const API_KEY_SECRET = 'codeterminal.apiKey';
const DISMISSED_KEY = 'mochiii.apiKeyPrompt.dismissed';

// THE NARROWEST CONTEXT THESE FUNCTIONS ACTUALLY USE.
//
// vscode.ExtensionContext satisfies this structurally, so callers pass the real
// one and nothing at the call site changes. A test passes a fake with two
// fields instead of constructing an ExtensionContext, which cannot be done
// outside a running extension host -- and a credential path that can only be
// exercised by hand is a credential path nobody exercises.
export interface KeyContext {
  secrets: Pick<vscode.SecretStorage, 'get' | 'store' | 'delete'>;
  globalState: Pick<vscode.Memento, 'get' | 'update'>;
}

/**
 * getApiKey returns the stored key, or undefined when none has been set.
 *
 * BOUNDED, BECAUSE IT IS ON THE ACTIVATION PATH. extension.ts awaits this
 * before the first daemon spawn, and VS Code does not consider the extension
 * active until activate() resolves -- so an unbounded read here is an
 * unbounded activation.
 *
 * That matters more than it looks. SecretStorage sits on a keyring service
 * that may be absent (a headless CI runner), locked (a fresh login), or slow.
 * A REJECTION was always handled; a HANG was not, and would have presented as
 * an extension that never activates, with no error to read. It is also a
 * surface no test in this repository had ever exercised before this file.
 *
 * Same shape as queryModelStatus in modelSetup.ts, for the same reason: an
 * unanswerable question resolves to "unknown" rather than rejecting, because
 * every caller's correct response to not knowing is to carry on without a key.
 * Nothing is lost by timing out -- extension.ts subscribes to
 * secrets.onDidChange, so a key that arrives late is picked up and carried by
 * the next spawn.
 */
export function getApiKey(context: KeyContext, timeoutMs = 2000): Promise<string | undefined> {
  return new Promise((resolve) => {
    let settled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const done = (v: string | undefined): void => {
      if (settled) {
        return;
      }
      settled = true;
      if (timer !== undefined) {
        clearTimeout(timer);
      }
      resolve(v);
    };

    // Assigned BEFORE the read, so a synchronous throw below cannot reach
    // clearTimeout through the temporal dead zone of a `const timer`.
    timer = setTimeout(() => done(undefined), timeoutMs);

    try {
      void Promise.resolve(context.secrets.get(API_KEY_SECRET)).then(
        (key) => done(key && key.trim() !== '' ? key.trim() : undefined),
        () => done(undefined),
      );
    } catch {
      done(undefined);
    }
  });
}

/**
 * promptForApiKey asks for a key and stores it. Returns the key on success,
 * undefined if the user cancelled.
 *
 * `password: true` keeps it out of the input box's visible text; `ignoreFocusOut`
 * keeps a pasted key from being lost to a click elsewhere, which is the most
 * likely way to lose one mid-paste.
 */
export async function promptForApiKey(context: KeyContext): Promise<string | undefined> {
  const entered = await vscode.window.showInputBox({
    title: 'Mochiii: API key',
    prompt: 'Key for the model provider the daemon talks to (by default https://openrouter.ai/api/v1).',
    placeHolder: 'sk-...',
    password: true,
    ignoreFocusOut: true,
    // Whitespace only is the one input worth refusing here: it stores
    // successfully, reaches the provider as an empty bearer token, and fails
    // as "credentials rejected" -- which sends the user looking for a wrong key
    // rather than an empty one.
    validateInput: (v) => (v.trim() === '' ? 'A key is required, or press Escape to cancel.' : undefined),
  });

  if (entered === undefined || entered.trim() === '') {
    return undefined;
  }

  await context.secrets.store(API_KEY_SECRET, entered.trim());
  return entered.trim();
}

/** clearApiKey removes the stored key. */
export async function clearApiKey(context: KeyContext): Promise<void> {
  await context.secrets.delete(API_KEY_SECRET);
}

/**
 * Whether to offer the key prompt, and why not when not.
 *
 * SEPARATED FROM THE DIALOG ON PURPOSE. Everything that decides IF a user is
 * interrupted is policy and belongs under test; everything that draws the
 * interruption is vscode.window and does not. Folding the two together is what
 * makes a prompt-policy testable only by reassigning `vscode.window.*` -- a
 * technique nothing else in this repository uses, in a suite that can only run
 * under a real extension host. modelSetup.ts's test covers its pure half
 * (queryModelStatus) and leaves its dialog alone for the same reason.
 *
 * `env` is a parameter rather than a read of process.env so a test states the
 * environment it means instead of mutating the one it runs in.
 */
export type KeyPromptDecision = 'have-key' | 'from-env' | 'dismissed' | 'ask';

export async function apiKeyPromptDecision(
  context: KeyContext,
  env: NodeJS.ProcessEnv,
): Promise<KeyPromptDecision> {
  if (await getApiKey(context)) {
    return 'have-key';
  }
  // An environment that already carries the key is the from-source workflow the
  // README documents (`export CODETERMINAL_API_KEY=...`). Prompting there would
  // offer to fix something that is not broken, and storing a second copy would
  // create two sources of truth for one credential.
  if ((env.CODETERMINAL_API_KEY ?? '').trim() !== '') {
    return 'from-env';
  }
  if (context.globalState.get<boolean>(DISMISSED_KEY)) {
    return 'dismissed';
  }
  return 'ask';
}

/**
 * ensureApiKey tells the user, once, that no key is set — and offers to fix it.
 *
 * Never throws and never blocks activation, for the same reason
 * ensureModelAvailable does not: the daemon coming up is not contingent on a
 * user answering a dialog. Declining leaves exactly the behaviour that existed
 * before this file, and "don't ask again" is honoured, or the prompt becomes
 * the nagware that trains people to dismiss without reading.
 */
export async function ensureApiKey(
  context: KeyContext,
  output: vscode.OutputChannel,
  onStored: (key: string) => Promise<void>,
): Promise<void> {
  try {
    const decision = await apiKeyPromptDecision(context, process.env);
    if (decision === 'have-key') {
      return;
    }
    if (decision === 'from-env') {
      output.appendLine('api key: taken from the environment; not prompting');
      return;
    }
    if (decision === 'dismissed') {
      output.appendLine('api key: not set; prompt suppressed by a previous "Don\'t ask again"');
      return;
    }

    const SET = 'Set key';
    const NOT_NOW = 'Not now';
    const NEVER = "Don't ask again";

    const choice = await vscode.window.showInformationMessage(
      'Mochiii has no API key, so the model cannot be reached and every answer will fail. ' +
        'Set one to start asking questions.',
      SET,
      NOT_NOW,
      NEVER,
    );

    if (choice === NEVER) {
      await context.globalState.update(DISMISSED_KEY, true);
      return;
    }
    if (choice !== SET) {
      return; // "Not now", or dismissed. Ask again next session.
    }

    const key = await promptForApiKey(context);
    if (key) {
      await onStored(key);
    }
  } catch (err) {
    // Activation must survive anything here.
    output.appendLine(`api key setup: ${(err as Error).message}`);
  }
}
