# Checkpoint addendum, 2026-09-16 — what the adversarial pass changed

<!-- coderefs: enforced -->

> **Who has been told, and by what means: nobody yet, beyond this repository.** Every row below was
> delivered by commit and by push to `Rav-2007/codeterminal-core`. No person has been messaged, no
> issue filed, no review requested. **That is the state this entire effort exists to correct, and
> naming it here is not the same as correcting it.** §10 says who must be told and what for.

**Appends to `06158f0`** (*"docs: commit the 2026-09-13 checkpoint, written that day, delivered
2026-09-14"*) and does not restate it. `docs/PROJECT_CHECKPOINT_2026-09-13.md` is **not edited**; it
is a dated record and dated records are not rewritten.

| | |
|---|---|
| **Branch** | `audit/adversarial-pass` |
| **HEAD** | `94a4c31` |
| **Tree** | clean |
| **Canonical remote** | `upstream` = **`Rav-2007/codeterminal-core`** (where CI runs) |
| **Other remote** | `origin` = `Rav-i24/Mochiii`, a **fork**. Every `gh` invocation in this pass carries `--repo Rav-2007/codeterminal-core` |
| **Canonical `main`** | `efc611d`, a **strict ancestor** of HEAD, 0 behind — fast-forward available |
| **Latest `gates`** | run **35060243918**, `f4ad7fa`, **success** |
| **Latest `build`** | run **35060244055**, `f4ad7fa`, **SUCCESS** — after the one flaked `fuzz` job was re-run. See §7a |
| **Latest `retrieval eval`** | run **35060243929**, `f4ad7fa`, **FAILURE** — mine, fixed in `94a4c31`. See §7b |

**"No build run at HEAD" is a distinct state from "build passed."** `b34a6a8` is documentation-only
and `build.yml` carries `paths-ignore: ["**.md"]`, so a push of it starts `gates` and not `build`.
That is correct behaviour and it is still not a build run at HEAD.

---

## 1. The register — every item the pass touched

`(M)` measured · `(R)` read · `(U)` undetermined, with what would settle it · `(C)` corrected.
**A recorded deferral with a concrete trigger closes a row as well as a fix does.**
**`TRIGGER: NONE STATED` is an honest record, not a gap to fill with something plausible.**

### C0 / C0b — ground truth and delivery

| Item | Verdict | Evidence | Pinning test | Trigger |
|---|---|---|---|---|
| Checkpoint written, never committed | **CLOSED** by commit `06158f0` | the commit | `scripts/reach.sh` check 2 | the merge lands |
| Two untracked reports | **CLOSED** | committed in C0b | `reach.sh` | — |
| The §1.3 offset — named discriminator cannot discriminate | **(C)** mechanism verified, decision upheld | C0b §3.1–3.4 | none, stated | `TRIGGER: NONE STATED` |
| The namespace instruction's premise | **DISSOLVED (H4)**; not carried out | C0b §3.5 | n/a | — |
| Quarantine notice | **CLOSED** | C0b §4 | none, stated | — |

### C1 / C1b / C1c / C1d — CI determinism, docs enforcement, `main`

| Item | Verdict | Evidence | Pinning test | Trigger |
|---|---|---|---|---|
| Is CI green at HEAD evidence? | **YES, for a named scope** | C1 §0 | `gate-parity.sh` | — |
| `main` red for something this branch already fixed | **(M)** | seven scheduled failures, §6 | `reach.sh` check 3 | the merge lands |
| Billing-failure filter | **(M)** sub-15s runs excluded | C1 §1 | none, stated | — |
| Reruns: zero | **(M)** and the right answer | C1 §4 | none | — |
| Wall-clock exposure | **(M)** | C1 §5 | `TestRepaintCostAtTheTranscriptCeiling` | see R1.12, §9 |
| Frame-budget memo | **DELIVERED**, recommendation only | C1 §6 | n/a | `clients/tui` owner acts |
| Docs enforcement glob | **(M)** | C1b §11–12, neuter 2/2 red | `docs-coderefs.sh` | — |
| `PROJECT_CHECKPOINT_2026-09-13.md` unenforced | **DECISION**, with reason | C1b §14 | `docs-coderefs` banner prints the unenforced count | the file stops being a dated record |
| The minimal `main` fix is `142e57d`, not `f5551ca` | **(C)** | C1c §1 | verified on a worktree off `efc611d` | — |
| `ca96966` — cherry-picked, verified, never pushed | **HELD by owner decision** | C1c §3 | `reach.sh` allowlist entry | the merge lands, or the owner reverses the hold |
| The 40 commits on a stale local ref | **(M)** ref safe to discard, work is not | C1d §2 | `reach.sh` check 1 | the merge lands |
| The eval red — already fixed, never delivered | **(M)** disposition (a) | C1d §3 | the eval itself | the merge lands |
| Event gating: three shapes | **(M)**, one has a merge consequence | C1d §4 | `gate-parity.sh` | the merge lands |
| C1d Step 4 | **NOT DONE** — premise never came true | C1d §5 | n/a | its premise comes true |

### C2 — item 41, the extension was dead on arrival

| Item | Verdict | Evidence | Pinning test | Trigger |
|---|---|---|---|---|
| The extension could not start its own daemon | **FIXED** | red pasted, then green; 72 days old, from the initial commit | `daemon/installpath_test.go` (control arm first, vacuity floor) | — |
| The setting was never contributed | **FIXED** | `contributes.configuration`, `"scope": "machine"` | `clients/vscode/scripts/install-path-check.js` | the setting's scope changes |
| The workspace-override hole the obvious fix would have opened | **AVOIDED** | C2 §7 | the same gate asserts `"scope": "machine"` | — |
| The pinning trap — scope sound, judgement inverted | **(C)** twice | C2 §4, §4b | — | — |
| Neuter | **7 arms, 7 fired** | C2 §8 | — | — |

### C3 / C3b — Class III, at file scope and as a chain

| Item | Verdict | Evidence | Pinning test | Trigger |
|---|---|---|---|---|
| `read_file` materialises the whole file | **FILED**, allocation measured | C3, `builtinreadalloc_test.go` | `TestBuiltinReadFile_DoesNotMaterialiseTheWholeFile` | — |
| `list_directory` reads every entry | **FILED** | same | `TestBuiltinListDirectory_DoesNotMaterialiseEveryEntry` | — |
| The tool-surface list was enumerated, not derived | **FIXED** | C3 §1–2, neuter 3/3 red | `TestBuiltinToolSurfaceListIsComplete` | — |
| `mcp_lsp.go` — listing it is necessary and not sufficient | **CARRIED to C3b** | C3 §4 | — | — |
| **TB7 header line unbounded** | **FIXED** | 135,962,152 → **36,680 bytes** **(M)** | `TestReadHeaders_DoesNotMaterialiseAnUnboundedHeaderLine` | — |
| **TB7 header count unbounded** | **FIXED** | hang past 10s with **no allocation growth** → refused at 64 lines | `TestReadHeaders_DoesNotLoopForeverOnEndlessHeaders` | — |
| Three handlers reach TB7, not two | **(C)** | `propose_ast_edit` skips `handleLSPQuery` | the chain guard | — |
| `parseGitignoreLayer` unbounded | **FIXED**, new finding | 90,333,064 → **432 bytes** **(M)** | three arms incl. a positive control | — |
| `readReferencedSpan`, `regionsOnDisk` | **BOUNDED**, recognised not exempted | `gateGuardEnds`, 4 fixture arms | `TestEligibilityGateStillExists` | the gate is renamed or stops being obeyed |
| `unboundedReadExemptions` | **EMPTY** — green on facts | — | — | — |
| My known-answer test would have failed on the fix | **(C)** synthetic fixture instead | C3b §5a | 16 fixture arms | — |
| My endless-header arm passed on a terminating fixture | **(C)** | C3b §5b | the rewritten arm | — |
| One exemption per handler hid every site but the first | **FIXED**, keyed `tool\|site` | C3b §5c | all sites reported | — |
| My fixture broke two retrieval guards | **FIXED**, moved to `testdata/*.gotxt` | C3b §5e | both guards | — |
| **A comment I wrote widened an eval anchor to 4 chunks** | **FIXED** `94a4c31` | eval run 35060243929 **FAILURE**; anchor count 1→2 **(M)**, derived over all 101 anchors | `TestEvalGroundTruthResolvesWithoutTheModel` — **0.40s** where CI took 23 min | — |
| A textual "no anchor in a comment" gate | **REJECTED ON EVIDENCE** — 5 of 101 anchors qualify, 4 pre-existing legitimate hits | measured before shipping | n/a | — |
| `docs-coderefs`' enforced count ≠ marked count | **RECOMMENDED, not implemented** | 23 marked, 19 counted **(M)** | — | a marked document's last citation is removed |

### C4 / C4b — closures, evidence integrity, the accommodation sweep

| Item | Verdict | Evidence | Pinning test | Trigger |
|---|---|---|---|---|
| The scrub decision had two homes | **FIXED** | AST guard + a detector run without its exemption | `daemon/noscrubaccessor_test.go` | — |
| `logChunkScrub` panicked on a nil config | **FIXED** | red pasted | `TestLogChunkScrub_NilConfigScrubsRatherThanPanicking` | — |
| `docs-coderefs` resolved against `find .` | **FIXED** | `git ls-files` instead | the gate's own self-check | — |
| `docs-coderefs` checked one extension for its life | **FIXED** | extension set derived from the citations | the banner prints the derived set | — |
| §13 cannot be cited as it stands | **(M)** 3 FALSE, 1 PARTLY FALSE of 27 | §6 below | — | §13 is edited or superseded |
| §13 miscounts itself: 31 → **27** | **(C)** | C4b | — | — |
| The accommodation sweep | **one family, six sites**, not three | C4b Part 2 | — | — |
| No second item-41-class defect | **(M)** | C4b §"No second…" | — | — |

### C5 / C5b — method, topology, the reach gate

| Item | Verdict | Evidence | Pinning test | Trigger |
|---|---|---|---|---|
| `scripts/reach.sh` | **BUILT and BLOCKING** | 213 findings → 10, each with a reason and a trigger | in `make check` | each entry's own trigger |
| The allowlist silenced detection, not just exit status | **FIXED** | `b621c06` | COVERED/ALLOWED lines print every run | — |
| Remote topology backwards | **RECOMMENDED**, owner action | C5b Part 1, and C7b §1 measured the consequence | `reach.sh` `remote:<branch>` | the rename **and** `git branch -u` |
| Two orphan branches | **MEASURED SAFE**, 0 unreachable from HEAD | C5b Part 2 | `reach.sh` check 2 | deletion, or repointing |
| The method document | **510 lines**, H1–H10 with instances | C5 Part 3 | `docs-coderefs` | — |

### C7 / C7b — release readiness and delivery

| Item | Verdict | Evidence | Pinning test | Trigger |
|---|---|---|---|---|
| Release verdict | **SHIP WITH NAMED EXCLUSIONS — not from `main`, not today** | C7, 861 lines | — | the four blockers |
| "darwin assets are excluded" | **(C) RETRACTED** — the guard runs *before* the attach step, so **zero** assets publish on every platform | C7 Step 0 | `release-signing-guard.sh --self-test`, 40 arms | — |
| `0925a3d` fixes the whole `package` failure | **(C)** all three targets; the derivation was wrong for all, the answer for one | C7 Step 0 | — | the merge lands |
| **`release.yml` had no branch guard** | **FIXED** | before/after expansion, 20 self-test arms, no tag created | `scripts/release-branch-guard.sh --self-test`, both sides | — |
| Five `MACOS_*` secrets | **UNBLOCKED** — the guard is in | `{"total_count":0}` **(M)** | the guard | the secrets land |
| `marketplace` environment 404s | **(M)** | C7 | — | `publish` is ever wired |
| Row A — the gate set is not reproducible | **RECORDED**, third instance of the named class | idle p50 48.674 / p99 61.737 vs 64 ms **(M)** | — | see R1.12 |
| Row B — measurement contradicts R1.12 | **(U)**, R1.12 **not updated** | 2.2× the recorded figure | — | R1.12's provenance |
| Row C — the email | **HOLD** (owner decision on record). Hold is the only available disposition, **not a deferral**: not in any file at HEAD, and authorship metadata on 194 commits reaches no file filter | C7 §7d | — | **before this repository or any fork of it is made public** |
| Row D — I poisoned the eval corpus | **FIXED** | both files in `evalSelfReferenceFiles` with reasons | `TestNoIndexedFileEchoesAnEvalQuery`, green in CI | — |
| `evalguard` discarded its own diagnostic | **FIXED** | probe both directions | the gate itself | — |
| The eval `:512` / `:570` contradiction | **DISSOLVED (H4)** — different predicates, relative vs absolute; never a contradiction on either revision | C7b §5 | — | **struck, not answered** |
| `reach.sh` could not see a delivery | **FIXED** | 57 commits + 178 citations reported undelivered after arrival | the ARRIVED report | — |
| `reach.sh` check 1 said "lost work" for a stale pointer | **FIXED** | the renamed clone | — | — |
| An allowlist entry matching nothing printed nothing | **FIXED** | 2 pre-placed entries now visible | — | — |
| `gate-parity`'s banner never mentions the eval | **RECOMMENDED, not implemented** | `grep -ic 'eval'` → **0** **(M)** | — | someone picks an axis |
| The banner claims macOS runs "only on main" | **(C)** wrong twice; `macos-tui` runs on every non-`main` push and did | run 35054090374 **(M)** | — | the banner is edited |
| My fixture broke two retrieval guards | **FIXED** | both report agreeing exactly, 0 stop short | the guards themselves | — |

---

## 2. The DELIVERY GAP, and what it costs

The canonical statement is `docs/ENGINEERING_METHOD.md` §"A sixth failure shape" plus its second
addendum; this is the index.

| # | Instance | Now |
|---|---|---|
| 1 | a memo committed with nobody told | **still open** — and so is this document |
| 2 | a checkpoint written and never committed | closed, `06158f0` |
| 3 | `142e57d`, the lint pin, fixed on a branch while `main` stayed red for weeks | closed by the merge |
| 4 | `ca96966`, cherry-picked, verified, never pushed | **held by owner decision**, with a trigger |
| 5 | 40 commits on a stale local ref | closed by the merge |
| 6 | the eval fix, green across three runs, never delivered | closed by the merge |
| 7 | `0925a3d`, repairing the only release run this repository ever had | closed by the merge |

> **H12: an undelivered artifact does not merely withhold value; it suppresses the checks that would
> have run on it.** Row D is the proof: `evalguard` had been red since `21a0854`, it runs in
> `gates.yml` with `-v`, `gates.yml` has **no `paths-ignore`**, and one push would have found it in
> under a minute. It stayed hidden for a session because the branch had not been pushed.
>
> **`reach.sh`'s unpushed-commit findings are not bookkeeping. Each one is a set of checks that has
> not run.**

`reach.sh` catches five of the seven. The two it cannot see are *"nobody was told"* and *"never
committed"*, and its closing banner says so rather than implying coverage it does not have.

---

## 3. The enforcement policy

Two kinds of document, and the distinction decides whether a gate reads it.

| | Enforced (`<!-- coderefs: enforced -->`) | Unenforced |
|---|---|---|
| What it is | a **live** document someone will act from | a **dated record** of what was true on a day |
| Gate behaviour | every `` `file.ext:line` `` must resolve at HEAD | not checked; the count of unchecked references is **printed** |
| Rewriting | annotated, never rewritten | never rewritten, ever |
| Examples | `RESIDUAL_RISKS.md`, `TRUST_BOUNDARIES.md`, every chunk report in this pass | `PROJECT_CHECKPOINT_2026-09-13.md`, `MANUAL_SESSION_2026-09-04.md` |

At HEAD: **191 references resolve across 19 enforced documents; 253 references in 17 unenforced
documents are NOT checked**, and the gate prints both numbers plus the extension set it derived from
the citations themselves.

**But "19 enforced documents" is not the number of documents that declare enforcement.** Measured:
**23 documents carry `<!-- coderefs: enforced -->`** and the gate counts **19** **(M)**. The loop does
`[ -n "$refs" ] || continue` *before* it looks for the marker, so a document with no `file.ext:line`
citation is skipped entirely and its marker is **inert**. At HEAD those four are
`docs/C5_METHOD_2026-09-15.md`, `docs/C5b_TOPOLOGY_2026-09-15.md`,
`docs/C7b_DELIVERY_2026-09-16.md` and **this document**.

Skipping a document with nothing to check is correct behaviour. **The count is what misleads:** it
reads as "documents under enforcement" and means "documents under enforcement *that happen to cite a
line*". So a document can carry the marker and be silently uncounted, and — the direction that
matters — **if a document's last `:line` citation is ever removed, its enforcement lapses in silence
and the marker stays, reading as protection that is not there.**

**Recommended, not implemented.** The remedy is one line in the banner — print the marked count
beside the counted one — and it belongs in a commit whose subject is that gate, not in a consolidation
document. Recorded here because this is where the enforcement policy lives.

**What the gate does not check, in its own words:** prose, anchors, section numbers, and any citation
written without a `:line` suffix. It also resolves every citation against **HEAD**, so a citation to
another revision's line numbers is silently "validated" against the wrong file — name the revision in
prose, because no gate will catch it.

---

## 4. §13 is citable only alongside C4b's companion table

**Do not cite §13 of the read-only audit on its own.** Of its 27 rows — not the 31 its own prose
claims — **three are FALSE and one is partly false**, and two of those name platform coverage CI has
been running green for months. A reader planning work from §13 alone would be sent to verify macOS
peer auth, Windows SID comparison, the Extension-Development-Host suites and `gate-parity`'s floors,
**all four of which are already done.**

**The citable replacement is `docs/C4b_EVIDENCE_INTEGRITY_2026-09-15.md` Part 1**, which classifies all
27: FALSE 3 · PARTLY FALSE 1 · CONFIRMED UNVERIFIED 16 · CONFIRMED with stale numbers 3 · `(U)` 4,
each `(U)` naming what would settle it.

Row 3 is the one to read first. It is **not wrong about the gap it names** — Windows
`GetNamedPipeClientProcessId` really is untested — it is wrong about the *mechanism*: it says
`crossvet` only compiles and forgets that `cross` also **runs**. A row can name a real gap and
misdescribe why it is a gap, and the misdescription is what sends the next reader to the wrong place.

---

## 5. H1–H13, each with its instance

Canonical text: `docs/ENGINEERING_METHOD.md`, first and second addenda. **One owner per fact** — this
is the index, not a second copy.

| Rule | In one line | The instance that earned it |
|---|---|---|
| **H1** | No circular evidence | an artifact cited as evidence about itself |
| **H2** | Anything you use to interpret evidence is untested until tested | **Listed, not counted** — the count drifted once in this pass and a stated count is not a counted count: a regex that missed an apostrophe · `grep -c`'s exit status on zero matches · `reach.sh`'s reference frame · a `tail -12` truncation · `docs-coderefs`' assumed scope · a harness's success field for a run that exited 2 · `origin` resolving to a **fork** in the branch guard's first probe · **a neuter arm that never took effect** · **a restore check whose predicate was the whole working tree when the question was one file** |
| **H3** | Retract before you report | the email finding, overstated and retracted in §7d before the verdict |
| **H4** | A dissolved premise is a first-class result | the namespace instruction; the eval `:512`/`:570` contradiction, twice over |
| **H5** | Name the recipient | every report in this pass opens with one |
| **H6** | Evidence at the resolution of the question | a job-level conclusion cited for a step-level claim |
| **H7** | Enumerate incumbents before claiming a name | "40 commits on a stale ref" — which ref, measured against four candidates |
| **H8** | Destructive neuter arms run from a committed tree | twice neutered before committing the fix; the restore check caught it both times |
| **H9** | A hedge is not a measurement | "three macOS jobs failing" — wrong three ways |
| **H10** | Derive from a source of truth; do not enumerate — **binds the chunk author too** | two instructions that named paths; and `gate-parity`'s banner, whose source of truth is real and whose **axis is wrong for the question** |
| **H11** | A wall-clock gate runs alone, and the report says what else was running | a release-blocking failure I created by loading the machine |
| **H12** | An undelivered artifact suppresses the checks that would have run on it | Row D; and `reach.sh` unable to see its own delivery |
| **H13** | A derivation that produces right answers on some inputs is still wrong on all of them | `stage-runtime`, correct by accident on two targets; a known-answer test that would have failed on the fix |

---

## 6. The merge

**Preconditions, all four, measured:**

| # | Precondition | State |
|---|---|---|
| 1 | C7b's push is green on the canonical remote | **Held at `aa81555`**, **at `c622848`**, and **at `f4ad7fa`** — `gates` 35060243918 success, `build` 35060244055 success after the flaked `fuzz` job was re-run. The `retrieval eval` at `f4ad7fa` failed and was **mine**, fixed in `94a4c31`; `daemon/chunker.go` is on that workflow's path filter, so **the push of the fix re-triggers it, and the merge waits on that run.** |
| 2 | Every divergence found in C7b Step 4 is closed or recorded | 4 closed, 2 recommended-not-implemented with reasons — C7b §4 |
| 3 | `reach.sh` passes against its reasoned allowlist | `reach: ok -- 280 item(s) examined`, exit 0, 13 exemptions each naming its trigger |
| 4 | The branch guard is in | `15ffa66`, 20 self-test arms, green in `gates` |

**It carries:** the lint pin (`142e57d`'s line), the eval fix, `0925a3d`, the stray `test.log` /
`test_output.txt`, item 41's three-element fix, both unbounded reads, the branch guard, `reach.sh`, and
fourteen reports.

**Two things switch on that this line has never run:**

1. **`cross (macos-latest, ×4 modules)`** — the matrix expands to macOS only when `github.ref ==
   'refs/heads/main'` or the event is a dispatch. `macos-tui` has been green on this branch and covers
   **`clients/tui` only**, so the new coverage is **`daemon`, `protocol` and `editapply` on macOS** —
   three modules that have never executed there.
2. **`build.yml`'s scheduled `eval` job**, which adds `TestTokenEfficiencyEval` and a `./...` package
   scope over what `retrieval-eval.yml` already ran green at `f4ad7fa`.

**macOS is 8/8 green on `efc611d` and expected to pass. Expected is not measured (H9).**

---

## 7. The two red runs at `f4ad7fa`, and what each was

### 7a. `build` — `fuzz`, and the gate told me what it was

Run **35060244055**, `f4ad7fa`, `Rav-2007/codeterminal-core`: **25 of 27 jobs green**, one skipped
correctly, and `fuzz` red. Every one of the four `cross (windows-latest, …)` jobs, `macos
(clients/tui)`, `vscode extension` and `proxy-image` passed.

`proxy/FuzzStripSSEAccountMetadata` reported `context deadline exceeded`, and `fuzz.sh`'s own banner is
the disposition:

```
NO crashing input was recorded -- this is not a fuzzing FINDING.
The fuzz command failed for another reason (the line above is it).
'context deadline exceeded' at ~$FUZZTIME is the coordinator timing
out under load, and is expected to be intermittent on a busy runner.
Re-run before investigating; do not go looking for a corpus file.
```

The exec rate makes it concrete **(M)**:

| Host | execs in 30s | rate |
|---|---|---|
| CI, 2-vCPU EPYC 7763 | 308,745 | ~10,300/sec |
| this machine | 2,010,832 | ~67,000/sec (**6.5×**) |

> **Fourth instance of the named class — a check whose result depends on something other than the
> code it checks.** `FUZZTIME=30s` is close enough to the coordinator's own deadline on a 2-vCPU
> runner that the job flakes, and when it does, **25 green jobs are discarded with it.** Row A's
> prescription does not fit here; what fits is a longer coordinator timeout than `FUZZTIME`, or
> `fuzz` as a separate workflow so its flake does not redden the matrix. **Recommended, not
> implemented** — it is a CI-topology decision.

**Re-run, and it passed: `fuzz` green, run 35060244055 now `completed/success` at `f4ad7fa`** **(M)**.
One flake, one re-run, one pass — which is consistent with the banner's claim and is **not proof of
it**: one re-run is one data point, and only the flake's frequency over time settles the class. `(U)`.

**The one failed job was re-run rather than the whole matrix**, following the gate's own written
remedy, on Linux at the 1× billing rate. That is not a `gh workflow run` and the standing rule on
dispatches is untouched; but it *is* an unprompted CI action, so it is recorded here rather than
mentioned in passing. **C1 recorded "reruns: zero" as the right answer for that chunk's question; this
is one, for a different question, and the count is now one.**

### 7b. `retrieval eval` — mine, and the check for it ran 23 minutes late

Run **35060243929**, `f4ad7fa`, **FAILURE**:

```
query 4 (...): anchor "editapply.MatchesSecretName" resolves to 4 chunks
([daemon/chunker.go:181-220 daemon/chunker.go:211-250 daemon/chunker.go:571-610
daemon/chunker.go:601-640]), past the 3 ceiling.
```

**The `(...)` is a marked elision and it is doing real work.** The omitted text is query 4 itself, and
quoting it here would put an eval query back into the indexed corpus — Row D's defect, a third time,
in the document *about* Row D. `TestNoIndexedFileEchoesAnEvalQuery` caught it in `make check` before
any push, which is the first time in this pass that gate has fired before a commit rather than after.

> **This refines Row D's rule rather than repeating its remedy.** There, the query text *was* the
> finding — the report's subject was that those files contained it — so paraphrasing would have
> falsified the evidence and a listed exemption was correct. Here the query text is **not** load-bearing:
> the informative content of the message is the anchor and the four chunk ranges. **An exemption is
> right when the quoted text is the finding; a marked elision is right when it is not.** The elision
> keeps the corpus clean without an exemption and without misrepresenting the quotation.

**Mine, and measured rather than guessed.** All 101 anchors were extracted from the query tables and
counted in both files C3b changed, before and after. **Exactly one count moved:**
`editapply.MatchesSecretName` in `chunker.go`, **1 → 2** **(M)** — the second occurrence a comment I
wrote to explain why an over-bound `.gitignore` loses precision and not secrecy. The chunker's
overlapping 40-line windows put one line into two chunks, so one explanatory sentence cost two.

> **A comment *about* a symbol is not an *answer* to a query about it, and the eval cannot tell those
> apart. The ground truth pays for the explanation.**

The gated number was never in danger: DELIVERED held at **40/49 (81.6%)** and retrieval improved
(hybrid 34/49, up from 33). The failure was the ground truth going malformed, not recall moving.

**The durable half.** `resolveExactChunks` refuses an anchor resolving to more than three chunks, or
to none — both properties of the **corpus**, needing the chunker and neither the model nor the index.
Nothing but the full BGE eval ran it, behind twenty-two minutes of embedding in a path-triggered
workflow. It now runs in `make check` and in the `corpus` job, **in 0.40 seconds, with the eval's own
messages**. Neutered by reinstating the comment: it reproduces CI's sentence and the same four chunk
ranges verbatim **(M)**.

**And one gate was designed, measured, and rejected before shipping.** A textual rule — *"no eval
anchor may appear in a comment"* — looked cheaper still. Measured: only **5 of 101** anchors are
qualified symbols, so the rule covers 5% of the set; over the full set it fired on ordinary prose,
because `scrub` and `bm25` are words; and restricted to qualified symbols it still had **four
pre-existing legitimate hits**. A gate needing four birth-defect exemptions for correct code is one
people switch off. **The right check already existed and was simply being run too late** — which is a
different defect from the one I set out to fix, and the cheaper one.

---

## 8. The closure condition, and it cannot be met today

> **The canonical `main` has failed **seven consecutive scheduled runs**, every Monday since
> 2026-08-03. The pass is not finished when the merge lands; it is finished when a **scheduled** run
> on `main` is green.**

Measured, every run named:

| Date | Run | Commit | Conclusion |
|---|---|---|---|
| 2026-09-14 | 34837230165 | `efc611d` | failure |
| 2026-09-07 | 34114544830 | `efc611d` | failure |
| 2026-08-31 | 33390351465 | `efc611d` | failure |
| 2026-08-24 | 32698022729 | `efc611d` | failure |
| 2026-08-17 | 32002184546 | `efc611d` | failure |
| 2026-08-10 | 31364806807 | `8ccc591` | failure |
| 2026-08-03 | 30801081242 | `6729017` | failure |

The last green run on `main` by any event was **2026-08-11, run 31521526101, `c9cbefa`, push** — so
`main` has been red on every run since 2026-08-12.

**The cron is `0 6 * * 1` — Mondays 06:00 UTC. Today is Wednesday 2026-09-16. The next scheduled run
is Monday 2026-09-21 06:00 UTC.**

> **Exit criterion 1 cannot be satisfied today, by anyone, at any cost.** The merge can land now; the
> pass closes in five days. Stating that is not a hedge — it is the schedule.

**A `workflow_dispatch` on `main` runs the same job set**, derived by reading the conditions: `cross`
expands to macOS on `main` *or* dispatch; `macos-tui` is skipped by both; `eval` runs on `schedule` *or*
dispatch; everything else runs on both. So a dispatch would answer the same question five days early.
**It is not run here.** The standing rule is that no `gh workflow run` happens without an explicit
grant and an in-flight check, and the one grant on record was spent on a macOS pre-flight. **The facts
are laid out so the owner can decide; the decision is not taken.**

---

## 9. What was not verified

Longer than the queue, and that is the point.

- **No `build` run at `b34a6a8`.** It is markdown-only and `build.yml` skips markdown-only pushes.
- **The remote rename** — recommended, measured in a throwaway clone that mirrors the topology, **not
  executed**. A model is `(M)` about the model.
- **`scripts/macos-sign-and-notarize.sh`'s signed path.** Never run by anyone, anywhere.
- **That the branch guard refuses a real tag.** Its verdict was proven on real commits with **no tag
  in existence**; the wiring around it is verified by reading and by YAML parse. `(U)` — one tag on the
  release line would settle it, and that is the owner's call.
- **`maxLSPHeaderLineBytes` / `maxLSPHeaderLines` against a real language server.** gopls,
  `typescript-language-server` and `pyright-langserver` were not run. `(U)` — one session each.
- **The grew-between-stat-and-read path** in `parseGitignoreLayer`. Reasoned, not raced.
- **R1.12's 22.4 ms.** Not updated, and this pass added no data: the ratchet runs `go test` without
  `-v`, so a *passing* run prints no repaint measurement at all.
- **Whether the eval's 40/49 reproduces.** One run, one fingerprint `abe91e22cbfe59a9`.
- **The 86 over-extending constructs** the restored heuristic guard reports. Pre-existing, untouched.
- **Row C.** Untouched, by decision.
- **The one-hour manual Linux session** in `docs/MANUAL_SESSION_2026-09-04.md`. No human has ever
  performed it.
- **Whether the `retrieval eval` is green at the tip.** `94a4c31` fixes the anchor and the model-free check confirms the ground truth resolves, but the 23-minute run with the real model has not re-executed. **The merge is gated on it** and §6 precondition 1 says so.
- **That `fuzz`'s flake is intermittent rather than a real regression.** `fuzz.sh`'s banner asserts the class and the 6.5× exec-rate gap is consistent with it, but **one re-run is one data point.** `(U)` — what would settle it: the flake's frequency across the next several runs, which only time supplies.
- **That the rejected textual anchor gate had no shippable form.** Two designs were measured and refused; a third was not sought, because the correct check already existed.
- **Whether a human was told.** See the line at the top of this document.

---

## 10. Handover — named recipients, and what each is being asked for

| # | What | Of whom | How long |
|---|---|---|---|
| 1 | Run or decline the remote rename, then delete the six allowlist entries `reach.sh` will name | **whoever holds the working copy** | 2 min + one `make check` |
| 2 | Delete or re-point `docs/readme-rewrite`, `feat/canonical-language-table`, `feat/edit-payload-ingestion`, `ci/main-lint-pin` — all measured contained in HEAD | same | 1 min |
| 3 | **Add the five `MACOS_*` secrets. The branch guard is in, so the ordering error is closed and this is unblocked** | **whoever holds repository settings** | 15 min |
| 4 | Push or keep holding `ca96966` | **repository owner** | 1 min |
| 5 | **Strike C7 §8.2's eval row rather than answering it** — the contradiction does not exist | **`daemon/` owner** | reading C7b §5 |
| 6 | The two `daemon/` reads and the chain guard — review as `daemon/` changes | **`daemon/` owner** | one sitting |
| 7 | Whether query 1 joins `mustHit` after it holds across several scheduled runs | **`daemon/` owner** | one eval cycle |
| 8 | The repaint gate's three remedies, re-priced at **1.31× headroom** rather than the ~2.9× at which R1.12's trades were declined; plus making the measurement visible on a *passing* run | **`clients/tui` owner** | one test edit |
| 9 | **R1.12's provenance: the commit and the host the 22.4 ms was taken on.** Until that exists the row cannot be corrected or confirmed | **whoever recorded it** | unknown until asked |
| 10 | The one-hour manual Linux session in `docs/MANUAL_SESSION_2026-09-04.md`, which no human has ever performed | **a person, named — this row has no name in it and that is the gap** | 1 hour |
| 11 | Watch Monday 2026-09-21's scheduled run on `main`. If red, report which job and **stop** — do not iterate fixes into `main` | **repository owner** | 15 min on the day |
| 12 | Decide whether `gate-parity`'s banner should derive from steps rather than scripts, so the two eval surfaces stop being invisible to it | **whoever owns the gates** | one sitting |
| 13 | `fuzz`'s coordinator deadline sits at `FUZZTIME` on a 2-vCPU runner, so the job flakes and takes 25 green jobs with it. Raise the timeout above `FUZZTIME`, or move `fuzz` to its own workflow | **whoever owns the gates** | one sitting |
| 14 | `docs-coderefs` counts 19 enforced documents where 23 carry the marker. Print both, so enforcement cannot lapse in silence when a document's last citation goes | **whoever owns the gates** | one line |

**Row 10 is the honest failure of this handover.** Every other row names a role; that one names a task
nobody has been assigned, and no amount of writing converts it into an assignment.

---

## 11. Final self-check, in writing

| Question | Answer |
|---|---|
| Does every CI claim name remote, run id, and commit? | **Yes.** Every run id in this document is paired with a SHA and `Rav-2007/codeterminal-core`. |
| Is every number labelled? | **Yes** — `(M)`, `DETERMINISTIC` or `WALL-CLOCK` at the point of use, in each source report. |
| Does every `(U)` say what would settle it? | **Yes**, §9. Four of them name a single concrete action. |
| Did I claim anything absent without pasting the search? | **No.** The two absence claims — no secrets, no repo variables — are pasted with a control proving the token can read repository config. The one "no crashing input" claim is `fuzz.sh`'s own words, quoted as its words. |
| Did any instrument mislead me, and did I say so? | **Three times, all recorded.** A neuter arm that never took effect, read as a coverage gap. A restore check that reported failure on an untracked new file. A known-answer test anchored on a live defect. None reached a conclusion. |
| Call chains followed to the end | `builtinLSPDefinition` → `handleLSPQuery` → `lspServerForFile` → `GetServer` → `readLoop` → `readHeaders`; `builtinProposeASTEdit` → `lspServerForFile` → …; `builtinSearchCode` → `gatherContext` → `resolveFileLineRefs` → `resolveRefs` → `findFilesBySuffix` → `matchDir` → `matches` → `layerFor` → `parseGitignoreLayer`; `builtinRepoMap` → `buildRepoMap` → …; the release path from `push: tags` to the attach step. |
| Call chains **not** followed | anything leaving `daemon/` — a handler reaching a read through `editapply` or `protocol` is invisible to the walk, by construction. The proxy's request path. The helper's CGO boundary. Every Windows and macOS runtime path, which no machine here can execute. |
| Is "what I did not verify" longer than the queue? | **Yes.** §9 has fourteen entries; §10 has twelve rows, three of which are one minute of work. |

**One thing this document cannot do.** Every row in §10 is written to a role. Not one has been sent to
a person. **A handover that has been published and not delivered is instance 1 of the pattern this
pass exists to name**, and it is the state this document is in as it is committed.
