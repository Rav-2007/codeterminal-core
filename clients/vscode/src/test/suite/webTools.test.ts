import * as assert from 'assert';
import * as cp from 'child_process';
import * as fs from 'fs';
import * as http from 'http';
import * as os from 'os';
import * as path from 'path';
import * as vscode from 'vscode';

import { useAutoConsent } from '../../autoConsent';
import { ChatPanel } from '../../chatPanel';
import { probeDaemon, setWorkspaceRoot } from '../../daemonClient';
import { CREDENTIALS_FROM_STDIN_FLAG, credentialsLine, withoutCredentials } from '../../daemonCredentials';

// WEB SEARCH IN VS CODE, AS IN THE TERMINAL (parity, 2026-11-28).
//
// The extension staged the repository's models.json, which turns web_search and
// web_fetch off (models.json: "web": {"disabled": true}, both "deny"), while the
// terminal runs the built-in config where both are on. So a question that
// needed the web got a model saying it had no way to look it up, and
// autoAccess.ts's "web search no longer asks" (owner, 2026-11-21) was never
// reached: the tools were never offered.
//
// THE EVIDENCE IS WHAT THE MODEL WAS OFFERED, read off a fake model's request
// log, with the REAL daemon started on the file the package stages
// (scripts/stagedConfig.js) -- and what the panel did with the daemon's
// question: Manual shows the card, Auto answers it.
//
// No test here reaches the internet: Manual's search is denied, and Auto's
// fetch names 127.0.0.1, which the daemon's address gate refuses before any
// connection.
//
// A MODEL ON THIS COMPUTER keeps its own rule: no internet tools. The menu is
// not a fence (a model can name a tool it was not offered), so that test has
// the model call web_search anyway and reads the refusal.

const EXT_ID = 'MochiiiAIAgent.mochiii-vscode';
const ROOT = path.resolve(__dirname, '../../..');
const daemonBin = path.join(ROOT, 'daemon', 'mochiii-daemon');

// eslint-disable-next-line @typescript-eslint/no-require-imports
const { stagedModelsJson } = require(path.join(ROOT, 'scripts/stagedConfig.js'));
// eslint-disable-next-line @typescript-eslint/no-require-imports
const { withSegmentBudget } = require(path.join(ROOT, 'scripts/segmentBudget.js'));

const rootConfig = (): string => fs.readFileSync(path.resolve(ROOT, '../../models.json'), 'utf8');

suite('web tools: the staged models.json', () => {
  test('turns web_search and web_fetch on, asking, and changes nothing else', () => {
    const staged = JSON.parse(stagedModelsJson(rootConfig()));
    assert.ok(!staged.mcp.web?.disabled, 'the staged file still turns the web tools off');
    assert.strictEqual(staged.mcp.builtin.tools.web_search, 'ask');
    assert.strictEqual(staged.mcp.builtin.tools.web_fetch, 'ask');

    // Everything else is the budgets' file, which is the root file plus budgets
    // (taskRun.test.ts holds that half).
    const budgeted = JSON.parse(withSegmentBudget(rootConfig()));
    budgeted.mcp.web = staged.mcp.web;
    budgeted.mcp.builtin.tools.web_search = 'ask';
    budgeted.mcp.builtin.tools.web_fetch = 'ask';
    assert.deepStrictEqual(staged, budgeted, 'the staged file differs in more than its budgets and web tools');
  });

  test('the repository models.json is left as it is', () => {
    const root = JSON.parse(rootConfig());
    assert.strictEqual(root.mcp.builtin.tools.web_search, 'deny');
    assert.strictEqual(root.mcp.web.disabled, true);
  });
});

interface Seen {
  tools: string[];
  lastRole: string;
  lastTool: string;
}

// A model that calls the tool its tag names, once, then answers.
class ScriptedModel {
  readonly seen: Seen[] = [];
  private server: http.Server | undefined;
  base = '';

  async start(host: string): Promise<void> {
    this.server = http.createServer((req, res) => {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        const url = (req.url ?? '').replace(/\/$/, '');
        if (req.method === 'GET' && url.endsWith('/models')) {
          res.setHeader('Content-Type', 'application/json');
          res.end(JSON.stringify({ object: 'list', data: [{ id: 'fake/web-model', object: 'model', context_length: 128000 }] }));
          return;
        }
        if (!(req.method === 'POST' && url.endsWith('/chat/completions'))) {
          res.statusCode = 404;
          res.end('{}');
          return;
        }
        const body = JSON.parse(Buffer.concat(chunks).toString('utf8') || '{}');
        const messages: any[] = body.messages ?? [];
        const text = (m: any): string => (typeof m?.content === 'string' ? m.content : JSON.stringify(m?.content ?? ''));
        const tools: string[] = (body.tools ?? []).map((t: any) => String(t.function?.name));
        const last = messages[messages.length - 1];
        const lastUser = [...messages].reverse().find((m) => m.role === 'user');
        this.seen.push({ tools, lastRole: String(last?.role), lastTool: last?.role === 'tool' ? text(last) : '' });

        let call: { name: string; args: unknown } | undefined;
        if (last?.role !== 'tool') {
          if (/WEB-SEARCH/.test(text(lastUser))) {
            call = { name: 'builtin__web_search', args: { query: 'latest Go release' } };
          } else if (/WEB-FETCH-LOCAL/.test(text(lastUser))) {
            call = { name: 'builtin__web_fetch', args: { url: 'http://127.0.0.1:9/never' } };
          }
        }
        res.writeHead(200, { 'Content-Type': 'text/event-stream' });
        const send = (o: unknown): void => void res.write('data: ' + JSON.stringify(o) + '\n\n');
        const base = { id: 'fake', object: 'chat.completion.chunk', model: body.model };
        send({ ...base, choices: [{ index: 0, delta: { role: 'assistant' }, finish_reason: null }] });
        if (call) {
          send({
            ...base,
            choices: [{
              index: 0,
              delta: { tool_calls: [{ index: 0, id: `call_${this.seen.length}`, type: 'function', function: { name: call.name, arguments: JSON.stringify(call.args) } }] },
              finish_reason: null,
            }],
          });
          send({ ...base, choices: [{ index: 0, delta: {}, finish_reason: 'tool_calls' }] });
        } else {
          send({ ...base, choices: [{ index: 0, delta: { content: 'WEB-DONE' }, finish_reason: null }] });
          send({ ...base, choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] });
        }
        send({ ...base, choices: [], usage: { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 } });
        res.end('data: [DONE]\n\n');
      });
    });
    await new Promise<void>((resolve) => this.server!.listen(0, host, resolve));
    this.base = `http://${host}:${(this.server!.address() as any).port}/v1`;
  }

  async stop(): Promise<void> {
    await new Promise<void>((resolve) => (this.server ? this.server.close(() => resolve()) : resolve()));
  }
}

// A provider's model is any address that is not this computer's loopback: the
// daemon calls a loopback one "a model on this computer". The first non-loopback
// IPv4 address of this machine plays the provider; nothing leaves it.
function providerAddress(): string | undefined {
  for (const list of Object.values(os.networkInterfaces())) {
    for (const a of list ?? []) {
      if (a.family === 'IPv4' && !a.internal) {
        return a.address;
      }
    }
  }
  return undefined;
}

async function waitUntil(done: () => boolean, timeoutMs: number, what: string): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (done()) {
      return;
    }
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(`timed out after ${timeoutMs}ms waiting for ${what}`);
}

suite('E2E web tools in the panel (real daemon on the staged models.json, fake model)', function () {
  this.timeout(90000);

  let model: ScriptedModel;
  let workspace: string;
  let runtimeDir: string;
  let daemonHome: string;
  let prevRuntime: string | undefined;
  let daemon: cp.ChildProcess | undefined;
  let panel: any;
  let posts: any[];
  let restoreWebview: () => void;

  async function start(host: string): Promise<void> {
    assert.ok(fs.existsSync(daemonBin), `the daemon binary is not at ${daemonBin}. Run \`npm run build:daemon\` first`);
    const ext = vscode.extensions.getExtension(EXT_ID);
    assert.ok(ext, `extension ${EXT_ID} is not in the dev host`);
    await ext.activate();

    model = new ScriptedModel();
    await model.start(host);
    workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'mwt-'));
    runtimeDir = fs.mkdtempSync(path.join(os.tmpdir(), 'mwtx-'));
    // The daemon's own home: its chat store and keys go here, never into the
    // home of whoever runs the suite.
    daemonHome = fs.mkdtempSync(path.join(os.tmpdir(), 'mwth-'));
    prevRuntime = process.env.XDG_RUNTIME_DIR;
    process.env.XDG_RUNTIME_DIR = runtimeDir;
    setWorkspaceRoot(workspace);

    // The file the package ships beside its daemon, built the way
    // stage-runtime.js builds it.
    const config = path.join(runtimeDir, 'models.json');
    fs.writeFileSync(config, stagedModelsJson(rootConfig()));
    daemon = cp.spawn(daemonBin, ['--workspace', workspace, '--config', config, CREDENTIALS_FROM_STDIN_FLAG, '-log-file', path.join(workspace, 'daemon.log')], {
      cwd: workspace,
      stdio: ['pipe', 'ignore', 'ignore'],
      env: {
        ...withoutCredentials(process.env),
        HOME: daemonHome,
        XDG_STATE_HOME: path.join(daemonHome, '.local', 'state'),
        MOCHIII_API_BASE: model.base,
      },
    });
    daemon.stdin?.on('error', () => undefined);
    daemon.stdin?.end(credentialsLine({ api_key: 'dummy-not-a-key' }));
    for (let i = 0; i < 80 && !(await probeDaemon()); i++) {
      await new Promise((r) => setTimeout(r, 250));
    }
    assert.ok(await probeDaemon(), 'the real daemon did not answer a handshake');

    (ChatPanel as any).current?.dispose();
    ChatPanel.createOrShow(ext.extensionUri);
    panel = (ChatPanel as any).current;
    assert.ok(panel, 'createOrShow left no panel');
    posts = [];
    const webview = panel.host.webview;
    const original = webview.postMessage.bind(webview);
    Object.defineProperty(webview, 'postMessage', {
      configurable: true,
      writable: true,
      value: (msg: any) => {
        posts.push(msg);
        return original(msg);
      },
    });
    restoreWebview = () => Object.defineProperty(webview, 'postMessage', { configurable: true, writable: true, value: original });
    await waitUntil(() => posts.some((m) => m?.type === 'history' || m?.type === 'error'), 20000, 'the preflight');
    // Auto's one-time warning, accepted: the warning is autoAccess.test's subject.
    useAutoConsent({ get: () => true, update: async () => undefined } as unknown as vscode.Memento);
  }

  teardown(async () => {
    useAutoConsent(undefined);
    try {
      restoreWebview?.();
      panel?.dispose();
    } catch {
      /* closing */
    }
    daemon?.kill('SIGTERM');
    daemon = undefined;
    await model?.stop();
    setWorkspaceRoot('');
    if (prevRuntime === undefined) {
      delete process.env.XDG_RUNTIME_DIR;
    } else {
      process.env.XDG_RUNTIME_DIR = prevRuntime;
    }
    for (const d of [workspace, runtimeDir, daemonHome]) {
      try {
        fs.rmSync(d, { recursive: true, force: true });
      } catch {
        /* a daemon may still be releasing it */
      }
    }
  });

  // Sends a question and waits for its end, answering each card with decide.
  async function ask(text: string, autoApply: boolean, decide: (req: any) => string): Promise<any[]> {
    posts = [];
    const answered = new Set<string>();
    panel.handleMessage({ type: 'prompt', text, autoApply });
    const deadline = Date.now() + 40000;
    while (Date.now() < deadline) {
      for (const m of posts) {
        if (m?.type === 'toolApproval' && !answered.has(m.request.call_id)) {
          answered.add(m.request.call_id);
          panel.handleMessage({ type: 'toolApprovalDecision', callId: m.request.call_id, decision: decide(m.request) });
        }
      }
      if (posts.some((m) => m?.type === 'done' || m?.type === 'error')) {
        return posts;
      }
      await new Promise((r) => setTimeout(r, 50));
    }
    throw new Error(`"${text}" was not answered: ${JSON.stringify(posts.map((m) => m?.type))}`);
  }

  test('a provider\'s model is offered web_search and web_fetch, and Manual asks before a search', async function () {
    const host = providerAddress();
    if (!host) {
      this.skip(); // no address but loopback: every model would be "on this computer"
      return;
    }
    await start(host);
    const seen = await ask('WEB-SEARCH what is the latest Go release?', false, () => 'deny');
    assert.ok(model.seen.length > 0, 'the model was never asked');
    assert.ok(model.seen[0].tools.includes('builtin__web_search'), `web_search was not offered: ${model.seen[0].tools.join(', ')}`);
    assert.ok(model.seen[0].tools.includes('builtin__web_fetch'), `web_fetch was not offered: ${model.seen[0].tools.join(', ')}`);
    const cards = seen.filter((m) => m?.type === 'toolApproval');
    assert.strictEqual(cards.length, 1, `Manual did not ask exactly once: ${JSON.stringify(cards)}`);
    assert.strictEqual(cards[0].request.tool, 'web_search');
  });

  test('in Auto, a web tool runs without asking', async function () {
    const host = providerAddress();
    if (!host) {
      this.skip();
      return;
    }
    await start(host);
    const seen = await ask('WEB-FETCH-LOCAL read the page', true, () => 'deny');
    assert.deepStrictEqual(seen.filter((m) => m?.type === 'toolApproval'), [], 'Auto showed a card for a web tool');
    const result = model.seen.find((s) => s.lastRole === 'tool');
    assert.ok(result, `the web_fetch call never ran: ${JSON.stringify(model.seen)}`);
    // It ran, and the daemon's address gate -- unchanged -- refused the address.
    assert.match(result.lastTool, /127\.0\.0\.1|private|local|refus|not allowed/i, `unexpected web_fetch result: ${result.lastTool}`);
  });

  test('a model on this computer gets no web tools, and one it names anyway is refused', async () => {
    await start('127.0.0.1');
    const seen = await ask('WEB-SEARCH what is the latest Go release?', true, () => 'deny');
    assert.ok(model.seen.length > 0, 'the model was never asked');
    for (const tool of ['builtin__web_search', 'builtin__web_fetch']) {
      assert.ok(!model.seen[0].tools.includes(tool), `${tool} was offered to a model on this computer`);
    }
    assert.ok(model.seen[0].tools.includes('builtin__read_file'), 'a model on this computer lost its other tools');
    assert.deepStrictEqual(seen.filter((m) => m?.type === 'toolApproval'), [], 'a refused tool still asked');
    const result = model.seen.find((s) => s.lastRole === 'tool');
    assert.ok(result, 'the call named anyway got no answer');
    assert.match(result.lastTool, /internet tools are off for this model|not offered|no tool called "builtin__web_search" available/i, `the call was not refused: ${result.lastTool}`);
  });
});
