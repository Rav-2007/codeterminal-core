# CodeTerminal VS Code Extension

Thinnest possible slice: a chat panel inside VS Code that talks to the
**already-running** local `codeterminal-daemon` over its existing
newline-delimited JSON Unix-socket protocol (see `protocol/protocol.go`).

Not in this slice: diff-apply, ghost text, error interceptor, MCP,
reset/ctrl+n, or any remote-host (SSH/WSL/devcontainer) daemon discovery —
this is local-machine-only, same as the TUI's usual two-terminal setup.

## Architecture

- `src/daemonClient.ts` — extension-host-only module that owns the Unix
  socket: reads the daemon's lockfile, connects, frames/parses newline-
  delimited JSON, and exposes a small async interface
  (`preflightHandshake`, `streamPrompt`). This is a straight TypeScript
  reimplementation of `clients/tui/daemonconn.go` + `stream.go` — the wire
  format is already JSON, so no new dependency and no Go sidecar is needed.
- `src/chatPanel.ts` — creates the webview panel, keeps the conversation
  transcript (source of truth for building `PromptRequest.History`, mirrors
  `buildHistory` in `clients/tui/chat.go`), and relays daemon events to the
  webview via `postMessage`. The webview cannot open a socket itself — all
  daemon I/O lives here.
- `media/main.js` — runs inside the sandboxed webview: renders the
  transcript, a grounding indicator, and forwards user input up via
  `postMessage`. No daemon access.

## Try it

```bash
# Terminal 1: start the daemon as usual (see repo root README)
cd /path/to/repo
set -a && source .env && set +a
./daemon/codeterminal-daemon

# In this directory:
npm install
npm run compile
```

Then open this `clients/vscode` folder as a VS Code workspace and press
`F5` to launch an Extension Development Host. Run the command
**"CodeTerminal: Open Chat"** (Ctrl/Cmd+Shift+P). Any cross-session history
the daemon has for its workspace renders before you type anything; sending
a prompt streams the grounded answer back token-by-token.
