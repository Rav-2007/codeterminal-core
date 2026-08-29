# Mochiii

**A local-first AI coding assistant.** Your codebase is indexed, embedded, and
retrieved entirely on your own machine. The only thing that leaves it is the
minimal prompt for a single inference turn — or, in agent mode, one prompt per
step of a bounded, per-call-approved loop.

A background daemon holds the index, the conversation memory, and the edit-safety
engine. Three clients — a chat TUI, a VS Code extension, and a scriptable CLI —
connect to it over a local socket. **Nothing on your machine listens on the
network.**

> ### Project status — read before you start
>
> **A `.vsix` now builds, and nothing is published yet.** `npm run package` in
> [`clients/vscode/`](clients/vscode/) produces an installable extension
> carrying the daemon, the embedder helper and `models.json` — verified by a
> gate that asserts on the archive's contents. It is **not on any marketplace**,
> and macOS packages are **unsigned**, which means Gatekeeper will quarantine
> them. See [Installing](#installing).
>
> Building from source still needs Go 1.25+ and a C compiler. Installing the
> `.vsix` needs neither.
>
> **The licence is proprietary** — `LICENSE`, "All rights reserved". This
> repository is readable, not open source. See [Licence](#licence).

| | |
|---|---|
| **Language** | Go 1.25+ (TypeScript for the VS Code client) |
| **Transport** | Local socket only — Unix domain socket (`0600`) or Windows named pipe. No TCP |
| **Retrieval** | Hybrid: on-device ONNX embeddings (BGE-small, 384d) + SQLite FTS5, RRF-fused |
| **Inference** | Any OpenAI-compatible API, direct or through the managed proxy |
| **Edit safety** | Five gates: path · exact-match · syntax · confirm · backup |
| **Agent mode** | Off by default. Per-call approval, four per-turn budgets, local audit log |
| **Platforms** | linux/amd64, darwin/arm64, windows/amd64 — [Intel Mac is an open gap](#known-platform-gaps) |

---

## Contents

| | |
|---|---|
| **Use it** | [Installing](#installing) · [Quick start](#quick-start--from-source) · [Commands](#commands) · [Configuration](#configuration) |
| **Understand it** | [How it works](#how-it-works) · [Security posture](#security-posture) |
| **Work on it** | [Development](#development) · [Documentation map](#documentation-map) |
| **Trust it** | [Status and scope](#status-and-scope) · [Licence](#licence) |

---

# Installing

The VS Code extension is the packaged path, and it manages the daemon for you —
no second terminal, no `index` command, no daemon started by hand.

```bash
cd clients/vscode
npm ci
npm run package        # builds daemon + helper, stages them, packages, verifies
code --install-extension codeterminal-vscode-0.0.1.vsix
```

`npm run package` needs the Go toolchain because it builds the binaries it
bundles. **Installing the resulting `.vsix` does not** — that is the point of it.

The package carries the daemon, the embedder helper and `models.json` side by
side in `daemon/`, which is where the daemon looks for its helper and config.
It does **not** carry the embedding model: that is a one-time **41 MB** download
on Linux (63 MB macOS, 104 MB Windows), and the extension offers it on first
activation. **Declining is fine** — you get a working extension that answers
without reading your code, and the offer returns next session.

Packaging is gated by [`scripts/verify-vsix.js`](clients/vscode/scripts/verify-vsix.js),
which opens the archive and asserts on its contents. That gate exists because
`vsce package` exiting `0` is exactly what produced an earlier package that
shipped this repository's own source and omitted the embedder helper entirely —
which does not error, it just silently turns retrieval off.

---

# Quick start — from source

For working on Mochiii, or for the TUI, which is not packaged.

**Requirements:** Go 1.25+, and a C compiler for the embedder helper only. The
daemon and TUI are pure Go and build with `CGO_ENABLED=0`.

### 1. Build

The helper is **not** optional — the daemon spawns it to compute embeddings
locally, and retrieval is disabled without it.

```bash
(cd helper      && go build -o codeterminal-embedder-helper .)
(cd daemon      && go build -o codeterminal-daemon .)
(cd clients/tui && go build -o codeterminal-tui .)
```

### 2. Fetch the model — once per machine

Downloads the pinned BGE model and the onnxruntime shared library into
`~/.codeterminal/models/`, verifying every file's size and SHA-256. A second run
with everything present makes no network requests.

```bash
./daemon/codeterminal-daemon download-model
```

### 3. Index — once per repo

```bash
./daemon/codeterminal-daemon index .
```

Skipping this is not fatal: the daemon starts and answers **ungrounded**, using
no code from your repo.

While the daemon runs it watches the workspace and re-indexes about a second
after you save, create, delete, or rename a file, so ordinary editing needs no
action. Re-run `index` after changes the watcher could not see — anything done
while the daemon was stopped, such as a branch switch or a pull.

### 4. Run

Pick one of the two inference paths.

<details open>
<summary><b>Direct mode</b> — your provider key goes from this machine to the provider</summary>

```bash
export CODETERMINAL_API_BASE="https://api.together.xyz/v1"
export CODETERMINAL_API_KEY="sk-..."

# ...or keep them in .env (gitignored, never commit it):
cp .env.example .env && $EDITOR .env
set -a && source .env && set +a

./daemon/codeterminal-daemon --workspace .    # terminal 1
./clients/tui/codeterminal-tui                # terminal 2
```

</details>

<details>
<summary><b>Managed proxy mode</b> — the pilot path; this machine holds one Mochiii key</summary>

Inference runs through the [managed proxy](#managed-proxy), which holds the
provider key server-side, meters usage per user, and enforces zero-data-retention
routing before anything reaches the provider.

[`run-proxy.sh`](run-proxy.sh) is the whole path: it sources `.env`, points
`CODETERMINAL_API_BASE` at the production proxy, sets `CODETERMINAL_USE_PROXY=true`,
**unsets `CODETERMINAL_API_KEY`** so a provider key can never reach the proxy by
accident, refuses to start without a Mochiii key, and then `exec`s the daemon — so
every daemon flag passes straight through.

```bash
cp .env.example .env && $EDITOR .env    # set CODETERMINAL_MOCHIII_KEY=mochi_...

./run-proxy.sh --workspace ~/some/repo               # terminal 1
./clients/tui/codeterminal-tui --workspace ~/some/repo   # terminal 2
```

Startup logs `proxy mode: forwarding inference through …` — that line is how you
confirm the pilot path is in use. Point at a different proxy with
`CODETERMINAL_PROXY_BASE=http://localhost:8080/v1`; `CODETERMINAL_API_BASE`
deliberately cannot do this, because the script sets it authoritatively so that
`source .env` stays safe to combine with proxy mode.

</details>

**VS Code users skip terminal 1.** The extension manages the daemon itself —
it probes the per-workspace lockfile, adopts a daemon that answers a handshake,
and starts one only when nothing does.

Stop the daemon with Ctrl-C; it removes its socket and lockfile on the way out.
If it is ever killed outright, the next start detects that nothing is listening
on the leftover socket, removes it, and binds a fresh one.

---

# Commands

All commands are subcommands of the `codeterminal-daemon` binary.

| Command | Purpose |
|---|---|
| *(none)* | Run the daemon in the foreground |
| `index <path>` | Build the vector + lexical index for a workspace |
| `retrieve --workspace <p> --k N <query>` | Query the index and print top-k hits |
| `edits apply [--workspace p] [file\|-]` | Apply SEARCH/REPLACE blocks from a response |
| `edits undo [--workspace p] [--session ts] [--force]` | Restore a backup session |
| `mcp list [--config p]` | Show what agent mode would advertise, and its policy |
| `skills list [--limit N] [--json]` · `skills delete <id>` | Local skills store |
| `download-model` | Fetch the pinned BGE model + onnxruntime library |
| `helper-smoketest <text>` | Spawn the helper, embed one string, shut down |

**Daemon flags**

| Flag | Effect |
|---|---|
| `--workspace <path>` | Which repo grounds answers (default: cwd) |
| `--config <path>` | Path to `models.json` (default: beside the binary, then `./models.json`) |
| `--model <slug>` | Override the resolved slug (testing only) |
| `--system-prompt <path>` | Use this file instead of the prompt compiled into the binary |
| `--log-file <path>` | Tee logs to a size-rotating file as well as stderr |
| `--no-context` | Disable retrieval for the daemon's lifetime |
| `--debug-context` | Log the full content of every retrieved chunk |
| `--no-rerank` | Bypass fusion and class re-ranking; raw similarity order |
| `--no-scrub` | Disable heuristic secret scrubbing |

---

# Configuration

Credentials and model selection live in two different places, deliberately.

### Environment — credentials only

The daemon reads plain environment variables and never parses `.env` itself.

| Variable | Meaning |
|---|---|
| `CODETERMINAL_API_BASE` | Base URL of an OpenAI-compatible API. **Required** — the daemon refuses to start without it |
| `CODETERMINAL_API_KEY` | Sent as `Authorization: Bearer`. May be unset for local servers that need no key |
| `CODETERMINAL_USE_PROXY` | `true` selects proxy mode |
| `CODETERMINAL_MOCHIII_KEY` | Per-user Mochiii key; required in proxy mode |

`CODETERMINAL_USE_PROXY` is load-bearing, not cosmetic: it is what makes the
daemon authenticate with the Mochiii key instead of the provider key, and startup
fails if the Mochiii key is empty. **In proxy mode, leave `CODETERMINAL_API_KEY`
unset** — with both set the daemon warns, because it would otherwise send a
provider key to a proxy that neither wants nor can use it.

### `models.json` — model tiers

The model slug lives in [`models.json`](models.json) rather than an environment
variable, since it is not a secret and is useful under version control.

```json
{
  "config_version": 1,
  "default_tier": "primary",
  "tiers": {
    "primary":         { "slug": "deepseek/deepseek-v4-flash", "active": true },
    "minimax_m3":      { "slug": "minimax/minimax-m3",         "active": true },
    "deepseek_v4_pro": { "slug": "deepseek/deepseek-v4-pro",   "active": true }
  }
}
```

Nine tiers ship active, defaulting to `primary` (DeepSeek V4 Flash);
`ghost_text` and `reasoning` ship inactive. Startup fails fast, with a specific
error, if the config is missing or malformed, or if any tier marked `active` has
a missing or empty slug.

The `zdr` block resolves to the provider-routing object sent on every request and
is **secure by default**: an absent or legacy block resolves to `zdr: true`,
`data_collection: "deny"`. Also configurable here: `retrieval.disabled`,
`retrieval.rerank_disabled`, `retrieval.context_budget_chars`, `no_scrub`, and
the whole `mcp` block ([agent mode](#agent-mode)). Unknown keys are warned about,
never silently ignored.

#### `models.agent.json` — agent mode, and the one setting that reaches the internet

`models.agent.json` is `models.json` plus an `mcp` block, and it is what
[`run-tui.sh`](run-tui.sh) uses by default. Two of the tools it enables —
`web_search` and `web_fetch` — **send text to a third party over the internet**,
and both ship as `"allow"`, meaning they run without stopping to ask:

```json
"mcp": { "builtin": { "tools": {
  "web_search": "allow",
  "web_fetch":  "allow"
} } }
```

That is a deliberate default: an assistant that cannot look anything up answers
questions about the changing world from a frozen memory, confidently and without
saying so, which is worse than the exposure. The daemon prints one line at every
startup naming exactly which tools do this and how to undo it. Change both values
to `"ask"` and every call stops for a human first — the prompt shows the outgoing
text in full before anything is sent.

What the daemon does and does not promise on this path: secret-shaped values are
stripped from the query before it leaves, private and link-local addresses are
refused, and fetched pages are fenced as untrusted data that the model may quote
but never obey. It cannot vouch for the far end. See
[`SECURITY_MODEL.md`](SECURITY_MODEL.md).

### Slash commands

Type `/` in the TUI or the VS Code chat for the menu. Local commands run in the
client; steered commands send a task preamble to the model.

```text
/model                 # list active tiers
/model minimax_m3      # select one for this session
/model clear           # back to default_tier

/help /mcp-server /explain /fix /test /refactor /doc /security
/review /plan /run /clear /compact /context /git /init /search /exit
```

---

# How it works

### The shape

```
protocol/        wire types + version handshake + socket discovery
editapply/       the SEARCH/REPLACE apply engine (parse, confine, verify, back up)
daemon/          the long-running process: index, retrieval, memory, agent loop
helper/          embedder subprocess — CGO is confined entirely to here
clients/tui/     the chat TUI, and the one-shot CLI path
clients/vscode/  the VS Code extension (TypeScript)
proxy/           the managed-tier proxy in front of OpenRouter
```

`go.work` ties the first five Go modules together. **`proxy/` is a separate
module with zero third-party dependencies, deliberately** — it is the only
internet-facing component. `mcp-servers/` is still a stub; the built-in tools
live in the daemon.

### A prompt's journey

```
  TUI / VS Code / CLI
        │  ① handshake → PersistedHistory
        │  ② PromptRequest
        ▼
  ┌──────────────────────────────────────────────┐
  │ DAEMON  (your machine)                       │
  │  ③ hybrid retrieve: vectors + FTS5, RRF-fused│
  │  ④ scrub secrets from prompt and chunks      │
  │  ⑤ wrap in <retrieved_context> delimiters    │
  │  ⑥ stream GroundingInfo to the client        │
  └──────────────────────────────────────────────┘
        │  ⑦ one inference turn (agent mode: one per step)
        ▼
  ┌──────────────────────────────────────────────┐
  │ PROXY (managed mode only)                    │
  │  auth · ZDR gate · model allow-list · quota  │
  └──────────────────────────────────────────────┘
        ▼
   Model provider ──⑧ SSE tokens, streamed back unbuffered──▶ client
```

Everything above the inference call stays local: indexing, embeddings, retrieval,
memory, and backups never leave the machine.

### Transport

**No TCP and no network listener** — a local socket is the only surface.

The daemon binds a Unix domain socket at `0600` inside a `0700` per-user runtime
directory, or a named pipe on Windows, and writes a JSON lockfile beside it
recording the address and PID. Clients read that lockfile; there is no other
discovery mechanism and nothing is guessed. Connections are peer-authenticated
against kernel-supplied credentials — `SO_PEERCRED` on Linux, `LOCAL_PEERCRED` on
macOS — and a peer whose UID is not yours is refused.

Messages are newline-delimited JSON: `HandshakeRequest` → `HandshakeResponse`
(carrying `PersistedHistory`; the connection closes here on a protocol-version
mismatch) → `PromptRequest` → `GroundingInfo`, then `TokenResponse` messages
until one carries `done`. Exact structs: [`protocol/protocol.go`](protocol/protocol.go).

### Retrieval

`index` walks the workspace **confined to its root**, never following symlinks,
and chunks eligible files into overlapping ~40-line windows. Each chunk is
embedded locally and written to **two** stores under
`<workspace>/.codeterminal/index/`: a [chromem-go](https://github.com/philippgille/chromem-go)
vector collection and a SQLite FTS5 table.

**Retrieval is hybrid, in three stages** ([`daemon/rerank.go`](daemon/rerank.go)):

1. **Two candidate pools** — semantic nearest-neighbours, and BM25 lexical
   matches for the same query. Both over-fetch well past `k`, because reweighting
   only the top `k` could never recover a chunk ranked just outside it.
2. **RRF fusion** — reciprocal rank fusion (`rrfK = 60`) merges the two rankings.
   Vectors find "the thing that means this"; FTS5 finds the exact identifier you
   typed. Fusion is what stops either one from being the only chance.
3. **Class re-ranking** — every chunk is tagged `code` · `other` · `config` ·
   `doc` at index time and weighted `1.15` · `1.00` · `0.90` · `0.75`. This is a
   **tilt, not a ban**: a doc with high enough raw similarity still wins.

The lexical tier is **optional at runtime** — if the FTS5 store fails to open or
a search errors, that call degrades to semantic-only rather than failing.

**What is never indexed:** anything that could carry a secret (`.env*`, `*.pem`,
`*.key`, `id_rsa*`, `.aws/`, `.ssh/`, any name containing "secret" or
"credential"), plus `.git/`, `node_modules/`, `vendor/`, build output, binaries,
files over 1 MB, and anything matched by a `.gitignore` at any directory level.
Lockfiles and `.gitignore` itself are excluded as pure noise. Every exclusion
runs through a single gate called immediately before a file is read, so no
walk-time prune can drift from what actually gets read.

**Guards worth knowing about:**

- **Stale-index guard** — both embedders report 384 dimensions, so dimension
  alone cannot distinguish a placeholder-built index from a real one. Every build
  stamps the embedder's ID and schema version; `retrieve` refuses with
  "re-index required" on any mismatch, including a stamp missing entirely.
- **No accidental network calls** — the vector collection is created with an
  embedding function that refuses to run, so a future bug that left a chunk
  unembedded fails loudly instead of silently calling a hosted embedder.
- **Retrieval never blocks a request.** No index, a stale index, a dead embedder,
  or a store error all degrade to answering from the bare prompt, with one line
  logged and the reason sent to the client as `GroundingInfo`.

Retrieved chunks are wrapped in `<retrieved_context>` delimiters in the **user**
message, never the system role, and capped by a character budget (default 8000).
Chunk text is untrusted, so tag-like text inside it is neutralised before
rendering — a file cannot forge an early close and inject instructions.

### Editing

The model is instructed to propose changes as SEARCH/REPLACE blocks:

```
path: relative/path/to/file.go
<<<<<<< SEARCH
(exact existing lines to find)
=======
(replacement lines)
>>>>>>> REPLACE
```

**The daemon's chat path only parses and logs — it never writes to disk.**
Applying goes through [`editapply/`](editapply/), one implementation shared by the
CLI and by in-chat review, so no surface has its own weaker copy. Five gates, in
order:

| # | Gate | Refuses |
|---|---|---|
| 1 | **Path safety** | Anything resolving, through symlinks, outside the workspace root; absolute paths; `..` escapes; files the indexer would secret-skip |
| 2 | **Exact match** | `SEARCH` text not found, or found more than once — ambiguous is a refusal, never a guess |
| 3 | **Syntax** | A `.go` edit whose result will not parse. **Go is the only language with a parser in this binary** — every other file type is written unchecked, and the note beside the diff says so verbatim. On a non-Go repository this gate does not fire, so the list below is four gates, not five |
| 4 | **Confirm** | Everything except a literal `y` at the diff prompt |
| 5 | **Backup** | Nothing — it records pre- and post-edit content under `.codeterminal/backups/<ts>/{before,after}/` before the real write |

**Nothing is applied without a human decision.** In the CLI and the TUI that is an
explicit `y` per edit. The VS Code panel additionally offers a session-scoped
auto-apply which the user turns on deliberately; it drives the *same* per-block
path a manual click would, so all five gates still run on every block
([`chatPanel.ts`](clients/vscode/src/chatPanel.ts)). What no surface has is an
edit reaching disk that the user did not ask for.

**Creating a file** is the same mechanism, not a second one: a block with an
**empty `SEARCH`** means "this file's content is, or should be, nothing", so
`REPLACE` is written as the whole file ([`editapply/create.go`](editapply/create.go)).
It goes through the identical gates — confinement, the syntax check, your
confirmation. Two behaviours are worth knowing:

- **It will not clobber.** An empty `SEARCH` against a file that already exists
  and is non-empty is **refused**, with an error telling the model to send a real
  `SEARCH` section instead. An existing but *empty* file is filled, because
  that is the same intent.
- **The syntax gate applies to creation too**, deliberately. Without that, a
  model whose edit was refused for not parsing could land the identical bytes by
  resending them as a creation.

`edits undo` restores a session, comparing current on-disk content against the
`after/` snapshot first. A file modified since is **never silently clobbered**:
it is listed as guarded and restored only with `--force` or an explicit
confirmation naming it.

### Agent mode

**Off by default.** With `mcp.enabled` unset, the outbound request body is
byte-identical to what it was before this existed. With it on, the daemon runs a
bounded loop — model → tool → model — until the model stops asking or a ceiling
stops it.

Two trust lanes, and **which map a server sits in *is* its lane** — there is no
`lane:` field to mistype:

- **`mcp.builtin`** — tools compiled into the daemon, confined by the same
  resolver that gates model-proposed edits. **None of them writes:**
  `propose_edit` files a change into the normal five-gate review.
- **`mcp.servers`** — MCP servers you configure, spawned as stdio subprocesses.
  Ordinary programs with your full access. **Not sandboxed**, and each needs
  `acknowledged_unconfined: true` before it resolves to anything runnable.

A tool you do not list resolves to `ask` — the default is a question. `ask`
suspends the turn and shows you the tool, the **complete** arguments, its lane,
and which step this is. A timeout, a garbled answer, an answer to a different
call, and a closed client are all denials. An "allow for this task" grant covers
one tool for one turn and is never written to disk.

Per-turn budgets, all configurable, shown here at their defaults:

```jsonc
"budget": {
  "max_iterations": 8,             // model calls per turn
  "turn_timeout_seconds": 600,     // MACHINE time; your thinking time is added back
  "max_tool_result_bytes": 32768,  // per result, after scrubbing
  "max_total_tool_bytes": 131072,  // per turn, after scrubbing
  "max_advertised_tools": 12       // cap on the menu the model sees
}
```

Every decision — including `allow` calls you were never prompted about — is
appended to `.codeterminal/logs/toolcalls.jsonl` (local, `0600`, rotated at
5 MiB). It records the **SHA-256 of the arguments and their length, never the
arguments themselves**: those are unscrubbed model output, and the digest is the
one your approval was bound to.

Full design and threat model: [`docs/MCP_MASTER.md`](docs/MCP_MASTER.md) and
[`docs/MCP_LANE_B_THREAT_MODEL.md`](docs/MCP_LANE_B_THREAT_MODEL.md).

### Conversation memory

The daemon keeps cross-session memory per workspace and delivers recent turns to
the client in the `HandshakeResponse`. Because the protocol is one prompt per
connection, the daemon cannot tell "fresh session" from "next prompt", so it
populates this on **every** handshake — acting on it is the client's job, and
only a startup connection should hydrate a transcript from it.

History sent to the model is re-validated and capped server-side: `user` and
`assistant` roles only, oldest first, inserted between the system message and the
final prompt. Retrieved context always lands in the final user message.

### Embedding helper

`BAAI/bge-small-en-v1.5`, int8-quantised ONNX, 384 dimensions, run through
onnxruntime — which needs CGO, while the daemon stays pure Go. So it lives in a
separate binary the daemon spawns and talks to over its own local socket.
`CGO_ENABLED=0 go build` still succeeds on `daemon`; `grep -r 'import "C"'` finds
nothing outside `helper/`.

BGE is asymmetric, and the daemon applies that asymmetry at the boundary:
documents embed unmodified, queries get BAAI's instruction prefix. The helper
itself is a dumb text-to-vector service with no notion of the distinction.

The daemon blocks on a real health RPC rather than a log line, respawns a dead
helper a bounded number of times, and on shutdown escalates `SIGTERM` → `SIGKILL`
and does not return until the process is reaped. Smoke-test the whole path with
`helper-smoketest`.

### Managed proxy

[`proxy/`](proxy/) exists so the provider key never ships to end users. **It never
reads message content** — every gate inspects only the request envelope, and
`messages` stays an opaque byte slice forwarded byte-for-byte.

| Concern | Control |
|---|---|
| Identity | Per-user Mochiii key validated against Supabase |
| Privacy | Rejects any request whose ZDR routing flags are missing or weakened |
| Cost | Model allow-list; refuses `models`, `plugins`, `transforms`, provider cost-steering |
| Spend | Atomic quota reservation + a per-request token ceiling enforced mid-stream |
| Abuse | Request-rate, token-rate and in-flight caps, pre- and post-auth |

All fail closed. Error codes and configuration: [`proxy/README.md`](proxy/README.md).

---

# Security posture

The full threat model — what each gate promises, what it does not, and the
accepted residuals — is [`SECURITY_MODEL.md`](SECURITY_MODEL.md). The summary:

**What holds.** No network listener on your machine. Peer-authenticated local
socket at `0600`. Confinement to the workspace root through symlinks, with
atomic symlink-refusing writes on both the forward and undo paths. Five edit
gates with no auto-apply. ZDR routing enforced server-side by the proxy, which
returns `403 zdr_required` rather than trusting the client to ask nicely.

**What is explicitly *not* claimed:**

- **Agent mode's Lane B is unconfined.** A configured MCP server is an ordinary
  program running with your full privileges. The protection is that you approve
  every call and everything is audited — **not** that it is sandboxed. This is
  why `acknowledged_unconfined: true` must be typed by hand.
- **Secret scrubbing is heuristic and client-side only.** It matches a fixed set
  of *prefixed* patterns, so novel, obfuscated or unprefixed secrets are missed;
  it can be disabled; and the proxy cannot scrub, because it never reads content.
  Defence in depth, not a guarantee.
- **A stale index still reports `grounded ✓`.** Nothing records when the index
  was built, so the model can confidently describe the old shape of a file it
  "retrieved". The watcher keeps a running daemon current, but offline changes
  are invisible until you re-run `index`. This is the top known honesty gap.
- **Proxy rate limits are per-instance.** They are in-memory, so across N
  replicas the effective ceiling is N× the configured value. Quota itself is
  atomic in Postgres and *is* correct across replicas.

---

# Development

There is **no root module** — only `go.work` — so `go test ./...` from the repo
root fails. Name the six modules explicitly; these are the exact commands CI runs.

```bash
for m in daemon editapply protocol clients/tui helper proxy; do
  (cd "$m" && go build ./... && gofmt -l . && go vet ./... && go test -race ./...)
done

# VS Code extension (real Extension Development Host; xvfb-run if headless)
(cd clients/vscode && npm ci && npm run compile && npm test)

# Retrieval quality — needs the real model and CGO, so it is behind a build tag
# and an untagged `go vet` does NOT compile it.
(cd daemon && go test -tags eval -run TestEvalRetrievalQuality -v ./...)
```

### Make targets

`make check` runs the same gates as CI, so green locally means green there.

```bash
make hooks    # install tracked git hooks — once per clone
make check    # fmt, vet, race, lint, coverage ratchet, errcheck ceiling
make lint     # staticcheck + ineffassign + bodyclose
make ratchet  # per-package coverage floors
make fuzz     # 30s per target; FUZZTIME=5m to search harder
make drill    # mid-stream SIGTERM drill against the real proxy binary
```

**Ratchets only tighten.** [`scripts/coverage-floors.txt`](scripts/coverage-floors.txt)
is raised to numbers measured on `main`, never lowered to make a red build green;
`scripts/errcheck-ceilings.txt` fails if the count moves in *either* direction, so
a new unchecked error is fixed rather than grandfathered.

**Stated plainly: `git push --no-verify` bypasses the pre-push hook, and nothing
in it can prevent that.** It guards against forgetting, not against deciding. It
exists as a compensating control because branch protection is unavailable on a
private free-plan repo, so CI reports but cannot block. Some jobs — the macOS
matrix and the retrieval eval — run only on `main` or by dispatch, because they
are the expensive ones.

### Retrieval eval set

[`testdata/evalset/`](testdata/evalset) is a fixed 8-file sample codebase, with 15
natural-language queries in [`testdata/queries.json`](testdata/queries.json)
phrased as *intent* rather than keyword copies of the code, each mapped to the
file that should be the top hit. The test prints a full per-query table so quality
is readable rather than pass/fail, and asserts top-3 recall against one named
constant (`0.80`) — tune it there, never by hand-picking queries.

> `queries.json` lives **outside** the indexed root deliberately. Indexing the
> query file makes a query's own text an unbeatable match against itself — a real
> contamination bug, caught and fixed by moving the file out.

---

# Status and scope

### Two boards, scored separately

**Engineering and readiness are not the same axis, and this project is
deliberately lopsided.** The code is heavily tested, race-clean, ratcheted and
cross-platform-green; the product is installable from a locally built `.vsix` but
is on no marketplace, unsigned on macOS, and has not yet been run by anyone
outside this machine.

That gap does not close with more hardening. What remains is a set of founder
decisions, a billing account and an Apple enrolment —
[`BACKLOG.md`](BACKLOG.md) lists them in the order they unblock each other.

**How the engineering side was built** — the verification discipline, the ratchets,
the parity tests, and the specific bug behind each — is
[`docs/ENGINEERING_METHOD.md`](docs/ENGINEERING_METHOD.md). The short version:
*a fix is not done when its test passes, it is done when the test has been
demonstrated to fail with the fix removed.* Numbers live in that document rather
than here, so there is one copy of them.

### Known platform gaps

**Intel Mac (`darwin/amd64`) is not supported.** Upstream onnxruntime v1.26.0
ships no prebuilt binary for it at all. This is a deliberate, open gap:
`download-model` and the helper both refuse with an explicit "Intel Mac is not
supported" error rather than a generic failure. Closing it most likely means
building onnxruntime from source. `linux/arm64` is unpinned but not committed
anywhere; adding it follows the same pattern as the three pinned platforms
([`daemon/onnxruntimefetch.go`](daemon/onnxruntimefetch.go)).

**Nothing is published.** A `.vsix` builds and installs, but it is not on any
marketplace, and the macOS package is **unsigned** — Gatekeeper quarantines
unsigned binaries inside an extension, so the daemon will not start and the
failure reads as an unexplained "daemon not running". Signing and notarisation
are the remaining work; until they are done, `linux-x64` and `win32-x64` are the
shippable targets.

### Out of scope, on purpose

| Area | Not done, deliberately |
|---|---|
| **Retrieval** | No *full* index build on start or per request — the running daemon's watcher does keep it current per file; no query rewriting; no multi-hop. Re-running `index` rebuilds chunk-by-chunk keyed by a deterministic ID rather than diffing |
| **Editing** | No *scored or approximate* `SEARCH` matching — the matcher normalizes (line endings, trailing space, indentation, unicode) and then matches exactly, and **ambiguous is always a refusal**, never a guess ([`match.go`](editapply/match.go)). No mid-stream parsing: edits are read from the completed response. Unified diffs are not parsed — a response carrying one is refused by name, not silently ignored. File *creation* **is** supported — see [Editing](#editing) |
| **Agent mode** | No OS sandboxing of third-party servers — consent and audit are the protection, and the docs say so; stdio only, so no remote MCP and no new egress; tools only, no resources or prompts; no hot-reload; tool results never persist into history |
| **Skills** | Storage plumbing only: no auto-capture, no injection into prompts, no vector search, no sync |
| **Routing** | `ghost_text` and `reasoning` stay inactive; VS Code has no model picker UI yet |

---

# Documentation map

The repository keeps **one owner per fact**. When something changes, it changes
in the document that owns it.

| Read | For |
|---|---|
| [`docs/README.md`](docs/README.md) | **The documentation index.** Which of the 44 files are current, which are dated measurements, and which are historical |
| [`docs/HANDOFF.md`](docs/HANDOFF.md) | The contributor entry point: working discipline, architecture, where every subsystem lives |
| [`docs/ENGINEERING_METHOD.md`](docs/ENGINEERING_METHOD.md) | **How this codebase is made robust** — each technique, and the bug that made it necessary |
| [`PRODUCT_OVERVIEW.md`](PRODUCT_OVERVIEW.md) | What the product is, for a reader who has never seen it |
| [`SECURITY_MODEL.md`](SECURITY_MODEL.md) | The threat model, and what each gate actually promises |
| [`docs/OPEN_ITEMS.md`](docs/OPEN_ITEMS.md) | The bug register, every entry labelled CONFIRMED / PLAUSIBLE / NOT RUN |
| [`BACKLOG.md`](BACKLOG.md) | What is still ahead: what shipped and when, then what is left in dependency order |
| [`proxy/README.md`](proxy/README.md) · [`clients/vscode/README.md`](clients/vscode/README.md) | The two components with their own front doors |

**Vocabulary** used consistently across all of them: **CONFIRMED** means
reproduced by something actually run. **PLAUSIBLE** means reasoned from source,
never executed. **NOT RUN** means honestly not attempted. **FIXED** means
implemented *and* neuter-verified — the fix removed and the test demonstrated to
fail. **CLOSED** is a founder signature; engineering never writes it.

---

# Licence

**Proprietary — see [`LICENSE`](LICENSE).** Copyright © 2026. All rights
reserved. This software is licensed, not sold, and this repository being readable
does not make it open source.

Third-party dependency licences are recorded in
[`THIRD_PARTY_LICENSES.txt`](THIRD_PARTY_LICENSES.txt).
