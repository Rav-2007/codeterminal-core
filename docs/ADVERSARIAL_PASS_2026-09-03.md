# Adversarial re-verification pass — 2026-09-03

Branch `audit/adversarial-pass`, audited at `d1a82a5` (43 commits ahead of
`main`; this pass added two more).
Four chunks. Three runs of everything where a second and third run could say
something a first could not.

Every claim below carries either a command that was run or a `file:line` that was
read. Where a hypothesis of mine was disproved, it is recorded as disproved.

## Verdict

`make check` **green three times** — cache cleared before each run, test order
shuffled on runs 2 and 3. **Five gates neutered, five went red**, working tree
clean after each. **Three cold end-to-end product runs produced byte-identical
provider requests.** The real-model retrieval eval **passed** with top-3 recall
14/15 = 0.93 against a 0.80 threshold.

Four new register rows (**37–40**), one of them part-fixed in this pass. No auth
bypass, no credential leak, no exposed endpoint, and no orphaned process.

On memory, stated to the limit of what was measured: the daemon's RSS was flat
across three cold end-to-end runs (15.3 → 17.5 MB, 17–18 fds, 9 threads, every
run). That is a short-run plateau, not a soak — the 30-minute soak in this pass
exercises the **proxy**, and no equivalent long-run measurement of the daemon
exists here.

The two findings worth acting on first:

1. **An unbounded cache, and a test suite writing into the user's real one**
   (item 37) — 1,709 directories and 232 MB, +24 per `make check`. Test half
   fixed here; production reclamation still open.
2. **A Stripe secret key reaches the model unredacted and trips no detector**
   (item 38) — caught on the wire, and outside D5's rejection because a
   structural pattern costs no false positives.

## Chunk 1 — Gate integrity

| Run | Conditions | Wall | Result |
|---|---|---|---|
| 1 | `go clean -testcache`, `-count=1` | 6m07s | **green** |
| 2 | cache cleared, `GOFLAGS=-shuffle=on` | 5m54s | **green** |
| 3 | cache cleared, `-shuffle=on`, fresh seed | 21m24s* | **green** |

*Run 3 overlapped my e2e work; wall time is not a clean measurement, the result is.

Shuffle was **proven to engage**, not assumed: same package run twice produced
different test order and printed `-test.shuffle 1788403042335419286`.

### Neuter checks — all five gates are real

| # | Neuter | Gate response |
|---|---|---|
| 1 | delete the `openai_key` scrub pattern | `TestScrub_TruePositives/openai_key` FAILs, names the leaked secret |
| 2 | unpin `actions/checkout` to `@v4` | `actions-pinned.sh` exit 1, names `build.yml:157` |
| 3 | flip register item 24 to FIXED | `docs-claims.sh` exit 1, `make docs` exit 2 |
| 4 | log the full key instead of `keyPrefix` | proxy tests exit 1 |
| 5 | leak `OPENROUTER_API_KEY` into the LSP child | `...NeverReceivesCredentials` FAILs, names the variable |

`git status` clean after each. **Correction to my own first reading of #3:** I
initially recorded it as "prints FAIL but exits 0". That was my measurement
error — I captured `$?` after a pipe through `tail`, so I was reading `tail`'s
exit code. Re-run without the pipe: exit 1.

## Chunk 2 — Credentials and data on the wire

### The one that matters: two planted secrets reached the provider

A canary workspace with three markers, indexed and queried through the real
daemon against a capturing sink:

| Canary | Shape | Structural scrub | Warn-mode | Reached the wire |
|---|---|---|---|---|
| 1 | `sk_live_…` (Stripe) | **no match** | **no match** | **yes** |
| 2 | base64 blob | no match | entropy fired (log-only) | **yes** |
| 3 | benign sentence | n/a | n/a | yes (correct) |

The scrubber **ran** (`daemon/context.go:417`) — it is a structural allow-list of
nine vendor formats, and Stripe is not one of them. `sk-[A-Za-z0-9]{20,}`
(OpenAI, hyphen) is there; `sk_live_`/`sk_test_` (underscore) is not. Canary 1
also failed to trip warn-mode entropy because a low-diversity key is low-entropy.

This is **not** the D5 question. D5 rejected *entropy/keyword* redaction on
false-positive grounds (33% of chunks, zero precision). A structural pattern has
the same near-zero false-positive profile as the nine already shipped.

### The keyword detector misses the commonest credential names

`keywordAssignmentPattern` (`daemon/chunkscrub.go:105`) requires `\b` after the
credential word. `_` is a word character, so:

| Source | Matches |
|---|---|
| `SECRET_KEY = "..."` | **no** |
| `stripe_secret_key = "..."` | **no** |
| `dbPassword = "..."` | **no** |
| `apiKey = "..."` | yes |
| `authToken := "..."` | yes |

Every positive case in `chunkscrub_test.go` uses a bare credential word as the
whole identifier (`token =`, `password:`, `api_key =`). None uses a compound
name, so the suite passes while the detector misses the dominant real-world form.
Log-only, so **not a leak** — but it biases the fire-rate data D5 will be decided
from.

### Everything else on this axis came back clean

- **No key-shaped secret** in the working tree, in **542 commits** of history, or
  in any built artifact. The only hits were the 28-char base *URL*, which
  legitimately appears in source.
- **Credentials never cross into subprocesses**: allow-list (`mcp.ServerEnv`),
  case-folded deny-list, verified by neuter #5.
- **No credential in any proxy log line** — every site uses `keyPrefix` or the
  database `key_id`.
- **Proxy pen-test, 7 auth shapes**: duplicate `Authorization`, lowercase header,
  tab separator, `X-Forwarded-Authorization`, `X-Original-URL`, empty Bearer,
  Basic — **no bypass**.
- **No exposure**: `/debug/vars` 404, `/debug/pprof/heap` 404, `/admin/metrics`
  401 without and with a wrong token.
- **Error bodies** carry a token and a request id only: `{"error":"malformed_request","request_id":"…"}`.
- **F1 confirmed live on the wire**: `{"error":"zdr_required"}`.
- **Pre-auth limiter exact**: 120 concurrent → **20 admitted, 100 × 429**.
- **Data at rest**: conversation DB is `0600` in a `0700` dir; prompts and answers
  persist, **neither canary secret does** — retrieved context is not written to
  disk. Retention is bounded (`maxTurnsPerWorkspace`, `maxTurnAge`).

## Chunk 3 — Does the product work, three times?

Three cold passes (index wiped each time) through the real daemon, real socket,
real agent loop, canned-SSE provider:

- **Byte-identical** provider requests across all three: same 7,952 bytes, same
  SHA-256, same chunk order (`payments.go:1-18 go.mod:1-4 README.md:1-3`).
- Full agent loop verified: 11 tools offered, `query_compiler_definition`
  requested → running → succeeded, reply streamed back.
- Resource profile flat: RSS 15.3→17.5 MB, 17–18 fds, 9 threads, every pass.
- **No orphans** after graceful shutdown.

- **VS Code surface verified against a real editor**, not just the offline gates:
  the EDH suite runs **99 passing, 1 pending**, and `make webview` is green
  (0 undefined symbols, 36 type findings against a ceiling of 36).

**One thing I got wrong and then disproved.** I read `"processId": nil` in the
LSP initialize request plus the absence of `SysProcAttr` (which this repo's own
MCP code sets) and predicted that a `kill -9` on the daemon would orphan gopls —
296 MB held forever. **Measured: it does not.** Both gopls processes died with
the daemon. The mechanism is stdin EOF, which a conforming language server exits
on. The protection is incidental rather than designed, but it is real.
## Chunk 4 — Memory, performance, and where optimization is owed

### The one new resource defect: an unbounded cache, and tests writing into the user's real one

`sandboxExecHome()` (`daemon/mcp_exec.go:126`) returns
`~/.cache/codeterminal/sandbox-home/<sha256(workspace)[:16]>`, created at
`mcp_exec.go:260`. **Nothing ever removes it** — a grep for
remove/clean/prune/sweep on that path returns nothing.

Measured on this machine:

| | |
|---|---|
| directories | **1,684 → 1,709** during this session |
| size | **229 MB → 232 MB** |
| created by **one `make check`** | **24** |
| accumulated over | 8 days (Aug 27 – Sep 3) |
| empty (command wrote nothing) | ~2 in 3 |
| non-empty content | a per-workspace Go build cache, up to 1.1 MB each |

Two distinct harms:

1. **Test isolation is broken.** Nothing redirects `XDG_CACHE_HOME`, so every
   `go test` with a `t.TempDir()` workspace hashes to a fresh tag and leaves a
   permanent directory in the developer's real cache. This is *the same failure
   mode this package already found and fixed once* — `daemon/helperproc_test.go:19`
   carries the postmortem: *"every `go test ./daemon` on every machine leaked
   this directory — measured at 379 directories and 1.5 GB."* Same shape,
   different directory, 1,709 directories and 232 MB.
2. **Unbounded growth in production.** A user who opens ten projects gets ten
   sandbox homes; deleting a project leaves its cache behind forever.

The contrast that makes this a defect rather than a trade: the conversation
database in the same codebase *is* bounded, in one place, with the bug it fixes
written above it (`daemon/memory.go:22`).

### Where optimization is actually owed

Hot-path benchmarks, `-benchtime 200x` (taken under load — treat the absolute
numbers as indicative and the *ratios* as the finding):

| Benchmark | ns/op | Throughput | Allocs |
|---|---|---|---|
| `Scrub` | 4,159 | 232 MB/s | **0** |
| `RenderChunk` | 4,743 | 203 MB/s | 3 |
| `RerankChunks` | 33,888 | — | 7 |
| `ChunkContent` | 69,757 | 111 MB/s | 56 |
| **`DetectHighEntropy`** | **60,496** | **15.9 MB/s** | 11 |
| `TurnScrubWork` (whole turn) | 2,362,182 | 4.09 MB/s | 120 |

**`DetectHighEntropy` is the slowest per-byte operation on the retrieval path —
14.5× slower per byte than the scrubber it runs alongside — and it is
log-only.** It runs on every chunk of every turn purely to gather the fire-rate
data D5 will be decided from. Combined with the keyword-detector gap above, the
product is paying its single largest per-byte cost to collect a measurement that
is itself skewed. **Recommendation:** sample it (say 1 chunk in N) rather than
running it on every chunk, or gate it behind a config flag. This is the highest-
value optimization available and it is not a correctness change.

Second: `TurnScrubWork/as_shipped_double_scrub` (2.362 ms) benchmarks *faster*
than `single_scrub_shared` (2.413 ms) — the obvious "scrub once and share"
optimization is already falsified by its own benchmark. Do not spend time there.

### Register items re-verified by measurement

- **Item 35 CONFIRMED, and refined.** A live `gopls` on a Go workspace is not one
  process but **two: 170 MB + 126 MB = 296 MB**, matching the recorded 295 MB.
  `LSPBridge.Close()` is reached only from `daemon/main.go:465` (shutdown); there
  is no idle eviction and no `lastUsed` tracking.
- **Item 36 CONFIRMED.** `keyPrefix` (`proxy/main.go:1623`) returns the whole key
  for input ≤ 8 bytes. Reachable pre-auth, but `slog`'s `TextHandler` quotes
  values so there is no log-injection path. Impact remains what the register says.
- **The 16× egress overrun is genuinely fixed.** `daemon/agentloop.go:796` floors
  the remainder at zero and treats an exhausted budget as its own case;
  `turn.toolBytes += emitted` accounts post-render and post-framing.

### Repo hygiene

`out-vsix/` is the **release output directory** (`release.yml:145`) and is **not
in `.gitignore`** (`out/` is). A 15 MB `.vsix` from 2026-08-12 is therefore
tracked in git, and its bundled daemon was built with **go1.25.12** while the
repo's enforced floor is 1.25.13. Scanned with `govulncheck -mode=binary`:
**4 reachable stdlib vulnerabilities** (GO-2026-6218 `net/url`, GO-2026-6090
`crypto/tls`, GO-2026-5972 `encoding/asn1`, GO-2026-5026 `net/http`), all fixed
in 1.25.13.

**Correcting my own first framing of this:** the release job does
`rm -rf daemon && mkdir -p daemon`, copies freshly built binaries and repackages,
so this stale file does **not** reach users through the release pipeline. It is a
tracked, vulnerable build artifact sitting at the release path — repo hygiene and
a trap for anyone installing from a clone, not a shipping defect. A freshly built
daemon is go1.25.13 and clean.

Related, and still worth doing: `verify-vsix.js:334` checks the bundled binary's
**size and magic bytes**, not the toolchain that built it. Go embeds that string;
asserting it matches the `go` directive would close the "CI floats above the line,
a laptop sits on it" gap the repo already documents for `govulncheck`.

## What was fixed here, and what was only written down

**Fixed** (one commit, with a neuter proving it does work):

- Item 37(a) — `daemon/helperproc_test.go` now redirects `XDG_CACHE_HOME` to a
  temp dir for the whole package. Measured: 3 stray directories without it from
  one small test subset, 0 with it.

**Written down, not fixed** — items 38, 39, 40 and 37(b) in
[`OPEN_ITEMS.md`](OPEN_ITEMS.md), each with severity, `file:line` and how it was
reproduced. Under this pass's fix-P0/P1-report-the-rest policy none of them is a
P0 or P1: 38 and 39 sit inside a scrubbing posture the founder has already ruled
on once (D5) and deserve that ruling rather than a unilateral patch, and 40 does
not reach users.

## Negative results, recorded so they are not mistaken for gaps

- **No key-shaped secret** in the working tree, in **542 commits** of history, or
  in any built artifact. The only matches were the 28-character API *base URL*.
- **No auth bypass** across seven header shapes; **no exposure** of
  `/debug/vars`, `/debug/pprof/*` or `/admin/*`; error bodies carry a token and a
  request id only; **F1 confirmed live** (`zdr_required`); pre-auth limiter exact
  at 20 of 120.
- **Credentials never reach a subprocess** — allow-list plus a case-folded
  deny-list, proven by neuter.
- **Retrieved context is never written to disk.** The conversation DB is `0600`
  in a `0700` directory, holds prompts and answers, holds **neither canary
  secret**, and is retention-bounded.
- **Index orphan pruning works**: deleting a file and re-indexing reported
  `removed 2 superseded chunk(s) across 1 file(s)` and the file left the results.
- **The 16× egress overrun is genuinely fixed** (`daemon/agentloop.go:796`).
- **gopls does not orphan on `kill -9`.** I predicted it would, from
  `"processId": nil` plus the absent `SysProcAttr`. Measured: both processes died
  with the daemon, via stdin EOF. Incidental rather than designed, but real.

## Reproducing any of this

The harnesses are in the session scratchpad, not the repo: a capturing sink that
saves every provider request verbatim, a tool-call sink that drives the agent
loop, and a Python client speaking the daemon's wire protocol directly. The
pattern is the one `scripts/wire-drill.sh` already establishes — real daemon,
real socket, real gates, faked model — and the canary workspace is three marker
strings of different shapes in one Go file.

The single most important discipline, and the reason three runs meant something:
`go clean -testcache` before **each** run, and a different `-shuffle` seed. That
shuffle genuinely reorders was proven rather than assumed.
