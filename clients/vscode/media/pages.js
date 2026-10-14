// THE CHAT'S PAGES: Customize, Account, Settings (Stage 8).
//
// The markup is static (src/chatPages/markup.ts); this file shows a page,
// asks the host for what goes in it, and fills the empty containers through
// textContent -- never markup, for the reason accessibility.test.ts holds
// main.js to. Every request it makes names an entry in a host-side table
// (src/chatPages/rules.ts): a command by its page name, a setting by its key,
// a link by its name. It never sends a command id, a URL or a path.
//
// One function scope, as marketplace.js: the webview scripts are type-checked
// as one project (media/jsconfig.json).

(function () {
  'use strict';

  /**
   * @param {any} vscode the webview API main.js acquired (it may be acquired once)
   * @param {{ openModelMenu: () => void, sendText: (t: string) => void, prefill: (t: string) => void,
   *   focusComposer: () => void, setEffort: (e: string) => void, getEffort: () => string, closePopups: () => void }} hooks
   */
  function init(vscode, hooks) {
    /** @type {Record<string, HTMLElement | null>} */
    const pages = {
      customize: document.getElementById('pageCustomize'),
      account: document.getElementById('pageAccount'),
      settings: document.getElementById('pageSettings'),
    };
    /** @type {Record<string, HTMLElement | null>} */
    const openers = {
      customize: document.getElementById('customizeBtn'),
      account: document.getElementById('accountBtn'),
      settings: document.getElementById('settingsBtn'),
    };
    let current = '';
    /** @type {HTMLElement | null} */
    let returnFocus = null;
    /** @type {{ receive: (msg: any) => void } | null} */
    let market = null;

    /**
     * @param {string} tag
     * @param {string} [className]
     * @param {string} [text]
     */
    function el(tag, className, text) {
      const node = document.createElement(tag);
      if (className) node.className = className;
      if (text !== undefined) node.textContent = text;
      return node;
    }

    /** @param {string} id @param {string} text */
    function setText(id, text) {
      const node = document.getElementById(id);
      if (node) node.textContent = text;
    }

    // ---- showing a page --------------------------------------------------------

    /**
     * @param {string} name
     * @param {{ tab?: string, sub?: string, search?: string }} [opts]
     */
    function open(name, opts) {
      const page = pages[name];
      if (!page) return;
      hooks.closePopups();
      if (current && current !== name) hide(current);
      if (!current) returnFocus = /** @type {HTMLElement | null} */ (document.activeElement);
      current = name;
      page.hidden = false;
      document.body.classList.add('page-open');
      const opener = openers[name];
      if (opener) opener.setAttribute('aria-expanded', 'true');
      if (opts && opts.tab) selectTab(name === 'settings' ? 'set' : 'cust', opts.tab);
      if (opts && opts.sub) selectTab('mcp', opts.sub);
      if (opts && opts.search !== undefined) showMarket(opts.search);
      const title = document.getElementById(page.id + 'Title');
      if (title) title.focus();
      vscode.postMessage({ type: 'page:load', page: name });
    }

    /** @param {string} name */
    function hide(name) {
      const page = pages[name];
      if (page) page.hidden = true;
      const opener = openers[name];
      if (opener) opener.setAttribute('aria-expanded', 'false');
    }

    function close() {
      if (!current) return;
      hide(current);
      current = '';
      document.body.classList.remove('page-open');
      if (returnFocus && document.body.contains(returnFocus)) returnFocus.focus();
      else hooks.focusComposer();
      returnFocus = null;
    }

    for (const name of Object.keys(openers)) {
      const b = openers[name];
      if (b) b.addEventListener('click', () => (current === name ? close() : open(name)));
    }

    // ---- tabs: one rule for every [data-tabs] group ----------------------------

    /** @param {string} group @param {string} key */
    function selectTab(group, key) {
      const list = document.querySelector('[data-tabs="' + group + '"]');
      if (!list) return;
      list.querySelectorAll('[role="tab"]').forEach((t) => {
        const on = t.getAttribute('data-tab') === key;
        t.setAttribute('aria-selected', String(on));
        t.setAttribute('tabindex', on ? '0' : '-1');
        const panel = document.getElementById(group + 'Panel-' + t.getAttribute('data-tab'));
        if (panel) panel.hidden = !on;
      });
      if (group === 'mcp' && key === 'market') mountMarket();
    }

    document.querySelectorAll('[data-tabs]').forEach((list) => {
      const group = list.getAttribute('data-tabs') || '';
      list.addEventListener('click', (e) => {
        const tab = /** @type {HTMLElement} */ (e.target).closest('[role="tab"]');
        if (tab) selectTab(group, tab.getAttribute('data-tab') || '');
      });
      // Arrow keys move between tabs, as a tablist should.
      list.addEventListener('keydown', (e) => {
        const ke = /** @type {KeyboardEvent} */ (e);
        const keys = ke.key === 'ArrowRight' || ke.key === 'ArrowDown' ? 1 : ke.key === 'ArrowLeft' || ke.key === 'ArrowUp' ? -1 : 0;
        if (!keys) return;
        const tabs = Array.from(list.querySelectorAll('[role="tab"]'));
        const at = tabs.findIndex((t) => t.getAttribute('aria-selected') === 'true');
        const next = /** @type {HTMLElement} */ (tabs[(at + keys + tabs.length) % tabs.length]);
        ke.preventDefault();
        selectTab(group, next.getAttribute('data-tab') || '');
        next.focus();
      });
    });

    // ---- buttons that name what they want --------------------------------------

    document.addEventListener('click', (e) => {
      const t = /** @type {HTMLElement} */ (e.target).closest('button');
      if (!t || !t.closest('.page')) return;
      if (t.hasAttribute('data-close-page')) close();
      const cmd = t.getAttribute('data-page-command');
      if (cmd) vscode.postMessage({ type: 'page:command', name: cmd });
      const link = t.getAttribute('data-page-link');
      if (link) vscode.postMessage({ type: 'page:link', name: link });
      const page = t.getAttribute('data-open-page');
      if (page) open(page);
      if (t.hasAttribute('data-open-models')) {
        close();
        hooks.openModelMenu();
      }
      if (t.hasAttribute('data-page-refresh') && current) vscode.postMessage({ type: 'page:load', page: current });
      if (t.hasAttribute('data-mcp-browse')) selectTab('mcp', 'market');
      if (t.hasAttribute('data-spec-new')) {
        close();
        hooks.prefill('/spec ');
      }
    });

    // Settings controls: each names its setting; the host checks key and value.
    document.querySelectorAll('[data-setting]').forEach((control) => {
      control.addEventListener('change', () => {
        const input = /** @type {HTMLInputElement} */ (control);
        const value = input.type === 'checkbox' ? input.checked : input.value;
        vscode.postMessage({ type: 'page:setSetting', key: control.getAttribute('data-setting'), value });
      });
    });
    const defaultModel = /** @type {HTMLSelectElement | null} */ (document.getElementById('setDefaultModel'));
    if (defaultModel) {
      defaultModel.addEventListener('change', () => vscode.postMessage({ type: 'page:chooseModel', tier: defaultModel.value }));
    }
    const effort = /** @type {HTMLSelectElement | null} */ (document.getElementById('setEffort'));
    if (effort) {
      effort.addEventListener('change', () => hooks.setEffort(effort.value));
    }

    // ---- the marketplace, inside the Customize page ----------------------------

    function mountMarket() {
      if (market || !window.MochiiiMarketplace) return;
      const root = document.getElementById('marketRoot');
      if (!root) return;
      // Its requests go to the host prefixed, so they cannot be mistaken for
      // the chat's own messages ("ready", "refresh" mean other things there).
      market = window.MochiiiMarketplace.mount(root, (msg) => {
        if (msg && msg.type === 'manage') {
          selectTab('mcp', 'installed');
          return;
        }
        vscode.postMessage(Object.assign({}, msg, { type: 'mk:' + msg.type }));
      });
    }

    /** @param {string} q */
    function showMarket(q) {
      selectTab('cust', 'mcp');
      selectTab('mcp', 'market');
      if (q && market) market.receive({ type: 'search', q });
      else if (q) pendingSearch = q;
    }
    let pendingSearch = '';

    // ---- filling the pages -----------------------------------------------------

    /**
     * @param {string} label
     * @param {() => void} run
     * @param {string} [className]
     */
    function act(label, run, className) {
      const b = el('button', 'row-act' + (className ? ' ' + className : ''), label);
      /** @type {HTMLButtonElement} */ (b).type = 'button';
      b.addEventListener('click', run);
      return b;
    }

    /** @param {any} msg */
    function renderMcp(msg) {
      const list = document.getElementById('mcpInstalledList');
      const empty = document.getElementById('mcpEmpty');
      const notice = document.getElementById('mcpNotice');
      if (!list) return;
      list.replaceChildren();
      // A row per server (Stage 9, the mockup's MCP list): its letter, name and
      // command, whether it is on, a switch to pause it, and edit and remove.
      // A server from the config file is shown, not changed, here.
      for (const s of msg.servers || []) {
        const own = s.source !== 'config';
        const row = el('div', 'row mcp-row');
        const mono = el('span', 'mcp-mono', String(s.name || '?').charAt(0).toUpperCase());
        mono.setAttribute('aria-hidden', 'true');
        const text = el('div', 'mcp-text');
        text.append(el('span', 'row-title', s.name));
        const sub = el('div', 'row-sub');
        sub.append(el('code', '', s.command));
        text.append(sub);
        if (s.env && s.env.length) text.append(el('div', 'row-sub', 'Reads ' + s.env.join(', ')));
        // WHETHER IT REALLY STARTS. "Active" used to mean only "not paused", and
        // a server with no key showed it while exiting on every start.
        const status = s.status || { short: '', detail: '', needsKey: false };
        const failed = !s.disabled && /^(failed|needs) /.test(status.short + ' ');
        if (!s.disabled && status.detail && (failed || status.needsKey)) {
          text.append(el('div', 'row-sub mcp-problem', status.detail));
        }
        const state = s.disabled
          ? 'Paused'
          : status.needsKey
            ? 'Needs key'
            : status.short === 'failed to start'
              ? 'Failed'
              : status.short === 'running'
                ? 'Running'
                : !own
                  ? 'From config'
                  : 'On';
        const badge = el('span', 'tag ' + (s.disabled ? 'off' : failed || status.needsKey ? 'warn' : 'on'), state);
        if (status.detail) badge.title = status.detail;
        row.append(mono, text, badge);
        if (!own && s.canSetKey) {
          const acts = el('div', 'mcp-acts');
          const key = act('Set key…', () => vscode.postMessage({ type: 'mcp:row', action: 'setKey', name: s.name }));
          key.setAttribute('aria-label', 'Set a key for ' + s.name);
          acts.append(key);
          row.append(acts);
        }
        if (own) {
          const sw = el('button', 'mcp-switch');
          /** @type {HTMLButtonElement} */ (sw).type = 'button';
          sw.setAttribute('role', 'switch');
          sw.setAttribute('aria-checked', s.disabled ? 'false' : 'true');
          sw.setAttribute('aria-label', (s.disabled ? 'Resume ' : 'Pause ') + s.name);
          sw.title = s.disabled ? 'Paused: its tools are not offered. Click to resume.' : 'On: its tools are offered. Click to pause.';
          sw.addEventListener('click', () => vscode.postMessage({ type: 'mcp:row', action: s.disabled ? 'enable' : 'disable', name: s.name }));
          const edit = act('Change…', () => vscode.postMessage({ type: 'mcp:row', action: 'edit', name: s.name }));
          edit.setAttribute('aria-label', 'Change ' + s.name);
          const remove = act('Remove', () => vscode.postMessage({ type: 'mcp:row', action: 'remove', name: s.name }), 'danger');
          remove.setAttribute('aria-label', 'Remove ' + s.name);
          const acts = el('div', 'mcp-acts');
          acts.append(sw);
          if (s.canSetKey) {
            const key = act('Set key…', () => vscode.postMessage({ type: 'mcp:row', action: 'setKey', name: s.name }), status.needsKey ? 'primary' : '');
            key.setAttribute('aria-label', 'Set a key for ' + s.name);
            acts.append(key);
          }
          acts.append(edit, remove);
          row.append(acts);
        }
        list.append(row);
      }
      if (empty) empty.hidden = (msg.servers || []).length > 0;
      if (notice) {
        notice.textContent = msg.notice || '';
        notice.hidden = !msg.notice;
      }
    }

    /** @param {any} msg */
    function renderSpecs(msg) {
      const list = document.getElementById('specList');
      if (!list) return;
      list.replaceChildren();
      const specs = msg.specs || [];
      if (specs.length === 0) {
        const e = el('div', 'empty');
        e.append(el('p', '', 'No specs yet. "New spec" asks Mochiii to write one with you, from a goal you describe.'));
        list.append(e);
        return;
      }
      for (const spec of specs) {
        const active = spec === msg.active;
        const row = el('div', 'row');
        const head = el('div', 'row-head');
        head.append(el('span', 'row-title', String(spec).replace(/^specs\//, '')));
        head.append(el('span', 'row-spacer'));
        if (active) head.append(el('span', 'tag on', 'active'));
        row.append(head);
        const acts = el('div', 'row-acts');
        // Each runs the /spec command it names, through the composer, so it is
        // checked and confined exactly as typing it would be.
        /** @param {string} sub */
        const run = (sub) => () => {
          close();
          hooks.sendText('/spec ' + sub + (sub === 'off' ? '' : ' ' + spec));
        };
        if (active) acts.append(act('Turn off', run('off')));
        else acts.append(act('Use', run('use')));
        acts.append(act('Show', run('show')), act('Build', run('build')), act('Check', run('check')));
        row.append(acts);
        list.append(row);
      }
    }

    /** @param {any} msg */
    function renderTools(msg) {
      const list = document.getElementById('toolList');
      if (!list) return;
      list.replaceChildren();
      for (const t of msg.tools || []) {
        const row = el('div', 'row');
        const head = el('div', 'row-head');
        head.append(el('span', 'row-title', String(t.name).replace(/^builtin__/, '')));
        head.append(el('span', 'row-spacer'));
        const policy = t.policy === 'deny' ? 'off' : t.policy;
        head.append(el('span', 'tag ' + (policy === 'allow' ? 'on' : policy === 'ask' ? 'ask' : 'off'), policy));
        row.append(head);
        const where = t.confined === 'network' ? 'reaches the internet' : t.confined === 'yes' ? 'confined to the workspace' : 'can start another program';
        row.append(el('div', 'row-sub', (t.description ? t.description + ' · ' : '') + where));
        list.append(row);
      }
      setText('toolSource', msg.note || 'Policies come from the mcp.builtin.tools block of the config the daemon loaded.');
    }

    /** @param {any} v */
    function renderAccount(v) {
      setText('acctBadge', v.badge);
      setText('acctProvider', v.provider);
      setText('acctBase', v.base);
      const dot = document.getElementById('acctDot');
      if (dot) {
        dot.className = 'dot ' + (v.reachable ? 'ok' : 'bad');
        dot.setAttribute('aria-label', v.reachable ? 'connected' : 'not connected');
      }
      setText('acctKey', v.key);
      setText('acctKeyMore', v.keyMore);
      setText('acctModel', v.model);
      setText('acctModelMore', v.modelMore);
      setText('acctDaemon', v.daemon);
      const spend = document.getElementById('acctSpend');
      const rows = document.getElementById('acctSpendRows');
      if (spend && rows) {
        rows.replaceChildren();
        for (const m of v.meters || []) {
          const meter = el('div', 'meter');
          const label = el('div', 'meter-label');
          label.append(el('span', '', m.label), el('span', '', m.spent));
          meter.append(label);
          if (m.fraction >= 0) {
            const track = el('div', 'meter-track');
            const fill = el('div', 'meter-fill' + (m.fraction >= 0.9 ? ' hot' : m.fraction >= 0.7 ? ' warn' : ''));
            fill.style.width = Math.round(m.fraction * 100) + '%';
            track.append(fill);
            meter.append(track);
          }
          rows.append(meter);
        }
        spend.hidden = (v.meters || []).length === 0;
        setText('acctSpendNote', v.spendNote || '');
      }
      fillList('acctPrivacy', v.privacy || []);
      const box = document.getElementById('acctPrivacyBox');
      if (box) box.hidden = (v.privacy || []).length === 0;
    }

    /** @param {string} id @param {string[]} items */
    function fillList(id, items) {
      const ul = document.getElementById(id);
      if (!ul) return;
      ul.replaceChildren(...items.map((t) => el('li', '', t)));
    }

    /** @param {any} msg */
    function renderSettings(msg) {
      setText('setProvider', msg.provider);
      if (defaultModel) {
        const options = [['', 'The daemon’s default' + (msg.daemonDefault ? ' (' + msg.daemonDefault + ')' : '')]].concat(
          (msg.tiers || []).map((/** @type {any} */ t) => [t.name, t.name === t.slug ? t.name : t.name + ' — ' + t.slug])
        );
        defaultModel.replaceChildren(
          ...options.map(([value, label]) => {
            const o = document.createElement('option');
            o.value = value;
            o.textContent = label;
            return o;
          })
        );
        defaultModel.value = msg.currentTier || '';
      }
      if (effort) effort.value = hooks.getEffort();
      for (const key of Object.keys(msg.values || {})) {
        const control = /** @type {HTMLInputElement | null} */ (document.querySelector('[data-setting="' + key + '"]'));
        if (!control) continue;
        if (control.type === 'checkbox') control.checked = !!msg.values[key];
        else control.value = String(msg.values[key]);
      }
      setText('setFolder', msg.folder || '(no folder open)');
      setText(
        'setFolderNote',
        msg.folderCount > 1
          ? 'This window has ' + msg.folderCount + ' folders. Mochiii reads, searches and edits only this one; choose another to move it.'
          : 'Mochiii reads, searches and edits inside this folder.'
      );
      const machine = document.getElementById('setMachine');
      if (machine) {
        machine.replaceChildren(
          ...(msg.machine || []).map((/** @type {any} */ m) => {
            const row = el('div', 'row');
            const head = el('div', 'row-head');
            head.append(el('span', 'row-title', m.label), el('span', 'row-spacer'));
            head.append(act('Edit in Settings', () => vscode.postMessage({ type: 'page:openSetting', key: m.key })));
            row.append(head);
            row.append(el('div', 'row-sub', m.value ? m.value : 'not set — ' + m.what));
            return row;
          })
        );
      }
      setText('setVersion', msg.version || '');
      setText('setDaemonVersion', msg.daemonVersion || 'not reachable');
      fillList('setPrivacyNotes', msg.privacy || []);
    }

    /** @param {string} page @param {string} text */
    function pageNotice(page, text) {
      const id = page === 'settings' ? 'setDefaultModelNote' : '';
      if (id) setText(id, text);
    }

    /**
     * handle takes one message from the host. Returns false for one that is not
     * a page's, so main.js handles it as before.
     * @param {any} msg
     */
    function handle(msg) {
      const type = msg && typeof msg.type === 'string' ? msg.type : '';
      if (type.indexOf('mk:') === 0) {
        if (market) market.receive(Object.assign({}, msg, { type: type.slice(3) }));
        if (type === 'mk:catalog' && pendingSearch && market) {
          market.receive({ type: 'search', q: pendingSearch });
          pendingSearch = '';
        }
        return true;
      }
      switch (type) {
        case 'page:open':
          open(String(msg.page), { tab: msg.tab, sub: msg.sub, search: msg.search });
          return true;
        case 'page:account':
          renderAccount(msg.view || {});
          return true;
        case 'page:settings':
          renderSettings(msg);
          return true;
        case 'page:mcp':
          renderMcp(msg);
          return true;
        case 'page:specs':
          renderSpecs(msg);
          return true;
        case 'page:tools':
          renderTools(msg);
          return true;
        case 'page:marketDeclined':
          // The notice was declined: back to the list, and the next visit asks again.
          market = null;
          {
            const root = document.getElementById('marketRoot');
            if (root) root.replaceChildren();
          }
          selectTab('mcp', 'installed');
          return true;
        case 'page:notice':
          pageNotice(String(msg.page), String(msg.text || ''));
          return true;
      }
      return false;
    }

    return {
      handle,
      open,
      close,
      isOpen: () => current !== '',
    };
  }

  window.MochiiiPages = { init };
})();
