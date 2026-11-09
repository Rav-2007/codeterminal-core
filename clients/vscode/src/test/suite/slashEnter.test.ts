import * as assert from 'assert';
import * as vscode from 'vscode';

import { ChatPanel } from '../../chatPanel';
import { ChatDevTools } from '../devtools';

const EXT_ID = 'MochiiiAIAgent.mochiii-vscode';

// ONE ENTER RUNS A COMPLETE COMMAND.
//
// FOUND 2026-11-08 (UI audit): with the slash menu open, Enter always took the
// highlighted row -- so "/mcp" + Enter rewrote the box to "/mcp" and nothing
// visible happened; a second Enter ran it. Every bare command in the menu
// needed Enter twice. The menu now takes Enter only to finish a partial name,
// or to take a row the user arrowed to.
//
// Real key presses into the composer (DevTools). The host's message handler is
// replaced by a recorder for the suite, so a command is seen being SENT and is
// not run: /clear, /init or /exit must not act on the test host.
//
// NEUTER CHECK (2026-11-09): with media/main.js as on main, "every command in
// the menu" fails for 39 of 39 commands (the box still holds each after one
// Enter); the partial-name and arrowed-row tests pass either way, as they
// should.

const SCREENS: Record<string, string> = {
  mcp: "!d.getElementById('mcpDlg').hidden",
  'mcp-server': "!d.getElementById('mcpDlg').hidden",
  account: "Array.from(d.querySelectorAll('.page')).some(p => !p.hidden)",
  config: "Array.from(d.querySelectorAll('.page')).some(p => !p.hidden)",
  model: "!d.getElementById('modelPopup').hidden",
};

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

suite('E2E chat — one Enter runs a complete slash command', () => {
  let dt: ChatDevTools;
  let post: (msg: Record<string, unknown>) => Thenable<boolean>;
  let panel: any;
  let realHandle: unknown;
  const sent: { type?: string; text?: string }[] = [];

  suiteSetup(async function () {
    this.timeout(60000);
    const ext = vscode.extensions.getExtension(EXT_ID);
    assert.ok(ext, `extension ${EXT_ID} is not in the dev host`);
    await ext.activate();
    for (const cmd of ['workbench.action.closeSidebar', 'workbench.action.closeAuxiliaryBar', 'workbench.action.closePanel', 'notifications.clearAll']) {
      await vscode.commands.executeCommand(cmd).then(undefined, () => undefined);
    }
    ChatPanel.createOrShow(ext.extensionUri);
    panel = (ChatPanel as any).current;
    assert.ok(panel, 'createOrShow left no panel');
    post = (msg) => panel.host.webview.postMessage(msg);
    dt = await ChatDevTools.connect();
    const deadline = Date.now() + 30000;
    while (!(await dt.chatEval<boolean>("!!d.getElementById('promptInput')"))) {
      if (Date.now() > deadline) {
        throw new Error('the chat never loaded');
      }
      await sleep(250);
    }
    // Record, do not run. Messages the page needs to start (ready, catalog
    // requests) still reach the real handler; a typed prompt does not.
    realHandle = panel.handleMessage;
    panel.handleMessage = function (msg: { type?: string; text?: string }) {
      if (msg && msg.type === 'prompt') {
        sent.push(msg);
        return;
      }
      return (realHandle as (m: unknown) => unknown).call(panel, msg);
    };
  });

  suiteTeardown(async () => {
    if (panel && realHandle) {
      panel.handleMessage = realHandle;
    }
    if (dt) {
      dt.close();
    }
  });

  async function waitFor(what: string, cond: () => Promise<boolean>, ms = 5000): Promise<void> {
    const deadline = Date.now() + ms;
    while (!(await cond())) {
      if (Date.now() > deadline) {
        const why = await dt.chatEval(`({ composer: d.getElementById('promptInput').value, menu: d.getElementById('slashMenu') && d.getElementById('slashMenu').className,
          active: d.activeElement && (d.activeElement.id || d.activeElement.tagName) })`);
        assert.fail(`${what}: chat ${JSON.stringify(why)}`);
      }
      await sleep(80);
    }
  }

  const composer = () => dt.chatEval<string>("d.getElementById('promptInput').value");
  const menuOpen = () => dt.chatEval<boolean>("d.getElementById('slashMenu').classList.contains('visible')");

  /** Nothing open, the composer empty and holding the keyboard, no turn running. */
  async function reset(): Promise<void> {
    await post({ type: 'done' });
    await dt.chatEval(`(() => {
      const x = d.getElementById('mcpDlgClose'); if (x && x.getClientRects().length) x.click();
      for (const b of Array.from(d.querySelectorAll('[data-close-page]'))) if (b.getClientRects().length) { b.click(); break; }
      const m = d.getElementById('modelPopup'); if (m && !m.hidden) d.getElementById('modelChip').click();
      const c = d.getElementById('promptInput'); c.value = ''; c.dispatchEvent(new Event('input')); c.focus();
      return true; })()`);
    await sleep(150);
  }

  /** Types text into the composer and waits for the slash menu to show for it. */
  async function typeWithMenu(text: string): Promise<void> {
    await dt.type(text);
    await waitFor(`${text} was not typed`, async () => (await composer()) === text);
    await waitFor(`the slash menu did not open for ${text}`, menuOpen);
  }

  test('every command in the menu runs on the first Enter', async function () {
    this.timeout(240000);
    await reset();
    await typeWithMenu('/');
    const names = await dt.chatEval<string[]>(
      "Array.from(d.querySelectorAll('#slashMenu .slash-name')).map(e => e.textContent.replace(/^\\//, ''))",
    );
    assert.ok(names.length >= 30, `the menu lists only ${names.length} commands`);
    const failed: string[] = [];
    for (const name of names) {
      await reset();
      sent.length = 0;
      await typeWithMenu('/' + name);
      await dt.press('Enter');
      await sleep(250);
      const left = await composer();
      const ran =
        sent.some((m) => m.text === '/' + name) || (SCREENS[name] !== undefined && (await dt.chatEval<boolean>(SCREENS[name])));
      if (left !== '' || !ran) {
        failed.push(`/${name}: box ${JSON.stringify(left)}, ${ran ? 'ran' : 'did not run'}`);
      }
    }
    await reset();
    assert.deepStrictEqual(failed, [], `needed more than one Enter (${failed.length} of ${names.length})`);
  });

  test('a partial name is still completed by Enter, not sent', async function () {
    this.timeout(30000);
    await reset();
    sent.length = 0;
    await typeWithMenu('/acco');
    await dt.press('Enter');
    await sleep(250);
    assert.strictEqual(await composer(), '/account', 'Enter did not complete the partial name');
    assert.deepStrictEqual(sent, [], 'a partial name was sent');
    await reset();
  });

  test('a row the user arrowed to is taken by Enter, even when the typed name is complete', async function () {
    this.timeout(30000);
    await reset();
    sent.length = 0;
    await typeWithMenu('/mcp');
    const active = () => dt.chatEval<string>("(d.querySelector('#slashMenu button.active .slash-name') || {}).textContent || ''");
    await dt.press('ArrowDown');
    for (let i = 0; i < 3 && ((await active()) === '' || (await active()) === '/mcp'); i++) {
      await dt.press('ArrowDown');
    }
    const chosen = await active();
    assert.ok(chosen !== '' && chosen !== '/mcp', `no other row could be chosen: ${chosen}`);
    await dt.press('Enter');
    await sleep(250);
    assert.deepStrictEqual(sent, [], 'Enter sent the typed name instead of taking the chosen row');
    assert.ok(!(await dt.chatEval<boolean>(SCREENS.mcp)), 'Enter ran /mcp instead of taking the chosen row');
    assert.ok((await composer()).startsWith(chosen), `the box holds ${JSON.stringify(await composer())}, not the chosen ${chosen}`);
    await reset();
  });
});
