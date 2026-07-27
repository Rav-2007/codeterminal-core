# CodeTerminal VS Code Extension

A chat panel inside VS Code that talks to the **already-running** local
`codeterminal-daemon` over its existing newline-delimited JSON Unix-socket
protocol (see `protocol/protocol.go`), plus in-editor diff-apply: a model-
proposed edit renders as a red/green diff with Apply/Skip, and Apply routes
through the same `editapply` engine (all five safety gates) the TUI uses —
the daemon applies, this client only renders and confirms.

Every proposed edit in a response is reviewable, one block at a time: each
is presented for Apply/Skip in turn, and block *i+1* is only ever matched
against what is actually on disk after block *i* has been applied or
skipped (see `startEditReview` in `src/chatPanel.ts`). An applied batch
gets a native **Undo this apply** button that runs the same backup restore
`codeterminal-daemon edits undo` does.

Not in this slice: a native VS Code diff view/inline decorations, ghost
text, error interceptor, MCP, reset/ctrl+n, or any remote-host
(SSH/WSL/devcontainer) daemon discovery — this is local-machine-only, same
as the TUI's usual two-terminal setup.

## Architecture

- `src/daemonClient.ts` — extension-host-only module that owns the Unix
  socket: reads the daemon's lockfile, connects, frames/parses newline-
  delimited JSON, and exposes a small async interface
  (`preflightHandshake`, `streamPrompt`, `applyEdit`). This is a straight
  TypeScript reimplementation of `clients/tui/daemonconn.go` + `stream.go`
  (and, for `applyEdit`, of the daemon's `ApplyEditRequest` handler) — the
  wire format is already JSON, so no new dependency and no Go sidecar is
  needed. `streamPrompt`'s final `TokenResponse` may carry `edit_proposals`;
  `applyEdit` sends the accepted block back verbatim on its own connection
  and relays the daemon's `ApplyEditResponse` (applied + backup dir, or the
  exact gate-refusal string) — no gate is reimplemented in TypeScript.
- `src/chatPanel.ts` — creates the webview panel, keeps the conversation
  transcript (source of truth for building `PromptRequest.History`, mirrors
  `buildHistory` in `clients/tui/chat.go`), tracks the single pending edit
  proposal (cleared on apply, skip, or a new prompt so a lagging click can
  never target a stale block), and relays daemon events to the webview via
  `postMessage`. The webview cannot open a socket itself — all daemon I/O
  lives here.
- `media/main.js` — runs inside the sandboxed webview: renders the
  transcript, a grounding indicator, the edit-proposal diff (whole-block
  red/green, matching the TUI's non-LCS rendering) with Apply/Skip buttons,
  the apply result (applied + backup-dir hint, or the refusal reason), and
  forwards user input up via `postMessage`. No daemon access.

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

If the model's answer includes a SEARCH/REPLACE edit block, the panel
renders it as a diff with **Apply**/**Skip** buttons right below the
answer. Apply sends the block to the daemon as-is; the daemon runs it
through `editapply.PrepareEdit` (exact-match, ambiguity-refuse, workspace
confinement, secret-file refusal, syntax gate) before writing + backing it
up, and the panel shows whatever the daemon reports — a success line with
the backup dir, or the exact refusal string, file untouched either way.
