# Robustness baseline — measured 2026-07-30

Phase 0 of the 70% → 90% hardening program. Everything here was **measured on
this tree** (`harden/proxy-spend-and-gates`, at `5ea25b2`), not inherited from an
earlier note. These numbers are the ratchet floors and the comparison points every
later phase is scored against.

The rule this phase ran under, stated before it started: **a finding that fails to
reproduce gets struck from the plan rather than fixed on faith.** One of the two
did fail to reproduce, and is struck below.

## Harness

The proxy findings were measured against the **real `proxy` binary**, not a test
double: a purpose-built fake Supabase (recording every RPC in order, with a
`/__ledger` read-out) and a fake OpenRouter streaming SSE at a configurable chunk
delay. Lives in the session scratchpad; it is the basis of the Phase-3.2
integration matrix and should be promoted into `proxy/` at that point.

The fake Supabase implements the four calls the proxy actually makes:
`GET /rest/v1/api_keys`, `POST /rest/v1/rpc/reserve_usage`,
`POST /rest/v1/rpc/apply_correction`, `POST /rest/v1/rpc/sweep_pending_corrections`.

## 1. Coverage — the ratchet floors

Measured with `go test -cover ./...` per module (there is no root module; `./...`
from the repo root fails outright).

| Module | Coverage | Floor to enforce |
|---|---:|---:|
| `protocol` | 88.9% | 88.9% |
| `editapply` | 87.0% | 87.0% |
| `proxy` | 80.3% | 80.3% |
| `helper` / `helperproto` | 6.5% / 75.0% | 6.5% / 75.0% |
| `clients/tui` | 66.7% | 66.7% |
| `daemon` | 69.3% | 69.3% |

`proxy` measures 80.3% here against the 80.7% recorded in BACKLOG for the same
commit — a rounding/run difference on a shared statement count, not a regression.
**80.3% is the floor**, i.e. the lower of the two, so the ratchet cannot be
tripped by that variance.

## 2. Runtime baseline

`threads` from `/proc/<pid>/status`, `fds` from `/proc/<pid>/fd`, `rss_kb` from
`VmRSS`. Note that `pgrep -f` matches the `setsid` wrapper as well as the process;
these figures are from the real listener PID.

### proxy

| State | threads | fds | rss_kb |
|---|---:|---:|---:|
| idle | 6 | 6 | 7,760 |
| 4 concurrent completions (round 1) | 9 | 10 | 10,616 |
| 12 concurrent completions (round 3) | 11 | 10 | 12,792 |
| settled, 12 requests complete | 11 | 10 | 12,792 |

All 12 requests returned 200 and **all 12 reservations were correctly closed**
(12 `apply_correction` calls, one per `pending_id`). FDs plateaued at 10 and did
not grow with request count; threads plateaued at 11. That plateau is Go's normal
M-growth for blocking syscalls, not a leak — and it is the exact shape the Phase-3.4
soak test asserts against over 30 minutes.

### daemon

| State | threads | fds | rss_kb |
|---|---:|---:|---:|
| idle (retrieval disabled, no index) | 8 | 9 | 12,872 |
| after 20 socket connections | 8 | 9 | 12,876 |

No FD leak on the socket accept path. Two incidental confirmations: peer auth
admits a same-UID caller and the version handshake then fails closed on a bad
version (`rejecting client: protocol version 0 != 1`); and the daemon `Fatalf`s
cleanly when `XDG_RUNTIME_DIR` makes the socket path exceed the 108-byte
`sun_path` limit, rather than misbehaving.

## 3. Finding CONFIRMED — SIGTERM mid-stream strands the reservation, permanently

The headline defect. Reproduced against the real binary.

**Control — a stream allowed to complete:**

```
authorize → reserve_usage → apply_correction(tokens=-3959, pending=1001)
```

Reserved 4096, upstream reported `total_tokens: 137`, refund −3959. Ledger
balanced, `pending_corrections` row closed. Correct.

**SIGTERM delivered mid-stream (10 chunks already relayed to the client):**

```
authorize → reserve_usage           ← and nothing else, ever
```

- The proxy died **instantly** — no drain, no in-flight completion. Confirmed
  mechanically: [proxy/main.go:339](../proxy/main.go#L339) calls
  `ListenAndServe` with no `signal.Notify` and no `srv.Shutdown` anywhere in the
  module.
- `pending_id=1002` was opened and **never closed**. Corrections stayed at 1
  while reservations reached 2.
- Cost: **3,959 tokens over-charged on that one request** (4096 reserved − 137
  actual), and it is not self-healing. The sweep is explicit that it never
  refunds — over-metering is the deliberately-chosen safe direction for a *crash*.
- The client saw `curl` exit 18 (`CURLE_PARTIAL_FILE`): a silently truncated
  stream with no error frame.

**Why this matters more than the sweep's design assumed:** the sweep treats a
stranded reservation as a rare crash artifact. A Railway redeploy is a SIGTERM,
so this fires on **every deploy**, for every request in flight. The sweep is
working correctly; its premise is what is wrong.

Second, distinct defect on the same path, recorded not fixed here: a
SIGTERM-truncated stream is indistinguishable to the daemon from a complete
answer. `IncompleteBudgetExceeded` (`b1fed6b`) covers the budget-kill case; there
is no equivalent signal for shutdown truncation.

## 4. Finding REFUTED — un-drained response bodies do NOT break connection pooling

The TTFT note carried an explicitly-labelled *untested hypothesis*: that
`authorize` and `reserveQuota` decode with
`json.NewDecoder(io.LimitReader(body, cap)).Decode(&rows)` and never read to EOF,
so `http.Transport` never re-pools the connection, and that this explains ~80 ms
of unexplained `reserveQuota` latency.

**Measured, isolating exactly that decode pattern against a connection-counting
server, 20 sequential requests per row:**

| Body size | un-drained (current) | drained (proposed) |
|---|---:|---:|
| 0 B | 1 conn | 1 conn |
| 100 B | 1 conn | 1 conn |
| 4 KB | 1 conn | 1 conn |
| 64 KB | 1 conn | 1 conn |

Pooling survives at every size. The reason: `json.Decoder`'s buffered reader
consumes through EOF while filling its buffer, so for a `Content-Length` body the
transport observes EOF and re-pools even though `Decode` returned early.

**The ~80 ms remains unexplained. P1.5 is not a TTFT fix and must not be reported
as one.** The other candidate from that note — collapsing validate+reserve into
one RPC — is untouched by this result and remains the live hypothesis.

### But the drain does matter in one case, and it is worth having

When the response **exceeds** the `io.LimitReader` cap, `Decode` stops early and
the unread remainder does break pooling:

| 8 KB body vs 1 KB cap | distinct TCP conns / 20 requests |
|---|---:|
| un-drained | **20** |
| drained | **1** |

So P1.5 stays in the program, rescoped honestly: it is **defense-in-depth for the
over-cap case**, not a latency optimisation. `maxAuthResponseBytes` is 64 KB, which
`authorize` would only exceed on a pathological row count that the
`len(rows) != 1` check then rejects anyway — but that is precisely the degraded
condition in which you least want to also lose connection pooling on every
request.

## 5. Premises confirmed mechanically (no repro needed)

- **No panic recovery in `proxy`.** The whole repo has exactly one `recover()`,
  at [daemon/server.go:173](../daemon/server.go#L173).
- **`finalizeUsage` is never deferred** — five hand-placed call sites, so the
  "finalized exactly once" invariant is maintained by hand.
- **`daemon` does not drain on shutdown.** Zero `sync.WaitGroup` / `wg.Wait` in
  either `daemon/main.go` or `daemon/server.go`; shutdown is
  `ln.Close(); os.Remove(...)`.
- **No request IDs, no structured logging, no metrics** in either binary.
- **Zero fuzz targets** in the repo.
- **Secret hygiene is clean**: no `.env` is tracked, none ever was in history, no
  build artifacts tracked.

## 6. What this changes in the plan

- P1.2 (graceful shutdown) is confirmed as the highest-value item, with a
  measured blast radius.
- P1.5 is **rescoped** from "TTFT fix" to "over-cap pooling defense", and the
  TTFT claim is withdrawn.
- A new item is recorded for the phase that owns stream protocol:
  SIGTERM-truncated streams need an incompleteness signal, mirroring
  `IncompleteBudgetExceeded`.
