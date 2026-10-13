<!-- coderefs: enforced -->

# VS Code client overhaul — the 0.2.0 plan

**Written 2026-10-12 against `0.1.1`; stage status updated 2026-10-13.** This
document is the scope, the evidence, and the order of work for the release that
makes the VS Code extension usable. Every claim below was read out of the
shipped code or reproduced on a real machine, and each carries its `file:line`.

**Read the findings as describing `0.1.1`, which is what shipped.** Stages 1-6
have since landed in the working tree, so several defects below are fixed in
the code you are reading; each says where it stands, and the citations point at
the CURRENT lines. Three corrections to the original findings are marked
**CORRECTION** -- they were wrong when written, and one of them would have sent
Stage 5 after a problem that did not exist.

---

## Why this exists

`0.1.0` shipped to the Marketplace on 2026-10-10 (Linux), `0.1.1` added Windows
on 2026-10-11. Both passed every gate this repository has. Both are, as a
product, not usable:

- On **Windows the extension can never reach its own daemon.** Not sometimes —
  never, on every machine.
- A user with no API key is told the **daemon failed to start**, which is
  neither true nor actionable.
- There is **no way to enter a key from the chat panel at all.**
- The model is **never given the file you have open**, and has **no tools**, so
  it cannot read your code.
- **Effort** and **modes** are controls that change nothing the user can
  observe.
- The panel **ignores your VS Code theme** entirely.

The gates caught none of it, and that is not a gate failure — they check
packaging, licensing, binary shape and security. None of them opens the panel
and tries to have a conversation. That gap is item 1 of the verification
section at the end.

**Founder decision, 2026-10-11:** one release, `0.2.0`, covering Linux and
Windows together. No interim hotfix. The consequence is recorded and accepted:
`0.1.1 win32-x64` stays live and unusable on the Marketplace until `0.2.0`
ships.

---

## What is actually wrong

### P0-1 — Windows: the client and the daemon derive different workspace tags

**The whole extension is dead on Windows.** Reproduced from a real log on
2026-10-11.

`protocol.WorkspaceTag` (`protocol/protocol.go:101`) is
`sha256(realRoot)[:16]`. It is computed independently in Go and in TypeScript,
and the two must agree byte for byte or the client waits for a lockfile the
daemon never writes.

They do not agree, because they are handed different strings:

| | derivation | result |
|---|---|---|
| extension | `workspaceFolders[0].uri.fsPath` (`clients/vscode/src/extension.ts:68`) → `fs.realpathSync` (`clients/vscode/src/daemonClient.ts:665`) | `c:\Users\shaik\OneDrive\Desktop\catalyst` |
| daemon | `filepath.Abs` → `filepath.EvalSymlinks` (`editapply/workspace.go:14-18`) | `C:\Users\shaik\OneDrive\Desktop\catalyst` |

VS Code's `Uri.fsPath` lower-cases the drive letter; Go's `EvalSymlinks`
upper-cases it. One character.

```
extension  sha256("c:\...") = 03ea217e0c967379  → waits for daemon-03ea217e0c967379.lock
daemon     sha256("C:\...") = 3b707727f6517a4d  → writes daemon-3b707727f6517a4d.lock
                                                 → listens on npipe:...-3b707727f6517a4d
```

Both hashes were confirmed against the user's own daemon log, which printed the
upper-case root and the matching pipe name. The daemon starts perfectly every
time and is unreachable every time; the supervisor then respawns it on a loop.

**Why no test caught it.** `protocol/workspacetag_test.go:30` and the
extension's `workspacetag.test.ts` share three golden vectors and assert both
sides produce *the same hash for the same input*. They do. Nothing compares the
two **canonicalisers** — and all three golden paths are POSIX (`/tmp/x`, `/`,
`/home/user/projects/alpha`), so no Windows-shaped path has ever been through
either.

**Fix - DONE (Stage 1).** `normalizeRoot` (`protocol/protocol.go:120`) now runs
INSIDE `WorkspaceTag`, upper-casing a leading ASCII drive letter, so no call
site can forget it and no new one can reintroduce the bug. It upper-cases
rather than lower-cases deliberately: the daemon already produces `C:\...`, so
every tag it has ever written stays valid and only the extension moves onto it.
Mirrored in `clients/vscode/src/daemonClient.ts`, with the shared golden
vectors extended by the two spellings of one Windows folder.

### P0-2 — Windows: `memory.db` is written to an XDG path

`conversation memory: C:\Users\shaik\.local\state\mochiii\memory.db` — a Unix
XDG state path on Windows. It belongs under `%LOCALAPPDATA%`. Same class as
P0-1: a path helper with no Windows branch.

**DONE (Stage 1).** `stateHome` is now per-platform
(`daemon/statedir_windows.go:32`, `daemon/statedir_unix.go:25`), and the Windows
branch takes `%LOCALAPPDATA%` -- Local rather than Roaming, because `memory.db`
keys conversations by a tag derived from a path on THIS machine, and a profile
sync would carry it to a machine where those folders do not exist.

### P0-3 — No key, and no way to enter one

`clients/vscode/media/main.js` contains **zero** occurrences of "api key",
"set key" or "connect a model". The only two routes in are the command palette
(`mochiii.setApiKey`) and typing `/connect` into the chat — a command the user
has to already know exists.

When the key is missing the user does not even learn that. The daemon logs
`warning: MOCHIII_API_KEY is not set` (`daemon/main.go:191`) and carries on,
sending an unauthenticated request; the panel shows whatever the provider
returns, or a daemon-lifecycle error. The one message a new user reliably sees —

> Could not reach the daemon: daemon not found (expected a lockfile at …)

— is about an implementation detail they have no stake in. **Nobody installing
an extension cares whether a daemon started.** They care that it needs a key
and where to put it.

**DONE (Stage 2).** The panel renders "Mochiii needs an API key to answer" with
a **Connect a model** button, and the daemon decides when to say it
(`HandshakeResponse.NeedsAPIKey`) because only the daemon knows all three
inputs -- it is false in proxy mode and false for a loopback `api_base`, where
nagging would be wrong.

### P0-4 — The model cannot see your code

Two independent causes, both confirmed:

1. `grep -rn "activeTextEditor|visibleTextEditors|tabGroups" clients/vscode/src/`
   found nothing in production code (**CORRECTION:** it returned two hits, both
   in the extension's own `smoke.test.ts`, which reads tab labels. The
   conclusion stands -- nothing shipped sent the open file -- but "0 results"
   was wrong as written). The extension never sent the open file, the
   selection, or the tab list.
2. Agent mode is off, so there are no tools either.
   `agentModeEngaged` (`daemon/agentloop.go:1728` -- **CORRECTION:** the first
   draft said `daemon/server.go`, which is only where it is called) is
   `s.cfg != nil && s.cfg.MCP.Enabled && hs.HasCapability(CapToolApproval)`:
   three conditions, not two. The **shipped**
   `clients/vscode/daemon/models.json` has keys `config_version, default_tier,
   tiers, zdr` — **no `mcp` block at all**, so `MCP.Enabled` is false and
   `read_file`, `search_code` and `repo_map` are never offered.

So a question about the open file is answered from the prompt text alone. The
"Retrieved context 0" the user saw was accurate.

**DONE (Stage 3).** Both halves landed. `models.json` ships an `mcp` block
enabling the read-only confined set (`read_file`, `list_directory`,
`search_code`, `repo_map`, `grep`) with `web_search` and `web_fetch` denied, and
`daemon/shippedconfig_test.go` asserts both that the set is allowed and the
stronger property that nothing non-confined ever is. Separately,
`clients/vscode/src/editorContext.ts:273` prepends the active file, cursor line,
selection and the other tabs' names to the wire prompt -- bounded at every edge,
workspace files only, and it SAYS when it clipped rather than letting the model
conclude a function is absent.

### P0-5 — The panel opens in the wrong place and ignores the theme

`clients/vscode/src/chatPanel.ts:222` calls `vscode.window.createWebviewPanel(..., vscode.ViewColumn.Beside)`
— an **editor tab**. `package.json` contributes **no `views` and no
`viewsContainers`**, so there is no activity-bar icon and no sidebar presence.
Claude Code and Cline both register a `WebviewViewProvider` in a view
container; Mochiii takes over an editor column instead.

**CORRECTION — the styling claim in the first draft was wrong, and wrong in the
direction that wastes work.** It said the stylesheet was "one inline block at
`chatPanel.ts:1488`" containing "`var(--vscode-*)` references: **0**", and was
"a fixed dark palette painted over whatever theme the user chose". None of that
held. That line is a three-line `<style>` wrapper interpolating
`chatPanelStyles()` (`clients/vscode/src/chatPanel.ts:1741`), and that function
already derived every surface from the theme -- 17 `var(--vscode-*)` references
for background, foreground, border, input, popover and code block, with a
`body.vscode-dark, body.vscode-high-contrast` override block carrying its own
WCAG reasoning. The 29 was a count of DISTINCT hex values in the whole file,
most of them `var()` fallbacks or brand colours.

**What was actually wrong** was a handful of exceptions, invisible to review
precisely because everything around them was right: a near-white code-block
gutter (`#c4a4ad` on `#fffafb`) down the left of every code block; `#f3e8ea`
painted on the UNUSED part of the context meter, so on a dark theme the bar read
as nearly full when it was nearly empty; a `#f9f5f6` row hover that flashed
white; a dim `#d4a017` border on the one dialog that must never be skimmed past;
and warn/danger literals bypassing tokens that already had dark-theme
overrides. That is the "brightness" report -- not a missing theme, but a dozen
lines that opted out of one.

The cheap problem itself was real: every message rendered an avatar circle
containing a single letter — `'Y'` for you, `'M'` for Mochiii, `'!'` for
errors — plus a `You` / `Mochiii` role caption. Neither Claude Code nor Cline
labels every turn this way.

**DONE (Stage 5).** The chat registers a sidebar `WebviewViewProvider` in its
own activity-bar container (`clients/vscode/src/chatPanel.ts:255`), with the
editor tab kept as an explicit command for people who prefer a full-width
column. The avatars and captions are gone in favour of a transcript layout,
with the speaker moved to an `aria-label` so the change is not an accessibility
regression. Every literal colour in a style rule is now a theme token, enforced
by `clients/vscode/scripts/theme-check.js` -- a gate that fails any hex in a
declaration while allowing palette definitions and `var()` fallbacks, which is
what makes the distinction mechanical instead of a judgement call.

### P0-6 — Context Usage reports a number that is made up

`clients/vscode/media/main.js:121`:

```js
const tools = 2400; // MCP / agent tool schemas when enabled (approx)
```

Displayed as "Tool definitions" on every turn, including turns where no tools
were sent — which, per P0-4, was every turn. Two more constants beside it (a
1200-token system prompt, 400 tokens of "Rules") and a flat 128000 ceiling for
every model were invented the same way; the default tier's real window is
1024000, so the meter under-reported fullness by eight times.

**DONE (Stage 3).** All five numbers are gone. The daemon already measured the
truth and never sent it to this client: `TurnUsage.context_tokens` and
`context_window` now ride the done message to the panel, which reports the
measured figure against the model's real window and labels it "measured last
turn". Before any turn has run it estimates only what this client is about to
send, and says "estimated".

### P1-1 — Effort is a preference the provider may silently discard

Effort is sent as the OpenAI top-level `reasoning_effort` field
(`daemon/providerdialect.go:114`). When a provider rejects it, the daemon
latches `noReasoningEffort = true` (`:214-216`) and stops sending it **for the
rest of that session**, while the picker keeps offering Low / Medium / High.
The control is real; its effect was invisible and, on several providers, nil.

**DONE (Stage 4).** The latch is reported. `effortDegradation`
(`daemon/degraded.go:62`) asks the dialect memo, at the point a turn resolves
its effort, whether this provider has already refused the field for this model,
and sends `DegradedReasoningEffort` when it has. The panel greys the picker and
says "not supported by this model", keeps the chosen value, and un-greys on a
model change -- the latch is per-model, so carrying it across a switch would be
the same lie in the other direction.

### P1-2 — Modes mostly do not act

`daemon/planmode.go` defines the modes. Without tools (P0-4):

- **Auto / Manual** only decide whether an edit auto-applies.
- **Plan** appends `planModeDirective` (`planmode.go:182`), which instructs the
  model to use `read_file`, `search_code` and `repo_map` — tools it does not
  have. The directive has been checked against the registry by a test since the
  version that shipped naming two tools that never existed; it is now
  self-consistent and still useless while MCP is off.

**DONE (Stage 4).** Stage 3's shipped `mcp` block is what makes plan mode real:
the directive names `read_file`, `search_code` and `repo_map`, and those are now
the tools the turn actually has, while `planModeDenies` keeps execution, network
and subprocess tools out and `planModeWithholdEdits` drops edit blocks the model
emits in prose. Each mode's description in the picker was rewritten to state an
observable effect -- Plan previously said "before editing" when plan mode
proposes no edits at all.

### P1-3 — Slash commands: 27 exist, most were prompt prefixes

**CORRECTION:** the first draft said 25 (12 local + 13 steered) and then listed
14 names under the label "13". The catalog at
`clients/vscode/src/slashCommands.ts:43` defined **27**: 12 `local` and 15
`steered`, the omitted name being `research`.

`SLASH_CATALOG` has two kinds (`:4`): `local` (really implemented) and `steered`
(**a prompt prefix and nothing more**).

- `local` (12): `help, model, mcp-server, clear, compact, context, git, init,
  search, spec, connect, exit`
- `steered` (15): `team, explain, fix, test, refactor, doc, security, review,
  plan, run, implement, debug, explore, research, reason`

Against the agreed Claude Code-shaped set, these were **missing entirely**:
`diff, export, copy, effort, mode, memory, config, permissions, agents,
resume, rename, cost, usage, status, doctor, theme, vim, index`.

A second defect sat beside it: `media/main.js` carried its OWN transcribed copy
of the catalog -- 24 entries against the real 27 -- so `/spec`, `/connect` and
`/team` existed and could not be discovered from the menu. Nothing could notice,
because the two lists had no relationship a compiler or a test could see.

**DONE (Stage 6).** Ten real `local` commands added: `diff`, `copy`, `export`,
`effort`, `mode`, `cost`, `usage`, `status`, `doctor`, `config`. Each is
dispatched by `runLocalCommand` and therefore driven through a maximally hostile
workspace by `localCommandsHostile.test.ts`, which is the condition for adding
one rather than a line of steering text; `/diff` reuses `safeGitArgs`, because
`diff.external` names a program git runs INSTEAD of diffing. The webview's
duplicate list is deleted -- the host posts `SLASH_CATALOG` itself. Autocomplete
matches substrings and summaries rather than name prefixes only, and completes a
no-argument command without a trailing space. Not built, for want of anything to
point them at: `agents`, `vim`, `theme` (VS Code owns the last two), `login`,
`upgrade`, `stickers`.

### P1-4 — MCP is unreachable, and there is no marketplace

Beyond P0-4's config gate: `clients/vscode/src/mcpServerList.ts` is **read-only** — it shells
out to `mcp list` to display configured servers. There is no add, edit, enable
or disable. A user who wants a server must find
`mochiii.configPath`, author a `models.json` by hand, and restart the daemon.
There is **no server catalog anywhere in the repository**.

---

## The work, in order

Each stage lands as its own commit with its own tests. Nothing here is a
rewrite: the daemon's protocol, retrieval, safety gates and agent loop stay as
they are.

**Status, 2026-10-13: Stages 1-6 are implemented; Stage 7 is implemented on
`feat/mcp-marketplace`, stacked on the 0.2.0 branch (see its entry for what is
still open).** The
extension type-checks, the webview and theme gates pass, and the Go suite is
green. Two things remain before a tag, both listed in Verification below: the
fresh-profile walkthrough on Windows and Linux (item 1, which no gate can do),
and the version bump with its changelog entry -- `package.json` still says
`0.1.1`, and `extensionGuidelines.test.ts` requires a CHANGELOG section for
whatever version it says.

### Stage 1 — Make it work at all (P0-1, P0-2) — **DONE**

Windows reaches its daemon. Drive-letter normalisation inside both
`WorkspaceTag` implementations, a Windows golden vector, a cross-canonicaliser
test, and `%LOCALAPPDATA%` for `memory.db`.

**Done when:** a Windows machine opens a folder, sends a prompt and gets an
answer.

### Stage 2 — Tell the truth about setup (P0-3) — **DONE**

- Replace the daemon-lockfile error with a first-class state: **"Mochiii needs
  an API key to answer."** with a **Connect a model** button inline in the
  panel.
- A real first-run empty state in the chat, not a wall of prose.
- Detect the provider from the key's shape (`sk-ant-`, `gsk_`, `sk-or-`,
  `nvapi-`, …) and confirm which provider was recognised, extending the
  provider table already in `clients/vscode/src/connectFlow.ts:46`.
- Show the live provider and model in the model chip, so "which key am I using"
  is answerable at a glance.
- Keep daemon-lifecycle errors, but behind **Show details** — they are for
  debugging, not for first contact.

**Done when:** a fresh profile with no key can go from install to a working
answer without the command palette, the docs, or a terminal.

### Stage 3 — Let it see the code (P0-4, P0-6) — **DONE**

- Send the active file, selection and open tabs with every prompt.
- Ship an `mcp` block in `models.json` enabling the **read-only** tools
  (`read_file`, `search_code`, `repo_map`) by default, confined to the
  workspace. Execution and network stay off and keep their approval flow.
- Make Context Usage report what was actually sent; delete `const tools = 2400`.

**Done when:** "what does the file I have open do?" is answered correctly, and
Context Usage reconciles with the daemon's own accounting.

### Stage 4 — Make the controls real (P1-1, P1-2) — **DONE**

- Map effort onto each provider's own setting; when a provider has none, grey
  the control and say **"not supported by this model"** rather than pretending.
- Surface the `noReasoningEffort` latch in the UI instead of hiding it.
- With Stage 3's tools present, Plan / Auto / Manual become behavioural rather
  than decorative. Align the mode set and naming with Claude Code's.

**Done when:** every mode and effort level produces an observable difference,
or says why it cannot.

### Stage 5 — The panel people look at (P0-5) — **DONE**

- Move the chat to a **sidebar `WebviewViewProvider`** in its own view
  container with an activity-bar icon, matching Claude Code and Cline. Keep
  "open in editor tab" as an explicit command for those who want it.
- Replace all 29 hardcoded colours with `--vscode-*` theme variables so the
  panel inherits the user's theme in light, dark and high-contrast.
- Drop the per-message avatar letters and role captions for a layout that reads
  as a transcript.
- Rebuild the input box: proper focus states, send affordance, attachment
  control, and the model/effort/mode row.

**Done when:** screenshots in three themes are indistinguishable in quality
from Cline's panel, and nothing is hardcoded.

### Stage 6 — Slash commands (P1-3) — **DONE**

Implement the agreed subset — the Claude Code commands that have a Mochiii
meaning — with a real autocomplete menu on `/`. Convert the `steered` entries
that deserve to be real (`diff`, `review`, `security`) into implemented
commands.

Explicitly **not** built, for want of anything to point them at: `stickers`,
`passes`, `upgrade`, `login`, `install-slack-app`, `desktop`, `mobile`,
`remote-control`, `chrome`, `feedback`.

**Done when:** every command in `/help` does what it says.

### Stage 7 — MCP and a server catalog (P1-4) — **DONE, on `feat/mcp-marketplace`**

- A real MCP management UI: add, edit, enable, disable, and the unconfined
  acknowledgement that `MCP_LANE_B_THREAT_MODEL.md` requires.
- A curated server catalog, installable in one click, each entry declaring the
  credentials it needs.

**Read `docs/MCP_LANE_B_THREAT_MODEL.md` before starting this stage.** Lane B
servers are unconfined by design; a one-click install is a one-click grant of
that, and the acknowledgement is the control.

**Done when:** a user can add a server from the catalog and call its tools
without editing JSON.

**What landed.**

- **The daemon** (`daemon/mcp_handler.go`). `MCPRequest` gained `update`
  (a new grant, behind the same acknowledgement as `add`; drops per-tool
  policies written for the old program), `enable` / `disable` (pause without
  forgetting; the registry already skipped a disabled server) and `servers`
  (the configured servers WITHOUT starting any, for a view that redraws).
  `MCPServerInfo` carries a server's parts; `FeatureMCPManage` advertises it.
  The CLI and the terminal's `/mcp-server` gained `enable` and `disable`.
- **The "MCP Servers" view** (`clients/vscode/src/mcpServersView.ts`): a
  native tree in the Mochiii sidebar beside the chat. Add, change, pause,
  resume, remove; a config-file server is shown but not editable here.
- **The marketplace** (`clients/vscode/src/mcpMarketplace.ts`,
  `media/marketplace.js`), opened by "Browse for more" behind a one-time
  notice. Its catalog is mochiii.in's (`/api/mcp/v1/catalog.json`, built from
  the official MCP Registry plus a reviewed verified list), fetched and
  checked by the extension host (`mcpCatalog.ts`) -- the webview never holds
  it, has no `connect-src`, and "Add" carries only a server's name.
- **One add flow for both** (`mcpInstall.ts` `storeServer`): the daemon's own
  warning, then the server's name typed -- the same two steps as the terminal.
- **mochiii.in's "Open in VS Code"** (`handleMarketplaceUri`): a
  `vscode://` link that names a server; the marketplace shows it, and never
  installs from a link.

**From the first walkthrough (2026-10-13).**

- **`/mcp-server` in the chat opens the marketplace** (`/mcp-server <words>`
  searches it) instead of printing `mcp list`'s table into the transcript. Its
  shell-out, `mcpServerList.ts`, is gone: it started every configured server
  to ask it and printed their stderr (`RESIDUAL_RISKS.md` R1.5). The tool
  table is **Mochiii: Show MCP Tools**, which asks the running daemon.
- **Mochiii's container moved to the secondary side bar.** In the activity bar
  it shared the primary side bar with the Explorer, which shows one container
  at a time: opening the files hid the chat and opening the chat hid the
  files. A VS Code too old for `secondarySidebar` puts the views in the
  Explorer.
- The marketplace no longer scrolls sideways when the editor is narrower than
  one card.

**Still open.** The optional mochiii.in sign-in agreed for the marketplace is
not built: the site has no account backend yet. And a server's token reaches
it only from the environment VS Code was started in -- the daemon reads no
`.env` -- which a GUI-launched editor rarely has; keeping server tokens in the
OS keychain and handing them over the way the model key is (stdin) is a
security-model decision, not yet taken.

### Stage 8 — the chat's pages, and a quieter transcript — **on `feat/chat-pages`**

From the second walkthrough (2026-10-13): tool lines filled the side bar and
never said why a call failed; a two-folder workspace failed silently; a rate
limit offered nothing to click; `/model` and `/mcp` printed into the chat;
there was no account or settings screen; and the user asked for Cline's
shape -- header buttons, pages, a composer toolbar -- in Mochiii's own design.

**What landed.**

- **Why a tool failed** (`daemon/agentloop.go` `toolFailureReason`): the
  first line of the same scrubbed text the model reads, capped at 160 runes.
- **The work block** (`media/main.js` `showToolActivity`): a turn's calls fold
  into one line ("Used 3 tools · 1 failed") that opens to a row per call in
  words. `mochiii.toolActivity` sets whether it starts open.
- **A failed turn's next step**: Try again (a retry replaces the unanswered
  prompt), Change key, Compact the chat, Continue -- chosen from the daemon's
  `error_class`, never from prose.
- **Provider notices once** (`degradedOnce.ts`): the per-turn
  `provider_routing` line is shown the first time, then kept for Account.
- **The working folder** (`workingFolder.ts`, **Mochiii: Choose Working
  Folder**): chosen among the open folders, kept in the extension's
  per-workspace storage -- not a setting a repository could write.
- **Pages** (`src/chatPages/`, `media/pages.js`): **Customize** (MCP Installed
  and Marketplace, Specs, Tools, Skills-to-come), **Account** (provider, the
  key in force and its source, model, spend; sign-in placeholder) and
  **Settings** (Model, Behaviour, Workspace, Privacy, Advanced, About). A page
  names entries in host-side tables (`rules.ts`); the machine-scoped settings
  are shown and never written from a page.
- **The marketplace inside the chat**: `marketplace.js` is a mount factory and
  `MarketplaceCore` its host logic, shared with the editor tab; the same
  notice, and every Add through `storeServer`.
- **Screen commands**: `/mcp`, `/mcp-server [search]`, `/account`, `/config`
  open their page and a bare `/model` the model menu; nothing is printed.
- **The composer**: **@** adds a file, the selection, a file's problems or an
  open file as an attachment chip (typing `@` at a word start does the same);
  the plug opens the MCP servers and shows how many are on.
- **Daemon**: MCP action `tools` (`FeatureMCPTools`) lists the built-in tools
  and their policy without starting any third-party server.

**Still open.** Skills (Stage 9): user skills in `~/.mochiii/skills/`,
repository skills off until turned on, a read-only `load_skill` tool, and a
skills catalog on mochiii.in. The mochiii.in sign-in behind Account's
placeholder. Tool policies are shown, not yet edited, from the Tools tab.

---

## Verification

The gate this release exists to close: **nobody had ever used the product
before shipping it.**

1. **A fresh-profile end-to-end test, run before the tag.** New VS Code
   profile, no key, no `~/.mochiii`. Install the `.vsix`. Open a folder. Ask
   about the open file. Enter a key through the panel. Ask again. Switch each
   effort level. Switch each mode. Run every `/help` command. **On Windows and
   on Linux.** Until this is automated, it is a written checklist someone
   performs and signs.
2. `protocol/workspacetag_test.go` gains a Windows golden vector and a
   cross-canonicaliser agreement test — the specific gap that let P0-1 ship.
3. Theme snapshot tests in light, dark and high-contrast; a lint that fails any
   hex colour in the webview stylesheet.
4. A test asserting the shipped `models.json` enables the read-only tool set,
   so P0-4 cannot regress silently.
5. The existing package gate, security gates and `make check` continue to pass
   unchanged.

**Where these stand, 2026-10-13.** Items 2, 3 and 4 are done: the Windows golden
vectors and the cross-canonicaliser agreement test are in
`protocol/workspacetag_test.go` and its TypeScript mirror; the hex lint is
`clients/vscode/scripts/theme-check.js`, wired into `pretest` and
`.github/workflows/build.yml`, and it was verified by reintroducing a literal on
each side and watching it fail; and `daemon/shippedconfig_test.go` asserts the
shipped config's tool policy in both directions. Item 5 holds for everything
runnable here.

**Item 1 is not done, and it is the one this release exists for.** It needs two
real machines and a human; nothing above substitutes for it. A theme snapshot in
three themes (part of item 3) is also still manual -- the lint proves the panel
ASKS the theme, not that the answer is legible, and it says so in its own
output.

---

## Scope notes

- **Nothing in the daemon's safety model is relaxed.** Execution and network
  tools keep their approval flow; Lane B keeps its acknowledgement; the webview
  CSP ceiling only goes down.
- `0.1.1 win32-x64` remains published and unusable until `0.2.0`. Unpublishing
  it is a one-line action if that becomes preferable.
- Stages 1–3 are what make the product work. Stages 4–7 are what make it good.
  They are ordered so that an early cut still ships something honest.
