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
