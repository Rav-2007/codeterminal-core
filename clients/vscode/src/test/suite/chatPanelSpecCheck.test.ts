import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';
import * as vscode from 'vscode';

import { ChatPanel } from '../../chatPanel';
import { Behavior, StubDaemon, writeLine } from '../stubDaemon';

const EXT_ID = 'MochiiiAIAgent.mochiii-vscode';
const PV = 1;

// /spec check ON A SPEC WITH NO "- [ ] C1:" LINES LOCKED THE PANEL.
//
// The daemon's check report then carries `"criteria": null` (protocol.SpecReport
// has no omitempty; daemon/spec.go builds it without a slice). specReportText
// called .filter on it inside the stream handler, the TypeError ended the
// handler before `done` was posted, and the panel stayed in flight: every later
// prompt returned at onPrompt's in-flight guard, silently, until the window was
// reloaded. This drives the real panel against a stub daemon that sends exactly
// that report. NEUTER CHECK: remove the `?? []` in specReportText and the first
// wait times out.

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

suite('E2E ChatPanel — /spec check with no criteria', () => {
  test('a check report whose criteria are null ends the turn and leaves the panel usable', async function () {
    this.timeout(40000);
    const ws = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
    if (!ws) {
      this.skip();
      return;
    }
    const rel = 'specs/nocrit-regression.md';
    fs.mkdirSync(path.join(ws, 'specs'), { recursive: true });
    fs.writeFileSync(path.join(ws, ...rel.split('/')), '# No criteria\n\nAdd a verbose flag.\n');

    // Activate FIRST: activation calls setWorkspaceRoot(<the window's folder>),
    // which would replace the root StubDaemon.start points the client at.
    const ext = vscode.extensions.getExtension(EXT_ID);
    assert.ok(ext, `extension ${EXT_ID} is not in the dev host`);
    await ext.activate();

    const stub = new StubDaemon();
    const prevRuntime = process.env.XDG_RUNTIME_DIR;
    const prompts: string[] = [];
    const behavior: Behavior = (req, socket) => {
      if (typeof req.prompt === 'string' && req.prompt.length > 0) {
        prompts.push(req.prompt);
      }
      writeLine(socket, { protocol_version: PV, token: 'checked.' });
      writeLine(socket, { protocol_version: PV, done: true, spec_report: { spec: rel, criteria: null } });
      socket.end();
    };
    await stub.start(behavior);

    ChatPanel.createOrShow(ext.extensionUri);
    const panel: any = (ChatPanel as any).current;
    assert.ok(panel, 'createOrShow left no panel');
    const webview = panel.host.webview;
    const original = webview.postMessage.bind(webview);
    let posts: any[] = [];
    Object.defineProperty(webview, 'postMessage', {
      configurable: true,
      writable: true,
      value: (msg: any) => {
        posts.push(msg);
        return original(msg);
      },
    });

    try {
      await waitUntil(() => posts.some((m) => m?.type === 'history' || m?.type === 'error'), 10000, 'the preflight').catch(
        () => undefined,
      );
      panel.transcript = [];
      posts = [];

      // Made active the way a user does it, through the panel.
      panel.handleMessage({ type: 'prompt', text: '/spec use nocrit-regression', autoApply: false });
      await waitUntil(() => posts.some((m) => m?.type === 'done'), 5000, '/spec use to answer');
      assert.ok(
        posts.some((m) => m?.type === 'activeSpec' && m.spec === rel),
        `/spec use did not make ${rel} active: ${JSON.stringify(posts).slice(0, 600)}`,
      );
      posts = [];

      panel.handleMessage({ type: 'prompt', text: '/spec check', autoApply: false });
      await waitUntil(() => posts.some((m) => m?.type === 'done'), 15000, 'the /spec check turn to post done').catch((e) => {
        throw new Error(`${e.message}; panel said ${JSON.stringify(posts).slice(0, 1500)}; daemon saw ${JSON.stringify(prompts)}`);
      });
      assert.ok(
        prompts.some((p) => p.includes(rel)),
        `the check turn never reached the daemon: ${JSON.stringify(prompts)}; panel said ${JSON.stringify(posts).slice(0, 800)}`,
      );
      const report = posts.find((m) => m?.type === 'specNotice' && m.kind === 'spec-report');
      assert.ok(report, 'no spec-report notice was posted');
      assert.ok(String(report.text).includes('0 of 0 criteria met'), String(report.text));

      // And the panel still takes a prompt: before the fix this returned at the
      // in-flight guard and posted nothing at all.
      posts = [];
      panel.handleMessage({ type: 'prompt', text: '/context', autoApply: false });
      await waitUntil(
        () => posts.some((m) => m?.type === 'token' && String(m.text).includes('workspace:')),
        5000,
        'the panel to answer /context after the check',
      );
    } finally {
      // Clear the spec through the panel and let it answer BEFORE disposing:
      // the reply is posted asynchronously, and posting to a disposed webview
      // is an unhandled rejection.
      posts = [];
      panel.handleMessage({ type: 'prompt', text: '/spec off', autoApply: false });
      await waitUntil(() => posts.some((m) => m?.type === 'done'), 5000, '/spec off to answer').catch(() => undefined);
      Object.defineProperty(webview, 'postMessage', { configurable: true, writable: true, value: original });
      panel.dispose();
      fs.rmSync(path.join(ws, ...rel.split('/')), { force: true });
      await stub.stop();
      if (prevRuntime === undefined) {
        delete process.env.XDG_RUNTIME_DIR;
      } else {
        process.env.XDG_RUNTIME_DIR = prevRuntime;
      }
    }
  });
});
