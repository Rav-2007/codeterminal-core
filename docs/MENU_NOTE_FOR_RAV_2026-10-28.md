Decision 2026-10-28: keep cap 24; see PR #76.

# The MCP tool menu: a decision for Rav-2007 — 2026-10-28

DRAFT, not sent. Measured on main **c338f7c** (after #73, #74 and #75). The method and the raw
numbers are in `docs/MCP_MENU_DECISION_2026-10-28.md`; this note is meant to be enough on its own.
CONFIRMED = measured here. NOT RUN = not measured.

## 1. What changed since 12 Oct

On 12 Oct the menu cap went to **24** so that MCP servers a user adds would get **13 places**,
because the daemon's own tools took 11. Five built-ins arrived on 14–15 Oct: `delete_path`,
`move_path` and `restore_deleted` (4df655c), `run_command` (772102a) and `find_files` (c496981).
The daemon's own tools now take **16–18** of the 24 on an ordinary turn. So an added server gets
**6–8** places, not 13. A second server the request does not name gets **0**, because the free
places go to the server that ranks first. The built-ins alone are **~12 KB of tool JSON on every
request**. One more detail: `find_tools` is added *after* the cap, so a menu that had anything cut
sends **25** tools.

## 2. What each mode leaves for added servers (CONFIRMED, cap 24 in every mode)

| mode | built-ins (no index / indexed) | + GitHub set up | places for added servers | built-in tool JSON |
|---|---|---|---|---|
| ordinary chat | 16 / 17 | 17 / 18 | **8 / 7** (7 / 6 with GitHub) | 12,016 / 12,432 B |
| build | 16 / 17 | 17 / 18 | 8 / 7 (7 / 6) | 11,898 / 12,265 B |
| long tasks (task, debug, fix, refactor, hunt) | 18 / 19 | 18 / 19 | **6 / 5** | 13,255 / 13,671 B |
| plan / check / spec | 5–6 / 7–8 / 6–7 | same | none: added servers are not connected | — |

With one oversized server (redis 53 tools, github 46), it gets every free place whether or not the
request names it. Which tools it gets follows the request: for example, "keys" brings `scan_keys`.
#75 changed no menu: the tool lists and bytes on c338f7c match 24c9274 exactly (CONFIRMED).

## 3. Measured accuracy (CONFIRMED, run 2026-10-28)

The real first request of an ordinary turn, built by the turn's own functions, with github's 46
tools attached. Model `nvidia/nemotron-3-ultra-550b-a55b`, 17 prompts × 5 trials per arm,
255 calls, 0 errors. **github's tools are names only** (no descriptions or schemas), so its column
is not accuracy on the real server. Scored on the first tool call.

| arm | built-in prompts | github prompts | tool JSON / request |
|---|---|---|---|
| today, cap 24 (8 github on the menu) | 51/55 | 24/30 | 13.5 KB |
| cap 64 (all 46 github, nothing cut) | 53/55 | **19/30** | 17.3 KB |
| github limited to 8 chosen tools, rest denied | 51/55 | **13/30** | 12.9 KB |

**Every miss** (expected → chosen ×count):

- **cap 24:**
  - `run_command` → `repo_map` ×2, `list_directory` ×1, `github__list_commits` ×1
  - open PRs → `run_command` ×1
  - comment → `find_tools` ×1, `github__issue_write` ×1
  - commits → `run_command` ×3
- **cap 64:**
  - `run_command` → `find_files` ×1, `list_directory` ×1
  - open PRs → `run_command` ×5
  - commits → `run_command` ×4, no tool ×1
  - merge → `github__list_pull_requests` ×1
- **subset of 8:**
  - `run_command` → `list_directory`, `sandbox_exec`, `github__list_commits` ×2
  - open PRs → `run_command` ×4
  - branch → `run_command` ×4, no tool ×1 (`create_branch` not in the subset)
  - commits → `run_command` ×3
  - merge → `github__pull_request_read` ×5 (`merge_pull_request` not in the subset)

At 5 trials a prompt, a gap of a few trials is a direction, not a measurement. **The 12 Oct curve
(100 / 94.3 / 91.4% at 12 / 24 / 32) is not comparable**: a different model (which this key no
longer serves), fixture tools (its 24-tool menu was 6,976 B against 13.5 KB here) and 7 fixture
prompts. Caps between 24 and 64 were not measured.

## 4. The options

**A. Keep 24 and accept 6–8 places.**
- **Changes:** nothing.
- **Files:** none, apart from the doc fixes in §5.
- **Measured:** 51/55 built-in and 24/30 github.
- **Token cost:** as today, 13.5 KB per request with github attached.
- **Does not fix:** a second server unnamed in the request gets 0; tools beyond the first 6–8 cost a
  `find_tools` round trip.
- **Already in place:** users see each server's whole count on its card and in `/mcp-server`
  (#75), and the model is told it (0e7f920).
- **Reversible:** n/a.

**B. Raise the cap.**
- **Changes:** to restore 13 places it would need **29** (chat, no index), **30** (indexed),
  **31** (indexed with GitHub) and **32** (long task, indexed). Long tasks use the same default
  (`defaultTaskMaxAdvertisedTools = defaultMaxAdvertisedTools`), so one value covers all: 32.
- **Files:** `daemon/mcpconfig.go` (constant and comment), `docs/REFERENCE.md`.
- **Measured at 29–32: NOT RUN.** Only 24 and 64 were measured. At 64: built-in 53/55, github
  19/30.
- **Token cost:** every extra place carries a server's real schema. Names-only it came to
  +3.8 KB at 62 tools; real schemas were not measured.
- **Does not fix:** a server with more tools than the cap (redis 53, github 46) still loses some.
  Whenever anything is cut, the menu order changes, and the provider's prompt cache keys on it.
- **Reversible:** yes (one constant, and the config knob stays).

**G. Trim which built-ins appear on an ordinary chat turn.**
- **Changes:** some built-ins leave the ordinary menu, either reachable through `find_tools` or
  only in the modes that still list them.
- **Files:** `daemon/mcpbuiltin.go` (`builtinTools`), `fileops.go` / `runcommand.go`, and
  `models.agent.json` if done by policy instead.
- **Measured:** NOT RUN.
- **Token cost:** saves each removed tool's schema on every request.
- **Does not fix:** a tool moved off the menu costs a `find_tools` call the first time it is needed.
- **Reversible:** yes.

Usage evidence is thin. The only per-tool record in the repo is `docs/agent_workflow_eval.jsonl`:
523 trials of a coding-task eval, 27 Sep – 4 Oct, mostly deepseek models. It is not user
telemetry, and **it predates the five new tools**. No issue, fixture or log in the repo records
real usage. The daemon's local audit log is per machine and not in the repo.

| tool | what it is for | calls (trials using it) in that eval | modes that keep it |
|---|---|---|---|
| read_file | read a text file | 3,246 (406/523) | every mode |
| propose_edit | edit or create a file, as a reviewed diff | 1,000 (319) | all but plan, check |
| repo_map | the shape of the workspace | 479 (371) | every mode |
| sandbox_exec | a confined build or test | 437 (227) | chat, build, long tasks, check |
| list_directory | list a folder | 412 (167) | every mode |
| propose_ast_edit | edit by symbol (AST) | 48 (34) | chat, build |
| query_compiler_references | language-server references | 28 (27) | chat, build, long tasks |
| query_compiler_definition | language-server definition | 8 (8) | chat, build |
| web_fetch | read one URL | 1 (1) | chat, build |
| web_search | search the web | 0 (tasks needed no web) | chat, build |
| search_code | search by meaning (indexed only) | 0 (no index in the task projects) | every mode, when indexed |
| github | read this project's GitHub repo | 0 (not set up in the eval) | chat, build |
| grep | exact or regex text search | 0 (added 1 Oct; on the menu for at most part of the window) | every mode except build |
| find_files | find files by name | no data (added 15 Oct) | every mode except build |
| run_command | a command in the user's own terminal | no data (added 14 Oct) | chat, build |
| delete_path | delete a file or folder | no data (added 14 Oct) | chat, build, long tasks |
| move_path | move or rename | no data (added 14 Oct) | chat, build, long tasks |
| restore_deleted | undo a delete_path | no data (added 14 Oct) | chat, build, long tasks |

**C. A curated subset for github** (the rest denied).
- **Measured: 13/30**, worse than today's 24/30. 10 of its misses are the two tools the chosen
  subset left out, and a denied tool is not reachable through `find_tools`.
- **Changes:** a catalog field naming the kept tools, carried into the existing per-server policy
  (`MCPServerConfig.Tools`).
- **Files:**
  - the field in all three validators: `scripts/mcp/catalog-rules.mjs` and `catalog.schema.json`
    (mochiii_website), `clients/vscode/src/mcpCatalog.ts`, and daemon `validateUserServer`
    (`mcpuser.go`);
  - the add request: a field on `protocol.MCPRequest` (`protocol/protocol.go`), copied into
    `MCPServerConfig.Tools` in `daemon/mcp_handler.go`, and sent by
    `clients/vscode/src/daemonClient.ts` and `mcpInstall.ts`;
  - a chosen subset for each oversized server.
- **Token cost:** −0.6 KB per request against today.
- **Does not fix:** a server a user adds by hand.
- **Reversible:** per entry yes; the protocol field stays.

**The 25-tool menu.**
- **(a) Count `find_tools` inside the cap:**
  - **Changes:** a cut menu becomes 23 tools + `find_tools` = 24.
  - **Effect:** one place fewer for added servers (5–7 on chat).
  - **Files:** `daemon/agentloop.go` and `daemon/mcp/registry.go`.
  - **Measured:** NOT RUN.
- **(b) Document it:**
  - **Changes:** "24, plus find_tools when something is left out".
  - **Effect:** no behaviour change.
  - **Files:** the `mcpconfig.go` comment and `REFERENCE.md`.
- Both are reversible.

## 5. Doc lines to fix once you decide

- `daemon/mcpconfig.go:605`, `:616` and `:617` ("take 11 places", "thirteen places", "eleven tools")
- `docs/REFERENCE.md:1132-1133` ("those take 11 … share 13 places")
- `daemon/mcp/relevance.go:180` ("left TWO places", a dated 15 Oct observation)

## 6. The question

**Which option (A, B, G, C, or a combination), and what should the cap be?**
