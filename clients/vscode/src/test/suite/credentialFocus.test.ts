import * as assert from 'assert';
import * as vscode from 'vscode';

import { ChatPanel } from '../../chatPanel';
import { ChatDevTools } from '../devtools';

const EXT_ID = 'MochiiiAIAgent.mochiii-vscode';

// WHILE A FORM THAT CAN HOLD A KEY IS OPEN, NO KEYSTROKE REACHES THE COMPOSER.
//
// FOUND 2026-11-08 (UI audit): /mcp -> a server -> "Set key…" opened the key
// form UNDER the /mcp dialog. A click on the part of the key field that showed
// hit the dialog's backdrop, which closed the dialog and put the caret in the
// composer; the key typed next went into the chat prompt, one Enter from the
// model provider, while the key field stayed empty.
//
// This tests the invariant, not that click. Each way a key form can be open
// (the key step and the Edit and Add forms; over the /mcp dialog, over a page,
// over nothing) is met with every way the keyboard could be taken from it: a
// real click on every part of the chat outside the form, Tab and Shift+Tab
// round and round, a turn ending (refocusComposer), code calling focus() on
// the composer, and the dialog behind it closing. After each, a real key is
// pressed and text is pasted. The composer must not have taken focus or any
// key or input event, its draft must be unchanged, and the keyboard must
// still be in the form. Run at a normal width and at a narrow one.
//
// NEUTER CHECK (2026-11-08): with media/main.js, mcpPopup.js and pages.js as
// on main (the pop-up still above the dialog), all eight "no route" tests fail
// -- clicks on the composer, Shift+Tab, a turn ending, focus() and the dialog
// closing all put the typed text in the composer. With .mcp-popup back at
// z-index 60 (the focus half kept), both stacking tests fail: the key field,
// Name, Command and the buttons are drawn under the /mcp dialog.

const DRAFT = 'a question I was writing';
const DUMMY = 'DUMMY-NOT-A-KEY';

const KEY_OPEN = {
  type: 'mcpPopupKeyOpen',
  server: 'needskey',
  name: '',
  vars: [{ name: 'PROBE_API_TOKEN', stored: false, missing: true }],
};
const EDIT_OPEN = {
  type: 'mcpPopupOpen',
  mode: 'update',
  name: 'needskey',
  commandLine: 'python3 -I /opt/probe_mcp.py',
  env: [{ name: 'PROBE_API_TOKEN', stored: false }],
  cwd: '',
  url: '',
  transport: '',
  headers: [],
};
const ADD_OPEN = { type: 'mcpPopupOpen', mode: 'add' };

interface State {
  name: string;
  /** What is open behind the form: a command typed into the composer, or nothing. */
  behind: '' | '/mcp' | '/config';
  msg: Record<string, unknown>;
}

const STATES: State[] = [
  { name: 'the key form over the /mcp dialog', behind: '/mcp', msg: KEY_OPEN },
  { name: 'the Edit form over the /mcp dialog', behind: '/mcp', msg: EDIT_OPEN },
  { name: 'the Add form over the Settings page', behind: '/config', msg: ADD_OPEN },
  { name: 'the key form over the chat alone', behind: '', msg: KEY_OPEN },
];

const WIDTHS: [string, number][] = [
  ['a normal editor width', 1200],
  ['a narrow width', 460],
];

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

suite('E2E chat — while a key form is open, no keystroke reaches the composer', () => {
  let dt: ChatDevTools;
  let post: (msg: Record<string, unknown>) => Thenable<boolean>;

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
    assert.ok(panel, 'createOrShow left no panel');
    post = (msg) => panel.host.webview.postMessage(msg);
    dt = await ChatDevTools.connect();
    const deadline = Date.now() + 30000;
    while (!(await dt.chatEval<boolean>("!!d.getElementById('promptInput') && !!d.getElementById('mcpPopup')"))) {
      if (Date.now() > deadline) {
        throw new Error('the chat never loaded');
      }
      await sleep(250);
    }
    // Whatever reaches the composer while the form is open is recorded here,
    // on the window in the capture phase: before any listener of the
    // composer's own can stop it.
    await dt.chatEval(`(() => { if (w.__credProbe) return true; w.__credProbe = [];
      const c = d.getElementById('promptInput');
      for (const t of ['focus', 'keydown', 'keypress', 'beforeinput', 'input', 'paste', 'drop']) {
        w.addEventListener(t, (e) => { if (e.target === c && !d.getElementById('mcpPopup').hidden) w.__credProbe.push(t); }, true);
      }
      return true; })()`);
  });

  suiteTeardown(async () => {
    if (dt) {
      await post({ type: 'mcpPopupClose' });
      await dt.clearViewport().catch(() => undefined);
      dt.close();
    }
  });

  async function rect(sel: string): Promise<[number, number, number, number] | null> {
    return dt.chatEval(`(() => { const e = d.querySelector(${JSON.stringify(sel)}); if (!e || !e.getClientRects().length) return null;
      const r = e.getBoundingClientRect(); return [r.x, r.y, r.width, r.height]; })()`);
  }

  async function waitFor(what: string, cond: () => Promise<boolean>, ms = 5000): Promise<void> {
    const deadline = Date.now() + ms;
    while (!(await cond())) {
      if (Date.now() > deadline) {
        const why = await dt.chatEval(`({ composer: d.getElementById('promptInput').value, active: d.activeElement && (d.activeElement.id || d.activeElement.tagName),
          focused: d.hasFocus(), form: !d.getElementById('mcpPopup').hidden, dialog: !d.getElementById('mcpDlg').hidden })`);
        assert.fail(`${what}: chat ${JSON.stringify(why)}`);
      }
      await sleep(80);
    }
  }

  /** Nothing open: no form, no dialog, no page; the composer empty. */
  async function closeEverything(): Promise<void> {
    await post({ type: 'mcpPopupClose' });
    await waitFor('the form did not close', () => dt.chatEval<boolean>("d.getElementById('mcpPopup').hidden"));
    await dt.chatEval(`(() => {
      const x = d.getElementById('mcpDlgClose'); if (x && x.getClientRects().length) x.click();
      for (const b of Array.from(d.querySelectorAll('[data-close-page]'))) if (b.getClientRects().length) { b.click(); break; }
      d.getElementById('promptInput').value = '';
      return true; })()`);
    await waitFor('a dialog or page stayed open', () =>
      dt.chatEval<boolean>("d.getElementById('mcpDlg').hidden && !Array.from(d.querySelectorAll('.page')).some(p => !p.hidden)"),
    );
  }

  /** The keyboard in the composer, by a real click on it (VS Code's toasts cleared off it first). */
  async function focusComposer(): Promise<void> {
    const deadline = Date.now() + 8000;
    for (;;) {
      const c = await rect('#promptInput');
      assert.ok(c, 'the composer is not on screen');
      const x = c[0] + c[2] / 2;
      const y = c[1] + c[3] / 2;
      if (await dt.chatIsAt(x, y)) {
        await dt.click(x, y);
        for (let i = 0; i < 10; i++) {
          await sleep(100);
          if (await dt.chatEval<boolean>("d.hasFocus() && d.activeElement === d.getElementById('promptInput')")) {
            return;
          }
        }
      } else {
        // VS Code's own toasts ("Mochiii searches your code on-device…") cover
        // the composer at a narrow width; they are VS Code's, not the chat's.
        await vscode.commands.executeCommand('notifications.clearAll').then(undefined, () => undefined);
      }
      if (Date.now() > deadline) {
        const why = await dt.chatEval(`({ active: d.activeElement && (d.activeElement.id || d.activeElement.tagName), focused: d.hasFocus(),
          form: !d.getElementById('mcpPopup').hidden, inert: d.querySelectorAll('[inert]').length, composer: [${x}, ${y}] })`);
        const over = { workbenchFocus: await dt.pageEval('(document.activeElement && document.activeElement.className) || ""'), chatAtComposer: await dt.chatIsAt(x, y) };
        assert.fail('a click on the composer never gave it the keyboard: ' + JSON.stringify(why) + ' ' + JSON.stringify(over));
      }
      await sleep(250);
    }
  }

  /** Opens what the state has behind the form the way a user does, then the form as the host does. */
  async function open(state: State): Promise<void> {
    await closeEverything();
    await focusComposer();
    if (state.behind) {
      const sel = state.behind === '/mcp' ? '#mcpDlg' : '#pageSettings';
      const shown = () => dt.chatEval<boolean>(`!d.querySelector(${JSON.stringify(sel)}).hidden`);
      await dt.type(state.behind);
      await waitFor(`${state.behind} was not typed`, () => dt.chatEval<boolean>(`d.getElementById('promptInput').value === ${JSON.stringify(state.behind)}`));
      // The first Enter completes the command in the slash menu, the second runs it.
      for (let i = 0; i < 4 && !(await shown()); i++) {
        if ((await dt.chatEval<string>("d.getElementById('promptInput').value")) === '') {
          break;
        }
        await dt.press('Enter');
        await sleep(300);
      }
      await waitFor(`${state.behind} did not open ${sel}`, shown);
    }
    await dt.chatEval(`(d.getElementById('promptInput').value = ${JSON.stringify(DRAFT)}, true)`);
    await post(state.msg);
    await waitFor('the form did not open', () => dt.chatEval<boolean>("!d.getElementById('mcpPopup').hidden"));
    await sleep(200);
    await dt.chatEval('(w.__credProbe.length = 0, true)');
  }

  /** What the composer took, and where the keyboard is. */
  async function look(): Promise<{ composer: string; probe: string[]; inForm: boolean; formOpen: boolean; active: string }> {
    return dt.chatEval(`(() => { const p = d.getElementById('mcpPopup'); const a = d.activeElement;
      return { composer: d.getElementById('promptInput').value, probe: w.__credProbe.slice(), formOpen: !p.hidden,
        inForm: d.hasFocus() && !!a && p.contains(a), active: a ? (a.id ? '#' + a.id : a.tagName.toLowerCase()) : '' }; })()`);
  }

  for (const [widthName, width] of WIDTHS) {
    for (const state of STATES) {
      test(`${state.name}, at ${widthName}: no route gives the composer a keystroke`, async function () {
        this.timeout(120000);
        await dt.setViewport(width, 900);
        const leaks: string[] = [];

        // One route: do it, press a key, paste, and see where they went.
        const route = async (what: string, act: () => Promise<void>) => {
          await open(state);
          await act();
          await dt.type('k');
          await dt.insertText(DUMMY);
          const s = await look();
          if (!s.formOpen) {
            leaks.push(`${what}: the form closed`);
          }
          if (s.probe.length > 0 || s.composer !== DRAFT || !s.inForm) {
            leaks.push(`${what}: composer took [${s.probe.join(',')}], its value is ${JSON.stringify(s.composer)}, the keyboard is on ${s.active}${s.inForm ? '' : ' (not in the form)'}`);
          }
        };

        await route('nothing done', async () => undefined);

        // A real click on every part of the chat the form does not cover,
        // each followed by a key: a grid, so a route nobody thought of is hit.
        await open(state);
        const view = await dt.chatEval<[number, number]>('[d.documentElement.clientWidth, d.documentElement.clientHeight]');
        const form = await rect('#mcpPopup');
        assert.ok(form, 'the form is not on screen');
        for (let gx = 0; gx < 6; gx++) {
          for (let gy = 0; gy < 6; gy++) {
            const x = Math.round(view[0] * (gx + 0.5) / 6);
            const y = Math.round(view[1] * (gy + 0.5) / 6);
            if (x >= form[0] - 4 && x <= form[0] + form[2] + 4 && y >= form[1] - 4 && y <= form[1] + form[3] + 4) {
              continue;
            }
            if (!(await dt.chatIsAt(x, y))) {
              continue; // a VS Code toast over the chat, not the chat
            }
            if (await dt.chatEval<boolean>("d.getElementById('mcpPopup').hidden")) {
              await open(state);
            }
            await dt.click(x, y);
            await dt.type('z');
            const s = await look();
            if (s.probe.length > 0 || s.composer !== DRAFT || !s.inForm) {
              leaks.push(`a click at (${x}, ${y}): composer took [${s.probe.join(',')}], its value is ${JSON.stringify(s.composer)}, the keyboard is on ${s.active}`);
              await dt.chatEval(`(d.getElementById('promptInput').value = ${JSON.stringify(DRAFT)}, w.__credProbe.length = 0, true)`);
            }
          }
        }

        await route('Tab, twenty times', async () => {
          for (let i = 0; i < 20; i++) {
            await dt.press('Tab');
          }
        });
        await route('Shift+Tab, twenty times', async () => {
          for (let i = 0; i < 20; i++) {
            await dt.press('Tab', true);
          }
        });
        await route('a turn ending (the host posts done)', async () => {
          await post({ type: 'done' });
          await sleep(200);
        });
        await route('code calling focus() on the composer', async () => {
          await dt.chatEval("(d.getElementById('promptInput').focus(), true)");
        });
        await route('the /mcp dialog behind it being closed', async () => {
          await dt.chatEval("(() => { const x = d.getElementById('mcpDlgClose'); if (x && x.getClientRects().length) x.click(); return true; })()");
          await sleep(100);
        });

        await closeEverything();
        assert.deepStrictEqual(leaks, [], `the composer was reachable while ${state.name} was open:\n  ` + leaks.join('\n  '));
      });
    }

    test(`at ${widthName}, each key form is above what opened it, and its fields can be clicked and typed in`, async function () {
      this.timeout(60000);
      await dt.setViewport(width, 900);
      const problems: string[] = [];
      const fieldsOf: Record<string, string[]> = {
        mcpPopupKeyOpen: ['#mcpPopKeyValue'],
        mcpPopupOpen: ['#mcpPopName', '#mcpPopCommand'],
      };
      for (const state of STATES.slice(0, 3)) {
        await open(state);
        // Every control of the step on screen is the topmost thing DRAWN at its
        // own centre. Hit-testing skips inert elements, and the fix makes the
        // dialog behind inert, so inert is lifted for this one synchronous
        // measurement: what is measured is paint order, what the user sees.
        const covered = await dt.chatEval<string[]>(`(() => { const p = d.getElementById('mcpPopup'); const out = [];
          const inert = Array.from(d.querySelectorAll('[inert]')); inert.forEach((e) => e.removeAttribute('inert'));
          try {
            for (const e of Array.from(p.querySelectorAll('input, select, textarea, button'))) {
              if (!e.getClientRects().length) continue;
              e.scrollIntoView({ block: 'nearest' });
              const r = e.getBoundingClientRect(); const top = d.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
              if (!top || !(top === e || e.contains(top))) out.push((e.id || e.getAttribute('aria-label') || e.textContent.trim()) + ' under ' + (top ? (top.id || top.className || top.tagName) : 'nothing'));
            }
          } finally { inert.forEach((e) => e.setAttribute('inert', '')); }
          return out; })()`);
        for (const c of covered) {
          problems.push(`${state.name}: ${c}`);
        }
        // The fields a user needs: a real click in each, then a real key.
        for (const sel of fieldsOf[String(state.msg.type)]) {
          const r = await rect(sel);
          if (!r) {
            problems.push(`${state.name}: ${sel} is not on screen`);
            continue;
          }
          const vis = await dt.chatEval<[number, number]>('[d.documentElement.clientWidth, d.documentElement.clientHeight]');
          if (r[0] < 0 || r[1] < 0 || r[0] + r[2] > vis[0] + 1 || r[1] + r[3] > vis[1] + 1) {
            problems.push(`${state.name}: ${sel} is partly off screen ${JSON.stringify(r)} in ${JSON.stringify(vis)}`);
          }
          if (await dt.chatEval<boolean>(`d.querySelector(${JSON.stringify(sel)}).disabled`)) {
            continue; // Edit's Name: shown, and fixed by design (a change keeps the name).
          }
          const before = await dt.chatEval<string>(`d.querySelector(${JSON.stringify(sel)}).value`);
          await dt.click(r[0] + r[2] / 2, r[1] + r[3] / 2);
          await dt.chatEval(`(() => { const e = d.querySelector(${JSON.stringify(sel)}); if (d.activeElement === e) e.setSelectionRange(e.value.length, e.value.length); return true; })()`);
          await dt.type('Q');
          const after = await dt.chatEval<string>(`d.querySelector(${JSON.stringify(sel)}).value`);
          if (after !== before + 'Q') {
            problems.push(`${state.name}: a click in ${sel} and a key did not type there (${JSON.stringify(before)} -> ${JSON.stringify(after)})`);
          }
        }
        const s = await look();
        if (s.composer !== DRAFT) {
          problems.push(`${state.name}: the composer changed to ${JSON.stringify(s.composer)}`);
        }
      }
      await closeEverything();
      assert.deepStrictEqual(problems, []);
    });
  }

  test('closing the form gives the keyboard back to what opened it, and leaves nothing inert', async function () {
    this.timeout(30000);
    await dt.setViewport(1200, 900);
    await open(STATES[0]);
    await dt.chatEval("(d.getElementById('mcpPopKeyCancel').click(), true)");
    await sleep(150);
    const after = await dt.chatEval<{ inert: number; active: string; dialog: boolean }>(`({ inert: d.querySelectorAll('[inert]').length,
      active: d.activeElement ? d.activeElement.id : '', dialog: !d.getElementById('mcpDlg').hidden })`);
    assert.strictEqual(after.inert, 0, 'something was left inert');
    assert.ok(after.dialog, 'the /mcp dialog behind the form closed with it');
    assert.notStrictEqual(after.active, 'promptInput', 'the keyboard went to the composer, not back to the dialog');
    await closeEverything();
  });
});
