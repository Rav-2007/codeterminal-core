import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';
import * as vscode from 'vscode';

import { accountView } from '../../chatPages/accountView';
import { ICONS, chatPagesMarkup, headerPageButtons } from '../../chatPages/markup';
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

  test('pages.js builds no markup and uses no HTML sink, like main.js', () => {
    const js = code('pages.js');
    for (const sink of ['innerHTML', 'outerHTML', 'insertAdjacentHTML', 'document.write', 'eval(', 'new Function']) {
      assert.ok(!js.includes(sink), `media/pages.js uses ${sink}`);
    }
    assert.strictEqual(js.match(/['"`]\s*<\/?[a-zA-Z][a-zA-Z0-9-]*[\s/>]/g), null, 'media/pages.js assembles markup as strings');
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

  test('@ and the plug sit beside the model chip, labelled', () => {
    assert.match(panel, /id="contextAddBtn"[^>]*aria-label="Add context"/);
    assert.match(panel, /id="toolsBtn"[^>]*aria-label="MCP servers" aria-controls="pageCustomize"/);
  });

  test('@ context is read from the editor by the host, and arrives as a removable attachment', () => {
    assert.match(panel, /msg\.type === 'addContext'/);
    assert.match(panel, /private attachText\(name: string, text: string, desc: string\)/);
    assert.match(panel, /type: 'attachmentStarted', id, name/, 'context must arrive the way a file does, as a chip');
    const js = code('main.js');
    assert.match(js, /before === '' \|\| \/\\s\/\.test\(before\)/, 'an @ inside a word (an address) must stay text');
  });
});
