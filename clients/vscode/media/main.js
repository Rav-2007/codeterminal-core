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

  function send() {
    const text = inputEl.value.trim();
    if (!text || streaming) {
      return;
    }
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
    }
  });
}());
