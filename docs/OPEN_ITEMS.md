# Open items — the register, re-derived against the code

**Written 2026-08-01. Statuses resolved in place 2026-08-07.**

> ## Only three items in §1–§2 are still open: **7, 10, 12.**
>
> Every other row carries **FIXED** and the commit that fixed it. Read the
> **Status** column; it is the answer.
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

---

## 2. Security residuals

| # | Status | Item | Where | Sev | Evidence |
|---|---|---|---|---|---|
| 7 | **OPEN** | **Gate 7 existence-oracle distinguishability.** Path scrubbing landed (`3aeb8b6`) so no absolute root reaches the wire, but error *shapes* still distinguish "no such file" from "exists and refused". The recorded fix is invasive — unify error responses. **This is the last engineering item under FAIL-3**; the rest of Gate 7 is the founder's ruling. | `daemon/server.go` Apply/Undo handlers | the P3 blocker | **CONFIRMED** open (BACKLOG:981-983) |
| 8 | **FIXED `648d38b`** | **No macOS peer credentials — the daemon refuses every non-Linux connection.** `readPeerCred` errors unconditionally off Linux and `authorizePeer` fails closed, so no Mac user can connect at all. This is an availability bug wearing security clothes. | `daemon/peercred_other.go` | High (availability) | **CONFIRMED** by reading; **NOT RUN** on hardware — no Mac here, and it will be labelled that way when fixed |
| 9 | **FIXED `e3ad4b0`** | **The lexical index is world-readable.** `lexical.db` holds full chunk `Content` — the user's source text — and is created `0644` inside a `0755` directory, with its `-wal`/`-shm` sidecars the same. `memory.db` is `0600` in a `0700` dir and `skills.db` locks its directory down; the FTS store got neither. chromem's own collection dir is `0700`, so the vector half is covered and the lexical half is not. | `daemon/lexicalstore.go:69` | Medium | **CONFIRMED** — probe run 2026-08-01: `index/ drwxr-xr-x`, `lexical.db -rw-r--r--`, both sidecars `-rw-r--r--` |
| 10 | **OPEN** | **`MatchesSecretName` over-refuses `.pub` public keys containing "secret".** `id_rsa.pub` and `id_ed25519.pub` are correctly allowed; `id_rsa_secret.pub` and `secrets.pub` are refused by the substring rule. Fails in the safe direction — a correctness annoyance, not a hole. | `editapply/secret.go` | Low | **CONFIRMED** — probe run 2026-08-01 |
| ~~11~~ | **FIXED `ffdd553`** | ~~**The MCP log writer has no buffer bound.** `prefixWriter.Write` appends to `w.buf` and only drains on a newline, on stderr from an unconfined third-party subprocess.~~ | `daemon/mcpruntime.go` | Medium | **NO — FIXED `ffdd553`**, the same commit and the same `maxLogLineBytes = 64 << 10` that closed L8. This row contradicted §6 of this very document for six days; corrected 2026-08-07. |
| 12 | **OPEN** | **The eight LOWs, re-verified.** The report is at `~/.claude/plans/what-can-we-improve-snappy-music.md` — **outside the repo**, which is why `docs/HANDOFF.md:243`'s pointer looks dangling from a checkout. All eight re-checked against current source; all eight still present. Listed in full below so the register no longer depends on a file that does not travel with the code. | various | Low | **CONFIRMED** |

### The eight LOWs (item 12), transcribed and re-verified 2026-08-01

| ID | Finding | Where | Still open? |
|---|---|---|---|
| L1 | TUI leaks a `context.CancelFunc` per turn — `streamCancel` is set to `nil` without being called | `clients/tui/chat.go:372,379` | **yes** — CONFIRMED, both sites nil it directly |
| L2 | TUI replays a partially-streamed-then-errored answer as prior assistant history to the model (live session only; cross-session memory stays clean) | `clients/tui/chat.go` | **yes** — PLAUSIBLE |
| L3 | Helper decodes a request with no size cap and no deadline, unlike the daemon's `limitedConn` | `helper/main.go:127` | **yes** — CONFIRMED, a bare `json.NewDecoder(conn).Decode` |
| L4 | Backup retention (`backupSessionsToKeep = 5`) can prune a still-needed session mid-review when a client doesn't echo `BackupSessionDir` | `editapply/backup.go:16` | **yes** — CONFIRMED present |
| L5 | `created-files` manifest is newline-delimited, so a path containing a literal `\n` resurrects the Fix-C spurious-0-byte-file revert | `editapply/backup.go:~217` | **yes** — CONFIRMED |
| L6 | `scrub` records redaction `Start`/`End` offsets against stale intermediate strings — harmless today (only `Kind` is consumed) but a trap for any future consumer | `daemon/scrub.go:63-70` | **yes** — CONFIRMED |
| L7 | Stale-socket reclaim TOCTOU plus a brief world-permissive window: `net.Listen` under the ambient umask, then `chmod 0600` | `daemon/main.go:206-210` | **yes** — CONFIRMED; mitigated by the per-user runtime dir + `SO_PEERCRED` |
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
| 15 | `EditProposals` carries accepted blocks only — a refused block is invisible to the client | `protocol/protocol.go:297` | **CONFIRMED** |
| 16 | `UndoResponse.Restored` is one integer over two different outcomes; a removal and a restore both increment it | `protocol/protocol.go:708` | **CONFIRMED** |
| 17 | `file:line` resolver does not match Python tracebacks (`File "x.py", line 42`) | `daemon/fileref.go:73` | **CONFIRMED** — the pattern set has no such form |
| 18 | Chunk end-lines overshoot by one on files ending in a newline | `daemon/fileref.go` span math | **PLAUSIBLE** — recorded, not re-measured |
| 19 | TUI has no search UI; the wire and daemon halves shipped for VS Code | `clients/tui` (no `SearchRequest` reference) | **CONFIRMED** |

---

## 4. Blocked on measurement, not on engineering

Unblocked as of this pass: real-model spend is authorized with a **$5 hard stop**.

| # | Item | What a number closes |
|---|---|---|
| 24 | **The reservation floor makes the tail of every quota unreachable.** `reserveQuota` reserves `defaultReservationTokens` (4,096) up front, so a key is refused once its headroom drops below that — 4.1% of a 100,000-token quota, and a larger fraction of a smaller one. The user is told `quota_exceeded` while their own accounting says tokens remain, which reads as a billing bug. Safe direction (never over-spend) and an inherent consequence of reserve-then-correct, but written down nowhere. Options: reserve `min(floor, headroom)` and let the correction settle it, or report the real remaining balance in the refusal. | `proxy/main.go:194`, `reserveQuota` | Medium (UX/billing clarity) | **CONFIRMED** — diagnosed live 2026-08-01 |

| ~~20~~ | ~~`max_advertised_tools` 4/8/12 curve~~ | **MEASURED 2026-08-01** (`d9dbc92`, `docs/TOOL_MENU_SIZE_2026-08-01.md`). Flat: 88.6% @ 5, 85.7% @ 8, 85.7% @ 12 over 105 trials, the whole spread being one trial. **The default moved back to 12** — the accuracy argument for 5 did not survive the data. Every failure at every size is one confusion (`run_tests` → `list_directory`, 14/15), so excluding it the score is 30/30 at all three sizes. |
| 21 | Production system-prompt delta (D1) | the loop gate used a minimal prompt and said so; a large delta is a finding *about the prompt* |
| 22 | `search_code` in a live loop (D2) | needs the ONNX embedder and a real index; exercises the production 4-tool menu |
| 23 | Default-model evaluation | repeatedly named the single biggest reply-quality lever, deferred for weeks |

If a result contradicts a shipped default, **the default moves and the commit says so.**
Item 20 is the worked example: it contradicted a default set the same morning, and
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

**This is a finding in its own right, item 24 below.** Fixing the pilot key is one
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
of being wrong. **None is taken on the founder's behalf.**

Socket auth model · Gate-6 formal closure · `allow_fallbacks` / F1 posture · warn-mode
Design B vs C · default-model choice (informed by §4) · shared confinement package ·
skills subsystem (wire in or delete) · Phase 4 packaging.

---

## 6. What this pass closed — items 1 through 19

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
| 8 | macOS peer credentials | `648d38b` | **NOT RUN on hardware** — compile-verified darwin/amd64+arm64, vet-clean |
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

- **L2** — the TUI replays a partially-streamed-then-errored answer as prior
  assistant history. Live session only; cross-session memory stays clean. It is a
  behavioural question (what *should* a cut-off answer contribute to the next
  turn's context?) rather than a defect with an obvious fix, and the M1 batch
  already made the truncation visible to the user.
- **L4** — backup retention can prune a still-needed session mid-review when a
  client does not echo `BackupSessionDir`. Fixing it means a retention policy that
  understands in-flight reviews, which is a design, not a patch.
- **L5** — the created-files manifest is newline-delimited, so a path containing a
  literal `\n` resurrects the Fix-C spurious-revert. A format change to a manifest
  that undo depends on, for an input no parser in this codebase can currently
  produce.
- **15** — `EditProposals` does not carry refused blocks. Genuine, and a wire
  addition with a client half on both sides; larger than the other honesty items
  and not attempted here.
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
