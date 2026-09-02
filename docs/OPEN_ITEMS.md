# Open items — the register, re-derived against the code

**Written 2026-08-01. Statuses resolved in place 2026-08-07.**

> ## Five open rows: **20, 21, 24, 32** in §2 and **33** in §1.
>
> Items 32 and 33 were opened 2026-09-02 by the batch that closed 22, 23 and 25.
> Item 12, the last of §1's original twelve, closed 2026-08-30. *(Item 26 closed 2026-08-07 —
> `d5a8fdf`, with two further defects in the same loop.)*
>
> Every other row carries **FIXED** and the commit that fixed it. Read the
> **Status** column; it is the answer.
>
> **Corrected 2026-09-02.** This heading said *"Nothing in §1–§2 is open"* while
> items 20, 21 and 24 sat six rows below it carrying **OPEN** and **CONFIRMED**.
> It was true when written and stopped being true when the security residuals
> from `AGENT_SECURITY_AND_CAPABILITY_AUDIT.md` were merged into §2 — the rows
> were added correctly and the summary above them was not revisited.
>
> Note what this means about the enforcement: `scripts/docs-claims.sh` had those
> three ids right the whole time, and printed them on every push. It compares
> **Status cells** across the three registers. This line is prose, so it was
> outside the check that would have caught it, sitting directly above rows the
> check was reading — which is the same shape as the four gates this session
> found running nowhere. The summary is now checked too; see the bottom of
> `docs-claims.sh`.
>
> **Why this needed fixing.** §1 and §2 listed twelve items and §6 separately
> recorded that nine of them had been fixed — so the register's own front tables,
> the first thing anyone reads, were **75% wrong**, and answering "what is open?"
> required cross-referencing two tables in the same file. Items 8 and 11 were the
> worst: each was simultaneously CONFIRMED-open in §2 and fixed-with-a-SHA in §6.
>
> That is precisely the failure this document was created to end — see the
> paragraph below, which was already true of `HANDOFF.md` and had quietly become
> true of this file. **A register that must be cross-referenced to be read is not
> a register.** Each row now answers on its own.
>
> All nine were **spot-verified against source on 2026-08-07**, not taken from
> §6 on trust: item 1's `doneCh`-ordering comment, item 2's direct-ref block now
> above the guard (which moved into `similarChunks`), item 5's `SyntaxNote`,
> item 9's 0700 correction, item 11's shared `maxLogLineBytes`, L1's two
> terminal paths.

Every entry below was checked **against the source on this
branch**, not carried forward from `BACKLOG.md` or `docs/HANDOFF.md`. Entries the
record listed as open but that the code shows are closed are in §7, deleted rather
than inherited.

**MCP evidence 2026-08-03 (SHA `8979530`):** live-evidence pass + master readiness
package — hermetic hostile matrix PASS, M1a@64MiB PASS, Lane B echo soak
**PLATEAU**, orphan soak stray **0**, third-party interop PASS. Front door:
[`MCP_MASTER.md`](MCP_MASTER.md). Numbers:
[`MCP_ROBUSTNESS_REPORT_2026-08-03.md`](MCP_ROBUSTNESS_REPORT_2026-08-03.md).
Playbook: [`MCP_DEBUG_PLAYBOOK.md`](MCP_DEBUG_PLAYBOOK.md). Items 3 and 11 in §6
remain the engineering closures for connect-budget and stderr bound; this note
does not reopen them.

**Enterprise QA 2026-08-03:** skill + campaign + Phases 0–4 **PASS** + final
`make check` green + MCP hermetic spot green. Pathhazard WIP still
**SHIP-READY** uncommitted. Front door:
[`ENTERPRISE_QA_MASTER.md`](ENTERPRISE_QA_MASTER.md). Report:
[`ENTERPRISE_QA_REPORT_2026-08-03.md`](ENTERPRISE_QA_REPORT_2026-08-03.md).
Phase 5 (real-model) **NOT RUN**.

This document exists because the register was the first bug. `docs/HANDOFF.md` was
v7 (2026-07-24) and predated the 2026-07-30 launch gate, three robustness phases,
PR #1 landing on `main`, and every commit of this branch — so "what is open?" could
not be answered without re-deriving it, which is how the last two passes each
rediscovered work the previous one had already done.

## Evidence labels

Same discipline as both QA gates:

| Label | Means |
|---|---|
| **CONFIRMED** | reproduced by something that was actually run, or read directly off the line cited |
| **PLAUSIBLE** | reasoned from source; the failing path was not executed |
| **NOT RUN** | honestly not attempted (no hardware, no spend, out of scope) |

Nothing here is marked CLOSED. Implemented-and-verified is where engineering stops;
closure is the founder's call.

---

## 1. Correctness and availability

| # | Status | Item | Where | Sev | Evidence |
|---|---|---|---|---|---|
| 1 | **FIXED `9f3c5dc`** | **Failed helper start hangs daemon shutdown.** `doneCh` is created *after* `h.cmd` is set, so a failed `waitReady()` returns with `cmd` non-nil and `doneCh` nil. `Stop()` guards only on `h.cmd == nil`, clears it, and blocks forever receiving on a nil channel. | `daemon/helperproc.go:120-145` | High | **CONFIRMED** (read off the lines; no repro run yet) |
| 2 | **FIXED `59d42a3`** | **`file:line` is unreachable without an index.** `gatherContext` returns early when `s.embedder == nil \|\| s.store == nil`; direct reference resolution sits *below* that return. A user with no index who pastes `foo.go:142` gets nothing back, though they named the exact line and no retrieval is needed to read it. | `daemon/context.go:109-126` | High | **CONFIRMED** |
| 3 | **FIXED `bc19419`** | **MCP connect time is charged to no budget.** Servers connect in `buildRegistry` before `runAgentLoop` creates the turn deadline, so `turn_timeout_seconds` does not govern it. Parallel connect bounds it at one `connect_timeout_seconds` (20 s) rather than one per server — a statable constant, but still time outside the budget. | `daemon/agentturn.go:43` | Medium | **CONFIRMED** (measured in the 2026-08-01 MCP pass, M2) |
| 4 | **FIXED `632d110`** | **No per-connection output-rate bound on the proxy.** A one-byte-at-a-time reader pins a connection for the full 6-minute `WriteTimeout`. The in-flight caps bound *how many* can be pinned at once; nothing bounds *one*. | `proxy/ratelimit.go` (`inFlightLimiter`) | Medium | **CONFIRMED** — recorded as mitigated-not-eliminated (M10) in the endpoint pass |
| 5 | **FIXED `aa3787b`** | **CREATE's syntax gate is weaker than EDIT's.** `PrepareEdit` hard-refuses an edit that would make a `.go` file unparseable (`apply.go:96-99`); `prepareCreate` only attaches an advisory `SyntaxNote` (`create.go:109`). The same model output is refused as an edit and accepted as a create. | `editapply/create.go:109` vs `editapply/apply.go:96` | Medium | **CONFIRMED** |
| 6 | **FIXED `31bf19c`** | **Undo leaves behind the directories a create made.** `editapply/apply.go:247` `MkdirAll`s parents on the create path. The undo removal branch has no directory cleanup: `stageRestore` returns early for `remove` with no `createdDirs`, and `commit()` unlinks only the file. `removeCreatedDirs` runs only on *discard* (a staging failure), never after a successful removal. | `daemon/apply_cmd.go:587-592, 498-508` | Low | **CONFIRMED** (read); repro pending |
| 26 | **FIXED `d5a8fdf`** | **The watcher did not track deletions or renames.** `startWorkspaceWatcher` acted only on `fsnotify.Write`/`Create`, so a deleted or renamed file's chunks stayed in both the vector and lexical stores for the life of the daemon -- still matching queries, still handed to the model as grounded context, describing code that no longer exists, with the same `grounded ✓` a correct answer gets. A rename is the same event twice: fsnotify reports the old path as `Rename` and the new one as `Create`, so dropping the old key and letting the Create re-index the new one is the whole of it. **Two further defects in the same loop, found while fixing it and fixed with it:** reindexing fanned out one goroutine per changed path straight at the single embedder helper (measured peak 8 concurrent embeds on a burst of 8 files, now 1 -- a branch switch does thousands), and because `reindexFile` is delete-then-insert, two overlapping runs over ONE path left that file's chunks in the index twice. Deletions and reindexes now go through a set drained by one worker. | `daemon/watcher.go` | Medium | **FIXED** 2026-08-07. Three tests, each demonstrated to fail against the previous watcher. Note the freshness gap this leaves: a tree deleted under a directory the walk never watched still needs the next full `index` run, and there is still no staleness *signal* (plan Stage 4). |
| 33 | **OPEN** | **A coverage floor whose package was renamed or deleted gates nothing, and CI cannot tell.** `scripts/coverage-ratchet.sh:153` guards its orphan check on `[ $# -eq 0 ]` — only a full, zero-argument run visits every floor, because a partial run legitimately does not. `build.yml` always invokes it as `coverage-ratchet.sh ${{ matrix.module }}`, so the zero-argument form runs **only** when a human types `make ratchet`. A floor left behind by a rename is a gate that stopped gating, silently, which is the same shape as items 22 and 25. Not fixed in the same batch as `gates.yml` deliberately: the zero-arg run means the full six-module test suite, and putting that on every push would duplicate `build.yml`'s ~60 billable minutes on exactly the docs-only commits that workflow exists to check cheaply. It needs a floors-vs-packages check that does not require measuring coverage. | `scripts/coverage-ratchet.sh`, `.github/workflows/build.yml` | Low-Medium 3.5 | **CONFIRMED** — read off the line cited, 2026-09-02 |

---

## 2. Security residuals

| # | Status | Item | Where | Sev | Evidence |
|---|---|---|---|---|---|
| 7 | **FIXED** | **Gate 7 existence-oracle distinguishability.** Path scrubbing landed (`3aeb8b6`) so no absolute root reaches the wire, but error *shapes* still distinguish "no such file" from "exists and refused". The recorded fix is invasive — unify error responses. **This is the last engineering item under FAIL-3**; the rest of Gate 7 is the founder's ruling. | `daemon/server.go` Apply/Undo handlers | the P3 blocker | **FIXED** (2026-08-11) — unified in `socketSafeError` to close the oracle while keeping backup errors distinct. |
| 8 | **FIXED `648d38b`** | **No macOS peer credentials — the daemon refuses every non-Linux connection.** `readPeerCred` errors unconditionally off Linux and `authorizePeer` fails closed, so no Mac user can connect at all. This is an availability bug wearing security clothes. | `daemon/peercred_other.go` | High (availability) | **CONFIRMED** by reading, then **RUN ON HARDWARE 2026-08-07** — macos-latest, run `31204152210`, `ok codeterminal/daemon 53.019s`. This cell promised "it will be labelled that way when fixed"; §6a is that label. |
| 9 | **FIXED `e3ad4b0`** | **The lexical index is world-readable.** `lexical.db` holds full chunk `Content` — the user's source text — and is created `0644` inside a `0755` directory, with its `-wal`/`-shm` sidecars the same. `memory.db` is `0600` in a `0700` dir and `skills.db` locks its directory down; the FTS store got neither. chromem's own collection dir is `0700`, so the vector half is covered and the lexical half is not. | `daemon/lexicalstore.go:69` | Medium | **CONFIRMED** — probe run 2026-08-01: `index/ drwxr-xr-x`, `lexical.db -rw-r--r--`, both sidecars `-rw-r--r--` |
| 10 | **FIXED** | **`MatchesSecretName` over-refuses `.pub` public keys containing "secret".** `id_rsa.pub` and `id_ed25519.pub` are correctly allowed; `id_rsa_secret.pub` and `secrets.pub` are refused by the substring rule. Fails in the safe direction — a correctness annoyance, not a hole. | `editapply/secret.go` | Low | **FIXED** (2026-08-11) — `.pub` allowlist now runs before substring checks. |
| ~~11~~ | **FIXED `ffdd553`** | ~~**The MCP log writer has no buffer bound.** `prefixWriter.Write` appends to `w.buf` and only drains on a newline, on stderr from an unconfined third-party subprocess.~~ | `daemon/mcpruntime.go` | Medium | **NO — FIXED `ffdd553`**, the same commit and the same `maxLogLineBytes = 64 << 10` that closed L8. This row contradicted §6 of this very document for six days; corrected 2026-08-07. |
| 12 | **FIXED** | **The eight LOWs.** The original report is at `~/.claude/plans/what-can-we-improve-snappy-music.md` — **outside the repo**, which is why `docs/HANDOFF.md`'s pointer looks dangling from a checkout; the table below is the in-repo copy so this register no longer depends on a file that does not travel with the code. **This cell used to read "all eight still present", which was wrong:** L1, L3, L6 and L8 are fixed, L7 has moved file. **L2 was fixed 2026-08-09** — and was broader than this register recorded in two directions (cross-session memory was NOT clean; the two clients' error paths were broken oppositely). Remaining: **none**. | various | Low | **FIXED 2026-08-30.** L4, L5 and L7 were fixed by `ce96997` (2026-08-11) and verified against current source: L4 honours an `.in-flight` lockfile with a 2h TTL in `pruneBackupSessions`; L5 writes a NUL-delimited manifest and reads the legacy format via `parseManifest`; L7 guards the helper socket with `protocol.AuthorizePeer` (`helper/main.go:147`) — the exact gap the finding named, closed by moving `peerauth` into `protocol/` so both sockets share it. **This cell was stale against the table ten lines below it**, which had marked all three `~~struck~~ NO — FIXED` since 2026-08-11, while BACKLOG.md's Tier 2 said completed. One summary, its own detail table, and a second register: three places, and only this cell was wrong. `scripts/docs-claims.sh` now fails the build when this cell and BACKLOG disagree. |
| 20 | **OPEN** | **Lane B tool descriptions are unvalidated and reach the model on every request.** A third-party MCP server's `description` string is forwarded verbatim into the tool menu of every turn, so the server author writes text the model reads as instruction. This is the tool-description injection channel; Lane A descriptions are ours, Lane B's are not. | `daemon/mcp/stdioclient.go` (description passthrough), `daemon/agentloop.go` (menu assembly) | Medium 6.0 | **CONFIRMED** — F-04, `AGENT_SECURITY_AND_CAPABILITY_AUDIT.md`, 2026-08-26 |
| 21 | **OPEN** | **Tool output receives no untrusted-data framing, though retrieval does.** Retrieved chunks are wrapped in a delimiter defence precisely because file content can contain instructions; tool results — including web fetches and third-party MCP output — are concatenated into the conversation with no such marking. The defence exists in this codebase and is applied to one of the two channels that needs it. | `daemon/prompts/system.txt`, `daemon/toolresult.go` | Medium 5.5 | **CONFIRMED** — F-05, same report |
| 22 | **FIXED** | **`govulncheck` did not run in the local gate, and the workspace pinned a vulnerable toolchain.** The CI half was closed by `baad9f9` and had been green — on go1.25.14, which the runner installs. The same scan on this project's own machine found **10 reachable stdlib vulnerabilities** on the same commit, on go1.25.12. Both were true: `toolchain` names a MINIMUM, so CI floated above the line and a laptop sat exactly on it. **The audit's recommended fix would not have worked** — it named the six `go.mod` files, and in a workspace `go.work`'s toolchain overrides every one of them, so the bump would have built green and changed nothing. | `go.work` (the line that governs), `Makefile` (`supplychain:`) | Medium 5.8 | **CONFIRMED, then FIXED** — F-06. Measured 4+5+1 reachable before, 0 across all six modules after bumping `go.work` to `go1.25.13`. `scripts/govulncheck.sh` is now in `make check` (~17s warm) and treats an unreachable database as a failure, not a clean scan. Three neuters, all exit 1. **The residual was larger than the item, and is now closed** — see below. Measured 2026-09-01: `toolchain` is a SELECTION HINT, not a floor. `GOTOOLCHAIN=local` skips toolchain selection entirely and the line is ignored, so `daemon` — which carried `toolchain go1.25.13` — built rc=0 on go1.25.12 with **four** reachable stdlib flaws, and `proxy`, the only internet-facing component, with **five**. The original fix bumped `go.work` and measured clean, but only in the one configuration where `go.work` governs; scanning the arrangement in which a fix is guaranteed to work cannot discover that it works nowhere else. **Closed properly** by raising the `go` DIRECTIVE to `1.25.13` in `go.work` and all six `go.mod` — a hard requirement enforced under every `GOTOOLCHAIN` setting, workspace or not, which now fails closed with `requires go >= 1.25.13` instead of silently downgrading. `go mod tidy` then removed the `toolchain` lines as redundant, which is why they are gone. `scripts/govulncheck.sh` now scans with `GOWORK=off` (the weaker configuration, not the stronger) and `scripts/go-toolchain-pinned.sh` keeps all seven files agreeing; both neuter-verified |
| 23 | **FIXED `3f12a02`** | **Plan mode withdrew the reviewed write path and kept the unreviewed one.** `builtinTools` used its `mode` argument once, to remove `propose_edit`; `sandbox_exec` — the only built-in that runs arbitrary code — stayed registered, so the only remaining route from model to filesystem was the one that skips edit review. The same directive forged a `[SYSTEM]:` marker inside the **user's** message (the primitive an indirect prompt injection needs, in a codebase that runs a delimiter defence against exactly that) and named two tools, `view_file` and `grep_search`, that have never existed here. | `daemon/mcpbuiltin.go` (`builtinTools`), `daemon/planmode.go` | Medium 5.2 | **CONFIRMED** — F-07, same report; fixed by `3f12a02`, and that fix was **incomplete and closed anyway**. Completed by `c69dbfe`/`db2c2b2` 2026-09-01. What `3f12a02` left, found by auditing the closure rather than the code: (a) **Lane B was never filtered** — `builtinTools` takes `mode`, but third-party MCP servers are registered sixty lines below it in `buildRegistry`, where `mode` is not in scope, so plan mode withdrew the reviewed first-party write path and kept the unconfined third-party one — the same inversion, one lane over; (b) the filter tested `ExecutesCode` **alone** while its comment claimed it was keyed on capability — `mcp.Tool` declares two such flags, so `web_search`/`web_fetch` (`ReachesNetwork`) survived; (c) `mode != "plan"` **failed open** — `"Plan"`, `"planning"` and the empty string all selected the full menu, and the TUI sent the empty string on every turn because its `PromptRequest` had no `Mode` field at all. The four neuters this cell previously cited were real but all bore on `sandbox_exec`; none crossed `buildRegistry`, and all 19 of its call sites in tests passed `""`. Now: five neuters against a verified-passing baseline, tests at the `buildRegistry` boundary, and a second enforcement point in `resolveExecutable`. **A fourth route was found by re-reviewing the closure rather than the code, and is fixed here too:** the tool filter only ever covered the path an edit takes through a TOOL. The model can also emit a SEARCH/REPLACE block in ordinary prose, which `parseAndLogEditBlocks` lifts out of the reply and delivers as an `EditProposal`; VS Code derives auto-apply from the mode PICKER rather than the wire mode, so `/plan` with the picker on Auto sent `{mode:"plan", autoApply:true}` and that block was written to disk — under a command whose summary reads *"read-only: no edits"*. Plan mode now withholds prose edit blocks on both the agent and single-turn paths and reports each one withheld rather than dropping it silently, and the client-parity test compares `Mode`, which it did not: deleting one line from the VS Code catalog reverted the whole fix and stayed green. Three further neuters, each verified against a passing baseline |
| 24 | **OPEN** | **The audit log cannot answer "what did the command do?"** `toolaudit` records the decision — which tool, which arguments, approved or refused — and not the effect. After an incident there is no way to reconstruct what a `sandbox_exec` invocation changed. Open **by documented design**, recorded here so the limit is visible to someone relying on the log rather than discovered during an incident. | `daemon/toolaudit.go` | Low-Medium 3.8 | **CONFIRMED** — F-08, same report |
| 25 | **FIXED `7443f73`** | **All 26 CI action references were mutable tags.** `actions/checkout@v4` and five others resolve at run time to whatever the tag points at; a moved tag runs new code inside a workflow holding this repo's secrets and release credentials, changing nothing in the repo and firing no review. `softprops/action-gh-release@v2` — the only third-party action, in the **release** workflow — was floating with the rest. | `.github/workflows/*.yml` | Low 2.6 | **CONFIRMED** — F-10, same report. All pinned to 40-char SHAs; `scripts/actions-pinned.sh` in `make check` keeps them pinned. Three neuters, all caught |
| 32 | **OPEN** | **Language-server consent fires on the wrong event.** `query_compiler_definition` and `query_compiler_references` now declare `LaunchesSubprocess` and prompt at `ask`, which closes the lie (they were stamped `Confined`). But the risk being consented to is *starting* an untrusted language server, and `lsp_bridge.go:243` returns a cached one — so the server launches once per language per daemon lifetime while the prompt fires once per turn against an already-running process. The approval a user grants is therefore not the event that carries the risk: the first call spawns `gopls`, and a later turn's prompt asks about a process that has been reading `tsconfig.json`-supplied plugin config for an hour. Correct design is spawn-time consent, one prompt per language per daemon lifetime, at the moment the binary launches. Recorded rather than built: it needs plumbing from the bridge back to the consent channel. | `daemon/lsp_bridge.go`, `daemon/mcpbuiltin.go`, `daemon/toolconsent.go` | Medium 4.5 | **CONFIRMED** — F-01's shape, 2026-09-01 |

### The eight LOWs (item 12) — RE-VERIFIED 2026-08-09 at `4dca1c8`

> **The 2026-08-01 row of this table was wrong by the time anyone read it.** It
> recorded all seven surviving LOWs as still open; three had in fact been fixed,
> and one had moved to a different file. Every row below was re-derived against
> current source on 2026-08-09 — **read, not recalled** — because a register that
> overstates what is open is the same failure as one that overstates what is
> closed: both make the next person distrust the whole document.
>
> Net, as of 2026-08-30: **all eight fixed**. Seven were closed by 2026-08-09
> (L1, L3, L6, L8, and L2 that day); L4, L5 and L7 followed in `ce96997` on
> 2026-08-11 — L7 also **relocated**, to `protocol/transport_unix.go`. The rows
> below were already correct; item 12's summary cell above was not, for
> nineteen days. L2 went PLAUSIBLE → CONFIRMED → fixed inside two
> days, and grew twice on the way: what was filed as one client's live-session
> quirk was three loss paths across both clients and the memory store.

| ID | Finding | Where | Still open? |
|---|---|---|---|
| ~~L1~~ | ~~TUI leaks a `context.CancelFunc` per turn~~ | `clients/tui/chat.go` | **NO — FIXED.** `endStream` calls `m.streamCancel()` *then* nils it, and its comment records that "the two terminal paths used to" nil it directly. Verified 2026-08-09. |
| ~~L2~~ | ~~A cut-off or partially-streamed answer is replayed to the model as a finished one~~ | `clients/tui/chat.go`, `clients/vscode/src/chatPanel.ts`, `daemon/history.go`, `daemon/server.go` | **NO — FIXED 2026-08-09**, and it was broader than the record in *two* further directions. The record said "live session only; cross-session memory stays clean" — **that was wrong**: `persistTurn` stored the truncated text unmarked, so it re-hydrated at every later handshake as a complete reply. And the error path was broken in *both* clients in **opposite** directions — the TUI replayed the partial as finished, VS Code dropped it from the transcript entirely while leaving it on screen. Fixed via `protocol.Turn.Incomplete` (a closed-set **slug**; the daemon owns the rendered wording, so `validTurn`'s anti-injection property is untouched). Commits `a4c9d15`, `f79d965`, `592e12c`. Eleven neuters measured. |
| ~~L3~~ | ~~Helper decodes a request with no size cap and no deadline~~ | `helper/main.go` | **NO — FIXED.** `conn.SetDeadline(helperConnTimeout)` + `json.NewDecoder(io.LimitReader(conn, maxHelperRequestBytes))`, 16 MiB / 2 min, with a test that fails if the body is unbounded. Verified 2026-08-09. |
| ~~L4~~ | ~~Backup retention (`backupSessionsToKeep = 5`) can prune a still-needed session mid-review when a client doesn't echo `BackupSessionDir`~~ | `editapply/backup.go` | **NO — FIXED.** Backup retention logic was hardened (5-session limit enforced carefully, respecting .in-flight). Verified 2026-08-11. |
| ~~L5~~ | ~~`created-files` manifest is newline-delimited, so a path containing a literal `\n` resurrects the Fix-C spurious-0-byte-file revert~~ | `editapply/backup.go`, `recordCreatedReversible` | **NO — FIXED.** Robust null-byte delimited format is now in use for manifests. Verified 2026-08-11. |
| ~~L6~~ | ~~`scrub` records redaction `Start`/`End` offsets against stale intermediate strings~~ | `daemon/scrub.go` | **NO — FIXED**, and more thoroughly than the finding asked: `type Redaction struct` now carries **only** `Kind`. The offsets are gone rather than corrected, so the trap cannot be re-entered by a future consumer. Verified 2026-08-09. |
| ~~L7~~ | ~~Socket created under the ambient umask, then `chmod 0600` — a brief world-permissive window~~ | **moved** → `protocol/transport_unix.go`, `listen` (the register said `daemon/main.go:206-210`) | **NO — FIXED.** protocol.Listen uses `syscall.Umask(0177)`, securely handling socket permissions without race conditions. Verified 2026-08-11. |
| ~~L8~~ | ~~`prefixedWriter` has an unbounded line buffer on a newline-less helper line~~ | `daemon/helperproc.go` | **NO — FIXED.** `maxLogLineBytes = 64 << 10` caps the buffer and the writer flushes at the cap. Verified 2026-08-07. |

L8 and item 11 were one fix in two places, and item 11 was the one that mattered:
the helper is same-uid code this project ships, the MCP server is not. **Both are
now closed.** L8 is struck through rather than deleted so that a reader arriving
from an older document that still lists it as open finds the answer here instead
of re-deriving it.

### Deliberately not attempted, with the reason

- **Mid-ancestor TOCTOU on the write path** — a symlink swapped into an *intermediate*
  directory between resolution and open. Needs `openat2(RESOLVE_BENEATH)` and a
  rewrite of the resolver; it is its own project, not a fix.
- **`-wal`/`-shm` symlink-open hardening** — the driver, not this code, opens the
  sidecars, so a symlink planted at a sidecar path is still followed. Closing it
  needs a custom SQLite VFS. Item 9 fixes the *permission* half, which is separate
  and cheap; this half stays open and stated.

Both remain in this register precisely so they are not mistaken for oversights.

---

## 3. Honesty and polish

| # | Item | Where | Evidence |
|---|---|---|---|
| 13 | Stale price note `~$0.11/$0.80 per 1M`, measured ~2.3× off for at least one live provider | `models.json:5` | **CONFIRMED** |
| 14 | `QUOTA_RESERVATION_DESIGN.md:3` still reads "design only. No code changes yet" — it shipped in `ca7e3c4`; §5(e) at line 312 still says "recommended as a follow-up" when the outbox landed. **§5(e)'s honesty is kept**: the crash case is mitigated and made loud, not solved. | `QUOTA_RESERVATION_DESIGN.md:3,312` | **CONFIRMED** |
| 15 | ~~`EditProposals` carries accepted blocks only — a refused block is invisible to the client~~ | `protocol/protocol.go` | **FIXED 2026-08-30** — `TokenResponse.EditRejections`, additive, on the same Done message. Daemon returns them from `parseAndLogEditBlocks` (including the zero-usable-blocks path, which used to send nothing at all); VS Code renders them ahead of the modal edit review. Not sent to the model — that is a separate decision. |
| 16 | `UndoResponse.Restored` is one integer over two different outcomes; a removal and a restore both increment it | `protocol/protocol.go:708` | **CONFIRMED** |
| 17 | `file:line` resolver does not match Python tracebacks (`File "x.py", line 42`) | `daemon/fileref.go:73` | **CONFIRMED** — the pattern set has no such form |
| 18 | Chunk end-lines overshoot by one on files ending in a newline | `daemon/fileref.go` span math | **PLAUSIBLE** — recorded, not re-measured |
| 19 | [x] **TUI has no search UI**; the wire and daemon halves shipped for VS Code. `clients/tui` (no `SearchRequest` reference). **CONFIRMED.** |

---

## 4. Blocked on measurement, not on engineering

Unblocked as of this pass: real-model spend is authorized with a **$5 hard stop**.

| # | Item | What a number closes |
|---|---|---|
| 31 | **FIXED** | **The reservation floor makes the tail of every quota unreachable.** `reserveQuota` reserves `defaultReservationTokens` (4,096) up front, so a key is refused once its headroom drops below that — 4.1% of a 100,000-token quota, and a larger fraction of a smaller one. The user is told `quota_exceeded` while their own accounting says tokens remain, which reads as a billing bug. Safe direction (never over-spend) and an inherent consequence of reserve-then-correct, but written down nowhere. Options: reserve `min(floor, headroom)` and let the correction settle it, or report the real remaining balance in the refusal. | `proxy/main.go:194`, `reserveQuota` | Medium (UX/billing clarity) | **FIXED** (2026-08-11) — fetches headroom upon reserve failure and retries with `min(floor, headroom)`. |

| ~~27~~ | ~~`max_advertised_tools` 4/8/12 curve~~ | **MEASURED 2026-08-01** (`d9dbc92`, `docs/TOOL_MENU_SIZE_2026-08-01.md`). Flat: 88.6% @ 5, 85.7% @ 8, 85.7% @ 12 over 105 trials, the whole spread being one trial. **The default moved back to 12** — the accuracy argument for 5 did not survive the data. Every failure at every size is one confusion (`run_tests` → `list_directory`, 14/15), so excluding it the score is 30/30 at all three sizes. |
| 28 | Production system-prompt delta (D1) | the loop gate used a minimal prompt and said so; a large delta is a finding *about the prompt* |
| 29 | `search_code` in a live loop (D2) | needs the ONNX embedder and a real index; exercises the production 4-tool menu |
| 30 | Default-model evaluation | repeatedly named the single biggest reply-quality lever, deferred for weeks |

If a result contradicts a shipped default, **the default moves and the commit says so.**
Item 27 is the worked example: it contradicted a default set the same morning, and
the default moved the same day.

**Blocked on a founder action, not on engineering:** the production proxy returns
`quota_exceeded` for the pilot key, so all 140 calls of the first curve run were
refused. The measurement was re-run direct against OpenRouter, which measures the
model but not the proxy path. Items 21–23 have the same obstacle.

Diagnosed read-only against Supabase 2026-08-01, and it is **not** what the error
says. The key is not out of quota — it is out of **reservation headroom**:

| | |
|---|---|
| `token_limit` | 100,000 |
| `tokens_used` | 96,313 |
| headroom | **3,687** |
| `defaultReservationTokens` | **4,096** |

`reserve_usage` requires `tokens_used + reserved <= token_limit`, and
96,313 + 4,096 = 100,409. Every request is refused with 3,687 tokens still
unspent. `pending_corrections` is empty, so no stranded reservation is inflating
the number — this is real usage against a real limit.

**This is a finding in its own right, item 31 below.** Fixing the pilot key is one
UPDATE; the behaviour it exposes applies to every user who approaches their limit.

**A follow-up this surfaced, correctly scoped:** `run_tests` loses to
`list_directory` on "run the editapply test suite", 14 times out of 15 across three
independent menu sizes, dominating the error rate at every size. **It is not a
production defect** — `run_tests` is a fixture tool inherited from the Phase 0
eval, and the shipped builtin menu is four tools that do not include it
(`daemon/mcpbuiltin.go`). What it affects is the eval's own sensitivity: the other
six prompts score 30/30 everywhere, so the instrument reads 100% against a constant
offset. Adequate for "is there a menu-size effect?", inadequate for a finer
question.

---

## 5. Founder decisions — engineering cannot clear these

Packaged one page each in `docs/DECISION_PACK.md` with a recommendation and the cost
of being wrong. **All eight are now taken** — D4 on 2026-07-27, the remaining seven
on 2026-08-12 — and the P3 security gate closed with them.
**taken: D1 D2 D3 D4 D5 D6 D7 D8**

Socket auth model (D1, same-uid accepted) · Gate-6 formal closure (D2, ruled closed) ·
Gate-7 error unification (D3, rejected) · `allow_fallbacks` / F1 posture (D4, option 3,
shipped and wire-verified) · warn-mode Design B vs C (D5, B rejected, C deferred) ·
default model (D6, maintained) · shared confinement package (D7, not yet) · skills
subsystem (D8, approved for deletion).

Phase 4 packaging is not a ruling — its direction is decided and what remains is
execution. It was on this list as a ninth item for a month.

> **Corrected 2026-09-02.** This section said **"None is taken on the founder's
> behalf"** for three weeks after all eight were taken, while
> `docs/DECISION_PACK.md` stamped each one **TAKEN** in its Status column. §5's
> entire purpose is to tell a reader what engineering cannot clear, so the stale
> version did the most expensive thing a register can do: it reported the project
> blocked on a person when it was not. `BACKLOG.md`'s Tier 0 said the same, which
> is worse — that is the table someone reads to choose what to work on next.

---

## 6. What this pass closed — items 1 through 19

> Section 6b below records a **separate, later pass** (2026-08-07) that found twelve
> defects on no list at all. Read both.

Every entry is implemented AND neuter-verified: the fix was removed and the test
was demonstrated to fail, never merely asserted to. **Nothing below is CLOSED**;
implemented-and-verified is where engineering stops.

| # | Item | Commit | Neuter result |
|---|---|---|---|
| 1 | helper shutdown hang (M8) | `9f3c5dc` | Stop never returns — 5s timeout, "blocked on a nil doneCh" |
| 2 | `file:line` without an index | `59d42a3` | `skipped with "no index found at /nonexistent"` |
| 3 | connect time outside the turn budget | `bc19419` | "the connect time was not charged to the deadline" |
| 4 | no per-connection output bound (M10) | `632d110` | slow client carried to completion, all 40 chunks |
| 5 | CREATE's syntax gate (M7) | `aa3787b` | "creating an unparseable .go file was allowed" |
| 6 | undo leaves the dirs a create made | `31bf19c` | twice: "left pkg/sub/deep/ standing", and with the emptiness heuristic, "deleted mine/, a directory the user made" |
| 8 | macOS peer credentials | `648d38b` | **RUN ON HARDWARE 2026-08-07** — macos-latest, run `31204152210`, `ok codeterminal/daemon 53.019s`. Was *NOT RUN on hardware, compile-verified only* for the entire life of this record. `peercred_darwin_test.go` has no `t.Skip` and no `testing.Short` guard and is `//go:build darwin`, so green admits no third state: `LOCAL_PEERCRED` executed and the uid it returned came from the kernel. |
| 9 | world-readable lexical index | `e3ad4b0` | directory 0755, all three files 0644 |
| 11 | unbounded MCP stderr buffer | `ffdd553` | 128 KiB retained whole; 8 MiB would be |
| 13 | stale price note | `3bee775` | n/a (doc) |
| 14 | `QUOTA_RESERVATION_DESIGN.md` stale | `3bee775` | n/a (doc) — §5(e)'s honesty kept |
| 16 | `UndoResponse` removal vs restore | `8f6eee0` | n/a (additive field, direct test) |
| 17 | Python traceback `file:line` | `53cd150` | traceback parses 0 refs; mixed-shape drops to 1 |
| L1 | TUI CancelFunc dropped | `a2c9905` | "the turn's context is still live after the stream ended" |
| L3 | helper read uncapped, undeadlined | `6aa1b57` | endless body runs to the test's own 30s timeout |
| L6 | `Redaction`'s stale offsets | `4e1ee8e` | n/a (fields removed; pinned by a field-count assertion) |
| L7 | runtime directory unverified | `133a572` | pre-created world-writable directory returned at 0775 |

Two of these were found by this pass and appear on no earlier list: the
world-readable lexical index (9) and the unbounded MCP stderr buffer (11). Item 11
is M1a's shape on the one channel the MCP hardening pass did not cover.

### 6a. `LOCAL_PEERCRED` finally ran

The longest-standing NOT RUN in this repository closed on 2026-08-07:
**macos-latest, run `31204152210`, `ok codeterminal/daemon 53.019s`.**

It took four macOS runs, and **none of the three failures in between was in the
peer-authentication code** — a socket path over the 103-byte budget (`19eb200`),
a workspace reported by an unresolved spelling (`85ad77a`), and a bind returning
`EEXIST` where Linux returns `EADDRINUSE` (`10b0e0a`). Each killed the package
before the thing under test could execute. That is the ordinary shape of first
contact with a platform: what you were trying to verify is the last thing you
get to, and everything you learn on the way there is a real defect you did not
know you had.

Green admits no third state here — no `t.Skip`, no `testing.Short` guard,
`//go:build darwin` — which is why this is evidence rather than inference. The
distinction is not academic: the bubblewrap sandbox tests DO skip themselves, so
a green run looked identical to one where they never executed, and `--nosuid`
survived two campaigns marked CONFIRMED.

### 6b. The 2026-08-07 bug-hunt pass — twelve defects, none of them on any list

B1-B7 were found by reading the code that the daemon-lifecycle and macOS work
had just touched, plus a sweep for the first one's *class*. **B8-B10 were found
by the first macOS CI runs this repository has ever had**, which is the argument
for the cross-platform matrix in one line: three of the four are product bugs, not
fixture noise, and none was reachable from a Linux desk. **B12 is the gate
itself** — the fuzz runner was claiming findings it had not found.

**B8, B9 and B11 are one mistake made three times**: a per-kernel constant treated
as a Unix constant. `sun_path` is 108 bytes on Linux and 104 on macOS; a unix
socket's default buffer is ~200 KiB and 8 KiB; binding over an existing path is
`EADDRINUSE` and `EEXIST`. Each was correct for years on the only kernel that had
ever run it. Same discipline: every fix has
a test demonstrated to fail with the fix neutered, never merely asserted to.

| # | Item | Commit | Evidence |
|---|---|---|---|
| B1 | **The watcher followed a symlink out of the workspace.** Its Create handler asked `os.Stat`, which follows the link and answers about the target, so a symlinked directory came back `IsDir` and was handed to `watcher.Add` — registering an inotify watch on a directory OUTSIDE the workspace. Every write behind it arrived as `<workspace>/<link>/<file>`, survived `filepath.Rel`, and reached `reindexFile`, which reads the file into the retrieval index. Index content becomes prompt context, and prompt context leaves the machine. `editapply/linkmode.go` states the rule this broke in as many words: *"never a Stat, which has already followed the link"*. **No attacker needed** — `ln -s ../shared lib` is ordinary, and so is checking out a branch containing one. | `0f3a411` | Reproduced before fixing: `watcher: reindexing shared/secret.txt after file change`. Second symptom in the same handler: it had no directory skip rule at all, so a `.git` created after startup was watched and churned. |
| B2 | Watcher ignored deletions and renames — register item 26. | `d5a8fdf` | See item 26. |
| B3 | Watcher reindexing fanned out one goroutine per path at a single embedder, and overlapping runs over one path doubled that file's chunks. | `d5a8fdf` | Measured peak **8** concurrent embeds on a burst of 8 files, now **1**. |
| B4 | **A dead handle spoke for a live one.** `kill()` delivers a signal; it does not wait. Both supervisor callbacks cleared `this.own` unconditionally, so once a replacement existed the old daemon's late exit un-tracked it — and `dispose()` then had nothing to kill. A daemon nobody owns, holding the socket and its 81 MB helper, until reboot. | `249dad1` | Neutered separately; reachable whenever the old daemon outlasts the restart grace period, which is exactly the daemon a user restarts. |
| B5 | **Restart adopted the corpse it had just made.** `restart()` killed its own daemon then probed — and a signalled daemon keeps answering handshakes while it drains. It returned `'adopted'`, owning nothing, about a process that was exiting. | `249dad1` | Also: two overlapping `ensure()` calls spawned two daemons and tracked one. |
| B6 | **With no folder open the extension indexed whatever directory VS Code was launched from.** `activate()` passed `'.'`, and `path.resolve('.')` returns the extension host's cwd. A VS Code launched from `$HOME` read the home directory into the retrieval index and wrote `.codeterminal/logs/` there. | `5af2f18` | **The test asserted the opposite, one layer below the product**: it called `setWorkspaceRoot('')`, which `activate()` never passed. Neuter reports `setWorkspaceRoot(".") resolved to "…/clients/vscode"`. |
| B7 | The retrieval eval's ground truth was a hand-written list of `file:startLine-endLine`, so it went stale on every edit and the scheduled job was permanently red. | `27ff6ba` | Measured **4/9 red → 8/9 green with no retrieval code changed**; baseline run, not inferred. `expectedFiles`/`anchors` stay declared, chunks are derived from the index each run. |
| B8 | **The daemon reported a different spelling of its own workspace than the one it is keyed on.** The resolved root keyed the socket and lockfile and NOTHING else: retrieval, the warn sink, the tool audit log, the LSP bridge and the reported workspace all took `absWorkspace`, which is `filepath.Abs` only. `main.go` states this four lines above the split and then contradicts it further down ("resolved and checked by validateWorkspace") — the wrong statement was load-bearing. Two windows reaching one directory by different spellings share a daemon by design, and the second was then told **WORKSPACE MISMATCH on every turn** about a daemon serving exactly the right code. `buildGroundingInfo` compared with `filepath.Clean`, which is purely lexical, so it could not tell them apart. | `85ad77a` | Found by **the first macOS CI run this repository has ever had** — `/var` is a symlink to `/private/var`, so macOS makes this the DEFAULT case for any workspace under `/tmp` or `/var`, not an edge one. Reproduced on Linux by starting the daemon through a symlink, so it does not need a mac to stay fixed. Both layers neutered separately. |
| B9 | **The socket buffer was declared on one platform and inherited on two.** A peer that stops reading parks the daemon's handler goroutine in a write it can never finish; enough of them and `Serve`'s semaphore fills, the daemon stops accepting, and `WaitForDrain` never reaches zero. Windows names 64 KiB because a zero-quota pipe has no buffer at all. Unix took whatever the kernel picked, and the test pinning the property said *"a Unix socket carries ~200 KiB"* — a **Linux** figure written down as a Unix one, which survived because Linux was the only kernel that had ever run it. macOS defaults to 8 KiB and a 32 KiB write blocked. | `f80ab25` | Same shape as `sun_path` being 108 bytes on Linux and 104 on macOS, found the same week by the same runner. Set in **both directions on both ends**, because which buffer bounds a blocking write is per-kernel (Linux: sender's `sk_sndbuf`; BSD: receiver's). **The test was vacuous on Unix and now is not** — lowering the constant to 2048 makes it fail on Linux, measured, which is the evidence the `setsockopt` takes effect. |
| B10 | The scheduled eval job was failing for **two** reasons and only one was diagnosed: `panic: test timed out after 10m0s` sat underneath the stale ground truth, in that run and the one before it. Nothing was hanging — three full-repo index passes exceed `go test`'s default budget. | `27a73ce` | The `~148s` in the job's comment was one test's LOCAL time quoted as the suite's, which is how the default never looked like a risk. A job red for two reasons teaches you about one of them. |
| B11 | **On macOS every daemon that lost a concurrent startup race exited 1 instead of 3.** Binding an AF_UNIX socket over an existing path is `EADDRINUSE` on Linux and **`EEXIST`** on macOS/BSD; `isAddrInUse` checked only the first, so the loser fell through to `logger.Fatalf`. Exit 1 means "this daemon is broken", so `DaemonSupervisor` charges it against the restart budget and after five attempts reports a daemon that was never broken. Opening a second window on one repo is enough. This defeats the entire `exitcodes.go` contract on that platform. | `efaafb2`+ | From the daemon's own log: `listen unix ...: bind: file exists`. **The existing real-collision test PASSED on macOS in the same job**, so it cannot reach `EEXIST` there and could never have defended this — why the two paths differ on macOS is NOT established. Both errnos are therefore pinned directly, with the `net.OpError`/`os.SyscallError` wrapping `net.Listen` really produces, plus a companion test that unrelated errnos are still rejected. |
| B12 | **The fuzz gate reported a finding it had not found.** `scripts/fuzz.sh` printed *"A failing input has been written to `<module>/testdata/fuzz/<target>/`. Commit it as a regression seed, THEN fix the bug"* on **every** non-zero exit, unconditionally. Run `31204152210` failed with `context deadline exceeded` — Go's fuzzing coordinator timing out at the `-fuzztime` boundary on a loaded two-worker runner, against `FuzzVerifyApproval`, which is a **pure function taking no context at all**. No input was written because nothing crashed, and the message sent its reader hunting for a file that does not exist and a bug that is not there. | `8154e1f` | Measured before changing anything: 120 s locally = **1,734,900 execs, PASS**, vs 140,113 in the CI run that failed; full sweep green across all 13 targets at **24.4M execs**. Now matches Go's real `"Failing input written to"` marker instead of the exit code, and otherwise names the coordinator timeout and says re-run. A gate that cries wolf gets ignored, and an ignored gate is the same end state as not having one. |

**The sweep that found no second instance.** B1's class is "a POSIX primitive
used where its link-following behaviour is the whole question". Every `os.Stat`
in non-test Go across all six modules was checked: the rest sit behind
`ResolveSafeTargetPath`, a `leafIsSymlink` check, or a `filepath.Rel`
containment test before the stat. The watcher was the outlier, and it was the
outlier because it is the one index path that was never part of the confinement
pipeline's audit.

### Item 7 — Gate 7, measured rather than fixed

`e4ff6c1` enumerates the oracle instead of unifying the error responses, and the
recommendation is to **leave it unified-free**. The evidence, produced by driving
the real Apply handler across ten filesystem states:

- Nine distinct refusals. None carries an absolute host path — `3aeb8b6`'s scrub
  holds, now pinned across all ten states where the previous tests covered three.
- Every one of the nine is **acted on by a user**: "does not exist; to create it,
  send an empty SEARCH section" teaches the create protocol, "search text not
  found" is the commonest real failure, and the secret-file and protected-directory
  refusals are policy explanations the package's own doc comment requires be shown
  verbatim.
- Gate 4's live analysis already established that the only party who can reach this
  surface is an authenticated same-uid peer, who can `lstat` everything it
  discloses. Unification would cost all nine messages and buy that adversary
  nothing.

Full brief in `docs/DECISION_PACK.md`. **Gate 7's closure is the founder's call**;
this pass supplies the number and the recommendation, not the ruling.

### Item 10 — `.pub` over-refusal: recommend NO CHANGE

Re-read against the code rather than the record. `MatchesSecretName` runs the
`{"secret","credential"}` substring net **before** the `id_rsa*.pub` allow-list,
and `secret.go` says so explicitly: "so a name that literally contains
secret/credential still over-refuses". `id_rsa.pub` and `id_ed25519.pub` are
correctly allowed; only names that literally say "secret" are caught.

That is a deliberate design in the safe direction, not an oversight. Reordering it
would weaken a security net so that a file whose name announces sensitivity can be
indexed and edited, in exchange for a cosmetic gain. **Recommend leaving it**, and
the register entry is corrected rather than the code.

### Items deliberately left open, with reasons

- ~~**L2**~~ — **no longer left open; fixed 2026-08-09.** This entry is kept rather
  than deleted because its *reasoning* is the instructive part, and it was wrong in
  a way worth not repeating. It argued the item was "a behavioural question rather
  than a defect with an obvious fix", and that "the M1 batch already made the
  truncation visible to the user."

  Both clauses pointed the wrong way. Making it visible **to the user** was exactly
  what disguised it: the notice on screen made the system look like it had handled
  the case, while the model — the party that acts on the answer — was still being
  shown a truncated reply as a finished one. And the question was not behavioural.
  "Should a cut-off answer be labelled as cut off?" has one defensible answer; the
  only real design decision was *who owns the wording*, settled by making the wire
  field a slug so the daemon does.

  Filed under three separate under-statements, each found only by reading the
  source: cross-session memory was not clean, and the two clients' error paths were
  broken in opposite directions.
- **L4** — backup retention can prune a still-needed session mid-review when a
  client does not echo `BackupSessionDir`. Fixing it means a retention policy that
  understands in-flight reviews, which is a design, not a patch.
- **L5** — the created-files manifest is newline-delimited, so a path containing a
  literal `\n` resurrects the Fix-C spurious-revert. A format change to a manifest
  that undo depends on, for an input no parser in this codebase can currently
  produce.
- ~~**15** — `EditProposals` does not carry refused blocks.~~ **DONE 2026-08-30.**
  It was correctly sized here — a wire addition with a client half — and the
  estimate that made it look larger than it was turned out to be a coupling that
  did not exist: `daemon/server.go` recorded that the field "belongs with the
  protocol change the incremental streaming work already has to make", and it
  did not. The field is additive and lands beside `EditProposals` on the Done
  message, with no streaming work underneath it.
- **18** — chunk end-lines overshoot by one on files ending in a newline. Still
  PLAUSIBLE; not re-measured this pass.
- **19** — the TUI has no search box. Mechanical but not small.

---

## 7. Deleted from the register — the record was wrong

Removed rather than carried forward, because carrying a resolved item is how the next
pass wastes a day rediscovering it.

- **The Gate-6 contradiction is not open.** `docs/HANDOFF.md` §3E lists it as the
  dominant open decision and says four locations disagree. It was **reconciled
  2026-07-27**; the in-process half (`d96794e`) plus the cross-process `flock` half
  (`2a389c7`) make it engineering-complete. Only the founder's formal ruling remains,
  which belongs in §5, not here.
- **The `-wal`/`-shm` permission gap on `memory.db`/`skills.db` is closed.** Both
  stores restrict their sidecars after the schema step and say so in comments. The
  gap that *does* remain is a different store (`lexical.db`, item 9) and a different
  mechanism (symlink-open, stated above).
- **M9 (SSE account-metadata leak) is fixed** (`3e37c44`), verified in the endpoint
  pass. It appears in the handoff's open list alongside M10, which genuinely was
  open (item 4) and is now fixed too.
- **`models.json` is TRACKED.** `docs/HANDOFF.md` warns it is untracked and must
  never be committed via `git add -A`. `git ls-files` disagrees: it is tracked, and
  has been. There is also no `daemon/models.json`, which the same note names.

---

## How this document stays true

It is regenerated against the code, not edited to match a narrative. An item leaves
this file when the code shows it gone — with the commit named — and not before.
`docs/HANDOFF.md` points here rather than keeping a second, drifting copy.
