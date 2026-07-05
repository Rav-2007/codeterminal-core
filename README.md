# CodeTerminal — Walking Skeleton

This is the walking skeleton for CodeTerminal. It proves that one token can
travel the full path: **CLI client → local daemon → model API → stream back**,
over a Unix domain socket, with a versioned handshake on the wire.

It is deliberately minimal. There is no RAG, no vector DB, no model routing,
no guardrails, no billing, no auth beyond socket file permissions, and no
network listener anywhere. Those are out of scope until this path is proven
and reviewed.

## What this proves

- The daemon starts, binds a Unix domain socket with owner-only permissions
  (`0600`), and advertises its location via a lockfile.
- A thin CLI client discovers the daemon via that lockfile, connects, and
  performs a `protocol_version` handshake before sending anything else.
- The daemon calls a real OpenAI-compatible `/v1/chat/completions` endpoint
  with `stream: true` and forwards each token to the client as it arrives —
  no buffering of the full response on either side.
- A crashed daemon leaves no stale socket/lockfile that blocks the next
  startup.

## Repo layout

```
protocol/        shared Go package: wire message types + version handshake
daemon/          Go module: long-running background process (the "server")
clients/tui/     Go module: thin CLI client (stands in for the future TUI)
clients/vscode/  stub — implemented in a later phase
mcp-servers/     stub — implemented in a later phase
```

`go.work` at the repo root ties the `protocol`, `daemon`, and `clients/tui`
modules together for local development.

## Transport

Unix domain socket only. No TCP, no listening network port. The daemon
picks a socket path under `$XDG_RUNTIME_DIR/codeterminal/` (falling back to
the OS temp dir if that's unset), creates it with `0600` permissions, and
writes a JSON lockfile (`daemon.lock`) next to it recording the socket path
and PID. The client reads that lockfile to find the socket — there is no
other discovery mechanism and nothing is guessed.

Messages on the socket are newline-delimited JSON, one JSON object per line,
in this order:

1. Client → daemon: `HandshakeRequest`
2. Daemon → client: `HandshakeResponse` (connection is closed here if
   `protocol_version` doesn't match)
3. Client → daemon: `PromptRequest`
4. Daemon → client: a stream of `TokenResponse` messages, ending with one
   that has `done: true` (or `error` set if the call failed)

See [`protocol/protocol.go`](protocol/protocol.go) for the exact structs.

## Configuration

Credentials and model selection are configured from two different places,
deliberately kept apart:

| Variable                 | Meaning                                              | Example                          |
|---------------------------|-------------------------------------------------------|-----------------------------------|
| `CODETERMINAL_API_BASE`   | Base URL of an OpenAI-compatible API                  | `https://api.together.xyz/v1`    |
| `CODETERMINAL_API_KEY`    | API key sent as `Authorization: Bearer <key>`         | `sk-...`                          |

`CODETERMINAL_API_BASE` is required; the daemon refuses to start without it.
`CODETERMINAL_API_KEY` may be left unset for local OpenAI-compatible servers
that don't require one. Neither is ever read from a config file.

Copy [`.env.example`](.env.example) to `.env`, fill in real values, and load
it into your shell before starting the daemon — the daemon only reads plain
environment variables, it does not parse `.env` files itself:

```bash
cp .env.example .env   # then edit .env with real values
set -a && source .env && set +a
```

`.env` is listed in `.gitignore` and must never be committed.

The **model slug** comes from [`models.json`](models.json) instead of an
env var, since it's not a secret and is useful to have under version
control. It defines named tiers, each with a slug and an `active` flag:

```json
{
  "config_version": 1,
  "default_tier": "primary",
  "tiers": {
    "primary":    { "slug": "qwen/qwen3-coder-next", "active": true,  "note": "..." },
    "ghost_text": { "slug": "...",                    "active": false, "note": "..." },
    "reasoning":  { "slug": "...",                    "active": false, "note": "..." }
  }
}
```

Every request uses the `default_tier`'s slug — there is no routing logic yet.
The other tiers are parsed and held in memory but never called; they exist
as the seam a future router will attach to. The daemon fails fast at startup
with a specific error if the config is missing, malformed, the `default_tier`
doesn't exist, isn't `active`, or has an empty slug (the same rule applies to
any other tier marked `active`).

By default the daemon reads `./models.json`. Override the path with
`--config`, or override the resolved slug outright with `--model` (testing
only — the config file remains the source of truth):

```bash
./daemon/codeterminal-daemon --config /path/to/models.json --model some/other-slug
```

## System prompt and edit blocks

The daemon loads [`daemon/prompts/system.txt`](daemon/prompts/system.txt) at
startup (default path; override with `--system-prompt`) and prepends it as
the `system` message on every request. It contains no secrets and is plain
text, so it's reviewable and editable independently of the code.

That prompt instructs the model to propose code changes as one or more
SEARCH/REPLACE edit blocks instead of rewriting whole files:

```
path: relative/path/to/file.go
<<<<<<< SEARCH
(exact existing lines to find)
=======
(replacement lines)
>>>>>>> REPLACE
```

After a response finishes streaming, the daemon parses it with
[`daemon/editblock.go`](daemon/editblock.go) and logs a summary — e.g.
`parsed 1 edit block(s)` plus each block's target path and line counts. A
response with no edit blocks (a plain answer) parses to an empty list, not
an error. **Nothing is applied to disk yet** — this step only produces and
parses the format; applying edits and validating them are later phases.

## Tier router

[`daemon/router.go`](daemon/router.go) decides which `models.json` tier
handles each request and resolves its slug. It ships **primary-only**:
every normal request logs `route tier=primary slug=... reason=default` and
is unaffected by the router's existence. The `reasoning` tier is wired but
gated — `Route` only selects it when a request carries a genuine non-zero
exit signal *and* `reasoning` is `active` in config, and today's request
path never sets that signal (capturing a real exit code is a later,
client-side phase). If the escalation target is inactive or missing, the
router falls back to the default tier rather than erroring or silently
calling something inactive. Ghost-text is untouched and not selectable
here. See [`daemon/router_test.go`](daemon/router_test.go) for the cases
this guarantees.

## Run it

Requires Go 1.23+.

```bash
# 1. Configure credentials — either via .env (see Configuration above)...
set -a && source .env && set +a
# ...or directly (never commit real keys):
export CODETERMINAL_API_BASE="https://api.together.xyz/v1"
export CODETERMINAL_API_KEY="sk-..."

# The model slug comes from models.json (repo root) — edit it there, not via env var.

# 2. Build both binaries
(cd daemon && go build -o codeterminal-daemon .)
(cd clients/tui && go build -o codeterminal-tui .)

# 3. Start the daemon in one terminal (it runs in the foreground; logs go to stderr)
./daemon/codeterminal-daemon

# 4. In another terminal, send a prompt and watch tokens stream back
./clients/tui/codeterminal-tui --prompt "Say hello in five words."

# ...or pipe a prompt in on stdin:
echo "Say hello in five words." | ./clients/tui/codeterminal-tui
```

Stop the daemon with Ctrl-C (`SIGINT`) or `SIGTERM`; it removes its socket
and lockfile before exiting. If it's ever killed without a chance to clean
up (e.g. `SIGKILL`, a crash), the next `codeterminal-daemon` start detects
that nothing is listening on the leftover socket file, removes it, and binds
a fresh one automatically.

## Explicitly out of scope

No RAG, no vector DB, no skills DB, no guardrails, no billing, no VS Code
extension code, no MCP servers, no TCP, no auth tokens (Unix socket
permissions are the security boundary for now). `models.json` defines
`ghost_text` and `reasoning` tiers, but they are inert (`active: false`) —
there is no tier-selection or routing logic; every request uses the
`default_tier` only. Edit blocks are parsed and logged only — no writing to
disk, no diff UI, no syntax validation/gate, and no mid-stream parsing
(parsing happens once the full response has streamed back).
