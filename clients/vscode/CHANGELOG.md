# Changelog

All notable changes to the Mochiii extension. Versions follow the extension's
`version` in `package.json`.

## [Unreleased]

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
