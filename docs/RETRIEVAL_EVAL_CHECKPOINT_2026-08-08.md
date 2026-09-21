# Retrieval eval checkpoint — 2026-08-08

**Measured at commit `23550e4`** (the tip pushed to `origin/main` on 2026-08-08),
locally, offline, against the cached BGE-small-en-v1.5-int8 embedder and
onnxruntime 1.26.0.

## Why this exists

The `retrieval eval (scheduled)` job is gated to `schedule || workflow_dispatch`
in `.github/workflows/build.yml`, so the 30-job CI run for this commit reported
it as **skipped** — correctly, by design, not by failure. The next scheduled run
would have been **Monday 06:00 UTC**, four days after the code landed.

That gap was closed by running the suite by hand rather than waiting. Triggering
it through `workflow_dispatch` was rejected deliberately: only the eval job is
event-gated, so a dispatch re-runs the *entire* workflow — the full Go matrix,
both cross-compile platforms, the extension host and the Docker build — for a
second time, at roughly 60 billable Actions minutes, to obtain one job's result
that a local run produces for free.

**This is a measurement of a date.** Cite it with the date and the SHA attached.
It does not become false later; it becomes *older*.

## Result: the three gated tests — all PASS

Exactly the command CI runs (`-run` filter and `-timeout` included):

```
go test -tags eval -timeout 30m \
  -run 'TestEvalRetrievalQuality|TestRerankEvalRetrievalRanking|TestTokenEfficiencyEval' ./...
```

```
--- PASS: TestEvalRetrievalQuality        (1.51s)
--- PASS: TestRerankEvalRetrievalRanking  (128.78s)
--- PASS: TestTokenEfficiencyEval         (134.74s)
ok      mochiii/daemon               265.287s
```

| Test | Headline | Against |
|---|---|---|
| `TestEvalRetrievalQuality` | top-3 recall **15/15 = 1.00** | threshold 0.80 |
| `TestRerankEvalRetrievalRanking` | chunk-level recall **8/9**, semantic-only *and* hybrid | 1 known gap, ungated |
| `TestTokenEfficiencyEval` | **90.1%** token savings over the 13 full-hit queries | raw ratio 89.9% |

### Corpus the ranking eval indexed

`scanned=444 chunks=3660` at the repo root, self-referential chunks excluded.
That number is worth recording: it is what "the whole repository" costs per test,
and it is the reason this suite is weekly rather than per-push.

### Token efficiency, in full

20 queries: **13 full hit**, **4 partial hit**, **3 miss**. Savings are reported
only over the 13 full hits, because a miss has nothing meaningful to compare
against — the harness prints `savings: N/A (miss)` rather than scoring a miss as
a cheap answer, which would invert the metric.

### The 8/9, stated precisely

Both the semantic-only and the hybrid (RRF-fused) configurations score 8/9. The
single miss is query 1, *"where does the daemon open the unix socket"*, which the
harness itself labels:

> `KNOWN GAP (not gated, pre-existing, out of scope)`

It is not a regression from this commit and not a new finding. The harness also
reports, for 5 of the 9 queries, that the declared anchor text appears in files
*outside* `expectedFiles` — "harmless unless one of those is a better answer than
what is declared". That is the ground truth being honest about its own limits,
not a failure.

## The fourth test, which CI never runs

`TestEditShapedRetrievalEval` is excluded from the job's `-run` filter. The
reason recorded in the workflow is that its **harness cannot build its own
fixtures**: three of its four cases reconstruct a pre-fix tree by reverting the
fix commit, those commits are now 178–221 commits behind `HEAD`, and the reverts
conflict — so those cases never issue a query at all. The `0/4` it prints is a
setup failure wearing a recall label.

That diagnosis was verified rather than assumed, by running the excluded test.
**It is correct for three of the four cases and too broad for the fourth.**

```
--- FAIL: TestEditShapedRetrievalEval                        (133.04s)
    --- PASS: TestEditShapedRetrievalEval/helperproc-env-allowlist   (131.83s)
    --- FAIL: TestEditShapedRetrievalEval/editapply-apply-extraction (0.12s)
    --- FAIL: TestEditShapedRetrievalEval/tui-header-collision       (0.11s)
    --- FAIL: TestEditShapedRetrievalEval/zdr-refusal-phrasing       (0.16s)
```

The three sub-second failures are the setup failure exactly as documented. Each
refuses explicitly rather than limping on — the harness prints *"does not revert
cleanly … refusing to fake a pre-fix state"* and names the conflicted file:
`editapply/apply.go`, `clients/tui/chat.go`, `daemon/provider.go` respectively.
That refusal is correct behaviour; a harness that faked the pre-fix state would
produce a number that looked like retrieval and was not.

**The fourth case is different, and the record did not say so.**
`helperproc-env-allowlist` reverts cleanly, runs for **131.83s**, indexes the
pre-fix tree, and issues a real query. It **misses** — rank `#109` in the full
ordering, `#189` semantic-only, not in top-5 under the production-shaped k=5
pool-gated metric.

So `0/4` decomposes as **three cases that never ran plus one that ran and
genuinely failed**. The claim "the 0/4 is a setup failure wearing a recall label"
was true of the aggregate's *majority* and false of its only real data point,
and would lead a reader to treat the number as carrying no retrieval signal.
There is signal, it is a single sample, and it is a miss at depth. The workflow
comment has been corrected to say this.

Note also that the sub-test *passes* while its parent fails: the case does not
gate on its own recall, so a green sub-test here does not mean the query
succeeded.

The workflow comment also forecloses the obvious wrong fix, and it is repeated
here so nobody re-derives it: checking out the fix commit's parent was probed and
does not compile against today's tree, and restoring the whole parent tree would
measure retrieval over 200-commit-old code. **Reconstructing a historical bug
state in a moving tree expires by design.** The eval needs rebuilding around
current queries with labelled ground truth; the `-run` filter comes off then.

## What is and is not covered

**Covered, as of this date:** retrieval quality, rerank ranking, and token
efficiency at `23550e4`, all green.

**Not covered:** edit-shaped retrieval — one of the four eval tests is not
verified by anything, on any schedule. It is excluded because including it would
leave the job permanently red, and a job that is always red is a job nobody
reads. That is the right call for the job and the wrong state for the product,
and it stays visible here rather than being quietly absorbed.

**The one thing this checkpoint changes rather than confirms:** the single
edit-shaped case that still runs, `helperproc-env-allowlist`, is a **measured
retrieval miss at rank #109**, not a setup artefact. One sample is not a verdict
on edit-shaped retrieval, and it is not evidence of a regression from this
commit — but it is the only such measurement anyone has, it is bad, and it was
previously filed under "the harness is broken". Rebuilding this eval around
current queries with labelled ground truth is the open work; this is a data
point for why it matters, not a reason to panic.

**Cadence:** weekly, not per-push. A retrieval regression introduced today is
detected within seven days, not at the commit that caused it.
