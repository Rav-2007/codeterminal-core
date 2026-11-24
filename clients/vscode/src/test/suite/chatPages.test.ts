import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';
import * as vscode from 'vscode';

import { accountView, accountWaiting, gaveUpNote } from '../../chatPages/accountView';
import { ICONS, chatPagesMarkup, chatPagesStyles, headerPageButtons } from '../../chatPages/markup';
import { chatLookStyles } from '../../chatLook';
import { ChatPagesHost } from '../../chatPages/pagesHost';
import {
  MACHINE_SETTINGS,
  PAGE_COMMANDS,
  PAGE_LINKS,
  PAGE_SETTINGS,
  checkSettingUpdate,
  pageCommand,
  pageLink,
} from '../../chatPages/rules';
import { chatPanelBodyMarkup } from '../../chatPanel';
import { ConnectResponse } from '../../daemonClient';

// THE CHAT'S PAGES (Stage 8): Customize, Account, Settings.
//
// They run in the webview, which renders model output and a third party's
// catalog, so what a page may ASK FOR is a table on the host side
// (src/chatPages/rules.ts). These tests hold the tables to what they claim,
// check every button in the markup names an entry that exists, and keep the
// page script to the same no-markup-strings rule as main.js.

const ROOT = path.join(__dirname, '..', '..', '..');
const code = (file: string): string =>
  fs
    .readFileSync(path.join(ROOT, 'media', file), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:])\/\/.*$/gm, '$1');

suite('chat pages: what a page may ask for', () => {
  test('a page can never change where questions go or what may run', () => {
    for (const m of MACHINE_SETTINGS) {
      const r = checkSettingUpdate(m.key, 'https://somewhere.example');
      assert.strictEqual(r.ok, false, `a page could set mochiii.${m.key}, which names where prompts go or what runs`);
    }
    for (const key of ['apiBase', 'configPath', 'model', 'mcp.catalogUrl', '__proto__', 'constructor', '', undefined, 42]) {
      assert.strictEqual(checkSettingUpdate(key, true).ok, false, `accepted ${String(key)}`);
    }
  });

  test('the settings a page may change take only their own values', () => {
    assert.deepStrictEqual(checkSettingUpdate('toolActivity', 'expanded'), { ok: true, key: 'toolActivity' });
    assert.strictEqual(checkSettingUpdate('toolActivity', 'loud').ok, false);
    assert.strictEqual(checkSettingUpdate('defaultMode', 'plan').ok, true);
    assert.strictEqual(checkSettingUpdate('defaultMode', 'yolo').ok, false);
    assert.strictEqual(checkSettingUpdate('sendEditorContext', 'true').ok, false, 'a string is not a boolean');
    assert.strictEqual(checkSettingUpdate('history.autoSave', false).ok, true);
  });

  test('every setting a page may change is contributed, and none is machine-scoped', () => {
    const pkg = JSON.parse(fs.readFileSync(path.join(ROOT, 'package.json'), 'utf8'));
    const props = pkg.contributes.configuration.properties;
    for (const key of Object.keys(PAGE_SETTINGS)) {
      const prop = props[`mochiii.${key}`];
      assert.ok(prop, `mochiii.${key} is not a contributed setting`);
      assert.notStrictEqual(prop.scope, 'machine', `mochiii.${key} is machine-scoped; a page must not write it`);
    }
    for (const m of MACHINE_SETTINGS) {
      assert.strictEqual(props[`mochiii.${m.key}`]?.scope, 'machine', `mochiii.${m.key} must stay machine-scoped`);
    }
  });

  test('commands and links are looked up, never taken as given', () => {
    assert.strictEqual(pageCommand('mochiii.connect'), undefined, 'a command id is not a page name');
    assert.strictEqual(pageCommand('workbench.action.terminal.new'), undefined);
    assert.strictEqual(pageCommand('__proto__'), undefined);
    assert.strictEqual(pageLink('https://evil.example'), undefined);
    for (const url of Object.values(PAGE_LINKS)) {
      assert.match(url, /^https:\/\//);
    }
  });

  test('every command a page may run exists', async () => {
    const ext = vscode.extensions.getExtension('MochiiiAIAgent.mochiii-vscode');
    await ext?.activate();
    const all = new Set(await vscode.commands.getCommands(true));
    for (const [name, id] of Object.entries(PAGE_COMMANDS)) {
      assert.ok(all.has(id), `page command ${name} names ${id}, which is not registered`);
    }
  });

  test('every button in the pages names an entry in a table', () => {
    const html = chatPagesMarkup();
    for (const m of html.matchAll(/data-page-command="([^"]+)"/g)) {
      assert.ok(pageCommand(m[1]), `a button asks for the command "${m[1]}", which the table does not have`);
    }
    for (const m of html.matchAll(/data-page-link="([^"]+)"/g)) {
      assert.ok(pageLink(m[1]), `a button asks for the link "${m[1]}", which the table does not have`);
    }
    for (const m of html.matchAll(/data-setting="([^"]+)"/g)) {
      assert.ok(Object.prototype.hasOwnProperty.call(PAGE_SETTINGS, m[1]), `a control writes "${m[1]}", which a page may not`);
    }
  });

  test('the host refuses a machine setting from a page, end to end', async () => {
    const posts: any[] = [];
    const before = vscode.workspace.getConfiguration('mochiii').inspect('apiBase')?.globalValue;
    const host = new ChatPagesHost({
      post: (m) => posts.push(m),
      currentTier: () => '',
      currentModel: () => '',
      selectTier: async () => undefined,
      providerNotes: () => [],
    });
    try {
      assert.strictEqual(host.handle({ type: 'page:setSetting', key: 'apiBase', value: 'https://evil.example/v1' }), true);
      await new Promise((r) => setTimeout(r, 100));
      assert.strictEqual(vscode.workspace.getConfiguration('mochiii').inspect('apiBase')?.globalValue, before);
      assert.ok(posts.some((p) => p.type === 'page:notice' && /may not change/.test(p.text)), 'the refusal was not said');
      assert.strictEqual(host.handle({ type: 'prompt', text: 'not a page message' }), false, 'a chat message was swallowed');
    } finally {
      host.dispose();
    }
  });
});

suite('chat pages: markup', () => {
  const html = chatPagesMarkup();

  test('each page is hidden until opened, has a heading, and a way back', () => {
    for (const id of ['pageCustomize', 'pageAccount', 'pageSettings']) {
      const open = new RegExp(`<section id="${id}" class="page" hidden role="region" aria-labelledby="${id}Title">`);
      assert.match(html, open, `${id} is not a hidden, labelled region`);
      assert.match(html, new RegExp(`<h2 id="${id}Title" tabindex="-1">`), `${id} has no focusable heading`);
    }
    assert.strictEqual((html.match(/data-close-page/g) || []).length, 3, 'every page needs its back button');
  });

  test('each header button controls the page it opens', () => {
    const buttons = headerPageButtons();
    for (const [btn, page] of [
      ['customizeBtn', 'pageCustomize'],
      ['accountBtn', 'pageAccount'],
      ['settingsBtn', 'pageSettings'],
    ]) {
      assert.match(buttons, new RegExp(`id="${btn}"[^>]*aria-controls="${page}" aria-expanded="false"`));
    }
  });

  test('every tab controls a panel that exists', () => {
    for (const m of html.matchAll(/role="tab" id="([^"]+)" data-tab="[^"]+" aria-controls="([^"]+)"/g)) {
      assert.ok(html.includes(`id="${m[2]}"`), `tab ${m[1]} controls ${m[2]}, which is not in the markup`);
    }
  });

  test('the icons are drawn inline and hidden from screen readers', () => {
    for (const svg of Object.values(ICONS)) {
      assert.match(svg, /aria-hidden="true"/);
    }
  });

  test('the Account page has a note and a Retry that asks the way Refresh does', () => {
    assert.match(html, /<p id="acctNote" class="page-note acct-note" hidden>/, 'the note starts hidden');
    assert.match(html, /<button type="button" id="acctRetry" class="pg-btn" data-page-refresh>Retry<\/button>/);
    const js = code('pages.js');
    assert.match(js, /dot\.className = 'dot ' \+ \(v\.waiting \? 'wait'/, 'the waiting state has its own dot');
    assert.match(js, /note\.hidden = !v\.note/, 'the note shows only when there is one');
  });

  test('a page tells the host when it is hidden, so the host stops watching the daemon', () => {
    const js = code('pages.js');
    assert.match(js, /function hide\(name\) \{[\s\S]*?vscode\.postMessage\(\{ type: 'page:hidden', page: name \}\);/);
  });

  test('pages.js builds no markup and uses no HTML sink, like main.js', () => {
    const js = code('pages.js');
    for (const sink of ['innerHTML', 'outerHTML', 'insertAdjacentHTML', 'document.write', 'eval(', 'new Function']) {
      assert.ok(!js.includes(sink), `media/pages.js uses ${sink}`);
    }
    assert.strictEqual(js.match(/['"`]\s*<\/?[a-zA-Z][a-zA-Z0-9-]*[\s/>]/g), null, 'media/pages.js assembles markup as strings');
  });

  // The Settings page's mode list said "Auto — edits apply as they arrive" after
  // #130 made Auto full access inside the project. Each option now says what the
  // modes menu over the composer says, in that menu's own first sentence.
  test('the Settings page describes each mode as the modes menu does', () => {
    const menu = chatPanelBodyMarkup();
    const firstSentence = (s: string) => s.split(/(?<=\.)\s/)[0].replace(/\.$/, '').toLowerCase();
    for (const mode of ['auto', 'manual', 'plan']) {
      const item = menu.match(new RegExp(`data-mode="${mode}">[\\s\\S]*?<div class="mode-name">([^<]+)</div>\\s*<div class="mode-desc">([^<]+)</div>`));
      assert.ok(item, `the modes menu has no ${mode}`);
      const option = html.match(new RegExp(`<option value="${mode}">([^<]+)</option>`));
      assert.ok(option, `the Settings page has no ${mode} option`);
      const [name, said] = option[1].split(' — ');
      assert.strictEqual(name, item[1], `the ${mode} option is not named as in the modes menu`);
      assert.strictEqual(said.toLowerCase(), firstSentence(item[2]), `the Settings page describes ${mode} differently from the modes menu`);
    }
  });

  test('pages.js names commands, links and settings, never ids or URLs', () => {
    const js = code('pages.js');
    assert.ok(!/mochiii\.[a-z]+\.?[a-zA-Z]*['"]/.test(js.replace(/mochiii\.in/g, '')), 'pages.js names a VS Code command or setting id');
    assert.ok(!/https?:\/\//.test(js), 'pages.js carries a URL');
  });
});

suite('chat pages: screen commands', () => {
  test('/mcp, /mcp-server, /account, /config and a bare /model open a screen, not a reply', () => {
    const js = code('main.js');
    assert.match(js, /function openScreenFor\(typed\)/);
    assert.match(js, /\(mcp\|mcp-server\|account\|config\|model\)/);
    assert.match(js, /if \(pendingAttachments\.length === 0 && openScreenFor\(typed\)\)/, 'send() does not try a screen first');
  });
});

suite('chat pages: the Account page', () => {
  const show = (over: Partial<ConnectResponse>): ConnectResponse => ({
    protocol_version: 1,
    ok: true,
    outcome: 'shown',
    detail: '',
    in_use: true,
    ...over,
  });

  test('a stored Groq key: provider, masked key, where it came from, the model', () => {
    const v = accountView({
      show: show({ active_api_base: 'https://api.groq.com/openai/v1', key_source: 'stored', active_masked_key: 'gsk_…abcd', model_slug: 'qwen/qwen3.8-27b' }),
      daemonVersion: '0.2.0',
      spend: undefined,
      currentModel: '',
      providerNotes: [],
    });
    assert.strictEqual(v.provider, 'Groq');
    assert.strictEqual(v.badge, 'G');
    assert.strictEqual(v.key, 'gsk_…abcd');
    assert.strictEqual(v.keyMore, 'stored on this machine');
    assert.strictEqual(v.model, 'qwen/qwen3.8-27b');
    assert.strictEqual(v.reachable, true);
  });

  test('a model chosen in this window that differs from the daemon default says so', () => {
    const v = accountView({
      show: show({ active_api_base: 'https://api.groq.com/openai/v1', key_source: 'stored', model_slug: 'a/one' }),
      daemonVersion: '',
      spend: undefined,
      currentModel: 'b/two',
      providerNotes: [],
    });
    assert.strictEqual(v.model, 'b/two');
    assert.match(v.modelMore, /daemon's own default is a\/one/);
  });

  test('a local model server needs no key, and proxy mode holds none here', () => {
    const local = accountView({ show: show({ active_api_base: 'http://localhost:11434/v1', key_source: 'none' }), daemonVersion: '', spend: undefined, currentModel: '', providerNotes: [] });
    assert.strictEqual(local.key, 'none needed');
    const proxy = accountView({ show: show({ active_api_base: 'https://proxy.example/v1', key_source: 'proxy' }), daemonVersion: '', spend: undefined, currentModel: '', providerNotes: [] });
    assert.strictEqual(proxy.key, 'none on this machine');
  });

  test('an unreachable daemon is said, not shown as an empty account', () => {
    const v = accountView({ show: undefined, showError: 'connect ENOENT', daemonVersion: '', spend: undefined, currentModel: '', providerNotes: [] });
    assert.strictEqual(v.reachable, false);
    assert.match(v.base, /ENOENT/);
  });

  test('the waiting state is not a verdict, and carries no raw error', () => {
    const w = accountWaiting({ currentModel: 'm/x', providerNotes: ['a note'], waitMs: 20000 });
    assert.strictEqual(w.waiting, true);
    assert.strictEqual(w.reachable, false);
    assert.strictEqual(w.provider, 'Waiting for the daemon…');
    assert.match(w.base, /20 seconds/);
    assert.ok(!/lockfile|ENOENT|ECONNREFUSED/.test(JSON.stringify(w)));
    assert.strictEqual(w.model, 'm/x');
    assert.deepStrictEqual(w.privacy, ['a note']);
  });

  test('a wait that gave up adds a note and leaves the raw error as it was', () => {
    const down = { show: undefined, showError: 'daemon not found (expected a lockfile at /x/y.lock): ENOENT', daemonVersion: '', spend: undefined, currentModel: '', providerNotes: [] };
    const plain = accountView(down);
    const gaveUp = accountView({ ...down, gaveUpAfterMs: 20000 });
    assert.strictEqual(plain.note, undefined, 'a plain failure has no note');
    assert.strictEqual(gaveUp.base, plain.base, 'the raw error was reworded');
    assert.strictEqual(gaveUp.note, gaveUpNote(20000));
    assert.match(gaveUp.note!, /20 seconds/);
    // A daemon that did answer never carries the note, whatever was asked.
    const up = accountView({ show: show({ active_api_base: 'https://api.groq.com/openai/v1' }), daemonVersion: '', spend: undefined, currentModel: '', providerNotes: [], gaveUpAfterMs: 20000 });
    assert.strictEqual(up.note, undefined);
  });

  test('spend is shown against a limit when there is one', () => {
    const v = accountView({
      show: show({ active_api_base: 'https://openrouter.ai/api/v1', key_source: 'stored' }),
      daemonVersion: '',
      spend: { session: { tokens: 100 }, day: { tokens: 0, usd: 0.5, limit_usd: 2 } },
      currentModel: '',
      providerNotes: [],
    });
    const day = v.meters.find((m) => m.label === 'Today');
    assert.ok(day);
    assert.strictEqual(day!.fraction, 0.25);
    assert.strictEqual(v.meters.find((m) => m.label === 'This session')!.fraction, -1, 'no limit means no bar');
  });
});

suite('chat pages: the composer toolbar', () => {
  const panel = fs.readFileSync(path.join(ROOT, 'src', 'chatPanel.ts'), 'utf8');

  test('+ sits beside the model chip, labelled, and MCP is not repeated there', () => {
    assert.match(panel, /id="contextAddBtn"[^>]*aria-label="Add context"/);
    // The plug opened a third copy of the MCP list (after Customize and /mcp)
    // in the one row that overflowed in a narrow sidebar (UI audit 2026-11-21).
    assert.doesNotMatch(panel, /id="toolsBtn"/, 'the composer must not carry an MCP button again');
  });

  test('@ context is read from the editor by the host, and arrives as a removable attachment', () => {
    assert.match(panel, /msg\.type === 'addContext'/);
    assert.match(panel, /private attachText\(name: string, text: string, desc: string\)/);
    assert.match(panel, /type: 'attachmentStarted', id, name/, 'context must arrive the way a file does, as a chip');
    const js = code('main.js');
    assert.match(js, /before === '' \|\| \/\\s\/\.test\(before\)/, 'an @ inside a word (an address) must stay text');
  });
});

// THE UI AUDIT'S REMOVALS (2026-11-21, build plan step 1). Each of these was on
// screen and either did nothing, promised something that does not exist, or
// repeated a control that has a home elsewhere. Asserted on the markup and the
// scripts as shipped, comments stripped, so a removal cannot drift back in.
suite('chat pages: what the panel no longer shows', () => {
  const body = chatPanelBodyMarkup('logo.png').replace(/<!--[\s\S]*?-->/g, '');
  const pagesHtml = chatPagesMarkup().replace(/<!--[\s\S]*?-->/g, '');
  const popover = (): string => {
    const at = body.indexOf('id="modelPopup"');
    const end = body.indexOf('id="contextPopup"');
    assert.ok(at > 0 && end > at, 'the model menu moved; this test reads it between modelPopup and contextPopup');
    return body.slice(at, end);
  };

  test('an answer has Copy and no feedback buttons, which only changed their own colour', () => {
    const js = code('main.js');
    assert.match(js, /copyBtn\.textContent = 'Copy'/);
    for (const gone of ['👍', '👎', 'Was this helpful?', "'Helpful'", "'Not helpful'"]) {
      assert.ok(!js.includes(gone), `main.js still has ${gone}`);
    }
  });

  test('the Account page advertises no sign-in that does not exist', () => {
    const at = pagesHtml.indexOf('id="pageAccount"');
    const account = pagesHtml.slice(at, pagesHtml.indexOf('</section>', at));
    assert.ok(at > 0, 'no Account page in the markup');
    assert.doesNotMatch(account, /coming soon|Sign in to mochiii/i);
  });

  test('the empty chat has no starters and no troubleshooting', () => {
    const at = body.indexOf('id="firstRun"');
    const welcome = body.slice(at, body.indexOf('id="notices"'));
    assert.doesNotMatch(welcome, /welcome-chip|Other ideas|welcome-card/);
    assert.doesNotMatch(welcome, /Restart Daemon|first answer can take/);
  });

  test('the header says Mochiii and the folder, without a tagline, and has three buttons', () => {
    assert.match(body, /class="brand-name">Mochiii<\/span><span class="brand-folder" id="brandFolder" hidden>/);
    assert.doesNotMatch(body, /AI Coding Assistant|brand-sub/);
    const actions = body.slice(body.indexOf('class="header-actions"'), body.indexOf('id="moreMenu"'));
    const buttons = [...actions.matchAll(/<button[^>]*id="([a-zA-Z]+)"/g)].map((m) => m[1]);
    assert.deepStrictEqual(buttons, ['newChatBtn', 'historyBtn', 'moreBtn'], 'the gear went: it and ⋯ both led to settings');
    const panel = fs.readFileSync(path.join(ROOT, 'src', 'chatPanel.ts'), 'utf8');
    assert.match(panel, /type: 'folder', name: path\.basename\(workspacePath\(\)\)/, 'the host must name the folder');
  });

  test('the ⋯ menu lists the pages, then the bookmark', () => {
    const menu = body.slice(body.indexOf('id="moreMenu"'), body.indexOf('id="historyPopup"'));
    const ids = [...menu.matchAll(/<button[^>]*id="([a-zA-Z]+)"/g)].map((m) => m[1]);
    assert.deepStrictEqual(ids, ['customizeBtn', 'accountBtn', 'settingsBtn', 'bookmarkBtn']);
  });

  test('the composer is one row: mode, model, effort ... add, context, send', () => {
    const bar = body.slice(body.indexOf('<div class="composer-bar">'), body.indexOf('id="contextMenu"'));
    const ids = [...bar.matchAll(/<(?:button|span)[^>]*id="([a-zA-Z]+)"/g)].map((m) => m[1]).filter((id) => /Btn$|Chip$|Toggle$/.test(id));
    assert.deepStrictEqual(ids, ['autoApplyToggle', 'modelChip', 'specChip', 'effortBtn', 'noticesChip', 'contextAddBtn', 'contextBtn', 'sendBtn']);
    assert.doesNotMatch(bar, /id="atBtn"|id="toolsBtn"|id="gearBtn"/);
  });

  test('the model menu is the models and a way to another provider -- nothing that has a home elsewhere', () => {
    const p = popover();
    assert.match(p, /id="modelList"/, 'the model list is what the model chip opens; it stays');
    assert.match(p, /id="switchProviderBtn"/);
    assert.doesNotMatch(p, /id="providerGrid"|id="contextPopup"|id="contextBar"/, 'provider cards and the context meter have their own homes');
    assert.match(body, /id="contextBtn"[^>]*aria-controls="contextPopup"/, 'the ring beside Send opens the context meter');
    assert.doesNotMatch(p, /id="effortSeg"|id="modeSeg"/);
    assert.doesNotMatch(p, /data-open-page=/);
    assert.match(body, /id="effortBtn"/, 'the effort chip is the one place to change effort');
    assert.match(body, /id="autoApplyToggle"/, 'the mode chip is the one place to change mode');
    assert.match(headerPageButtons(), /id="customizeBtn"[\s\S]*id="accountBtn"[\s\S]*id="settingsBtn"/, 'the ⋯ menu is the way to the pages');
  });
});

// THE PAGES, ONE PLACE PER CONTROL (UI audit 2026-11-21, build plan step 4).
// The markup as shipped; pagesLayout.test.ts drives the same pages with real
// key presses.
suite('chat pages: one place per control', () => {
  const html = chatPagesMarkup().replace(/<!--[\s\S]*?-->/g, '');
  const between = (from: string, to: string): string => {
    const at = html.indexOf(from);
    const end = html.indexOf(to, at + 1);
    assert.ok(at >= 0 && end > at, `${from} or ${to} moved; this test reads between them`);
    return html.slice(at, end);
  };
  const count = (s: string, re: RegExp): number => (s.match(re) || []).length;

  test('MCP is one list and one Add button, whose menu holds the two ways to add', () => {
    const mcp = between('id="custPanel-mcp"', 'id="custPanel-specs"');
    assert.doesNotMatch(mcp, /data-tabs=|role="tab"/, 'the Installed / Marketplace sub-tabs are back');
    assert.strictEqual(count(mcp, /pg-btn primary/g), 1, 'MCP has one primary button');
    assert.strictEqual(count(mcp, /data-mcp-browse/g), 1, 'one route to the Marketplace');
    assert.strictEqual(count(mcp, /data-page-command="mcpAdd"/g), 1, 'one route to a server of your own');
    const menu = between('id="mcpAddMenu"', 'id="mcpNotice"');
    assert.match(menu, /data-mcp-browse/);
    assert.match(menu, /data-page-command="mcpAdd"/);
    assert.match(mcp, /id="mcpPanel-market" hidden>\s*<button[^>]*data-mcp-installed/, 'the Marketplace needs a way back');
  });

  test('Account is one status card, one primary and a quiet Forget key', () => {
    const account = between('id="pageAccount"', 'id="pageSettings"');
    const card = between('class="conn-card"', 'id="acctNote"');
    for (const id of ['acctProvider', 'acctKey', 'acctModel']) {
      assert.ok(card.includes(`id="${id}"`), `${id} is outside the status card`);
    }
    assert.strictEqual(count(account, /pg-btn primary/g), 1, 'Account has one primary button');
    assert.match(account, /data-page-command="connect">Change provider or key</);
    assert.match(account, /class="link-btn danger" data-page-command="forgetKey">Forget key</);
    assert.doesNotMatch(account, /data-open-models/, 'the model chip is where the model is changed');
  });

  test('Settings is one page of labelled sections, each reached by name', () => {
    const settings = between('id="pageSettings"', 'id="mcpDlg"');
    assert.doesNotMatch(settings, /role="tab|data-tabs=/, 'the rail of tabs is back');
    const jumps = [...settings.matchAll(/data-set-jump="([a-z]+)">([^<]+)</g)];
    assert.deepStrictEqual(jumps.map((m) => m[2]), ['Model', 'Behaviour', 'Workspace', 'Privacy', 'Advanced', 'About']);
    for (const [, key] of jumps) {
      assert.match(settings, new RegExp(`<section id="setPanel-${key}" class="set-section" aria-labelledby="setHead-${key}">`), `${key} has no visible section`);
    }
  });

  test('Settings does not repeat the effort chip', () => {
    assert.doesNotMatch(html, /id="setEffort"/, 'the effort chip is the one place to change effort');
    assert.match(html, /id="setDefaultModel"/, 'the saved default is not the chip (which lasts for this window), so it stays');
  });

  test('in High Contrast an MCP switch that is on, and a primary in the /mcp dialog, can be seen', () => {
    // The theme's button colour there is black (since step 2 the accent fill).
    assert.match(chatLookStyles(), /body\.vscode-high-contrast \.mcp-switch\[aria-checked="true"\] \{ background: var\(--ct-accent\); \}/);
    assert.match(chatPagesStyles(), /\.mcp-dlg-btn\.primary \{[^}]*border-color: var\(--vscode-button-border/);
  });

  test('the Marketplace in the panel has one way back', () => {
    assert.match(code('marketplace.js'), /button\('Installed servers', 'btn secondary manage'/);
    assert.match(chatPagesStyles(), /#marketRoot \.manage \{ display: none; \}/);
  });

  test('Tools says it is loading, then says when there are none', () => {
    assert.match(html, /id="toolStatus"[^>]*role="status">Loading tools…</);
    assert.match(code('pages.js'), /status\.hidden = \(msg\.tools \|\| \[\]\)\.length > 0;\s*status\.textContent = 'No tools to show\.'/);
  });
});
