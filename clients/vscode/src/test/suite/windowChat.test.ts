import * as assert from 'assert';
import * as vscode from 'vscode';

import { ChatPanel } from '../../chatPanel';
import { OPEN_CHAT_IN_NEW_WINDOW_COMMAND } from '../../chatWindow';
import { ChatDevTools } from '../devtools';

const EXT_ID = 'MochiiiAIAgent.mochiii-vscode';
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

// THE CHAT IN A WINDOW OF ITS OWN, used with a real mouse and keyboard
// (2026-11-24). After "Mochiii: Open Chat in New Window" the chat is in a
// second VS Code window, and from there it must do what the side bar's chat
// does: send what is typed, show an answer as it streams, answer a tool
// question and apply an edit -- through the same host. Closing that window
// closes the chat, and leaves the extension (and the daemon it supervises)
// running. The real daemon's side was checked by hand: see the PR.

suite('E2E chat — in a new window', () => {
  let dt: ChatDevTools | undefined;
  let panel: any;
  let realHandle: unknown;
  const got: { type?: string; [k: string]: unknown }[] = [];

  suiteTeardown(async () => {
    if (panel && realHandle) {
      panel.handleMessage = realHandle;
    }
    dt?.close();
  });

  test('the chat moves into a second window and works there', async function () {
    this.timeout(120000);
    const ext = vscode.extensions.getExtension(EXT_ID);
    assert.ok(ext);
    await ext.activate();
    await vscode.commands.executeCommand('notifications.clearAll').then(undefined, () => undefined);

    await vscode.commands.executeCommand(OPEN_CHAT_IN_NEW_WINDOW_COMMAND);
    panel = (ChatPanel as any).current;
    assert.ok(panel && panel.host.kind === 'panel', 'the command did not open the chat as an editor panel');

    // The chat is now in a window of its own: ChatDevTools finds it in a page
    // that is not the main workbench window.
    dt = await ChatDevTools.connect(60000);
    const where = await dt.pageEval<{ main: boolean; tabs: number }>(
      "({ main: !!document.querySelector('.monaco-workbench .part.activitybar'), tabs: document.querySelectorAll('.tab').length })",
    );
    assert.strictEqual(where.main, false, 'the chat is still in the main window');
    assert.strictEqual(where.tabs, 1, 'the new window holds something besides the chat');
    const deadline = Date.now() + 30000;
    while (!(await dt.chatEval<boolean>("!!d.getElementById('promptInput')"))) {
      if (Date.now() > deadline) throw new Error('the chat never loaded in the new window');
      await sleep(250);
    }

    // Record what the chat sends the host, and let the rest through.
    realHandle = panel.handleMessage;
    panel.handleMessage = function (msg: { type?: string }) {
      if (msg && ['prompt', 'toolApprovalDecision', 'applyEdit'].includes(msg.type ?? '')) {
        got.push(msg);
        return;
      }
      return (realHandle as (m: unknown) => unknown).call(panel, msg);
    };
    const post = (m: Record<string, unknown>) => panel.host.webview.postMessage(m);
    await post({ type: 'done' });
    await post({ type: 'clearTranscript' });
    // Manual, so an edit waits for Apply (in Auto one inside the project is saved by itself).
    await post({ type: 'setModeFromHost', mode: 'manual' });

    // Sends: typed into the composer in the new window, Enter.
    const c = await dt.chatEval<{ x: number; y: number }>(
      "(() => { const r = d.getElementById('promptInput').getBoundingClientRect(); return { x: r.left + 20, y: r.top + 12 }; })()",
    );
    await dt.click(c.x, c.y);
    await dt.type('hello from the new window');
    await dt.press('Enter');
    await sleep(500);
    assert.deepStrictEqual(
      got.filter((m) => m.type === 'prompt').map((m) => m.text),
      ['hello from the new window'],
      'Enter in the new window sent nothing',
    );

    // Streams: tokens posted by the host appear there as they arrive.
    await post({ type: 'token', text: 'Streamed into ' });
    await sleep(150);
    await post({ type: 'token', text: 'the new window.' });
    await sleep(300);
    assert.ok(
      await dt.chatEval<boolean>("Array.from(d.querySelectorAll('.msg.assistant')).some((e) => e.textContent.includes('Streamed into the new window.'))"),
      'the streamed answer is not in the new window',
    );

    // Approves: a tool question answered with Allow, in the new window.
    await post({
      type: 'toolApproval',
      request: { call_id: 'nw1', server: '', tool: 'run_command', arguments: '{"command":"npm test"}', arguments_sha256: 'x', lane: 'builtin', confined: false, iteration: 1 },
    });
    await sleep(400);
    const allow = await dt.chatEval<{ x: number; y: number } | null>(
      "(() => { const b = Array.from(d.querySelectorAll('.tool-approval button')).find((e) => e.textContent.trim() === 'Allow'); if (!b) return null; b.scrollIntoView({ block: 'center' }); const r = b.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()",
    );
    assert.ok(allow, 'no Allow on the tool question');
    await dt.click(allow.x, allow.y);
    await sleep(300);
    assert.deepStrictEqual(
      got.filter((m) => m.type === 'toolApprovalDecision').map((m) => [m.callId, m.decision]),
      [['nw1', 'approve']],
      'Allow in the new window did not answer the host',
    );

    // Applies an edit: Apply on an edit waiting for it.
    await post({ type: 'editProposal', edit: { file_path: 'src/app.js', search: 'a', replace: 'b' }, index: 0, total: 1 });
    await sleep(400);
    const apply = await dt.chatEval<{ x: number; y: number } | null>(
      "(() => { const b = Array.from(d.querySelectorAll('.edit-proposal button')).find((e) => e.textContent.trim() === 'Apply'); if (!b) return null; b.scrollIntoView({ block: 'center' }); const r = b.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()",
    );
    assert.ok(apply, 'no Apply on the edit');
    await dt.click(apply.x, apply.y);
    await sleep(300);
    assert.strictEqual(got.filter((m) => m.type === 'applyEdit').length, 1, 'Apply in the new window did not reach the host');
    await post({ type: 'done' });
    await post({ type: 'setModeFromHost', mode: 'auto' });

    // Closing the window closes the chat, and only the chat.
    panel.handleMessage = realHandle;
    await dt.pageEval('window.close()').catch(() => undefined);
    const closedBy = Date.now() + 15000;
    while ((ChatPanel as any).current === panel && Date.now() < closedBy) {
      await sleep(200);
    }
    assert.notStrictEqual((ChatPanel as any).current, panel, 'closing the window left the chat panel open');
    assert.strictEqual(ext.isActive, true, 'closing the chat window deactivated the extension, which stops its daemon');
    assert.ok((await vscode.commands.getCommands(true)).includes(OPEN_CHAT_IN_NEW_WINDOW_COMMAND));
  });
});
