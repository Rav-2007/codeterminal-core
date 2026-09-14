# PROJECT CHECKPOINT — 2026-09-13

**Purpose of this document.** Everything an agent or a person needs to resume work on
`codeterminal-core` without re-deriving what three audit passes already established. Read it
top to bottom once; after that, use the section index.

**Written for:** a fresh session with no memory of the prior work.
**Authority:** Ravi Kiran — CTO/CEO, and (as of 2026-09-13) the named `daemon/` owner.
**State at time of writing:** branch `audit/adversarial-pass`, HEAD `1013e1b`, local = upstream,
tree clean, CI green, release gate closed.

---

## 0. THE ONE-PARAGRAPH VERSION

`codeterminal-core` is a Go monorepo implementing a local coding assistant: a daemon holding all
credentials, a Bubble Tea terminal client, a VS Code extension, an embedding helper subprocess, a
diff-application engine, a wire protocol, and a hosted proxy. Between 2026-09-03 and 2026-09-13 it
underwent three adversarial passes — terminal-client hardening, daemon re-verification, and a
full-codebase read-only audit — followed by a patching pass and a CI root-cause investigation.
Sixteen product defects and eight verification defects were fixed, each with a test seen to fail
before the fix. Five defect *classes* were named, of which two are novel to this codebase. The
single highest-severity finding (a secret scrub reverted one turn later, with a false assurance to
the user) was independently re-derived three times over five weeks because the decision memo
describing it had been committed but never delivered to anyone — the `daemon/` owner role was
unassigned, not unresponsive. Naming the role closed it. All five memo items now carry recorded
verdicts dated 2026-09-13.

---

## 1. SYSTEM ARCHITECTURE

### 1.1 Modules

| Module | Product LOC | Test LOC | Coverage | Floor | Purpose |
|---|---|---|---|---|---|
| `daemon` | 27,578 | 47,509 | 78.8% | 78.0 | Holds all credentials; model API, retrieval, tools, MCP, LSP |
| `daemon/mcp` | 2,458 | 3,132 | 92.1% | 91.0 | MCP stdio client, sandbox |
| `clients/tui` | 5,339 | 12,931 | 84.5% | 84.0 | Bubble Tea terminal client |
| `editapply` | 4,130 | 6,878 | 89.6% | 88.0 | Diff/edit application, path hazards |
| `protocol` | 2,498 | 2,096 | 90.2% | 89.5 | Wire format, peer authentication |
| `proxy` | 4,563 | 6,309 | 86.1% | 85.0 | Hosted API proxy, ZDR routing |
| `helper` | 565 | 1,277 | 22.2% (61.5% local) | 22.0 | ONNX embedding subprocess |
| `helper/helperproto` | — | — | 75.0% | 75.0 | Helper wire types |
| `clients/vscode` | 4,694 TS | 3,452 TS | n/a | n/a | VS Code extension |

**Totals:** 663 tracked files, 122,216 LOC, test-to-product ratio ≈ 1.98:1 by file count.
483 `.go`, 61 `.md`, 27 `.ts`, 24 `.sh`, 4 workflows.

`helper` coverage is environment-dependent: tests load a cached ONNX model and skip without it.
CI measures ~22%, a local machine with the model measures 61.5%. **The floor must never be raised
from a host with the model cached.**

### 1.2 Binaries and processes

| Binary | Entry | Started by | Stopped by |
|---|---|---|---|
| `codeterminal-daemon` | `daemon/main.go:29` | VS Code supervisor, TUI, CLI | SIGTERM drain; exit 3 lifecycle contract |
| `codeterminal-embedder-helper` | `helper/main.go:36` | daemon `helperproc.go:202` | daemon shutdown |
| `codeterminal-tui` | `clients/tui/main.go:23` | user | esc / ctrl+c |
| `proxy` | `proxy/main.go:417` | Docker/Railway | SIGTERM |
| VS Code extension | `src/extension.ts` | VS Code host | host |

**Release asymmetry:** `release.yml` builds three binaries (daemon, helper, tui). The proxy ships
by Dockerfile. Two release paths, one artifact set.

**Goroutine launch sites in product code:** daemon 13, tui 4, proxy 2, helper 1, editapply 0,
protocol 0.

### 1.3 The thirteen trust boundaries (measured, `file:line`)

| # | Crossing | Site |
|---|---|---|
| B1 | peer → daemon control socket | `daemon/server.go:241` accept → `:276` handleConn |
| B2 | peer identity check | `daemon/server.go:295` `protocol.AuthorizePeer` |
| B3 | peer → helper socket | `helper/main.go:197` `protocol.AuthorizePeer` |
| B4 | daemon → completion provider (egress) | `daemon/provider.go:579` POST |
| B5 | provider → daemon (SSE / model output) | `daemon/provider.go` stream path |
| B6 | model output → filesystem writes | `editapply/editpayload.go:68`, `editblock.go:59`, `unifieddiff.go:36` |
| B7 | MCP server (stdio subprocess) → daemon | `daemon/mcp/stdioclient.go:164` |
| B8 | LSP server → daemon | `daemon/lsp_bridge.go:257` |
| B9 | web content → daemon | `daemon/webfetch.go:207`; SSRF guard at `Dialer.Control:129` |
| B10 | workspace files → index → prompt | `daemon/index_cmd.go`, `context.go` renderChunk |
| B11 | chunk text → egress (scrub choke) | `daemon/scrub.go:87` |
| B12 | client → proxy (API-key authz) | `proxy/authcache.go`, `main.go` |
| B13 | proxy → upstream (ZDR/F1 gate) | `proxy/main.go`, `FuzzZDRRoutingEnforced` |

**Caveat:** derived from entry-point greps, not an exhaustive call graph. Completeness is (U).

**Note on an earlier error:** an "eleven trust boundaries" figure circulated in tasking documents.
It was a conflation — every occurrence of "eleven" in the repo counts something else (gate claims,
pin sites, model tiers, neuters). Do not repeat it.

### 1.4 Persistent state

| Store | Size (measured) | Mode | Growth bound | Removal |
|---|---|---|---|---|
| `.codeterminal/index/lexical.db` | 56.7 MB | 0600 | file count (10,000), **not bytes** | `pruneOrphanedChunks` |
| `.codeterminal/index/<hash>/` | ~26 MB | 0700 | same | same |
| `embedder_stamp.json` | 154 B | **0600** (was 0644, fixed) | n/a | reindex |
| `.codeterminal/logs/toolcalls.jsonl` | 17,639 B | 0600 | size-rotated | rotation to `.1` |
| `.codeterminal/logs/warnmode.jsonl` | 6,160 B | 0600 | same | same |
| `.codeterminal/backups/` | 164 KB | 0700 | **(U) no cap found** | **(U) no sweep found** |
| `memory.db` | — | 0600 in 0700 | `maxTurnsPerWorkspace`, 30 days | eviction |

**Idle RSS 8.6 MB; 13.7 MB at 120 turns.** An earlier figure of 39.3 MB could not be reproduced
across two attempts and is superseded — do not re-derive from it if you find it in old text.

---

## 2. REPOSITORY TOPOLOGY — READ THIS BEFORE ANY CI CLAIM

Two remotes carry the same workflows. **Both emit runs named `build` and `gates`. A run id alone
cannot distinguish them.**

```
origin    git@github.com:Rav-i24/Mochiii.git            ← a fork; Actions quota exhausted
upstream  https://github.com/Rav-2007/codeterminal-core ← WHERE CI ACTUALLY RUNS
```

- `gh` resolves to **origin**, which is *not* where CI runs. Always pass
  `--repo Rav-2007/codeterminal-core` explicitly.
- There is **no sync hook and no sync workflow**. A push to one does not reach the other.
- **Every CI claim must name the remote, the run id, and the commit.** A readiness statement once
  cited two green runs that were green *on the fork* while the upstream run was red.
- `build.yml` carries `paths-ignore: ["**.md"]`. A markdown-only commit runs `gates` and no
  `build` — "there is no build run at HEAD and that is not a pass."
- `paths-ignore` is evaluated over **every file changed in the push**, not per commit. A mixed
  push does not skip.
- `workflow_dispatch` and `push` share the concurrency group. **Dispatching cancels an in-flight
  push run.** Dispatch first or wait; never both.
- Billing blocked all Actions once (jobs failing in 5–13 s with "recent account payments have
  failed"). Free-tier: 2,000 min/month. macOS bills 10×, Windows 2×, Linux 1×.

**Branch:** `audit/adversarial-pass`, 155+ commits ahead of `main`.
**HEAD:** `1013e1b`. Local = upstream.
**Last green CI:** `build 34739694099`, `gates 34739694106`, both at `1013e1b` on
`Rav-2007/codeterminal-core`. One skip, named: `retrieval eval (scheduled)`.

---

## 3. THE METHOD — RULES EARNED BY FAILURES, NOT IMPORTED

Every rule below exists because something in this repository failed without it. They are the most
transferable output of the whole effort.

| Rule | Statement | The failure that earned it |
|---|---|---|
| **M1** | Measure, do not read. Nothing closes on inspection. | Repeated |
| **M2** | Neuter everything. A test never seen to fail is not evidence. | Multiple vacuous tests shipped |
| **M4** | Check every doc comment, register row and gate claim against the code it describes. | Five confirmed doc/code mismatches |
| **M5** | Two states meaning different things never share a phrase. | "skipped"≠"passed", "committed"≠"handed", "unfixed-by-decision"≠"pending-a-decision", "bounded"≠"rate-limited", "not recoverable"≠"not verifiable", "unassigned"≠"unresponsive" |
| **M7** | Correct yourself out loud. Shrink claims that overstate their evidence. | Every self-correction outranked the finding it replaced |
| **M8** | Search for prior art before concluding anything is absent. | Failed 5+ times in one session, at descending scale |
| **M9** | Neuter your own new gates, not only the code they guard. | A class guard passed while the allocation test failed |
| **S7** | A document nobody was told about is indistinguishable from one that does not exist. | The memo, five weeks, three re-derivations |
| **CI-1** | A gate red for a reason nobody will read gets waived. | Six lint jobs that reported nothing about lint |
| **CI-2** | A gate that passes by configuring the host rather than by the code being correct lies to developers. | CI relaxed a sysctl for itself while the test stayed broken |
| **T-1** | A test inferring a property from elapsed time asserts the wrong thing. | "Returned fast, so cancellation worked" — it returned *before* the cancel fired |

**Additional standing conventions:**

- **Assert allocations; report wall-clock.** Allocations are deterministic and
  machine-independent. Wall-clock under `-race` is 5–20× slower and flaps. Every benchmark gates
  on allocs; wall-clock prints with its distance to budget and asserts only at a wide multiple,
  behind a `raceEnabled` build guard.
- **Every number is labelled DETERMINISTIC or WALL-CLOCK, with provenance.**
- **Provenance tags:** `(M)` measured this pass · `(R)` read only · `(U)` unknown, with what would
  establish it · `(C)` carried from a prior pass, not re-verified.
- **"Gate exit 0" is meaningless without naming the invocation.** A partial run skips checks a
  whole-repo run performs.
- **Behaviour-preserving and behaviour-changing work never share a commit.**
- **A recorded deferral with a trigger closes a row as well as a fix does.** An acceptance without
  a concrete reopening condition is a postponement, not a decision. Where no trigger was stated,
  record `TRIGGER: NONE STATED` rather than inventing a plausible one.
- **Report enumeration before implementing.** Flag anything cross-module, >150 lines, a new
  dependency, or a public API change before writing it.
- **Complete enumeration is mandatory; complete inspection is not claimed.** Read closely /
  skimmed / not opened / could not parse are four different states.

---

## 4. THE FIVE DEFECT CLASSES

A named class is worth more than the findings in it, because it predicts the next one.

### CLASS I — A gate whose expected scope derives from the artefact it validates
*The gate computes what it should check from the thing it is checking, so anything absent from the
artefact is absent from the expectation, and the gate reports agreement with itself.*

**Eight instances:**
1. `platformcoverage_test.go` — floor of 20 asserted, 73 advertised in two documents and a commit message
2. `scripts/fuzz.sh` — `TARGETS` is a hand-edited array and its own source of truth; deleting rows shrinks `${#TARGETS[@]}` and reports "N of N"
3. `coverage-ratchet.sh` — empty floors file, `set -u` aborted before the emptiness check ("fail-closed by luck rather than by design")
4. `coverage-ratchet.sh` — parser matched only two of three `go test -cover` output shapes
5. `badserver` / `fakelsp` mode switches — deleting a mode deletes its coverage silently
6. `make check` vs CI workflows — two unreconciled enumerations of "what to check", nothing compared them
7. **The vanished-floor sweep behind `if [ $# -eq 0 ]`** — CI always passes a module, so it was false in *every CI run this repository has ever produced*. Its comment ("a partial run legitimately does not visit them") was a defensible sentence and a wrong conclusion. **First instance where a correct-sounding comment was the concealment.**
8. **`docs-coderefs.sh`'s own honesty counter** — "162 refs in 15 unenforced documents NOT checked" derived from the same glob that defines its blindness. True figure: 187 in 16. **Class I applied to the mitigation for Class I.**

**Prediction this licenses:** every "known gap" counter in this repo is a candidate instance.

### CLASS II — A gate whose scope is sound and whose judgement is broken
*No vacuity floor can detect it — a floor counts what was inspected, and a blinded detector still
inspects everything. Only a case with a known answer catches it.*

**Instances:** a timing guard that passed green with `mentionsAnyIdent → true` while both floors
passed; a stamp-perms test whose `Perm()` assertion read a constant on Windows.

### CLASS III — The cap is applied to the result, after the whole resource has been materialised
*A bound exists, is documented, and is real — but it is enforced downstream of the allocation it
appears to govern. The output is bounded; the consumption is not.* **Newly named.**

**Three instances, all measured, all fixed:**

| Site | Materialised | Capped to | After fix |
|---|---|---|---|
| `mcpbuiltin.go:367→:373` read_file | 33,715,296 B | 64 KB | bounded |
| `mcpbuiltin.go:403→:429` list_directory | 9,694,832 B | 500 entries | 2,072,048 B |
| `mcp_ast_edit.go:141` | 503,440,408 B | nothing | 5,286,648 B, refuses |

Largest reachable file measured in-repo: 219,843,800 B. Amplification 3,354×.

**The antidote already existed in-repo and the tool surface never inherited it:**
`fileref.go:353` re-runs `shouldSkipFile` ("Exactly the indexer's eligibility gate"),
`chunker.go:275` re-runs it immediately before reading, `planmode.go:59-64` states the principle —
*the read side must not assume the write side ran.*

**Why it stayed invisible:** `maxBuiltinReadBytes`'s comment said it "bounds one read_file result"
— accurate, and the accuracy is the trap. And `TestBuiltinReadFileAnnouncesTruncation` drove an
oversized file, asserted `len(res.Content)`, always passed, and its failure message read "the read
cap did not apply" *while measuring the result*. **The RESULT/READ distinction was already
mis-stated inside a test.**

### CLASS IV — The same policy, computed twice, by two halves that have drifted
*A policy deliberately implemented on a read side and a write side — a pattern this repo endorses
for good reasons. When later extended, one half is updated and the other is not. Both still exist,
both still look deliberate, and the comments still say "both halves are deliberate."* **Newly
named.**

**Two instances** (originally reported as three; the history-ceiling instance was a false positive
and is retracted):
1. **Secret scrubbing** — prompt ✓, chunks ✓, history ✗. This is F-1/R1.22. **Now fixed.**
2. **Sandbox confinement** — `WrapCommand:415-427` probes and fails closed; `ResolveMode:374` /
   `Confines()` does not probe. Consent text ≠ execution reality. **Open, LOW.**

**The trap, precisely:** `history.go:109-112` says "both halves are deliberate" — true about
*empty-content validation*, and it is the same sentence a reader meets when asking about
*scrubbing*, where there is no write-side half at all.

**The counter-example done right:** `history.go:130-135` — "one enormous final turn can still
exceed maxHistoryBytes… That residual is named rather than hidden."

### CLASS V — Incomplete enumeration of what is forbidden
*Scope right, judgement right, but the list of prohibited forms is short.* **Newly named.**

**Instance:** a class guard recognised `os.ReadDir` and not the semantically identical
`dir.ReadDir(-1)`. Found by neutering the gate, not the code.

### The meta-observation
**Classes I and IV are the same failure at different altitudes:** a verification artefact deriving
its expectation from something that shares its blind spot. Class I's artefact is a glob; Class
IV's is a sibling function. In both, the check passes because checker and checked were built from
one understanding, and neither is updated when that understanding grows.

---

## 5. THE HEADLINE DEFECT — F-1 / R1.22 (FIXED 2026-09-13)

### What it was

A user pastes an API key. The daemon redacts it, sends the redacted version to the model, and
**shows the user a "redacted 1 secret" notice.** It then stores the key unredacted, hands it back
at the next handshake, and sends it in full to the provider on turn 2 and every turn after.

Traced, every hop, with measured line numbers:

```
:567  cleanPrompt = scrub(prompt)          scrubbed for the wire
:586  "redacted N secrets"                 the user is told it worked
:707  persistTurn(promptReq.Prompt, ...)   RAW, not cleanPrompt
:385  PersistedHistory:                    raw turns to the client at every handshake
:524 → history.go:113                      role + non-empty only; no scrub
:615  buildChatMessages(system, history, augmentedPrompt)   three inputs, two scrubbed
```

`grep scrub( across daemon/` returned zero hits on any history, turn, or memory path.

**Why the notice makes it worse than never scrubbing:** a silent failure leaves the user
uncertain; this one affirmatively tells them the secret did not leave, then sends it.

### What concealed it

`server.go:555-566` enumerates the scrub surface with unusual care — names the prompt, names the
chunk choke point, even names its own residual ("Chunk scrubbing is PARTIAL: structural signatures
only"). **History is absent.** Eleven lines later, `:592-598` discusses history and answers only
"is it merged twice?", never "is it scrubbed?"

**A thorough comment incomplete in exactly one place is harder to audit than no comment**, because
its evident care is what stops the reader checking.

### The delivery failure — the actual root cause

The defect was found **three times independently**:
- 2026-09-04, by execution, filed as memo item 1 with a recommendation
- 2026-09-08, re-derived from scratch, filed as R1.22
- 2026-09-12, re-derived again, filed as F-1

The memo **predicted its own re-derivation and was right twice.** The cause was not anyone's
inattention: **the `daemon/` owner role was UNASSIGNED, not unresponsive.** Nobody declined to
read the memo — nobody was asked, because nobody had been named. The memo was committed to the
repo on 2026-09-04 and shown to no one until 2026-09-13.

**Naming the role is what closed this, not any of the five patches.**

### The fix, and two things tracing to the end found

- **`persistTurn` has two callers** — `server.go:707` and `agentturn.go:284`. The memo's own fix
  description said "one identifier at `:707`"; taking it literally would have left every agent
  turn storing secrets verbatim. **The control went inside `persistTurn`.**
- **A fail-open in the first draft:** `s.cfg == nil || s.cfg.NoScrub` — nil config would have
  disabled scrubbing entirely. Caught before landing; now `s.noScrub()`, pinned by a test.

**Three states kept apart everywhere (M5):** provider path closed · handshake payload closed ·
rows written before the fix **purged** (schema v4). `server.go:586`'s notice is now true, and its
comment names the three controls that make it so.

### The third path, found by tracing properly

`SearchTurns` (`search.go:128`, reached from `server.go:1007` — the `/search` command) returns
`snippet(turns_fts, …)` of raw stored content and passes through neither `prepareHistory` nor
`scrub`. This is what made **PURGE** the right migration answer: unmigrated rows would have stayed
retrievable by the exact string that matches them, indefinitely.

---

## 6. DECISIONS RECORDED — 2026-09-13, Ravi Kiran, `daemon/` owner

| Item | Verdict | Trigger | Commit |
|---|---|---|---|
| 1 — scrub bypassed by one turn | **FIXED** (1a+1b) | n/a | `9987985` |
| 1 — migration of existing `memory.db` | **PURGE** (schema v4) | n/a | `9987985` |
| 1c — scrub assistant turns | **DECLINED** | NONE STATED | pinned by test |
| 2 — `/mcp-server` stderr unredacted | **FIXED** at `mcp.Connect` | n/a | `e44f217` |
| 3 — inbound model text not redacted | **ACCEPTED IN WRITING** | transcript export / multi-user access / telemetry sampling | `1e48e3c` |
| 4 — four fuzz targets generate nothing | **FIXED** (lazy `sync.Once` build) | n/a | `d15c808` |
| 4 — fuzz gate fatal? | **LOUD-BUT-NON-FATAL** | NONE STATED | `1e48e3c` |
| 5 — panic containment | **FIXED** ours; SDK's four **OPEN** | next `go-sdk` upgrade, or first unexplained daemon exit with MCP configured | `ab5fbf6` |
| `backup_log.txt` | **DELETED FROM HEAD**, history left | NONE STATED | `1e48e3c` |

Item 3 is cleanly accepted because item 1 was FIXED — no flagged combination.

**Item 3's reasoning, so it does not read as a to-do:** `redactionsMsg` matches **shapes**, not
values the daemon provisioned. "Apply the same set inbound" means running ten regexes over prose,
which decision D5 already refused on measured data (33% of chunks, zero precision). A coding
assistant that redacts its own key-format examples is broken in the one place users read them.
Severity drops materially now that item 1 has landed.

**Item 2's enumerated partiality:** the fix strips values the daemon provisioned at
`mcpruntime.go:111`. It does not catch a credential a server reads from its own keychain, nor the
~23 variables `npx` adds. One stated judgement inside it: an 8-character minimum below which
values are not redacted.

---

## 7. EVERYTHING FIXED, WITH THE EVIDENCE

### 7.1 Product defects

| Defect | Fix | Verifying test | Neuter result |
|---|---|---|---|
| Terminal escape injection — 7 payloads reached the terminal verbatim | Allowlist sanitizer, reject-by-default, stateful across chunks, one ingest door (`appendTurn`) | 43-entry corpus + 14 CSI shapes + 2 fuzzers + real-pty test | filter off → 37 tests fail |
| C1 in a model-authored edit path — `RejectUnprintablePath` refused `r < 0x20` and DEL but **not C1 (U+0080–U+009F)**; U+009B is the 8-bit CSI introducer | `unicode.IsControl` | `TestRejectUnprintablePath_RefusesTheWholeControlClass`, 65-rune sweep + count floor | revert → 33 C1 chars pass |
| SIGHUP emitted **zero** restore bytes (Bubble Tea registers only SIGINT/SIGTERM) | Both funnel into the library's graceful shutdown | pty suite, 57-byte assertion | 20 assertions fire |
| `--prompt \| head` exited 141 (SIGPIPE) | One-shot installs the handler; chat UI deliberately does not | real binary into a real pipe | fails on "was KILLED" |
| Scroll-back impossible during a stream — `refreshViewport` ended in unconditional `GotoBottom` | Follow only for a reader already at the bottom; ask every refresh | 3 tests pulling opposite ways | both directions fail correctly |
| Quadratic lexical query, unbounded — 1 MB prompt occupied the daemon 89 s | `maxLexicalQueryChars = 32768` at the shared chokepoint | `lexicalbound_test.go`, 7 tests | remove → 1 m 28.894 s |
| `serveConn` used `context.Background()` — client gone at 59 ms, handler worked 11.27 s | `cancelConn` threaded through 5 call sites | `cancellation_test.go` | cancellation never propagates |
| Transcript unbounded | 500 turns / 2 MiB ceiling, visible eviction marker | 2,000-turn soak | — |
| 1 MB paste over budget; **4,000-char truncation silent since inception** | Deterministic rune bound + header saying arrived/kept/dropped | `TestNoMoreRunesReachTheInputThanCanBeKept` | 15.6 ms → 0.30 ms |
| `TOO_LARGE` marker read uncapped — 256 MB → 512 MiB heap, 268 MB wire | 4096-byte cap + fstat + `O_NONBLOCK` | 7 tests incl. FIFO, symlink, oversize | heap and wire blow out |
| Two discriminator keys dispatched by declaration order into the destructive handler | `countDiscriminators` in `requestFields` | 7 dispatch rows asserting the file on disk | ambiguous bodies dispatch again |
| `helper/handleConn` had no `recover()` — a tokenizer panic killed the process | `recover` + stack log | `TestHandleConn_ContainsAPanicToOneConnection` | the test binary itself dies |
| `--debug-context` logged raw chunk content past the scrub choke point | Scrub through the same predicate | `TestSentinel_RetrievedChunkIsScrubbedOnTheWireButNotInTheDebugLog` | sentinel reappears |
| Unparseable tool name recorded unbounded — 1,048,596 B name → 1,048,851 B audit line | `truncateForClient(…, maxReportedToolName)` | `TestSentinel_ToolAuditBoundsAnUnparseableToolName` | line unbounded again |
| Class III ×3 (see §4) | `readBoundedFile`, `dir.ReadDir(n)`, bounded read + refusal | allocation tests via `runtime.MemStats` | all three reproduce |
| `jsonlsink` rotation bound removable by persistent rename failure — **620,013 B against a 4,096 B bound, 151.4×** | Refuse the append; `droppedForBound` atomic counter | writable file in non-writable dir | unbounded growth returns |
| `embedder_stamp.json` 0644 alone among 0600 siblings | 0600 | perms assertion | `-rw-r--r--` |
| F-1 (see §5) | Control inside `persistTurn` + `prepareHistory` | canary across two turns | — |
| `/mcp-server` MCP stderr unredacted | Exact-match strip of provisioned env at `mcp.Connect` | wiring test polling to a deadline | — |
| MCP connect goroutine outside any `recover` | `recover` + log at `mcpruntime.go:206` | — | — |

**Class III instance ③ refuses rather than truncates**, and the reason matters: `extractLSPRange`
would slice a shortened buffer and produce wrong `Search` text. Truncation there is a correctness
bug, not merely a memory one.

**The `jsonlsink` trade, argued:** the sink already drops records when it cannot open the file, so
losing a record is inside its contract; unbounded growth is not, and an unbounded log is a way to
cause the full disk the package exists to survive. `droppedForBound` exists so the fix does not
trade silent unbounded growth for silent unbounded data loss — two failures wearing one silence.

### 7.2 Verification defects

| Defect | Effect | Fix |
|---|---|---|
| `errcheck-ceiling.sh` counted lines with stderr discarded | A module that would not build scored a **perfect zero** | Fails closed |
| `scripts/fuzz.sh` — nonexistent target exited 0 | **Both TUI sanitizer fuzzers had never run under the gate since task 2.1** | Detects and reports |
| Debt-marker gate | **Did not exist** — a manual grep over one module of six, reported as a gate every round | Real gate; 156 files, 0 markers |
| `supply-chain.sh` | A module with no Go files → silent skip, exit 0 | Fails closed |
| `go-toolchain-pinned.sh` | Verified 7 files, printed "7 pin sites agree on go1.25.13" **while CI ran go1.25.14** | 11 sites; the runner is now one of them |
| Same check, first fix | With the workflow directory missing, printed "7 pin site(s) agree" from a gate that had stopped checking | Missing directory fails |
| `lint.sh` | Checked `command -v` only; local ran staticcheck v0.7.0 against CI's v0.8.1 | Version probe over four binaries, 49 ms |
| **`lint.sh`'s printed remediation was itself broken** | The gate was **un-bootstrappable from a clean machine**; passed locally only on binaries dated 2026-08-04 | Fixed and verified by running it with tools off PATH (4/4 exit 0) |
| The install step failed → **Lint never ran**; six red jobs reported nothing about lint | `x/sys/execabs` is a *missing import* resolved at `@latest` regardless of pinned versions; `x/sys@v0.48.0` needs go ≥ 1.26 | All four linters pinned + `GOTOOLCHAIN` floor |
| The vanished-floor sweep | False in **every CI run this repo has ever produced** | Scoped per module |
| `make check` vs CI | Two unreconciled enumerations | `gate-parity.sh` — derives both sides, checks both directions, mandatory reason per asymmetry; found three real divergences **and itself** on first run |
| No `-count=1` | `setup-go` caching could serve stale results | `-count=1` on `make test`, `make race`, both CI test steps |
| `govulncheck` `@latest` | Last unpinned tool; same failure shape as the linters | Pinned v1.6.0 |
| `stage-runtime.js:28` read `process.platform === 'win32'` | Asked what the script **runs on**, not what it **builds for**; the release job builds on three runners and packages all three `.vsix` on one Linux host. Pre-existing on `main` | Target passed in; host as fallback; 6 neuters |
| **macOS signing secrets absent** | The step is a no-op when the five `MACOS_*` secrets are missing, so **the run is green either way**. Darwin binaries ship unsigned; Gatekeeper quarantines them | Signing guard (§7.3) |
| `TestEveryUnrunnableSandboxRefusesWithAReason` | Green in CI because `build.yml` relaxes the sysctl **for itself**; red for any developer on stock Ubuntu 24.04 | Expectation follows `BwrapUsable()` — live on both host types, and newly covers the unusable-host refusal |
| Timing literals | 17 bare literals; two were **lower bounds** (fail on a *faster* machine) | All 17 converted to derived bounds; static guard fails on a new bare literal |
| 27 stale code references across 24 files | Files renamed `peercred_*` → `peerauth_*` two moves ago; references never followed. Included `SECURITY_MODEL.md:789` naming the wrong file *and* wrong module | All fixed except dated archives, deliberately |

### 7.3 The signing guard — predicate on "was this signed?", not "is this darwin?"

A hard exclusion would encode today's decision in the machinery, creating a second place the
decision lives, and it is an enumeration — the shape this repo has been burned by repeatedly.

| Trigger | Marker absent | Behaviour |
|---|---|---|
| `workflow_dispatch` (rehearsal) | yes | Exclude that target's assets, warn loudly, job **green** |
| tag `refs/tags/v*` (real release) | yes | **Fail the job** |

- Marker written at **four** unsigned paths, not three — the fourth is signed-but-not-notarized,
  because Gatekeeper blocks that download exactly as it blocks an unsigned one. **The marker
  asserts DISTRIBUTABLE, not CODESIGNED.**
- **Fails closed:** keys on *presence* of `SIGNED-<target>`, never absence of `UNSIGNED-<target>`.
- Per-target, so Windows Authenticode works later without a second mechanism.
- Manifests regenerated from disk, not pruned by hand.
- 40 assertions, 6 neuters each demonstrated red.
- **Darwin ships with zero edits the day the secrets land.**

---

## 8. THE MEASUREMENTS

### 8.1 Per-token allocations (the TUI render cache)

| Prior turns | Before / cache neutered | After |
|---|---|---|
| 0 | 23 | **18** |
| 30 | 195 | **25** |
| 120 | 692 | **27** |
| 240 | 1,353 | **28** |
| 400 | 2,234 | **29** |
| **growth** | **97×** | **1.6×** |

The **ratio** is the acceptance signal, not the absolute count. The "before" column is what the
same tests report **today** with the cache disabled — that neuter was run before any improvement
was claimed and re-run after the `Update` refactor.

**The cache design: validated, not invalidated.** Each entry stores the inputs it was rendered
from; a mutation nobody thought of yields a **cache miss, not stale text**. The invalidation
enumeration lists twelve entries; entry #4 (activity-line rewrite keyed by tool-call id, mutating
an arbitrary earlier turn) is the one an invalidation-hook design would have missed.

### 8.2 The repaint at the transcript bound — the one budget knowingly unmet

At 490 turns / 2,096,839 bytes / 512 evictions:

| | Value (WALL-CLOCK) | vs 8 ms repaint budget | vs 16 ms hard per-Update ceiling |
|---|---|---|---|
| p50 | **22.4 ms** | 2.8× | **1.4× — breached** |
| p99 | **29.9 ms** | 3.7× | 1.9× — breached |
| min/max | 21.9 / 42.3 ms | spread 1.9× | |

**All 200 samples were over both lines. It does not straddle** — unlike the paste measurement,
which did (2.6× spread) and was correctly recorded as "no reliable headroom" rather than a breach.

Where it goes, cache warm: `wrapToWidth` 17.6 ms (79%), `viewport.SetContent` 4.5 ms (20%),
`transcriptCache.render` 334 µs (1.5%). Neither of the first two is ours.

**Disposition: R1.12 stays OPEN, deliberately.** It is one repaint, not a queue — coalescing means
the next cannot begin until 16 ms after this one ends. The cost is input latency at the bound
(a keystroke waits up to 22 ms rather than 3 ms), plus CPU and battery. Not correctness, not a
stall. Two cheap trades exist and neither was taken: `refreshInterval` 16→33 ms halves the
sustained cost; lowering the 2 MiB ceiling moves the figure proportionally.

**The magnitude is one idle laptop's.** A shared runner would be worse, so the verdict holds but
the number is one machine's.

### 8.3 Everything else

| Property | Before | After |
|---|---|---|
| Token Update alone @ 240 turns | — | p50 1 µs / p99 44 µs |
| Token + forced repaint @ 240 turns | 2.69 ms wrap + 0.61 ms SetContent | p50 3.36 ms / p99 5.39 ms |
| Transcript at 2,000 turns | 1.2 MB text, 6.3 MB heap, 30 MB peak RSS | 502 turns, 335 KB, 2.9 MB heap (on a runner) |
| 1 MB paste | 15.6 ms median | **389 µs**, identical at 0 B and at the 2 MiB ceiling |
| Unchecked errors | 5 | **0** |
| Absolute build paths in a binary | 678 | **0** |
| Rendering determinism | **3 distinct byte strings** for the same state | **1** |
| `-race` suite wall time | 376 s | **154 s** |
| Sanitizer, per clean token | 73 ns / 0 allocs | unchanged |
| Sanitizer, highlighted token | 1833 ns / 216 B / 23 allocs | 828 ns / 24 B / **1 alloc** |
| Lexical query, 1 M chars | 1 m 28.894 s | refused |
| Daemon fuzz targets @ 30 s | **0 execs ×4** | 148k / 118k / 151k / 83k on a runner |
| Daemon package startup | 5.68 s | ~3.2 s |

**The colour-profile finding:** `CLICOLOR_FORCE` set before the first render gave 382 bytes; set
after it, 291. Same state, same width, same environment at the compared render — the difference
was entirely *whether anything had drawn yet*, which in a test binary means *whether another test
ran first*.

---

## 9. THE RESIDUAL RISK REGISTER — 28+ ROWS

Five fields per row: what it is, why deferred, blast radius, the pinning test, the trigger.
**A row with no pinning test says so. A trigger that cannot fire is not a trigger.**

### Still open, with triggers

| Row | What | Trigger | Strength |
|---|---|---|---|
| R1.1 | pty destruction delivers no SIGHUP; the client outlives its terminal. Goroutine 1 parked in Bubble Tea's event loop | The client holding a lock, lease, subscription, spawned subprocess, or anything metered by wall-clock while orphaned | **Strong** |
| R1.2 | Over-long CSI leaks parameter bytes as literal text. No ESC survives either route. Boundary exactly 65/66 | "A terminal emulator that acts on partial sequences" — neither clause observable by anything in this repo | **Weak, flagged** |
| R1.3 | One-shot stdout no longer byte-stable for control sequences. Answer text *is* stable | A user reporting a broken pipeline; the recorded answer is an escape hatch, not pass-through | Reactive |
| R1.4 | Edit-review and approval buffers hold raw bytes by design (disk + digest binding). Sanitized at render | Adding a clipboard binding, transcript export, crash uploader, structured logging of model state, or a second renderer. **The AST guard fires on the code change itself** | **Strong, self-detecting** |
| R1.10 | Locale reaches the input line via `bubbles/textinput`. The transcript is locale-free — `renderTranscript` uses x/ansi `GraphemeWidth`, never runewidth | A CJK user reporting scroll misbehaviour; anything caching/diffing the *input* line; `bubbles` changing width measurement | **Strong** |
| R1.11 | The transcript ceiling can be overshot within one turn (502 vs 500) — the price of index safety | Any change making a single turn produce unbounded turns | **Strong** |
| R1.12 | Repaint at the bound costs 22.4 ms p50 against an 8 ms budget | "Fan noise, battery drain, or laggy typing in a long session" | **Weakest in the register, flagged by its own author** |
| R1.13 | onnxruntime native library and the extension's npm tree are scanned by nothing. `govulncheck` covers six Go modules | "Shipping to anyone who performs a supply-chain review" — **satisfied by the event it is meant to precede** | **Flagged as a note, not a trigger** |
| R1.15 | macOS **DEFERRED BY DECISION**; the five `MACOS_*` secrets do not exist | Self-detecting: the warning fires on every release run until they are added | Self-detecting |
| R1.16 | The gate-scope class, 8 instances (§4) | A ninth instance, or the next gate audit | — |
| R1.17 | An ambiguous request body falls through to "prompt is empty" | Any change to the refusal vocabulary | — |
| R1.18 | Non-UTF-8 files embedded with U+FFFD, silently. `encoding/json` substitutes at marshal *and* decode | A wire-encoding change, or degraded retrieval on a non-UTF-8 codebase — **which will not arrive labelled as this** | Flagged |
| R1.19 | Two tripwires whose failure is **good news** (the encoder stops sanitizing / the library stops panicking) | Tokenizer upgrade, Go `encoding/json` change | — |
| R1.20 | Unbounded refusal `Detail` | **Waits on someone noticing an oversized message** | **Weak, flagged. No pinning test** |
| R1.21 | Warn-mode oracle (§10.2) | D5 is already scheduled and ends warn mode | **Strong** |
| R1.26 | `helper` cannot join any cross-platform check — CGO `onnxruntime_go` makes `GOOS=windows` report "build constraints exclude all Go files". **A platform-specific symbol in a helper test is caught by nothing, anywhere** | — | **Weak, flagged; the row exists in place of a gate** |
| R1.27 | Confinement tests' only execution anywhere is on a host CI relaxes for itself. Cannot be stubbed — they need a real user namespace | `CODETERMINAL_REQUIRE_SANDBOX` fires automatically | Reasonably strong |
| R1.28 | daemon timing literals — **NARROWED**, not closed. Four residuals stated | A timing failure read as flake and re-run | Weak in one direction |
| Class III guard scope | Covers **two enumerated files**. A tool handler in a third file is uncovered until someone adds it. `TestBuiltinToolSurfaceFilesAllExist` catches deletion, not omission | **This is Class I's shape in the author's own gate**; could not be closed without globbing, which would absorb new files silently | **Flagged** |
| Class IV #2 | Sandbox confinement — `Confines()` reports "confined" without probing; `WrapCommand` re-probes and fails closed. Consent wording, not a bypass | — | LOW |
| `go-sdk` zero recover | Four goroutines decoding untrusted MCP stdout. `grep -rn "recover()"` across its `mcp/` returns nothing. **Dependency-policy call** | Next `go-sdk` upgrade, or first unexplained daemon exit with MCP configured | Honest unknown attached |
| 6.4 | Three sites read `s.cfg.NoScrub` directly, bypassing `s.noScrub()`'s nil guard | **Open question: if `s.cfg` cannot be nil, the accessor is the thing that is wrong.** Never established | — |
| 2.3-B | Workspace bound is "> 10,000 files" — **bounded by count is not bounded in size** (M5). 9,999 large files index without limit | First report of daemon memory growth on a workspace under 10,000 files | — |
| F-3 | Chunk content unscrubbed in `lexical.db` at rest (56.7 MB). Retention outlives the source until reindex. **Pinned by `sentinel_rows_test.go` ROW 8 as PRESENT BY DESIGN** — "the index stores what the file says, because retrieval must match the code that is actually there" | — | Owner-adjacent |
| `backup_log.txt` | Deleted from HEAD; the personal email remains in history (`3ff9ee2`) on both remotes | NONE STATED | — |
| Windows frame-budget test | `worst=49.723 ms` against a 48 ms bound (median 5.455 ms). A wall-clock test in a module nobody in this effort touched | **Will flake again** | — |

### Closed

**R1.9** (1 MB paste — closed on a *corrected* figure: the original was taken with the input
blurred, so the paste was discarded) · **R1.14** (TUI not a release artefact — now built on all
three runners with `-trimpath`, in the macOS signing list, asserted *out* of the `.vsix`)

---

## 10. FINDINGS THAT ARE ABOUT CLAIMS, NOT CODE

### 10.1 Doc/code mismatches confirmed (M4)

- A "single retrieval-time choke point for chunk secret scrubbing" — true of the prompt, false as
  a general claim; `logRetrieval` formatted `c.Content` directly
- `RejectUnprintablePath` refusing "control characters" — refused only C0 and DEL
- `SearchTurns` "always wrapped as one literal double-quoted phrase" — a NUL defeats the wrapper
  (FTS5's expression parser stops at the NUL, losing the closing quote)
- `TestPlatformCoverageIsStated` advertised as asserting on 73 files; the code is `if inspected < 20`
- `lint.sh`'s printed remediation did not work
- `build.yml`'s fuzz comment said "Fifteen targets"; the list held 18 — **and that comment's own
  text warns about this exact drift**, having previously said "Nine" while the array held thirteen
- `protocol/peerauth.go:4-7` — **27 references to 6 non-existent filenames across 24 files**
- `daemon/CHUNK_SCRUB_DESIGN.md` — **the code cites it as the decision it implements
  (`scrub.go:79`, "Option A §4") while the document's own status line says "not a decision, no fix
  is written and none is picked here."** Neither side can see the other: one is a `.go` comment,
  the other a `.md` outside the glob
- The memo's own header said four items; there are five (item 5 added 2026-09-09), and `:491` had
  asked for five all along. **That contradiction is how three readers stopped at item 4**

**Go doc comments are checked by no gate.** All three doc gates are `.md`-only.

### 10.2 The warn-mode oracle — an M5 finding about a claim

`warnmode.jsonl`'s `Note` holds `len=41 bits_per_char=5.11` — derived statistics over a suspected
secret, in durable state. `valueIndicator`'s doc says "the value is not recoverable from it."
**True. But not recoverable ≠ not verifiable.**

Measured: the `Note` narrows candidates; the **32-bit truncated SHA-256 `Indicator` verifies one**.
The true value satisfied the record; 39 same-shape candidates were rejected; 0 false accepts.

**Filed as an M5 finding about a claim, not as a plaintext leak.** "Secret-free note" reads as
"this file tells an attacker nothing"; what it guarantees is "this file does not contain the
secret in plaintext." Disclosure and confirmation are different states sharing one phrase.
Collapsing them into "warnmode.jsonl leaks secrets" would be false and would get a real finding
dismissed. Bites only when the value is guessable and the attacker has read access to a 0600 file.

### 10.3 Comments that explain why a gap is acceptable (§5.4 shapes)

- `server.go:555-566` — the scrub-surface enumeration that omits history (§5)
- `server.go:1085` — "raw prompt" justifies raw-vs-augmented, silent on raw-vs-scrubbed, making
  F-1 read as a considered decision
- `jsonlsink.go:72-74` — "worst case it grows slightly past the bound, which is still
  failure-safe." Measured: 151.4× past it, permanently
- The vanished-floor sweep's "a partial run legitimately does not visit them"

**A comment that answers the question before it is asked is harder to audit than a gap with no
comment at all.**

---

## 11. NEGATIVE RESULTS — EQUAL WEIGHT

A report listing only findings reads as though everything examined was broken.

- **Frame handling held under every constructed attack:** overlong UTF-8 (`c0 af`), lone
  continuation bytes, truncated 3-byte sequences, lone surrogates — all normalise to U+FFFD; the
  16 MiB request cap enforced exactly (48 MB refused); leaf and directory symlink escape both
  refused; `BackupSessionDir` traversal (`/etc`, `../../../..`, absolute) creates nothing outside
  the workspace; FTS5 quote-doubling holds against `" OR turns_fts MATCH "`.
- **The helper boundary is clean by construction.** A sentinel in a prompt appeared in none of:
  argv (6 entries), environment (120 — the daemon passes only PATH and HOME), helper stderr, the
  socket response, decode-error logs, temp files. Text arrives over a socket and never as an
  argument.
- **No secret in 638 commits.** 33 pattern hits, every one inspected: test fixtures
  (`AKIA1234567890ABCDEF`), GitHub's own documentation example token, and `scrub.go`'s pattern
  definitions.
- **The audit log's digest survived all four arms** that could have had a fallback — malformed
  JSON, 1 MiB of arguments, non-JSON, empty. SHA-256 cannot fail.
- **`warnsink` is structurally clean.** No field carries the value.
- **No product fail-open found.** ~45 security-decision signatures enumerated and their error
  paths read. `ResolveMode` defaults to `SandboxNone`; `Confines` correctly reports false for it;
  `degraded.go:262` returns degraded on a read error.
- **SQL/FTS5 injection closed by construction.** Zero `Sprintf`-built queries repo-wide. `MATCH ?`
  still parses its bound value as an FTS5 expression, but `buildLexicalQuery` extracts tokens with
  `[A-Za-z0-9_.]+`, which cannot emit `"`, `*`, `(` or `^`.
- **Shell injection closed.** `exec.Command` throughout, never a shell.
- **`AuthorizePeer` is fully implemented** across linux/darwin/windows/other with correct build
  tags; `peerauth_other.go` **fails closed** (returns an error unconditionally).
- **No file-descriptor leak.** All 9 `os.Open`/`OpenFile` sites checked; the 5 without a local
  `defer Close` return the handle to a caller that owns it.
- **Three of four CI failures were gates that already existed in `make check` and had simply not
  been run.** The gate set was well built; the command run was a subset.
- **`clients/tui`'s timing guard needed no change** — correct for what it claimed, and its own doc
  named its limit accurately.
- **`gate-parity.sh` could not be broken by reading.** Recorded as UNTESTED, not as holding.

---

## 12. SELF-CORRECTIONS — THE MOST VALUABLE OUTPUT

Every one of these was volunteered, not caught by review. Each cost credit already given.

1. **The R2 escalation.** Two red daemon tests were reported as pre-existing and not ours, on
   evidence that `git stash` showed them red on a clean tree. **That check was invalid** —
   `git stash` without `-u` leaves untracked files on disk, and the cause was an untracked test
   fixture with `func` at column 0 inside a raw string, which a line-based scanner read as a real
   declaration. The evidence was wrong, not just the conclusion.
2. **The width-1 resize "violation."** Withdrawn — a units bug. Pairs were counted where the
   budget counts turns, so 480 turns were measured and a breach reported at 240.
3. **The zombie watchdog.** Built, then deleted: `/proc/<pid>/stat` survives a zombie, so unreaped
   children read as "still running" and a 200 ms shutdown looked like a 15 s hang. Working code
   guarding a failure that does not occur.
4. **`/compact` measured nothing** — a stream was in flight and `startTurn` refuses commands then.
   3 µs and a transcript never compacted; 193 µs done properly.
5. **The vacuous `%q` test**, relabelled as a forward guard rather than claimed as a fourth hole.
6. **"The memo was handed to the daemon owner"** — it was *committed*, not handed. Four days late,
   unprompted, and it explained a blocked row everyone was reading as inattention.
7. **"The daemon fuzz targets contribute nothing"** — falsified before being stated. 4,006,948
   execs at 2 m. The refined version (a `TestMain` tax consuming the 30 s window and costing ~80%
   of throughput at 2 m) is better than either extreme.
8. **"1.2 million fuzz executions"** in a readiness statement — deleted. Two runs at the same
   `FUZZTIME=10s` gave 899,373 and 1,827,117, a 2× spread. Replaced by a provenance section
   separating deterministic from wall-clock figures.
9. **"CI is green"** citing two runs — both real, both green, **on the fork.**
10. **A correct caveat dropped while fixing a claim** — "there is no build run at HEAD and that is
    not a pass." Restored. *Editing a claim for accuracy is when its neighbours are most at risk.*
11. **"No adversarial input constructed at all" for boundaries 6 and 7** — there are two hostile
    harnesses (`badserver`, 11 modes; `fakelsp`, 12 modes) and 40 passing adversarial tests. The
    correct statement: these are among the *better*-tested surfaces; what they lack is panic
    containment.
12. **The S.2 containment table** said MCP `CallTool`/`ListTools` were covered transitively by
    `handleConn`. The SDK parses hostile stdout on **its own** goroutines. Opposite conclusion,
    not a refinement.
13. **F-1.6 retracted.** A byte-ceiling defect was reported in `loadPersistedHistory`; the failing
    test **passed**. `LoadRecentTurns` runs rows through `prepareHistory` at `memory.go:330`.
    A correct document had been publicly accused of being wrong.
14. **The same shallow trace, twice, on the same call chain** — the second made *while writing the
    retraction of the first*. The durable form: **a function that applies some of a policy is the
    most misleading place to stop, because partial application reads exactly like complete
    application.**
15. **"F-1 survived two adversarial passes"** — false. Both found it. It was found, measured by
    execution, pinned with a tripwire, and argued in a memo.
16. **1.4-B understated** — 5 stale references reported, 27 measured.
17. **A summary that contradicted its own body for 52 days** — an index line said "no durable
    sink, zero data on disk" while the body recorded the fix.
18. **A fail-open in a freshly written fix**, caught by neutering the fix rather than the code.
19. **M8 failed three times in one session at descending scale:** the defect (F-1), a helper
    (`itoa`), and a platform-split helper *whose own comment describes the bug* (`assertOwnerOnly`
    — "five tests once made this mistake"; this was the sixth).

**Running false-positive rate: 1 in 6 (17%)** on findings put to a falsifying test.

**Kept separate (M5):** one defect *introduced* on a true finding and caught by a Windows runner.
That is faulty work on a true finding, not a false finding. Conflating them would flatter the
first number.

---

## 13. WHAT WAS NEVER VERIFIED

This section grew 8 → 10 → 12 → 16 → 19 → 26 → 31 as the work closed. **That is the right
direction.** A short version of this section is a signal about the report, not about the codebase.

### Requires execution, a host, credentials, load, or fuzzing

1. **No human has ever driven the client by hand.** Every number in every pass is from a harness.
2. Terminal restore is verified on **Linux only** — the pty suites are `//go:build linux`, so
   macOS and Windows never compile them **at any trigger**. This is a build-tag gap, not a trigger
   gap; no scheduling change touches it. Closing it means writing a darwin pty helper.
3. Windows peer auth (DACL + `GetNamedPipeClientProcessId`) — `crossvet` only compiles.
4. Windows subprocess reaping (`procgroup_windows.go`).
5. macOS `LOCAL_PEERCRED`, `sun_path` 104, 8 KiB sockbuf, EEXIST — CI runs macOS beyond
   `clients/tui` only on `main`.
6. `BwrapUsable()` on a stock Ubuntu 24.04 with `apparmor_restrict_unprivileged_userns=1`. **The
   sandbox's failing branch has never been observed anywhere** — the dev box reads 0 and CI sets 0
   for itself. Thirty seconds for whoever hits it.
7. The Docker sandbox backend.
8. Proxy ZDR/F1 enforcement on the wire; proxy key auth, rate limiting, quota/outbox rows.
9. Whether **any input panics `go-sdk`** — the unknown that decides whether four missing recovers
   are theoretical or urgent. Needs fuzzing a third-party parser.
10. Cross-process `flock` apply/undo serialisation with two real processes.
11. Behaviour under load — the 22.4 ms repaint, every timing bound.
12. Corruption/truncation/absence of `memory.db`, `lexical.db`, the vector index.
13. Partial failure — step 3 failing after 1 and 2 commit.
14. The 13 VS Code Extension-Development-Host suites; `tsc` over 27 `.ts` files.
15. `release.yml`'s **release creation and asset upload** — a dispatch is not a tag.
16. A **`SIGNED` marker produced outside a test** — every release run to date took an unsigned
    branch.
17. TypeScript and Python LSP paths — `typescript-language-server` and `pyright-langserver` are
    not installed.

### Structural and inventory limits

18. **The full-codebase audit read 7% of the repository.** 11 files read closely, ~37 skimmed,
    ~615 never opened. **Nothing in it licenses a claim that the other 93% is clean.**
19. **320 of 321 test files were never opened.** This is where the evidence actually lives —
    vacuity floors, neuter results, the fail-when-neutered property. Two floors were checked and
    extrapolated; **that extrapolation is unearned across the other 319.**
20. **28,715 ignored files** — `node_modules` (23,652) and `.vscode-test` (6,200) never enumerated
    individually. **A supply-chain finding would live there.**
21. `helper` is unobservable cross-platform by any gate, anywhere (CGO).
22. `gate-parity`'s three vacuity floors — reasoned sound, never tested by trying to break them.
23. Per-gate counts do not reach the CI log: `build.yml:298` runs `go test -race -count=1 ./...`
    without `-v`. Non-vacuity rests on the tests' own floors and on local neutering.
24. `make check` in one invocation was piped through `tail -12`; exit 0 is authoritative but that
    run's per-gate output was destroyed.
25. Coverage floors not raised where the measurement is environment-dependent.
26. Timing and cache-hit oracles (§3.4 of the audit) were **not measured at all** — the weakest
    sweep in the pass, stated as such.
27. Every finding in the read-only audit is **(R)**, and the standing counter-example is that
    context threading *looked* correct while `sqlite3_interrupt` was not polled inside an FTS5
    phrase match. **Visibly correct plumbing can lack the property it appears to have.**

---

## 14. HOW THE GATES WORK

### 14.1 `make check` — 14 targets, one invocation, exit 0

```
hookcheck → gofmt → vet (incl. -tags eval, -tags warnscan) → crossvet (windows+darwin × 5 modules)
→ race (9 packages, 6 modules, -count=1) → lint (18 checks) → ratchet (9 packages, no-arg:
vanished-floor sweep runs) → errcheck (ceiling 0) → evalguard → supplychain (6 tidy, 4 trimpath,
govulncheck 6 modules) → webview → docs (326 links, 63 coderefs, claims) → debtmarkers (156 files)
→ parity (22 scripts)
```

**Excluded and named by the closing banner:** `fuzz.sh`, macOS signing, `install-tools.sh`, real
Windows/macOS execution, the EDH, the container, the full soak.

`make check` **reports what it did not run** — half derived from the parity manifest (so a gate
becoming CI-only appears with no edit), half capability-based (no Windows runner, no macOS, no
docker, no EDH) and stated as underivable.

### 14.2 `gate-parity.sh` — the gate on the gates

Derives both sides (`make -n check` for local; every `scripts/*.sh` named in the workflows for CI)
rather than hand-listing. Three checks: a script with no manifest entry → red; a manifest entry
naming a deleted script → red (**the anti-Class-I property**); observed side ≠ declared side →
red. Mandatory reason for every asymmetry. Three vacuity floors refusing to compare an empty set.

**On its first run it found three real divergences and itself** — `debt-markers.sh` and
`supply-chain.sh` ran only in CI with no make target, and `gate-parity.sh` was on neither side.

### 14.3 Per-platform coverage

| Platform | Builds | Test suite | pty-backed | Signal/restore | Trigger |
|---|---|---|---|---|---|
| Linux | pass | 327 pass, `-race` | **14 pass** | **12 pass** | every push |
| macOS | pass | 313 pass, no `-race` | **0 run — 14 not compiled** | **6 pass, 6 not compiled** | every push (`clients/tui`); `cross` on main/dispatch |
| Windows | pass | 307 pass, no `-race` | **0 run — 14 not compiled** | **0 run — 12 not compiled** | every push |

**Windows's zero is correct** — `exitsignals_windows.go` is two no-op stubs; the platform has no
SIGHUP and no POSIX SIGTERM. **macOS's six is the real gap.**

A runtime banner prints in the failing platform's own CI log:

```
PLATFORM COVERAGE on windows/amd64: 154 of 179 test files compiled; 25 DID NOT RUN here:
  NOT RUN  addrinuse_unix_test.go
  ...
```

Derived via `go/build` `MatchFile`, not a hand list — the difference from the `clients/tui`
original, which cannot see a renamed file.

**Why one macOS job and not the whole matrix**, measured not estimated: Linux 1× / Windows 2× /
macOS 10×. 22 Linux jobs = 74 billable min, 4 Windows = 18, 4 macOS = **70**; 162 total against 92
without. All four on every push takes the free plan from ~21 pushes/month to ~12. One job costs
20 minutes — **29% of the price, where the risk is.**

---

## 15. WHAT REMAINS

### 15.1 Nothing is blocked on anyone

The release gate is closed. All five memo items carry recorded verdicts. No row is waiting on a
person.

### 15.2 Deferred with triggers — act when a trigger fires

- **`go-sdk`'s four uncovered goroutines** — dependency policy. Trigger: next upgrade, or the
  first unexplained daemon exit with MCP configured. Honest unknown: whether any input panics it
  was never established.
- **Class III guard covers two enumerated files** — a handler in a third file is uncovered.
- **6.4** — three sites bypass the nil guard; the open question is whether `s.cfg` can be nil.
- **R1.12** — 22.4 ms repaint; two cheap trades named and not taken.
- **R1.26** — `helper` uncatchable cross-platform.
- **2.3-B** — workspace bound by count, not size.
- **The five `MACOS_*` secrets** — macOS rejoins with zero edits the day they land.
- **The Windows frame-budget test** will flake again. Someone else's module; the bound was
  deliberately not raised.

### 15.3 Genuinely unscheduled

- **An hour of a person's time at a terminal**, on Linux. `docs/MANUAL_SESSION_2026-09-04.md`
  scripts it: long session past the ceiling, scroll back mid-stream, a ~1 MB paste into a deep
  transcript, an approval, the eviction marker read rather than merely observed, quit four ways.
  **The point is what a person notices that the harness cannot.** The worked example to give
  them: `refreshViewport` ended in an unconditional `GotoBottom`, so scrolling back during a
  stream was impossible — eight wheel-ups reach `YOffset` 187 and one token snaps it back. **No
  test caught it because no test scrolled during a stream.** A person finds that in ninety
  seconds.
- **Boundaries 6 and 7** (MCP stdout/stderr, LSP/gopls). The read-only recon recommended **not**
  scheduling a full pass: two hostile harnesses and 40 adversarial tests already guard them, and
  input validation is good on both. What they lack is panic containment, one half of which is now
  fixed. **The reachability inversion matters:** MCP requires a user to install a malicious server
  *and* set `acknowledged_unconfined: true`; **LSP requires a user to open a repo**, since gopls
  parses attacker-authored source. If only one gets attention it should be 7. The one case
  predicted to find something: `lsp_bridge.go:491`'s `ReadString('\n')` bounds neither a single
  header line nor the number of header lines.
- **Coverage floors** — three uncached runs on `main` before any floor moves. `helper`'s 61.5% is
  environment-dependent and must not be raised from a host with the model cached.

---

## 16. FOR THE NEXT SESSION — THE SHORT BRIEFING

**If you are an agent picking this up, these ten things matter most:**

1. **Nothing closes on reading code.** Write the failing test, run it on HEAD, record the output,
   then fix. This rule killed a fix for a defect that did not exist, on its first use.
2. **Neuter everything, including your own new gates.** Three shipping defects were caught this
   way and by nothing else.
3. **Search for prior art before concluding anything is absent.** This failed five times in one
   session, including on the headline finding itself.
4. **Trace to the end of the call chain.** A function that applies *some* of a policy is the most
   misleading place to stop, because partial application reads exactly like complete application.
   This error was made twice on the same chain, the second time while retracting the first.
5. **Name the remote, the run id and the commit** for every CI claim. Two remotes carry the same
   workflow names.
6. **State scope with every gate result.** "Exit 0" without the invocation is meaningless.
7. **Label every number DETERMINISTIC or WALL-CLOCK** with its provenance. Two wall-clock
   artefacts have been presented as measurements in this project's history.
8. **Never let two states share a phrase.** The list in §3 is not exhaustive; add to it.
9. **Correct yourself out loud and shrink claims that overstate their evidence.** Nineteen
   instances are recorded in §12, and each was more valuable than the finding it replaced.
10. **"What I did not verify" should grow as work closes.** If yours is short, that is a signal
    about your report.

**The single most transferable lesson:** the root cause of the highest-severity defect surviving
five weeks was not technical. It was that a document existed, was correct, was committed, and
nobody had been told. **A document nobody was told about is indistinguishable from one that does
not exist.**

---

## APPENDIX A — KEY FILE LOCATIONS

```
daemon/server.go:567        scrub for the wire
daemon/server.go:586        the redaction notice (now true)
daemon/server.go:707        persistTurn — one of TWO callers
daemon/agentturn.go:284     the other caller
daemon/server.go:385        PersistedHistory handshake payload
daemon/server.go:615        buildChatMessages — three inputs
daemon/history.go:113       validTurn
daemon/history.go:130-135   the residual-stated-not-hidden counter-example
daemon/memory.go:330        LoadRecentTurns → prepareHistory
daemon/search.go:128        SearchTurns — the third path
daemon/scrub.go:79          cites CHUNK_SCRUB_DESIGN.md §4
daemon/mcpbuiltin.go:367    Class III ①
daemon/mcpbuiltin.go:403    Class III ②
daemon/mcp_ast_edit.go:141  Class III ③
daemon/jsonlsink.go:83      the best-effort rename
daemon/mcpruntime.go:206    the connect goroutine (now recovered)
daemon/mcp/sandbox.go:374   ResolveMode — does not probe
daemon/mcp/sandbox.go:415   WrapCommand — probes, fails closed
daemon/lsp_bridge.go:491    ReadString('\n') — unbounded header (open)
daemon/fileref.go:353       the Class III antidote
daemon/chunker.go:275       the Class III antidote
daemon/planmode.go:59-64    "the read side must not assume the write side ran"
daemon/requestfields.go     "must not be guessed at, least of all into a destructive handler"
daemon/sentinel_rows_test.go  ROW 7 (now a control), ROW 8 (by design)
editapply/pathhazard.go:198 unicode.IsControl
protocol/peerauth.go:4-7    the corrected header
clients/tui/chat.go:1457    appendTurn — the only door into m.turns
clients/tui/main.go:103     WithMouseCellMotion (ADR-001)
scripts/gate-parity.sh      the gate on the gates
scripts/fuzz.sh             18 targets
docs/DECISION_MEMO_2026-09-04.md   five items, all now decided
docs/RESIDUAL_RISKS.md             28+ rows
docs/HANDBACK_2026-09-12.md
docs/BRANCH_STATUS_2026-09-05.md
docs/ADVERSARIAL_PASS_2026-09-09.md
docs/MANUAL_SESSION_2026-09-04.md  the script for a human tester
```

## APPENDIX B — COMMANDS

```bash
export PATH=$PATH:~/.local/go/bin:~/go/bin

make check                                    # 14 gates, one invocation, scope in the banner
./scripts/lint.sh clients/tui                 # staticcheck + ineffassign + bodyclose
./scripts/coverage-ratchet.sh                 # NO ARGS = vanished-floor sweep runs
./scripts/coverage-ratchet.sh clients/tui     # per module; sweep SKIPPED
./scripts/errcheck-ceiling.sh clients/tui     # ceiling 0
./scripts/gate-parity.sh                      # 22 scripts accounted for
./scripts/docs-coderefs.sh                    # enforced documents only
cd clients/tui && govulncheck ./...

go test -count=1 -race ./clients/tui/...
go test -run=NONE -bench=. -benchmem ./clients/tui/...
GOOS=windows go vet ./...                      # catches the Mkfifo class
go list -f '{{len .TestGoFiles}}' ./clients/tui

gh run list --repo Rav-2007/codeterminal-core  # gh resolves to origin without --repo
git push upstream audit/adversarial-pass       # upstream = where CI runs

sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0   # if bwrap tests fail locally
```

---

*Checkpoint ends. HEAD `1013e1b`, tree clean, CI green on `Rav-2007/codeterminal-core`
(`build 34739694099`, `gates 34739694106`), release gate closed, five verdicts recorded against
Ravi Kiran, 2026-09-13.*
