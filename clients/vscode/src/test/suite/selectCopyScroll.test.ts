import * as assert from 'assert';
import * as vscode from 'vscode';

import { ChatPanel } from '../../chatPanel';
import { ChatDevTools } from '../devtools';

const EXT_ID = 'MochiiiAIAgent.mochiii-vscode';

// SELECT, COPY, PASTE AND SCROLL IN THE CHAT, WITH A REAL MOUSE AND KEYBOARD.
//
// Every press, drag, wheel turn and chord below is a DevTools input event sent
// to the workbench, as a hand sends it; the clipboard is read back through
// vscode.env.clipboard, the system's own. Answers are posted to the webview the
// way the host posts them (history, turnStarted, token, done), so a streaming
// answer streams at a pace the test controls, with no model.
//
// Found 2026-11-24 before the fixes, on 26ef54d:
//   - every token re-set the streaming answer's whole text, which threw away a
//     selection inside it, and scrolled the chat to the bottom, which yanked a
//     reader who had scrolled up and fought a drag-select;
//   - a selection across a code block or two answers carried the UI's words
//     ("python", "Copy", "Used 2 passages ...") into the clipboard;
//   - nothing said how big a paste was, and a pasted image was dropped.

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

const ANSWER_A = 'Alpha paragraph one has some words.\n\nAlpha paragraph two ends here.';
const QUESTION_B = 'And the code?';
const CODE = 'def add(a, b):\n    return a + b\n\nprint(add(2, 3))';
const ANSWER_B = 'Bravo starts here and continues.\n\n```python\n' + CODE + '\n```\n\nBravo ends after the code.';

function filler(n: number): { role: string; content: string }[] {
  const turns: { role: string; content: string }[] = [];
  for (let i = 1; i <= n; i++) {
    turns.push({ role: 'user', content: `Filler question ${i}` });
    turns.push({ role: 'assistant', content: `Filler answer ${i}: ` + 'words that fill the line '.repeat(6) + `\n\nSecond paragraph of filler ${i}.` });
  }
  return turns;
}

/** Newlines and spaces as one, for text whose blank lines are the browser's to choose. */
const squash = (s: string) => s.replace(/\s+/g, ' ').trim();

suite('E2E chat — select, copy, paste and scroll with a real mouse and keyboard', () => {
  let dt: ChatDevTools;
  let post: (msg: Record<string, unknown>) => Thenable<boolean>;
  let panel: any;
  let realHandle: unknown;
  const sent: { type?: string; text?: string }[] = [];

  suiteSetup(async function () {
    this.timeout(90000);
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
    await dt.setViewport(1200, 800);
    // Record, do not run: a typed prompt is seen being SENT and goes nowhere.
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
      await dt.clearViewport().catch(() => undefined);
      dt.close();
    }
  });

  // ---- the page, read the way a user's eyes and hands find it ----

  /** Where the needle's first (edge 'start') or last (edge 'end') character is, in the chat's coordinates. */
  async function at(needle: string, edge: 'start' | 'end', nth = 0): Promise<{ x: number; y: number }> {
    const p = await dt.chatEval<[number, number] | null>(`(() => {
      const w2 = d.createTreeWalker(d.getElementById('transcript'), NodeFilter.SHOW_TEXT);
      let n, seen = 0;
      while ((n = w2.nextNode())) {
        const i = n.data.indexOf(${JSON.stringify(needle)});
        if (i < 0) continue;
        if (seen++ < ${nth}) continue;
        const r = d.createRange();
        const k = ${JSON.stringify(edge)} === 'start' ? i : i + ${needle.length} - 1;
        r.setStart(n, k); r.setEnd(n, k + 1);
        const b = r.getBoundingClientRect();
        return [${JSON.stringify(edge)} === 'start' ? b.left + 1 : b.right - 1, b.top + b.height / 2];
      }
      return null; })()`);
    assert.ok(p, `"${needle}" is not in the chat`);
    return { x: p[0], y: p[1] };
  }

  /** Scrolls the chat so the needle is in the middle of it, as a reader scrolls to what they want. */
  async function reveal(needle: string): Promise<void> {
    await dt.chatEval(`(() => {
      const w2 = d.createTreeWalker(d.getElementById('transcript'), NodeFilter.SHOW_TEXT);
      let n;
      while ((n = w2.nextNode())) {
        if (n.data.includes(${JSON.stringify(needle)})) { n.parentElement.scrollIntoView({ block: 'center' }); return true; }
      }
      return false; })()`);
    await sleep(250);
  }

  const selection = () => dt.chatEval<string>('w.getSelection().toString()');
  const scroll = () =>
    dt.chatEval<{ top: number; max: number; height: number }>(
      "(() => { const t = d.getElementById('transcript'); return { top: t.scrollTop, max: t.scrollHeight - t.clientHeight, height: t.clientHeight }; })()",
    );
  const box = (id: string) =>
    dt.chatEval<{ top: number; bottom: number; left: number; right: number }>(
      `(() => { const r = d.getElementById(${JSON.stringify(id)}).getBoundingClientRect(); return { top: r.top, bottom: r.bottom, left: r.left, right: r.right }; })()`,
    );
  const composer = () => dt.chatEval<string>("d.getElementById('promptInput').value");
  const clip = () => vscode.env.clipboard.readText();

  async function select(fromNeedle: string, toNeedle: string): Promise<void> {
    const a = await at(fromNeedle, 'start');
    const b = await at(toNeedle, 'end');
    await dt.mouseDown(a.x, a.y);
    await dt.mouseDragTo(b.x, b.y, 10, a);
    await dt.mouseUp(b.x, b.y);
  }

  async function copyWithKeys(): Promise<string> {
    await vscode.env.clipboard.writeText('(nothing copied)');
    await dt.chord('c', { ctrl: true });
    await sleep(300);
    return clip();
  }

  /** A chat with filler above two known answers, scrolled to the bottom, nothing streaming. */
  async function freshChat(fillerTurns = 0): Promise<void> {
    await post({ type: 'done' });
    await post({ type: 'clearTranscript' });
    await post({
      type: 'history',
      turns: [
        ...filler(fillerTurns),
        { role: 'user', content: 'First question' },
        { role: 'assistant', content: ANSWER_A },
        { role: 'user', content: QUESTION_B },
        { role: 'assistant', content: ANSWER_B },
      ],
    });
    await sleep(300);
    await dt.chatEval("(() => { const t = d.getElementById('transcript'); t.scrollTop = t.scrollHeight; w.getSelection().removeAllRanges(); return true; })()");
    await sleep(150);
  }

  /** A streaming answer of `lines` lines, `ms` apart, on its way while `during` runs. */
  async function streaming(lines: number, ms: number, during: () => Promise<void>): Promise<void> {
    await post({ type: 'turnStarted' });
    let stop = false;
    const feed = (async () => {
      for (let i = 1; i <= lines && !stop; i++) {
        await post({ type: 'token', text: `Streamed line ${i} of the answer, long enough to read.\n\n` });
        await sleep(ms);
      }
    })();
    try {
      await during();
    } finally {
      stop = true;
      await feed;
      await post({ type: 'done' });
      await sleep(200);
    }
  }

  // ---- 1. select with the mouse, copy with the keys ----

  test('B1a: a selection inside an answer copies exactly that text', async function () {
    this.timeout(30000);
    await freshChat();
    await select('paragraph one', 'some words');
    assert.strictEqual(await selection(), 'paragraph one has some words');
    assert.strictEqual(await copyWithKeys(), 'paragraph one has some words');
  });

  test('B1b: a selection across two messages copies their text and none of the UI', async function () {
    this.timeout(30000);
    await freshChat();
    await select('two ends here', 'Bravo starts');
    const got = await copyWithKeys();
    assert.strictEqual(squash(got), squash('two ends here.\n\n' + QUESTION_B + '\n\nBravo starts'), `copied ${JSON.stringify(got)}`);
  });

  test('B1c: a selection inside a code block copies the code, without line numbers', async function () {
    this.timeout(30000);
    await freshChat();
    await select('return a + b', 'print(add(2, 3))');
    const got = await copyWithKeys();
    assert.strictEqual(got, 'return a + b\n\nprint(add(2, 3))', `copied ${JSON.stringify(got)}`);
  });

  test('B1d: a selection across a code block carries neither its language nor its Copy button', async function () {
    this.timeout(30000);
    await freshChat();
    await select('continues.', 'Bravo ends');
    const got = await copyWithKeys();
    assert.strictEqual(squash(got), squash('continues.\n' + CODE + '\nBravo ends'), `copied ${JSON.stringify(got)}`);
  });

  // ---- 3. the Copy buttons ----

  test('B3: the answer Copy and the code Copy copy the raw text', async function () {
    this.timeout(30000);
    await freshChat();
    await vscode.env.clipboard.writeText('(nothing copied)');
    const codeBtn = await dt.chatEval<[number, number]>(
      "(() => { const b = Array.from(d.querySelectorAll('.code-copy')).pop(); b.scrollIntoView({ block: 'center' }); const r = b.getBoundingClientRect(); return [r.left + r.width / 2, r.top + r.height / 2]; })()",
    );
    await dt.click(codeBtn[0], codeBtn[1]);
    await sleep(400);
    assert.strictEqual(await clip(), CODE, 'the code Copy');
    const answerBtn = await dt.chatEval<[number, number]>(
      "(() => { const b = Array.from(d.querySelectorAll('.msg-copy')).pop(); b.scrollIntoView({ block: 'center' }); const r = b.getBoundingClientRect(); return [r.left + r.width / 2, r.top + r.height / 2]; })()",
    );
    await dt.click(answerBtn[0], answerBtn[1]);
    await sleep(400);
    assert.strictEqual(await clip(), ANSWER_B, 'the answer Copy');
  });

  // ---- 2. right-click ----

  async function contextMenuItems(): Promise<{ label: string; x: number; y: number }[]> {
    return dt.pageEval(`Array.from(document.querySelectorAll('.context-view .monaco-menu .action-item:not(.disabled) .action-label'))
      .filter((e) => e.getClientRects().length)
      .map((e) => { const r = e.getBoundingClientRect(); return { label: (e.getAttribute('aria-label') || e.textContent || '').trim(), x: r.left + r.width / 2, y: r.top + r.height / 2 }; })`);
  }

  async function pickFromMenu(label: string): Promise<void> {
    const items = await contextMenuItems();
    const item = items.find((i) => i.label === label);
    assert.ok(item, `the context menu has no ${label}: ${JSON.stringify(items.map((i) => i.label))}`);
    await dt.clickPage(item.x, item.y);
    await sleep(300);
  }

  test('B2a: right-click on a selection in the chat offers Copy, and it copies', async function () {
    this.timeout(30000);
    await freshChat();
    await select('paragraph one', 'some words');
    await vscode.env.clipboard.writeText('(nothing copied)');
    const p = await at('some words', 'end');
    await dt.rightClick(p.x - 10, p.y);
    await pickFromMenu('Copy');
    assert.strictEqual(await clip(), 'paragraph one has some words');
  });

  test('B2b: right-click in the composer offers Cut, Copy and Paste, and each works', async function () {
    this.timeout(30000);
    await freshChat();
    const c = await box('promptInput');
    await dt.click(c.left + 20, c.top + 12);
    await dt.insertText('keep cut');
    // Selected with the keys, and right-clicked on the selected text, as a
    // hand does: a right-click outside a selection moves the caret instead.
    await dt.chord('a', { ctrl: true });
    await vscode.env.clipboard.writeText('(nothing copied)');
    await dt.rightClick(c.left + 20, c.top + 12);
    await pickFromMenu('Cut');
    assert.strictEqual(await clip(), 'keep cut', 'Cut put the selection on the clipboard');
    assert.strictEqual(await composer(), '', 'Cut took it out of the composer');
    await dt.rightClick(c.left + 20, c.top + 12);
    await pickFromMenu('Paste');
    assert.strictEqual(await composer(), 'keep cut', 'Paste put it back');
    const focus = await dt.chatEval<string>("(d.hasFocus() ? 'doc ' : 'no-doc ') + (d.activeElement ? d.activeElement.id || d.activeElement.tagName : 'none')");
    assert.strictEqual(focus, 'doc promptInput', 'after the menu\'s Paste the keyboard is not in the composer');
    await dt.chord('a', { ctrl: true });
    await vscode.env.clipboard.writeText('(nothing copied)');
    await dt.rightClick(c.left + 20, c.top + 12);
    await pickFromMenu('Copy');
    assert.strictEqual(await clip(), 'keep cut', 'Copy');
    await dt.chatEval("(() => { const c = d.getElementById('promptInput'); c.value = ''; c.dispatchEvent(new Event('input')); return true; })()");
  });

  // ---- 4. drag past the chat's edge ----

  /** Scroll and selection length at two moments of a held drag: early, and a second later. */
  async function holdAndSample(x: number, y: number): Promise<{ early: { top: number; sel: number }; late: { top: number; sel: number } }> {
    await dt.mouseHold(x, y, 500);
    const early = { top: (await scroll()).top, sel: (await selection()).length };
    await dt.mouseHold(x, y, 1000);
    const late = { top: (await scroll()).top, sel: (await selection()).length };
    return { early, late };
  }

  /**
   * The pointer back over the chat with no button held, after a release
   * outside it (where the chat never hears the mouseup): the drag is over, so
   * the chat must stop scrolling and keep the selection.
   */
  async function backAndStill(): Promise<void> {
    const t = await box('transcript');
    await dt.mouseMove((t.left + t.right) / 2, (t.top + t.bottom) / 2);
    await sleep(300);
    const a = (await scroll()).top;
    const kept = (await selection()).length;
    await sleep(700);
    const b = (await scroll()).top;
    assert.ok(Math.abs(a - b) < 2, `the chat kept scrolling after the drag ended (${a} -> ${b})`);
    assert.ok(kept > 0, 'the selection was lost when the drag ended');
  }

  test('B4a: dragging below the chat scrolls it down and the selection grows', async function () {
    this.timeout(40000);
    await freshChat(80);
    await dt.chatEval("(() => { d.getElementById('transcript').scrollTop = 0; return true; })()");
    await sleep(200);
    const t = await box('transcript');
    const start = await at('Filler answer 2:', 'start');
    await dt.mouseDown(start.x, start.y);
    await dt.mouseDragTo(start.x, t.bottom + 40, 10, start);
    const { early, late } = await holdAndSample(start.x, t.bottom + 40);
    await dt.mouseUp(start.x, t.bottom + 40);
    await backAndStill();
    assert.ok(early.top > 50, `the chat did not scroll down: ${JSON.stringify({ early, late })}`);
    assert.ok(late.top > early.top && late.sel > early.sel, `the drag stopped scrolling or the selection stopped growing: ${JSON.stringify({ early, late })}`);
  });

  test('B4b: dragging above the chat, outside the webview, scrolls it up and the selection grows', async function () {
    this.timeout(40000);
    await freshChat(80);
    const start = await at('Bravo ends', 'start');
    await dt.mouseDown(start.x, start.y);
    const before = (await scroll()).top;
    // Above the chat's header and above the webview itself: the editor's tab bar.
    await dt.mouseDragTo(start.x, -30, 10, start);
    const { early, late } = await holdAndSample(start.x, -30);
    await dt.mouseUp(start.x, -30);
    await backAndStill();
    assert.ok(before - early.top > 50, `the chat did not scroll up: ${JSON.stringify({ before, early, late })}`);
    assert.ok(late.top < early.top && late.sel > early.sel, `the drag stopped scrolling or the selection stopped growing: ${JSON.stringify({ early, late })}`);
  });

  test('B4c: dragging above the chat while an answer streams scrolls up, and stays up', async function () {
    this.timeout(60000);
    await freshChat(80);
    await streaming(80, 60, async () => {
      await sleep(800);
      await reveal('Streamed line 2 of');
      const start = await at('Streamed line 2 of', 'start');
      await dt.mouseDown(start.x, start.y);
      const before = (await scroll()).top;
      await dt.mouseDragTo(start.x, -30, 10, start);
      const { early, late } = await holdAndSample(start.x, -30);
      await dt.mouseUp(start.x, -30);
      await backAndStill();
      assert.ok(before - early.top > 50, `the chat did not scroll up while streaming: ${JSON.stringify({ before, early, late })}`);
      assert.ok(late.top < early.top && late.sel > early.sel, `the stream pulled the chat back down or the selection stopped: ${JSON.stringify({ early, late })}`);
    });
  });

  // ---- 5. while an answer streams ----

  test('B5a: new tokens do not clear a selection inside the streaming answer', async function () {
    this.timeout(60000);
    await freshChat();
    await streaming(40, 120, async () => {
      await sleep(700);
      await select('Streamed line 1', 'line 2 of');
      const chosen = await selection();
      assert.ok(chosen.includes('line 1') && chosen.includes('line 2'), `nothing was selected: ${JSON.stringify(chosen)}`);
      await sleep(1500); // a dozen more tokens
      assert.strictEqual(await selection(), chosen, 'a token cleared or changed the selection');
      assert.strictEqual(squash(await copyWithKeys()), squash(chosen));
    });
  });

  test('B5b: the chat stays where the user scrolled while tokens arrive, and follows again at the bottom', async function () {
    this.timeout(60000);
    await freshChat(25);
    await streaming(70, 80, async () => {
      await sleep(600);
      const t = await box('transcript');
      const mid = { x: (t.left + t.right) / 2, y: (t.top + t.bottom) / 2 };
      for (let i = 0; i < 6; i++) {
        await dt.wheel(mid.x, mid.y, -300);
      }
      const up = (await scroll()).top;
      await sleep(1500);
      const later = await scroll();
      assert.ok(Math.abs(later.top - up) < 2, `the chat was yanked from ${up} to ${later.top} (bottom is ${later.max})`);
      for (let i = 0; i < 30; i++) {
        await dt.wheel(mid.x, mid.y, 600);
      }
      await sleep(1500);
      const end = await scroll();
      assert.ok(end.max - end.top < 4, `back at the bottom, the chat did not follow the answer: ${JSON.stringify(end)}`);
    });
  });

  test('B5c: the chat does not move under a button held down to select', async function () {
    this.timeout(60000);
    await freshChat(25);
    await streaming(60, 80, async () => {
      await sleep(600);
      // Following the end, as a reader at the bottom is; the press lands on
      // whatever text is in the middle of the chat.
      const t = await box('transcript');
      const p = { x: (t.left + t.right) / 2, y: (t.top + t.bottom) / 2 };
      await dt.mouseDown(p.x, p.y);
      const before = (await scroll()).top;
      await dt.mouseHold(p.x, p.y, 1500);
      const after = (await scroll()).top;
      await dt.mouseUp(p.x, p.y);
      assert.ok(Math.abs(after - before) < 2, `the chat moved from ${before} to ${after} while the button was down`);
    });
  });

  // ---- 6. paste into the composer ----

  async function pasteIntoComposer(): Promise<void> {
    const c = await box('promptInput');
    await dt.click(c.left + 20, c.top + 12);
    await dt.chatEval("(() => { const c = d.getElementById('promptInput'); c.value = ''; c.dispatchEvent(new Event('input')); return true; })()");
    await dt.chord('v', { ctrl: true });
    await sleep(500);
  }

  test('B6a: plain and multi-line text paste as they are, and nothing is sent', async function () {
    this.timeout(30000);
    await freshChat();
    sent.length = 0;
    await vscode.env.clipboard.writeText('hello paste');
    await pasteIntoComposer();
    assert.strictEqual(await composer(), 'hello paste');
    const lines = 'first line\nsecond line\n\nfourth line';
    await vscode.env.clipboard.writeText(lines);
    await pasteIntoComposer();
    assert.strictEqual(await composer(), lines);
    assert.strictEqual(sent.length, 0, 'a paste sent a message');
  });

  test('B6b: 50 KB pastes whole, and the composer says how much it is', async function () {
    this.timeout(30000);
    await freshChat();
    sent.length = 0;
    const big = ('0123456789abcdef'.repeat(4) + '\n').repeat(788); // 51,220 characters
    await vscode.env.clipboard.writeText(big);
    await pasteIntoComposer();
    assert.strictEqual((await composer()).length, big.length, 'the paste was cut');
    const note = await dt.chatEval<string>("(() => { const n = d.getElementById('pasteNote'); return n && !n.hidden ? n.textContent : ''; })()");
    assert.match(note, /51,220 characters/, `no paste-size notice: ${JSON.stringify(note)}`);
    assert.strictEqual(sent.length, 0, 'a paste sent a message');
    await dt.chatEval("(() => { const c = d.getElementById('promptInput'); c.value = ''; c.dispatchEvent(new Event('input')); return true; })()");
    assert.strictEqual(await dt.chatEval<boolean>("d.getElementById('pasteNote').hidden"), true, 'the notice outlived the text it was about');
  });

  test('B6c: text copied from a web page pastes as plain text', async function () {
    this.timeout(30000);
    await freshChat();
    await dt.writeClipboard({ 'text/html': '<p><b>Bold</b> and <a href="https://example.com">a link</a></p>', 'text/plain': 'Bold and a link' });
    await pasteIntoComposer();
    assert.strictEqual(await composer(), 'Bold and a link');
  });

  test('B6d: a pasted image is attached, as one picked with + Image is', async function () {
    this.timeout(60000);
    await freshChat();
    // "PASTED 42" in black on white, 420x90: read by OCR on this machine, as
    // an image picked with + Image is.
    const png = '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';
    await dt.writeClipboard({ 'image/png': png });
    await pasteIntoComposer();
    const deadline = Date.now() + 40000;
    let state = { chips: [] as string[], errors: [] as string[], text: '' };
    while (Date.now() < deadline) {
      state = await dt.chatEval(`({
        chips: Array.from(d.querySelectorAll('#attachList .attach-chip')).map((e) => (e.textContent || '') + (e.classList.contains('pending') ? ' [pending]' : '')),
        errors: Array.from(d.querySelectorAll('.msg.error')).map((e) => e.textContent || ''),
        text: '' })`);
      if (state.errors.some((e) => /pasted-image/.test(e)) || state.chips.some((c) => /pasted-image/.test(c) && !/pending|reading/i.test(c))) break;
      await sleep(300);
    }
    assert.deepStrictEqual(state.errors.filter((e) => /pasted-image/.test(e)), [], 'the pasted image could not be read');
    assert.ok(state.chips.some((c) => /pasted-image-\d+\.png/.test(c)), `no attachment for the pasted image: ${JSON.stringify(state)}`);
    assert.strictEqual(await composer(), '', 'the image was pasted as text');
    await dt.chatEval("(() => { for (const b of Array.from(d.querySelectorAll('.attach-chip button'))) b.click(); return true; })()");
  });

  // ---- 7. the keyboard in the composer ----

  test('B7: ctrl+A selects only the composer, shift+arrows select, Esc keeps the text', async function () {
    this.timeout(30000);
    await freshChat();
    const c = await box('promptInput');
    await dt.click(c.left + 20, c.top + 12);
    await dt.chatEval("(() => { const c = d.getElementById('promptInput'); c.value = ''; c.dispatchEvent(new Event('input')); return true; })()");
    await dt.insertText('draft text');
    await dt.chord('a', { ctrl: true });
    const all = await dt.chatEval<{ s: number; e: number; doc: string }>(
      "(() => { const c = d.getElementById('promptInput'); return { s: c.selectionStart, e: c.selectionEnd, doc: w.getSelection().toString() }; })()",
    );
    assert.deepStrictEqual([all.s, all.e], [0, 10], 'ctrl+A did not select the composer text');
    assert.ok(!all.doc.includes('Alpha'), 'ctrl+A selected the chat too');
    await dt.chord('End');
    await dt.chord('ArrowLeft', { shift: true });
    await dt.chord('ArrowLeft', { shift: true });
    const two = await dt.chatEval<[number, number]>("(() => { const c = d.getElementById('promptInput'); return [c.selectionStart, c.selectionEnd]; })()");
    assert.deepStrictEqual(two, [8, 10], 'shift+left did not select');
    await dt.chord('Escape');
    assert.strictEqual(await composer(), 'draft text', 'Esc cleared the composer');
    await dt.chatEval("(() => { const c = d.getElementById('promptInput'); c.value = ''; c.dispatchEvent(new Event('input')); return true; })()");
  });
});
