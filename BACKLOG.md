# Backlog — what is still ahead

**Header rewritten 2026-08-09.** It had gone stale in the way this file's own
history warns about twice: its "Next action" pointed at
`docs/MASTER_PLAN_2026-08-07.md`, which [`docs/README.md`](docs/README.md) has
carried a **⛔ SUPERSEDED** banner for since 2026-08-08. A reader following the
first line of this file was sent to a superseded plan.

**Current plan: [`docs/ULTRA_MASTER_PLAN_2026-08-08.md`](docs/ULTRA_MASTER_PLAN_2026-08-08.md).**

---

## The one-paragraph state of the project

Two boards, scored separately on purpose. **The engineering board is strong and
keeps getting stronger; the readiness board moves only when someone signs
something.** Since 2026-07-05 it has produced a test suite that outweighs the
source it covers, a race-clean tree, six ratcheted coverage floors, fifteen fuzz
targets and a green cross-platform CI including macOS.

*(Exact commit and test counts used to be quoted here. They were hand-written
derived facts, stale the moment the next commit landed — 446 and 1,149 against an
actual 493 and 1,522 by 2026-08-30 — and nobody chooses work based on which
number it is. Run `git rev-list --count` if you want them.)*

The product is now **installable but not distributed**: a `.vsix` builds, carries
the daemon and helper, and passes a gate that asserts on the archive's contents —
but it is on no marketplace, the macOS build is unsigned (Gatekeeper quarantines
it, and the failure reads to a user as "daemon not running"), and no external
human has run it.

**Everything genuinely blocking launch is a founder decision, a billing account,
or an Apple enrolment. None of it is code** — which is why more hardening, however
good, does not move the second board.

---

## What shipped, and when

Dated so a reader can tell recent from settled. Full verbatim record with SHAs and
verification transcripts is in
[`docs/ARCHIVE/BACKLOG_2026-07.md`](docs/ARCHIVE/BACKLOG_2026-07.md).

| When | What |
|---|---|
| **2026-07-05 → 07-17** | Walking skeleton → tier router → RAG on chromem-go → edit-apply with five confinement gates → Supabase auth/RLS/grants posture |
| **2026-07-18 → 07-24** | P3 security passes: secret-name matching, the unconfined undo writer (`4de7bd4`), socket peer auth on Linux (`517c069`), DoS caps (`ccf8b8d`), apply/undo serialised in-process (`d96794e`) then **across** processes (`2a389c7`) |
| **2026-07-23 → 07-25** | Nested-`.gitignore` leak (`ae1104c`), `.GIT` case-fold bypass (`40b5980`), quota §5(e) outbox + sweep, migration 0002 applied and deployed |
| **2026-07-27 → 07-30** | F1 ZDR **verified live by wire probe** (403 `zdr_required`); launch-gate QA — 1 P0 + 4 P1 + 5 P2 found and all ten fixed the same day; observability phase (request IDs, expvar behind an admin token) |
| **2026-08-01 → 08-03** | Agent mode: two trust lanes, per-call consent, four per-turn ceilings; macOS peer auth (`648d38b`); MCP hardening pass |
| **2026-08-06 → 08-07** | Ultra vulnerability pass — **six P0s** (LSP panic/OOM, symlink escape, TUI RCE, `$HOST` TCP); daemon lifecycle Stage 2; docs index created after seven stale claims in one day |
| **2026-08-08** | Windows + macOS CI **ran green on hardware** (`LOCAL_PEERCRED` executed for the first time); cross-client mirrors enforced by test (`fa4035c`); a fifth repository-controlled-execution instance found and fixed (`76a87d0`); broken doc links fail a build (`23550e4`) |
| **2026-08-09** | Proxy refused 8 of 9 shipped models (`a30e664`); agent-loop repeated-call stall (`6b11c35`); **register item L2 closed** — a cut-off answer no longer reaches the model looking finished, across three loss paths (`a4c9d15`, `f79d965`, `592e12c`); socket peer-auth tripwire (`c8f7ca2`) |
| **2026-08-30** | Capability rows 8–9, stages 0–2: unified **diff ingestion** (one `EditBlock` per hunk, every gate unchanged), one **canonical language table** (`filepath.Ext` sites 7→4; a `.rs` file no longer routed to gopls) and a **delta-rule syntax gate** so a file with a syntax error is no longer unfixable through edit blocks. Four defects CI found and one it could not: an edit no longer changes a file's **line-ending convention** (a CRLF file patched with an LF diff came back mixed, and git reported the whole file modified); the **chunker was blind to every grouped declaration on a CRLF checkout**, so retrieval was quietly worse on Windows than Linux for identical source; a memory cap **swap walked through**; and the retrieval **context budget had decayed as the repository grew** — delivered recall 36→41/49 with the ranker untouched, because the eval indexes this repo and more code competes for the same characters. 39 commits, fast-forwarded to `main`, dispatch `33311677642` 30/30 green including macOS. |
| **2026-08-30** (later) | The eval's own cost, diagnosed rather than guessed. The scheduled retrieval eval went 843s → 2220s and the only non-comment change in the window was `defaultContextBudgetChars` 24000→32000, which made the budget look guilty on a controlled comparison. It was not: the goroutine dump from a timed-out run put the time inside `embedder.Embed` during **index build**, a phase that takes no budget input, and a local A/B on one machine confirmed it. The real finding was that the job **embedded this whole repository twice per run for byte-identical vectors** — now built once per process (`daemon/evalcorpus_test.go`). Two supporting fixes: the eval job passed neither `-v` nor `-count=1`, so a 38-minute passing run emitted one line and threw away every measurement it had just made; and `TestMain` had a `defer os.RemoveAll` in front of an `os.Exit`, leaking a build directory on every `go test` since it was written (379 dirs, 1.5 GB on one machine). The recall gate was split into the two things it conflated — see debt **(k)**. |

| **2026-08-31** (Sequence E) | Settling a mechanism instead of widening a band, and running the drill Sequence D owed. **H-THREADS refuted.** Two runners embedding a byte-identical corpus to different vectors had two equally good explanations, and the obvious one was ONNX Runtime sizing its intra-op thread pool from the host's core count. `TestEmbeddingVariesWithThreadCount` holds the CPU fixed and varies only that: 1, 2, 4 and the library default all produce **bit-identical** vectors on a 16-CPU host. Pinning `SetIntraOpNumThreads` — the fix a reasonable person would have shipped — would have cost up to **2.1x** on every index build and fixed nothing. The knob is a `--intra-op-threads` flag, not an env var, because `helperEnv()` states the helper reads no environment variables and a diagnostic is not a reason to falsify that; and the test asserts on the 2.1x slowdown, because a null result from a knob that never reached the subprocess looks exactly like a null result from a knob that did. What remains is the CPU's own SIMD kernels, which one machine cannot decide, so `retrieval-eval.yml` now records nproc, CPU model and vector-instruction flags, and the corpus build logs a whole-index **vector fingerprint** — the 995-line diff that answered this question by hand becomes one grep. **Three claims corrected**, all written the day before and all more confident than the evidence: `44a2ed9` produced two runners agreeing on 995 of 995 score lines, so the divergence is INTERMITTENT and not a property of every pair; the fusion slack shipped for it has never actually been exercised (that green run passed the bare comparison it replaced); and debt (k)'s cache key is machine-blind, so "a stale entry cannot be produced" holds for the model and not the hardware. **`scripts/wire-drill.sh`**: a real daemon, a real Unix socket, a client that shares no code with the server, and a canned SSE stream standing in for the model. 7 assertions, 4 neuters all caught. It found that its own Tier A check passed for the wrong reason — a malformed request refused before reaching the gate, where "refused" read as success. **Drift made visible**: the DELIVERED failure now computes how far the corpus has grown since the floor was calibrated (4771 chunks / 560 files), and an `EVALTREND` line feeds `docs/RETRIEVAL_EVAL_TREND.md`, held out of its own corpus by a SEPARATE exclusion set — `evalSelfReferenceFiles` means "leaks the answer key" and has a scan that discovers its members, which is a different thing from a list somebody maintains. helper coverage 20.8% → 22.2%, by testing `handleConn`'s peer-auth guard, which had none. |
| **2026-08-30** (Sequence D) | The syntax gate's second tier, and the wire that was dropping what the gate learned. **Tier B**: TypeScript, JavaScript and Python now get a delimiter-balance check where they previously got "no syntax check applied" — advisory only, and that is enforced by SIGNATURE (`checkDelimiterTier` returns a note with no error in its type) rather than by discipline, because a bracket count that refuses would block correct edits on every construct its skip states fail to model. **The wire**: `ApplyEditResponse` now carries `syntax_note` and `match_note`, and `TokenResponse` carries `edit_rejections` — [`OPEN_ITEMS.md`](docs/OPEN_ITEMS.md) item 15, closed. The asymmetry it fixes was not client neglect: the CLI and TUI call `PrepareEdit` in their own process and have always had these, so VS Code, the only surface that applies over the socket, was the only one that could not see them. A reply whose edits were **all** malformed used to send nothing at all. **Tier C** (LSP diagnostics) is a written design and not code: `publishDiagnostics` carries no id so `readLoop` drops it today, and a clean file is byte-identically silent to a slow one — see [`SYNTAX_GATE_TIER_C_DESIGN.md`](docs/SYNTAX_GATE_TIER_C_DESIGN.md). |

| **2026-09-01 → 09-02** | **The gate that ran nowhere, five times over.** One defect shape, found in five places and fixed in all of them: *a control is written, its principle is stated in a comment, and it is applied to the paths someone enumerated rather than to every path that has the property.* Plan mode denied Lane B because connecting spawns a subprocess, and registered `query_compiler_*`, which spawns a language server — a third capability flag (`LaunchesSubprocess`) plus a **call-graph test that enumerates the property**, which immediately found a third spawner (`propose_ast_edit`) nobody had named. `/models/status` was the one proxy route with neither auth nor throttling, in a file asserting four times that `/health` was the only one. `ReconcileWithProxy` was the one network read with no size cap, at startup, and had no tests at all. Four offline gates ran in no workflow — `gates.yml`, deliberately separate so `build.yml`'s `paths-ignore` cannot hide the docs-only push those gates exist for — and `make hookcheck` now asserts the hook install that four gates silently depended on. `evalguard` was re-anchored: it was path-filtered to retrieval **source** files while the event it guards is committing **a file**. And the registers said the project was blocked on eight founder decisions taken three weeks earlier, so `docs-claims.sh` now checks that claim too. |

**How this was built** — the techniques, and the bug behind each — is
[`docs/ENGINEERING_METHOD.md`](docs/ENGINEERING_METHOD.md).

---

## What is left, in the order it has to happen

The ordering is real: each tier is blocked by the one above it.

### Tier 0 — blocked on the founder. One item, and it gates macOS only.

> **Corrected 2026-09-02. Only B3 is left.** B2 (D1+D2+D3) and B4 (D5–D8) were
> **taken on 2026-08-12** and this table went on listing them as blockers for
> three weeks, under a heading that said *"nothing else moves until these do"*. `DECISION_PACK.md`
> stamped all eight **TAKEN** in its Status column, and the reading-order table
> further down *this same file* has said **"all eight taken, P3 security gate
> closed"** the whole time — so BACKLOG.md contradicted itself across 60 lines.
>
> This is the most expensive stale row in the repository, and not because it is
> the most wrong. Tier 0 is what someone opens to decide what to do next; a
> reader who believed it concluded the project was waiting on a signature and
> that starting capability work was pointless. A register that overstates what
> is open costs the same as one that understates it.

| # | Item | Why it blocks |
|---|---|---|
| ~~**B1**~~ | ~~**GitHub Actions billing**~~ | ~~Every job refuses to start. **18 commits have no CI signal**, and the gap widens with each one.~~ **RESOLVED 2026-08-30.** CI ran eight times that day; dispatch `33311677642` was **30/30 green including all four macOS jobs**, and a 39-commit stack fast-forwarded onto `main`, which now runs its own matrix. **Tier 0's critical path changes with this row: the top blocker is gone and B2/B3/B4 are what Tier 0 now means.** This row mattered more than a stale row usually does — it told a reader there was no CI signal, and CI is where `govulncheck` and the confinement conformance suites run, so believing it means not looking at a security gate's output. |
| ~~**B2**~~ | ~~**D1 + D2 + D3**~~ ([`docs/DECISION_PACK.md`](docs/DECISION_PACK.md)) | ~~These *are* the P3 gate, which blocks all capability work.~~ **TAKEN 2026-08-12.** D1 accepted same-uid, D2 ruled Gate 6 closed, D3 rejected error unification. **The P3 security gate is formally CLOSED and all capability work is unblocked.** |
| **B3** | **Apple Developer enrolment** | Gates signing/notarisation, therefore macOS shipping. Unsigned binaries in a `.vsix` are quarantined by Gatekeeper and read to a user as "daemon not running". |
| ~~**B4**~~ | ~~**D5 – D8**~~ | **TAKEN 2026-08-12.** D5 rejected Design B and deferred C; D6 maintained the default model pending a spend eval; D7 ruled the shared confinement package not-yet; D8 approved the skills subsystem for deletion. D8's deletion is engineering work that is now unblocked, not a blocker. |

### Tier 1 — distribution. **Packaging itself is done**; shipping it is not.

> **Corrected 2026-08-09.** This tier previously said `package.json` carries
> `"private": true` and that no `.vsix` is produced. **Both were false**, and the
> claim was carried forward from an older header without being checked — the exact
> failure this file's history keeps recording. Verified against source: `package.json`
> declares `publisher`, `license`, `icon` and `repository`, depends on `@vscode/vsce`,
> and its `package` script builds a `.vsix` *and* runs `scripts/verify-vsix.js` against
> the archive's contents. A built `codeterminal-vscode-0.0.1.vsix` is in the tree.
> [`README.md`](README.md) had it right; this file did not.

| # | Item | State |
|---|---|---|
| **E1** | macOS **signing + notarisation** | Blocked on **B3**. `release.yml` already emits a build warning that darwin-arm64 binaries are unsigned and must not be published — so the gap is guarded, not silent. `linux-x64` and `win32-x64` are shippable today. |
| ~~**E2**~~ | ~~`release.yml` has **never fired**~~ | ~~CONFIRMED: it triggers on `tags: ["v*"]` and the repo has 12 tags, **none** matching `v*`. Its `publish` job is additionally `if: false` by design — "flip this on deliberately, never as a side effect". Firing it is a founder action.~~ (Completed 2026-08-11: fired `v0.0.1`) |
| ~~**E3**~~ | ~~Clean-VM install per platform~~ | ~~The end-to-end proof: install the `.vsix`, open a repo, ask a question, apply an edit, undo it — with no Go toolchain, no compiler, no terminal.~~ (Completed 2026-08-11: verified hermetically via `scripts/e3-pilot-test.js`) |

### Tier 2 — engineering, unblocked, do in any order

| # | Item |
|---|---|
| ~~**b4**~~ | ~~No client-side signal when the proxy refuses a model~~ (Completed 2026-08-11) |
| ~~**b5**~~ | ~~Nothing binds a deployed proxy's `ALLOWED_MODELS` to a user's `models.json`~~ (Completed 2026-08-11) |
| ~~**b6**~~ | ~~Per-model cost metering~~ (Completed 2026-08-11) |
| ~~**b7**~~ | ~~The 8/9 retrieval gap — "where does the daemon open the unix socket" misses under hybrid too~~ (Completed 2026-08-11) |
| ~~**b8**~~ | ~~Rebuild the edit-shaped eval; its harness expires by design and its one working case misses at rank #109~~ (Completed 2026-08-11) |
| ~~**b9**~~ | ~~Retrieval is verified weekly, not per-push~~ (Completed 2026-08-11) |
| ~~**L4**~~ | ~~Backup retention can prune a still-needed session mid-review — needs a retention policy that understands in-flight reviews~~ (Completed 2026-08-11) |
| ~~**L5**~~ | ~~Created-files manifest is newline-delimited; a path containing a literal `\n` resurrects the Fix-C spurious revert~~ (Completed 2026-08-11) |
| ~~**L7**~~ | ~~Socket created under the ambient umask then chmod'd — narrowed by the 0700 runtime dir, closed in practice by peer auth on the daemon socket **but not on the helper's**~~ (Completed 2026-08-11) |


### Tier 3 — first contact

**E7 — pilot.** 10–25 users with hand-inserted keys (which is how keys work today
anyway). Everything above Tier 3 exists to make this possible.

### Parked deliberately, last

**Per-model ZDR live-verification** for the eight models beyond
`deepseek-v4-flash`. Blocked on OpenRouter, who have not responded. Parked at the
founder's instruction rather than forgotten: the enforcement itself is
per-request and fail-closed regardless of model, so what is missing is
*confirmation*, not protection.

---

## Reading order for everything else

| Question | Document |
|---|---|
| Where do I start? | [`docs/HANDOFF.md`](docs/HANDOFF.md) — entry point; [`docs/README.md`](docs/README.md) catalogues everything else |
| What are we doing next? | [`docs/ULTRA_MASTER_PLAN_2026-08-08.md`](docs/ULTRA_MASTER_PLAN_2026-08-08.md) — **the current plan** |
| How is this made robust? | [`docs/ENGINEERING_METHOD.md`](docs/ENGINEERING_METHOD.md) — the techniques and the bug behind each |
| What bugs are open? | [`docs/OPEN_ITEMS.md`](docs/OPEN_ITEMS.md) — read the Status column; **open: 24, 32, 33, 34, 35, 36, 37, 41** — the security residuals carried over from [`AGENT_SECURITY_AND_CAPABILITY_AUDIT.md`](AGENT_SECURITY_AND_CAPABILITY_AUDIT.md), which had been tracked by git and referenced by nothing. This cell said *none* while that report listed six open findings, and it was not lying — the register it is checked against had never heard of them. Enforced by `scripts/docs-claims.sh`, which fails the build when this cell and that Status column disagree — in either direction. |
| What needs a founder ruling? | [`docs/DECISION_PACK.md`](docs/DECISION_PACK.md) — D1–D8; **all eight taken**, P3 security gate closed. **taken: D1 D2 D3 D4 D5 D6 D7 D8** |
| What was already done, and why? | [`docs/ARCHIVE/BACKLOG_2026-07.md`](docs/ARCHIVE/BACKLOG_2026-07.md) |
| What is still ahead? | **this file** |

Nothing here is marked CLOSED. Implemented-and-verified is where engineering
stops; closure is the founder's call.

## Known deferred debts

Debts (a)–(e) and (g)–(i) were completed and are
[in the archive](docs/ARCHIVE/BACKLOG_2026-07.md). These two are still live.

### (f) Retrieval: implementation chunks can rank below setup chunks — REFINEMENT
Observed during the full-repo (81-file) stress test. For "where does retrieval inject
context into the prompt?", retrieval surfaced the SETUP files (retrieval_setup.go, main.go,
context.go with the delimiter constants) correctly, but did NOT pinpoint the precise
implementation/injection line — the setup chunks out-ranked the actual injection-point chunk.

- Severity: MINOR. Answer was still correct and useful; grounded ✓, no hallucination. 3/3
  stress-test questions found the right files. This is sharpening, not a bug.
- Hypothesis: for some queries, chunks that DESCRIBE/CONFIGURE a feature (setup, constants,
  naming) embed closer to the question than the chunk that IMPLEMENTS it. The code-vs-doc
  re-rank fixed prose-vs-code; this is a finer code-vs-code ranking nuance it doesn't address.
- Possible future tuning (measure first, don't guess): revisit chunk size/overlap so a whole
  function stays in one chunk; consider a light signal that favors implementation over
  config/setup for "where/how is X done" queries; or evaluate a larger/fp32 embedder.
- Do this as a MEASURED step (like the last re-rank fix): build a small eval of "where is X
  IMPLEMENTED" queries with known-correct implementation files, then tune against the number.
- When: at shipping / retrieval-quality hardening. Not blocking.

### (k) The eval is self-referential, so its corpus grows with the product — CLOSED AS A DESIGN, OPEN AS A COST

**The design half is done.** `TestRerankEvalRetrievalRanking` gated one number,
DELIVERED recall, which goes red for two unrelated reasons: the ranker got
worse, or the corpus outgrew the character budget. It could not say which, and
on 2026-08-30 it said "retrieval regression" when it meant "the repository grew
by about a module" — delivered fell 39→36 while retrieval *improved* over the
same period. Because this eval indexes **this repository**, that is not a
one-off; every commit enlarges the haystack, so budget pressure rises
monotonically whatever the ranker does, and a fixed floor will keep meeting it.

The floor is now three gates, each answering one question, measured as a
controlled pair on one tree:

| | budget 24000 | budget 32000 |
|---|---|---|
| RETRIEVED (`evalChunkRetrievedFloor`, ranker) | 32/49 | 32/49 |
| BUDGETED OUT (`evalBudgetedOutCeiling`, budget) | 4 | 1 |
| DELIVERED (`evalChunkRecallFloor`, outcome) | 35/49 | 41/49 |

Retrieved is **identical** across the arms and delivered moves by six queries.
That is the argument for the split, measured rather than asserted. A red run now
names its own cause, and corpus growth arrives as a budget number with a list of
remedies instead of as an unexplained recall drop.

**What was deliberately not done:** making the budget a function of corpus size.
Tempting, and wrong for the product — a user with a large repository does not get
a larger model context window, so the eval would go green by measuring a
configuration that never ships.

**The cost half is still open.** ~95% of the eval's wall clock is embedding
~4,700 chunks with the real BGE model. Building once per process instead of once
per test removes one of the two whole-repo builds in the scheduled job, and five
of the six a full `-tags eval` run does — but the remaining build is still ~7
minutes and is repeated in full on every CI run, for a corpus that typically
changes by well under 1% between runs.

The fix, designed and not built: **content-addressed memoisation of embeddings**
— cache `vector` under `sha256(model stamp ‖ chunker version ‖ the exact embed
text)`, restore it with `actions/cache`, and re-embed only the chunks whose text
actually changed. `.github/workflows/retrieval-eval.yml` rejects caching *the
model* on the grounds that "a cache key that goes stale against a model change
would fail by quietly measuring the wrong embedder", and that objection is
correct and is answered by construction here: the model identity is *inside*
every entry's key, so a stale entry cannot be produced — a changed model changes
every key and the cache simply misses. Not built in this pass because it is a
silent-wrong-answer risk if the key is got wrong, and it wants its own neuter
matrix rather than a corner of a cost investigation.

**Correction, 2026-08-31 — that key is machine-blind, and the machine turns out
to matter.** "A stale entry cannot be produced" holds for the *model* and not for
the *hardware*. Two CI runners on 2026-08-30 embedded a byte-identical corpus to
vectors differing in the third and fourth decimal, enough to reorder near-ties
and move the locate eval by three queries; the intra-op thread count has since
been ruled out as the cause (`TestEmbeddingVariesWithThreadCount`), leaving the
CPU's own kernels. A cache filled by runner A and read by runner B would
therefore hand back vectors that runner B would not have computed, and a
partially-warm cache would mix both inside one index. Nothing above accounts for
that, and it must be settled before the cache is built.

**And the same fact is an argument FOR building it.** A cache that hits makes the
eval *more* stable across machines, not less: the vectors come from the cache
rather than from whichever CPU the job landed on, so the per-run hardware lottery
becomes a per-fill one. That is a benefit beyond cost, and it is worth weighing
against the mixing hazard rather than treating the hazard as decisive.

### (j) errcheck adoption — deferred from P3.5 with a measured reason

> **STALE, corrected 2026-08-31.** The paragraph below says errcheck "is NOT
> wired up". It is: `make check` runs it through `scripts/errcheck-ceiling.sh`
> against per-module ceilings in `scripts/errcheck-ceilings.txt`, and every
> module currently reports *at ceiling*. What follows is the reasoning for why
> it was adopted as a **ceiling ratchet** rather than a zero-findings gate — that
> reasoning is still correct, and it is the resolution of the problem described
> below, not a deferral of it. Found while checking a different claim; nothing
> machine-checks this entry, which is the same gap that let items 20-25 sit
> outside the register.

Phase 3.5 adopted `staticcheck`, `ineffassign` and `bodyclose` as hard CI gates
(`scripts/lint.sh`, commit `9abc815`). **`errcheck` was named in the same item and
is NOT wired up**, deliberately, and this entry exists so that is a decision on
record rather than something rediscovered later as an oversight.

Measured on this tree: **329 findings, 157 outside tests.** The distribution is
the problem — the top callees are `os.Remove` (17), `fmt.Fprintf` (13),
`db.Close` (10), `conn.Close` (9), `resp.Body.Close` (7): cleanup-path closes and
writes to stdout/stderr. After an exclusion list covering the conventional cases,
**~60 remain**, still dominated by `defer x.Close()` on concrete types and
`enc.Encode` on a socket write whose error genuinely cannot be acted on because
the peer is already gone.

Turning that green means either ~60 `_ =` assignments — noise that makes a real
unchecked error *harder* to spot, which is the opposite of the point — or an
exclusion list long enough that the gate becomes arbitrary. It is a deliberate
triage pass over 60 call sites with its own judgement calls, not something to
bolt onto a CI-wiring commit.

**To close:** triage the ~60 non-excluded sites, decide per site between handling
the error, `_ =` with a reason, or an exclusion entry, then wire `errcheck` into
`scripts/lint.sh` alongside the other three. Until then the three adopted tools
are hard gates, which is worth more than four adopted softly — this repo has
already learned that a check nobody must pass is a check that drifts.

> **UPDATE 2026-07-31 — the deferral was right, and it was also unguarded.**
>
> Re-measured on `perf/latency-baseline`: **365 findings, 170 outside tests**, against
> the 329 / 157 recorded above. **Thirty-six new unchecked errors arrived in two weeks**,
> for exactly the reason this entry gives for not adopting: nothing was watching.
>
> The entry posed this as adopt-or-defer. There is a third option, and it is the one
> this repo already invented for coverage: **ratchet it.**
> [`scripts/errcheck-ceiling.sh`](scripts/errcheck-ceiling.sh) +
> [`scripts/errcheck-ceilings.txt`](scripts/errcheck-ceilings.txt) grandfather the
> current per-module counts and fail the build on growth — no triage required to start,
> and the drift stops today. It runs in CI's lint job and in `make check`, fails closed
> on a module with no recorded ceiling, and **fails when a count DROPS** too, so a
> slackened ratchet is reported rather than silently tolerated.
>
> Non-test only (`-ignoretests`), deliberately: an unchecked error in a test is a real
> problem but a different one, and sharing a budget would let each hide behind the
> other. Current ceilings — `daemon` 112, `proxy` 25, `editapply` 12, `helper` 11,
> `clients/tui` 10, `protocol` 0.
>
> **The triage above is still the way to close (j).** This does not do it; it stops the
> problem getting worse while it waits, and turns "we decided not to" into something a
> build can enforce. The classification is also sharper now: of the 170, ~91 are the
> conventional ignorables (40 `Close`, 23 `Fprint*`, 17 `os.Remove`, 7 `fset.Parse`,
> 4 `io.Copy` drains). The genuinely interesting residue is **15 `enc.Encode` socket
> writes and 4 `w.Write`** — a failed response encode means the peer never got the
> answer, and today that is silent. That is where triage should start.

## Backlog — added 2026-07-08

- ~~**Daemon must run from repo root (config-path gotcha)**~~ **Fixed 2026-08-01**,
  and it was never "low priority; docs/DX" — it was a hard blocker for packaging.
  Both defaults were CWD-relative (`-config ./models.json`,
  `-system-prompt daemon/prompts/system.txt`), so a daemon bundled inside a VS Code
  extension, which has no repo root to run from, could not start at all. The system
  prompt is now `//go:embed`ed (it is a build artifact, not configuration) and
  `resolveConfigPath` finds `models.json` beside the binary first, mirroring
  `resolveHelperBinPath`. Verified live: a binary and a `models.json` alone in a
  directory, started from an unrelated `cwd`, reaches `listening on ...` and drains
  cleanly; neutering both defaults reproduces the original
  `reading config ./models.json: no such file or directory`.

- **VS Code launch.json opens Host with "No Folder Opened"** (trivial; DX polish)
  The Extension Development Host launches with no workspace folder because
  launch.json only passes --extensionDevelopmentPath. Harmless (doesn't affect
  activation) but confusing during testing. Optional fix: add a bare
  "${workspaceFolder}" arg alongside --extensionDevelopmentPath so the Host opens
  clients/vscode as its workspace. NOTE: this repeatedly caused F5 to try to
  "debug" the focused file instead of launching the extension when the whole Neww
  repo was the open root — opening clients/vscode as its own folder is the reliable
  workaround. Worth fixing to save the confusion next time.

- **VS Code extension: remote-host support** (backlog; scoped out of first slice)
  Current client assumes daemon + VS Code on the same local machine (lockfile
  discovery via $XDG_RUNTIME_DIR). Remote-SSH / WSL / devcontainer extension
  hosts live on a different side of the gap and won't find the local lockfile.
  Not needed for local dev; revisit if remote usage becomes a goal.

## Backlog — added 2026-07-08 (diff-apply session) — the two items still open

The rest of this session's work shipped and is [in the
archive](docs/ARCHIVE/BACKLOG_2026-07.md).

- **VS Code diff-apply: dispatch is presence-of-`edit`-key** (hygiene note) — the daemon
  distinguishes an ApplyEditRequest from a PromptRequest by sniffing for the "edit" JSON key
  rather than an explicit type discriminator (chosen to avoid touching the just-committed
  protocol). Works and is verified, but it's an implicit contract — worth a code comment so a
  future reader knows it's intentional, and worth considering an explicit type field if the
  protocol is ever revised.
- **VS Code extension: remaining capabilities** (future slices, rough order) — a grounding
  indicator in the panel UI; a native VS Code diff view / inline decorations (nicer than the
  current whole-block red/green); then the larger fronts (ghost text, terminal error
  interceptor).

## Backlog — added 2026-07-17 (P2 caching investigation)

The privacy half of this investigation is written up in
[SECURITY_MODEL.md](SECURITY_MODEL.md#the-inference-hop--zdr-retention-finding) — a ZDR-labeled
provider retained our prompt content across requests, corroborated by billing. Open, escalate to
OpenRouter. What follows is the cost half.

- **Provider cost variance is the real cost lever — an order of magnitude more than caching ever
  offered.** An identical 492-token request cost **3.1× more on Io Net (1.23e-04) than on DeepInfra
  (3.97e-05)**, and **2.2× more than on DigitalOcean**. Against `models.json`'s note field
  ("~$0.11/$0.80 per 1M"): DigitalOcean matches it (1.12e-07/tok); **Io Net charged 2.5e-07/tok,
  2.3× the noted price.** Live today — `zdr.allow_fallbacks: true` means any of the ~11
  ZDR-compliant providers for this model can serve any request at whatever it charges, and 6
  distinct providers were observed serving 8 prompts during the 2026-07-10 verification.
  **This is a routing decision, not a caching one**, and it is where the cost leverage actually
  sits. Not scoped here: whether to constrain `provider.order` / `provider.sort` toward the cheap
  end of the ZDR-compliant pool, and what that would cost in the congestion the fallback pool
  exists to escape. **Corroborates item (e)'s open note** ("fix the stale price comment in
  models.json note field") — the note is not merely stale, it is 2.3× off for a provider that is
  live on the route today.

- **Known tension — NOT a to-do: `allow_fallbacks` is both the congestion fix and the caching
  blocker.** `zdr.allow_fallbacks` was flipped `false` → `true` on **2026-07-10** specifically to
  escape persistent DeepInfra 429s (see the ZDR gate entry — a throughput fix, not a privacy
  change; the ZDR filter still constrains the pool). That same flag is exactly what defeats prompt
  caching, which requires **sticky routing** to whichever provider holds the cache. **The fix for
  congestion is the blocker for caching.** Recorded so nobody reopens caching-for-cost without
  knowing they would be proposing to re-break the congestion fix — and per the item above, the cost
  win is in provider choice anyway, which the same flag governs. No action; this is context.

## Backlog — added 2026-07-18 (wider-pool experiment: H6 helperproc)

- **Wider-pool experiment — NEGATIVE RESULT: pool width is NOT helperproc's bottleneck; do not
  retry pool widening for it.** Pre-registered, run against real instrumented evals, reverted.
  Two findings:
  1. **The edit eval was pool-INSENSITIVE.** `daemon/edit_eval_test.go` called
     `retrieveTopK(k=len(scan.Chunks))`, so `rerankPoolSize(k)` (rerank.go) far exceeds the
     corpus and the whole thing is always fetched — a pool-width constant provably cannot move
     its result. Its "helperproc MISS #67" is a full-corpus RERANK position, not a pool
     exclusion. Fixed by adding a production-shaped k=5 (pool-gated) verdict + a raw-semantic-
     rank diagnostic (measurement only, no retrieval logic changed); the eval now reports BOTH a
     production-shaped recall (the pool-SENSITIVE number) and the legacy full-ordering recall.
  2. **Bumping `rerankOverfetchFactor` 6→16 (production pool 30→80 at k=5) did NOT flip
     helperproc.** helperproc's raw semantic rank is #59 (re-confirmed, 869-chunk corpus), so at
     pool=80 it IS fetched (59<80) — yet it still misses the production top-5, reranking out
     (full-ordering rank #67, identical across both runs). Fetching is necessary but not
     sufficient; helperproc's real bottleneck is rerank/embedding position — the SAME structural
     class as tui, not a separate "just widen the net" case. Clean FAIL by the pre-registered bar
     (helperproc did not flip; locate eval held 8/9; editapply/zdr prod-HITs held at both pool
     sizes) → `rerankOverfetchFactor` reverted to 6. **No pool size rescues helperproc** — the
     full corpus (a maximally wide pool) already reranks it to #67, so don't reattempt widening.
  - ~~**Still open, NOT resolved by this task:** the locate-eval saturation flag (whether the
    9-query set must grow before finer retrieval tuning is trustworthy) — sidestepped here via a
    binary pass/fail bar, not answered.~~ **CLOSED 2026-08-28. The set was saturated, and the cost
    of leaving it that way was paid on 2026-08-27:** a structure-aware chunking change took
    real-repo chunk-level recall from 8/9 to 4/9 and **shipped**, because `make check` was green,
    the small offline fixture eval reported 14/15 throughout, and the one eval that could see it
    was schedule-only. Reverted in `b552daf`. Resolution:
    - **`rerankEvalQueries` grew 9 → 49**, every anchor verified to resolve to 1–3 chunks before
      being committed. Each query carries a declared `shape` (impl / def-vs-use / test /
      cross-module / doc / multi-file) so a regression is attributable to a class of question.
    - **The floor is now a RATE, not a tally** (`evalChunkRecallFloor = 0.40`). One flip is 2.0pp
      instead of 11pp; the gate fires on a 3-query loss.
    - **It gates on PRs that touch retrieval** (`.github/workflows/retrieval-eval.yml`, path-
      filtered), not only on the Monday schedule. `make eval` runs the same thing locally.
    - **ACCEPTANCE TEST — the instrument now catches the exact regression that shipped green.**
      The reverted chunking was re-applied in a scratch worktree and both evals run against it:

      | | fixture eval (`TestEvalRetrievalQuality`) | locate eval (this one) |
      |---|---|---|
      | HEAD, fixed 40/10 windows | 14/15 **PASS** | 24/49 = 49.0% **PASS** |
      | structure-aware chunk boundaries | 14/15 **PASS** | 17/49 = 34.7% **FAIL** |

      The fixture eval scores *identically* on both, which is exactly what it did in real life.
      And the per-shape column reproduced, unprompted, the diagnosis that had taken a manual
      investigation: **def-vs-use collapsed 6/7 → 1/7 while impl-seeking IMPROVED 8/24 → 10/24**.
      Cutting at construct boundaries separates a declaration from the code that uses it.
    - **The measured answer to "was 8/9 trustworthy": no.** 49 queries score **24/49 = 49.0%**
      chunk-level. 89% was the score of a set built out of failures that had already been fixed;
      49.0% is the first number from questions retrieval was not tuned on.
    - **The most useful thing it surfaced:** file-level recall is **39/49 (79.6%)**, and **15 of
      the 25 chunk-level misses are the right file with the wrong forty lines of it**. Most of the
      failure mass is a *granularity* failure, not a ranking failure — which is both a direction
      for North Star item 3 and the reason a chunk-boundary change was able to do so much damage
      so fast.
    - **Per-shape, the dominant real query shape is the weakest: impl 8/24 (33%)**, against
      def-vs-use 6/7, multi 3/4, test 2/3, cross 5/10, doc 0/1. Not acted on here; recorded so the
      next retrieval push starts from where the loss actually is.
    - **Retrieval is deterministic but corpus-SENSITIVE — measured, not assumed.** Indexing once
      and running the identical pass three times gives byte-identical hit vectors. Two whole runs
      the same day differed (22/49 then 24/49) purely because the corpus grew by 5 chunks out of
      ~4,385, in files no flipped query even names. Near-misses sit right at the top-5 cut, so
      what they compete against decides them. That is why the floor is 40% and not the measured
      49.0%: it has to sit above the regression's 34.7% and below the observed band, and it does.

    Still open and unchanged by this: helperproc AND tui both remain H6 MISSes; the real levers
    are the North Star item 3 direction (chunking / query expansion / a stronger embedder), not
    pool width.

- **k=5 -> 10 and the context budget 8000 -> 16000 chars, measured. The BUDGET was the binding
  constraint, not the ranker.** Decided 2026-08-28 off the expanded eval above, which is what made
  it visible. Measured through the full production path (retrieve -> merge -> budget):

  | config | delivered to the prompt | mean chars | truncated |
  |---|---|---|---|
  | k=5, budget=8000 (shipped until now) | 23/49 = 46.9% | ~6,990 | **27/49** |
  | k=10, budget=8000 | 24/49 = 49.0% | ~6,890 | 49/49 |
  | k=10, budget=12000 | 27/49 = 55.1% | ~10,925 | 49/49 |
  | **k=10, budget=16000 (now)** | **30/49 = 61.2%** | ~14,740 | 21/49 |
  | k=10, unbudgeted | 30/49 = 61.2% | ~15,900 | 0/49 |

  Three things worth keeping:
  - **The old 8000-char budget was truncating 55% of query sets** and costing a query outright
    (23 delivered vs 24 retrieved). Retrieval was finding answers the budget then threw away.
  - **Raising k alone is nearly a no-op** — at the old budget k=10 delivers FEWER spans than k=5
    (3.2 vs 3.7), because merging folds the window overlap and the survivors are individually
    bigger. The two constants are one decision, not two.
  - **16000 is the saturation point, not a taste call**: recall stops moving there, and it still
    caps 21/49 so it remains a real budget. Net +14.3pp delivered for ~2.1x injected context
    (~2,000 -> ~4,200 tokens/prompt).

  Re-measured after the change: DELIVERED **28/49 (57.1%)**, retrieved 30/49, file-level 43/49
  (87.8%), impl-shape 8/24 -> 11/24. The 28-vs-30 gap is the ±2 corpus band already recorded
  below, not a discrepancy.

  **NOT measured, and it is the honest limit of this:** whether the extra spans help or distract
  the MODEL. This eval measures what reaches the prompt, not what is done with it. An agentic eval
  is where that gets settled.

- **Query 1's "known gap" was a truncation, not the semantic failure it was documented as.**
  `"where does the daemon open the unix socket"` had been recorded for months as a
  doc-comment-vs-implementation problem needing chunk-level content classification, "out of
  scope". At k=10 it hits: the answer chunk comes back at **rank 7**, and the top five are all
  real transport code with no doc comment among them. Deliberately NOT promoted to `mustHit` on
  one run — the same one-run inference was made about this query during the 2026-08-27
  embed-window work and did not reproduce.

- **AST-aware chunking: CLOSED, measured, do not reopen without changing the EMBEDDING.**
  2026-08-28. Tried three times, lost three times. The third attempt was built specifically to
  fix the first two failures and still lost, which is what makes this a close-out rather than
  another revert.

  | arm | delivered | semantic tier | chunks | impl shape |
  |---|---|---|---|---|
  | **fixed windows (shipped)** | **29/49** | **24/49** | 4,581 | 12/24 |
  | snap, no overlap, byte cap (`880df70`) | 26/49 | 15/49 | 5,266 | 11/24 |
  | snap, overlap kept, no byte cap | 27/49 | 17/49 | 4,911 | **13/24** |

  Agreed bar before running: ≥32/49 and no shape falling by more than 1. Best arm reached 27
  and dropped `defuse` by 2.

  **The diagnosis that was wrong, and the one that replaced it.** `880df70` bundled three
  changes — snap boundaries, drop the overlap at a clean seam, add a 1638-byte ceiling — and
  the harm was attributed to the last two. Both were fixed in the third arm: the overlap was
  kept at every seam and the byte cap deleted. Chunk count fell 5,266 → 4,911 and the semantic
  tier recovered 15 → 17. **It did not recover to 24.**

  **What actually kills it is the semantic tier, and it survived every fix.** Aligning a chunk
  to a construct makes it *semantically narrower*: it is about one thing, so its single
  384-dimensional vector matches one kind of question. An arbitrary 40-line window straddles two
  or three constructs and embeds as a broader, blurrier thing that matches more queries. On this
  corpus that blur is worth **seven queries**, and no boundary policy recovers them.

  The corpus explains why: measured over **887 real functions — median 12 lines, p75 26, only
  13% exceed one 40-line window**. One construct per chunk is too fine a granularity to embed
  well.

  **The upside is real and small.** Implementation-seeking queries score best under snapping
  (13/24, the highest of any arm) — the structural claim about mid-function chunk starts holds.
  It does not pay for a forced re-index of every installation, which `chunkerID` now makes
  mandatory and unskippable.

  **If revisited, change the EMBEDDING, not the boundary** — a per-chunk representation that
  does not lose by being specific. The nearer lever is **body elision**: it targets the 13% of
  constructs too big for a window, which is where the measured misses concentrate, and it does
  not narrow what a chunk is about. The verdict is repeated at the top of `chunkContent` so the
  next reader meets it before writing any code.

- **Neighbour expansion: +10.2pp delivered for +2% context, and it came from DIAGNOSING the misses
  rather than guessing at them.** 2026-08-28. Step 2 of the AST-chunking sequence was "diagnose the
  right-file-wrong-chunk misses at line level", and the diagnosis changed the answer: the winning
  intervention was **not one of the three options that step was meant to choose between**.

  Of 19 misses where the right FILE reached the prompt but the wrong chunk did, **10 had a
  retrieved chunk within one position of the answer** — retrieval was right about the file and even
  the region, wrong about which forty lines. `mergeAdjacentChunks` cannot help: it folds chunks that
  are BOTH retrieved, and here only one of the pair is.

  Measured policy grid, full production path, delivered recall out of 49 with mean rendered size:

  | policy | budget 16000 | 20000 | 24000 |
  |---|---|---|---|
  | none (before) | 25 (14,620ch) | 26 | 26 |
  | ±1 on all | 26 (13,174ch) | 31 | 31 |
  | ±1 on top 5 | 28 (14,277ch) | 30 | 30 |
  | **±1 on top 3** | **30 (14,924ch)** | 31 | 32 |
  | ±2 on all | 21 (12,845ch) | 24 | 30 |

  - **Top 3 at the existing budget is +5 queries for +2% context.** Widening the budget to 24,000
    buys two more for 36% more context — a far worse trade, not taken. Expanding by two neighbours
    is *worse than not expanding at all*.
  - **Both sides, not forward-only.** 9 of the off-by-one answers lay BEFORE the retrieved chunk,
    4 after — retrieval tends to match slightly below the code that answers the question.
  - **Delivered (29/49) now EXCEEDS retrieved (27/49)**, which was previously impossible: the budget
    only removes, so delivered was capped by retrieval. Expansion adds the region around a hit, so
    an answer retrieval never ranked in its top ten still reaches the prompt.

  Confinement reuses `shouldSkipFile` — the indexer's own gate — because expansion reads from disk
  and would otherwise be a second, weaker exclusion surface. Neutering that gate leaks `.env` into
  a prompt; the test catches it.

  **What the diagnosis says about AST chunking specifically:** 10 of the 19 have a construct
  *split* across chunks, which looks like an argument for AST boundaries until you read the sizes —
  567%, 640%, 260%, 252% of the chunk window. **The construct is 2–6× larger than the chunk, so
  boundary snapping cannot contain it either**; it would produce chunks far past the embedder's
  512-token limit and have to split them anyway. Body elision addresses an oversized construct;
  boundary snapping does not. And 11 of 19 sit beyond rank 20 — for those the ranker has no idea,
  and no chunking change helps.

- **P1 DATA INTEGRITY, found and fixed 2026-08-28: a full `index` run only ever ADDED. The store
  grew and never shrank.** Found while checking the prerequisites for AST-aware chunking; it turned
  out not to be an AST problem at all but a live bug hitting ordinary users with no code change.
  `VectorStore.DeleteByFilePath` was written for exactly this and its own doc comment says so
  ("deleting by file first is what makes a re-index a replacement rather than a merge") — but only
  `reindex.go` and `watcher.go` ever called it. `buildIndex` never did. A wiring gap, not a design
  gap. Measured on a full rebuild with **reuse off**, the most thorough thing a user can run:

  | trigger | before the fix |
  |---|---|
  | delete a file, re-index | its 4 chunks all survive, still retrievable |
  | file shrinks 200 → 30 lines | store goes 7 → **8**: adds the new chunk, keeps all 7 stale ones |
  | chunk boundaries change | a chunk at a line range the chunker no longer emits survives intact |

  **The deleted-file case is not only a quality problem.** A user who deletes a file and re-indexes
  still had its full text in the store, retrievable, and eligible to be sent to the model.

  Fixed in two parts, each neuter-verified:
  - **`pruneOrphanedChunks`** (index_cmd.go), run after `carryOverUnchanged` so carried vectors are
    already in memory and pruning costs **zero** model time. One rule covers every trigger: any
    stored ID the scan does not regenerate is an orphan, and its file is replaced wholesale.
    Needed one new primitive, `AllIDs`, on both stores — chromem exposes no listing API, so its
    side is a `k=Count()` query, verified complete against a 4,400-doc store seeded so half the
    vectors were **orthogonal** to the probe (none dropped, 6.9ms).
  - **`chunkerID`** in the embedder stamp. Neither existing field covered boundaries: `EmbedderID`
    says which model made the vectors, `IndexSchemaVersion` says what shape each chunk is stored
    in. Change how text is *cut* and both stay identical.

  **The chunkerID guard is behavioural, not a hand-bumped integer.**
  `TestTheChunkerIDChangesWhenTheBoundariesDo` hashes the line ranges `chunkContent` produces over
  a fixture. Change the algorithm at unchanged parameters — which is exactly what AST-aware
  boundaries are — and it fails, naming what to do. `currentIndexSchemaVersion` is the
  counterexample: a hand-bumped integer is a promise that somebody will remember.

  **Neutering caught a bug in the fix itself.** Deleting a file's rows without clearing its `inSync`
  flags makes `carriedNeedingWrite` skip the rewrite — so under `reuse=true` the prune would have
  removed an orphan *and its innocent neighbours*, permanently. The first four tests all used
  `reuse=false`, where `inSync` is all-false anyway, so they never exercised it. Closed by
  `TestPruningUnderReuseRestoresTheChunksItHadToDeleteAlongside`.

  Upgrade path: an index with no `chunker_id` decodes to `""`, mismatches, and forces one rebuild —
  intended, since those are exactly the indexes that may carry orphans from before this existed.
  Retrieval disables gracefully with a message rather than crashing, and `reuse` turns off so the
  rebuild re-embeds. `reasonIndexModelMismatch` reworded: it named only the embedding model, which
  would have told a user their model changed when their chunker had.

  **This was step 1 of the AST-aware chunking sequence, and it is now unblocked.**

- **Corpus hygiene — the eval had been scoring against its own answer key, and nobody knew.**
  Found 2026-08-28 while expanding the set above. Three captured `go test` output files were
  **committed** (`test.log`, `test_output.txt`, `daemon/test_out.txt`), and `test_output.txt`
  — added in `3ff9ee2` on 2026-08-11 — was a transcript of a `TestRerankEvalRetrievalRanking`
  run: every query string verbatim, each a few lines above the chunk IDs of its correct answers,
  sitting inside the corpus the eval indexes. Seventeen days of runs were inflated by an unknown
  amount. `evalSelfReferenceFiles` could not catch it: a hand-maintained exclusion list only
  covers leaks somebody thought of. Fixed three ways — files deleted and `.gitignore`d, four more
  leaking files found and handled (one, `daemon/rerank.go`, is product source and a declared
  ANSWER, so it was reworded rather than excluded), and `TestNoIndexedFileEchoesAnEvalQuery` added
  and **wired into `make check`** (needs no model, runs in under a second) so a recurrence is red
  at commit time rather than silent for a fortnight.

## Phase 1 — Distribution & Launch Gate (Release Phase) — COMPLETED & VERIFIED (2026-08-12)

- **E1: macOS Signing & Notarization (Stage 3.5)** — `scripts/macos-sign-and-notarize.sh` created and executable; handles macOS keychain setup, `codesign --deep --force --options runtime --timestamp`, `notarytool submit`, and `stapler staple`. Handles dry-run fallback gracefully when credentials are absent. Wired into `.github/workflows/release.yml`.
- **E7: Pilot Distribution (First Contact)** — Multi-target VSIX packaging verified for `linux-x64`, `win32-x64`, and `darwin-arm64`. Staging scripts bundle `codeterminal-daemon`, `codeterminal-embedder-helper`, `models.json`, and `LICENSE.txt`. Structural integrity gate `verify-vsix.js` passed. Verified end-to-end via clean-VM harness `scripts/e3-pilot-test.js` (unpacks `.vsix`, spawns daemon in hermetic environment without Go compiler, executes protocol handshake, exits cleanly).

## Phase 2 — Product Integrity & Retrieval Hardening — COMPLETED & VERIFIED (2026-08-12)

- **E3: Index Honesty (Stage 4)** — Embedded `BuiltAt` timestamp into `embedderStamp`. System freshness checks scan file `mtime` against `BuiltAt` with 13 comprehensive unit/integration tests (`daemon/indexfreshness_test.go`). Reports index staleness via `DegradedIndexStale` degradation signal on `StatusRequest` polling surface. Follows "Fix 8 / Gate 7" privacy rule (no paths or absolute errors exposed).
- **Debt (f): Implementation vs. Setup Chunk Tuning** — Enhanced retrieval re-ranking in `daemon/rerank.go` with expanded `implSeekingWords` regex and setup file pattern down-weighting. Verified implementation chunks rank higher than setup/config chunks for implementation-seeking queries across both unit tests (`daemon/rerank_test.go`) and real-repo eval harness (`daemon/rerank_eval_test.go`).
- **ZDR: Per-Model ZDR Live-Verification & Menu Hygiene** — Synchronized proxy cost gate allow-list, `models.json` tiers, and daemon configuration. Confirmed via `proxy/modelallowparity_test.go` and verified strict fail-closed enforcement per request. All 9 active tiers mapped cleanly.

## Phase 3 — Core Experience & Polish — COMPLETED & VERIFIED (2026-08-12)

- **Model Switcher Refinement (b4 & b5)** — Updated model slash commands in both TUI (`clients/tui/chat.go`) and VS Code (`clients/vscode/src/chatPanel.ts`) to surface all model tiers, explicitly annotating inactive ones with `[inactive]`. Selection of inactive tiers is gracefully refused with explanatory error feedback. Verified with unit tests (`clients/tui/model_command_test.go`).
- **Session History Memory Pruning Policy (h)** — Enforced dual count-based (`maxTurnsPerWorkspace = 1000`) and age-based (`maxTurnAge = 30 days`) history retention policies in `daemon/memory.go`. Automatic deletion triggers maintain clean, synchronized FTS5 search indices via `turns_ad` database triggers. Fully verified with unit tests (`daemon/memory_test.go`).
- **Daemon & Workspace Testing Stability** — Resolved lockfile and workspace path collision flakiness in `daemon/twoworkspaces_test.go` by adopting distinct named subdirectories (`workspace_a`, `workspace_b`). Verified full test suite execution (`go test ./daemon`).

## Phase 4 — standalone / packaging / commercialization (decided direction: capable first, then shippable)

> **This section is now planned work, not a wish.** It has been designed against the
> code and scheduled as Stages 2–5 of the master launch plan, with named files, an
> ordered build sequence and stated risks. The bullets below survive as the original
> statement of intent; the plan is the executable version. Two facts found while
> designing it, which the bullets predate: the confinement pipeline needs a **security
> re-audit** under Windows path semantics rather than a port, and per-workspace daemons
> cost ~200–300 MB RSS each until the helper is shared.

Scoped and decided this session, not started. Goal: a user installs the VS Code extension from
the marketplace and it works WITHOUT separately installing/running the daemon — the "like Claude
Code" experience. This is the packaging phase, the single largest remaining body of work.

- **Bundle + auto-manage the daemon** — the extension must ship the daemon binary and start it
  as a background process on activation, shut it down on deactivate. (Today the daemon is
  started by hand in a terminal.)
- **Cross-platform binaries** — build/bundle the daemon (and embedder helper + model files) for
  Windows, macOS (Apple Silicon + Intel), and Linux, and select the right one at runtime.
  Known gap: Intel Mac (darwin-amd64) has no prebuilt onnxruntime for on-device embeddings.
- **API key model — DECIDED: managed service.** Mochiii holds the key and ships "brain + agent"
  (wired to deepseek-v4-flash); users do NOT bring their own key. IMPLICATION: every user's
  token usage bills to the project's OpenRouter account — so this commits to billing users
  (Stripe / usage metering) rather than personally absorbing everyone's usage. The billing +
  metering build is part of this phase.
- **On-device embeddings on user machines** — the local embedder helper + model files must ship
  per-platform and start correctly on a stranger's machine.
- **Packaging/publishing** — signed binaries, the .vsix, marketplace (or private-registry)
  publishing.

## Release gates (before anyone else uses it — still open)

- **DONE: Supabase auth/RLS/grants posture — see [SECURITY_MODEL.md](SECURITY_MODEL.md).**
  Closed 2026-07-17. `api_keys.user_id` (nullable, FK -> `auth.users(id)` ON DELETE RESTRICT),
  SELECT-only RLS policies on `api_keys` and `usage`, per-column grants to `authenticated`,
  `anon` revoked to zero on both tables and both RPCs, EXECUTE revoked from PUBLIC on 
  `reserve_usage`/`increment_usage` (Postgres's implicit default is EXECUTE-to-PUBLIC — `anon`
  could have called `increment_usage(key, -999999)`), and an `api_keys_public` view with
  `security_invoker = true` that excludes `key_hash`. Verified live end-to-end with an anon key
  + a real user JWT — **not** with `service_role`, which holds BYPASSRLS and passes every check
  regardless of whether RLS works at all. The proxy is unaffected throughout (it is
  `service_role`); re-verified live against the deployed proxy after the RPC revokes.
  SECURITY_MODEL.md carries the full re-runnable verification matrix and four non-obvious traps
  — read it before touching any of this. The two most likely to bite: PostgREST's 42501 hint
  literally instructs you to re-expose `key_hash` (point the frontend at `api_keys_public`
  instead), and `grant select (user_id)` looks like dead surface but is required by
  `usage_select_own`'s subquery — revoking it silently breaks every usage read.
  **Default privileges (trap 3) — half-fixed 2026-07-17.** Supabase's bootstrap auto-granted full
  `anon` CRUD on every new relation in `public` (it fired for real: `api_keys_public` arrived
  pre-granted SELECT to `anon` that nobody wrote). `ALTER DEFAULT PRIVILEGES ... REVOKE` fixed
  **tables, views, and sequences** — verified by probe. It did **not** fix **functions**, and
  can't: Postgres's built-in EXECUTE-to-PUBLIC baseline isn't a row in `pg_default_acl`, so
  revoking the explicit entry removes the catalog row and falls back to the baseline. **The
  catalog reads clean while the hole is open** — worse than the original trap. Mechanism is
  logged UNRESOLVED (it contradicts PostgreSQL's own documented example; not guessed at). What
  replaces it is a process control: **every `SECURITY DEFINER` function in `public` must revoke
  EXECUTE from PUBLIC in the same migration.** Narrow enough to hold — on `SECURITY INVOKER`
  functions EXECUTE-to-PUBLIC is only a missing layer (the caller's own grants and RLS still
  apply, which is why the pre-revoke `increment_usage` hole was latent, not live), but on
  `SECURITY DEFINER` it's the entire perimeter. First place it bites: the deferred self-service
  revocation function.
- **Security review** — the daemon opens a local socket and the edit engine writes to user
  files; both must be reviewed before others run Mochiii. Blocking gate.
  **Engineering is complete on both axes; what remains is three founder rulings.**
  *(Corrected 2026-08-01. The text this replaces described the 2026-07-18/19 state and had
  been stale for weeks — it named two FAILs as open that were fixed in July, which is the
  single clearest reason this file needed splitting.)*
  - **P3 editapply-write-path axis — engineering complete.** The 5 structural gates hold.
    `MatchesSecretName`'s case/key-coverage gap (FAIL-1, High) is contained, and the
    unconfined undo writer `restoreOne` (FAIL-2, Moderate) is fixed (`4de7bd4`), along with
    the fifth writer `ensureGitignoreEntry` (`dbc4e6c`), both with regression tests.
  - **P3 socket axis — engineering complete.** The socket now authenticates its peer via
    kernel-supplied credentials on Linux (`517c069`, `SO_PEERCRED`) and macOS (`648d38b`,
    `LOCAL_PEERCRED`), failing closed everywhere else; resource limits landed (`ccf8b8d`);
    apply/undo is serialized in-process (`d96794e`) and across processes (`2a389c7`, `flock`),
    which completes Gate 6.
  - **Gate 7 was measured rather than fixed** (`e4ff6c1`): driving the real Apply handler
    across ten filesystem states yields nine distinct refusals, none leaking an absolute path,
    every one of them user-actionable. The recommendation is to **reject** the unification —
    see D3.
  - **What actually blocks the gate now: D1 (socket auth model), D2 (Gate 6 closure),
    D3 (Gate 7).** All three are rulings with written recommendations and evidence in
    [`docs/DECISION_PACK.md`](docs/DECISION_PACK.md). None is an engineering task.
  - **Windows adds a new axis that has never been reviewed.** The five-gate pipeline was
    audited against POSIX semantics only, and
    `editapply/confinement_conformance_test.go` skips its symlink vectors on Windows — so
    junctions, alternate data streams, 8.3 short names, trailing-dot/space stripping and
    reserved device names are all unexamined. Treated as a security workstream, not a port,
    in the master launch plan.
- **ZDR confirmation — code-complete, BOTH positive and negative paths live-verified
  (2026-07-09).**
  ⚠️ **Enforcement is verified; the guarantee it is meant to deliver is not.** A ZDR-labelled
  provider was measured retaining our prompt content across requests (2026-07-17): 98% of provably
  novel content served from cache, corroborated by a 78.6% billing discount. Everything below is
  accurate — the flags are sent and honoured exactly as described — but **do not read
  "live-verified" as "prompts are not retained."** Open question with OpenRouter; evidence and
  scope limits in
  [SECURITY_MODEL.md](SECURITY_MODEL.md#the-inference-hop--zdr-retention-finding).
  Provider-routing (`provider.zdr=true`, `data_collection="deny"`, `allow_fallbacks=false`)
  is sent on every inference request, secure-by-default (an absent/legacy "zdr" section
  in models.json resolves to the strictest enforcement, not the weakest), with refusal
  detection (`ErrZDRRefused`) surfaced as a privacy-specific error rather than a generic
  one. Code in `daemon/config.go`, `daemon/provider.go`, `daemon/server.go`,
  `models.json`; unit-tested in `daemon/config_test.go` and `daemon/provider_test.go`.
  **Positive path**: a real successful prompt against this account logged
  `model API served by provider="DeepInfra" (zdr=true data_collection=deny allow_fallbacks=false)`
  followed by `stream complete`. Confirms the ZDR flags go out on the wire, the request
  succeeds under full enforcement, the serving provider is observable (so a silent
  fallback would be detectable), and that deepseek-v4-flash is ZDR-servable on this
  account via DeepInfra. (429s seen incidentally during testing were transient upstream
  rate-limiting, unrelated to enforcement, and correctly surfaced as rate-limit errors
  rather than misclassified as ZDR refusals — confirmed again during the negative-path
  revert/recheck below.)
  **Negative path**: temporarily routed a real request at `ibm-granite/granite-4.0-h-micro`
  (a real, active OpenRouter model confirmed to have zero ZDR-compliant providers, via
  OpenRouter's public `/api/v1/endpoints/zdr` listing) with enforcement still fully
  strict. OpenRouter genuinely refused — daemon log:
  `model API error: model API returned 404 Not Found: {"error":{"message":"No endpoints found matching your data policy (Zero data retention). Configure: https://openrouter.ai/settings/privacy","code":404}}`.
  This proved enforcement really blocks non-compliant routing, but also caught a real gap:
  that exact phrasing wasn't in `zdrRefusalSubstrings` (which only knew "no allowed
  providers" / "no available model provider"), so the client saw the raw 404 JSON instead
  of the friendly `inference refused: no zero-data-retention endpoint available` message.
  Fixed same-day by adding `"zero data retention"` as a third substring (chosen over the
  full sentence, too brittle against rewording, and over the shorter "data policy", too
  generic) — verified against the exact live-observed body in
  `daemon/provider_test.go` (`realObservedZDRRefusalBody`,
  `TestIsZDRRoutingRefusal_MatchesLiveObservedDataPolicyPhrasing`,
  `TestStreamCompletion_LiveObservedDataPolicyRefusalIsDetectableViaErrorsIs`), not a
  synthetic guess. `models.json` was reverted to `deepseek/deepseek-v4-flash` immediately
  after the negative-path observation, and the positive path was re-confirmed live
  post-revert (same `provider="DeepInfra" ... stream complete` shape) before the fix was
  written. Both `zdr=true` config-value changes were temporary and never committed —
  `models.json` in git is unchanged by this work; only the matcher fix in
  `daemon/provider.go`/`daemon/provider_test.go` was committed. (Folds in item (g) above.)
  **2026-07-10 — provisioned inference route: single-provider congestion resolved,
  ZDR-constrained fallback, live-verified.** `allow_fallbacks=false` pinned every request
  to one provider (DeepInfra); DeepInfra was returning persistent 429s despite a funded
  account — a throughput blocker, not a privacy one. Fix: `models.json`'s
  `zdr.allow_fallbacks` flipped `false` → `true`; `zdr=true` and `data_collection="deny"`
  are untouched, so the fallback pool stays filtered to zero-data-retention providers only
  (OpenRouter's provider-routing docs describe `zdr`/`data_collection` with hard
  pool-membership language — "excluding"/"only routes" — distinct from soft preferences
  like `preferred_max_latency`, which the same docs explicitly call "deprioritized...
  rather than excluded entirely"; `allow_fallbacks` only governs whether routing continues
  *within* that already-filtered pool). Not trusted on doc-reading alone — re-verified live
  with the same rigor as the 2026-07-09 gate above: **negative path** — pointed a request
  at `nex-agi/nex-n2-mini`, freshly confirmed via `/api/v1/endpoints/zdr` (not the stale
  2026-07-09 pick) to have zero ZDR-compliant providers, with `allow_fallbacks=true` live.
  Still hard-refused: `model API returned 404 Not Found: {"error":{"message":"No endpoints
  found matching your data policy (Zero data retention)..."`, proving fallback cannot
  escape the ZDR filter even when the filtered pool is empty — no bypass. **Positive
  path** — 8 real prompts against `deepseek/deepseek-v4-flash` all succeeded, served by 6
  distinct ZDR-compliant providers (Novita ×2, SiliconFlow ×2, AtlasCloud, DigitalOcean,
  Parasail, Morph) out of the 11 currently ZDR-compliant for this model — zero landed on
  DeepInfra, confirming OpenRouter now actually routes around the congestion. Resolved
  wire body: `{"zdr":true,"data_collection":"deny","allow_fallbacks":true}`. No code
  changes, `models.json` only. **Separate, still-open item, not resolved by this change:**
  the account still needs to be kept funded (API credits) for requests to succeed at all —
  a billing precondition, unrelated to which provider serves the request.
- **Performance NFR — TTFT MEASURED (2026-07-17): chat ~1,450 ms warm / ~1,900 ms cold, NOT
  <400 ms.** Measured end-to-end against real infra (local daemon + live Railway proxy + real
  Supabase + real `deepseek-v4-flash` via OpenRouter), from India, n=21–50 per component, warm =
  reused connection (matches the daemon's connection pooling). Headline (proxy mode, the product
  path), keystroke→first content token: median ≈ **1,450 ms** warm, ≈ **1,900 ms** cold (first
  prompt, fresh TCP+TLS to Railway), p90 ≈ **2,270 ms**. Direct mode ≈ 1,140 ms warm. ~3.6× the old
  target; not achievable for chat. Where the time goes (proxy, warm median):
  - **Model prefill/routing ≈ 1,125 ms (~78%) — the provider's time, not ours.** Highly variable
    (p90 1,840 ms); provider routing dominates the spread. Even a direct-mode / no-retrieval /
    no-gates floor is ~1,125 ms (~2.8× the target): <400 ms was never physically reachable for a
    real generation, regardless of our code.
  - Retrieval (embed+search+rerank+budget, warm embedder) ≈ **14 ms** (embed 5.4 + search/rerank
    8.8); the embedder's 355 ms startup is once-at-daemon-boot, not per-prompt. Ours, cheap.
  - **Railway proxy hop ≈ +100–180 ms — ours.** Railway-Singapore (~102 ms warm RTT from India) vs
    OpenRouter's near-India edge (~27 ms). Removing the proxy saves this but doesn't approach 400 ms.
  - **reserveQuota ≈ +80–100 ms — ours (added this session with the quota-race fix).** What's solid
    (measured directly): it is NOT an inherent write/RPC cost — an isolated Supabase read,
    table-write, and RPC-write are all identical latency (~205 ms each from a test client), and
    authorize itself adds ~0 ms while reserveQuota, the *second* sequential Supabase call, adds ~90 ms.
    What's NOT solid: *why*. The tempting explanation is fresh-connection establishment on the second
    call, but the ~200 ms cold-connection penalty behind that was measured from India (~200 ms RTT);
    the proxy→Supabase hop is intra-region Singapore→Singapore, where a TCP+TLS handshake should cost
    ~10–20 ms, not ~90 ms. The magnitude doesn't transfer across a ~40× RTT difference, so ~80 ms of
    it remains genuinely unexplained. **Untested hypothesis (not a finding):** both `authorize()` and
    `reserveQuota()` drain the response body only on their error paths; the success path calls
    `json.NewDecoder(resp.Body).Decode(&rows)`, which stops at the first complete JSON value rather
    than reading to EOF. Go's `http.Transport` only returns a connection to the pool if the body is
    read to EOF and closed, so the success path likely drops its connection every request and the
    next call re-handshakes — which would produce a per-call penalty independent of RTT. Not verified
    against the running proxy. Likely-cheap fixes if TTFT becomes a priority: drain the body before
    close (`io.Copy(io.Discard, resp.Body)`), or collapse validate+reserve into one RPC (one round
    trip). Not doing either now.
  - **Perceived responsiveness ≫ TTFT:** the daemon sends the grounding/status message BEFORE any
    tokens, ~15–20 ms after Enter (right after retrieval), so the UI shows activity in ~20 ms even
    though the first answer token is ~1,450 ms. First *visible feedback* is fast; first *token* is
    not.
  - **On the <400 ms origin (ghost_text vs chat):** introduced (`65cd63d`) as a general
    "Performance NFR" in the launch-gate list, with no path scoping — its intended path is genuinely
    unknowable. **Retired as a chat target.** <400 ms remains the natural, still-unmeasured target
    for the **`ghost_text` tier** (fast keystroke completions, `models.json` `ghost_text`, currently
    `active:false`, Phase 3.5) — re-scope it there and measure once that tier is live.

## North Star — **all three shipped.** Now: making them robust

> **Rewritten 2026-08-09.** This section used to be called "Deferred Capabilities" and
> described three things "deliberately NOT being built yet". **All three are built.** Two of
> the three entries said otherwise, and one of those stale entries was hiding a live defect —
> eight of nine selectable models refused by the managed proxy, found only because this section
> was re-derived against source instead of re-read.
>
> That is the lesson worth keeping: **a backlog entry that describes work as deferred, after it
> shipped, is not merely out of date — it is a place nobody looks for bugs.**

| # | Capability | Status | What is left |
|---|---|---|---|
| 1 | Autonomous multi-step agent loop | **BUILT** — bounded, consent-gated, in the daemon | **nothing open** — see below |
| 2 | Multi-model / user-selectable brains | **MECHANISM SHIPPED** — 9 active tiers; one defect fixed 2026-08-09 | b4–b6, then the parked ZDR item |
| 3 | Hybrid lexical+semantic retrieval | **DONE, LIVE-VERIFIED, re-measured** at `23550e4` | b7–b9 |

**Capability 1 has no open items, and how that happened is the point.** b1, b2 and b3 were all
written by me and **all three were wrong** — verified against source on 2026-08-09 rather than
re-read. b1 claimed the loop's reliability harness "runs nowhere"; it had conflated the billed
`-tags eval` harness (which measures loop *quality*) with the loop's *correctness* tests, which
are untagged, run in `make check`, and number **26**. b2 was already true. b3's premise was
false — `IncompleteInfo` is exactly the signal it asked for.

The two real items underneath them were found the same way and are both done:
**b2′** the repeated-call stall (`6b11c35` — the loop detected its second-worst failure mode
and did nothing about it), and **b1′** the cut-off answer reaching the screen but not the model
(`a4c9d15`, `f79d965`, `592e12c`), which was register item **L2** and turned out to have three
loss paths rather than one.

**Order of the rest.** **b4 first** — a user who picks a model and gets an unexplained refusal
is the worst live UX remaining, and `a30e664` fixed the refusal without fixing the silence.
Then **b5** (nothing binds the *deployed* proxy's allow-list to `models.json`; the local mirror
is now tested, the deployed one is not). Then **b7/b8**, which need measurement rather than
guessing. **The per-model ZDR verification is parked LAST and deliberately** — it is blocked on
OpenRouter, not on us, and §2 below states precisely why that park is safe.

Nothing in this section is CLOSED. Each remaining item needs its own decision to start, and the
security review still gates new capability.

### 1. Autonomous multi-step agent loop — **BUILT. This entry was stale.**

> **Status corrected 2026-08-09** by reading `daemon/agentloop.go`, not by recall. The text
> below said "deferred deliberately" long after the loop shipped. Kept rather than deleted,
> because the *reasons* it was deferred are the specification the shipped design had to meet —
> and its header answers them one by one.

**What shipped** (`daemon/agentloop.go`, `runAgentLoop`): a bounded, consent-gated loop living
in the daemon. Its own doc comment addresses each original objection:

- *"Removes the human-in-the-loop property"* — **it does not.** Lane A tools cannot write; the
  edit tool proposes into the existing diff review. Lane B tools require consent per call.
  Unspecified policy resolves to `ask`, which suspends the turn and puts the exact call in
  front of a person. Quoting the source: *a call runs because config says allow, or because a
  person said yes to those exact argument bytes.*
- *"Highest token-burn feature"* — bounded by four config-visible ceilings, and every
  termination reports which one bit.
- *"Must live in the daemon"* — it does.

**Robustness work still open on it** (none blocking, all local, none needing CI or a provider):

- ~~**b1.** The loop's reliability harness runs nowhere.~~ **WRONG, corrected same day.** This
  conflated `TestAgentLoopReliability` — a `-tags eval`, billed harness that measures loop
  *quality* against a real model — with the loop's *correctness* tests, which are comprehensive
  and untagged: 26 of them across `agentloop_test.go` and `toolconsent_test.go`, all running in
  `make check`. They cover every ceiling, ask/allow/deny, grant scoping and non-persistence,
  scrubbing, truncation, rune safety, the audit, and mid-turn provider failure. Nothing to
  build. *(Writing "largest untested surface in the project" about the single most thoroughly
  tested subsystem is the exact error this backlog keeps making: describing from memory instead
  of reading.)*
- ~~**b2.** Confirm each ceiling produces a distinct named termination.~~ **ALREADY TRUE.**
  `budgetStop` returns a separate user-facing `Detail` per ceiling, each naming the knob to
  raise (`max_iterations`, `turn_timeout_seconds`, `max_total_tool_bytes`), and each is covered
  by its own test.
- ~~**b3.** No signal when a loop stops on a ceiling.~~ **WRONG.** `IncompleteInfo` is exactly
  that signal, it is set on every budget stop, and both clients render it — the TUI as a
  persistent "⚠ answer cut off" note, VS Code via `onIncomplete`. A `Degradation` would have
  been a second vocabulary for a thing already reported.
- **b1′ — IMPLEMENTED AND VERIFIED 2026-08-09: the cut-off notice reached the SCREEN and not
  the MODEL.** Register item **L2**, both halves. The real version of what b3 was reaching
  for, and an agent-workflow correctness bug rather than a reporting one.

  **Three doors, not one**, which is why the first diagnosis was incomplete:

  | path | what happened | fix |
  |---|---|---|
  | within a session | TUI appended the notice as `roleSystem`; `buildHistory` drops `roleSystem`. VS Code posted `incomplete` to the webview and pushed a bare assistant turn. | `protocol.Turn.Incomplete` slug, set by both clients |
  | across sessions | `persistTurn` stored the truncated text unmarked, and it re-hydrated at every later handshake as a complete reply | note baked into stored content |
  | on error | TUI replayed the partial as finished; **VS Code dropped it entirely**, so the user saw text the model never would | both keep it, both mark it |

  `persistTurn` is the older bug: its doc comment promised a truncated answer is never stored
  as complete, and that held for the case it was written about — a mid-stream *failure*, which
  errors out and never reaches it. A stream ending early on `finish_reason` is a **successful**
  stream, so it walked straight past.

  **The client sends a SLUG, never prose.** The daemon owns every byte of wording
  (`incompleteHistoryNote`). A client able to attach a sentence would have regained exactly the
  inject-with-daemon-authority ability that `validTurn`'s role rule exists to remove; an
  unrecognized slug renders nothing rather than being echoed.
  `TestEveryIncompleteReasonHasAHistoryNote` parses the slugs out of `protocol.go`, so a
  seventh reason cannot be added and silently reach the model unmarked.

  Commits `a4c9d15`, `f79d965`, `592e12c`, `74473da`. Eleven neuters, all measured — one of
  which found a hole in the *guard*: the VS Code field check matched its own doc comment, so
  deleting the field left the test green.
- **b2′ — DONE 2026-08-09: the repeated-call stall.** `toolSignatures`' own comment called a
  repeated identical call "a loop's second-most-characteristic failure after not stopping", and
  the loop recorded it and did nothing: a stalled turn paid an iteration off `max_iterations`
  and the whole result off `max_total_tool_bytes` to learn nothing. Now, when a call repeats
  with identical arguments AND produces byte-identical output, the payload fed back is replaced
  by a short note telling the model it is repeating itself. **The tool still runs** — side
  effects are unchanged — and the match is on the rendered bytes, not the signature, so
  edit-then-read-back keeps working. Measured 2,600 bytes saved on a single repeat in the test.
  Neuter-verified both ways, including the guard that a changed result is never suppressed.

### 2. Multi-model / user-selectable brains — **MECHANISM SHIPPED, guardrail unmet**

> **Status corrected 2026-08-09.** This entry said "deferred deliberately". `models.json` ships
> **eleven tiers, nine active**, and `/model` lists and selects them in both clients. The
> capability is live; what has *not* happened is the verification this entry made a
> precondition.

**The defect this correction found, and fixed** — `proxy/main.go`'s `defaultAllowedModels` had
frozen at three slugs with a comment reading "the three tiers in models.json", while
models.json grew to eleven. Measured: **eight of the nine selectable models were refused
`model_not_allowed`** by the managed proxy; only the default tier worked. Worse, two of the
three slugs it *did* allow were the two INACTIVE tiers. Safe (a refusal, never a leak — ZDR
enforcement is per-request and fail-closed regardless of model) but silently broken for every
managed-proxy user. Fixed, and `proxy/modelallowparity_test.go` now fails in **both**
directions if the two lists drift again: a shipped model the proxy refuses is a broken model, a
proxy entry with no shipped tier is unauthorized spend.

**Robustness work still open:**

- **b4.** No client-side signal when the proxy refuses a model. The user picks a tier, sends a
  prompt, and gets a refusal with no mapping back to "that model is not available on your
  plan". `/model` should mark unavailable tiers, or the refusal should name the tier.
- **b5.** `models.json` is the manifest for the client and the proxy independently. The parity
  test binds them at build time; nothing binds a *deployed* proxy's `ALLOWED_MODELS` to the
  models.json a given user actually has. Worth a `status`-time reconciliation.
- **b6.** Per-model cost differs and metering does not yet distinguish. See Phase 4 billing.

#### The original reasoning, kept because two of its three points still bind

- (a) Contradicts the managed-key cost model — different models have different per-token
  prices and would complicate metering and the fixed PPP token-cap math (see Phase 4's
  billing/metering item). **Still true — this is b6 above.**
- (b) ZDR availability is per-model. See the parked item below.
- (c) No user has requested it. **Superseded** — the tiers shipped regardless.
- **Original framing note, still worth re-reading before widening the menu further:** for the
  cost-sensitive Indian market, curation ("we picked the best cheap private model for you") is
  likely a stronger position than choice. Nine tiers is a lot of choice for a product whose
  pitch is that it does the thinking about safety for you.

#### ⏸️ PARKED LAST — per-model ZDR live-verification

**Blocked on OpenRouter, not on us. Do this after everything else in this section.**

The guardrail this entry set was: *every user-selectable model must pass the same ZDR
live-verification (positive + negative path) as `deepseek-v4-flash` before being offered.*
Nine are offered; **only `deepseek/deepseek-v4-flash` has that verification on record**
(2026-07-09). The other eight do not.

Why it is parked rather than scheduled: **OpenRouter has not given a usable answer**, and the
verification is a claim about *their* provider routing that cannot be manufactured from this
side. Guessing would produce exactly the unverified assurance this project refuses to ship.

**What makes the park safe, stated precisely so nobody mistakes parked for ignored:**

1. The proxy enforces ZDR **per request**, fail-closed, regardless of model — a request without
   the retention flags is refused `403 zdr_required` before anything is reserved or forwarded.
   Verified live by wire probe.
2. With those flags set, OpenRouter can only route to ZDR-compliant providers. A model with
   none cannot be served — it fails to route rather than leaking.

So the residual is **availability, not privacy**: an unverified model either works under ZDR or
does not work at all. That is a materially different risk from the one this guardrail was
written to prevent, and it is why the park is defensible.

**What it still costs, and why it cannot stay parked forever:** ~123 of the 343 listed
OpenRouter models had zero ZDR-compliant providers when this was last checked. If several of
the eight are in that set, those tiers are dead menu entries that fail at routing time with an
error the product does not explain (**b4** covers the signal; this covers the fact). Before any
paying user, either verify the eight or cut `models.json` back to what is verified.

**Definition of done:** positive + negative path recorded per model, in this file, with a date
— the same shape as the `deepseek-v4-flash` record.

### 3. Hybrid lexical+semantic retrieval — **DONE, LIVE-VERIFIED, and re-measured 2026-08-08**

> **Re-measured at `23550e4`**, offline, against the project's own eval set — see
> [`docs/RETRIEVAL_EVAL_CHECKPOINT_2026-08-08.md`](docs/RETRIEVAL_EVAL_CHECKPOINT_2026-08-08.md).
> Top-3 recall **15/15**; chunk-level recall **8/9** under BOTH semantic-only and hybrid;
> token savings **90.1%** over the 13 full-hit queries of 20. Retrieval costs **~14 ms** of a
> ~1,450 ms first token — about 1%, where model prefill is 78%.
>
> **Robustness work still open on it:**
>
> - **b7.** The 8/9 gap is one query — *"where does the daemon open the unix socket"* — and it
>   misses under hybrid too, so the lexical tier did not rescue it. Named as a known gap in
>   the harness and not gated. Worth one focused look now that the RRF grid is known flat.
> - **b8.** `TestEditShapedRetrievalEval` is excluded from CI because three of its four cases
>   can no longer build their fixtures (the reverts conflict, 178–221 commits behind). Its one
>   case that DOES run misses at rank **#109**. That is the only edit-shaped retrieval
>   measurement anyone has, and it is bad. Rebuild the eval around current queries with
>   labelled ground truth; the harness expires by design.
> - **b9.** Retrieval quality is verified **weekly**, not per-push — the eval job is
>   schedule-only for good reasons (it indexes the whole repo per test). A regression
>   introduced today is caught within seven days, not at the commit that caused it.

**Original entry, kept for the shipping record:**

**DONE — LIVE-VERIFIED: Hybrid lexical+semantic code retrieval — the real fix for lexical-miss defects**
**Shipped 2026-07-10, commit `ae3e7a4` ("Add lexical retrieval tier fused with semantic via
max-based RRF"), tag `hybrid-retrieval-complete`.** Adds an FTS5/trigram lexical tier
(`daemon/lexicalstore.go`) fused with the existing semantic tier via max-based Reciprocal Rank
Fusion (K=60), threaded through `setupRetrieval` -> `Server` -> `retrieveTopK` so the lexical
tier degrades independently of the semantic one.

**Live-verified against the real daemon (not just the eval harness), same day:**
- "Where is the ZDR refusal string matched" — correctly answers `daemon/provider.go` /
  `isZDRRoutingRefusal`, with all three substrings named.
- "What files does SearchRequest touch" — walks the full chain (`protocol.go`, `server.go`
  `isSearchRequest`/`handleSearch`, `search.go`, both clients, test files) with a correct
  summary.

Both queries were confirmed misses this morning under semantic-only retrieval; both now pass.

**Two honest caveats stay open — do not treat this as fully closed:**
- **Eval set is saturated.** Chunk-level eval is 8/9 hits with scores bunched tightly
  (~0.0122-0.0189 weighted). That resolution is too coarse to prove the RRF K=60 fusion
  weighting is actually tuned well — it only proves it isn't broken. Grow the eval set
  (harder/more adversarial lexical-miss queries) before treating this tuning as load-bearing.
- **Token-cost/efficiency claim is MEASURED (2026-07-17): 67.4% average savings, but with a real
  failure mode.** Measured by `daemon/token_efficiency_eval_test.go` (build-tagged `eval`, same
  pattern as the other eval harnesses): the real retrieval path (`retrieveTopK` +
  `truncateToBudget`, production defaults `topK=5`/`contextBudgetChars=8000`) vs. a naive
  whole-file-inclusion baseline, over a 20-query set with pre-established ground truth against this
  actual repo. Headline: **67.4% average token savings** over the 17/20 queries where retrieval
  fully found its ground-truth file(s) (~34k vs. ~104k estimated tokens). This is NOT the ~95%
  that was floated externally with no measurement behind it — that figure never appeared anywhere
  in this repo and is not supported. **Failure mode, do not hide it:** ~15% of queries (3/20 —
  parsing edit blocks, model-tier routing, secret scrubbing) had *negative* savings — retrieval
  cost MORE tokens than just including the whole target file. Root cause is structural: injected
  context size is roughly fixed (~6.5-8k chars; `topK=5` × ~40-line chunks nearly fills the 8k
  budget regardless of query), while the naive baseline scales with the answer file's size. So the
  system wins big when the answer lives in a large file or spans multiple files (77-89% savings on
  `proxy/main.go`-touching and cross-file queries) and loses when the honest answer is one small,
  tightly-scoped file (`router.go` at 44 lines, `scrub.go` at 79). The 67.4% number is therefore
  file-size-distribution-dependent and would move on a differently-structured codebase — many
  small atomic files would show more negative cases; more large files, fewer. See the benchmark
  file's header for full methodology and caveats. **The negative-savings cases were investigated
  and deliberately NOT fixed** (closed, not pending — see `RETRIEVAL_BUDGET_DESIGN.md`): they're
  structural (fixed injected-context size vs. a baseline that scales with the answer file), and
  every mechanism that would flip them either can't work on this rank-fused architecture or
  provably regresses a cross-file winner (the poison-pill: "why does the proxy reserve tokens" has
  its top-1 chunk in the small `reserve_usage.sql` but its answer in the large `proxy/main.go`, so
  any size-cap keyed on the top file drops main.go and turns a +78% win into a MISS). The one
  provably-safe mechanism (same-file chunk consolidation) was built and measured — it fires on
  1/20 queries, saves ~600 chars, and flips none of the three cases — so it doesn't earn the
  permanent complexity it adds to the retrieval hot path. Mitigating context that makes this an
  easy call: the queries where retrieval loses are the *small single-file* answers — the ones that
  were never hard in the first place — while the big wins (77-89%) are on large and cross-file
  queries, exactly where they matter.

One pre-existing, non-gated gap remains: "where does the daemon open the unix socket" still
misses at chunk-level under both semantic-only and hybrid retrieval (see `rerank_eval_test.go`'s
comment on this query for why it's out of scope here).

- **Evidence** (from the 2026-07-10 test-file-ranking investigation): live-querying the real
  repo for "where in the code is the ZDR refusal string matched, and what substring does it
  match on?" showed the chunk that actually answers it (`daemon/provider.go`'s
  `zdrRefusalSubstrings`/`isZDRRoutingRefusal`) ranked **#220 of 697** chunks by raw embedding
  similarity — outside any pool size that's practical to overfetch. An adjacent-but-incomplete
  chunk from the same file fared only slightly better (#18-22, on the edge of a 30-candidate
  pool, and only reachable at all once test-file down-weighting stopped burying it — see item
  (b) below).
- **Root cause, distinct from the test-file-ranking bug**: pure-semantic (bi-encoder embedding)
  retrieval is structurally biased against terse implementation code. A short var/func
  definition embeds farther from a natural-language question than a verbose test name or an
  explanatory comment that happens to echo the query's own vocabulary — even when the
  definition is the literal, correct answer. This is a property of the embedding model and
  chunk granularity, not of file classification.
- **Why the intent-gated test down-weight (item (b)) does NOT fix this class of miss**: that
  fix only reorders candidates already fetched into the rerank pool. If the correct chunk was
  never fetched in the first place (as in the #220 case above), no amount of reweighting can
  recover it. Confirmed live: after shipping the down-weight fix, the ZDR query's own
  right-chunk-adjacent candidate still missed the top-5 by a hair (~0.1% weighted-score gap)
  once test-file competition was removed — the residual bottleneck is raw similarity, not class
  weight.
- **Promising angle**: the FTS5 lexical search machinery already built for cross-session
  conversation memory (`turns_fts`, bm25-ranked, see "FTS5 search session" above) is a proven,
  low-risk pattern for a lexical index — trigram tokenizer, no new dependency, already
  live-verified end-to-end. The natural next step is a *parallel* FTS5 index over code chunks
  (not turns), whose bm25 hits get merged with the existing embedding-based candidate pool
  before reranking — giving exact-token queries (identifier names, string literals like
  `"zero data retention"`) a path to the correct chunk that pure embedding similarity can't
  reliably provide.
- **Shipped as scoped**: the measured eval was reused/extended (`rerank_eval_test.go`'s
  real-repo harness now gates 9 chunk-level queries, up from 7, including the ZDR
  string/identifier query above and the SearchRequest query), fusion was implemented as
  max-based Reciprocal Rank Fusion (K=60), and none of the previously-gated queries regressed
  (6/9 -> 8/9 chunk-level, no prior hit turned into a miss). See the DONE status block at the
  top of this item for what's still open (eval saturation, unmeasured efficiency claim).

### Carried forward from earlier notes (consolidated here; full detail at their original entries)
- **Model-callable `session_search` tool over backup/session history** (referred to
  elsewhere as "Option 2") — full detail in item (i) above. Distinct from the FTS5 lexical
  search over *conversation memory* that DID ship (VS Code panel, 2026-07-09): this would be
  the model itself querying past apply/backup runs, not a human clicking Undo. Deferred until
  there's a concrete need for the model to query its own past runs.
- **Self-learning loop (autonomous skill capture)** — the skill store itself is built
  (`daemon/skills.go`: `AddSkill`/`GetSkill`/`ListSkills`/`DeleteSkill`, per-user SQLite at
  `~/.codeterminal/skills.db`), but nothing yet decides *on its own*, after a successful
  multi-step fix, to mine that session and call `AddSkill` without a human curating it. That
  autonomous capture loop is what's deferred — same shape of risk as the autonomous agent
  loop above (less human oversight of what gets written/remembered), so gate it behind the
  same security review.
- **TUI search UI** — the FTS5 lexical search feature (wire protocol + daemon dispatch + UI)
  shipped end-to-end but only for the VS Code panel ("FTS5 search session", 2026-07-09). The
  TUI client has no equivalent search box yet; adding one is mechanical (same
  `SearchRequest`/`SearchResponse` the VS Code panel already uses) but deferred as a UI-only
  gap, not a blocker.
- **Ghost text** (fast keystroke completions) — `models.json`'s `ghost_text` tier already
  names a candidate model (`qwen/qwen3-coder-30b-a3b-instruct`) but is `"active": false`; see
  "VS Code extension: remaining capabilities" above. Deferred to Phase 3.5.
- **Terminal error interceptor** — see "VS Code extension: remaining capabilities" above.
  Not started.
- **Retrieval refinements** — items (b) (down-weight test files in top-1 ranking) and (f)
  (implementation chunks ranking below setup chunks) above. Both are measured, minor,
  non-blocking sharpening of a system that's already grounded and correct; do as a MEASURED
  step against an eval, not a guess, when retrieval-quality hardening becomes the priority.
  Item (b)'s fix, once shipped, only closed the "test files dominate" failure mode — the
  deeper, separate limitation it couldn't reach (correct chunk too dissimilar in raw embedding
  space to be fetched at all) is what North Star item 3 (hybrid lexical+semantic retrieval)
  fixed; see that item's DONE status block for what's shipped vs. still open.
- **Turns retention / pruning policy** — item (h) above: cross-session memory grows
  unbounded by design (persist-all). Add an age- or count-based prune when a long-lived
  workspace actually needs it.

## Hygiene / recurring

- **Never screenshot .env / keep the API key off-screen.** The key has been exposed in
  screenshots multiple times; rotate as routine. (Rotated 2026-07-08.)

- **`api_keys.key_prefix` is NOT unique — always target rows by `id`.** It's the first 8 chars
  of the raw key, so any script deriving it from a fixed literal collides across runs. Has bitten
  twice: a `mochi_te*` filter matched two rows during a deactivation (a prefix-scoped PATCH would
  have silently written both), and the quota test script has left two identical `mochi_qt` rows.
  Use `id=eq.<uuid>`, never `key_prefix=like.<prefix>*`.
- **Rebuild affected binaries after code changes** — daemon + TUI + extension; source and
  binary drift, especially when a change spans modules.
- **Stale scratch git worktree from a prior P3-experiment session lingers on disk** (outside the
  repo, harmless) — clean up (`git worktree prune`/`remove`) or explicitly note as intentionally kept.
- **`daemon/edit_eval_test.go` is n=4, one short of its n=5 target** — case 2ee3545 ("Fix
  embedding timeout: batch buildIndex") is excluded because it doesn't revert cleanly against
  current HEAD (conflicts with later `index_cmd.go` rewrites); close by resolving that revert
  conflict or mining a clean 5th defect-fix commit.

