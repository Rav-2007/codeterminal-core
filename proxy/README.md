# mochiii-proxy

The managed-tier proxy in front of OpenRouter.

It exists so the OpenRouter API key never ships to end users. It holds that key
server-side, authenticates every caller by a per-user Mochiii key, and streams
completions back without buffering. Around that core it enforces the policies a
shared credential requires: who may call, which models they may bill, how much
one request may spend, and that zero-data-retention routing is actually on the
wire.

**It never reads message content.** Every gate below inspects only the request's
top-level *envelope* — key names and a handful of scalar flags. `messages` stays
an opaque byte slice from ingress to egress, and the accepted body is forwarded
**byte-for-byte**. That property is what makes this a ZDR-preserving proxy rather
than a relay that happens to see everything, and it constrains every design
decision here.

---

## Contents

- [Request lifecycle](#request-lifecycle)
- [Endpoints](#endpoints)
- [Policy reference](#policy-reference)
- [Error reference](#error-reference)
- [Configuration](#configuration)
- [Running it](#running-it)
- [Design notes](#design-notes)
- [What it deliberately does not do](#what-it-deliberately-does-not-do)

---

## Request lifecycle

Gates run in this order, and **every one fails closed**. Nothing reaches
OpenRouter until all of them pass.

| # | Gate | Rejects with |
|---|---|---|
| 1 | Method must be `POST` | `405` |
| 2 | Pre-auth admission — global bucket, then per-source | `429 rate_limited` |
| 3 | Mochiii key validated against Supabase | `401` |
| 4 | Per-key request rate | `429 rate_limited` |
| 5 | Per-key token-volume rate | `429 rate_limited` |
| 6 | Per-key in-flight ceiling | `429 rate_limited` |
| 7 | Body read, capped at 4 MB | `400` |
| 8 | Cost authorization (model + billable side-channels) | `403` |
| 9 | ZDR routing flags present and correct | `403 zdr_required` |
| 10 | Streaming requested | `403 stream_required` |
| 11 | Declared `max_tokens` within cap | `403 max_tokens_too_large` |
| 12 | Quota reserved atomically | `429 quota_exceeded` |
| → | **Forwarded to OpenRouter**, key injected server-side | |
| 13 | Per-request token ceiling, enforced **mid-stream** | stream killed, `budget_exceeded` chunk |

Two ordering choices are deliberate:

- **Admission before the Supabase lookup** (2 before 3), so a flood of bad keys
  cannot amplify into a third-party dependency.
- **Auth before the body is read** (3 before 7), so an unauthenticated caller
  cannot push 4 MB into the process at all.

---

## Endpoints

### `POST /v1/chat/completions` · `POST /chat/completions`

Forwards to `https://openrouter.ai/api/v1/chat/completions` (or
`OPENROUTER_API_BASE`), adding `Authorization: Bearer <key>` server-side. The
accepted body is forwarded byte-for-byte. The response is streamed back as SSE as
it arrives — never buffered.

Only `Content-Type` is relayed from OpenRouter's response headers. Everything
else is dropped, and OpenRouter's account-identifying fields are stripped from
both the streamed and buffered response paths.

### `GET /health`

Returns `200 {"status":"ok"}`. One of two unauthenticated routes (with
`/models/status`), and rate-limited
like any other. The build commit is **omitted by default** — it fingerprints the
exact running build for anonymous callers — and included only when
`HEALTH_EXPOSE_COMMIT=1`, for deploy verification.

### Everything else

`404`, behind the same pre-auth limiter. Unregistered paths are throttled rather
than served as a free anonymous endpoint.

---

## Policy reference

### Authentication

Every route except `/health` requires a valid per-user Mochiii key, SHA-256'd and
looked up in Supabase with `active = true`. It fails closed on every path:
missing header, unconfigured Supabase, lookup error, timeout, non-200, or a row
count other than exactly one. With `SUPABASE_URL` / `SUPABASE_SERVICE_ROLE_KEY`
unset, **every** request is rejected `401` rather than served unauthenticated.

### Privacy — ZDR enforcement

Rejects any request whose `provider` routing object is missing, malformed, or
weakened (`zdr` not `true`, or `data_collection` not `"deny"`). **The proxy, not
the client, is the authority that these flags are correct on the wire.**

Keys are matched **exactly**, not via Go's default case-insensitive struct-tag
decoding — see [Design notes](#why-keys-are-matched-exactly).

**Scope:** this closes "enforcement is client-side." It does not resolve whether
OpenRouter honours `zdr:true` when a fallback provider is used; that residual is
on OpenRouter's side and is addressed by the D4 provider deny-list.

### Spend — cost authorization

Quota is metered in **tokens**; the bill is in **dollars**. Model choice and
several sibling fields are therefore spending decisions, and each is refused:

| Field | Why it is refused |
|---|---|
| `model` outside the allow-list | $/token varies by orders of magnitude |
| `model` absent | previously a fail-open — the check was skipped entirely |
| `models` | fallback array can name arbitrary alternatives |
| `plugins` | billed per use, invisible to a token quota |
| `transforms` | provider-side processing outside model pricing |
| `provider.only` / `order` / `sort` | cost steering — measured 3.1x variance between providers of one model |

`provider.ignore` is **deliberately still accepted**: it is what D4 uses to
exclude a provider, and narrowing where traffic may go is the safe direction.

### Spend — per-request token ceiling

`min(reserved + remaining quota headroom, 65536)`, enforced **mid-stream**.
Crossing it kills the stream, emits a terminal
`{"error":"budget_exceeded","truncated":true}` chunk so the client can tell
throttling from a dropped connection, and **charges** the request rather than
refunding it.

This is the only control that bounds spend when a caller declares no
`max_tokens` — as the shipped daemon does not. Admission control alone let one
request run to the provider's own, far larger, default ceiling.

It is a **circuit breaker, not an accountant.** Enforcement uses two independent
envelope-only bounds, never a content read:

- a chunk **count** against the token ceiling — one SSE chunk carries one delta,
  so the count is the token proxy;
- a raw **byte** guard at `ceiling × 512`, catching a provider that batches an
  answer into a few huge chunks — the shape counting cannot see.

Exact accounting still comes from the terminal usage chunk. **Known residual:** a
provider batching N tokens per chunk undercounts the token bound by N, so the
ceiling fires late — the safe direction, still bounded by the byte guard and by
quota.

### Spend — quota and rate limiting

- **Per-user metering.** Tokens are reserved atomically *before* the forward and
  reconciled after, with a durable outbox and a crash-recovery sweep
  (`QUOTA_RESERVATION_DESIGN.md` §5(e)).
- **Request rate and in-flight caps**, per authenticated key, plus pre-auth
  per-source and global buckets.
- **Token-volume rate limiting** per key. Two requests per second is a trivial
  request rate and an unbounded *spend* rate, which the request limiter cannot
  see. Charged post-hoc from real usage into a debt-capable bucket, so an overrun
  is paid off by the next request rather than being free.

> **Limitation, stated rather than buried:** rate and in-flight limiters are
> **in-memory and therefore per-instance**. Across N replicas the effective
> ceiling is N times these values. Quota itself (`reserve_usage`) is atomic in
> Postgres and *is* correct across replicas.

### Logging

Request and response **bodies are never logged**, at any point. Only method and
path, upstream status code, the authenticated key id, token counts, and — if
visible in-flight in the stream — the serving provider name. At most the first 8
characters of a caller-supplied key ever appear in a log line.

---

## Error reference

| Status | Code | Meaning |
|---|---|---|
| `400` | — | Body unreadable or over the 4 MB cap |
| `401` | — | Missing, malformed, unknown, or inactive Mochiii key |
| `403` | `malformed_request` | Body is not parseable JSON |
| `403` | `model_required` | No `model` field |
| `403` | `model_not_allowed` | Model outside the allow-list |
| `403` | `cost_surface_not_allowed` | `models`, `plugins`, `transforms`, or `provider.only`/`order`/`sort` |
| `403` | `zdr_required` | ZDR routing flags missing or weakened |
| `403` | `stream_required` | `stream` absent or `false` |
| `403` | `max_tokens_too_large` | Declared `max_tokens` above the reservation cap |
| `405` | — | Method other than `POST` |
| `429` | `rate_limited` | Throttled; `scope` is one of `global`, `source`, `key_rate`, `token_rate`, `in_flight` |
| `429` | `quota_exceeded` | Reservation would exceed the key's `token_limit` |
| `502` | — | Upstream unreachable or the request could not be built |
| *(in-stream)* | `budget_exceeded` | Per-request token ceiling crossed; stream truncated |

`429` responses carry a `Retry-After` header. A throttled caller is always told
so explicitly rather than seeing a silent drop.

---

## Configuration

All configuration is environment-only. Nothing is read from a file.

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `OPENROUTER_API_KEY` | **yes** | — | The OpenRouter key. Fatal if unset. Never logged or returned. |
| `SUPABASE_URL` | **yes** | — | Key lookup and quota RPCs. Unset ⇒ every request `401`. |
| `SUPABASE_SERVICE_ROLE_KEY` | **yes** | — | As above. Sent as `apikey` only — never `Authorization`. |
| `OPENROUTER_API_BASE` | no | `https://openrouter.ai/api/v1` | Upstream override. |
| `PORT` | no | `8080` | Railway sets this automatically. |
| `ALLOWED_MODELS` | no | the shipped tier set | Comma-separated allow-list. **Empty disables the check** and is warned about loudly at startup. |
| `HEALTH_EXPOSE_COMMIT` | no | unset | `1` includes the build commit in `/health`. |
| `RAILWAY_GIT_COMMIT_SHA` | no | `unknown` | Set by Railway's build; reported via `/health` when exposed. |

Both Supabase variables are marked required because the proxy **fails closed**
without them: it still starts and serves `/health`, but rejects every completion
request `401`. That is deliberate — a misconfigured deploy must not become an
unauthenticated one.

---

## Running it

### Locally

```bash
cd proxy
export OPENROUTER_API_KEY=sk-or-v1-...
export SUPABASE_URL=https://<project>.supabase.co
export SUPABASE_SERVICE_ROLE_KEY=sb_secret_...
go run .
```

Listens on `http://localhost:8080`.

### Build

```bash
cd proxy && go build .
```

### Docker

```bash
cd proxy
docker build -t mochiii-proxy .
docker run -p 8080:8080 \
  -e OPENROUTER_API_KEY=sk-or-v1-... \
  -e SUPABASE_URL=... -e SUPABASE_SERVICE_ROLE_KEY=... \
  mochiii-proxy
```

The image runs as a **non-root user**. The proxy binds a high port, reads config
from the environment, and writes nothing to disk, so it needs no privilege.

**Railway:** set the service root directory to `proxy/` and supply the secrets as
environment variables (never commit them). Railway provides `PORT`.

### Pointing the daemon at it

```bash
MOCHIII_API_BASE=http://localhost:8080/v1
MOCHIII_USE_PROXY=true
MOCHIII_PROXY_KEY=mochi_...
# MOCHIII_API_KEY left unset -- the proxy adds its own key
```

`MOCHIII_USE_PROXY` is **required** and is not cosmetic: it is the switch
that makes the daemon take its credential from `MOCHIII_PROXY_KEY` instead
of `MOCHIII_API_KEY`, and the daemon refuses to start if the Mochiii key is
then empty. Without it the daemon sends no `Authorization` header at all and the
proxy rejects every request `401`. It also changes the startup log line to an
explicit "proxy mode" message, so an operator can tell *intentionally proxied*
from *forgot to set the key* at a glance.

Setting `MOCHIII_API_KEY` as well is a misconfiguration the daemon warns
about — the key would be sent to the proxy needlessly, and the proxy neither
wants nor uses it.

**Direct mode is unaffected.** Leaving `MOCHIII_API_BASE` pointed at
OpenRouter with `MOCHIII_API_KEY` set works exactly as before;
`MOCHIII_USE_PROXY` unset is a no-op.

From the repo root, [`run-proxy.sh`](../run-proxy.sh) does all of the above,
including defaulting to the production proxy and refusing to start on a missing
Mochiii key.

---

## Design notes

### Reject, never rewrite

Every gate **refuses** a non-conforming request rather than correcting it. The
proxy could stamp the right ZDR flags onto a body that lacks them, or clamp an
oversized `max_tokens` — and both were considered and rejected
([F1_ENFORCEMENT_DESIGN.md](F1_ENFORCEMENT_DESIGN.md)). Rewriting would forfeit
byte-for-byte forwarding, and a silently-corrected request teaches a caller
nothing. Clamping specifically caused a real bug: the reservation was clamped
while the caller's larger number was forwarded, so a request overshot its own
admission.

### Why keys are matched exactly

Go's `encoding/json` matches object keys to struct tags **ignoring case**;
OpenRouter matches exactly. Gates built on struct tags therefore read a *different
request* than the upstream does — and the difference split unsafe:
`{"PROVIDER":{"zdr":true,...}}` satisfied the ZDR gate while OpenRouter saw no
`provider` key at all and applied no ZDR routing.

Every body gate now looks keys up exactly, against a `map[string]json.RawMessage`
whose values are never inspected. A case-variant key is simply not found, so the
gate refuses — fail-closed.

### Zero third-party dependencies

`go.mod` has no `require` block. For the one internet-facing component of this
product that is a real supply-chain property, and it is worth more than the small
amount of hand-written limiter code it costs.

---

## What it deliberately does not do

**It does not scrub secrets out of message content, and cannot.**

The proxy never parses `messages` — that is precisely what makes it
ZDR-preserving. Secret scrubbing runs client-side in the daemon
([`daemon/scrub.go`](../daemon/scrub.go)) *before anything leaves the user's
machine*. Three limits follow, stated here rather than buried:

1. It matches a fixed set of high-confidence **prefixed** shapes — novel,
   obfuscated, or unprefixed secrets are missed.
2. It is **disableable** (`--no-scrub`).
3. **A caller that is not the shipped daemon gets no scrubbing at all.**

Closing that would require the proxy to read user code. That is a trade this
product has deliberately declined.

---

## Further reading

| Document | Covers |
|---|---|
| [`F1_ENFORCEMENT_DESIGN.md`](F1_ENFORCEMENT_DESIGN.md) | ZDR enforcement: Reject vs Stamp |
| [`../QUOTA_RESERVATION_DESIGN.md`](../QUOTA_RESERVATION_DESIGN.md) | Reservation, true-up, crash recovery |
| [`../SECURITY_MODEL.md`](../SECURITY_MODEL.md) | Supabase schema, RLS, and grants |
| [`migrations/`](migrations/) | `reserve_usage`, `apply_correction`, the outbox, grant revocations |
