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

  test('every welcome card and idea fills in a command that exists, with an argument', () => {
    const starters = [...html.matchAll(/data-cmd="\/([a-z-]+) " data-arg="([^"]+)"/g)];
    assert.ok(starters.length >= 8, `expected the 4 cards and 4 ideas, found ${starters.length}`);
    const known = new Set(SLASH_CATALOG.map((d) => d.name));
    for (const [, name, arg] of starters) {
      assert.ok(known.has(name), `/${name} is not a command`);
      assert.ok(arg.trim().length > 0, `/${name} has no default argument`);
    }
  });

  test('a welcome card only fills the composer; it never sends', () => {
    const js = media('controls.js');
    const section = js.slice(js.indexOf('// ---- the welcome'), js.indexOf('return {'));
    assert.match(section, /hooks\.prefill\(/);
    assert.ok(!/postMessage|sendText|send\(/.test(section), 'a welcome card sends on its own');
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
  });

  test('every code block carries its own Copy, of that block alone', () => {
    const js = media('main.js');
    const fenced = js.slice(js.indexOf('  function renderFencedContent('), js.indexOf('  function attachFeedbackFooter('));
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
