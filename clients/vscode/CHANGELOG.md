# Changelog

All notable changes to the Mochiii extension. Versions follow the extension's
`version` in `package.json`.

## [1.0.0] - Unreleased

The first non-preview release. Everything since 0.1.1 (PRs #23–#40), plus the
1.0.0 fixes below. A 0.1.1 install updates in place: no setting was renamed or
removed, and the chat's view ids are unchanged.

### Added

- **Mochiii has its own icon in the Activity Bar**, beside Explorer, Search and
  the other agents. Clicking it opens the chat in the primary side bar. The
  secondary side bar home stays, so the chat can still sit beside your files.
  It is one conversation shown in one place at a time; the other place says
  where the chat is and offers **Move the chat here**, and never cuts off an
  answer, an approval or an edit review because you clicked it.
- **The chat in the side bar** (0.2.0 overhaul, #23; Stage 8, #29; Stage 9, #30):
  a work block that folds each turn's tool steps, Try again and Continue on a
  failed turn, a working-folder chooser, Customize, Account and Settings pages,
  `@` context and the composer redesign.
- **MCP servers** (#26, #27, #32–#37): an MCP Servers view and a marketplace
  backed by mochiii.in; server keys kept by the daemon; health checks; remote
  servers over streamable HTTP and SSE; browser sign-in (OAuth); and servers a
  project's `.mcp.json` proposes, each run only once you approve it.
- **The agent can delete, move and restore files** (#38), each asked about, with
  a trash; and **run a terminal command** (#39), approved per call.
- **Project rules** that reach every later chat (#28, #31) and **GitHub reading**:
  issues, pull requests and why CI failed (#40).
- `mochiii.developerDiagnostics` (off by default) shows which daemon component
  raised each notice in the chat. **Mochiii: Show Daemon Log** has the full record.

### Changed

- **Connected tools past the menu limit stay usable.** The model is shown at
  most 24 tools at a time, because a wider menu measurably made it choose worse.
  That limit used to cut the list alphabetically, so a question about Supabase
  projects could run without `list_projects`. Now each question keeps the tools
  that match it best, and the model can look up the rest (`find_tools`) when a
  task needs them. Every call still asks before it runs, exactly as before.
- The chat no longer shows "degraded (tool_menu_truncated)" with a list of
  internal tool names on every turn. Notices are written in plain words; the
  internal component name appears only with `mochiii.developerDiagnostics` on.
- No longer marked as a preview on the Marketplace.

## [0.1.1] - 2026-10-11

Adds the **Windows** package. 0.1.0 shipped for Linux only — not by choice: the
Windows build was produced and was fine, but the packaging gate refused it
because four Go libraries that only the Windows binaries link were missing from
`THIRD_PARTY_LICENSES.txt`, and a licence that does not travel with the binary
is a licence violation. This release adds those notices, so the gate passes and
the Windows `.vsix` ships.

No behaviour changed. A 0.1.0 install can update in place.

### Added

- **Windows (`win32-x64`) package.** Windows binaries are unsigned, so
  SmartScreen may warn on first run; that is known and accepted for the preview.
- Licence notices for the four Windows-only Go modules: `go-winio` (the
  named-pipe transport the daemon listens on under Windows), `go-strftime`
  (reached through SQLite's C runtime), and `coninput` plus `go-localereader`
  (the terminal client's Windows console input and code-page decoding).

### Fixed

- The packaging gate's notice check now has a complete answer for every module
  linked into a Windows binary, on all three shipped executables rather than the
  two inside the `.vsix`.

## [0.1.0] - 2026-10-10

The first release on the Visual Studio Marketplace, as a preview.

**Installed an earlier `.vsix` from a GitHub Release?** Uninstall it first. Those
were published as `mochiii.mochiii-vscode`; this is
`MochiiiAIAgent.mochiii-vscode`, so VS Code treats them as two extensions, and
both would register the same commands and each try to manage the daemon. A key
connected with `/connect` carries over (it is stored by the daemon); a key saved
with **Set API Key** in the old extension does not, and needs entering once more.

### Added

- **One chat across the editor and the terminal.** The panel keeps up with
  what the Mochiii terminal client adds to the same folder's chat: when the
  panel or the window gets focus, it shows what the other client said, and a
  question asked in either is answered with the other's turns in view.
- **Attachments.** Attach PDF, Word, Excel, PowerPoint and image files; their
  text is read on this machine.
- **History.** Earlier chats are kept, can be bookmarked and opened again, and
  `/compact` summarises the earlier part of a long chat.
- **`/connect`** takes an API key for any of 17 providers.
- **Specs and local models.** The `/spec` workflow, and the `mochiii.model`
  setting for a local server such as Ollama or LM Studio.
- **The agent tests its own edits** in a private working copy before offering
  them for review, and can read folders outside the project, asking each time.
- **Agent mode (MCP) can be turned on from the extension.** Set
  `mochiii.configPath` to a `models.json` of your own with agent mode enabled,
  and the extension starts its daemon with it — no need to launch a daemon by
  hand. The setting is machine-scoped, so a repository cannot choose what runs.
- The chat panel reopens after VS Code restarts, if it was open when the window
  closed.

### Changed

- Mochiii starts when you first use it (opening the chat or running a Mochiii
  command), not with every VS Code window.
- Mochiii needs a trusted workspace, and is not available on virtual
  workspaces: its daemon indexes the folder on disk and, with your approval,
  edits files and runs commands in it.
- Commands are listed under the **Mochiii** category in the Command Palette.
- A sharper, 256-pixel icon.
- The extension's code ships as one bundled file instead of 15 separate
  modules, so it loads faster.

### Fixed

- **`/mcp-server` now reports the config the daemon actually loaded**, and
  names the file, rather than always reading the one bundled with the
  extension — which could say agent mode was off while the running daemon had
  tools, or the reverse.
- **When the tool menu is capped, the chat now names the tools left out**, not
  just how many, so you can tell whether the one you need was dropped.
- **`/mcp-server` no longer counts the daemon's own tools as external MCP
  servers.** It reported "N run in external MCP servers" with N inflated by
  first-party tools; it now counts only the third-party ones.

### Security

- The API key is handed to the daemon on its standard input, never in its
  environment, so it no longer sits in `/proc/<pid>/environ`.

## [0.0.4] - 2026-09-21

The first release meant to be installed; v0.0.1 to v0.0.3 were tagged and built
but never published.

- **Everything is named Mochiii**: binaries, settings, command IDs and the
  state directory.
- **`Mochiii: Set API Key`** stores a key in VS Code's secret storage, and the
  daemon is restarted to use it. Before this a packaged install had no way to
  receive a key.
- The package carries its own daemon and embedder helper, for Linux x64 and
  Windows x64.
