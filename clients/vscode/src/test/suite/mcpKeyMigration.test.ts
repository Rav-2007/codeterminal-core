import * as assert from 'assert';
import * as vscode from 'vscode';
import { loadMcpEnv, cachedMcpEnv } from '../../mcpSecrets';
import { migrateKeysToDaemon } from '../../mcpServersView';

// THE KEYS MOVE TO THE DAEMON ONCE (FEATURE_MCP_SECRETS). Before the daemon kept
// them, this extension kept server keys in SecretStorage; a daemon that keeps
// them itself is handed each one, and only when all are stored is the
// SecretStorage copy deleted.

function fakeContext(stored: Record<string, string>) {
  const secrets = new Map<string, string>();
  if (Object.keys(stored).length > 0) {
    secrets.set('mochiii.mcpServerEnv', JSON.stringify(stored));
  }
  const state = new Map<string, unknown>();
  return {
    secrets: {
      get: async (k: string) => secrets.get(k),
      store: async (k: string, v: string) => void secrets.set(k, v),
      delete: async (k: string) => void secrets.delete(k),
    },
    globalState: {
      get: (k: string) => state.get(k),
      update: async (k: string, v: unknown) => void state.set(k, v),
    },
    raw: secrets,
  } as unknown as vscode.ExtensionContext & { raw: Map<string, string> };
}

suite('server keys move from SecretStorage to the daemon', () => {
  // The cache is the module's: leave it empty for the tests that follow.
  teardown(async () => {
    await loadMcpEnv(fakeContext({}));
  });

  test('every key is handed over, then the local copy is deleted and the move is not repeated', async () => {
    const ctx = fakeContext({ PERPLEXITY_API_KEY: 'pplx-1', SUPABASE_ACCESS_TOKEN: 'sbp-2' });
    await loadMcpEnv(ctx);
    const sent: Array<[string, string]> = [];
    const n = await migrateKeysToDaemon(ctx, async (name, value) => {
      sent.push([name, value]);
      return undefined;
    });
    assert.strictEqual(n, 2);
    assert.deepStrictEqual(sent.sort(), [['PERPLEXITY_API_KEY', 'pplx-1'], ['SUPABASE_ACCESS_TOKEN', 'sbp-2']]);
    assert.strictEqual(ctx.raw.size, 0, 'the SecretStorage copy is deleted');
    assert.deepStrictEqual(cachedMcpEnv(), {});
    assert.strictEqual(await migrateKeysToDaemon(ctx, async () => 'should not be called'), 0);
  });

  test('a key the daemon refuses keeps every local copy for the next try', async () => {
    const ctx = fakeContext({ A_KEY: 'a', B_KEY: 'b' });
    await loadMcpEnv(ctx);
    const n = await migrateKeysToDaemon(ctx, async (name) => (name === 'B_KEY' ? 'daemon unreachable' : undefined));
    assert.strictEqual(n, 0);
    assert.ok(ctx.raw.has('mochiii.mcpServerEnv'), 'nothing is deleted when one key did not reach the daemon');
    assert.strictEqual(ctx.globalState.get('mochiii.mcpEnvMigrated'), undefined);
  });
});
