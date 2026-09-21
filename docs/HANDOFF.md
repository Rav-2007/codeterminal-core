# Mochiii — Handoff

**Rewritten 2026-08-01 (v9).** Supersedes v8 and every predecessor.

**Purpose:** get a fresh session productive fast. This document is an *entry
point*, not a synthesis of everything — that is what v5 through v8 tried to be,
and it is why they went stale. Each version added a layer and removed none, so
by v8 the same file claimed macOS refused every connection (it has not since
`648d38b`), that the proxy did not enforce ZDR (it has since F1, verified live
by wire probe), and that `models.json` was untracked (it is tracked, and always
has been).

**This version keeps only what does not rot:** how to work on this project, what
the architecture is, and where the code lives. Everything that changes weekly is
now owned by exactly one document, and this one points at it.

---

## Read these four, in this order

| Question | Document |
|---|---|
| What is still ahead? | [`../BACKLOG.md`](../BACKLOG.md) — forward-looking work only |
| What bugs are open? | [`OPEN_ITEMS.md`](OPEN_ITEMS.md) — the register, re-derived against source, every entry with a `file:line` and a CONFIRMED / PLAUSIBLE / NOT RUN label |
| What needs a founder ruling? | [`DECISION_PACK.md`](DECISION_PACK.md) — D1–D8, one page each; **all eight taken** (D4 on 2026-07-27, the rest on 2026-08-12) and the P3 security gate closed with them. **taken: D1 D2 D3 D4 D5 D6 D7 D8** |
| What was already done, and why? | [`ARCHIVE/BACKLOG_2026-07.md`](ARCHIVE/BACKLOG_2026-07.md) — 3,839 lines of verbatim record: commit SHAs, verification transcripts, measured numbers |

**Do not add a fifth.** The failure mode this project keeps hitting is a second
copy of the truth that drifts from the first. When something changes, change it
in the one document that owns it.

Everything *else* under `docs/` — measurements, QA transcripts, superseded plans
— is catalogued in [`README.md`](README.md), which says for each one whether it
is current, a dated measurement, or historical. **A superseded plan carries a
`⛔ SUPERSEDED` banner at the top naming its successor**; if you are reading a
plan without one, it is the current plan.

---

## Current state

> **Corrected 2026-09-02, and the correction is structural.** The two bullets
> below used to quote a `main` SHA, a commit count and a CI run id. Every one of
> them is a derived fact that goes stale on the next push, and all three had:
> they named `aead295`/50 commits and run `31210785353` three and a half weeks
> after both moved. `BACKLOG.md` fixed the same class of rot by deleting its
> hand-written commit counts and saying to run `git rev-list --count` instead.
> The same applies here — a document nobody can keep current will not be kept
> current, so it should not make the claim.

- **`main` is where work lands, by direct fast-forward.** For its current head,
  the commit count and whether anything is unpushed, ask git:
  `git log --oneline -1 origin/main` and `git status -sb`. Do not trust a SHA
  written into a document.
- **`make check` is the local gate** — gofmt, vet, cross-vet, `-race` on all six
  modules, lint, the coverage ratchet, the errcheck ceiling, evalguard, the
  supply-chain gates and the doc gates. CI runs the same ground in three
  workflows (`build`, `gates`, `retrieval eval`); for their current state, run
  `gh run list -R Rav-i24/Mochiii --branch main --limit 3` rather than reading a
  run id here. The scheduled retrieval eval reports `skipped` on a push run,
  which is correct and not a failure.
- **The last pass measured the project at ~77% engineering robustness and ~27%
  product readiness.** That split is still the whole story: the code is stronger
  than most shipped commercial software, and nobody outside this machine can
  install it — though that gap narrowed on 2026-08-08. **A `.vsix` now builds
  and installs**: `npm run package` in `clients/vscode/` bundles the daemon, the
  embedder helper and `models.json`, gated by `scripts/verify-vsix.js`, which
  asserts on the ARCHIVE CONTENTS rather than on vsce's exit code. `"private":
  true` is gone. What is left before a stranger can install it: **signing**
  (unsigned macOS binaries are quarantined by Gatekeeper, so the daemon never
  starts) and a marketplace listing, which is a founder action.
- **macOS HAS now executed on hardware** — macos-latest, run `31204152210`,
  `ok mochiii/daemon 53.019s`. `LOCAL_PEERCRED` had never run anywhere
  before that. Four macOS runs were needed to get there, and three product bugs
  unreachable from a Linux desk fell out of the first three: a socket path over
  the 103-byte `sun_path` budget, a workspace grounded against an unresolved
  spelling, and a bind returning `EEXIST` where Linux returns `EADDRINUSE`.
  **All three were per-kernel constants mistaken for Unix ones.**
- **Windows compiles and its seams are written**, still labelled NOT RUN ON
  HARDWARE — the Windows jobs build and test, but the peer-auth and
  junction-resolution claims specifically have not been exercised. It is no
  longer true that "Windows does not run at all" — see
  `MASTER_PLAN_2026-08-07.md` §2.
- **`LICENSE` exists** — 122 lines, proprietary, "All rights reserved". It is
  *not* an open-source licence, and that is a constraint on marketplace framing
  rather than a missing file.
- **Active plan:** [`ULTRA_MASTER_PLAN_2026-08-08.md`](ULTRA_MASTER_PLAN_2026-08-08.md)
  — packaging. Stages 3.0–3.4 are **done**; Stage 3.3 is written but has never
  fired (no tag pushed); **Stage 3.5 (signing) has not started** and is blocked
  on the Apple enrolment, which is Tier 0's only remaining item.

  > **Corrected 2026-09-02.** This bullet said the plan was *"Blocked on a P0
  > first: `/mcp-server` in the VS Code extension executes a binary out of the
  > opened repository"*. That P0 was closed the day after the sentence was
  > written — Stage 3.0, `b7e393d`, **two** RCE paths rather than one, both
  > confirmed by execution in a real Extension Development Host and both
  > neuter-verified. The plan linked on the line directly above has carried a
  > ✅ DONE banner on it ever since.
  >
  > This is the most expensive stale claim the repository has held. `README.md`
  > ranks this document **#1, "The entry point"**, so it is the first thing a
  > new reader opens, and it told them the project was blocked on a live remote
  > code execution for three and a half weeks after the fix shipped. A register
  > that overstates what is open costs the same as one that understates it, and
  > it costs the most at the top of the reading order.

---

## PART 0 — How to work on this project

This discipline is why the codebase is in the shape it is. It is not optional.

1. **Audit first, fix second, document third.** An independent pass proves claims
   by *live execution*, not code reading, and produces a PASS/FAIL/PARTIAL table
   with reproducibility rates. Fixing is a separate task; documenting a third.
2. **Never round up, and correct severity in *both* directions.** "Mostly works"
   is not "works"; 4-of-5 fixed is reported as 4-of-5. Severity moves up (FAIL-2
   went Moderate→High once exploited live) *and down* (a "confirmed CRITICAL
   ship-blocker" became a latent defensive gap once a live repro proved the path
   unreachable). Downgrading a scary claim to what the evidence supports is the
   same discipline as upgrading a quiet one.
3. **Verify against real execution, never memory or comments.** This project has
   a documented history of doc and comment claims that were false and were caught
   only by running the code. This very file was one of them.
4. **A fix's test must enter through the same door the user does.** Learned the
   hard way: a file-creation fix passed every engine-layer test while being
   unreachable from every shipped client, because a parser rejected the input
   first. Test through production entry points.
5. **Every fix has a test demonstrated to fail when the fix is neutered** —
   neutered and observed, not asserted. This is the single most valuable habit
   here; roughly 20 fixes carry a recorded neuter result.
6. **Nobody but the founder closes an item.** Every task stops at
   "implemented and verified."
7. **Isolated commits per concern.** Don't bundle unrelated changes.
8. **Ratchets only tighten.** `scripts/coverage-floors.txt` goes up, never down.
   `scripts/errcheck-ceilings.txt` fails if the count moves in *either*
   direction — a new unchecked error is fixed, never grandfathered.

**Build notes**
- `./...` fails from the repo root — the root is not a module. Name the six
  `go.work` modules explicitly: `daemon`, `editapply`, `protocol`,
  `clients/tui`, `helper`, `proxy`.
- `staticcheck`/`errcheck` live in `~/go/bin`; the Go toolchain is at
  `~/.local/go/bin`. A lint step that silently passes is usually a `PATH`
  problem — check that first.
- `-tags eval` code is **not** compiled by an untagged `go vet`. Both of these
  have hidden real breakage before.
- Rebuild affected binaries after cross-module changes; daemon, TUI and the
  extension drift from source otherwise.

**Standing constraints**
- Secrets live in `.env` and `proxy/.env`. Redact before printing:
  `sed -E 's/(mochi_|sk-or-v1-)[A-Za-z0-9_-]+/\1[REDACTED]/g'`.
- `ForbiddenEnvNames` must never reach an MCP subprocess, however loudly a
  config asks.
- **Lane B is unconfined.** The claim is "you approve every call and everything
  is audited" — never "it is sandboxed."
- `api_keys.key_prefix` is **not unique**; always target rows by `id=eq.<uuid>`.

---

## PART 1 — What Mochiii is

A **security-first, retrieval-grounded AI coding assistant** running as a local
daemon on the developer's own machine. Product thesis: **trust is a feature** —
an agent with write access to your code and your inference credential should be
held to safety-critical engineering standards.

- **Local daemon** (`daemon/`) — serves CLI, TUI and VS Code clients from one
  shared indexed understanding of the workspace ("one brain, thin clients").
  Unix socket, `0600`, in a `0700` per-user runtime dir — or a named pipe on
  Windows — peer-authenticated by kernel-supplied credentials: `SO_PEERCRED` on
  Linux, `LOCAL_PEERCRED` on macOS (**run on hardware**, run `31204152210`), and
  on Windows the pipe's DACL plus `GetNamedPipeClientProcessId`
  (`protocol/peerauth_windows.go`, **NOT RUN on hardware**). Its header records why
  `ImpersonateNamedPipeClient` — the obvious answer — is the wrong one: every
  go-winio dial connects at `PipeImpLevelAnonymous`, so an impersonation check
  would refuse this product's own clients every time. Only platforms that are
  none of those three refuse every connection.
- **Retrieval/indexing** — local ONNX embeddings (BGE-small int8) via a
  subprocess helper; hybrid retrieval (vector + FTS5 lexical, RRF-max fusion
  K=60, class-aware rerank). Honours nested `.gitignore` at every directory
  level. **The index rebuilds incrementally** — `daemon/watcher.go` runs a
  debounced recursive workspace watcher, started by `Server.startWorkspaceWatcher`
  from `main`, and it calls `reindexFile` per changed path. What is still missing
  is a **staleness signal**: nothing records when the index was built, so a stale
  one still reports `grounded ✓`. It degrades gracefully when absent.
- **Edit engine** (`editapply/`) — five confinement gates, backups, multi-run
  undo. Forward and undo writes are both atomic (temp+rename) and
  symlink-refusing. **Audited against POSIX semantics only** — the conformance
  suite skips its symlink vectors on Windows.
- **Secret detection** ("warn-mode") — filename-pattern gate plus
  entropy/keyword content heuristics. Structural-signature scrubbing is live at
  retrieval time; the entropy/keyword layers are **log-only**, pending D5.
- **Agent mode / MCP** — off unless configured. Lane A is this daemon's own
  in-process tools (confined, never writes); Lane B is a stdio subprocess with
  the user's full privileges (**unconfined**, requires `acknowledged_unconfined:
  true` typed by hand, every call human-approved and digest-bound).
- **Inference routing** — two paths to OpenRouter: direct (`daemon/provider.go`)
  and a managed proxy (`proxy/main.go`, on Railway). Both send `zdr:true`,
  `data_collection:"deny"`, `allow_fallbacks:true`, `ignore:["DeepInfra"]`.
  **The proxy enforces this** — a request lacking the routing block gets
  `403 zdr_required` (F1, verified live by wire probe).

---

## PART 2 — Where things live

**Confinement** — `editapply/apply.go` (`resolveSafeTarget`, the atomic
symlink-refusing writer), `editapply/create.go` (`resolveSafeNewPath`),
`editapply/protected.go` (`ProtectedDirNames`, `IsProtectedDirName`),
`editapply/secret.go` (`MatchesSecretName`), `editapply/atomicwrite.go`,
`editapply/applylock.go` (cross-process `flock`), mirrored in
`daemon/apply_cmd.go` (`restoreOne`, `confinedRestorePath`, `openNoFollow`).
The mirroring is enforced by `editapply/confinement_conformance_test.go` — read
its header before touching any of them.

**Indexing / retrieval** — `daemon/chunker.go` (`ScanWorkspace`, per-directory
`gitignoreMatcher`, `isPrunedDir`), `daemon/index_cmd.go` (`buildIndex`,
batches of 40), `daemon/reindex.go` (`reindexFile` — the only incremental path,
and it fires only for files the daemon itself just wrote), `daemon/fileref.go`
(`file:line` resolution), `daemon/retrieval_setup.go` (**resolved once at
startup and constant for the daemon's lifetime** — an index built afterwards
does nothing until restart), `rerank.go`, `vectorstore.go`, `lexicalstore.go`.

**Embedder boundary** — `daemon/helperproc.go`, `daemon/helperpath.go`
(`resolveHelperBinPath` — checks `<exedir>` before CWD, and its comment records
why), `helper/onnxembedder.go`, `daemon/modelfetch.go` +
`daemon/onnxruntimefetch.go` (pinned URLs, exact byte sizes, sha256 for every
asset — this is what makes the local-model claim true).

**Socket / daemon** — `daemon/main.go` (listen, lockfile, `reclaimStaleSocket`,
drain), `daemon/server.go` (dispatch by presence-of-key, not a type
discriminator — deliberate, see the comment), `protocol/peerauth.go` (Gate 3),
`server_limits.go` (Gate 5), `server_workspace_lock.go` (Gate 6),
`server_errors.go` (Gate 7, path scrubbing), `daemon/degraded.go` (five
degradation components with prewritten user-facing prose),
`daemon/modelerror.go` (closed error-class vocabulary; `Error()` is the scrubbed
string, `Detail()` is log-only).

**Protocol** — `protocol/protocol.go` owns the discovery convention
(`RuntimeDir`, `SocketDir`, `LockPath`, `LockFile`) and every wire type. Its
header explains that both sides must derive paths independently and identically;
honour that.

**Clients** — `clients/tui/` (bubbletea; `/reason` and `/refactor` prefixes,
`ctrl+n` reset, no search box), `clients/vscode/src/` +
`media/main.js` (search, native undo, auto-apply toggle, full ARIA pass; renders
via `textContent`, never `innerHTML`). **The extension manages the daemon**:
`daemonSupervisor.ts` probes the per-workspace lockfile, adopts a daemon that
answers a full handshake, and spawns one only when nothing does. It never
deletes a stale lockfile — that belongs to `reclaimStaleSocket` alone. Restarts
are bounded and back off; exit code 3 means "another window already serves this
repo" and is adopted silently rather than charged to the budget.

**Proxy** — `proxy/main.go` (auth → ZDR gate → model allow-list → quota
reservation → stream), `proxy/ratelimit.go` (token buckets + in-flight
semaphore; **per-instance**, so N replicas give one key N× its rate),
`proxy/metrics.go` (`/admin/metrics` behind `PROXY_ADMIN_TOKEN`, ≥24 chars or
the process fatals), `proxy/migrations/` (apply-by-hand via the Supabase SQL
editor; 0002 and 0003 are applied and verified live).
**`proxy/go.mod` has zero third-party requires — keep it that way.** It is the
only internet-facing component.

**Config** — root `models.json` (**tracked**; there is no `daemon/models.json`),
`daemon/config.go`, `daemon/mcpconfig.go` (the polarity contract is stated at
the top: absent means off, and lanes are structural rather than a field a typo
could flip).

**Scripts** — `scripts/lint.sh`, `coverage-ratchet.sh`, `errcheck-ceiling.sh`,
`fuzz.sh` (9 targets), `sigterm-drill.sh`, `soak.sh`, `latency-bench.sh`,
`agent-cost-bench.sh`. All quality gates; **none of them builds or packages
anything.**

**Project memory** —
`~/.claude/projects/-home-ravi-kiran-Desktop-Neww/memory/`, `MEMORY.md` index
plus per-topic files.

---

## PART 3 — Traps a cold reader falls into

- **"C1/C2/C3" names two unrelated clusters.** The Tier-4 operability cluster
  (`0599a11`, `6618dda`, `2241daa`) and the bug-hunt ship-blockers (`ee9e992`,
  `c7cff6c`, `a703978`) reuse the same letters for different work. Always name
  the cluster.
- **The eight LOWs live outside the repo**, at
  `~/.claude/plans/what-can-we-improve-snappy-music.md`, so the pointer looks
  dangling from a checkout. All eight are transcribed into `OPEN_ITEMS.md` §2.
- **`.mochiii/index/` in this repo is months stale** and the product will
  still report `grounded ✓` against it. That is the bug, not a local accident.
- **CI does not block.** No branch protection is configured, so a red run is a
  signal someone has to read. `.githooks/pre-push` is the compensating control,
  and `--no-verify` bypasses it. **Changed 2026-09-20:** this used to say
  protection was *unavailable* (private, free plan, endpoint answered 403
  Upgrade). The repo is now public and the endpoint answers 404 "no rule
  configured", so protection is available and simply not set up -- an owner
  action, not a platform limit.
- **`gh` resolves to the wrong repo from this directory.** Check before trusting
  any `gh` output.
- **JSON `null` → `bool` unmarshals with a nil error**, and hex caps Shannon
  entropy at exactly 4.0. Both have caused real misreadings here.
