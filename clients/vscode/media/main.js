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
  const inputEl = document.getElementById('promptInput');
  const sendBtn = document.getElementById('sendBtn');
  const searchInputEl = document.getElementById('searchInput');
  const searchBtn = document.getElementById('searchBtn');
  const searchResultsEl = document.getElementById('searchResults');
  const searchRowEl = document.getElementById('searchRow');
  const slashMenuEl = document.getElementById('slashMenu');
  const modelChipEl = document.getElementById('modelChip');
  const modelChipLabelEl = document.getElementById('modelChipLabel');
  const newChatBtn = document.getElementById('newChatBtn');
  const historyBtn = document.getElementById('historyBtn');
  const settingsBtn = document.getElementById('settingsBtn');
  const closeBtn = document.getElementById('closeBtn');
  const attachBtn = document.getElementById('attachBtn');
  const fileInputEl = document.getElementById('fileInput');
  const attachListEl = document.getElementById('attachList');
  const composerShellEl = document.getElementById('composerShell');
  const effortMeterEl = document.getElementById('effortMeter');
  const modeBtnLabelEl = document.getElementById('modeBtnLabel');
  const contextBtn = document.getElementById('contextBtn');
  const contextPopup = document.getElementById('contextPopup');
  const contextPopupClose = document.getElementById('contextPopupClose');
  const contextPctEl = document.getElementById('contextPct');
  const contextTotalsEl = document.getElementById('contextTotals');
  const contextBarEl = document.getElementById('contextBar');
  const contextRowsEl = document.getElementById('contextRows');
  const contextRingFill = document.getElementById('contextRingFill');
  const modesPopup = document.getElementById('modesPopup');
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
  let pendingAttachments = [];
  const MAX_ATTACH_CHARS = 80000;
  const MAX_ATTACH_FILES = 8;
  const CONTEXT_LIMIT_TOKENS = 128000;
  const RING_CIRCUMFERENCE = 2 * Math.PI * 8; // r=8 in the SVG

  let lastGroundingInfo = null;
  let lastHistoryMeta = null;

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

  function computeContextUsage() {
    const systemPrompt = 1200; // embedded daemon system prompt (approx)
    const tools = 2400; // MCP / agent tool schemas when enabled (approx)
    const rules = 400;
    const attachChars = pendingAttachments.reduce((sum, f) => sum + f.name.length + f.text.length, 0);
    const attachments = estimateTokens(attachChars);
    const chunks = (lastGroundingInfo && lastGroundingInfo.chunks) || 0;
    const retrieval = estimateTokens(chunks * 800); // ~800 chars/chunk estimate
    const conversation = estimateTokens(collectConversationChars());
    const rows = [
      { key: 'system', label: 'System prompt', tokens: systemPrompt, color: '#c4b0b4' },
      { key: 'tools', label: 'Tool definitions', tokens: tools, color: '#c4a0e0' },
      { key: 'rules', label: 'Rules', tokens: rules, color: '#7dbe8a' },
      { key: 'retrieval', label: 'Retrieved context', tokens: retrieval, color: '#6eb0e0' },
      { key: 'attachments', label: 'Attachments', tokens: attachments, color: '#f0a060' },
      { key: 'conversation', label: 'Conversation', tokens: conversation, color: '#d4727d' },
    ];
    const used = rows.reduce((s, r) => s + r.tokens, 0);
    const pct = Math.min(100, Math.round((used / CONTEXT_LIMIT_TOKENS) * 100));
    return { rows, used, pct, limit: CONTEXT_LIMIT_TOKENS };
  }

  function refreshContextUsage() {
    const usage = computeContextUsage();
    if (contextRingFill) {
      const offset = RING_CIRCUMFERENCE * (1 - usage.pct / 100);
      contextRingFill.setAttribute('stroke-dasharray', String(RING_CIRCUMFERENCE));
      contextRingFill.setAttribute('stroke-dashoffset', String(offset));
    }
    if (contextBtn) {
      contextBtn.classList.toggle('hot', usage.pct >= 80);
      contextBtn.title = `Context usage: ${usage.pct}% (~${formatTokenCount(usage.used)} / ${formatTokenCount(usage.limit)})`;
    }
    if (!contextPopup || contextPopup.hidden) {
      return;
    }
    if (contextPctEl) {
      contextPctEl.textContent = usage.pct + '% Full';
    }
    if (contextTotalsEl) {
      contextTotalsEl.textContent =
        '~' + formatTokenCount(usage.used) + ' / ' + formatTokenCount(usage.limit) + ' Tokens';
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
        empty.className = 'seg';
        empty.style.background = '#f3e8ea';
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
    if (contextPopup && !contextPopup.hidden) {
      if (!contextPopup.contains(e.target) && (!contextBtn || !contextBtn.contains(e.target))) {
        setContextPopupOpen(false);
      }
    }
    if (modesPopup && !modesPopup.hidden) {
      if (!modesPopup.contains(e.target) && (!autoApplyToggle || !autoApplyToggle.contains(e.target))) {
        setModesPopupOpen(false);
      }
    }
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      setContextPopupOpen(false);
      if (typeof setModesPopupOpen === 'function') setModesPopupOpen(false);
    }
  });

  refreshContextUsage();

  // Keep in sync with clients/vscode/src/slashCommands.ts (and TUI slash.go).
  const SLASH_COMMANDS = [
    { name: 'help', summary: 'list slash commands' },
    { name: 'model', summary: 'list or select a models.json tier' },
    { name: 'mcp-server', summary: 'show configured MCP servers and tool policy' },
    { name: 'clear', summary: 'clear the on-screen transcript' },
    { name: 'compact', summary: 'drop older turns; keep the last few' },
    { name: 'context', summary: 'show workspace, model tier, and last grounding' },
    { name: 'git', summary: 'show git status for the workspace' },
    { name: 'init', summary: 'quick start checklist for this workspace' },
    { name: 'search', summary: 'search past conversation turns' },
    { name: 'exit', summary: 'close the chat panel' },
    { name: 'explain', summary: 'explain code or a concept' },
    { name: 'fix', summary: 'find and fix a bug' },
    { name: 'test', summary: 'add or improve tests' },
    { name: 'refactor', summary: 'refactor code' },
    { name: 'doc', summary: 'write or improve documentation' },
    { name: 'security', summary: 'security review' },
    { name: 'review', summary: 'code review' },
    { name: 'plan', summary: 'make an implementation plan' },
    { name: 'run', summary: 'suggest how to run/build/test' },
    { name: 'implement', summary: 'implement a new feature from end-to-end' },
    { name: 'debug', summary: 'deeply debug an issue, error, or failing test' },
    { name: 'explore', summary: 'explore the codebase to gather context' },
    { name: 'research', summary: 'research a topic comprehensively' },
    { name: 'reason', summary: 'deep reasoning pass' },
  ];

  let slashFilter = '';
  let slashIndex = 0;

  let streaming = false;
  let currentAssistantBubble = null;
  let currentAssistantCard = null;
  let currentAssistantRaw = '';
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
    
    if (modeBtnLabelEl) {
      modeBtnLabelEl.textContent = currentMode.charAt(0).toUpperCase() + currentMode.slice(1);
    }
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

  let effortLevel = 3;
  function renderEffort() {
    if (!effortMeterEl) {
      return;
    }
    const dots = effortMeterEl.querySelectorAll('.dot');
    dots.forEach((dot) => {
      const level = Number(dot.getAttribute('data-level') || '0');
      dot.classList.toggle('on', level <= effortLevel);
      dot.classList.toggle('active', level === effortLevel);
    });
    effortMeterEl.setAttribute('aria-valuenow', String(effortLevel));
    effortMeterEl.title = 'Effort: ' + effortLevel + '/5';
  }
  if (effortMeterEl) {
    effortMeterEl.addEventListener('click', () => {
      effortLevel = effortLevel >= 5 ? 1 : effortLevel + 1;
      renderEffort();
    });
    renderEffort();
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
  if (historyBtn && searchRowEl) {
    historyBtn.addEventListener('click', () => {
      searchRowEl.classList.toggle('visible');
      if (searchRowEl.classList.contains('visible')) {
        searchInputEl.focus();
      }
    });
  }
  if (settingsBtn) {
    settingsBtn.addEventListener('click', () => {
      inputEl.value = '/model ';
      inputEl.focus();
      updateSlashFromInput();
    });
  }
  if (closeBtn) {
    closeBtn.addEventListener('click', () => {
      vscode.postMessage({ type: 'closePanel' });
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
    modelChipEl.addEventListener('click', () => queueCommand('/model'));
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
      name.textContent = file.name;
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

  function readFileAsText(file) {
    return new Promise((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => resolve(String(reader.result || ''));
      reader.onerror = () => reject(reader.error || new Error('read failed'));
      reader.readAsText(file);
    });
  }

  function readFileAsDataURL(file) {
    return new Promise((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => resolve(String(reader.result || ''));
      reader.onerror = () => reject(reader.error || new Error('read failed'));
      reader.readAsDataURL(file);
    });
  }

  if (attachBtn && fileInputEl) {
    attachBtn.addEventListener('click', () => {
      fileInputEl.click();
    });
    fileInputEl.addEventListener('change', async () => {
      const files = Array.from(fileInputEl.files || []);
      fileInputEl.value = '';
      for (const file of files) {
        if (pendingAttachments.length >= MAX_ATTACH_FILES) {
          break;
        }
        // Increase size limit for images if needed, but 512KB is what's there
        if (file.size > 2 * 1024 * 1024) { // Allow up to 2MB for images and text now
          addBubble('error', `skipped ${file.name}: larger than 2MB`);
          continue;
        }
        try {
          let text;
          let isImage = file.type.startsWith('image/');
          if (isImage) {
            text = await readFileAsDataURL(file);
          } else {
            text = await readFileAsText(file);
            if (text.length > MAX_ATTACH_CHARS) {
              text = text.slice(0, MAX_ATTACH_CHARS) + '\n…[truncated]';
            }
          }
          pendingAttachments.push({ name: file.name, text, isImage });
        } catch (err) {
          addBubble('error', `could not read ${file.name}`);
        }
      }
      renderAttachments();
      refreshContextUsage();
      inputEl.focus();
    });
  }

  // dismissFirstRun drops the static first-run guidance (see chatPanel.ts's
  // #firstRun) once the transcript has real content. Deliberately NOT called
  // for 'error' bubbles: the commonest first error is "daemon not found", and
  // that is precisely when the instructions are still worth reading.
  function dismissFirstRun() {
    const el = document.getElementById('firstRun');
    if (el) {
      el.remove();
    }
  }

  function avatarLabel(role) {
    if (role === 'user') {
      return 'Y';
    }
    if (role === 'error') {
      return '!';
    }
    return 'C';
  }

  function roleLabel(role) {
    if (role === 'user') {
      return 'You';
    }
    if (role === 'error') {
      return 'Error';
    }
    return 'CodeTerminal';
  }

  function addBubble(role, text) {
    if (role === 'user' || role === 'assistant') {
      dismissFirstRun();
    }
    const msg = document.createElement('div');
    msg.className = 'msg ' + role;

    const avatar = document.createElement('div');
    avatar.className = 'avatar';
    avatar.textContent = avatarLabel(role);
    msg.appendChild(avatar);

    const card = document.createElement('div');
    card.className = 'card';

    const label = document.createElement('span');
    label.className = 'role';
    label.textContent = roleLabel(role);
    card.appendChild(label);

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

  // renderFencedContent builds DOM for assistant text with ``` fences.
  // textContent / createElement ONLY -- never assemble markup strings.
  function renderFencedContent(container, text) {
    clearChildren(container);
    const parts = String(text).split(/```/);
    for (let i = 0; i < parts.length; i++) {
      const chunk = parts[i];
      if (i % 2 === 0) {
        if (!chunk) {
          continue;
        }
        const p = document.createElement('div');
        p.className = 'md-p';
        p.textContent = chunk;
        container.appendChild(p);
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
      if (lang) {
        const langEl = document.createElement('div');
        langEl.className = 'code-lang';
        langEl.textContent = lang;
        block.appendChild(langEl);
      }
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
      renderFencedContent(currentAssistantBubble, currentAssistantRaw);
      attachFeedbackFooter(currentAssistantCard, currentAssistantRaw);
    }
    currentAssistantBubble = null;
    currentAssistantCard = null;
    currentAssistantRaw = '';
  }

  function setStreaming(value) {
    streaming = value;
    inputEl.disabled = value;
    sendBtn.disabled = value;
    if (composerShellEl) {
      composerShellEl.classList.toggle('sending', value);
    }
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

    // The diff itself is a labelled region so a reader can be told what it is
    // about to read out, rather than encountering bare +/- lines.
    const pre = document.createElement('pre');
    pre.setAttribute('role', 'region');
    pre.setAttribute('aria-label', `Diff for ${edit.file_path}`);
    for (const line of edit.search.split('\n')) {
      const span = document.createElement('span');
      span.className = 'diff-line removed';
      span.textContent = '- ' + line;
      pre.appendChild(span);
    }
    for (const line of edit.replace.split('\n')) {
      const span = document.createElement('span');
      span.className = 'diff-line added';
      span.textContent = '+ ' + line;
      pre.appendChild(span);
    }
    container.appendChild(pre);

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
      resultEl.textContent = `${prefix} — backup: ${result.backupDir} (restore with: codeterminal-daemon edits undo)`;
    } else {
      resultEl.className = 'result refused';
      const prefix = currentRunAuto ? 'auto-refused' : 'refused';
      resultEl.textContent = `${prefix}: ${result.error}`;
    }
    container.appendChild(resultEl);
    if (currentEditProposalEl === container) {
      currentEditProposalEl = null;
    }
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
  // toolActivityEls maps a call id to its narration line, so a call's line is
  // REWRITTEN from "running" to its outcome rather than a new line per phase.
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
    container.setAttribute(
      'aria-label',
      'Approval needed to run ' + req.server + '__' + req.tool +
        ', step ' + req.iteration + ' of at most ' + req.max_iterations
    );
    container.setAttribute('aria-describedby', argsId + ' ' + laneId);

    const heading = document.createElement('div');
    heading.className = 'approval-heading';
    heading.textContent =
      'Run ' + req.server + '__' + req.tool + '? (step ' + req.iteration +
      ' of at most ' + req.max_iterations + ')';
    container.appendChild(heading);

    // THE FULL ARGUMENTS, never a summary: a consent prompt that shows less
    // than what will run is not consent, and the daemon binds its approval to a
    // digest of exactly these bytes.
    const argsLabel = document.createElement('div');
    argsLabel.className = 'approval-args-label';
    argsLabel.textContent = 'Arguments:';
    container.appendChild(argsLabel);

    const args = document.createElement('pre');
    args.id = argsId;
    args.className = 'approval-args';
    args.setAttribute('role', 'region');
    args.setAttribute('aria-label', 'Arguments this tool will receive');
    args.textContent = req.arguments;
    container.appendChild(args);

    const lane = document.createElement('div');
    lane.id = laneId;
    if (req.confined) {
      lane.className = 'approval-lane confined';
      lane.textContent =
        'This tool ships with CodeTerminal. Anything it changes goes through the same review you use for edits.';
    } else {
      // NEVER SOFTENED. A third-party MCP server is an ordinary subprocess with
      // the user's full access; this approval is the only thing in front of it.
      // Saying "sandboxed" here would be the single most damaging sentence this
      // panel could print.
      lane.className = 'approval-lane unconfined';
      lane.textContent =
        'NOT SANDBOXED: this is a separate program running with your full access. ' +
        'CodeTerminal cannot limit what it reads or changes — your approval is the only thing in its way.';
    }
    container.appendChild(lane);

    if (req.destructive) {
      const warn = document.createElement('div');
      warn.className = 'approval-lane unconfined';
      warn.textContent = 'The server describes this tool as destructive.';
      container.appendChild(warn);
    }

    const actions = document.createElement('div');
    actions.className = 'approval-actions';

    const deny = approvalButton('Deny', 'deny', req, 'Do not run this tool call');
    const approve = approvalButton('Run once', 'approve', req, 'Run this tool call with these exact arguments');
    const forTurn = approvalButton(
      'Allow for this task',
      'approve_for_turn',
      req,
      'Run this call and any later call to the same tool for the rest of this task'
    );
    const cancel = approvalButton('Stop the task', 'cancel_turn', req, 'Deny this call and abandon the whole task');

    // Deny first in DOM order as well as focus order: tab order should reach
    // the safe option before the permissive one.
    actions.appendChild(deny);
    actions.appendChild(approve);
    actions.appendChild(forTurn);
    actions.appendChild(cancel);
    container.appendChild(actions);

    transcriptEl.appendChild(container);
    transcriptEl.scrollTop = transcriptEl.scrollHeight;

    currentApprovalEl = container;
    currentApprovalCallId = req.call_id;
    deny.focus();
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

  // showToolActivity narrates one step of an agent turn, one line per call.
  //
  // The 'requested' and 'approved' phases are deliberately not rendered: the
  // user has just answered a dialog about that exact call, and reading it back
  // to them is noise -- worse for a screen reader than for an eye.
  function showToolActivity(activity) {
    const name = activity.server + '__' + activity.tool;
    let text = '';
    if (activity.phase === 'running') {
      text = 'Running ' + name + '…';
    } else if (activity.phase === 'succeeded') {
      text = name + ' — ' + (activity.result_bytes || 0) + ' bytes to the model in ' +
        (activity.duration_ms || 0) + 'ms';
    } else if (activity.phase === 'failed') {
      text = name + ' failed' + (activity.detail ? ': ' + activity.detail : '');
    } else if (activity.phase === 'denied') {
      text = name + ' not run' + (activity.detail ? ': ' + activity.detail : '');
    }
    if (text === '') {
      return;
    }

    const existing = toolActivityEls.get(activity.call_id);
    if (existing && existing.parentElement) {
      existing.textContent = text;
      return;
    }
    const el = document.createElement('div');
    el.className = 'tool-activity';
    el.setAttribute('role', 'status');
    el.textContent = text;
    transcriptEl.appendChild(el);
    transcriptEl.scrollTop = transcriptEl.scrollHeight;
    toolActivityEls.set(activity.call_id, el);
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
      b.textContent = `backups: ${summary.backupDir} (restore with: codeterminal-daemon edits undo)`;
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

  // renderSnippet turns a SearchResult.snippet's literal '[' / ']' match
  // markers (inserted by the daemon's FTS5 snippet() call, see
  // daemon/search.go) into visible highlighting -- built via createElement/
  // textContent like every other renderer in this file, never assembling
  // markup, so nothing in a search result (which is a user's own past
  // conversation text, not vetted markup) can inject anything into the page.
  function renderSnippet(container, text) {
    const parts = text.split(/([[\]])/);
    let highlighting = false;
    for (const part of parts) {
      if (part === '[') {
        highlighting = true;
        continue;
      }
      if (part === ']') {
        highlighting = false;
        continue;
      }
      if (part === '') {
        continue;
      }
      if (highlighting) {
        const mark = document.createElement('mark');
        mark.textContent = part;
        container.appendChild(mark);
      } else {
        container.appendChild(document.createTextNode(part));
      }
    }
  }

  // showSearchResults renders the outcome of one search: an error (search
  // couldn't run at all), a clean "no results" empty state (found nothing --
  // NOT an error, see protocol.SearchResponse's doc comment), or the ranked
  // result list -- already bm25-ranked by the daemon, never re-sorted here.
  function showSearchResults(msg) {
    clearChildren(searchResultsEl);

    if (msg.error) {
      const el = document.createElement('div');
      el.className = 'search-error';
      el.textContent = `search failed: ${msg.error}`;
      searchResultsEl.appendChild(el);
      return;
    }

    if (!msg.results || msg.results.length === 0) {
      const el = document.createElement('div');
      el.className = 'search-empty';
      el.textContent = 'no results';
      searchResultsEl.appendChild(el);
      return;
    }

    for (const result of msg.results) {
      const card = document.createElement('div');
      card.className = 'search-result';

      const meta = document.createElement('div');
      meta.className = 'search-result-meta';
      const role = result.role === 'user' ? 'you' : 'codeterminal';
      const when = new Date(result.created_at).toLocaleString();
      meta.textContent = `${role} · ${when}`;
      card.appendChild(meta);

      const snippet = document.createElement('div');
      snippet.className = 'search-result-snippet';
      renderSnippet(snippet, result.snippet);
      card.appendChild(snippet);

      searchResultsEl.appendChild(card);
    }
  }

  function doSearch() {
    const query = searchInputEl.value.trim();
    if (!query) {
      return;
    }
    searchBtn.disabled = true;
    searchBtn.textContent = 'Searching…';
    vscode.postMessage({ type: 'search', text: query });
  }

  searchBtn.addEventListener('click', doSearch);
  searchInputEl.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      doSearch();
    }
  });

  function filteredSlashCommands() {
    const q = slashFilter.toLowerCase();
    return SLASH_COMMANDS.filter((c) => c.name.startsWith(q));
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

  function applySlash(name) {
    inputEl.value = '/' + name + ' ';
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
    hideSlashMenu();
    clearEditProposal();
    clearToolApproval();
    toolActivityEls.clear();

    let text = typed;
    if (pendingAttachments.length > 0) {
      const blocks = pendingAttachments.map((f) => {
        if (f.isImage) {
          return `![${f.name}](${f.text})`;
        }
        return `--- attached: ${f.name} ---\n${f.text}\n--- end ${f.name} ---`;
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
    vscode.postMessage({ type: 'prompt', text, autoApply: autoApplyEnabled, mode: currentMode });
    // Keep focus on the input across a send. Clicking Send moved focus to the
    // button, so a keyboard or screen-reader user had to navigate back to ask a
    // follow-up; the natural next action is always another prompt.
    inputEl.focus();
  }

  sendBtn.addEventListener('click', send);
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
      send();
    }
  });

  // Initial focus: the prompt input is the panel's primary control, so opening
  // the panel puts the caret where the user is going to type. Previously nothing
  // was focused and a keyboard user had to tab in from the top of the document.
  inputEl.focus();

  function clearTranscriptView() {
    clearChildren(transcriptEl);
    currentAssistantBubble = null;
    currentAssistantCard = null;
    currentAssistantRaw = '';
    currentReasoningBody = null;
    clearEditProposal();
    clearToolApproval();
    toolActivityEls.clear();
    clearChildren(searchResultsEl);
    lastGroundingInfo = null;
    lastHistoryMeta = null;
    refreshContextUsage();
  }

  window.addEventListener('message', (event) => {
    const msg = event.data;
    switch (msg.type) {
      case 'history':
        for (const turn of msg.turns) {
          const body = addBubble(turn.role, '');
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
      case 'modelTier':
        if (modelChipEl) {
          if (modelChipLabelEl) {
            modelChipLabelEl.textContent = msg.tier || 'Model';
          } else if (modelChipEl) {
            modelChipEl.textContent = msg.tier || 'Model';
          }
        }
        break;
      case 'grounding':
        setGrounding(msg.info);
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
      case 'token':
        if (!currentAssistantBubble) {
          currentAssistantBubble = addBubble('assistant', '');
        }
        currentAssistantRaw += msg.text;
        currentAssistantBubble.textContent = currentAssistantRaw;
        transcriptEl.scrollTop = transcriptEl.scrollHeight;
        break;
      case 'done':
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
        addBubble('error', msg.message);
        setStreaming(false);
        break;
      case 'incomplete':
        showIncomplete(msg.info);
        break;
      case 'toolActivity':
        showToolActivity(msg.activity);
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
          });
        }
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
      case 'searchResults':
        searchBtn.disabled = false;
        searchBtn.textContent = 'Search';
        showSearchResults(msg);
        break;
    }
  });
}());
