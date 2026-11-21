// THE CHAT'S CONTROLS (Stage 9, the mockup sheets): the ⋯ menu in the header,
// the ⚙ popover's provider cards, and the composer's ＋ (add context) and @
// (mention a file).
//
// The popover's model list and context meter stay main.js's (renderModelMenu,
// refreshContextUsage): this file draws what is new around them and asks
// main.js, through hooks, for the state it already owns -- how many more files
// may be attached. The popover once had its own effort and mode buttons and
// links to the pages; the effort and mode chips and the ⋯ menu are their one
// home now, so they are not repeated here.
//
// Everything dynamic goes in through textContent, as in main.js and pages.js.
// One function scope; the webview scripts are type-checked as one project
// (media/jsconfig.json).

(function () {
  'use strict';

  /** Providers shown as cards before "All providers", in the mockup's order. */
  const FEATURED = ['groq', 'openai', 'anthropic', 'gemini', 'openrouter', 'nvidia'];

  /**
   * @param {any} vscode the webview API main.js acquired
   * @param {{
   *   toggleSettings: () => void, closeSettings: () => void,
   *   closePopups: () => void,
   *   attachRemaining: () => number, focusComposer: () => void,
   *   prefill: (cmd: string, arg: string) => void
   * }} hooks
   */
  function init(vscode, hooks) {
    const moreBtn = document.getElementById('moreBtn');
    const moreMenu = document.getElementById('moreMenu');
    const gearBtn = document.getElementById('gearBtn');
    const settingsClose = document.getElementById('settingsClose');
    const providerGrid = document.getElementById('providerGrid');
    const addBtn = document.getElementById('contextAddBtn');
    const atBtn = document.getElementById('atBtn');
    const contextMenu = document.getElementById('contextMenu');
    const currentFileDesc = document.getElementById('ctxCurrentFile');

    // ---- menus that drop from a button --------------------------------------

    /** @param {HTMLElement | null} menu @param {HTMLElement | null} btn @param {boolean} open */
    function setMenu(menu, btn, open) {
      if (!menu || !btn) return;
      menu.hidden = !open;
      btn.setAttribute('aria-expanded', open ? 'true' : 'false');
      if (open) {
        const first = /** @type {HTMLElement | null} */ (menu.querySelector('.menu-item'));
        if (first) first.focus();
      }
    }

    function closeMenus() {
      setMenu(moreMenu, moreBtn, false);
      setMenu(contextMenu, addBtn, false);
    }

    /** @param {HTMLElement | null} menu @param {HTMLElement | null} btn */
    function wireMenu(menu, btn) {
      if (!menu || !btn) return;
      btn.addEventListener('click', (e) => {
        e.stopPropagation();
        const open = menu.hidden;
        hooks.closePopups();
        closeMenus();
        setMenu(menu, btn, open);
      });
      // Arrow keys between items; Escape back to the button.
      menu.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') {
          e.stopPropagation();
          setMenu(menu, btn, false);
          btn.focus();
          return;
        }
        if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
        e.preventDefault();
        const items = Array.from(menu.querySelectorAll('.menu-item'));
        const at = items.indexOf(/** @type {Element} */ (document.activeElement));
        const next = items[(at + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length];
        if (next) /** @type {HTMLElement} */ (next).focus();
      });
    }

    wireMenu(moreMenu, moreBtn);
    wireMenu(contextMenu, addBtn);

    // The ⋯ items do their own work (the bookmark in main.js, the pages in
    // pages.js); the menu only has to close behind them.
    if (moreMenu) {
      moreMenu.addEventListener('click', (e) => {
        if (/** @type {HTMLElement} */ (e.target).closest('.menu-item')) {
          setMenu(moreMenu, moreBtn, false);
        }
      });
    }

    document.addEventListener('click', (e) => {
      const t = /** @type {Node} */ (e.target);
      if (moreMenu && !moreMenu.hidden && !moreMenu.contains(t)) setMenu(moreMenu, moreBtn, false);
      if (contextMenu && !contextMenu.hidden && !contextMenu.contains(t)) setMenu(contextMenu, addBtn, false);
    });

    // ---- ＋ and @: what goes in with the question ----------------------------

    if (contextMenu) {
      contextMenu.addEventListener('click', (e) => {
        const item = /** @type {HTMLElement | null} */ (/** @type {HTMLElement} */ (e.target).closest('[data-context]'));
        if (!item) return;
        setMenu(contextMenu, addBtn, false);
        hooks.focusComposer();
        vscode.postMessage({ type: 'addContext', remaining: hooks.attachRemaining(), kind: item.getAttribute('data-context') || '' });
      });
    }
    if (atBtn) {
      atBtn.addEventListener('click', () => {
        vscode.postMessage({ type: 'addContext', remaining: hooks.attachRemaining(), kind: 'mention' });
      });
    }

    // ---- ⚙: the popover around the model list ---------------------------------

    if (gearBtn) {
      gearBtn.addEventListener('click', (e) => {
        e.stopPropagation();
        closeMenus();
        hooks.toggleSettings();
      });
    }
    if (settingsClose) {
      settingsClose.addEventListener('click', () => hooks.closeSettings());
    }

    /** @param {string} base */
    function hostOf(base) {
      try {
        // The registrable part: "integrate.api.nvidia.com" says nvidia.com.
        return new URL(base).hostname.split('.').slice(-2).join('.');
      } catch (_) {
        return '';
      }
    }

    /**
     * @param {string} name
     * @param {string} sub
     * @param {string} base '' for "All providers"
     * @param {boolean} current
     */
    function providerCard(name, sub, base, current) {
      const card = document.createElement('button');
      card.type = 'button';
      card.className = 'provider-card' + (current ? ' current' : '');
      card.setAttribute('role', 'listitem');
      card.setAttribute('data-base', base);
      card.title = base ? (current ? name + ' is connected. Choose it to change its key or model.' : 'Connect ' + name) : 'Every provider Mochiii can connect to';
      const mono = document.createElement('span');
      mono.className = 'provider-mono';
      mono.setAttribute('aria-hidden', 'true');
      mono.textContent = base ? name.charAt(0) : '…';
      const text = document.createElement('span');
      text.className = 'provider-text';
      const n = document.createElement('span');
      n.className = 'provider-name';
      n.textContent = name;
      const s = document.createElement('span');
      s.className = 'provider-sub';
      s.textContent = sub;
      text.appendChild(n);
      text.appendChild(s);
      card.appendChild(mono);
      card.appendChild(text);
      return card;
    }

    /** @param {{ providers?: Array<{ id: string, name: string, apiBase: string }>, currentBase?: string }} msg */
    function renderProviders(msg) {
      if (!providerGrid) return;
      while (providerGrid.firstChild) providerGrid.removeChild(providerGrid.firstChild);
      const all = Array.isArray(msg.providers) ? msg.providers : [];
      const currentBase = typeof msg.currentBase === 'string' ? msg.currentBase : '';
      const current = all.find((p) => p.apiBase === currentBase);
      let shown = FEATURED.map((id) => all.find((p) => p.id === id)).filter((p) => !!p);
      // The provider in use is always a card, first.
      if (current) {
        shown = [current].concat(shown.filter((p) => p && p.id !== current.id)).slice(0, FEATURED.length);
      }
      for (const p of shown) {
        if (!p) continue;
        const isCurrent = !!current && p.id === current.id;
        providerGrid.appendChild(providerCard(p.name, isCurrent ? 'connected' : hostOf(p.apiBase), p.apiBase, isCurrent));
      }
      providerGrid.appendChild(providerCard('All providers', all.length + ' to choose from', '', false));
    }

    if (providerGrid) {
      providerGrid.addEventListener('click', (e) => {
        const card = /** @type {HTMLElement | null} */ (/** @type {HTMLElement} */ (e.target).closest('.provider-card'));
        if (!card) return;
        hooks.closeSettings();
        vscode.postMessage({ type: 'connectProvider', apiBase: card.getAttribute('data-base') || '' });
      });
    }

    // ---- the welcome: a card fills the composer, the user sends -----------------

    const welcome = document.getElementById('firstRun');
    if (welcome) {
      welcome.addEventListener('click', (e) => {
        const b = /** @type {HTMLElement | null} */ (/** @type {HTMLElement} */ (e.target).closest('[data-cmd]'));
        if (b) hooks.prefill(b.getAttribute('data-cmd') || '', b.getAttribute('data-arg') || '');
      });
    }

    return {
      closeMenus: closeMenus,
      renderProviders: renderProviders,
      /** @param {string} name the file open in the editor, '' for none */
      currentFile(name) {
        if (currentFileDesc) currentFileDesc.textContent = name ? name : 'No file is open';
      },
    };
  }

  window.MochiiiControls = { init: init };
})();
