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

## What is verified, and by what

- **Correctness of the render cache.** `cache.render == renderTranscript`
  byte-for-byte over 200 seeded mutation sequences, four colour profiles, widths
  0–200, and **1.2 million fuzz executions** across the three TUI fuzz targets.
  Twelve mutation shapes enumerated and each asserted individually.
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
4. **The 39.3 MB RSS baseline could not be reproduced.** The bound was declared
   against numbers I measured myself (8.6 MB idle, 13.7 MB at 120 turns, both
   before and after the render cache) after two attempts at reproducing the
   figure I was given, including varying answer size. If that number came from a
   different harness, the memory ceiling in this document is anchored to the
   wrong baseline.
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

**One item I could not place.** The brief asked to include "the C1 edit-path
finding". Nothing in this pass's record carries that label, and I have not
invented one to fill the row. The nearest candidates are R1.4 above and the
earlier `editapply` axis findings (FAIL-1 case-folding, FAIL-2 undo writer),
which are recorded elsewhere and were not re-verified here. **Please confirm
which is meant** rather than reading this table as complete on that point.

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
