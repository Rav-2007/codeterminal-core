# Why retrieval's negative-savings cases are not worth fixing (P1 efficiency follow-up — CLOSED)

Status: **decision record, not a design-to-implement.** The negative-savings
cases found by `daemon/token_efficiency_eval_test.go` (committed, `899a8a9`)
were investigated with real data, one candidate fix was actually built and
measured, and the conclusion is to **ship nothing** and leave retrieval's
hot path unchanged. This document is the rationale for that decision, kept
so the investigation isn't repeated from scratch later.

## 0. Conclusion (up front)

**All viable mechanisms were evaluated against real per-query data and
rejected.** Three of the four are unsafe; the fourth is safe but doesn't
earn its keep:

1. **Relevance-score cutoff** — unsafe/unsound: the fused RRF score is
   rank-based and has no knee to threshold on (§2A).
2. **Raw-cosine cutoff** — unsafe: blind to the lexical retrieval tier,
   would drop exactly the hybrid wins the system was built for (§2B).
3. **Size-cap / collapse-to-dominant-file** — unsafe: provably regresses a
   winner. The poison-pill case is concrete and documented in §1(c) and
   §2C — "why does the proxy reserve tokens" has its **top-1 chunk in the
   small `reserve_usage.sql`** while its correct answer is the **large
   `proxy/main.go`**, so any cap keyed on the top file's size drops main.go
   and turns a **+78% win into a MISS**.
4. **Same-file chunk consolidation** — the one provably-safe lever. It was
   **implemented and measured** (then reverted), not just theorized: on the
   20-query benchmark it **fired on 1/20 queries, saved 596 chars on that
   one query, flipped none of the three negative cases, and improved the
   full-hit headline by 0.2 points (70.0% → 70.2%)**. Provably zero
   regressions — and provably not worth the permanent complexity it adds to
   the retrieval hot path (a per-request file read + regrouping) for that
   payoff. See §2D / §3.

**"Safe" was never the bar; "worth it" is.** The one mechanism that clears
the safety bar doesn't clear the value bar.

**Mitigating context that makes this an easy call:** the queries where
retrieval *loses* to naive inclusion are exactly the *small, single-file
answer* queries (`router.go` at 44 lines, `scrub.go` at 79,
`editblock.go` at ~90) — i.e. the cases that were never hard to answer in
the first place and where injecting a few extra KB of adjacent context is
cheap in absolute terms. Retrieval's large wins (77–89% savings) are
concentrated exactly where they matter: large files and cross-file
questions. Optimizing the cheap cases at the cost of risking the expensive
ones is backwards.

## 1. What was measured (the thing not being fixed)

`daemon/token_efficiency_eval_test.go` measured the real retrieval path
against a naive whole-file baseline over 20 queries: **67.4% average token
savings on the 17/20 full-hit queries**, but **3/20 queries had NEGATIVE
savings** — retrieval cost *more* tokens than just including the whole
answer file:

| # | query | expected file | naive chars | retrieval chars | savings |
|---|-------|---------------|-------------|-----------------|---------|
| 2 | how are edit blocks parsed | `editapply/editblock.go` (3378) | 3378 | 6723 | **−99.0%** |
| 3 | where is model tier routing decided | `daemon/router.go` (3191) | 3191 | 7287 | **−128.4%** |
| 15 | where is outbound secret scrubbing implemented | `daemon/scrub.go` (3504) | 3504 | 7419 | **−111.7%** |

Root cause (confirmed): injected context size is **roughly fixed**
(~6.5–8k chars — `topK=5` × ~40-line chunks nearly fills the 8000-char
budget) regardless of query, while the naive baseline scales with the
answer file's size. When the honest answer is one *small* file, the fixed
budget overshoots it. This is structural — a property of fixed `topK` +
fixed budget vs. a variable baseline — not a bug in any single component.

## 2. Per-chunk data gathered before deciding (not guessed)

A throwaway instrumented run dumped every kept chunk's file, size, fused
score, weighted score, and raw cosine for all 20 queries. Three facts from
that data drive — and rule out — the candidate mechanisms.

**(a) The fused RRF score has no knee.** Across all queries the fused
`RawScore` sits in `0.0137–0.0164` and the weighted `Score` in
`0.0158–0.0189`, declining by ~0.0003 per rank with no sharp drop anywhere.
This is structural, not incidental: RRF is **rank-based**
(`max(1/(60+rank_sem), 1/(60+rank_lex))`, see `fuseRRF` in `rerank.go`), so
consecutive ranks differ by `1/(60+r) − 1/(61+r)` ≈ 2.6e−4. There is no
magnitude signal to threshold on.

**(b) Raw cosine is blind to the lexical tier.** Many chunks that rank in
the top 4 show `cos=0.0000` — they were found by the FTS5/lexical tier and
never appeared in the semantic pool at all (e.g. query 2's #2
`daemon/server.go:541-580`, query 15's #1 `daemon/config.go:1-40`). A
cosine cutoff would blind-drop exactly the lexical-only hits that hybrid
retrieval exists to recover.

**(c) There is a poison-pill winner for any "dominant small file"
heuristic.** Query 8 ("why does the proxy reserve tokens before
forwarding…", a **+77.8% winner**) has as its **top-1** chunk
`proxy/migrations/0001_reserve_usage.sql` (a *small* 2742-char file), while
its pre-registered ground-truth answer is the **large** `proxy/main.go`
(35914 chars), which supplies chunks #2–#5. Any mechanism that caps total
context at the top-1 file's size, or collapses to the top-1 file, **drops
`proxy/main.go` and turns a +77.8% full-hit into a MISS.** Query 19 ("what
happens when a quota reservation would exceed the token limit") has the
same shape (top-1 `.sql`, answer spans `.sql` + `main.go`). This is the
single most important finding in this document: it is why the entire
"detect a small answer and cap the budget to it" family is unshippable.

### A. Relevance-score cutoff on the fused/weighted score — REJECTED (unsound)
Ruled out by §2(a): there is no knee in a rank-based score. A "score
dropped sharply" test would fire on rank gaps and class-weight boundaries
(code 1.15 vs doc 0.75), i.e. on *class*, not *relevance*. Not implementable
soundly on this architecture.

### B. Raw-cosine cutoff — REJECTED (unsafe)
Ruled out by §2(b): blind to lexical-only hits, which routinely rank in the
kept top-4. Would regress the exact hybrid-retrieval wins the RRF fusion was
built to deliver.

### C. Collapse-to-dominant-file / cap-at-top-1-file-size — REJECTED (unsafe)
Ruled out by §2(c): the `reserve_usage.sql`-top-1 winners (queries 8, 19)
prove that "top-1 is a small file" does **not** imply "the answer is
contained in that small file." Any cap keyed on the top file's size drops
the large co-answer file and regresses a winner from +78% to MISS. The
whole family is out.

### D. Same-file chunk consolidation — SAFE, BUILT, MEASURED, then REJECTED (not worth it)
The only mechanism that clears the safety bar. When the kept result
contains **≥2 chunks from the same file** and that file's **whole content
renders no larger than the chunks it would replace**, inject the whole file
**once** instead of the several overlapping chunks. By construction it never
drops a distinct file (can't change coverage) and never increases size
(gated on `wholeFile ≤ Σ replaced chunks`), so it is provably safe for
every winner. Applied as a post-`truncateToBudget` step in `gatherContext`;
every non-triggering case is byte-for-byte today's behavior.

It was implemented (`consolidateSmallFiles`), unit-tested (6 offline
cases), and run on the real 20-query benchmark. The measured result is why
it's rejected — see §3.

## 3. Why the safe mechanism (D) still isn't worth shipping — measured, not guessed

Implemented and benchmarked on the real repo (before/after, same corpus,
same 20 queries, same ground truth):

- **Fired on 1 of 20 queries** (query 19, "quota reservation exceeds
  limit"): two `reserve_usage.sql` chunks collapsed to the whole file,
  saving **596 chars**, improving that query 81.8% → 83.3%, still a full
  hit.
- **Flipped none of the three negative cases.** Query 2 (edit blocks): the
  small answer file contributes only **one** chunk; the padding is four
  *distinct* files, which D (correctly) won't drop. Query 15 (scrub): the
  answer file ranks #4; the top chunks are other files. Query 3 (routing):
  in the benchmarked corpus it returned only one `router.go` chunk, so D
  didn't fire; even when it does fire (a corpus where `router.go` returns
  ≥2 chunks) it only trims that file's internal overlap and leaves the
  `config.go`/`server.go` padding, so it stays negative.
- **Full-hit headline: 70.0% → 70.2%.** Zero regressions; coverage
  identical on all 20 (guaranteed by construction, verified by an assertion
  in the harness).

So the safe mechanism buys ~0.2 headline points and fixes zero of the three
cases it was aimed at, in exchange for **permanent added logic on the
retrieval hot path**: a synchronous file read per consolidatable file per
request, plus a regrouping pass, plus a whole-file re-render — on the path
that runs for every single prompt. That is a bad trade. The complexity is
real and forever; the benefit is marginal and, for the target cases, zero.

**Note on corpus sensitivity (a second reason to not ship D):** the
benchmark indexes the live working tree, so which queries even *exhibit*
the redundant-same-file pattern shifts as the repo changes (836 → 853
chunks between runs moved query 3 from 3 `router.go` chunks to 1). A fix
whose already-marginal payoff is itself a function of transient corpus
shape is not a fix worth maintaining.

## 4. The aggressive variant, for completeness — REJECTED (regresses winners)
A "cap total context at `max(naive answer size, one-chunk floor)` when the
top-2 chunks agree on a single small file" would flip queries 2/3 fully
positive — but it regresses queries 8 and 19 (the `.sql`-top-1 winners)
from +78%/+81% to MISS, per §2(c). Net over the 20: it trades 3 modest
loser-fixes for 2 catastrophic winner-losses. Recorded here so the tradeoff
is visible, not silently omitted. This is the mechanism someone would reach
for first; it is exactly the one the poison-pill case rules out.

## 5. What would reopen this
Not a permanent "never," just "not now, not like this." A future change
that would justify revisiting:
- **Chunk-level content classification** (not the current per-file
  classification): if retrieval could tell "the doc-comment chunk that
  paraphrases the query" from "the implementation chunk that answers it,"
  the fixed-budget padding on small-answer queries could be trimmed without
  the blind size-cap that the poison-pill rules out.
- **A real tokenizer in the daemon** (currently deliberately absent, see
  `config.go`'s char-budget comment): would let the budget be token-exact
  rather than char-approximate, though this doesn't by itself fix the
  fixed-vs-variable mismatch.
- **A materially different file-size distribution** in real customer
  workspaces than this repo's: if real usage skews to many tiny
  single-file answers, the negative-savings cases stop being the cheap
  cases and the calculus changes. Measure before assuming.

Absent one of those, the negative-savings cases stay as documented: real,
understood, structural, and deliberately not fixed.
