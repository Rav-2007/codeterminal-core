// THE CHAT'S LOOK: the header, the composer, the ⋯ and ＋ menus, the model menu
// and the empty chat. Stage 9 drew them to the mockup sheets of 2026-10-14; the
// UI audit of 2026-11-21 (step 2) made the panel quieter, in the manner of an
// editor's own chat: one row under the prompt, VS Code's own button colours,
// a header of three buttons and an empty chat that is the lotus and one line.
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
  .icon-btn, .composer-icon, .menu-item, .send-btn, .recent-row {
    font: inherit;
  }
  .icon-btn, .composer-icon { padding: 0; }
  .icon-btn svg, .composer-icon svg, .menu-item svg, .send-btn svg, .pill svg { flex-shrink: 0; display: block; }

  /* ---- header ------------------------------------------------------------- */
  #appHeader { padding: 10px 12px 10px 14px; }
  #appHeader .brand { gap: 10px; }
  #appHeader .brand-logo { width: 30px; height: 30px; }
  #appHeader .brand { min-width: 0; flex: 1; }
  #appHeader .brand-logo { flex-shrink: 0; }
  .brand-text { display: flex; align-items: baseline; gap: 6px; min-width: 0; line-height: 1.15; }
  .brand-name { font-weight: 650; font-size: 14.5px; color: var(--ct-ink); flex-shrink: 0; }
  /* The folder, muted, after a dot; it gives way first (full path in its title). */
  .brand-folder { font-weight: 400; font-size: 12.5px; color: var(--ct-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
  .brand-folder::before { content: '·'; margin-right: 6px; }
  #appHeader .header-actions { gap: 2px; flex-shrink: 0; }
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
  /* A plain input box: the theme's border, its focus colour when focused, no
     glow and no pulse on send. */
  #composerShell {
    border-radius: 10px;
    padding: 8px 8px 6px 10px;
    overflow: visible;
    border-color: var(--vscode-input-border, var(--ct-composer-border));
    box-shadow: none;
  }
  #composerShell.focused { border-color: var(--ct-accent); box-shadow: none; }
  #composerShell.sending { animation: none; }
  #composerShell #promptInput {
    display: block;
    width: 100%;
    min-height: 44px;
    padding: 2px 2px 6px;
  }
  #composerShell .attach-list { padding: 0 0 8px; }
  .composer-bar { display: flex; align-items: center; gap: 4px; flex-wrap: nowrap; min-width: 0; }
  .composer-bar .bar-gap { flex: 1; min-width: 2px; }
  .composer-icon { width: 28px; height: 28px; border-radius: 7px; flex-shrink: 0; }
  /* Chips are quiet until hovered: no fill, no border. */
  .composer-bar .pill { flex-shrink: 0; height: 26px; padding: 0 8px; border-color: transparent; background: transparent; color: var(--ct-muted); }
  .composer-bar .pill:hover { background: var(--ct-accent-soft); border-color: transparent; color: var(--ct-ink); }
  /* The model chip is the one that gives way: it ellipsizes down to a few
     letters, and its tooltip names the model in full. */
  .model-chip { gap: 3px; flex: 0 1 auto !important; min-width: 44px !important; max-width: 200px; }
  .model-chip #modelChipLabel { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .model-chip .chev svg { width: 12px; height: 12px; }
  /* Effort is its bars: the word overflowed the row, and the tooltip and the
     accessible name still say the level. */
  .effort-pill { gap: 0; padding: 0 7px !important; }
  .effort-pill .effort-value { display: none; }
  /* The mode chip says its mode in words, the one place that matters most --
     in Auto, edits reach the files without a question. */
  #autoApplyToggle.pill { color: var(--ct-ink); }
  #autoApplyToggle.on { background: transparent; border-color: transparent; color: var(--ct-ink); }
  #autoApplyToggle.on:hover { background: var(--ct-accent-soft); }
  /* The context ring: a percentage only once it is worth reading. */
  .context-btn { width: auto; min-width: 28px; padding: 0 4px; }
  .context-btn .context-pct { display: none; }
  .context-btn.warn .context-pct, .context-btn.hot .context-pct { display: inline; }
  .context-popup-summary { flex-wrap: wrap; justify-content: flex-start; gap: 2px 8px; }
  .context-popup-summary #contextPct { white-space: nowrap; }
  .context-ring-fill { stroke: var(--ct-muted); }
  /* Send: VS Code's own primary button. */
  #sendBtn.send-btn {
    width: 28px; height: 28px; margin: 0 0 0 2px; border-radius: 7px; padding: 0;
    background: var(--ct-accent-solid); color: var(--ct-on-accent);
    border: 1px solid var(--vscode-button-border, transparent);
    box-shadow: none;
  }
  #sendBtn.send-btn:hover:not(:disabled) { background: var(--ct-accent-solid-hover); box-shadow: none; transform: none; }
  #sendBtn.send-btn.stop { background: var(--ct-danger); box-shadow: none; }
  #sendBtn.send-btn svg { width: 14px; height: 14px; }
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
  .sp-scroll { overflow-y: auto; padding: 0 10px 10px; display: flex; flex-direction: column; gap: 6px; }
  .model-popover { max-height: min(420px, 70vh); }
  #switchProviderBtn { padding: 7px 8px; }

  .model-list { max-height: 260px; overflow-y: auto; border: none; border-bottom: 1px solid var(--ct-border); border-radius: 0; padding: 0 0 6px; }
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


  /* ---- the empty chat ---------------------------------------------------- */
  .first-run.welcome {
    max-width: none;
    background: transparent;
    border: none;
    padding: 10vh 4px 8px;
    text-align: center;
    color: var(--ct-ink);
  }
  .welcome-logo { width: 56px; height: 56px; display: block; margin: 0 auto 10px; }
  .welcome-title { font-size: 16px; font-weight: 600; margin: 0; color: var(--ct-ink); line-height: 1.3; }
  .recent-chats { max-width: 420px; margin: 28px auto 0; text-align: left; }
  .recent-head { font-size: 11.5px; color: var(--ct-muted); margin: 0 8px 4px; }
  .recent-list { display: flex; flex-direction: column; }
  .recent-row {
    display: flex;
    align-items: baseline;
    gap: 10px;
    width: 100%;
    padding: 6px 8px;
    border: none;
    border-radius: 6px;
    background: transparent;
    color: var(--ct-ink);
    font-size: 12.5px;
    text-align: left;
    cursor: pointer;
  }
  .recent-row:hover { background: var(--vscode-list-hoverBackground, var(--ct-accent-soft)); }
  .recent-row:focus-visible { outline: 1px solid var(--ct-accent); outline-offset: -1px; }
  .recent-title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .recent-age { flex-shrink: 0; font-size: 11.5px; color: var(--ct-muted); }

  /* ---- messages ----------------------------------------------------------- */
  /* Your message is a quiet box the width of the chat (UI audit, step 3): it
     was a bright bubble on the right, the loudest thing in the transcript. The
     reply under it is plain text with no header. */
  .msg.user { display: block; }
  .msg.user .card {
    max-width: none;
    background: var(--vscode-input-background, var(--ct-user-bg));
    color: var(--ct-ink);
    border: 1px solid var(--vscode-input-border, var(--ct-border));
    border-radius: 6px;
    padding: 7px 10px;
  }
  .msg.user .card .body { font-size: 13px; }
  .msg.assistant { margin-top: 2px; }
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

  /* ---- agent actions: the tool runs, the steps, the result ---------------- */
  /* A turn's tool runs are one quiet line -- "● Used 3 tools · 1 failed" --
     that opens to a row each (UI audit, step 3: no box around them). */
  .work {
    margin: 4px 0 10px;
    font-size: 12px;
    border: none;
    border-radius: 0;
    background: transparent;
    overflow: visible;
  }
  .work-summary {
    display: flex;
    width: 100%;
    gap: 8px;
    padding: 2px 0;
    border: none;
    border-radius: 0;
    color: var(--ct-muted);
    font-weight: 400;
    cursor: pointer;
  }
  .work-summary .work-label::before { content: '\u25cf'; margin-right: 7px; color: var(--ct-ok); font-size: 9px; vertical-align: 2px; }
  .work.running .work-summary .work-label::before { color: var(--ct-accent); }
  .work.has-failure .work-summary .work-label::before { color: var(--ct-danger); }
  .work-summary:hover { color: var(--ct-ink); }
  .work-summary::before { order: 2; margin-left: auto; color: var(--ct-muted); font-weight: 400; }
  .work.running .work-summary { color: var(--ct-ink); }
  .work-rows { margin: 2px 0 0 4px; padding: 0 0 0 10px; border-left: 1px solid var(--ct-border); border-top: none; }
  .work-row {
    display: grid;
    grid-template-columns: 18px minmax(0, 1fr) auto auto;
    grid-template-areas: "glyph name badge time" ". why why why";
    column-gap: 8px;
    align-items: center;
    padding: 3px 4px;
    border-radius: 4px;
  }
  .work-row + .work-row { border-top: none; }
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

  /* ---- cards in the conversation: approval, edit, no key (UI audit, step 3) */
  /* No avatar indent: these lined up under an avatar column that is gone. */
  .tool-approval, .edit-proposal, .edit-rejections, .approval-record { margin-left: 0; }
  .tool-approval {
    border: 1px solid var(--ct-border);
    border-radius: 8px;
    background: var(--vscode-editorWidget-background, var(--ct-surface));
    padding: 10px 12px;
    font-size: 12.5px;
  }
  .tool-approval .approval-auto, .edit-proposal .approval-auto { margin: 6px 0; color: var(--ct-ink); }
  .tool-approval .approval-heading {
    font-weight: 400;
    margin-bottom: 8px;
    font-family: var(--vscode-editor-font-family, monospace);
    font-size: 12px;
    white-space: pre-wrap;
    word-break: break-word;
  }
  /* The risk: one line with a sign. The words are the terminal's, unchanged;
     the colour says how bad -- red leaves the machine, writes the real project
     or is marked destructive; amber runs outside the sandbox. */
  .tool-approval .approval-lane { display: flex; gap: 6px; align-items: baseline; margin-bottom: 10px; font-size: 12px; font-weight: 400; color: var(--ct-ink); }
  .tool-approval .approval-lane .approval-sign { flex-shrink: 0; }
  .tool-approval .approval-lane.warn .approval-sign, .tool-approval .risk.warn { color: var(--ct-warn); }
  .tool-approval .approval-lane.danger .approval-sign, .tool-approval .risk.danger { color: var(--ct-danger); }
  .tool-approval .approval-actions { display: flex; gap: 6px; flex-wrap: wrap; align-items: center; }
  .tool-approval .approval-btn { font: inherit; font-size: 12px; }
  .tool-approval .approval-btn.link { margin-left: auto; color: var(--ct-muted); }
  .approval-record { margin: 2px 0 8px; }

  .edit-proposal { border-radius: 8px; padding: 10px 12px; font-size: 12px; }
  .edit-head { display: flex; align-items: baseline; justify-content: space-between; gap: 8px; margin-bottom: 6px; }
  .edit-proposal .file-path { margin: 0; font-family: var(--vscode-editor-font-family, monospace); overflow-wrap: anywhere; }
  .edit-proposal .edit-index { margin: 0; flex-shrink: 0; }
  .edit-diff {
    border: 1px solid var(--ct-border);
    border-radius: 6px;
    background: var(--ct-code-bg);
    font-family: var(--vscode-editor-font-family, monospace);
    font-size: 11.5px;
    line-height: 1.5;
    overflow-x: auto;
    padding: 4px 0;
  }
  .edit-diff .diff-row { display: flex; white-space: pre; min-width: max-content; padding: 0 8px 0 0; }
  .edit-diff .diff-row.more { display: none; }
  .edit-diff.expanded .diff-row.more { display: flex; }
  .edit-diff .diff-sign { width: 18px; flex-shrink: 0; text-align: center; color: var(--ct-muted); }
  .edit-diff .diff-row.add { background: var(--vscode-diffEditor-insertedLineBackground, color-mix(in srgb, var(--ct-ok) 14%, transparent)); }
  .edit-diff .diff-row.del { background: var(--vscode-diffEditor-removedLineBackground, color-mix(in srgb, var(--ct-danger) 14%, transparent)); }
  .edit-diff .diff-row.ctx { color: var(--ct-muted); }
  .edit-diff-more { font-size: 11.5px; margin-top: 2px; }
  .edit-footer { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; margin-top: 8px; }
  .edit-proposal .edit-footer .view-diff-btn { margin: 0; border-radius: 6px; padding: 5px 12px; font-size: 12px; color: var(--vscode-button-secondaryForeground, var(--ct-ink)); background: var(--vscode-button-secondaryBackground, transparent); }
  .edit-proposal .edit-footer .actions { margin-left: auto; display: flex; gap: 6px; }
  .edit-proposal .edit-footer .actions button { font: inherit; font-size: 12px; }
  .edit-proposal .result { margin-top: 8px; }

  /* Under an answer: what it stood on, and Copy, shown while the answer is
     hovered or the button has the keyboard. */
  .msg-footer { border-top: none; padding-top: 0; margin-top: 6px; gap: 10px; min-height: 22px; }
  .msg-footer .msg-grounding { color: var(--ct-muted); font-size: 11.5px; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .msg-footer .msg-grounding.warn { color: var(--ct-warn); }
  .msg-footer .msg-copy { margin-left: auto; padding: 1px 8px; font-size: 11.5px; border-radius: 4px; opacity: 0; transition: opacity 120ms ease; }
  .msg:hover .msg-footer .msg-copy, .msg-footer .msg-copy:focus-visible { opacity: 1; }

  /* No key in use: one card in the empty chat. */
  .conn-cta {
    max-width: 420px;
    margin: 18px auto 8px;
    padding: 12px 14px;
    border: 1px solid var(--ct-border);
    border-radius: 8px;
    background: var(--vscode-editorWidget-background, var(--ct-surface));
    text-align: left;
  }
  .conn-cta-title { font-weight: 600; font-size: 13px; margin-bottom: 4px; color: var(--ct-ink); }
  .conn-cta-body { font-size: 12px; color: var(--ct-muted); margin-bottom: 10px; line-height: 1.45; }
  .conn-cta-button { font: inherit; font-size: 12.5px; }

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
  /* The actions wrap under the server's name when they do not fit beside it:
     in one unwrapped row they ran off a 300px side bar, Remove out of reach. */
  .mcp-row { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 10px; }
  .mcp-mono {
    width: 28px; height: 28px; border-radius: 8px; flex-shrink: 0;
    display: inline-flex; align-items: center; justify-content: center;
    background: var(--ct-accent-soft); color: var(--ct-accent-strong); font-weight: 700; font-size: 12.5px;
  }
  .mcp-text { flex: 1 1 100px; min-width: 0; display: flex; flex-direction: column; }
  .mcp-text .row-sub { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .mcp-acts { display: flex; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: 4px; margin-left: auto; max-width: 100%; }
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
  /* High Contrast's button colour is black: as a track it vanished, and a
     switch that was on was a lone dot. Its on state is the focus colour. */
  body.vscode-high-contrast .mcp-switch[aria-checked="true"] { background: var(--ct-accent); }
  .mcp-switch[aria-checked="true"]::after { transform: translateX(14px); }
  .mcp-switch:focus-visible { outline: 2px solid var(--ct-accent); outline-offset: 2px; }
  .mcp-acts .row-act { padding: 2px 6px; font-size: 11px; }

  /* ---- narrow side bars: nothing hides but the mode's word ---------------- */
  @media (max-width: 340px) {
    #modeBtnLabel { display: none; }
    #autoApplyToggle.pill { padding: 0 6px; }
    #appHeader { padding-left: 10px; padding-right: 8px; }
    #composer { padding: 8px 8px 10px; }
  }
`;
}
