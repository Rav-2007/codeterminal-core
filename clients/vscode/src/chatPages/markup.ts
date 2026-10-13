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
  plug: svg('<path d="M6 2v3M10 2v3M4.5 5h7v2.5a3.5 3.5 0 0 1-7 0V5zM8 11v3"/>'),
  at: svg('<circle cx="8" cy="8" r="2.4"/><path d="M10.4 8v1a1.6 1.6 0 0 0 3.2 0V8a5.6 5.6 0 1 0-2.3 4.5"/>'),
  model: svg('<rect x="3" y="3" width="10" height="10" rx="2"/><path d="M6 1.5v1.5M10 1.5v1.5M6 13v1.5M10 13v1.5M1.5 6H3M1.5 10H3M13 6h1.5M13 10h1.5"/><rect x="6" y="6" width="4" height="4" rx=".6"/>'),
  sliders: svg('<path d="M3 4h10M3 8h10M3 12h10"/><circle cx="6" cy="4" r="1.4"/><circle cx="10.5" cy="8" r="1.4"/><circle cx="5" cy="12" r="1.4"/>'),
  folder: svg('<path d="M2 4.5a1 1 0 0 1 1-1h3l1.5 1.5H13a1 1 0 0 1 1 1V12a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1V4.5z"/>'),
  shield: svg('<path d="M8 1.8 13 3.6v4c0 3-2.1 5.4-5 6.6-2.9-1.2-5-3.6-5-6.6v-4L8 1.8z"/><path d="m5.8 8 1.6 1.6 2.9-3"/>'),
  wrench: svg('<path d="M10.2 2.2a3.3 3.3 0 0 0-3.9 4.4L2.4 10.5a1.4 1.4 0 0 0 2 2l3.9-3.9a3.3 3.3 0 0 0 4.4-3.9l-1.9 1.9-1.6-.4-.4-1.6 1.4-2.4z"/>'),
  info: svg('<circle cx="8" cy="8" r="6"/><path d="M8 7.2v3.6M8 5.1v.1"/>'),
};

/** The three header buttons that open a page. Appended after the existing ones. */
export function headerPageButtons(): string {
  return `<span class="header-divider" aria-hidden="true"></span>
      <button type="button" id="customizeBtn" class="icon-btn page-btn" title="Customize: MCP servers, specs, tools" aria-label="Customize" aria-controls="pageCustomize" aria-expanded="false">${ICONS.customize}</button>
      <button type="button" id="accountBtn" class="icon-btn page-btn" title="Account: provider, key and model" aria-label="Account" aria-controls="pageAccount" aria-expanded="false">${ICONS.account}</button>
      <button type="button" id="settingsBtn" class="icon-btn page-btn" title="Settings" aria-label="Settings" aria-controls="pageSettings" aria-expanded="false">${ICONS.settings}</button>`;
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
    <div id="custPanel-mcp" class="page-panel" role="tabpanel" aria-labelledby="custTab-mcp">
      ${tabs('mcp', 'MCP servers', [
        ['installed', 'Installed'],
        ['market', 'Marketplace'],
      ])}
      <div id="mcpPanel-installed" role="tabpanel" aria-labelledby="mcpTab-installed">
        <p id="mcpNotice" class="page-note" hidden></p>
        <div id="mcpInstalledList" class="rows" aria-live="polite"></div>
        <div id="mcpEmpty" class="empty" hidden>
          <p>No MCP servers yet. An MCP server gives Mochiii’s agent new tools: web search, browsers, databases, clouds.</p>
        </div>
        <div class="page-actions">
          <button type="button" class="pg-btn primary" data-mcp-browse>Browse the Marketplace</button>
          <button type="button" class="pg-btn" data-page-command="mcpAdd">Add a server by hand</button>
          <button type="button" class="pg-btn ghost" data-page-command="mcpShowTools">Show MCP tools</button>
        </div>
        <p class="page-note">A server runs on your machine with your privileges. Mochiii shows exactly what will run and asks you to type its name before adding it, and each of its tools asks before it runs.</p>
      </div>
      <div id="mcpPanel-market" role="tabpanel" aria-labelledby="mcpTab-market" hidden>
        <div id="marketRoot" class="market-root"></div>
        <div class="page-actions">
          <button type="button" class="pg-btn ghost" data-page-command="marketInEditor">Open the marketplace in an editor tab</button>
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
    ${pageHead('pageAccount', 'Account', 'Who Mochiii talks to on your behalf. Nothing here leaves this machine.')}
    <div class="conn-card">
      <div id="acctBadge" class="conn-badge" aria-hidden="true">?</div>
      <div class="conn-main">
        <div class="conn-provider"><span id="acctProvider">Checking…</span> <span id="acctDot" class="dot" role="img" aria-label="status unknown"></span></div>
        <div id="acctBase" class="conn-base"></div>
      </div>
    </div>
    <dl class="kv">
      <dt>Key</dt><dd><span id="acctKey"></span><span id="acctKeyMore" class="kv-more"></span></dd>
      <dt>Model</dt><dd><span id="acctModel"></span><span id="acctModelMore" class="kv-more"></span></dd>
      <dt>Daemon</dt><dd id="acctDaemon"></dd>
    </dl>
    <div id="acctSpend" class="spend" hidden>
      <h3>Spend</h3>
      <div id="acctSpendRows"></div>
      <p id="acctSpendNote" class="page-note"></p>
    </div>
    <div class="page-actions">
      <button type="button" class="pg-btn primary" data-page-command="connect">Switch provider or key</button>
      <button type="button" class="pg-btn" data-open-models>Change model</button>
      <button type="button" class="pg-btn ghost" data-page-command="forgetKey">Forget the stored key</button>
      <button type="button" class="pg-btn ghost" data-page-refresh>Refresh</button>
    </div>
    <div id="acctPrivacyBox" class="sub-block" hidden>
      <h3>Privacy</h3>
      <ul id="acctPrivacy" class="plain-list"></ul>
    </div>
    <div class="soon-card">
      <h3>Sign in to mochiii.in</h3>
      <p>Coming soon: one sign-in to keep your favourite servers and settings across machines. Mochiii works fully without it, and nothing is sent until you choose to sign in.</p>
      <button type="button" class="pg-btn" disabled>Sign in — coming soon</button>
    </div>
  </section>`;
}

function railButton(key: string, label: string, icon: string, first: boolean): string {
  return `<button type="button" role="tab" id="setTab-${key}" data-tab="${key}" aria-controls="setPanel-${key}" aria-selected="${first}" tabindex="${first ? 0 : -1}" title="${label}">${icon}<span>${label}</span></button>`;
}

function settingsPage(): string {
  return `<section id="pageSettings" class="page" hidden role="region" aria-labelledby="pageSettingsTitle">
    ${pageHead('pageSettings', 'Settings', 'How Mochiii behaves in this editor.')}
    <div class="settings">
      <div class="rail" role="tablist" aria-label="Settings sections" aria-orientation="vertical" data-tabs="set">
        ${railButton('model', 'Model', ICONS.model, true)}
        ${railButton('behaviour', 'Behaviour', ICONS.sliders, false)}
        ${railButton('workspace', 'Workspace', ICONS.folder, false)}
        ${railButton('privacy', 'Privacy', ICONS.shield, false)}
        ${railButton('advanced', 'Advanced', ICONS.wrench, false)}
        ${railButton('about', 'About', ICONS.info, false)}
      </div>
      <div class="settings-body">
        <div id="setPanel-model" role="tabpanel" aria-labelledby="setTab-model">
          <h3>Model</h3>
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
          <label class="field" for="setEffort">
            <span class="field-label">Thinking effort</span>
            <select id="setEffort">
              <option value="">Auto (the model’s own)</option>
              <option value="low">Low</option>
              <option value="medium">Medium</option>
              <option value="high">High</option>
            </select>
          </label>
        </div>
        <div id="setPanel-behaviour" role="tabpanel" aria-labelledby="setTab-behaviour" hidden>
          <h3>Behaviour</h3>
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
        </div>
        <div id="setPanel-workspace" role="tabpanel" aria-labelledby="setTab-workspace" hidden>
          <h3>Workspace</h3>
          <div class="field">
            <span class="field-label">Working folder</span>
            <span id="setFolder" class="field-value"></span>
          </div>
          <p id="setFolderNote" class="page-note"></p>
          <div class="page-actions">
            <button type="button" class="pg-btn" data-page-command="chooseFolder">Choose working folder…</button>
          </div>
        </div>
        <div id="setPanel-privacy" role="tabpanel" aria-labelledby="setTab-privacy" hidden>
          <h3>Privacy and data</h3>
          <label class="check" for="setHistory">
            <input type="checkbox" id="setHistory" data-setting="history.autoSave" />
            <span>Keep every chat in History on this machine</span>
          </label>
          <p class="page-note">What leaves this machine: your question, the conversation so far, the parts of your code Mochiii chose to show, and the open file when that is on. They go to the provider on the Account page, and nowhere else.</p>
          <ul id="setPrivacyNotes" class="plain-list"></ul>
        </div>
        <div id="setPanel-advanced" role="tabpanel" aria-labelledby="setTab-advanced" hidden>
          <h3>Advanced</h3>
          <p class="page-note">These name where your questions go or what may run, so they are changed in VS Code’s own Settings, for this machine only — never from this page, and never by a repository.</p>
          <div id="setMachine" class="rows"></div>
        </div>
        <div id="setPanel-about" role="tabpanel" aria-labelledby="setTab-about" hidden>
          <h3>About Mochiii</h3>
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
        </div>
      </div>
    </div>
  </section>`;
}

/** All three pages, hidden until opened. Placed after the composer. */
export function chatPagesMarkup(): string {
  return `${customizePage()}
  ${accountPage()}
  ${settingsPage()}`;
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
  .page-back:focus-visible, .seg button:focus-visible, .rail button:focus-visible,
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
  .pg-btn.primary { background: var(--ct-accent-solid); color: var(--ct-on-accent); border-color: transparent; }
  .pg-btn.primary:hover { background: var(--ct-accent-solid-hover); }
  .pg-btn.ghost { background: transparent; }
  .pg-btn:disabled { opacity: 0.55; cursor: default; }
  .link-btn { font: inherit; font-size: 12px; background: none; border: none; color: var(--ct-accent); cursor: pointer; padding: 0; }
  .page-actions { display: flex; flex-wrap: wrap; gap: 6px; margin: 12px 0; }

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
  .tag.ask { color: var(--ct-accent); border-color: color-mix(in srgb, var(--ct-accent) 50%, transparent); }
  .row-spacer { flex: 1; }
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
  .kv { display: grid; grid-template-columns: auto 1fr; gap: 6px 14px; margin: 14px 0; }
  .kv dt { color: var(--ct-muted); }
  .kv dd { margin: 0; min-width: 0; overflow-wrap: anywhere; }
  .kv-more { display: block; color: var(--ct-muted); font-size: 11.5px; }
  .meter { margin: 6px 0 10px; }
  .meter-label { display: flex; justify-content: space-between; font-size: 12px; }
  .meter-track { height: 6px; border-radius: 999px; background: color-mix(in srgb, var(--ct-ink) 10%, transparent); margin-top: 4px; overflow: hidden; }
  .meter-fill { height: 100%; border-radius: 999px; background: var(--ct-accent); }
  .meter-fill.warn { background: var(--ct-warn); }
  .meter-fill.hot { background: var(--ct-danger); }
  .plain-list { margin: 0; padding-left: 18px; color: var(--ct-muted); font-size: 12px; line-height: 1.5; }

  /* Settings: an icon rail, labels beside the icons when there is room. */
  .settings { display: flex; gap: 12px; min-width: 0; }
  .rail { display: flex; flex-direction: column; gap: 2px; flex: none; }
  .rail button {
    display: flex;
    align-items: center;
    gap: 8px;
    font: inherit;
    font-size: 12px;
    border: none;
    border-radius: 8px;
    background: transparent;
    color: var(--ct-muted);
    padding: 6px 8px;
    cursor: pointer;
    text-align: left;
  }
  .rail button[aria-selected="true"] { background: var(--ct-accent-soft); color: var(--ct-ink); }
  .settings-body { flex: 1; min-width: 0; }
  .settings-body h3 { margin-top: 2px; }
  @media (max-width: 420px) {
    .rail button span { display: none; }
  }
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

  /* The composer's plug: how many MCP servers are on, as a small count. */
  .tools-btn { position: relative; }
  .tools-btn svg, #contextAddBtn svg { display: block; }
  .tools-count {
    position: absolute;
    top: 1px;
    right: 0;
    min-width: 14px;
    height: 14px;
    padding: 0 3px;
    border-radius: 999px;
    font-size: 9.5px;
    line-height: 14px;
    text-align: center;
    background: var(--ct-accent-solid);
    color: var(--ct-on-accent);
  }
  `;
}
