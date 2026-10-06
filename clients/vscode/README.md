# Mochiii VS Code Extension

A chat panel inside VS Code that talks to the local `mochiii-daemon` over
its existing newline-delimited JSON socket protocol (see `protocol/protocol.go`),
plus in-editor diff-apply: a model-proposed edit renders as a red/green diff with
Apply/Skip, and Apply routes through the same `editapply` engine (all five safety
gates) the TUI uses — the daemon applies, this client only renders and confirms.

**The extension manages the daemon; you do not start one by hand.** It used to
require an already-running daemon, which is why the TUI's two-terminal setup is
described everywhere else. `src/daemonSupervisor.ts` probes the per-workspace
lockfile, adopts a daemon that answers a full handshake, and starts one only when
nothing does — so two windows open on the same folder share one daemon rather
than racing to bind. It never deletes a stale lockfile: that belongs to the
daemon's own `reclaimStaleSocket` and nowhere else.

Every proposed edit in a response is reviewable, one block at a time: each
is presented for Apply/Skip in turn, and block *i+1* is only ever matched
against what is actually on disk after block *i* has been applied or
skipped (see `startEditReview` in `src/chatPanel.ts`). An applied batch
gets a native **Undo this apply** button that runs the same backup restore
`mochiii-daemon edits undo` does.

Not in this slice: a native VS Code diff view/inline decorations, ghost
text, error interceptor, MCP, reset/ctrl+n, or any remote-host
(SSH/WSL/devcontainer) daemon discovery — this is local-machine-only.

Also not here yet, and worth knowing before relying on it: closing the window
that STARTED the daemon stops it under a window that adopted it. The fix is an
idle timeout plus a shutdown RPC, both scoped and neither built.

## Connecting a model provider

Type **`/connect`** in the chat (or run **Mochiii: Set API Key**) and paste the
key into the masked box. The daemon recognises the provider from the key in most
cases -- Groq (`gsk_`), NVIDIA (`nvapi-`), OpenRouter (`sk-or-`), Anthropic
(`sk-ant-`), Google Gemini (`AIza`), xAI, Together, Fireworks, Cerebras, Hugging
Face and more (17 in all) -- checks it with that provider, finds a model that
answers, stores it in `~/.mochiii/credentials.json` (mode 0600) and uses it at
once, with no restart. The key never appears in the chat; only a masked form
comes back.

- `/connect <provider>` (e.g. `/connect groq`) or `/connect <API address>`
  chooses the provider first, for a key that does not say whose it is.
- A key several providers issue (a bare `sk-...`) is sent nowhere until you pick
  the provider from a list.
- `/connect show` says which key is in use; `/connect forget` removes it.
- If a question is sent before any key is connected, the box opens first and the
  question is sent as soon as the key works.
- The terminal client and VS Code share this one stored key. A key VS Code kept
  in its own secret store before this change is passed to the daemon as
  `MOCHIII_API_KEY`, which outranks the stored one; `/connect` removes it so the
  key you connect is the one used. With no folder open there is no daemon to
  check a key, so **Set API Key** still stores it in VS Code's secret store.

## Composer controls

- **Model chip** shows the model actually answering (e.g. `qwen3.8-27b`), not the
  tier name; click it for `/model`.
- **Effort** sets how long the model reasons before it answers, per prompt:
  Auto (the model's default), Low, Medium, High. It is sent as
  `reasoning_effort`; Auto sends nothing. Measured on Groq: `qwen3.8-27b` does
  not reason at all on Auto, and `gpt-oss` accepts only low/medium/high. If a
  model refuses the field, the daemon asks again without it and remembers that
  for that model, so a refused effort never costs the turn.
- **Context ring** shows how full the conversation is, with the percentage;
  amber from 60%, red from 80%.
- Answers render markdown (headings, lists, tables, code, links, and the common
  inline LaTeX arrows) without ever inserting HTML from the model's reply.

## Attachments

The paperclip opens the editor's file dialog and takes text and code files,
**PDF (including scanned PDFs), Word (.docx), Excel (.xlsx), PowerPoint (.pptx)
and images**. Everything is read **on your machine**, by the extension host from
disk: the files are turned into text by `src/attachmentExtract.ts` (parsers in
`src/extractors/`, bundled with their libraries into `out/vendor/` by
`npm run build:extractors`), and only that text is added to your prompt, the same
way an attached `.txt` always was. Reading a file makes no network connection.
Whether the prompt itself leaves your machine depends on the model you
connected, as it always did.

What is and is not read:

| File | Read | Not read |
|---|---|---|
| PDF | the text layer, up to 200 pages; **a page with no text layer is read by OCR of its scanned image** (up to 30 such pages, enlarged to ~300 DPI when the scan is coarser than 200 DPI, turned upright if the page is rotated) | pictures on pages that do have text; text drawn as vector shapes |
| Word | body text, headings, lists, tables, text boxes | headers/footers, footnotes, comments, images |
| Excel | every sheet as tab-separated rows (cached values; dates shown as dates), up to 20 sheets x 2000 rows x 50 columns | charts, formulas as formulas |
| PowerPoint | each slide's text in order, plus speaker notes, up to 200 slides | pictures, charts, slide numbers and footers |
| Image | **text in the image**, by OCR (English) | anything that is not text: a photo or chart yields nothing |

Limits: 8 files per message, 80,000 characters per file, and by size: text 10 MB,
images 20 MB, PDF 100 MB, Office files 200 MB (they are mostly pictures, which are
never opened; only the text parts are inflated, and those are capped). Anything
read by OCR is labelled as such to the model, with how OCR typically fails
(symbols read as digits, URLs losing dots) and an instruction not to guess
corrections -- added after a model "corrected" OCR'd figures into invented ones.
Old `.doc`/`.xls`/`.ppt` files are refused with a message asking for the modern
format. An image with no readable text is refused rather than sent as noise.
The first image read starts an OCR worker (about 100-300 MB) that is released
after 30 seconds idle. An image is never *described*: that needs a vision model,
which this does not include.

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
./daemon/mochiii-daemon

# In this directory:
npm install
npm run compile
```

Then open this `clients/vscode` folder as a VS Code workspace and press
`F5` to launch an Extension Development Host. Run the command
**"Mochiii: Open Chat"** (Ctrl/Cmd+Shift+P). Any cross-session history
the daemon has for its workspace renders before you type anything; sending
a prompt streams the grounded answer back token-by-token.

If the model's answer includes a SEARCH/REPLACE edit block, the panel
renders it as a diff with **Apply**/**Skip** buttons right below the
answer. Apply sends the block to the daemon as-is; the daemon runs it
through `editapply.PrepareEdit` (exact-match, ambiguity-refuse, workspace
confinement, secret-file refusal, syntax gate) before writing + backing it
up, and the panel shows whatever the daemon reports — a success line with
the backup dir, or the exact refusal string, file untouched either way.
