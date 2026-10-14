// THE CONNECT POPUP: a provider, its key, then a model, above the composer --
// the terminal's setup (clients/tui/setup.go) in the chat. The flow and the
// messages are in docs/PROVIDER_MODEL_FLOW.md.
//
// Step 1 lists "Any key" and the providers (src/connectFlow.ts, sent with
// 'connectOpen'), the one in use marked. Step 2 takes the key in a password
// field, or keeps the key in force when the provider in use was picked again.
// Step 3 lists the connected provider's models ('connectModels').
//
// The key leaves this file once, in 'connectSubmit', and the field is emptied
// whenever the popup closes. It is never put in vscode.setState, the transcript
// or a message back. A paste into either search box is refused: someone looking
// at "Select Provider" is likely holding the key, and a search box would show
// it in clear.
//
// One function scope, as pages.js: the webview scripts are type-checked as one
// project (media/jsconfig.json).

(function () {
  'use strict';

  /**
   * @typedef {{ id: string, label: string, hint: string, current: boolean, dim: boolean }} Row
   * @typedef {{ id: string, name: string, apiBase: string, hint: string }} Provider
   */

  /**
   * @param {any} vscode the webview API main.js acquired
   * @param {{ closePopups: () => void, closePage: () => void, focusComposer: () => void }} hooks
   */
  function init(vscode, hooks) {
    const popup = document.getElementById('connectPopup');
    const stepEl = document.getElementById('connectStep');
    const titleEl = document.getElementById('connectTitle');
    const noticeEl = document.getElementById('connectNotice');
    const backBtn = /** @type {HTMLButtonElement | null} */ (document.getElementById('connectBack'));
    const closeBtn = /** @type {HTMLButtonElement | null} */ (document.getElementById('connectClose'));
    const pickEl = document.getElementById('connectPick');
    const filterEl = /** @type {HTMLInputElement | null} */ (document.getElementById('connectFilter'));
    const listEl = document.getElementById('connectList');
    const keyStep = document.getElementById('connectKeyStep');
    const keyLabel = document.getElementById('connectKeyLabel');
    const keyEl = /** @type {HTMLInputElement | null} */ (document.getElementById('connectKey'));
    const keepKeyBtn = /** @type {HTMLButtonElement | null} */ (document.getElementById('connectKeepKey'));
    const cancelBtn = /** @type {HTMLButtonElement | null} */ (document.getElementById('connectCancelBtn'));
    const submitBtn = /** @type {HTMLButtonElement | null} */ (document.getElementById('connectSubmit'));
    const modelStep = document.getElementById('connectModelStep');
    const loadingEl = document.getElementById('connectModelsLoading');
    const modelFilterEl = /** @type {HTMLInputElement | null} */ (document.getElementById('connectModelFilter'));
    const modelListEl = document.getElementById('connectModelList');
    const keepModelBtn = /** @type {HTMLButtonElement | null} */ (document.getElementById('connectKeepModel'));

    /** @type {Provider[]} */
    let providers = [];
    /** Addresses the provider list is narrowed to (needs_provider); null for all. */
    /** @type {string[] | null} */
    let only = null;
    /** @type {Provider | null} */
    let chosen = null;
    /** @type {Row[]} */
    let models = [];
    let currentBase = '';
    let maskedKey = '';
    let keyInForce = false;
    let firstRun = false;
    let busy = false;

    function isOpen() {
      return !!popup && !popup.hidden;
    }

    /** @param {string} text */
    function notice(text) {
      if (!noticeEl) {
        return;
      }
      noticeEl.textContent = text;
      noticeEl.hidden = text === '';
    }

    /**
     * @param {string} step "Step n of 3"
     * @param {string} title
     */
    function heading(step, title) {
      if (stepEl) stepEl.textContent = step;
      if (titleEl) titleEl.textContent = title;
    }

    /** @param {'pick' | 'key' | 'model'} which */
    function show(which) {
      if (pickEl) pickEl.hidden = which !== 'pick';
      if (keyStep) keyStep.hidden = which !== 'key';
      if (modelStep) modelStep.hidden = which !== 'model';
      if (backBtn) backBtn.hidden = which !== 'key';
    }

    /** @param {boolean} on */
    function setBusy(on) {
      busy = on;
      if (keyEl) keyEl.disabled = on;
      if (submitBtn) {
        submitBtn.disabled = on;
        submitBtn.textContent = on ? 'Checking with ' + (chosen && chosen.apiBase ? chosen.name : 'the provider') + '…' : 'Connect';
      }
      if (keepKeyBtn) keepKeyBtn.disabled = on;
      if (backBtn) backBtn.disabled = on;
      if (keepModelBtn) keepModelBtn.disabled = on;
    }

    /** @param {string} base */
    function hostOf(base) {
      try {
        return new URL(base).hostname;
      } catch (_) {
        return '';
      }
    }

    /**
     * Draws rows into a list, narrowed by the search text: every word must
     * appear in the label, hint or id ("open ro" finds OpenRouter).
     * @param {HTMLElement | null} el
     * @param {Row[]} rows
     * @param {HTMLInputElement | null} filter
     */
    function renderRows(el, rows, filter) {
      if (!el) {
        return;
      }
      while (el.firstChild) {
        el.removeChild(el.firstChild);
      }
      const words = (filter ? filter.value : '').toLowerCase().split(/\s+/).filter((w) => w !== '');
      const shown = rows.filter((r) => {
        const hay = (r.label + ' ' + r.hint + ' ' + r.id).toLowerCase();
        return words.every((w) => hay.includes(w));
      });
      if (shown.length === 0) {
        const empty = document.createElement('div');
        empty.className = 'connect-empty';
        empty.textContent = 'Nothing matches.';
        el.appendChild(empty);
        return;
      }
      for (const r of shown) {
        const item = document.createElement('button');
        item.type = 'button';
        item.className = 'connect-item' + (r.current ? ' current' : '') + (r.dim ? ' quiet' : '');
        item.setAttribute('role', 'option');
        item.setAttribute('aria-selected', r.current ? 'true' : 'false');
        item.setAttribute('data-id', r.id);
        if (r.current) {
          const dot = document.createElement('span');
          dot.className = 'connect-dot';
          dot.setAttribute('aria-hidden', 'true');
          item.appendChild(dot);
        }
        const name = document.createElement('span');
        name.className = 'connect-name';
        name.textContent = r.label;
        item.appendChild(name);
        if (r.current) {
          const cur = document.createElement('span');
          cur.className = 'connect-cur';
          cur.textContent = 'current';
          item.appendChild(cur);
        }
        if (r.hint) {
          const hint = document.createElement('span');
          hint.className = 'connect-hint';
          hint.textContent = r.hint;
          item.appendChild(hint);
        }
        el.appendChild(item);
      }
    }

    /** @returns {Row[]} */
    function providerRows() {
      return providers
        .filter((p) => only === null || only.includes(p.apiBase))
        .map((p) => ({
          id: p.apiBase,
          label: p.name,
          hint: p.apiBase === '' ? p.hint : [p.hint, hostOf(p.apiBase)].filter((x) => x !== '').join('  ·  '),
          current: p.apiBase !== '' && p.apiBase === currentBase,
          dim: false,
        }));
    }

    function renderProviders() {
      renderRows(listEl, providerRows(), filterEl);
    }

    function renderModels() {
      renderRows(modelListEl, models, modelFilterEl);
    }

    /** Focus the marked row, else the search box. @param {HTMLElement | null} el @param {HTMLInputElement | null} filter */
    function focusCurrent(el, filter) {
      const cur = el ? /** @type {HTMLElement | null} */ (el.querySelector('.connect-item.current')) : null;
      if (cur) {
        cur.focus();
        cur.scrollIntoView({ block: 'nearest' });
      } else if (filter) {
        filter.focus();
      }
    }

    function showPick() {
      chosen = null;
      heading('Step 1 of 3', firstRun ? 'Connect a model provider to get started' : 'Select Provider');
      show('pick');
      renderProviders();
      focusCurrent(listEl, filterEl);
    }

    /** @param {Provider} p */
    function showKey(p) {
      chosen = p;
      heading('Step 2 of 3', p.apiBase ? 'API key for ' + p.name : 'Paste any API key');
      show('key');
      if (keyLabel) {
        keyLabel.textContent = p.apiBase ? 'Paste your ' + p.name + ' API key' : 'Paste the key: the provider is told from it';
      }
      const keep = keyInForce && p.apiBase !== '' && p.apiBase === currentBase;
      if (keepKeyBtn) {
        keepKeyBtn.hidden = !keep;
        keepKeyBtn.textContent = maskedKey ? 'Keep current key (' + maskedKey + ')' : 'Keep current key';
      }
      if (keyEl) {
        keyEl.placeholder = keep ? 'Paste a new key, or keep the current one' : p.hint && p.apiBase ? p.hint : 'API key';
        keyEl.focus();
      }
    }

    function showModelsLoading() {
      heading('Step 3 of 3', 'Select Model');
      show('model');
      if (loadingEl) loadingEl.hidden = false;
      if (modelFilterEl) modelFilterEl.hidden = true;
      if (modelListEl) modelListEl.hidden = true;
      if (keepModelBtn) keepModelBtn.hidden = true;
    }

    /** @param {{ rows?: Array<{ name: string, label: string, hint: string, current: boolean, quiet: boolean }>, inForce?: string }} msg */
    function showModels(msg) {
      setBusy(false);
      models = (Array.isArray(msg.rows) ? msg.rows : []).map((r) => ({ id: r.name, label: r.label, hint: r.hint, current: r.current, dim: r.quiet }));
      heading('Step 3 of 3', 'Select Model');
      show('model');
      if (loadingEl) loadingEl.hidden = true;
      if (modelFilterEl) {
        modelFilterEl.hidden = false;
        modelFilterEl.value = '';
      }
      if (modelListEl) modelListEl.hidden = false;
      if (keepModelBtn) {
        const inForce = typeof msg.inForce === 'string' ? msg.inForce : '';
        keepModelBtn.hidden = inForce === '';
        keepModelBtn.textContent = 'Keep ' + inForce;
      }
      renderModels();
      focusCurrent(modelListEl, modelFilterEl);
    }

    /** @param {any} msg */
    function open(msg) {
      if (!popup) {
        return;
      }
      if (isOpen()) {
        if (keyEl && keyStep && !keyStep.hidden) keyEl.focus();
        return;
      }
      providers = Array.isArray(msg.providers) ? msg.providers : [];
      currentBase = typeof msg.currentBase === 'string' ? msg.currentBase : '';
      maskedKey = typeof msg.maskedKey === 'string' ? msg.maskedKey : '';
      keyInForce = msg.keyInForce === true;
      firstRun = msg.firstRun === true;
      only = null;
      hooks.closePopups();
      hooks.closePage();
      popup.hidden = false;
      if (filterEl) filterEl.value = '';
      if (keyEl) keyEl.value = '';
      setBusy(false);
      notice('');
      const base = typeof msg.base === 'string' ? msg.base : '';
      if (base !== '') {
        const known = providers.find((p) => p.apiBase === base);
        showKey(known || { id: base, name: base, apiBase: base, hint: '' });
      } else {
        showPick();
      }
    }

    function close() {
      if (!popup) {
        return;
      }
      popup.hidden = true;
      if (keyEl) keyEl.value = '';
      chosen = null;
      models = [];
      setBusy(false);
      notice('');
      hooks.focusComposer();
    }

    function cancel() {
      if (!isOpen()) {
        return;
      }
      close();
      vscode.postMessage({ type: 'connectCancel' });
    }

    function submit() {
      if (busy || !chosen || !keyEl) {
        return;
      }
      const key = keyEl.value.trim();
      if (key === '') {
        notice('Paste a key first.');
        keyEl.focus();
        return;
      }
      notice('');
      setBusy(true);
      vscode.postMessage({ type: 'connectSubmit', apiBase: chosen.apiBase, key: key });
    }

    /**
     * Click and arrow keys for a list.
     * @param {HTMLElement | null} el
     * @param {HTMLInputElement | null} filter
     * @param {(id: string) => void} pick
     * @param {() => void} render
     */
    function wireList(el, filter, pick, render) {
      if (el) {
        el.addEventListener('click', (e) => {
          const item = /** @type {HTMLElement | null} */ (/** @type {HTMLElement} */ (e.target).closest('.connect-item'));
          if (item && !busy) {
            pick(item.getAttribute('data-id') || '');
          }
        });
        el.addEventListener('keydown', (e) => {
          if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') {
            return;
          }
          e.preventDefault();
          const items = Array.from(el.querySelectorAll('.connect-item'));
          const at = items.indexOf(/** @type {Element} */ (document.activeElement));
          if (e.key === 'ArrowUp' && at <= 0 && filter) {
            filter.focus();
            return;
          }
          const next = items[Math.min(items.length - 1, Math.max(0, at + (e.key === 'ArrowDown' ? 1 : -1)))];
          if (next) {
            /** @type {HTMLElement} */ (next).focus();
          }
        });
      }
      if (filter) {
        filter.addEventListener('input', render);
        filter.addEventListener('paste', (e) => {
          e.preventDefault();
          notice('Pasting is turned off in search, so a key is never shown here. Pick a provider first; the key goes on the next screen.');
        });
        filter.addEventListener('keydown', (e) => {
          const first = el ? /** @type {HTMLElement | null} */ (el.querySelector('.connect-item')) : null;
          if (e.key === 'ArrowDown' && first) {
            e.preventDefault();
            first.focus();
          } else if (e.key === 'Enter' && first) {
            e.preventDefault();
            first.click();
          }
        });
      }
    }

    wireList(
      listEl,
      filterEl,
      (id) => {
        const p = providers.find((x) => x.apiBase === id);
        if (p) {
          notice('');
          showKey(p);
        }
      },
      renderProviders,
    );
    wireList(
      modelListEl,
      modelFilterEl,
      (id) => {
        notice('');
        setBusy(true);
        vscode.postMessage({ type: 'connectModel', tier: id });
      },
      renderModels,
    );

    if (keyEl) {
      keyEl.addEventListener('keydown', (e) => {
        if (e.key === 'Enter') {
          e.preventDefault();
          // Enter on an empty field for the provider in use keeps its key, as
          // in the terminal: picking the provider you are on is how someone
          // who only wants another model arrives here.
          if (keyEl.value.trim() === '' && keepKeyBtn && !keepKeyBtn.hidden) {
            keepKeyBtn.click();
            return;
          }
          submit();
        }
      });
    }
    if (submitBtn) submitBtn.addEventListener('click', submit);
    if (keepKeyBtn) {
      keepKeyBtn.addEventListener('click', () => {
        if (busy) return;
        if (keyEl) keyEl.value = '';
        notice('');
        showModelsLoading();
        vscode.postMessage({ type: 'connectKeep' });
      });
    }
    if (keepModelBtn) keepModelBtn.addEventListener('click', cancel);
    if (cancelBtn) cancelBtn.addEventListener('click', cancel);
    if (closeBtn) closeBtn.addEventListener('click', cancel);
    if (backBtn) {
      backBtn.addEventListener('click', () => {
        if (!busy) {
          notice('');
          showPick();
        }
      });
    }
    if (popup) {
      // Escape: from the key step back to the list it came from, otherwise
      // cancel. Nothing else -- the chat's own Escape handling (pages, other
      // popups) is on document, below this.
      popup.addEventListener('keydown', (e) => {
        if (e.key !== 'Escape') {
          return;
        }
        e.stopPropagation();
        if (keyStep && !keyStep.hidden && backBtn && !backBtn.hidden && !busy && providers.length > 0) {
          notice('');
          showPick();
          return;
        }
        cancel();
      });
    }

    return {
      isOpen: isOpen,
      /** @param {any} msg */
      handle(msg) {
        switch (msg && msg.type) {
          case 'connectOpen':
            open(msg);
            return true;
          case 'connectNeedsProvider':
            // Sent nowhere: the key's shape names several providers. The key
            // stays in its field; the list narrows to them.
            setBusy(false);
            only = Array.isArray(msg.candidates) ? msg.candidates : null;
            if (filterEl) filterEl.value = '';
            showPick();
            notice(typeof msg.text === 'string' ? msg.text : 'Pick the provider that issued this key.');
            return true;
          case 'connectModelsLoading':
            setBusy(false);
            notice('');
            showModelsLoading();
            return true;
          case 'connectModels':
            showModels(msg);
            return true;
          case 'connectError':
            setBusy(false);
            notice(typeof msg.text === 'string' ? msg.text : 'That did not work.');
            if (keyStep && !keyStep.hidden && keyEl) {
              keyEl.focus();
            } else if (modelStep && !modelStep.hidden && loadingEl && !loadingEl.hidden) {
              // The model list never arrived: back to the key step it came from.
              if (chosen) showKey(chosen);
              else showPick();
            }
            return true;
          case 'connectClose':
            close();
            return true;
          default:
            return false;
        }
      },
    };
  }

  window.MochiiiConnect = { init: init };
})();
