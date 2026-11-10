import * as assert from 'assert';
import * as cp from 'child_process';
import * as fs from 'fs';
import * as http from 'http';
import * as os from 'os';
import * as path from 'path';
import * as vscode from 'vscode';

import { ChatPanel } from '../../chatPanel';
import { probeDaemon, setWorkspaceRoot } from '../../daemonClient';
import { CREDENTIALS_FROM_STDIN_FLAG, credentialsLine, withoutCredentials } from '../../daemonCredentials';

const EXT_ID = 'MochiiiAIAgent.mochiii-vscode';
const daemonBin = path.resolve(__dirname, '../../../daemon/mochiii-daemon');

// A LOCAL COMMAND'S ANSWER IS NOT THE MODEL'S ANSWER.
//
// Every reply a local command printed ("mode: plan", "copied the last reply
// (252 characters)", "export cancelled") used to be stored in the transcript as
// an assistant turn, and the transcript is what four things read:
//
//   a) the history sent with the next question -- so the model was shown
//      sentences it never wrote, and billed for them;
//   b) /copy, which copies the last assistant turn -- so after any local
//      command it copied that command's answer, and before any model reply it
//      copied the answer to the previous command instead of saying there was
//      nothing to copy;
//   c) /export, which wrote them as "## Mochiii" turns;
//   d) /search, which matched them, though /help says it searches past turns.
//
// THE EVIDENCE IS WHAT THE MODEL RECEIVED. This drives the real ChatPanel, the
// real compiled daemonClient and the REAL daemon binary against a fake model
// that logs every request body it is sent, so (a) is read off the model's own
// request log rather than off the extension's idea of what it sent. (b)-(d) go
// through the panel's own slash handling with the two side effects (the
// clipboard, the save dialog) replaced by recorders.
//
// NEUTER CHECK: restoring `this.transcript.push(...)` in ChatPanel.replyLocal
// fails all four tests below (and only them in this file).

interface ModelRequest {
  messages: { role: string; content: string }[];
}

class FakeModel {
  readonly requests: ModelRequest[] = [];
  private server: http.Server | undefined;
  base = '';

  async start(): Promise<void> {
    this.server = http.createServer((req, res) => {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        const url = req.url ?? '';
        if (req.method === 'GET' && url.replace(/\/$/, '').endsWith('/models')) {
          res.setHeader('Content-Type', 'application/json');
          res.end(JSON.stringify({ object: 'list', data: [{ id: 'fake/fake-model', object: 'model', context_length: 128000 }] }));
          return;
        }
        if (req.method === 'POST' && url.replace(/\/$/, '').endsWith('/chat/completions')) {
          const body = JSON.parse(Buffer.concat(chunks).toString('utf8') || '{}');
          const messages = (body.messages ?? []).map((m: any) => ({
            role: String(m.role),
            content: typeof m.content === 'string' ? m.content : JSON.stringify(m.content ?? ''),
          }));
          this.requests.push({ messages });
          const n = this.requests.length;
          res.writeHead(200, { 'Content-Type': 'text/event-stream' });
          const send = (o: unknown): void => void res.write('data: ' + JSON.stringify(o) + '\n\n');
          const base = { id: 'fake', object: 'chat.completion.chunk', model: body.model };
          send({ ...base, choices: [{ index: 0, delta: { role: 'assistant' }, finish_reason: null }] });
          send({ ...base, choices: [{ index: 0, delta: { content: `FAKE-REPLY-${n} from the model` }, finish_reason: null }] });
          send({ ...base, choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] });
          send({ ...base, choices: [], usage: { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 } });
          res.end('data: [DONE]\n\n');
          return;
        }
        res.statusCode = 404;
        res.end('{}');
      });
    });
    // 127.0.0.1 here is only the test's own server; the live verification uses
    // the harness's fake model on the LAN address.
    await new Promise<void>((resolve) => this.server!.listen(0, '127.0.0.1', resolve));
    this.base = `http://127.0.0.1:${(this.server!.address() as any).port}/v1`;
  }

  async stop(): Promise<void> {
    await new Promise<void>((resolve) => (this.server ? this.server.close(() => resolve()) : resolve()));
  }
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

suite('E2E ChatPanel: local replies are not model replies (real daemon, fake model)', function () {
  this.timeout(90000);

  let model: FakeModel;
  let workspace: string;
  let runtimeDir: string;
  let prevRuntime: string | undefined;
  let daemon: cp.ChildProcess | undefined;
  let panel: any;
  let posts: any[];
  let clipboard: string[];
  let saved: string[];
  let restoreWebview: () => void;

  setup(async () => {
    assert.ok(fs.existsSync(daemonBin), `the daemon binary is not at ${daemonBin}. Run \`npm run build:daemon\` first`);
    const staged = path.join(path.dirname(daemonBin), 'models.json');
    if (!fs.existsSync(staged)) {
      fs.copyFileSync(path.resolve(__dirname, '../../../../../models.json'), staged);
    }
    // Activate first: activation records the window's folder as the workspace,
    // which would replace the one set below.
    const ext = vscode.extensions.getExtension(EXT_ID);
    assert.ok(ext, `extension ${EXT_ID} is not in the dev host`);
    await ext.activate();

    model = new FakeModel();
    await model.start();
    workspace = fs.mkdtempSync(path.join(os.tmpdir(), 'mlr-'));
    runtimeDir = fs.mkdtempSync(path.join(os.tmpdir(), 'mlrx-'));
    prevRuntime = process.env.XDG_RUNTIME_DIR;
    process.env.XDG_RUNTIME_DIR = runtimeDir;
    setWorkspaceRoot(workspace);

    // The real daemon, started the way the extension starts it: no credential
    // in its environment, a dummy one handed over on stdin.
    daemon = cp.spawn(daemonBin, ['--workspace', workspace, CREDENTIALS_FROM_STDIN_FLAG, '-log-file', path.join(workspace, 'daemon.log')], {
      cwd: workspace,
      stdio: ['pipe', 'ignore', 'ignore'],
      env: { ...withoutCredentials(process.env), MOCHIII_API_BASE: model.base },
    });
    daemon.stdin?.on('error', () => undefined);
    daemon.stdin?.end(credentialsLine({ api_key: 'dummy-not-a-key' }));
    for (let i = 0; i < 80 && !(await probeDaemon()); i++) {
      await new Promise((r) => setTimeout(r, 250));
    }
    assert.ok(await probeDaemon(), 'the real daemon did not answer a handshake');

    // A panel an earlier suite left open would only be revealed, not started
    // afresh against THIS daemon, and no preflight would ever arrive.
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

    // The two side effects of /copy and /export, recorded instead of performed
    // (the clipboard is the person's own; the save dialog needs a person).
    clipboard = [];
    saved = [];
    const localHost = panel.localHost.bind(panel);
    panel.localHost = () => ({
      ...localHost(),
      writeClipboard: async (t: string) => void clipboard.push(t),
      saveTextToFile: async (t: string) => {
        saved.push(t);
        return '';
      },
    });
  });

  teardown(async () => {
    try {
      restoreWebview?.();
      panel?.dispose();
    } catch {
      /* closing */
    }
    daemon?.kill('SIGTERM');
    await model.stop();
    setWorkspaceRoot('');
    if (prevRuntime === undefined) {
      delete process.env.XDG_RUNTIME_DIR;
    } else {
      process.env.XDG_RUNTIME_DIR = prevRuntime;
    }
    for (const d of [workspace, runtimeDir]) {
      try {
        fs.rmSync(d, { recursive: true, force: true });
      } catch {
        /* a daemon may still be releasing it */
      }
    }
  });

  // Sends one line through the panel and returns what the chat printed for it.
  async function say(text: string): Promise<string> {
    posts = [];
    panel.handleMessage({ type: 'prompt', text, autoApply: false });
    await waitUntil(() => posts.some((m) => m?.type === 'done' || m?.type === 'error'), 30000, `"${text}" to be answered`);
    assert.ok(!posts.some((m) => m?.type === 'error'), `"${text}" failed: ${JSON.stringify(posts.find((m) => m?.type === 'error'))}`);
    return posts
      .filter((m) => m?.type === 'token')
      .map((m) => String(m.text))
      .join('');
  }

  // Every conversation turn the model was ever sent, flattened: the question is
  // whether any sentence a local command printed is among them. The system
  // prompt is the daemon's own text, not a turn, and is left out.
  const sentToModel = (): string =>
    model.requests
      .map((r) =>
        r.messages
          .filter((m) => m.role !== 'system')
          .map((m) => `${m.role}: ${m.content}`)
          .join('\n'),
      )
      .join('\n----\n');

  test('(a) the model is never sent what a local command printed', async () => {
    assert.strictEqual(await say('/mode plan'), 'mode: plan');
    assert.strictEqual(await say('/mode auto'), 'mode: auto');
    // What these answer is asserted below, under (b) and (c); here they only
    // have to have been run.
    await say('/copy');
    await say('/export');

    const first = await say('how do I list the files');
    assert.match(first, /FAKE-REPLY-1/);
    assert.strictEqual(model.requests.length, 1);
    // The first question: the system prompt and the question, no turns of
    // anyone's in between.
    const roles = model.requests[0].messages.map((m) => m.role);
    assert.deepStrictEqual(
      roles.filter((r) => r !== 'system'),
      ['user'],
      `the first request carried more than the question: ${sentToModel()}`,
    );

    await say('/export');
    assert.strictEqual(await say('/mode manual'), 'mode: manual');
    await say('and in subdirectories');
    assert.strictEqual(model.requests.length, 2);
    // The second: the first exchange and the new question, nothing else.
    const second = model.requests[1].messages.filter((m) => m.role !== 'system');
    assert.deepStrictEqual(
      second.map((m) => m.role),
      ['user', 'assistant', 'user'],
      `the second request carried turns nobody took: ${sentToModel()}`,
    );
    assert.ok(second[1].content.includes('FAKE-REPLY-1'), second[1].content);

    // Whatever /copy and /export said, in either of its wordings.
    for (const printed of ['mode: plan', 'mode: auto', 'mode: manual', 'copy', 'export']) {
      assert.ok(!sentToModel().includes(printed), `the model was sent a local reply containing "${printed}"`);
    }
  });

  test('(a) the local replies still reach the screen', async () => {
    assert.strictEqual(await say('/mode plan'), 'mode: plan');
    const shown = posts.filter((m) => m?.type === 'token');
    assert.strictEqual(shown.length, 1);
    assert.strictEqual(shown[0].plain, true);
    assert.ok(posts.some((m) => m?.type === 'done'), 'the composer was never unlocked');
  });

  test('(b) /copy before any model reply says there is nothing to copy', async () => {
    assert.strictEqual(await say('/mode plan'), 'mode: plan');
    assert.match(await say('/copy'), /no reply to copy/);
    assert.deepStrictEqual(clipboard, [], `/copy wrote "${clipboard[0]}" to the clipboard`);
  });

  test('(b) /copy copies the model\'s last reply, not a command\'s answer after it', async () => {
    await say('how do I list the files');
    assert.strictEqual(await say('/mode plan'), 'mode: plan');
    const said = await say('/copy');
    assert.match(said, /^copied the last reply \(\d+ characters\)$/);
    assert.strictEqual(clipboard.length, 1);
    assert.ok(clipboard[0].includes('FAKE-REPLY-1'), `copied: ${JSON.stringify(clipboard[0])}`);
    // And again: the answer to /copy itself must not become the next target.
    await say('/copy');
    assert.strictEqual(clipboard[1], clipboard[0], 'the second /copy copied something else');
  });

  test('(c) /export writes the conversation, not the commands\' answers', async () => {
    assert.strictEqual(await say('/mode plan'), 'mode: plan');
    await say('how do I list the files');
    assert.strictEqual(await say('/mode auto'), 'mode: auto');
    assert.match(await say('/export'), /export cancelled/);
    assert.strictEqual(saved.length, 1);
    const md = saved[0];
    assert.ok(md.includes('how do I list the files') && md.includes('FAKE-REPLY-1'), md);
    assert.strictEqual(md.split('## Mochiii').length - 1, 1, `a local reply was exported as a Mochiii turn:\n${md}`);
    assert.strictEqual(md.split('## You').length - 1, 1, md);
    for (const printed of ['mode: plan', 'mode: auto']) {
      assert.ok(!md.includes(printed), `the export holds "${printed}":\n${md}`);
    }
  });

  test('(d) /search finds past turns, not the commands\' answers', async () => {
    await say('how do I list the files');
    assert.strictEqual(await say('/mode plan'), 'mode: plan');
    // "plan" is in the answer to /mode and in no turn of the conversation.
    assert.strictEqual(await say('/search plan'), 'no matching turns');
    // The control: a word that is in the conversation is still found, so the
    // empty answer above is not a search that finds nothing at all.
    const hit = await say('/search list the files');
    assert.match(hit, /^matches:\n1\. \[user\] how do I list the files$/);
    assert.match(await say('/search FAKE-REPLY'), /^matches:\n2\. \[assistant\] FAKE-REPLY-1/);
  });
});
