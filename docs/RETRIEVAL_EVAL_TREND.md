# Retrieval eval — the series

**One line per run of `TestRerankEvalRetrievalRanking`. Appended by hand.**

Every other number this project records is a point measurement of a date. This one
is deliberately a *series*, because the thing it tracks only exists as a slope.

## Why a slope

This eval indexes **this repository**. The corpus is the product, so the haystack
grows with every commit while `defaultContextBudgetChars` does not. Budget
pressure therefore rises monotonically whatever the ranker does, and a fixed
DELIVERED floor will keep meeting corpus growth — not as a hypothesis, but as
something already observed twice.

A single red run cannot distinguish "retrieval regressed" from "the repository
got bigger". Three gates were split apart on 2026-08-30 so that a red run names
its own cause; this file is the other half of that, and answers the question the
gates cannot: **how fast is it moving, and since when.**

## Why by hand

The eval prints an `EVALTREND` line on every run — grep a job log for it. It is
not appended here automatically, because a CI job that commits to its own
repository re-triggers itself, and in an eval whose corpus *is* that repository,
each such commit would also enlarge the haystack it is measuring. An instrument
that perturbs its own subject on every reading is worse than one somebody has to
copy a line into.

So: at a checkpoint, or after any run that moved a number, paste the line.

## The series

| date | commit | runner | chunks | files | semantic | retrieved | budgeted out | delivered | fingerprint | result |
|---|---|---|---|---|---|---|---|---|---|---|
| 2026-08-30 | `3ada6ea` | branch | 4757 | 558 | 35/49 | 34/49 | 1 | 41/49 | *not recorded* | **RED** — `semanticOnlyCount > hybridCount`, a gate with no slack |
| 2026-08-30 | `3ada6ea` | main | 4757 | 558 | 32/49 | 33/49 | 0 | 42/49 | *not recorded* | green |
| 2026-08-30 | `44a2ed9` | branch | 4771 | 560 | 31/49 | 33/49 | 1 | 39/49 | *not recorded* | green |
| 2026-08-30 | `44a2ed9` | main | 4771 | 560 | 31/49 | 33/49 | 1 | 39/49 | *not recorded* | green |
| 2026-09-21 | `c0f3a0d` | main | 6194 | 696 | 31/49 | 33/49 | 2 | 38/49 | `0f3838c92c6738c9` | green — first run after the `codeterminal` -> `mochiii` rename, run `35594537792` |
| 2026-10-02 | `fe0fd00` | branch | 7664 | 1056 | 27/49 | 30/49 | 2 | 35/49 | `164ac48ac3fcdabd` | **RED** — run `37032917525`; see below |
| 2026-10-02 | `fe0fd00` | local (i5, 16 threads) | 7664 | 1056 | 26/49 | 30/49 | 1 | 37/49 | `f1709c05a37bad71` | green — the same tree, exported from git; see below |
| 2026-10-02 | `61d445d` | branch | 7665 | 1056 | 27/49 | 31/49 | 2 | 37/49 | `f1f421e0ced8d632` | green — run `37039395233`; **36/49 before ground-truth corrections**, so a pass on the floor, not above it |

Floors in force across all four rows: DELIVERED ≥ 75% (36.75/49), RETRIEVED ≥ 61%
(29.9/49), budgeted out ≤ 4.

### What these four rows already show

**The two `3ada6ea` rows are the same commit and the same corpus**, byte for byte,
and they are three queries apart on semantic-only. 308 of 400 compared per-chunk
score lines differed in the third and fourth decimal. That is the machine, not the
code — and it is why the fingerprint column exists from here on. The four seeded
rows say *not recorded* because they predate it; that is a gap in the record, not
a value of zero.

**The two `44a2ed9` rows are identical to each other** — 995 of 995 score lines
agreed. So the divergence is **intermittent**, and a green pair is not evidence
that it has gone away.

**Delivered fell 41–42 → 39 between the two commits**, on a corpus that grew 0.3%
(4757 → 4771 chunks) because that commit added two test files. Retrieved
*improved* over the same step, 33 from 32. That is the gate split working exactly
as designed: the ranker got no worse and the outcome did, because the budget is
fixed and the haystack is not.

**Headroom at the last row is two queries.** 39 against a floor of 36.75, with a
measured sensitivity of roughly 2–3 queries per 0.3% of corpus. Another commit of
that size meets the floor.

## Reading a new row

- **Delivered down, retrieved flat or up, budgeted-out up** → corpus growth. The
  remedies, in order: pack the budget better, narrow expansion, or raise
  `defaultContextBudgetChars` and price the CI cost. Raising the floor is not one
  of them.
- **Retrieved down** → a ranking or indexing change. The budget cannot move this
  number.
- **Numbers moved, fingerprint changed, corpus identical** → the runner. Check the
  `Record what machine this ran on` step in the same job before touching anything.
- **Fingerprint identical, numbers moved** → real. Something in the code did it.

### 2026-09-21 — the rename did not move retrieval; the corpus did

`semantic=31` and `retrieved=33` are **identical** to the `44a2ed9` baseline,
digit for digit, across a change that renamed the vector collection
(`codeterminal-chunks` -> `mochiii-chunks`) and the model cache path. Retrieval
is untouched.

`delivered` fell 39 -> 38 and `budgeted_out` rose 1 -> 2, on a corpus that grew
**4771 -> 6194 chunks and 560 -> 696 files** in the three weeks since that
baseline. The loss is at the BUDGET stage, not the retrieval stage: one more
chunk was squeezed out of the same 32,000-char budget by a bigger corpus.

Worth naming because it is a cost nobody bills: **documentation dilutes
retrieval.** Some of those 1,423 new chunks are the readiness report and the
findings written the same day. Each document added to this repository competes
with code for the prompt budget, and this table is where that shows up.

### 2026-10-02 — the headroom is gone, and the runner now decides

**The same commit, the same corpus (7664 chunks, 1056 files), two machines, two
verdicts**: 35/49 on the CI runner, 37/49 here, fingerprints `164ac48a` and
`f1709c05`. The `3ada6ea` rows already showed the machine moving numbers by a few
queries; what is new is that the floor (36.75) now sits between the two, so a red
or green run on this corpus says more about the runner than the code.

**What took the headroom**: the corpus grew **6194 -> 7664 chunks, 696 -> 1056
files** (+24%) in eleven days — the long-task engine, its tests and fixtures, and
the documents written alongside — and **retrieval**, not the budget, absorbed it:
semantic 31 -> 26-27, retrieved 33 -> 30, budgeted out unchanged at 1-2. The
queries lost (q13, q22, q25, q30, q45) are each answered by a file this branch
grew, whose answer chunk now competes with more of its own neighbours
(`agentloop.go`, `mcpbuiltin.go`, `orchestrator.go`) or by new code on the same
subject.

**The delivery sweep on the local index** (`TestDeliveryPolicySweep`, same tree):
the shipped policy is the best arm at 37/49 and 29,837 mean chars, and **no budget
helps** — 36,000 chars also delivers 37. Three cheaper arms tie it (`+-1 top5` at
28,000: 37/49 on 23,188 chars, 22% fewer), which is worth a measured look and not
a switch: a tie inside the runner's noise is not evidence.

**One correction, made by the rule in `supersededGroundTruth`**: q40 ("how does
the daemon count tokens it has spent in a turn") declared `counters.go`, which has
never counted tokens — the word appears nowhere in it. The per-turn tally is
`usage.go`'s `usageTally`, added with `/usage`. The pre-correction number keeps
being printed.

**Not done, deliberately**: moving the floor. Restoring the headroom means making
retrieval rank the answer chunk above its own file's neighbours again, which is
retrieval work with its own measurement, not a change to the gate.

