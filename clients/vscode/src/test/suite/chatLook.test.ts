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

// THE CONVERSATION (UI audit 2026-11-21, step 3). Structural, like the rest of
// this file: the screenshots in the pull request are what show how it looks.
suite('the conversation (UI audit, step 3)', () => {
  const js = media('main.js');
  const panel = fs.readFileSync(path.join(ROOT, 'src', 'chatPanel.ts'), 'utf8');
  const fnBody = (name: string, next: string): string => {
    const from = js.indexOf(`  function ${name}(`);
    const to = js.indexOf(`  function ${next}(`, from + 1);
    assert.ok(from > 0 && to > from, `${name} or ${next} moved; this test reads between them`);
    return js.slice(from, to);
  };

  test('an approval: Deny first and focused, Allow the one primary, Stop task a link, Esc denies', () => {
    const fn = fnBody('showToolApproval', 'approvalQuestion');
    const order = ['actions.appendChild(deny)', 'actions.appendChild(forTurn)', 'actions.appendChild(approve)', 'actions.appendChild(cancel)'].map((s) => fn.indexOf(s));
    assert.ok(order.every((i) => i > 0) && order.every((v, i) => i === 0 || v > order[i - 1]), 'the buttons must be Deny, Allow for this task, Allow, then Stop task');
    assert.match(fn, /approvalButton\('Deny', 'deny'/);
    assert.match(fn, /approvalButton\('Allow', 'approve'/);
    assert.match(fn, /approve\.classList\.add\('primary'\)/);
    assert.match(fn, /cancel\.classList\.add\('link'\)/);
    assert.match(fn, /deny\.focus\(\)/);
    assert.match(fn, /e\.key === 'Escape'[\s\S]{0,120}answerToolApproval\(req, 'deny', 'Deny'\)/, 'Esc in the card must deny');
    assert.ok(!/key === '[yYaA]'|key === 'Enter'/.test(fn), 'no single key may allow');
  });

  test('the risk line keeps the terminal\'s sentences, word for word, and only adds a colour', () => {
    const fn = fnBody('showToolApproval', 'approvalQuestion');
    for (const sentence of [
      "'LEAVES YOUR MACHINE: sent to a third party over the internet'",
      "'NOT SANDBOXED: a separate program with your full access'",
      "'STARTS ANOTHER PROGRAM: a language server (gopls, tsserver or pyright)'",
      "'marked destructive'",
    ]) {
      assert.ok(fn.includes(sentence), `${sentence} is not said as it was`);
    }
    assert.match(fn, /level: 'warn', text: 'NOT SANDBOXED/, 'running outside the sandbox is a warning (amber)');
    assert.match(fn, /level: 'danger', text: 'LEAVES YOUR MACHINE/, 'leaving the machine is danger (red)');
    assert.match(fn, /level: 'danger', text: 'RUNS IN YOUR REAL PROJECT/, 'writing the real project is danger (red)');
    assert.match(fn, /part\.textContent = r\.text/, 'a risk goes in as text');
  });

  test('an edit shows its change inline, built from text nodes, with Open diff and Apply as the primary', () => {
    const diff = fnBody('editDiff', 'showEditProposal');
    assert.match(diff, /line\.textContent = text/);
    assert.ok(!/innerHTML|insertAdjacentHTML/.test(diff), 'the diff assembles markup from the model\'s text');
    assert.match(diff, /EDIT_DIFF_SHOWN/);
    const card = fnBody('showEditProposal', 'showEditResult');
    assert.match(card, /editDiff\(edit\.search, edit\.replace, edit\.file_path\)/);
    assert.match(card, /diffBtn\.textContent = 'Open diff'/);
    assert.match(card, /applyBtn\.className = 'primary'/);
    assert.match(card, /if \(currentRunAuto && !autoAsk\)/, 'an auto-apply run still renders no Apply or Skip, but for the edit the host holds');
    assert.match(panel, /autoAsk: this\.asksBeforeSaving\(this\.pendingBlocks\[this\.currentIndex\]\)/, 'buttons on an auto-apply run come only from the edit the host is holding');
    assert.match(panel, /if \(this\.asksBeforeSaving\(this\.pendingBlocks\[this\.currentIndex\]\)\) \{\s*await new Promise/, 'the host must not save the edit it shows buttons for');
  });

  test('a button is the theme\'s secondary unless it is the primary', () => {
    const styles = panel.slice(panel.indexOf('  button {'), panel.indexOf('  button:disabled'));
    assert.match(styles, /background: var\(--vscode-button-secondaryBackground/);
    assert.match(styles, /button\.primary \{ background: var\(--ct-accent-solid\)/);
    assert.match(styles, /:where\(button:hover:not\(:disabled\)\)/, 'the default hover must not outrank a styled button\'s own');
  });

  test('a reply has no header, and what it stood on is said under it', () => {
    assert.ok(!/function replyHead\(/.test(js), 'the per-reply lotus, name and time are back');
    assert.match(js, /attachCopyFooter\(currentAssistantCard, currentAssistantRaw, lastGroundingInfo\)/);
    const set = fnBody('setGrounding', 'setHistoryInfo');
    assert.match(set, /if \(!info \|\| !info\.workspace_mismatch\) \{\s*groundingEl\.textContent = '';/, 'a routine grounding report must not become a notice');
  });

  test('the host says when no key is in use, and when one connects', () => {
    assert.match(panel, /if \(session\.needsApiKey\) \{\s*this\.postSafe\(\{ type: 'connection', state: 'noKey' \}\)/);
    assert.ok((panel.match(/type: 'connection', state: 'ready'/g) || []).length >= 3, 'every path that puts a key in use must say so');
  });
});
