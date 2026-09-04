# Terminal client — production readiness

<!-- coderefs: enforced -->

**Date:** 2026-09-04. **Branch:** `audit/adversarial-pass`. **Scope:** `clients/tui`.
**Verdict:** ship-ready on the axes measured below, with four named conditions
and **one performance budget knowingly unmet** — a repaint at the transcript
ceiling costs 22.4 ms against an 8 ms budget and against D-1's 16 ms hard
per-Update ceiling. Measured, not extrapolated; recorded as R1.12; not fixed.

This is written to be read before a release decision. **What was not verified is
stated as prominently as what was**, because the second list is the one that
decides whether the first is worth anything.

---

## What changed, and what it was worth

| Property | Before | After | How it is held |
|---|---|---|---|
| Allocations per streamed token, 400 prior turns | 2,234 | 29 | `TestPerTokenAllocationsAreFlatInTranscriptLength` — a ratio, not a constant |
| Growth of that number with conversation length | 97× | 1.6× | same |
| 1 MB paste, worst input measured | 15.6 ms median | **389 µs**, the same at 0 bytes and at the 2 MiB ceiling | `TestNoMoreRunesReachTheInputThanCanBeKept` (deterministic); `TestOneMegabytePasteIntoACeilingTranscript` |
| Oversized paste | silently truncated at 4,000 chars | reported | `TestAnOversizedPasteSaysWhatItDropped` |
| Transcript memory | unbounded | 500 turns / 2 MiB, with a visible marker | 2,000-turn soak |
| Scrolling back during a stream | impossible — snapped to bottom every token | works | three separate tests |
| Mouse text selection | silently disabled, undiscoverable | `/mouse`, in the idle hint | `TestTheMouseToggleIsDiscoverable` |
| Unchecked errors | 5 | 0 | ceiling lowered, and the gate no longer fails open |
| Absolute build paths in a binary | 678 | 0 | `scripts/supply-chain.sh`, `-trimpath` on releases |
| Rendering determinism | 3 different byte strings for the same state | 1 | 14-environment × 4-profile subprocess matrix |

## What a repaint costs at the bound, and it is over budget

Taken at P3.1, and it is the one measurement in this document that changed a
verdict rather than confirming one.

Every other performance figure here was taken at 240 prior turns of 1.2 KB —
about 300 KB, one seventh of what the transcript bound actually permits.
Multiplying by seven would have been arithmetic. Measured instead, at a
transcript sitting at **both** ceilings at once (490 turns, 2,096,839 bytes,
after 512 evictions), one repaint costs:

| | value | against the 8 ms repaint budget | against D-1's 16 ms hard ceiling |
|---|---|---|---|
| p50 | **22.4 ms** | 2.8× — no headroom, −14.4 ms | **1.4× — breached** |
| p99 | **29.9 ms** | 3.7× — −21.9 ms | 1.9× — breached |
| min / max | 21.9 ms / 42.3 ms | spread 1.9× | |

**The spread is reported because the median alone would mislead.** A 1.9× spread
that sits entirely above both lines is a different result from one that straddles
them — the paste measurement straddled 16 ms at 2.6× and was correctly recorded
as "no reliable headroom" rather than as a breach. This one does not straddle.
Every one of the 200 samples was over the 8 ms budget, and the minimum was over
D-1's 16 ms.

Where it goes, medians, cache warm: **`wrapToWidth` 17.6 ms (79 %)**,
`viewport.SetContent` 4.5 ms (20 %), `transcriptCache.render` 334 µs (1.5 %).
Neither of the first two is ours and neither takes an incremental update. A cold
repaint — cache empty, as after a resize — is 31–47 ms.

**What this does and does not mean.** It is one repaint, not a queue: coalescing
means the next cannot begin until 16 ms after this one ends, so nothing
accumulates and the loop stays responsive. The cost is input latency at the
bound — a keystroke arriving mid-repaint waits up to 22 ms rather than the 3 ms
it waits at 240 turns — plus CPU and battery in a session that has run that long.
It is not correctness and it is not a stall.

**It was not fixed, deliberately.** The instruction at P3.2 was to report before
optimizing, and the two available fixes — per-block wrap caching, or replacing
the viewport — are each larger than this pass. Two cheap trades exist and neither
was taken on my own judgment: `refreshInterval` 16 ms → 33 ms halves the
sustained cost, and lowering the 2 MiB byte ceiling moves this figure
proportionally. Both are in R1.12's trigger.

**The deterministic half**, per the standing rule: **39 allocations** for a token
plus its repaint at the bound, against **2,734** with the render cache neutered.
That number is machine-independent and is what actually gates this path.

## Provenance of the numbers above

Added after an audit of this document's own figures, because one of them turned
out not to be reproducible.

**Deterministic — these reproduce exactly on any machine.** Allocation counts
(2,234 → 29; 30,452 vs 24,314), the growth ratios (97× → 1.6×; 1.25×), embedded
build paths (678 → 0), unchecked errors (5 → 0), distinct render outputs (3 → 1),
turns and bytes retained by the soak, file-descriptor counts, coverage
percentages, and every code constant (4,000 chars; 500 turns / 2 MiB).

**Wall-clock — these vary run to run and are reported, not gated.** Per S1 every
timing gate in the tree asserts allocations and reports wall-clock at a wide
multiple. Observed spreads on the reference machine: the 1 MB paste median moved
between 15.1 ms and 17.1 ms across runs *before* the fix, which is why its gate
is a rune count and not a stopwatch; the resize-storm and repaint figures carry
the same caveat. The repaint-at-the-ceiling figures (22.4 ms p50 / 29.9 ms p99)
are wall-clock and have a measured 1.9× spread — reported with min and max
alongside, because a median on its own cannot show whether a result straddles a
budget or clears it entirely. This one clears it entirely, in the wrong
direction: every sample was over. The paste-at-depth figures (389 µs) have a
1.1× spread.

**Not reproducible as stated, and corrected.** An earlier draft of this document
claimed "1.2 million fuzz executions". Fuzz execution counts are time-boxed and
scale with machine load: two runs of the same gate at `FUZZTIME=10s` produced
899,373 and 1,827,117 executions across the three TUI targets — a 2× spread. The
claim has been replaced with what is actually stable, which is that the targets
are registered in the gate and clean.

**Memory:** see item 4 under *What was NOT verified*.

## The gates themselves were audited, and three were fail-open

Added 2026-09-04. Every "gates green" line in every report about this work was
reported *through* these scripts, so their trustworthiness is a precondition for
everything above, not a tidy-up. All twelve were probed against four questions:
what they report when the target set is empty, when the module does not build,
when a tool they shell out to is missing, and whether they distinguish
"inspected N, found 0" from "inspected 0".

**Nine were already sound.** `coverage-ratchet` refuses to pass with no floors
parsed and detects a floor whose package vanished; `govulncheck` asserts it
scanned all six modules; `docs-links`, `docs-claims`, `actions-pinned` and
`go-toolchain-pinned` each carry an explicit count floor; `lint`, `go vet` and
`errcheck-ceiling` fail closed on every probe.

**Three were not, and are now fixed and neutered:**

| Gate | What it concealed |
|---|---|
| `scripts/fuzz.sh` | A fuzz target that does not exist reported `ok` — `go test -fuzz` with no match exits 0. This is the hole that let **both TUI sanitizer fuzzers go unrun from task 2.1 until they were noticed**, and it was still live. It also reported the daemon's four targets as plain `ok` when they only replay their seed corpus and generate nothing at short `FUZZTIME`. |
| `scripts/supply-chain.sh` | Written earlier in this pass. Treated "this is a library" and "`go list` failed" as the same answer, so **a module with no Go files produced a silent skip and the gate exited 0**. Concealed nothing yet — it is new — but would have concealed a module that stopped producing a binary. |
| debt markers | **The gate did not exist.** "Zero TODO/FIXME/HACK in non-test code" was a standing baseline invariant enforced by a manual grep over a hardcoded `clients/tui/*.go` — one module of six. Re-run properly across all six: **156 non-test files, 0 markers**, so the claim was true, but it had never been checked outside the TUI. |

**Consequence for the claims in this document.** The fuzz-gate line is
re-verified under the fixed script (18 of 18 targets ran; the three TUI targets
produce real execution counts). The debt-marker line is re-verified repo-wide
for the first time. No claim above was found to be false; two were found to have
been resting on less evidence than they appeared to.

## What is verified, and by what

- **Correctness of the render cache.** `cache.render == renderTranscript`
  byte-for-byte over 200 seeded mutation sequences, four colour profiles, widths
  0–200, and the three TUI fuzz targets clean under the gate. Twelve mutation
  shapes enumerated and each asserted individually.
- **The gates are load-bearing.** Every fix in this batch was neutered and the
  guarding test observed to fail. Four separate instrument errors were caught
  this way and are listed below.
- **No leaks.** Goroutines flat across completed, interrupted and reset turns;
  file descriptors flat across 25 turns; 2,000-turn soak at 3.1 MB heap.
- **The worst case the bound permits.** Repaint and paste both measured at a
  transcript at both ceilings, not extrapolated from a seventh of it. The helper
  that builds it **fails the test if it did not reach the ceiling**, so the
  measurement cannot quietly run at half scale.
- **Repo-wide green.** vet (linux + windows), staticcheck/ineffassign/bodyclose,
  `-race` (376 s), coverage ratchet on all nine modules with the TUI floor
  raised 83.0 → 84.0, errcheck 0, govulncheck 0 reachable, fuzz gate, docs
  links and registers, supply chain.

## Per-platform status

Added at P4.3. Every number here is deterministic — it comes from `go list`
under each `GOOS`, not from a stopwatch — and it is reproducible with
`GOOS=<os> go list -f '{{.TestGoFiles}}' ./clients/tui`.

| | Linux | macOS | Windows |
|---|---|---|---|
| Test files compiled | **57** | 52 | 51 |
| `Test` functions compiled | **326** | **312** | **306** |
| Fuzz targets | 3 | 3 | 3 |
| Test binary links (`go test -c`) | yes | **yes** | **yes** |
| `go vet ./...` | yes | **yes** | **yes** |
| Suite actually executed | yes, incl. `-race` | CI `cross` job, main/dispatch only | CI `cross` job, every push |

**What macOS does not run — 14 tests, all of them about the terminal itself:**

| File | Tests | What is therefore unverified there |
|---|---|---|
| `ptysmoke_test.go` | 2 | the real binary in a real terminal on a real socket |
| `exitsignals_pty_test.go` | 6 | terminal restored on every exit path; the SIGHUP gap (R1.1) |
| `renderprofile_pty_test.go` | 4 | the 14-environment × 4-profile determinism matrix |
| `sanitize_pty_test.go` | 1 | escape filtering measured at an actual terminal |
| `brokenpipe_pty_test.go` | 1 | an early reader closing the pipe underneath |

**Windows does not run those 14 either, plus `exitsignals_test.go` (6 more) —
20 fewer in total.** That file is `//go:build !windows` because **Windows has no
SIGHUP and no SIGTERM in the POSIX sense**, so the signal contract it asserts
does not exist there. Windows gains `testaddr_windows_test.go` in exchange.

**Why they are Linux-only, and why that is defensible.** Allocating a pty is
per-kernel — Linux uses `TIOCSPTLCK`/`TIOCGPTN`, the BSDs `TIOCPTYUNLK`/
`TIOCPTYGNAME`. A portable second implementation is one nobody runs. One platform
that actually executes beats two that are skipped.

**What is NOT defensible, and was fixed here.** A build tag is a silent skip. On
macOS `go test ./...` printed `ok` while five files' worth of terminal behaviour
was never compiled, and nothing anywhere said so — the same fail-open shape the
gate audit removed from this repo's scripts, wearing a different hat.
`TestPlatformCoverageIsStated` now runs on all three platforms and does opposite
jobs: on Linux it is a **gate** (every listed suite must carry the tag, and no
tagged suite may go unlisted, so a sixth cannot appear without landing in this
table), and elsewhere it is a **report** that names, in that platform's own test
output, exactly what did not run there. It asserts on the count of files
inspected — 73 — so a version that reads nothing fails rather than passes.

**The honest one-line summary: the terminal is verified on Linux and on no other
platform.** Everything that is not the terminal — the render cache, the
sanitizer's logic, the transcript bound, the paste bound, scroll semantics, the
slash catalogue, history construction — compiles and runs on all three.

## What was NOT verified — read this part

1. **No human has used any of this.** Every measurement here is from a test
   harness or a pty driven by a test. Nobody has typed into the client since
   these changes landed. The scroll fix, the eviction marker, the paste notice
   and `/mouse` are all judged by assertions about what the model contains, not
   by anyone looking at a screen.
2. **The terminal is verified on Linux only** — see *Per-platform status* above
   for the exact figures. macOS and Windows compile, link and run 312 and 306 of
   the 326 tests respectively; the 14 (macOS) and 20 (Windows) they do not run
   are the pty-backed suites and, on Windows, the signal contract that platform
   does not have. **This is now stated by a test rather than by this paragraph**,
   which is the part that changed at P4.3.
3. **Not run in CI.** Everything above was measured on one developer machine.
   The CI workflow changes in this batch — the supply-chain gate, the three
   newly registered fuzz targets — have never executed on a runner.
4. **The memory ceiling is anchored to my own measurement, and supersedes an
   earlier figure that could not be reproduced.** The numbers of record are
   **8.6 MB idle and 13.7 MB at 120 turns**, measured on this tree and identical
   before and after the render cache. An earlier baseline given to me could not
   be reproduced across two attempts, including varying the answer size from
   1.2 KB to 8 KB per turn; the superseded figure is deliberately not repeated
   here so it cannot be picked up again by a reader who finds it in older text.
   See R1.11 in the residual-risk register for the same note.
5. **Two dependency surfaces are scanned by nothing** — the onnxruntime native
   library and the extension's npm tree (R1.13).
6. ~~**The terminal client is not built by the release workflow** (R1.14)~~
   **Closed 2026-09-04 at P4.1.** It now builds on all three release runners with
   `-trimpath`, is in the macOS signing list, and ships as a standalone download
   with checksums. It is asserted *out* of the `.vsix` by the packaging gate. What
   remains unverified is that the release workflow **has never been run** with
   these steps in it — it is tag- and dispatch-triggered, so nothing on an
   ordinary push exercises it.
7. **No performance measurement under memory pressure, on a slow disk, or on a
   shared CI runner.** Every wall-clock figure is from an idle laptop.
8. **Security posture was not re-audited.** This batch was performance,
   correctness and operability. The security findings below are carried
   forward from earlier passes, not re-verified here.

## Security findings carried forward

| Issue | Exploit scenario | Fix | Verifying test |
|---|---|---|---|
| **`/mcp-server` shows daemon + MCP stderr unredacted** (R1.5) | A third-party MCP server prints its API key at startup; `mcp list` runs it with `CombinedOutput()` and the key lands on the user's screen and in their scrollback. Only path in the client that puts daemon stderr in front of a user. | **None. Unfixed by decision** — enumerated in 2.3a, no go-ahead for the structural redactor. | **None.** Stated as a coverage gap. |
| **Model-emitted secrets in the transcript** (R1.6) | A model echoes a credential it read from a file; it is rendered and persisted like any other answer. | **None. Unfixed by decision**, same reason. Memo recommends accepting it: the daemon matches on shapes, so the asymmetry is permanent. | **None.** |
| **The outbound scrub is bypassed by one turn** (found 2026-09-04 at P5.1, in `daemon/`, not previously recorded anywhere) | The daemon redacts `sk-…` from the prompt and tells the user so. `persistTurn` then writes the RAW prompt to `memory.db` and its `turns_fts` index, and `prepareHistory` sends it to the provider verbatim as history on the next turn. The redaction notice makes it *worse* than never scrubbing: the user reasonably concludes the key did not leave. | **None yet — not mine to make.** The fix is `cleanPrompt` at one call site plus a scrub in `prepareHistory`, and needs no new detector. Recommended in the memo. | **None.** Verified by execution, twice, with the probes removed afterwards. |
| **Edit-review and approval buffers hold raw bytes** (R1.4) | Deliberate: edit bytes go to disk and approval bytes carry the daemon's digest, so both must stay byte-exact. Sanitized at render instead. Risk is a *new* reader rendering them raw. | Structural — an AST guard names every function allowed to touch them. | `TestRawByteStructuresHaveNoNewReaders`. **It fired during 3.7**: the decomposition moved the reader out of `Update` and the test failed until the reviewed list moved with it. |
| **`git status` failures echo git's output** (R1.7) | A remote URL with an embedded credential appears in an error line. | None. | None. |
| **Terminal escape injection** (closed earlier in this pass) | Model or MCP output repaints the approval prompt — forged consent. | Allowlist sanitizer, one ingest door. | 43-entry corpus + 14 CSI shapes + 2 fuzzers + a real-pty test, all now registered in the fuzz gate for the first time. |
| **C1 introducer in a model-authored edit path** (closed in this pass, task 2.2) | `editapply.RejectUnprintablePath` refuses `r < 0x20`, DEL and a named set of Unicode direction/zero-width characters — **but not C1 (U+0080–U+009F)**. U+009B is the CSI introducer in 8-bit mode. A model names a file whose path carries one; the path parses cleanly, survives every upstream check, and reaches a `%s` in the review summary's refusal reason, where the terminal executes it as a control sequence. | Sanitization at the `appendTurn` door, which is now the only route into `m.turns`. | `TestC1InAnEditPathCannotReachTheTranscript` (`clients/tui/sanitize_wiring_test.go:438`). **Re-verified 2026-09-04**: it exists, it passes, and it **runs rather than skips** — the gap is still open upstream. It self-skips only if `editapply` ever closes it. Neutering `appendTurn`'s sanitization fails it immediately, with the C1 byte visible in the transcript. |

### The C1 finding, verified rather than transcribed

The upstream gap was re-probed directly on 2026-09-04 rather than taken from the
earlier report. `editapply.RejectUnprintablePath` returns:

| Input | Result |
|---|---|
| `U+009B` (CSI introducer) | **accepted** |
| `U+0080`, `U+009F` (C1 range ends) | **accepted** |
| `0x1B` (ESC), `0x01` (SOH), `0x7F` (DEL) | refused — "contains a control character" |

So the description is exact: C0 and DEL are refused, the entire C1 range is not.
The client-side fix stands on its own and does not depend on editapply changing.

## Instrument errors caught, and why they are listed here

Five of this batch's findings were errors in my own measurements, not in the
code. They are listed because a reader deciding whether to trust the numbers
above should know how the numbers were checked.

1. **Coalescing silently made three earlier gates vacuous.** Both per-token
   allocation gates passed with the render cache neutered, and two of three
   scroll tests passed with the scroll fix neutered — a token no longer draws,
   and they were sending bare tokens. Caught by re-neutering; fixed by
   `deliverToken`, which forces the repaint the token asked for.
2. **A coalescing test counted arms, not repaints.** A `refreshSoon` neutered to
   draw on every token still armed exactly once. It now asserts the drawn view
   does not change between ticks.
3. **The soak's allocation sample measured a discarded message.** A `tokenMsg`
   after `streamDoneMsg` is dropped as a stray; it reported 3 allocations at
   every transcript length — the number for doing nothing.
4. **A neuter check that did not compile looked like a passing guard**, twice.
   The second time it exposed a real defect: `scripts/errcheck-ceiling.sh`
   counted lines with stderr discarded, so a module that would not build scored
   a perfect zero. That gate now fails closed.
5. **The 3.6 paste measurement was taken with the input blurred.** `startTurn`
   blurs the prompt box, so a paste arriving mid-stream is bounded, reported to
   the user, and then discarded by `textinput`. The "0.30 ms" figure was the cost
   of a paste that never landed. Found at P3.3 by an assertion added for exactly
   this reason — that the paste reaches the input before the clock starts. The
   delta 3.6 reported still stands; the absolute number is now 389 µs, and it is
   the same at 0 bytes as at the 2 MiB ceiling.

## Conditions on the release decision

1. **Somebody drives the client by hand** before this ships. Item 1 above is the
   largest gap in this document and no amount of test coverage substitutes.
2. **CI runs green once** with the new workflow steps — they have never
   executed on a runner. This condition was **unsatisfiable until P4.1**: the
   release workflow did not build the terminal client at all, so "green for this
   target" named nothing. That is why R1.14 was promoted out of the risk register
   and fixed rather than accepted.
3. **Decide R1.5/R1.6.** A decision memo now exists —
   [docs/DECISION_MEMO_REDACTION_2026-09-04.md](DECISION_MEMO_REDACTION_2026-09-04.md)
   — with a recommendation on each: **fix R1.5** in the daemon (exact-match
   stripping of provisioned env values, ~40 lines, available because
   `mcp.ServerEnv` holds the literal bytes), **reject R1.6** and accept it in
   writing (the daemon matches on shapes, so the asymmetry is permanent).

   It also carries a **third item that was in neither row and is a defect rather
   than a design question**: the outbound scrub is bypassed by one turn. A secret
   the daemon redacts from the prompt is persisted RAW to `memory.db`, copied
   into the `turns_fts` index, and sent to the provider verbatim inside the next
   turn's history — `persistTurn` stores `promptReq.Prompt` rather than
   `cleanPrompt`, and `prepareHistory` does not scrub. Verified by execution.
   The user is shown a redaction notice and the key leaves anyway, one turn late.
   Fixing it needs no new detector and no new judgment about what a secret looks
   like; it applies a decision the product already made.
4. **Confirm the memory baseline** in item 4, or re-derive the ceiling.
5. **Accept R1.12 in writing, or take one of its cheap trades.** A repaint at the
   transcript ceiling breaches D-1's hard per-Update ceiling. It is bounded,
   measured and recorded, and it is the one place in this document where a stated
   budget is knowingly not met. Shipping over it is defensible; shipping without
   someone having decided to is not.
