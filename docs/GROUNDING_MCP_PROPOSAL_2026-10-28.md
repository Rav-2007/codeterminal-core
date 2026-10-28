# Grounding check vs MCP lookups — 2026-10-28

Decision 2026-10-28: option (d); see PR https://github.com/Rav-2007/codeterminal-core/pull/77.

**The proposal as written before the decision.** It describes main at `faf04a8`, before PR #77.

**Short version.** When a question looks like it asks about something that changes over time, the
agent loop requires the built-in `web_search` or `web_fetch` to have run. If neither ran, it forces
one more model call. An MCP server that fetched the live fact does not count. A failing test
reproduces this on main `faf04a8`. The cheapest precise fix, (d), uses the MCP spec's own
`openWorldHint` annotation. Terraform, Context7, Playwright and Nx already set it to `true`.
Filesystem, Memory and Sequential Thinking set it to `false`. So (d) fixes the observed case without
letting a local tool switch the check off, and it needs no catalog field.

Verification words: **CONFIRMED** (measured or run here), **PLAUSIBLE** (read in the code, not run),
**NOT RUN**.

---

## 1. What the check does today

All of this is PLAUSIBLE: read in `daemon/groundingnudge.go`, `daemon/livequestion.go` and
`daemon/agentloop.go` at `faf04a8`.

**What triggers it.** At the start of a turn, `looksLikeLiveWorldQuestion` (livequestion.go:123)
classifies the user's words. It marks the question "live" when:
- it is short (600 characters or fewer);
- it is not about the workspace (no file path, coding intent or identifier);
- it matches a world frame or a currency word such as "current", "latest" or "who is".

It runs only when a web tool is on the menu (agentloop.go:597-598).

A live question gets a paragraph in the system message telling the model to "Call web_search
FIRST". Then, when the model gives an answer with no tool calls, agentloop.go:836-839 checks four
conditions:
- this turn has not been nudged yet;
- a web tool was offered;
- `webToolUsed(turn.toolNames)` is false;
- the answer **either** hedges (a staleness phrase in its last 700 characters) **or** the question
  was classified live.

**What counts as a lookup today.** Only a call whose bare name is `web_search` or `web_fetch`
(`webToolUsed`, groundingnudge.go:171, via `isWebToolName`, webtools.go:433). `turn.toolNames`
records every call that dispatched, **including one that returned an error**. So a failed
`web_search` counts as a lookup, while a successful `terraform__get_latest_provider_version`
does not.

**What happens when it fires.**
1. The loop logs "a question about current facts was answered without a lookup".
2. It streams `[checking the web rather than answering from memory…]` to the user and adds the
   same text to the answer.
3. It appends the model's own answer, plus a user-role message (`uncheckedNudge`, 514 bytes)
   telling it "Call web_search now…".
4. It runs one more iteration.

The model's second reply is added after the first one, so the user sees: answer, notice, second
answer. In the live run, the second answer was "I didn't answer from memory — I called the
Terraform server's `get_latest_provider_version` tool…". It fires at most once per turn. The whole
text, notice and second answer included, becomes `FinalText` and is saved into chat history
(agentturn.go:338-347), so later turns in that chat re-send it.

## 2. Reproduction — CONFIRMED

`daemon/groundingmcp_repro_test.go`, `TestAnMCPLookupCountsAsHavingChecked`, is uncommitted. The
setup:
- A real stdio MCP server (`mcp/testdata/echoserver`) is configured as server `registry`, with
  policy `allow`. Its `echo` tool returns "hashicorp/aws latest version: 6.70.0".
- A scripted model calls `registry__echo` and then answers "…is **6.70.0**".
- The question is the live one: "Using the Terraform server, look up the latest version of the
  hashicorp/aws provider in the Terraform registry." The test asserts the classifier marks it live.
- `web_search` and `web_fetch` are on the menu, as in production.

The test asserts what should happen: 2 model calls and no notice.

| Run | Result |
|---|---|
| main `faf04a8` | **FAIL**: 3 model calls; the final text holds the notice and the forced paragraph |
| with a temporary option-(a) edit to `webToolUsed` (reverted, `git diff` empty afterwards) | **PASS**: 2 calls. The 35 existing grounding, hedge and live-question tests also pass |
| after reverting that edit | **FAIL** again |

## 3. Options

| | Counts as a lookup | Fixes the Terraform case | Gets wrong | Files | Tests | Measured eval changed? |
|---|---|---|---|---|---|---|
| **a** | any successful (non-error) third-party MCP call this turn | yes (CONFIRMED by the temporary edit) | Any local tool turns the check off. Filesystem, Git, SQLite, ESLint, Sequential Thinking or Memory, called on a live question, all count. **Memory is the worst case**: reading back a stale stored fact counts as checking it. | `agentloop.go` (record a successful Lane B call; `toolNames` also holds errors, so a new turn field), `groundingnudge.go` | the repro test, plus "a Lane B error still nudges" | none (§5) |
| **b** | an MCP call whose result the final answer actually used | yes, if detection works | **"Used" can't be detected reliably.** The best heuristic is distinctive answer tokens (versions, numbers, proper nouns) found in a successful result. It misses paraphrase, unit changes, counts ("3 rows" from a list of rows) and translation. It false-matches short or common tokens. It needs result text kept per turn, after scrubbing and Lane B framing. It is a new classifier that needs its own labelled corpus before it can be trusted. | `agentloop.go`, `groundingnudge.go`, a new matcher | the matcher's fixture corpus, the repro test and negatives | none, but it needs a new eval of its own |
| **c** | tools a catalog entry marks as lookups (for example `"lookup_tools": [...]` per server) | yes, for catalog-installed servers | It covers only the 17 verified entries. The 16.5k community entries, `/mcp-server add` servers and project `.mcp.json` servers get nothing. Curators must track tool renames with every version bump. | website `curated.json`, `catalog-rules.mjs`, the sync. Client: `mcpCatalog.ts` (parse, `checkServer`), the install path. Daemon: `MCPServerConfig`, `validateUserServer` (mcpuser.go), protocol `MCPRequest`/`MCPServerInfo`, the user-server store, `groundingnudge.go`. **All three validators must agree** (the catalog-parity rule from PR #66) | parity tests in all three, plus the repro | none |
| **d** | a successful third-party call to a tool that declares MCP `annotations.openWorldHint: true` | yes: Terraform declares it on all 9 tools (CONFIRMED, §3.1) | An absent hint counts as "not a lookup" (the spec's default is `true`, but this proposal is conservative). Unannotated lookup tools therefore still get nudged; Chrome DevTools and possibly Fetch are in that state. A server can claim openWorld falsely, but this is not a consent gate: it saves one model call, and the server already controls what its tool returns. | `mcp/mcp.go` (`Tool.OpenWorldHint`), `mcp/session.go` (`annotationBool`, explicit `true` only), `agentloop.go`, `groundingnudge.go` | session test: the echo server declares the hint on one tool, `false` on another, and none on a third; the repro test; "openWorldHint false still nudges"; "an errored openWorld call still nudges" | none |
| d+ | (d), **or** a successful call to a server the request names (`mcp.serversNamedIn`, relevance.go:236) | yes | It covers servers with no annotation that the user named, such as "use Chrome DevTools to…". It is wrong when the user names a local server on a live question ("use the filesystem server… who is the current PM"), which is rare. | (d) + exporting `serversNamedIn` | one more loop test | none |

### 3.1 Which servers declare `openWorldHint` — CONFIRMED

`initialize` plus `tools/list` against each catalog launch line, with PATH and HOME only, on
2026-10-28:

| Server | Tools | openWorldHint true / false / absent | readOnlyHint true / false / absent |
|---|---|---|---|
| Terraform | 9 | **9** / 0 / 0 | 9 / 0 / 0 |
| Context7 | 2 | **2** / 0 / 0 | 2 / 0 / 0 |
| Playwright | 25 | **25** / 0 / 0 | 7 / 18 / 0 |
| Nx | 1 | **1** / 0 / 0 | 1 / 0 / 0 |
| Filesystem | 14 | 0 / **14** / 0 | 10 / 4 / 0 |
| Memory | 9 | 0 / **9** / 0 | 3 / 6 / 0 |
| Sequential Thinking | 1 | 0 / **1** / 0 | 0 / 1 / 0 |
| Chrome DevTools (`--headless`) | 32 | 0 / 0 / 32 | 8 / 24 / 0 |
| ESLint | 1 | 0 / 0 / 1 | absent |
| Next.js DevTools | 7 | 0 / 0 / 7 | absent |
| SQLite | 2 | 0 / 0 / 2 | 0 / 2 / 0 |
| Fetch, Git | — | NOT RUN (need uvx, not installed here) | — |
| GitHub, PostgreSQL, MongoDB, Redis | — | NOT RUN (need a key or a database) | — |

The three servers that would make option (a) wrong all say `false` explicitly. That is the case
for (d).

## 4. Cost of one forced call

**Counted? Yes.** Every streamed model call goes through `usageTally.add` (provider.go:750 →
usage.go:73). That puts it in the turn's `Usage` (calls, prompt and completion tokens) and in the
session and day spend ledger (PLAUSIBLE, read). It was CONFIRMED live on 2026-10-28: the day's
spend record (482,323 tokens) equals the sum of all 55 logged calls, the forced Terraform call
included. The forced iteration also counts against the turn's iteration budget, because the nudge
`continue`s the same `for turn.iteration` loop. The added paragraph is part of `FinalText` and of
the saved history (§1).

**Measured from the test (bytes).** The fake upstream sends no usage, so the test measures bytes,
not tokens:

| Call | Request body | Messages |
|---|---|---|
| 1 (question) | 14,737 B | 2 |
| 2 (after the tool result) | 15,109 B | 4 |
| 3 (forced) | 15,775 B | 6 |

The forced call re-sends the whole conversation and tool menu, plus 89 B (the model's answer
echoed back) and 514 B (`uncheckedNudge`). The user sees 59 B of notice and the forced reply. The
test's system message is a stub, so a real request is larger.

**Measured live (tokens), the 2026-10-28 Terraform turn**, from the daemon's "model API usage"
lines, on nvidia/nemotron-3-ultra-550b-a55b:

| Call | Prompt | Completion |
|---|---|---|
| 1 | 8,731 | 83 |
| 2 | 8,809 | 47 |
| 3, forced | **8,948** | **157** |

The forced call is **9,105 tokens**, on top of 17,670 for the two calls the turn needed: **+52%
for that turn**. It also adds one network round trip, about 3 s in that run.

## 5. Evals

No measured eval exercises the nudge. `prodmenu_eval_test.go` sends only an ordinary turn's
**first** model call (its header says so), and the nudge acts at the turn's end. Of the other eval files,
`agentloop_eval`, `tasksuccess_eval`, `orchestration_ab_eval` and `toolmenu_eval` mention no web
tool and ask no live-world question (searched for `web_search`, `webCfg`, "current", "latest
version", "who is"). `realrepo_eval` turns `web_search` and `web_fetch` off. None of (a)–(d)
changes a system message or a tool description, so no recorded eval number moves (PLAUSIBLE:
read, not re-run).

**Correction after PR #77's CI: wrong for one eval.** The locate eval (`retrieval eval / locate
eval (whole-repo corpus)`) searches this repository's own code, so any edit can move it. It
cuts files into fixed 40-line windows. #77's first version put a 10-line comment in
`agentTurn`, which pushed agentloop.go's iteration stop from line 995 to 1008, out of the window
that answers "where does the agent loop stop iterating". Retrieved fell from 30/49 to 29/49, and
the gate failed (CONFIRMED on CI and locally). Moving the comment beside the check in
groundingnudge.go restored it: no question changed (CONFIRMED locally against `faf04a8`).

## 6. Related, not in any option

- `preflightGroundingDirective` (livequestion.go:228) and `uncheckedNudge` both say "Call
  web_search" even when the user named a connected MCP server. Live, the model ignored the
  directive and used Terraform correctly. A fix to the check might want the nudge text to say
  "web_search or a connected server's lookup tool", but that changes the system message, so the
  production-menu eval would then apply.
- A failed `web_search` counts as a lookup today, by design (groundingloop_test.go:133-138). The
  options above require a **successful** MCP call. That asymmetry is deliberate: a Chrome DevTools
  call that failed (as on 2026-10-28) proves nothing about the answer.

---

**Which option should be built: (a), (b), (c), (d), or d+ ((d) plus a server named in the
request)?**
