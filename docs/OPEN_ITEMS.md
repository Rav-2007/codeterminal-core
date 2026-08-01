# Open items — the register, re-derived against the code

**Written 2026-08-01.** Every entry below was checked **against the source on this
branch**, not carried forward from `BACKLOG.md` or `docs/HANDOFF.md`. Entries the
record listed as open but that the code shows are closed are in §6, deleted rather
than inherited.

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

| # | Item | Where | Sev | Evidence |
|---|---|---|---|---|
| 1 | **Failed helper start hangs daemon shutdown.** `doneCh` is created *after* `h.cmd` is set, so a failed `waitReady()` returns with `cmd` non-nil and `doneCh` nil. `Stop()` guards only on `h.cmd == nil`, clears it, and blocks forever receiving on a nil channel. | `daemon/helperproc.go:120-145` | High | **CONFIRMED** (read off the lines; no repro run yet) |
| 2 | **`file:line` is unreachable without an index.** `gatherContext` returns early when `s.embedder == nil \|\| s.store == nil`; direct reference resolution sits *below* that return. A user with no index who pastes `foo.go:142` gets nothing back, though they named the exact line and no retrieval is needed to read it. | `daemon/context.go:109-126` | High | **CONFIRMED** |
| 3 | **MCP connect time is charged to no budget.** Servers connect in `buildRegistry` before `runAgentLoop` creates the turn deadline, so `turn_timeout_seconds` does not govern it. Parallel connect bounds it at one `connect_timeout_seconds` (20 s) rather than one per server — a statable constant, but still time outside the budget. | `daemon/agentturn.go:43` | Medium | **CONFIRMED** (measured in the 2026-08-01 MCP pass, M2) |
| 4 | **No per-connection output-rate bound on the proxy.** A one-byte-at-a-time reader pins a connection for the full 6-minute `WriteTimeout`. The in-flight caps bound *how many* can be pinned at once; nothing bounds *one*. | `proxy/ratelimit.go` (`inFlightLimiter`) | Medium | **CONFIRMED** — recorded as mitigated-not-eliminated (M10) in the endpoint pass |
| 5 | **CREATE's syntax gate is weaker than EDIT's.** `PrepareEdit` hard-refuses an edit that would make a `.go` file unparseable (`apply.go:96-99`); `prepareCreate` only attaches an advisory `SyntaxNote` (`create.go:109`). The same model output is refused as an edit and accepted as a create. | `editapply/create.go:109` vs `editapply/apply.go:96` | Medium | **CONFIRMED** |
| 6 | **Undo leaves behind the directories a create made.** `editapply/apply.go:247` `MkdirAll`s parents on the create path. The undo removal branch has no directory cleanup: `stageRestore` returns early for `remove` with no `createdDirs`, and `commit()` unlinks only the file. `removeCreatedDirs` runs only on *discard* (a staging failure), never after a successful removal. | `daemon/apply_cmd.go:587-592, 498-508` | Low | **CONFIRMED** (read); repro pending |

---

## 2. Security residuals

| # | Item | Where | Sev | Evidence |
|---|---|---|---|---|
| 7 | **Gate 7 existence-oracle distinguishability.** Path scrubbing landed (`3aeb8b6`) so no absolute root reaches the wire, but error *shapes* still distinguish "no such file" from "exists and refused". The recorded fix is invasive — unify error responses. **This is the last engineering item under FAIL-3**; the rest of Gate 7 is the founder's ruling. | `daemon/server.go` Apply/Undo handlers | the P3 blocker | **CONFIRMED** open (BACKLOG:981-983) |
| 8 | **No macOS peer credentials — the daemon refuses every non-Linux connection.** `readPeerCred` errors unconditionally off Linux and `authorizePeer` fails closed, so no Mac user can connect at all. This is an availability bug wearing security clothes. | `daemon/peercred_other.go` | High (availability) | **CONFIRMED** by reading; **NOT RUN** on hardware — no Mac here, and it will be labelled that way when fixed |
| 9 | **The lexical index is world-readable.** `lexical.db` holds full chunk `Content` — the user's source text — and is created `0644` inside a `0755` directory, with its `-wal`/`-shm` sidecars the same. `memory.db` is `0600` in a `0700` dir and `skills.db` locks its directory down; the FTS store got neither. chromem's own collection dir is `0700`, so the vector half is covered and the lexical half is not. | `daemon/lexicalstore.go:69` | Medium | **CONFIRMED** — probe run 2026-08-01: `index/ drwxr-xr-x`, `lexical.db -rw-r--r--`, both sidecars `-rw-r--r--` |
| 10 | **`MatchesSecretName` over-refuses `.pub` public keys containing "secret".** `id_rsa.pub` and `id_ed25519.pub` are correctly allowed; `id_rsa_secret.pub` and `secrets.pub` are refused by the substring rule. Fails in the safe direction — a correctness annoyance, not a hole. | `editapply/secret.go` | Low | **CONFIRMED** — probe run 2026-08-01 |
| 11 | **The MCP log writer has no buffer bound.** `prefixWriter.Write` appends to `w.buf` and only drains on a newline — on **stderr from an unconfined third-party subprocess**. A server that writes newline-free bytes forever grows the buffer without bound. This is M1a's exact shape (unbounded allocation driven by a hostile server, before any consent) on the one channel the M1a fix did not cover: `DefaultMaxMessageBytes` caps *stdout*. Compounded by `strings.IndexByte(string(w.buf), …)`, which copies the whole buffer on every write. | `daemon/mcpruntime.go:212-226` | Medium | **CONFIRMED** by reading; repro pending. Found by this pass, not previously recorded |
| 12 | **The eight LOWs, re-verified.** The report is at `~/.claude/plans/what-can-we-improve-snappy-music.md` — **outside the repo**, which is why `docs/HANDOFF.md:243`'s pointer looks dangling from a checkout. All eight re-checked against current source; all eight still present. Listed in full below so the register no longer depends on a file that does not travel with the code. | various | Low | **CONFIRMED** |

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
| L8 | `prefixedWriter` has an unbounded line buffer on a newline-less helper line | `daemon/helperproc.go:399` | **yes** — CONFIRMED; item 11 is the same defect on a hostile stream |

L8 and item 11 are one fix in two places, and item 11 is the one that matters: the
helper is same-uid code this project ships, the MCP server is not.

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
| 20 | `max_advertised_tools` 4/8/12 curve | the default moved 12 → 5 on evidence that stops at 5; 8 and 12 are unmeasured |
| 21 | Production system-prompt delta (D1) | the loop gate used a minimal prompt and said so; a large delta is a finding *about the prompt* |
| 22 | `search_code` in a live loop (D2) | needs the ONNX embedder and a real index; exercises the production 4-tool menu |
| 23 | Default-model evaluation | repeatedly named the single biggest reply-quality lever, deferred for weeks |

If a result contradicts a shipped default, **the default moves and the commit says so.**

---

## 5. Founder decisions — engineering cannot clear these

Packaged one page each in `docs/DECISION_PACK.md` with a recommendation and the cost
of being wrong. **None is taken on the founder's behalf.**

Socket auth model · Gate-6 formal closure · `allow_fallbacks` / F1 posture · warn-mode
Design B vs C · default-model choice (informed by §4) · shared confinement package ·
skills subsystem (wire in or delete) · Phase 4 packaging.

---

## 6. Deleted from the register — the record was wrong

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
  pass. It appears in the handoff's open list alongside M10, which genuinely is open
  (item 4).

---

## How this document stays true

It is regenerated against the code, not edited to match a narrative. An item leaves
this file when the code shows it gone — with the commit named — and not before.
`docs/HANDOFF.md` points here rather than keeping a second, drifting copy.
