import * as assert from 'assert';
import * as vscode from 'vscode';

import { ChatPanel } from '../../chatPanel';
import { PAGE_COMMANDS } from '../../chatPages/rules';
import { ChatDevTools } from '../devtools';

const EXT_ID = 'MochiiiAIAgent.mochiii-vscode';

// "/mcp-server add" OPENS THE ADD POPUP; every other /mcp-server stays as it was.
//
// FOUND 2026-11-10: the chat took everything after /mcp-server as a marketplace
// search, so "/mcp-server add files npx ..." opened the Marketplace with that text
// in its search box and added nothing -- while the terminal app's command adds. The
// webview now answers a first word of exactly "add" by asking the host for the same
// popup the "Add a server by hand" button opens (page:command mcpAdd), which does
// the daemon's two steps: what will run, then the server's name typed.
//
// Real key presses into the composer (DevTools). The host's message handler is a
// recorder for the suite: a prompt or a page command is SEEN being sent and is not
// run, so nothing starts a server, reaches the daemon or opens a dialog on the test
// host. Everything else the page needs (ready, page loads) still reaches the real
// handler.
//
// DUMMY STRINGS ONLY. The text after "add" below is made up; the point of the
// secret check is that whatever follows "add" -- possibly a mistyped key -- is not
// kept anywhere this change could keep it.
//
// NEUTER CHECK: with media/main.js as on main, the "add" tests fail (the Customize
// page opens and page:load is sent); the search and bare-command tests pass either
// way, as they should.

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const DUMMY_KEY_LIKE = 'sk-dummy-not-a-key-0000';

suite('E2E chat — /mcp-server add opens the add popup', () => {
  let dt: ChatDevTools;
  let panel: any;
  let realHandle: unknown;
  let post: (msg: Record<string, unknown>) => Thenable<boolean>;
  const seen: Record<string, unknown>[] = [];

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
    realHandle = panel.handleMessage;
    panel.handleMessage = function (msg: Record<string, unknown>) {
      if (msg && msg.type !== 'ready') {
        seen.push(msg);
      }
      // A prompt, a request for the add popup, and the marketplace's own traffic
      // (whose first reply is a modal notice) are recorded and not run.
      if (
        msg &&
        (msg.type === 'prompt' ||
          (msg.type === 'page:command' && msg.name === 'mcpAdd') ||
          (typeof msg.type === 'string' && msg.type.startsWith('mk:')))
      ) {
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

  const composer = () => dt.chatEval<string>("d.getElementById('promptInput').value");
  const pageOpen = () => dt.chatEval<boolean>("Array.from(d.querySelectorAll('.page')).some(p => !p.hidden)");
  const dialogOpen = () => dt.chatEval<boolean>("!d.getElementById('mcpDlg').hidden");

  async function reset(): Promise<void> {
    await post({ type: 'done' });
    await dt.chatEval(`(() => {
      const x = d.getElementById('mcpDlgClose'); if (x && x.getClientRects().length) x.click();
      for (const b of Array.from(d.querySelectorAll('[data-close-page]'))) if (b.getClientRects().length) { b.click(); break; }
      const c = d.getElementById('promptInput'); c.value = ''; c.dispatchEvent(new Event('input')); c.focus();
      return true; })()`);
    await sleep(200);
    seen.length = 0;
  }

  async function run(text: string): Promise<void> {
    await reset();
    await dt.type(text);
    const deadline = Date.now() + 5000;
    while ((await composer()) !== text && Date.now() < deadline) {
      await sleep(80);
    }
    assert.strictEqual(await composer(), text, `${text} was not typed`);
    await dt.press('Enter');
    await sleep(600);
  }

  const types = () => seen.map((m) => (m.type === 'page:command' ? `page:command/${m.name}` : String(m.type)));

  test('the popup is reached through a command the host already runs for the button', () => {
    assert.strictEqual(PAGE_COMMANDS.mcpAdd, 'mochiii.mcp.add', 'the page command for "Add a server by hand" changed');
  });

  for (const typed of [
    `/mcp-server add demo npx -y some-mcp-server@1.2.3`,
    `/mcp-server add`,
    `/mcp-server   add   --env KEY=${DUMMY_KEY_LIKE} demo npx`,
  ]) {
    test(`${typed.replace(DUMMY_KEY_LIKE, '<dummy>')} asks for the popup and does nothing else`, async function () {
      this.timeout(30000);
      await run(typed);
      assert.deepStrictEqual(types(), ['page:command/mcpAdd'], `the host was sent ${JSON.stringify(types())}`);
      assert.strictEqual(await composer(), '', 'the box still holds the command');
      assert.ok(!(await pageOpen()), 'the Customize page opened');
      assert.ok(!(await dialogOpen()), 'the MCP dialog opened');
      // Not shown as a turn, and not in the page's local or session storage.
      const mark = typed.includes(DUMMY_KEY_LIKE) ? DUMMY_KEY_LIKE : 'some-mcp-server';
      const where = await dt.chatEval<string>(
        "(d.getElementById('transcript') ? d.getElementById('transcript').innerText : '') + '\\n' + JSON.stringify([w.localStorage, w.sessionStorage].map((st) => { try { return Object.entries(st); } catch (e) { return []; } }))",
      );
      assert.ok(!where.includes(mark) && !where.includes('/mcp-server'), `the command is on screen or in state: ${where.slice(0, 200)}`);
      // And not in anything the host was sent.
      assert.ok(!JSON.stringify(seen).includes(mark), 'the typed text reached the host');
    });
  }

  for (const typed of [`/mcp-server filesystem`, `/mcp-server addons`, `/mcp-server add-ons here`, `/mcp-server ADD demo`]) {
    test(`${typed} is still a marketplace search`, async function () {
      this.timeout(30000);
      await run(typed);
      assert.ok(!types().includes('page:command/mcpAdd'), `${typed} asked for the add popup`);
      assert.ok(types().includes('page:load'), `the Customize page was not opened: ${JSON.stringify(types())}`);
      assert.ok(await pageOpen(), 'no page is open');
    });
  }

  test('/mcp-server alone is unchanged: the MCP dialog opens, and no add is asked for', async function () {
    this.timeout(30000);
    await run('/mcp-server');
    assert.ok(!types().includes('page:command/mcpAdd'), 'a bare /mcp-server asked for the add popup');
    assert.ok(await dialogOpen(), 'the MCP dialog did not open');
  });

  test('/mcp add is not /mcp-server add: it is left as it was', async function () {
    this.timeout(30000);
    await run('/mcp add demo');
    assert.ok(!types().includes('page:command/mcpAdd'), '/mcp add asked for the add popup');
  });
});
