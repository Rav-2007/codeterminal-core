// E2E-through-the-real-client tests for the M1/M2/M3 behaviors. These drive the
// ACTUAL compiled daemonClient.ts (../../daemonClient -- the same module the
// shipped ChatPanel imports) inside the real Extension Development Host process,
// over a real Unix-domain socket served by a controllable stub daemon. This is a
// genuine step past the M1-M3 report's hand-driven stubs: the client is the real
// compiled module running in VS Code's own Node runtime, dialing a real socket
// it located via the real lockfile path.
//
// Honest scope boundary: this validates the CLIENT/protocol half of M1/M2/M3
// (daemonClient decoding + handler dispatch + hang-vs-reject). The WEBVIEW-DOM
// render half (media/main.js turning these into a visible incomplete notice /
// truncation warning / thinking block) is exercised for-real by the smoke test
// (the panel + main.js render in a real webview), but VS Code exposes no webview
// DOM to the test host, so this suite cannot assert those pixels per-behavior.
// Stated so the coverage isn't over-claimed.

import * as assert from 'assert';

import { StreamHandlers, applyEdit, streamPrompt } from '../../daemonClient';
import { Behavior, StubDaemon, writeLine } from '../stubDaemon';

const CLIENT = 'codeterminal-vscode-test';
const PV = 1;

// streamToCompletion runs streamPrompt and resolves only once the stream has
// actually finished. streamPrompt is fire-and-forget -- its returned promise
// settles the instant the request is WRITTEN, then results arrive later via the
// handler callbacks (exactly how ChatPanel consumes it: it never awaits
// streamPrompt, it reacts to onDone/onError). A test that asserted right after
// `await streamPrompt(...)` would race those callbacks; this gates on the
// terminal handler instead. The caller's own onDone/onError still run first.
function streamToCompletion(prompt: string, handlers: StreamHandlers): Promise<void> {
  const ctrl = new AbortController();
  return new Promise<void>((resolve) => {
    void streamPrompt(CLIENT, prompt, '', [], ctrl.signal, {
      ...handlers,
      onDone: () => {
        handlers.onDone?.();
        resolve();
      },
      onError: (e) => {
        handlers.onError?.(e);
        resolve();
      },
    });
  });
}

async function withStub(behavior: Behavior, fn: () => Promise<void>): Promise<void> {
  const prev = process.env.XDG_RUNTIME_DIR;
  const stub = new StubDaemon();
  await stub.start(behavior);
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

suite('E2E daemonClient (M1/M2/M3 client half)', () => {
  // M1: a done message carrying `incomplete` must reach the client as a distinct
  // onIncomplete signal (a cut-off answer surfaced as a wire-visible incomplete
  // state), ahead of onDone.
  test('M1: incomplete finish_reason surfaces via onIncomplete before onDone', async () => {
    const behavior: Behavior = (_req, socket) => {
      writeLine(socket, { protocol_version: PV, token: 'partial ' });
      writeLine(socket, {
        protocol_version: PV,
        done: true,
        incomplete: { reason: 'length', detail: 'the answer was cut off at its length limit' },
      });
      socket.end(); // the real daemon closes the connection after the done message
    };
    await withStub(behavior, async () => {
      const order: string[] = [];
      let incomplete: { reason: string; detail: string } | undefined;
      await streamToCompletion('hi', {
        onToken: () => order.push('token'),
        onIncomplete: (info) => {
          order.push('incomplete');
          incomplete = info;
        },
        onDone: () => order.push('done'),
        onError: (e) => order.push('error:' + e.message),
      });
      assert.ok(incomplete, `onIncomplete was never called; order=${JSON.stringify(order)}`);
      assert.strictEqual(incomplete!.reason, 'length');
      assert.deepStrictEqual(order, ['token', 'incomplete', 'done'], `unexpected order: ${order.join(',')}`);
    });
  });

  // M2: the daemon cleanly closing an apply connection BEFORE replying must make
  // applyEdit REJECT, not hang forever (the wedge the real fix, b647490,
  // addressed: a hung applyEdit froze autoApplyRunInFlight and silently dropped
  // every future prompt).
  test('M2: clean close before reply rejects applyEdit (does not hang)', async () => {
    const behavior: Behavior = (_req, socket) => {
      // Simulate a graceful daemon shutdown mid-apply: FIN, no reply, no error.
      socket.end();
    };
    await withStub(behavior, async () => {
      const edit = { file_path: 'x.txt', search: 'a', replace: 'b' };
      let rejected = false;
      try {
        await withTimeout(applyEdit(CLIENT, '', edit), 4000);
      } catch (err) {
        rejected = true;
        assert.match((err as Error).message, /closed the connection before replying|timed out/);
      }
      assert.ok(rejected, 'applyEdit resolved/hung instead of rejecting on a clean close');
    });
  });

  // M3: a pre-token message carrying grounding + history + reasoning must fan out
  // to the matching handlers (the render-parity inputs the M3 fix wired up).
  test('M3: grounding/history/reasoning dispatch to their handlers', async () => {
    const behavior: Behavior = (_req, socket) => {
      writeLine(socket, {
        protocol_version: PV,
        grounding: { grounded: true, chunks: 3 },
        history: { turns: 5, truncated: true },
      });
      writeLine(socket, { protocol_version: PV, reasoning: 'thinking about it' });
      writeLine(socket, { protocol_version: PV, token: 'answer' });
      writeLine(socket, { protocol_version: PV, done: true });
      socket.end(); // the real daemon closes the connection after the done message
    };
    await withStub(behavior, async () => {
      const seen: Record<string, unknown> = {};
      await streamToCompletion('hi', {
        onGrounding: (g) => (seen.grounding = g),
        onHistory: (h) => (seen.history = h),
        onReasoning: (t) => (seen.reasoning = t),
        onToken: (t) => (seen.token = t),
        onDone: () => (seen.done = true),
        onError: (e) => (seen.error = e.message),
      });
      assert.deepStrictEqual(seen.grounding, { grounded: true, chunks: 3 });
      assert.deepStrictEqual(seen.history, { turns: 5, truncated: true });
      assert.strictEqual(seen.reasoning, 'thinking about it');
      assert.strictEqual(seen.token, 'answer');
      assert.strictEqual(seen.done, true);
      assert.ok(!seen.error, `unexpected error: ${seen.error}`);
    });
  });
});

function withTimeout<T>(p: Promise<T>, ms: number): Promise<T> {
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => reject(new Error('timed out')), ms);
    p.then(
      (v) => {
        clearTimeout(t);
        resolve(v);
      },
      (e) => {
        clearTimeout(t);
        reject(e);
      }
    );
  });
}
