// THE CHAT'S PAGES (Stage 8): Customize, Account and Settings.
//
// Static markup, shown and filled by media/pages.js. It lives here, not in
// the webview scripts, for the rule accessibility.test.ts holds the webview
// to: no script builds markup from strings. Everything dynamic -- a server's
// name, a provider's address, a model id -- is added by pages.js through
// textContent into the empty containers below.
//
// A page REPLACES the conversation while it is open (body.page-open hides the
// transcript, the notices and the composer) rather than floating over it: in a
// side bar a few hundred pixels wide there is no room for both, and a page you
// can half-see the chat behind is a page you are never sure you have left.

const svg = (body: string): string =>
  `<svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">${body}</svg>`;

/** Icons, drawn here: the CSP has no font-src, so codicons cannot load. */
export const ICONS = {
  customize: svg('<rect x="2.5" y="2.5" width="4.5" height="4.5" rx="1"/><rect x="9" y="2.5" width="4.5" height="4.5" rx="1"/><rect x="2.5" y="9" width="4.5" height="4.5" rx="1"/><path d="M11.25 9v4.5M9 11.25h4.5"/>'),
  account: svg('<circle cx="8" cy="5.6" r="2.6"/><path d="M2.8 13.6c.9-2.5 2.9-3.7 5.2-3.7s4.3 1.2 5.2 3.7"/>'),
  settings: svg('<circle cx="8" cy="8" r="2.1"/><path d="M8 1.8v1.7M8 12.5v1.7M1.8 8h1.7M12.5 8h1.7M3.6 3.6l1.2 1.2M11.2 11.2l1.2 1.2M3.6 12.4l1.2-1.2M11.2 4.8l1.2-1.2"/>'),
  back: svg('<path d="M9.5 3.5 5 8l4.5 4.5"/>'),
  at: svg('<circle cx="8" cy="8" r="2.4"/><path d="M10.4 8v1a1.6 1.6 0 0 0 3.2 0V8a5.6 5.6 0 1 0-2.3 4.5"/>'),
  info: svg('<circle cx="8" cy="8" r="6"/><path d="M8 7.2v3.6M8 5.1v.1"/>'),
  // Stage 9: the header, the composer and the cards (docs: the two mockup sheets).
  plus: svg('<path d="M8 3v10M3 8h10"/>'),
  history: svg('<circle cx="8" cy="8" r="5.8"/><path d="M8 4.8V8l2.2 1.4"/>'),
  more: svg('<circle cx="3.5" cy="8" r=".9"/><circle cx="8" cy="8" r=".9"/><circle cx="12.5" cy="8" r=".9"/>'),
  star: svg('<path d="M8 2.2l1.8 3.6 4 .6-2.9 2.8.7 4L8 11.3l-3.6 1.9.7-4L2.2 6.4l4-.6z"/>'),
  send: svg('<path d="M8 13V3.5M3.8 7.6 8 3.4l4.2 4.2"/>'),
  stop: svg('<rect x="4" y="4" width="8" height="8" rx="1.5"/>'),
  copy: svg('<rect x="5.5" y="5.5" width="8" height="8" rx="1.5"/><path d="M10.5 5.5V3.8A1.3 1.3 0 0 0 9.2 2.5H3.8a1.3 1.3 0 0 0-1.3 1.3v5.4a1.3 1.3 0 0 0 1.3 1.3h1.7"/>'),
  check: svg('<path d="m3.5 8.3 2.9 2.9 6.1-6.4"/>'),
  chevron: svg('<path d="m4.5 6 3.5 3.5L11.5 6"/>'),
  file: svg('<path d="M4 1.8h5l3 3v8.4a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V2.8a1 1 0 0 1 1-1z"/><path d="M9 1.8v3h3"/>'),
  selection: svg('<path d="M2.5 4.5v-2h2M11.5 2.5h2v2M13.5 11.5v2h-2M4.5 13.5h-2v-2"/><path d="M5.5 6h5M5.5 8h5M5.5 10h3"/>'),
  image: svg('<rect x="2" y="2.8" width="12" height="10.4" rx="1.5"/><circle cx="5.6" cy="6.2" r="1.2"/><path d="m2.5 12 3.6-3.6 2.4 2.4 1.8-1.8 3.2 3.2"/>'),
  warning: svg('<path d="M8 2.2 14.2 13H1.8L8 2.2z"/><path d="M8 6.4v3M8 11.2v.1"/>'),
  paperclip: svg('<path d="M12.6 7.4 7.8 12.2a3 3 0 0 1-4.3-4.3l5-5a2 2 0 0 1 2.8 2.9l-5 4.9a1 1 0 0 1-1.4-1.4l4.6-4.6"/>'),
  search: svg('<circle cx="7" cy="7" r="4.3"/><path d="m10.2 10.2 3.3 3.3"/>'),
  terminal: svg('<rect x="1.8" y="2.8" width="12.4" height="10.4" rx="1.5"/><path d="m4.5 6.5 2 1.8-2 1.8M8.5 10.2h3"/>'),
  pencil: svg('<path d="M10.8 2.6a1.6 1.6 0 0 1 2.3 2.3L5.6 12.4l-3 .7.7-3z"/>'),
  bug: svg('<rect x="4.5" y="5" width="7" height="8.5" rx="3.5"/><path d="M6 5V4a2 2 0 0 1 4 0v1M2 9h2.5M11.5 9H14M2.8 5.6l1.9 1.2M13.2 5.6l-1.9 1.2M2.8 12.6l1.9-1.2M13.2 12.6l-1.9-1.2M8 7.5v5"/>'),
  flask: svg('<path d="M6 1.8h4M6.6 1.8v4.4L2.6 12.6a1.2 1.2 0 0 0 1 1.8h8.8a1.2 1.2 0 0 0 1-1.8L9.4 6.2V1.8M4.6 9.6h6.8"/>'),
  spark: svg('<path d="M8 1.8 9.4 6.6 14.2 8 9.4 9.4 8 14.2 6.6 9.4 1.8 8l4.8-1.4z"/>'),
  code: svg('<path d="m5.5 4.5-3.5 3.5 3.5 3.5M10.5 4.5 14 8l-3.5 3.5M9.2 2.8 6.8 13.2"/>'),
  key: svg('<circle cx="5.2" cy="10.8" r="2.7"/><path d="m7.2 8.8 6-6M11 5l1.8 1.8M12.5 3.5l1.3 1.3"/>'),
  globe: svg('<circle cx="8" cy="8" r="6"/><path d="M2 8h12M8 2c1.8 1.7 2.6 3.7 2.6 6S9.8 12.3 8 14C6.2 12.3 5.4 10.3 5.4 8S6.2 3.7 8 2z"/>'),
  dot: svg('<circle cx="8" cy="8" r="4.5"/>'),
};

/** The three ⋯ menu items that open a page (Stage 9: they were header icons). */
export function headerPageButtons(): string {
  return `<button type="button" id="customizeBtn" class="menu-item page-btn" title="Customize: MCP servers, specs, tools" aria-label="Customize" aria-controls="pageCustomize" aria-expanded="false">${ICONS.customize}<span class="mi-text"><span class="mi-name">Customize</span><span class="mi-desc">MCP servers, specs, tools, skills</span></span></button>
      <button type="button" id="accountBtn" class="menu-item page-btn" title="Account: provider, key and model" aria-label="Account" aria-controls="pageAccount" aria-expanded="false">${ICONS.account}<span class="mi-text"><span class="mi-name">Account</span><span class="mi-desc">Provider, API key and spend</span></span></button>
      <button type="button" id="settingsBtn" class="menu-item page-btn" title="Settings" aria-label="Settings" aria-controls="pageSettings" aria-expanded="false">${ICONS.settings}<span class="mi-text"><span class="mi-name">Settings</span><span class="mi-desc">Model, behaviour, workspace, privacy</span></span></button>`;
}

function pageHead(id: string, title: string, subtitle: string): string {
  return `<div class="page-head">
      <button type="button" class="page-back" data-close-page aria-label="Back to the chat" title="Back to the chat">${ICONS.back}<span>Chat</span></button>
      <div class="page-titles">
        <h2 id="${id}Title" tabindex="-1">${title}</h2>
        <p class="page-sub">${subtitle}</p>
      </div>
    </div>`;
}

function tabs(group: string, label: string, items: [string, string][]): string {
  return `<div class="seg" role="tablist" aria-label="${label}" data-tabs="${group}">
      ${items
        .map(
          ([key, text], i) =>
            `<button type="button" role="tab" id="${group}Tab-${key}" data-tab="${key}" aria-controls="${group}Panel-${key}" aria-selected="${i === 0}" tabindex="${i === 0 ? 0 : -1}">${text}</button>`,
        )
        .join('\n      ')}
    </div>`;
}

function customizePage(): string {
  return `<section id="pageCustomize" class="page" hidden role="region" aria-labelledby="pageCustomizeTitle">
    ${pageHead('pageCustomize', 'Customize', 'What Mochiii’s agent can reach, and the instructions it works from.')}
    ${tabs('cust', 'Customize', [
      ['mcp', 'MCP'],
      ['specs', 'Specs'],
      ['tools', 'Tools'],
      ['skills', 'Skills'],
    ])}
    <!-- MCP: ONE LIST, ONE WAY TO ADD (UI audit, step 4). There were
         Installed / Marketplace sub-tabs, an "Add Server" button, "Browse the
         Marketplace" and "Add a server by hand": four routes to two actions.
         The Add menu holds the two, and the marketplace opens as its own view
         with a way back. -->
    <div id="custPanel-mcp" class="page-panel" role="tabpanel" aria-labelledby="custTab-mcp">
      <div id="mcpPanel-installed">
        <div class="mcp-head">
          <h3>MCP servers</h3>
          <div class="mcp-add">
            <button type="button" class="pg-btn primary small" id="mcpAddBtn" aria-haspopup="menu" aria-expanded="false" aria-controls="mcpAddMenu">${ICONS.plus}<span>Add server</span></button>
            <div id="mcpAddMenu" class="pop-menu mcp-add-menu" role="menu" aria-label="Add an MCP server" hidden>
              <button type="button" class="menu-item" role="menuitem" data-mcp-browse><span class="mi-text"><span class="mi-name">From the Marketplace</span><span class="mi-desc">Browse servers you can add</span></span></button>
              <button type="button" class="menu-item" role="menuitem" data-page-command="mcpAdd"><span class="mi-text"><span class="mi-name">Your own server</span><span class="mi-desc">A command, a URL or pasted JSON</span></span></button>
            </div>
          </div>
        </div>
        <p id="mcpNotice" class="page-note" hidden></p>
        <div id="mcpInstalledList" class="rows" aria-live="polite"></div>
        <div id="mcpEmpty" class="empty" hidden>
          <p>No MCP servers yet. An MCP server gives Mochiii’s agent new tools: web search, browsers, databases, clouds.</p>
        </div>
        <div class="page-actions">
          <button type="button" class="link-btn" data-page-command="mcpShowTools">Show MCP tools</button>
        </div>
        <p class="page-note">A server runs on your machine with your privileges. Mochiii shows exactly what will run and asks you to type its name before adding it, and each of its tools asks before it runs.</p>
      </div>
      <div id="mcpPanel-market" hidden>
        <button type="button" class="link-btn mcp-back" data-mcp-installed>‹ Installed servers</button>
        <div id="marketRoot" class="market-root"></div>
        <div class="page-actions">
          <button type="button" class="link-btn" data-page-command="marketInEditor">Open the marketplace in an editor tab</button>
        </div>
      </div>
    </div>
    <div id="custPanel-specs" class="page-panel" role="tabpanel" aria-labelledby="custTab-specs" hidden>
      <p class="page-note">A spec is a plan in <code>specs/</code> that Mochiii writes with you, then builds and checks against. The active one rides along with every question.</p>
      <div id="specList" class="rows" aria-live="polite"></div>
      <div class="page-actions">
        <button type="button" class="pg-btn primary" data-spec-new>New spec…</button>
      </div>
    </div>
    <div id="custPanel-tools" class="page-panel" role="tabpanel" aria-labelledby="custTab-tools" hidden>
      <p class="page-note">The tools Mochiii’s agent may call, and what each needs from you: <b>allow</b> runs it, <b>ask</b> waits for your yes, <b>off</b> never offers it.</p>
      <p id="toolStatus" class="empty" role="status">Loading tools…</p>
      <div id="toolList" class="rows" aria-live="polite"></div>
      <p id="toolSource" class="page-note"></p>
    </div>
    <div id="custPanel-skills" class="page-panel" role="tabpanel" aria-labelledby="custTab-skills" hidden>
      <div class="soon-card">
        <h3>Skills are coming</h3>
        <p>A skill is a set of instructions Mochiii loads only when a task needs it — how your team writes migrations, how to cut a release — without filling every question with it.</p>
        <p>They will install from mochiii.in like MCP servers, and live in your user folder; a repository’s own skills will be off until you turn them on.</p>
      </div>
    </div>
  </section>`;
}

function accountPage(): string {
  return `<section id="pageAccount" class="page" hidden role="region" aria-labelledby="pageAccountTitle">
    ${pageHead('pageAccount', 'Account', 'Who Mochiii talks to on your behalf. Your key is kept on this machine and sent to its provider.')}
    <div class="conn-card">
      <div id="acctBadge" class="conn-badge" aria-hidden="true">?</div>
      <div class="conn-main" aria-live="polite">
        <div class="conn-provider"><span id="acctProvider">Checking…</span> <span id="acctDot" class="dot" role="img" aria-label="status unknown"></span></div>
        <div id="acctBase" class="conn-base"></div>
      </div>
      <dl class="kv">
        <dt>Key</dt><dd><span id="acctKey"></span><span id="acctKeyMore" class="kv-more"></span></dd>
        <dt>Model</dt><dd><span id="acctModel"></span><span id="acctModelMore" class="kv-more"></span></dd>
        <dt>Daemon</dt><dd id="acctDaemon"></dd>
      </dl>
    </div>
    <p id="acctNote" class="page-note acct-note" hidden><span id="acctNoteText"></span> <button type="button" id="acctRetry" class="pg-btn" data-page-refresh>Retry</button></p>
    <div id="acctSpend" class="spend" hidden>
      <h3>Spend</h3>
      <div id="acctSpendRows"></div>
      <p id="acctSpendNote" class="page-note"></p>
    </div>
    <!-- One primary; Forget is a quiet link in the danger colour -- it asks
         first (forgetKey.ts), and it is not what most visits are for. The model
         is shown in the card and changed from its chip under the prompt, so
         there is no second button for it here. -->
    <div class="page-actions">
      <button type="button" class="pg-btn primary" data-page-command="connect">Change provider or key</button>
    </div>
    <div class="page-actions quiet">
      <button type="button" class="link-btn" data-page-refresh>Refresh</button>
      <button type="button" class="link-btn danger" data-page-command="forgetKey">Forget key</button>
    </div>
    <div id="acctPrivacyBox" class="sub-block" hidden>
      <h3>Privacy</h3>
      <ul id="acctPrivacy" class="plain-list"></ul>
    </div>
  </section>`;
}

const SETTINGS_SECTIONS: [string, string][] = [
  ['model', 'Model'],
  ['behaviour', 'Behaviour'],
  ['workspace', 'Workspace'],
  ['privacy', 'Privacy'],
  ['advanced', 'Advanced'],
  ['about', 'About'],
];

function settingsPage(): string {
  return `<section id="pageSettings" class="page" hidden role="region" aria-labelledby="pageSettingsTitle">
    ${pageHead('pageSettings', 'Settings', 'How Mochiii behaves in this editor.')}
    <!-- ONE PAGE OF LABELLED SECTIONS (UI audit, step 4). It was a rail of six
         unlabelled icons beside one section at a time, in a panel a few hundred
         pixels wide. The names at the top jump to their section. -->
    <div class="settings">
      <nav class="set-nav" aria-label="Settings sections">
        ${SETTINGS_SECTIONS.map(([key, label]) => `<button type="button" class="link-btn" data-set-jump="${key}">${label}</button>`).join('\n        ')}
      </nav>
      <div class="settings-body">
        <section id="setPanel-model" class="set-section" aria-labelledby="setHead-model">
          <h3 id="setHead-model">Model</h3>
          <div class="field">
            <span class="field-label">Provider</span>
            <span id="setProvider" class="field-value"></span>
            <button type="button" class="link-btn" data-open-page="account">Account ›</button>
          </div>
          <label class="field" for="setDefaultModel">
            <span class="field-label">Default model</span>
            <select id="setDefaultModel"></select>
          </label>
          <p id="setDefaultModelNote" class="page-note"></p>
        </section>
        <section id="setPanel-behaviour" class="set-section" aria-labelledby="setHead-behaviour">
          <h3 id="setHead-behaviour">Behaviour</h3>
          <label class="field" for="setDefaultMode">
            <span class="field-label">Mode for a new chat</span>
            <select id="setDefaultMode" data-setting="defaultMode">
              <option value="auto">Auto — edits apply as they arrive</option>
              <option value="manual">Manual — every edit waits for Apply</option>
              <option value="plan">Plan — reads and plans, edits nothing</option>
            </select>
          </label>
          <label class="field" for="setToolActivity">
            <span class="field-label">Tool steps in the chat</span>
            <select id="setToolActivity" data-setting="toolActivity">
              <option value="compact">Folded to one line</option>
              <option value="expanded">Open, one row per step</option>
            </select>
          </label>
          <label class="check" for="setEditorContext">
            <input type="checkbox" id="setEditorContext" data-setting="sendEditorContext" />
            <span>Send the open file, cursor and selection with each question</span>
          </label>
        </section>
        <section id="setPanel-workspace" class="set-section" aria-labelledby="setHead-workspace">
          <h3 id="setHead-workspace">Workspace</h3>
          <div class="field">
            <span class="field-label">Working folder</span>
            <span id="setFolder" class="field-value"></span>
          </div>
          <p id="setFolderNote" class="page-note"></p>
          <div class="page-actions">
            <button type="button" class="pg-btn" data-page-command="chooseFolder">Choose working folder…</button>
          </div>
        </section>
        <section id="setPanel-privacy" class="set-section" aria-labelledby="setHead-privacy">
          <h3 id="setHead-privacy">Privacy and data</h3>
          <label class="check" for="setHistory">
            <input type="checkbox" id="setHistory" data-setting="history.autoSave" />
            <span>Keep every chat in History on this machine</span>
          </label>
          <p class="page-note">What leaves this machine: your question, the conversation so far, the parts of your code Mochiii chose to show, and the open file when that is on. They go to the provider on the Account page, and nowhere else.</p>
          <ul id="setPrivacyNotes" class="plain-list"></ul>
        </section>
        <section id="setPanel-advanced" class="set-section" aria-labelledby="setHead-advanced">
          <h3 id="setHead-advanced">Advanced</h3>
          <p class="page-note">These name where your questions go or what may run, so they are changed in VS Code’s own Settings, for this machine only — never from this page, and never by a repository.</p>
          <div id="setMachine" class="rows"></div>
        </section>
        <section id="setPanel-about" class="set-section" aria-labelledby="setHead-about">
          <h3 id="setHead-about">About Mochiii</h3>
          <dl class="kv">
            <dt>Extension</dt><dd id="setVersion"></dd>
            <dt>Daemon</dt><dd id="setDaemonVersion"></dd>
          </dl>
          <div class="page-actions">
            <button type="button" class="pg-btn ghost" data-page-link="site">mochiii.in</button>
            <button type="button" class="pg-btn ghost" data-page-link="issues">Report a problem</button>
            <button type="button" class="pg-btn ghost" data-page-link="docs">Documentation</button>
          </div>
          <div class="page-actions">
            <button type="button" class="pg-btn" data-page-command="daemonLog">Show the daemon log</button>
            <button type="button" class="pg-btn" data-page-command="restartDaemon">Restart the daemon</button>
          </div>
        </section>
      </div>
    </div>
  </section>`;
}

/**
 * THE /mcp DIALOG, the shape of Claude Code's: every server in one list, by
 * where it comes from, each with its state; open one for what can be done to
 * it -- Authenticate, Set key, Check connection, Disable. Filled by pages.js
 * from the same page:mcp message as Customize > MCP, and acting through the
 * same mcp:row table (rules.ts), so the two never disagree.
 */
function mcpDialog(): string {
  return `<div class="mcp-dlg-backdrop" id="mcpDlg" hidden>
    <div class="mcp-dlg" role="dialog" aria-modal="true" aria-labelledby="mcpDlgTitle">
      <div class="mcp-dlg-head">
        <h2 id="mcpDlgTitle" tabindex="-1">MCP servers</h2>
        <button type="button" class="mcp-dlg-x" id="mcpDlgClose" aria-label="Close" title="Close (Esc)">&#x2715;</button>
      </div>
      <div id="mcpDlgList">
        <input type="search" id="mcpDlgFilter" class="mcp-dlg-filter" placeholder="Filter servers…" aria-label="Filter servers" autocomplete="off" spellcheck="false">
        <p class="mcp-dlg-notice" id="mcpDlgNotice" role="status" hidden></p>
        <div id="mcpDlgGroups"></div>
        <div class="mcp-dlg-foot">
          <button type="button" class="mcp-dlg-link" data-mcpdlg="add">Add a server…</button>
          <button type="button" class="mcp-dlg-link" data-mcpdlg="market">Browse the marketplace</button>
        </div>
      </div>
      <div id="mcpDlgDetail" hidden>
        <button type="button" class="mcp-dlg-link mcp-dlg-back" id="mcpDlgBack">&larr; Back to list</button>
        <div class="mcp-dlg-dhead"><span class="mcp-dlg-name" id="mcpDlgName"></span><span class="mcp-pill" id="mcpDlgBadge"></span></div>
        <div class="mcp-dlg-info" id="mcpDlgInfo"></div>
        <div class="mcp-dlg-actions" id="mcpDlgActions"></div>
      </div>
    </div>
  </div>`;
}

/** All three pages and the /mcp dialog, hidden until opened. Placed after the composer. */
export function chatPagesMarkup(): string {
  return `${customizePage()}
  ${accountPage()}
  ${settingsPage()}
  ${mcpDialog()}`;
}

/** The pages' styles. Tokens only (scripts/theme-check.js scans this file). */
export function chatPagesStyles(): string {
  return `
  .header-actions { display: flex; align-items: center; gap: 2px; }
  .header-divider { width: 1px; height: 18px; background: var(--ct-border); margin: 0 4px; }
  .page-btn svg { display: block; }
  .page-btn[aria-expanded="true"] { color: var(--ct-accent); background: var(--ct-accent-soft); }

  body.page-open #transcript,
  body.page-open #notices,
  body.page-open #composer { display: none; }
  .page {
    flex: 1;
    overflow-y: auto;
    padding: 0 16px 24px;
    font-size: 13px;
    min-width: 0;
  }
  .page-head {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 14px 0 10px;
  }
  .page-back {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    font: inherit;
    font-size: 12px;
    color: var(--ct-muted);
    background: transparent;
    border: 1px solid var(--ct-border);
    border-radius: 999px;
    padding: 3px 10px 3px 6px;
    cursor: pointer;
    flex: none;
  }
  .page-back:hover { color: var(--ct-ink); }
  .page-back:focus-visible, .seg button:focus-visible,
  .pg-btn:focus-visible, .link-btn:focus-visible, .row-act:focus-visible {
    outline: 2px solid var(--ct-accent);
    outline-offset: 1px;
  }
  .page-titles { min-width: 0; }
  .page-titles h2 { margin: 0; font-size: 16px; font-weight: 600; outline: none; }
  .page-sub { margin: 2px 0 0; color: var(--ct-muted); font-size: 12px; }
  .page h3 { font-size: 13px; margin: 16px 0 8px; font-weight: 600; }
  .page-note { color: var(--ct-muted); font-size: 12px; line-height: 1.5; margin: 8px 0; }
  .page-note code { font-family: var(--vscode-editor-font-family); }

  /* Segmented tabs: one pill track, the chosen tab lifted onto the surface. */
  .seg {
    display: flex;
    gap: 2px;
    padding: 3px;
    margin: 4px 0 12px;
    border-radius: 10px;
    background: color-mix(in srgb, var(--ct-ink) 7%, transparent);
    overflow-x: auto;
  }
  .seg button {
    flex: 1 1 auto;
    font: inherit;
    font-size: 12px;
    border: none;
    border-radius: 7px;
    padding: 5px 10px;
    background: transparent;
    color: var(--ct-muted);
    cursor: pointer;
    white-space: nowrap;
  }
  .seg button[aria-selected="true"] {
    background: var(--ct-surface);
    color: var(--ct-ink);
    box-shadow: 0 1px 2px color-mix(in srgb, var(--ct-ink) 18%, transparent);
  }

  .pg-btn {
    font: inherit;
    font-size: 12px;
    padding: 5px 12px;
    border-radius: 8px;
    border: 1px solid var(--ct-border);
    background: var(--ct-surface);
    color: var(--ct-ink);
    cursor: pointer;
  }
  .pg-btn:hover { border-color: var(--ct-accent); }
  /* The theme's primary button, border included: High Contrast's is black with
     a light border, and without it the button read as plain text. */
  .pg-btn.primary { background: var(--ct-accent-solid); color: var(--ct-on-accent); border-color: var(--vscode-button-border, transparent); }
  .pg-btn.primary:hover { background: var(--ct-accent-solid-hover); }
  .pg-btn.ghost { background: transparent; }
  .pg-btn:disabled { opacity: 0.55; cursor: default; }
  /* A link is text, so it takes the theme's link colour, not its focus border. */
  .link-btn { font: inherit; font-size: 12px; background: none; border: none; color: var(--ct-accent-strong); cursor: pointer; padding: 0; }
  .page-actions { display: flex; flex-wrap: wrap; gap: 6px; margin: 12px 0; }
  .page-actions.quiet { gap: 16px; margin-top: -2px; }
  .link-btn.danger { color: var(--ct-danger); }
  .link-btn:hover { text-decoration: underline; }
  /* Customize > MCP: one Add button with a menu; the marketplace's way back. */
  .mcp-add { position: relative; }
  /* Two classes: chatLook's .pop-menu width is a share of the button's box. */
  .pop-menu.mcp-add-menu { top: calc(100% + 4px); right: 0; left: auto; width: 240px; }
  /* The marketplace's own "Installed servers" button, where the back link
     above it already is one. (The editor tab has no back link and keeps it.) */
  #marketRoot .manage { display: none; }
  .mcp-back { margin: 2px 0 8px; }

  .rows { display: flex; flex-direction: column; gap: 6px; }
  .row {
    border: 1px solid var(--ct-border);
    border-radius: 10px;
    padding: 9px 10px;
    background: var(--ct-surface);
    min-width: 0;
  }
  .row-head { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .row-title { font-weight: 600; overflow-wrap: anywhere; min-width: 0; }
  .row-sub { color: var(--ct-muted); font-size: 11.5px; margin-top: 3px; overflow-wrap: anywhere; }
  .row-sub code { font-family: var(--vscode-editor-font-family); }
  .row-acts { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 7px; }
  .row-act {
    font: inherit;
    font-size: 11.5px;
    border: 1px solid var(--ct-border);
    border-radius: 999px;
    background: transparent;
    color: var(--ct-ink);
    padding: 1px 9px;
    cursor: pointer;
  }
  .row-act.danger { color: var(--ct-danger); }
  .tag {
    flex: none;
    font-size: 10.5px;
    border-radius: 999px;
    padding: 0 7px;
    border: 1px solid var(--ct-border);
    color: var(--ct-muted);
  }
  .tag.on { color: var(--ct-ok); border-color: color-mix(in srgb, var(--ct-ok) 50%, transparent); }
  .tag.off { color: var(--ct-warn); border-color: color-mix(in srgb, var(--ct-warn) 50%, transparent); }
  /* A server that is on but does not start (no key, or it failed). */
  .tag.warn { color: var(--ct-danger); border-color: color-mix(in srgb, var(--ct-danger) 50%, transparent); }
  .row-sub.mcp-problem { color: var(--ct-danger); white-space: normal; }
  .row-act.primary { color: var(--ct-accent); font-weight: 600; }
  .tag.ask { color: var(--ct-accent); border-color: color-mix(in srgb, var(--ct-accent) 50%, transparent); }
  .row-spacer { flex: 1; }

  /* The /mcp dialog (mcpDialog above). */
  .mcp-dlg-backdrop {
    position: fixed; inset: 0; z-index: 70;
    display: flex; align-items: flex-start; justify-content: center;
    padding: 48px 12px 12px;
    background: color-mix(in srgb, var(--ct-bg) 55%, transparent);
  }
  .mcp-dlg-backdrop[hidden] { display: none; }
  .mcp-dlg {
    width: 100%; max-width: 520px; max-height: calc(100vh - 64px); overflow-y: auto;
    background: var(--ct-popover-bg); color: var(--ct-ink);
    border: 1px solid var(--ct-border); border-radius: 12px; box-shadow: var(--ct-shadow);
    padding: 14px 16px 16px;
  }
  .mcp-dlg-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
  .mcp-dlg-head h2 { margin: 0; font-size: 15px; font-weight: 600; }
  .mcp-dlg-x { font: inherit; background: transparent; border: 0; color: var(--ct-muted); cursor: pointer; padding: 2px 6px; border-radius: 6px; }
  .mcp-dlg-x:hover { color: var(--ct-ink); background: var(--ct-control-bg); }
  .mcp-dlg-filter {
    width: 100%; box-sizing: border-box; font: inherit; padding: 7px 10px; margin-bottom: 10px;
    background: var(--ct-control-bg); color: var(--ct-ink); border: 1px solid var(--ct-control-border); border-radius: 8px;
  }
  .mcp-dlg-notice { color: var(--ct-muted); font-size: 12px; margin: 0 0 8px; }
  .mcp-dlg-group { color: var(--ct-muted); font-size: 11.5px; font-weight: 600; margin: 12px 0 6px; }
  .mcp-dlg-item {
    display: flex; align-items: center; gap: 8px; width: 100%; box-sizing: border-box; text-align: left;
    font: inherit; color: var(--ct-ink); cursor: pointer;
    background: var(--ct-surface); border: 1px solid var(--ct-border); border-radius: 8px;
    padding: 8px 10px; margin-bottom: 6px;
  }
  .mcp-dlg-item:hover, .mcp-dlg-item:focus-visible { border-color: var(--ct-accent); }
  .mcp-dlg-item .mcp-dlg-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .mcp-dlg-name { font-family: var(--vscode-editor-font-family); font-size: 13px; }
  .mcp-pill { flex: none; font-size: 11px; border-radius: 6px; padding: 2px 8px; border: 1px solid var(--ct-border); color: var(--ct-muted); white-space: nowrap; }
  .mcp-pill.on { color: var(--ct-ok); border-color: color-mix(in srgb, var(--ct-ok) 55%, transparent); background: color-mix(in srgb, var(--ct-ok) 12%, transparent); }
  .mcp-pill.warn { color: var(--ct-warn); border-color: color-mix(in srgb, var(--ct-warn) 55%, transparent); background: color-mix(in srgb, var(--ct-warn) 12%, transparent); }
  .mcp-pill.bad { color: var(--ct-danger); border-color: color-mix(in srgb, var(--ct-danger) 55%, transparent); background: color-mix(in srgb, var(--ct-danger) 12%, transparent); }
  .mcp-dlg-foot { display: flex; flex-wrap: wrap; gap: 14px; margin-top: 12px; padding-top: 10px; border-top: 1px solid var(--ct-border); }
  .mcp-dlg-link { font: inherit; font-size: 12.5px; background: transparent; border: 0; padding: 0; color: var(--ct-accent); cursor: pointer; }
  .mcp-dlg-back { color: var(--ct-muted); margin-bottom: 12px; }
  .mcp-dlg-dhead { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-bottom: 8px; }
  .mcp-dlg-dhead .mcp-dlg-name { font-size: 15px; font-weight: 600; overflow-wrap: anywhere; }
  .mcp-dlg-info { color: var(--ct-muted); font-size: 12px; line-height: 1.5; margin-bottom: 12px; }
  .mcp-dlg-info p { margin: 0 0 4px; overflow-wrap: anywhere; }
  .mcp-dlg-info code { font-family: var(--vscode-editor-font-family); }
  .mcp-dlg-info .mcp-problem { color: var(--ct-danger); }
  .mcp-dlg-actions { display: flex; flex-direction: column; gap: 8px; }
  .mcp-dlg-btn {
    font: inherit; width: 100%; padding: 8px 10px; border-radius: 8px; cursor: pointer;
    background: var(--ct-control-bg); color: var(--ct-ink); border: 1px solid var(--ct-control-border);
  }
  .mcp-dlg-btn:hover { border-color: var(--ct-accent); }
  .mcp-dlg-btn.primary { background: var(--ct-accent-solid); color: var(--ct-on-accent); border-color: var(--vscode-button-border, transparent); }
  .mcp-dlg-btn.primary:hover { background: var(--ct-accent-solid-hover); }
  .mcp-dlg-btn.danger { color: var(--ct-danger); }
  .empty { color: var(--ct-muted); padding: 10px 12px; border: 1px dashed var(--ct-border); border-radius: 10px; }
  .empty p { margin: 0; line-height: 1.5; }

  .soon-card {
    margin: 18px 0 8px;
    padding: 12px 14px;
    border-radius: 12px;
    border: 1px dashed color-mix(in srgb, var(--ct-rose) 60%, transparent);
    background: color-mix(in srgb, var(--ct-rose) 7%, transparent);
  }
  .soon-card h3 { margin: 0 0 6px; }
  .soon-card p { margin: 0 0 8px; color: var(--ct-muted); line-height: 1.5; font-size: 12px; }

  /* The connection card: who Mochiii talks to, at a glance. */
  .conn-card {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 12px;
    border-radius: 14px;
    border: 1px solid var(--ct-border);
    background: var(--ct-surface);
  }
  .conn-badge {
    flex: none;
    width: 40px;
    height: 40px;
    border-radius: 12px;
    display: grid;
    place-items: center;
    font-weight: 700;
    font-size: 15px;
    color: var(--ct-on-accent);
    background: var(--ct-brand-gradient);
  }
  .conn-main { min-width: 0; }
  .conn-provider { font-size: 14px; font-weight: 600; display: flex; align-items: center; gap: 6px; }
  .conn-base { color: var(--ct-muted); font-size: 11.5px; overflow-wrap: anywhere; }
  .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--ct-muted); display: inline-block; }
  .dot.ok { background: var(--ct-ok); }
  .dot.bad { background: var(--ct-danger); }
  .dot.wait { background: var(--ct-warn); animation: dotWait 1.2s ease-in-out infinite; }
  @keyframes dotWait { 50% { opacity: 0.25; } }
  @media (prefers-reduced-motion: reduce) { .dot.wait { animation: none; } }
  .acct-note { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  .acct-note[hidden] { display: none; }
  .kv { display: grid; grid-template-columns: auto 1fr; gap: 6px 14px; margin: 14px 0; }
  .kv dt { color: var(--ct-muted); }
  .kv dd { margin: 0; min-width: 0; overflow-wrap: anywhere; }
  /* Account's status card holds what is in use: provider, key, model. */
  .conn-card:has(> .kv) { flex-wrap: wrap; }
  .conn-card > .conn-main { flex: 1; }
  .conn-card > .kv { flex: 0 0 100%; margin: 0; padding-top: 10px; border-top: 1px solid var(--ct-border); }
  .kv-more { display: block; color: var(--ct-muted); font-size: 11.5px; }
  .meter { margin: 6px 0 10px; }
  .meter-label { display: flex; justify-content: space-between; font-size: 12px; }
  .meter-track { height: 6px; border-radius: 999px; background: color-mix(in srgb, var(--ct-ink) 10%, transparent); margin-top: 4px; overflow: hidden; }
  .meter-fill { height: 100%; border-radius: 999px; background: var(--ct-accent); }
  .meter-fill.warn { background: var(--ct-warn); }
  .meter-fill.hot { background: var(--ct-danger); }
  .plain-list { margin: 0; padding-left: 18px; color: var(--ct-muted); font-size: 12px; line-height: 1.5; }

  /* Settings: one page of labelled sections; the names at the top jump to
     them (UI audit, step 4 -- it was a rail of unlabelled icons). */
  .settings { display: block; min-width: 0; }
  .set-nav {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 12px;
    padding: 0 0 8px;
    margin: 0 0 4px;
    border-bottom: 1px solid var(--ct-border);
  }
  .set-nav .link-btn { color: var(--ct-muted); }
  .set-nav .link-btn:hover { color: var(--ct-ink); }
  .settings-body { min-width: 0; }
  .set-section { padding: 6px 0 10px; scroll-margin-top: 8px; }
  .set-section + .set-section { border-top: 1px solid var(--ct-border); }
  .set-section h3 { margin: 6px 0 4px; }
  .field { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 10px; margin: 10px 0; }
  .field-label { color: var(--ct-muted); min-width: 110px; }
  .field-value { overflow-wrap: anywhere; min-width: 0; }
  .field select {
    font: inherit;
    font-size: 12px;
    max-width: 100%;
    padding: 4px 6px;
    border-radius: 6px;
    border: 1px solid var(--ct-control-border);
    background: var(--ct-control-bg);
    color: var(--ct-ink);
  }
  .check { display: flex; align-items: flex-start; gap: 8px; margin: 10px 0; line-height: 1.45; }
  .market-root { min-width: 0; }

  #contextAddBtn svg { display: block; }
  `;
}
