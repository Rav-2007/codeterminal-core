<!-- coderefs: enforced -->
# C1d — the divergence, the second red, and what "green" does not mean

| | |
|---|---|
| **Branch** | `audit/adversarial-pass` @ `2236e56`, tree clean |
| **`upstream/main`** | **still `efc611d`** — re-fetched this session. **`ca96966` has NOT landed** |
| **Latest `build` on `main`** | run `34837230165` @ `efc611d`, **schedule**, failure, on `Rav-2007/codeterminal-core` |
| **Status** | Steps 1, 2, 3 complete. **Step 4 deliberately NOT done — its premise is not yet true. §5** |

Read-only. One report commit. No push, no reset, no delete, no ground-truth regeneration.

---

## 1. `main`'s scheduled build has failed **seven consecutive times**, every week since 2026-08-03

Not "red since 2026-09-14". `(M)`, all on `Rav-2007/codeterminal-core`:

| run id | date | head | failing jobs |
|---|---|---|---|
| `34837230165` | 2026-09-14 | `efc611d` | `lint` × 6 + `retrieval eval (scheduled)` |
| `34114544830` | 2026-09-07 | `efc611d` | **`retrieval eval (scheduled)` — and nothing else** |
| `33390351465` | 2026-08-31 | `efc611d` | 7 s — suspected billing, excluded |
| `32698022729` | 2026-08-24 | `efc611d` | 8 jobs, scattered across platforms |
| `32002184546` | 2026-08-17 | `efc611d` | 8 jobs, incl. 3 × `cross (macos-latest, …)` |
| `31364806807` | 2026-08-10 | `8ccc591` | failure |
| `30801081242` | 2026-08-03 | `6729017` | failure |

**The 2026-09-07 row is the load-bearing one: the retrieval eval was the *only* failing job, a week
before the lint break.** So `ca96966` will not make the scheduled build green. That prediction is now
evidenced rather than inferred.

The 2026-08-17 and 2026-08-24 patterns — failures scattered across Windows, macOS, Linux and
`proxy-image` simultaneously — look infrastructural rather than code-caused, and they sit adjacent to
the known billing window. **Not diagnosed.** `(U)` — what would settle it: the job-level logs for
those two runs, ~10 minutes.

## 2. Step 1 — the 40 commits: **the ref is safe to discard, the work is not**

`local main = e881bbd`, 40 ahead of `upstream/main`, **0 behind.** 183 files, **30,306 insertions**,
1,006 deletions, dated 2026-08-27 → 2026-08-30. `(M)`

These are not stale experiments. A sample of what is in them: `fix(retrieval): the context budget
decayed as the repository grew` · `fix(edits): three silent failures that threw away the user's
edits` · `fix(sandbox): a memory cap that swap walked straight through` · `fix(index): a full
re-index now removes what it no longer produces` · `fix(windows): the chunker was blind to grouped
decls on a CRLF checkout` · `feat: ground live-fact answers in the web` · `feat: make the multi-agent
pipeline reachable from both clients` · **`fix(eval): correct two queries that scored a right answer
as a miss`** · **`ci(eval): the retrieval guard was blind to the files holding its biggest lever`**.

**The classification is simpler than the three categories the chunk expected, and it collapses them:**

```
git merge-base --is-ancestor main HEAD   ->  YES, local main is an ancestor of audit/adversarial-pass
git cherry HEAD main                     ->  EMPTY, nothing on main is missing from HEAD
git cherry upstream/main main            ->  40 '+', all 40 absent from upstream/main
```

So every one of the 40 is **already on the working branch**, and none is a separate body of work.
`local main` is a **stale pointer at an earlier commit on the same line of development**, not a fork.

**Verdict, stated as the chunk asks — and the two halves differ, which is the point:**

- **`local main` the ref: SAFE TO DISCARD or re-point.** Pushing it would be strictly redundant with
  pushing `audit/adversarial-pass`, and would deliver 40 of the 213 commits with no review boundary.
  **Nothing was discarded, reset or deleted here** — this is an inventory.
- **The 40 commits the work: MUST REACH `upstream/main`.** 30k insertions of real fixes have never
  reached the shipping branch. Their vehicle is the working branch, not this ref.

**This is the fifth DELIVERY GAP instance and the only one whose contents were unknown. Now they are
known, and the finding is that the gap is wider than five commits — it is 213.**

### 2.1 A direct causal link to §3

`test.log` and `test_output.txt`:

| ref | present? |
|---|---|
| `upstream/main` | **YES, both** |
| `local main` | no — deleted by these 40 |
| `HEAD` | no |

Main's eval failure message names both files among the places an anchor "also appears". **Two stray
artifacts, deleted on 2026-08-30 on the line that never merged, are still on the shipping branch and
still polluting its eval's anchor search.** `(M)`

## 3. Step 2 — the eval red: disposition **(a)**, already fixed, never delivered

**The apparent contradiction in the log dissolves on reading main's source.** Two different
properties of one query, one gated and one not:

| site (on `efc611d`) | mechanism | gated? |
|---|---|---|
| `daemon/rerank_eval_test.go:511-513` | `t.Errorf("hybrid retrieval REGRESSED %d quer(ies) that passed semantic-only")` | **YES — this is what fails the test** |
| `daemon/rerank_eval_test.go:567-571` | `t.Logf("KNOWN GAP (not gated, pre-existing, out of scope): query 1 … still misses")` | **no, explicitly not** |

So *"query 1 misses"* is a declared ungated gap, while *"hybrid lost a query semantic-only found"* is
the gated assertion — and the failure is the second. The `:474` lines about anchors appearing outside
`expectedFiles` are informational too; their own text says *"harmless unless one of those is a better
answer than what is declared."*

**M5 pair: *query 1 misses* ≠ *hybrid regressed query 1 relative to semantic-only*.** One is
accepted in writing; the other fails the build. They differ by which retrieval mode is the baseline.

**Disposition: (a) — eval data and test already corrected on the working branch, never merged.**
Evidence, `(M)`:

- `daemon/rerank_eval_test.go` differs between `efc611d` and `HEAD` by **+1,154 / −112** — main runs
  a substantially older eval (9 queries; the branch's is the expanded set).
- **The `retrieval eval` workflow is GREEN on the working branch, three consecutive runs**, all on
  `Rav-2007/codeterminal-core`: `34703641095` @ `d732c34` and `34678287935` @ `a0635bf` (2026-09-12),
  `34314941704` @ `4c782df` (2026-09-09). The only `retrieval eval` failures in recent history are on
  `feat/web-grounding` (2026-08-28), a different branch.
- Two of the 40 commits are eval corrections by name (§2), and the 40 delete the two stray files the
  failure message cites (§2.1).

**Not (b) a real retrieval regression in current code**, and **not a case for regenerating ground
truth** — which the chunk forbids and which is unnecessary, because the corrected version exists.

**Sixth DELIVERY GAP instance.** The pattern named in C1c §6 now has six members and every one has
the same shape: the work was done, verified, and never reached where it was needed.

**One thing this does NOT establish** `(U)`: whether the eval fix can be cherry-picked to `main`
*alone*. The branch's eval passes on the branch's code, and +1,154 lines of test may depend on branch
behaviour. What would settle it: cherry-pick the eval commits onto a worktree at `efc611d` and run
`TestRerankEvalRetrievalRanking` — ~10 minutes of local compute. **The merge is the likelier vehicle
either way.**

## 4. Step 3 — event gating: three shapes, and one has a merge consequence

`git grep -n 'github.event_name' -- .github/workflows/` → **9 hits, all in `build.yml`.** `gates.yml`,
`release.yml` and `retrieval-eval.yml` have none. `(M)`

| job | line | gate | what a **push** does |
|---|---|---|---|
| `lint`, `fuzz`, `govulncheck`, `extension`, `cross` | `:345`, `:432`, `:654`, `:688`, `:481` | `!= 'pull_request'` | **runs** — these skip only the same-repo PR duplicate |
| **`eval`** | **`:774`** | `== 'schedule' \|\| == 'workflow_dispatch'` | **SKIPPED ENTIRELY** |
| **`cross` OS matrix** | **`:506`** | `(github.ref == 'refs/heads/main' \|\| workflow_dispatch) ? [windows, macos] : [windows]` | **Windows only. macOS arm does not exist off `main`** |
| `macos-tui` | `:614` | `!= 'workflow_dispatch'` | runs — inverse gating, skipped on dispatch |

**So the M5 pair the chunk asked for is right, and understated. Two axes, not one:**

> ***the push build is green* ≠ *main is green***, and
> ***green on a branch* ≠ *green on `main`*, because the `cross` matrix is gated on the ref, not the
> event.**

**Any green claim must name the event *and* the ref.**

**The merge consequence, and it is a real one.** HEAD's build `34739694099` ran
`cross (windows-latest, …)` × 4 plus the separate `macos (clients/tui, every push)` job — and **no
`cross (macos-latest, …)` job at all**, because `:506` withholds the macOS arm off `main`. So merging
this branch to `main` will, for the first time on this line of development, run
`cross (macos-latest, <module>)` across the matrix. **The last scheduled run that exercised those
jobs, `32002184546` on 2026-08-17, had three of them failing.** That is not a prediction of failure —
those failures are a month old and may be the same infrastructural pattern as the rest of that run —
but it is an untested surface that the merge turns on. `(M)` for the gating and the job lists, `(U)`
for what the macOS jobs will do.

## 5. Step 4 — **NOT done, because its premise has not come true**

Step 4 says: *"After `ca96966`, `efc611d` is no longer `upstream/main`. C7 Step 1 and the scope notice
on `AGENT_SECURITY_AND_CAPABILITY_AUDIT.md` both describe it as the head of the shipping branch.
Correct both."*

**`ca96966` has not landed.** `git fetch upstream` then `git merge-base --is-ancestor ca96966
upstream/main` → false; `upstream/main` is still `efc611d`. `(M)`

So **`efc611d` IS still `upstream/main`, both documents are currently correct, and making the
prescribed correction would make them wrong.** The scope notice's sentence *"This document audits
`efc611d`, which is `upstream/main`"* is true as of now.

**Left for whoever runs the chunk after the push lands.** The edit is two sentences and should be made
then, not pre-emptively. Recorded here so it is not forgotten: the notice is at
`AGENT_SECURITY_AND_CAPABILITY_AUDIT.md:13`, and the phrase to change is "which is `upstream/main`" →
"which was `upstream/main` until `ca96966`". The "213 commits stale" and "audit of record" framing
stays either way.

## 6. Premises that did not hold (H4)

1. **"Run after the push lands."** It has not landed. Steps 1–3 do not depend on it and were run;
   Step 4 does and was not. §5
2. **Step 4's own premise** — that `efc611d` would no longer be `upstream/main` — is false, and acting
   on it would have introduced two wrong statements. §5
3. **"`main` is red since 2026-09-14."** Red for seven consecutive scheduled runs since 2026-08-03. §1
4. **"The 40 commits are of unknown provenance, to be classified into three categories."** All 40 are
   ancestors of `HEAD`; `git cherry HEAD main` is empty. The three categories collapse into one
   answer about the ref and a different answer about the work. §2
5. **"Two causes of `main` being red."** At least two, and the eval one predates the pin one by a week
   and stood alone. §1
6. **The eval failure is "stale ground truth".** More precisely: an *older eval*, on a branch that
   also still carries two stray artifacts the eval trips over. The ground truth is not wrong so much
   as superseded. §3

## 7. Self-corrections (H3)

1. **Caught by checking, and it would have produced two false statements in committed documents.** I
   was about to make Step 4's wording corrections. The falsifying observation was one fetch: the push
   has not landed, so the current wording is correct. §5
2. **Withdrawn mid-step.** I read `daemon/rerank_eval_test.go:500-520` on the **working branch** to
   interpret line numbers from a CI log produced on **`efc611d`**. The two versions differ by 1,266
   changed lines, so the lines I read were not the lines that failed. Re-read via
   `git show efc611d:…`. This is H2 applied to file versions rather than to shell: **the instrument
   was the wrong revision.**
3. **Shrunk.** I first read the `:474` "anchor also appears outside expectedFiles" lines as the
   failure. They are informational and say so in their own text; the gated assertion is `:511-513`.

## 8. What I did not verify

1. **Nothing was pushed and no CI was triggered.** C1c Step 4 remains unrun.
2. **I did not diagnose the 2026-08-17 or 2026-08-24 scheduled failures** — 16 failing jobs between
   them, scattered across platforms. `(U)`; their job logs would settle it.
3. **I did not run the eval locally, on either revision.** The claim that the branch's eval is green
   rests on three CI runs `(M)`; the claim that main's failure is fixed by the branch's version is an
   inference from those runs plus the diff, **not** a local reproduction.
4. **I did not test whether the eval fix cherry-picks to `main` alone.** §3, `(U)`.
5. **I did not read the 40 commits' diffs**, only their subjects, the diffstat, and the ancestry.
   "Real fixes" is `(R)` from commit subjects; the ancestry facts are `(M)`.
6. **I did not establish what the macOS `cross` jobs will do after a merge.** `(U)` — the gating is
   measured, the outcome is not. A `workflow_dispatch` on the branch would exercise them, but
   dispatch shares a concurrency group with push and is forbidden by the standing rules.
7. **I did not check whether `gates.yml` has ref-gated steps** as opposed to event-gated ones; the
   sweep searched `github.event_name` only, so a `github.ref` gate outside `build.yml:506` would have
   been missed. One grep for `github.ref` would close it.
8. **Chains traced to the end:** the scheduled-run history → per-run failing jobs → the 2026-09-07
   run's sole failure → main's source at the failing assertion → the gated/ungated split. The 40
   commits → ancestry → patch-id overlap → the two stray files' presence per ref. The event sweep →
   each job's condition → HEAD's actual job list. **Not traced:** the 16 older failing jobs; what the
   +1,154 eval lines actually changed; whether any of the 40 commits conflicts with anything now on
   `upstream/main` (nothing is behind, so probably none, but `git merge-tree` would say).

## 9. What is being asked, of whom

| What | Of whom | Time |
|---|---|---|
| **Push `ca96966`** — still pending. Explicit refspec: `git push upstream ci/main-lint-pin:main`. A bare `git push upstream main` sends 41 commits | repo owner | 1 min |
| **Decide the vehicle for the 213-commit gap.** `local main` is redundant and safe to re-point; the work must arrive via `audit/adversarial-pass`. This is the real release decision and it is bigger than C1c | repo owner | 30 min |
| **The eval red is a delivery problem, not an engineering one.** Decide: merge the branch, or cherry-pick the eval commits and accept the `(U)` on whether they stand alone | whoever sets priorities | 15 min |
| **Before merging, know that macOS `cross` turns on.** `build.yml:506` withholds it off `main`; it has never run on this branch | repo owner | context, not a task |
| Delete or gitignore `test.log` and `test_output.txt` on `main` — or let the merge remove them | whoever pushes | included in the merge |

Nobody above has been contacted. This document is in the repository, which C1c §6 establishes is not
delivery.
