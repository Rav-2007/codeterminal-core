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

  let streaming = false;
  let currentAssistantBubble = null;
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
  let autoApplyEnabled = false;

  const autoApplyToggle = document.getElementById('autoApplyToggle');

  function renderAutoApplyToggle() {
    autoApplyToggle.textContent = autoApplyEnabled ? 'Auto-apply: ON' : 'Auto-apply: OFF';
    autoApplyToggle.className = autoApplyEnabled ? 'auto-apply-toggle on' : 'auto-apply-toggle off';
  }

  autoApplyToggle.addEventListener('click', () => {
    autoApplyEnabled = !autoApplyEnabled;
    renderAutoApplyToggle();
  });

  renderAutoApplyToggle();

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

  function addBubble(role, text) {
    if (role === 'user' || role === 'assistant') {
      dismissFirstRun();
    }
    const div = document.createElement('div');
    div.className = 'turn ' + role;
    const label = document.createElement('span');
    label.className = 'role';
    label.textContent = role === 'user' ? 'you' : role === 'error' ? 'error' : 'codeterminal';
    div.appendChild(label);
    const body = document.createElement('span');
    body.textContent = text;
    div.appendChild(body);
    transcriptEl.appendChild(div);
    transcriptEl.scrollTop = transcriptEl.scrollHeight;
    return body;
  }

  function setStreaming(value) {
    streaming = value;
    inputEl.disabled = value;
    sendBtn.disabled = value;
  }

  function setGrounding(info) {
    if (!info) {
      groundingEl.textContent = '';
      return;
    }
    groundingEl.textContent = info.grounded
      ? `grounded · ${info.chunks ?? 0} chunk(s)${info.truncated ? ' (truncated)' : ''}`
      : `not grounded${info.reason ? ' · ' + info.reason : ''}`;
  }

  // setHistoryInfo renders a warning when the daemon dropped the oldest
  // conversation turns this client sent (see protocol.HistoryInfo.truncated),
  // mirroring setGrounding's lifetime: cleared at the start of every turn (see
  // send()) and set at most once per response, since 'historyInfo' rides the
  // same pre-token message as 'grounding'. A non-truncated report shows nothing.
  function setHistoryInfo(info) {
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
  // thought. textContent (never innerHTML): thinking is daemon-relayed model
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
        currentAssistantBubble && currentAssistantBubble.parentElement;
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
  // (never innerHTML) per line: detail is daemon-authored prose and is
  // inserted as text, not markup.
  function setDegraded(items) {
    degradedEl.textContent = '';
    if (!items || items.length === 0) {
      return;
    }
    for (const item of items) {
      const line = document.createElement('span');
      line.className = 'item';
      line.textContent = `⚠ degraded (${item.component}): ${item.detail}`;
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
  // (never innerHTML): the provider name is daemon-relayed text. Strictly
  // "served by X" — no fallback claim, no ZDR verdict.
  function setProvider(provider) {
    providerEl.textContent = provider ? `served by: ${provider}` : '';
  }

  // showIncomplete renders a persistent, visibly-distinct notice that the
  // model's answer was CUT OFF rather than finished (see
  // protocol.TokenResponse.Incomplete). It is appended to the transcript right
  // after the partial answer -- NOT a transient header like grounding/provider
  // that clears next turn -- so the scrollback keeps an honest record that this
  // reply is incomplete. textContent (never innerHTML): detail is daemon-relayed
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

    const indexEl = document.createElement('div');
    indexEl.className = 'edit-index';
    indexEl.textContent = `Edit ${index + 1} of ${total}`;
    container.appendChild(indexEl);

    const pathEl = document.createElement('div');
    pathEl.className = 'file-path';
    pathEl.textContent = edit.file_path;
    container.appendChild(pathEl);

    const pre = document.createElement('pre');
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
      const applyBtn = document.createElement('button');
      applyBtn.textContent = 'Apply';
      const skipBtn = document.createElement('button');
      skipBtn.textContent = 'Skip';
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
  // textContent like every other renderer in this file, never innerHTML, so
  // nothing in a search result (which is a user's own past conversation
  // text, not vetted markup) can inject anything into the page.
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
    while (searchResultsEl.firstChild) {
      searchResultsEl.removeChild(searchResultsEl.firstChild);
    }

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

  function send() {
    const text = inputEl.value.trim();
    if (!text || streaming) {
      return;
    }
    clearEditProposal();
    addBubble('user', text);
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
    vscode.postMessage({ type: 'prompt', text, autoApply: autoApplyEnabled });
  }

  sendBtn.addEventListener('click', send);
  inputEl.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      send();
    }
  });

  window.addEventListener('message', (event) => {
    const msg = event.data;
    switch (msg.type) {
      case 'history':
        for (const turn of msg.turns) {
          addBubble(turn.role, turn.content);
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
        if (currentAssistantBubble) {
          currentAssistantBubble.textContent += msg.text;
          transcriptEl.scrollTop = transcriptEl.scrollHeight;
        }
        break;
      case 'done':
        currentAssistantBubble = null;
        setStreaming(false);
        inputEl.focus();
        break;
      case 'error':
        if (currentAssistantBubble && currentAssistantBubble.textContent === '') {
          currentAssistantBubble.parentElement.remove();
        }
        currentAssistantBubble = null;
        addBubble('error', msg.message);
        setStreaming(false);
        break;
      case 'incomplete':
        showIncomplete(msg.info);
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
