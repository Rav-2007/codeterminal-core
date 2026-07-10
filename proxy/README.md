# codeterminal-proxy

Step 1 of the managed-proxy build: the smallest possible pass-through HTTP proxy in
front of OpenRouter. It holds the OpenRouter API key server-side and streams
inference requests through. No auth, no metering, no database yet -- those are
later steps, deliberately not built here.

## What it does

- `POST /v1/chat/completions` -- forwards the request body **as-is** to
  `https://openrouter.ai/api/v1/chat/completions` (or `OPENROUTER_API_BASE`),
  adding `Authorization: Bearer <key>` server-side. The payload (including the
  daemon's ZDR `provider` routing object) is never parsed or altered. The
  response is streamed back (SSE) as it arrives -- never buffered.
- `GET /health` -- returns `200 ok`.
- Never logs request or response bodies. Only method/path, upstream status
  code, and (if visible in-flight in the SSE stream) the serving provider
  name are logged -- never message content. This is what keeps the proxy
  zero-data-retention-preserving.

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

The daemon already omits the `Authorization` header whenever
`CODETERMINAL_API_KEY` is empty, so no request-path code changes were needed for
this -- only a config value pointing at a different base URL.
`CODETERMINAL_USE_PROXY` is optional and purely cosmetic: it changes the
daemon's startup log line from a "no API key configured" warning to an
explicit "proxy mode" message, so an operator can tell "intentionally proxied"
apart from "forgot to set the key" at a glance. Leaving `CODETERMINAL_API_BASE`
pointed at OpenRouter directly (with `CODETERMINAL_API_KEY` set, as before) is
completely unaffected -- direct mode still works unchanged.

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

## Deliberately NOT built yet

- **Auth on this proxy's own endpoint.** Anyone who can reach this port can use
  it today. Fine for local/private testing; not safe to expose publicly
  without an auth layer added in a later step.
- **Per-user metering/billing.**
- **Any persistence/database.**
