# codeterminal-proxy

The managed proxy in front of OpenRouter. It holds the OpenRouter API key
server-side and streams inference requests through. It authenticates every
caller by a per-user Mochiii key (Supabase-backed), meters usage against a
per-key quota, rate-limits, enforces a model allow-list, and enforces the
zero-data-retention routing flags. It began as a bare pass-through with none of
that; the sections below describe what it does now.

## What it does

- `POST /v1/chat/completions` -- forwards the request body **as-is** to
  `https://openrouter.ai/api/v1/chat/completions` (or `OPENROUTER_API_BASE`),
  adding `Authorization: Bearer <key>` server-side. The payload (including the
  daemon's ZDR `provider` routing object) is never parsed or altered. The
  response is streamed back (SSE) as it arrives -- never buffered.
- `GET /health` -- returns `200 ok`.
- Never logs request or response bodies. Only method/path, upstream status
  code, and (if visible in-flight in the SSE stream) the serving provider
  name are logged -- never message content. So the proxy's own logging never
  undermines the ZDR posture.
- **ZDR enforcement (F1).** The proxy rejects -- fail closed, `403
  {"error":"zdr_required"}` -- any request whose `provider` routing object is
  missing, malformed, or weakened (`zdr` not `true`, or `data_collection` not
  `"deny"`), before anything is reserved or forwarded. The proxy, not the
  client, is the authority that the flags are correct on the wire. The check is
  a read-only one-field peek, so the accept path still forwards the caller's
  body byte-for-byte and message content is never parsed. It only ever fires
  for a caller other than the shipped daemon, which always sends the flags.
  **Scope:** this closes "enforcement is client-side". It does not resolve
  whether OpenRouter honors `zdr:true` when a fallback provider is used -- that
  residual is on OpenRouter's side and is addressed by the D4 provider
  deny-list, not here.
- **Cost authorization beyond the model field.** 403
  `{"error":"cost_surface_not_allowed"}` for any request carrying a billable
  side-channel the token quota cannot see: a `models` fallback array, `plugins`
  (billed per use), `transforms`, or the cost-steering `provider.only` /
  `order` / `sort`. `provider.ignore` is deliberately still accepted -- it is
  what D4 uses to exclude a provider, and narrowing where traffic may go is the
  safe direction. A body with no `model` at all is refused (`model_required`)
  rather than waved through, which was a fail-open.
- **A per-request token ceiling, enforced mid-stream.** `min(reserved +
  remaining quota headroom, 65536)`. Crossing it kills the stream, emits a
  terminal `{"error":"budget_exceeded","truncated":true}` chunk so the client
  can tell throttling from a dropped connection, and **charges** the request
  rather than refunding it. This is the only control that bounds spend when a
  caller declares no `max_tokens`, as the shipped daemon does not -- admission
  control alone let one request run to the provider's own (far larger) default
  ceiling. It is a circuit breaker, not an accountant. Enforcement uses two
  INDEPENDENT envelope-only bounds, never a content read: a chunk COUNT against
  the token ceiling (one SSE chunk carries one delta, so the count is the token
  proxy), and a raw BYTE guard at `ceiling * 512` that catches a provider
  batching an answer into a few huge chunks -- the shape counting cannot see.
  They are deliberately not fused into one number: an earlier version divided
  accumulated line length by 4 and took the max, which measured the repeated
  ~294-byte JSON envelope rather than the token inside it, scored 73x high, and
  would have truncated every completion past ~900 tokens. Known residual: a
  provider batching N tokens per chunk undercounts the token bound by N, so the
  ceiling fires late -- the safe direction, still bounded by the byte guard and
  by quota. Exact accounting still comes from the terminal usage chunk.
- **Streaming is required.** 403 `{"error":"stream_required"}` for a request that
  sets `stream:false` or omits it. The budget ceiling can only be enforced on a
  stream; a buffered completion was bounded only by the 16MB response cap, a 64x
  escape. Refusing keeps the ceiling enforceable on 100% of forwarded traffic.
  The buffered response branch remains for upstream *error* bodies, which arrive
  as `application/json` regardless.
- **A declared `max_tokens` above the reservation cap is refused**, 403
  `max_tokens_too_large`, rather than silently clamped. Clamping the
  reservation while forwarding the caller's larger number was the mismatch that
  let a request overshoot its own admission.
- **Token-volume rate limiting** per key, alongside the request-rate limit --
  the bill is denominated in tokens, and two requests per second is a trivial
  request rate and an unbounded spend rate. Charged post-hoc from real usage
  into a debt-capable bucket, so an overrun is paid off by the next request
  rather than being free.

## Environment variables

- `OPENROUTER_API_KEY` (required) -- the OpenRouter key. Set only in the
  server's environment (a Railway secret in production, a shell export
  locally). Never read from a file, never hardcoded, never logged.
- `OPENROUTER_API_BASE` (optional, default `https://openrouter.ai/api/v1`)
- `PORT` (optional, default `8080`; Railway sets this automatically)

## Run locally

```
cd proxy
export OPENROUTER_API_KEY=sk-or-v1-...   # your real (or test) OpenRouter key
go run .
```

Listens on `http://localhost:8080`.

## Point the daemon at it

The daemon reads `CODETERMINAL_API_BASE` / `CODETERMINAL_API_KEY` from its own
environment. To route through this proxy instead of OpenRouter directly:

```
CODETERMINAL_API_BASE=http://localhost:8080/v1
CODETERMINAL_USE_PROXY=true
# CODETERMINAL_API_KEY left unset -- the proxy adds its own key
```

`CODETERMINAL_USE_PROXY` is **required** for this path, and is not cosmetic: it
is the switch that makes the daemon take its credential from
`CODETERMINAL_MOCHIII_KEY` instead of `CODETERMINAL_API_KEY`, and the daemon
refuses to start (`Fatal`) if the Mochiii key is then empty. Without it the
daemon sends no `Authorization` header at all and the proxy rejects every
request with 401. It also changes the startup log line to an explicit "proxy
mode" message, so an operator can tell "intentionally proxied" apart from
"forgot to set the key" at a glance.

Setting `CODETERMINAL_API_KEY` as well is a misconfiguration the daemon warns
about: the key would be sent to the proxy needlessly, and the proxy neither
wants nor uses it.

Leaving `CODETERMINAL_API_BASE` pointed at OpenRouter directly (with
`CODETERMINAL_API_KEY` set, as before) is completely unaffected -- direct mode
still works unchanged, and `CODETERMINAL_USE_PROXY` unset is a no-op.

From the repo root, [`run-proxy.sh`](../run-proxy.sh) does all of the above --
including defaulting to the production proxy and refusing to start on a missing
Mochiii key.

## Build

```
cd proxy
go build .
```

## Docker (Railway deploys from this Dockerfile)

```
cd proxy
docker build -t codeterminal-proxy .
docker run -p 8080:8080 -e OPENROUTER_API_KEY=sk-or-v1-... codeterminal-proxy
```

Railway: set the service's root directory to `proxy/`, and set
`OPENROUTER_API_KEY` as a Railway environment variable/secret (never commit it).
Railway supplies `PORT` automatically.

## Built since

- **Auth on this proxy's own endpoint.** Every route except `/health` requires
  a valid per-user Mochiii key, looked up in Supabase. It fails closed: with
  `SUPABASE_URL`/`SUPABASE_SERVICE_ROLE_KEY` unset, every request is rejected
  401 rather than served unauthenticated.
- **Per-user metering.** Token usage is reserved before the forward and
  reconciled after, with a durable outbox and a crash-recovery sweep
  (`QUOTA_RESERVATION_DESIGN.md` §5(e)). Over-quota requests get 429
  `quota_exceeded`.
- **Rate limiting and in-flight caps**, both pre-auth (per-source and global,
  so a bad-key flood cannot amplify into Supabase) and per authenticated key.
- **A model allow-list** as cost authorization -- 403 `model_not_allowed` for a
  model outside the shipped tier set (`ALLOWED_MODELS` overrides; empty
  disables the check and is warned about loudly).
- **ZDR enforcement**, **cost-surface refusal**, the **mid-stream budget
  ceiling**, and **token-volume limiting** -- see above.
- **Every unregistered path is rate-limited too.** Unknown routes used to 404
  straight out of the mux without passing through the pre-auth limiter at all,
  which made them an unauthenticated, entirely unthrottled endpoint.
- **Runs as a non-root user** in the container. The proxy binds a high port,
  reads its config from the environment, and writes nothing to disk, so it
  needs no privilege at all.

## What it deliberately does NOT do

**It does not scrub secrets out of message content, and cannot.** The proxy
never parses `messages` -- that is exactly what makes it zero-data-retention-
preserving rather than a relay that happens to see everything. Secret scrubbing
runs client-side in the daemon (`daemon/scrub.go`) *before anything leaves the
user's machine*. Three limits follow from that, and they are stated here rather
than buried: the scrubber matches a fixed set of high-confidence **prefixed**
shapes, so novel, obfuscated, or unprefixed secrets are missed; it is
**disableable** (`--no-scrub`); and **a caller that is not the shipped daemon
gets no scrubbing at all**. Closing that would require the proxy to read user
code, which is a deliberate trade this product has decided against.
