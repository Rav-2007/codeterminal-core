// THE MCP MARKETPLACE WEBVIEW (src/mcpMarketplace.ts is its host).
//
// What this file may do is deliberately little. It never holds the catalog --
// the host answers each search a page at a time -- and it never says what to
// run: "Add" posts a server's id, and the host builds the request from its own
// checked copy. Everything it draws came from a catalog somebody else serves,
// so all of it goes in through textContent, never as markup.
//
// One function scope: this file and main.js are type-checked as one project
// (media/jsconfig.json), and two scripts' top-level names would collide.
//
// A FACTORY SINCE STAGE 8: window.MochiiiMarketplace.mount(root, post) draws
// the marketplace into `root` and sends its requests through `post`, so the
// chat's Customize page shows the same marketplace its editor tab does. On
// its own page (#app, no chat) it mounts itself, as before.

(function () {
  'use strict';

  const PAGE = 40;

  // WHERE THE BUNDLED LOGOS ARE: media/mcp-icons beside this very script. Read
  // now, while the script runs -- document.currentScript is null afterwards. Both
  // hosts (the editor tab and the chat's Customize page) load this file with a
  // plain <script src>, so the folder is a webview resource of the extension and
  // nothing here can name another host. A card carries a slug, never a URL, and
  // the slug is checked once more here before it becomes part of one.
  const ICON_BASE = (() => {
    const src = document.currentScript instanceof HTMLScriptElement ? document.currentScript.src : '';
    return src ? src.slice(0, src.lastIndexOf('/') + 1) + 'mcp-icons/' : '';
  })();
  const ICON_SLUG = /^[a-z0-9-]{1,32}$/;

  /**
   * @param {HTMLElement} app where to draw
   * @param {(msg: any) => void} post how a request reaches the host
   * @returns {{ receive: (msg: any) => void }}
   */
  function mount(app, post) {

  /**
   * @typedef {{ name: string, required: boolean, secret: boolean, description?: string }} EnvView
   * @typedef {{ id: string, name: string, description: string, publisher: string, namespace?: string,
   *   tier: 'verified' | 'community', categories: string[], pkg: string, runtime: string, commandLine: string,
   *   env: EnvView[], hasHomepage: boolean, hasPackageUrl: boolean, icon?: string,
   *   installed: '' | 'added' | 'disabled' | 'taken', tools?: string }} Card
   */

  const state = {
    q: '',
    /** @type {'verified' | 'community' | 'all'} */
    tier: 'verified',
    category: '',
    /** @type {Card[]} */
    items: [],
    total: 0,
    verifiedCount: 0,
    communityCount: 0,
    loadingCommunity: false,
    /** @type {Set<string>} */
    busy: new Set(),
    /** The server a mochiii.in link opened, kept first. @type {Card | null} */
    focus: null,
  };

  /**
   * @param {string} tag
   * @param {string} [className]
   * @param {string} [text]
   * @returns {HTMLElement}
   */
  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) {
      node.className = className;
    }
    if (text !== undefined) {
      node.textContent = text;
    }
    return node;
  }

  /**
   * @param {string} label
   * @param {string} className
   * @param {() => void} onClick
   * @returns {HTMLButtonElement}
   */
  function button(label, className, onClick) {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = className;
    b.textContent = label;
    b.addEventListener('click', onClick);
    return b;
  }

  // ---- the page shell, built once -------------------------------------------

  app.append(
    el('h1', '', 'MCP Marketplace'),
    el(
      'p',
      'sub',
      "Servers you can give Mochiii's agent. Each runs on your machine with your privileges: before one is added, Mochiii " +
        "shows exactly what will run and asks you to type its name, and each of its tools asks before it runs."
    )
  );

  const bar = el('div', 'bar');
  const search = document.createElement('input');
  search.type = 'search';
  search.placeholder = 'Search servers, publishers, packages…';
  search.setAttribute('aria-label', 'Search MCP servers');

  const tabs = el('div', 'tabs');
  tabs.setAttribute('role', 'tablist');
  /** @type {Record<string, HTMLButtonElement>} */
  const tabButtons = {};
  for (const tier of /** @type {('verified' | 'community' | 'all')[]} */ (['verified', 'community', 'all'])) {
    const b = button('', '', () => {
      if (state.tier !== tier) {
        state.tier = tier;
        drawTabs();
        query(0, PAGE);
      }
    });
    b.setAttribute('role', 'tab');
    tabButtons[tier] = b;
    tabs.append(b);
  }

  const category = document.createElement('select');
  category.setAttribute('aria-label', 'Category');

  bar.append(
    search,
    tabs,
    category,
    button('Refresh', 'btn secondary', () => post({ type: 'refresh' })),
    button('Installed servers', 'btn secondary', () => post({ type: 'manage' }))
  );

  const notices = el('div', '');
  const focusBox = el('div', 'focusbox');
  const tierNote = el('p', 'safety');
  const count = el('p', 'count');
  const grid = el('div', 'grid');
  const more = el('div', 'more');
  const foot = el('p', 'foot');
  app.append(bar, notices, focusBox, tierNote, count, grid, more, foot);

  function drawTabs() {
    tabButtons.verified.textContent = `Verified (${state.verifiedCount})`;
    tabButtons.community.textContent = `Community (${state.communityCount.toLocaleString()})`;
    tabButtons.all.textContent = 'All';
    for (const tier of Object.keys(tabButtons)) {
      tabButtons[tier].setAttribute('aria-selected', String(tier === state.tier));
    }
    // Categories are Mochiii's own labels on the verified tier; the community
    // tier is not sorted into them, so the filter would only ever empty it.
    category.hidden = state.tier === 'community';
    tierNote.textContent =
      state.tier === 'verified'
        ? 'Verified: official servers Mochiii chose and reviewed.'
        : 'Community servers are imported from the public MCP Registry, whose publishers verified their names. Nobody at ' +
          'Mochiii has read their code: check the package before you add one.';
  }

  /**
   * @param {number} offset
   * @param {number} limit
   */
  function query(offset, limit) {
    if (state.tier !== 'verified' && state.communityCount > 0 && offset === 0) {
      state.loadingCommunity = true;
    }
    post({ type: 'query', q: state.q, tier: state.tier, category: state.category, offset, limit });
  }

  // window's timer, not Node's: this runs in a webview, and @types/node (visible
  // to the type-check) would otherwise type the handle as a NodeJS.Timeout.
  let debounce = 0;
  search.addEventListener('input', () => {
    window.clearTimeout(debounce);
    debounce = window.setTimeout(() => {
      state.q = search.value;
      query(0, PAGE);
    }, 180);
  });
  category.addEventListener('change', () => {
    state.category = category.value;
    query(0, PAGE);
  });

  // ---- cards -----------------------------------------------------------------

  /**
   * placeholder is the logo a server gets when it has none: its first letter on
   * the theme's badge colours. Decorative -- the name is right beside it.
   * @param {Card} c
   * @returns {HTMLElement}
   */
  function placeholder(c) {
    const ph = el('span', 'ico ph', (Array.from(c.name.trim())[0] || '?').toUpperCase());
    ph.setAttribute('aria-hidden', 'true');
    return ph;
  }

  /**
   * logo is the bundled image for a card's icon slug, or the placeholder: when the
   * card names none, or when the file will not load.
   * @param {Card} c
   * @returns {HTMLElement}
   */
  function logo(c) {
    if (!ICON_BASE || typeof c.icon !== 'string' || !ICON_SLUG.test(c.icon)) {
      return placeholder(c);
    }
    const img = document.createElement('img');
    img.className = 'ico';
    img.alt = '';
    img.src = `${ICON_BASE}${c.icon}.svg`;
    img.addEventListener('error', () => img.replaceWith(placeholder(c)), { once: true });
    return img;
  }

  /**
   * @param {Card} c
   * @param {boolean} [focused]
   * @returns {HTMLElement}
   */
  function card(c, focused) {
    const node = el('article', focused ? 'card focused' : 'card');
    node.setAttribute('aria-label', `${c.name}, ${c.tier === 'verified' ? 'verified' : 'community'} server by ${c.publisher}`);

    const head = el('div', 'head');
    const title = el('div', '');
    title.append(el('h2', '', c.name), el('div', 'pub', c.publisher));
    const who = el('div', 'who');
    who.append(logo(c), title);
    head.append(who, el('span', c.tier === 'verified' ? 'tier verified' : 'tier', c.tier === 'verified' ? 'Verified' : 'Community'));
    node.append(head, el('p', 'desc', c.description));

    if (c.categories.length > 0) {
      node.append(el('div', 'meta', c.categories.join(' · ')));
    }
    node.append(el('div', 'meta', `${c.pkg} — needs ${c.runtime}`));
    // The host sets it only from what the daemon listed (cardFor); no count is shown otherwise.
    if (c.tools) {
      node.append(el('div', 'meta', `Offers ${c.tools}`));
    }

    if (c.env.length > 0) {
      const env = el('div', 'env');
      env.setAttribute('aria-label', 'Variables it reads');
      for (const e of c.env) {
        const chip = el('span', e.required ? 'req' : '', `${e.secret ? '🔑 ' : ''}${e.name}${e.required ? ' (required)' : ''}`);
        if (e.description) {
          chip.title = e.description;
        }
        env.append(chip);
      }
      node.append(env);
    }

    const cmd = el('code', 'cmd', c.commandLine);
    cmd.title = 'Exactly what will run';
    node.append(cmd);

    if (c.tier === 'community' && c.namespace) {
      node.append(el('p', 'prov', `In the MCP Registry as ${c.namespace}. Not reviewed by Mochiii.`));
    }

    const actions = el('div', 'actions');
    const busy = state.busy.has(c.id);
    const label =
      busy ? 'Adding…' : c.installed === 'added' ? 'Added' : c.installed === 'disabled' ? 'Added (disabled)' : c.installed === 'taken' ? 'Name in use' : 'Add';
    const add = button(label, 'btn', () => post({ type: 'install', id: c.id }));
    add.disabled = busy || c.installed !== '';
    if (c.installed === 'taken') {
      add.title = `A server named ${c.id} is already defined in the daemon's models.json`;
    }
    actions.append(add);
    if (c.hasHomepage) {
      actions.append(button('Homepage', 'link', () => post({ type: 'open', id: c.id, which: 'homepage' })));
    }
    if (c.hasPackageUrl) {
      actions.append(button('Package', 'link', () => post({ type: 'open', id: c.id, which: 'package' })));
    }
    node.append(actions);
    return node;
  }

  function drawResults() {
    grid.replaceChildren(...state.items.map((c) => card(c)));
    count.textContent = state.loadingCommunity
      ? `Loading ${state.communityCount.toLocaleString()} community servers…`
      : `${state.total.toLocaleString()} server${state.total === 1 ? '' : 's'}`;
    more.replaceChildren();
    if (state.items.length < state.total) {
      more.append(
        button(`Show more (${(state.total - state.items.length).toLocaleString()} left)`, 'btn secondary', () =>
          query(state.items.length, PAGE)
        )
      );
    }
  }

  function drawFocus() {
    if (state.focus) {
      focusBox.replaceChildren(el('h3', '', 'Opened from mochiii.in'), card(state.focus, true));
    }
  }

  /**
   * @param {string} kind
   * @param {string} text
   */
  function notice(kind, text) {
    const n = el('div', kind === 'error' ? 'notice error' : 'notice', text);
    n.setAttribute('role', kind === 'error' ? 'alert' : 'status');
    notices.append(n);
  }

  // ---- messages from the host --------------------------------------------------

  /** @param {any} msg */
  function receive(msg) {
    switch (msg && msg.type) {
      case 'loading':
        if (msg.what === 'catalog') {
          notices.replaceChildren();
          count.textContent = 'Loading the catalog…';
        }
        return;
      case 'notice':
        notice(String(msg.kind), String(msg.text));
        return;
      case 'catalog': {
        state.verifiedCount = msg.verifiedCount;
        state.communityCount = msg.communityCount;
        const options = [['', 'All categories'], ...msg.categories.map((/** @type {string} */ c) => [c, c])];
        category.replaceChildren(
          ...options.map(([value, label]) => {
            const o = document.createElement('option');
            o.value = value;
            o.textContent = label;
            return o;
          })
        );
        category.value = state.category;
        drawTabs();
        foot.textContent =
          `Catalog from ${msg.source}` +
          (msg.generatedAt ? `, generated ${new Date(msg.generatedAt).toLocaleDateString()}` : '') +
          (msg.rejected > 0 ? `. ${msg.rejected} entr${msg.rejected === 1 ? 'y' : 'ies'} broke a rule and are not shown.` : '.');
        return;
      }
      case 'results': {
        const q = msg.query;
        // An answer to a search the user has since changed is not drawn.
        if (q.q !== state.q || q.tier !== state.tier || q.category !== state.category) {
          return;
        }
        state.loadingCommunity = false;
        state.total = msg.total;
        state.items = q.offset === 0 ? msg.items : state.items.slice(0, q.offset).concat(msg.items);
        const fresh = state.focus && state.items.find((c) => c.id === (state.focus && state.focus.id));
        if (fresh) {
          state.focus = fresh;
        }
        drawResults();
        drawFocus();
        return;
      }
      case 'stateChanged':
        query(0, Math.max(PAGE, state.items.length));
        return;
      case 'search':
        // /mcp-server <words> in the chat. Across both tiers: someone who typed
        // a name wants it found, not only if Mochiii happened to verify it.
        window.clearTimeout(debounce);
        search.value = String(msg.q);
        state.q = search.value;
        state.tier = 'all';
        state.category = '';
        category.value = '';
        drawTabs();
        query(0, PAGE);
        return;
      case 'focus':
        state.focus = msg.item;
        drawFocus();
        focusBox.scrollIntoView({ block: 'start' });
        return;
      case 'busy':
        if (msg.busy) {
          state.busy.add(msg.id);
        } else {
          state.busy.delete(msg.id);
        }
        drawResults();
        drawFocus();
        return;
    }
  }

  drawTabs();
  post({ type: 'ready' });
  return { receive };
  }

  window.MochiiiMarketplace = { mount };

  // Its own editor tab: the page has #app and no chat.
  const standalone = document.getElementById('app');
  if (standalone && !document.getElementById('pageCustomize')) {
    const vscode = acquireVsCodeApi();
    const market = mount(standalone, (msg) => vscode.postMessage(msg));
    window.addEventListener('message', (event) => market.receive(event.data));
  }
})();
