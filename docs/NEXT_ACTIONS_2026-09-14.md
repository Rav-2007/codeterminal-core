# What moves next — reconnaissance of `audit/adversarial-pass`

**Written 2026-09-14.** Read-only pass. Nothing was edited, committed, pushed, dispatched, or
un-floored. This file is the only thing written.

**Audience:** whoever sets priorities on this branch, plus the `daemon/` owner for the two rows
that need a verdict rather than work.

---

## 1. Ground truth

```
BRANCH:        audit/adversarial-pass        (expected: audit/adversarial-pass — MATCH)
HEAD:          1013e1b690d24e128173daab307699f4a7386cec
               2026-09-13 10:34:10 +0530
               "test(daemon/mcp): the item 2 wiring test was green because it won a race"
TREE:          clean (0 files)
LOCAL vs UPSTREAM:
  vs upstream/audit/adversarial-pass  [Rav-2007/codeterminal-core]   EQUAL — 0 ahead, 0 behind
  vs origin/audit/adversarial-pass    [Rav-i24/Mochiii — the fork]   AHEAD 16, behind 0
  git's configured tracking branch is the FORK, not upstream (`@{u}` = origin/...).
  Three different mains: local `main` e881bbd · origin/main 4b8c53e · upstream/main efc611d.
  origin/main is 79 ahead of upstream/main; local main is 40 ahead of upstream/main and
  39 behind origin/main. HEAD is 173 ahead of local main.
LATEST build:  run 34739694099 @ 1013e1b — success   on Rav-2007/codeterminal-core
LATEST gates:  run 34739694106 @ 1013e1b — success   on Rav-2007/codeterminal-core
BUILD AT HEAD: YES — run 34739694099 @ 1013e1b, 26 real jobs, all success.
               One job "retrieval eval (scheduled)" was SKIPPED by design. Skipped is not passed.
DRIFT VERDICT: HEAD is current. THE CHECKPOINT IS NOT — see §2.
```

Provenance: every line above is `(M)`, measured this pass by `git rev-parse`, `git rev-list
--count`, `git for-each-ref`, and `gh run list/view --repo Rav-2007/codeterminal-core`.

---

## 2. Drift

### 2.1 `PROJECT_CHECKPOINT_2026-09-13.md` does not exist `(M)`

Not at HEAD, not in `docs/`, not on any branch, not in any commit ever made to this repository.

```
$ git ls-files | grep -i checkpoint
docs/RETRIEVAL_EVAL_CHECKPOINT_2026-08-08.md          # the only match
$ find . -iname '*CHECKPOINT*' -not -path './.git/*'
./docs/RETRIEVAL_EVAL_CHECKPOINT_2026-08-08.md
$ git log --all --oneline --name-only -- '*PROJECT_CHECKPOINT*'
                                                       # empty
$ git grep -n 'PROJECT_CHECKPOINT' -- .
                                                       # zero hits, tracked tree
$ git status --ignored --porcelain | grep -i checkpoint
                                                       # nothing untracked or ignored
```

No document carries its structure. Only `AGENT_SECURITY_AND_CAPABILITY_AUDIT.md` has an
"Appendix A", and it enumerates **four** trust boundaries, not thirteen.

**Consequence for this report.** Every claim the tasking attributes to "the checkpoint" — the
Appendix A anchor map, the thirteen boundaries, §9/§13/§15, the coverage and LOC tables — is
carried by *the tasking document alone*. Those are tagged `(C)`. I reconciled them against HEAD
anyway, because §7 and §8 quote the anchors inline. But "the checkpoint said X" is not a citable
source in this repository, and the thirteen-boundary figure has no in-repo origin at all (§5.1).

This is the failure mode §1 of the tasking names, one turn worse. §1 says a correct document was
committed and nobody was told. Here the document was never committed — so there is no way to tell
whether its claims were ever true.

### 2.2 HEAD's green build is one repair plus two lucky passes `(M)`

HEAD changed exactly one file — `daemon/mcp/stderrredact_test.go`, +22/−3 — yet **three** jobs went
FAIL→PASS from the previous commit `1e48e3c` (build **34738752316**, failure, on
Rav-2007/codeterminal-core):

| job | test | what it was |
|---|---|---|
| `go (daemon)` | `TestConnect_RedactsProvisionedValuesFromServerStderr` | **genuinely repaired** by HEAD — the race HEAD's message describes |
| `fuzz` | `FuzzPeekUsageTotal` | **not a finding.** `context deadline exceeded`, no crashing input. `scripts/fuzz.sh` prints its own note: *"NO crashing input was recorded -- this is not a fuzzing FINDING… expected to be intermittent on a busy runner. Re-run before investigating"* |
| `cross (windows-latest, clients/tui)` | `TestAResizeStormStaysWithinTheFrameBudget` | **latent at HEAD.** `median=5.455ms p99=9.286ms worst=49.723ms (311% of a 16ms frame)`, over the 48 ms bound. HEAD touched no TUI code |

So "build green at HEAD" is true and is not evidence that the branch is healthy. Two of the three
transitions were not repairs.

All three numbers above are **WALL-CLOCK**, from run 34738752316's own log, and will vary per run.

### 2.3 Trap-list corrections

- **T6 is NO LONGER TRUE at HEAD.** The vanished-floor sweep in `scripts/coverage-ratchet.sh:171-187`
  now runs on **both** argc paths; the argument only selects which failure message is printed. The
  script documents the old trap in past tense at `:152-162`. CI's invocation is unchanged
  (`.github/workflows/build.yml:330`, `./scripts/coverage-ratchet.sh ${{ matrix.module }}`). `(M)` —
  read at HEAD, and corroborated by the fix commit `9fae051` and by `belongs_to_module` existing at
  `coverage-ratchet.sh:163`. A residual remains: a floor whose *whole module* is absent from CI's
  matrix is still only caught by the argc-0 local run.
- **T8 cannot be checked.** "Thirteen trust boundaries" has no source in this repository (§5.1).
- **T4 confirmed live, as a worked example.** `1e48e3c` deleted `backup_log.txt` (866 lines,
  non-`.md`) alongside six `.md` files, so `build.yml`'s `paths-ignore: ["**.md"]` did not skip and
  the push ran the full build. `(M)`
- Minor size drift from the tasking's figures: **670** tracked files (said 663), **123,894** Go LOC
  (said 122k), **328** `_test.go` files (said 321). `(M)`

### 2.4 Anchor map: 12 of 12 present `(M)`

All twelve sampled anchors resolve to role-consistent code. No `GONE`. One off-by-one:
`daemon/server.go:707` is a blank line; the statement is at `:708`. Everything else lands on the
construct the tasking describes, including `daemon/scrub.go:79` citing
`daemon/CHUNK_SCRUB_DESIGN.md §4` — which is the subject of Q1 below.

### 2.5 Goroutine recount `(M)`

`go func` launch sites in product code (`git grep` with recursive pathspec, `_test.go` excluded):
daemon **12** (tasking said 13) · clients/tui **2** (said 4) · proxy **2** ✓ · helper **1** ✓ ·
editapply **0** ✓ · protocol **0** ✓. Counting `go ident(...)` launches as well raises the totals to
28 / 6 / 8 / 3 / 0 / 0. The tasking's figures sit between the two spellings, so I cannot say whether
the deltas are new goroutines or a different counting rule — **no new unrecovered launch site was
identified**, and that negative is bounded by not knowing the original method. `recover()` in
product `.go`: **9**.

---

## 3. THE QUEUE

Ranked by §10: reachability heaviest, then blast radius, then evidence strength, then cost, with
the four tie-breakers applied. Rows 1–3 are the ones I would move first.

| RANK | ID | ONE-LINE | TRIGGER STATE | REACHABILITY | BLAST | COST | FIRST TEST TO WRITE | TAG |
|---|---|---|---|---|---|---|---|---|
| 1 | `OPEN_ITEMS` **41** | VS Code extension is dead on arrival on an ordinary install: `daemon/main.go:111-113` `logger.Fatal`s without `MOCHIII_API_BASE`, and `extension.ts:235` `cp.spawn` passes **no `env`**, so a desktop-launched host has nothing to inherit. `package.json:27-28` `contributes` has only `commands` — no setting to supply it | ARMED, and the register calls it *"the most severe thing open"* | **default-config** — the packaged path, the product's primary GUI | product DOA (correctness) | agent-hours | `clients/tui/../vscode/src/test/suite/daemonRealSpawn.test.ts` — new case: spawn via the extension's own path with `MOCHIII_API_BASE` **deleted from `process.env`**, assert the daemon reaches ready. Today `:199-215` asserts the *opposite* (`/MOCHIII_API_BASE must be set/`), so the test pinning this pins the bug | **(M)** |
| 2 | Class III **scope** | The class guard `builtinToolSurface` enumerates **2** files; **5** files hold `func (s *Server) builtin…` handlers. `mcp_exec.go`, `mcp_lsp.go`, `webtools.go` are outside it. All three are clean of the forbidden readers today, so this is latent, not live | ARMED | user-action-required (a future handler) | memory/DoS | agent-hours | `daemon/builtinreadalloc_test.go` — `TestBuiltinToolSurfaceListIsComplete`: collect files matching `func (s *Server) builtin` in `daemon/*.go` (non-test), assert each is in `builtinToolSurface`. **Fails at HEAD**, naming the three | **(M)** |
| 3 | **R1.24** | The LSP header read is unbounded in *both* directions: `daemon/lsp_bridge.go:491` `out.ReadString('\n')` over a `bufio.Reader` wrapping a bare `cmd.StdoutPipe()` (`:271`, `:283`) with no `io.LimitReader` — one header line is unbounded **memory**; and the `for` loop at `:490-506` has no iteration cap — unbounded header *count* is unbounded **CPU**, bounded memory. `maxLSPMessageBytes` bounds only the body, and only after the headers are consumed | ARMED — "any work on `lsp_bridge.go`'s framing; an LSP server upgrade" | **attacker-must-be-trusted** — see the correction in §8.2; NOT "a user opens a repo" | memory/DoS | agent-hours | `daemon/testdata/fakelsp/main.go` — new mode `endless-header`; `daemon/lsp_bridge_test.go` — assert `Call` returns `errServerGone` and RSS stays bounded for (a) a header line with no `\n` and (b) 10⁶ well-formed header lines. Register says the pin is *"NOTHING. Not measured, not tested; predicted from a read"* — this pass **measured it**, so the row moves from `(R)` to `(M)` | **(M)** |
| 4 | Doc-gate **scope** | `scripts/docs-coderefs.sh:46` globs `docs/*.md docs/**/*.md *.md` with `globstar` **unset** (only `nullglob`, `:45`), so `**` degrades to one level and **no gate can read `daemon/*.md` or `proxy/*.md`**. It is also opt-in by marker (`:51`). Measured: 63 refs checked across 6 enforced docs; **162 refs in 15 in-glob docs unchecked**; 7 files out of glob entirely, including `daemon/CHUNK_SCRUB_DESIGN.md` (25 code refs) and `proxy/F1_ENFORCEMENT_DESIGN.md` (22) | ARMED | n/a — gate coverage | correctness of the record | agent-hours | `scripts/docs-coderefs.sh --self-test` — assert the enumerated doc set **contains** `daemon/CHUNK_SCRUB_DESIGN.md`. Fails at HEAD. This is the mechanism behind Q1 | **(M)** |
| 5 | **6.4** | Three sites read `s.cfg.NoScrub` directly, bypassing `s.noScrub()`'s nil guard: `daemon/context.go:435`, `daemon/server.go:567`, `:570`. Exactly three, confirmed | ARMED | user-action-required (a future `Server` built without a `Config`) | correctness / nil-deref panic | agent-hours | `daemon/scrubpersist_test.go` — `TestNoScrubAccessorIsTheOnlyReader`: strip comments (the repo's `stripGoComments` idiom) and assert `cfg.NoScrub` appears in `daemon/*.go` only at `context.go:322` and in `main.go`. **The open question is answered — see §5, it does not invert** | **(M)** |
| 6 | **R1.22** pin identifier | `docs/RESIDUAL_RISKS.md:1513` names `TestSentinelRow7_PersistedPromptIsStillRaw_TRIPWIRE`. That identifier does not exist. The test was **renamed** to `TestSentinelRow7_PersistedPromptIsScrubbed` (`daemon/sentinel_rows_test.go:159`) when `9987985` landed, and the rename is recorded in code at `daemon/sentinel_secrets_test.go:377`. The protection exists; one doc identifier is stale | FIRED (the ingest-vs-sink decision was taken 2026-09-13) | n/a | correctness of the record | minutes | No new test. The gap is that **no gate checks doc-cited Go *identifiers*** — `docs-coderefs.sh` checks `file.go:NNN` line refs only. First test: extend it to verify backticked `Test[A-Za-z0-9_]+` names resolve to a `func` in the tree | **(M)** |
| 7 | `OPEN_ITEMS` **33** | Stale-OPEN: item 33 describes the ratchet's argc-guarded sweep, fixed by `9fae051` (2026-09-12). `belongs_to_module` is present at `coverage-ratchet.sh:163`. The doc's own summary still counts it among "Eight open rows" | n/a — already fixed | n/a | correctness of the record | minutes | Same gate as row 6 in spirit; the concrete fix is a one-line status edit. Triple-confirmed: two independent sweeps plus my own check of the commit and the function at HEAD | **(M)** |
| 8 | Coverage floors | `scripts/coverage-floors.txt` header says a floor "may only ever be raised… Lower it only with a recorded reason", yet commit `3ff9ee2` **lowered** `protocol` 91.8→89.5 and `helper` 21.0→20.8 inside an unrelated feature commit, with no reason recorded. Several header comments are also stale against the data (claims helper's 6.5% is lowest; daemon/mcp "86.0" is 91.0; proxy "80.3" is 85.0) | ARMED | n/a | correctness of a gate | needs-a-decision + agent-hours | `scripts/coverage-ratchet.sh --self-test` — assert every floor in the file is ≥ the value at the previous commit, i.e. gate the raise-only rule the file states. **I did not raise or change any floor** | **(R)** |
| 9 | **R1.16** | Trigger FIRED — *"a third instance of the form being found"* is satisfied; the row now records **seven**. The row was not closed or re-decided when its own trigger fired | **FIRED** | n/a | correctness of the record | needs-a-decision | n/a — this is a verdict, not work. Belongs to §4 | **(R)** |
| 10 | Windows frame budget | `TestAResizeStormStaysWithinTheFrameBudget` failed in CI with `worst=49.723ms` against a 48 ms bound (run **34738752316** @ `1e48e3c`, job `cross (windows-latest, clients/tui)`, on Rav-2007/codeterminal-core). WALL-CLOCK | FIRED at least once — see §5.2 for what I could not establish | n/a — CI reliability | latency + red builds | needs-a-decision | n/a — the bound is deliberately not raised, and raising it is the owner's call. Belongs to §4 | **(M)** |

Not queued, deliberately: the `fuzz` job exits 1 on a failure mode its own output calls expected and
intermittent. That is a CI-reliability nuisance, not a defect, and I am not proposing a retry
without the owner deciding whether a silently-retried fuzz gate is still a gate.

---

## 4. NEEDS A PERSON, NOT AN AGENT

Each row names what is asked, of whom, and how long. An unaddressed item is indistinguishable from
one nobody wrote — that is the transferable lesson of §2.1, so no row here is left without a
recipient.

| # | What is being asked | Of whom | How long |
|---|---|---|---|
| 1 | **Sit down with the terminal client once.** `docs/MANUAL_SESSION_2026-09-04.md` scripts it. It has been committed since 2026-09-04 and, by three separate documents' own words, *handed to no one*. No human has ever driven this client by hand | **A named tester on Linux.** The repo owner must pick the name — I cannot, and every prior document stalled at exactly this step | ~40–60 min |
| 2 | **Add the five `MACOS_*` secrets, or accept unsigned darwin assets in writing.** R1.15 is SELF-DETECTING — the release workflow warns on every run until they exist — so this needs no scheduling, only a decision and repo-settings access | Whoever holds GitHub repo settings on Rav-2007/codeterminal-core | 15 min once decided |
| 3 | **Q1 — the `CHUNK_SCRUB_DESIGN` verdict.** Five Go files cite `daemon/CHUNK_SCRUB_DESIGN.md §4` as the decision they implement. The doc's status line says *"scoping analysis, not a design-to-implement, not a decision… No fix is written and none is picked here"* and its closing line says *"No code was written or committed for this item."* The decision **was** taken — the code landed 4 h 06 m after the doc, same day (`8027acd` → `d8fb8d5`), and the only record of it is `docs/ARCHIVE/BACKLOG_2026-07.md:410-411`, in a directory `docs/README.md:112` marks *"Do not act on anything in this section"* | The `daemon/` owner. The doc says the choice is the founder's; the founder made it and the doc was never told | 10 min to amend the status line |
| 4 | **Q1b — the label.** All five Go citations say **"Option A"**. That string does not occur in the cited document; it labels them `Design A`/`B`/`C` (`:223`, `:241`, `:259`). Either the code's label or the doc's is wrong | Same as #3 | 5 min |
| 5 | **Six triggers that cannot fire** need a new trigger or a verdict: R1.2, R1.3, R1.12, R1.13, R1.25, R1.26. Two say so in the register's own words — R1.25: *"If this row is ever closed it will be closed by someone doing the work, not by the trigger firing"*; R1.26: *"it fires when somebody thinks to look."* Three depend on a user report, and `BRANCH_STATUS §5.1` states *"No human has used any of this"* with no telemetry | Whoever owns the register | ~30 min for all six |
| 6 | **Three rows recorded TRIGGER: NONE STATED** — item 1c (scrub assistant turns, DECLINED), the fuzz gate's non-fatality, and `backup_log.txt`'s personal email left in history at `3ff9ee2` **on both remotes**. Decide whether each absence is still acceptable. I did not invent triggers for them | Register owner; the `backup_log.txt` one is a privacy call | 20 min |
| 7 | **The `go-sdk` dependency-policy call.** At the pinned `v1.7.0`, `recover()` appears **zero** times in the SDK's `mcp/` product code (all 14 occurrences are in `_test.go`). Four SDK goroutines decode untrusted MCP stdout. Ours is fixed; theirs is a policy question — vendor, wrap, upstream, or accept | Whoever owns dependency policy | 30 min |
| 8 | **R1.16's firing** (queue row 9) and **the Windows bound** (queue row 10) are verdicts, not work | `clients/tui` owner / register owner | 15 min each |
| 9 | **The fork divergence.** `git`'s tracking branch is `origin` (Rav-i24/Mochiii), which is **16 commits behind HEAD**, while CI runs on `upstream`. There is no sync hook. Decide whether the fork should be retired, re-pointed, or synced — otherwise the next `git push` with no argument goes to the remote CI does not watch | Repo owner | 10 min |

---

## 5. OPEN QUESTIONS BLOCKING RANKING

### 5.1 How many trust boundaries are there? `(U)`
The tasking's "thirteen" (and the "eleven" it corrects) have **no source in this repository**. The
only enumeration is `AGENT_SECURITY_AND_CAPABILITY_AUDIT.md:56-91`, which names **four**. I did not
re-derive a boundary count — doing it honestly means an entry-point-to-sink call graph, not a grep,
and the tasking itself tags the completeness of the thirteen as `(U)` derived from greps.
**What would establish it:** a named owner picks the definition of "boundary", then one pass
enumerating every process/socket/network/file ingress with its authentication and its sink. Size:
half a day, not an hour. **Until then no row can be ranked "boundary N", and I have ranked none that
way.**

### 5.2 Is the Windows frame-budget failure a *second* occurrence? `(U)`
The tasking's seed quotes `worst=49.723 ms` — byte-identical to run 34738752316's output. So this
may be the very occurrence that row was written from rather than a recurrence. I cannot tell,
because the document that would say is the one that does not exist (§2.1).
**What would establish it:** `gh run list --repo Rav-2007/codeterminal-core --workflow build.yml
--json databaseId,headSha,conclusion` over the last ~60 runs, grepping each Windows TUI job log for
`ResizeStorm`, which gives the true failure rate. One command, ~5 minutes. I did not run it, and
without it "it will flake again" stays `(C)`.

### 5.3 Are the goroutine deltas real? `(U)`
See §2.5. Two counting rules bracket the tasking's figures. **What would establish it:** the
original count's command. It was in the checkpoint.

### 5.4 Five `.md` basenames cited from Go with no tracked counterpart `(U)`
`CHANGELOG.md`, `implementation_plan.md`, `notes.md`, `NOTES.md`, `talk.md`. Almost certainly test
fixtures. **What would establish it:** read the ~5 call sites. 10 minutes. Not done.

---

## 6. CORRECTLY DEFERRED — DO NOT RE-OPEN

This section is the point of the pass as much as §3. Each row below was checked this pass and is
properly closed by a recorded deferral or by work already done. Re-deriving any of them is waste.

1. **F-3 — chunk content unscrubbed at rest. PRESENT BY DESIGN.** Pinned by
   `daemon/sentinel_rows_test.go:199`, `TestSentinelRow8_ChunkTextAtRestIsRawByDesign`. `(R)`
2. **Decision-memo item 3 — inbound model text not redacted (= R1.6). ACCEPTED IN WRITING**,
   2026-09-13, with a three-clause trigger (transcript export, multi-user transcript access, or
   telemetry sampling conversation content). The acceptance is explicitly coupled to item 1 being
   fixed. `(R)`
3. **`.mochiii/backups/` — the cap and the sweep BOTH EXIST.** The seed row's `(U) no cap
   found, (U) no sweep found` is **wrong at HEAD**: `editapply/backup.go:15` defines
   `backupSessionsToKeep = 5`, `:59` calls `pruneBackupSessions(base, backupSessionsToKeep)` from
   inside `NewBackupSessionDir` — i.e. on the write path, every time — `:63-76` implements it with a
   documented no-self-prune argument, and two tests cover it. **This row closes.** `(M)`
   One precise residual, which is a rename rather than a reopening: the bound is **5 sessions**, so
   it is bounded by count, not by bytes — the same shape as 2.3-B. Record it as that, not as "no cap".
4. **`go-sdk` `recover()` — the seed claim STANDS.** At the pinned `v1.7.0` (`daemon/go.mod:11`),
   `recover()` appears **zero** times in the SDK's `mcp/` product code. The 14 raw grep hits are all
   in `_test.go` across 7 files. `(M)` The remaining question is policy, not measurement — §4 row 7.
5. **T6 / the ratchet's vanished-floor sweep.** Fixed by `9fae051`; runs on both argc paths. Do not
   re-file it as open — `OPEN_ITEMS` item 33 still does, which is queue row 7. `(M)`
6. **Class III has no live third instance.** `mcp_exec.go`, `mcp_lsp.go` and `webtools.go` contain
   none of `os.ReadFile(`, `os.ReadDir(`, `ReadDir(-1)`, `io.ReadAll(`. Every `io.ReadAll` in daemon
   product code is wrapped in `io.LimitReader` except `daemon/apply_cmd.go:67` (`os.Stdin` in a CLI
   subcommand — the user's own bytes, not model-controlled). The *scope* gap is queue row 2; the
   instance count is zero. `(M)`
7. **R1.22's protection is intact.** Renamed, not deleted. Queue row 6 is a doc identifier, nothing
   more. `(M)`
8. **`FuzzPeekUsageTotal`'s CI failure is not a defect.** No crashing input; the harness explains the
   class in its own output. Do not go looking for a corpus file — `scripts/fuzz.sh` says so
   explicitly. `(M)`
9. **`gate-parity.sh` is clean.** `./scripts/gate-parity.sh` from the repo root, exit **0**: *"22
   script(s) accounted for -- 12 both, 1 local-only, 3 CI-only, 6 manual (each asymmetry carries a
   reason)"*. All 22 scripts on disk accounted for; zero divergences now; the three it originally
   found are recorded fixed at `.github/workflows/gates.yml:100-103`. `(M)` One scope caveat worth
   knowing before trusting it further: its `ci_set` is a `grep` for `scripts/*.sh` over
   `.github/workflows/`, so a **comment** mentioning a script counts as "present in CI".
10. **`daemon/CHUNK_SCRUB_DESIGN.md` is the ONLY instance of the disclaiming-doc-cited-as-authority
    shape.** Established by execution over the disclaimer vocabulary × Go citations, with ten
    candidates opened and disconfirmed individually. Three sibling design docs
    (`proxy/F1_ENFORCEMENT_DESIGN.md`, `QUOTA_RESERVATION_DESIGN.md`, `SIGNAL_ESCALATION_DESIGN.md`)
    had the identical defect and all three were **corrected**; this is the one that was missed, and
    the mechanical reason is queue row 4 — it is the only such doc living outside the gate's glob.
    `(M)` Limits of that negative are in §7.

---

## 7. WHAT I DID NOT VERIFY

Longer than §3, and that is a statement about the pass, not about the codebase.

**Phases and items not attempted**

1. **Phase 2 item 2 — the fourteenth boundary.** `(U) NOT ATTEMPTED` — the premise has no in-repo
   source (§5.1). I did not re-run the entry-point greps, because without the original definition a
   new count would be a third incompatible number rather than a check on the second.
2. **Phase 1E's inventory gaps were sized, not closed**, as instructed. **320 of 328 `_test.go` files
   were not opened this pass** — I read 4 (`lsp_bridge_test.go`, `builtinreadalloc_test.go`,
   `scrubpersist_test.go`, and parts of `sentinel_rows_test.go`) and grepped across the rest. That is
   where vacuity floors and neuter results live, so every claim I make about what a test *proves* is
   `(R)` unless I name an execution.
3. **28,715 ignored files** (`node_modules` 23,652, `.vscode-test` 6,200) were not enumerated
   individually. A supply-chain finding would live there. Not attempted; pricing it: one SBOM diff
   against `THIRD_PARTY_LICENSES.txt`, ~2 hours.
4. **`docs/RESIDUAL_RISKS.md` (106 KB) and `BRANCH_STATUS_2026-09-05.md` (95 KB) were read by a
   delegated sweep, not by me.** I independently verified only the three claims I carry into §3
   (R1.22's identifier, item 33's staleness, item 41's live status). The other ~25 register rows in
   my trigger tally are `(C)`-on-`(R)` — a sweep's reading of a document. Treat the CANNOT-FIRE count
   of six as needing one confirming read before it is quoted onward.

**Things I measured only one way**

5. **I never watched any test fail (M2).** Every "first test to write" is a prediction about what
   would fail, not an observation. The three I am most confident in are queue rows 2, 4 and 5,
   because each is a source-level assertion whose failing input is the current tree. Queue row 3's
   test does not exist in any form yet.
6. **I ran exactly one test.** `go test -count=1 -run
   'TestPersistTurn_NilConfigScrubsRatherThanSkips|TestPersistTurnNoMemory' ./...` in `daemon/`,
   `ok mochiii/daemon 0.067s`. I did **not** run `make check`, the fuzz suite, the race
   detector, the eval, or any gate other than `gate-parity.sh` (via a delegated sweep) — and I did
   not re-run `coverage-ratchet.sh` at all, to avoid any chance of touching a floor.
7. **No CI run was triggered, and none was re-run.** Every CI claim is from history. I did not
   establish the Windows test's failure *rate* (§5.2), so "latent at HEAD" means "failed at the
   parent commit in code HEAD did not touch", not "fails N% of runs".
8. **The LSP unbounded-header finding is a code read, not an executed exploit.** I confirmed the
   absence of a bound by reading the construction chain (`cmd.StdoutPipe()` → `bufio.NewReader` → no
   `LimitReader`) and confirmed no existing test covers it. I did **not** run a fake server that
   emits an unterminated header and watch RSS climb. That is what row 3's test is for. Until then the
   *mechanism* is `(M)` and the *impact* is `(R)`.
9. **I did not establish item 41's blast radius by running it.** I verified the three code facts
   (`main.go:111`, `spawn` with no `env`, `contributes` without a config section). I did not launch
   VS Code from a desktop icon and watch the daemon die. The register says
   `daemonRealSpawn.test.ts` found it on its first run; I did not re-run that suite.
10. **Reachability judgements are `(R)` throughout.** I traced the LSP server-selection chain to a
    closed three-name table and checked nothing prepends a repo directory to `PATH`
    (`git grep -n 'node_modules/.bin\|PATH=\|"PATH"'` over product `.go` → 3 hits, all env
    allow-lists). I did not audit how `PATH` reaches the daemon in each of its launch modes (systemd,
    VS Code host, shell, TUI), and that is the fact the rank of row 3 depends on.

**Bounds on the negatives I asserted (M8)**

11. **"Only one disclaiming-doc instance"** is bounded by a fixed disclaimer vocabulary in `.md` and
    `basename.md` literals in `.go`. It would miss a doc disclaiming in other words, a Go comment
    citing a doc by title or section without the filename, and any doc cited only from `.ts`, `.sql`
    or YAML. Also: **33 of 61 tracked `.md` files have no status line at all**, so for those there is
    nothing to contradict.
12. **"No new unrecovered goroutine"** is bounded by §2.5 — I could not reproduce the baseline's
    counting rule, so I can only say no launch site I found lacks containment, not that the count is
    unchanged.
13. **"Exactly three `s.cfg.NoScrub` bypass sites"** covers `git grep 'cfg\.NoScrub'` over tracked
    `.go`. A read through an alias, a struct copy, or reflection would not appear.
14. **Coverage floors**: I read the file and one `git blame`. I did not verify that the 91.8→89.5
    lowering actually broke something, nor that raising it back is safe — and per §2.3 of the
    tasking I did not touch `helper`'s floor or any other.

**Chains traced to the end, and chains not**

15. **Traced to the end:** `persistTurn` → both callers → `scrub(prompt, s.noScrub())` → the
    accessor's nil guard → the three bypass sites → whether `s.cfg` can be nil (answered by
    execution). `readHeaders` → `readLoop` → `bufio.NewReader(cmd.StdoutPipe())` → no limiter →
    `serverCommand`'s closed set → `PATH` inheritance. `builtinToolSurface` → all five handler files
    → each file's read shapes.
16. **NOT traced to the end:** what the extension host's environment actually contains in each launch
    mode (row 1's severity depends on it). What `webfetch.go`'s `maxBytes` resolves to at each call
    site — I confirmed the `LimitReader` wrapper exists but not that the bound is small. Whether
    `daemon/apply_cmd.go:67`'s unbounded stdin read is reachable from anything but a human typing a
    pipe. The `fuzz` job's four zero-exec targets (decision-memo item 4 says FIXED, `BRANCH_STATUS`
    §5 item 9 still says unverified; I checked neither).

**Two states that share a phrase — new M5 pairs found this pass**

- `green at HEAD` ≠ `repaired at HEAD` (§2.2 — one repair, two lucky passes)
- `a fuzz job failed` ≠ `the fuzzer found a crasher`
- `the pinning test does not exist` ≠ `the register names the pre-rename identifier` (queue row 6)
- `bounded by session count` ≠ `bounded in bytes` (backups, §6.3 — an instance of the known pair)
- `the decision is recorded` ≠ `the document the code cites records it` (§4 row 3)
- `the guard's listed files all exist` ≠ `all the files that should be listed are` — the anti-rot
  test runs in one direction only (queue row 2)
- `no gate checks this` ≠ `no gate can reach this file` (queue row 4 — glob scope, not policy)

---

## 8. SELF-CORRECTIONS

Four claims made and withdrawn during this pass. Each names the wrong *evidence*, not just the
wrong conclusion.

1. **"`FuzzPeekUsageTotal` failed in CI — a real defect found and silently passed over."**
   Wrong evidence: I read `FAIL proxy/FuzzPeekUsageTotal` and `exit status 1` from the job summary
   and stopped there. Reading twelve lines further gave the harness's own note — `NO crashing input
   was recorded -- this is not a fuzzing FINDING` — and the reason (`context deadline exceeded` at
   `~$FUZZTIME` is the coordinator timing out under load). **Withdrawn.** This is the "stopped at a
   partial read" error in miniature.

2. **"The reachability inversion holds: LSP ranks above MCP because a user only has to open a
   repo."** Wrong evidence: I accepted the tasking's premise, which is sound for bugs whose input is
   attacker-authored *source*. It does not hold for `readHeaders`, whose input is the language
   server's own stdout framing. `serverCommand` (`daemon/lsp_bridge.go:156-167`) returns one of three
   hardcoded names; `exec.Command` resolves it on the daemon's inherited `PATH`; nothing prepends a
   repo directory. Attacker-authored source cannot forge an unterminated header line. **Row 3's
   reachability corrected from `default-config`/`user-action-required` down to
   `attacker-must-be-trusted`**, which moved it below rows 1 and 2. The inversion may still hold for
   other LSP-surface bugs — ones whose input is response *content* — and I did not re-rank those.

3. **"The seed's `go-sdk` claim is false — there are 14 `recover()` calls, not zero."** Wrong
   evidence: `grep -rn 'recover()' .../mcp/` without excluding `_test.go`. Split by file kind, the
   product code has **zero** and all 14 are in tests. **Withdrawn; the seed claim stands.** This is
   the exact M8 failure the tasking warns about, committed while checking someone else's M8 failure.

4. **"R1.22 is a closed row pointing at a dangling test — the protection is unpinned."** Wrong
   evidence: a grep for the exact identifier in `RESIDUAL_RISKS.md:1513` returning nothing from the
   tree, which I read as deletion. The test was **renamed** to
   `TestSentinelRow7_PersistedPromptIsScrubbed` (`daemon/sentinel_rows_test.go:159`) and the rename
   is documented in code (`daemon/sentinel_secrets_test.go:377`, *"was a tripwire until 2026-09-13;
   now a control"*). **Severity withdrawn** from "unpinned protection" to "one stale identifier in
   one document."

Retraction rate this pass: 4 findings withdrawn or materially shrunk. Two of the four (#1, #3) were
errors of stopping a read too early, which is the same failure in two costumes.

---

## 9. SELF-CHECK

1. **Every CI statement names remote, run id, commit?** Yes. Runs cited: 34739694099 and
   34739694106 @ `1013e1b`; 34738752316 @ `1e48e3c` — all on Rav-2007/codeterminal-core.
2. **Every number DETERMINISTIC or WALL-CLOCK?** The three frame-budget figures and all job
   durations are WALL-CLOCK and labelled. Counts (670 files, 123,894 LOC, 328 tests, 12/5/3/9/22,
   ahead/behind counts, floor values) are DETERMINISTIC. No allocation figures are claimed.
3. **Every row tagged, every `(U)` says what would establish it?** Yes — §5 gives a command or
   observation for each of the four.
4. **Any absence claimed without pasting the search?** No. §2.1 pastes five searches; §6.3, §6.4 and
   §6.6 name their greps; §7.11–§7.13 state each negative's bounds.
5. **Stopped at a partial policy?** §7.15 lists the three chains traced to the end; §7.16 lists five
   I did not. Self-correction #1 is an instance caught.
6. **Two states sharing a phrase?** Seven new pairs, §7.
7. **Is §7 longer than §3?** Yes — 16 numbered items plus 7 pairs, against a 10-row queue.
8. **Did I fix, edit, commit, push, dispatch, or raise a floor?** No. One test executed, read-only.
   `gate-parity.sh` run (read-only, exit 0). `coverage-ratchet.sh` **read, not executed**. No
   `workflow_dispatch`. Tree was clean at start and this file is the only addition.
9. **Would I still rank the urgent rows there on `(R)` alone?** Row 1 — yes, three independent code
   facts. Row 2 — yes, a file count. Row 3 — **no**: on `(R)` alone I would rank it below row 4,
   because its severity rests on a `PATH` question I did not close (§7.10). Its position is earned by
   the mechanism being `(M)`, not by the impact.
10. **Who is being told?** Every §4 row names a recipient. The three that cannot be addressed by an
    agent — the tester's name, the repo-settings holder, the `daemon/` owner's verdict on Q1 — are
    the same three that have stalled in every prior handoff, which is why they are first in that
    section rather than last.
