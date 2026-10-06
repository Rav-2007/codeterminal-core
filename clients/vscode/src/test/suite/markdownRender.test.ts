// The webview's markdown renderer, run for real.
//
// media/main.js is one browser script with no module system, so the renderer
// cannot be imported. Instead this cuts the "MARKDOWN, SAFELY" section out of
// the file and runs it in a VM against a minimal fake document -- the same
// code that ships, not a copy of it. Nothing here imports `vscode`, so it runs
// under plain mocha: npx mocha --ui tdd out/test/suite/markdownRender.test.js

import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';
import * as vm from 'vm';

const MAIN_JS = path.resolve(__dirname, '..', '..', '..', 'media', 'main.js');
const CHAT_PANEL_TS = path.resolve(__dirname, '..', '..', '..', 'src', 'chatPanel.ts');

class FakeNode {
  children: FakeNode[] = [];
  attrs: Record<string, string> = {};
  text = '';
  className = '';
  href = '';
  title = '';
  target = '';
  rel = '';
  constructor(public tag: string) {}
  appendChild(c: FakeNode): FakeNode {
    this.children.push(c);
    return c;
  }
  setAttribute(k: string, v: string): void {
    this.attrs[k] = v;
  }
  set textContent(v: string) {
    this.children = [];
    this.text = v;
  }
  get textContent(): string {
    return this.text + this.children.map((c) => c.textContent).join('');
  }
}

// Compact HTML-ish serialisation, enough to assert structure.
function html(n: FakeNode): string {
  if (n.tag === '#text') {
    return n.text;
  }
  const attrs = (n.tag === 'a' ? ` href="${n.href}"` : '') + (n.attrs.start ? ` start="${n.attrs.start}"` : '');
  const inner = n.text + n.children.map(html).join('');
  return n.tag === 'br' || n.tag === 'hr' ? `<${n.tag}>` : `<${n.tag}${attrs}>${inner}</${n.tag}>`;
}

function loadRenderer(): (container: FakeNode, text: string) => void {
  const src = fs.readFileSync(MAIN_JS, 'utf8');
  const start = src.indexOf('  // MARKDOWN, SAFELY.');
  const end = src.indexOf('  function renderFencedContent(container, text) {');
  assert.ok(start > 0 && end > start, 'the markdown section markers moved; update this test');
  const document = {
    createElement: (tag: string) => new FakeNode(tag),
    createTextNode: (s: string) => {
      const n = new FakeNode('#text');
      n.text = s;
      return n;
    },
  };
  const sandbox: Record<string, unknown> = { document };
  vm.runInNewContext(src.slice(start, end) + '\n;globalThis.__render = renderMarkdown;', sandbox);
  return sandbox.__render as (container: FakeNode, text: string) => void;
}

const render = loadRenderer();
function md(text: string): string {
  const c = new FakeNode('div');
  render(c, text);
  // renderMarkdown wraps everything in <div class="md">; return what is inside.
  return c.children[0].children.map(html).join('');
}

suite('markdown rendering in the chat panel', () => {
  test('headings, bold and italics are rendered, not shown as symbols', () => {
    assert.strictEqual(md('### 2. Strengths'), '<h3>2. Strengths</h3>');
    assert.strictEqual(md('**Inclusivity:** features for *blind* users'), '<p><strong>Inclusivity:</strong> features for <em>blind</em> users</p>');
    assert.strictEqual(md('# Big'), '<h1>Big</h1>');
    assert.strictEqual(md('###### small'), '<h4>small</h4>', 'deep headings are capped at h4');
  });

  test('snake_case and arithmetic are not mistaken for emphasis', () => {
    assert.strictEqual(md('call read_file_now and 2 * 3 * 4'), '<p>call read_file_now and 2 * 3 * 4</p>');
  });

  test('bullet and numbered lists, nested, with continuation lines', () => {
    assert.strictEqual(
      md('*   **Problem:** slow\n    *   *Detail:* GPS\n*   Next item'),
      '<ul><li><strong>Problem:</strong> slow<ul><li><em>Detail:</em> GPS</li></ul></li><li>Next item</li></ul>',
    );
    assert.strictEqual(md('1.  **Fill Slide 6**\n2.  Clarify'), '<ol><li><strong>Fill Slide 6</strong></li><li>Clarify</li></ol>');
    assert.strictEqual(md('3. three\n4. four'), '<ol start="3"><li>three</li><li>four</li></ol>');
    assert.strictEqual(md('- [ ] todo\n- [x] done'), '<ul><li>☐ todo</li><li>☑ done</li></ul>');
  });

  test('a paragraph directly followed by a list becomes both', () => {
    assert.strictEqual(md('To make it stronger:\n1. Fill it'), '<p>To make it stronger:</p><ol><li>Fill it</li></ol>');
  });

  test('inline LaTeX arrows become symbols; plain dollar amounts are untouched', () => {
    assert.strictEqual(md('Team $\\rightarrow$ Problem $\\rightarrow$ Solution'), '<p>Team → Problem → Solution</p>');
    assert.strictEqual(md('costs $5 and $10'), '<p>costs $5 and $10</p>');
    assert.strictEqual(md('odd $\\frac{a}{b}$ stays'), '<p>odd $\\frac{a}{b}$ stays</p>', 'an unknown command is left as written');
  });

  test('inline code is literal: no emphasis or links inside it', () => {
    assert.strictEqual(md('use `**not bold** http://x.y`'), '<p>use <code>**not bold** http://x.y</code></p>');
  });

  test('links are made only from http(s) URLs', () => {
    assert.strictEqual(md('[docs](https://example.com/a)'), '<p><a href="https://example.com/a">docs</a></p>');
    assert.strictEqual(md('see https://example.com/x.'), '<p>see <a href="https://example.com/x">https://example.com/x</a>.</p>');
    assert.strictEqual(md('[bad](javascript:alert(1))'), '<p>[bad](javascript:alert(1))</p>', 'a javascript: link must stay text');
    // A link's text is never linked again (this recursed until the stack overflowed).
    assert.strictEqual(md('[https://a.b/c](https://a.b/c)'), '<p><a href="https://a.b/c">https://a.b/c</a></p>');
    assert.strictEqual(md('[**see https://a.b**](https://a.b)'), '<p><a href="https://a.b"><strong>see https://a.b</strong></a></p>');
  });

  test('model output cannot become markup', () => {
    // The dangerous text is a TEXT node, never an element.
    const c = new FakeNode('div');
    render(c, '<img src=x onerror=alert(1)>');
    const p = c.children[0].children[0];
    assert.strictEqual(p.tag, 'p');
    assert.strictEqual(p.children.length, 1);
    assert.strictEqual(p.children[0].tag, '#text');
    assert.strictEqual(p.children[0].text, '<img src=x onerror=alert(1)>');
    // And the renderer never reaches for innerHTML at all.
    const src = fs.readFileSync(MAIN_JS, 'utf8');
    const section = src.slice(src.indexOf('  // MARKDOWN, SAFELY.'), src.indexOf('  function renderFencedContent(container, text) {'));
    assert.ok(!/innerHTML|outerHTML|insertAdjacentHTML/.test(section.replace(/^\s*\/\/.*$/gm, '')), 'the renderer must not use innerHTML');
  });

  test('tables, quotes and rules', () => {
    assert.strictEqual(
      md('| Region | Revenue |\n|---|---:|\n| North | **1200** |'),
      '<div><table><thead><tr><th>Region</th><th>Revenue</th></tr></thead><tbody><tr><td>North</td><td><strong>1200</strong></td></tr></tbody></table></div>',
    );
    assert.strictEqual(md('> note **this**'), '<blockquote><p>note <strong>this</strong></p></blockquote>');
    assert.strictEqual(md('above\n\n---\n\nbelow'), '<p>above</p><hr><p>below</p>');
  });

  test('the answer from the screenshot renders without raw markdown symbols', () => {
    const answer = [
      '### 2. Strengths of the Proposal',
      '*   **Strong Focus on Accessibility:** Most healthcare apps ignore it.',
      '*   **Clear Problem-Solution Fit:**',
      '    *   *Problem:* Fragmented systems & delayed emergencies.',
      '*   **Professional Structure:** Team $\\rightarrow$ Problem $\\rightarrow$ Solution.',
      '',
      '**My Rating:** The *idea* is strong (8/10).',
    ].join('\n');
    const c = new FakeNode('div');
    render(c, answer);
    const visible = c.textContent;
    assert.ok(!/###|\*\*|\$\\rightarrow\$/.test(visible), `raw markdown leaked into the visible text: ${visible}`);
    assert.ok(visible.includes('Team → Problem → Solution'));
  });
});

suite('composer controls say what they are', () => {
  test('the effort control is a labelled button whose accessible name carries the level', () => {
    const markup = fs.readFileSync(CHAT_PANEL_TS, 'utf8');
    const btn = /<button type="button" class="pill effort-pill" id="effortBtn"[\s\S]*?>/.exec(markup);
    assert.ok(btn, 'no #effortBtn button in the composer');
    assert.match(btn[0], /aria-label="Thinking effort: Auto/);
    assert.ok(!/id="effortMeter"/.test(markup), 'the old unlabelled dot meter is still in the markup');
    const js = fs.readFileSync(MAIN_JS, 'utf8');
    assert.match(js, /effortBtn\.setAttribute\(\s*'aria-label',[^;]*effortName/, 'renderEffort must fold the level into the accessible name');
    assert.match(js, /type: 'setEffort'/, 'the chosen effort must be sent to the extension host');
  });

  test('the model chip opens a dropdown instead of posting /model into the chat', () => {
    const markup = fs.readFileSync(CHAT_PANEL_TS, 'utf8');
    const chip = /<button type="button" class="pill" id="modelChip"[\s\S]*?>/.exec(markup);
    assert.ok(chip, 'no #modelChip button');
    assert.match(chip[0], /aria-haspopup="listbox"/);
    assert.match(chip[0], /aria-expanded="false"/);
    assert.match(markup, /<div id="modelPopup"[^>]*role="dialog"/);
    assert.match(markup, /id="modelList" role="listbox"/);
    const js = fs.readFileSync(MAIN_JS, 'utf8');
    assert.ok(!/queueCommand\('\/model'\)/.test(js), "the chip still sends '/model' as a chat message");
    assert.match(js, /modelChipEl\.setAttribute\('aria-expanded'/, 'the dropdown state must be mirrored into aria-expanded');
    assert.match(js, /type: 'listModels'/);
    assert.match(js, /type: 'selectModel'/);
  });

  test('a dropdown switch leaves a neutral line in the chat, not a red error', () => {
    const panel = fs.readFileSync(CHAT_PANEL_TS, 'utf8');
    assert.match(panel, /Model switched to/);
    assert.match(panel, /type: err \? 'notice' : 'info'/);
    const js = fs.readFileSync(MAIN_JS, 'utf8');
    assert.match(js, /case 'info': \{[\s\S]*?className = 'msg-info'/);
  });

  test('command output is shown as written, not rendered as markdown', () => {
    // "* qwen/..." marks the current model in /model; as markdown it became a bullet.
    const panel = fs.readFileSync(CHAT_PANEL_TS, 'utf8');
    assert.match(panel, /type: 'token', text: reply, plain: true/);
    const js = fs.readFileSync(MAIN_JS, 'utf8');
    assert.match(js, /if \(!currentAssistantPlain\) \{\s*renderFencedContent/);
  });

  test('the hidden attribute wins over component display rules', () => {
    const css = fs.readFileSync(CHAT_PANEL_TS, 'utf8');
    assert.match(css, /\[hidden\] \{ display: none !important; \}/, 'without it, an empty spec chip shows as a blank pill');
  });
});
