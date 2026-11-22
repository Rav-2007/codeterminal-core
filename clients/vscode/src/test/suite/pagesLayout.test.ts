import * as assert from 'assert';
import * as vscode from 'vscode';

import { ChatPanel } from '../../chatPanel';
import { ChatDevTools } from '../devtools';

const EXT_ID = 'MochiiiAIAgent.mochiii-vscode';

// THE PAGES BY KEYBOARD (UI audit 2026-11-21, step 4): real key presses into the
// real webview. Customize > MCP lost its sub-tabs for an Add menu and two
// views, and Settings lost its rail for links to sections, so what is tested
// is that each can be opened, used and left without a mouse, and that the
// keyboard is never left on a control that has just been hidden.

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

suite('E2E chat — the pages by keyboard', () => {
  let dt: ChatDevTools;
  let post: (msg: Record<string, unknown>) => Thenable<boolean>;

  async function until(what: string, expr: string): Promise<void> {
    const deadline = Date.now() + 8000;
    while (!(await dt.chatEval<boolean>(expr))) {
      if (Date.now() > deadline) {
        const on = await dt.chatEval("d.activeElement && (d.activeElement.id || d.activeElement.textContent.trim().slice(0, 40))");
        assert.fail(`${what} (the keyboard is on ${JSON.stringify(on)})`);
      }
      await sleep(100);
    }
  }

  async function openPage(page: string, el: string, extra: Record<string, unknown> = {}): Promise<void> {
    await post({ type: 'page:open', page, ...extra });
    await until(`${page} did not open`, `!d.getElementById('${el}').hidden && d.activeElement === d.getElementById('${el}Title')`);
  }

  suiteSetup(async function () {
    this.timeout(60000);
    const ext = vscode.extensions.getExtension(EXT_ID);
    assert.ok(ext, `extension ${EXT_ID} is not in the dev host`);
    await ext.activate();
    for (const cmd of ['workbench.action.closeSidebar', 'workbench.action.closeAuxiliaryBar', 'workbench.action.closePanel', 'notifications.clearAll']) {
      await vscode.commands.executeCommand(cmd).then(undefined, () => undefined);
    }
    ChatPanel.createOrShow(ext.extensionUri);
    const panel: any = (ChatPanel as any).current;
    post = (msg) => panel.host.webview.postMessage(msg);
    dt = await ChatDevTools.connect();
    await until('the chat never loaded', "!!d.getElementById('mcpAddBtn') && !!d.getElementById('promptInput')");
    // Opening the Marketplace asks a one-time question and fetches a catalogue.
    // Neither is what is tested, so it is not mounted.
    await dt.chatEval('(w.__market = w.MochiiiMarketplace, w.MochiiiMarketplace = undefined, true)');
  });

  setup(async () => {
    // Whatever an earlier suite or test left up.
    await post({ type: 'mcpPopupClose' });
    await post({ type: 'connectClose' });
    await dt.chatEval(`(() => {
      const x = d.getElementById('mcpDlgClose'); if (x && x.getClientRects().length) x.click();
      for (const b of Array.from(d.querySelectorAll('[data-close-page]'))) if (b.getClientRects().length) { b.click(); break; }
      return true; })()`);
  });

  suiteTeardown(async () => {
    if (!dt) return;
    await dt.chatEval(`(() => {
      w.MochiiiMarketplace = w.__market;
      for (const b of Array.from(d.querySelectorAll('[data-close-page]'))) if (b.getClientRects().length) { b.click(); break; }
      return true; })()`);
    await dt.setViewport(1200, 900);
    dt.close();
  });

  test('Add server: Enter opens it, the arrow moves in it, Esc closes it and not the page', async function () {
    this.timeout(30000);
    await openPage('customize', 'pageCustomize', { tab: 'mcp', sub: 'installed' });
    await dt.chatEval("(d.getElementById('mcpAddBtn').focus(), true)");
    await dt.press('Enter');
    await until('Enter did not open the menu on its first item', "!d.getElementById('mcpAddMenu').hidden && d.activeElement.hasAttribute('data-mcp-browse')");
    // It was squashed to the width of its button, less 24px, by .pop-menu's rule.
    assert.ok((await dt.chatEval<number>("d.getElementById('mcpAddMenu').getBoundingClientRect().width")) >= 200, 'the menu is squashed');
    await dt.press('ArrowDown');
    await until('the arrow did not move to the second item', "d.activeElement.getAttribute('data-page-command') === 'mcpAdd'");
    await dt.press('Escape');
    await until('Esc did not close the menu back to its button', "d.getElementById('mcpAddMenu').hidden && d.activeElement === d.getElementById('mcpAddBtn')");
    assert.strictEqual(await dt.chatEval("d.getElementById('pageCustomize').hidden"), false, 'Esc closed the page under the menu');
  });

  test('Tab out of the Add menu closes it', async function () {
    this.timeout(30000);
    await openPage('customize', 'pageCustomize', { tab: 'mcp', sub: 'installed' });
    await dt.chatEval("(d.getElementById('mcpAddBtn').focus(), true)");
    await dt.press('Enter');
    await until('the menu did not open', "!d.getElementById('mcpAddMenu').hidden");
    await dt.press('Tab');
    await dt.press('Tab');
    await until('the menu stayed open behind the keyboard', "d.getElementById('mcpAddMenu').hidden");
  });

  test('the Marketplace opens from the menu and leads back, the keyboard following', async function () {
    this.timeout(30000);
    await openPage('customize', 'pageCustomize', { tab: 'mcp', sub: 'installed' });
    await dt.chatEval("(d.getElementById('mcpAddBtn').focus(), true)");
    await dt.press('Enter');
    await until('the menu did not open', "!d.getElementById('mcpAddMenu').hidden");
    await dt.press('Enter');
    await until('the Marketplace did not open on its way back', "!d.getElementById('mcpPanel-market').hidden && d.getElementById('mcpPanel-installed').hidden && d.activeElement.hasAttribute('data-mcp-installed')");
    await dt.press('Enter');
    await until('back did not return to the list', "d.getElementById('mcpPanel-market').hidden && !d.getElementById('mcpPanel-installed').hidden && d.activeElement === d.getElementById('mcpAddBtn')");
  });

  // FOUND 2026-11-22 by measuring the pages at 280 and 300px: a server's
  // Check, Edit and Remove sat in one unwrapped row, and Remove was drawn at
  // x=305..360 in a 300px panel -- off it, where no mouse could reach it.
  test('in a narrow side bar every button of an MCP server is inside the panel', async function () {
    this.timeout(30000);
    await dt.setViewport(350, 700);
    await openPage('customize', 'pageCustomize', { tab: 'mcp', sub: 'installed' });
    // The page load has the host answer with the real list; that answer first.
    await sleep(1500);
    await post({
      type: 'page:mcp',
      agentMode: true,
      notice: '',
      servers: [{ name: 'filesystem', command: 'npx -y @modelcontextprotocol/server-filesystem /home/dev/scratch', source: 'user', canCheck: true, env: [], warnings: [] }],
    });
    await until('the server was not drawn', "!!d.querySelector('#mcpInstalledList .mcp-row .mcp-acts button')");
    const seen = await dt.chatEval<{ width: number; buttons: number; outside: string[] }>(`(() => {
      const width = d.documentElement.clientWidth;
      const all = Array.from(d.querySelectorAll('#mcpInstalledList .mcp-row button'));
      return { width, buttons: all.length, outside: all.filter(b => b.getBoundingClientRect().right > width + 0.5).map(b => b.getAttribute('aria-label') || b.textContent) };
    })()`);
    assert.ok(seen.width <= 360 && seen.buttons >= 3, 'the panel was not narrow, or the row has no actions: ' + JSON.stringify(seen));
    assert.deepStrictEqual(seen.outside, [], 'buttons off the panel: ' + JSON.stringify(seen));
  });

  test('Settings shows every section, and a name jumps to its own without moving the header', async function () {
    this.timeout(30000);
    await dt.setViewport(460, 600);
    await openPage('settings', 'pageSettings');
    assert.strictEqual(await dt.chatEval("Array.from(d.querySelectorAll('.set-section')).filter(s => s.getClientRects().length).length"), 6);
    await dt.chatEval("(d.querySelector('[data-set-jump=\"about\"]').focus(), true)");
    await dt.press('Enter');
    await until('the keyboard did not land on About', "d.activeElement === d.getElementById('setHead-about')");
    const at = await dt.chatEval<{ head: number; header: number; height: number }>(
      "({ head: d.getElementById('setHead-about').getBoundingClientRect().top, header: d.getElementById('appHeader').getBoundingClientRect().top, height: w.innerHeight })",
    );
    assert.ok(at.head >= 0 && at.head < at.height, 'About is off screen: ' + JSON.stringify(at));
    assert.ok(Math.abs(at.header) < 1, 'the jump scrolled the header away: ' + JSON.stringify(at));
    await dt.press('Escape');
    await until('Esc did not close Settings', "d.getElementById('pageSettings').hidden");
  });
});
