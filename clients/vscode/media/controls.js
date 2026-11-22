// THE CHAT'S CONTROLS: the ⋯ menu in the header, the composer's ＋ (add
// context, a file by name among it), and the model menu's way to another
// provider.
//
// The model list and the context meter stay main.js's (renderModelMenu,
// refreshContextUsage): this file draws what is around them and asks main.js,
// through hooks, for the state it already owns -- how many more files may be
// attached. The model menu was once a "Model & settings" popover behind a gear,
// with provider cards, effort and mode buttons and links to the pages; each of
// those has one home now (/connect and Account, the effort and mode chips, the
// ⋯ menu), so none is repeated here.
//
// Everything dynamic goes in through textContent, as in main.js and pages.js.
// One function scope; the webview scripts are type-checked as one project
// (media/jsconfig.json).

(function () {
  'use strict';

  /**
   * @param {any} vscode the webview API main.js acquired
   * @param {{
   *   closeSettings: () => void,
   *   closePopups: () => void,
   *   attachRemaining: () => number, focusComposer: () => void
   * }} hooks
   */
  function init(vscode, hooks) {
    const moreBtn = document.getElementById('moreBtn');
    const moreMenu = document.getElementById('moreMenu');
    const settingsClose = document.getElementById('settingsClose');
    const switchProviderBtn = document.getElementById('switchProviderBtn');
    const currentProviderEl = document.getElementById('currentProvider');
    const addBtn = document.getElementById('contextAddBtn');
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

    // ---- ＋: what goes in with the question (@ is its first item) ------------

    if (contextMenu) {
      contextMenu.addEventListener('click', (e) => {
        const item = /** @type {HTMLElement | null} */ (/** @type {HTMLElement} */ (e.target).closest('[data-context]'));
        if (!item) return;
        setMenu(contextMenu, addBtn, false);
        hooks.focusComposer();
        vscode.postMessage({ type: 'addContext', remaining: hooks.attachRemaining(), kind: item.getAttribute('data-context') || '' });
      });
    }

    // ---- the model menu: its close button, and another provider ----------------

    if (settingsClose) {
      settingsClose.addEventListener('click', () => hooks.closeSettings());
    }
    if (switchProviderBtn) {
      switchProviderBtn.addEventListener('click', () => {
        hooks.closeSettings();
        // '' is "every provider": the connect popup opens on its list.
        vscode.postMessage({ type: 'connectProvider', apiBase: '' });
      });
    }

    /** @param {{ providers?: Array<{ id: string, name: string, apiBase: string }>, currentBase?: string }} msg */
    function renderProvider(msg) {
      if (!currentProviderEl) return;
      const all = Array.isArray(msg.providers) ? msg.providers : [];
      const currentBase = typeof msg.currentBase === 'string' ? msg.currentBase : '';
      const current = all.find((p) => p.apiBase === currentBase);
      currentProviderEl.textContent = current ? 'Using ' + current.name : currentBase ? 'Using ' + currentBase : '';
    }

    return {
      closeMenus: closeMenus,
      renderProvider: renderProvider,
      /** @param {string} name the file open in the editor, '' for none */
      currentFile(name) {
        if (currentFileDesc) currentFileDesc.textContent = name ? name : 'No file is open';
      },
    };
  }

  window.MochiiiControls = { init: init };
})();
