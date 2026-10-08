// THE SHARED CHAT, FROM THIS SIDE (daemon/chatsync.go).
//
// FOUND 2026-10-08: with the terminal client and this panel open on one
// workspace, a question asked in the terminal never reached the panel -- not
// its screen, and not the history it sent next -- because the chat is ONE
// conversation per workspace and the panel read it once, when it opened. These
// tests put a stub daemon at the far end of the real panel and check what the
// panel shows and sends.

import * as assert from 'assert';
import * as vscode from 'vscode';

import { ChatPanel } from '../../chatPanel';
import { chatHistory, resetChat, streamPrompt } from '../../daemonClient';
import { Behavior, StubDaemon, writeLine } from '../stubDaemon';

const EXT_ID = 'mochiii.mochiii-vscode';

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

// What a failed wait reports about the panel: enough to tell "never asked"
// from "asked and dropped the answer".
const panelState = (panel: any, s: SharedChatStub, posts: any[]) => () => ({
  chatSync: panel.chatSync,
  chatRev: panel.chatRev,
  catchUpInFlight: panel.catchUpInFlight,
  inFlight: !!panel.inFlight,
  catchUps: s.catchUps.map((r) => r.since),
  posts: posts.map((m) => m?.type),
});

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

suite('the shared chat on the wire', function () {
  this.timeout(10_000);

  test('catching up asks for the chat since the revision on screen', async () => {
    let seen: any;
    const stub = new StubDaemon();
    await stub.start((req, socket) => {
      seen = req;
      writeLine(socket, { protocol_version: 1, chat_revision: 'r2', append: true, turns: [{ role: 'user', content: 'q' }] });
    });
    await withStub(stub, async () => {
      const r = await chatHistory('test', 'current', { since: 'r1' });
      assert.strictEqual(r.chat_revision, 'r2');
      assert.strictEqual(r.append, true);
    });
    assert.deepStrictEqual(seen, { protocol_version: 1, chats: true, action: 'current', since: 'r1' });
  });

  test('"+" learns the new chat\'s revision', async () => {
    const stub = new StubDaemon();
    await stub.start((_req, socket) => writeLine(socket, { protocol_version: 1, done: true, chat_revision: 'r5' }));
    await withStub(stub, async () => {
      assert.strictEqual(await resetChat('test'), 'r5');
    });
  });

  test('a prompt carries its revision, and the Done says where the chat stands before onDone', async () => {
    let seen: any;
    const order: string[] = [];
    const stub = new StubDaemon();
    await stub.start((req, socket) => {
      seen = req;
      writeLine(socket, { protocol_version: 1, token: 'ok' });
      writeLine(socket, { protocol_version: 1, done: true, chat_revision: 'r3', chat_behind: true });
      socket.end();
    });
    await withStub(stub, async () => {
      await new Promise<void>((resolve, reject) => {
        void streamPrompt(
          'test',
          'hi',
          '',
          [],
          new AbortController().signal,
          {
            onChatRevision: (rev, behind) => order.push(`rev ${rev} behind ${behind}`),
            onDone: () => {
              order.push('done');
              resolve();
            },
            onError: reject,
          },
          { chatRevision: 'r1' },
        );
      });
    });
    assert.strictEqual(seen.chat_revision, 'r1');
    assert.deepStrictEqual(order, ['rev r3 behind true', 'done']);
  });
});

// A real panel against the stub: it opened at revision r1 with one exchange,
// and the terminal has since asked one question.
interface SharedChatStub {
  stub: StubDaemon;
  catchUps: any[];
  prompts: any[];
}

const TERMINAL_ADDED = [
  { role: 'user', content: 'asked in the terminal' },
  { role: 'assistant', content: 'answered there' },
];

async function startSharedChatStub(opts: { revision?: string; done?: Record<string, unknown> }): Promise<SharedChatStub> {
  const s: SharedChatStub = { stub: new StubDaemon(), catchUps: [], prompts: [] };
  s.stub.handshakeExtra = {
    persisted_history: [
      { role: 'user', content: 'asked in the panel' },
      { role: 'assistant', content: 'answered in the panel' },
    ],
    ...(opts.revision ? { chat_revision: opts.revision } : {}),
  };
  const behavior: Behavior = (req, socket) => {
    if (req.chats === true) {
      if (req.action === 'current') {
        // As the daemon answers: from r1 the terminal's exchange; from no
        // revision the whole chat; from anything later, nothing new. Focus
        // events fire on their own in a test host as in a real one, so a
        // panel is asked more than once and must hear "nothing new" then.
        s.catchUps.push(req);
        let reply: Record<string, unknown>;
        if (req.since === 'r1') {
          reply = { protocol_version: 1, chat_revision: 'r2', append: true, turns: TERMINAL_ADDED };
        } else if (!req.since) {
          reply = {
            protocol_version: 1,
            chat_revision: 'r9',
            turns: [
              { role: 'user', content: 'the whole chat' },
              { role: 'assistant', content: 'as stored' },
            ],
          };
        } else {
          reply = { protocol_version: 1, chat_revision: req.since, append: true };
        }
        writeLine(socket, reply);
      } else {
        writeLine(socket, { protocol_version: 1, entries: [] });
      }
      socket.end();
      return;
    }
    if (typeof req.prompt === 'string' && req.prompt.length > 0) {
      s.prompts.push(req);
      writeLine(socket, { protocol_version: 1, token: 'ok' });
      writeLine(socket, { protocol_version: 1, done: true, ...(opts.done ?? {}) });
    }
    socket.end();
  };
  await s.stub.start(behavior);
  return s;
}

async function withPanel(fn: (panel: any, posts: any[]) => Promise<void>): Promise<void> {
  const ext = vscode.extensions.getExtension(EXT_ID);
  assert.ok(ext, `extension ${EXT_ID} is not in the dev host`);
  await ext.activate();
  (ChatPanel as any).current?.dispose();
  ChatPanel.createOrShow(ext.extensionUri);
  const panel: any = (ChatPanel as any).current;
  assert.ok(panel, 'createOrShow left no panel');
  const posts: any[] = [];
  const webview = panel.panel.webview;
  const original = webview.postMessage.bind(webview);
  Object.defineProperty(webview, 'postMessage', {
    configurable: true,
    writable: true,
    value: (msg: any) => {
      posts.push(msg);
      return original(msg);
    },
  });
  try {
    await waitUntil(() => posts.some((m) => m?.type === 'history' || m?.type === 'error'), 10_000, 'the preflight');
    await fn(panel, posts);
  } finally {
    Object.defineProperty(webview, 'postMessage', { configurable: true, writable: true, value: original });
    panel.dispose();
  }
}

const texts = (turns: any[]): string => turns.map((t) => t.content).join(' | ');

suite('the shared chat in the panel', function () {
  this.timeout(30_000);

  // Neuter check: delete the 'focused' branch from handleMessage -- nothing is
  // fetched and the wait below times out (verified with this file run alone,
  // where this is the first panel; later panels also get the host's own focus
  // and view-state events, which reach catchUp by the other two routes).
  test('focusing the panel shows what the terminal added, below what was here', async () => {
    const s = await startSharedChatStub({ revision: 'r1' });
    await withStub(s.stub, () =>
      withPanel(async (panel, posts) => {
        panel.handleMessage({ type: 'focused' });
        await waitUntil(() => panel.chatRev === 'r2', 5000, 'the panel to catch up', panelState(panel, s, posts));
        assert.strictEqual(
          texts(panel.transcript),
          'asked in the panel | answered in the panel | asked in the terminal | answered there',
        );
        assert.ok(
          posts.some((m) => m?.type === 'history' && texts(m.turns) === texts(TERMINAL_ADDED)),
          'the terminal\'s exchange was not drawn',
        );
        assert.ok(
          posts.some((m) => m?.type === 'info' && /2 messages from another Mochiii window/.test(m.text)),
          'nothing said where the new messages came from',
        );
        assert.ok(!posts.some((m) => m?.type === 'clearTranscript'), 'catching up redrew the whole chat instead of appending');
        assert.strictEqual(s.catchUps[0]?.since, 'r1');
      }),
    );
  });

  test('a question after catching up carries the terminal\'s turns and the revision they are', async () => {
    const s = await startSharedChatStub({ revision: 'r1', done: { chat_revision: 'r3', chat_behind: false } });
    await withStub(s.stub, () =>
      withPanel(async (panel) => {
        panel.handleMessage({ type: 'focused' });
        await waitUntil(() => panel.chatRev === 'r2', 5000, 'the panel to catch up');
        panel.handleMessage({ type: 'prompt', text: 'and now?', autoApply: false });
        await waitUntil(() => s.prompts.length === 1 && !panel.inFlight, 5000, 'the turn to finish');
        assert.strictEqual(texts(s.prompts[0].history), 'asked in the panel | answered in the panel | asked in the terminal | answered there');
        assert.strictEqual(s.prompts[0].chat_revision, 'r2');
        assert.strictEqual(panel.chatRev, 'r3', 'a level turn did not move the panel to the new revision');
      }),
    );
  });

  // Neuter check: make onChatRevision always take the revision -- the panel
  // never fetches the chat and the wait times out.
  test('a turn the terminal wrote during is followed by the whole chat', async () => {
    const s = await startSharedChatStub({ revision: 'r1', done: { chat_revision: 'r4', chat_behind: true } });
    await withStub(s.stub, () =>
      withPanel(async (panel, posts) => {
        panel.handleMessage({ type: 'prompt', text: 'mine', autoApply: false });
        await waitUntil(() => panel.chatRev === 'r9', 5000, 'the whole chat to be fetched');
        assert.strictEqual(texts(panel.transcript), 'the whole chat | as stored');
        assert.strictEqual(s.catchUps.at(-1)?.since, '', 'the fetch after a turn that was not level asked for less than the whole chat');
        assert.ok(posts.some((m) => m?.type === 'clearTranscript'), 'the whole chat was not redrawn');
      }),
    );
  });

  test('a daemon that gives no revision is never asked, and prompts go out as before', async () => {
    const s = await startSharedChatStub({});
    await withStub(s.stub, () =>
      withPanel(async (panel) => {
        panel.handleMessage({ type: 'focused' });
        panel.handleMessage({ type: 'prompt', text: 'plain', autoApply: false });
        await waitUntil(() => s.prompts.length === 1 && !panel.inFlight, 5000, 'the turn to finish');
        await new Promise((r) => setTimeout(r, 200));
        assert.deepStrictEqual(s.catchUps, []);
        assert.strictEqual(s.prompts[0].chat_revision, undefined);
      }),
    );
  });
});
