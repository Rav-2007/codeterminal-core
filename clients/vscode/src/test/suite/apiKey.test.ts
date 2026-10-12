import * as assert from 'assert';
import * as vscode from 'vscode';

import {
  API_KEY_SECRET,
  KeyContext,
  apiKeyPromptDecision,
  clearApiKey,
  getApiKey,
} from '../../apiKey';
import { daemonEnvironment } from '../../extension';
import { credentialsLine, daemonCredentials, withoutCredentials } from '../../daemonCredentials';

// THE TEST THAT WAS MISSING.
//
// The packaged extension shipped with no path from a user to
// MOCHIII_API_KEY: the daemon reads it from its environment and nothing
// put it there, so a .vsix install could never authenticate and every answer
// failed. Item 41 was the identical shape one value over -- a thing the daemon
// needs, with no way for a user to supply it -- and it survived 72 days because
// "every test supplied the variable by hand, so nothing ever saw what an
// install sees".
//
// So the load-bearing assertion here is the first one: given a key, the daemon
// is HANDED it. It is now handed it on stdin, not in its environment -- a key in
// the environment stays readable in /proc/<daemon-pid>/environ for the daemon's
// whole life -- so that assertion has two halves and both are load-bearing: the
// stdin line carries the key, and the environment does not. Break either and a
// test below fails.
//
// NOTHING HERE REASSIGNS `vscode.window.*`. An earlier draft stubbed the
// dialogs that way to test the prompt policy -- the only file in this
// repository that did, in the one suite that cannot be run without a real
// extension host. The policy now lives in apiKeyPromptDecision(), which takes
// its environment as an argument and returns a verdict, so every branch is
// reachable with a fake context and no globals. That is the same split
// modelSetup.test.ts already makes: test the pure half, leave the dialog.

function fakeContext(opts: { stored?: string; dismissed?: boolean } = {}): KeyContext {
  let current = opts.stored;
  const state = new Map<string, unknown>();
  if (opts.dismissed) {
    state.set('mochiii.apiKeyPrompt.dismissed', true);
  }
  return {
    secrets: {
      get: async (key: string) => (key === API_KEY_SECRET ? current : undefined),
      store: async (key: string, value: string) => {
        if (key === API_KEY_SECRET) {
          current = value;
        }
      },
      delete: async (key: string) => {
        if (key === API_KEY_SECRET) {
          current = undefined;
        }
      },
    },
    globalState: {
      get: (<T>(key: string, fallback?: T) =>
        (state.has(key) ? (state.get(key) as T) : fallback)) as KeyContext['globalState']['get'],
      update: async (key: string, value: unknown) => {
        state.set(key, value);
      },
    },
  } as KeyContext;
}

/** A context whose keychain never answers — a locked or absent keyring. */
function hangingContext(): KeyContext {
  const ctx = fakeContext();
  return {
    ...ctx,
    secrets: { ...ctx.secrets, get: () => new Promise<string | undefined>(() => undefined) },
  } as KeyContext;
}

/** A context whose keychain rejects. */
function rejectingContext(): KeyContext {
  const ctx = fakeContext();
  return {
    ...ctx,
    secrets: { ...ctx.secrets, get: async () => Promise.reject(new Error('keyring locked')) },
  } as KeyContext;
}

suite('api key — the credential reaches the daemon, on stdin and never in its environment', () => {
  // daemonEnvironment() builds the child environment FROM this process's, so
  // these cases have to state this process's. Plain object mutation, restored in
  // teardown -- not a module-namespace reassignment.
  const realEnvKey = process.env.MOCHIII_API_KEY;
  const realProxyKey = process.env.MOCHIII_PROXY_KEY;
  teardown(() => {
    for (const [name, value] of [
      ['MOCHIII_API_KEY', realEnvKey],
      ['MOCHIII_PROXY_KEY', realProxyKey],
    ] as const) {
      if (value === undefined) {
        delete process.env[name];
      } else {
        process.env[name] = value;
      }
    }
  });

  // THE ONE THAT MATTERS, both halves of it. Everything else is about when to
  // prompt; this is about whether the key arrives, and where it travels.
  test('theStoredKeyIsHandedOverOnStdin', () => {
    delete process.env.MOCHIII_API_KEY;
    assert.deepStrictEqual(daemonCredentials('sk-test-12345', process.env), { api_key: 'sk-test-12345' });
  });

  test('theDaemonEnvironmentNeverCarriesAKey', () => {
    // Even when VS Code itself inherited both from a shell.
    process.env.MOCHIII_API_KEY = 'sk-from-the-shell';
    process.env.MOCHIII_PROXY_KEY = 'mk-from-the-shell';
    const env = daemonEnvironment();
    assert.strictEqual(env.MOCHIII_API_KEY, undefined);
    assert.strictEqual(env.MOCHIII_PROXY_KEY, undefined);
    assert.ok(!('MOCHIII_API_KEY' in env) && !('MOCHIII_PROXY_KEY' in env), 'a credential name survived, even if empty');
  });

  test('theKeyIsTrimmed', () => {
    delete process.env.MOCHIII_API_KEY;
    assert.strictEqual(daemonCredentials('  sk-padded  ', process.env).api_key, 'sk-padded');
  });

  // Whitespace stores fine, reaches the provider as an empty bearer token and
  // comes back "credentials rejected" -- which sends the user hunting a wrong
  // key rather than an absent one.
  test('aWhitespaceKeyIsNotTreatedAsAKey', async () => {
    delete process.env.MOCHIII_API_KEY;
    assert.deepStrictEqual(daemonCredentials('   ', process.env), {});
    assert.strictEqual(await getApiKey(fakeContext({ stored: '   ' })), undefined);
  });

  // The from-source workflow the README documents: the key is exported in the
  // shell. It must still reach the daemon -- on stdin now, not inherited.
  test('anInheritedKeyIsHandedOverWhenNothingIsStored', () => {
    process.env.MOCHIII_API_KEY = 'sk-from-the-shell';
    assert.strictEqual(daemonCredentials(undefined, process.env).api_key, 'sk-from-the-shell');
    assert.strictEqual(daemonEnvironment().MOCHIII_API_KEY, undefined);
  });

  // The order the environment path had: a stored key overwrote an inherited one.
  test('aStoredKeyStillWinsOverAnInheritedOne', () => {
    process.env.MOCHIII_API_KEY = 'sk-from-the-shell';
    assert.strictEqual(daemonCredentials('sk-stored', process.env).api_key, 'sk-stored');
  });

  // A proxy key travels in its own field: the daemon keeps provider and proxy
  // keys apart, so neither is ever sent to the other's host.
  test('aProxyKeyTravelsInItsOwnField', () => {
    delete process.env.MOCHIII_API_KEY;
    process.env.MOCHIII_PROXY_KEY = 'mk-from-the-shell';
    assert.deepStrictEqual(daemonCredentials(undefined, process.env), { proxy_key: 'mk-from-the-shell' });
  });

  test('theStdinLineIsOneJSONObjectAndANewline', () => {
    assert.strictEqual(credentialsLine({}), '{}\n');
    const line = credentialsLine({ api_key: 'sk-a' });
    assert.strictEqual(line.indexOf('\n'), line.length - 1, 'more than one line');
    assert.deepStrictEqual(JSON.parse(line), { api_key: 'sk-a' });
  });

  test('withoutCredentialsLeavesEverythingElse', () => {
    const out = withoutCredentials({ MOCHIII_API_KEY: 'a', MOCHIII_PROXY_KEY: 'b', MOCHIII_API_BASE: 'c', PATH: 'd' });
    assert.deepStrictEqual(out, { MOCHIII_API_BASE: 'c', PATH: 'd' });
  });

  test('getApiKeyReadsAndTrimsWhatWasStored', async () => {
    assert.strictEqual(await getApiKey(fakeContext()), undefined);
    assert.strictEqual(await getApiKey(fakeContext({ stored: '  sk-stored ' })), 'sk-stored');
  });

  test('clearApiKeyRemovesIt', async () => {
    const ctx = fakeContext({ stored: 'sk-stored' });
    await clearApiKey(ctx);
    assert.strictEqual(await getApiKey(ctx), undefined);
  });

  // ACTIVATION MUST NOT BE ABLE TO HANG ON A KEYCHAIN.
  //
  // extension.ts awaits getApiKey() before the first spawn, and VS Code holds
  // the extension inactive until activate() resolves. A keyring that never
  // answers -- absent on a headless runner, locked on a fresh login -- would
  // therefore present as an extension that never activates, with no error to
  // read. Remove the timer in getApiKey and this test hangs to the suite's
  // 20s limit rather than failing fast, which is itself the symptom.
  test('aKeychainThatNeverAnswersDoesNotBlock', async () => {
    const started = Date.now();
    assert.strictEqual(await getApiKey(hangingContext(), 50), undefined);
    assert.ok(Date.now() - started < 5000, 'getApiKey must resolve on its own budget');
  });

  test('aRejectingKeychainReadsAsNoKey', async () => {
    assert.strictEqual(await getApiKey(rejectingContext(), 1000), undefined);
  });

  // The prompt policy, every branch, with no globals and no stubbed dialogs.
  suite('prompt policy', () => {
    test('aStoredKeyMeansNoPrompt', async () => {
      const d = await apiKeyPromptDecision(fakeContext({ stored: 'sk-stored' }), {});
      assert.strictEqual(d, 'have-key');
    });

    test('anEnvironmentKeyMeansNoPrompt', async () => {
      const d = await apiKeyPromptDecision(fakeContext(), { MOCHIII_API_KEY: 'sk-shell' });
      assert.strictEqual(d, 'from-env');
    });

    test('aStoredKeyWinsOverTheEnvironment', async () => {
      const d = await apiKeyPromptDecision(fakeContext({ stored: 'sk-stored' }), {
        MOCHIII_API_KEY: 'sk-shell',
      });
      assert.strictEqual(d, 'have-key');
    });

    test('dontAskAgainIsHonoured', async () => {
      const d = await apiKeyPromptDecision(fakeContext({ dismissed: true }), {});
      assert.strictEqual(d, 'dismissed');
    });

    test('nothingAnywhereMeansAsk', async () => {
      assert.strictEqual(await apiKeyPromptDecision(fakeContext(), {}), 'ask');
    });

    // A variable that is set but blank is not a key, and treating it as one
    // would silently suppress the prompt for a user who has no key at all.
    test('aBlankEnvironmentVariableIsNotAKey', async () => {
      const d = await apiKeyPromptDecision(fakeContext(), { MOCHIII_API_KEY: '   ' });
      assert.strictEqual(d, 'ask');
    });
  });
});

// The same delivery property, for the model a local server knows: a
// mochiii.model setting that never reached the daemon would look configured
// and change nothing.
suite('local model — the setting reaches the daemon', () => {
  const config = () => vscode.workspace.getConfiguration('mochiii');
  teardown(async () => {
    await config().update('model', undefined, vscode.ConfigurationTarget.Global);
  });

  test('daemonEnvironmentCarriesTheModel', async () => {
    await config().update('model', '  qwen2.5-coder:7b ', vscode.ConfigurationTarget.Global);
    assert.strictEqual(daemonEnvironment().MOCHIII_MODEL, 'qwen2.5-coder:7b');
  });

  test('anEmptyModelSettingSendsNothing', async () => {
    await config().update('model', '', vscode.ConfigurationTarget.Global);
    const saved = process.env.MOCHIII_MODEL;
    delete process.env.MOCHIII_MODEL;
    try {
      assert.strictEqual(daemonEnvironment().MOCHIII_MODEL, undefined);
    } finally {
      if (saved !== undefined) {
        process.env.MOCHIII_MODEL = saved;
      }
    }
  });
});
