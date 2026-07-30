# CodeTerminal

**A local-first AI coding assistant.** Your codebase is indexed, embedded, and
retrieved entirely on your own machine. The only thing that leaves it is the
minimal prompt for a single inference turn.

A background daemon holds the index, the conversation memory, and the edit-safety
engine. Clients — **Mochiii** (the chat TUI), a VS Code extension, and a scriptable
CLI — connect to it over a Unix domain socket. There is no network listener
anywhere on your machine.

| | |
|---|---|
| **Language** | Go 1.25+ (plus TypeScript for the VS Code client) |
| **Transport** | Unix domain socket, `0600`, lockfile discovery — no TCP |
| **Retrieval** | On-device ONNX embeddings (BGE-small, 384d) + local vector store |
| **Inference** | Any OpenAI-compatible API, direct or through the managed proxy |
| **Edit safety** | Five gates: path, exact-match, syntax, confirm, backup |
| **Platforms** | linux/amd64, darwin/arm64, windows/amd64 ([Intel Mac is an open gap](#known-gaps)) |

---

## Contents

**Getting started**
- [Requirements](#requirements)
- [Quick start — direct mode](#quick-start--direct-mode)
- [Quick start — managed proxy](#quick-start--managed-proxy)
- [Command reference](#command-reference)

**Architecture**
- [Repo layout](#repo-layout)
- [How a prompt flows](#how-a-prompt-flows)
- [Transport](#transport)
- [Configuration](#configuration)

**Capabilities**
- [Retrieval](#retrieval)
- [Editing code](#editing-code)
- [Conversation memory](#conversation-memory)
- [Clients](#clients)

**Runtime components**
- [Embedding helper process](#embedding-helper-process)
- [Model acquisition](#model-acquisition)
- [Managed proxy](#managed-proxy)
- [Tier router](#tier-router)
- [Skills database](#skills-database)

**Development**
- [Building and testing](#building-and-testing)
- [Retrieval-quality eval set](#retrieval-quality-eval-set)

**Scope**
- [Known gaps](#known-gaps)
- [Out of scope](#out-of-scope)

---

# Getting started

## Requirements

- **Go 1.25+** — `go.work` and most modules declare `go 1.25.0`.
- **A C compiler** — only for the embedder helper, which needs CGO. The daemon
  and TUI are pure Go and build with `CGO_ENABLED=0`.

## Quick start — direct mode

Your own provider key leaves this machine and goes to the provider.

```bash
# 1. Credentials
cp .env.example .env          # then edit .env with real values
set -a && source .env && set +a

# ...or export directly (never commit real keys):
export CODETERMINAL_API_BASE="https://api.together.xyz/v1"
export CODETERMINAL_API_KEY="sk-..."

# 2. Build all three binaries. The helper is NOT optional — the daemon spawns
#    it to compute embeddings locally, and without it retrieval is disabled.
(cd helper      && go build -o codeterminal-embedder-helper .)
(cd daemon      && go build -o codeterminal-daemon .)
(cd clients/tui && go build -o codeterminal-tui .)

# 3. One-time: fetch the embedding model and onnxruntime library into
#    ~/.codeterminal/models/. A second run with everything present is a no-op.
./daemon/codeterminal-daemon download-model

# 4. Per repo: index the workspace you want answers grounded in.
#    Skipping this is NOT fatal — the daemon starts and answers ungrounded,
#    using no code from your repo. Re-run after changes; the index is a snapshot.
./daemon/codeterminal-daemon index .

# 5. Start the daemon (foreground; logs to stderr).
#    --workspace must be the directory you indexed in step 4 (default: cwd).
./daemon/codeterminal-daemon

# 6. In another terminal, launch Mochiii.
./clients/tui/codeterminal-tui
```

The model slug comes from [`models.json`](models.json), not an environment
variable — edit it there.

Stop the daemon with Ctrl-C (`SIGINT`) or `SIGTERM`; it removes its socket and
lockfile before exiting. If it is ever killed without a chance to clean up
(`SIGKILL`, a crash), the next start detects that nothing is listening on the
leftover socket, removes it, and binds a fresh one automatically.

## Quick start — managed proxy

The pilot path. Inference runs through a [managed proxy](#managed-proxy) that
holds the provider key server-side, meters usage per user, and enforces
zero-data-retention routing before anything reaches the provider. Your machine
then holds exactly one credential: a Mochiii key.

[`run-proxy.sh`](run-proxy.sh) is the whole path. It sources `.env`, points
`CODETERMINAL_API_BASE` at the production proxy, sets `CODETERMINAL_USE_PROXY=true`,
unsets `CODETERMINAL_API_KEY` so a provider key can never be sent to the proxy by
accident, refuses to start on a missing Mochiii key, and then `exec`s the daemon —
so every daemon flag passes straight through.

```bash
# 0. One-time: put your Mochiii key in .env (gitignored; never commit it)
cp .env.example .env
$EDITOR .env                  # set CODETERMINAL_MOCHIII_KEY=mochi_...

# 1. One-time: build the binaries and fetch the embedding model. The proxy
#    serves inference only — embeddings are still computed locally, so without
#    this the daemon starts but has no grounding.
(cd helper      && go build -o codeterminal-embedder-helper .)
(cd daemon      && go build -o codeterminal-daemon .)
(cd clients/tui && go build -o codeterminal-tui .)
./daemon/codeterminal-daemon download-model

# 2. Per repo: index it. Re-run after changes — a stale index makes the model
#    confidently describe the OLD shape of a file it "retrieved".
./daemon/codeterminal-daemon index ~/some/repo

# 3. Start the daemon in proxy mode, pointed at that same repo
./run-proxy.sh --workspace ~/some/repo

# 4. In another terminal, as usual
./clients/tui/codeterminal-tui --workspace ~/some/repo
```

The daemon logs `proxy mode: forwarding inference through ...` at startup — that
line is how you confirm the pilot path is actually in use.

The proxy URL is a baked-in default, not a secret. Point the script at a
different proxy (a local one, say) with `CODETERMINAL_PROXY_BASE`:

```bash
CODETERMINAL_PROXY_BASE=http://localhost:8080/v1 ./run-proxy.sh
```

`CODETERMINAL_API_BASE` deliberately does **not** do this: in proxy mode the
script sets it authoritatively and ignores whatever your shell or `.env` carried,
printing a note when it displaced a different value. That is what makes
`source .env` — whose `CODETERMINAL_API_BASE` is the direct-to-provider one —
safe to combine with this script. Otherwise your Mochiii key would be sent to the
provider, which cannot use it.

## Command reference

All commands are subcommands of the `codeterminal-daemon` binary.

| Command | Purpose |
|---|---|
| *(no subcommand)* | Run the daemon in the foreground |
| `index <path>` | Build the vector index for a workspace |
| `retrieve --workspace <p> --k N <query>` | Query the index and print top-k hits |
| `edits apply [--workspace p] [file\|-]` | Apply SEARCH/REPLACE blocks from a response |
| `edits undo [--workspace p] [--session ts] [--force]` | Restore a backup session |
| `skills list [--limit N] [--json] [--db p]` | List recorded skills, newest first |
| `skills delete <id> [--db p]` | Delete a skill |
| `download-model` | Fetch the pinned BGE model + onnxruntime library |
| `helper-smoketest <text>` | Spawn the helper, embed one string, shut down |

Key daemon flags:

| Flag | Effect |
|---|---|
| `--workspace <path>` | Which repo grounds answers (default: cwd) |
| `--config <path>` | Path to `models.json` (default: `./models.json`) |
| `--model <slug>` | Override the resolved slug (testing only) |
| `--system-prompt <path>` | Override the system prompt file |
| `--no-context` | Disable retrieval for the daemon's lifetime |
| `--debug-context` | Log the full content of every retrieved chunk |
| `--no-rerank` | Bypass class re-ranking, return raw similarity order |
| `--no-scrub` | Disable heuristic secret scrubbing |

---

# Architecture

## Repo layout

```
protocol/        shared Go package: wire message types + version handshake
editapply/       shared Go module: the SEARCH/REPLACE apply-edits engine
                 (parse, path-safety, exact-match, syntax gate, backup)
daemon/          Go module: the long-running background process
helper/          Go module: embedder helper subprocess (CGO confined here)
clients/tui/     Go module: Mochiii, the chat TUI (plus the one-shot CLI path)
clients/vscode/  TypeScript: the VS Code extension
proxy/           Go module: the managed-tier proxy in front of OpenRouter
mcp-servers/     stub — a later phase
testdata/        committed retrieval-quality eval set (sample code + queries)
docs/            handoff and working notes
```

`go.work` at the repo root ties the `protocol`, `editapply`, `daemon`, `helper`,
and `clients/tui` modules together for local development. `proxy/` is a separate
module with **zero third-party dependencies**, deliberately — it is the only
internet-facing component.

## How a prompt flows

```
  Mochiii / VS Code / CLI
          │  ① handshake (protocol_version) + PersistedHistory
          │  ② PromptRequest
          ▼
  ┌─────────────────────────────────────────────┐
  │ DAEMON  (your machine)                      │
  │  ③ retrieve top-k chunks from local index   │
  │  ④ scrub secrets from prompt + chunks       │
  │  ⑤ wrap in <retrieved_context> delimiters   │
  │  ⑥ send GroundingInfo to the client         │
  └─────────────────────────────────────────────┘
          │  ⑦ one inference turn
          ▼
  ┌─────────────────────────────────────────────┐
  │ PROXY (managed mode only)                   │
  │  auth · quota · ZDR enforcement · spend caps│
  └─────────────────────────────────────────────┘
          │
          ▼
     Model provider
          │  ⑧ SSE tokens, streamed back unbuffered
          ▼
  Client renders; edit blocks enter review
```

Everything above the inference call stays local: indexing, embeddings,
retrieval, memory, and backups never leave the machine.

## Transport

Unix domain socket only. No TCP, no listening network port.

The daemon picks a socket path under `$XDG_RUNTIME_DIR/codeterminal/` (falling
back to the OS temp dir if unset), creates it with `0600` permissions, and writes
a JSON lockfile (`daemon.lock`) next to it recording the socket path and PID. The
client reads that lockfile to find the socket — there is no other discovery
mechanism and nothing is guessed.

Messages are newline-delimited JSON, one object per line, in this order:

1. Client → daemon: `HandshakeRequest`
2. Daemon → client: `HandshakeResponse` — carries `PersistedHistory`; the
   connection is closed here if `protocol_version` doesn't match
3. Client → daemon: `PromptRequest`
4. Daemon → client: `GroundingInfo`, then a stream of `TokenResponse` messages,
   ending with one that has `done: true` (or `error` set if the call failed)

See [`protocol/protocol.go`](protocol/protocol.go) for the exact structs.

## Configuration

Credentials and model selection come from two different places, deliberately kept
apart.

### Environment — credentials only

| Variable | Meaning | Example |
|---|---|---|
| `CODETERMINAL_API_BASE` | Base URL of an OpenAI-compatible API (**required**) | `https://api.together.xyz/v1` |
| `CODETERMINAL_API_KEY` | Sent as `Authorization: Bearer <key>` | `sk-...` |
| `CODETERMINAL_USE_PROXY` | `true` selects [proxy mode](#quick-start--managed-proxy) | `true` |
| `CODETERMINAL_MOCHIII_KEY` | Per-user Mochiii key; required in proxy mode | `mochi_xxxxx` |

`CODETERMINAL_API_BASE` is required — the daemon refuses to start without it.
`CODETERMINAL_API_KEY` may be unset for local servers that don't require one.
Neither is ever read from a config file.

The last two select **proxy mode**. `CODETERMINAL_USE_PROXY=true` is load-bearing,
not cosmetic: it is what makes the daemon authenticate with
`CODETERMINAL_MOCHIII_KEY` instead of `CODETERMINAL_API_KEY`, and the daemon
refuses to start if the Mochiii key is empty. In proxy mode
`CODETERMINAL_API_KEY` must be left **unset** — with both set, the daemon warns
and would send the provider key to a proxy that neither wants nor uses it.

The daemon only reads plain environment variables; it does not parse `.env`
files itself:

```bash
cp .env.example .env   # then edit with real values
set -a && source .env && set +a
```

`.env` is gitignored and must never be committed.

### `models.json` — model tiers

The **model slug** lives in [`models.json`](models.json) rather than an env var,
since it is not a secret and is useful under version control:

```json
{
  "config_version": 1,
  "default_tier": "primary",
  "tiers": {
    "primary":    { "slug": "deepseek/deepseek-v4-flash",        "active": true  },
    "ghost_text": { "slug": "qwen/qwen3-coder-30b-a3b-instruct", "active": false },
    "reasoning":  { "slug": "deepseek/deepseek-r1",              "active": false }
  },
  "zdr": {
    "allow_non_zdr": false,
    "allow_data_collection": false,
    "allow_fallbacks": true,
    "provider_ignore_list": ["DeepInfra"]
  }
}
```

The daemon fails fast at startup with a specific error if the config is missing,
malformed, the `default_tier` doesn't exist, isn't `active`, or has an empty slug
(the same rule applies to any other tier marked `active`).

The `zdr` block resolves to the provider-routing object sent on every request,
and is **secure by default**: an absent or legacy block resolves to `zdr: true`,
`data_collection: "deny"`.

Also configurable here: `retrieval.disabled`, `retrieval.rerank_disabled`,
`retrieval.context_budget_chars`, and `no_scrub`. Unknown keys are warned about
rather than silently ignored.

---

# Capabilities

## Retrieval

### Workspace indexing

The daemon binary doubles as a one-shot CLI for building and querying a local,
per-workspace vector index — separate from, and never triggered by, the
long-running serve path:

```bash
./daemon/codeterminal-daemon index /path/to/workspace
./daemon/codeterminal-daemon retrieve --workspace /path/to/workspace --k 5 how does routing work
```

`index` walks the workspace **confined to its root** — it never follows symlinks
(regardless of whether the target is absolute or a relative `../escape`) and
never reads anything outside the resolved root. It chunks eligible files into
overlapping ~40-line windows (10-line overlap), embeds each chunk, and upserts
them into a [chromem-go](https://github.com/philippgille/chromem-go) collection
persisted at `<workspaceRoot>/.codeterminal/index/`. It logs how many files were
scanned, how many were skipped and why, and how many chunks were produced.

The first `index` run also appends `.codeterminal/` to that workspace's
`.gitignore` if it isn't already there (idempotent), and `.codeterminal/` is
pruned from every walk so the index never indexes its own previous output.

**What is skipped.** Anything that could carry a secret — `.env`/`.env.*`,
`*.pem`, `*.key`, `id_rsa*`, `*.p12`, `.aws/`, `.ssh/`, and any filename
containing "secret" or "credential" — along with `.git/`, `node_modules/`,
`vendor/`, common build output directories, anything matched by the workspace's
`.gitignore`, binaries, and files over 1 MB.

It also hard-excludes a narrow, exact-basename noise list that can never usefully
answer a code question: `.gitignore` itself and dependency lockfiles (`go.sum`,
`package-lock.json`, `yarn.lock`, `pnpm-lock.yaml`, `Cargo.lock`, `Gemfile.lock`,
`composer.lock`, `poetry.lock`, `Pipfile.lock`) — see `noiseBasenames` /
`isNoiseFile` in [`daemon/fileclass.go`](daemon/fileclass.go). **This is the only
category excluded outright**; every other file, including every doc, stays indexed
and is merely down-weighted (see [ranking](#ranking-code-vs-prose)).

All exclusion runs through a single gate (`shouldSkipFile` in
`daemon/chunker.go`) called immediately before a file's content is read, so there
is no separate walk-time prune that could diverge from what actually gets read.

### Embedding

Embedding goes through the `Embedder` interface
([`daemon/embedder.go`](daemon/embedder.go)); nothing outside that file depends on
a concrete implementation. It has **two** embedding methods, not one, because BGE
is asymmetric — `Embed` (documents, no prefix) and `EmbedQuery` (queries, prefix
applied) — plus `ID()`, used by the stale-index guard.

- **`BgeEmbedder`** ([`daemon/bge_embedder.go`](daemon/bge_embedder.go)) — the
  active one. Delegates to the [embedder helper subprocess](#embedding-helper-process).
  `Embed` sends chunk text unmodified; `EmbedQuery` first prepends BAAI's
  documented instruction prefix (`"Represent this sentence for searching relevant
  passages: "`). The helper has no notion of this distinction — it is a dumb
  text-to-vector service, and the asymmetry is applied entirely at this layer,
  right before the text leaves the daemon.
- **`PlaceholderEmbedder`** — a deterministic hash-based vectorizer. Still
  injectable in tests, but no longer the default.

**Stale-index guard.** Both embedders report the same `Dim()` (384), so dimension
alone cannot tell a placeholder-built index from a real one. `index` stamps
`<indexDir>/embedder_stamp.json` with the active embedder's `ID()` and the current
`IndexSchemaVersion` after every successful build
([`daemon/embedderstamp.go`](daemon/embedderstamp.go)); `retrieve` checks it first
and refuses with a `"re-index required"` message — rather than silently comparing
incompatible vectors — on any mismatch, including a stamp missing entirely (exactly
what every index built before this guard looks like). `IndexSchemaVersion` was
bumped when chunks started carrying file-class metadata, so an older index is
caught rather than silently read back as unclassified.

**No accidental network calls.** chromem-go's collection is created with an
embedding function that refuses to run (`refuseEmbeddingFunc` in
`daemon/vectorstore.go`), so even a future bug that left a chunk unembedded fails
loudly instead of silently calling chromem-go's default OpenAI embedder.

chromem-go is pure Go with no dependencies of its own and needs no separate server
or CGO; its license (MPL 2.0) is recorded in
[`THIRD_PARTY_LICENSES.txt`](THIRD_PARTY_LICENSES.txt).

### Ranking (code vs. prose)

A real-repo stress test found that raw vector similarity systematically let prose
*about* code (READMEs, the system prompt) outrank the code that actually answers a
code question — including one query where the correct file didn't make the raw top
5. The embedder was fine; this is a ranking problem, fixed by **tilting, not
banning**.

**Classification.** Every chunk is tagged at index time with a `FileClass` —
`code`, `doc`, `config`, or `other` — by extension/basename (`classifyFile` in
[`daemon/fileclass.go`](daemon/fileclass.go)), stored alongside its path and line
range.

**Re-ranking.** `retrieveTopK` fetches a wider raw candidate pool than `k`
(`max(k*6, 20)`, [`daemon/rerank.go`](daemon/rerank.go)) — reweighting only the raw
top-`k` could never recover a chunk ranked just outside it — then combines each
hit's raw similarity with a named class weight:

| Class | Weight |
|---|---|
| `code` | 1.15 |
| `other` | 1.00 |
| `config` | 0.90 |
| `doc` | 0.75 |

`weighted = raw × classWeight(class)`, re-sorted, truncated to `k`. A doc with high
enough raw similarity can still win outright. A chunk missing its stored class
falls back to recomputing it from the file path rather than being silently treated
as neutral.

**Observability and A/B.** Every logged hit shows its class alongside both scores
(`class=code score=0.77 weighted=0.88`). `retrieve --raw` and the daemon's
`--no-rerank` flag (or `retrieval.rerank_disabled` in `models.json`) bypass
re-ranking entirely for direct comparison. Both `retrieve` and the live prompt path
share the exact same `retrieveTopK` — there is no duplicated ranking logic.

### Live retrieval-augmented generation

Every prompt through the normal serve path (`handleConn` in
[`daemon/server.go`](daemon/server.go)) is automatically augmented with relevant
local code before it reaches the model, reusing the same `retrieveTopK` the CLI
calls. The daemon opens the embedder and index **once at startup**, not per
request, via `setupRetrieval`
([`daemon/retrieval_setup.go`](daemon/retrieval_setup.go)).

**Request structure.** Retrieved chunks are never placed in the system role. The
system prompt stays static and authoritative; only the `user` message changes,
wrapped in explicit labeled delimiters ([`daemon/context.go`](daemon/context.go)):

```
<retrieved_context>
[1] path/to/file.go:10-25
... chunk content ...
</retrieved_context>

<user_request>
... the user's raw prompt, unmodified ...
</user_request>
```

A paragraph in `system.txt` tells the model to treat everything inside
`<retrieved_context>` strictly as reference data, never as instructions —
retrieved code is untrusted (it is whatever happens to live in the workspace) and
must never redirect the model's behavior.

**Delimiter injection defense.** Since chunk content is untrusted, a file could
contain a comment engineered to look like `</retrieved_context>`, forging an early
close followed by a fake `<user_request>`. `neutralizeDelimiters` defuses this: it
matches tag-like text tolerant of case, whitespace (including newlines), and
underscore/space variants, and replaces the angle brackets with lookalike
characters (`‹`/`›`) so the exact tag substring can never survive into the rendered
message — while leaving real code (`<-`, generics, comparisons) untouched, since a
match requires spelling out one of the two exact tag names.

**Budget and degradation.** Injected context is capped by a character budget
(`retrieval.context_budget_chars`, default 8000 — roughly 2000–2600 tokens for
code; a character cap rather than a real token count, to avoid pulling a tokenizer
into the otherwise dependency-light daemon for what is only a soft safety cap).
Chunks are ranked best-first; if they don't all fit, only the lowest-ranked
overflow is dropped — the top hit is never sacrificed for a lower-ranked one.

**Retrieval never blocks or fails a request.** No index, a stale index, an embedder
that failed to start, or a transient store error all degrade to answering from the
bare prompt, with a one-line reason logged.

**Grounding visibility on the wire.** Every client goes through this same
`handleConn` — there is no separate ungrounded path to opt into. Before any tokens,
the daemon sends a `protocol.GroundingInfo` (`Grounded`, the daemon's actual
`Workspace`, a `Reason` when ungrounded, `Chunks`, `Truncated`) built from the same
`retrievalOutcome` `gatherContext` already computes. `PromptRequest` also carries an
optional `Workspace` — the client's *expectation* — purely so the daemon can flag
`WorkspaceMismatch`. Grounding itself is still decided once, at daemon startup, by
the daemon's own `--workspace`.

### Secret scrubbing

Before anything leaves the machine, both the user's typed prompt and retrieved
chunk content pass through a heuristic scrubber
([`daemon/scrub.go`](daemon/scrub.go)) that redacts high-confidence secret shapes:
OpenAI-style, AWS, GitHub, Slack, Google, Supabase, and Mochiii keys, plus PEM
private-key blocks.

**Stated limits.** It matches a fixed set of **prefixed** patterns, so novel,
obfuscated, or unprefixed secrets are missed; it can be disabled (`--no-scrub`);
and it runs client-side only — the proxy never reads message content and cannot
scrub. Treat it as defense-in-depth, not a guarantee.

## Editing code

### Edit blocks

The daemon loads [`daemon/prompts/system.txt`](daemon/prompts/system.txt) at
startup (override with `--system-prompt`) and prepends it as the `system` message
on every request. It contains no secrets and is plain text, so it is reviewable
and editable independently of the code.

That prompt instructs the model to propose changes as SEARCH/REPLACE blocks rather
than rewriting whole files:

```
path: relative/path/to/file.go
<<<<<<< SEARCH
(exact existing lines to find)
=======
(replacement lines)
>>>>>>> REPLACE
```

After a response finishes streaming, the daemon parses it with
[`editapply.ParseEditBlocks`](editapply/editblock.go) and logs a summary. A
response with no edit blocks parses to an empty list, not an error. **The daemon's
live chat path only parses and logs** — it never writes to disk.

### The five safety gates

The apply engine — parsing, path safety, exact-match verification, the syntax gate,
and backup — lives in its own shared module, [`editapply/`](editapply/), so there is
exactly **one implementation** shared by the CLI's `edits apply` and Mochiii's
in-chat review. Neither surface keeps its own copy or a weaker check.

For each block, in order:

1. **Path safety** — the target must resolve, through any symlinks, to inside the
   workspace root. Absolute paths, `..` escapes, and files the indexer would
   secret-skip are refused, via the same `editapply.MatchesSecretName` the indexer
   calls — a single shared secret-file policy.
2. **Exact-match verification** — the `SEARCH` text must appear in the file
   **exactly once** (literal substring). Not found, or found more than once
   (ambiguous), and the edit is refused rather than guessed at.
3. **Syntax gate** — for `.go` files, the post-edit content is parsed with the
   stdlib `go/parser`; an edit that would make the file unparseable is refused.
   Other languages skip this (logged as such); it is a guard against obviously
   broken writes, not a type-checker.
4. **Diff + confirm** — the whole `SEARCH` block is shown as removed lines and the
   whole `REPLACE` block as added, then `Apply this edit? [y/N]:`. Only a literal
   `y`/`Y` applies. Edits are independent — accept some, decline others in one run.
5. **Backup, then write** — the first time a file is written in a run, its pre-edit
   content is saved to
   `<workspace>/.codeterminal/backups/<timestamp>/before/<relpath>`. The content
   immediately after each write is also recorded to `.../after/<relpath>` — that is
   what makes `edits undo`'s safety check possible. Only then is the real file
   written.

A run ends with `N applied, M skipped, K refused`.

**There is no auto-apply anywhere.** Every edit requires an explicit `y`.

### Undo

```bash
codeterminal-daemon edits undo [--workspace path] [--session ts] [--force]
```

Restores a backup session (the most recent by default). For each backed-up file it
compares the *current* on-disk content against the `after/` snapshot. Unchanged
files are restored silently. A file modified since — hand-edited further, or
deleted — is **never silently clobbered**: it is listed as guarded and restored only
with `--force` or an interactive confirmation naming exactly which files are
affected.

New-file creation is out of scope: `ParseEditBlocks` rejects an empty `SEARCH`, so
every block necessarily targets an existing file containing that exact text.

## Conversation memory

The daemon keeps **cross-session** conversation memory for its configured
workspace ([`daemon/memory.go`](daemon/memory.go)). The most recent turns —
oldest first, re-validated server-side — are delivered to the client in the
`HandshakeResponse` as `PersistedHistory`.

Because the wire protocol is one prompt per connection, the daemon cannot
distinguish "fresh client session" from "next prompt in an ongoing one" at
handshake time, so it populates this on **every** handshake. Acting on it is the
client's responsibility: only a client's own startup/preflight connection should
hydrate its transcript from this field. A client that re-applied it after every
prompt would duplicate turns it already has.

History sent to the model is validated and capped by `prepareHistory`
([`daemon/provider.go`](daemon/provider.go)) — oldest first, roles `user` and
`assistant` only — and is inserted between the system message and the final prompt.
Retrieved context always lands in the final user message, never in `system` and
never in a history turn.

## Clients

### Mochiii — the chat TUI

`clients/tui/codeterminal-tui`, run with no `--prompt` from a real terminal,
launches **Mochiii**: an interactive streaming chat UI built on
[Bubble Tea](https://github.com/charmbracelet/bubbletea), with
[bubbles](https://github.com/charmbracelet/bubbles) components and
[lipgloss](https://github.com/charmbracelet/lipgloss) styling.

**Splash → chat.** A pink ASCII lotus, the wordmark, and a tagline fill the screen;
any keypress dismisses it into the chat view, which keeps only a small `✿ Mochiii`
glyph in a one-line header. `✿` is the default rather than the lotus emoji — emoji
render inconsistently across terminals and often ignore foreground color;
`lotusGlyphEmoji` in `clients/tui/logo.go` is a one-line swap if you prefer it. The
header also shows connection/streaming state and grounding status.

**Palette** (`clients/tui/styles.go`): pink (`205`) for the logo, brand, and the
user's own prompts; teal (`44`) for accents and the streaming indicator; soft gray
(`252`) for assistant answers; red (`203`) for errors. All in named
`lipgloss.Style` variables in one file, so the look is a one-file edit. The lotus
itself is a single raw-string constant — hand-edit it freely;
`TestLotusLogo_RowsAreSymmetric` checks every row still mirrors correctly.

**Streaming without blocking the UI.** Bubble Tea's `Update` never touches the
network. Sending a prompt starts a goroutine (`clients/tui/stream.go`) that opens a
connection — reusing the same `connectToDaemon` handshake as the one-shot path — and
pushes a message onto a channel for every token, plus a final done-or-error. A
`Cmd` waits on that channel and is re-issued after every token, so `Update` only
ever handles one already-arrived message and the UI never freezes mid-response.

**Cancellation.** Quitting mid-stream cancels a `context.Context` tied to that turn.
A context cannot by itself interrupt an in-flight blocked socket read, so a watcher
goroutine closes the connection when the context is done — that is what actually
unblocks the pending read. See `TestStreamPrompt_ContextCancelUnblocksBlockedRead`
for an end-to-end proof against a fake daemon that hangs mid-response.

**Grounding.** The header shows whatever the daemon reports for the current turn —
`grounded ✓ N chunk(s)`, `ungrounded (reason)`, or `⚠ grounded against <path>, not
<expected>` on a workspace mismatch. It appears as soon as the turn starts, not
after the answer finishes, and is cleared at the start of each new turn.

**Applying edits from chat.** When a completed answer contains edit blocks, Mochiii
parses it with the same `editapply.ParseEditBlocks` and enters a modal review: one
block at a time, shown as a diff (removed `SEARCH` in red, added `REPLACE` in teal,
plus the syntax-check note) with `y apply · n skip · q cancel remaining`. This calls
the same `editapply.PrepareEdit` core the CLI uses, so **every safety gate fires
identically**. A block that fails to prepare is refused with its reason shown, no
prompt. Applied edits are backed up into the same
`.codeterminal/backups/<session>/{before,after}/` layout — restorable with the CLI's
`edits undo` regardless of which surface applied it.

**Daemon-down handling.** Before drawing the splash, the TUI preflights the daemon
connection; if the daemon isn't running or the handshake fails, it prints one clear
message to stderr and exits non-zero instead of entering the alt-screen.

### VS Code extension

[`clients/vscode/`](clients/vscode/) is a TypeScript extension speaking the same
socket protocol as every other client. It surfaces grounding state in the editor,
including `workspace_mismatch`, and classifies the proxy's `zdr_required` refusal as
a privacy refusal rather than a generic error. See its own
[README](clients/vscode/README.md) for what has shipped.

### One-shot CLI

Passing `--prompt`, or piping text on stdin, keeps the original one-shot behavior
completely unchanged — connect, send one prompt, print the streamed answer, exit.
That path is what scripts and tests use.

```bash
./clients/tui/codeterminal-tui --prompt "Say hello in five words."
echo "Say hello in five words." | ./clients/tui/codeterminal-tui
```

---

# Runtime components

## Embedding helper process

The embedding model (`BAAI/bge-small-en-v1.5`, int8-quantized ONNX, 384 dims) runs
via [ONNX Runtime](https://github.com/microsoft/onnxruntime) through the
[`onnxruntime_go`](https://github.com/yalue/onnxruntime_go) binding, which needs
CGO — and the daemon must stay pure Go. So it runs in a separate binary,
[`helper/`](helper/), which the daemon spawns as a child process and talks to over
a Unix domain socket (no TCP, no network surface).

`CGO_ENABLED=0 go build` still succeeds on `daemon`: CGO is confined entirely to
`helper` — `grep -r 'import "C"'` finds nothing outside it.

`onnxruntime_go` loads the shared library with `dlopen` at **runtime** rather than
linking at compile time (that is how it supports Windows without MinGW), so
`go build` on `helper` succeeds regardless of whether the library is present. The
failure necessarily happens at helper **startup** instead, and
[`helper/main.go`](helper/main.go)'s `loadEmbedder` makes that failure actionable —
naming the missing path and the fix — rather than a raw `dlopen` error.

**Inference** ([`helper/onnxembedder.go`](helper/onnxembedder.go)): tokenize with
[`sugarme/tokenizer`](https://github.com/sugarme/tokenizer) (pure Go, loads
`tokenizer.json` directly), run the batch through `onnxruntime_go`, take position 0
(`[CLS]`) of `last_hidden_state` for each item, and L2-normalize.

> ⚠️ **`addSpecialTokens` must be passed as `true` explicitly.** `EncodeSingle`
> defaults it to `false` and silently drops `[CLS]`/`[SEP]`, which corrupts CLS
> pooling **without erroring**.

**Lifecycle** ([`daemon/helperproc.go`](daemon/helperproc.go)):

- **Spawn + readiness** — starts the helper with `--socket`, `--model-dir`, and
  `--onnxruntime-lib` (scoped by the daemon's PID) and blocks until a real Health
  RPC succeeds, not until the logs say so.
- **Health + restart** — if the helper dies, a monitor goroutine respawns it,
  bounded to a fixed number of attempts with a delay between each, so a helper that
  can never come back causes a clear failure instead of a hot loop.
- **Clean shutdown** — `Stop` sends `SIGTERM`, escalates to `SIGKILL` after a grace
  period, and does not return until the process is reaped. No orphans, no zombies.

Smoke-test it end to end:

```bash
./daemon/codeterminal-daemon helper-smoketest "some test string"
```

This starts the helper, waits for health, sends one real embedding request, prints
the vector length (384) and first few values, and shuts down cleanly. Lifecycle
tests ([`daemon/helperproc_test.go`](daemon/helperproc_test.go)) run against a
test-only fixture helper under
[`daemon/testdata/fakehelper/`](daemon/testdata/fakehelper), so the fast unit-test
path never needs the real model or CGO.

## Model acquisition

`codeterminal-daemon download-model` fetches the pinned BGE model and tokenizer
files (`bgeModelAssets` in [`daemon/modelfetch.go`](daemon/modelfetch.go)) into
`~/.codeterminal/models/bge-small-en-v1.5-int8/`, plus the onnxruntime shared
library for the current platform into `~/.codeterminal/models/onnxruntime-1.26.0/`,
**verifying every file's size and sha256** before treating it as usable.

Every URL and checksum is pinned against a real, verified download — not a
placeholder — so the check is meaningful. A cache hit makes no network requests at
all; a checksum or size mismatch removes the bad file and fails loudly rather than
silently using it.

## Managed proxy

[`proxy/`](proxy/) is the managed-tier proxy in front of OpenRouter. It exists so
the provider API key never ships to end users: it holds that key server-side,
authenticates every caller by a per-user Mochiii key, and streams completions back
without buffering.

**It never reads message content.** Every gate inspects only the request's
top-level envelope; `messages` stays an opaque byte slice and the accepted body is
forwarded byte-for-byte.

What it enforces, all fail-closed:

| Concern | Control |
|---|---|
| Identity | Per-user Mochiii key validated against Supabase |
| Privacy | Rejects any request whose ZDR routing flags are missing or weakened |
| Cost | Model allow-list; refuses `models`, `plugins`, `transforms`, provider cost-steering |
| Spend | Atomic quota reservation + a per-request token ceiling enforced mid-stream |
| Abuse | Request-rate, token-rate, and in-flight caps, pre- and post-auth |

Full details, error codes, and configuration: [`proxy/README.md`](proxy/README.md).

## Tier router

[`daemon/router.go`](daemon/router.go) decides which `models.json` tier handles each
request and resolves its slug. It ships **primary-only**: every normal request logs
`route tier=primary slug=... reason=default`.

The `reasoning` tier is wired but gated — `Route` selects it only when a request
carries a genuine non-zero exit signal *and* `reasoning` is `active`, and today's
request path never sets that signal. If the escalation target is inactive or
missing, the router falls back to the default tier rather than erroring or silently
calling something inactive. Ghost-text is not selectable here. See
[`daemon/router_test.go`](daemon/router_test.go) for the guaranteed cases.

## Skills database

A local, per-user SQLite database (`~/.codeterminal/skills.db` — cross-project,
unlike the per-workspace RAG index) recording "skills": successful solution steps,
for later phases to build reuse on.

**Storage plumbing only.** A `Skill` (`daemon/skills.go`) is
`{ID, CreatedAt, Title, Steps, Tags, SourcePrompt}`, with
`AddSkill`/`ListSkills`/`GetSkill`/`DeleteSkill`. It does **not** auto-capture from
conversations and does **not** inject into prompts.

The driver is `modernc.org/sqlite`, pure-Go and CGO-free (pinned to v1.39.0, the
newest release that still only requires `go 1.23.0`). One connection is held open
for the store's lifetime (`SetMaxOpenConns(1)`, plus `journal_mode=WAL` and
`busy_timeout=5000`) rather than reopening per call. Schema is tracked via a
`schema_meta.version` row checked on open.

```bash
codeterminal-daemon skills list [--limit N] [--json] [--db path]
codeterminal-daemon skills delete <id> [--db path]
```

There is deliberately no `skills add` — skills get captured by the agent loop in a
later phase, not typed in by hand.

---

# Development

## Building and testing

```bash
# Build everything
(cd helper      && go build -o codeterminal-embedder-helper .)
(cd daemon      && go build -o codeterminal-daemon .)
(cd clients/tui && go build -o codeterminal-tui .)
(cd proxy       && go build .)

# Fast unit tests — never need the real model or CGO.
# There is NO root module (only go.work), so `go test ./...` from the repo root
# fails with "directory prefix . does not contain modules listed in go.work".
# Run each module explicitly — these are the exact commands CI runs:
for m in daemon editapply proxy helper protocol clients/tui; do
  (cd "$m" && go build ./... && gofmt -l . && go vet ./... && go test -race ./...)
done

# Retrieval quality (needs the real model + CGO; gated behind a build tag)
(cd daemon && go test -tags eval -run TestEvalRetrievalQuality -v ./...)

# VS Code extension (real Extension Development Host; needs a display,
# so use xvfb-run on a headless machine)
(cd clients/vscode && npm ci && npm run compile && npm test)
```

The untagged run never compiles or runs the eval tests.

CI (`.github/workflows/build.yml`) runs exactly the loop above across all six
modules on every push and pull request, plus `govulncheck`, the extension's
Extension Development Host suite, and the proxy image build. The `-tags eval`
suite runs on a weekly schedule rather than per-PR, because it downloads a real
embedding model and takes ~150s.

## Retrieval-quality eval set

[`testdata/evalset/`](testdata/evalset) is a small, fixed sample codebase — 8
single-responsibility Go files: config parsing, an HTTP handler, math stats, an
auth/token check, retry-with-backoff, an LRU cache, structured logging, input
validation — plus [`testdata/queries.json`](testdata/queries.json): 15
natural-language queries phrased as *intent*, not keyword copies of the code, each
mapped to the file (and approximate line range) that should be the top hit.

> `queries.json` deliberately lives **outside** `testdata/evalset/`. Indexing the
> eval set must not also index the query file, or a query's own verbatim text
> becomes an unbeatable match against itself — a contamination bug caught and fixed
> while building this set. Moving the file out of the indexed root was the fix.

[`daemon/eval_test.go`](daemon/eval_test.go) indexes the eval set with the real
`BgeEmbedder`, runs every query, and computes top-1 accuracy and top-3 recall,
printing the full per-query table so quality is *readable*, not just pass/fail. It
asserts `top3Recall >= evalTop3RecallThreshold`, a single named constant (currently
`0.80`) — tune it there, not by hand-picking queries.

---

# Scope

## Known gaps

**Intel Mac (`darwin/amd64`) is not supported.** Upstream onnxruntime v1.26.0 ships
no prebuilt binary for that platform. This is a deliberate, open gap: both
`download-model` and the helper refuse with an explicit "Intel Mac is not
supported" error rather than a generic failure. Since Intel Mac is a committed
platform target, closing this — most likely by building onnxruntime from source —
is an open decision. `linux/arm64` is also unpinned but not committed anywhere yet;
adding it follows the same pattern as the three pinned platforms
(`onnxRuntimePlatforms` in
[`daemon/onnxruntimefetch.go`](daemon/onnxruntimefetch.go)).

**Secret scrubbing is heuristic and client-side only** — see
[Secret scrubbing](#secret-scrubbing) for the three stated limits.

**Proxy rate limiters are per-instance.** They are in-memory, so across N replicas
the effective ceiling is N times the configured values. Quota itself is atomic in
Postgres and *is* correct across replicas.

## Out of scope

**Retrieval** — no auto-indexing on daemon start or per request (a missing index
degrades gracefully rather than triggering a build), no file-watching, no
incremental re-index (re-running `index` rebuilds chunk-by-chunk, keyed by a
deterministic ID, rather than diffing), no query rewriting, no multi-hop retrieval.
The embedder helper is spawned only by explicit commands
(`index`/`retrieve`/`helper-smoketest`), never automatically on daemon start.

**Editing** — no auto-apply anywhere, no fuzzy or approximate `SEARCH` matching, no
multi-occurrence disambiguation (ambiguous is always a refusal), no new-file
creation, and no mid-stream parsing (blocks are parsed only once a full response has
streamed back).

**Skills** — no auto-capture from conversations or agent runs, no injection into
prompts, no embedding/vector search over skills, no dedup or ranking, no sync.

**Routing** — `ghost_text` and `reasoning` are defined but inert; there is no live
tier-selection logic.

**Other** — `mcp-servers/` is still a stub. On the local machine there is no TCP,
no network listener, and no auth beyond Unix socket permissions — the socket's
`0600` mode is the boundary.
