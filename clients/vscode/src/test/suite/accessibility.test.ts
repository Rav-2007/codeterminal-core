// Accessibility structure tests for the webview.
//
// A grep for aria-*, role= and tabindex across the entire webview -- both
// media/main.js and src/chatPanel.ts -- previously returned NOTHING. A
// screen-reader user could not follow a streaming answer (no live region), could
// not read the Auto-apply toggle's state (it existed only in textContent and a
// CSS class), and could not operate the edit-approval flow -- Gate 4 of the
// safety pipeline, where the user authorizes writes to their own files, which
// presumed sight.
//
// SCOPE, STATED HONESTLY. These are STRUCTURAL assertions: they prove the roles,
// labels and live regions are present and stay present through a refactor, which
// is what makes them worth having in CI. They do NOT prove the experience is
// good. Real NVDA / VoiceOver / Orca passes remain on the blocked list -- an
// announcement order that reads badly, or a label that is technically correct
// and practically useless, will pass every assertion here.
//
// The static markup is asserted through chatPanelBodyMarkup (getHtml itself
// needs a live webview and a nonce a test cannot supply). The DYNAMIC attributes
// -- the toggle's aria-checked and the per-edit diff labels -- are applied by
// media/main.js at runtime, and VS Code exposes no webview DOM to the test host,
// so those are asserted against main.js's source text. That is a weaker check
// than reading the live DOM, and is labelled as such rather than over-claimed.

import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';

import { chatPanelBodyMarkup } from '../../chatPanel';

function mainJsSource(): string {
  // out/test/suite -> ../../.. is the extension root.
  return fs.readFileSync(path.join(__dirname, '..', '..', '..', 'media', 'main.js'), 'utf8');
}

// mainJsCode strips comments so a sink check reads CODE, not prose. main.js is
// heavily commented and several of those comments say "textContent (never
// innerHTML)" -- a naive substring search over the whole file reports the word
// innerHTML five times and every one of them is a promise not to use it.
function mainJsCode(): string {
  return mainJsSource()
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/(^|[^:])\/\/.*$/gm, '$1');
}

suite('Webview accessibility structure', () => {
  test('the transcript is a live log region so streamed tokens are announced', () => {
    const html = chatPanelBodyMarkup();
    const transcript = /<div id="transcript"[^>]*>/.exec(html);
    assert.ok(transcript, 'no #transcript element found at all');

    const tag = transcript[0];
    assert.match(tag, /role="log"/, '#transcript needs role="log" or a streaming answer is never announced');
    assert.match(tag, /aria-live="polite"/, '#transcript needs aria-live="polite"');
    // The load-bearing one: aria-atomic="true" would make the reader re-read the
    // WHOLE transcript on every appended token, which is unusable at streaming
    // rates. "false" is what makes incremental announcement work.
    assert.match(
      tag,
      /aria-atomic="false"/,
      '#transcript needs aria-atomic="false" -- "true" re-reads the entire transcript on every token'
    );
  });

  test('the auto-apply toggle is a labelled switch, not a bare button', () => {
    const html = chatPanelBodyMarkup();
    const toggle = /<button id="autoApplyToggle"[\s\S]*?>/.exec(html);
    assert.ok(toggle, 'no #autoApplyToggle element found');

    const tag = toggle[0];
    assert.match(tag, /role="switch"/, 'the auto-apply toggle needs role="switch"');
    assert.match(tag, /aria-checked="(true|false)"/, 'the auto-apply toggle needs an initial aria-checked');
    assert.match(tag, /aria-label="[^"]+"/, 'the auto-apply toggle needs an accessible name');
  });

  test('auto-apply state changes are mirrored into aria-checked', () => {
    // This is the control that decides whether edits reach disk without a
    // per-edit confirmation. Its state must not live only in textContent.
    const js = mainJsSource();
    assert.match(
      js,
      /autoApplyToggle\.setAttribute\(\s*'aria-checked'/,
      "renderAutoApplyToggle must set aria-checked, or the switch's state is invisible to a reader"
    );
  });

  test('both text inputs have accessible names', () => {
    const html = chatPanelBodyMarkup();
    // A placeholder is not an accessible name.
    assert.match(html, /<label for="promptInput"/, 'the prompt input needs a <label>');
    assert.match(html, /<label for="searchInput"/, 'the search input needs a <label>');
  });

  test('the notice strips are status regions so redaction and degradation are announced', () => {
    const html = chatPanelBodyMarkup();
    for (const id of ['redactions', 'degraded', 'grounding', 'historyNotice', 'provider']) {
      const el = new RegExp(`<div id="${id}"[^>]*>`).exec(html);
      assert.ok(el, `no #${id} element found`);
      assert.match(
        el[0],
        /role="status"/,
        `#${id} needs role="status" -- a redaction or degradation notice the user cannot hear ` +
          'is a notice that did not happen'
      );
      assert.match(el[0], /aria-live="polite"/, `#${id} needs aria-live="polite"`);
    }
  });

  test('the edit-approval surface names the file and the change size', () => {
    // Gate 4. A sighted user reads "Edit 2 of 3", the path, and the diff size off
    // the header; a reader must be given the same three facts.
    const js = mainJsSource();
    assert.match(
      js,
      /container\.setAttribute\(\s*'role',\s*'group'\s*\)/,
      'the edit proposal container needs role="group"'
    );
    assert.match(
      js,
      /Proposed edit \$\{index \+ 1\} of \$\{total\} in \$\{edit\.file_path\}/,
      "the edit proposal's aria-label must name which edit and which file"
    );
    assert.match(
      js,
      /line\(s\) removed, \$\{addedCount\} line\(s\) added/,
      "the edit proposal's aria-label must state the change size"
    );
    assert.match(
      js,
      /applyBtn\.setAttribute\(\s*'aria-label'/,
      '"Apply" alone is ambiguous with several proposals in scrollback -- name the file'
    );
    assert.match(js, /skipBtn\.setAttribute\(\s*'aria-label'/, 'the Skip button needs an accessible name');
    assert.match(
      js,
      /resultEl\.setAttribute\(\s*'role',\s*'status'\s*\)/,
      'the apply/refuse outcome must be announced, especially the gate-refusal reason'
    );
  });

  // THE TOOL-APPROVAL SURFACE, ASSERTED THE SAME WAY GATE 4 IS.
  //
  // Gate 4 was found unusable non-visually because it had been built for the eye
  // and given roles afterwards. Tool consent is the same kind of moment -- a
  // person authorizing something with real consequences, here an UNCONFINED
  // subprocess -- so it is pinned from the day it lands rather than audited into
  // shape later.
  //
  // Same honest scope as the rest of this file: these are structural assertions
  // over main.js's source, because VS Code exposes no webview DOM to the test
  // host. They prove the roles and labels are present and stay present; they do
  // not prove the experience is good.
  test('the tool-approval dialog is an alertdialog, not a passive notice', () => {
    const code = mainJsCode();
    assert.match(
      code,
      /setAttribute\('role', 'alertdialog'\)/,
      "the approval panel needs role=alertdialog: it is a question that has stopped everything, not a notification"
    );
    assert.match(code, /setAttribute\('aria-modal', 'true'\)/, 'the approval panel needs aria-modal');
    assert.match(
      code,
      /setAttribute\('aria-live', 'assertive'\)/,
      "a blocking security question is the one thing that SHOULD interrupt; polite would let it pass unannounced"
    );
    assert.match(
      code,
      /setAttribute\(\s*'aria-describedby'/,
      'the buttons must be described by the arguments and the confinement warning, not announced as bare verbs'
    );
  });

  test('the arguments are a labelled region, shown in full', () => {
    const code = mainJsCode();
    assert.match(
      code,
      /aria-label', 'Arguments this tool will receive'/,
      'the arguments region needs a name, or a reader encounters bare JSON with no context'
    );
    // A consent prompt that shows less than what will run is not consent, so the
    // whole argument string is written, never a slice or an ellipsis of it.
    assert.match(code, /args\.textContent = req\.arguments;/, 'the arguments must be rendered whole');
    assert.ok(
      !/req\.arguments\.slice|req\.arguments\.substring/.test(code),
      'the arguments are being truncated for display; the user would approve less than what runs'
    );
  });

  test('focus lands on Deny, which is the accessible form of [y/N]', () => {
    const code = mainJsCode();
    assert.match(
      code,
      /deny\.focus\(\)/,
      'initial focus must be the safe option: a reflex Enter must not authorise an unsandboxed subprocess'
    );
    // Tab order should reach the safe option before the permissive one.
    const denyAppend = code.indexOf('actions.appendChild(deny)');
    const approveAppend = code.indexOf('actions.appendChild(approve)');
    assert.ok(denyAppend >= 0 && approveAppend >= 0, 'the approval actions are not both present');
    assert.ok(denyAppend < approveAppend, 'Deny must come before Run in DOM (and therefore tab) order');
  });

  test('every approval button has a name that says what it does', () => {
    const code = mainJsCode();
    assert.match(
      code,
      /btn\.setAttribute\('aria-label', label \+ ': ' \+ description\)/,
      'approval buttons need more than a one-word visible label to be announced usefully'
    );
  });

  test('the unconfined warning is present and is not softened', () => {
    const code = mainJsCode();
    assert.match(
      code,
      /NOT SANDBOXED/,
      'a third-party tool must be described as unconfined; it is an ordinary subprocess with the user full access'
    );
    // And it must not be shown for a confined built-in -- an alarm on everything
    // is an alarm on nothing.
    assert.match(code, /if \(req\.confined\)/, 'the lane message must branch on confined');
  });

  test('the decision and the tool narration are announced as status', () => {
    const code = mainJsCode();
    const statusRoles = code.match(/setAttribute\('role', 'status'\)/g) || [];
    assert.ok(
      statusRoles.length >= 2,
      'the approval record and the tool-activity line both need role=status, so a reader learns ' +
        `what was decided and what ran; found ${statusRoles.length}`
    );
  });

  test('the prompt input holds focus so a follow-up needs no navigation', () => {
    const js = mainJsSource();
    const focusCalls = js.match(/inputEl\.focus\(\)/g) ?? [];
    assert.ok(
      focusCalls.length >= 2,
      'expected focus() on initial load AND after send; clicking Send otherwise leaves focus ' +
        `on the button (found ${focusCalls.length})`
    );
  });

  test('the XSS posture is unchanged -- these additions are attributes only', () => {
    // The Phase 3 review verified zero dangerous sinks behind a nonce CSP. This
    // accessibility work must not have introduced one, so it is re-asserted here
    // rather than trusted: a `role=` added via innerHTML would be a regression
    // that every other assertion in this file would happily pass.
    const code = mainJsCode();
    for (const sink of ['innerHTML', 'outerHTML', 'insertAdjacentHTML', 'document.write', 'eval(', 'new Function']) {
      assert.ok(
        !code.includes(sink),
        `media/main.js must not use ${sink} -- every renderer writes through textContent`
      );
    }
    // And the accessibility attributes themselves must be set as ATTRIBUTES,
    // never assembled into markup.
    assert.ok(
      /setAttribute\(/.test(code),
      'the accessibility work should be additive setAttribute calls; if this fails the ' +
        'implementation approach changed and the sink check above needs re-reading'
    );
  });
});
