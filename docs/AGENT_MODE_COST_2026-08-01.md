# Agent-mode cost baseline — measured 2026-08-01

What one agent turn costs, end to end through the **real daemon** and the **real
proxy binary**, against `proxy/testharness`. No production contact, no spend.

```bash
scripts/agent-cost-bench.sh                          # 5 turns, 3 iterations each
TURNS=10 TOOL_CALLS=7 scripts/agent-cost-bench.sh    # worst case at the default budget
RESERVE_FAIL_AFTER=4 TOOL_CALLS=7 scripts/agent-cost-bench.sh  # quota dies mid-turn
```

**Scope, stated before the numbers.** The upstream is a fake that always asks
for the same tool and reports a fixed usage frame, so nothing here measures
model quality or real token counts — `docs/AGENT_LOOP_RELIABILITY_2026-07-31.md`
covers behaviour against a real model. What this measures is the **shape and
cost structure** of a turn: request counts, quota reservations, context growth,
latency, and resource use. No number in this repo answered any of those before.

---

## 1. The headline: an agent turn multiplies quota reservations 1:1 with steps

The proxy reserves quota **per request** (`defaultReservationTokens = 4096`) and
the daemon declares no `max_tokens`, so the reservation is the default every
time. An agent turn is N+1 requests.

| Turn shape | Requests | `reserve_usage` calls | **Tokens reserved / turn** | vs. single-turn |
|---|---|---|---|---|
| Single-turn (agent mode off) | 1 | 1 | 4,096 | 1× |
| 3 iterations (measured median shape) | 3 | 3 | **12,288** | **3×** |
| 8 iterations (`max_iterations` default) | 8 | 8 | **32,768** | **8×** |

Measured, not modelled — straight off the fake Supabase ledger:

```
=== money path, TURNS=10 TOOL_CALLS=7 ===
authorize        2   (0.2 per turn)      <- the auth cache working (B.1)
reserve_usage    80  (8.0 per turn, expected 8)
apply_correction 80
tokens reserved  327680  (32768 per turn)
open pending     0   (>0 means a reservation was stranded)
```

`apply_correction` fires once per reservation and `open_pending` is 0, so the
**net** accounting is correct — a turn is reconciled down to what it actually
used. What multiplies is the **peak** a key must have headroom for. A key with
30k tokens of remaining quota cannot complete one worst-case agent turn, even
though that turn will only end up consuming a few hundred.

This closes the risk the MCP plan registered and never worked:
> "N× quota reservations per turn — reconcile with `QUOTA_RESERVATION_DESIGN.md`
> before enabling for managed keys."

It is 8×, exactly, and it was already visible: Phase 0's first tool-calling eval
lost **33 of 84 probes to `quota_exceeded`** through the managed proxy and was
re-run direct to OpenRouter. That workaround was never a fix.

## 2. Context growth is LINEAR, not quadratic

The loop re-sends the whole message list every step, so the fear was superlinear
growth. It is not:

```
upstream request body bytes, one 8-iteration turn:
  iter 1:  2015 bytes  [tools]
  iter 2:  2273 bytes  [tools]  (+258)
  iter 3:  2531 bytes  [tools]  (+258)
  ...
  iter 8:  3821 bytes  [tools]  (+258)
  growth across the turn: 1.90x
```

A flat +258 bytes per iteration — one tool result, capped by
`max_tool_result_bytes`. Total prompt bytes over a turn are
**O(n²/2) in the increment**, but the increment is bounded by config, and at the
default caps an 8-step turn carries **1.9× the first request's body**, not 8×.

**The cost driver is the request count, not the context.** That is the useful
finding: `max_iterations` prices a turn almost exactly linearly.

## 3. Latency

| Shape | p50 wall | p95 wall | p50 TTFT |
|---|---|---|---|
| 3 iterations, paced (3 s between turns) | 31 ms | 33 ms | 12 ms |
| 8 iterations, paced | 50 ms | **1,050 ms** | 31 ms |
| 3 iterations, **unpaced** (50 turns back to back) | **1,033 ms** | 2,036 ms | 1,014 ms |

The proxy and daemon add tens of milliseconds per turn. Everything above that is
**rate-limit retry**, see §4. Real-model latency is ~17.6 s/turn
(`AGENT_LOOP_RELIABILITY`), so the machinery measured here is ~0.3% of a real
turn — the loop's overhead is not a latency lever.

## 4. The per-key rate limiter was sized for single turns, and agent mode falsifies its premise

`proxy/ratelimit.go` reasons from stale premises in its own comments — *"a
developer prompting from an IDE is well under 1 req/s"* (`keyRatePerSecond = 2.0`,
`keyBurst = 20.0`).

| Cadence | Turns before throttling | Observed |
|---|---|---|
| 8-iteration turns, 3 s apart | **7** | 3 × `rate limited` at the proxy → 3 × 429 → daemon retried at 1 s each |
| 3-iteration turns, back to back | ~7 | 63 × `rate limited` over 50 turns; every turn pays 1–2 s |

The arithmetic: a turn spends N tokens of a 20-token burst that refills at 2/s.
At 8 iterations and 3 s spacing that is −8 +6 = **−2 per turn**, so the burst
drains in ten turns and stays drained.

**It degrades gracefully today.** `streamWithRetry` retries a 429 while nothing
has streamed, so the user sees a ~1 s pause, not a failure. That grace is
load-bearing and it is thin — see finding **A-2** in the gate report for what
happens when the retry budget is gone.

## 5. Egress — what tools put on the wire

| Metric | Value |
|---|---|
| Tool output to the model, per call | 11 bytes (this fixture; `list_directory` on a 2-file dir) |
| Per-turn ceiling | `max_tool_result_bytes` 32,768 / result, `max_total_tool_bytes` 131,072 / turn |
| Reported to the user | per call, post-scrub, on `ToolActivity.result_bytes` |

The bound is config-visible and enforced at a single choke point. The fixture
number is uninteresting; **the instrument is the point** — a user can watch the
figure that leaves their machine, per call, which is what a privacy-positioned
product should be able to show.

## 6. Resources — 50 turns, 150 requests, unpaced

```
before   fds=9   threads=8    children=0   rss_kb=13172
after    fds=10  threads=14   children=0   rss_kb=21232
```

**No fd leak and no process leak.** Registries are built and closed per turn, and
`children=0` after 50 turns says `Close()` reaps.

RSS grew **+8 MB**. 50 turns is too short to separate warm-up from slow growth,
and this run had no Lane B server configured (no subprocess spawn/reap cycle).
Recorded as **not established** rather than passed — see "Blocked, not skipped".

## 7. What invalidates these numbers

- `defaultReservationTokens`, `keyRatePerSecond`/`keyBurst`, or
  `maxReservationTokens` changing in the proxy.
- `mcp.budget` defaults changing — `max_iterations` is the multiplier in §1 and
  `max_tool_result_bytes` is the increment in §2.
- The daemon starting to declare `max_tokens` (which would end §1's problem).
- A Lane B server in the loop: §6 measured Lane A only, and a subprocess per turn
  is the case most likely to leak.
