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
| 2026-10-03 | `6859e3b` | local (i5, 16 threads) | 7665 | 1057 | 26/49 | 31/49 | 2 | 37/49 | `334634b76584a870` | green — the pre-registered baseline; held-out set 21 retrieved / **23/28** delivered |
| 2026-10-03 | `0d0ca5b` | local | 7701 | 1061 | 28/49 | 31/49 | 2 | 36/49 | `643086c59da2e912` | **RED, and not used** — a comment in `lexicalstore.go` quoted locate q29 verbatim (caught by evalguard, fixed in `2c5cbda`) |
| 2026-10-03 | `2c5cbda` | local | 7702 | 1061 | 27/49 | 31/49 | 2 | 36/49 | `1652bf796196b224` | **RED** at 73.5% — old defaults on a corpus the round's own code had grown; the decision sweep ran on this build (below); held-out 22 / 23 |
| 2026-10-03 | `3e74e4a` | local | 7718 | 1062 | 26/49 | 32/49 | **0** | **41/49** | `7afa9f886f31a9b4` | green — path column at weight 8 + two-pass packing; 38/49 before corrections; held-out 21 / **24/28** |
| 2026-10-03 | `9bae0ef` | branch | 7718 | 1062 | 27/49 | 32/49 | **0** | **41/49** | `44feda9b9902d224` | green — run `37105002648`; the same 41, 32 and 0 as the local run on different vectors, where the same code once split 35 against 37 across the floor; held-out 21 / **24/28** |
| 2026-10-04 | `f89e8d2` | local (i5, 16 threads) | 7725 | 1065 | 27/49 | 32/49 | **0** | **41/49** | `97e52a8f30ca6a2b` | green — the outside-repository round's baseline; held-out 21 / **24/28**; outside repositories 32/40 + 14/20 (below) |
| 2026-10-04 | `278bd47` | local | 7751 | 1067 | 26/49 | 33/49 | **0** | **42/49** | `e7b516ab5930bc71` | green — the round's three levers built and OFF; +1 is the round's own code growing the corpus; the sweep ran on this build (below); held-out 20 / **24/28** |

Floors in force across every row: DELIVERED ≥ 75% (36.75/49), RETRIEVED ≥ 61%
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


## Pre-registered 2026-10-03: the keyword-tier round

Written before any run of what it describes, so the result can be checked
against it rather than explained after it.

**The diagnosis it rests on.** In the three saved runs, the fused top ten
splits 255 slots from the meaning tier and 235 from the keyword tier, and a
keyword slot holds an answer chunk 6% of the time against 10%. The keyword query
ORs raw substrings of every non-stopword word: the file path is not searchable
(the meaning tier embeds it, and that was worth seven of its queries), nothing
handles inflection ("iterating" never meets `Iterations`), and only 31 words are
stopped. Separately, delivery widens hits to their declarations in rank order
before the budget applies, so junk at ranks 1-3 can spend the budget a
retrieved answer needed: q45 is retrieved at rank 4 and dropped.

**The levers.** (1) The file path as a searchable column of the keyword index,
weighted in BM25. (2) A query builder with general-English filler stopwords and
light suffix stripping of plain lowercase words, leaving identifiers alone.
(3) Delivery that places every retrieved hit before widening any of them.

**A second query set.** 28 questions in `daemon/heldout_eval_test.go`, written
before any of the above was run against them, about code none of the 49 ask
about, held out of the index, quoted nowhere else, and never used to choose. The
eval prints them as an `EVALHELDOUT` line beside `EVALTREND`.

**The rule.** An arm ships only if DELIVERED on the 49 rises by at least 2 over
the shipped row, DELIVERED on the held-out set does not fall, no shape of the 49
falls by more than 1, it was measured inside one index build, and each lever in a
combination does no harm on either set by itself.

**The prediction.** An offline replica of the retrieval stage (it agrees with
the real runs on 48 of 49 queries, and 49 of 49 on the older tree) puts
RETRIEVED at 31 -> 35 on today's corpus for levers (1)+(2) at path weight 4,
gaining q13, q18, q22 and q29 and losing none, on both runners' vectors. On
the 2026-09-21 corpus the same arm is +3 -2. Delivery is not replicated, so
lever (3) carries no prediction beyond its mechanism: "budgeted out" should
reach zero.

### 2026-10-03 — the keyword-tier round: what the rule decided

**The decision run** (`TestKeywordAndPackingSweep` on `2c5cbda`, one index build,
both query sets), against the shipped row of 36/49 delivered and 23/28 held-out:

| arm | retrieved | DELIVERED | held-out | lost |
|---|---|---|---|---|
| path weight 8 | 34/49 | **42/49** | **24/28** | none, on either set |
| path weight 8, two-pass | 34/49 | 42/49 | 24/28 | none |
| path weight 4 | 34/49 | 41/49 | 24/28 | none |
| two-pass alone | 31/49 | 38/49 | 24/28 | none |
| path 4 + filler + stem | 35/49 | 40/49 | 23/28 | q25, a held-out answer |

**The prediction held where it was made, and the rule overruled it anyway.** The
offline replica put levers (1)+(2) at 35 retrieved, and the run measured 35. But
the rule gates on DELIVERED, and the arm that won retrieval lost delivery: stemming
and filler words, added to the path column, cost q25 on the 49 and an answer on the
held-out set. Retrieval is not what reaches the model. They stay in the code,
switched off, so a later sweep can ask again.

**Shipped (`3e74e4a`): the path column at weight 8, and two-pass packing.** Two-pass
ties path 8 alone on both sets. It ships for the property the pre-registration
predicted: a retrieved hit can no longer be lost to the budget, and "budgeted out"
went 2 -> 0 on the final tree.

**On the final tree** the gated number is 41/49 against a floor of 36.75: four
queries of headroom where there were none. The held-out set went 23 -> 24 and lost
nothing. The decision run predicted 42; the one-query gap is corpus drift from the
commits after it, which is the reason the decision was taken inside one build. The
floor stays where it is.

**Two notes on the instrument.** The `0d0ca5b` row carried an answer-key leak, and
its numbers are not used. The final run's sweep also labelled its reference row
"one-pass" after the default had become two-pass. That was fixed in the next commit,
and the decision run predates the change.

## Pre-registered 2026-10-04: outside repositories

Written before any model run against them, for the same reason as the section
above.

**Why.** Every number above is this repository, in Go, scored by the eval it
tunes. Three choices in the pipeline are visibly Go-shaped and invisible to that
eval: test files are recognised only as `_test.go`; the setup-file down-weight
names only Go files; and constructs are found at column zero, where Go puts its
methods and Java, Python, Rust and TypeScript do not -- so a hit inside a Java or
Python class widens to ±1 neighbour, not to its method.

**The instrument.** `TestExternalRepoRetrieval` runs the production pipeline
over four public repositories, pinned in `daemon/testdata/evalrepos/repos.txt`
and fetched by `scripts/fetch-eval-repos.sh` (never committed): pallets/flask
(Python, `d73fa1c`), honojs/hono (TypeScript, `08a023c`), BurntSushi/ripgrep
(Rust, `3fce3b5`), google/gson (Java, `854c825`) -- 9,439 chunks. Fifteen
questions each, chunk-level anchors resolved by `TestExternalEvalGroundTruth`;
every third question by position is held out (40 tuning, 20 held-out). Printed
as `EVALEXTERNAL` lines. Not gated, not in CI.

**The levers.** Each a policy field, off until the rule passes. (L1) Test files
beyond Go, recognised from the path at ranking time: `test_*.py`, `*_test.py`,
`*.test.*` and `*.spec.*` for ts/tsx/js/jsx, `*Test.java`, `*Tests.java`, and
paths under `tests/`, `test/`, `__tests__/`, `src/test/`. (L2) When the
enclosing top-level construct is over the cap, widen to the innermost enclosing
function or method found by indentation, counting declaration lines only, before
falling back to ±1. (L3) The setup-file down-weight for `main`/`setup`/
`config`/`init` with any code extension. Any lever the baseline's misses point
to is added here, before the sweep runs.

**The rule.** An arm ships only if, inside one process (one build per corpus):
outside tuning DELIVERED rises by at least 3 of 40 over the shipped row; outside
held-out DELIVERED does not fall; in-repo DELIVERED on the 49 and on the 28 does
not fall; no outside repository falls by more than 1; and each lever does no
harm by itself on any of the four sets.

**The baseline, and what it adds to the menu** (`f89e8d2`, recorded before the
sweep ran). Outside repositories at shipped defaults: DELIVERED 32/40 tuning and
14/20 held-out (46/60, 77% -- this repository's own is 84%); RETRIEVED 25/40 and
12/20. Per repository, tuning+held-out: flask 13, hono 12, ripgrep 10, gson 11 of
15. Retrieval took 14-40 ms at the median, 20-91 ms at p95. Of the 14 misses, none
was budgeted out; 6 never had the answer's file in the top ten; 8 had the right
file and the wrong part of it. Test files crowd the top ten in 5 of them (hono,
gson), all named by conventions only L1 recognises. Nothing points past L1-L3, so
the menu stands as written. Two findings that are not levers: flask's `uv.lock`
is indexed (a lockfile the noise list does not name), and `.mts` is not a code
extension.

### 2026-10-04 — outside repositories: what the rule decided

`TestExternalLeverSweep` on `278bd47`, one build per corpus, DELIVERED on all
four sets. The shipped row reproduces `EVALTREND` (42), `EVALHELDOUT` (24) and
`EVALEXTERNAL` (32 and 14) from the same process.

| arm | in-repo 49 | in-repo 28 | outside 40 | outside 20 | per repository, of 15 | rule |
|---|---|---|---|---|---|---|
| SHIPPED | 42 | 24 | 32 | 14 | flask 13, hono 12, ripgrep 10, gson 11 | — |
| L1 tests beyond Go | 42 | 24 | **34** | **16** | flask 14, gson 14 | fails: +2 where +3 was asked |
| L2 widen to the method | 41 | 24 | 32 | 16 | flask 14, gson 12 | fails: in-repo −1 (q22) |
| L3 setup files beyond Go | 42 | 24 | 32 | 14 | unchanged | inert |
| L1+L2 | 41 | 24 | 34 | 17 | flask 15, gson 14 | fails |
| L1+L3 | 42 | 24 | 34 | 16 | flask 14, gson 14 | fails |
| L2+L3 | 41 | 24 | 32 | 16 | flask 14, gson 12 | fails |
| L1+L2+L3 | 41 | 24 | 34 | 17 | flask 15, gson 14 | fails |

**Nothing ships.** The levers stay in the code, off, with these numbers at
their definitions.

**L1 is the near miss.** Four gains -- a Flask held-out question and three Gson
questions (two tuning, one held-out) -- each one a case where Python or Java test
files had been holding top-ten slots over the code they test. It loses nothing on
any of the 137 questions. The bar was +3 on the outside tuning set and it reached
+2; the bar was written before the run, so +2 is a fail, and the honest next step
is fresh questions, not a lower bar.

**L2's in-repo loss is a budget trade inside Go.** Locate q22 is delivered only
by widening its rank-9 hit. A rank-6 hit sits in a 329-line Go function, just over
the 300-line cap, and L2 now widens it to a 132-line inner block first, spending
what q22 needed. Go's methods are already at column zero, so L2 limited to the
class-based languages is the obvious variant -- and, chosen after this run, it
must be pre-registered and measured on fresh questions before it can count.

**L3 changes nothing anywhere.** Not a tie-break moved. A candidate for deletion.

**hono and ripgrep are untouched by every lever.** Their eight misses are all
ranking misses: the answer's chunk is not in the top ten, and in four of them its
file is not either. In the other four the file is there but the hit sits in a
different function from the answer, and widening only ever reaches the construct
around a hit. No class weight or widening reaches these; a reranker might, and
this instrument is what would show it.

