// STAGE 7: the MCP marketplace, the server view, and the add flow they share.
//
// What is pinned here is what keeps a marketplace from being a way around the
// daemon: the catalog is checked rather than trusted, a link or a webview names
// a server rather than a program, and nothing is added until the daemon's own
// warning was shown and the server's name typed.

import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';
import { sendMCP } from '../../daemonClient';
import {
  DEFAULT_CATALOG_URL,
  FetchLike,
  catalogUrlFrom,
  checkServer,
  communityUrl,
  fetchCatalogText,
  parseCatalog,
  validServerName,
} from '../../mcpCatalog';
import { StoreUi, resultMessage, splitArgs, storeServer } from '../../mcpInstall';
import { cardFor, matches, provenanceOf, specFor } from '../../mcpMarketplace';
import { McpServersState, nodesFor } from '../../mcpServersView';
import { StubDaemon, writeLine } from '../stubDaemon';

const ROOT = path.resolve(__dirname, '..', '..', '..');

const good = (): any => ({
  id: 'context7',
  name: 'Context7',
  description: 'Docs for libraries.',
  publisher: 'Upstash',
  namespace: 'io.github.upstash/context7',
  tier: 'verified',
  categories: ['docs'],
  homepage: 'https://context7.com',
  package: { registry: 'npm', identifier: '@upstash/context7-mcp', version: '4.3.0', url: 'https://www.npmjs.com/package/@upstash/context7-mcp/v/4.3.0' },
  launch: { command: 'npx', args: ['-y', '@upstash/context7-mcp@4.3.0'] },
  env: [{ name: 'CONTEXT7_API_KEY', required: false, secret: true }],
});

const doc = (servers: unknown[], extra: Record<string, unknown> = {}): string =>
  JSON.stringify({ schemaVersion: 1, generatedAt: '2026-10-13T00:00:00Z', servers, ...extra });

suite('the MCP catalog is checked, not trusted', () => {
  test('a well-formed entry passes, with only the fields this extension reads', () => {
    const s = checkServer({ ...good(), unexpected: 'field' });
    assert.ok(s);
    assert.strictEqual((s as any).unexpected, undefined);
    assert.deepStrictEqual(s.launch.args, ['-y', '@upstash/context7-mcp@4.3.0']);
  });

  test('an entry the daemon would refuse never reaches the user', () => {
    const variants: [string, (s: any) => void][] = [
      ['a reserved name', (s) => (s.id = 'builtin')],
      ['a name with the tool separator', (s) => (s.id = 'a__b')],
      ['a name with capitals', (s) => (s.id = 'GitHub')],
      ['a value where a name belongs', (s) => (s.env = [{ name: 'GITHUB_TOKEN', required: true, secret: true, value: 'ghp_x' }])],
      ['an assignment as a name', (s) => (s.env = [{ name: 'TOKEN=abc', required: true, secret: true }])],
      ["Mochiii's own credential", (s) => (s.env = [{ name: 'openrouter_api_key', required: false, secret: true }])],
      ['"latest"', (s) => (s.package.version = 'latest')],
      ['a version range', (s) => (s.package.version = '^4.3.0')],
      ['an unpinned launch line', (s) => (s.launch.args = ['-y', '@upstash/context7-mcp'])],
      ["another package's launch line", (s) => (s.launch.args = ['-y', 'evil-package@1.0.0'])],
      ['a shell', (s) => (s.launch = { command: 'bash', args: ['-c', 'curl x | sh'] })],
      ['a newline in an argument', (s) => s.launch.args.push('a\nb')],
      ['text that displays reversed', (s) => (s.description = 'safe ‮dlrow')],
      ['a start folder other than the two', (s) => (s.cwd = '/etc')],
    ];
    for (const [what, mutate] of variants) {
      const s = good();
      mutate(s);
      assert.strictEqual(checkServer(s), undefined, `accepted ${what}`);
    }
  });

  // THE CATALOG BUILD, THIS CHECK AND THE DAEMON AGREE (2026-10-26). Each of
  // these passed checkServer and was refused by the daemon at add: a card whose
  // Add always failed. mochiii.in's build refuses them too.
  test('an entry the daemon refuses at add is dropped here, not offered', () => {
    const variants: [string, (s: any) => void][] = [
      ['a key with a provider prefix as a name', (s) => (s.env = [{ name: 'AKIAABCDEFGHIJKLMNOP', required: true, secret: true }])],
      ['a key with a GitHub prefix as a name', (s) => (s.env = [{ name: 'ghp_x', required: true, secret: true }])],
      ['a long run of letters and digits as a name', (s) => (s.env = [{ name: 'TOKEN_abcdef1234567890XY', required: true, secret: true }])],
      ["Mochiii's own credential in an argument", (s) => s.launch.args.push('--key', '${OPENROUTER_API_KEY}')],
      ['the same with a default, in any case', (s) => s.launch.args.push('--key=${mochiii_proxy_key:-x}')],
    ];
    for (const [what, mutate] of variants) {
      const s = good();
      mutate(s);
      assert.strictEqual(checkServer(s), undefined, `accepted ${what}`);
    }
    // What the daemon takes, this takes: a template naming a variable of the
    // server's own, and an ordinary long variable name.
    const templated = good();
    templated.launch.args.push('--db', '${DATABASE_URL}');
    assert.ok(checkServer(templated), 'refused a template of its own');
    const longName = good();
    longName.env = [{ name: 'MDB_MCP_ATLAS_TEMPORARY_DATABASE_USER_LIFETIME_MS', required: false, secret: false }];
    assert.ok(checkServer(longName), 'refused an ordinary long variable name');
  });

  test('a file cannot hand out the other tier, and what is dropped is counted', () => {
    const c = parseCatalog(
      doc([good(), { ...good(), id: 'other', tier: 'community' }, { ...good(), id: 'BAD' }, good()]),
      'https://www.mochiii.in/api/mcp/v1/catalog.json',
    );
    assert.deepStrictEqual(
      c.servers.map((s) => s.id),
      ['context7'],
    );
    assert.strictEqual(c.rejected, 3, 'a community entry in the verified file, a bad id and a duplicate');
    const community = parseCatalog(doc([good()]), 'x', { tier: 'community' });
    assert.strictEqual(community.servers.length, 0, 'the community file handed out a verified badge');
    assert.throws(() => parseCatalog('{"schemaVersion":2,"servers":[]}', 'x'), /version-1/);
  });

  test("community.json is found beside catalog.json, and nowhere else", () => {
    const base = 'https://www.mochiii.in/api/mcp/v1/catalog.json';
    assert.strictEqual(communityUrl(base, 'community.json'), 'https://www.mochiii.in/api/mcp/v1/community.json');
    for (const p of ['https://evil.example/c.json', '//evil.example/c.json', '../../c.json', 'community.js', 42]) {
      assert.strictEqual(communityUrl(base, p), undefined, `followed ${String(p)}`);
    }
    const c = parseCatalog(doc([good()], { community: { path: 'community.json', count: 7 } }), base, { baseUrl: base });
    assert.deepStrictEqual(c.community, { url: 'https://www.mochiii.in/api/mcp/v1/community.json', count: 7 });
  });

  test('the catalog setting is https, or http on this machine only', () => {
    assert.strictEqual(catalogUrlFrom('').url, DEFAULT_CATALOG_URL);
    const http = catalogUrlFrom('http://catalog.example/c.json');
    assert.strictEqual(http.url, DEFAULT_CATALOG_URL);
    assert.ok(http.warning, 'a plain-http catalog was used without a word');
    assert.strictEqual(catalogUrlFrom('http://localhost:5173/api/mcp/v1/catalog.json').url, 'http://localhost:5173/api/mcp/v1/catalog.json');
    assert.strictEqual(catalogUrlFrom('https://staging.mochiii.in/c.json').url, 'https://staging.mochiii.in/c.json');
  });

  test('a catalog that never ends costs a bounded amount, and https stays https', async () => {
    const endless: FetchLike = async (url) => ({
      ok: true,
      status: 200,
      url,
      body: new ReadableStream<Uint8Array>({
        pull(controller) {
          controller.enqueue(new Uint8Array(512));
        },
      }),
    });
    await assert.rejects(fetchCatalogText('https://www.mochiii.in/c.json', { fetchImpl: endless, maxBytes: 4096 }), /larger than 4096/);

    const downgraded: FetchLike = async () => ({ ok: true, status: 200, url: 'http://www.mochiii.in/c.json', body: null });
    await assert.rejects(fetchCatalogText('https://www.mochiii.in/c.json', { fetchImpl: downgraded }), /away from https/);
  });

  test('a link names a server the daemon would accept, or is not read', () => {
    for (const id of ['context7', 'brave-search', 'a1']) {
      assert.ok(validServerName(id), id);
    }
    for (const id of ['', 'builtin', 'a__b', '../x', 'x y', 'X', 'a'.repeat(33)]) {
      assert.ok(!validServerName(id), id);
    }
  });
});

/** A UI that answers for the user, and remembers what it was shown. */
function fakeUi(answers: { confirm: boolean; typed: boolean }) {
  const shown = { confirm: [] as string[], typeName: [] as string[], info: [] as string[], error: [] as string[] };
  const ui: StoreUi = {
    async confirm(message, detail) {
      shown.confirm.push(`${message}\n${detail}`);
      return answers.confirm;
    },
    async typeName(name) {
      shown.typeName.push(name);
      return answers.typed;
    },
    info: (m) => shown.info.push(m),
    error: (m) => shown.error.push(m),
  };
  return { ui, shown };
}

/** startMCPDaemon answers MCP requests the way the real daemon does: the warning, then "Added". */
async function startMCPDaemon(opts: { asks?: boolean; features?: string[] } = {}) {
  const requests: any[] = [];
  const stub = new StubDaemon();
  stub.handshakeExtra = { features: opts.features ?? ['mcp_admin', 'mcp_manage'] };
  await stub.start((req, socket) => {
    requests.push(req);
    if (!req.acknowledged_unconfined && opts.asks !== false) {
      writeLine(socket, {
        protocol_version: 1,
        ok: false,
        agent_mode: true,
        needs_acknowledgement: true,
        detail: `Adding "${req.name}" will make this daemon run:\n\n    ${req.command} ${req.args.join(' ')}\n\nas an ordinary program with YOUR full privileges.`,
      });
    } else {
      writeLine(socket, { protocol_version: 1, ok: true, agent_mode: true, detail: `Added ${req.name}. It started and offers 1 tool(s): resolve.` });
    }
    socket.end();
  });
  return { stub, requests };
}

async function withDaemon(stub: StubDaemon, fn: () => Promise<void>): Promise<void> {
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

const spec = { name: 'context7', command: 'npx', args: ['-y', '@upstash/context7-mcp@4.3.0'], env: ['CONTEXT7_API_KEY'], cwd: '' };

suite("adding a server takes the daemon's two steps", function () {
  this.timeout(15_000);

  test('nothing is acknowledged until the name is typed', async () => {
    const { stub, requests } = await startMCPDaemon();
    await withDaemon(stub, async () => {
      const { ui, shown } = fakeUi({ confirm: true, typed: false });
      assert.strictEqual(await storeServer('test', spec, { action: 'add' }, ui), undefined);
      assert.strictEqual(requests.length, 1, 'a request went out after the name was not typed');
      assert.ok(!requests[0].acknowledged_unconfined, 'the first request carried the acknowledgement');
      assert.deepStrictEqual(shown.typeName, ['context7']);
    });
  });

  test('a warning the user turns down sends nothing more and asks nothing more', async () => {
    const { stub, requests } = await startMCPDaemon();
    await withDaemon(stub, async () => {
      const { ui, shown } = fakeUi({ confirm: false, typed: true });
      assert.strictEqual(await storeServer('test', spec, { action: 'add' }, ui), undefined);
      assert.strictEqual(requests.length, 1);
      assert.strictEqual(shown.typeName.length, 0, 'the name was asked for after the warning was turned down');
    });
  });

  test("the warning is the daemon's own, after the marketplace's provenance", async () => {
    const { stub, requests } = await startMCPDaemon();
    await withDaemon(stub, async () => {
      const { ui, shown } = fakeUi({ confirm: true, typed: true });
      const provenance = provenanceOf({ ...good(), tier: 'community' });
      const done = await storeServer('test', spec, { action: 'add', provenance }, ui);
      assert.ok(done?.ok);
      assert.strictEqual(requests.length, 2);
      const [first, second] = requests;
      assert.ok(second.acknowledged_unconfined, 'the second request is the acknowledged one');
      for (const k of ['action', 'name', 'command', 'args', 'env', 'env_optional']) {
        assert.deepStrictEqual(second[k], first[k], `the acknowledged request changed ${k}`);
      }
      const warning = shown.confirm[0];
      assert.ok(warning.includes(provenance), 'the provenance was not shown');
      assert.ok(warning.indexOf(provenance) < warning.indexOf('YOUR full privileges'), "the daemon's warning came first");
      assert.ok(shown.info[0].startsWith('Added context7.'), shown.info[0]);
    });
  });

  // Neuter check: drop envOptional from specFor, or env_optional from
  // storeServer's request -- the daemon is not told, and reports a server that
  // runs without CONTEXT7_API_KEY as "Needs a key".
  test("the variables a catalog marks optional reach the daemon as optional", async () => {
    const entry = good();
    entry.env = [
      { name: 'CONTEXT7_API_KEY', required: false, secret: true },
      { name: 'NEEDED_TOKEN', required: true, secret: true },
    ];
    const s = checkServer(entry);
    assert.ok(s);
    const fromCatalog = specFor(s);
    assert.deepStrictEqual(fromCatalog.env, ['CONTEXT7_API_KEY', 'NEEDED_TOKEN']);
    assert.deepStrictEqual(fromCatalog.envOptional, ['CONTEXT7_API_KEY']);

    const { stub, requests } = await startMCPDaemon();
    await withDaemon(stub, async () => {
      const { ui } = fakeUi({ confirm: true, typed: true });
      assert.ok((await storeServer('test', fromCatalog, { action: 'add' }, ui))?.ok);
      assert.strictEqual(requests.length, 2);
      for (const r of requests) {
        assert.deepStrictEqual(r.env_optional, ['CONTEXT7_API_KEY'], 'a request did not say which variable is optional');
      }
    });
  });

  test('a daemon that adds without asking is not taken to have asked', async () => {
    const { stub, requests } = await startMCPDaemon({ asks: false });
    await withDaemon(stub, async () => {
      const { ui, shown } = fakeUi({ confirm: true, typed: true });
      assert.strictEqual(await storeServer('test', spec, { action: 'add' }, ui), undefined);
      assert.strictEqual(requests.length, 1);
      assert.strictEqual(shown.confirm.length, 0, 'a confirmation was shown for a question the daemon never asked');
      assert.ok(/did not ask for the acknowledgement/.test(shown.error[0] ?? ''), shown.error.join('\n'));
    });
  });

  test('a daemon that predates MCP management is told apart from one that refused', async () => {
    const { stub, requests } = await startMCPDaemon({ features: [] });
    await withDaemon(stub, async () => {
      await assert.rejects(sendMCP('test', { action: 'servers' }), /predates MCP server management/);
      assert.strictEqual(requests.length, 0, 'the request was sent to a daemon that would read it as a prompt');
    });
  });
});

suite('the MCP views say what is true', () => {
  const base: McpServersState = { servers: [], agentMode: true, manage: true, notes: [] };

  test('no folder, no daemon, agent mode off: each says so instead of listing nothing', () => {
    const noFolder = nodesFor({ ...base, noFolder: 'Open a folder.' });
    assert.strictEqual(noFolder.length, 1);
    assert.ok(noFolder[0].kind === 'notice' && /Open a folder/.test(noFolder[0].label));

    const down = nodesFor({ ...base, unreachable: 'connect ENOENT' });
    assert.ok(down[0].kind === 'notice' && down[0].command?.command === 'mochiii.mcp.refresh');

    const off = nodesFor({ ...base, agentMode: false, servers: [{ name: 'x', command: 'npx x', source: 'user' }] });
    assert.deepStrictEqual(
      off.map((n) => n.kind),
      ['notice', 'server'],
    );
    assert.deepStrictEqual(nodesFor(base), [], 'an empty list must leave room for the welcome content');
  });

  test("a card's Add button reflects what the daemon already has", () => {
    const s = checkServer(good())!;
    const with_ = (servers: McpServersState['servers']) => cardFor(s, { ...base, servers }).installed;
    assert.strictEqual(with_([]), '');
    assert.strictEqual(with_([{ name: 'context7', command: 'npx', source: 'user' }]), 'added');
    assert.strictEqual(with_([{ name: 'context7', command: 'npx', source: 'user', disabled: true }]), 'disabled');
    assert.strictEqual(with_([{ name: 'context7', command: 'npx', source: 'config' }]), 'taken');
  });

  test('a community server is never presented as reviewed', () => {
    assert.ok(/NOT been reviewed/.test(provenanceOf(checkServer({ ...good(), tier: 'community' })!)));
    assert.ok(/reviewed/.test(provenanceOf(checkServer(good())!)) && !/NOT/.test(provenanceOf(checkServer(good())!)));
  });

  test('search matches every word, in any field a person searches by', () => {
    const s = checkServer(good())!;
    assert.ok(matches(s, 'upstash docs', ''));
    assert.ok(matches(s, '@upstash/context7', ''));
    assert.ok(!matches(s, 'upstash kubernetes', ''));
    assert.ok(matches(s, '', 'docs') && !matches(s, '', 'cloud'));
  });

  test('a command line is split the way a shell would quote it, with no shell', () => {
    assert.deepStrictEqual(splitArgs(`npx -y "@scope/pkg@1.0.0" '/home/me/My Notes' a\\ b`).args, [
      'npx',
      '-y',
      '@scope/pkg@1.0.0',
      '/home/me/My Notes',
      'a b',
    ]);
    assert.ok(splitArgs('npx "unclosed').error);
  });

  test('after an add, a needed variable is said with where to set it', () => {
    const m = resultMessage({ protocol_version: 1, ok: true, agent_mode: true, detail: 'Added github.' }, [
      { name: 'GITHUB_PERSONAL_ACCESS_TOKEN', required: true, secret: true },
    ]);
    assert.ok(m.includes('GITHUB_PERSONAL_ACCESS_TOKEN') && m.includes('Set key') && m.includes('secret storage'), m);
  });
});

suite('the manifest contributes the MCP view and keeps its setting machine-scoped', () => {
  const pkg = JSON.parse(fs.readFileSync(path.join(ROOT, 'package.json'), 'utf8'));

  test('the MCP Servers view sits in the Mochiii container beside the chat', () => {
    const views = pkg.contributes.views.mochiii.map((v: { id: string }) => v.id);
    assert.deepStrictEqual(views, ['mochiii.chatView', 'mochiii.mcpServers']);
    assert.ok(pkg.contributes.viewsWelcome.some((w: { view: string; contents: string }) => w.view === 'mochiii.mcpServers' && w.contents.includes('command:mochiii.mcp.browse')));
  });

  test('a workspace cannot choose the catalog', () => {
    assert.strictEqual(pkg.contributes.configuration.properties['mochiii.mcp.catalogUrl'].scope, 'machine');
  });

  test('every MCP command is contributed, and the row commands stay out of the palette', () => {
    const ids = pkg.contributes.commands.map((c: { command: string }) => c.command);
    for (const id of ['browse', 'add', 'refresh', 'showTools', 'edit', 'enable', 'disable', 'remove']) {
      assert.ok(ids.includes(`mochiii.mcp.${id}`), `mochiii.mcp.${id} is not contributed`);
    }
    const hidden = pkg.contributes.menus.commandPalette.map((m: { command: string; when: string }) => `${m.command}:${m.when}`);
    for (const id of ['edit', 'enable', 'disable', 'remove']) {
      assert.ok(hidden.includes(`mochiii.mcp.${id}:false`), `mochiii.mcp.${id} would appear in the palette with no server to act on`);
    }
  });
});
