# MCP tool menu: what to do about servers with more tools than fit — 2026-10-28

**DRAFT, uncommitted. A decision for the owner; nothing here is implemented.**

**Short version.** The premise behind the cap of 24 no longer holds. The owner chose 24 on
2026-10-12 as "thirteen places for added servers", because the daemon's own tools took 11.
They now take **16 to 18** on an ordinary turn, so an added server gets **6 to 8** places, not 13.
Five built-ins arrived after that decision. Two other things in the brief are also out of
date: a request naming its server almost never changes the menu, and the model is already
told a server's full tool count.

Verification words: **CONFIRMED** (measured or run here), **PLAUSIBLE** (read in the code or the
history, not run), **NOT RUN**.

---

## 1. Measured facts

### 1.1 The daemon's own tools take 16–18 places, not 11 — CONFIRMED

Method: a throwaway test in `daemon/` builds each mode's menu the way a turn does. It loads the
shipped `models.agent.json` (byte-identical to the embedded
`daemon/builtinconfig/models.agent.json.gz`, CONFIRMED with `cmp`), calls `builtinRegistry(cfg, …, mode)`,
then `advertisedToolSpecsFor` (the cap, relevance ranking and named-server reserve), and adds
`find_tools` when the cap left anything out, as `runAgentLoop` does. The shipped `pipeline` list is
empty, so an ordinary turn has no specialist role. Cap = `resolvedMaxAdvertisedToolsFor(mode)` = 24 in
every mode.

| mode | built-ins (search-by-meaning off / on) | + GitHub set up for the project | places left for added servers | added servers connected? |
|---|---|---|---|---|
| ordinary chat | **16 / 17** | 17 / 18 | **8 / 7** (7 / 6 with GitHub) | yes |
| build | 16 / 17 | 17 / 18 | 8 / 7 (7 / 6) | yes |
| task, debug, fix, refactor, hunt (long tasks) | **18 / 19** | 18 / 19 (github is withheld, like the web tools) | **6 / 5** | yes |
| plan | 5 / 6 | same | — | **no** (`modeWithholdsLaneB`) |
| check | 7 / 8 | same | — | **no** |
| spec | 6 / 7 | same | — | **no** |

Ordinary chat, search-by-meaning on, in menu order: `delete_path, find_files, grep, list_directory,
move_path, propose_ast_edit, propose_edit, query_compiler_definition, query_compiler_references,
read_file, repo_map, restore_deleted, run_command, sandbox_exec, search_code, web_fetch, web_search`
(17), plus `github` when GitHub is set up for the project.

**Why 11 became 16 (PLAUSIBLE, from history).** On 2026-10-12 (5c8ada2, the commit that chose 24) an
ordinary turn registered `read_file, list_directory, query_compiler_definition,
query_compiler_references, sandbox_exec, web_search, web_fetch, repo_map, grep, propose_edit,
propose_ast_edit`: 11 without search-by-meaning, matching the docs. Then 4df655c (2026-10-14) added
`delete_path`, `move_path` and `restore_deleted`, 772102a (2026-10-14) added `run_command`, and
c496981 (2026-10-15) added `find_files`. The 11 was read from the code at 5c8ada2, not run there.

**The real built-ins alone are ~12 KB of tool JSON per request — CONFIRMED.** Chat 12,016 B
(16 tools), build 11,898 B, long task 13,255 B. With search-by-meaning: 12,432 / 12,265 / 13,671 B.
That is **more than the whole 24-tool menu in the size eval (6,976 B)**. The eval's menus were
fixture tools, so its byte column understates the real request. Bytes are not tokens; tokens are
NOT RUN.

### 1.2 One oversized server: the leftover places, named or not — CONFIRMED

redis (53 tool names) and github (46) from `mochiii_website/mcp/handshakes.json`, attached as an
added server. The query was either the request naming the server ("use redis to list the keys
that match user:*"), the same request without the name, or an unrelated one ("fix the failing
test in the editapply package").

| mode, search-by-meaning | redis on the menu (named / unnamed / unrelated) | github (same) | tools sent |
|---|---|---|---|
| chat, off | 8 / 8 / 8 | 8 / 8 / 8 | 25 (24 + `find_tools`) |
| chat, on | 7 / 7 / 7 | 7 / 7 / 7 | 25 |
| chat, on, GitHub set up | 6 / 6 / 6 | 6 / 6 / 6 | 25 |
| build | same as chat | same as chat | 25 |
| long task, off | 6 / 6 / 6 | 6 / 6 / 6 | 25 |
| long task, on | **6 / 5 / 5** | **6 / 5 / 5** | 25 |

- **Naming the server changes the count in exactly one cell**: a long task with search-by-meaning on,
  where only 5 places are free and the reserve of 6 displaces one built-in. Everywhere else the free
  places (6–8) already cover the reserve (`withNamedServerReserve`: "room the built-ins already
  leave counts toward the reserve"), so naming does nothing.
- **Which tools are kept does follow the request**: for "keys" redis gets `scan_keys`,
  `scan_all_keys`, `get_indexed_keys_number`, …; for "pull requests" github gets `list_pull_requests`,
  `list_issues`, …. For an unrelated request it gets the alphabetically first names (`client_list`,
  `create_vector_index_hash`, `dbsize`, `delete`, …). The records hold **names only**, so ranking ran
  on names alone. Real descriptions could rank differently: PLAUSIBLE.
- Every menu with a server cut is **25 tools**: `find_tools` is added after the cap.

### 1.3 Two servers: the one not named gets nothing — CONFIRMED

redis and github both attached, ordinary chat, search-by-meaning off (8 free places):

| request | redis | github |
|---|---|---|
| "use redis to list the keys that match user:*" | 8 | **0** |
| "use github to list the open pull requests" | **0** | 8 |
| "list the keys that match user:* and the open pull requests" (names neither) | **0** | 8 |
| "fix the failing test in the editapply package" | **0** | 8 |

When neither server is named, github takes every place even for a request about keys. With names
only, the relevance tie falls to alphabetical order (PLAUSIBLE).

### 1.4 The model is already told the full count — CONFIRMED

0e7f920 (2026-10-15) fixed the "Supabase has 2 tools" failure at its source. The agent-setup
section lists every tool a server has, not only the menu's. Measured for redis this turn:

> redis: running, with 53 tools. On your tool list now: client_list, get_indexed_keys_number,
> scan_all_keys, scan_keys, create_vector_index_hash, dbsize, delete, expire. Also available, not
> on your list until you call find_tools with what you need: get, get_index_info, … and 5 more.

An `evidenceRule` also tells the model to look a thing up (with `find_tools` if needed) before
saying it does not exist. Whether the model now *behaves* correctly is a live-model question:
NOT RUN. The user already sees the full count too: `/mcp-server` shows "✔ Connected (N tools)"
(`daemon/mcpstatus.go:41`, `MCPServerInfo.ToolCount`). That was read in the code, not run.

### 1.5 Confirmed as stated in the brief

`defaultMaxAdvertisedTools = 24`, ceiling 64 (`daemon/mcpconfig.go:643-644`).
`namedServerReserve = 6`, capped at cap/4 (`daemon/mcp/relevance.go:191, 206-209`). Policy-denied
tools are skipped before the cap (`registry.go:304, 323`). `find_tools` adds at most 5 per call and
15 per turn (`toolsearch.go:52, 55`). The menu is ranked only when something is cut, otherwise
alphabetical (`registry.go:331-337`). All read in the code, and the cap and reserve behaviour was
seen in the runs above.

### 1.6 Accuracy — Step 2 NOT RUN

**Why not.** The arms specified were (a) as today with the reserve, (b) the request naming github,
and (c) not naming it. §1.2 shows they send **the same menu**: github gets the same 8 (or 7, 6)
places, with the same names when the request is the same apart from the server's name. A billed run
comparing three identical menus measures noise. In addition:

- The existing harness (`TestToolMenuSizeCurve`, `-tags eval`) builds a fixture menu, not the
  production one. It reads only `MOCHIII_API_BASE`/`MOCHIII_API_KEY` from the environment, and both
  are unset here, so it would skip.
- github's real schemas and descriptions are not in `handshakes.json` (names only). Getting them
  means starting `@modelcontextprotocol/server-github` (or its successor) with a GitHub token and
  recording `tools/list`.

**What would be worth running instead** (needs the owner's go-ahead, because it is billed). Use the
production-menu harness (`TestProductionMenuSelection`, `prodmenu_eval_test.go`). It sends the real
built-ins, and it can use the stored credential at `~/.mochiii/credentials.json`, which exists on
this machine. Arms:

1. today: 16 built-ins + 8 github;
2. option D: 16 built-ins + all 46 github (cap 64);
3. option C: 16 built-ins + a curated github subset.

Use real github schemas, the existing seven prompts plus github prompts, 5 trials a cell (or 20 to
see a 1-in-10 effect), and the model the stored key serves. Report accuracy per cell with n, and
tool bytes per request.

---

## 2. The options, against these numbers

The baseline for every option is the real one: an added server has **6–8 places** on an ordinary
turn, **5–6** in a long task, and **none** in plan, check or spec. Sizes are estimates.

### A. Ship as is

- **Today, measured:** one oversized server gets 6–8 tools, chosen by the request. The rest are one
  `find_tools` call away, and the model is told they exist (§1.4). With two servers, the one not
  named can get **none** (§1.3).
- **"The menu reported as full capability" failure:** the information fix shipped on 2026-10-15
  (§1.4). Whether it recurs is model behaviour: NOT RUN, so the brief's "recurs" is PLAUSIBLE at
  most.
- **Files / tests:** none. **Size:** zero. **Reversible:** n/a.
- **Does not fix:** a wide server's tools beyond the first 6–8 cost a `find_tools` round trip; a
  second server can be starved entirely.

### B. Drop or demote the six oversized servers from verified

- Verified falls 17 → 11, below the owner's 12–20 decision, and loses github, postgres and
  playwright. **Unchanged by Step 1.**
- **Files:** `mochiii_website/mcp/curated.json` and `handshakes.json`, then rebuild
  `public/api/mcp/v1/catalog.json` and `community.json`. Website repo only; no daemon change.
- **Tests:** `scripts/mcp/catalog.test.mjs`: an entry over the menu size is refused from the verified
  tier. Neuter check: remove the rule and that test fails.
- **Size:** small. **Reversible:** yes (catalog data).
- **Does not fix:** a user can still add redis themselves and get 6–8 places.

### C. A curated tool subset per catalog entry, the rest denied

- Denied tools skip the cap (§1.5), so a subset of ≤ 6–8 fits whole. **Correction to the brief:** the
  daemon already has the mechanism. `MCPServerConfig.Tools` maps a tool name to deny, ask or allow,
  and `mcpuser.go:178` validates it. What is missing is the path from the catalog to it: the add
  request (`protocol.MCPRequest`) has no tools field (CONFIRMED).
- **Files:**
  - `scripts/mcp/catalog-rules.mjs` (mochiii_website), `clients/vscode/src/mcpCatalog.ts` and
    daemon `validateUserServer`: the three validators, which must agree, as with `icon` (#73 and
    mochiii_website#5);
  - `protocol/protocol.go` (a tools field on `MCPRequest`), `daemon/mcp_handler.go` (copy it into
    `MCPServerConfig.Tools`), `clients/vscode/src/daemonClient.ts` and
    `clients/vscode/src/mcpInstall.ts`;
  - the card's "chosen and reviewed" wording becomes "a chosen subset of N of M tools";
  - six subsets for someone to choose.
- **Tests:**
  - parity: all three validators accept and refuse the same subsets;
  - add a server with a subset: only those tools are advertised and the rest are absent from
    `find_tools`;
  - neuter check: drop the protocol field and the advertise test fails.
- **Size:** medium. It spans three components and two repos, plus the curation work.
- **Does not fix:** a server the user adds by hand. It also hides the denied tools from
  `find_tools`, which is a capability loss unless the policy is "ask" rather than "deny", and only
  deny skips the cap.
- **Reversible:** yes per entry; the protocol field stays.

### D. Raise the cap

- **Restated cost:** the built-ins alone are 12 KB per request (§1.1), so every place added is paid
  on top of that on every iteration. To fit github whole on an ordinary turn the cap would need
  16 + 46 = 62. For redis it would be 69, past the 64 ceiling. The accuracy curve was measured only
  to 32, on fixture tools.
- **Files:** `daemon/mcpconfig.go` (the constant and its comment) and `docs/REFERENCE.md`.
- **Tests:** update the existing default test. A neuter check is meaningless for a constant; what
  matters is the eval in §1.6.
- **Size:** one line. **Reversible:** yes.
- **Does not fix:** prompt-cache stability once anything is cut, or the 32-to-64 accuracy unknown.

### E. A bigger named-server reserve

- **Restated against Step 1:** the reserve is a **no-op for one server on an ordinary turn**, because
  the 6–8 free places already exceed it (§1.2). A bigger reserve only acts when it exceeds the free
  places, and then it displaces built-ins. For example, a reserve of 12 on chat would push 4–6
  built-ins to `find_tools`. It is capped at cap/4 = 6 today, so raising it also means changing that
  cap. It does help the two-server case (§1.3) when the user names a server, and does nothing when
  they do not.
- **Files:** `daemon/mcp/relevance.go` (the constant and the cap/4 rule) and its comment.
- **Tests:** `relevance_test.go`: with a named server and N free places below the reserve, the named
  server gets the reserve and the displaced built-ins are in `DroppedTools`. Neuter check: restore 6
  and the test fails.
- **Size:** small. **Reversible:** yes.
- **Does not fix:** unnamed requests (the common case), or the cost of displaced built-ins.

### F. Surface the real count

- **Restated:** much of F has shipped. The model is told "with 53 tools" (§1.4). `/mcp-server`
  status says "Connected (N tools)". What remains:
  - the marketplace cards show no tool count. The catalog has none, but `handshakes.json` has the
    names, so a count can be derived at build time;
  - nothing tells the *user* "up to N offered per request, the rest via find_tools", and N varies
    by mode and setup (§1.1).
- **Files:**
  - `scripts/mcp/sync-registry.mjs` or `check-catalog.mjs` and `catalog.schema.json`
    (mochiii_website): a `toolCount` from handshakes;
  - `clients/vscode/src/mcpCatalog.ts` and `media/marketplace.js`: the card line;
  - `daemon/mcpstatus.go`: the "offered per request" clause. That needs the per-turn number, which
    is only known per turn, so the safest wording is "the rest via find_tools" without a fixed N.
- **Tests:**
  - catalog: the count equals the handshake's names;
  - status: a server over the free places says so.
  - Neuter checks: drop the count or the clause and its test fails.
- **Size:** small. **Reversible:** yes.
- **Does not fix:** any capability. It is wording.

---

## 3. Recommendation (updated from the brief's)

The brief recommended "F now, leave cap/reserve/tier alone, and use Step 2 to decide C for github".
**Three numbers change it:**

1. **16, not 11** (§1.1). The cap of 24 was chosen for 13 places and gives 6–8. That decision
   should go back to the owner with the true numbers, whichever option is chosen.
2. **The 15 Oct failure's information fix is already shipped** (§1.4). F is now only the card count
   and the user-facing "rest via find_tools" line: still worth doing, and smaller than briefed.
3. **The reserve is a no-op for one server** (§1.2). E is weaker than briefed and only helps a named
   server competing with others.

**So:**

- **(1)** Do the remaining F.
- **(2)** Put the 11 → 16 change in front of the owner before touching cap, reserve or tier. A lever
  not among the six options: the five built-ins added on 10-14 and 10-15 could be policy-denied by
  default or left to `find_tools`, which returns places without raising the cap. That is a product
  decision, noted, not proposed.
- **(3)** Before choosing C or D for github, run the redesigned eval in §1.6. It is billed and needs
  your go-ahead.

## 4. The open question for the owner

Does **"verified"** mean *"works well within the menu"* or *"starts and lists its tools"*?
The handshake proves the second. If it means the first, six of 17 verified servers do not meet it on
any ordinary turn (6–8 of 25–53 tools offered), and the choice is B or C. If it means the second,
A + F is honest, as long as the card says so.

---

## Eval — 2026-10-28 (the run §1.6 proposed, approved with a $5 limit)

**Setup.**
- Model `nvidia/nemotron-3-ultra-550b-a55b`, the stored key's chosen model, via `integrate.api.nvidia.com`
  through the production client (`streamWithRetry`).
- The **real first request**: the system message and menu built by the turn's own functions, as
  `prodMenuFirstRequest` does. The one difference is that `builtinRegistry` + `AddServer` stand in for
  the connect loop, which would start a real process. Ordinary chat turn, search-by-meaning off, on
  main 24c9274 plus PR #75 (which changes no menu). Throwaway harness
  `daemon/zz_menueval_throwaway_test.go` (`-tags eval`, uncommitted).
- **github's 46 tools are NAMES ONLY** (`mochiii_website/mcp/handshakes.json`: no descriptions, schema
  `{"type":"object"}`). Accuracy on name-only tools is not accuracy on real ones: real descriptions
  could help or hurt, and real schemas would make arm D's request much larger.
- 17 prompts × 5 trials × 3 arms = **255 calls**: 0 retries, 0 transport errors.
  - 11 prompts are the production-menu eval's unindexed cases, each with a built-in answer.
  - 6 are GitHub prompts, each with a github answer.
  - The plan said "the existing seven prompts". Those expect fixture tools (`run_tests` …) that are
    not on the real menu, so they cannot be graded here; the production cases replace them.
- Scored strictly on the **first tool call**. A `find_tools` first call is counted as a miss, and
  shown in the list below.
- Arms:
  - **A**: today, cap 24. Menu of 25: 16 built-ins, 8 github, `find_tools`.
  - **D**: cap 64. 62 tools: all 46 github, nothing cut.
  - **C**: cap 24, github limited by the existing `deny` policy to 8 tools chosen before the first
    call: `list_issues, issue_read, list_pull_requests, pull_request_read, get_file_contents,
    list_commits, search_code, add_issue_comment`.
- In arm A the relevance ranking put the expected tool on the menu for **every** prompt (verified
  dry, before any call). In arm C, `create_branch` and `merge_pull_request` are off by design.

**Results** (n per cell; nothing rounded away):

| arm | built-in prompts | github prompts | all | tool JSON per request |
|---|---|---|---|---|
| A today (cap 24) | **51/55** (92.7%) | **24/30** (80.0%) | 75/85 (88.2%) | 13,501–13,536 B |
| D cap 64 | **53/55** (96.4%) | **19/30** (63.3%) | 72/85 (84.7%) | 17,304 B |
| C subset of 8 | **51/55** (92.7%) | **13/30** (43.3%); 13/20 (65.0%) on the four prompts its subset covers | 64/85 (75.3%) | 12,921 B |

Per prompt (correct/5), only where any arm missed. All other prompts were 5/5 in all three arms:

| prompt (expected) | A | D | C |
|---|---|---|---|
| pip (`run_command`) | 2 | 3 | 3 |
| git_log (`run_command`) | 4 | 5 | 3 |
| gh_open_prs (`list_pull_requests` / `search_pull_requests`) | 4 | **0** | 1 |
| gh_comment (`add_issue_comment`) | 3 | 5 | 5 |
| gh_commits (`list_commits` / `search_commits`) | 2 | **0** | 2 |
| gh_branch (`create_branch`) | 5 | 5 | **0** (not in subset) |
| gh_merge (`merge_pull_request`) | 5 | 4 | **0** (not in subset) |

**Every miss** (expected → chosen ×count):

- **A (10):**
  - pip: `run_command` → `repo_map` ×2, `list_directory` ×1
  - git_log: `run_command` → `github__list_commits` ×1
  - gh_open_prs: → `run_command` ×1
  - gh_comment: → `find_tools` ×1, `github__issue_write` ×1
  - gh_commits: → `run_command` ×3
- **D (13):**
  - pip: → `find_files` ×1, `list_directory` ×1
  - gh_open_prs: → `run_command` ×5
  - gh_commits: → `run_command` ×4, no tool ×1
  - gh_merge: → `github__list_pull_requests` ×1
- **C (21):**
  - pip: → `list_directory` ×1, `sandbox_exec` ×1
  - git_log: → `github__list_commits` ×2
  - gh_open_prs: → `run_command` ×4
  - gh_branch: → `run_command` ×4, no tool ×1
  - gh_commits: → `run_command` ×3
  - gh_merge: → `github__pull_request_read` ×5

**How to read it.**
- **Built-in prompts did not suffer from a 62-tool menu**: 53/55 in D against 51/55 in A. Two trials
  apart at n=55 is noise, not an effect.
- **GitHub prompts did worse in D than A**: 19/30 against 24/30. The drop is two prompts, gh_open_prs
  and gh_commits, both 0/5, where the model chose `run_command` (presumably `gh` or `git` on the
  command line; the arguments were not recorded). Whether the wide list of bare names pushed it to
  the shell is PLAUSIBLE, not shown. At 5 trials a prompt this is a direction, not a measurement.
- **C's loss is mostly by design**: 10 of its 17 github misses are the two prompts whose tool the
  subset denies, and a denied tool is not reachable through `find_tools` either. On what it covers,
  it scored 13/20.
- **`find_tools` was called first once** (A, gh_comment), although every expected tool was already
  on A's menu.

**Comparable to the 2026-10-12 curve (100 / 94.3 / 91.4% at 12 / 24 / 32)? No.**
- That run used a **different model** (`nvidia/nemotron-3-super-120b-a12b`, which this key no
  longer serves).
- It used **fixture tools** (its 24-tool menu was 6,976 B; today's real 24 is 13.5 KB).
- It used **7 fixture prompts**, and a different selection of tools per size.

The three arms here are comparable **to each other**: same model, prompts, trials and run, minutes
apart.

**Spend.** 255 requests and 0 retries, under the 300-call abort. Dollars: **not observable from here**.
NVIDIA's hosted API is free for prototyping in the Developer Program, metered in request credits
(reportedly about one a request), with no public per-token price. Expected $0 and about 255 credits,
PLAUSIBLE. No billing or credit refusal was returned.

---

## Lines now wrong (not fixed; listed for when you decide)

| file:line | says | measured today |
|---|---|---|
| `daemon/mcpconfig.go:605` | "the shipped agent config's own tools take 11 places" | 16–18 on an ordinary turn (§1.1) |
| `daemon/mcpconfig.go:616` | "chose 24: thirteen places for added servers" | 6–8 places (5–6 in a long task) |
| `daemon/mcpconfig.go:617` | "eleven tools fit either way" | 16–18 |
| `docs/REFERENCE.md:1132-1133` | "`models.agent.json` as shipped those take 11, so the servers you add share 13 places" | 16–18 / 6–8 |
| `daemon/mcp/relevance.go:180-181` | "a 24-place menu left TWO places for every connected server together" | a dated observation (2026-10-15, Supabase + Firecrawl); today's census gives 6–8. PLAUSIBLE that it was measured under another setup |

Dated records that were right when written, and should stay as history:
- `docs/TOOL_MENU_SIZE_2026-08-01.md:148, 197, 199` ("11 tools", "thirteen places", "eleven tools"):
  true on 2026-10-12. A dated note pointing here would stop them being read as current.
- `docs/ADVERSARIAL_PASS_2026-09-03.md:162` ("11 tools offered"): 2026-09-03.

## Corrections to the brief

- "A named server keeps up to 6 places; an unnamed one may get none." True only when servers
  compete (§1.3). One server gets all the free places, named or not (§1.2).
- "The 'menu reported as full capability' failure recurs." Its information fix shipped on 2026-10-15
  (§1.4). Recurrence is NOT RUN.
- Option C's "new add-request field" holds (CONFIRMED). The daemon-side policy it needs already
  exists (`MCPServerConfig.Tools`).
- Step 2's arms (a), (b) and (c) are the same menu (§1.2), so it was NOT RUN as specified.
- The size-eval byte figures (3,649 / 6,976 / 9,111 B) are fixture menus. The real built-ins alone
  are 12,016 B (§1.1).
