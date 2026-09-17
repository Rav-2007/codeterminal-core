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

---

# Addendum, 2026-09-15 — the method for auditing, and its ten rules

**Annotated, not rewritten**, per technique 10. Everything above describes how code
is made robust. This section describes how *an audit of that code* is kept honest,
and it exists because a six-chunk adversarial pass produced more instrument
failures than code failures.

**Every rule below is followed by the instance that earned it.** A rule without its
instance decays into a slogan, and a slogan is what people skip.

## H1 — No circular evidence

An artifact's presence in the repository is evidence about **the work**, never about
**the documentation of the work**.

> *Instance:* §13 of the 2026-09-13 checkpoint is a list of what was never verified.
> Citing the list as evidence that those things were never verified is circular: the
> list is a claim, not a measurement. Checked against the workflows instead, **three
> of its 27 rows are false** and a fourth is partly false.

## H2 — Anything you use to interpret evidence is untested until tested

Not only extractors. **An exit status, a reference frame, a truncation, a gate's
scope, and your own shell loop are all instruments.**

> *Seven instances, all in one pass:* a 7-hex regex matching the decimal `1734900`;
> `$?` after a pipe returning `tail`'s status; a `for` loop reporting 1 after six
> clean lints; CI line numbers read against a working-branch copy of a file 1,266
> lines different; `head -8` reported as a cardinality; **a gate that silently
> checked one file extension**, which "validated" a line number from one commit
> against a file 1,627 lines long where it was 585; and a neuter harness whose inner
> loop shadowed its outer loop variable, mislabelling six rows that were themselves
> correct.

**The generalisation that costs the most to learn:** when you interpret output
produced at commit X, **check out commit X or name the revision you read**.

## H3 — Retract before you report

State what would falsify each finding, and whether you looked.

> *Instance:* "32 dangling citations" was reported and the true answer was **zero** —
> the extractor had split shell flags and counted a gate's own self-test fixtures.

## H4 — A dissolved premise is a first-class result

> *Instance:* C2's stated premise was *"the 13 Extension-Development-Host suites have
> never run."* They run on every push. **The falsehood was the finding**: the suites
> ran, and the one that touched the defect *asserted* it.

## H5 — Name the recipient

A finding with no addressee is a finding nobody owns.

> *Instance:* whether `daemon/CHUNK_SCRUB_DESIGN.md` is a decision record or a
> scoping note is the owner's call; the audit's job was to put both halves of the
> contradiction on one page and stop.

## H6 — Evidence must be at the resolution of the question

> *Instance:* "macOS `cross` is green" does not answer "did macOS run the tests".
> The job log does: `go test -count=1 ./...` runs unconditionally, so the green was
> test coverage and not compilation.

## H7 — Enumerate incumbents before claiming a name

> *Instance:* `B<n>` already meant four different things, so the trust boundaries
> became `TB<n>`. **And `M5` is currently two things** — the phrase-pair rule below,
> and a Lane B threat-model row for a lying `readOnlyHint`. That collision is
> recorded here rather than resolved, because renaming either costs more than it
> returns today.

## H8 — Destructive neuter arms run from a committed tree

> *Instance, twice:* an arm's `git checkout` discarded an **uncommitted** fix, so the
> following arms ran against the unfixed tree. **The restore check caught it both
> times.** Now mechanical: `git status --porcelain` must be empty before the first
> arm, edits are anchored to a line rather than a pattern, and the restore check
> stays even though the precondition is now enforced.

## H9 — A hedge is not a measurement

If you write "possibly" or "likely", either run the check or write `(U)` with what
would settle it. **Apply every filter you have to the evidence you reason FROM, not
only to the evidence you tabulate.**

> *Instance:* a report hedged that "the last scheduled run had three macOS jobs
> failing." Wrong three ways — four jobs, four-second billing failures rather than
> code, and not the last run. The sub-15-second billing filter had been applied to
> the runs being *counted* and not to the run being *reasoned from*.

## H10 — Derive from a source of truth; do not enumerate

**This binds whoever writes the instruction, not only whoever runs it.**

> *Instance, twice:* an instruction specified *"exclude `.codeterminal/` by name"*
> and another specified *"grep `Handler: s.X`"* — in a repository whose recurring
> defect is enumerated scope. Both times the derivation was better: `git ls-files`
> closed `.vscode-test/` and build outputs at once, and a call graph kept three
> inline-closure handlers a grep would have dropped. **An instruction that names
> paths is a smell.**

---

## The mandatory report header

Every audit report opens with this table, because every one of these was wrong in
some report during this pass:

| Field | Why |
|---|---|
| branch | reports were written about the wrong branch |
| HEAD sha | line numbers are a property of a revision |
| tree state (clean / dirty) | a dirty tree invalidates every neuter below it |
| local vs upstream, both directions | "pushed" and "delivered" are different |
| latest `build` and `gates` run ids, with SHAs | a run id without a SHA is unverifiable |
| **the remote, named** | `gh` here resolves to a fork where CI does not run |

And one sentence that is not optional: **"no build run at HEAD" is a distinct state
from "build passed."**

## The M5 pair list

M5 is the rule that **two states meaning different things never share a phrase**.
Every pair below was found by something going wrong.

| | |
|---|---|
| *written* | ≠ *committed* ≠ *delivered* |
| *bounded by count* | ≠ *bounded in size* |
| *972 lines* | ≠ *972 newlines* |
| *`B<n>` as a boundary* | ≠ *`B<n>` as an item id* |
| *the command printed FAIL* | ≠ *the command failed* |
| *the push build on `main` is green* | ≠ *`main` is green* |
| *green on a branch* | ≠ *green on `main`* |
| *a duration that looks like a round number* | ≠ *a duration that is a limit* |
| *certifying a defect* | ≠ *accommodating one* |
| *a reference is checked* | ≠ *a reference of this extension is checked* |
| *a stated count* | ≠ *a counted count* |
| *green on a dispatch* | ≠ *green on a schedule* |
| *the run is green* | ≠ *the run passed on its first attempt* |

Two deserve expansion because they cost the most:

- **A duration that looks like a limit.** A scheduled job displayed `15m 0s`, which
  is timeout-shaped. The Go test inside it ran **847.941 s** under an explicit
  `-timeout 60m`. Nothing timed out.
- **Certifying a defect ≠ accommodating one.** A test that asserts a daemon dies
  without an environment variable, and a test that supplies the variable so the
  daemon lives, are the same failure in different clothes: both make the defect a
  fixture. **The accommodating form looks like ordinary setup and does not grep.**

## A sixth failure shape: the DELIVERY GAP

The taxonomy above has five shapes. This pass produced a sixth, seven times:

> **6. Delivery gap** — the artifact exists and the delivery does not. An audit of
> the *work* passes every time, because the work is fine.

*Instances:* a memo committed with nobody told; a checkpoint written and never
committed; a lint pin fixed on a branch and never merged while `main` stayed red;
a cherry-pick verified and never pushed; 40 commits on a stale local ref; an eval
fix green across three runs and never delivered; and a release fix repairing the
failure that killed the only release run this repository has ever had, still not on
`main`.

`scripts/reach.sh` is the gate for this shape. **It catches five of the seven** —
the two it cannot see are "nobody was told" and "never committed", and its closing
banner says so rather than implying coverage it does not have.

## A technique worth naming: commit the requirement before the implementation

When a fix needs a property that could be quietly dropped, **commit the check
first, while there is nothing for it to check.**

> *Instance:* adding a configuration surface to the VS Code extension needed
> `"scope": "machine"`, because a default-scope setting is overridable by a
> workspace's `.vscode/settings.json`. The requirement was committed **before the
> setting existed**, with its commit message stating plainly that the assertion was
> *"NOT yet evidence of anything"* because the branch was unreachable. It became
> evidence in the neuter. A requirement written afterwards is a requirement fitted
> to what was built.

## A candidate trust boundary — filed, not added

`docs/TRUST_BOUNDARIES.md` has twelve rows and does not name this crossing:

> **A VS Code workspace's `.vscode/settings.json` is attacker-authored content in
> exactly the sense repository source is.** A contributed setting at default scope
> would let a repository redirect the daemon's model endpoint, and the prompt is the
> user's own code. The mitigation is already in place — the setting is
> `"scope": "machine"`, and `clients/vscode/scripts/install-path-check.js` fails if
> that ever changes.

**Adding a thirteenth row is a taxonomy decision and it is the owner's.** Filed with
the evidence and the mitigation; not taken.

## What of this can be enforced mechanically, stated plainly

**Most of it cannot, and calling a convention a gate is the exact M5 error this
repository keeps finding.**

| Rule | Mechanical? |
|---|---|
| H8's committed-tree precondition | **Yes** — `git status --porcelain` before the first arm |
| The delivery gap (H-adjacent) | **Yes** — `scripts/reach.sh`, five of seven shapes |
| Document citations resolving | **Yes** — `scripts/docs-coderefs.sh`, now across every extension its documents cite |
| H1, H3, H4, H5, H6, H9 | **No.** These are judgements about evidence. No gate can ask whether you looked |
| H2 | **Partly.** A gate can print its own scope — `docs-coderefs` now names the extension set it checked and what it did not. It cannot test *your* instrument |
| H7 | **No**, and a grep is not a substitute: `M5` collided and nobody noticed for weeks |
| H10 | **No.** "Is this list derived or enumerated?" is not decidable from the list |

**Two structural limits worth stating once.** The documentation gates are `.md`-only,
so a rule written in a Go doc comment is checked by nothing. And `docs-coderefs`
resolves every citation against `HEAD`, so a citation to another revision's line
numbers is silently "validated" against the wrong file — **name the revision in
prose, because no gate will catch it.**

---

**A closing note on the section above this addendum.** It ends: *"None of this makes
the product installable, and no amount more of it will."* On 2026-09-15 that turned
out to be literally true — the VS Code extension could not start the daemon it ships
on any ordinary install, from the initial commit onward, and every test passed over
it for seventy-two days because every test supplied by hand the one thing no install
has. The sentence was written as a caveat. It was a finding.

---

# Second addendum, 2026-09-16 — three more rules, and the two that grew

Annotated, not rewritten. The ten rules above stand exactly as written; these are
what Phase 4 and the closing chunks earned, and each carries the instance that
earned it.

## H11 — A wall-clock gate runs alone, and the report says what else was running

> *Instance:* I reported a release-blocking gate failure I had created.
> `TestRepaintCostAtTheTranscriptCeiling` measured p50 **71.675 ms** against a 64 ms
> threshold and failed, while `make check` was backgrounded next to `gh` API calls,
> `npm audit` and several `go build` runs. Idle, the same commit measures p50
> **48.674 ms** and passes. The number was real; the verdict was about the machine.
>
> Mechanical form: every `make check` transcript in this pass opens with a
> `WHAT ELSE WAS RUNNING` line, and it says *nothing* or it says what.

**The corollary is sharper than the rule.** `make check` exit 0 is a statement about
the host as much as about the code. Idle p99 is **61.737 ms** against 64 ms — inside
5% — so the gate set this repository cites as the scope of a release verdict **is not
reproducible**, and a report that cites it owes the reader that sentence.

## H12 — An undelivered artifact does not merely withhold value; it suppresses the checks that would have run on it

> *Instance:* `evalguard` had been red since `21a0854` because two reports I wrote
> quote an eval query verbatim. `evalguard` runs in `gates.yml` with `-v`, and
> `gates.yml` carries **no `paths-ignore`**, so one push would have turned it red
> within a minute with a readable message. It stayed hidden for a session because
> the branch had not been pushed since `1013e1b`.

This reframes the delivery gap from bookkeeping to a defect class. `reach.sh`'s
unpushed-commit findings are not a filing convention: **each one is a set of checks
that has not run.**

> *Second instance, in the gate itself:* `reach.sh` measured "ahead" against the
> branch's configured upstream, which points at a fork. After the branch was pushed
> to the canonical remote — `upstream/audit/adversarial-pass` and `HEAD` both at
> `aa81555` — it went on reporting the branch as in flight, plus 57 pipeline commits
> and 178 citations as undelivered. Every one had arrived. **A gate for the delivery
> gap that cannot see a delivery is worse than no gate: it teaches you to ignore it.**

## H13 — A derivation that produces right answers on some inputs is still wrong on all of them. Check the derivation, not the outcomes.

> *Instance:* `stage-runtime`'s derivation was wrong for all three release targets
> and the answer was wrong for one. Linux and darwin packaged clean, each reading
> from its own per-target artifact directory, and their passes were **correct by
> accident**. Only win32 made the reason visible, and only the third target's failure
> got the derivation read.
>
> *Second instance, and it is the cleaner one:* a known-answer test asserted that
> `readHeaders` **is** an unbounded-read site. True on the day it was written, and it
> would have gone red the moment the defect it names was fixed — **a guard that is an
> argument against its own remedy.** Right answers today, wrong as a derivation. Now
> a synthetic fixture, so the detector's proof does not depend on a defect surviving.

## The delivery gap's seventh instance, and what `reach.sh` now sees

The section above records seven instances and says the gate catches five. Both halves
have moved:

| | |
|---|---|
| The seventh | `0925a3d` repairs the failure that killed the only release run this repository has ever had, and was not on `main`. **It is now, via the merge.** |
| What the gate could not see | "nobody was told" and "never committed" — still true, still in its banner |
| What the gate could not see and nobody knew | **a delivery.** Fixed 2026-09-16: `ahead` is now asked of the canonical remote's copy of the branch, and a branch 0 ahead of it is reported as **ARRIVED** rather than as its absence |

Two further defects in that gate, both found by using it rather than reading it, and
both recorded here because the shapes recur:

- **Check 1 described a stale pointer in the words of lost work.** Check 2 measures
  `HEAD..ref` for exactly this reason — its first version made two stale pointers read
  as "487 commit(s)" of lost work — and the correction went into check 2 and not into
  check 1, in the same commit. Simulating the remote rename in a throwaway clone made
  local `main` read as "40 commit(s) ahead", which is forty commits every one of which
  is already on `HEAD`.
- **An allowlist entry that matched nothing printed nothing.** Every exemption that
  *fires* is printed with the event that retires it — that was the design — so the one
  thing the allowlist could still hide was itself. Unconsulted entries are now listed,
  and not as a failure: an entry written ahead of an event is a legitimate recorded
  decision, and telling that from a dead one is a judgement no gate can make.

## One more shape, for the checkers: a gate that discards its own evidence

Three instances now, and it is worth naming because each cost a debugging cycle:

| | |
|---|---|
| `tail -12` | destroyed a `make check` run's per-gate output. Standing rule since: never pipe `make check` |
| `evalguard`'s `>/dev/null` | the gate failed printing only `Error 1`. The identical CI step runs `-v` and names every offending file — **a developer got a strictly worse report than CI from the same assertion.** Fixed |
| the coverage ratchet's `go test` without `-v` | a *passing* run prints no repaint measurement at all, so the one wall-clock number this repository argues about cannot be trended. **Recommended, not implemented** |

The third is the interesting one: the first two hide a failure, the third hides a
**measurement**. A wall-clock gate whose number is visible only when it breaches can
be argued about but not tracked.

---

# Third addendum, 2026-09-17 — two rules the closing sequence earned, and the index that makes all of them checkable

H11–H13 were written from work that was still in flight. These two come from work
that closed, and they are about the audit's own output rather than the code's: one
about proposing gates, one about describing anything by counting it. The index at the
end exists because **this document had itself begun to describe itself by counting** —
which is precisely the failure H15 names.

## H14 — A gate that needs exemptions at birth is miscalibrated

Exemptions granted before a gate has ever run are indistinguishable, in the
allowlist, from exemptions earned by real asymmetries later: both are a line with a
reason. **Measure coverage and false-positive rate before proposing a gate, and
report both even when the verdict is to reject.**

> *Instance:* a proposed gate — *no eval anchor may appear in a source comment* —
> designed in direct response to a real defect, where a comment added to
> `daemon/chunker.go` widened an anchor from one occurrence to two and broke the
> eval's ground truth. The gate would have caught that exact commit. It was measured
> before it was written, and rejected.

The measurements are the reusable part:

| | |
|---|---|
| Coverage | **5 of 101** eval anchors are qualified symbols; the remaining 96 are prose a comment-scanner cannot tell from prose. The gate would not have covered the defect class, only one member of it |
| False positives, unnarrowed | anchors that are ordinary English words fired on sentences mentioning neither retrieval nor the eval |
| False positives, narrowed to qualified symbols | **four pre-existing legitimate hits** in correct code, each needing an exemption before the gate had run once |

A gate needing four birth-defect exemptions is one the next person switches off, and
a switched-off gate is worse than no gate, because it still reads as protection in
the workflow list.

**The verdict is not the durable half — the numbers are.** A rejected design with
measurements tells the next person what to build instead. A rejected design without
them gets re-proposed by whoever next has the same idea, and the measurement is paid
for twice. What replaced it was not a cheaper gate but a *relocation*: the assertion
that already covered the whole class was lifted out of a twenty-two-minute host and
now runs in under a second on every push.

## H15 — List, do not count. A stated count is not a counted count.

A count in prose is a claim about an enumeration that lives somewhere else, and
nothing fails when the two disagree. **Enumerate the members. If a number must
appear, derive it from the enumeration in the same breath, so they cannot drift.**

> *Three instances, listed:*

| | |
|---|---|
| the delivery gap | prose said **213**; measuring it said **222**. The standing correction since is to name it as *everything between the canonical main and HEAD* and never as a number — the quantity was never what the finding was about |
| §13 of the 2026-09-13 checkpoint | prose said **31 rows**; the register holds **27** |
| this document | the H2 instance count drifted once, inside the same pass that wrote H2 |

The third is the one to sit with. H2's section opens "seven instances, all in one
pass", and the list beneath it is the authority — so when an eighth instrument was
caught lying, the sentence above it became false and **nothing could fail.** The
count and the list were two registers for one fact, which is technique 11's
violation in miniature, committed by the document that states technique 11.

## The rule index — every rule with its instance, listed and not counted

H15 applied to this document. This table is the authority on which rules exist; any
sentence elsewhere that totals them is derived from here and not the other way round.

| Rule | In one line | Its instance |
|---|---|---|
| H1 | No circular evidence | §13 of the 2026-09-13 checkpoint is a list of what was never verified; its presence in the repository evidenced the documentation, not the work |
| H2 | Anything you use to interpret evidence is untested until tested | a 7-hex regex matching a decimal, an exit status, a reference frame, a truncation, a gate's scope, a shell loop — and in this pass a restore check whose predicate was the whole tree, and a neuter arm defeated by a `+1` |
| H3 | Retract before you report | "32 dangling citations" was reported; the true answer was **zero** |
| H4 | A dissolved premise is a first-class result | "the 13 Extension-Development-Host suites have never run" — they run on every push, and the one touching the defect *asserted* it |
| H5 | Name the recipient | whether `daemon/CHUNK_SCRUB_DESIGN.md` is a decision record or a scoping note is the owner's call, not the audit's |
| H6 | Evidence at the resolution of the question | "macOS `cross` is green" does not answer "did macOS run the tests"; the job log does |
| H7 | Enumerate incumbents before claiming a name | `B<n>` already meant four things, so the boundaries became `TB<n>` — and `M5` is still two things |
| H8 | Destructive neuter arms run from a committed tree | an arm's `git checkout` discarded an uncommitted fix, twice; `git status --porcelain` must be empty first |
| H9 | A hedge is not a measurement | "three macOS jobs failing" was wrong three ways; the billing filter had been applied to the runs counted and not to the run reasoned from |
| H10 | Derive from a source of truth; do not enumerate | *"exclude `.codeterminal/` by name"* and *"grep `Handler: s.X`"* — both times the derivation was better, and both instructions were written by the auditor |
| H11 | A wall-clock gate runs alone, and the report says what else was running | a release-blocking repaint failure at p50 **71.675 ms** against 64 ms, measured while `make check` ran beside `gh` API calls |
| H12 | An undelivered artifact suppresses the checks that would have run on it | `evalguard` had been red since `21a0854` and nothing had run it, because the branch had not been pushed |
| H13 | A derivation that is right on some inputs is wrong on all of them | `stage-runtime`'s derivation was wrong for all three release targets and the *answer* wrong for one; and a known-answer test that asserted its own subject's defect would have gone red on the fix |
| H14 | A gate that needs exemptions at birth is miscalibrated | the anchor-in-comment gate: 5 of 101 anchors covered, four pre-existing legitimate hits, rejected on measurement before it shipped |
| H15 | List, do not count | 213 against 222, 31 against 27, and this document's own H2 count |

## What the closing sequence added to the taxonomy

Two shapes that were not visible until the pass tried to finish:

- **A check whose dependencies are a strict subset of its host's can be lifted out
  and run cheaply.** The eval's ground-truth assertion needed the chunker and neither
  the model nor the index, and was nevertheless reachable only behind twenty-two
  minutes of embedding in a path-filtered workflow. It hid a real defect for the
  length of a session because nobody had asked what it actually needed. **Ask that of
  every slow gate.**
- **An exemption is right when the quoted text *is* the finding; an elision is right
  when it is not.** These two had been collapsing into each other. The distinction is
  as sharp as any M5 pair, and it decides whether a document about a corpus defect
  becomes an instance of it.

## The M5 pair this chunk added: *green on a dispatch* ≠ *green on a schedule*

The pass's closing criterion is a **scheduled** run of `main` going green, and the
word is load-bearing. Seven consecutive scheduled runs are what has been red, every
Monday since 2026-08-03 under `0 6 * * 1`. A `workflow_dispatch` runs the same job
set — derived by reading each job's conditions, not assumed — and therefore answers
the question, but it does not make the *same observation*:

| | |
|---|---|
| a dispatch | runs now, from a ref chosen by hand, with the actor's own permissions |
| a schedule | runs unattended, from the default branch, on the identity the cron has — and is the thing that has been failing |

So the closure is recorded as **two states, not one**: *provisional* on the dispatch
result and *formal* on the scheduled run. Collapsing them would be the M5 failure in
its ordinary form — one phrase for two states — and here it has a specific cost. **A
green dispatch is exactly the condition under which the scheduled check gets
forgotten**, because the question feels answered. That is why the Monday check
carries a named recipient rather than a note, and why it is on the handover list and
not in a conclusion.
