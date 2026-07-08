// Runs inside the webview sandbox: no Node, no filesystem, no socket access
// -- only the DOM and postMessage to/from the extension host. All daemon
// I/O happens in daemonClient.ts; this script only renders what arrives and
// forwards what the user types.
(function () {
  const vscode = acquireVsCodeApi();

  const transcriptEl = document.getElementById('transcript');
  const groundingEl = document.getElementById('grounding');
  const inputEl = document.getElementById('promptInput');
  const sendBtn = document.getElementById('sendBtn');

  let streaming = false;
  let currentAssistantBubble = null;
  let currentEditProposalEl = null;
  let pendingUndoButton = null;

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

  function addBubble(role, text) {
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

    transcriptEl.appendChild(container);
    transcriptEl.scrollTop = transcriptEl.scrollHeight;
    currentEditProposalEl = container;
  }

  // showEditResult replaces a proposal's Apply/Skip buttons with the
  // outcome: the daemon's exact gate-refusal string on failure (the same
  // text the TUI would show for the same bad edit), or a success note with
  // the backup dir restore hint stays with `daemon edits undo`, matching
  // the TUI -- this slice adds no native undo button.
  function showEditResult(container, result) {
    const actions = container.querySelector('.actions');
    if (actions) {
      actions.remove();
    }
    const resultEl = document.createElement('div');
    if (result.skipped) {
      resultEl.className = 'result';
      resultEl.textContent = 'skipped';
    } else if (result.applied) {
      resultEl.className = 'result ok';
      resultEl.textContent = `applied — backup: ${result.backupDir} (restore with: codeterminal-daemon edits undo)`;
    } else {
      resultEl.className = 'result refused';
      resultEl.textContent = `refused: ${result.error}`;
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
      el.textContent = `undo failed: ${result.error}`;
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

  function send() {
    const text = inputEl.value.trim();
    if (!text || streaming) {
      return;
    }
    clearEditProposal();
    addBubble('user', text);
    inputEl.value = '';
    setGrounding(null);
    setStreaming(true);
    currentAssistantBubble = addBubble('assistant', '');
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
    }
  });
}());
