# Engineering method — how this codebase is made robust

**Written 2026-08-09.** Every other document here records *what* was built or
*what* is broken. This one records *how*, because the method is the reusable
asset and it was previously visible only by reading forty-three documents and
inferring it.

It is descriptive, not aspirational. Every technique below is in the tree today,
and every one is introduced by the bug that made it necessary — because a
practice with no failure behind it is a preference, and preferences do not
survive a deadline.

---

## The measured state this method produced

| | Measured 2026-08-09 |
|---|---|
| Go source | 32,601 lines across six modules |
| Go tests | 48,059 lines — **1.47 : 1** test-to-source |
| Test functions | 1,149 |
| TypeScript (VS Code client) | 6,934 lines |
| Commits | 446, from 2026-07-05 |
| Coverage floors | `daemon` 74.0 · `daemon/mcp` 91.0 · `editapply` 88.0 · `proxy` 85.0 · `protocol` 91.8 · `clients/tui` 78.9 |

Ratios are not the claim. The claim is that each number below it was **measured
on this tree**, and that the gates which produce them fail when the thing they
guard is removed — which is the subject of the next section.

---

## The one rule everything else follows from

> **A fix is not done when the test passes. It is done when the test has been
> demonstrated to FAIL with the fix removed.**

A test written alongside a fix usually passes for the wrong reason. It passes
because the code is fresh in mind and the assertion was written to match what the
code does, rather than to describe what must be true. The only way to tell the
difference is to break the fix on purpose and watch.

This is called **neuter verification**, and it is run as a matrix — one row per
way the fix could be undone, each naming the test that must catch it:

```
NEUTER                                          RESULT      TEST
history annotation removed                      CAUGHT      ACutOffPriorAnswerReaches...
unknown slug echoed into model context          CAUGHT      AClientCannotAuthorText...
buildHistory drops the flag (THE ORIGINAL BUG)  CAUGHT      Chat_ACutOffAnswerIsMarked...
VS Code Turn type loses the field               CAUGHT      VSCodeTurnTypeCarries...
RESTORED                                        NOT CAUGHT  all
```

The last row is not decoration. A matrix that never reports NOT CAUGHT after
restoring is a matrix whose neuters were not applied.

**It pays for itself immediately.** On 2026-08-09 the matrix caught three tests
that passed against live regressions — see *Match the construct* below. None of
those three would have been found by review; two were written by the same person
who wrote the fix, minutes earlier.

---

## The techniques

### 1. Anti-vacuity floors on anything that scans

Any test that greps, parses, or walks can silently match nothing and then pass by
comparing two empty sets. This has happened **four times** in this project, so
every scanner now asserts a plausible count *before* it compares.

```go
if len(found) < 3 {
    t.Fatalf("found only %d accept site(s): %v\nThe scanner must have broken ...", len(found), found)
}
```

**The bug that created it** (`fa4035c`): the cross-client slash-catalog parser
anchored on the first `[` in the file, which belongs to the *type* — `SlashDef[]`
— not the array. It parsed **zero** entries and compared them happily against
zero. Only the count assertion caught it.

### 2. Match the construct, not the word

A guard that greps for a name matches that name in comments, in variables that
merely contain it, and in the neighbouring function. All three are real, all
three were measured on one day:

| The check | What it actually matched | Consequence |
|---|---|---|
| `Contains(src, "incomplete")` | the field's own **doc comment** | deleting the field left the test green |
| `Contains(handler, "incomplete")` | the local variable `incompleteReason` | deleting the field it feeds changed nothing |
| `Contains(src, "authorizePeer")` | the comment *"Fails closed. See authorizePeer."* | **deleting the peer-authentication call left the security test green** |

The fixes are mechanical once the class is named: strip comments before matching
(`stripGoComments`, `daemon/socketauthcoverage_test.go`), and match a call or a
declaration — `authorizePeer(`, `\bincomplete\??\s*:` — not a bare identifier.
Each matcher then gets its own test driving it against the near-misses above.

### 3. Ratchets — floors that only rise, ceilings that fail both ways

Coverage floors (`scripts/coverage-floors.txt`) may only ever be raised, and only
to a number **measured on this tree**. Lowering one to make a red build green
converts the mechanism that noticed a regression into a rubber stamp.

The errcheck ceiling fails in **either** direction: more unchecked errors is a
regression, fewer means the ceiling is stale and should be tightened.

**A trap worth knowing** (2026-08-09): `go test` **caches** results, so three
"repeat" measurements can be one measurement wearing three hats. Forcing
`-count=1` revealed `protocol` swinging 93.3 / 93.3 / 92.3 — a one-point spread
that would have made a floor set just under 93.3 flake forever. Floors are set a
few tenths below the *lowest* of three uncached readings.

### 4. Parity tests, because a comment cannot fail a build

Any invariant spanning two files kept in step "by convention" eventually drifts.
This project had three such mirrors, all requested by comment, and the pair
drifted **twice in one week**, both times a fix landing in one client and not the
other (`4918a0c`, `76a87d0`).

They are now enforced by tests that read both sides and fail the build:

| Mirror | Why drift is not cosmetic |
|---|---|
| Slash catalog (TUI ↔ VS Code) | preambles go **on the wire to the model**, so drift means one command behaves differently in each client |
| `neutralisedGitConfig` | a **security list** — one client neutralising a config key and the other not means one client is silently exploitable |
| Proxy allow-list ↔ `models.json` | drift means a shipped model is refused, or an unshipped model is paid for |
| Cut-off flag (both clients) | one client's users get a model that treats its own truncated answers as complete |

`a30e664` is what this catches: `defaultAllowedModels` carried the comment *"the
three tiers in models.json"* and named three. `models.json` had grown to eleven.
**Eight of the nine selectable models were refused**, and the two extras the proxy
did allow were the two *inactive* tiers.

### 5. Closed sets over prose, when a boundary is involved

When a client tells the daemon something that ends up in the model's context, it
sends a **slug from a closed set** — never a sentence. The daemon owns every byte
of rendered wording.

This is a security property, not a style choice. `validTurn` refuses any role but
`user`/`assistant` precisely so a client cannot inject a message carrying
system-level authority; a field that accepted client-authored prose would hand
that ability straight back. An unrecognised slug renders **nothing** rather than
being echoed (`a4c9d15`).

The same shape recurs: report a *conclusion* the receiver can branch on, never
the internal detail behind it — `IncompleteInfo.Reason`, `ErrorClass`,
`Degradation.Component`, `Redactions` (kinds, never the matched text).

### 6. Tripwires on premises, not just on code

A decision that says *"revisit this if X becomes true"* is an unenforced
invariant. `daemon/socketauthcoverage_test.go` classifies every accept loop in the
product and fails the build on an unclassified one, naming the two founder
rulings whose premise just changed (`c8f7ca2`).

Writing it found a **second listener nobody had ruled on** — the embedder helper
accepts on its own socket and never authenticates its peer.

### 7. Fail closed, on every path, including the ones that cannot happen

Peer authentication refuses on: UID mismatch, a connection exposing no
credentials, a failing syscall, an unsupported platform, and a `Getuid()` of −1.
The last two are "impossible" cases; they are refusals anyway, because the cost of
being wrong about impossibility is trust-everyone.

### 8. Compile what you do not run

`go vet` on Linux compiles **two** of this repo's six build tags. Two gates exist
because of what that hid:

- `87af8d7` — the `warnscan` tag was compiled by **nothing**. The code had rotted unnoticed.
- `0c7afb9` — `make crossvet` compiles the Windows and Darwin files locally, so `make vet` can no longer report "clean" over broken platform code.

The general form: **an untested path and an uncompiled path fail the same way,
and the uncompiled one is quieter.**

### 9. Audit by execution, never by reading

An earlier QA campaign recorded **PASS on a sandbox that did not exist**. Since
then a claim is only as good as the thing that was run to produce it, and the
vocabulary is fixed so a reader can tell which is which:

| Label | Means |
|---|---|
| **CONFIRMED** | Reproduced by something actually run, or read directly off the line cited |
| **PLAUSIBLE** | Reasoned from source; the failing path was never executed |
| **NOT RUN** | Honestly not attempted — no hardware, no spend, out of scope |
| **FIXED** | Implemented *and* neuter-verified |
| **CLOSED** | A founder signature. **Engineering never writes this.** |

### 10. Records are annotated, never rewritten

When a register entry turns out to be wrong, it is struck through and the wrong
reasoning is kept. `docs/OPEN_ITEMS.md`'s entry for L2 argued the item was *"a
behavioural question rather than a defect"* and that *"the M1 batch already made
the truncation visible to the user."* The second clause is what disguised the bug
for weeks — making it visible **to the user** made the system look handled while
the model was still being shown truncated answers as complete.

Deleting that would remove the only evidence of how a real bug stayed open. A
register that quietly erases its own wrong calls teaches nothing.

### 11. One owner per fact

Every fact lives in exactly one document, and the others link to it. Both
[`HANDOFF.md`](HANDOFF.md) and [`README.md`](README.md) carry the same
instruction — *do not add a fifth* — because the failure this project keeps
hitting is a second copy of the truth that drifts from the first.

Broken links now fail a build (`23550e4`, `scripts/docs-links.sh`, 265 links),
since documentation-only pushes start no CI at all.

### 12. A performance gate asserts allocations, and only reports wall-clock

**The bug that produced this rule.** A test asserting that no single `Update`
exceeds one 16 ms frame was written against a measured 6.3 ms. It then failed in
CI. It was retuned, and failed again — in the other direction, passing where it
should have caught a regression. Four samplings of *the same input, same machine,
same commit* gave medians of **6.3 ms, 13.6 ms, 14.5 ms and 16.6 ms**: a 2.6×
run-to-run spread straddling the line the test was drawn on. Under `-race`, which
is how the repository's own gate runs, the same measurements are 5–20× slower
again.

A wall-clock threshold on a shared machine cannot resolve a 2.6× spread. A gate
that flaps gets waived, and a waived gate is worse than no gate, because it is
still on the list of things believed to be checked.

**The rule.**

- **Allocations are what a performance test fails on.** `allocs/op` is
  deterministic, identical in CI and on a laptop, and independent of load. It is
  also the better signal: a render cache is doing its job when allocations per
  token go *flat* with respect to transcript length, and a cache can be quietly
  wrong while wall-clock improves.
- **Wall-clock is measured, printed with its distance to the budget, and
  asserted only at a wide multiple** of that budget — wide enough that firing
  means the cost grew past anything noise explains.
- **Timing tests do not run under `-race`.** `raceEnabled`
  (`clients/tui/raceflag_race_test.go`, set by build tag) is what they skip on.

**Enforced, not just written down.** `TestTimingBudgetsAreGuardedAgainstTheRaceDetector`
(`clients/tui/testpolicy_guard_test.go`) parses the package's own test sources,
finds every declared `time.Duration` budget, and fails if the file declaring one
does not reference `raceEnabled`. It is structural rather than name-driven: it
keys on the declaration's *type*, so a new budget constant in a new file is
caught whatever it is called. What it does **not** catch is a timing assertion
written inline with no named constant — stated here because a gate whose limits
are undocumented gets trusted past them.

---

## The failure taxonomy underneath all of it

Nearly every defect in this project's record is one of five shapes. Naming them is
what makes the next one findable:

1. **Unenforced invariant** — two things kept in step by a comment. *Every mirror above.*
2. **Vacuous verification** — a test that passes when the fix is removed. *Four occurrences.*
3. **Screen-not-model / report-not-enforce** — the system tells a human it handled something while the mechanism that acts on it never learns. *L2; warn-mode's log-only half.*
4. **Uncompiled path** — code no gate builds. *`warnscan`, Windows, macOS.*
5. **Stale record** — a document describing a tree that no longer exists. *Seven corrections on 2026-08-07 alone.*

Shape 3 is the most expensive, because it looks like success from the outside.

---

## What this costs, stated honestly

Neuter verification roughly **doubles** the time a fix takes. The matrices in this
session ran 18 neuters across two features; three of them found tests that were
already green against live regressions, and one found a *security* test that was
green with peer authentication deleted.

That is the trade: every fix costs about twice as much, and in exchange the suite
means what it says. On a codebase where the security boundary is a local socket
and the write path edits a user's files, that has been worth paying.

**What it does not buy.** None of this makes the product *installable*, and no
amount more of it will. See [`../BACKLOG.md`](../BACKLOG.md) — the engineering
board and the readiness board are scored separately, on purpose.
