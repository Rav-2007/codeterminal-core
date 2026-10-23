// A TURN THAT HEARS NOTHING MUST END, AND A QUESTION NEVER SENT MUST SAY SO.
//
// Found 2026-10-23: "Mochiii is answering" stayed on screen for nearly two
// hours with no answer, no error, and no prompt in the daemon log. Driven at
// the protocol level, the daemon answered a keyless prompt with a classified
// auth error in 30 ms -- so the prompt never reached it. The panel had held it
// back for a key (connectThenStart) while the webview showed it as being
// answered, and nothing on this side would ever have ended a turn either.
//
// These put a stub daemon behind a real panel. Each limit is shortened
// (streamSilenceLimits), and each test checks the limit ends what it should
// AND leaves alone what it should not: a turn waiting on the user's approval,
// a tool that is running, a question that is sent once a key connects.

import * as assert from 'assert';
import * as vscode from 'vscode';

import { ChatPanel, NO_KEY_CLASS } from '../../chatPanel';
import { NO_RESPONSE_CLASS, streamSilenceLimits } from '../../daemonClient';
import { Behavior, StubDaemon, writeLine } from '../stubDaemon';

const EXT_ID = 'MochiiiAIAgent.mochiii-vscode';

async function waitUntil(done: () => boolean, timeoutMs: number, what: string, state?: () => unknown): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (done()) {
      return;
    }
    await new Promise((r) => setTimeout(r, 25));
  }
  throw new Error(`timed out after ${timeoutMs}ms waiting for ${what}` + (state ? `; state: ${JSON.stringify(state())}` : ''));
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

// The same harness as turnRecovery.test.ts: activate first, then the stub.
async function withPanel(
  behavior: Behavior,
  handshakeExtra: Record<string, unknown>,
  fn: (panel: any, posts: any[], stub: StubDaemon) => Promise<void>,
): Promise<void> {
  const ext = vscode.extensions.getExtension(EXT_ID);
  assert.ok(ext, `extension ${EXT_ID} is not in the dev host`);
  await ext.activate();
  const prev = process.env.XDG_RUNTIME_DIR;
  const limits = { ...streamSilenceLimits };
  const stub = new StubDaemon();
  stub.handshakeExtra = handshakeExtra;
  await stub.start(behavior);
  (ChatPanel as any).current?.dispose();
  ChatPanel.createOrShow(ext.extensionUri);
  const panel: any = (ChatPanel as any).current;
  const posts: any[] = [];
  const webview = panel.host.webview;
  const original = webview.postMessage.bind(webview);
  Object.defineProperty(webview, 'postMessage', {
    configurable: true,
    writable: true,
    value: (msg: any) => {
      posts.push({ ...msg, at: Date.now() });
      return original(msg);
    },
  });
  try {
    await waitUntil(() => posts.some((m) => m?.type === 'history' || m?.type === 'error'), 10_000, 'the preflight');
    await fn(panel, posts, stub);
  } finally {
    Object.assign(streamSilenceLimits, limits);
    Object.defineProperty(webview, 'postMessage', { configurable: true, writable: true, value: original });
    panel.host.close();
    panel.dispose();
    await stub.stop();
    if (prev === undefined) {
      delete process.env.XDG_RUNTIME_DIR;
    } else {
      process.env.XDG_RUNTIME_DIR = prev;
    }
  }
}

const errorsOf = (posts: any[], cls: string) => posts.filter((m) => m?.type === 'error' && m.errorClass === cls);

suite('a turn that hears nothing', function () {
  this.timeout(30_000);

  test('a daemon that takes the question and says nothing: the panel stops waiting, says so, and takes the next one', async () => {
    const prompts: any[] = [];
    let silentSocketClosed = false;
    const behavior: Behavior = (req, socket) => {
      if (typeof req.prompt === 'string' && req.prompt.length > 0) {
        prompts.push(req);
        if (prompts.length === 1) {
          // Accept the question and never answer it -- and never close.
          socket.once('close', () => (silentSocketClosed = true));
          return;
        }
        writeLine(socket, { protocol_version: 1, token: 'answered' });
        writeLine(socket, { protocol_version: 1, done: true });
      } else if (req.chats === true) {
        writeLine(socket, { protocol_version: 1, entries: [] });
      }
      socket.end();
    };
    await withPanel(behavior, {}, async (panel, posts, stub) => {
      streamSilenceLimits.idleMs = 600;
      const sentAt = Date.now();
      panel.handleMessage({ type: 'prompt', text: 'make hello.py', autoApply: false });
      await waitUntil(() => errorsOf(posts, NO_RESPONSE_CLASS).length === 1 && !panel.inFlight, 8000, 'the panel to give up', () => ({
        prompts: prompts.length,
        handshakes: stub.handshakes.length,
        inFlight: !!panel.inFlight,
        posts: posts.map((m) => `${m?.type}:${m?.errorClass ?? ''}`),
      }));
      const err = errorsOf(posts, NO_RESPONSE_CLASS)[0];

      // It reached the daemon, so this is the silence limit and not a failure
      // to connect -- which would also be an error, and arrive at once.
      assert.strictEqual(prompts.length, 1, 'the question never reached the stub');
      assert.ok(err.at - sentAt >= 550, `gave up after ${err.at - sentAt}ms, before the 600ms limit: not the limit`);
      assert.match(err.message, /No answer arrived/);
      assert.match(err.message, /stopped waiting/);
      assert.match(err.message, /Nothing is running now/);
      assert.match(err.message, /\b\d+ minutes? ago/, 'it does not say how long it waited');
      await waitUntil(() => silentSocketClosed, 2000, 'the silent turn to be hung up on');

      // The composer is free: the next question goes out and is answered.
      panel.handleMessage({ type: 'prompt', text: 'make hello.py', autoApply: false, retry: true });
      await waitUntil(() => prompts.length === 2 && !panel.inFlight, 5000, 'the retried turn');
      assert.strictEqual(panel.transcript[panel.transcript.length - 1].content, 'answered');
      assert.strictEqual(errorsOf(posts, NO_RESPONSE_CLASS).length, 1, 'a turn that answered was also given up on');
    });
  });

  test('the clock stops while an approval waits on the user, and runs longer while a tool runs', async () => {
    let approvalReply: any;
    const behavior: Behavior = (req, socket) => {
      if (typeof req.prompt === 'string' && req.prompt.length > 0) {
        writeLine(socket, {
          protocol_version: 1,
          tool_approval: { call_id: 'c1', server: 'builtin', tool: 'propose_edit', arguments: '{}', arguments_sha256: 'ab' },
        });
        let buf = '';
        socket.on('data', (chunk: Buffer) => {
          buf += chunk.toString('utf8');
          const nl = buf.indexOf('\n');
          if (nl < 0 || approvalReply) {
            return;
          }
          approvalReply = JSON.parse(buf.slice(0, nl));
          writeLine(socket, { protocol_version: 1, tool_activity: { call_id: 'c1', server: 'builtin', tool: 'propose_edit', phase: 'running' } });
          // Silent for longer than idleMs, shorter than toolRunningMs.
          setTimeout(() => {
            writeLine(socket, { protocol_version: 1, tool_activity: { call_id: 'c1', server: 'builtin', tool: 'propose_edit', phase: 'succeeded' } });
            writeLine(socket, { protocol_version: 1, token: 'done after the tool' });
            writeLine(socket, { protocol_version: 1, done: true });
            socket.end();
          }, 1500);
        });
        return;
      }
      if (req.chats === true) {
        writeLine(socket, { protocol_version: 1, entries: [] });
      }
      socket.end();
    };
    await withPanel(behavior, {}, async (panel, posts) => {
      streamSilenceLimits.idleMs = 500;
      streamSilenceLimits.toolRunningMs = 4000;
      panel.handleMessage({ type: 'prompt', text: 'edit it', autoApply: false });
      await waitUntil(() => posts.some((m) => m?.type === 'toolApproval'), 5000, 'the approval card');

      // The user takes three times the idle limit to decide.
      await sleep(1500);
      assert.strictEqual(errorsOf(posts, NO_RESPONSE_CLASS).length, 0, 'the turn was given up on while the user was deciding');
      assert.ok(panel.inFlight, 'the turn ended while waiting on the user');

      panel.handleMessage({ type: 'toolApprovalDecision', callId: 'c1', decision: 'approve' });
      await waitUntil(() => !panel.inFlight, 8000, 'the turn to finish');
      assert.strictEqual(approvalReply?.call_id, 'c1', 'the approval never reached the stub');
      assert.strictEqual(errorsOf(posts, NO_RESPONSE_CLASS).length, 0, 'a running tool was given up on at the idle limit');
      assert.strictEqual(panel.transcript[panel.transcript.length - 1].content, 'done after the tool');
    });
  });
});

suite('a question held back for a key', function () {
  this.timeout(30_000);

  const quietBehavior = (prompts: any[]): Behavior => (req, socket) => {
    if (typeof req.prompt === 'string' && req.prompt.length > 0) {
      prompts.push(req);
      writeLine(socket, { protocol_version: 1, token: 'sent after all' });
      writeLine(socket, { protocol_version: 1, done: true });
    } else if (req.chats === true) {
      writeLine(socket, { protocol_version: 1, entries: [] });
    }
    socket.end();
  };

  test('says at once that it was not sent, and is sent when a key connects', async () => {
    const prompts: any[] = [];
    await withPanel(quietBehavior(prompts), { needs_api_key: true }, async (panel, posts) => {
      const sentAt = Date.now();
      panel.handleMessage({ type: 'prompt', text: 'make hello.py', autoApply: false });
      await waitUntil(() => errorsOf(posts, NO_KEY_CLASS).length === 1, 2000, 'the not-sent notice');
      const err = errorsOf(posts, NO_KEY_CLASS)[0];
      assert.ok(err.at - sentAt < 1500, 'the notice waited for the connect box to close');
      assert.match(err.message, /Not sent yet/);
      assert.match(err.message, /Mochiii: Set API Key/);
      assert.strictEqual(prompts.length, 0, 'a keyless question was sent anyway');
      assert.ok(!panel.inFlight, 'a turn is marked live while nothing was sent');

      // The connect box is still open; a key connects.
      panel.finishConnect({ message: '', inUse: true });
      await waitUntil(() => prompts.length === 1 && !panel.inFlight, 5000, 'the held question to be sent');
      assert.strictEqual(prompts[0].prompt.includes('make hello.py'), true);
      const started = posts.findIndex((m) => m?.type === 'turnStarted');
      assert.ok(started > posts.indexOf(err), 'the panel was not put back into the answering state');
      assert.strictEqual(panel.transcript[panel.transcript.length - 1].content, 'sent after all');
    });
  });

  test('a box closed with no key says the question was not sent, and sends nothing', async () => {
    const prompts: any[] = [];
    await withPanel(quietBehavior(prompts), { needs_api_key: true }, async (panel, posts) => {
      panel.handleMessage({ type: 'prompt', text: 'make hello.py', autoApply: false });
      await waitUntil(() => errorsOf(posts, NO_KEY_CLASS).length === 1, 2000, 'the not-sent notice');
      panel.finishConnect({ message: 'Cancelled: no key was sent.', inUse: false });
      await waitUntil(() => posts.some((m) => m?.type === 'info' && /question was not sent/.test(m.text)), 2000, 'the closing note');
      await sleep(300);
      assert.strictEqual(prompts.length, 0);
      assert.ok(!posts.some((m) => m?.type === 'turnStarted'));
    });
  });
});

