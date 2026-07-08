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
  // the same way. moreCount > 0 means the response had additional blocks
  // this slice doesn't act on yet; that's stated explicitly so it reads as
  // "not shown yet", not as a bug that silently dropped them.
  function showEditProposal(edit, moreCount) {
    clearEditProposal();

    const container = document.createElement('div');
    container.className = 'edit-proposal';

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

    if (moreCount > 0) {
      const note = document.createElement('div');
      note.className = 'more-note';
      note.textContent = `${moreCount} more edit${moreCount === 1 ? '' : 's'} not shown yet`;
      container.appendChild(note);
    }

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
    vscode.postMessage({ type: 'prompt', text });
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
        showEditProposal(msg.edit, msg.moreCount);
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
    }
  });
}());
