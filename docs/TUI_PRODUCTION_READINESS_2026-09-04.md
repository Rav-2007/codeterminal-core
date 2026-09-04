# Terminal client — production readiness

**Date:** 2026-09-04. **Branch:** `audit/adversarial-pass`. **Scope:** `clients/tui`.
**Verdict:** ship-ready on the axes measured below, with four named conditions.

This is written to be read before a release decision. **What was not verified is
stated as prominently as what was**, because the second list is the one that
decides whether the first is worth anything.

---

## What changed, and what it was worth

| Property | Before | After | How it is held |
|---|---|---|---|
| Allocations per streamed token, 400 prior turns | 2,234 | 29 | `TestPerTokenAllocationsAreFlatInTranscriptLength` — a ratio, not a constant |
| Growth of that number with conversation length | 97× | 1.6× | same |
| 1 MB paste, worst input measured | 15.6 ms median | 0.30 ms | `TestNoMoreRunesReachTheInputThanCanBeKept` (deterministic) |
| Oversized paste | silently truncated at 4,000 chars | reported | `TestAnOversizedPasteSaysWhatItDropped` |
| Transcript memory | unbounded | 500 turns / 2 MiB, with a visible marker | 2,000-turn soak |
| Scrolling back during a stream | impossible — snapped to bottom every token | works | three separate tests |
| Mouse text selection | silently disabled, undiscoverable | `/mouse`, in the idle hint | `TestTheMouseToggleIsDiscoverable` |
| Unchecked errors | 5 | 0 | ceiling lowered, and the gate no longer fails open |
| Absolute build paths in a binary | 678 | 0 | `scripts/supply-chain.sh`, `-trimpath` on releases |
| Rendering determinism | 3 different byte strings for the same state | 1 | 14-environment × 4-profile subprocess matrix |

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
the same caveat.

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
- **Repo-wide green.** vet (linux + windows), staticcheck/ineffassign/bodyclose,
  `-race` (376 s), coverage ratchet on all nine modules with the TUI floor
  raised 83.0 → 84.0, errcheck 0, govulncheck 0 reachable, fuzz gate, docs
  links and registers, supply chain.

## What was NOT verified — read this part

1. **No human has used any of this.** Every measurement here is from a test
   harness or a pty driven by a test. Nobody has typed into the client since
   these changes landed. The scroll fix, the eviction marker, the paste notice
   and `/mouse` are all judged by assertions about what the model contains, not
   by anyone looking at a screen.
2. **Linux only.** The pty tests, `LOCAL_PEERCRED`-adjacent paths, the signal
   handling and the file-descriptor test are all Linux. Windows gets `go vet`
   and nothing else in this batch. macOS was not exercised at all.
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
6. **The terminal client is not built by the release workflow** (R1.14), so none
   of the release hardening in this batch — `-trimpath`, signing — currently
   applies to it.
7. **No performance measurement under memory pressure, on a slow disk, or on a
   shared CI runner.** Every wall-clock figure is from an idle laptop.
8. **Security posture was not re-audited.** This batch was performance,
   correctness and operability. The security findings below are carried
   forward from earlier passes, not re-verified here.

## Security findings carried forward

| Issue | Exploit scenario | Fix | Verifying test |
|---|---|---|---|
| **`/mcp-server` shows daemon + MCP stderr unredacted** (R1.5) | A third-party MCP server prints its API key at startup; `mcp list` runs it with `CombinedOutput()` and the key lands on the user's screen and in their scrollback. Only path in the client that puts daemon stderr in front of a user. | **None. Unfixed by decision** — enumerated in 2.3a, no go-ahead for the structural redactor. | **None.** Stated as a coverage gap. |
| **Model-emitted secrets in the transcript** (R1.6) | A model echoes a credential it read from a file; it is rendered and persisted like any other answer. | **None. Unfixed by decision**, same reason. | **None.** |
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

Four of this batch's findings were errors in my own measurements, not in the
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

## Conditions on the release decision

1. **Somebody drives the client by hand** before this ships. Item 1 above is the
   largest gap in this document and no amount of test coverage substitutes.
2. **CI runs green once** with the new workflow steps — they have never
   executed on a runner.
3. **Decide R1.5/R1.6**: authorise the structural redactor, or accept both in
   writing. They are currently open with no test and no owner.
4. **Confirm the memory baseline** in item 4, or re-derive the ceiling.
