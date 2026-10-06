// /connect in VS Code: the provider list, what may follow the command, how each
// outcome is worded, and the request on the wire. No `vscode` import, so it
// runs under plain mocha: npx mocha --ui tdd out/test/suite/connectFlow.test.js

import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';

import { PROVIDERS, formatConnectResult, parseConnectArgs, providerByName, providerForBase, providerLabel } from '../../connectFlow';
import { ConnectResponse, preflightSession, sendConnect } from '../../daemonClient';
import { StubDaemon, writeLine } from '../stubDaemon';

const PROVIDERS_GO = path.resolve(__dirname, '..', '..', '..', '..', '..', 'protocol', 'providers.go');

function base(over: Partial<ConnectResponse>): ConnectResponse {
  return { protocol_version: 1, ok: true, outcome: 'accepted', detail: '', in_use: true, ...over };
}

suite('/connect providers mirror protocol/providers.go', () => {
  test('same ids, names, addresses and aliases, in the same order', () => {
    const go = fs.readFileSync(PROVIDERS_GO, 'utf8');
    const body = go.slice(go.indexOf('var providers = []Provider{'), go.indexOf('\n}\n', go.indexOf('var providers = []Provider{')));
    const entries = body.split('{ID: ').slice(1);
    const parsed = entries.map((e) => {
      const idRaw = /^([A-Za-z]+|"[^"]+")/.exec(e)![1];
      const id = idRaw === 'ProviderOpenRouter' ? 'openrouter' : idRaw.replace(/"/g, '');
      const name = /Name: "([^"]+)"/.exec(e)![1];
      const apiBase = /APIBase: "([^"]+)"/.exec(e)![1];
      const al = /Aliases: \[\]string\{([^}]*)\}/.exec(e);
      const aliases = al ? al[1].split(',').map((x) => x.trim().replace(/"/g, '')).filter((x) => x !== '') : [];
      return { id, name, apiBase, aliases };
    });
    assert.strictEqual(parsed.length, 17, 'the Go list itself changed size; check the parser');
    assert.deepStrictEqual(PROVIDERS, parsed, 'connectFlow.ts PROVIDERS has drifted from protocol/providers.go');
  });

  test('names and aliases resolve, case-insensitively; addresses resolve by host', () => {
    assert.strictEqual(providerByName('GROQ')?.id, 'groq');
    assert.strictEqual(providerByName('claude')?.id, 'anthropic');
    assert.strictEqual(providerByName('hf')?.id, 'huggingface');
    assert.strictEqual(providerByName('nope'), undefined);
    assert.strictEqual(providerForBase('https://api.groq.com/v1')?.id, 'groq', 'the path is ignored');
    assert.strictEqual(providerLabel('https://api.groq.com/openai/v1'), 'Groq (https://api.groq.com/openai/v1)');
    assert.strictEqual(providerLabel('http://localhost:11434/v1'), 'a model server on this machine (http://localhost:11434/v1)');
  });
});

suite('what may follow /connect', () => {
  test('nothing, show, forget', () => {
    assert.deepStrictEqual(parseConnectArgs(''), { kind: 'key' });
    assert.deepStrictEqual(parseConnectArgs('  show '), { kind: 'show' });
    assert.deepStrictEqual(parseConnectArgs('forget'), { kind: 'forget' });
  });

  test('a provider by name or by address', () => {
    assert.deepStrictEqual(parseConnectArgs('nvidia'), { kind: 'to', base: 'https://integrate.api.nvidia.com/v1' });
    assert.deepStrictEqual(parseConnectArgs('grok'), { kind: 'to', base: 'https://api.x.ai/v1' });
    assert.deepStrictEqual(parseConnectArgs('http://localhost:11434/v1/'), { kind: 'to', base: 'http://localhost:11434/v1' });
  });

  test('anything that could be a key is refused, and the refusal never repeats it', () => {
    for (const arg of [
      'gsk_5Uy5uUPTdnq9iaWzZ3mTWGdyb3FY',
      'sk-or-v1-abc',
      'https://user:secret@api.example.com/v1',
      'https://api.example.com/v1?key=sk-abc',
      'https://api.example.com/v1#sk-abc',
      'groq gsk_abc',
      'ftp://api.example.com',
      'nvapi-xyz',
    ]) {
      const r = parseConnectArgs(arg);
      assert.strictEqual(r.kind, 'refuse', arg);
      if (r.kind === 'refuse') {
        assert.ok(!r.message.includes(arg), `the refusal echoed the argument: ${arg}`);
        assert.match(r.message, /paste the key into the box/);
      }
    }
  });
});

suite('how the daemon\'s answer is worded', () => {
  test('accepted and in use names the provider, the masked key, and the model that answered', () => {
    const out = formatConnectResult(base({
      outcome: 'accepted', api_base: 'https://api.groq.com/openai/v1', masked_key: '...Kyxa (56 characters)',
      detail: 'the provider authenticated the key', model: 'qwen/qwen3.8-27b', model_count: 4, model_tested: true,
    }));
    assert.match(out, /^Connected to Groq \(https:\/\/api\.groq\.com\/openai\/v1\)\./);
    assert.match(out, /Key \.\.\.Kyxa \(56 characters\) is in use now — no restart needed\./);
    assert.match(out, /Ready — prompts go to qwen\/qwen3\.8-27b\. 4 models are available/);
  });

  test('"stored and proven" and "stored but unproven" never share a phrase', () => {
    const ok = formatConnectResult(base({ outcome: 'accepted', masked_key: 'k' }));
    const unproven = formatConnectResult(base({ outcome: 'unverified', masked_key: 'k', detail: 'no /key endpoint' }));
    assert.match(unproven, /but NOT verified/);
    assert.doesNotMatch(unproven, /^Connected/);
    assert.doesNotMatch(ok, /NOT verified/);
    const untested = formatConnectResult(base({ outcome: 'accepted', model: 'm', model_count: 2, model_tested: false }));
    assert.match(untested, /has NOT answered a test request/);
    assert.doesNotMatch(untested, /Ready/);
  });

  test('a key several providers could own: nothing was sent, and the choices are named', () => {
    const out = formatConnectResult(base({ outcome: 'needs_provider', ok: false, in_use: false, detail: 'sk- keys are issued by several providers', candidates: ['openai', 'deepseek'] }));
    assert.match(out, /^That key was sent nowhere and nothing was stored/);
    assert.match(out, /\/connect openai {2}\(OpenAI\)/);
    assert.match(out, /\/connect deepseek {2}\(DeepSeek\)/);
  });

  test('a refusal says who refused it and that nothing changed', () => {
    const out = formatConnectResult(base({ outcome: 'rejected', ok: false, in_use: false, api_base: 'https://openrouter.ai/api/v1', detail: '401' }));
    assert.match(out, /refused by OpenRouter/);
    assert.match(out, /Nothing was stored, and the key this daemon was already using is unchanged/);
    assert.match(out, /name its provider first/);
  });

  test('stored but overridden by the environment is said, not hidden', () => {
    const out = formatConnectResult(base({ outcome: 'accepted', in_use: false, env_override: true, masked_key: 'k' }));
    assert.match(out, /is stored, but it is NOT what this daemon is sending/);
    assert.match(out, /MOCHIII_API_KEY/);
  });

  test('provider text cannot draw on the panel', () => {
    const out = formatConnectResult(base({ outcome: 'rejected', ok: false, in_use: false, detail: 'bad\u001b[31mred\u0007' }));
    assert.ok(!/[\u001b\u0007]/.test(out));
  });

  test('show and forget', () => {
    assert.strictEqual(formatConnectResult(base({ outcome: 'shown', masked_key: '(none)' })), 'No key is stored.');
    assert.match(formatConnectResult(base({ outcome: 'shown', masked_key: '...abcd', api_base: 'https://api.groq.com/openai/v1', detail: 'verified' })), /^Connected to Groq .* with key \.\.\.abcd — verified$/);
    assert.match(formatConnectResult(base({ outcome: 'removed', detail: 'restart to stop using it' })), /^Stored key removed\./);
  });
});

suite('/connect on the wire', function () {
  this.timeout(10_000);

  async function withStub(stub: StubDaemon, fn: () => Promise<void>): Promise<void> {
    const prev = process.env.XDG_RUNTIME_DIR;
    try {
      await fn();
    } finally {
      await stub.stop();
      if (prev === undefined) {
        delete process.env.XDG_RUNTIME_DIR;
      } else {
        process.env.XDG_RUNTIME_DIR = prev;
      }
    }
  }

  test('sendConnect sends the key and the chosen address, and returns the daemon\'s answer', async () => {
    let seen: any;
    const stub = new StubDaemon();
    await stub.start((req, socket) => {
      seen = req;
      writeLine(socket, { protocol_version: 1, ok: true, outcome: 'accepted', detail: 'ok', in_use: true, masked_key: '...cdef' });
    });
    await withStub(stub, async () => {
      const r = await sendConnect('test', { api_key: 'gsk_test_abcdef', api_base: 'https://api.groq.com/openai/v1' });
      assert.deepStrictEqual(seen, { protocol_version: 1, connect: true, api_key: 'gsk_test_abcdef', api_base: 'https://api.groq.com/openai/v1' });
      assert.strictEqual(r.outcome, 'accepted');
      assert.strictEqual(r.masked_key, '...cdef');
    });
  });

  test('show and forget carry no key', async () => {
    const seen: any[] = [];
    for (const req of [{ show: true }, { forget: true }]) {
      const stub = new StubDaemon();
      await stub.start((r, socket) => {
        seen.push(r);
        writeLine(socket, { protocol_version: 1, ok: true, outcome: 'shown', detail: '', in_use: true });
      });
      await withStub(stub, async () => {
        await sendConnect('test', req);
      });
    }
    assert.deepStrictEqual(seen, [{ protocol_version: 1, connect: true, show: true }, { protocol_version: 1, connect: true, forget: true }]);
  });

  test('the handshake\'s needs_api_key reaches the panel', async () => {
    const stub = new StubDaemon();
    stub.handshakeExtra = { needs_api_key: true };
    await stub.start(() => undefined);
    await withStub(stub, async () => {
      const s = await preflightSession('test');
      assert.strictEqual(s.needsApiKey, true);
    });
  });
});
