# Design: closing the quota-check TOCTOU race (Phase 3 continuation)

Status: SHIPPED, 2026-07-24. This document is the design that was built, kept
as the record of why the shape is what it is -- not a proposal awaiting
approval. It read "design only, no code changes yet" until 2026-08-01, by which
point a cold reader would conclude the quota TOCTOU race was still open when it
had been closed for a week.

What landed: `proxy/migrations/0001_reserve_usage.sql`, `reserveQuota`,
`peekMaxTokens`, `finalizeUsage` (all on `main`), and then the section 5(e)
outbox in `ca7e3c4` -- migration 0002, `pending_corrections`, and the
reconciliation sweep. Section 5(e) below is annotated where it describes that
work as a recommendation.

## 0. What's confirmed broken

Live 8-concurrent race test against the local proxy: all 8 requests'
`checkQuota` reads observed `used=0, limit=100` before any of the 8
`recordUsage` writes had landed, so all 8 were forwarded to OpenRouter and
billed. Recorded usage ended at 236 tokens against a `token_limit` of 100.
Root cause is TOCTOU -- `checkQuota` (a `SELECT`) and `recordUsage` (a
write, applied only *after* the response completes) are two separate
round trips with an unguarded gap between them. `increment_usage`'s SQL
itself is fine (a single `UPDATE ... SET x = x + $n` is atomic); the bug is
that nothing atomic ever gates admission before the call is forwarded.

## 1. Schema (verified, not assumed)

Pulled from the live PostgREST OpenAPI document
(`GET {SUPABASE_URL}/rest/v1/` with `Accept: application/openapi+json`,
using the service-role key already in `proxy/.env` -- read-only
introspection, no DDL) rather than guessed from the Go code's usage of it:

```
usage:
  key_id       uuid            PK, FK -> api_keys.id
  tokens_used  bigint          default 0
  token_limit  bigint          default 100000  (NOT NULL in the live schema --
                                                 see note below)
  period_start timestamptz     default now()   (unused by any current Go
                                                 code path; not touched here)

api_keys:
  id           uuid            PK, default gen_random_uuid()
  key_hash     text
  key_prefix   text
  label        text
  active       boolean         default true
  created_at   timestamptz     default now()

rpc increment_usage(p_key_id uuid, p_tokens integer) -> exists live today,
  params confirmed via the same introspection call. Body/behavior is not
  introspectable this way (PostgREST doesn't expose function source), so
  the migration below reconstructs it from the Go-side contract already
  documented at its call site (`tokens_used = tokens_used + tokens`,
  no other side effects) -- this is also literally the task's ask: capture
  it in the repo for the first time, since today it exists only in the
  Supabase dashboard.
```

Note on `token_limit` NOT NULL: `checkQuota` today has a defensive branch
for `token_limit IS NULL` ("can't verify" -> refuse). The live column
doesn't currently allow null, so that branch is dead code in practice --
but I'm keeping the equivalent defensive handling in the new path anyway,
since the Go code has no way to *enforce* the DB stays that way, and
fail-closed-on-unexpected-shape is the same posture used everywhere else
in this file (`authorize`, `checkQuota`'s row-count checks, etc.).

## 2. Chosen pattern: single atomic `UPDATE ... WHERE ... RETURNING`

Confirming the proposed pattern over the `SELECT FOR UPDATE` alternative:
a single UPDATE statement already takes the row-level lock it needs as
part of executing -- there's no window between "check" and "write" because
they're the same statement. A concurrent second UPDATE against the same
`key_id` blocks on that row lock until the first commits, then
re-evaluates its own `WHERE` clause against the now-committed value. So
three concurrent reservations of 50 against a fresh `limit=100` row
resolve deterministically to: first succeeds (0->50), second succeeds
(50->100), third sees 100+50<=100 is false and gets 0 rows back. No
explicit locking, no separate transaction-wrapped two-statement sequence
to get wrong. `SELECT FOR UPDATE` would need an explicit transaction
spanning two statements (lock, then conditionally write) for no additional
correctness -- strictly more moving parts for the same guarantee, so I'm
not proposing it.

```sql
-- new RPC
create or replace function reserve_usage(p_key_id uuid, p_reserved integer)
returns table(tokens_used bigint, token_limit bigint)
language sql
as $$
  update usage
  set tokens_used = usage.tokens_used + p_reserved
  where usage.key_id = p_key_id
    and usage.tokens_used + p_reserved <= usage.token_limit
  returning usage.tokens_used, usage.token_limit;
$$;
```

Zero rows back means the reservation was refused -- either `key_id` doesn't
have a `usage` row, or the reservation would exceed `token_limit`. Same
ambiguity `checkQuota` already had for "row not found" vs. other failure
shapes (it doesn't distinguish either); not a new gap.

The existing `increment_usage(p_key_id, p_tokens)` RPC is reused verbatim,
unchanged in signature or body, for the *true-up* step -- `p_tokens` was
always just "amount to add," and a correction is simply a signed amount to
add (negative for a refund). No new RPC needed for step 2.

## 3. Reservation sizing

The proxy's stated contract is "forwards the request body AS-IS ... never
parsed or altered" (main.go's top comment, README). Reading `max_tokens`
out of the body to size a reservation is a narrow, deliberate exception to
that -- flagging it explicitly rather than quietly walking back a stated
invariant. It's scoped the same way `extractUsage`/`extractProvider`
already scope their exceptions to the *response* side: the decode target
has exactly one field (`max_tokens`), so message content is still never
touched, on either side of the proxy.

In practice this will rarely trigger: the daemon's own
`chatCompletionRequest` struct (`daemon/provider.go`) has no `MaxTokens`
field at all -- confirmed by reading it -- and its one call site
(`streamCompletion`, `provider.go:170`) never sets one. So for all real
traffic today, reservation falls through to a fixed default ceiling. The
peek is implemented anyway because (a) the task asked for it, (b) it's
forward-compatible if `max_tokens` is ever added, and (c) other future
callers of this proxy aren't guaranteed to share the daemon's request
shape.

Proposed constants:
```go
defaultReservationTokens = 4096  // used when max_tokens is absent/invalid
maxReservationTokens     = 32768 // clamp ceiling if max_tokens is present
                                  // but absurd (e.g. caller error)
```

**Checked against real provider ceilings, not assumption** (OpenRouter's
public `/api/v1/models/{slug}/endpoints`, queried directly -- no auth
needed for listing):

| model (models.json tier)        | active | real max_completion_tokens across providers        |
|----------------------------------|--------|------------------------------------------------------|
| deepseek/deepseek-v4-flash (primary) | yes | 32,768 (Venice) up to 1,048,576 = full context (Morph, Parasail); several providers (GMICloud, DigitalOcean, Fireworks, NextBit) report **no cap at all** |
| qwen/qwen3-coder-30b-a3b-instruct (ghost_text) | no | 32,768 to 262,144 |
| deepseek/deepseek-r1 (reasoning) | no | 4,096 (Azure) to 16,000 (Novita) -- **meaningfully higher on Novita, 4x the proposed default** |

Correcting my own earlier framing rather than letting it stand: 4096 is
**not** "generous" or a bound on overshoot. Against the currently-active
model, it's 8x to 256x smaller than what several providers will actually
let a single completion run to, with real ones reporting no cap at all.
No fixed default can be sized to cover that worst case -- reserving
anywhere near 1,048,576 tokens upfront would mean a single request
consumes an entire default-quota key's headroom (100,000) by itself,
which would break normal concurrency for the one thing this fix is
supposed to preserve. So the constant's job is narrower than I originally
implied: it sizes *typical-case* admission control (so concurrent
requests correctly compete for remaining quota), not worst-case
containment. 4096 is defensible for that narrower job and I'm keeping it,
but the "bounds overshoot" claim was wrong and is retracted here rather
than quietly carried forward.

**What actually contains the worst case is the true-up step, not the
constant** -- and that only works if the true-up write itself is
reliable. See the revised §5 below: this is exactly why (d)/(e) needed a
harder look rather than being accepted as minor bookkeeping risk.

Hard-capping a single response's length (sending `max_tokens` on
outgoing requests) would be a structural alternative that actually
bounds worst-case overshoot, but changes model behavior and is a
separate, unopened problem -- not in scope here.

## 4. Go-side call shape

```go
// replaces checkQuota
func (p *proxy) reserveQuota(ctx context.Context, apiKeyID string, reserved int) bool

// replaces recordUsage; same detached-context/fire-and-forget contract
// (never delays or fails the client's already-complete response), but
// now takes a signed delta against an existing reservation rather than
// an absolute total, and retries up to correctUsageMaxAttempts times
// (network error / 5xx only, not 4xx) with a short backoff between
// attempts before giving up and logging a distinct "CORRECTION LOST"
// line -- see §5's (d)/(e) direction analysis for why this exists.
func (p *proxy) correctUsage(keyID string, delta int)

// new, narrow peek mirroring extractUsage's one-field-only discipline
func peekMaxTokens(body []byte) (int, bool)

// new, mirrors extractUsage but reads the top-level (non-SSE) response
// shape instead of one SSE line -- needed so the non-streaming io.Copy
// path can also true up instead of silently never recording anything
// (see 5c)
func peekUsageTotal(body []byte) (int, bool)
```

`handleChatCompletions` changes shape as follows (full diff in the
implementation phase, this is the call sequence):

1. `authorize` unchanged.
2. Body is read fully into memory (`io.ReadAll` over the existing
   `http.MaxBytesReader`-wrapped reader -- already bounded at
   `maxRequestBodyBytes` today, so this isn't a new unbounded read, just a
   buffer instead of a direct stream-through) so it can be peeked *and*
   still forwarded byte-for-byte after.
3. `reserved := peekMaxTokens(bodyBytes)` clamped to
   `maxReservationTokens`, else `defaultReservationTokens`.
4. `p.reserveQuota(ctx, apiKeyID, reserved)` -- on failure, same response
   as today (429, `{"error":"quota_exceeded"}`), nothing was reserved, no
   refund needed.
5. Upstream request body becomes `bytes.NewReader(bodyBytes)` (was the
   live `limitedBody` reader) -- otherwise unchanged.
6. `p.client.Do` error (upstream unreachable, before any response) ->
   full refund (`go p.correctUsage(apiKeyID, -reserved)`), then existing
   502 response, unchanged.
7. `streamSSE` and the non-SSE `io.Copy` branch both end by calling a new
   shared helper instead of duplicating the true-up logic:
   ```go
   func (p *proxy) finalizeUsage(keyID string, reserved, actual int) {
       delta := -reserved
       if actual > 0 {
           delta = actual - reserved
       }
       go p.correctUsage(keyID, delta)
   }
   ```
   `streamSSE` passes the `totalTokens` it already extracts (0 if no usage
   chunk ever arrived); the non-SSE branch buffers the response (it's
   already fully read for `io.Copy` today) and runs `peekUsageTotal` on it
   first.

## 5. The refund/unwind edge case, worked through explicitly per point 3

Every point where a reservation can be made and then never corrected:

- **(a) `reserveQuota` itself fails.** Nothing was reserved. No refund
  needed. Unchanged from today's `checkQuota` failure shape.
- **(b) Upstream call errors before any response** (network failure,
  DNS, connection refused). Full refund, synchronous fire-and-forget call
  at the point of the error, before the 502 is written. This case doesn't
  exist today because there was nothing to unwind before this change.
- **(c) Response received but no usable usage figure ever appears** --
  covers: upstream returned a non-2xx with no usage payload, the SSE
  stream was cut short (client disconnect, network drop) before the final
  usage chunk, or a non-streaming response whose body doesn't parse as
  expected. All of these currently hit `streamSSE`'s
  `if totalTokens > 0 { go p.recordUsage(...) }` guard and silently do
  *nothing* in the false branch -- today that just means undercounted
  usage (harmless-ish). After this change, that same false branch would
  strand a live reservation forever if left alone, so it changes to always
  call `finalizeUsage` (which refunds the full `reserved` amount when
  `actual` is 0), never skip it.
### (d)/(e) direction analysis -- required before accepting either as residual

`finalizeUsage`'s delta is signed and goes one of two ways, and the two
directions are **not equally safe** if the correction write is lost:

- `actual <= reserved` -> delta is negative (refund). If this write is
  lost, `tokens_used` stays at the higher *reserved* value forever ->
  **overstated usage -> safe direction** (a later request gets throttled
  slightly early; it can never admit more than the true remaining quota).
- `actual > reserved` -> delta is positive (top-up: the response used
  more than was reserved for it). If this write is lost, `tokens_used`
  stays at the lower *reserved* value forever -> **understated usage ->
  unsafe direction** (a later request can be admitted against quota that
  was already really spent, because the tracked counter doesn't reflect
  it).

The `actual > reserved` case is not a rare tail here -- §3's real
provider data shows the currently-active model routinely allows
completions far larger than the 4096 default, so top-up corrections are
a normal, frequent occurrence for this product, not an edge case. That
means both (d) and (e) skew toward the unsafe direction more often than
not, in practice. Accepting that quietly would reopen, via a different
mechanism, materially the same problem this whole task exists to close --
so it isn't being accepted as-is.

- **(d) The refund/top-up call itself fails** (Supabase unreachable, a
  transient 5xx, a network blip at the moment of the correction). Closed
  as far as is proportionate without a rearchitecture:
  - `correctUsage` gets bounded retries: up to 3 attempts, short backoff
    (~250ms, ~750ms) between them, retrying only on a network error or a
    5xx response -- not on 4xx, which indicates a real request-shape
    problem a retry can't fix. Still fully async/fire-and-forget from the
    client's perspective (the client already has its complete response by
    the time this runs, same as today's `recordUsage` contract) -- this
    only narrows the single-attempt failure window, it adds no latency to
    the request itself.
  - If all retries are exhausted, a **distinct, greppable log line**
    (`usage: CORRECTION LOST after N attempts (key_id=..., delta=...) --
    tokens_used may be inaccurate`) fires, separate from the routine
    single-failure log line `recordUsage` already had. A lost correction
    becomes loud and operationally traceable instead of silent.
  - This narrows the exposure window from "any single transient hiccup
    loses it" to "3 consecutive failures within ~15s loses it." It is
    **not** full durability -- a Supabase outage lasting longer than that
    window still loses the correction. Naming that limit explicitly
    rather than implying the retry closes it completely.
- **(e) Process crashes between a successful reservation and a completed
  correction** (proxy killed/redeployed mid-flight, including mid-retry).
  Retries don't help here by definition -- there's no process left to
  retry with. This sub-case remains genuinely open, and given the
  direction analysis above, it is the unsafe direction whenever the
  crashed request's actual usage would have exceeded its reservation.
  I'm not folding this into soft "accepted residual risk" language --
  closing it fully needs real infra this pass doesn't build: a durable
  outbox for pending corrections, or a periodic reconciliation sweep that
  compares summed real usage against `tokens_used` and drift-corrects.
  **Recommending this as a named near-term follow-up**, not doing it in
  this pass (it's out of the scope just confirmed: migration, main.go,
  unit tests) -- flagging it prominently rather than letting scope
  boundaries quietly absorb a confirmed-unsafe gap.

Point 3's original ask was "don't leave it as a known gap" for the
failed/timed-out-*call* case -- (b) and (c) close that fully (both are
request-side failures with no ambiguity about direction: nothing was
truly used, so a full refund is always correct). (d) is now closed to the
same "transient failure" standard as everything else in this file (bounded
retry, then loud logging) but not to full durability. (e) is named,
directionally analyzed, and explicitly not closed -- recommended as
follow-up work instead of silently accepted.

> **Update 2026-08-01: (e)'s follow-up was built (`ca7e3c4`), and the honest
> reading of it has not changed.** Every reservation now opens a durable
> `pending_corrections` row in the same atomic statement that reserves, every
> correction closes it in the same statement that increments, and a 5-minute
> sweep claims and loudly reports whatever neither happened to. Proven with a
> `kill -9` against the real binary and a real tick.
>
> **It does not recover a dead request's true usage** -- that number left with
> the process. It converts a silent, indefinite, unsafe-direction stranding into
> a loud, ~20-minute-bounded, safely-directed one. The log line, not the
> accounting, is the deliverable. The paragraph above stands as written: (e) is
> mitigated and made loud, not solved.

## 6. What changes vs. what's purely additive

Purely additive: `reserve_usage` RPC (new), `peekMaxTokens` (new),
`peekUsageTotal` (new), `finalizeUsage` (new), the migration file itself.

Not additive, called out explicitly: `checkQuota` is removed (replaced by
`reserveQuota` -- different gate shape, can't coexist), and `recordUsage`
becomes `correctUsage` with a changed contract (signed delta against an
existing reservation, not an absolute total against nothing). This is the
one place this patch changes existing verified behavior rather than
sitting alongside it -- unavoidable, since the entire fix is replacing
"record after the fact" with "reserve before, correct after."
`authorize`, `streamSSE`'s line-by-line relay, `extractProvider`,
`extractUsage`, and the SSE flushing behavior are all untouched.

## 7. Migration file

Lands at `proxy/migrations/0001_reserve_usage.sql`, containing both the
reconstructed `increment_usage` (matching current live behavior, captured
in version control for the first time) and the new `reserve_usage`. I
don't have a path to execute DDL against the live Supabase project myself
-- no `psql`/`supabase` CLI in this environment and no direct Postgres
connection string, only the PostgREST REST surface (which is read/RPC
only, not DDL). The migration file will need to be applied by pasting it
into the Supabase SQL editor, same as the `key_hash` fix earlier. Flagging
this now so it isn't a surprise at implementation time -- I can write and
test everything else (unit tests against a mocked PostgREST) without it,
but the live re-run of the 8-concurrent test in step 3 is blocked on it
being applied.

## 8. Unit test plan (implementation phase)

All against an `httptest.Server` standing in for PostgREST -- no live
Supabase dependency for the test suite itself:
1. Normal single request: reservation succeeds (1 row back), upstream
   call succeeds with a usage chunk, correction delta matches
   `actual - reserved` exactly (including negative deltas when actual <
   reserved).
2. Over-limit rejection: mock returns 0 rows from `reserve_usage` -> 429,
   upstream never called (assert the mock upstream handler was never hit).
3. The 8-concurrent scenario, reproduced against a mutex-guarded in-memory
   fake of the `usage` row (not real Postgres -- real Postgres's
   single-statement atomicity is a documented guarantee, not what's in
   question; what's being tested here is that the Go-side request/response
   handling around `reserve_usage` is correct under real goroutine
   concurrency): fire 8 goroutines at a fake with `limit=100`,
   `reserved=50` each, assert exactly 2 succeed and 6 get 429, and that
   the fake's final `tokens_used` is exactly 100.
4. Failed-call refund: upstream `client.Do` returns an error after a
   successful reservation -> assert a correction call is made with
   `delta == -reserved`.

## 9. Re-test plan (implementation phase, once migration is applied)

Same script, same methodology as the original run (backgrounded curl
subshells launched in a loop then `wait`ed on, launch-offset logging kept
so genuine concurrency is visible in the report, not asserted). Expect:
some 200s, some 429s (exact split depends on `token_limit` on the test key
and the reservation size, will report actual numbers), and the 429s must
be occurring pre-forward (upstream never contacted) rather than
post-hoc -- verifiable from the proxy's own log lines distinguishing
`quota: reservation refused` from any upstream call log.
