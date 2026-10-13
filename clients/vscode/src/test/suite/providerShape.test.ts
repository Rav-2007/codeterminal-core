// WHICH PROVIDER A KEY LOOKS LIKE IT IS FOR, from the key's shape alone.
//
// providerForKey() exists so the panel can say "that looks like a Groq key"
// before anything is sent, and so the no-folder path -- where there is no daemon
// to read the key -- is not left with nothing. It is a hint: the caller confirms
// with the user, because a wrongly-detected provider sends a good key to a
// company that did not issue it and the 401 that comes back reads like a bad key.
//
// THE BUG THIS SUITE IS HERE TO CATCH is ordering. "sk-" is a prefix of "sk-or-",
// "sk-ant-", "sk-proj-" and "sk-svcacct-", so a first-match-wins lookup with the
// short one listed first would quietly route every Anthropic and OpenRouter key
// to OpenAI. The matcher takes the longest match instead, and the tests below
// pin that from both ends: the long forms resolve, and the bare "sk-" -- which
// four providers issue -- resolves to nobody.
//
// EVERY KEY IN THIS FILE IS INVENTED and says so in its own body. Nothing here
// is, resembles, or is long enough to be a credential: a real key is never
// needed to test a prefix, and the last test in the file enforces that.
//
// No `vscode` import, so it runs under plain mocha:
//   npx mocha --ui tdd out/test/suite/providerShape.test.js

import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';

import { KEY_PREFIXES, PROVIDERS, providerByName, providerForKey } from '../../connectFlow';

const PROVIDERS_GO = path.resolve(__dirname, '..', '..', '..', '..', '..', 'protocol', 'providers.go');

// The body every fake key in this file carries. Words, not entropy: "sk-ant-"
// plus this is 22 characters of English, well under the 32 that daemon/scrub.go
// needs before it will call a string a key, which is the point.
const FAKE = 'EXAMPLE-NOT-A-REAL-KEY';
const FAKE_SNAKE = 'EXAMPLE_NOT_A_REAL_KEY';

// One per prefix the detector supports, written out rather than generated, so
// this file also reads as the list of shapes a user may paste.
const FIXTURES: ReadonlyArray<{ key: string; id: string }> = [
  { key: `sk-or-${FAKE}`, id: 'openrouter' },
  { key: `sk-ant-${FAKE}`, id: 'anthropic' },
  { key: `sk-proj-${FAKE}`, id: 'openai' },
  { key: `sk-svcacct-${FAKE}`, id: 'openai' },
  { key: `gsk_${FAKE_SNAKE}`, id: 'groq' },
  { key: `nvapi-${FAKE}`, id: 'nvidia' },
  { key: `xai-${FAKE}`, id: 'xai' },
  { key: `csk-${FAKE}`, id: 'cerebras' },
  { key: `hf_${FAKE_SNAKE}`, id: 'huggingface' },
  { key: `tgp_v1_${FAKE_SNAKE}`, id: 'together' },
  { key: `fw_${FAKE_SNAKE}`, id: 'fireworks' },
  { key: `AIza${FAKE}`, id: 'gemini' },
];

function goProviders(): Array<{ id: string; prefixes: string[]; shared: boolean }> {
  const go = fs.readFileSync(PROVIDERS_GO, 'utf8');
  const start = go.indexOf('var providers = []Provider{');
  const body = go.slice(start, go.indexOf('\n}\n', start));
  return body.split('{ID: ').slice(1).map((e) => {
    const idRaw = /^([A-Za-z]+|"[^"]+")/.exec(e)![1];
    const id = idRaw === 'ProviderOpenRouter' ? 'openrouter' : idRaw.replace(/"/g, '');
    const kp = /KeyPrefixes: \[\]string\{([^}]*)\}/.exec(e);
    const prefixes = kp
      ? kp[1].split(',').map((x) => x.trim().replace(/"/g, '')).filter((x) => x !== '')
      : [];
    return { id, prefixes, shared: /SharedKeyPrefix: true/.test(e) };
  });
}

suite('a key\'s shape names its provider', () => {
  test('every supported prefix resolves to the provider that issues it', () => {
    for (const f of FIXTURES) {
      const p = providerForKey(f.key);
      assert.strictEqual(p?.id, f.id, `${f.key} resolved to ${p ? p.id : 'nothing'}`);
    }
    assert.strictEqual(FIXTURES.length, KEY_PREFIXES.length, 'a prefix was added to KEY_PREFIXES with no fixture here');
  });

  test('what comes back is the PROVIDERS entry itself, so the caller gets the real apiBase', () => {
    const groq = providerForKey(`gsk_${FAKE_SNAKE}`);
    assert.strictEqual(groq, providerByName('groq'), 'not the same object as the table holds');
    assert.strictEqual(groq?.apiBase, 'https://api.groq.com/openai/v1');
    for (const s of KEY_PREFIXES) {
      assert.ok(PROVIDERS.some((p) => p.id === s.providerId), `KEY_PREFIXES names "${s.providerId}", which is in no Provider`);
    }
  });

  test('"sk-" does not shadow sk-or-, sk-ant-, sk-proj- or sk-svcacct-', () => {
    assert.strictEqual(providerForKey(`sk-or-${FAKE}`)?.id, 'openrouter');
    assert.strictEqual(providerForKey(`sk-ant-${FAKE}`)?.id, 'anthropic');
    assert.strictEqual(providerForKey(`sk-proj-${FAKE}`)?.id, 'openai');
    assert.strictEqual(providerForKey(`sk-svcacct-${FAKE}`)?.id, 'openai');
  });

  test('a bare "sk-" key names nobody, because four providers issue one', () => {
    // Not a gap: OpenAI, DeepSeek, Moonshot AI and Alibaba Cloud all hand out
    // plain "sk-..." keys (SharedKeyPrefix in providers.go), so reading one as
    // OpenAI would send three sets of users' working keys to a company that did
    // not issue them. The daemon's needs_provider outcome asks instead.
    const detected = providerForKey(`sk-${FAKE}`);
    assert.strictEqual(detected, undefined, `a shared "sk-" key was attributed to ${detected?.name}`);
    // The count comes from the Go table so the reasoning above cannot go stale
    // in silence: if a day comes when one provider alone issues bare "sk-" keys,
    // reading one is no longer a guess, and this is what says so.
    const shared = goProviders().filter((p) => p.shared).map((p) => p.id);
    assert.ok(shared.length > 1, `"sk-" is now issued by ${shared.join(', ') || 'nobody'} alone; revisit returning undefined for it`);
  });

  test('no shorter prefix can ever beat a longer one it is the start of', () => {
    // The invariant, checked against the table rather than against a list of
    // examples, so it holds for whatever is added later. It finds no pair TODAY,
    // and that is the result rather than a gap: the one prefix that nests inside
    // the others is the bare "sk-", and that is deliberately not in the table
    // (the test above is what pins its absence). The day someone adds a nesting
    // prefix, this is the test that says where those keys went.
    for (const a of KEY_PREFIXES) {
      for (const b of KEY_PREFIXES) {
        if (a.prefix !== b.prefix && b.prefix.startsWith(a.prefix)) {
          const p = providerForKey(b.prefix + FAKE);
          assert.strictEqual(p?.id, b.providerId, `"${a.prefix}" (${a.providerId}) swallowed "${b.prefix}" (${b.providerId})`);
        }
      }
    }
  });

  test('an unrecognised shape, and nothing at all, are undefined', () => {
    // Mistral's, SambaNova's and Z.ai's keys carry no prefix of their own, and
    // the empty string is what an untouched input box holds. undefined is the
    // answer in all of these; the caller asks the user.
    for (const key of ['', '   ', '\n', 'not-a-key', 'hunter2', 'Bearer abc', 'MOCHIII_API_KEY', 'sk_live_NOT-A-STRIPE-KEY', 'ghp_NOT-A-GITHUB-TOKEN']) {
      assert.strictEqual(providerForKey(key), undefined, `${JSON.stringify(key)} was attributed to a provider`);
    }
  });

  test('a pasted key keeps working with the newline paste brings, and case is not folded', () => {
    assert.strictEqual(providerForKey(`  gsk_${FAKE_SNAKE}\n`)?.id, 'groq', 'a trailing newline is normal when a key is pasted');
    // "AIza" is how Google writes it, and strings.HasPrefix in the daemon's
    // ProvidersForKey is case-sensitive too: folding case here would both invent
    // matches and make the panel and the daemon disagree about one key.
    assert.strictEqual(providerForKey(`aiza${FAKE}`), undefined);
    assert.strictEqual(providerForKey(`SK-ANT-${FAKE}`), undefined);
  });
});

suite('the prefixes mirror protocol/providers.go', () => {
  test('same prefixes, attributed to the same providers', () => {
    // The daemon is what actually reads a pasted key. If these two disagree, the
    // panel tells the user one provider and the daemon uses another.
    const go = goProviders();
    const fromGo = go
      .flatMap((p) => p.prefixes.map((prefix) => ({ prefix, providerId: p.id })))
      .sort((a, b) => a.prefix.localeCompare(b.prefix));
    const here = KEY_PREFIXES.map((s) => ({ prefix: s.prefix, providerId: s.providerId }))
      .sort((a, b) => a.prefix.localeCompare(b.prefix));
    assert.deepStrictEqual(here, fromGo, 'connectFlow.ts KEY_PREFIXES has drifted from protocol/providers.go KeyPrefixes');
  });

  test('every prefix the daemon knows is detected here too, with the same answer', () => {
    for (const p of goProviders()) {
      for (const prefix of p.prefixes) {
        assert.strictEqual(providerForKey(prefix + FAKE)?.id, p.id, `${prefix} is ${p.id} in providers.go`);
      }
    }
  });
});

suite('this file holds no credentials', () => {
  test('every key in it is visibly invented, and none is key-shaped', () => {
    // Pinned so the next person to add a shape cannot reach for a real key, or
    // for a random-looking string that a reader -- or a scanner -- would have to
    // treat as one. Both halves matter: the body says out loud that it is fake,
    // and it is words, so it has no run of key characters long enough for
    // daemon/scrub.go's patterns (32 for most, 20 at the shortest) to consider
    // it a credential at all.
    for (const { key } of FIXTURES) {
      assert.ok(/NOT.A.REAL.KEY/.test(key), `${key} does not say it is fake`);
      const run = /[A-Za-z0-9]{20,}/.exec(key);
      assert.strictEqual(run, null, `${key} contains a key-shaped run: ${run?.[0]}`);
    }
  });
});
