# CodeTerminal — Walking Skeleton

This is the walking skeleton for CodeTerminal. It proves that one token can
travel the full path: **CLI client → local daemon → model API → stream back**,
over a Unix domain socket, with a versioned handshake on the wire.

It is deliberately minimal. Local workspace indexing and retrieval-augmented
generation are now wired into the live prompt path (see "Workspace indexing"
and "Live retrieval-augmented generation" below), but there is still no
guardrails, no billing, no auth beyond socket file permissions, and no
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
helper/          Go module: embedder helper subprocess (CGO confined here — see "Embedding helper process")
clients/tui/     Go module: thin CLI client (stands in for the future TUI)
clients/vscode/  stub — implemented in a later phase
mcp-servers/     stub — implemented in a later phase
testdata/        committed retrieval-quality eval set (sample code + queries) — see "Retrieval-quality eval set"
```

`go.work` at the repo root ties the `protocol`, `daemon`, `helper`, and
`clients/tui` modules together for local development.

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

## Workspace indexing (RAG plumbing)

The daemon binary doubles as a one-shot CLI for building and querying a
local, per-workspace vector index — separate from, and never triggered by,
the long-running serve path above:

```bash
./daemon/codeterminal-daemon index /path/to/workspace
./daemon/codeterminal-daemon retrieve --workspace /path/to/workspace --k 5 how does routing work
```

`index` walks the workspace confined to its root — it never follows
symlinks (regardless of whether their target is absolute or a relative
`../escape`) and never reads anything outside the resolved root — chunks
eligible files into overlapping ~40-line windows (10-line overlap), embeds
each chunk, and upserts them into a [chromem-go](https://github.com/philippgille/chromem-go)
collection persisted at `<workspaceRoot>/.codeterminal/index/`. It logs how
many files were scanned, how many were skipped and why, and how many chunks
were produced. The first `index` run on a workspace also appends
`.codeterminal/` to that workspace's `.gitignore` if it isn't already
there (idempotent — re-running never duplicates the line), and
`.codeterminal/` is itself pruned from every walk so the index never indexes
its own previous output.

Indexing hard-skips anything that could carry a secret — `.env`/`.env.*`,
`*.pem`, `*.key`, `id_rsa*`, `*.p12`, `.aws/`, `.ssh/`, and any filename
containing "secret" or "credential" — along with `.git/`, `node_modules/`,
`vendor/`, common build output directories, anything matched by a (simple,
root-level-only) subset of the workspace's `.gitignore`, binaries, and files
over 1MB. That exclusion is enforced by a single gate
(`shouldSkipFile` in `daemon/chunker.go`) called immediately before a file's
content is read, so there's no separate walk-time-only prune that could
diverge from what actually gets read.

`retrieve` embeds the query text and logs the top-k hits (file path, line
range, similarity score) — see [`daemon/index_cmd.go`](daemon/index_cmd.go).
The live prompt path reuses this exact function (`retrieveTopK`) to feed
retrieved chunks into the model automatically — see "Live
retrieval-augmented generation" below.

Embedding goes through the `Embedder` interface
([`daemon/embedder.go`](daemon/embedder.go)); nothing outside that file
depends on a concrete implementation. `Embedder` has two embedding methods,
not one, because BGE is asymmetric — `Embed` (documents, no prefix) and
`EmbedQuery` (queries, prefix applied) — plus `ID()`, used by the stale-index
guard below. Two implementations exist:

- **`BgeEmbedder`** ([`daemon/bge_embedder.go`](daemon/bge_embedder.go)) —
  the active one. It delegates to a running embedder helper subprocess (see
  "Embedding helper process" below); `Embed` sends chunk text unmodified,
  `EmbedQuery` prepends BAAI's documented instruction prefix
  (`"Represent this sentence for searching relevant passages: "`) first.
  The helper itself has no notion of this distinction — it's a dumb
  text-to-vector service; the asymmetry is applied entirely at this layer,
  right before the text leaves the daemon.
- **`PlaceholderEmbedder`** — the original local, deterministic hash-based
  vectorizer from the previous step. Still in the tree and still injectable
  in tests, but no longer the default for `index`/`retrieve`.

**Stale-index guard**: since both embedders report the same `Dim()` (384),
dimension alone can't tell a placeholder-built index apart from a real one.
`index` stamps `<indexDir>/embedder_stamp.json` with the active embedder's
`ID()` after every successful build
([`daemon/embedderstamp.go`](daemon/embedderstamp.go)); `retrieve` checks it
first and refuses — with a `"re-index required"` message, not silently
comparing incompatible vectors — on any mismatch, including a stamp that's
missing entirely (exactly what every index built before this guard existed
looks like).

Nothing in the indexing/retrieval path ever makes a network call outside of
what the embedder helper itself does (see below): chromem-go's collection is
created with an embedding function that refuses to run
(`refuseEmbeddingFunc` in `daemon/vectorstore.go`), so even a future bug that
left a chunk unembedded would fail loudly instead of silently calling
chromem-go's default OpenAI embedder.

chromem-go is pure Go with no dependencies of its own and requires no
separate server or CGO; its license (Mozilla Public License 2.0) is recorded
in [`THIRD_PARTY_LICENSES.txt`](THIRD_PARTY_LICENSES.txt).

## Live retrieval-augmented generation

Every prompt sent through the normal serve path (`handleConn` in
[`daemon/server.go`](daemon/server.go)) is automatically augmented with
relevant local code context before it reaches the model, reusing the exact
same `retrieveTopK` the CLI `retrieve` command calls — there is no
duplicated retrieval path. The daemon opens the embedder/index once at
startup (not per-request) via `setupRetrieval`
([`daemon/retrieval_setup.go`](daemon/retrieval_setup.go)), for the
workspace given by `--workspace` (default `.`).

**Request structure.** Retrieved chunks are never placed in the system
role. The system prompt (`daemon/prompts/system.txt`) stays static and
authoritative, loaded once at startup; only the `user` message changes,
wrapped in explicit, labeled delimiters
([`daemon/context.go`](daemon/context.go)):

```
<retrieved_context>
[1] path/to/file.go:10-25
... chunk content ...
</retrieved_context>

<user_request>
... the user's raw prompt, unmodified ...
</user_request>
```

A short paragraph appended to `system.txt` tells the model to treat
everything inside `<retrieved_context>` strictly as reference data, never
as instructions — retrieved code is untrusted (it's whatever text happens
to live in the user's workspace) and must never be able to redirect the
model's behavior.

**Delimiter injection.** Since chunk content is untrusted, a file could
contain a comment engineered to look like `</retrieved_context>`, trying to
forge an early close followed by a fake `<user_request>` of its own.
`neutralizeDelimiters` defuses this: it matches tag-like text tolerant of
case, whitespace (including newlines), and underscore/space variants inside
retrieved content, and replaces its angle brackets with visually similar
lookalike characters (`‹`/`›`) so the exact tag substring can never survive
into the rendered message — while leaving real code (`<-`, generics,
comparisons) completely untouched, since a match requires spelling out one
of the two exact tag names, however sloppily.

**Budget and degradation.** Injected context is capped by a character
budget (`retrieval.context_budget_chars` in `models.json`, default 8000 —
roughly 2000-2600 tokens for code, a small, conservative slice of any
modern context window; a character cap rather than a real token count, to
avoid pulling a tokenizer into the otherwise CGO-free, dependency-light
daemon module for what is only a soft safety cap). Chunks are ranked
best-first by the vector store; if they don't all fit, only the
lowest-ranked overflow is dropped — the top hit is never sacrificed to make
room for a lower-ranked one. Retrieval never blocks or fails a request: no
index yet, a stale/mismatched index, an embedder that failed to start, or a
transient store error all degrade to answering from the bare prompt, with a
clear one-line reason logged (`Server.gatherContext` in
`daemon/context.go`).

**Controls.** `--no-context` disables retrieval outright for the daemon's
lifetime; `retrieval.disabled: true` in `models.json` does the same from
config. Default is enabled either way — a `models.json` predating this
feature decodes to `disabled: false` automatically, so nothing needs
migrating. `--debug-context` additionally logs the full content of every
retrieved chunk. Every request logs a one-line summary
(`retrieval: chunks=N truncated=bool sources=[file:line-range, ...]`) or a
skip reason, e.g. `retrieval skipped: no index found at ...`.

## Embedding helper process

The real embedding model (`BAAI/bge-small-en-v1.5`, int8-quantized ONNX,
384 dims) runs via [ONNX Runtime](https://github.com/microsoft/onnxruntime)
through the [`onnxruntime_go`](https://github.com/yalue/onnxruntime_go)
binding, which needs CGO — and the daemon must stay pure Go. So it runs in a
separate binary, [`helper/`](helper/codeterminal-embedder-helper) (module
`codeterminal/helper`, command `codeterminal-embedder-helper`), which the
daemon spawns as a child process and talks to over a Unix domain socket
(loopback-only; no TCP, no network surface). `CGO_ENABLED=0 go build`
still succeeds on `daemon` — CGO is confined entirely to `helper` (`grep -r
'import "C"'` finds nothing outside it; the only CGO is inside
`onnxruntime_go` itself, which `helper` depends on).

`onnxruntime_go` loads the onnxruntime shared library with `dlopen` at
**runtime** rather than linking it at compile time (that's how it supports
Windows without MinGW) — so `go build` on `helper` succeeds regardless of
whether that library is present on the build machine. The failure
necessarily happens at helper **startup** instead, and
[`helper/main.go`](helper/main.go)'s `loadEmbedder` is written to make that
failure actionable (naming the missing path and the fix) rather than a raw
`dlopen` error.

Inference ([`helper/onnxembedder.go`](helper/onnxembedder.go)): tokenize
with [`sugarme/tokenizer`](https://github.com/sugarme/tokenizer) (pure Go,
loads `tokenizer.json` directly — **`addSpecialTokens` must be passed as
`true` explicitly**; `EncodeSingle` defaults it to `false` and silently
drops `[CLS]`/`[SEP]` otherwise, which would corrupt CLS pooling without
erroring), run the batch through `onnxruntime_go`, take position 0
(`[CLS]`) of the `last_hidden_state` output for each item, and L2-normalize
it. The helper has no concept of "query" vs "document" — it just embeds
whatever text it's given; see the `Embedder` section above for where that
distinction actually gets applied.

**Known platform gaps**: onnxruntime shared libraries are pinned (URL, size,
sha256, all verified against a real download — see `onnxRuntimePlatforms`
in [`daemon/onnxruntimefetch.go`](daemon/onnxruntimefetch.go)) for
`linux/amd64`, `darwin/arm64`, and `windows/amd64`. **Intel Mac
(`darwin/amd64`) is not supported** — upstream onnxruntime v1.26.0 ships no
prebuilt binary for that platform at all. This is a deliberate, open gap,
not an oversight: both `download-model` and the helper itself refuse with an
explicit "Intel Mac is not supported" error rather than a generic failure.
Since Intel Mac is a committed Phase 4 platform target, closing this gap
(most likely by building onnxruntime from source for `darwin/amd64`) is an
open decision for that phase. `linux/arm64` is also unpinned but not
committed anywhere yet; adding it follows the exact same pattern as the
three platforms already there.

[`daemon/helperproc.go`](daemon/helperproc.go)'s `HelperProcess` owns the
whole lifecycle:

- **Spawn + readiness**: starts the helper with `--socket`, `--model-dir`,
  and `--onnxruntime-lib` (scoped by the daemon's own PID, under the same
  runtime dir as its client-facing socket) and blocks until a real Health
  RPC succeeds — not until the helper's logs say so.
- **Health + restart**: if the helper dies unexpectedly, a monitor goroutine
  detects it and respawns it, bounded to a fixed number of attempts with a
  delay between each — so a helper that can never come back up causes a
  clear failure instead of a hot loop.
- **Clean shutdown**: `Stop` sends `SIGTERM`, escalates to `SIGKILL` if the
  helper hasn't exited within a grace period, and doesn't return until the
  process has actually been reaped — no orphaned child, no zombie.

Try it end to end (build both binaries and fetch the model first):

```bash
(cd helper && go build -o codeterminal-embedder-helper .)
(cd daemon && go build -o codeterminal-daemon .)
./daemon/codeterminal-daemon download-model
./daemon/codeterminal-daemon helper-smoketest "some test string"
```

This starts the helper, waits for it healthy, sends one real embedding
request, prints the returned vector's length (384) and first few values,
and shuts the helper down cleanly. See
[`daemon/helperproc_test.go`](daemon/helperproc_test.go) for the lifecycle
tests (spawn/ready/call, kill-and-restart, bounded restart policy, and
zero-orphan shutdown), all run against a test-only fixture helper under
[`daemon/testdata/fakehelper/`](daemon/testdata/fakehelper) rather than the
real binary — the fast unit-test path never needs the real model or CGO.

## Model acquisition

`codeterminal-daemon download-model` fetches the pinned BGE model and
tokenizer files (see `bgeModelAssets` in
[`daemon/modelfetch.go`](daemon/modelfetch.go)) into
`~/.codeterminal/models/bge-small-en-v1.5-int8/`, plus the onnxruntime
shared library for the current platform (see "Known platform gaps" above)
into `~/.codeterminal/models/onnxruntime-1.26.0/`, verifying every file's
size and sha256 before treating it as usable. Every URL and checksum is
pinned against a real, verified download — not a placeholder — so the
check is meaningful. A cache hit (everything already present and valid)
makes no network requests at all; a checksum or size mismatch removes the
bad file and fails loudly rather than silently using it.

## Retrieval-quality eval set

[`testdata/evalset/`](testdata/evalset) is a small, fixed sample codebase (8
single-responsibility Go files: config parsing, an HTTP handler, math
stats, an auth/token check, retry-with-backoff, an LRU cache, structured
logging, input validation) plus
[`testdata/queries.json`](testdata/queries.json) — 15 natural-language
queries phrased as intent, not keyword copies of the code, each mapped to
the file (and approximate function/line range) that should be the top hit.
`testdata/queries.json` deliberately lives *outside* `testdata/evalset/`:
indexing the eval set must not also index the query file itself, or a
query's own verbatim text becomes an unbeatable match against itself (a
contamination bug caught and fixed while building this eval set — moving
the file out of the indexed root was the fix).

[`daemon/eval_test.go`](daemon/eval_test.go) indexes `testdata/evalset/`
with the real `BgeEmbedder`, runs every query, and computes top-1 accuracy
and top-3 recall, printing the full per-query table (query, expected file,
top-1 hit, whether the expected file appeared in the top 3, score) so
quality is readable, not just pass/fail. It asserts
`top3Recall >= evalTop3RecallThreshold`, a single named, commented constant
(currently `0.80`) — tune it there, not by hand-picking queries. This test
needs the real model and CGO, so it's gated behind the `eval` build tag and
skipped in `-short` mode; `go test ./...` never compiles or runs it:

```bash
go test -tags eval -run TestEvalRetrievalQuality -v ./...
```

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

No skills DB, no guardrails, no billing, no VS Code extension code, no MCP
servers, no TCP, no auth tokens (Unix socket permissions are the security
boundary for now). `models.json` defines `ghost_text` and `reasoning`
tiers, but they are inert (`active: false`) — there is no tier-selection or
routing logic; every request uses the `default_tier` only. Edit blocks are
parsed and logged only — no writing to disk, no diff UI, no syntax
validation/gate, and no mid-stream parsing (parsing happens once the full
response has streamed back).

Workspace indexing and retrieval use the real `BgeEmbedder`, and retrieved
chunks are now wired into the live LLM prompt (see "Live
retrieval-augmented generation" above) — but there is still no auto-indexing
on daemon start or per-request (a missing index degrades gracefully rather
than triggering a build), no file-watching, no incremental re-index
(re-running `index` rebuilds chunk-by-chunk, keyed by a deterministic ID,
rather than diffing what changed), no re-ranking, no query rewriting, and no
multi-hop retrieval (a single retrieve feeds a single generate). Applying
edits to disk, a diff/confirm UI, and a syntax gate remain entirely
separate, later milestones — this step only augments generation.

The embedder helper is spawned only by explicit commands
(`index`/`retrieve`/`helper-smoketest`), never automatically on daemon
start or per-prompt. Intel Mac (`darwin/amd64`) has no pinned onnxruntime
library and is refused with an explicit error — see "Known platform gaps"
above; that's a real, open gap against the Phase 4 platform commitment, not
a nicety left for later.
