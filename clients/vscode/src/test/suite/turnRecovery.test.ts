// A FAILED TURN OFFERS ITS NEXT STEP (Stage 8).
//
// Found in the first Stage 7 walkthrough: Groq rate-limited a turn and the
// panel showed "the model provider is rate-limiting requests right now -- wait
// a few seconds and try again" with nothing to click. The daemon has said what
// KIND of failure it was (protocol.TokenResponse.ErrorClass) since the error
// work, and the panel threw that away. These tests put a stub daemon behind a
// real panel and check that the class reaches the webview, and that "Try
// again" resends the question without the model reading it twice.

import * as assert from 'assert';
import * as vscode from 'vscode';

import { ChatPanel } from '../../chatPanel';
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

// ACTIVATE FIRST, THEN START THE STUB. Activation sets the workspace root and
// may start the real daemon -- with whatever XDG_RUNTIME_DIR is in force. Run
// after the stub had taken over, it pointed the panel at a real daemon living
// in the stub's runtime directory, and the prompt never reached the stub.
async function withPanel(behavior: Behavior, fn: (panel: any, posts: any[], stub: StubDaemon) => Promise<void>): Promise<void> {
  const ext = vscode.extensions.getExtension(EXT_ID);
  assert.ok(ext, `extension ${EXT_ID} is not in the dev host`);
  await ext.activate();
  const prev = process.env.XDG_RUNTIME_DIR;
  const stub = new StubDaemon();
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
      posts.push(msg);
      return original(msg);
    },
  });
  try {
    await waitUntil(() => posts.some((m) => m?.type === 'history' || m?.type === 'error'), 10_000, 'the preflight');
    await fn(panel, posts, stub);
  } finally {
    Object.defineProperty(webview, 'postMessage', { configurable: true, writable: true, value: original });
    // Close the TAB, not only the panel object: test files run in whatever
    // order glob returns them, and smoke.test.ts asserts no chat tab is open.
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

suite('a failed turn and its next step', function () {
  this.timeout(30_000);

  test('a rate limit reaches the webview as rate_limited, and Try again does not send the question twice', async () => {
    const prompts: any[] = [];
    const behavior: Behavior = (req, socket) => {
      if (typeof req.prompt === 'string' && req.prompt.length > 0) {
        prompts.push(req);
        if (prompts.length === 1) {
          writeLine(socket, {
            protocol_version: 1,
            done: true,
            error: 'the model provider is rate-limiting requests right now',
            error_class: 'rate_limited',
          });
        } else {
          writeLine(socket, { protocol_version: 1, token: 'here it is' });
          writeLine(socket, { protocol_version: 1, done: true });
        }
      } else if (req.chats === true) {
        writeLine(socket, { protocol_version: 1, entries: [] });
      }
      socket.end();
    };
    await withPanel(behavior, async (panel, posts, stub) => {
      panel.handleMessage({ type: 'prompt', text: 'list the folder', autoApply: false });
      await waitUntil(() => posts.some((m) => m?.type === 'error') && !panel.inFlight, 5000, 'the failed turn', () => ({
        prompts: prompts.length,
        handshakes: stub.handshakes.length,
        inFlight: !!panel.inFlight,
        hostKind: panel.host?.constructor?.name,
        posts: posts.map((m) => m?.type),
      }));
      const err = posts.find((m) => m?.type === 'error');
      assert.strictEqual(err.errorClass, 'rate_limited', 'the kind of failure did not reach the webview');
      assert.strictEqual(err.keyReplaceable, false);

      // What the webview's "Try again" sends.
      panel.handleMessage({ type: 'prompt', text: 'list the folder', autoApply: false, retry: true });
      await waitUntil(() => prompts.length === 2 && !panel.inFlight, 5000, 'the retried turn');

      const asked = (turns: any[]) => turns.filter((t) => t.role === 'user' && t.content === 'list the folder').length;
      assert.strictEqual(asked(prompts[1].history ?? []), 0, 'the retry sent the unanswered question in its history as well');
      assert.strictEqual(asked(panel.transcript), 1, 'the transcript holds the question twice after a retry');
      assert.strictEqual(panel.transcript[panel.transcript.length - 1].content, 'here it is');
    });
  });

  test('a prompt that is not a retry keeps an earlier unanswered question', async () => {
    const prompts: any[] = [];
    const behavior: Behavior = (req, socket) => {
      if (typeof req.prompt === 'string' && req.prompt.length > 0) {
        prompts.push(req);
        writeLine(socket, prompts.length === 1
          ? { protocol_version: 1, done: true, error: 'down', error_class: 'upstream_unavailable' }
          : { protocol_version: 1, done: true });
      } else if (req.chats === true) {
        writeLine(socket, { protocol_version: 1, entries: [] });
      }
      socket.end();
    };
    await withPanel(behavior, async (panel) => {
      panel.handleMessage({ type: 'prompt', text: 'same words', autoApply: false });
      await waitUntil(() => prompts.length === 1 && !panel.inFlight, 5000, 'the failed turn');
      panel.handleMessage({ type: 'prompt', text: 'same words', autoApply: false });
      await waitUntil(() => prompts.length === 2 && !panel.inFlight, 5000, 'the second turn');
      assert.strictEqual(
        panel.transcript.filter((t: any) => t.role === 'user' && t.content === 'same words').length,
        2,
        'only a retry may replace the earlier question; typing the same words again is a new message',
      );
    });
  });
});
