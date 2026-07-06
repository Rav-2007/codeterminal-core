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
clients/tui/     Go module: Mochiii, the interactive chat TUI (plus the original one-shot CLI path, kept for scripts)
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
an error. Parsing itself never writes to disk; the live chat path still
only parses-and-logs. Actually writing parsed edit blocks to disk is a
separate, deliberate command — see "Applying edits to disk" below.

## Applying edits to disk

`codeterminal-daemon edits apply [--workspace path] [<response-file>|-]`
takes a completed model response (from a file, or stdin if omitted/`-`),
parses it with the same unmodified `ParseEditBlocks`, and runs every block
through a safety tripod before anything touches disk — see
[`daemon/apply.go`](daemon/apply.go) and
[`daemon/apply_cmd.go`](daemon/apply_cmd.go). This is a deliberate, explicit
command; the live chat path never auto-applies edits.

For each block, in order:

1. **Path safety** — the target must resolve (through any symlinks) to
   inside the workspace root; absolute paths, `..` escapes, and files the
   indexer would secret-skip (`.env`, `*.pem`, etc. — the same
   `matchesSecretName` check `chunker.go` uses for indexing) are refused.
2. **Exact-match verification** — the block's `SEARCH` text must appear in
   the target file exactly once (literal substring match). Not found, or
   found more than once (ambiguous), and the edit is refused rather than
   guessed at.
3. **Syntax gate** — for `.go` files, the post-edit content is parsed with
   the stdlib `go/parser`; an edit that would make the file unparseable is
   refused. Other languages skip this check (logged as such); it's a guard
   against obviously-broken writes, not a type-checker.
4. **Diff + confirm** — the whole `SEARCH` block is shown as removed lines
   and the whole `REPLACE` block as added lines (file path + line range;
   no word-level diffing), then `Apply this edit? [y/N]:` is prompted.
   Only a literal `y`/`Y` applies; anything else (including empty input)
   skips that edit. Edits are independent — accept some, decline others in
   one run.
5. **Backup, then write** — the first time a file is written in a run, its
   pre-edit content is saved to
   `<workspace>/.codeterminal/backups/<timestamp>/before/<relpath>`
   (already covered by the existing `.codeterminal/` git-ignore and
   indexer-ignore rules, so backups are never indexed or committed). The
   file's content immediately after each write is also recorded, to
   `.../<timestamp>/after/<relpath>` — this is what makes `edits undo`'s
   safety check possible (below). Only after backing up does the real file
   get written.

A run ends with a summary line: `N applied, M skipped, K refused`.

**Restoring**: `codeterminal-daemon edits undo [--workspace path] [--session
ts] [--force]` restores a backup session (the most recent one, by default).
For each backed-up file, it compares the file's *current* on-disk content
against the `after/` snapshot recorded right after the apply run. Unchanged
files are restored silently. A file that has been modified since (hand-
edited further, or deleted) is never silently clobbered — it's listed as
guarded, and only restored if `--force` is passed or you confirm at an
interactive prompt naming exactly which files are affected.

New-file creation is out of scope: `ParseEditBlocks` already rejects an
empty `SEARCH`, so every block necessarily targets an existing file
containing that exact text.

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
over 1MB. It also hard-excludes a narrow, exact-basename noise list that can
never usefully answer a code question — `.gitignore` itself and dependency
lockfiles (`go.sum`, `package-lock.json`, `yarn.lock`, `pnpm-lock.yaml`,
`Cargo.lock`, `Gemfile.lock`, `composer.lock`, `poetry.lock`,
`Pipfile.lock`) — see `noiseBasenames`/`isNoiseFile` in
[`daemon/fileclass.go`](daemon/fileclass.go). This is the only category
that's excluded outright; every other file, including every doc, stays
indexed and is only down-weighted (see "Retrieval ranking" below). All of
this exclusion is enforced by a single gate (`shouldSkipFile` in
`daemon/chunker.go`) called immediately before a file's content is read, so
there's no separate walk-time-only prune that could diverge from what
actually gets read.

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
`ID()` and the current `IndexSchemaVersion` after every successful build
([`daemon/embedderstamp.go`](daemon/embedderstamp.go)); `retrieve` checks it
first and refuses — with a `"re-index required"` message, not silently
comparing incompatible vectors — on any mismatch, including a stamp that's
missing entirely (exactly what every index built before this guard existed
looks like). `IndexSchemaVersion` exists for the same reason as the embedder
ID check: it was bumped when chunks started carrying file-class metadata
(below), so an index built before that change — which has no `class` key in
its stored metadata at all — is caught and forced to re-index rather than
silently reading back every chunk as unclassified.

Nothing in the indexing/retrieval path ever makes a network call outside of
what the embedder helper itself does (see below): chromem-go's collection is
created with an embedding function that refuses to run
(`refuseEmbeddingFunc` in `daemon/vectorstore.go`), so even a future bug that
left a chunk unembedded would fail loudly instead of silently calling
chromem-go's default OpenAI embedder.

chromem-go is pure Go with no dependencies of its own and requires no
separate server or CGO; its license (Mozilla Public License 2.0) is recorded
in [`THIRD_PARTY_LICENSES.txt`](THIRD_PARTY_LICENSES.txt).

## Retrieval ranking (code vs. prose)

A real-repo stress test found that raw vector similarity alone systematically
let prose *about* code (READMEs, `daemon/prompts/system.txt`) outrank the
code that actually answers a code question — including one query where the
correct file didn't even make the raw top 5. The embedder itself was fine;
this is a ranking problem, fixed by tilting (not banning) results toward
code.

**Classification.** Every chunk is tagged at index time with a `FileClass` —
`code`, `doc`, `config`, or `other` — by extension/basename
(`classifyFile` in [`daemon/fileclass.go`](daemon/fileclass.go)), stored in
the chunk's metadata alongside its file path and line range.

**Re-ranking.** `retrieveTopK` fetches a wider raw candidate pool than `k`
(`max(k*6, 20)`, [`daemon/rerank.go`](daemon/rerank.go)) — reweighting only
the raw top-`k` could never recover a chunk ranked just outside it — then
combines each hit's raw similarity with a named class weight
(`weighted = raw * classWeight(class)`: code `1.15`, other `1.00`, config
`0.90`, doc `0.75`), re-sorts by that weighted score, and truncates to `k`.
A doc with high enough raw similarity can still win outright — this is a
tilt, not a hard exclusion (see "Workspace indexing" above for the one
category that *is* hard-excluded: `.gitignore` and lockfiles). A chunk
missing its stored class (e.g. one built by hand in a test) falls back to
recomputing it from the file path rather than being silently treated as
neutral by accident.

**Observability and A/B.** Every logged hit shows its class alongside both
scores (`class=code score=0.77 weighted=0.88`) — in the CLI `retrieve`
command's per-line output and the live path's per-request summary and
`--debug-context` dump. `retrieve --raw` and the daemon's `--no-rerank` flag
(or `retrieval.rerank_disabled: true` in `models.json`, same
defaults-enabled pattern as the other retrieval toggles) bypass re-ranking
entirely, returning raw similarity order, for direct comparison. Both
`retrieve` and the live prompt path share the exact same `retrieveTopK` —
there is no separate/duplicated ranking logic between them.

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

**Grounding visibility on the wire.** Every client that completes the
handshake and sends a `PromptRequest` — Mochiii, the one-shot CLI path, or
any future client — goes through this exact same `handleConn`; there is no
separate ungrounded path to opt into or out of. What used to be invisible
outside the daemon's own log is now also reported to the client itself:
before any tokens, `handleConn` sends one message carrying a
`protocol.GroundingInfo` (`Grounded`, the daemon's actual `Workspace`, a
`Reason` when ungrounded, `Chunks`, `Truncated`) built directly from the
same `retrievalOutcome` `gatherContext` already computes — no retrieval
logic is duplicated to produce it. `PromptRequest` also gained an optional
`Workspace` field: a client's own expectation of which repo it's grounding
against, purely so the daemon can flag `WorkspaceMismatch` when a client
expects a different workspace than this daemon instance actually has open
(grounding itself is still decided once, at daemon startup, by the
daemon's own `--workspace` — a client's `Workspace` never changes what's
retrieved). Both fields are additive and `omitempty`; older clients and
daemons that don't know them keep working unmodified.

## Skills database

A local, per-user SQLite database (`~/.codeterminal/skills.db`, not
per-workspace — skills are cross-project, unlike the RAG index) that
records "skills": successful solution steps, for later phases to build
reuse on. This is storage plumbing only — a `Skill` record
(`daemon/skills.go`) is `{ID, CreatedAt, Title, Steps, Tags, SourcePrompt}`,
with `AddSkill` / `ListSkills` / `GetSkill` / `DeleteSkill` on `SkillStore`.
It does **not** auto-capture skills from conversations and does **not**
inject them into prompts yet — both are later phases.

The driver is `modernc.org/sqlite`, a pure-Go, CGO-free SQLite
implementation (pinned to v1.39.0, the newest release that still only
requires `go 1.23.0` — newer releases bump that to `go 1.24`/`1.25` and
would force a toolchain upgrade). One connection is held open for the
store's lifetime (`SetMaxOpenConns(1)`, plus `PRAGMA journal_mode=WAL` and
`busy_timeout=5000` as a second line of defense) rather than reopening the
file per call. Schema is tracked via a `schema_meta.version` row, checked
and created on open (`ensureSchema`); no migrations exist yet since there's
only ever been one version.

```
codeterminal-daemon skills list [--limit N] [--json] [--db path]
codeterminal-daemon skills delete <id> [--db path]
```

`list` prints a table (or `--json`) newest-first; `--db` overrides the
default path. There is deliberately no `skills add` — skills get captured
by the agent loop in a later phase, not typed in by hand.

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

# 4. In another terminal, launch Mochiii — the interactive chat TUI
./clients/tui/codeterminal-tui

# ...or point it at a specific repo for grounding (default: current dir) —
# this only affects what Mochiii tells the daemon to cross-check against;
# the daemon's own --workspace at startup is what actually decides grounding
./clients/tui/codeterminal-tui --workspace ~/some/indexed/repo

# ...or keep using the original one-shot path (unchanged, for scripts/tests):
./clients/tui/codeterminal-tui --prompt "Say hello in five words."
echo "Say hello in five words." | ./clients/tui/codeterminal-tui
```

Stop the daemon with Ctrl-C (`SIGINT`) or `SIGTERM`; it removes its socket
and lockfile before exiting. If it's ever killed without a chance to clean
up (e.g. `SIGKILL`, a crash), the next `codeterminal-daemon` start detects
that nothing is listening on the leftover socket file, removes it, and binds
a fresh one automatically.

## Mochiii (interactive chat TUI)

`clients/tui/codeterminal-tui`, run with no `--prompt` flag from an actual
terminal (no piped stdin), launches **Mochiii**: an interactive, streaming
chat UI built on [Bubble Tea](https://github.com/charmbracelet/bubbletea)
(plus [bubbles](https://github.com/charmbracelet/bubbles) for the
textinput/viewport/spinner components and
[lipgloss](https://github.com/charmbracelet/lipgloss) for styling). Passing
`--prompt`, or piping text on stdin, keeps the original one-shot behavior
completely unchanged (connect, send one prompt, print the streamed answer,
exit) — that path is what scripts and tests still use.

**Splash → chat.** On launch, a pink ASCII lotus + the "Mochiii" wordmark +
a tagline fill the screen; any keypress dismisses it into the chat view,
which keeps only a small `✿ Mochiii` glyph in a one-line header (the full
logo would waste vertical space during a conversation). `✿` is the default
header glyph rather than the LOTUS emoji — emoji render inconsistently
across terminals/fonts and often ignore foreground color; `lotusGlyphEmoji`
in `clients/tui/logo.go` is kept as a one-line swap if you'd rather use it.
The header also shows connection/streaming state (teal) — `idle`, a
spinner + `sending…`, a spinner + `streaming…`, or `error: ...` in red —
and a hint line under the input: `enter to send · ctrl+c to quit · no chat
memory yet (each message is independent)`. That memory disclaimer is
deliberate, not decoration — see below.

**Palette** (`clients/tui/styles.go`): pink (`lipgloss.Color("205")`) for
the logo, brand name, and the user's own prompts; teal (`"44"`, pink's
complement) for accents and the streaming indicator; soft gray (`"252"`)
for assistant answers; red (`"203"`) for errors. All in named `lipgloss.Style`
variables in one file, so the look is a one-file edit. The lotus itself
(`clients/tui/logo.go`) is a single raw-string constant — hand-edit it
freely; `TestLotusLogo_RowsAreSymmetric` (`clients/tui/logo_test.go`) checks
that every row still mirrors correctly around its own center after a change.

**Streaming without blocking the UI.** Bubble Tea's `Update` function never
touches the network directly. Sending a prompt starts a goroutine
(`clients/tui/stream.go`) that opens a connection (reusing the exact same
`connectToDaemon` handshake/transport as the one-shot path — see
`clients/tui/daemonconn.go`), and pushes a message onto a channel for every
token, plus one final done-or-error message. A Bubble Tea `Cmd` waits on
that channel and is re-issued after every token, so `Update` only ever
handles one already-arrived message at a time and the UI (scrolling,
quitting, the spinner) never freezes while a response is streaming in.

**Cancellation.** Quitting (`ctrl+c` / `esc`) mid-stream cancels a
`context.Context` tied to that turn. A `context.Context` can't by itself
interrupt an in-flight, blocked socket read, so a small watcher goroutine
closes the connection when the context is done — that's what actually
unblocks the pending read, so nothing is left behind reading a dead socket.
See `TestStreamPrompt_ContextCancelUnblocksBlockedRead`
(`clients/tui/stream_test.go`) for an end-to-end proof against a real
(fake) daemon that hangs mid-response.

**Retrieval grounding.** Mochiii's prompts already go through the daemon's
normal serve path — the same one described in "Live retrieval-augmented
generation" above — so an answer is grounded whenever the daemon it's
talking to was started with `--workspace` pointing at a built index; there
was never a separate ungrounded path to route around. What Mochiii adds is
visibility and a cross-check: a `--workspace` flag (default `.`, resolved
to an absolute path) sent with every prompt, purely so the daemon can
confirm it matches its own actual grounding workspace. The header shows
whichever the daemon reports for the current turn — `grounded ✓ N
chunk(s)`, `ungrounded (reason)`, or, if the daemon's real workspace
differs from what Mochiii expected, `⚠ grounded against <path>, not
<expected>`. This shows up as soon as the turn starts (the daemon sends it
before any tokens), not just after the answer finishes, and is cleared at
the start of each new turn rather than carried over from the last one.

**No conversational memory yet.** Each turn sends only its own prompt — the
wire protocol is one prompt per connection with no history field (see
"Transport" above), and this step doesn't change that. Mochiii doesn't
pretend otherwise: the help line says so, and nothing in the UI implies
context the model doesn't actually have.

**Daemon-down handling.** Before ever drawing the splash, `codeterminal-tui`
preflights the daemon connection; if the daemon isn't running or the
handshake fails, it prints one clear message to stderr and exits non-zero
instead of entering the alt-screen.

## Explicitly out of scope

No guardrails, no billing, no VS Code extension code, no MCP servers, no
TCP, no auth tokens (Unix socket permissions are the security boundary for
now). `models.json` defines `ghost_text` and `reasoning` tiers, but they
are inert (`active: false`) — there is no tier-selection or routing logic;
every request uses the `default_tier` only. Edit blocks are parsed and
logged only on the live chat path — no mid-stream parsing (parsing happens
once the full response has streamed back). Writing them to disk is now
possible, but only via the deliberate `edits apply`/`edits undo` commands
(see "Applying edits to disk" above): no auto-apply from the live chat
path, no fuzzy/approximate `SEARCH` matching, no multi-occurrence
disambiguation (ambiguous is always a refusal), and no TUI/VS Code UI for
reviewing diffs.

Mochiii (see "Mochiii (interactive chat TUI)" above) is a chat-only thin
slice: no retrieval-grounding controls in the UI (the daemon may still
retrieve under the hood — this step adds no UI for it), no apply-edits/
diff/`[y/N]` UI, no slash-commands, no conversation memory (each turn is
independent — the UI says so), no cross-session history, and no config or
theme screens.

There is now a skills database (see "Skills database" above), but it is
storage plumbing only: no auto-capture of skills from conversations or
agent runs, no injecting skills into prompts or generation, no
embedding/vector search over skills, no dedup/ranking intelligence, and no
sync/cloud — all later phases.

Workspace indexing and retrieval use the real `BgeEmbedder`, and retrieved
chunks are now wired into the live LLM prompt (see "Live
retrieval-augmented generation" above), with file-class re-ranking (see
"Retrieval ranking" below) — but there is still no auto-indexing on daemon
start or per-request (a missing index degrades gracefully rather than
triggering a build), no file-watching, no incremental re-index (re-running
`index` rebuilds chunk-by-chunk, keyed by a deterministic ID, rather than
diffing what changed), no query rewriting, and no multi-hop retrieval (a
single retrieve feeds a single generate). Applying edits to disk, a
diff/confirm UI, and a syntax gate remain entirely separate, later
milestones — this step only augments generation.

The embedder helper is spawned only by explicit commands
(`index`/`retrieve`/`helper-smoketest`), never automatically on daemon
start or per-prompt. Intel Mac (`darwin/amd64`) has no pinned onnxruntime
library and is refused with an explicit error — see "Known platform gaps"
above; that's a real, open gap against the Phase 4 platform commitment, not
a nicety left for later.
