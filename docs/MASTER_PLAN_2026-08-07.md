# Master Plan — 2026-08-07

**Supersedes** [`ULTRA_MASTER_PLAN_2026-08-06.md`](ULTRA_MASTER_PLAN_2026-08-06.md),
whose Track A is closed and whose Track C is now half-executed. Roles: CTO ·
Security · QA.

Every claim below was **re-derived against the source on this branch**, not
carried forward. Where the existing record disagrees with the code, §1 says so
and the code wins.

---

## 1. Corrections to the record — read this first

The single most expensive recurring failure in this project is a document that
went stale and sent a session down a path that was already done, or already
wrong. Six such claims are live right now.

| Claim | Where it is written | What the code says |
|---|---|---|
| "There is no LICENSE file anywhere in the repo… five minutes, hard blocker" | launch plan Stage 2.0 | **`LICENSE` exists** — 122 lines, proprietary, "All rights reserved". Not a blocker; but see §4, it is *not* an OSS licence and that constrains marketplace framing. |
| "The index is a one-shot snapshot — no watcher, no incremental rebuild" | `HANDOFF.md` PART 1 | **Wrong.** `daemon/watcher.go` exists and is wired at `daemon/main.go:300`, debounces, and calls `reindexFile`. |
| "`watcher.go` exists but is unwired" | `MASTER_PLAN_2026-08-05.md` B4 | **Wrong**, same line. It was true when written; it is not now. |
| "The extension does not start the daemon — it reads the lockfile and connects" | `HANDOFF.md` PART 2 | **Wrong.** `clients/vscode/src/extension.ts:33` spawns it. See §3, Stage 2 — the *way* it spawns it is the finding. |
| D4 (`allow_fallbacks` / F1 posture) listed as an open ruling | `DECISION_PACK.md` | **Already implemented.** `models.json` ships `provider_ignore_list: ["DeepInfra"]` and `provider_sort: "price"`. The decision was taken; the pack was never updated. |
| L8 (`prefixedWriter` unbounded line buffer) "still open: yes" | `OPEN_ITEMS.md` §2 | **Fixed.** `maxLogLineBytes = 64 << 10` at `daemon/helperproc.go:424`. |

One more, self-inflicted and small: the `cross` job's header comment in
`.github/workflows/build.yml` still says **"BUILD AND VET ONLY, on purpose"**.
It runs `go test ./...` as of the transport-seam batch. Fix the comment.

**Stage 0 is these seven edits.** They cost under an hour and they are the
difference between the next session starting from the truth or from 2026-08-01.

---

## 2. Where the project actually stands

Measured on this tree, 2026-08-07:

| | Value |
|---|---|
| `make check` | **exit 0** |
| Tests | **1,081** (1,055 on 08-06) · VS Code **45/45** |
| Coverage | daemon 74.0 · mcp 92.1 · editapply 88.5 · proxy 86.1 · protocol **94.0** · tui 77.6 · helper 21.7 |
| errcheck ceilings | all six at ceiling, none moved |
| Commits pending | **18** on `ci/cross-go-test` (10 Windows + 6 lifecycle + 2 docs) |
| CI | **blocked** — GitHub Actions `major_outage` since 15:22Z; run #41 cancelled |

**Windows went from 58 failures to 13, and all 13 now have fixes that have never
executed on Windows.** Run #40 was the last real signal. `protocol`, `editapply`
and `clients/tui` are green there; `daemon` was the remaining module.

**macOS has still never executed anywhere** — not on hardware, not in CI.
`peercred_darwin.go` gets its first run when this branch reaches `main`.

The 08-06 plan named **"junctions vs `Lstat`/`EvalSymlinks`" as the top unknown
security risk.** It is now closed (`c17e14c`): Go 1.23+ reports a junction as
`ModeIrregular`, not `ModeSymlink`, and seven confinement checks tested only for
the latter — including the indexer walk, which is a read primitive whose output
reaches the model and leaves the machine. A junction needs **no privilege** to
create; a symlink needs `SeCreateSymbolicLinkPrivilege`. The cheaper vector was
the unguarded one.

---

## 3. The plan

Ordered by what stops a stranger from using this. Everything else is parked.

### Stage 0 — Make the record true (1 hour)

The seven corrections in §1. Also mark D4 **taken** in `DECISION_PACK.md` rather
than leaving a decided question in a queue of undecided ones.

### Stage 1 — Land Windows (blocked on GitHub, then 1–3 days)

Push the 10 commits when Actions recovers. **Do not push into the outage** — the
`concurrency: cancel-in-progress` group means a second push cancels the first
run, and run #41 already sat 14 minutes with zero of 26 jobs allocated.

Expect red on the first honest Windows test run of the daemon. Red is the
deliverable. The specific things that have never met Win32:

- the `SetNamedSecurityInfo` owner-only DACL (six call sites)
- the DENY-ace fixture in `denyReads`, against an elevated runner account
- `mklink /J` and the junction confinement test
- the 64 KiB named-pipe buffers, against the three tests that hung

Then macOS's first-ever execution, which happens automatically when this reaches
`main` (the matrix expands on `main` only — macOS is 10× billing).

**Known and deliberately excluded:** `helper` cannot cross-build to Windows —
`onnxruntime_go` excludes every file without a cgo toolchain. That is a
packaging problem (Stage 3), not a porting one.

### Stage 2 — The daemon lifecycle, which is the real product blocker

**This is the finding this pass adds, and it outranks packaging.**

`protocol.LockPath()` is `RuntimeDir()/<service>/lock` — **one lockfile per
user**, not per workspace. The extension spawns a daemon unconditionally
(`extension.ts:33`), never adopting a running one. So:

1. Window A opens repo A → daemon starts, binds, writes the lockfile.
2. Window B opens repo B → spawns a second daemon → `reclaimStaleSocket` finds
   the first alive → `logger.Fatal` → **exit 1**.
3. The extension's exit handler restarts on any non-zero exit that is not a
   signal → **a 3-second restart loop, with a warning toast each cycle**, for as
   long as both windows are open.

Meanwhile window B's client reads the same lockfile and reaches **daemon A**,
answering against repo A's index.

*Severity, stated honestly:* the wrong-workspace half is **not silent** —
`GroundingInfo.WorkspaceMismatch` is set at `daemon/context.go:338` and rendered
by both clients (`media/main.js:739`, `clients/tui/chat.go:1453`). The user is
told. But "told their answer is wrong" plus a toast every three seconds is not a
working product, and two open projects is the normal case, not an edge case.

**CONFIRMED by reading both halves; the loop itself has not been executed.**
Repro first, then fix — per PART 0 rule 1.

The fix is the one the launch plan already designed and nothing has consumed:
`protocol.LockPathFor(root)` keyed on `sha256(canonical root)[:16]`, plus a
supervisor that probes → adopts → spawns → retries, and a distinguishable exit
code so the loser of a race does not read as a crash. The extension must **never
delete a stale lockfile**; that logic exists correctly in exactly one place
(`reclaimStaleSocket`) and stays there.

#### DONE 2026-08-07 — six commits, `b1bfa5e`…`f1babbd`

All of the above landed, and the pass turned up one defect nobody had
predicted.

| | |
|---|---|
| `b1bfa5e` | repro: two workspaces, both halves |
| `4ae4778` | per-workspace lockfile/socket/pipe + TS mirror + golden vectors |
| `200308e` | bounded the extension's infinite restart loop |
| `ef7eb0e` | repro: the startup-race leak |
| `e21809b` | claim the address before acquiring resources; exit-code contract |
| `57c22ad` | probe-and-adopt supervisor |

**The unpredicted defect, and it was the severe one.** `main()` ran
`setupRetrieval` and `setupMemoryStore` *before* `reclaimStaleSocket`, so a
daemon that had already lost spawned the 81 MB embedder helper and opened SQLite
first — then exited through `logger.Fatal`, which is `os.Exit(1)`, which **does
not run deferred functions**. The helper was reparented and never collected.

Measured, not reasoned: baseline 0 helpers → A running 1 → B lost and exited
leaving **2** → stopping A cleanly returned to **1, not 0**. Combined with the
pre-`200308e` three-second retry, that was **81 MB orphaned every three seconds**
for as long as the window stayed open. After the fix the same sequence ends at 0
and B's entire output is two lines.

The general lesson, which applies well beyond this file: *mutual exclusion is the
cheapest step in startup and the one that decides everything, and it ran last.*

`exitAlreadyRunning = 3` is now a contract between `daemon/exitcodes.go` and
`daemonSupervisor.ts`, covered in **both** places a daemon can lose — the
sequential probe and the concurrent `bind` (`isAddrInUse`, a platform seam
because `syscall.EADDRINUSE` on Windows is synthetic and never matches).

**Still open in this stage** (unchanged, and none of it is blocked):

- **Shutdown is `SIGTERM`**, which Windows does not deliver — `child.kill()` is
  `TerminateProcess` and skips the entire drain path at `daemon/main.go:287-309`.
  A shutdown RPC, dispatched via the existing peek-for-a-key pattern, already
  gated by peer auth.
- **`--idle-timeout`**, because `deactivate()` cannot know whether another window
  has adopted the daemon, and never runs at all if the extension host crashes.
- **stderr is `'ignore'`** — nothing is captured. An `OutputChannel`, plus always
  passing `-log-file` so an *adopting* window can still see the log.
- Delete `DAEMON_LAUNCH_COMMAND` and the first-run `<pre>` block telling users to
  start the daemon by hand.
- **Shared-daemon lifetime**, surfaced by adoption and *not* fixed by it: closing
  the window that OWNS the daemon stops it under a window that adopted it. This
  is pre-existing — before adoption the second window never got a daemon of its
  own either, so it already depended on the winner's — but adoption makes it the
  normal path rather than an accident. The real fix is `--idle-timeout` plus the
  shutdown RPC above, i.e. the two items already in this list. Recorded so it is
  not rediscovered as a regression of `57c22ad`.

### Stage 3 — Packaging (1.5–2 weeks)

Nothing here has started. Verified absent right now in
`clients/vscode/package.json`: `license`, `icon`, `repository`,
`contributes.configuration` (**zero settings are contributed**), `@vscode/vsce`,
and a `.vscodeignore`. `"private": true` is still set, which `vsce` refuses
outright. `activationEvents` is still `onCommand:*` and must become
`onStartupFinished` now that the extension manages a process.

Platform-specific `.vsix` via `--target`. Binaries land in `bin/`, which
`resolveHelperBinPath` already checks first — zero Go changes for helper
discovery.

Model download stays a **first-activation prompt, never a gate**: the product
already degrades correctly without it
(`disabledRetrieval(reasonEmbedderUnavailable)`), and that must not be undone.

**macOS signing and notarization is unscoped and gates macOS specifically.**
Unsigned binaries inside a `.vsix` are killed by Gatekeeper. Not designed here.

### Stage 4 — Index honesty (2–3 days)

Half of this landed and the record does not say so. The watcher is wired and
reindexes on save. What is **still entirely absent** is any freshness signal:
no `BuiltAt`, no index-staleness `Degradation` component (the five that exist are
lexical-retrieval, memory, workspace-too-large and provider-routing).

The gap the watcher cannot close is the one that matters: **files that change
while the daemon is not running.** `git pull`, a branch switch, an edit from
another editor. The proof is in this repository right now —
`.codeterminal/index/` is dated **Jul 10**, HEAD is **Aug 6**: a **27-day-stale**
index the product will still report `grounded ✓` against.

Add `BuiltAt` to `embedderStamp` (a zero value means *unknown* and must suppress
the signal, not assert staleness) and a stat-only sweep reusing `isPrunedDir`
and `newGitignoreMatcher`. Run it on `StatusRequest` only, never per prompt,
cached — which deliberately breaks the "constant for the daemon's lifetime"
invariant at `degraded.go:34-37`, so it goes in a separate `statusDegradations()`
whose comment says exactly that.

**Rejected, with reasons:** age-based staleness ("built >24h ago") is a proxy
that cries wolf on an untouched repo, and users stop reading signals that lie.

### Stage 5 — Founder decisions, then first contact

**D1, D2, D3 gate the P3 security gate, which gates all capability work.** All
three have written recommendations and evidence: accept same-uid · rule Gate 6
closed · reject the Gate 7 unification. One sitting.

**D8 — delete the skills subsystem.** Verified dead this pass: nothing outside
`daemon/skills.go` calls `AddSkill`/`GetSkill`/`ListSkills`/`OpenSkillStore`. It
has cost two security audits already (file permissions, then sidecar handling)
and today a third — it was one of the five `restrictToOwner` call sites. Git
remembers it.

**One more ruling, from the 08-06 report §5 and still untaken:**
`resolveHelperBinPath`'s CWD-relative last candidate. Same class as P0-6. Medium,
not critical — it is the *last* candidate. Recommendation: drop it and have the
error name `CODETERMINAL_HELPER_BIN`.

Then 10–25 pilot users with hand-inserted keys.

**Two quota facts to fix before any paying user**, both diagnosed and both still
present: `reserved := defaultReservationTokens` at `proxy/main.go:1050` is a flat
4,096 with no `min(floor, headroom)`, so the last 4,096 tokens of every quota are
unreachable and the user is told `quota_exceeded` while their own accounting says
tokens remain (register item 24). And an 8-iteration agent turn reserves 32,768
at peak — a 100k quota fits three worst-case turns.

---

## 4. Standing rules (unchanged, non-negotiable)

1. **Test through the user's door.** A control tested one layer below where it
   runs is untested. This produced six P0s in reviewed code, and it produced
   every Windows defect found this week.
2. **Every fix is neuter-verified** — the fix removed, the test observed failing,
   the fix restored. This caught two of *my own* vacuous tests this week: the
   `describeFileError` pair passed with the classifier deleted, because on Linux
   `EISDIR` already renders as "is a directory" and `fs.ErrPermission`'s own
   `Error()` is literally "permission denied". They were measuring libc.
3. **A vacuous assertion is worse than no assertion** — it spends confidence and
   returns nothing. Where a property genuinely cannot be tested on this platform,
   say **NOT RUN** with the reason.
4. **Ratchets only tighten.** Floors up; errcheck ceilings fail in *either*
   direction.
5. **Credentials never enter a subprocess.** `ForbiddenEnvNames`, or a written
   reason.
6. **Lane B is unconfined.** The claim is "you approve every call and everything
   is audited" — never "it is sandboxed".
7. **Nothing is CLOSED by engineering.** Implemented-and-verified is the stop.
8. **Isolated commits, one concern each.**

---

## 5. Risks, stated plainly

1. **The Windows fixes are unexecuted.** Ten commits, `GOOS=windows` vet-clean,
   green on Linux — and the DACL code, the DENY-ace fixture, the junction test
   and the pipe buffers have never met the OS. Stage 1 is not optional.
2. **macOS has never run at all.** Not "probably fine" — unrun.
3. **The daemon-lifecycle defect (Stage 2) has not been reproduced**, only read.
   Repro before fixing.
4. **The hostile-repository surface is larger than what has been found.** P0-5
   and P0-6 were both found by hand in one session; that is evidence about the
   search, not the remainder. The reusable hostile-repo fixture (08-06 plan B1)
   is still missing and is the mitigation.
5. **CI cannot block.** Branch protection is unavailable on a private free-plan
   repo. `.githooks/pre-push` is the compensating control and `--no-verify`
   bypasses it.
6. **CI costs ~60 billable minutes per push** (~40 Linux + ~11 Windows at 2×).
   August is at 857 of 2,000. macOS at 10× is why it is gated to `main`.
7. **`LICENSE` is proprietary, not OSS.** Fine for a closed pilot; it constrains
   how the marketplace listing can be framed, and the commercial-model question
   (closed pilot vs. BYO-key OSS vs. managed SaaS) is still deliberately open.
   Recommendation unchanged: decide after the pilot, on retention evidence.

---

## 6. Sequence

1. **Now** — Stage 0's seven doc corrections. No engineering, no CI needed.
2. **When Actions recovers** — push; read run #42; iterate Windows to green.
3. **Then** — Stage 2, the daemon lifecycle. Repro the restart loop first.
4. **Then** — Stage 3 packaging, which is what makes it installable.
5. **Then** — Stage 4 index honesty.
6. **Founder, in parallel with all of the above** — D1/D2/D3, D8, and the
   `resolveHelperBinPath` ruling. None needs engineering to start.
