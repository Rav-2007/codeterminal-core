// Runs inside the webview sandbox: no Node, no filesystem, no socket access
// -- only the DOM and postMessage to/from the extension host. All daemon
// I/O happens in daemonClient.ts; this script only renders what arrives and
// forwards what the user types.
(function () {
  const vscode = acquireVsCodeApi();

  const transcriptEl = document.getElementById('transcript');
  const groundingEl = document.getElementById('grounding');
  const historyNoticeEl = document.getElementById('historyNotice');
  const redactionsEl = document.getElementById('redactions');
  const degradedEl = document.getElementById('degraded');
  const providerEl = document.getElementById('provider');
  // NARROWED, not left as HTMLElement. These two are dereferenced for their
  // element-specific properties all over this file -- .value, .disabled,
  // .placeholder -- and every one of those was a separate "Property does not
  // exist on type 'HTMLElement'" finding counted against webview-check's
  // ceiling. Annotating the lookup once removes eleven of them, which is the
  // repair that gate asks for: narrow the DOM lookup rather than raise the
  // ceiling.
  //
  // Non-nullable on purpose, matching how the code already uses them: both are
  // static elements of chatPanelBodyMarkup and every call site dereferences
  // them unconditionally. If either id ever disappeared from the markup the
  // panel would fail loudly at load, which is the correct outcome for a missing
  // primary control -- not a quietly inert composer.
  const inputEl = /** @type {HTMLTextAreaElement} */ (document.getElementById('promptInput'));
  const sendBtn = /** @type {HTMLButtonElement} */ (document.getElementById('sendBtn'));
  const searchInputEl = /** @type {HTMLInputElement | null} */ (document.getElementById('searchInput'));
  const historyPopup = document.getElementById('historyPopup');
  const historyListEl = document.getElementById('historyList');
  const bookmarkBtn = document.getElementById('bookmarkBtn');
  const noticesEl = document.getElementById('notices');
  const noticesChip = document.getElementById('noticesChip');
  const noticesCountEl = document.getElementById('noticesCount');
  const slashMenuEl = document.getElementById('slashMenu');
  const modelChipEl = document.getElementById('modelChip');
  const modelChipLabelEl = document.getElementById('modelChipLabel');
  const specChipEl = document.getElementById('specChip');
  const newChatBtn = document.getElementById('newChatBtn');
  const historyBtn = document.getElementById('historyBtn');
  const attachBtn = document.getElementById('attachBtn');
  const attachListEl = document.getElementById('attachList');
  const composerShellEl = document.getElementById('composerShell');
  const effortBtn = document.getElementById('effortBtn');
  const effortValueEl = document.getElementById('effortValue');
  const modeBtnLabelEl = document.getElementById('modeBtnLabel');
  const contextBtn = document.getElementById('contextBtn');
  const contextPopup = document.getElementById('contextPopup');
  const contextPopupClose = document.getElementById('contextPopupClose');
  const contextPctEl = document.getElementById('contextPct');
  const contextTotalsEl = document.getElementById('contextTotals');
  const contextBarEl = document.getElementById('contextBar');
  const contextRowsEl = document.getElementById('contextRows');
  const contextRingFill = document.getElementById('contextRingFill');
  const contextPctShortEl = document.getElementById('contextPctShort');
  const modesPopup = document.getElementById('modesPopup');
  const modelPopup = document.getElementById('modelPopup');
  const modelListEl = document.getElementById('modelList');
  const modesList = document.getElementById('modesList');
  const modeBtnIcon = document.getElementById('modeBtnIcon');

  // One detached <svg> per mode, cloned out of the popup's own markup at load,
  // so the mode button's icon can be swapped WITHOUT assembling any markup.
  //
  // This replaced three SVG source strings and a `modeBtnIcon.innerHTML = ...`.
  // That assignment was not exploitable -- the three strings were constants and
  // the key was one of 'manual'/'plan'/'auto' -- and it is removed anyway,
  // because "there is no HTML sink in this file at all" is a property a reviewer
  // confirms with one grep, while "the only innerHTML here is safe" is a
  // judgement every future reader has to re-make. This webview renders model
  // output; once one blessed innerHTML is present, the second one arrives in a
  // diff that looks consistent with the file. accessibility.test.ts bans the
  // sinks categorically for exactly that reason, and it is the test that caught
  // this.
  //
  // Cloning parses nothing: the panel's HTML is server-rendered under the nonce
  // CSP, so these nodes are already-trusted DOM. It also removes a duplicate --
  // the same three icons were spelled out here AND in chatPanel.ts, so an icon
  // change had to be made twice to avoid the button disagreeing with the menu.
  /** @type {Record<string, Element>} */
  const modeIcons = {};
  document.querySelectorAll('.mode-item[data-mode]').forEach(function (item) {
    const mode = item.getAttribute('data-mode');
    const svg = item.querySelector('.mode-icon svg');
    if (mode && svg) {
      modeIcons[mode] = svg;
    }
  });

  /** @type {{ name: string, text: string }[]} */
  /**
   * @typedef {{ name: string, text: string, pending: boolean, id: string, note: string, desc: string }} Attachment
   * Files are chosen in the editor's dialog and read by the extension host
   * (onPickAttachments in chatPanel.ts); this list only holds the results.
   * `pending` is true while the host is still reading the file; `note` is shown
   * to the user (also progress, e.g. "OCR page 2 of 9"); `desc` says how `text`
   * was obtained and is shown to the model with it.
   * @type {Attachment[]}
   */
  let pendingAttachments = [];
  const MAX_ATTACH_FILES = 8;
  // FALLBACK CEILING ONLY, used until a turn has reported the real one. The
  // model's own limit arrives with TurnUsage.context_window and replaces it; a
  // flat 128000 was previously shown for every model, including the ones with a
  // 32k window (where it under-reported fullness by 4x) and the ones with a 1M
  // window (where it over-reported).
  const CONTEXT_LIMIT_TOKENS = 128000;
  const RING_CIRCUMFERENCE = 2 * Math.PI * 8; // r=8 in the SVG

  let lastGroundingInfo = null;
  let lastHistoryMeta = null;
  // lastUsage is the daemon's TurnUsage for the most recent turn that reached a
  // model -- the only non-estimated figure in the meter. Null until then, and
  // deliberately NOT cleared when a turn fails: the last true measurement is a
  // better answer than falling back to a guess.
  let lastUsage = null;
  // editorContextChars is the size of the editor-state block the host will
  // prepend to the next prompt (editorContext.ts). Posted by the host on every
  // editor or selection change, 0 when mochiii.sendEditorContext is off.
  let editorContextChars = 0;

  function estimateTokens(chars) {
    return Math.max(0, Math.ceil(chars / 4));
  }

  function formatTokenCount(n) {
    if (n >= 1000) {
      const k = n / 1000;
      return (k >= 10 ? k.toFixed(0) : k.toFixed(1).replace(/\.0$/, '')) + 'K';
    }
    return String(n);
  }

  function collectConversationChars() {
    let chars = 0;
    const msgs = transcriptEl.querySelectorAll('.msg.user .body, .msg.assistant .body');
    msgs.forEach((el) => {
      chars += (el.textContent || '').length;
    });
    return chars;
  }

  // THE CONTEXT METER, and what it is allowed to claim.
  //
  // It used to be fiction. Three constants -- a 1200-token system prompt, 400
  // tokens of "Rules", and `const tools = 2400` captioned "MCP / agent tool
  // schemas when enabled" -- were added into every total, and the tools line was
  // shown on every turn including the turns where the daemon sent no tools at
  // all, which until models.json enabled the read-only set was every turn. The
  // ceiling was a flat 128000 regardless of which model answered. So the meter
  // moved, looked precise, and was measuring nothing.
  //
  // TWO MODES NOW, and the popup says which one it is in.
  //
  // MEASURED: after a turn, the daemon sends TurnUsage -- context_tokens (the
  // prompt size of that turn's last model call) and context_window (that model's
  // own limit). Those are the provider's numbers, not ours. The total and the
  // ceiling come from them directly.
  //
  // ESTIMATED: before the first turn of a session there is no measurement, so
  // the meter estimates what THIS client is about to send and says "estimated".
  //
  // THE BREAKDOWN IS ESTIMATED IN BOTH MODES, because TurnUsage reports one
  // total and no decomposition -- the provider bills a prompt, not a budget. So
  // the parts we can actually measure locally (the composer's attachments, the
  // transcript, the editor block, the retrieved chunk count) are estimated from
  // their own bytes, and in measured mode the DIFFERENCE between the measured
  // total and those parts is shown as one residual row: the daemon's system
  // prompt, its tool schemas and whatever else it added. That row is derived,
  // never invented, and when the local estimate overshoots the measured total it
  // collapses to zero rather than going negative.
  function computeContextUsage() {
    const attachChars = pendingAttachments.reduce((sum, f) => sum + f.name.length + f.text.length, 0);
    const attachments = estimateTokens(attachChars);
    const chunks = (lastGroundingInfo && lastGroundingInfo.chunks) || 0;
    const retrieval = estimateTokens(chunks * 800); // ~800 chars/chunk estimate
    const conversation = estimateTokens(collectConversationChars());
    // What the open file costs. The host measures it (it is the only side with
    // the vscode API) and posts the byte count; 0 when the feature is off or no
    // workspace file is focused.
    const editor = estimateTokens(editorContextChars);

    const local = [
      { key: 'editor', label: 'Open file', tokens: editor, color: '#7dbe8a' },
      { key: 'retrieval', label: 'Retrieved context', tokens: retrieval, color: '#6eb0e0' },
      { key: 'attachments', label: 'Attachments', tokens: attachments, color: '#f0a060' },
      { key: 'conversation', label: 'Conversation', tokens: conversation, color: '#d4727d' },
    ];
    const localTotal = local.reduce((s, r) => s + r.tokens, 0);

    const measuredTotal = lastUsage && lastUsage.context_tokens > 0 ? lastUsage.context_tokens : 0;
    const measuredWindow = lastUsage && lastUsage.context_window > 0 ? lastUsage.context_window : 0;

    if (measuredTotal > 0) {
      const residual = Math.max(0, measuredTotal - localTotal);
      const rows = [
        { key: 'daemon', label: 'System prompt + tools', tokens: residual, color: '#c4a0e0' },
      ].concat(local);
      const limit = measuredWindow > 0 ? measuredWindow : CONTEXT_LIMIT_TOKENS;
      return {
        rows: rows,
        used: measuredTotal,
        pct: Math.min(100, Math.round((measuredTotal / limit) * 100)),
        limit: limit,
        measured: true,
        windowKnown: measuredWindow > 0,
        model: (lastUsage && lastUsage.model) || '',
      };
    }

    return {
      rows: local,
      used: localTotal,
      pct: Math.min(100, Math.round((localTotal / CONTEXT_LIMIT_TOKENS) * 100)),
      limit: CONTEXT_LIMIT_TOKENS,
      measured: false,
      windowKnown: false,
      model: '',
    };
  }

  function refreshContextUsage() {
    const usage = computeContextUsage();
    if (contextRingFill) {
      const offset = RING_CIRCUMFERENCE * (1 - usage.pct / 100);
      contextRingFill.setAttribute('stroke-dasharray', String(RING_CIRCUMFERENCE));
      contextRingFill.setAttribute('stroke-dashoffset', String(offset));
    }
    if (contextPctShortEl) {
      contextPctShortEl.textContent = usage.pct + '%';
    }
    if (contextBtn) {
      contextBtn.classList.toggle('warn', usage.pct >= 60 && usage.pct < 80);
      contextBtn.classList.toggle('hot', usage.pct >= 80);
      // The tilde belongs on an estimate and nowhere else: a measured figure
      // that apologises for itself teaches the user to distrust the one number
      // here that is actually true.
      const amount = usage.measured
        ? `${formatTokenCount(usage.used)} / ${formatTokenCount(usage.limit)}`
        : `~${formatTokenCount(usage.used)} / ${formatTokenCount(usage.limit)}`;
      const how = usage.measured ? 'last turn, measured' : 'estimated, nothing sent yet';
      contextBtn.title = `Context usage: ${usage.pct}% (${amount}) -- ${how}`;
    }
    if (!contextPopup || contextPopup.hidden) {
      return;
    }
    if (contextPctEl) {
      contextPctEl.textContent = usage.pct + '% Full';
    }
    if (contextTotalsEl) {
      // SAY WHICH NUMBER THIS IS. "128K" with no provenance is what let a
      // fabricated total pass for a measurement for as long as it did.
      const total = usage.measured
        ? formatTokenCount(usage.used) + ' / ' + formatTokenCount(usage.limit) + ' tokens'
        : '~' + formatTokenCount(usage.used) + ' / ' + formatTokenCount(usage.limit) + ' tokens';
      let note;
      if (!usage.measured) {
        note = ' estimated (no turn sent yet)';
      } else if (usage.windowKnown) {
        note = ' measured last turn' + (usage.model ? ' \u00b7 ' + usage.model : '');
      } else {
        note = ' measured last turn \u00b7 window not declared in models.json';
      }
      contextTotalsEl.textContent = total + note;
    }
    if (contextBarEl) {
      clearChildren(contextBarEl);
      usage.rows.forEach((row) => {
        if (row.tokens <= 0) {
          return;
        }
        const seg = document.createElement('div');
        seg.className = 'seg';
        seg.style.background = row.color;
        seg.style.flexGrow = String(Math.max(row.tokens, 1));
        seg.title = row.label + ': ' + formatTokenCount(row.tokens);
        contextBarEl.appendChild(seg);
      });
      const remain = Math.max(usage.limit - usage.used, 0);
      if (remain > 0) {
        const empty = document.createElement('div');
        // A CLASS, NOT AN INLINE COLOUR. This segment is the UNUSED part of the
        // window, and it was painted '#f3e8ea' -- a light pink that stayed light
        // on every theme, so on a dark one the bar looked almost entirely full
        // while being almost entirely empty. The class is defined in
        // chatPanelStyles() against the theme's own ink (see .context-bar), so
        // "free" now reads as absence on both.
        empty.className = 'seg free';
        empty.style.flexGrow = String(remain);
        empty.title = 'Free: ' + formatTokenCount(remain);
        contextBarEl.appendChild(empty);
      }
    }
    if (contextRowsEl) {
      clearChildren(contextRowsEl);
      usage.rows.forEach((row) => {
        const line = document.createElement('div');
        line.className = 'context-row';
        const swatch = document.createElement('span');
        swatch.className = 'swatch';
        swatch.style.background = row.color;
        const label = document.createElement('span');
        label.className = 'label';
        label.textContent = row.label;
        const count = document.createElement('span');
        count.className = 'count';
        count.textContent = formatTokenCount(row.tokens);
        line.appendChild(swatch);
        line.appendChild(label);
        line.appendChild(count);
        contextRowsEl.appendChild(line);
      });
    }
  }

  function setContextPopupOpen(open) {
    if (!contextPopup || !contextBtn) {
      return;
    }
    contextPopup.hidden = !open;
    contextBtn.setAttribute('aria-expanded', open ? 'true' : 'false');
    if (open) {
      refreshContextUsage();
    }
  }

  if (contextBtn) {
    contextBtn.addEventListener('click', (e) => {
      e.stopPropagation();
      setContextPopupOpen(contextPopup.hidden);
    });
  }
  if (contextPopupClose) {
    contextPopupClose.addEventListener('click', () => setContextPopupOpen(false));
  }
  document.addEventListener('click', (e) => {
    // A click's target is a Node; said once, here, for all three popups.
    const target = /** @type {Node} */ (e.target);
    if (contextPopup && !contextPopup.hidden) {
      if (!contextPopup.contains(target) && (!contextBtn || !contextBtn.contains(target))) {
        setContextPopupOpen(false);
      }
    }
    if (modesPopup && !modesPopup.hidden) {
      if (!modesPopup.contains(target) && (!autoApplyToggle || !autoApplyToggle.contains(target))) {
        setModesPopupOpen(false);
      }
    }
    if (modelPopup && !modelPopup.hidden) {
      if (!modelPopup.contains(target) && (!modelChipEl || !modelChipEl.contains(target))) {
        setModelPopupOpen(false);
      }
    }
    if (historyPopup && !historyPopup.hidden) {
      if (!historyPopup.contains(target) && (!historyBtn || !historyBtn.contains(target))) {
        setHistoryOpen(false);
      }
    }
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && pages && pages.isOpen()) {
      pages.close();
      return;
    }
    if (e.key === 'Escape') {
      setContextPopupOpen(false);
      if (typeof setModesPopupOpen === 'function') setModesPopupOpen(false);
      if (modelPopup && !modelPopup.hidden) {
        setModelPopupOpen(false);
        if (modelChipEl) {
          modelChipEl.focus();
        }
      }
      if (historyPopup && !historyPopup.hidden) {
        setHistoryOpen(false);
        if (historyBtn) {
          historyBtn.focus();
        }
      }
    }
  });

  refreshContextUsage();

  // Keep in sync with clients/vscode/src/slashCommands.ts (and TUI slash.go).
  // THE CATALOG COMES FROM THE HOST, and nothing is hardcoded here.
  //
  // This used to be a literal array of 24 entries transcribed from
  // src/slashCommands.ts. It had already drifted from the 27 the catalog
  // actually defines -- /spec, /connect and /team were simply missing from the
  // menu, so three commands existed and could not be discovered -- and nothing
  // could detect that, because the two lists had no relationship a compiler or
  // a test could see.
  //
  // Now the host posts SLASH_CATALOG itself ('slashCatalog') and this is where
  // it lands. Empty until it arrives, which is correct: a menu that lists
  // commands before it knows what they are would be guessing again.
  let SLASH_COMMANDS = [];

  let slashFilter = '';
  let slashIndex = 0;

  let streaming = false;
  let currentAssistantBubble = null;
  let currentAssistantCard = null;
  let currentAssistantRaw = '';
  // True while the current reply is command output (replyLocal): shown exactly
  // as written, not rendered as markdown.
  let currentAssistantPlain = false;
  let currentReasoningBody = null;
  let currentEditProposalEl = null;
  let pendingUndoButton = null;

  // currentRunAuto mirrors chatPanel.ts's currentRunAutoApply: captured from
  // autoApplyEnabled exactly once, in send(), at the moment a prompt goes
  // out -- never re-read from the live toggle afterward. This is what
  // showEditProposal/showEditResult consult to decide whether to render
  // clickable Apply/Skip buttons or a non-interactive auto-applied state for
  // every block belonging to THIS run, so a toggle flip mid-run cannot
  // retroactively change how an in-flight run is already being rendered.
  let currentRunAuto = false;

  // autoApplyEnabled is session-scoped ONLY: a plain in-memory flag with no
  // persistence, so it naturally resets to OFF whenever this script is
  // re-run (panel reload, new extension host) -- there is deliberately no
  // VS Code settings/disk write path for it. It is read once per prompt at
  // send() time and threaded onto the outgoing 'prompt' message; the host
  // captures that value per-run and never re-reads this variable mid-run
  // (see currentRunAutoApply in chatPanel.ts), so flipping this toggle
  // while a run is in flight has no effect until the next prompt.
  let currentMode = 'auto'; // 'manual', 'plan', 'auto'
  let autoApplyEnabled = true;

  const autoApplyToggle = document.getElementById('autoApplyToggle');

  function clearChildren(el) {
    while (el.firstChild) {
      el.removeChild(el.firstChild);
    }
  }

  // THE ⚙ POPOVER (Stage 9) is the model menu grown: the provider, its models,
  // the effort, the mode, the context meter. The model chip and the gear both
  // open it; it keeps the model popup's id, so everything that closed the
  // model menu closes it.
  function setModelPopupOpen(open) {
    if (!modelPopup || !modelChipEl) {
      return;
    }
    modelPopup.hidden = !open;
    modelChipEl.setAttribute('aria-expanded', open ? 'true' : 'false');
    const gear = document.getElementById('gearBtn');
    if (gear) gear.setAttribute('aria-expanded', open ? 'true' : 'false');
    if (open) {
      refreshContextUsage();
    }
  }

  function toggleSettings() {
    if (modelPopup && !modelPopup.hidden) {
      setModelPopupOpen(false);
      return;
    }
    setModesPopupOpen(false);
    if (controls) {
      controls.closeMenus();
      controls.opened();
    }
    showModelMenuMessage('Loading models\u2026');
    setModelPopupOpen(true);
    vscode.postMessage({ type: 'listModels' });
  }

  /** @param {string} text */
  function showModelMenuMessage(text) {
    if (!modelListEl) {
      return;
    }
    clearChildren(modelListEl);
    const row = document.createElement('div');
    row.className = 'model-empty';
    row.textContent = text;
    modelListEl.appendChild(row);
  }

  /**
   * @param {string} name tier name, '' for the default
   * @param {string} title
   * @param {string} desc
   * @param {boolean} selected
   * @param {boolean} enabled
   * @param {string} tag
   */
  function modelMenuItem(name, title, desc, selected, enabled, tag) {
    const item = document.createElement('button');
    item.type = 'button';
    item.className = 'mode-item';
    item.setAttribute('role', 'option');
    item.setAttribute('aria-selected', selected ? 'true' : 'false');
    item.setAttribute('data-tier', name);
    if (!enabled) {
      item.setAttribute('aria-disabled', 'true');
    }
    const text = document.createElement('div');
    text.className = 'mode-text';
    const nameEl = document.createElement('div');
    nameEl.className = 'mode-name';
    nameEl.textContent = title;
    if (tag) {
      const tagEl = document.createElement('span');
      tagEl.className = 'model-tag';
      tagEl.textContent = tag;
      nameEl.appendChild(tagEl);
    }
    const descEl = document.createElement('div');
    descEl.className = 'mode-desc';
    descEl.textContent = desc;
    text.appendChild(nameEl);
    text.appendChild(descEl);
    const check = document.createElement('div');
    check.className = 'mode-check';
    check.setAttribute('aria-hidden', 'true');
    check.textContent = '\u2713';
    item.appendChild(text);
    item.appendChild(check);
    return item;
  }

  /** @param {{ current?: string, tiers?: Array<{ name: string, slug: string, active: boolean, isDefault: boolean, quiet?: boolean }>, error?: string }} msg */
  function renderModelMenu(msg) {
    if (!modelListEl) {
      return;
    }
    if (msg.error) {
      showModelMenuMessage('Could not list models: ' + msg.error);
      return;
    }
    const tiers = msg.tiers || [];
    if (tiers.length === 0) {
      showModelMenuMessage('No models are configured.');
      return;
    }
    clearChildren(modelListEl);
    const current = msg.current || '';
    const short = (/** @type {string} */ slug) => slug.slice(slug.lastIndexOf('/') + 1);
    const def = tiers.find((t) => t.isDefault);
    modelListEl.appendChild(
      modelMenuItem('', 'Default', def ? 'Uses ' + short(def.slug) + ', the configured default' : 'The configured default model', current === '', true, '')
    );
    // Models that did not answer when the key was connected go last, dimmed:
    // still a choice, never the easy one (clients/tui/setup.go modelRows).
    const ordered = tiers.filter((t) => !t.quiet || current === t.name).concat(tiers.filter((t) => t.quiet && current !== t.name));
    for (const t of ordered) {
      // When a provider is connected its models ARE the tiers, so the tier name
      // and the model id are the same string; show it once.
      const title = short(t.slug);
      let desc = t.name === t.slug ? t.slug : t.slug + '  \u00b7  tier ' + t.name;
      const quiet = t.quiet === true && current !== t.name;
      if (quiet) desc += '  \u00b7  did not answer when the key was connected';
      const item = modelMenuItem(t.name, title, desc, current === t.name, t.active, t.active ? (t.isDefault ? 'default' : '') : 'inactive');
      if (quiet) item.classList.add('quiet');
      modelListEl.appendChild(item);
    }
    const selected = /** @type {HTMLElement | null} */ (modelListEl.querySelector('[aria-selected="true"]'));
    if (selected) {
      selected.focus();
    }
  }

  if (modelListEl) {
    modelListEl.addEventListener('click', (e) => {
      const target = /** @type {HTMLElement} */ (e.target);
      const item = /** @type {HTMLElement | null} */ (target.closest('.mode-item'));
      if (!item || item.getAttribute('aria-disabled') === 'true') {
        return;
      }
      vscode.postMessage({ type: 'selectModel', tier: item.getAttribute('data-tier') || '' });
      setModelPopupOpen(false);
      if (modelChipEl) {
        modelChipEl.focus();
      }
    });
    // Arrow keys move between models, as in any listbox.
    modelListEl.addEventListener('keydown', (e) => {
      if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') {
        return;
      }
      e.preventDefault();
      const items = Array.from(modelListEl.querySelectorAll('.mode-item'));
      const at = items.indexOf(/** @type {Element} */ (document.activeElement));
      const next = items[(at + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length];
      if (next) {
        /** @type {HTMLElement} */ (next).focus();
      }
    });
  }

  function setModesPopupOpen(open) {
    if (!modesPopup || !autoApplyToggle) {
      return;
    }
    modesPopup.hidden = !open;
    autoApplyToggle.setAttribute('aria-expanded', open ? 'true' : 'false');
  }

  function renderMode() {
    const isAuto = currentMode === 'auto';
    autoApplyEnabled = isAuto;

    const modeName = currentMode.charAt(0).toUpperCase() + currentMode.slice(1);

    if (modeBtnLabelEl) {
      modeBtnLabelEl.textContent = modeName;
    }

    // The accessible name must carry the CURRENT mode, not just the button's
    // purpose, and this is the line that makes that true.
    //
    // Auto is the mode in which the agent writes to the user's files WITHOUT
    // asking per edit (autoApplyEnabled, three lines up). The visible mode lives
    // in modeBtnLabelEl's textContent -- but the button carries a static
    // aria-label="Select mode", and an explicit aria-label OVERRIDES inner text
    // when the accessible name is computed. So a screen reader announced
    // "Select mode, button, collapsed" in all three modes, and a blind user had
    // no way to tell whether the next answer would edit their disk unattended.
    //
    // This property used to be enforced. Before the mode selector replaced the
    // two-state auto-apply switch, the control was role="switch" and a test
    // asserted its aria-checked was kept in sync, with the comment "its state
    // must not live only in textContent". The refactor moved the state into
    // exactly that, and correctly retargeted the test at the popup's
    // aria-expanded -- which is a different property, so nothing failed.
    autoApplyToggle.setAttribute('aria-label', 'Mode: ' + modeName + '. Select agent mode');
    if (modeBtnIcon) {
      const icon = modeIcons[currentMode] || modeIcons.auto;
      if (icon) {
        clearChildren(modeBtnIcon);
        modeBtnIcon.appendChild(icon.cloneNode(true));
      }
    }
    
    // Auto gets special on style, others get off style
    autoApplyToggle.className = isAuto ? 'pill mode-btn on' : 'pill mode-btn off';
    
    // Update selected state in popup
    if (modesList) {
      const items = modesList.querySelectorAll('.mode-item');
      items.forEach(item => {
        item.setAttribute('aria-selected', item.getAttribute('data-mode') === currentMode ? 'true' : 'false');
      });
    }
  }

  autoApplyToggle.addEventListener('click', (e) => {
    e.stopPropagation();
    if (modesPopup && !modesPopup.hidden) {
      setModesPopupOpen(false);
    } else {
      setContextPopupOpen(false);
      setModesPopupOpen(true);
    }
  });

  if (modesList) {
    modesList.addEventListener('click', (e) => {
      const item = e.target.closest('.mode-item');
      if (item) {
        currentMode = item.getAttribute('data-mode') || 'auto';
        renderMode();
        setModesPopupOpen(false);
      }
    });
  }

  renderMode();

  // Saved per-panel state (vscode.setState survives the panel being hidden or
  // reloaded). Read through this so the unknown it is typed as is checked once.
  /** @returns {Record<string, unknown>} */
  function savedState() {
    const st = vscode.getState();
    return st && typeof st === 'object' ? /** @type {Record<string, unknown>} */ (st) : {};
  }
  /** @param {Record<string, unknown>} patch */
  function saveState(patch) {
    vscode.setState(Object.assign({}, savedState(), patch));
  }

  // THINKING EFFORT. Sent with every prompt as reasoning_effort (chatPanel.ts
  // keeps the value; the daemon forwards it). Auto sends nothing and the model
  // uses its default -- which for some models, measured on Groq, is no reasoning
  // at all. The five dots this replaced changed nothing: the level was never sent.
  const EFFORTS = ['', 'low', 'medium', 'high'];
  const EFFORT_LABELS = ['Auto', 'Low', 'Medium', 'High'];
  let effortIndex = Math.max(0, EFFORTS.indexOf(String(savedState().effort || '')));
  // effortSupported is false only while the CURRENT model is known to reject
  // the reasoning_effort field (the daemon reports it as a
  // DegradedReasoningEffort notice, per model). Declared HERE, above
  // renderEffort, because renderEffort reads it and runs once at init -- a
  // `let` declared further down the file would be in its temporal dead zone at
  // that point, which is a ReferenceError and a blank panel, not a warning.
  //
  // Reset to true whenever the model changes: the latch is per-model, so a
  // verdict about one model says nothing about the next one the user picks, and
  // a control left greyed after a switch is the same lie in the other
  // direction.
  let effortSupported = true;
  let effortUnsupportedModel = '';

  function setEffortSupported(supported, model) {
    effortSupported = supported;
    effortUnsupportedModel = supported ? '' : model;
    renderEffort();
  }

  function renderEffort() {
    if (!effortBtn) {
      return;
    }
    const effortName = EFFORT_LABELS[effortIndex];
    effortBtn.setAttribute('data-level', String(effortIndex));
    if (effortValueEl) {
      // "Auto" twice in one row (effort and mode) read as one setting.
      effortValueEl.textContent = effortIndex === 0 ? 'Effort' : effortName;
    }
    // UNSUPPORTED IS A STATE, NOT A DISABLED BUTTON. The click still works and
    // the chosen value is still remembered and still sent -- it has to be,
    // because the user's next action may well be to switch to a model that
    // honours it, and a setting silently reset on them would be its own small
    // betrayal. What changes is that the control looks inert and says why.
    effortBtn.classList.toggle('unsupported', !effortSupported);
    if (effortSupported) {
      effortBtn.removeAttribute('aria-disabled');
      effortBtn.setAttribute('aria-label', 'Thinking effort: ' + effortName + '. Click to change.');
      effortBtn.title = 'Thinking effort: ' + effortName + '. Click to change (low / medium / high).';
      return;
    }
    const which = effortUnsupportedModel ? ' by ' + effortUnsupportedModel : ' by this model';
    // aria-disabled, never the disabled attribute: a screen-reader user is told
    // the control has no effect here, and can still reach it to read the reason.
    effortBtn.setAttribute('aria-disabled', 'true');
    effortBtn.setAttribute('aria-label', 'Thinking effort: ' + effortName + ', not supported' + which + '.');
    effortBtn.title =
      'Thinking effort is not supported' + which + ' -- the provider rejects the setting, ' +
      'so answers use the model\'s own default. Your choice (' + effortName + ') is kept and ' +
      'takes effect if you switch to a model that accepts it.';
  }
  function sendEffort() {
    vscode.postMessage({ type: 'setEffort', effort: EFFORTS[effortIndex] });
  }
  // setEffortLevel is the Settings page's way in: the same state the pill
  // cycles through, saved and sent the same way.
  /** @param {string} level */
  function setEffortLevel(level) {
    effortIndex = Math.max(0, EFFORTS.indexOf(level));
    renderEffort();
    saveState({ effort: EFFORTS[effortIndex] });
    sendEffort();
  }
  if (effortBtn) {
    effortBtn.addEventListener('click', () => {
      effortIndex = (effortIndex + 1) % EFFORTS.length;
      renderEffort();
      saveState({ effort: EFFORTS[effortIndex] });
      sendEffort();
    });
    renderEffort();
    sendEffort();
  }

  if (composerShellEl && inputEl) {
    inputEl.addEventListener('focus', () => composerShellEl.classList.add('focused'));
    inputEl.addEventListener('blur', () => composerShellEl.classList.remove('focused'));
  }

  if (newChatBtn) {
    newChatBtn.addEventListener('click', () => {
      vscode.postMessage({ type: 'newChat' });
    });
  }
  // HISTORY PANEL. Every chat, newest first, bookmarks pinned on top; the
  // search box asks the daemon, which matches what was said, not only titles.
  let historyQuery = '';
  /** @type {ReturnType<typeof setTimeout> | undefined} */
  let historySearchTimer;
  let currentBookmarked = false;

  /** @param {boolean} open */
  function setHistoryOpen(open) {
    if (!historyPopup || !historyBtn) {
      return;
    }
    historyPopup.hidden = !open;
    historyBtn.setAttribute('aria-expanded', open ? 'true' : 'false');
    if (open) {
      if (historyListEl) {
        clearChildren(historyListEl);
        const loading = document.createElement('div');
        loading.className = 'history-empty';
        loading.textContent = 'Loading chats\u2026';
        historyListEl.appendChild(loading);
      }
      vscode.postMessage({ type: 'historyList', text: historyQuery });
      if (searchInputEl) {
        searchInputEl.focus();
      }
    }
  }

  // "now", "5m", "3h", "2d", "3w", "4mo": the age column Claude Code shows.
  /** @param {string | undefined} iso */
  function ageOf(iso) {
    const t = iso ? Date.parse(iso) : NaN;
    if (Number.isNaN(t)) {
      return '';
    }
    const mins = Math.max(0, Math.round((Date.now() - t) / 60000));
    if (mins < 1) return 'now';
    if (mins < 60) return mins + 'm';
    const hours = Math.round(mins / 60);
    if (hours < 24) return hours + 'h';
    const days = Math.round(hours / 24);
    if (days < 14) return days + 'd';
    if (days < 60) return Math.round(days / 7) + 'w';
    return Math.round(days / 30) + 'mo';
  }

  /** @param {unknown} when @returns {Date | null} */
  function dateOf(when) {
    const t = typeof when === 'number' ? when : Date.parse(String(when || ''));
    return Number.isFinite(t) && t > 0 ? new Date(t) : null;
  }

  /** Which day group a chat falls in. @param {unknown} when */
  function dayGroup(when) {
    const d = dateOf(when);
    if (!d) return 'Earlier';
    const start = new Date();
    start.setHours(0, 0, 0, 0);
    const day = 24 * 60 * 60 * 1000;
    if (d.getTime() >= start.getTime()) return 'Today';
    if (d.getTime() >= start.getTime() - day) return 'Yesterday';
    if (d.getTime() >= start.getTime() - 6 * day) return 'Earlier this week';
    return 'Earlier';
  }

  /** A row's time: the clock today, the day this week, else the date. @param {unknown} when */
  function whenOf(when) {
    const d = dateOf(when);
    if (!d) return ageOf(typeof when === 'string' ? when : undefined);
    const group = dayGroup(when);
    if (group === 'Today' || group === 'Yesterday') return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    if (group === 'Earlier this week') return d.toLocaleDateString([], { weekday: 'short' });
    return d.toLocaleDateString([], { month: 'short', day: 'numeric' });
  }

  /**
   * The title with the searched words marked, built from text nodes.
   * @param {HTMLElement} el @param {string} text @param {string} query
   */
  function markMatch(el, text, query) {
    clearChildren(el);
    const q = query.trim().toLowerCase();
    const at = q ? text.toLowerCase().indexOf(q) : -1;
    if (at < 0) {
      el.textContent = text;
      return;
    }
    const mark = document.createElement('mark');
    mark.textContent = text.slice(at, at + q.length);
    el.append(document.createTextNode(text.slice(0, at)), mark, document.createTextNode(text.slice(at + q.length)));
  }

  /** @param {string} text */
  function historyMessage(text) {
    if (!historyListEl) {
      return;
    }
    clearChildren(historyListEl);
    const el = document.createElement('div');
    el.className = 'history-empty';
    el.textContent = text;
    historyListEl.appendChild(el);
  }

  /** @param {any} e */
  function historyRow(e) {
    const row = document.createElement('div');
    row.className = 'history-row' + (e.current ? ' current' : '');
    row.setAttribute('role', 'option');
    row.setAttribute('aria-selected', e.current ? 'true' : 'false');
    row.tabIndex = 0;
    const main = document.createElement('span');
    main.className = 'h-main';
    const title = document.createElement('span');
    title.className = 'h-title';
    markMatch(title, e.title || 'Untitled chat', historyQuery);
    title.title = e.title || '';
    const meta = document.createElement('span');
    meta.className = 'h-meta';
    meta.textContent = (e.current ? 'current chat · ' : '') + e.turns + ' message' + (e.turns === 1 ? '' : 's');
    main.appendChild(title);
    main.appendChild(meta);
    const age = document.createElement('span');
    age.className = 'h-age';
    age.textContent = e.current ? 'now' : whenOf(e.ended || e.started);
    const star = document.createElement('button');
    star.type = 'button';
    star.className = 'h-act h-star' + (e.bookmarked ? ' on' : '');
    star.textContent = e.bookmarked ? '\u2605' : '\u2606';
    star.title = e.bookmarked ? 'Remove bookmark' : 'Bookmark';
    star.setAttribute('aria-label', (e.bookmarked ? 'Remove bookmark from ' : 'Bookmark ') + (e.title || 'chat'));
    star.setAttribute('aria-pressed', e.bookmarked ? 'true' : 'false');
    star.addEventListener('click', (ev) => {
      ev.stopPropagation();
      vscode.postMessage({ type: 'historyBookmark', id: e.current ? '' : e.id, on: !e.bookmarked });
    });
    row.appendChild(main);
    row.appendChild(age);
    row.appendChild(star);
    if (!e.current) {
      const del = document.createElement('button');
      del.type = 'button';
      del.className = 'h-act h-del';
      del.textContent = '\u2715';
      del.title = 'Delete from history';
      del.setAttribute('aria-label', 'Delete ' + (e.title || 'chat') + ' from history');
      del.addEventListener('click', (ev) => {
        ev.stopPropagation();
        vscode.postMessage({ type: 'historyDelete', id: e.id });
      });
      row.appendChild(del);
    }
    const open = () => {
      if (e.current) {
        setHistoryOpen(false);
        return;
      }
      setHistoryOpen(false);
      vscode.postMessage({ type: 'historyResume', id: e.id });
    };
    row.addEventListener('click', open);
    row.addEventListener('keydown', (ev) => {
      if (ev.key === 'Enter' || ev.key === ' ') {
        ev.preventDefault();
        open();
      }
    });
    return row;
  }

  /** @param {{ entries?: any[], query?: string, error?: string }} msg */
  function renderHistory(msg) {
    if (!historyListEl || (msg.query || '') !== historyQuery) {
      return; // a reply to a search that has since changed
    }
    if (msg.error) {
      historyMessage('Could not load history: ' + msg.error);
      return;
    }
    const all = msg.entries || [];
    // The current chat is listed once: the daemon also lists its saved copy.
    const current = all.find((e) => e.current);
    const entries = all.filter((e) => e.current || !current || e.id !== current.saved_as);
    if (entries.length === 0) {
      historyMessage(historyQuery ? 'No chats mention \u201c' + historyQuery + '\u201d.' : 'No chats yet. Every chat you have will be listed here.');
      return;
    }
    clearChildren(historyListEl);
    // Bookmarks first, then the rest by day (Stage 9, the mockup's history):
    // Today, Yesterday, Earlier this week, Earlier.
    const pinned = entries.filter((e) => e.bookmarked);
    const recent = entries.filter((e) => !e.bookmarked);
    /** @param {string} label @param {any[]} list */
    const section = (label, list) => {
      if (list.length === 0) {
        return;
      }
      const h = document.createElement('div');
      h.className = 'history-section';
      h.textContent = label;
      historyListEl.appendChild(h);
      for (const e of list) {
        historyListEl.appendChild(historyRow(e));
      }
    };
    section('Bookmarked', pinned);
    /** @type {Record<string, any[]>} */
    const byDay = { Today: [], Yesterday: [], 'Earlier this week': [], Earlier: [] };
    for (const e of recent) {
      byDay[e.current ? 'Today' : dayGroup(e.ended || e.started)].push(e);
    }
    for (const label of Object.keys(byDay)) {
      section(label, byDay[label]);
    }
  }

  /** @param {boolean} on */
  function setCurrentBookmark(on) {
    currentBookmarked = on;
    if (bookmarkBtn) {
      const label = document.getElementById('bookmarkLabel');
      if (label) label.textContent = on ? 'Remove bookmark' : 'Bookmark this chat';
      bookmarkBtn.classList.toggle('on', on);
      bookmarkBtn.setAttribute('aria-pressed', on ? 'true' : 'false');
      bookmarkBtn.title = on ? 'Remove bookmark from this chat' : 'Bookmark this chat';
      bookmarkBtn.setAttribute('aria-label', bookmarkBtn.title);
    }
  }

  if (historyBtn) {
    historyBtn.addEventListener('click', (ev) => {
      ev.stopPropagation();
      setHistoryOpen(!historyPopup || historyPopup.hidden);
    });
  }
  if (searchInputEl) {
    searchInputEl.addEventListener('input', () => {
      if (historySearchTimer) {
        clearTimeout(historySearchTimer);
      }
      historySearchTimer = setTimeout(() => {
        historyQuery = searchInputEl.value.trim();
        vscode.postMessage({ type: 'historyList', text: historyQuery });
      }, 200);
    });
  }
  if (bookmarkBtn) {
    bookmarkBtn.addEventListener('click', () => {
      vscode.postMessage({ type: 'historyBookmark', id: '', on: !currentBookmarked });
    });
  }

  // NOTICES CHIP. The strips stay live regions (announced), folded visually
  // behind one chip. A notice is shown in full the FIRST time its text appears
  // in this panel's life -- so the Groq privacy note is read once -- and folds
  // away when the next prompt is sent.
  /** @type {Set<string>} */
  const seenNotices = new Set();
  function noticeTexts() {
    if (!noticesEl) {
      return [];
    }
    return Array.from(noticesEl.children)
      .filter((el) => el.id !== 'provider')
      .map((el) => (el.textContent || '').trim())
      .filter((t) => t !== '');
  }
  /** @param {boolean} open */
  function setNoticesOpen(open) {
    if (!noticesEl || !noticesChip) {
      return;
    }
    noticesEl.classList.toggle('collapsed', !open);
    noticesChip.setAttribute('aria-expanded', open ? 'true' : 'false');
  }
  function refreshNoticesChip() {
    if (!noticesChip || !noticesCountEl) {
      return;
    }
    const texts = noticeTexts();
    noticesChip.hidden = texts.length === 0;
    noticesCountEl.textContent = String(texts.length);
    noticesChip.title = texts.length ? texts.join('\n\n') : 'No notices';
    noticesChip.setAttribute('aria-label', texts.length + ' notice' + (texts.length === 1 ? '' : 's') + ' about the last answer. Show them.');
    let fresh = false;
    for (const t of texts) {
      if (!seenNotices.has(t)) {
        seenNotices.add(t);
        fresh = true;
      }
    }
    if (fresh) {
      setNoticesOpen(true);
    } else if (texts.length === 0) {
      setNoticesOpen(false);
    }
  }
  if (noticesEl) {
    new MutationObserver(refreshNoticesChip).observe(noticesEl, { childList: true, subtree: true, characterData: true });
  }
  if (noticesChip) {
    noticesChip.addEventListener('click', () => {
      setNoticesOpen(!!noticesEl && noticesEl.classList.contains('collapsed'));
    });
  }
  function queueCommand(text) {
    if (streaming) {
      return;
    }
    inputEl.value = text;
    send();
  }
  if (modelChipEl) {
    // A dropdown, not a chat command: this used to send "/model" as a message,
    // so clicking the chip printed a reply instead of opening a menu.
    modelChipEl.addEventListener('click', (e) => {
      e.stopPropagation();
      toggleSettings();
    });
  }

  function renderAttachments() {
    if (!attachListEl) {
      return;
    }
    clearChildren(attachListEl);
    if (pendingAttachments.length === 0) {
      attachListEl.hidden = true;
      return;
    }
    attachListEl.hidden = false;
    pendingAttachments.forEach((file, index) => {
      const chip = document.createElement('div');
      chip.className = 'attach-chip';
      const name = document.createElement('span');
      name.className = 'attach-name';
      name.textContent = file.pending
        ? file.name + ' (reading\u2026' + (file.note ? ' ' + file.note : '') + ')'
        : file.note ? file.name + ' \u00b7 ' + file.note : file.name;
      name.title = file.name;
      const remove = document.createElement('button');
      remove.type = 'button';
      remove.className = 'attach-remove';
      remove.setAttribute('aria-label', 'Remove ' + file.name);
      remove.textContent = '×';
      remove.addEventListener('click', () => {
        pendingAttachments = pendingAttachments.filter((f) => f !== file);
        renderAttachments();
      });
      chip.appendChild(name);
      chip.appendChild(remove);
      attachListEl.appendChild(chip);
    });
  }

  if (attachBtn) {
    attachBtn.addEventListener('click', () => {
      vscode.postMessage({ type: 'pickAttachments', remaining: MAX_ATTACH_FILES - pendingAttachments.length });
    });
  }

  // @ CONTEXT and THE PLUG (Stage 8). @ asks the host what to add -- a file,
  // the selection, a file's problems, an open file -- and each arrives as an
  // attachment chip, removable until sent. Typing @ at the start of a word
  // does the same; an @ inside a word (an address) is just text.
  const contextAddBtn = document.getElementById('contextAddBtn');
  const toolsBtn = document.getElementById('toolsBtn');
  const toolsCountEl = document.getElementById('toolsCount');
  function askForContext() {
    vscode.postMessage({ type: 'addContext', remaining: MAX_ATTACH_FILES - pendingAttachments.length });
  }
  // ＋ opens the add-context menu and @ the file search; both are controls.js's.
  void contextAddBtn;
  inputEl.addEventListener('input', (e) => {
    const ie = /** @type {InputEvent} */ (e);
    if (ie.data !== '@') {
      return;
    }
    const at = inputEl.selectionStart - 1;
    const before = at > 0 ? inputEl.value.charAt(at - 1) : '';
    if (at >= 0 && inputEl.value.charAt(at) === '@' && (before === '' || /\s/.test(before))) {
      inputEl.value = inputEl.value.slice(0, at) + inputEl.value.slice(at + 1);
      inputEl.selectionStart = inputEl.selectionEnd = at;
      askForContext();
    }
  });
  if (toolsBtn) {
    toolsBtn.addEventListener('click', () => {
      if (pages) pages.open('customize', { tab: 'mcp', sub: 'installed' });
    });
  }
  /** @param {any} msg */
  function setToolsCount(msg) {
    if (!toolsBtn || !toolsCountEl) {
      return;
    }
    const on = Number(msg.on) || 0;
    toolsCountEl.textContent = String(on);
    toolsCountEl.hidden = on === 0;
    const what = on === 0 ? 'no MCP servers on' : on + ' MCP server' + (on === 1 ? '' : 's') + ' on';
    const off = msg.agentMode === false && msg.total > 0 ? ' (agent mode is off, so none starts)' : '';
    toolsBtn.setAttribute('aria-label', 'MCP servers: ' + what + off);
    toolsBtn.title = 'MCP servers: ' + what + off + '. Click to manage them or browse for more.';
  }

  // dismissFirstRun drops the static first-run guidance (see chatPanel.ts's
  // #firstRun) once the transcript has real content. Deliberately NOT called
  // for 'error' bubbles: the commonest first error is "daemon not found", and
  // that is precisely when the instructions are still worth reading.
  //
  // Stage 9: the guidance is the welcome screen, and it comes BACK when the
  // chat is cleared (clearTranscriptView), so it is held, not looked up.
  const firstRunEl = document.getElementById('firstRun');
  function dismissFirstRun() {
    if (firstRunEl && firstRunEl.parentElement) {
      firstRunEl.remove();
    }
  }

  // A TRANSCRIPT, NOT A CHAT WINDOW.
  //
  // Every message used to carry an avatar circle with a single letter in it --
  // 'Y' for you, 'M' for Mochiii, '!' for an error -- plus a You / Mochiii
  // caption above the text. Two decorations per turn, on every turn, saying
  // something the reader already knew: in a two-party conversation laid out in
  // order, whose turn it is is obvious from the turn. Neither Claude Code nor
  // Cline labels every message this way, and that is most of what made this
  // panel read as a cheap chatbot bolted into an editor.
  //
  // WHAT CARRIES THE DISTINCTION NOW is layout, in the stylesheet: the user's
  // turn is a tinted, inset block and the assistant's is flush body text. That
  // is the same device a diff or a mail thread uses, and it survives being
  // skim-read, which a 20px initial does not.
  //
  // ERRORS KEEP A MARKER, and only errors. An error is the one case where the
  // source genuinely is not inferable -- an assistant message and a failure
  // notice are both "not from you", and confusing them matters. It gets a
  // visible label rather than a glyph, because '!' in a circle was decoration
  // where a word was needed.
  function roleLabel(role) {
    if (role === 'error') {
      return 'Error';
    }
    return '';
  }

  const brandLogo = /** @type {HTMLImageElement | null} */ (document.querySelector('.brand-logo'));
  /** @param {boolean} timed false for a restored turn: its time is not known */
  function replyHead(timed) {
    const head = document.createElement('div');
    head.className = 'reply-head';
    head.setAttribute('aria-hidden', 'true');
    if (brandLogo && brandLogo.src) {
      const img = document.createElement('img');
      img.className = 'reply-logo';
      img.src = brandLogo.src;
      img.alt = '';
      head.appendChild(img);
    }
    const name = document.createElement('span');
    name.className = 'reply-name';
    name.textContent = 'Mochiii';
    head.appendChild(name);
    if (timed) {
      const time = document.createElement('span');
      time.className = 'reply-time';
      time.textContent = new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
      head.appendChild(time);
    }
    return head;
  }

  /** @param {string} role @param {string} text @param {boolean} [restored] */
  function addBubble(role, text, restored) {
    if (role === 'user' || role === 'assistant') {
      dismissFirstRun();
    }
    const msg = document.createElement('div');
    msg.className = 'msg ' + role;

    const card = document.createElement('div');
    card.className = 'card';

    // SCREEN READERS STILL NEED THE SPEAKER. Dropping the visible caption must
    // not drop the fact: visual grouping is what replaces it for a sighted
    // reader, and an aria-label is what replaces it for everyone else. Without
    // this the change would trade a small aesthetic problem for a real
    // accessibility regression -- see accessibility.test.ts.
    msg.setAttribute('role', 'group');
    msg.setAttribute(
      'aria-label',
      role === 'user' ? 'Your message' : role === 'error' ? 'Error' : 'Mochiii\u2019s reply'
    );

    // MOCHIII'S REPLY CARRIES A HEAD (Stage 9, the mockups): the lotus, the
    // name and the time. The user's turn is a bubble on the right instead.
    if (role === 'assistant') {
      card.appendChild(replyHead(!restored));
    }

    const visibleLabel = roleLabel(role);
    if (visibleLabel) {
      const label = document.createElement('span');
      label.className = 'role';
      label.textContent = visibleLabel;
      card.appendChild(label);
    }

    const body = document.createElement('div');
    body.className = 'body';
    body.textContent = text;
    card.appendChild(body);

    msg.appendChild(card);
    transcriptEl.appendChild(msg);
    transcriptEl.scrollTop = transcriptEl.scrollHeight;

    if (role === 'assistant') {
      currentAssistantCard = card;
      currentAssistantRaw = text;
    }
    return body;
  }

  // A ONE-LINE NEUTRAL NOTE in the transcript -- a model switch, a compaction
  // in progress, a stopped answer. Not a bubble: it is ABOUT the conversation
  // rather than part of it, so it is never added to the transcript the model is
  // sent.
  //
  // Extracted from the 'info' message handler when the stop control needed the
  // same rendering; two copies of this would have drifted on the first change
  // to either.
  function addInfoLine(text) {
    const line = document.createElement('div');
    line.className = 'msg-info';
    line.setAttribute('role', 'status');
    const dot = document.createElement('span');
    dot.className = 'info-dot';
    dot.setAttribute('aria-hidden', 'true');
    const span = document.createElement('span');
    span.textContent = text;
    line.appendChild(dot);
    line.appendChild(span);
    // A status that arrives while the reply is still an empty bubble
    // ("Compacting: summarising…") goes ABOVE it: appended, it landed under the
    // answer that later filled the bubble, reading out of order.
    const pendingRow = currentAssistantCard && currentAssistantRaw === '' ? currentAssistantCard.parentElement : null;
    if (pendingRow && pendingRow.parentElement === transcriptEl) {
      transcriptEl.insertBefore(line, pendingRow);
    } else {
      transcriptEl.appendChild(line);
    }
    transcriptEl.scrollTop = transcriptEl.scrollHeight;
  }

  // renderFencedContent builds DOM for assistant text with ``` fences.
  // textContent / createElement ONLY -- never assemble markup strings.
  // MARKDOWN, SAFELY. Answers arrive as markdown and were shown raw -- "###" and
  // "**bold**" as literal characters. This renders the common subset (headings,
  // emphasis, inline code, links, lists, quotes, tables, rules) by creating
  // elements and setting textContent. It never assigns innerHTML, so nothing in
  // a model's reply can become markup or script, and a link is made only from an
  // http(s) URL. Fenced code blocks stay with renderFencedContent above.

  // Inline LaTeX the models write in prose ("Team $\rightarrow$ Problem"). Only
  // a $...$ span holding a backslash command is treated as math, so "$5 and $10"
  // is left alone; a span with any command not listed here is left as written.
  const LATEX_SYMBOLS = new Map([
    ['rightarrow', '→'], ['to', '→'], ['leftarrow', '←'], ['gets', '←'], ['Rightarrow', '⇒'],
    ['Leftarrow', '⇐'], ['leftrightarrow', '↔'], ['Leftrightarrow', '⇔'], ['implies', '⇒'],
    ['uparrow', '↑'], ['downarrow', '↓'], ['times', '×'], ['div', '÷'], ['pm', '±'], ['mp', '∓'],
    ['leq', '≤'], ['le', '≤'], ['geq', '≥'], ['ge', '≥'], ['neq', '≠'], ['ne', '≠'], ['approx', '≈'],
    ['sim', '∼'], ['equiv', '≡'], ['infty', '∞'], ['cdot', '·'], ['ldots', '…'], ['dots', '…'],
    ['cdots', '⋯'], ['checkmark', '✓'], ['degree', '°'], ['circ', '°'], ['alpha', 'α'], ['beta', 'β'],
    ['gamma', 'γ'], ['delta', 'δ'], ['Delta', 'Δ'], ['epsilon', 'ε'], ['theta', 'θ'], ['lambda', 'λ'],
    ['mu', 'μ'], ['pi', 'π'], ['sigma', 'σ'], ['Sigma', 'Σ'], ['omega', 'ω'], ['Omega', 'Ω'],
    ['sum', '∑'], ['prod', '∏'], ['sqrt', '√'], ['partial', '∂'], ['nabla', '∇'], ['in', '∈'],
    ['notin', '∉'], ['subset', '⊂'], ['subseteq', '⊆'], ['cup', '∪'], ['cap', '∩'], ['forall', '∀'],
    ['exists', '∃'], ['neg', '¬'], ['land', '∧'], ['lor', '∨'], ['quad', ' '], ['qquad', '  '],
    ['%', '%'], ['$', '$'], ['&', '&'], ['#', '#'], ['_', '_'], ['{', '{'], ['}', '}'],
  ]);
  /** @param {string} src @returns {string | null} */
  function latexToText(src) {
    if (src.indexOf('\\') < 0) {
      return null;
    }
    let known = true;
    const out = src
      .replace(/\\(?:text|mathrm|mathbf|textbf|mathit|operatorname)\{([^{}]*)\}/g, '$1')
      .replace(/\\([A-Za-z]+|[%$&#_{}])/g, (whole, name) => {
        const sym = LATEX_SYMBOLS.get(name);
        if (sym === undefined) {
          known = false;
          return whole;
        }
        return sym;
      })
      .replace(/[{}]/g, '')
      .trim();
    return known ? out : null;
  }

  const INLINE_MD =
    '(`+)([^`]|[^`][\\s\\S]*?[^`])\\1(?!`)' + // 1,2 inline code
    '|\\[([^\\]\\n]+)\\]\\(\\s*(https?:\\/\\/[^\\s)]+)\\s*\\)' + // 3,4 [text](url)
    '|\\*\\*(?=\\S)([\\s\\S]*?\\S)\\*\\*' + // 5 **strong**
    '|__(?=\\S)([\\s\\S]*?\\S)__' + // 6 __strong__
    '|~~(?=\\S)([\\s\\S]*?\\S)~~' + // 7 ~~strike~~
    '|(?<![\\w*])\\*(?=[^\\s*])([^*\\n]*?[^\\s*])\\*(?![\\w*])' + // 8 *em*
    '|(?<![\\w_])_(?=[^\\s_])([^_\\n]*?[^\\s_])_(?![\\w_])' + // 9 _em_ (not snake_case)
    '|(https?:\\/\\/[^\\s<>()]*[^\\s<>().,;:!?\'"\\]])' + // 10 bare URL
    '|\\$([^$\\n]{1,160})\\$'; // 11 $math$

  /** @param {string} href @param {string} text @returns {HTMLAnchorElement} */
  function mdLink(href, text) {
    const a = document.createElement('a');
    a.href = href;
    a.title = href;
    a.target = '_blank';
    a.rel = 'noopener noreferrer';
    // inLink: a link's own text is never parsed for links again. Without it a
    // bare URL linked to itself, re-matched as a URL, and recursed until the
    // stack overflowed -- found by the test, on the first URL it tried.
    renderInline(a, text, true);
    return a;
  }

  /** @param {Node} parent @param {string} text @param {boolean} [inLink] */
  function renderInline(parent, text, inLink) {
    // A fresh RegExp per call: this recurses, and a shared /g regex would have
    // its lastIndex moved by the inner call.
    const re = new RegExp(INLINE_MD, 'g');
    let last = 0;
    let m;
    while ((m = re.exec(text)) !== null) {
      if (m[0] === '') {
        re.lastIndex++;
        continue;
      }
      if (m.index > last) {
        parent.appendChild(document.createTextNode(text.slice(last, m.index)));
      }
      if (m[2] !== undefined) {
        const code = document.createElement('code');
        code.textContent = m[2].length > 2 && m[2].startsWith(' ') && m[2].endsWith(' ') ? m[2].slice(1, -1) : m[2];
        parent.appendChild(code);
      } else if (inLink && (m[3] !== undefined || m[10] !== undefined)) {
        parent.appendChild(document.createTextNode(m[0]));
      } else if (m[3] !== undefined) {
        parent.appendChild(mdLink(m[4], m[3]));
      } else if (m[5] !== undefined || m[6] !== undefined) {
        const strong = document.createElement('strong');
        renderInline(strong, m[5] !== undefined ? m[5] : m[6], inLink);
        parent.appendChild(strong);
      } else if (m[7] !== undefined) {
        const del = document.createElement('del');
        renderInline(del, m[7], inLink);
        parent.appendChild(del);
      } else if (m[8] !== undefined || m[9] !== undefined) {
        const em = document.createElement('em');
        renderInline(em, m[8] !== undefined ? m[8] : m[9], inLink);
        parent.appendChild(em);
      } else if (m[10] !== undefined) {
        parent.appendChild(mdLink(m[10], m[10]));
      } else {
        const math = latexToText(m[11]);
        parent.appendChild(document.createTextNode(math === null ? m[0] : math));
      }
      last = re.lastIndex;
    }
    if (last < text.length) {
      parent.appendChild(document.createTextNode(text.slice(last)));
    }
  }

  const MD_LIST = /^(\s*)([-*+]|\d{1,3}[.)])\s+(.*)$/;
  const MD_TABLE_SEP = /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/;
  /** @param {string} s */
  function indentOf(s) {
    const lead = /^\s*/.exec(s);
    return lead ? lead[0].replace(/\t/g, '    ').length : 0;
  }

  /** @param {Node} parent @param {string[]} lines @param {number} start @returns {number} */
  function renderList(parent, lines, start) {
    const first = MD_LIST.exec(lines[start]);
    if (!first) {
      return start + 1;
    }
    const indent = indentOf(first[1]);
    const ordered = /\d/.test(first[2]);
    const list = document.createElement(ordered ? 'ol' : 'ul');
    const startNum = parseInt(first[2], 10);
    if (ordered && startNum > 1) {
      list.setAttribute('start', String(startNum));
    }
    /** @type {HTMLLIElement | null} */
    let li = null;
    let i = start;
    while (i < lines.length) {
      const line = lines[i];
      if (/^\s*$/.test(line)) {
        // A blank line ends the list unless an item at this depth or deeper follows.
        let j = i + 1;
        while (j < lines.length && /^\s*$/.test(lines[j])) {
          j++;
        }
        const next = j < lines.length ? MD_LIST.exec(lines[j]) : null;
        if (next && indentOf(next[1]) >= indent) {
          i = j;
          continue;
        }
        break;
      }
      const m = MD_LIST.exec(line);
      if (m) {
        const depth = indentOf(m[1]);
        if (depth > indent && li) {
          i = renderList(li, lines, i);
          continue;
        }
        if (depth < indent || /\d/.test(m[2]) !== ordered) {
          break;
        }
        li = document.createElement('li');
        renderInline(li, m[3].replace(/^\[ \]\s+/, '☐ ').replace(/^\[[xX]\]\s+/, '☑ '));
        list.appendChild(li);
        i++;
        continue;
      }
      if (li && indentOf(line) > indent) {
        li.appendChild(document.createElement('br'));
        renderInline(li, line.trim());
        i++;
        continue;
      }
      break;
    }
    parent.appendChild(list);
    return i;
  }

  /** @param {string} row @returns {string[]} */
  function tableCells(row) {
    return row.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map((c) => c.trim());
  }

  /** @param {Node} root @param {string[]} lines */
  function renderBlocks(root, lines) {
    /** @type {string[]} */
    let para = [];
    const flush = () => {
      if (para.length === 0) {
        return;
      }
      const p = document.createElement('p');
      para.forEach((l, k) => {
        if (k > 0) {
          p.appendChild(document.createElement('br'));
        }
        renderInline(p, l);
      });
      root.appendChild(p);
      para = [];
    };
    let i = 0;
    while (i < lines.length) {
      const line = lines[i];
      if (/^\s*$/.test(line)) {
        flush();
        i++;
        continue;
      }
      const heading = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/.exec(line);
      if (heading) {
        flush();
        const h = document.createElement('h' + Math.min(4, heading[1].length));
        renderInline(h, heading[2]);
        root.appendChild(h);
        i++;
        continue;
      }
      if (/^\s{0,3}([-*_])(\s*\1){2,}\s*$/.test(line)) {
        flush();
        root.appendChild(document.createElement('hr'));
        i++;
        continue;
      }
      if (/^\s{0,3}>/.test(line)) {
        flush();
        const quote = document.createElement('blockquote');
        /** @type {string[]} */
        const inner = [];
        while (i < lines.length && /^\s{0,3}>/.test(lines[i])) {
          inner.push(lines[i].replace(/^\s{0,3}>\s?/, ''));
          i++;
        }
        renderBlocks(quote, inner);
        root.appendChild(quote);
        continue;
      }
      if (line.indexOf('|') >= 0 && i + 1 < lines.length && MD_TABLE_SEP.test(lines[i + 1]) && lines[i + 1].indexOf('-') >= 0) {
        flush();
        const wrap = document.createElement('div');
        wrap.className = 'md-table-wrap';
        const table = document.createElement('table');
        const thead = document.createElement('thead');
        const headRow = document.createElement('tr');
        for (const cell of tableCells(line)) {
          const th = document.createElement('th');
          renderInline(th, cell);
          headRow.appendChild(th);
        }
        thead.appendChild(headRow);
        table.appendChild(thead);
        const tbody = document.createElement('tbody');
        i += 2;
        while (i < lines.length && lines[i].indexOf('|') >= 0 && !/^\s*$/.test(lines[i])) {
          const tr = document.createElement('tr');
          for (const cell of tableCells(lines[i])) {
            const td = document.createElement('td');
            renderInline(td, cell);
            tr.appendChild(td);
          }
          tbody.appendChild(tr);
          i++;
        }
        table.appendChild(tbody);
        wrap.appendChild(table);
        root.appendChild(wrap);
        continue;
      }
      if (MD_LIST.test(line)) {
        flush();
        i = renderList(root, lines, i);
        continue;
      }
      para.push(line);
      i++;
    }
    flush();
  }

  /** @param {Node} container @param {string} text */
  function renderMarkdown(container, text) {
    const root = document.createElement('div');
    root.className = 'md';
    renderBlocks(root, String(text).replace(/\r\n?/g, '\n').split('\n'));
    container.appendChild(root);
  }

  function renderFencedContent(container, text) {
    clearChildren(container);
    const parts = String(text).split(/```/);
    for (let i = 0; i < parts.length; i++) {
      const chunk = parts[i];
      if (i % 2 === 0) {
        if (!chunk) {
          continue;
        }
        renderMarkdown(container, chunk);
        continue;
      }
      let lang = '';
      let code = chunk;
      const nl = chunk.indexOf('\n');
      if (nl >= 0) {
        lang = chunk.slice(0, nl).trim();
        code = chunk.slice(nl + 1);
      } else {
        lang = chunk.trim();
        code = '';
      }
      if (code.endsWith('\n')) {
        code = code.slice(0, -1);
      }
      const block = document.createElement('div');
      block.className = 'code-block';
      // A header on every block (Stage 9): its language, and a Copy of this
      // block alone -- the reply's own Copy takes the prose with it.
      const head = document.createElement('div');
      head.className = 'code-head';
      const langEl = document.createElement('span');
      langEl.className = 'code-lang';
      langEl.textContent = lang || 'code';
      const copy = document.createElement('button');
      copy.type = 'button';
      copy.className = 'code-copy';
      copy.textContent = 'Copy';
      copy.setAttribute('aria-label', 'Copy this code' + (lang ? ' (' + lang + ')' : ''));
      const text = code;
      copy.addEventListener('click', () => {
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(text).then(() => {
            copy.textContent = 'Copied';
            setTimeout(() => {
              copy.textContent = 'Copy';
            }, 1200);
          });
        }
      });
      head.appendChild(langEl);
      head.appendChild(copy);
      block.appendChild(head);
      const lines = code.split('\n');
      for (let n = 0; n < lines.length; n++) {
        const row = document.createElement('div');
        row.className = 'code-row';
        const ln = document.createElement('span');
        ln.className = 'ln';
        ln.textContent = String(n + 1);
        const lc = document.createElement('span');
        lc.className = 'lc';
        lc.textContent = lines[n];
        row.appendChild(ln);
        row.appendChild(lc);
        block.appendChild(row);
      }
      container.appendChild(block);
    }
  }

  function attachFeedbackFooter(card, rawText) {
    if (!card || card.querySelector('.msg-footer')) {
      return;
    }
    const footer = document.createElement('div');
    footer.className = 'msg-footer';

    const copyBtn = document.createElement('button');
    copyBtn.type = 'button';
    copyBtn.textContent = 'Copy';
    copyBtn.addEventListener('click', () => {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(rawText).then(() => {
          copyBtn.textContent = 'Copied';
          setTimeout(() => {
            copyBtn.textContent = 'Copy';
          }, 1200);
        });
      }
    });
    footer.appendChild(copyBtn);

    const up = document.createElement('button');
    up.type = 'button';
    up.textContent = '👍';
    up.setAttribute('aria-label', 'Helpful');
    const down = document.createElement('button');
    down.type = 'button';
    down.textContent = '👎';
    down.setAttribute('aria-label', 'Not helpful');
    up.addEventListener('click', () => {
      up.classList.add('active');
      down.classList.remove('active');
    });
    down.addEventListener('click', () => {
      down.classList.add('active');
      up.classList.remove('active');
    });
    footer.appendChild(up);
    footer.appendChild(down);

    const helpful = document.createElement('span');
    helpful.className = 'helpful';
    helpful.textContent = 'Was this helpful?';
    footer.appendChild(helpful);

    card.appendChild(footer);
  }

  function finalizeAssistant() {
    if (currentAssistantBubble && currentAssistantCard) {
      if (!currentAssistantPlain) {
        renderFencedContent(currentAssistantBubble, currentAssistantRaw);
      }
      attachFeedbackFooter(currentAssistantCard, currentAssistantRaw);
    }
    currentAssistantPlain = false;
    currentAssistantBubble = null;
    currentAssistantCard = null;
    currentAssistantRaw = '';
  }

  // Captured from the markup, not duplicated: setStreaming swaps the
  // placeholder for a "Stop interrupts it" line while a turn runs and has to
  // restore the real one afterwards. A second literal here would drift from
  // chatPanelBodyMarkup the first time either changed.
  const composerPlaceholder = inputEl.placeholder;

  function setStreaming(value) {
    streaming = value;
    // THE INPUT STAYS EDITABLE. It used to be disabled for the whole answer,
    // which meant you could not even begin drafting the follow-up -- and the
    // thought you had while reading the reply is exactly when you want to.
    // Sending is still refused while a turn is in flight (see send() and the
    // Enter handler); what is allowed is typing.
    inputEl.disabled = false;
    // And the button is never disabled now, because while streaming it is the
    // Stop button and must be clickable.
    sendBtn.disabled = false;
    if (composerShellEl) {
      composerShellEl.classList.toggle('sending', value);
    }
    // The button holds both icons (chatPanel.ts); .stop shows the square.
    sendBtn.classList.toggle('stop', value);
    sendBtn.title = value ? 'Stop this answer' : 'Send';
    sendBtn.setAttribute('aria-label', value ? 'Stop this answer' : 'Send');
    inputEl.placeholder = value
      ? 'Mochiii is answering \u2014 Stop interrupts it; you can type your next question now'
      : composerPlaceholder;
  }

  function setGrounding(info) {
    lastGroundingInfo = info || null;
    refreshContextUsage();
    if (!info) {
      groundingEl.textContent = '';
      return;
    }
    let text = info.grounded
      ? `grounded · ${info.chunks ?? 0} chunk(s)${info.truncated ? ' (truncated)' : ''}`
      : `not grounded${info.reason ? ' · ' + info.reason : ''}`;
    // The daemon indexes ONE workspace, chosen at its launch, and reports a
    // mismatch when the workspace this panel sent doesn't match it (see
    // protocol.GroundingInfo.WorkspaceMismatch, computed in
    // daemon/context.go's buildGroundingInfo). Untreated, that reads as a
    // perfectly normal "grounded · N chunk(s)" while the answer is actually
    // grounded in a DIFFERENT repository -- the one failure mode where the
    // indicator being reassuring is worse than it being absent. Appended, not
    // substituted: the grounded/not-grounded verdict is unchanged.
    if (info.workspace_mismatch) {
      text +=
        `  ⚠ the daemon is indexed on a different workspace` +
        `${info.workspace ? ' (' + info.workspace + ')' : ''}` +
        ` than the one open here — restart it against this folder for grounded answers`;
    }
    groundingEl.textContent = text;
  }

  // setHistoryInfo renders a warning when the daemon dropped the oldest
  // conversation turns this client sent (see protocol.HistoryInfo.truncated),
  // mirroring setGrounding's lifetime: cleared at the start of every turn (see
  // send()) and set at most once per response, since 'historyInfo' rides the
  // same pre-token message as 'grounding'. A non-truncated report shows nothing.
  function setHistoryInfo(info) {
    lastHistoryMeta = info || null;
    refreshContextUsage();
    historyNoticeEl.textContent =
      info && info.truncated
        ? '⚠ older conversation history was dropped to fit the model’s limit'
        : '';
  }

  // appendReasoning renders a reasoning-tier model's thinking tokens (see
  // protocol.TokenResponse.reasoning) as a dim, labelled "thinking" block placed
  // ABOVE the answer -- a visibly SEPARATE area, never spliced into the answer
  // text (which is what gets parsed for edits and stored as history). Before this
  // the tokens were dropped and the user watched a blank bubble while the model
  // thought. textContent (never assemble markup): thinking is daemon-relayed model
  // text. Accumulates across the many small chunks that arrive per turn.
  function appendReasoning(text) {
    if (!currentReasoningBody) {
      const container = document.createElement('div');
      container.className = 'turn reasoning';
      const label = document.createElement('span');
      label.className = 'role';
      label.textContent = 'thinking';
      container.appendChild(label);
      const body = document.createElement('span');
      container.appendChild(body);
      // Place the thinking block just before the (already-created) answer bubble
      // so it reads top-to-bottom as think-then-answer.
      const answerContainer =
        currentAssistantBubble &&
        currentAssistantBubble.parentElement &&
        currentAssistantBubble.parentElement.parentElement;
      if (answerContainer && answerContainer.parentElement === transcriptEl) {
        transcriptEl.insertBefore(container, answerContainer);
      } else {
        transcriptEl.appendChild(container);
      }
      currentReasoningBody = body;
    }
    currentReasoningBody.textContent += text;
    transcriptEl.scrollTop = transcriptEl.scrollHeight;
  }

  // setRedactions renders the kinds of secret-shaped text the daemon's
  // heuristic scrubber redacted from the prompt (see daemon/scrub.go),
  // mirroring setGrounding's shape exactly: cleared at the start of every
  // new turn (see send()) and set at most once per response, since
  // 'redactions' arrives as its own pre-token message just like
  // 'grounding'. Kinds only, never a matched value.
  function setRedactions(kinds) {
    if (!kinds || kinds.length === 0) {
      redactionsEl.textContent = '';
      return;
    }
    redactionsEl.textContent = `⚠ redacted ${kinds.length} suspected secret(s) before sending: ${kinds.join(', ')}`;
  }

  // setDegraded renders the subsystems the daemon reported as running in a
  // REDUCED mode (see protocol.TokenResponse.Degraded), one line each,
  // mirroring setGrounding/setRedactions exactly: cleared at the start of
  // every new turn (see send()) and set at most once per response, since
  // 'degraded' arrives on the same pre-token message as 'grounding'.
  //
  // Without this the panel showed "grounded · 3 chunk(s)" for a daemon whose
  // hybrid retrieval had silently collapsed to semantic-only. textContent
  // (never assemble markup) per line: detail is daemon-authored prose and is
  // inserted as text, not markup.
  function setDegraded(items) {
    clearChildren(degradedEl);
    if (!items || items.length === 0) {
      return;
    }
    for (const item of items) {
      const line = document.createElement('span');
      line.className = 'item';
      let text = `⚠ degraded (${item.component}): ${item.detail}`;
      if (item.component === 'workspace_too_large' && item.metadata && item.metadata.file_count) {
        const count = new Intl.NumberFormat().format(item.metadata.file_count);
        text = `⚠ degraded (${item.component}): workspace is too large (> 10,000 files, exactly ${count}), semantic search is disabled`;
      }
      line.textContent = text;
      degradedEl.appendChild(line);

      // THE EFFORT PICKER STOPS PRETENDING. The daemon reports, per model, that
      // this provider refused reasoning_effort -- so Low/Medium/High were three
      // labels on one behaviour. Greying the control is the honest rendering:
      // the setting is kept (switching model makes it live again) and it no
      // longer claims to do anything for the model that cannot use it.
      if (item.component === 'reasoning_effort') {
        const model = (item.metadata && item.metadata.model) || '';
        setEffortSupported(false, model);
      }
    }
  }


  // setProvider renders the upstream provider the daemon reported serving this
  // turn (see protocol.TokenResponse.Provider). Unlike setGrounding/setDegraded
  // the value arrives mid-stream (at or before the first token), not before
  // tokens, but is cleared at the start of every new turn (see send()) just the
  // same. A falsy/absent provider clears the line and shows nothing — absence
  // is the common case (OpenRouter does not guarantee the field) and is NOT an
  // error, so it gets neutral styling, never the warning treatment. textContent
  // (never assemble markup): the provider name is daemon-relayed text. Strictly
  // "served by X" — no fallback claim, no ZDR verdict.
  function setProvider(provider) {
    providerEl.textContent = provider ? `served by: ${provider}` : '';
  }

  // THE PANEL SAYS WHAT IT NEEDS. Until this block existed, a 0.1.1 install
  // with no key showed the user "Could not reach the daemon: daemon not found
  // (expected a lockfile at ...)" -- a sentence about an implementation detail
  // nobody installing an extension has a stake in -- and offered no way to
  // enter a key at all: the only two routes in were the command palette
  // (mochiii.setApiKey) and typing /connect, each of which you have to already
  // know exists. A user said exactly that on 2026-10-11, after installing on a
  // friend's machine: "there is no option to provide the api key also ... who
  // the once cares about the deamon weather it started or not".
  //
  // So the host now reports the connection as a STATE the user can act on --
  // { type: 'connection', state: 'ready' | 'noKey' | 'error', provider?,
  // model?, summary?, detail? } -- and these functions render it. summary is
  // already user-facing prose; detail is technical and is never shown until
  // somebody debugging asks for it.

  // showConnectionCta renders the 'noKey' empty state: the panel's own
  // statement that it needs a key, and the button that asks for one.
  //
  // The existing block is found by class rather than held in a variable
  // because clearTranscript (/clear) wipes the transcript wholesale -- a held
  // reference would outlive the node it names and this would quietly stop
  // finding anything to remove.
  function showConnectionCta() {
    // The host re-reports the connection (a reconnect, a window focus), so
    // this runs again with the panel already correct. The block is left
    // exactly where it is: removing and re-appending it would stack nothing,
    // but it would yank the one thing the user is reading to the bottom of the
    // transcript and scroll under their cursor.
    if (transcriptEl.querySelector('.conn-cta')) {
      return;
    }

    const cta = document.createElement('div');
    cta.className = 'conn-cta';

    const title = document.createElement('div');
    title.className = 'conn-cta-title';
    title.textContent = 'Mochiii needs an API key to answer';
    cta.appendChild(title);

    // WRITTEN FOR SOMEONE WHO JUST INSTALLED AN EXTENSION: what is missing,
    // where it comes from, and what the button does. It names no provider, because the
    // key itself is what tells the daemon which provider it belongs to
    // (daemon/connect_handler.go) -- a guess here would be a wrong one, and a
    // wrong provider name next to a credential is the kind of detail that
    // makes a user distrust the whole panel. It says nothing about lockfiles
    // or processes: nothing a first-time reader can act on is in there.
    const body = document.createElement('div');
    body.className = 'conn-cta-body';
    body.textContent =
      'Mochiii answers by sending your question to a model provider, and that needs an API key from your account there. ' +
      'Connect a model opens a box where you paste the key — it stays on this machine and is never written into your project files.';
    cta.appendChild(body);

    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'conn-cta-button';
    button.textContent = 'Connect a model';
    // addEventListener, never an onclick attribute: this webview's CSP is
    // `default-src none; script-src nonce-...`, under which an inline handler
    // does not run at all -- the button would look live and do nothing.
    //
    // Left ENABLED after the click. The host opens a masked input box, and
    // pressing Escape in one is an ordinary thing to do; a button that
    // disabled itself on first click would leave a cancelled setup with no
    // way back to it.
    button.addEventListener('click', () => {
      vscode.postMessage({ type: 'connect' });
    });
    cta.appendChild(button);

    transcriptEl.appendChild(cta);
    transcriptEl.scrollTop = transcriptEl.scrollHeight;
    // Focus is deliberately NOT moved to the button: the composer holds it
    // when the panel opens, and stealing it would interrupt someone already
    // typing. #transcript is a polite live region (see chatPanel.ts), so a
    // screen reader announces the block where it stands.
  }

  // removeConnectionCta drops the ask once a key is in use. Separate from
  // showConnectionCta so 'ready' can clear the block without knowing how it
  // was built.
  function removeConnectionCta() {
    const existing = transcriptEl.querySelector('.conn-cta');
    if (existing) {
      existing.remove();
    }
  }

  // setConnectionChip makes the model chip answer "which key am I using" at a
  // glance, which 0.1.1 could not: it showed a tier or a model and never named
  // the provider the key reached.
  //
  // Both halves are optional and a missing one is simply left out, NEVER
  // guessed: the daemon does not always know the provider, and a chip reading
  // "OpenRouter" over a Groq key is worse than one reading only the model --
  // that exact confusion is what connectFlow.ts was written to end. With
  // neither half the chip keeps whatever 'modelTier' last put there, because
  // replacing a true label with an empty one tells the user less.
  function setConnectionChip(provider, model) {
    const name = typeof provider === 'string' ? provider.trim() : '';
    const full = typeof model === 'string' ? model.trim() : '';
    // Same shortening as the 'modelTier' case below, for the same reason: the
    // slug's provider prefix does not fit the composer's width, and beside a
    // provider name it reads twice ("DeepSeek · deepseek/deepseek-v4-pro").
    // The full slug stays in the tooltip.
    const shortModel = full ? full.slice(full.lastIndexOf('/') + 1) : '';
    const label = [name, shortModel].filter((part) => part !== '').join(' · ');
    if (label === '') {
      return;
    }
    if (modelChipLabelEl) {
      modelChipLabelEl.textContent = label;
    }
    if (modelChipEl) {
      const inUse = name && full ? name + ' · ' + full : full || name;
      modelChipEl.title = 'In use: ' + inUse + '. Click to choose (/model).';
      modelChipEl.setAttribute('aria-label', 'Model: ' + label + '. Choose model.');
    }
  }

  // showConnectionError renders the 'error' state: the summary is what the
  // user reads, and detail -- the lockfile path, the ENOENT, the stack -- goes
  // behind a disclosure for whoever is debugging. That ordering is the whole
  // point: the technical text was the headline in 0.1.1 and it is still here,
  // one click away, for the case where somebody actually needs it.
  //
  // Rendered as an 'error' bubble so it carries the same avatar and label as
  // every other error in the transcript, and so it stays in the scrollback as
  // an honest record. Any .conn-cta showing is left alone: a failure while no
  // key is in use does not make the key less needed, and that button is the
  // way to try again.
  function showConnectionError(summary, detail) {
    const shown = typeof summary === 'string' && summary.trim() !== '' ? summary.trim() : '';
    // The host always sends a summary; this is the floor, so a message that
    // arrives without one is never an empty bubble with a disclosure under it.
    const body = addBubble('error', shown || 'Mochiii could not connect to your model provider.');

    const technical = typeof detail === 'string' ? detail.trim() : '';
    if (technical === '') {
      return;
    }
    const details = document.createElement('details');
    details.className = 'conn-detail';
    // No `open` attribute: collapsed is the default and the default is the
    // requirement.
    const label = document.createElement('summary');
    label.textContent = 'Show details';
    details.appendChild(label);
    const pre = document.createElement('pre');
    pre.className = 'conn-detail-body';
    // textContent (never assemble markup): detail is relayed text -- a daemon
    // error string, a provider's response, a path from the user's own disk.
    pre.textContent = technical;
    details.appendChild(pre);
    body.appendChild(details);
    transcriptEl.scrollTop = transcriptEl.scrollHeight;
  }

  // applyConnection renders the one state the host reported. Called for every
  // 'connection' message, repeats included.
  function applyConnection(msg) {
    if (msg.state === 'noKey') {
      showConnectionCta();
      return;
    }
    if (msg.state === 'ready') {
      // The ask has been answered -- drop it, and name who is answering.
      removeConnectionCta();
      setConnectionChip(msg.provider, msg.model);
      return;
    }
    if (msg.state === 'error') {
      showConnectionError(msg.summary, msg.detail);
      return;
    }
    // An unrecognised state renders nothing. A state a future host adds is
    // better absent from an older panel than shown as a block with no text in
    // it. Streaming state is deliberately untouched here: this message reports
    // the CONNECTION (it replaces runPreflight's daemon error, which arrives
    // before anybody has typed), while 'done' and 'error' own the composer.
  }

  // showIncomplete renders a persistent, visibly-distinct notice that the
  // model's answer was CUT OFF rather than finished (see
  // protocol.TokenResponse.Incomplete). It is appended to the transcript right
  // after the partial answer -- NOT a transient header like grounding/provider
  // that clears next turn -- so the scrollback keeps an honest record that this
  // reply is incomplete. textContent (never assemble markup): detail is daemon-relayed
  // prose. Arrives before 'done', while the answer bubble is still current.
  function showIncomplete(info) {
    const div = document.createElement('div');
    div.className = 'turn incomplete-notice';
    const detail = info && info.detail ? info.detail : 'the answer may be incomplete';
    div.textContent = `⚠ answer cut off: ${detail}`;
    // Continue picks up where it stopped: one click instead of typing "go on".
    const cont = document.createElement('button');
    cont.type = 'button';
    cont.className = 'turn-action';
    cont.textContent = 'Continue';
    cont.addEventListener('click', () => {
      cont.remove();
      sendText('Continue from where you stopped.');
    });
    div.appendChild(document.createTextNode(' '));
    div.appendChild(cont);
    transcriptEl.appendChild(div);
    transcriptEl.scrollTop = transcriptEl.scrollHeight;
  }

  // showEditProposal renders ONE edit block as a whole-block diff -- the
  // entire search text red, the entire replace text green, no word/line-
  // level diffing -- matching the TUI's non-LCS rendering (renderReviewPanel
  // in clients/tui/chat.go) so both surfaces present the same information
  // the same way. index/total place this block in the sequential review
  // (e.g. "Edit 2 of 3") -- the host walks the full block list one at a
  // time (see startEditReview/postCurrentBlockOrSummary in chatPanel.ts)
  // and this function is called once per block as the host advances.
  function showEditProposal(edit, index, total) {
    clearEditProposal();

    const container = document.createElement('div');
    container.className = 'edit-proposal';
    // This is Gate 4 -- the point where the user authorizes a write to their own
    // files -- and it presumed sight. role="group" with a label naming the file
    // and the change size gives a screen-reader user the same three facts a
    // sighted user reads off the header: which edit, which file, how big.
    // aria-live so an edit proposal arriving mid-transcript is announced.
    const removedCount = edit.search.split('\n').length;
    const addedCount = edit.replace.split('\n').length;
    container.setAttribute('role', 'group');
    container.setAttribute('aria-live', 'polite');
    container.setAttribute(
      'aria-label',
      `Proposed edit ${index + 1} of ${total} in ${edit.file_path}: ` +
        `${removedCount} line(s) removed, ${addedCount} line(s) added`
    );

    const indexEl = document.createElement('div');
    indexEl.className = 'edit-index';
    indexEl.textContent = `Edit ${index + 1} of ${total}`;
    container.appendChild(indexEl);

    const pathEl = document.createElement('div');
    pathEl.className = 'file-path';
    pathEl.textContent = edit.file_path;
    container.appendChild(pathEl);

    // The diff itself is now handled natively via the extension host,
    // so we just offer a button to view it instead of red/green lines.
    const diffBtn = document.createElement('button');
    diffBtn.textContent = 'View Native Diff';
    diffBtn.className = 'view-diff-btn';
    diffBtn.setAttribute('aria-label', `View diff for ${edit.file_path}`);
    diffBtn.addEventListener('click', () => {
      vscode.postMessage({ type: 'viewDiff', index: index });
    });
    container.appendChild(diffBtn);

    // HARD SAFETY REQUIREMENT: during an auto-apply run, no clickable
    // Apply/Skip buttons are rendered at all -- the host is already firing
    // the same ApplyEditRequest for this block on its own (see runAutoApply
    // in chatPanel.ts), and a stray click here could race it (e.g. applying
    // the wrong index, or double-applying). A non-interactive placeholder
    // communicates the same information without offering anything to click.
    if (currentRunAuto) {
      const pending = document.createElement('div');
      pending.className = 'result auto-pending';
      pending.textContent = 'auto-applying…';
      container.appendChild(pending);
    } else {
      const actions = document.createElement('div');
      actions.className = 'actions';
      // A labelled group, and buttons whose accessible names name the FILE.
      // "Apply" alone is ambiguous when several proposals are in the scrollback;
      // "Apply edit to daemon/server.go" is not.
      actions.setAttribute('role', 'group');
      actions.setAttribute('aria-label', `Approve or reject the proposed edit to ${edit.file_path}`);
      const applyBtn = document.createElement('button');
      applyBtn.textContent = 'Apply';
      applyBtn.setAttribute('aria-label', `Apply edit ${index + 1} of ${total} to ${edit.file_path}`);
      const skipBtn = document.createElement('button');
      skipBtn.textContent = 'Skip';
      skipBtn.setAttribute('aria-label', `Skip edit ${index + 1} of ${total} to ${edit.file_path}`);
      actions.appendChild(applyBtn);
      actions.appendChild(skipBtn);
      container.appendChild(actions);

      applyBtn.addEventListener('click', () => {
        applyBtn.disabled = true;
        skipBtn.disabled = true;
        applyBtn.textContent = 'Applying…';
        vscode.postMessage({ type: 'applyEdit' });
      });
      skipBtn.addEventListener('click', () => {
        vscode.postMessage({ type: 'skipEdit' });
        showEditResult(container, { skipped: true });
      });
    }

    transcriptEl.appendChild(container);
    transcriptEl.scrollTop = transcriptEl.scrollHeight;
    currentEditProposalEl = container;
  }

  // showEditResult replaces a proposal's Apply/Skip buttons (or, in an auto
  // run, the non-interactive "auto-applying…" placeholder) with the
  // outcome: the daemon's exact gate-refusal string on failure (the same
  // text the TUI would show for the same bad edit), or a success note with
  // the backup dir restore hint stays with `daemon edits undo`, matching
  // the TUI -- this slice adds no native undo button. The "auto-" prefix is
  // added only for text shown for THIS run (currentRunAuto), never
  // retroactively for a manual run.
  function showEditResult(container, result) {
    const actions = container.querySelector('.actions');
    if (actions) {
      actions.remove();
    }
    const pending = container.querySelector('.auto-pending');
    if (pending) {
      pending.remove();
    }
    const resultEl = document.createElement('div');
    // The outcome of authorizing a write to your files must be announced, not
    // just shown -- especially the refusal case, which carries the gate's reason.
    resultEl.setAttribute('role', 'status');
    if (result.skipped) {
      resultEl.className = 'result';
      resultEl.textContent = 'skipped';
    } else if (result.applied) {
      resultEl.className = 'result ok';
      const prefix = currentRunAuto ? 'auto-applied' : 'applied';
      resultEl.textContent = `${prefix} — backup: ${result.backupDir} (restore with: mochiii-daemon edits undo)`;
    } else {
      resultEl.className = 'result refused';
      const prefix = currentRunAuto ? 'auto-refused' : 'refused';
      resultEl.textContent = `${prefix}: ${result.error}`;
    }
    container.appendChild(resultEl);

    // What the gates LEARNED, under what they decided. Secondary on purpose:
    // the outcome is the headline and these qualify it. A Tier B delimiter
    // advisory in particular must not read as a failure -- the edit applied.
    //
    // Not announced (no role="status"): the outcome above already is, and
    // interrupting a screen reader twice for one action is worse than once.
    const notes = [result.syntaxNote, result.matchNote].filter(Boolean);
    if (result.applied && notes.length > 0) {
      const notesEl = document.createElement('div');
      notesEl.className = 'result-notes';
      notesEl.textContent = notes.join(' · ');
      container.appendChild(notesEl);
    }

    if (currentEditProposalEl === container) {
      currentEditProposalEl = null;
    }
  }

  // showEditRejections reports blocks the daemon's parser could not read.
  //
  // It is deliberately NOT attached to a proposal bubble: these have no
  // proposal to attach to, and the case that matters most is the one where
  // there are no proposals at all -- a reply whose every edit was malformed,
  // which used to render as a perfectly ordinary answer that silently did
  // nothing.
  function showEditRejections(rejections) {
    if (!rejections || rejections.length === 0) {
      return;
    }
    const el = document.createElement('div');
    el.className = 'edit-rejections';
    el.setAttribute('role', 'status');
    const heading = document.createElement('div');
    heading.className = 'edit-rejections-heading';
    heading.textContent =
      rejections.length === 1
        ? '1 proposed edit could not be read'
        : `${rejections.length} proposed edits could not be read`;
    el.appendChild(heading);
    for (const r of rejections) {
      const line = document.createElement('div');
      line.className = 'edit-rejection';
      line.textContent = `line ${r.line}: ${r.reason}`;
      el.appendChild(line);
    }
    // transcriptEl, and the scroll idiom used by every other renderer in this
    // file (see appendMessage and the answer container).
    //
    // THIS SAID `messages.appendChild(el); scrollToBottom();` UNTIL 2026-09-02,
    // and neither name exists anywhere in this file -- so every call threw
    // ReferenceError and the user saw nothing. The feature's own reason for
    // existing, in daemonClient.ts, is that a reply whose every edit was
    // malformed "sends rejections and no proposals, which is the case where the
    // user is most owed an explanation and previously got silence". The bug
    // delivered exactly the silence the feature was built to end.
    //
    // Found by turning on checkJs for this file, which had never been
    // typechecked or linted by anything.
    transcriptEl.appendChild(el);
    transcriptEl.scrollTop = transcriptEl.scrollHeight;
  }

  // The spec workflow's reports -- whether the change was built or tested, a
  // check's verdicts, an accepted spec. PLAIN TEXT through textContent only:
  // the text carries the model's words and file names, and none of it is
  // markup.
  function showSpecNotice(kind, text) {
    if (!text) {
      return;
    }
    const el = document.createElement('div');
    el.className = 'spec-notice spec-notice-' + String(kind || '').replace(/[^a-z-]/g, '');
    el.setAttribute('role', 'status');
    el.textContent = text;
    transcriptEl.appendChild(el);
    transcriptEl.scrollTop = transcriptEl.scrollHeight;
  }

  // A build's task list, updated IN PLACE while the turn runs, so the user
  // sees what it is doing and what is left; the last version stays in the
  // transcript when the turn ends.
  /** @type {HTMLDivElement | null} */
  let currentTasksEl = null;
  // THE STEPS (Stage 9, the mockup's "Planning steps"): the spec workflow's
  // task list as a checklist -- done, the one under way, blocked, still to do.
  // The plain text stays as the region's accessible name.
  /** @param {string} text @param {Array<{ title: string, status: string }>} [items] */
  function showTasks(text, items) {
    if (!currentTasksEl) {
      currentTasksEl = document.createElement('div');
      currentTasksEl.className = 'spec-notice spec-notice-tasks';
      currentTasksEl.setAttribute('role', 'status');
      currentTasksEl.setAttribute('aria-live', 'polite');
      transcriptEl.appendChild(currentTasksEl);
    }
    if (!Array.isArray(items) || items.length === 0) {
      currentTasksEl.textContent = String(text || '');
      transcriptEl.scrollTop = transcriptEl.scrollHeight;
      return;
    }
    clearChildren(currentTasksEl);
    currentTasksEl.classList.add('steps');
    currentTasksEl.setAttribute('aria-label', String(text || ''));
    const done = items.filter((t) => t.status === 'done').length;
    const head = document.createElement('div');
    head.className = 'steps-head';
    head.textContent = (done === items.length ? 'Steps done' : 'Working through the steps\u2026') + '  ' + done + ' of ' + items.length;
    currentTasksEl.appendChild(head);
    const list = document.createElement('ol');
    list.className = 'steps-list';
    list.setAttribute('aria-hidden', 'true');
    /** @type {Record<string, string>} */
    const marks = { done: '\u2713', active: '\u25cc', blocked: '\u2715' };
    for (const t of items) {
      const li = document.createElement('li');
      li.setAttribute('data-status', t.status);
      const mark = document.createElement('span');
      mark.className = 'step-mark';
      mark.textContent = marks[t.status] || '';
      const title = document.createElement('span');
      title.className = 'step-title';
      title.textContent = t.title;
      li.append(mark, title);
      list.appendChild(li);
    }
    currentTasksEl.appendChild(list);
    transcriptEl.scrollTop = transcriptEl.scrollHeight;
  }

  // setActiveSpec shows the spec every prompt now works to, or hides the chip.
  function setActiveSpec(spec) {
    if (!specChipEl) {
      return;
    }
    const name = String(spec || '').split('/').pop().replace(/\.md$/i, '');
    specChipEl.hidden = !name;
    specChipEl.textContent = name ? 'spec: ' + name : '';
  }

  // clearEditProposal removes a still-pending (not yet applied/skipped)
  // proposal panel when a new prompt is sent -- a lagging Apply click must
  // never be able to target a stale block from a previous turn.
  function clearEditProposal() {
    if (currentEditProposalEl && currentEditProposalEl.parentElement) {
      currentEditProposalEl.remove();
    }
    currentEditProposalEl = null;
  }

  // ---------------------------------------------------------------- tool consent

  // currentApprovalEl is the panel for the tool call the daemon is currently
  // holding open, and currentApprovalCallId is the call it belongs to. Both are
  // null except while a turn is suspended waiting for the user.
  let currentApprovalEl = null;
  let currentApprovalCallId = null;
  // toolActivityEls maps a call id to its row in the work block, so a call's
  // row is REWRITTEN from "running" to its outcome rather than a row per phase.
  const toolActivityEls = new Map();

  // showToolApproval renders one pending tool call and waits for a decision.
  //
  // ACCESSIBILITY IS BUILT IN HERE, NOT ADDED LATER. The Phase 3 review found
  // Gate 4 -- the edit-approval flow -- unusable non-visually, because it had
  // been built for the eye and given roles afterwards. This is the same kind of
  // moment (a person authorizing something with real consequences) and gets the
  // same treatment from the start:
  //
  //   - role="alertdialog" + aria-modal: this is not a notification, it is a
  //     question that has stopped everything until it is answered.
  //   - aria-live="assertive": the ONLY assertive region in this panel. A
  //     streamed answer is polite because interrupting it is rude; a blocking
  //     security question is exactly the thing that should interrupt.
  //   - aria-describedby pointing at the arguments and the confinement warning,
  //     so the buttons are not announced as bare verbs with no object.
  //   - initial focus on DENY. That is the accessible form of `[y/N]`: a user
  //     who hits Enter on the default does the safe thing. Focusing Approve
  //     would make a reflex keystroke authorise an unsandboxed subprocess.
  function showToolApproval(req) {
    clearToolApproval();

    const argsId = 'approval-args-' + req.call_id;
    const laneId = 'approval-lane-' + req.call_id;

    const container = document.createElement('div');
    container.className = 'tool-approval';
    container.setAttribute('role', 'alertdialog');
    container.setAttribute('aria-modal', 'true');
    container.setAttribute('aria-live', 'assertive');
    const question = approvalQuestion(req);
    container.setAttribute('aria-label', 'Approval needed: ' + question);
    container.setAttribute('aria-describedby', argsId + ' ' + laneId);

    // ONE QUESTION, then a warning only when there is a real risk -- the same
    // two lines, in the same words, as the terminal (clients/tui/chat.go
    // renderApprovalPanel). The heading carries the arguments IN FULL: the
    // daemon binds approval to a digest of all of them, so none is hidden.
    const heading = document.createElement('div');
    heading.id = argsId;
    heading.className = 'approval-heading';
    heading.setAttribute('role', 'region');
    heading.setAttribute('aria-label', 'Arguments this tool will receive');
    heading.textContent = question;
    container.appendChild(heading);

    const lane = document.createElement('div');
    lane.id = laneId;
    lane.className = 'approval-lane unconfined';
    const risks = [];
    if (req.reaches_network) {
      risks.push('LEAVES YOUR MACHINE: sent to a third party over the internet');
      // Why this one is asked about when the tool is allowed. Kept word for
      // word with the TUI's egressAddressRisk / egressQueryRisk.
      if (req.egress_review === 'address') {
        risks.push('THE AGENT WROTE THIS ADDRESS ITSELF: you did not type it and no search returned it, ' +
          'and everything in it is sent to that site — check it carries nothing of yours');
      } else if (req.egress_review === 'query') {
        risks.push('WRITTEN AFTER THE AGENT READ FILES OR PAGES IN THIS TURN: check it carries nothing of yours');
      }
    } else if (req.launches_subprocess) {
      risks.push(req.program
        ? 'STARTS ' + req.program + ', which keeps running until the daemon exits'
        : 'STARTS ANOTHER PROGRAM: a language server (gopls, tsserver or pyright)');
    } else if (req.program) {
      risks.push('ASKS ' + req.program + ', WHICH IS ALREADY RUNNING: starts nothing new');
    } else if (req.outside_path) {
      // The question already names the path; nothing to add.
    } else if (req.confined) {
      // A confined built-in: no warning -- an alarm on everything is an alarm
      // on nothing.
    } else {
      // NEVER SOFTENED: a third-party MCP server is an ordinary subprocess
      // with the user's full access, and this approval is all that stands in
      // front of it.
      risks.push('NOT SANDBOXED: a separate program with your full access');
    }
    // A command with no private copy to run in. Kept word for word with the
    // TUI's inPlaceRisk.
    if (req.in_place) {
      risks.push('RUNS IN YOUR REAL PROJECT, not in a private copy: whatever it writes stays, unreviewed — ' +
        'including .git/hooks, which run later with your full access');
    }
    if (req.destructive) {
      risks.push('marked destructive');
    }
    if (risks.length > 0) {
      lane.textContent = risks.join(' · ');
      container.appendChild(lane);
    }

    const actions = document.createElement('div');
    actions.className = 'approval-actions';

    const deny = approvalButton('No', 'deny', req, 'Do not run this tool call');
    const approve = approvalButton('Yes', 'approve', req, 'Run this tool call with these exact arguments');
    const forTurn = approvalButton(
      'Yes for this task',
      'approve_for_turn',
      req,
      // For a tool that runs commands the daemon covers only this exact
      // command; the old wording promised "any later call to the same tool".
      'Run this call, and the same call again, for the rest of this task (a different command still asks)'
    );
    // Offered only where the daemon offered it and a spec is active.
    const forSpec = req.spec_grant
      ? approvalButton(
          'Yes while this spec is active',
          'approve_for_spec',
          req,
          'Run this exact command now, and again without asking while the active spec stays on; any other command still asks'
        )
      : null;
    const cancel = approvalButton('Stop the task', 'cancel_turn', req, 'Deny this call and abandon the whole task');

    // Deny first in DOM order as well as focus order: tab order should reach
    // the safe option before the permissive one.
    actions.appendChild(deny);
    actions.appendChild(approve);
    actions.appendChild(forTurn);
    if (forSpec) {
      actions.appendChild(forSpec);
    }
    actions.appendChild(cancel);
    container.appendChild(actions);

    transcriptEl.appendChild(container);
    transcriptEl.scrollTop = transcriptEl.scrollHeight;

    currentApprovalEl = container;
    currentApprovalCallId = req.call_id;
    deny.focus();
  }

  // approvalQuestion mirrors clients/tui/chat.go approvalQuestion: what will
  // happen, in words. A short form is used only when its field is the WHOLE
  // of the arguments; otherwise every argument is shown.
  function approvalQuestion(req) {
    let args = {};
    try {
      args = JSON.parse(req.arguments || '{}') || {};
    } catch (e) {
      args = {};
    }
    const only = (k) =>
      Object.keys(args).length === 1 && typeof args[k] === 'string' ? args[k] : '';
    if (req.outside_path) {
      return 'Read outside this project: ' + req.outside_path + ' ?';
    }
    if (only('command')) {
      return 'Run command: ' + only('command') + ' ?';
    }
    if (req.reaches_network && only('query')) {
      return 'Search the web for: ' + only('query') + ' ?';
    }
    if (req.reaches_network && only('url')) {
      return 'Open web page: ' + only('url') + ' ?';
    }
    let name = req.tool;
    if (req.server && req.server !== 'builtin') {
      name = req.server + '/' + name;
    }
    const compact = (req.arguments || '').split(/\s+/).join(' ').trim();
    if (!compact || compact === '{}') {
      return 'Allow ' + name + ' ?';
    }
    return 'Allow ' + name + ': ' + compact + ' ?';
  }

  function approvalButton(label, decision, req, description) {
    const btn = document.createElement('button');
    btn.className = 'approval-btn ' + decision;
    btn.textContent = label;
    btn.setAttribute('aria-label', label + ': ' + description);
    btn.addEventListener('click', () => {
      // The call id rides along so a lagging click cannot answer a question the
      // user never saw; the extension host drops a mismatch, and the daemon
      // re-checks the id and the argument digest behind that.
      if (currentApprovalCallId !== req.call_id) {
        return;
      }
      answerToolApproval(req, decision, label);
    });
    return btn;
  }

  function answerToolApproval(req, decision, label) {
    vscode.postMessage({ type: 'toolApprovalDecision', callId: req.call_id, decision: decision });
    clearToolApproval();

    // A permanent record in the transcript, because "did I approve that?" is a
    // question worth being able to answer by scrolling back. role="status" so
    // it is announced as the outcome of the choice just made.
    const note = document.createElement('div');
    note.className = 'approval-record';
    note.setAttribute('role', 'status');
    note.textContent = label + ' — ' + req.server + '__' + req.tool;
    transcriptEl.appendChild(note);
    transcriptEl.scrollTop = transcriptEl.scrollHeight;
    inputEl.focus();
  }

  function clearToolApproval() {
    if (currentApprovalEl && currentApprovalEl.parentElement) {
      currentApprovalEl.remove();
    }
    currentApprovalEl = null;
    currentApprovalCallId = null;
  }

  // THE WORK BLOCK (Stage 8): a turn's tool calls as ONE folded line --
  // "Used 3 tools · 1 failed" -- that opens to a row per call.
  //
  // It replaced one italic line per call ("builtin__repo_map — 67 bytes to the
  // model in 0ms"), which filled a narrow side bar with plumbing and, on a
  // failure, said only "failed". A row now names what the call did in words,
  // says why it failed when it did (the daemon sends the reason since Stage 8),
  // and keeps the byte and time figures in its tooltip.
  //
  // The 'requested' and 'approved' phases are deliberately not rendered: the
  // user has just answered a dialog about that exact call, and reading it back
  // to them is noise -- worse for a screen reader than for an eye.
  const TOOL_LABELS = {
    repo_map: 'Mapped the project',
    list_directory: 'Listed a folder',
    read_file: 'Read a file',
    search_code: 'Searched the code',
    propose_edit: 'Proposed an edit',
    propose_ast_edit: 'Proposed an edit',
    query_compiler_definition: 'Looked up a definition',
    query_compiler_references: 'Looked up references',
    sandbox_exec: 'Ran a command in the sandbox',
    web_fetch: 'Fetched a web page',
    web_search: 'Searched the web',
  };
  const WORK_STATE_WORDS = { run: 'running', ok: 'done', fail: 'failed', denied: 'not run', step: '' };
  // The badge each row carries (Stage 9, the mockup's "Tool Execution" card).
  const WORK_BADGES = { run: 'Running', ok: 'Done', fail: 'Failed', denied: 'Not run', step: '' };
  const WORK_GLYPHS = { run: '\u25cc', ok: '\u2713', fail: '\u2715', denied: '\u2298', step: '\u203a' };
  // compact: a block starts folded; expanded: it starts open (mochiii.toolActivity).
  let toolActivityMode = 'compact';
  let defaultModeApplied = false;
  /** @type {HTMLDetailsElement | null} */
  let workBlockEl = null;

  /** @param {number} ms */
  function workDuration(ms) {
    return ms < 1000 ? Math.round(ms) + 'ms' : (ms / 1000).toFixed(ms < 60000 ? 1 : 0) + 's';
  }

  /** @param {any} activity */
  function toolLabel(activity) {
    if (activity.phase === 'step') {
      return activity.tool + (activity.detail ? ' \u2014 ' + activity.detail : '');
    }
    const known = activity.server === 'builtin' ? TOOL_LABELS[String(activity.tool)] : undefined;
    return known || activity.server + ' \u00b7 ' + activity.tool;
  }

  // currentWorkBlock is the block the next row goes in: the open one while it
  // is still the last thing in the transcript, else a new one -- so rows stay
  // in order with the reply text and approvals around them.
  function currentWorkBlock() {
    if (workBlockEl && workBlockEl.parentElement === transcriptEl && transcriptEl.lastElementChild === workBlockEl) {
      return workBlockEl;
    }
    const block = document.createElement('details');
    block.className = 'work';
    block.open = toolActivityMode === 'expanded';
    const summary = document.createElement('summary');
    summary.className = 'work-summary';
    const label = document.createElement('span');
    label.className = 'work-label';
    label.setAttribute('role', 'status');
    summary.appendChild(label);
    const rows = document.createElement('ol');
    rows.className = 'work-rows';
    block.append(summary, rows);
    transcriptEl.appendChild(block);
    workBlockEl = block;
    return block;
  }

  /** @param {Element} block */
  function refreshWorkSummary(block) {
    const label = block.querySelector('.work-label');
    if (!label) {
      return;
    }
    let tools = 0;
    let steps = 0;
    let running = 0;
    let failed = 0;
    let denied = 0;
    block.querySelectorAll('.work-row').forEach((row) => {
      const state = row.getAttribute('data-state');
      if (state === 'step') {
        steps++;
        return;
      }
      tools++;
      if (state === 'run') running++;
      if (state === 'fail') failed++;
      if (state === 'denied') denied++;
    });
    const plural = (n, word) => n + ' ' + word + (n === 1 ? '' : 's');
    let text;
    if (running > 0) {
      text = 'Working\u2026 ' + plural(tools + steps, 'step');
    } else if (tools === 0) {
      text = 'Ran ' + plural(steps, 'step');
    } else {
      text = 'Used ' + plural(tools, 'tool');
    }
    if (failed > 0) text += ' \u00b7 ' + failed + ' failed';
    if (denied > 0) text += ' \u00b7 ' + denied + ' not run';
    label.textContent = text;
    block.classList.toggle('has-failure', failed > 0);
    block.classList.toggle('running', running > 0);
  }

  /** @param {any} activity */
  function showToolActivity(activity) {
    /** @type {Record<string, string>} */
    const states = { running: 'run', succeeded: 'ok', failed: 'fail', denied: 'denied', step: 'step' };
    const state = states[String(activity.phase)];
    if (!state) {
      return;
    }
    const key = activity.phase === 'step' ? '' : String(activity.call_id || '');
    let row = key ? toolActivityEls.get(key) : undefined;
    if (!row || !row.parentElement) {
      const block = currentWorkBlock();
      row = document.createElement('li');
      row.className = 'work-row';
      const glyph = document.createElement('span');
      glyph.className = 'work-glyph';
      glyph.setAttribute('aria-hidden', 'true');
      const name = document.createElement('span');
      name.className = 'work-name';
      const why = document.createElement('span');
      why.className = 'work-why';
      const badge = document.createElement('span');
      badge.className = 'work-badge';
      const time = document.createElement('span');
      time.className = 'work-time';
      row.append(glyph, name, why, badge, time);
      const list = block.querySelector('.work-rows');
      if (list) {
        list.appendChild(row);
      }
      if (key) {
        toolActivityEls.set(key, row);
      }
    }
    const label = toolLabel(activity);
    const reason = state === 'fail' || state === 'denied' ? String(activity.detail || '') : '';
    row.setAttribute('data-state', state);
    const glyphEl = row.querySelector('.work-glyph');
    const nameEl = row.querySelector('.work-name');
    const whyEl = row.querySelector('.work-why');
    if (glyphEl) glyphEl.textContent = WORK_GLYPHS[state];
    if (nameEl) nameEl.textContent = label;
    if (whyEl) whyEl.textContent = reason;
    const badgeEl = row.querySelector('.work-badge');
    const timeEl = row.querySelector('.work-time');
    if (badgeEl) badgeEl.textContent = WORK_BADGES[state];
    if (timeEl) timeEl.textContent = (state === 'ok' || state === 'fail') && activity.duration_ms ? workDuration(activity.duration_ms) : '';
    const word = WORK_STATE_WORDS[state];
    row.setAttribute('aria-label', label + (word ? ': ' + word : '') + (reason ? ', ' + reason : ''));
    row.title = state === 'ok'
      ? (activity.result_bytes || 0) + ' bytes to the model in ' + (activity.duration_ms || 0) + 'ms'
      : (activity.server && activity.tool ? activity.server + '__' + activity.tool : label);
    const block = row.closest('.work');
    if (block) {
      refreshWorkSummary(block);
    }
    transcriptEl.scrollTop = transcriptEl.scrollHeight;
  }

  // showEditSummary renders the end-of-run outcome once every block in a
  // sequential review has been applied, skipped, or refused -- mirroring
  // finishReview's system-turn text in clients/tui/chat.go ("N applied, N
  // skipped, N refused" + refusal reasons + a backup restore hint).
  function showEditSummary(summary) {
    const container = document.createElement('div');
    container.className = 'edit-summary';

    const line = document.createElement('div');
    line.className = 'summary-line';
    line.textContent = `${summary.total} processed: ${summary.applied} applied, ${summary.skipped} skipped, ${summary.refused} refused`;
    container.appendChild(line);

    for (const reason of summary.refusalReasons || []) {
      const r = document.createElement('div');
      r.className = 'summary-refusal';
      r.textContent = `refused: ${reason}`;
      container.appendChild(r);
    }

    if (summary.applied > 0 && summary.backupDir) {
      const b = document.createElement('div');
      b.className = 'summary-backup';
      b.textContent = `backups: ${summary.backupDir} (restore with: mochiii-daemon edits undo)`;
      container.appendChild(b);

      const undoBtn = document.createElement('button');
      undoBtn.className = 'undo-btn';
      undoBtn.textContent = 'Undo this apply';
      undoBtn.addEventListener('click', () => {
        if (pendingUndoButton) {
          return; // another undo is already in flight; ignore extra clicks
        }
        pendingUndoButton = undoBtn;
        undoBtn.disabled = true;
        undoBtn.textContent = 'Undoing…';
        vscode.postMessage({ type: 'undoEdit', backupDir: summary.backupDir });
      });
      container.appendChild(undoBtn);
    }

    transcriptEl.appendChild(container);
    transcriptEl.scrollTop = transcriptEl.scrollHeight;
  }

  // showUndoResult replaces the clicked Undo button with an honest outcome
  // line -- "N file(s) restored" -- and, CRITICALLY, if any files were left
  // guarded (changed since the apply run), an explicit note naming them.
  // Never implies a full revert happened when some files were guarded, and
  // never re-enables the button (a second undo of the same session would
  // just report 0 restored -- see Phase 0 -- so there's nothing useful a
  // second click could do).
  function showUndoResult(button, result) {
    const container = button.parentElement;
    button.remove();

    if (result.error) {
      const el = document.createElement('div');
      el.className = 'summary-refusal';
      // The daemon hard-refuses undo for a backup dir that's been pruned
      // (bounded backups keep only the last 5 runs) with an error containing
      // the stable substring "not found under" -- see
      // isWorkspaceBackupSessionDir/handleUndo in daemon/server.go. That
      // substring is the only signal we have client-side that this is a
      // pruned-away run rather than some other undo failure, so we match on
      // it and show an honest, specific message instead of raw daemon
      // wording. A typed UndoResponse.NotFound field would be a more robust
      // way to distinguish this case, but a string match is enough for this
      // slice -- revisit if it ever proves fragile.
      if (result.error.includes('not found under')) {
        el.textContent = 'no longer undoable (backup pruned)';
      } else {
        el.textContent = `undo failed: ${result.error}`;
      }
      container.appendChild(el);
      return;
    }

    const el = document.createElement('div');
    el.className = 'summary-undo-result';
    el.textContent = `${result.restored} file(s) restored`;
    container.appendChild(el);

    if (result.guarded && result.guarded.length > 0) {
      const g = document.createElement('div');
      g.className = 'summary-refusal';
      g.textContent = `${result.guarded.length} file(s) changed since this apply and were left as-is: ${result.guarded.join(', ')}`;
      container.appendChild(g);
    }
  }

  // PREFIX FIRST, THEN SUBSTRING. `name.startsWith(q)` alone meant that with 27
  // commands you had to already know how the one you wanted began: typing
  // "/usage" found it, typing "/token" found nothing, and "/server" did not
  // find /mcp-server. The ordering is what keeps it predictable -- an exact
  // prefix never loses its place to a substring hit further down a name -- and
  // matching the SUMMARY too is what makes the menu answer "what was the
  // command for cost?" rather than only completing a name.
  function filteredSlashCommands() {
    const q = slashFilter.toLowerCase();
    if (q === '') {
      return SLASH_COMMANDS.slice();
    }
    const prefix = [];
    const inName = [];
    const inSummary = [];
    for (const c of SLASH_COMMANDS) {
      const name = c.name.toLowerCase();
      if (name.startsWith(q)) {
        prefix.push(c);
      } else if (name.includes(q)) {
        inName.push(c);
      } else if (String(c.summary || '').toLowerCase().includes(q)) {
        inSummary.push(c);
      }
    }
    return prefix.concat(inName, inSummary);
  }

  function hideSlashMenu() {
    slashMenuEl.classList.remove('visible');
    clearChildren(slashMenuEl);
  }

  function renderSlashMenu() {
    const items = filteredSlashCommands();
    if (!inputEl.value.startsWith('/') || inputEl.value.includes(' ') || items.length === 0) {
      hideSlashMenu();
      return;
    }
    if (slashIndex >= items.length) {
      slashIndex = 0;
    }
    clearChildren(slashMenuEl);
    items.forEach((c, i) => {
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.setAttribute('role', 'option');
      if (i === slashIndex) {
        btn.className = 'active';
      }
      const name = document.createElement('span');
      name.className = 'slash-name';
      name.textContent = '/' + c.name;
      const summary = document.createElement('span');
      summary.className = 'slash-summary';
      summary.textContent = c.summary;
      btn.appendChild(name);
      btn.appendChild(summary);
      btn.addEventListener('mousedown', (e) => {
        e.preventDefault();
        applySlash(c.name);
      });
      slashMenuEl.appendChild(btn);
    });
    slashMenuEl.classList.add('visible');
  }

  // A TRAILING SPACE ONLY WHERE AN ARGUMENT GOES. Every completion used to add
  // one, so picking /clear left "/clear " in the box -- which parseSlash reads
  // as a command with empty arguments and which, for the commands that take
  // them, printed a usage line instead of running. Commands that take no
  // argument now complete to exactly what the user would have typed, ready to
  // send.
  function applySlash(name) {
    const def = SLASH_COMMANDS.find((c) => c.name === name);
    inputEl.value = '/' + name + (def && def.needsArgs ? ' ' : '');
    hideSlashMenu();
    inputEl.focus();
  }

  function updateSlashFromInput() {
    const v = inputEl.value;
    if (!v.startsWith('/') || v.includes(' ')) {
      hideSlashMenu();
      return;
    }
    slashFilter = v.slice(1);
    renderSlashMenu();
  }

  function send() {
    const typed = inputEl.value.trim();
    if ((!typed && pendingAttachments.length === 0) || streaming) {
      return;
    }
    if (pendingAttachments.length === 0 && openScreenFor(typed)) {
      inputEl.value = '';
      hideSlashMenu();
      return;
    }
    setNoticesOpen(false);
    if (pendingAttachments.some((f) => f.pending)) {
      addBubble('error', 'Still reading ' + pendingAttachments.filter((f) => f.pending).map((f) => f.name).join(', ') + ' \u2014 send again in a moment.');
      return;
    }
    hideSlashMenu();
    clearEditProposal();
    clearToolApproval();
    toolActivityEls.clear();
    workBlockEl = null;

    let text = typed;
    if (pendingAttachments.length > 0) {
      const blocks = pendingAttachments.map((f) => {
        // desc says how the text was obtained ("text extracted from PDF, 3 pages",
        // "OCR text from an image"), so the model does not take it for the file's
        // own bytes or trust an OCR misreading as exact.
        const label = f.desc ? ` (${f.desc})` : '';
        return `--- attached: ${f.name}${label} ---\n${f.text}\n--- end ${f.name} ---`;
      });
      text = (typed ? typed + '\n\n' : '') + blocks.join('\n\n');
    }

    addBubble('user', typed || `(${pendingAttachments.length} attached file(s))`);
    if (pendingAttachments.length > 0) {
      // Show which files rode along without dumping full contents into the bubble.
      const note = document.createElement('div');
      note.className = 'attach-chip';
      note.style.marginTop = '8px';
      const label = document.createElement('span');
      label.className = 'attach-name';
      label.textContent = 'attached: ' + pendingAttachments.map((f) => f.name).join(', ');
      note.appendChild(label);
      const card = transcriptEl.lastElementChild && transcriptEl.lastElementChild.querySelector('.card');
      if (card) {
        card.appendChild(note);
      }
    }
    pendingAttachments = [];
    renderAttachments();
    refreshContextUsage();

    inputEl.value = '';
    beginTurnView();
    lastPrompt = { text, autoApply: autoApplyEnabled, mode: currentMode };
    vscode.postMessage({ type: 'prompt', text, autoApply: autoApplyEnabled, mode: currentMode });
    // Keep focus on the input across a send. Clicking Send moved focus to the
    // button, so a keyboard or screen-reader user had to navigate back to ask a
    // follow-up; the natural next action is always another prompt.
    inputEl.focus();
  }

  // SCREEN COMMANDS open a page or a menu instead of answering in the chat:
  // /mcp and /mcp-server the MCP servers (words after them search the
  // marketplace), /account and /config their pages, /model with nothing after
  // it the model menu. Printing a list into the transcript for these is what
  // the first Stage 7 walkthrough objected to -- the answer to "show me my
  // servers" is the servers, not a table about them.
  /** @param {string} typed */
  function openScreenFor(typed) {
    const m = /^\/(mcp|mcp-server|account|config|model)(?:\s+([\s\S]*))?$/.exec(typed);
    if (!m) {
      return false;
    }
    const arg = (m[2] || '').trim();
    if (m[1] === 'model') {
      if (arg !== '' || !modelChipEl) {
        return false; // "/model <name>" selects, and says so
      }
      modelChipEl.click();
      return true;
    }
    if (!pages) {
      return false;
    }
    if (m[1] === 'mcp' || m[1] === 'mcp-server') {
      pages.open('customize', { tab: 'mcp', sub: arg ? 'market' : 'installed', search: arg || undefined });
    } else {
      pages.open(m[1] === 'account' ? 'account' : 'settings');
    }
    return true;
  }

  // beginTurnView resets the per-turn strips and opens the reply bubble: what
  // a send does once the user's message is on screen, and all a retry does.
  function beginTurnView() {
    setGrounding(null);
    setHistoryInfo(null);
    setRedactions(null);
    setDegraded(null);
    setProvider(null);
    setStreaming(true);
    // A fresh turn starts a new thinking block; the previous turn's stays in
    // scrollback but must not accumulate this turn's reasoning.
    currentReasoningBody = null;
    currentAssistantBubble = addBubble('assistant', '');
    // Captured once, for this run only -- see currentRunAuto's doc comment.
    currentRunAuto = autoApplyEnabled;
  }

  // TRY AGAIN, after a failure the provider says will pass (a rate limit, an
  // outage). The same prompt, sent as a retry: the user's message is already
  // on screen, and the host replaces the unanswered copy rather than keeping
  // two (ChatPanel.onPrompt).
  /** @type {{ text: string, autoApply: boolean, mode: string } | null} */
  let lastPrompt = null;
  function retryLastPrompt() {
    if (streaming || !lastPrompt) {
      return;
    }
    clearEditProposal();
    clearToolApproval();
    toolActivityEls.clear();
    workBlockEl = null;
    beginTurnView();
    vscode.postMessage({ type: 'prompt', text: lastPrompt.text, autoApply: lastPrompt.autoApply, mode: lastPrompt.mode, retry: true });
    inputEl.focus();
  }

  // sendText puts words in the composer and sends them, as if typed: what the
  // Continue and Compact buttons do, so they go through the same checks.
  /** @param {string} text */
  function sendText(text) {
    if (streaming) {
      return;
    }
    inputEl.value = text;
    send();
  }

  // errorActions puts the one useful next step under a failed turn, chosen by
  // the daemon's error_class (protocol.TokenResponse.ErrorClass) -- never by
  // reading the message, which is prose.
  /**
   * @param {HTMLElement} body
   * @param {string} errorClass
   * @param {boolean} keyReplaceable
   */
  function addErrorActions(body, errorClass, keyReplaceable) {
    /** @type {[string, () => void][]} */
    const actions = [];
    if (errorClass === 'rate_limited' || errorClass === 'upstream_unavailable') {
      actions.push(['Try again', retryLastPrompt]);
    } else if ((errorClass === 'auth' || errorClass === 'quota_exceeded') && keyReplaceable) {
      actions.push(['Change key', () => vscode.postMessage({ type: 'connect' })]);
    } else if (errorClass === 'context_too_large') {
      actions.push(['Compact the chat', () => sendText('/compact')]);
    }
    const card = body.parentElement;
    if (actions.length === 0 || !card || !lastPrompt) {
      return;
    }
    const row = document.createElement('div');
    row.className = 'turn-actions';
    for (const [label, run] of actions) {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'turn-action';
      b.textContent = label;
      b.addEventListener('click', () => {
        row.remove();
        run();
      });
      row.appendChild(b);
    }
    card.appendChild(row);
  }

  // ONE BUTTON, TWO JOBS -- send, or stop what is running.
  //
  // There was previously no way to interrupt a turn at all: the composer simply
  // went dead until the answer finished, and an agent turn runs for tens of
  // seconds (a long task, for minutes). So "I asked the wrong question" meant
  // waiting it out, paying for it, or discarding the whole conversation with
  // "+". The two states are deliberately the same control rather than a second
  // button appearing next to it: the thing you want is always in the same place.
  sendBtn.addEventListener('click', () => {
    if (streaming) {
      vscode.postMessage({ type: 'stopTurn' });
      return;
    }
    send();
  });
  inputEl.addEventListener('input', updateSlashFromInput);
  inputEl.addEventListener('keydown', (e) => {
    const menuOpen = slashMenuEl.classList.contains('visible');
    if (menuOpen && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
      e.preventDefault();
      const items = filteredSlashCommands();
      if (items.length === 0) {
        return;
      }
      slashIndex =
        e.key === 'ArrowDown'
          ? (slashIndex + 1) % items.length
          : (slashIndex - 1 + items.length) % items.length;
      renderSlashMenu();
      return;
    }
    if (menuOpen && e.key === 'Tab') {
      e.preventDefault();
      const items = filteredSlashCommands();
      if (items[slashIndex]) {
        applySlash(items[slashIndex].name);
      }
      return;
    }
    if (menuOpen && e.key === 'Escape') {
      hideSlashMenu();
      return;
    }
    if (e.key === 'Enter' && !e.shiftKey) {
      if (menuOpen) {
        const items = filteredSlashCommands();
        if (items[slashIndex] && !inputEl.value.includes(' ')) {
          e.preventDefault();
          applySlash(items[slashIndex].name);
          return;
        }
      }
      e.preventDefault();
      // ENTER DOES NOT QUEUE. Now that the input stays editable during a turn
      // (see setStreaming), Enter can be pressed while one is running. Silently
      // holding the question until the answer finished would be a surprise --
      // the user would have sent something and seen nothing happen -- and
      // sending it concurrently is not supported: the wire is one prompt per
      // connection and the host refuses a second (onPrompt's inFlight guard).
      // So it says what to do instead, and the draft is left in the box.
      if (streaming) {
        addInfoLine('Mochiii is still answering. Press Stop to interrupt it, then send.');
        return;
      }
      send();
    }
  });

  // Initial focus: the prompt input is the panel's primary control, so opening
  // the panel puts the caret where the user is going to type. Previously nothing
  // was focused and a keyboard user had to tab in from the top of the document.
  inputEl.focus();

  function clearTranscriptView() {
    clearChildren(transcriptEl);
    if (firstRunEl) {
      transcriptEl.appendChild(firstRunEl);
    }
    currentAssistantBubble = null;
    currentAssistantCard = null;
    currentAssistantRaw = '';
    currentReasoningBody = null;
    clearEditProposal();
    clearToolApproval();
    toolActivityEls.clear();
    workBlockEl = null;
    lastGroundingInfo = null;
    lastHistoryMeta = null;
    refreshContextUsage();
  }

  // Clicking into this panel -- from VS Code's own terminal, where the other
  // Mochiii client may be running -- is when the shared chat may have moved
  // on. The extension asks the daemon and appends whatever it gained.
  window.addEventListener('focus', () => vscode.postMessage({ type: 'focused' }));

  // THE PAGES (media/pages.js, Stage 8): Customize, Account and Settings,
  // opened from the header. They are given what they may do here as hooks,
  // and see only their own messages (handle returns false for the rest).
  const pages = window.MochiiiPages
    ? window.MochiiiPages.init(vscode, {
        openModelMenu: () => {
          if (modelChipEl) modelChipEl.click();
        },
        sendText: (/** @type {string} */ t) => sendText(t),
        prefill: (/** @type {string} */ t) => {
          inputEl.value = t;
          inputEl.focus();
          updateSlashFromInput();
        },
        focusComposer: () => inputEl.focus(),
        setEffort: (/** @type {string} */ e) => setEffortLevel(e),
        getEffort: () => EFFORTS[effortIndex],
        closePopups: () => {
          setHistoryOpen(false);
          setModelPopupOpen(false);
          setModesPopupOpen(false);
          setContextPopupOpen(false);
          if (controls) controls.closeMenus();
        },
      })
    : null;

  const controls = window.MochiiiControls
    ? window.MochiiiControls.init(vscode, {
        toggleSettings: () => toggleSettings(),
        closeSettings: () => setModelPopupOpen(false),
        getEffort: () => EFFORTS[effortIndex],
        setEffort: (/** @type {string} */ e) => setEffortLevel(e),
        getMode: () => currentMode,
        setMode: (/** @type {string} */ m) => {
          currentMode = m || 'auto';
          renderMode();
        },
        openPage: (/** @type {string} */ name) => {
          if (pages) pages.open(name);
        },
        closePopups: () => {
          setHistoryOpen(false);
          setModelPopupOpen(false);
          setModesPopupOpen(false);
        },
        attachRemaining: () => MAX_ATTACH_FILES - pendingAttachments.length,
        focusComposer: () => inputEl.focus(),
        // The command, then its default argument selected: typing replaces it.
        prefill: (/** @type {string} */ cmd, /** @type {string} */ arg) => {
          inputEl.value = cmd + arg;
          inputEl.focus();
          inputEl.setSelectionRange(cmd.length, cmd.length + arg.length);
        },
      })
    : null;

  const connectPopup = window.MochiiiConnect
    ? window.MochiiiConnect.init(vscode, {
        closePopups: () => {
          setHistoryOpen(false);
          setModelPopupOpen(false);
          setModesPopupOpen(false);
          setContextPopupOpen(false);
        },
        closePage: () => {
          if (pages && pages.isOpen()) pages.close();
        },
        focusComposer: () => inputEl.focus(),
      })
    : null;

  window.addEventListener('message', (event) => {
    const msg = event.data;
    if (connectPopup && connectPopup.handle(msg)) {
      return;
    }
    if (pages && pages.handle(msg)) {
      return;
    }
    switch (msg.type) {
      case 'history':
        for (const turn of msg.turns) {
          const body = addBubble(turn.role, '', true);
          if (turn.role === 'assistant') {
            currentAssistantRaw = turn.content || '';
            renderFencedContent(body, currentAssistantRaw);
            if (currentAssistantCard) {
              attachFeedbackFooter(currentAssistantCard, currentAssistantRaw);
            }
            currentAssistantBubble = null;
            currentAssistantCard = null;
            currentAssistantRaw = '';
          } else {
            body.textContent = turn.content || '';
          }
        }
        break;
      case 'clearTranscript':
        clearTranscriptView();
        break;
      case 'modelTier': {
        // Show the MODEL, not the tier name: "default" told nobody what was
        // answering. The provider prefix is dropped for space ("qwen/qwen3.8-27b"
        // -> "qwen3.8-27b") and kept in the tooltip.
        const model = typeof msg.model === 'string' ? msg.model : '';
        const shortModel = model ? model.slice(model.lastIndexOf('/') + 1) : '';
        // A NEW MODEL GETS THE BENEFIT OF THE DOUBT. The effort latch the
        // daemon reports belongs to one model; carrying it across a switch
        // would grey the control for a model that may well honour it, and the
        // user would have no way to find out otherwise. If the new model also
        // refuses, the next turn says so again.
        if (model && model !== effortUnsupportedModel) {
          setEffortSupported(true, '');
        }
        const tierName = msg.tier && msg.tier !== 'default' ? String(msg.tier) : '';
        const label = shortModel || tierName || 'Model';
        if (modelChipLabelEl) {
          modelChipLabelEl.textContent = label;
        }
        if (modelChipEl) {
          modelChipEl.title = 'Model: ' + (model || tierName || 'default') + (tierName ? ' (tier ' + tierName + ')' : '') + '. Click to choose (/model).';
          modelChipEl.setAttribute('aria-label', 'Model: ' + label + '. Choose model.');
        }
        break;
      }
      case 'grounding':
        setGrounding(msg.info);
        break;
      // THE MEASURED CONTEXT SIZE for the turn that just finished. Replaces the
      // estimate for good: once a real figure exists the meter never goes back
      // to guessing (see computeContextUsage).
      case 'usage':
        if (msg.usage && typeof msg.usage === 'object') {
          lastUsage = msg.usage;
          refreshContextUsage();
        }
        break;
      // How many characters the open-file block will add to the next prompt.
      // Arrives on every editor and selection change, so the meter moves when
      // you switch files -- which is the point: it is the one part of the
      // context the user controls directly.
      case 'editorContext':
        editorContextChars = typeof msg.chars === 'number' && msg.chars > 0 ? msg.chars : 0;
        if (controls) controls.currentFile(typeof msg.file === 'string' ? msg.file : '');
        refreshContextUsage();
        break;
      case 'historyInfo':
        setHistoryInfo(msg.info);
        break;
      case 'redactions':
        setRedactions(msg.kinds);
        break;
      case 'reasoning':
        appendReasoning(msg.text);
        break;
      case 'degraded':
        setDegraded(msg.items);
        break;
      case 'provider':
        setProvider(msg.provider);
        break;
      // The setup state the panel can act on: 'noKey' puts the ask and its
      // button in the transcript, 'ready' clears it and names provider and
      // model on the chip, 'error' shows one sentence with the technical text
      // folded away behind it. See applyConnection.
      case 'connection':
        applyConnection(msg);
        break;
      case 'token':
        if (!currentAssistantBubble) {
          currentAssistantBubble = addBubble('assistant', '');
        }
        currentAssistantRaw += msg.text;
        currentAssistantPlain = msg.plain === true;
        currentAssistantBubble.textContent = currentAssistantRaw;
        transcriptEl.scrollTop = transcriptEl.scrollHeight;
        break;
      case 'done':
        currentTasksEl = null;
        finalizeAssistant();
        // Nothing should still be pending -- the daemon does not send done
        // while it is waiting for an answer -- but a panel left on screen with
        // nothing behind it would invite a click that goes nowhere.
        clearToolApproval();
        setStreaming(false);
        refreshContextUsage();
        inputEl.focus();
        break;
      case 'error':
        currentTasksEl = null;
        if (currentAssistantBubble && currentAssistantRaw === '') {
          const card = currentAssistantBubble.parentElement;
          const msgEl = card && card.parentElement;
          if (msgEl) {
            msgEl.remove();
          }
        }
        currentAssistantBubble = null;
        currentAssistantCard = null;
        currentAssistantRaw = '';
        clearToolApproval();
        setStreaming(false);
        addErrorActions(addBubble('error', msg.message), String(msg.errorClass || ''), msg.keyReplaceable === true);
        break;
      case 'incomplete':
        showIncomplete(msg.info);
        break;
      case 'toolActivity':
        showToolActivity(msg.activity);
        break;
      case 'mcpCount':
        setToolsCount(msg);
        break;
      case 'uiSettings':
        toolActivityMode = msg.toolActivity === 'expanded' ? 'expanded' : 'compact';
        // The mode a new chat starts in (mochiii.defaultMode), applied once,
        // when the panel opens: changing the setting later does not switch a
        // chat that is already going.
        if (!defaultModeApplied && (msg.defaultMode === 'auto' || msg.defaultMode === 'manual' || msg.defaultMode === 'plan')) {
          defaultModeApplied = true;
          currentMode = msg.defaultMode;
          renderMode();
        }
        break;
      case 'toolApproval':
        showToolApproval(msg.request);
        break;
      case 'editProposal':
        showEditProposal(msg.edit, msg.index, msg.total);
        break;
      case 'applyResult':
        if (currentEditProposalEl) {
          showEditResult(currentEditProposalEl, {
            applied: msg.applied,
            error: msg.error,
            backupDir: msg.backupDir,
            syntaxNote: msg.syntaxNote,
            matchNote: msg.matchNote,
          });
        }
        break;
      case 'editRejections':
        showEditRejections(msg.rejections);
        break;
      case 'specNotice':
        showSpecNotice(msg.kind, msg.text);
        break;
      case 'tasks':
        showTasks(msg.text, msg.items);
        break;
      case 'activeSpec':
        setActiveSpec(msg.spec);
        break;
      case 'editSummary':
        showEditSummary(msg);
        break;
      case 'undoResult':
        if (pendingUndoButton) {
          showUndoResult(pendingUndoButton, {
            restored: msg.restored,
            guarded: msg.guarded,
            error: msg.error,
          });
          pendingUndoButton = null;
        }
        break;
      case 'attachmentStarted':
        if (pendingAttachments.length < MAX_ATTACH_FILES) {
          pendingAttachments.push({ name: msg.name, text: '', pending: true, id: msg.id, note: '', desc: '' });
          renderAttachments();
        }
        break;
      case 'attachmentProgress': {
        const att = pendingAttachments.find((f) => f.id === msg.id);
        if (att && att.pending) {
          att.note = msg.note;
          renderAttachments();
        }
        break;
      }
      case 'attachmentExtracted': {
        const att = pendingAttachments.find((f) => f.id === msg.id);
        if (att) {
          att.text = msg.text;
          att.pending = false;
          att.note = msg.note || '';
          att.desc = msg.desc || '';
          renderAttachments();
          refreshContextUsage();
        }
        break;
      }
      case 'attachmentNotice':
      case 'notice':
        addBubble('error', msg.text);
        break;
      case 'modelMenu':
        renderModelMenu(msg);
        if (controls) controls.renderProviders(msg);
        break;
      case 'info':
        addInfoLine(String(msg.text || ''));
        break;
      // The turn was stopped by the user (the Stop button). Said in the
      // transcript, because a reply that simply stops mid-sentence with nothing
      // to mark it reads like a bug or a truncated model answer -- and the
      // partial text IS kept (see onStopTurn), so it needs to be accounted for.
      case 'stopped':
        if (currentAssistantBubble) {
          currentAssistantBubble = null;
          currentAssistantCard = null;
          currentAssistantRaw = '';
        }
        addInfoLine('You stopped this answer.');
        break;
      case 'attachmentFailed':
        pendingAttachments = pendingAttachments.filter((f) => f.id !== msg.id);
        renderAttachments();
        refreshContextUsage();
        addBubble('error', msg.error);
        break;
      case 'historyEntries':
        renderHistory(msg);
        break;
      case 'historyChanged':
        if (historyPopup && !historyPopup.hidden) {
          vscode.postMessage({ type: 'historyList', text: historyQuery });
        }
        break;
      case 'currentBookmark':
        setCurrentBookmark(msg.on === true);
        break;
      // The slash catalog, from src/slashCommands.ts. See SLASH_COMMANDS above
      // for why this is posted rather than transcribed.
      case 'slashCatalog':
        if (Array.isArray(msg.commands)) {
          SLASH_COMMANDS = msg.commands;
        }
        break;
      // /effort and /mode changed the setting; the pill has to agree with the
      // command. Two routes to one setting that disagreed with each other would
      // be worse than having only one route.
      case 'setEffortFromHost': {
        const i = EFFORTS.indexOf(String(msg.effort || ''));
        if (i >= 0) {
          effortIndex = i;
          renderEffort();
          saveState({ effort: EFFORTS[effortIndex] });
        }
        break;
      }
      case 'setModeFromHost':
        if (msg.mode === 'auto' || msg.mode === 'manual' || msg.mode === 'plan') {
          currentMode = msg.mode;
          renderMode();
        }
        break;
    }
  });

  // THE WEBVIEW ASKS, rather than the host telling it on construction.
  //
  // panel.webview.html is assigned in the ChatPanel constructor and this script
  // loads asynchronously afterwards, so a postMessage made in that constructor
  // can arrive before this listener exists and be dropped. Everything the panel
  // needs in order to render correctly before the first turn -- the slash
  // catalog, the size of the editor-context block -- is therefore requested
  // here, once this listener is installed and cannot miss the reply.
  vscode.postMessage({ type: 'ready' });
}());
