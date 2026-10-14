// THE MCP POP-UP: add or change a server, or set a key it reads, in the chat
// rather than in VS Code's input boxes at the top of the window -- which a
// user found stuck at "Change perplexity (2/4)" when the chat took the
// keyboard back (2026-10-14), and asked for "in a pop-up" instead.
//
// THREE STEPS, ONE BOX. The form (a command and the variables it reads, or
// another client's JSON entry); then the daemon's warning, and the server's
// name typed to agree to it -- the same two steps every other way of adding a
// server takes; then done. A key alone is its own small step.
//
// SECRETS LEAVE THIS FILE ONCE. A variable's value and a key are sent in
// 'mcpPopupSubmit' or 'mcpPopupKeySave' and every field is emptied whenever
// the pop-up closes. Nothing here goes into vscode.setState, the transcript or
// a message back. The JSON pane can hold a key the user pasted: it is emptied
// on close too.
//
// The host side is src/mcpPopup.ts.

(function () {
  'use strict';

  /**
   * @param {any} vscode the webview API main.js acquired
   * @param {{ closePopups: () => void }} hooks
   */
  function init(vscode, hooks) {
    /** @param {string} id */
    const $ = (id) => document.getElementById(id);
    const popup = $('mcpPopup');
    const stepEl = $('mcpPopStep');
    const titleEl = $('mcpPopTitle');
    const noticeEl = $('mcpPopNotice');
    const form = $('mcpPopForm');
    const confirmEl = $('mcpPopConfirm');
    const keyEl = $('mcpPopKey');
    const tabCommand = $('mcpPopTabCommand');
    const tabJson = $('mcpPopTabJson');
    const tabUrl = $('mcpPopTabUrl');
    const urlPane = $('mcpPopUrlPane');
    const urlEl = /** @type {HTMLInputElement | null} */ ($('mcpPopUrl'));
    const transportEl = /** @type {HTMLSelectElement | null} */ ($('mcpPopTransport'));
    const headersEl = $('mcpPopHeaders');
    const commandPane = $('mcpPopCommandPane');
    const jsonPane = $('mcpPopJsonPane');
    const nameEl = /** @type {HTMLInputElement | null} */ ($('mcpPopName'));
    const commandEl = /** @type {HTMLInputElement | null} */ ($('mcpPopCommand'));
    const envEl = $('mcpPopEnv');
    const cwdEl = /** @type {HTMLSelectElement | null} */ ($('mcpPopCwd'));
    const jsonEl = /** @type {HTMLTextAreaElement | null} */ ($('mcpPopJson'));
    const continueBtn = /** @type {HTMLButtonElement | null} */ ($('mcpPopContinue'));
    const warningEl = $('mcpPopWarning');
    const typedLabel = $('mcpPopTypedLabel');
    const typedEl = /** @type {HTMLInputElement | null} */ ($('mcpPopTyped'));
    const agreeBtn = /** @type {HTMLButtonElement | null} */ ($('mcpPopAgree'));
    const keyNameEl = /** @type {HTMLSelectElement | null} */ ($('mcpPopKeyName'));
    const keyValueEl = /** @type {HTMLInputElement | null} */ ($('mcpPopKeyValue'));
    const keyForgetBtn = /** @type {HTMLButtonElement | null} */ ($('mcpPopKeyForget'));
    const keySaveBtn = /** @type {HTMLButtonElement | null} */ ($('mcpPopKeySave'));

    let mode = 'add';
    let tab = 'command';
    let expectName = '';
    let keyServer = '';
    /** @type {{ name: string, stored: boolean, missing: boolean, header?: boolean }[]} */
    let keyVars = [];
    let busy = false;

    function isOpen() {
      return !!popup && !popup.hidden;
    }

    /** @param {string} text */
    function notice(text) {
      if (!noticeEl) return;
      noticeEl.textContent = text;
      noticeEl.hidden = text === '';
    }

    /** @param {'form' | 'confirm' | 'key'} which */
    function show(which) {
      if (form) form.hidden = which !== 'form';
      if (confirmEl) confirmEl.hidden = which !== 'confirm';
      if (keyEl) keyEl.hidden = which !== 'key';
    }

    /** @param {boolean} on */
    function setBusy(on) {
      busy = on;
      for (const b of [continueBtn, agreeBtn, keySaveBtn]) {
        if (b) b.disabled = on || (b === agreeBtn && typedEl !== null && typedEl.value !== expectName);
      }
      if (continueBtn) continueBtn.textContent = on && !form?.hidden ? 'Checking…' : 'Continue';
    }

    /** @param {string} which */
    function selectTab(which) {
      tab = which;
      if (tabCommand) tabCommand.setAttribute('aria-selected', String(which === 'command'));
      if (tabUrl) tabUrl.setAttribute('aria-selected', String(which === 'url'));
      if (tabJson) tabJson.setAttribute('aria-selected', String(which === 'json'));
      if (commandPane) commandPane.hidden = which !== 'command';
      if (urlPane) urlPane.hidden = which !== 'url';
      if (jsonPane) jsonPane.hidden = which !== 'json';
    }

    /**
     * One row: the variable's name, and its value hidden. A stored value is
     * shown as "kept" and left alone unless something is typed over it.
     * @param {string} name @param {boolean} stored
     * @param {HTMLElement | null} [into] @param {string} [placeholder]
     */
    function addEnvRow(name, stored, into, placeholder) {
      const box = into || envEl;
      if (!box) return;
      const row = document.createElement('div');
      row.className = 'mcp-pop-envrow';
      const n = document.createElement('input');
      n.className = 'connect-input mono';
      n.placeholder = placeholder || 'NAME';
      n.value = name;
      n.spellcheck = false;
      n.setAttribute('aria-label', 'Variable name');
      const v = document.createElement('input');
      v.className = 'connect-input';
      v.type = 'password';
      v.autocomplete = 'off';
      v.placeholder = stored ? 'kept — type to replace' : 'value (hidden), or empty';
      v.setAttribute('aria-label', 'Value of the variable, hidden');
      const del = document.createElement('button');
      del.type = 'button';
      del.className = 'connect-icon';
      del.textContent = '✕';
      del.title = 'Remove this variable';
      del.setAttribute('aria-label', 'Remove this variable');
      del.addEventListener('click', () => row.remove());
      row.append(n, v, del);
      box.append(row);
    }

    /** Every field emptied: secrets do not outlive the pop-up. */
    function clearFields() {
      for (const el of [nameEl, commandEl, jsonEl, typedEl, keyValueEl, urlEl]) {
        if (el) el.value = '';
      }
      if (envEl) envEl.replaceChildren();
      if (headersEl) headersEl.replaceChildren();
      if (keyNameEl) keyNameEl.replaceChildren();
      if (warningEl) warningEl.textContent = '';
      expectName = '';
      keyServer = '';
      keyVars = [];
    }

    function close() {
      if (!popup) return;
      popup.hidden = true;
      clearFields();
      notice('');
      setBusy(false);
    }

    function cancel() {
      if (!isOpen()) return;
      close();
      vscode.postMessage({ type: 'mcpPopupCancel' });
    }

    /** @param {any} msg */
    function open(msg) {
      if (!popup) return;
      hooks.closePopups();
      clearFields();
      notice('');
      mode = msg.mode === 'update' ? 'update' : 'add';
      const name = typeof msg.name === 'string' ? msg.name : '';
      if (stepEl) stepEl.textContent = mode === 'update' ? 'Change an MCP server' : 'Add an MCP server';
      if (titleEl) titleEl.textContent = mode === 'update' ? 'Change ' + name : 'A server of your own';
      if (nameEl) {
        nameEl.value = name;
        nameEl.disabled = mode === 'update';
      }
      if (commandEl) commandEl.value = typeof msg.commandLine === 'string' ? msg.commandLine : '';
      for (const v of Array.isArray(msg.env) ? msg.env : []) {
        addEnvRow(String(v.name || ''), !!v.stored);
      }
      if (cwdEl) cwdEl.value = msg.cwd === 'workspace' ? 'workspace' : '';
      if (urlEl) urlEl.value = typeof msg.url === 'string' ? msg.url : '';
      if (transportEl) transportEl.value = msg.transport === 'sse' ? 'sse' : 'http';
      for (const h of Array.isArray(msg.headers) ? msg.headers : []) {
        addEnvRow(String(h.name || ''), !!h.stored, headersEl, 'Header');
      }
      const remote = typeof msg.url === 'string' && msg.url !== '';
      if (tabJson) tabJson.hidden = mode === 'update';
      // A change keeps the server's kind: a program stays a program.
      if (tabCommand) tabCommand.hidden = mode === 'update' && remote;
      if (tabUrl) tabUrl.hidden = mode === 'update' && !remote;
      selectTab(remote ? 'url' : 'command');
      show('form');
      setBusy(false);
      popup.hidden = false;
      (mode === 'update' ? commandEl : nameEl)?.focus();
    }

    /** @param {any} msg */
    function openKey(msg) {
      if (!popup) return;
      hooks.closePopups();
      clearFields();
      notice('');
      keyServer = String(msg.server || '');
      keyVars = Array.isArray(msg.vars) ? msg.vars : [];
      if (stepEl) stepEl.textContent = 'Set a key';
      if (titleEl) titleEl.textContent = keyServer;
      if (keyNameEl) {
        for (const v of keyVars) {
          const o = document.createElement('option');
          o.value = (v.header ? 'header:' : '') + v.name;
          o.textContent = (v.header ? v.name + ' header' : v.name) +
            (v.missing ? ' — no value yet' : v.stored ? ' — kept' : v.header ? '' : ' — from the environment');
          keyNameEl.append(o);
        }
        const first = keyVars.find((v) => v.name === msg.name) || keyVars.find((v) => v.missing) || keyVars[0];
        if (first) keyNameEl.value = (first.header ? 'header:' : '') + first.name;
      }
      syncForget();
      show('key');
      setBusy(false);
      popup.hidden = false;
      keyValueEl?.focus();
    }

    function syncForget() {
      const v = keyVars.find((x) => (x.header ? 'header:' : '') + x.name === keyNameEl?.value);
      if (keyForgetBtn) keyForgetBtn.hidden = !(v && v.stored);
    }

    function submitForm() {
      if (busy) return;
      notice('');
      /** @type {{ name: string, value: string }[]} */
      /** @param {HTMLElement | null} box */
      const rowsOf = (box) => {
        /** @type {{ name: string, value: string }[]} */
        const out = [];
        for (const row of Array.from(box ? box.children : [])) {
          const inputs = row.querySelectorAll('input');
          const n = (/** @type {HTMLInputElement} */ (inputs[0]).value || '').trim();
          const v = /** @type {HTMLInputElement} */ (inputs[1]).value || '';
          if (n) out.push({ name: n, value: v });
        }
        return out;
      };
      const env = rowsOf(envEl);
      const headers = rowsOf(headersEl);
      setBusy(true);
      vscode.postMessage({
        type: 'mcpPopupSubmit',
        mode,
        tab,
        name: (nameEl?.value || '').trim(),
        commandLine: commandEl?.value || '',
        env,
        cwd: cwdEl?.value || '',
        json: tab === 'json' ? jsonEl?.value || '' : '',
        url: tab === 'url' ? urlEl?.value || '' : '',
        transport: transportEl?.value || 'http',
        headers: tab === 'url' ? headers : [],
      });
    }

    function agree() {
      if (busy || !typedEl || typedEl.value !== expectName) return;
      setBusy(true);
      vscode.postMessage({ type: 'mcpPopupAgree', typed: typedEl.value });
    }

    /** @param {boolean} forget */
    function saveKey(forget) {
      if (busy || !keyNameEl) return;
      const value = forget ? '' : keyValueEl?.value || '';
      if (!forget && value.trim() === '') {
        notice('Paste the value, or press Cancel.');
        return;
      }
      setBusy(true);
      vscode.postMessage({ type: 'mcpPopupKeySave', server: keyServer, name: keyNameEl.value, value });
    }

    tabCommand?.addEventListener('click', () => selectTab('command'));
    tabJson?.addEventListener('click', () => selectTab('json'));
    tabUrl?.addEventListener('click', () => selectTab('url'));
    $('mcpPopAddVar')?.addEventListener('click', () => addEnvRow('', false));
    $('mcpPopAddHeader')?.addEventListener('click', () => addEnvRow('Authorization', false, headersEl, 'Header'));
    continueBtn?.addEventListener('click', submitForm);
    $('mcpPopCancel')?.addEventListener('click', cancel);
    $('mcpPopClose')?.addEventListener('click', cancel);
    $('mcpPopBack')?.addEventListener('click', () => {
      notice('');
      show('form');
      setBusy(false);
      vscode.postMessage({ type: 'mcpPopupBack' });
    });
    typedEl?.addEventListener('input', () => {
      if (agreeBtn) agreeBtn.disabled = busy || typedEl.value !== expectName;
    });
    typedEl?.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') agree();
    });
    agreeBtn?.addEventListener('click', agree);
    keyNameEl?.addEventListener('change', syncForget);
    keyValueEl?.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') saveKey(false);
    });
    keySaveBtn?.addEventListener('click', () => saveKey(false));
    keyForgetBtn?.addEventListener('click', () => saveKey(true));
    $('mcpPopKeyCancel')?.addEventListener('click', cancel);
    popup?.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') {
        e.stopPropagation();
        cancel();
      }
    });

    /** @param {any} msg */
    function handle(msg) {
      if (!msg || typeof msg.type !== 'string' || !msg.type.startsWith('mcpPopup')) return false;
      switch (msg.type) {
        case 'mcpPopupOpen':
          open(msg);
          break;
        case 'mcpPopupKeyOpen':
          openKey(msg);
          break;
        case 'mcpPopupConfirm':
          expectName = String(msg.name || '');
          if (warningEl) warningEl.textContent = String(msg.detail || '');
          if (typedLabel) typedLabel.textContent = 'Type ' + expectName + ' to agree and ' + (mode === 'update' ? 'change it' : 'add it');
          if (agreeBtn) agreeBtn.textContent = mode === 'update' ? 'Change' : 'Add';
          if (typedEl) typedEl.value = '';
          notice('');
          show('confirm');
          setBusy(false);
          typedEl?.focus();
          break;
        case 'mcpPopupError':
          notice(String(msg.text || 'Something went wrong.'));
          setBusy(false);
          break;
        case 'mcpPopupClose':
          close();
          break;
      }
      return true;
    }

    return { handle, isOpen };
  }

  window.MochiiiMcpPopup = { init };
})();
