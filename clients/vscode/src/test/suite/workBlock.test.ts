import * as assert from 'assert';
import * as fs from 'fs';
import * as path from 'path';
import * as vm from 'vm';

// THE WORK BLOCK (Stage 8), run for real.
//
// The first Stage 7 walkthrough showed a turn's tools as a stack of italic
// lines -- "builtin__repo_map — 67 bytes to the model in 0ms",
// "builtin__list_directory failed" -- that said nothing about WHY a call
// failed. main.js now folds a turn's calls into one line that opens to a row
// per call. This runs that section of main.js against a small fake DOM, the
// same way markdownRender.test.ts runs the renderer.

const MAIN_JS = path.join(__dirname, '..', '..', '..', 'media', 'main.js');

class El {
  children: El[] = [];
  parentElement: El | null = null;
  attrs: Record<string, string> = {};
  textContent = '';
  title = '';
  open = false;
  className = '';
  constructor(readonly tag: string) {}
  get classList() {
    const self = this;
    return {
      toggle(c: string, on: boolean) {
        const set = new Set(self.className.split(' ').filter(Boolean));
        if (on) set.add(c);
        else set.delete(c);
        self.className = [...set].join(' ');
      },
      contains: (c: string) => self.className.split(' ').includes(c),
    };
  }
  get lastElementChild(): El | null {
    return this.children[this.children.length - 1] ?? null;
  }
  setAttribute(k: string, v: string) {
    this.attrs[k] = v;
  }
  getAttribute(k: string) {
    return this.attrs[k] ?? null;
  }
  appendChild(c: El) {
    c.parentElement = this;
    this.children.push(c);
    return c;
  }
  append(...cs: El[]) {
    cs.forEach((c) => this.appendChild(c));
  }
  private all(): El[] {
    return this.children.flatMap((c) => [c, ...c.all()]);
  }
  querySelectorAll(sel: string) {
    const cls = sel.replace(/^\./, '');
    return this.all().filter((e) => e.className.split(' ').includes(cls));
  }
  querySelector(sel: string) {
    return this.querySelectorAll(sel)[0] ?? null;
  }
  closest(sel: string): El | null {
    const cls = sel.replace(/^\./, '');
    let e: El | null = this;
    while (e) {
      if (e.className.split(' ').includes(cls)) return e;
      e = e.parentElement;
    }
    return null;
  }
}

function load() {
  const src = fs.readFileSync(MAIN_JS, 'utf8');
  const start = src.indexOf('  // THE WORK BLOCK (Stage 8)');
  const end = src.indexOf('  // showEditSummary renders the end-of-run outcome');
  assert.ok(start > 0 && end > start, 'the work-block section markers moved; update this test');
  const transcriptEl = new El('div');
  const sandbox: Record<string, unknown> = {
    document: { createElement: (t: string) => new El(t) },
    transcriptEl,
    toolActivityEls: new Map(),
  };
  vm.runInNewContext(
    src.slice(start, end) +
      '\n;globalThis.__show = showToolActivity; globalThis.__setMode = (m) => { toolActivityMode = m; };' +
      'globalThis.__split = splitTurnNotes;',
    sandbox,
  );
  return {
    transcriptEl,
    show: sandbox.__show as (a: Record<string, unknown>) => void,
    setMode: sandbox.__setMode as (m: string) => void,
    split: sandbox.__split as (t: string) => { body: string; tools: string[]; cutOff: string },
  };
}

const summary = (block: El) => block.querySelector('.work-label')!.textContent;
const rows = (block: El) => block.querySelectorAll('.work-row');

suite('the work block', () => {
  test("a turn's calls fold into one block, and a running row is rewritten in place", () => {
    const { transcriptEl, show } = load();
    show({ call_id: 'a', server: 'builtin', tool: 'repo_map', phase: 'running' });
    assert.strictEqual(transcriptEl.children.length, 1);
    const block = transcriptEl.children[0];
    assert.strictEqual(block.tag, 'details');
    assert.strictEqual(summary(block), 'Working… 1 step');

    show({ call_id: 'a', server: 'builtin', tool: 'repo_map', phase: 'succeeded', result_bytes: 67, duration_ms: 3 });
    show({ call_id: 'b', server: 'builtin', tool: 'list_directory', phase: 'running' });
    show({ call_id: 'b', server: 'builtin', tool: 'list_directory', phase: 'failed', detail: 'cannot list new_file: no such folder' });

    assert.strictEqual(transcriptEl.children.length, 1, 'every call of the turn belongs in the one block');
    assert.strictEqual(rows(block).length, 2, 'running and its outcome are one row, not two');
    assert.strictEqual(summary(block), 'Used 2 tools · 1 failed');
    assert.ok(block.classList.contains('has-failure'));
  });

  test('a row names what the call did, and a failure says why', () => {
    const { transcriptEl, show } = load();
    show({ call_id: 'a', server: 'builtin', tool: 'repo_map', phase: 'succeeded', result_bytes: 67, duration_ms: 3 });
    show({ call_id: 'b', server: 'builtin', tool: 'list_directory', phase: 'failed', detail: 'no such folder' });
    show({ call_id: 'c', server: 'github', tool: 'list_prs', phase: 'denied', detail: 'you denied it' });
    const [ok, fail, denied] = rows(transcriptEl.children[0]);

    assert.strictEqual(ok.querySelector('.work-name')!.textContent, 'Mapped the project');
    assert.strictEqual(ok.title, '67 bytes to the model in 3ms', 'the figures move to the tooltip');
    assert.strictEqual(fail.querySelector('.work-name')!.textContent, 'Listed a folder');
    assert.strictEqual(fail.querySelector('.work-why')!.textContent, 'no such folder');
    assert.strictEqual(fail.getAttribute('aria-label'), 'Listed a folder: failed, no such folder');
    assert.strictEqual(denied.querySelector('.work-name')!.textContent, 'github · list_prs', 'a third-party tool keeps its own name');
  });

  test('something said after the block starts a new one, so the transcript stays in order', () => {
    const { transcriptEl, show } = load();
    show({ call_id: 'a', server: 'builtin', tool: 'read_file', phase: 'succeeded' });
    transcriptEl.appendChild(new El('div'));
    show({ call_id: 'b', server: 'builtin', tool: 'read_file', phase: 'succeeded' });
    assert.strictEqual(transcriptEl.children.length, 3);
    assert.strictEqual(transcriptEl.children[2].tag, 'details');
  });

  test('requested and approved are not drawn; a specialist step is', () => {
    const { transcriptEl, show } = load();
    show({ call_id: 'a', server: 'builtin', tool: 'read_file', phase: 'requested' });
    show({ call_id: 'a', server: 'builtin', tool: 'read_file', phase: 'approved' });
    assert.strictEqual(transcriptEl.children.length, 0);
    show({ phase: 'step', tool: 'Planner', detail: 'step 1/3' });
    assert.strictEqual(summary(transcriptEl.children[0]), 'Ran 1 step');
    assert.strictEqual(rows(transcriptEl.children[0])[0].querySelector('.work-name')!.textContent, 'Planner — step 1/3');
  });

  test('mochiii.toolActivity decides whether a block starts open', () => {
    const a = load();
    a.show({ call_id: 'x', server: 'builtin', tool: 'read_file', phase: 'running' });
    assert.strictEqual(a.transcriptEl.children[0].open, false, 'compact is the default');
    const b = load();
    b.setMode('expanded');
    b.show({ call_id: 'x', server: 'builtin', tool: 'read_file', phase: 'running' });
    assert.strictEqual(b.transcriptEl.children[0].open, true);
  });
});

// A REOPENED ANSWER'S NOTES (2026-10-14). The daemon saves each answer with
// "[used N tool call(s): builtin__repo_map, ...]" and, for a cut-off one,
// "[This answer was cut off: ...]". Reopened from history they were printed as
// reply text, and markdown read "__repo_map, builtin__" as bold, which is how a
// user saw "builtinrepo_map".
suite("a saved answer's notes", () => {
  test('are taken off the text and named the way the work block names them', () => {
    const { split } = load();
    const got = split(
      'I looked.\n\n[used 3 tool call(s): builtin__repo_map, builtin__read_file, builtin__read_file]' +
        '\n\n[This answer was cut off: the model provider failed partway through the turn. The work above is real but unfinished.]',
    );
    assert.strictEqual(got.body, 'I looked.');
    assert.deepStrictEqual([...got.tools], ['Mapped the project', 'Read a file']);
    assert.strictEqual(got.cutOff, 'This answer was cut off: the model provider failed partway through the turn.');
  });

  test("a third-party tool keeps its server's name, and an answer with no notes is left alone", () => {
    const { split } = load();
    assert.deepStrictEqual([...split('ok\n\n[used 1 tool call(s): perplexity__search]').tools], ['perplexity · search']);
    const plain = split('An answer that mentions [used 2 tool call(s): x] in the middle, then goes on.');
    assert.strictEqual(plain.body, 'An answer that mentions [used 2 tool call(s): x] in the middle, then goes on.');
    assert.deepStrictEqual([...plain.tools], []);
  });

  test('a turn the user stopped is said as stopped', () => {
    const { split } = load();
    const got = split('Half\n\n[This turn was stopped by the user before it finished. The work above is real, but they chose to stop it -- do not simply resume it unless they ask.]');
    assert.strictEqual(got.body, 'Half');
    assert.strictEqual(got.cutOff, 'This turn was stopped by the user before it finished.');
  });
});
