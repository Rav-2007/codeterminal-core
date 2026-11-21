import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';

import { chatPanelBodyMarkup } from '../../chatPanel';
import { chatLookStyles } from '../../chatLook';
import { SLASH_CATALOG } from '../../slashCommands';

// THE CHAT'S LOOK (Stage 9). Structural checks only, as accessibility.test.ts
// says of itself: they hold the markup and the scripts to what the redesign
// relies on, and say nothing about how it looks. The renders compared against
// the owner's mockups are what showed that.

const ROOT = path.join(__dirname, '..', '..', '..');
const media = (file: string): string => fs.readFileSync(path.join(ROOT, 'media', file), 'utf8');

suite('the chat look (Stage 9)', () => {
  const html = chatPanelBodyMarkup('logo.png');
  const css = chatLookStyles();

  test('icon buttons take back the padding every <button> is given, so their icons are not crushed to dots', () => {
    // chatPanelStyles() gives every button `padding: 6px 14px`; a 30px icon
    // button kept 2-4px for a 16px icon (measured in a headless render).
    assert.match(css, /\.icon-btn, \.composer-icon \{ padding: 0; \}/);
    assert.match(css, /\.icon-btn svg, \.composer-icon svg[^{]*\{ flex-shrink: 0;/);
  });

  test('the empty chat is the lotus, one line and the recent chats -- no starter cards', () => {
    // UI audit 2026-11-21, step 2, and the owner's choice of 2026-11-22: the
    // terminal app's first screen ("What can I do for you?") and the last few
    // chats in this folder. The four starter cards were a menu of commands the
    // / menu already is.
    const at = html.indexOf('id="firstRun"');
    const welcome = html.slice(at, html.indexOf('id="notices"'));
    assert.ok(at > 0, 'no empty-chat block');
    assert.match(welcome, /<h1 class="welcome-title">What can I do for you\?<\/h1>/);
    assert.match(welcome, /id="recentChats"[^>]*hidden/, 'the recent list must start hidden: it is shown only when there are chats');
    assert.match(welcome, /id="recentList"[^>]*role="list"/);
    assert.doesNotMatch(welcome, /data-cmd=|welcome-card/);
  });

  test('a recent chat only opens that chat; nothing is sent', () => {
    const js = media('main.js');
    const recent = js.slice(js.indexOf('  function renderRecent('), js.indexOf('  // The folder this chat works in'));
    assert.ok(recent.length > 100, 'renderRecent moved; this test reads it up to showFolder');
    assert.match(recent, /postMessage\(\{ type: 'historyResume', id: e\.id \}\)/);
    assert.ok(!/type: 'prompt'|sendText|send\(/.test(recent), 'a recent chat sends a prompt');
    assert.match(recent, /textContent = e\.title/, 'a title goes in as text, never as markup');
    assert.ok(!/innerHTML/.test(recent));
  });

  test('the ⋯ menu keeps the openers pages.js looks up, and the bookmark main.js does', () => {
    const menu = html.slice(html.indexOf('id="moreMenu"'));
    for (const id of ['bookmarkBtn', 'customizeBtn', 'accountBtn', 'settingsBtn']) {
      assert.ok(menu.includes(`id="${id}"`), `#${id} is not in the ⋯ menu`);
    }
  });

  test('the ＋ menu offers only sources the host can read', () => {
    const offered = [...html.matchAll(/data-context="([a-zA-Z]+)"/g)].map((m) => m[1]);
    const host = fs.readFileSync(path.join(ROOT, 'src', 'chatPanel.ts'), 'utf8');
    for (const kind of offered) {
      assert.ok(host.includes(`case '${kind}':`), `the ＋ menu offers "${kind}", which onAddContext does not handle`);
    }
    assert.ok(host.includes(`case 'mention':`), '@ has no source on the host');
    assert.deepStrictEqual(offered[0], 'mention', 'a file by name (@) is the first thing ＋ offers: it has no button of its own');
  });

  test('every code block carries its own Copy, of that block alone', () => {
    const js = media('main.js');
    const from = js.indexOf('  function renderFencedContent(');
    const to = js.indexOf('  function attachCopyFooter(');
    // A renamed function must fail here, not widen the slice to the whole file.
    assert.ok(from > 0 && to > from, 'renderFencedContent or attachCopyFooter moved; this test reads between them');
    const fenced = js.slice(from, to);
    assert.match(fenced, /className = 'code-copy'/);
    assert.match(fenced, /const text = code;/);
    assert.match(fenced, /writeText\(text\)/);
  });

  test('a tool row says its outcome in words and how long it took', () => {
    const js = media('main.js');
    assert.match(js, /const WORK_BADGES = \{ run: 'Running', ok: 'Done', fail: 'Failed', denied: 'Not run'/);
    assert.match(js, /row\.append\(glyph, name, why, badge, time\)/);
  });

  test('history is grouped by day, and a searched word is marked through text nodes', () => {
    const js = media('main.js');
    assert.match(js, /const byDay = \{ Today: \[\], Yesterday: \[\], 'Earlier this week': \[\], Earlier: \[\] \}/);
    const mark = js.slice(js.indexOf('  function markMatch('), js.indexOf('  function historyMessage('));
    assert.match(mark, /createTextNode/);
    assert.ok(!/innerHTML/.test(mark), 'markMatch builds markup from a string');
  });
});
