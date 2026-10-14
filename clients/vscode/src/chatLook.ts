// THE CHAT'S LOOK (Stage 9): the header, the composer, the ⋯ and ＋ menus and
// the ⚙ popover, drawn to the two mockup sheets the owner chose on 2026-10-14.
//
// Loaded AFTER chatPanelStyles() and chatPagesStyles(), so where a rule here
// names the same element it wins. Theme tokens only, as everywhere in the
// webview: scripts/theme-check.js scans this file.
//
// THE ICONS WERE DOTS. chatPanelStyles() styles every <button> with
// `padding: 6px 14px`, and the icon buttons never took it back: a 32px button
// with 28px of padding leaves 4px for a 16px SVG, which flexbox shrinks to a
// sliver. Text glyphs (＋ ☆ ◷) overflowed and so still showed; the SVGs could
// not. Every button class below therefore sets its own padding, and an icon
// never shrinks.

export function chatLookStyles(): string {
  return `
  .icon-btn, .composer-icon, .menu-item, .provider-card, .seg-row button, .send-btn {
    font: inherit;
  }
  .icon-btn, .composer-icon { padding: 0; }
  .icon-btn svg, .composer-icon svg, .menu-item svg, .send-btn svg, .pill svg { flex-shrink: 0; display: block; }

  /* ---- header ------------------------------------------------------------- */
  #appHeader { padding: 10px 12px 10px 14px; }
  #appHeader .brand { gap: 10px; }
  #appHeader .brand-logo { width: 30px; height: 30px; }
  .brand-text { display: flex; flex-direction: column; line-height: 1.15; }
  .brand-name { font-weight: 650; font-size: 14.5px; color: var(--ct-ink); }
  .brand-sub { font-size: 11px; font-weight: 400; color: var(--ct-muted); }
  #appHeader .header-actions { gap: 2px; }
  .icon-btn { width: 30px; height: 30px; }
  .icon-btn[aria-expanded="true"] { background: var(--ct-accent-soft); color: var(--ct-accent-strong); }

  /* ---- menus that drop from a button: ⋯ and ＋ ---------------------------- */
  .pop-menu {
    position: absolute;
    z-index: 30;
    width: min(280px, calc(100% - 24px));
    padding: 6px;
    background: var(--ct-popover-bg);
    color: var(--ct-ink);
    border: 1px solid var(--ct-border);
    border-radius: 14px;
    box-shadow: var(--ct-shadow);
  }
  .more-menu { top: calc(100% - 2px); right: 10px; }
  .context-menu { bottom: calc(100% + 6px); left: 14px; }
  .pop-title { font-size: 12px; font-weight: 600; color: var(--ct-muted); padding: 6px 8px 4px; }
  .menu-sep { height: 1px; margin: 4px 6px; background: var(--ct-border); }
  .menu-item {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    padding: 8px;
    border: none;
    border-radius: 9px;
    background: transparent;
    color: var(--ct-ink);
    text-align: left;
    cursor: pointer;
  }
  .menu-item:hover, .menu-item:focus-visible { background: var(--ct-accent-soft); outline: none; }
  .menu-item > svg { color: var(--ct-muted); }
  .menu-item:hover > svg { color: var(--ct-accent-strong); }
  .mi-text { display: flex; flex-direction: column; min-width: 0; flex: 1; }
  .mi-name { font-size: 13px; }
  .mi-desc { font-size: 11.5px; color: var(--ct-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  #bookmarkBtn.on > svg { color: var(--ct-accent); fill: currentColor; }

  /* ---- the composer: one box, one row ------------------------------------- */
  #composer { padding: 10px 12px 12px; }
  #composerShell { border-radius: 16px; padding: 10px 10px 8px 12px; overflow: visible; }
  #composerShell #promptInput {
    display: block;
    width: 100%;
    min-height: 44px;
    padding: 2px 2px 6px;
  }
  #composerShell .attach-list { padding: 0 0 8px; }
  .composer-bar { display: flex; align-items: center; gap: 4px; flex-wrap: nowrap; min-width: 0; }
  .composer-bar .bar-gap { flex: 1; min-width: 4px; }
  .composer-icon { width: 30px; height: 30px; border-radius: 9px; flex-shrink: 0; }
  .composer-bar .pill { flex-shrink: 1; min-width: 0; }
  .model-chip { gap: 4px; max-width: 160px; }
  .model-chip #modelChipLabel { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .model-chip .chev svg { width: 12px; height: 12px; }
  .effort-pill { gap: 6px; }
  #sendBtn.send-btn { width: 34px; height: 34px; margin: 0 0 0 2px; border-radius: 10px; padding: 0; }
  .send-btn .i-stop { display: none; }
  .send-btn.stop .i-send { display: none; }
  .send-btn.stop .i-stop { display: inline-flex; }
  .send-btn span { display: inline-flex; }

  /* ---- ⚙ model & settings ------------------------------------------------- */
  .settings-popover {
    position: absolute;
    left: 12px;
    right: 12px;
    bottom: calc(100% + 8px);
    max-height: min(600px, 78vh);
    display: flex;
    flex-direction: column;
    background: var(--ct-popover-bg);
    color: var(--ct-ink);
    border: 1px solid var(--ct-border);
    border-radius: 16px;
    box-shadow: var(--ct-shadow);
    z-index: 25;
    overflow: hidden;
  }
  .sp-head { display: flex; align-items: center; justify-content: space-between; padding: 12px 12px 6px 14px; }
  .sp-title { font-weight: 650; font-size: 13.5px; }
  .sp-scroll { overflow-y: auto; padding: 0 12px 12px; display: flex; flex-direction: column; gap: 14px; }
  .sp-label {
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--ct-muted);
    margin: 0 2px 6px;
  }
  .sp-label-row { display: flex; justify-content: space-between; text-transform: none; letter-spacing: 0; font-size: 12px; }
  .sp-label-row > span:first-child { text-transform: uppercase; letter-spacing: 0.04em; font-size: 11px; }

  .provider-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(118px, 1fr)); gap: 6px; }
  .provider-card {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px;
    border: 1px solid var(--ct-border);
    border-radius: 10px;
    background: transparent;
    color: var(--ct-ink);
    text-align: left;
    cursor: pointer;
    min-width: 0;
  }
  .provider-card:hover, .provider-card:focus-visible { border-color: var(--ct-accent); outline: none; }
  .provider-card.current { border-color: var(--ct-accent); background: var(--ct-accent-soft); }
  .provider-mono {
    width: 24px; height: 24px; border-radius: 7px; flex-shrink: 0;
    display: inline-flex; align-items: center; justify-content: center;
    background: var(--ct-accent-soft); color: var(--ct-accent-strong); font-weight: 700; font-size: 12px;
  }
  .provider-card.current .provider-mono { background: var(--ct-accent-solid); color: var(--ct-on-accent); }
  .provider-text { display: flex; flex-direction: column; min-width: 0; }
  .provider-name { font-size: 12.5px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .provider-sub { font-size: 10.5px; color: var(--ct-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .provider-card.current .provider-sub { color: var(--ct-ok); }

  .model-list { max-height: 210px; overflow-y: auto; border: 1px solid var(--ct-border); border-radius: 10px; padding: 4px; }
  .model-list .mode-item.quiet { opacity: 0.55; }
  .model-list .mode-item { padding: 6px 10px; gap: 8px; align-items: center; }
  .model-list .mode-name { font-size: 12.5px; }
  .model-list .mode-desc { font-size: 11px; }
  .model-list .mode-item[aria-disabled="true"] { opacity: 0.5; cursor: not-allowed; }
  .model-list .model-tag {
    font-size: 10px; padding: 1px 6px; margin-left: 6px; border-radius: 999px;
    background: var(--ct-accent-soft); color: var(--ct-accent-strong); vertical-align: 1px; font-weight: 500;
  }
  .model-list .model-empty { padding: 10px; color: var(--ct-muted); font-size: 12px; }

  .seg-row { display: grid; grid-template-columns: repeat(auto-fit, minmax(64px, 1fr)); gap: 6px; }
  .seg-row button {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 1px;
    padding: 7px 4px;
    border: 1px solid var(--ct-border);
    border-radius: 9px;
    background: transparent;
    color: var(--ct-ink);
    cursor: pointer;
  }
  .seg-row button:hover, .seg-row button:focus-visible { border-color: var(--ct-accent); outline: none; }
  .seg-row button[aria-checked="true"] { border-color: var(--ct-accent); background: var(--ct-accent-soft); color: var(--ct-accent-strong); }
  .seg-name { font-size: 12.5px; font-weight: 600; }
  .seg-desc { font-size: 10.5px; color: var(--ct-muted); }

  #contextPopup .context-bar { margin: 0 2px; }
  #contextPopup .context-popup-summary { padding: 4px 2px 0; font-size: 11.5px; color: var(--ct-muted); }
  #contextPopup .context-rows { padding: 4px 2px 0; }
  #contextPopup .context-note { padding: 4px 2px 0; }

  .sp-links { display: flex; flex-direction: column; border-top: 1px solid var(--ct-border); padding-top: 8px; }
  .sp-links .menu-item > svg:last-child { transform: rotate(-90deg); width: 12px; height: 12px; }

  /* ---- the welcome ------------------------------------------------------- */
  .first-run.welcome {
    max-width: none;
    background: transparent;
    border: none;
    padding: 18px 4px 8px;
    text-align: center;
    color: var(--ct-ink);
  }
  .welcome-logo { width: 64px; height: 64px; display: block; margin: 8px auto 10px; }
  .welcome-title { font-size: 19px; font-weight: 650; margin: 0 0 6px; color: var(--ct-ink); line-height: 1.3; }
  .welcome-sub { font-size: 12.5px; color: var(--ct-muted) !important; margin: 0 0 18px !important; }
  .welcome-cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 8px; text-align: left; }
  .welcome-card {
    display: flex;
    align-items: flex-start;
    gap: 10px;
    padding: 12px;
    border: 1px solid var(--ct-border);
    border-radius: 12px;
    background: var(--ct-surface);
    color: var(--ct-ink);
    font: inherit;
    text-align: left;
    cursor: pointer;
    transition: border-color 120ms ease, transform 120ms ease;
  }
  .welcome-card:hover, .welcome-card:focus-visible { border-color: var(--ct-accent); outline: none; transform: translateY(-1px); }
  .wc-icon {
    width: 30px; height: 30px; border-radius: 9px; flex-shrink: 0;
    display: inline-flex; align-items: center; justify-content: center;
    background: color-mix(in srgb, var(--ct-rose) 18%, transparent); color: var(--ct-rose);
  }
  .wc-icon svg { width: 17px; height: 17px; }
  .wc-text { display: flex; flex-direction: column; min-width: 0; }
  .wc-title { font-size: 13px; font-weight: 600; }
  .wc-desc { font-size: 11.5px; color: var(--ct-muted); margin-top: 2px; }
  .welcome-ideas-label { text-align: left; font-size: 12px; color: var(--ct-muted); margin: 18px 2px 8px; }
  .welcome-chips { display: flex; flex-wrap: wrap; gap: 6px; }
  .welcome-chip {
    padding: 5px 11px;
    border: 1px solid var(--ct-border);
    border-radius: 999px;
    background: transparent;
    color: var(--ct-ink);
    font: inherit;
    font-size: 12px;
    cursor: pointer;
  }
  .welcome-chip:hover, .welcome-chip:focus-visible { border-color: var(--ct-accent); color: var(--ct-accent-strong); outline: none; }
  .first-run.welcome .first-run-quiet { margin: 18px 0 0 !important; font-size: 11px; text-align: center; }

  /* ---- messages ----------------------------------------------------------- */
  .msg.user { display: flex; justify-content: flex-end; }
  .msg.user .card {
    max-width: 85%;
    background: var(--ct-accent-solid);
    color: var(--ct-on-accent);
    border-left: none;
    border-radius: 14px 14px 4px 14px;
    padding: 8px 12px;
  }
  .msg.user .card .body { font-size: 13px; }
  .reply-head { display: flex; align-items: center; gap: 8px; margin: 0 0 6px; }
  .reply-logo { width: 22px; height: 22px; }
  .reply-name { font-weight: 600; font-size: 12.5px; color: var(--ct-ink); }
  .reply-time { font-size: 11px; color: var(--ct-muted); }
  .msg .code-block { border: 1px solid var(--ct-border); }
  .msg .code-block .code-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 3px 4px 3px 10px;
    background: color-mix(in srgb, var(--ct-ink) 6%, var(--ct-code-bg));
    border-bottom: 1px solid var(--ct-border);
  }
  .msg .code-block .code-head .code-lang { padding: 0; background: none; border: none; font-size: 11px; }
  .code-copy {
    padding: 2px 8px;
    border: none;
    border-radius: 6px;
    background: transparent;
    color: var(--ct-muted);
    font: inherit;
    font-size: 11px;
    cursor: pointer;
  }
  .code-copy:hover, .code-copy:focus-visible { background: var(--ct-accent-soft); color: var(--ct-accent-strong); outline: none; }

  /* ---- agent actions: the tool card, the steps, the result ---------------- */
  .work {
    margin: 6px 0 12px;
    font-size: 12px;
    border: 1px solid var(--ct-border);
    border-radius: 12px;
    background: var(--ct-surface);
    overflow: hidden;
  }
  .work-summary {
    display: flex;
    width: 100%;
    gap: 8px;
    padding: 8px 12px;
    border: none;
    border-radius: 0;
    color: var(--ct-ink);
    font-weight: 600;
  }
  .work-summary::before { order: 2; margin-left: auto; color: var(--ct-muted); font-weight: 400; }
  .work.has-failure { border-color: color-mix(in srgb, var(--ct-danger) 45%, transparent); }
  .work.running .work-summary { color: var(--ct-accent-strong); }
  .work-rows { margin: 0; padding: 2px 6px 6px; border-left: none; border-top: 1px solid var(--ct-border); }
  .work-row {
    display: grid;
    grid-template-columns: 18px minmax(0, 1fr) auto auto;
    grid-template-areas: "glyph name badge time" ". why why why";
    column-gap: 8px;
    align-items: center;
    padding: 6px 6px;
    border-radius: 8px;
  }
  .work-row + .work-row { border-top: 1px solid color-mix(in srgb, var(--ct-border) 60%, transparent); border-radius: 0; }
  .work-glyph { grid-area: glyph; font-size: 12px; }
  .work-name { grid-area: name; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .work-why { grid-area: why; font-size: 11.5px; }
  .work-badge {
    grid-area: badge;
    font-size: 10.5px;
    padding: 1px 7px;
    border-radius: 999px;
    border: 1px solid var(--ct-border);
    color: var(--ct-muted);
  }
  .work-badge:empty, .work-time:empty { display: none; }
  .work-row[data-state="ok"] .work-badge { color: var(--ct-ok); border-color: color-mix(in srgb, var(--ct-ok) 45%, transparent); background: color-mix(in srgb, var(--ct-ok) 10%, transparent); }
  .work-row[data-state="fail"] .work-badge { color: var(--ct-danger); border-color: color-mix(in srgb, var(--ct-danger) 45%, transparent); background: color-mix(in srgb, var(--ct-danger) 10%, transparent); }
  .work-row[data-state="denied"] .work-badge { color: var(--ct-warn); border-color: color-mix(in srgb, var(--ct-warn) 45%, transparent); }
  .work-row[data-state="run"] .work-badge { color: var(--ct-accent-strong); border-color: color-mix(in srgb, var(--ct-accent) 45%, transparent); }
  .work-time { grid-area: time; font-size: 11px; color: var(--ct-muted); font-variant-numeric: tabular-nums; min-width: 34px; text-align: right; }

  .spec-notice, .edit-summary { margin-left: 0; }
  .spec-notice.steps {
    white-space: normal;
    border-left: none;
    border: 1px solid var(--ct-border);
    border-radius: 12px;
    background: var(--ct-surface);
    padding: 10px 12px;
  }
  .steps-head { font-weight: 600; font-size: 12.5px; margin-bottom: 6px; color: var(--ct-ink); }
  .steps-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 5px; }
  .steps-list li { display: flex; align-items: center; gap: 8px; font-size: 12.5px; color: var(--ct-muted); }
  .step-mark {
    width: 15px; height: 15px; border-radius: 50%; flex-shrink: 0;
    display: inline-flex; align-items: center; justify-content: center;
    border: 1.5px solid color-mix(in srgb, var(--ct-ink) 35%, transparent); font-size: 9px; font-weight: 700;
  }
  .steps-list li[data-status="done"] { color: var(--ct-ink); }
  .steps-list li[data-status="done"] .step-mark { background: var(--ct-ok); border-color: var(--ct-ok); color: var(--ct-on-accent); }
  .steps-list li[data-status="active"] { color: var(--ct-ink); font-weight: 600; }
  .steps-list li[data-status="active"] .step-mark { border-color: var(--ct-accent); color: var(--ct-accent); }
  .steps-list li[data-status="blocked"] .step-mark { border-color: var(--ct-danger); color: var(--ct-danger); }
  .edit-summary { border-radius: 12px; padding: 12px; }
  .edit-summary .summary-line { font-size: 13px; }

  /* ---- history ------------------------------------------------------------ */
  .history-row .h-title mark {
    background: color-mix(in srgb, var(--ct-accent) 28%, transparent);
    color: inherit;
    border-radius: 3px;
    padding: 0 1px;
  }
  .history-row .h-age { min-width: 52px; text-align: right; }

  /* ---- MCP servers (Customize) ------------------------------------------- */
  .mcp-head { display: flex; align-items: center; justify-content: space-between; margin: 4px 0 8px; }
  .mcp-head h3 { margin: 0; font-size: 13.5px; }
  .pg-btn.small { display: inline-flex; align-items: center; gap: 4px; padding: 4px 10px; font-size: 12px; }
  .pg-btn.small svg { width: 13px; height: 13px; }
  .mcp-row { display: flex; align-items: center; gap: 10px; }
  .mcp-mono {
    width: 28px; height: 28px; border-radius: 8px; flex-shrink: 0;
    display: inline-flex; align-items: center; justify-content: center;
    background: var(--ct-accent-soft); color: var(--ct-accent-strong); font-weight: 700; font-size: 12.5px;
  }
  .mcp-text { flex: 1; min-width: 0; display: flex; flex-direction: column; }
  .mcp-text .row-sub { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .mcp-acts { display: flex; align-items: center; gap: 4px; flex-shrink: 0; }
  .mcp-switch {
    position: relative;
    width: 32px;
    height: 18px;
    padding: 0;
    border: none;
    border-radius: 999px;
    background: color-mix(in srgb, var(--ct-ink) 25%, transparent);
    cursor: pointer;
    flex-shrink: 0;
  }
  .mcp-switch::after {
    content: '';
    position: absolute;
    top: 2px;
    left: 2px;
    width: 14px;
    height: 14px;
    border-radius: 50%;
    background: var(--ct-on-accent);
    transition: transform 120ms ease;
  }
  .mcp-switch[aria-checked="true"] { background: var(--ct-accent-solid); }
  .mcp-switch[aria-checked="true"]::after { transform: translateX(14px); }
  .mcp-switch:focus-visible { outline: 2px solid var(--ct-accent); outline-offset: 2px; }
  .mcp-acts .row-act { padding: 2px 6px; font-size: 11px; }

  /* ---- narrow side bars --------------------------------------------------- */
  @media (max-width: 360px) {
    .brand-sub { display: none; }
    .composer-bar .effort-pill, #toolsBtn { display: none; }
    .model-chip { max-width: 110px; }
  }
`;
}
