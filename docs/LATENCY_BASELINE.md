# Latency baseline — measured 2026-07-31

Stage A of the performance phase. Everything here was **measured on this tree**, on
the commit named below, not inherited from the 2026-07-17 TTFT note. It runs under the
same standing rule as the robustness program: **a finding that fails to reproduce gets
struck rather than fixed on faith.** One of this stage's own hypotheses was struck, and
is recorded below rather than quietly dropped.

## Why this document exists

The only end-to-end latency figure this project owned was measured **2026-07-17**
(`BACKLOG.md:1174`). Since then **29 commits landed in `proxy/` and 57 in `daemon/`** —
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

*A.2 (per-stage proxy timing), A.3 (end-to-end TTFT re-measurement) and A.4 (network
geography) follow.*
