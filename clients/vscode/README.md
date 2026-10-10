# Mochiii

**A local-first AI coding assistant.** Mochiii indexes, embeds, and retrieves
your codebase entirely on your own machine — a background daemon does the
work, and this extension puts a full chat panel in your editor for it.
Nothing on your machine listens on the network, and the only thing that ever
leaves it is the minimal prompt for a single turn, sent to the model
provider *you* choose.

**The extension manages the daemon for you.** Open a workspace, run
**Mochiii: Open Chat**, and it starts one automatically — `src/daemonSupervisor.ts`
probes for an existing daemon first, so a second window on the same folder
shares it rather than racing to start its own.

## Why Mochiii

- **Your code stays yours.** Indexing, embedding, and retrieval all happen
  on-device. No telemetry, no silent uploads.
- **Review every edit.** A model-proposed change renders as a red/green diff
  — Apply or Skip, one block at a time, each matched against what is
  actually on disk after the block before it — routed through a five-gate
  safety engine before anything touches your files. One click (**Undo this
  apply**) reverts a whole batch from its backup.
- **Bring your own model.** `/connect` takes an API key for 17+ providers
  (OpenRouter, Groq, Anthropic, Google Gemini, and more), or point it at a
  local server (Ollama, LM Studio) and keep inference on-device too.
- **One chat, everywhere.** The panel and Mochiii's terminal client share the
  same conversation per workspace — ask in one, keep going in the other.
- **Understands your files.** Attach PDFs (including scanned ones), Word,
  Excel, PowerPoint, or images — read and understood locally.
- **Agent mode, on your terms.** Off by default. Turn it on to let Mochiii
  call tools — its own sandboxed built-ins, or external servers you
  configure (MCP) — and approve every call before it runs.

## Getting started

1. Install the extension and open a trusted workspace.
2. Run **Mochiii: Open Chat** (Ctrl/Cmd+Shift+P), or click the Mochiii icon.
3. `/connect` a provider, or set `mochiii.apiBase` to a local model server.
4. Ask a question — your codebase is indexed automatically.

## Platform support

Linux x64 and Windows x64 today (no macOS build yet). This is a **preview**
release — expect rough edges, and [tell us about them](https://github.com/Rav-2007/codeterminal-core/issues).

Known limits, worth knowing before relying on them: no native VS Code diff
view/inline decorations, ghost text, or error interceptor yet; no remote-host
(SSH/WSL/devcontainer) daemon discovery — this is local-machine-only; and
closing the VS Code window that *started* the daemon stops it even for a
window that only adopted it (an idle timeout plus a shutdown RPC are planned,
not yet built).

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

## Agent mode (MCP)

Off by default: the daemon bundled with the extension ships with agent mode
disabled, so nothing is spawned and no tools are offered. To turn it on, set
**`mochiii.configPath`** to the absolute path of a `models.json` of your own
whose `mcp` block has `enabled: true` — `models.agent.json` in the repo is a
worked example, and each external server under `mcp.servers` needs
`acknowledged_unconfined: true`. The extension starts its daemon with that
config; a file of your own is used rather than the bundled one so an update
never overwrites it.

`mochiii.configPath` is **machine-scoped on purpose**: it names programs the
daemon will run, and a workspace's `.vscode/settings.json` must never be able
to choose them — the same reasoning as `mochiii.apiBase`, and why the path must
be absolute.

Once on, a tool the model calls appears as an **approval card** naming the
server, the tool and the exact arguments, and — for an external (Lane B)
server — that it is **not sandboxed** and runs with your full privileges;
nothing runs until you approve it. **`/mcp-server`** lists what agent mode would
offer and the policy on each tool, read from the same config the daemon loaded.
The tool menu is capped (`mcp.budget.max_advertised_tools`); when it drops a
tool, the chat says which ones, so you can raise the cap if you need them.

## History, bookmarks and /compact

- **History** (the clock button) lists every chat, newest first, with its age;
  bookmarked chats are pinned on top. The search box matches what was said in a
  chat, not only its title. Click a chat to open it; the one you leave is saved.
- **Every chat is kept** (setting `mochiii.history.autoSave`, on by default):
  a chat is saved when you start a new one with **+**, open another, or compact.
  Saved chats live on this machine, outside the project, bounded to 50 chats and
  20 MB per workspace. Turn the setting off to keep only bookmarked chats.
- **☆ Bookmark** (header, or the star on a row) pins a chat; a bookmarked chat
  is never removed by those limits.
- **/compact** summarises the earlier part of the chat with one model call and
  keeps the most recent messages word for word, like Claude Code. The full chat
  is saved to History first.
- **Notices** about an answer (not grounded, a privacy note about the provider,
  a scrubbed secret) are shown in full the first time, then fold into the
  **ⓘ** chip next to the input; screen readers still announce every one.

The terminal client shares the same saved chats through `/history`.

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
