# Latency baseline — measured 2026-07-31

Stage A of the performance phase. Everything here was **measured on this tree**, on
the commit named below, not inherited from the 2026-07-17 TTFT note. It runs under the
same standing rule as the robustness program: **a finding that fails to reproduce gets
struck rather than fixed on faith.** One of this stage's own hypotheses was struck, and
is recorded below rather than quietly dropped.

## Why this document exists

The only end-to-end latency figure this project owned was measured **2026-07-17**
(recorded in [`ARCHIVE/BACKLOG_2026-07.md`](ARCHIVE/BACKLOG_2026-07.md), P2 caching investigation). Since then **29 commits landed in `proxy/` and 57 in `daemon/`** —
a window in which Phases 1–4 *added* work to the hot path: request IDs, structured
logging, metrics, rate limiting, extra gates, and per-chunk secret scrubbing. Nobody
had re-measured, there were **zero Go benchmarks in 49k lines**, and the only timing
instrument was a single whole-request `latency_ms`
([proxy/logging.go:238](../proxy/logging.go#L238)) — so a regression in any one stage
was invisible by construction.

The 2026-07-17 figures, which this document is scored against:

| Stage | Warm median | Whose |
|---|---:|---|
| Model prefill / routing | ~1,125 ms (**78%**) | the provider's |
| Railway proxy hop | ~100–180 ms | ours — geography |
| `reserveQuota` | ~80–100 ms | ours — ~80 ms unexplained |
| Retrieval (embed + search + rerank) | ~14 ms | ours |
| **Total, keystroke → first content token** | **~1,450 ms warm / ~1,900 ms cold** | |

## A.1 — Retrieval hot-path CPU

**Harness:** [daemon/bench_test.go](../daemon/bench_test.go), Go benchmarks sized to a
real turn rather than a round number — `chunkLines` 40, `defaultK` 5,
`defaultContextBudgetChars` 8000. The corpus chunk deliberately carries the token
shapes the fire-rate report found dominating real retrieval traffic (a git SHA, a
UUID, a base64 blob, a credential-named assignment), because a prose corpus would skip
the detectors' inner loops and under-report their cost.

**Machine:** linux/amd64, 13th Gen Intel Core i5-1340P, `-benchtime 2s -count 5`.
Medians of 5.

| Benchmark | Median | Allocs | Note |
|---|---:|---:|---|
| `scrub` (1 chunk) | **2.3 µs** | 0 | the live structural redaction |
| `detectKeywordSecrets` (1 chunk) | **35.4 µs** | 0 | log-only |
| `detectHighEntropy` (1 chunk) | **40.6 µs** | 11 | log-only |
| `detectWarnModeSecrets` (1 chunk) | **69.4 µs** | 12 | the two above, together |
| `renderChunk` (1 chunk) | 9.3 µs | 9 | scrub + delimiter-neutralize + format |
| `buildAugmentedUserMessage` (5 chunks) | 49.0 µs | 50 | whole prompt assembly |
| `chunkContent` (8 windows) | 31.9 µs | 45 | index-time, not per-turn |
| `rerankChunks` | 15.5 µs | 7 | |
| `fuseRRF` | 11.5 µs | 38 | |
| `looksTestSeeking` | 3.3 µs | 2 | debt (b)'s twice-regressed classifier |

### Finding — the retrieval CPU path is not a latency lever, at all

Summing the per-turn work (prompt assembly + rerank + fusion + query classification +
`logChunkScrub`'s five-chunk scrub-and-detect):

**~0.44 ms per turn — about 0.03% of the 1,450 ms TTFT.**

Everything Phases 1–4 added to this path is, together, under half a millisecond. The
suspicion that motivated this stage — that four phases of hardening had quietly taxed
the hot path — **does not survive measurement.** No retrieval-CPU optimisation in this
program can produce a user-perceptible change, and B.3 should be struck as a latency
item on that basis.

**What this does NOT measure, stated so the number is not over-read:** these are pure
CPU benchmarks. They do not cover the ONNX embedder call (~5.4 ms warm per query, ~355 ms
once at daemon boot) or the SQLite vector search — which is where the original 14 ms
actually lived. This stage priced the *added* work and found it negligible; it did not
re-measure the 14 ms itself.

### Hypothesis STRUCK — the duplicate `scrub()` is real but its removal is unmeasurable

Reading the code found `scrub()` running **twice per chunk on every turn**: once in
`renderChunk` ([daemon/context.go:201](../daemon/context.go#L201)) to build the outbound
prompt, and again in `logChunkScrub`
([daemon/context.go:256](../daemon/context.go#L256)) to measure the same budget-kept
set. Same input, same nine regexes, same result, computed twice. It looked like a free
win.

Benchmarked head-to-head over a full 5-chunk turn:

| Variant | Median | Range over 5 runs |
|---|---:|---|
| as shipped (double scrub) | **417 µs** | 394–463 µs |
| single scrub, shared | **419 µs** | 403–436 µs |

The "optimised" variant measured **2 µs slower**, and the two ranges overlap almost
completely. The duplication is genuine, but `scrub` costs 2.3 µs against a turn of
~420 µs — **it is 0.5% of a path that is itself 0.03% of TTFT**, which is to say it is
noise inside noise. **Struck as a latency item.** If it is ever removed it should be
for clarity, on a maintainability ticket, and must not be reported as a performance fix.

### Incidental finding — warn-mode dominates the scrub path, and it is log-only

`detectWarnModeSecrets` is **69.4 µs** against `scrub`'s **2.3 µs** — the log-only
detectors cost **~30× the live redaction they supplement**, and account for ~97% of the
scrub path's per-turn cost. At 0.35 ms per turn this is not a latency argument for
changing anything.

It is, however, a cost datum for the still-open Design B/C decision, and it points the
same way the precision data did: `docs/CHUNK_SCRUB_FIRE_RATE.md` measured entropy
firing on 33% of real chunks with zero true positives, and this says that firing costs
30× what the detector it supplements costs. Cheap enough to keep running for
measurement; one more reason not to promote it to redacting. **The decision remains the
founder's** — this only adds a price tag to it.

---

## A.2 — The proxy can now say where its time goes

`accessLog` emitted one `latency_ms`. Four stages now record their own duration on
that same line, under the same `req_id`: `auth_ms`, `reserve_ms`, `upstream_ms` (to
provider response headers) and `ttfb_ms` (to the first SSE data chunk reaching the
client). See [proxy/stagetimer.go](../proxy/stagetimer.go).

Two properties are pinned by tests that fail when the property is reversed: an absent
stage is **absent, never zero** (a 401 logging `reserve_ms=0` would read as "the
reservation was instant"), and `ttfb_ms` is the **first** chunk, recorded once.

## A.3 — Per-stage measurement against the real proxy binary

**Harness:** [scripts/latency-bench.sh](../scripts/latency-bench.sh), driving the
**real proxy binary** against the tracked [proxy/testharness](../proxy/testharness).
A new `-supabase-delay` flag on the harness imposes a per-call latency on the fake
Supabase, because the interesting cost is a round trip and a loopback benchmark reports
it as free.

Two modes, and reading only one of them is a mistake.

### Mode 1 — proxy overhead only (loopback Supabase)

| stage | median | p90 | max |
|---|---:|---:|---:|
| `auth_ms` | **0** | 0 | 1 |
| `reserve_ms` | **0** | 0 | 8 |
| `upstream_ms` | 0 | 0 | 0 |
| `ttfb_ms` | 0 | 0 | 0 |
| `latency_ms` | 46 | 47 | 55 |

n=30. **The proxy's own CPU cost for both money-path gates is sub-millisecond.**
Whatever those stages cost in production, essentially none of it is this code. The
46 ms `latency_ms` is the fake stream's own duration (8 chunks × 5 ms), not overhead.

### Mode 2 — production-shaped (90 ms imposed per Supabase call)

| stage | median | p90 | max |
|---|---:|---:|---:|
| `auth_ms` | **91** | 91 | 91 |
| `reserve_ms` | **91** | 91 | 92 |
| `upstream_ms` | 0 | 0 | 1 |
| `ttfb_ms` | 0 | 0 | 0 |
| `latency_ms` | **228** | 228 | 229 |

n=30. **`auth` + `reserve` = 182 ms of a 228 ms request — 80% of it.**

### Finding — the record attributed this cost to the wrong call, and undercounted it by half

That 2026-07-17 record ([`ARCHIVE/BACKLOG_2026-07.md`](ARCHIVE/BACKLOG_2026-07.md)) frames the ~80–100 ms as a property of `reserveQuota` specifically:
*"authorize itself adds ~0 ms while reserveQuota, the **second** sequential Supabase
call, adds ~90 ms."* That framing survived into every later note, including the
Phase-0 baseline's REFUTED entry, and it made the cost look like something peculiar to
the second call — a connection-reuse artifact, a warm-up effect.

Measured directly, with both stages instrumented for the first time: **91 ms and
91 ms.** Identical. The cost is not a property of `reserveQuota`; it is the price of
**one Supabase round trip**, and the proxy makes **two** of them before a single byte
goes upstream. `authorize` appeared free because nothing had ever timed it separately,
not because it was.

This roughly doubles the size of the available prize, and it is what makes B.1 the
right first move: the 30 s auth cache removes one of the two round trips outright —
**91 ms of the 182 ms** — for every warm key, and B.2 (collapsing the pair into one
RPC) addresses the other.

**Stated precisely, so this is not over-read:** the 90 ms in mode 2 is *imposed by the
harness*, not discovered. This experiment does **not** explain why production sees
~90 ms on an intra-region Singapore→Singapore hop where a handshake should cost
10–20 ms — that remains genuinely open, and A.4 is where it gets attacked. What mode 2
proves is narrower and still decisive: whatever the per-call cost turns out to be, the
proxy adds ~nothing on top of it, and it is paid **twice**, symmetrically. A fix that
removes a round trip removes all of it; a fix that optimises proxy code removes none.

---

## B.1 — API-key auth cache: measured before and after

[proxy/authcache.go](../proxy/authcache.go). 30 s TTL, successful lookups only,
LRU-bounded at 10,000 entries, flushable on an admin-authenticated endpoint.

Same harness, same 90 ms imposed per Supabase call, n=30 each:

| stage | before | after |
|---|---:|---:|
| `auth_ms` | 91 | **0** |
| `reserve_ms` | 91 | 91 |
| **`latency_ms`** | **228** | **136** |

**92 ms removed; the proxy-controlled request time falls 228 → 136 ms, a 40%
reduction.** The saving matches the predicted 91 ms to within a millisecond, which is
the check that matters: a saving that had come out at 30 ms would have meant the model
of where the time goes was wrong.

`auth`+`reserve` falls from 80% of the request to 67% — still the dominant cost,
because the *remaining* round trip is now the whole of it. That is B.2's target.

### What it costs, stated plainly

A key revoked in the database keeps working for **up to 30 seconds**. That is the
entire price, it was the founder's call taken against this measured 91 ms, and
`POST /admin/auth-cache/flush` makes revocation immediate without a redeploy.

Three properties make the cache safe to sit in front of an auth check, and each is
pinned by a test that fails when the property is reversed:

- **Positive results only.** An invalid key is never cached, so every attempt still
  costs a full round trip and is still rate-limited — the cache cannot amplify
  credential stuffing. *Neuter-checked by adding the plausible "cache misses too"
  optimisation: the test reports 1 lookup for 4 rejected attempts instead of 4.*
- **The TTL is the whole exposure.** *Neuter-checked by deleting the expiry
  comparison: the revoked key then authorizes forever, and the test says so.*
- **Keyed by hash, never by key.** The map holds the SHA-256 `authorize()` already
  computed, so a heap dump yields what the database already stores.

One honest note on the neuter-checks: the first attempt at breaking "positive results
only" — hoisting the `put` above the row-count gate — **did not break anything**,
because `put` takes `rows[0].ID` and is therefore already guarded by the same
condition. The property is structural rather than defended by a single line, and the
mutation that *does* break it is adding negative caching, which is a design change
someone might genuinely propose. That is the one the test now forbids.

---

## B.2 — collapsing authorize+reserve: RECOMMENDED AGAINST, and B.1 is why

The plan made B.2 *conditional on A.3*. A.3 and B.1 have now answered it, and the
answer is not the one the plan expected.

**B.1 and B.2 are alternatives, not complements.** A combined `authorize`+`reserve`
RPC is called on every request by construction — it *is* the reservation, and a
reservation is a write that can never be cached. So a request that calls it gets its
authorization free and has no use for a cache. Shipping B.2 would make B.1 dead code.

What each one actually buys, from the measured numbers:

| | first request (cold) | subsequent (warm) | schema change |
|---|---:|---:|---|
| neither | 228 ms | 228 ms | — |
| **B.1 (shipped)** | 228 ms | **136 ms** | none |
| B.2 instead | 136 ms | 136 ms | **money-path migration** |

**B.2's entire marginal value over B.1 is 92 ms on the first request of a session**,
and nothing thereafter. Against that it wants a new RPC on the money path — the code
that has already produced a billing abort-refund bug (C3), a quota TOCTOU, and the
stranded-reservation finding — plus a Supabase migration, which in this project is
founder-gated because the SQL catalog is not reachable over PostgREST.

**Recommendation: keep B.1, do not build B.2.** The trade is a rewrite of the money
path for a saving on one request per 30-second idle gap.

### What this means for the remaining 91 ms

It is one Supabase round trip, and it is **irreducible by caching** — reserving quota
is a write, and a write must reach the database. No further code change in this
repository removes it.

The only remaining lever on that 91 ms is the round trip's own cost: **network
distance**. That makes A.4 the next real question rather than a footnote to it.

## A.4 — network geography: what this machine can and cannot measure

**It cannot measure the leg that matters, and neither can any local benchmark.**

The three legs are:

| Leg | Measurable from here? |
|---|---|
| client → Railway | in principle, yes |
| **Railway → Supabase** | **no** — this is the 91 ms, and it happens inside Railway's network |
| **Railway → OpenRouter** | **no** — likewise |

Two of the three, including the one that owns the entire remaining cost, are invisible
from a developer machine. Measuring `curl` from here to `openrouter.ai` measures
*this laptop's* path to OpenRouter, which is a different route on a different network
and answers a question nobody asked.

Attempted anyway, for completeness, and the attempt is worth recording: repeated
samples to both hosts from this machine produced 3.2 s TLS handshakes, an 8.4 s TTFB,
and four outright connection timeouts in eight samples. Those numbers describe a
congested local link, not either service. **No leg-1 figure is published here, because
the honest one is "not measurable on this connection today."**

### The instrument for this already exists — it just has to be deployed

A.2's per-stage timing is exactly the missing measurement. Once this branch is
deployed, the production access log reports, per request, from inside Railway:

- `auth_ms` — the real Railway→Supabase round trip (on cache misses)
- `reserve_ms` — the same leg, on every request
- `upstream_ms` — the real Railway→OpenRouter round trip

That answers, with real numbers and no harness, the question the 2026-07-17 note left
open and this stage could not close: **why an intra-region Singapore→Singapore hop
costs ~90 ms where a TCP+TLS handshake should cost 10–20 ms.** If `reserve_ms` in
production comes back at ~15 ms, the ~90 ms was never the network and the note's whole
premise was wrong; if it comes back at ~90 ms, the region decision has its evidence.

**A.4 is therefore blocked on a deploy, not on analysis** — and that is a much better
place for it to be than where it started.

### The replica-dilution decision rides along

The rate limiter's per-instance buckets (N replicas ⇒ N× the intended rate) and the
auth cache's per-instance TTL are the same shape of residual, and both are decided by
the same fact: how many replicas actually run. That, too, is answerable from the
deployed counters rather than from here.

---

## Where the program stands on latency

| | 2026-07-17 | now |
|---|---:|---|
| Retrieval CPU | ~14 ms (never re-measured) | ~0.44 ms of *added* work priced; the 14 ms itself still not re-measured |
| Proxy money path | ~180 ms, one call blamed | **136 ms**, both calls measured |
| Provider prefill | ~1,125 ms (78%) | unchanged, and unchangeable from here |

The code-controllable budget was ~200–280 ms. **92 ms of it is now gone.** The rest is
one irreducible round trip plus geography, and both are now measurable in production
for the first time.
