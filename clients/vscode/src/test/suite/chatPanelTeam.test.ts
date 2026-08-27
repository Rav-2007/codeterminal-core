import * as assert from 'assert';
import * as path from 'path';
import * as vscode from 'vscode';

import { ChatPanel } from '../../chatPanel';
import { setWorkspaceRoot } from '../../daemonClient';
import { Behavior, StubDaemon, writeLine } from '../stubDaemon';

const EXT_ID = 'codeterminal.codeterminal-vscode';
const PV = 1;

// CT_E2E_REAL_MODEL=1 opts into the one case that spends money. Default OFF, so
// `npm test` and CI stay hermetic, offline and free -- a billed test that runs
// by default is a bill nobody chose and a credential CI would have to hold.
const REAL_MODEL = process.env.CT_E2E_REAL_MODEL === '1';

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

/**
 * drivePanelPrompt runs one prompt through the REAL ChatPanel, from the same
 * entry point the webview uses.
 *
 * THIS IS THE GLUE THAT WAS ONLY TYPECHECKED. handleMessage → onPrompt →
 * parseTeamCommand → startModelTurn → streamPrompt is four hops of ordinary
 * plumbing, and ordinary plumbing is exactly where a pipeline gets dropped: the
 * compiler is happy either way. Posting the message the webview posts executes
 * all four.
 *
 * Every message the panel sends BACK is captured, which is the only way to
 * observe a turn from outside without a browser. postMessage is restored and
 * the panel disposed in a finally, so no patched object, no webview and no
 * in-flight socket outlives the test (ChatPanel.dispose aborts the connection
 * and clears the static).
 */
async function drivePanelPrompt(
  text: string,
  until: (posts: any[]) => boolean,
  timeoutMs: number,
  what: string
): Promise<any[]> {
  const ext = vscode.extensions.getExtension(EXT_ID);
  assert.ok(ext, `extension ${EXT_ID} is not in the dev host`);
  await ext.activate();

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
      // A tool approval is a question the daemon HOLDS a call open for. Answer
      // it immediately, in the safe direction, so an unattended run can never
      // park the daemon on a five-minute human deadline.
      if (msg?.type === 'toolApproval') {
        panel.handleMessage({
          type: 'toolApprovalDecision',
          decision: 'deny',
          callId: msg.request?.call_id,
        });
      }
      return original(msg);
    },
  });

  try {
    // The panel hydrates cross-session memory at startup. Wait for that to land
    // and then drop it: this driver asserts about THIS turn, and carrying a
    // previous session's transcript into the request would make the assertion
    // depend on state the test does not control -- and, on the billed path, pay
    // for every token of it.
    await waitUntil(
      () => posts.some((m) => m?.type === 'history' || m?.type === 'error'),
      10000,
      'the preflight to settle'
    ).catch(() => undefined);
    panel.transcript = [];

    panel.handleMessage({ type: 'prompt', text, autoApply: false });
    await waitUntil(() => until(posts), timeoutMs, what);
  } finally {
    Object.defineProperty(webview, 'postMessage', {
      configurable: true,
      writable: true,
      value: original,
    });
    panel.dispose();
  }
  return posts;
}

suite('E2E ChatPanel — the /team glue, executed', () => {
  // The hermetic half: a real panel, a real socket, a stub at the far end.
  // Proves the shape survives every hop from the typed line to the wire.
  test('a /team prompt typed into the panel reaches the daemon as a pipeline', async () => {
    const stub = new StubDaemon();
    const prevRuntime = process.env.XDG_RUNTIME_DIR;
    let seen: unknown;
    let sawPrompt = false;

    const behavior: Behavior = (req, socket) => {
      if (typeof req.prompt === 'string' && req.prompt.length > 0) {
        seen = req.pipeline;
        sawPrompt = true;
      }
      writeLine(socket, { protocol_version: PV, token: 'ok' });
      writeLine(socket, { protocol_version: PV, done: true });
      socket.end();
    };

    await stub.start(behavior);
    try {
      await drivePanelPrompt(
        '/team why is this slow',
        () => sawPrompt,
        15000,
        'the panel to send a prompt'
      );
    } finally {
      await stub.stop();
      if (prevRuntime === undefined) {
        delete process.env.XDG_RUNTIME_DIR;
      } else {
        process.env.XDG_RUNTIME_DIR = prevRuntime;
      }
    }

    assert.deepStrictEqual(
      seen,
      ['researcher', 'coder'],
      'the panel sent no pipeline (or the wrong one), so /team is an ordinary turn in this client'
    );
  });

  // The billed half, opt-in. Everything above proves the SHAPE travels; only a
  // real provider proves the daemon then runs a real specialist pipeline for a
  // prompt typed into this client — two phases, in order, from a live model.
  test('a real /team turn runs its phases in order', async function () {
    if (!REAL_MODEL) {
      this.skip();
      return;
    }
    this.timeout(240000);

    const ext = vscode.extensions.getExtension(EXT_ID);
    assert.ok(ext, 'extension missing');
    // The live daemon is keyed on the repository root, two levels above the
    // extension. Derived rather than hardcoded so this runs on any checkout.
    const repoRoot = process.env.CT_E2E_REAL_WORKSPACE ?? path.resolve(ext.extensionUri.fsPath, '..', '..');
    setWorkspaceRoot(repoRoot);

    const phases: string[] = [];
    const posts = await drivePanelPrompt(
      '/team in one sentence, what does run-tui.sh --stop do?',
      (p) => p.some((m) => m?.type === 'done' || m?.type === 'error'),
      210000,
      'the real turn to finish'
    );

    for (const m of posts) {
      if (m?.type === 'toolActivity' && m.activity?.phase === 'step' && m.activity?.tool) {
        phases.push(m.activity.tool);
      }
    }

    const failed = posts.find((m) => m?.type === 'error');
    assert.ok(!failed, `the turn errored: ${failed?.message}`);

    // Two distinct specialists, narrated in order, is the pipeline running.
    assert.ok(
      phases.length >= 2,
      `saw ${phases.length} phase marker(s) (${phases.join(' → ')}); a real pipeline narrates one per specialist`
    );
    assert.notStrictEqual(phases[0], phases[1], `both phases were ${phases[0]}`);

    // The answer is not asserted on -- a model's wording is not a contract, and
    // echoing it into a log is how transcripts end up somewhere they were never
    // meant to be. Length is enough to know prose arrived.
    const answered = posts.filter((m) => m?.type === 'token').map((m) => String(m.text ?? '')).join('');
    assert.ok(answered.trim().length > 0, 'the pipeline finished without producing an answer');
    console.log(`      [real] phases: ${phases.join(' → ')}; answer ${answered.trim().length} chars`);
  });
});
