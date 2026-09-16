# C3b — `reachesUnboundedRead`: the chain property a file-scoped guard cannot reach

<!-- coderefs: enforced -->

**Recipient: the `daemon/` owner.** Two production reads were bounded and a class guard was added.
Nothing here is a recommendation; every row below is a fix or a measurement.

| | |
|---|---|
| **Branch** | `audit/adversarial-pass` |
| **HEAD** | `075f438` |
| **Tree** | clean at every commit boundary and before every neuter arm (H8) |
| **Canonical remote** | `upstream` = **`Rav-2007/codeterminal-core`** |
| **`make check`** | `REAL_MAKE_CHECK_EXIT=0` at `075f438`, run alone (H11), nothing else in flight |
| **`build` / `gates`** | run **35056415778** / **35056415792**, `c622848`, **success** — the C7b push, one commit behind this work |
| **CI at HEAD** | **none yet.** "No build run at HEAD" is a distinct state from "build passed." |

---

## 1. What C3 left, and why a file could not hold it

C3 closed Class III at file scope. `TestBuiltinToolSurfaceBoundsItsReads` reads the files
implementing model-facing tools and forbids the unbounded readers in them. It was the right shape for
three of four sites and its own comment records the fourth:

> `mcp_lsp.go` has NO read of its own. Its handlers' only outward call is `srv.Call`, and the
> unbounded read in that chain is `readHeaders`' `ReadString` in `lsp_bridge.go` — one file deeper,
> where a guard that reads FILES cannot see it. Listing `mcp_lsp.go` is therefore necessary and NOT
> sufficient.

So this walks the call graph, as `TestNoBuiltinSpawnsWithoutDeclaringIt` walks it for `exec.Command`,
and asks which handlers can **reach** a read whose size the thing being read decides.

**The definition is the test.** A read is unbounded when the byte count is chosen by the resource,
not the caller: `os.ReadFile`, `os.ReadDir`, `io.ReadAll`, and bufio's `ReadString` / `ReadBytes`.
`io.ReadFull` is deliberately absent — its length is the caller's slice, which is exactly why
`lsp_bridge.go`'s **body** read was never the problem and its **header** read was.

---

## 2. It found more than the row it was written for

| Site | Reached by | Status |
|---|---|---|
| `readHeaders` (`daemon/lsp_bridge.go:491`) | `query_compiler_definition`, `query_compiler_references`, **`propose_ast_edit`** | **FIXED**, `9f405c0` |
| `parseGitignoreLayer` (`daemon/chunker.go:547`) | `search_code`, `repo_map` | **FIXED**, `0807d4a` |
| `readReferencedSpan` (`daemon/fileref.go:359`) | `search_code` | bounded; **recognised**, not exempted |
| `regionsOnDisk` (`daemon/chunkexpand.go:266`) | `search_code` | bounded; **recognised**, not exempted |

**Three handlers reach TB7, not two.** `propose_ast_edit` calls `lspServerForFile` directly, skipping
`handleLSPQuery`. C4 row 4 enumerated the entry points by tracing examples and was short by one
**(M)**. That is the difference between a chain property and a traced example, stated as plainly as it
can be.

`parseGitignoreLayer` is a **new finding**: the one read in `chunker.go` that does not re-run the
gate. `readEligibleFile` in the same file re-runs `shouldSkipFile` immediately before reading, and
`planmode.go` states the rule in general — the read side must not assume the write side ran.

**Both chains were verified rather than believed (H13).** The walk matches callees on the function's
own name, ignoring the receiver — an over-approximation that can pull in an unrelated method sharing a
name. `matchDir`, `matches` and `layerFor` are all methods on one type in one file, so the gitignore
chain is real, not a collision **(M)**.

---

## 3. TB7 was unbounded in two dimensions, and they needed different fixes

`maxLSPMessageBytes` (8 MiB) bounds the **body**: `readHeaders` returns a length, `readLoop` refuses
one over the cap, and `io.ReadFull` reads into a slice of exactly that size. Nothing bounded the
**header**.

| Dimension | Before | After |
|---|---|---|
| header **line** | **135,962,152 bytes** allocated over a 64 MiB newline-free stream — twice the input, 16× the body cap | **36,680 bytes**, and an error naming the limit |
| header **count** | an endless run of well-formed `X-Pad: y\r\n` held `readHeaders` past a 10s deadline with **no allocation growth at all** — a hang, not an OOM | refused at 64 lines |

All **WALL-CLOCK** for the timeout, **DETERMINISTIC** for the byte counts (same fixture, same result
across runs).

The count dimension is the sharper one: it hangs `readLoop`, the goroutine every LSP caller waits
behind, and it does so without consuming memory — so every allocation-based instrument is blind to
it.

**The fix is `ReadByte` in place of `ReadString`.** `ReadString`'s length is whatever arrives before
the delimiter; `readBoundedLine`'s is `max`. Still buffered underneath, so the syscall count is
unchanged and only the allocation is bounded. The newline is consumed and not returned, matching what
the caller already did with `ReadString`'s result — it `TrimSpace`d it, so `\r\n` and `\n` were
already equivalent there.

8 KiB and 64 lines are **generous rather than tuned**. LSP in practice sends `Content-Length` and
optionally `Content-Type`, neither over ~40 bytes.

Reachability stays low — `serverCommand` returns one of three hardcoded names on an inherited `PATH`
— and **low is not bounded**, which is why this is a fix rather than the recorded exemption C3b left
open as an option. All eleven existing LSP tests still pass, including the body-cap boundary pair.

### 3a. The neuter matrix is orthogonal, and it says what the AST guard cannot see

Each cap reverted independently, from a committed tree, restore check after each:

| Arm | line test | count test | AST guard |
|---|---|---|---|
| line cap removed, count cap kept | **RED** | GREEN | **RED** |
| count cap removed, line cap kept | GREEN | **RED** | GREEN |

Each behavioural test fires only for its own dimension, so neither is standing in for the other.

> **The AST guard covers the line dimension only.** It detects unbounded *readers*; a loop with no
> bound on its iteration count is invisible to it. The count dimension is pinned solely by
> `TestReadHeaders_DoesNotLoopForeverOnEndlessHeaders`, and this is the sentence that says so instead
> of letting a green guard imply coverage it does not have.

---

## 4. The gitignore read

| | |
|---|---|
| before | **90,333,064 bytes** allocated for a 33.5 MB `.gitignore`, against a `maxFileSize` of 1 MiB |
| first fix | `io.ReadAll(io.LimitReader(f, bound+1))` — still **5,241,464 bytes** |
| final | **432 bytes**; an oversized file costs no read at all |

`io.ReadAll` grows by doubling, so its intermediate buffers sum to about twice the final one **even
when the final one is bounded**. Stat first, then read into a slice sized from the stat, which spends
exactly what the file needs — the property this whole guard is about, since `io.ReadFull`'s length is
the caller's.

The buffer carries **one spare byte** because the stat is a hint, not the bound: a file that grows
between stat and read fills it, and that is refused rather than truncated.

> **Truncation is the wrong failure here, and worse than not reading at all.** A rule cut mid-line
> becomes a *different* rule — `build/secr` for `build/secret/` — so a truncating parse invents an
> ignore pattern nobody wrote.

An over-bound file returns the same empty layer the function already returned for an unreadable one.
That loses **precision, not secrecy**: `shouldSkipFile` checks `MatchesSecretName` and `isNoiseFile`
independently of any `.gitignore`.

`maxGitignoreBytes` is `maxFileSize` and not a new number. This indexer already refuses any ordinary
source file over that size; a second constant would be a second policy.

### 4a. The positive control is the arm that matters

The allocation test reports **"0 rule(s)"** for the oversized file. A bound that returned an empty
layer for *every* file would pass it perfectly while switching gitignore handling off — and nested
`.gitignore` support was a security fix (S1), so a layer that always parses to nothing re-opens it.

So: an ordinary `.gitignore` must still yield its three rules with the `dirOnly` and `negate` flags
intact, and the boundary is pinned on both sides. Neuter arms, from a committed tree:

| Arm | alloc test | boundary test | positive control |
|---|---|---|---|
| stat bound removed | **RED** | **RED** | GREEN |
| genuinely truncating | GREEN | **RED** (`parsed to 262144 rule(s), wanted 0`) | GREEN |
| parse fed an empty slice | GREEN | **RED** | **RED** |

---

## 5. Three defects in my own guard, and one in unrelated code

### 5a. The known-answer test would have broken the moment the defect was fixed

Its first version asserted `readHeaders` **is** a site. Fixing `readHeaders` would have turned it red
— **a guard that is an argument against its own remedy.**

It is now a synthetic fixture in `daemon/testdata/unboundedread_fixture.gotxt`: sixteen shapes, nine
that must fire and seven that must not, every one a spelling that occurs or occurred in this
repository. The negatives that survive a fix — `mcp_exec.go`, `mcp_lsp.go`, `webtools.go` — are
asserted against the real package instead, in a separate test.

With the `LimitReader` exception disabled, both bounded-`ReadAll` arms must fire, or the negative
proves nothing. Modelled on `detectorSeesTheAccessor`.

### 5b. My endless-header test passed, falsely

Its reader carried a 64 MiB limit, so the stream **ended** and `readHeaders` returned the resulting
EOF. **A fixture that terminates cannot test a loop that does not**, and the arm was a recorded claim
that the header count was bounded when nothing bounded it. Rewritten endless, with a stop flag so the
reader cannot outlive the test spinning — and it went red immediately.

### 5c. One exemption per handler hid every site but the first

`reachesUnboundedRead` returned the **first** site it reached, and exemptions were keyed on the tool
name alone. Together, one recorded decision silenced every other site under the same handler. **It
did:** `search_code`'s entry for `parseGitignoreLayer` hid `readReferencedSpan` completely, and that
site surfaced only when the first was fixed and the exemption removed. Reporting all sites then
immediately exposed a third, `regionsOnDisk`.

> **A gate that stops looking after one hit is a gate whose coverage shrinks every time you use it.**

Exemptions are now keyed `<tool>|<site function>`. One handler reaching one site is one decision; the
same handler reaching a different site is a different one and must still fail. That is the
two-states-one-phrase error this pass keeps finding, committed by the person writing the guard against
it.

### 5d. The antidote is a property, so the detector learned it rather than collecting exemptions

`readReferencedSpan` and `regionsOnDisk` each re-run `shouldSkipFile` — which enforces `maxFileSize`
— one statement before their `os.ReadFile`, and each says so in its own comment.
`builtinreadalloc_test.go` names `readReferencedSpan` as the reference implementation of the correct
pattern.

`gateGuardEnds` accepts a read preceded by `if … shouldSkipFile(…) … { return }`. **All three
conditions are load-bearing and each has a fixture arm that must fire without it:**

| Condition | The arm that proves it |
|---|---|
| the call is in the `if`'s init or condition | `gateResultIgnored` — a bare call statement |
| the `if`'s body returns | `gateBodyDoesNotReturn` |
| the `if` closes **before** the read | `gateAfterTheRead` |

Because *"the gate was mentioned"* is not *"the gate was obeyed"*: a function that calls the gate and
ignores its result reads exactly as far as one that never called it.

`TestEligibilityGateStillExists` is the anti-rot half. A recogniser keyed on a function name silently
excuses nothing if that name stops resolving, so the name is asserted to resolve **and** the matcher
is asserted to fire on a real use of it, with bound recognition disabled to prove the recogniser is
what excludes it. **That `shouldSkipFile` still enforces a size cap is NOT asserted there**, and its
comment says so rather than implying it.

**`unboundedReadExemptions` is empty. The guard is green on facts, not on silence.**

### 5e. My fixture broke two retrieval guards I had nothing to do with

The fixture began as Go source in a raw string literal inside a Go file. Two unrelated guards went
red:

```
TestTheHeuristicAgreesWithTheCompiler
  the heuristic invents 16 boundaries the compiler does not recognise
TestConstructExtentsNeverStopShortOfTheCompiler
  constructExtents stops SHORT of the compiler on 1 construct(s), past the 0 measured on 2026-08-28
```

`chunkcontext`'s heuristic finds top-level declarations by scanning lines, so `func unboundedDir()
{…}` inside a string literal reads to it as a real declaration while `go/parser` correctly sees
string contents. **Not cosmetic guards:** `constructExtents` feeds `expandToNeighbours`, the +10.2pp
delivery-time expansion, and an extent that ends early delivers a function with its tail cut off —
the exact failure expansion exists to prevent.

Moved to `testdata/*.gotxt`, which is a Go file to neither. Both guards report *"agreeing exactly"*
and *"0 stop short"* again **(M)**.

> Recorded rather than routed around: **any file embedding Go source in a raw string over-counts its
> declarations to this heuristic and can make `constructExtents` over-extend.** Rare, low impact, and
> now demonstrated by accident.

---

## 6. Corrections (H3)

| # | What I had | What is true |
|---|---|---|
| 1 | *"the header count is bounded"* — a green test arm | **Retracted before it was committed.** The fixture terminated; the count is unbounded, and the corrected arm hangs past 10s. |
| 2 | *"truncation is not covered by any test"* — read off a neuter arm | **Wrong, and it never left the scratchpad.** The neuter had not taken effect: the `+1` grew-check defeated it. A genuinely truncating neuter makes the boundary test fire with `parsed to 262144 rule(s), wanted 0`. H2 applied to my own neuter arm. |
| 3 | C4 row 4's two TB7 entry points | **Three.** `propose_ast_edit` reaches `lspServerForFile` without `handleLSPQuery`. |
| 4 | The first `io.ReadAll(io.LimitReader(…))` fix | Correct in kind, insufficient in fact — 5.2 MB, because `ReadAll` doubles. |

One more, caught by the local gate rather than by me: `staticcheck` U1000 on a helper the
known-answer rewrite left unused. `go vet` does not report an unused package-level function;
`staticcheck` does. Fixed in `075f438` before any push.

---

## 7. What I did not verify

- **No CI run at HEAD.** The green `build` and `gates` are at `c622848`, one commit behind `2246d10`.
- **That any of this is exploitable.** Reachability is not a threat model, and the guard's own header
  says so. A language server is reached only through `serverCommand`'s three hardcoded names on an
  inherited `PATH`; what was measured is allocation and hang, not an attack.
- **The grew-between-stat-and-read path.** The `+1` byte and its refusal are reasoned and unit-shaped,
  not raced. `(U)` — what would settle it: a test that grows the file between the two syscalls, which
  needs a filesystem hook this suite does not have.
- **`maxLSPHeaderLineBytes` and `maxLSPHeaderLines` against a real language server.** gopls,
  typescript-language-server and pyright were not run; the values are argued from the protocol, and
  no LSP implementation is known here to send a header over ~40 bytes or more than two of them.
  `(U)` — what would settle it: one real session per server.
- **That `shouldSkipFile` still enforces `maxFileSize`.** The recogniser trusts it and
  `TestEligibilityGateStillExists` asserts only that the name resolves and the matcher fires.
- **Whether any other module reaches an unbounded read.** The walk is within `daemon/`, like its
  template. A handler reaching a read through `editapply` or `protocol` is invisible to it.
- **The 86 over-extending constructs** reported by the restored guard. Pre-existing, unchanged by this
  work, and not examined.

---

## 8. Handover

| What | Who |
|---|---|
| The two fixes and the class guard — review as `daemon/` changes | **`daemon/` owner** |
| Whether `maxLSPHeaderLines` at 64 is right for a server nobody here has run | **`daemon/` owner** |
| Whether the heuristic's raw-string over-count is worth fixing or recording | **`daemon/` owner** |
| Extending the walk past `daemon/`, or deciding not to | **`daemon/` owner** |

## 9. Hardening instances earned here

| Rule | Instance |
|---|---|
| **H2** (ninth) | A **neuter arm** was the untested instrument. I read "did not fire" as a coverage gap when the neuter had never taken effect. |
| **H3** | Two retractions, both before they reached a report. |
| **H8** | Every neuter arm ran from a committed tree, with a restore check after each; HEAD verified unchanged at the end of each matrix. |
| **H9** | "Low reachability" was not allowed to stand in for "bounded". The fix landed because low is not a measurement of a bound. |
| **H13** | A known-answer test anchored on a live defect produces right answers *today* and is wrong as a derivation — it would have failed on the fix. Check the derivation, not the outcomes. |
