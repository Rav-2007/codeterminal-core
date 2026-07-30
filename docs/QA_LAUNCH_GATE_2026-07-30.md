# Launch-Gate QA Review — 2026-07-30

> **REMEDIATION STATUS — appended 2026-07-30, after the review.**
>
> All ten findings (1 P0, 4 P1, 5 P2) have been ADDRESSED on
> `harden/proxy-spend-and-gates`, one isolated commit each, plus P3-1. The
> findings below are left EXACTLY as originally written — nothing is edited to
> look as though it was never there — and each carries a `RESOLVED` note naming
> its commit and stating what was actually done, including where the fix
> differed from the recommendation.
>
> | Finding | Commit | Status |
> |---|---|---|
> | P0-1 byte-guard refund | `efe2bda` | FIXED, fail-when-neutered test |
> | P1-1 invisible budget kill | `b1fed6b`, `2e68d9d` | FIXED, 2 seam tests |
> | P1-2 CI gates nothing | `0f3bca2` | FIXED, unverified until first CI run |
> | P1-3 retrieval gate RED | `96924a7` | FIXED — root cause was NOT retrieval |
> | P1-4 schema not in VCS | `50ff5b3` | FILE WRITTEN, founder must apply/verify |
> | P2-1 duplicate JSON keys | `f8416a5` | FIXED |
> | P2-2 daemon dispatcher | `fd8d865` | FIXED |
> | P2-3 webview accessibility | `74adfea` | STRUCTURE FIXED, screen-reader passes still blocked |
> | P2-4 `increment_usage` revoke | `50ff5b3` | FILE WRITTEN, founder must apply/verify |
> | P2-5 zero coverage | `ae6bbfe` | FIXED (protocol 88.9%, chunkscrub 100%) |
> | P3-1 broken test command | (docs commit) | FIXED |
>
> **The verdict below is NOT retracted.** It was correct when written. Whether
> the gate now passes is the founder's call, and two P1-class items remain
> genuinely open: applying the migrations, and the first green CI run.
>
> **Two corrections to this report, found while remediating:**
>
> 1. **P1-3's diagnosis was wrong, and the report's framing propagated it.**
>    The eval was red because its GROUND TRUTH had gone stale, not because
>    retrieval had regressed. Five of nine queries pointed at line ranges whose
>    contents had moved; the ranker was returning the correct chunks and being
>    scored against the wrong answer. Corrected expectations restore 8/9 — the
>    figure `fuseRRF`'s doc comment records from the original grid search. The
>    quoted assertion messages ("still MISS under hybrid retrieval") were the
>    harness misreporting, not a measurement of the feature.
> 2. **A second eval test was also RED at baseline and went unreported.**
>    `TestEditShapedRetrievalEval` fails 0/4, and a `git worktree` at `17ffad6`
>    reproduces it identically — so it predates this branch. The baseline
>    section's account of the four gated eval tests is incomplete. Tracked as
>    the open H6 workstream in `BACKLOG.md`.

Whole-product adversarial QA pass across all seven surfaces, run locally against
branch `harden/proxy-spend-and-gates` (2 commits ahead of `main`, HEAD `17ffad6`).

**Depth:** local build + test execution only. No production contact — no Railway
probes, no live Supabase queries, no real spend.
**Evidence discipline:** `CONFIRMED` = reproduced by a script that was run.
`PLAUSIBLE` = reasoned from source, not reproduced. Nothing is asserted without
one of those two labels. Repro bundle paths are given per finding.

---

## Executive Summary

**Overall Quality Score: 6.5 / 10**

The engineering quality of the shipped code is high and unusually well
documented — 584 tests, 87% / 79.7% coverage on the two safety-critical modules,
`gofmt`/`vet` clean across all six Go modules, zero reachable vulnerabilities,
no secrets in git history, and a thoughtful fail-closed posture almost
everywhere I attacked it. Four of my ten pre-registered attack hypotheses were
**refuted by the code being correct**, which is a good ratio.

The score is not higher because the *process* around that code does not defend
it: **CI runs no tests at all**, the project's own retrieval-quality gate is
currently red, the core billing schema is not in version control, and the one
subsystem the unmerged branch rewrote — per-request spend — contains a
reproducible refund-on-maximum-spend defect of exactly the class this repo
already fixed once (C3).

**Release Decision: ❌ FAIL**

**Confidence: High** for the seven surfaces exercised locally.
**Medium** overall — the live-Supabase, multi-replica, and production-wire axes
were out of scope by agreement and remain unverified (see *Blocked, not skipped*).

| Surface | Verdict |
|---|---|
| Proxy: spend ceiling & body gates | ❌ FAIL (1 P0, 1 P2) |
| Proxy ↔ daemon integration | ❌ FAIL (1 P1) |
| Daemon socket API | ⚠️ RISKS (1 P2) |
| editapply five gates | ✅ PASS |
| Retrieval / privacy scrub | ⚠️ RISKS (eval red, 1 P2) |
| Clients (VS Code / TUI) | ⚠️ RISKS (1 P2 accessibility) |
| Database & migrations | ❌ FAIL (1 P1, 1 P2) |
| CI / production readiness | ❌ FAIL (1 P1) |

---

## Baseline evidence (Phase 0)

Everything below is measured, not estimated.

| Module | Tests | Coverage | Race | gofmt | vet | govulncheck |
|---|---:|---:|---|---|---|---|
| `daemon` | 369 | 66.9% | clean | clean | clean | 0 reachable |
| `editapply` | 86 | **87.0%** | clean | clean | clean | 0 |
| `proxy` | 52 | **79.7%** | clean | clean | clean | 0 |
| `clients/tui` | 77 | 66.7% | clean | clean | clean | 0 reachable |
| `protocol` | **0** | **0.0%** | — | clean | clean | 0 |
| `helper` | **0** | **0.0%** | — | clean | clean | 0 |
| `clients/vscode` | 6 (real EDH) | n/a | — | `tsc` clean | — | — |

- The branch's claimed "tests 28 → 52, coverage 79.7%" is **confirmed exactly**.
- `-tags eval` adds 4 gated tests. `TestEvalRetrievalQuality` passes with top-1
  1.00 and top-3 recall **1.00** (threshold 0.80). `TestRerankEvalRetrievalRanking`
  **FAILS** — see P1-3.
- VS Code Extension Development Host launches for real and passes 6/6, including
  the M1/M2/M3 client-half regressions.

**Correction to my own pre-review gap map:** I flagged `proxy/ratelimit.go`,
`editapply/match.go`, `protected.go`, `workspace.go` and `atomicwrite.go` as
untested on a sibling-filename heuristic. Per-function coverage refutes that —
`ratelimit.go` is ~100% covered, `protected.go` 100%, `match.go` 80–100% per
function. The heuristic was wrong; tests live in differently-named files. The
two genuine zeros are `protocol` and `helper`.

---

## Critical Issues (P0)

### P0-1 — A stream killed by the byte guard refunds almost its entire reservation

**Status: CONFIRMED** · `proxy/main.go:1056-1066` → `proxy/main.go:1568-1576`
· Repro: `scratchpad/repro/proxy/zz_qa_repro_test.go::TestQA_P0_1_ByteGuardKillMustNotRefund`

**Description.** `streamSSE`'s budget kill raises the charge to the chunk count:

```go
if dataChunks > totalTokens { totalTokens = dataChunks }   // main.go:1061
```

`dataChunks` is a **chunk count**, not a token count. `budgetExceeded` has two
independent bounds, and on the **`byte_guard`** bound the chunk count is by
definition tiny while the bytes streamed are at the ceiling. `finalizeUsage` then
takes its `actual > 0` branch and hands `correctUsage` a large **negative** delta.

The code comment at that site states the intended invariant — *"Charge what was
streamed, never refund"* — and it does not hold. It holds only when
`dataChunks >= reserved`, which is guaranteed on a `token_ceiling` kill and
**never** on a `byte_guard` kill.

**Evidence (measured).** Reservation 4096, headroom 100 → ceiling 4196 → byte
guard at 2,148,352 bytes. Upstream streams 3 × 900 KB:

```
reserved=4096  correction delta=-4093
finalizeUsage got actual=3 vs reserved=4096
```

A request that streamed 2.7 MB — the maximum its ceiling permits — was **refunded
4093 of its 4096 reserved tokens**. The same figure is charged to the per-key
token-rate bucket (`finalizeUsage:1562-1566`), so both new controls are defeated
by one defect.

**Impact.** Repeatable free paid inference. This is the **C3 abort-refund bug
class**, which this proxy already found, fixed, and wrote a regression test for
(`a703978`) — reintroduced through a new path that C3's test does not cover.

**Reachability is worse than it looks, and inverted.** The refund needs
`dataChunks < reserved` when the byte guard trips, i.e. average chunk size
> 512 B. The guard scales with the ceiling, so the *smaller* a key's remaining
headroom, the smaller the ceiling, the easier the byte guard fires, and the
larger the refund **as a fraction of the key's remaining quota**. A key at its
limit is the easiest to exploit and gains the most — a perverse incentive
gradient. `deepseek/deepseek-r1` is in the shipped default allow-list and is a
reasoning model whose chunks carry `reasoning` deltas well above 512 B, so this
does not require an adversarial provider.

**Recommendation.** Never let a budget kill produce a negative delta. Floor the
charge at the reservation on any kill path:

> **RESOLVED — `efe2bda`.** Fixed as recommended, generalized. Rather than a
> bare floor at the kill site, the charge decision is now
> `chargeForKill(measured, dataChunks, reserved) = max of the three`, a named
> function with the invariant in its doc comment, so a third bound added to
> `budgetExceeded` later inherits it. Measured delta went `-4093` → `0`.
> Tests: byte_guard non-negative delta, byte_guard token-rate bucket charged at
> least the reservation (reading the bucket BALANCE — `available()` is a `>= 1`
> boolean against a 120000 burst and cannot tell 3 from 4096), and a
> `chargeForKill` unit table. All fail with the control neutered.
> The pre-existing token_ceiling test accepted a `-4085` partial refund and now
> asserts no refund of any size.

```go
if totalTokens < reserved { totalTokens = reserved }
```

or pass an explicit `killed bool` into `finalizeUsage` and force the
"keep the reservation" branch, which already exists for precisely this
"produced output, no trustworthy figure" case. Add a fail-when-neutered test on
the **`byte_guard`** bound specifically — the existing budget tests exercise
`token_ceiling`, which is why this survived a green 52-test suite.

---

## High Priority Issues (P1)

### P1-1 — The `budget_exceeded` chunk is invisible to the shipped daemon; killed answers are reported as successful

**Status: CONFIRMED** · `proxy/main.go:1114-1120` vs `daemon/provider.go:83-114`
· Repro: `scratchpad/repro/daemon_budget_chunk_repro_test.go.txt`

**Description.** The proxy emits, on a kill:

```
data: {"error":"budget_exceeded","truncated":true}
data: [DONE]
```

`writeBudgetExceeded`'s doc comment states its whole purpose: *"so a client can
render 'response truncated: budget exceeded' and, crucially, tell throttling
apart from a crashed connection. A silent close would be indistinguishable from a
network failure."*

The daemon sits between the proxy and **every** client, and `chatCompletionChunk`
(`daemon/provider.go:83`) has fields for `provider`, `choices[].delta.content`,
`choices[].delta.reasoning` and `choices[].finish_reason` — and **no `error`
field**. The kill chunk therefore decodes to an entirely empty chunk.

**Evidence (measured).**
```
decoded provider="" choices=0
incompleteInfoFor("") = <nil>
```
No content, no `finish_reason`, so the M1 truncation path (`incompleteInfoFor`)
does not fire either. The daemon then reads `data: [DONE]` and reports
`Done:true` with no `Error` and no `IncompleteInfo`.

**Impact.** A budget-killed answer reaches the user as a **complete, successful
response** that is silently truncated. That is strictly worse than the silent
close the chunk was written to avoid: it is indistinguishable from *success*,
not from a network failure. Users get quietly wrong answers and cannot tell.

This is a seam defect: the proxy suite asserts the chunk is emitted, the daemon
suite asserts its own parsing, and **no test spans the two**. There is no
proxy↔daemon integration test anywhere in the repo.

**Recommendation.** Add an `Error string \`json:"error"\`` (and `Truncated bool`)
to `chatCompletionChunk`, map it to a new `protocol.Incomplete*` reason
(`"budget_exceeded"`), and surface it through the existing `onIncomplete`
path that M1 already built and both clients already render. Then add one
integration test that runs the real proxy handler in front of the real daemon
stream parser.

> **RESOLVED — `b1fed6b`, extended by `2e68d9d`.** Implemented via the existing
> `onIncomplete` path as recommended, with one deliberate departure: the field is
> `json.RawMessage`, **not** `string`. OpenRouter also sends `error` as an
> OBJECT, and a `string` field would make the whole chunk fail to unmarshal and
> hit the `continue`, silently discarding content that chunk also carried —
> turning a missing signal into a content-loss bug. `errorSlug` accepts both
> shapes. The check sits ABOVE the zero-choices guard, since the kill chunk has
> no choices; that placement is separately neuter-tested.
> Both clients already render `IncompleteInfo.Detail` generically — verified,
> not assumed — so no client change was needed.
> Two integration tests, the repo's first: the real proxy binary in front of the
> real parser, and the same carried through `Server.serveConn` to assert the
> WIRE BYTES, which is where this finding's symptom was actually stated.

### P1-2 — CI runs no tests, no linters, and no type-check

**Status: CONFIRMED** · `.github/workflows/build.yml`

The entire pipeline is one job: `docker build -t codeterminal-proxy:ci ./proxy`.

**584 Go tests, 6 extension E2E tests, 4 gated eval tests, `gofmt`, `go vet`,
`govulncheck` and `tsc` exist in this repo and not one of them gates a merge.**
Every green result in this report was produced by me running them by hand.

**Impact.** Nothing mechanically prevents any of the defects in this report from
landing on `main`. The two CONFIRMED regressions above (P0-1, P1-1) both sit in
code that shipped with a green local suite; a suite nobody is required to run is
a suite that will drift. This also explains why the eval gate (P1-3) could go red
unnoticed.

**Recommendation.** Extend `build.yml`: matrix over the six modules running
`go build`, `go vet`, `gofmt -l` (fail on output), `go test -race`; plus
`npm ci && npm run compile && xvfb-run -a npm test` for the extension; plus
`govulncheck`. Run `-tags eval` on a schedule rather than per-PR (74 s + model
download). Keep the existing image build.

> **RESOLVED — `0f3bca2`.** Implemented as recommended. Every command was run
> locally first, command for command: build/gofmt/vet/`go test -race` green on
> all six modules, `govulncheck` clean on all six, `tsc` clean, EDH 6/6 (now
> 14/14).
> **Not verified:** the `xvfb-run -a` wrapper itself — this machine has no xvfb,
> so the EDH run used a real display. Unproven until the first CI run.
> The scheduled eval job is scoped by `-run` to the three GREEN eval tests; see
> correction 2 in the banner for why.

### P1-3 — The project's own retrieval-quality gate is currently RED

**Status: CONFIRMED** · `daemon/rerank_eval_test.go:387`
· Log: `scratchpad/logs/eval_run1.log`

```
semantic-only chunk-level recall: 4/9
hybrid chunk-level recall:        4/9
--- FAIL: TestRerankEvalRetrievalRanking (74.04s)
```

The test's own assertion messages name the failure precisely:

> query 8 ("where is the ZDR refusal string matched") — one of the two measured
> live failures this feature exists to fix — is still MISS under hybrid retrieval
> query 9 ("what files does SearchRequest touch") — … is still MISS

So the reranking feature does not achieve the two outcomes it was built for, and
hybrid retrieval scores **no better than semantic-only** (4/9 both). The
file-level eval (`TestEvalRetrievalQuality`) is perfect at top-3 recall 1.00, so
the gap is specifically chunk-level ranking.

**Impact.** Retrieval quality is the product's core value claim ("answers cite
YOUR code"). Shipping with the quality gate red means shipping below the bar the
project set for itself. Compounded by P1-2: because CI never runs `-tags eval`,
this is invisible in normal development.

**Recommendation.** Either fix queries 8/9 or explicitly re-baseline the
assertion with a written justification, so the suite's red/green state means
something. Do not launch with a red gate and no decision recorded.

> **RESOLVED — `96924a7`. The diagnosis in this finding is wrong; see banner
> correction 1.** A diagnostic over both retrieval tiers showed the expected
> chunk was absent from BOTH candidate lists at depth 30 — so fusion was not
> discarding a lexical contribution, there was nothing to discard, and the
> expected chunk did not contain the answer. Five of nine `exactChunks` had gone
> stale (query 8 expected `daemon/provider.go:91-130`, which holds the chunk
> struct; the ZDR matching is ~150 lines further down — and this was already
> true at `17ffad6`).
> Corrected against the SOURCE — grep the symbol, compute covering chunks from
> the chunker's stride — never against ranker output, which would be teaching to
> the test. Result: **8/9**, all three gated queries hit.
> So this was NOT the "re-baseline lower with a justification" outcome. Lowering
> the gate to 4/9 would have enshrined a broken measurement and hidden real
> regressions. Gate is now `evalChunkRecallFloor = 8`, a floor not an equality.
> Durable part: an `anchor` field plus `assertExpectationsAreCurrent`, which runs
> before any query and distinguishes a stale harness from a retrieval regression
> BY NAME, printing where the anchor actually lives. It immediately caught two
> chunk IDs derived wrongly while writing the fix.

### P1-4 — The core billing schema is not in version control, and migrations have no runner, no versioning, and no rollback

**Status: CONFIRMED** · `proxy/migrations/`

Three findings that compound into one operational risk:

1. **No DDL for `usage` or `api_keys`.** The only `create table` in the repo is
   `pending_corrections` (`0002:35`). The two tables the entire billing system
   depends on exist only in the Supabase dashboard. The repo **cannot recreate
   its own database**, and constraints cannot be verified from source — including
   whether `usage.key_id` is unique, which `reserve_usage`'s correctness depends
   on (its `select ... from reserved, opened` is a cross join; with duplicate
   `key_id` rows it would multiply).
2. **No migration runner and no applied-version tracking.** Migrations are pasted
   into the SQL editor by hand. Nothing detects a schema/binary mismatch — while
   `0002`'s own banner warns that getting the order wrong means *"every correction
   on every request would be lost outright."* That constraint is enforced only by
   a human reading a comment, and the record shows it has already gone wrong once
   (a premature "applied" note, corrected 2026-07-25).
3. **No down/rollback scripts.** A bad migration has no scripted reversal.

**Recommendation.** Capture `usage`/`api_keys` DDL in an `0000_baseline.sql`
reconstructed from the live schema and verified against it; add a
`schema_migrations` table plus a startup assertion that the expected version is
present (fail closed, matching the posture everywhere else); add down scripts.

> **PARTIALLY RESOLVED — `50ff5b3`. Founder action still required.**
> `0000_baseline_usage_api_keys.sql` written, with every column's provenance
> named and everything not recoverable from code marked GUESSED inline. It is
> labelled RECONSTRUCTED and UNVERIFIED, and states that production is the
> authority. It carries the `usage.key_id` uniqueness constraint plus both the
> constraint query and a live duplicate-check query.
> Also documented while reconstructing: `token_limit NOT NULL` is load-bearing —
> a NULL makes `reserve_usage`'s `<=` guard evaluate NULL, the UPDATE match
> nothing, and the function return zero rows, which the proxy reads as "refuse".
> Fails closed, but as a total outage for that key.
> **NOT done, per the decision taken:** no migration runner, no
> `schema_migrations` table, no startup assertion, no down scripts. **Applying
> the file and answering the uniqueness question remain founder-gated.**

---

## Medium Issues (P2)

### P2-1 — Duplicate top-level JSON keys: gate verdict and forwarded bytes disagree

**Status: CONFIRMED (proxy-side half)** · `proxy/main.go:1275-1281`
· Repro: `TestQA_M2_DuplicateKeysJudgedOnLastForwardedWithBoth`

All four body gates read through `topLevelFields` → `json.Unmarshal` into
`map[string]json.RawMessage`, which resolves duplicate keys **last-wins**. The
body is then forwarded **byte-for-byte** carrying both.

```
gate admitted the request reading model="cheap-model" (last)
forwarded: {"model":"expensive-model","model":"cheap-model",...}
```

The cost-authorization allow-list permitted a request whose forwarded bytes name
a **banned** model first. Whether that bypass completes depends on OpenRouter's
parser resolving duplicates first-wins — **not tested, out of scope, and RFC 8259
leaves it explicitly undefined.** The proxy-side inconsistency is confirmed; the
end-to-end exploit is `PLAUSIBLE`.

This is the same shape as branch defect 3 (case-insensitive struct-tag matching)
one level down: the proxy's claim to be *the authority* on `model` / `provider` /
`stream` / `max_tokens` holds only if the provider resolves ambiguity identically.

**Recommendation.** Reject bodies containing duplicate top-level keys (stream the
JSON with `json.Decoder` token-wise and 400 on a repeat), rather than relying on
the two parsers agreeing. Cheap, and removes the dependency entirely.

> **RESOLVED — `f8416a5`.** Implemented as recommended, and extended to NESTED
> objects so a duplicate inside `provider` (where the ZDR flags live) is caught
> too. 400, not 403: an ambiguous body is malformed, not a policy refusal.
> Placed before `costSurfaceRefusal` so no gate ever judges one. Recursion
> bounded at 64 levels — a 4MB body of `[[[[...` would otherwise recurse millions
> of frames — and over-depth refuses.
> Values stay opaque; only key names are compared. The shipped daemon is
> unaffected, asserted both by unit case and by the seam test putting a genuinely
> daemon-marshalled body through the real proxy binary.

### P2-2 — Daemon request dispatcher is case-insensitive and duplicate-key-tolerant, routing into a destructive handler

**Status: CONFIRMED** · `daemon/server.go:473, 583, 691`
· Repro: `scratchpad/repro/daemon_sniffer_repro_test.go.txt`

The dispatcher sniffs request type by decoding into structs with json tags — the
exact pattern branch commit `491e0f2` identified as defective and replaced in the
proxy with exact-key lookup. The daemon was never updated.

```
{"UNDO":true}   -> isUndoRequest       = true
{"Undo":true}   -> isUndoRequest       = true
{"SEARCH":true} -> isSearchRequest     = true
{"EDIT":{...}}  -> isApplyEditRequest  = true
{"undo":null,"undo":true} -> isUndoRequest = true   (last-wins, never rejected)
```

**Severity is bounded and stated honestly:** the socket is a local UNIX socket
with owner-only permissions and `SO_PEERCRED` peer auth, so a caller who can send
`{"UNDO":true}` can already send `{"undo":true}`. This is **not** privilege
escalation. I checked `protocol.PromptRequest` for a field that could collide
accidentally (`protocol_version`, `prompt`, `workspace`, `history`, `reset`,
`prompt_kind`) — none case-folds onto a sniffed key, so accidental misrouting by
the shipped clients is not reachable either.

It remains P2 rather than P3 because the misrouted destination is **destructive**
(`undo` restores files from backup over the user's current work), the wire
contract silently accepts ambiguous bodies, and the project has already decided
this pattern is wrong — leaving it fixed in one component and unfixed in the
other is the kind of inconsistency that regresses.

**Recommendation.** Port `topLevelFields`' exact-match discipline to the three
sniffers, and reject duplicate top-level keys, so both components share one rule.

> **RESOLVED — `fd8d865`.** All FOUR sniffers ported (this finding lists three;
> `isStatusRequest` in `status.go` had the same defect), plus duplicate-key
> rejection. A second copy of the helper, not a shared import, with the reason
> stated in both files: `proxy/Dockerfile` copies only `*.go` and `go.mod`.
> Semantics preserved exactly, which mattered more than expected: the old
> sniffers tested a `*bool` for non-nil, so `{"undo":null}` fell through to the
> prompt path. Unmarshalling JSON null into a bool is a documented no-op
> returning a NIL error, so a naive port would have started routing a null undo
> INTO the destructive handler. Caught by the test, not by inspection.
> The "no PromptRequest field case-folds onto a sniffed key" check this finding
> did by hand is now an assertion.

### P2-3 — Zero accessibility affordances in the VS Code webview

**Status: CONFIRMED** · `clients/vscode/media/main.js`, `src/chatPanel.ts`

A grep for `aria-*`, `role=`, and `tabindex` across the entire webview — 647
lines of `main.js` and 678 of `chatPanel.ts` — returns **nothing**.

Concretely missing: no `role="log"` / `aria-live` region, so streamed answer
tokens are never announced; no accessible label on the Auto-apply toggle (its
state lives only in `textContent`); no roles or labels on the diff-review
surface, which is where the user authorizes writes to their files; no managed
focus order for the panel.

**Impact.** A screen-reader user cannot follow a streaming answer and cannot
safely operate the edit-approval flow — Gate ④ of the safety pipeline is
"you see the change as a diff and approve it explicitly," which presumes sight.

**Recommendation.** `role="log"` + `aria-live="polite"` + `aria-atomic="false"`
on the transcript container; `role="switch"` + `aria-checked` on the toggle;
`aria-label` on the diff container naming file and change counts; verify with an
actual screen reader (see *Blocked, not skipped*).

> **RESOLVED (structurally) — `74adfea`.** All recommended affordances added,
> plus `role="status"` live regions on the redaction/degradation/grounding
> strips, `sr-only` labels for both inputs (a placeholder is not an accessible
> name), managed focus, and per-button labels naming the FILE on the
> approve/reject pair ("Apply" alone is ambiguous with several proposals in
> scrollback).
> `getHtml`'s body markup was extracted to an exported `chatPanelBodyMarkup()`
> purely so the structure is assertable — VS Code exposes no webview DOM to the
> test host. EDH tests 6 → 14.
> The XSS posture is re-asserted rather than trusted: a test checks for
> dangerous sinks after STRIPPING COMMENTS, because `main.js` promises "never
> innerHTML" five times in prose and the first version of that check read those
> promises as violations.
> **Still blocked, unchanged:** real NVDA/VoiceOver/Orca passes. These
> assertions prove the structure is present, not that the experience is good.

### P2-4 — `0003`'s corrective revoke missed `increment_usage`, the fourth function

**Status: CONFIRMED (static)** · `proxy/migrations/0003_revoke_public_grants.sql`

`0003` exists specifically to close the EXECUTE-to-PUBLIC surface that `0002`'s
`drop function`/recreate reverted. It revokes `reserve_usage`,
`apply_correction`, and `sweep_pending_corrections`. The string
`increment_usage` appears **0 times** in the file, yet `0001` does
`create or replace` on it and `0002` explicitly leaves it live ("deliberately
left in place, unchanged").

`increment_usage` is the **most dangerous of the four** if reachable: it is an
unconditional `update usage set tokens_used = tokens_used + p_tokens` with no
`token_limit` check at all. Containment is identical to the others (SECURITY
INVOKER, so an `anon` caller runs as `anon` and hits `usage`'s SELECT-only
grants), so this is a **defense-in-depth gap, not a live write hole** — the same
severity `0003` itself assigns to gaps #2/#3. But an audit written to close a
class that misses one member of that class is exactly trap 3's pattern: the
catalog reads clean while an established control is missing.

**Recommendation.** Add `increment_usage(uuid,integer)` to `0003`'s revoke list
(or a `0004`), and add it to the verification query, which also omits it. If
`increment_usage` is genuinely dead code now, drop it instead.

> **PARTIALLY RESOLVED — `50ff5b3`. Founder action still required.**
> `0004_revoke_increment_usage.sql` written with the revoke and a verification
> query covering all FOUR functions, superseding `0003`'s three-row version. It
> also adds a `prosecdef` check: if any of the four is SECURITY DEFINER, the
> containment argument this finding and that file both rest on is wrong and it
> is a live hole — the query says to escalate rather than proceed.
> Not dropped, though the proxy no longer calls it, for the reason `0002` gives:
> dropping a live function is riskier than revoking a grant, and nothing outside
> this repo's view is known not to call it.
> **Applying it and running the verification remain founder-gated.**

### P2-5 — `protocol` (625 lines) and `helper` have zero test coverage; `chunkscrub.go` has zero

**Status: CONFIRMED** · coverage profiles in `scratchpad/logs/`

- `protocol`: **0.0%**, 0 tests. This is the wire contract shared by the daemon,
  TUI, VS Code extension, and CLI — every struct, tag, and sentinel constant that
  P2-2's dispatcher and both clients depend on.
- `helper`: **0.0%**, 0 tests, both packages — the ONNX embedder subprocess.
- `daemon/chunkscrub.go`: **all 8 functions at 0.0%** —
  `detectWarnModeSecrets`, `detectHighEntropy`, `shannonEntropy`,
  `detectKeywordSecrets`, `isNonSecretValue`, `valueIndicator`,
  `classifyTokenShape`. This is the warn-mode secret detector: the component
  whose job is to tell you your index is about to leak credentials. Its
  false-negative rate is therefore **unmeasured**.

Note the send-time scrubber `daemon/scrub.go` *is* at 100% — the gap is the
chunk-level warn-mode detector, not the load-bearing prompt scrubber.

**Recommendation.** Table-driven tests for `protocol` round-tripping every
request/response shape (this would have caught P1-1's missing field as a gap);
a fixture corpus for `chunkscrub` with a **reported false-negative number**, since
the privacy claim is quantitative in nature and currently unquantified.

> **RESOLVED — `ae6bbfe`.** `protocol` 0.0% → **88.9%**, `helper` 0.0% →
> 6.5% / **75.0%** (helperproto), `chunkscrub.go` all 8 functions 0.0% → **100%**.
> The `chunkscrub` corpus reports numbers as recommended:
> **false negatives 0/12 = 0.000**, false positives on ordinary code
> **3/16 = 0.188**, known-benign high entropy **2/6 = 0.333** (reported, NOT
> gated — over-reporting there is the stated design intent).
> The 0.188 is a finding in itself: all three are ordinary code on the entropy
> tier — a 42-char camelCase identifier at 4.10 bits/char, a 25-char snake_case
> constant at exactly 4.00, and a 6-char value at `isNonSecretValue`'s floor. The
> threshold is set just above the measurement rather than at an aspirational
> 0.05, so it gates drift instead of failing on day one.
> Worth recording: pure hex never fires — 16 symbols cap Shannon entropy at
> exactly 4.0 and the threshold is 4.0 — so git SHAs, sha256 digests and UUIDs,
> the dominant expected false-positive class, are excluded by arithmetic rather
> than a special case. A test pins that the threshold may not drop below 4.0.
> `protocol`'s tests also pin what this finding predicted they would: that the
> four discriminator keys are not `omitempty` (which would make a false value
> vanish and the request be treated as a prompt).

---

## Low Priority Issues (P3)

### P3-1 — The documented test command does not work
**CONFIRMED.** [README.md:849](../README.md#L849) documents `go test ./...`. From
the repo root:
```
pattern ./...: directory prefix . does not contain modules listed in go.work
FAIL	./... [setup failed]     (rc=1)
```
There is no root module, only `go.work`. It fails loudly rather than silently
passing, so no false confidence — but the "Building and testing" section is the
first thing a contributor runs and it is broken. Fix: document the six explicit
module paths, or add a `Makefile`/`go.work`-aware script so the correct
invocation is the easy one (and reuse it in CI per P1-2).

> **RESOLVED (docs commit).** `README.md` now documents the explicit six-module
> loop, and it is the SAME loop CI runs, so the documented command and the gating
> command cannot drift. Verified by running it.

### P3-2 — "Five gates" overstates protection for non-Go repositories
**CONFIRMED.** Gate ③ parses only `.go` (`editapply/apply.go:33`); everything
else returns `"no syntax check applied (unsupported for %s)"`. This is honest —
it is reported, not silent, and `PRODUCT_OVERVIEW.md:295` does say "For Go
files." But the headline "five gates, in order — a change only reaches your disk
if it passes all of them" reads as five active gates, when a Python or TypeScript
user gets four. Suggest a one-clause qualifier at the table header.

### P3-3 — TUI keyboard surface is thin
`ctrl+c`, `esc`, `enter`, `ctrl+n`, `pgup`, `pgdown`, plus `y`/`n`/`q` in review
mode (`clients/tui/chat.go`). Adequate for a single-input TUI; no home/end,
word-wise editing, or scrollback search. Cosmetic, listed for completeness.

---

## Security Findings

| Severity | Location | Description | Risk | Fix |
|---|---|---|---|---|
| **High** | `proxy/main.go:1061` | Byte-guard kill refunds the reservation (P0-1) | Repeatable free paid inference; worst for near-exhausted keys | Floor the charge at `reserved` on any kill path |
| **Medium** | `proxy/main.go:1275` | Duplicate-key ambiguity between gate and forwarded bytes (P2-1) | Cost-authorization bypass **if** upstream is first-wins (untested) | Reject duplicate top-level keys |
| **Low** | `daemon/server.go:473,583,691` | Case-fold + duplicate-key dispatch into destructive `undo` (P2-2) | Not privilege escalation — same-UID socket; wire-contract robustness | Port `topLevelFields` exact-match discipline |
| **Low** | `proxy/migrations/0003` | `increment_usage` omitted from corrective EXECUTE revoke (P2-4) | Defense-in-depth regression; contained by SECURITY INVOKER | Add to revoke list + verify query |

### Verified sound (hypotheses I pre-registered and the code refuted)

Reporting these because a QA pass that only lists failures misrepresents the
system:

- **Missing-env fail-open — REFUTED.** `main.go:288-290` warns loudly and every
  request then fails auth with 401. `authorize` fails closed on unconfigured
  Supabase, lookup error, non-200, or a row count other than one. The proxy never
  serves unmetered inference.
- **Webview XSS — REFUTED (sound).** Zero `innerHTML` / `outerHTML` /
  `insertAdjacentHTML` / `document.write` / `eval` / `new Function` in
  `media/` or `src/`. Every renderer uses `textContent`. CSP is
  `default-src 'none'` with a per-load script nonce (`chatPanel.ts:450`) and
  `localResourceRoots` narrowed to `media/`.
- **Webview state loss — REFUTED.** `retainContextWhenHidden: true` is set
  (`chatPanel.ts:92`).
- **Container hardening — CONFIRMED SOUND.** `proxy/Dockerfile:17-18` adds a
  non-root user and `USER 10001:10001`.
- **Secrets hygiene — CONFIRMED SOUND.** Full `git log --all -p` scan for
  `sk-or-v1-`, `sk-`, `mochi_`, `eyJ`, `AKIA`, `ghp_` prefixes returns only the
  obvious fixture `AKIA1234567890ABCDEF`. No real key material has ever been
  committed; `.env` is untracked.
- **`apply_correction`'s unreferenced data-modifying CTE — REFUTED.** I suspected
  the `bumped` CTE might not execute since the outer query never references it.
  PostgreSQL guarantees data-modifying `WITH` statements run exactly once to
  completion regardless of whether their output is read. The comment is correct.
- **Supply chain — SOUND.** `govulncheck` reports 0 reachable vulnerabilities in
  all six modules (1 unreachable advisory in a required module).

---

## Performance Findings

| Issue | Impact | Optimization |
|---|---|---|
| Rate limiters are per-instance and in-memory (`ratelimit.go`; README concedes it) | Across N replicas the effective ceiling is N× configured. Unlike quota — atomic in Postgres — the new **token-volume** limiter inherits this, so it is not a spend bound under horizontal scale, only a per-instance one. `PLAUSIBLE`: not reproduced, single-instance testing only | Move the token bucket to the same atomic Postgres path quota uses, or accept and document a per-replica ceiling of `configured/N` |
| `daemon` suite takes 21.5 s; eval path 148 s | Slow enough to discourage the local runs that are currently the *only* gate | Fix P1-2 so speed stops being a correctness dependency; keep eval on a schedule |
| Reconciliation sweep runs on every replica every 5 min | N replicas × sweep. Harmless — `DELETE … RETURNING` is atomic so a row is claimed once (`0002:117-121`) — but it is N× the query load | Acceptable as-is; noted only for scale planning |
| `stripSSEAccountMetadata` re-marshals only on actual deletion | Deliberately efficient; the common chunk is forwarded byte-for-byte | No change — called out as a good decision |

---

## UX Findings

| Problem | Impact | Suggested improvement |
|---|---|---|
| Budget-killed answers render as complete successes (P1-1) | The user's most consequential failure mode is invisible. Silently truncated answers with no signal | Surface via the existing `onIncomplete` path both clients already render |
| No screen-reader support anywhere in the webview (P2-3) | Streaming answers unannounced; the diff-approval gate is unusable non-visually | ARIA live region, `role="switch"`, labelled diff container |
| Non-Go repos silently get four gates, not five (P3-2) | Expectation mismatch on the product's headline safety claim | One qualifying clause at the gate table |
| Index goes stale silently after file changes (known pilot sharp edge, carried forward from prior sessions) | Answers cite stale code with no indication | Surface index age/staleness in the status line |

---

## Code Quality Findings

| Issue | Why it matters | Recommended fix |
|---|---|---|
| A defect class was fixed in one component and left in its twin (P2-2) | `491e0f2` correctly identified Go's case-insensitive tag matching and rebuilt the proxy gates. The daemon has the same pattern. Class-based fixes must sweep the codebase, or the class regresses | Grep for the pattern; fix both; add a shared helper so there is one implementation |
| `proxy/main.go` is 1791 lines holding auth, admission, gates, streaming, budget, and billing reconciliation | The P0 lives at the seam between `streamSSE`'s kill and `finalizeUsage`'s three-way branch — two concerns 500 lines apart that must agree on an invariant no type enforces | Extract the charge decision into one function with the invariant expressed in its signature (e.g. `chargeFor(outcome streamOutcome, reserved, measured int) int`) so `killed` cannot silently mean "refund" |
| Coverage is high but bound-specific paths are untested | 79.7% coverage and 52 tests did not catch P0-1, because the budget tests exercise `token_ceiling` and not `byte_guard`. The branch message itself notes a toy fixture previously hid a 73× defect — same lesson, recurring | Require every *branch* of a safety control to have a fail-when-neutered test, not every function |
| No integration test spans any two components | P1-1 is invisible to both suites individually and obvious the moment they meet | One test per seam: proxy↔daemon, daemon↔editapply, daemon↔clients |
| Documentation carries load-bearing claims and drifts | Recent history is full of "correct N stale claims" commits; P3-1 and P3-2 are live instances | Assert doc claims in tests where feasible; treat `PRODUCT_OVERVIEW.md` §7 claims as a checklist in CI |

---

## Missing Test Cases

**Positive**
- `protocol`: round-trip every request/response struct; assert every wire tag and
  sentinel constant (`IncompleteLength`, `Degraded*`).
- `helper`: embedder subprocess handshake, dimension agreement, clean shutdown.
- Proxy↔daemon integration: a real forwarded completion end-to-end.

**Negative**
- Budget kill via **`byte_guard`** asserting a non-negative delta (**this is P0-1**).
- Budget kill via `token_ceiling` asserting a top-up (present today).
- Duplicate top-level keys on all four proxy gates → expect 400/403.
- Case-variant keys on all three daemon sniffers → expect fall-through to prompt.
- `increment_usage` EXECUTE revoke present (founder SQL — see below).

**Edge**
- Chunks at exactly `ceiling*512` bytes and exactly `ceiling` count (off-by-one
  on both bounds).
- CJK/emoji-heavy deltas where bytes-per-token collapses; multi-token chunks.
- Reservation succeeds in Postgres but the response is lost to client
  cancellation (stranded row → sweep). Design covers it; untested here.
- `usage.key_id` non-unique → `reserve_usage` cross-join multiplication.
- Socket: 16 MiB + 1 body, 129th connection, slowloris, invalid UTF-8, NUL bytes.

**Regression**
- Fail-when-neutered twins for both P0-1 and P1-1 fixes.
- C3's abort-refund test extended to cover every kill path, not just disconnect.

**Security**
- Duplicate-key bypass against a first-wins parser (needs an upstream that is one).
- `chunkscrub` false-negative corpus: split-across-boundary secrets, PEM blocks,
  base64 blobs, provider-prefixed tokens, Unicode-escaped values — reported as a
  **number**.

**Performance**
- Multi-replica rate-limit ceiling (N× dilution) under concurrent load.
- 100k-file repo indexing time and memory.

---

## Risk Assessment

**Technical.** One confirmed money-path defect (P0-1) and one confirmed
user-facing correctness defect (P1-1), both in code added by the unmerged branch,
both invisible to a green suite. The absence of any integration test means the
seam class that produced P1-1 is systematically undetected.

**Business.** P0-1 is repeatable free paid inference on the managed tier —
direct, unbounded margin loss, easiest to trigger on exactly the keys closest to
their limit. P1-1 silently degrades answer quality, the product's core claim,
with no signal to the user or operator.

**Scalability.** Rate and token limiters dilute linearly with replica count while
quota does not; the spend bound is per-instance. No load testing has been done at
any scale.

**Operational.** No CI gate (P1-2). Migrations applied by hand with no version
tracking, no rollback, and a documented ordering constraint enforced only by a
comment (P1-4) — a class that has already produced one incident. Observability is
log-lines only; no metrics, no alerting on the `CORRECTION LOST` / `ABANDONED
RESERVATION` lines that the design relies on being noticed.

**Security.** The posture is genuinely strong — fail-closed throughout, no
network listener, non-root container, clean supply chain, no committed secrets,
sound webview CSP. Residual risk is concentrated in parser-agreement assumptions
(P2-1) and one incomplete defense-in-depth sweep (P2-4).

**Maintenance.** A 1791-line handler where a safety invariant spans two distant
functions with no type enforcing it (P0-1's root cause). A defect class fixed in
one place and not its twin (P2-2). Zero coverage on the shared wire contract every
component depends on (P2-5).

---

## Blocked, not skipped

Out of scope by agreement (local-only depth). Each needs the named evidence:

1. **Live Supabase grants** — P2-4 and `usage.key_id` uniqueness. Founder SQL:
   ```sql
   select has_function_privilege('public','increment_usage(uuid,integer)','EXECUTE');
   select conname, contype from pg_constraint
     where conrelid = 'usage'::regclass;
   ```
2. **Production wire probes** — Railway `/health`, authed completion, the 403
   gates. Needs a real key against prod; previously verified 2026-07-27, not
   re-verified here and not re-verified against **this branch**.
3. **Multi-replica behaviour** — the N× rate-limit dilution. Needs ≥2 instances
   behind one load balancer.
4. **Real screen-reader testing** — P2-3 lists structural absences found by
   inspection; NVDA/VoiceOver passes are still required to characterize the
   actual experience.
5. **Intel Mac / linux-arm64** — the documented platform gaps; no such hardware here.
6. **Upstream duplicate-key resolution** — decides whether P2-1 is exploitable
   end-to-end. Needs one probe against OpenRouter with a deliberately duplicated
   `model`.
7. **Load/soak** — no testing at any scale was performed.

---

## Final Verdict

## ❌ FAIL

**Justification.** The gate is one unresolved P0 and four unresolved P1s, so PASS
is arithmetically unavailable under the rule set for this review. More
importantly, it is unavailable on the merits:

**P0-1 is a reproduced money defect in the code this branch exists to add.** The
branch's stated purpose is to *bound per-request spend*; on one of its two budget
bounds it instead **refunds 4093 of 4096 reserved tokens after streaming the
maximum permitted output**, and charges the rate limiter the same near-zero
figure. It is the C3 bug class this repo already fixed once, reintroduced through
a path C3's regression test does not cover, and it is easiest to exploit on the
keys with the least quota remaining.

**P1-1 means the branch's user-visible safety net does not exist.** The
`budget_exceeded` chunk was added so users could distinguish throttling from
failure. The shipped daemon has no field to receive it, so a killed answer is
reported as a **complete success**. Both suites pass; no test spans them.

**P1-2 is why both survived.** CI runs one `docker build`. 584 tests gate
nothing. Every green figure in this report exists only because I ran it by hand,
and P1-3 shows what that costs: the project's own retrieval-quality gate is red
right now, and normal development cannot see it.

**What this verdict is not.** It is not a judgment that the codebase is weak. The
safety architecture held up under direct attack: four of my ten pre-registered
hypotheses were refuted by correct code, `editapply`'s gates are at 87% with the
confinement paths covered, the fail-closed discipline is consistent, and the
security posture (CSP, non-root, no committed secrets, clean supply chain) is
better than most projects at this stage. The failures are concentrated and
specific: **one seam between two functions, one seam between two components, and
no automation defending either.**

**Path to PASS.** Fix P0-1 (a one-line floor plus a `byte_guard` fail-when-
neutered test). Fix P1-1 (one struct field plus one integration test). Land
P1-2's CI pipeline so the suite becomes a gate. Decide P1-3 explicitly — fix or
re-baseline with written justification. Capture the baseline schema for P1-4.
That is a small, well-defined body of work, and none of it is architectural.

---

*Reviewed against `harden/proxy-spend-and-gates` @ `17ffad6`. Repro bundle:
`/tmp/claude-1000/.../scratchpad/{repro,logs}/`. All findings labelled CONFIRMED
were reproduced by a script that was run; nothing in this report is inferred and
presented as measured.*
