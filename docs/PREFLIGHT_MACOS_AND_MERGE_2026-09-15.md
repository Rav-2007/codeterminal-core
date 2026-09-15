<!-- coderefs: enforced -->
# Pre-flight — the merge vehicle, and the macOS dispatch that was not needed

**2026-09-15.** Read-only. No dispatch was run. **The granted `workflow_dispatch` is declined as
unnecessary**, and §3 retracts the finding of mine that justified granting it.

| | |
|---|---|
| `upstream/main` | `efc611d` (re-fetched) |
| `HEAD` | `dd1c8fc` on `audit/adversarial-pass` |

---

## 1. Fast-forward pre-flight: **FF-AVAILABLE**

```
git merge-base --is-ancestor upstream/main HEAD   ->  FF-AVAILABLE
git rev-list --count upstream/main..HEAD          ->  222
git rev-list --count HEAD..upstream/main          ->  0
git cherry -v upstream/main HEAD | grep -c '^+'   ->  222
```

`(M)` DETERMINISTIC. **Not `DIVERGED`.** The merge-as-vehicle decision's premise holds: `upstream/main`
is a strict ancestor of `HEAD`, nothing is behind, and all 222 commits are absent upstream by
patch-id.

**The count is 222, not 213.** The difference is this pass's own doc commits. Every earlier figure —
"213 commits stale", "a 213-commit gap" — was correct when written and is now off by nine. Worth
stating because the number appears in the scope notice and in C7's tasking, and it will keep drifting
with each commit. **The durable form is "everything between `upstream/main` and `HEAD`", not a
number.**

## 2. Quota gate: headroom is still `(U)`, so I did not spend

```
gh auth status         ->  scopes: 'gist', 'read:org', 'repo', 'workflow'   (no 'user')
GET /users/Rav-2007/settings/billing/actions  ->  404
```

The macOS pre-flight's Step 1 says: *"If headroom is still `(U)` because the billing endpoint 404s,
say so and ask before spending rather than proceeding on an unknown."* It is, so I am saying so. `(U)`

Nothing was in flight at check time, so Step 2's condition was satisfied — but Step 1's was not, and
§3 makes the question moot anyway.

## 3. **The macOS surface does not block the merge, and the evidence was already in CI**

`build.yml:506` gates the `cross` OS matrix on the ref, with modules
`[daemon, protocol, editapply, clients/tui]`. The pre-flight was granted because those jobs
"have never run on this line" and "the last scheduled run that exercised them had three failing."

**Both halves of that justification are wrong, and they were my errors.** `(M)`

macOS `cross` conclusions, measured per job with real durations, on `Rav-2007/codeterminal-core`:

| run | date | head | result |
|---|---|---|---|
| `34837230165` | 2026-09-14 | `efc611d` | **4/4 success** — daemon 87 s, protocol 10 s, clients/tui 25 s, editapply 14 s |
| `34114544830` | 2026-09-07 | `efc611d` | **4/4 success** — daemon 133 s, protocol 25 s, clients/tui 27 s, editapply 23 s |
| `31364806807` | 2026-08-10 | `8ccc591` | 4/4 failure at **3–4 s** — billing, not code |
| `32698022729` | 2026-08-24 | `efc611d` | 4/4 failure at **4–5 s** — billing, not code |
| `32002184546` | 2026-08-17 | `efc611d` | 4/4 failure at **4 s** — billing, not code |

All durations WALL-CLOCK.

**macOS `cross` is 8/8 green across the two most recent valid runs, both within the last eight days,
both on `efc611d` — which is precisely the commit the merge fast-forwards from.** There is no macOS
evidence to gather that CI has not already gathered.

**And macOS is not otherwise untested either.** `build.yml:610` defines
`macos (clients/tui, every push)` — `if: github.event_name != 'workflow_dispatch' && …`, so it runs
on every branch push and is *skipped* on dispatch. It does `go build`, `go vet` and **`go test`** on
`clients/tui` on `macos-latest`. It was **success** in HEAD's build `34739694099`. So the dispatch
would have skipped the one macOS job that actually runs tests, while re-running four build-only jobs
that are already green.

**Verdict, in the one sentence asked for: the macOS surface does not block the merge — `cross` is
8/8 green on `efc611d` in the last eight days, and the per-push macOS TUI job including its tests is
green at HEAD.**

## 4. Self-corrections — two, and the second cost a granted CI budget

**H3 applied retroactively to my own committed report, which is the case it is hardest to apply to.**

1. **`docs/C1d_DIVERGENCE_2026-09-15.md` §1 says runs `32002184546` and `32698022729` failed "8 jobs,
   scattered across platforms."** Wrong. **All 30 jobs failed in each**, every one in 3–5 s. The "8"
   was an artifact of `head -8` in my own command, reported as a cardinality. I did flag those runs
   `(U) not diagnosed`, which was honest — but I printed a count I had truncated.
   **H2, fifth instance: a `head -8` became a number.**
2. **`docs/C1d_DIVERGENCE_2026-09-15.md` §4 says "the last scheduled run that exercised those jobs,
   `32002184546` on 2026-08-17, had three of them failing."** Wrong three ways:
   - it was **four** macOS jobs, not three;
   - they failed at **4 s**, i.e. the billing signal I had myself defined a section earlier and
     applied to other rows in the same document;
   - and it was **not** the last run to exercise them — `34114544830` and `34837230165` did, later,
     and both were **4/4 green**.

   I hedged it as *"not a prediction of failure… but an untested surface the merge turns on"*, which
   softened the claim without checking it. **The hedge was doing the work the measurement should have
   done.** This finding is what justified granting an exception to the dispatch prohibition and
   spending macOS minutes at 10×. The correct answer was one `gh run view --json jobs` away, and
   I had already run that exact command shape twice in the same session.

**The generalisable failure:** I applied the sub-15 s billing filter to the runs I was *counting* and
not to the run I was *reasoning from*. A filter used in one section and forgotten in the next is worse
than no filter, because its presence earlier makes the later claim look screened.

**Neither error changes any other conclusion in C1d** — the seven-consecutive-failures finding, the
40-commit ancestry, the eval disposition and the event-gating sweep all stand, and the two corrected
rows were the ones already marked `(U)`. The corrections are recorded here rather than by editing
C1d, so the mistake and its correction both stay legible.

## 5. Premises that did not hold (H4)

1. **"The merge switches on three jobs this line has never run."** Four jobs, and they have run — on
   `main`, twice, green, in the last eight days. §3
2. **"The last scheduled run that exercised them had three failing."** §4.2
3. **"macOS cross is the macOS surface."** The per-push `macos (clients/tui, every push)` job runs
   `go test` on macOS on every push and is green at HEAD; the `cross` macOS jobs are build-only. The
   dispatch would have *skipped* the testing one. §3
4. **"213 commits."** 222 as of `dd1c8fc`, and rising with every commit this pass makes. §1

## 6. What I did not verify

1. **No dispatch was run**, so I have no macOS result from `audit/adversarial-pass` itself. The
   evidence is from `efc611d`, the merge base. **That is the right evidence for a fast-forward
   question but not for "does the branch's code build on macOS"** — `(U)`, and what would settle it is
   the dispatch, which remains available if you want it despite §3.
2. **`cross` on macOS is build-only for daemon/protocol/editapply.** I read the job's steps for
   `macos-tui` but did not confirm whether the `cross` job runs `go test` on macOS for the other three
   modules. If it does not, then daemon/protocol/editapply have **never been tested** on macOS, only
   compiled — a different and larger gap than this pre-flight was about. `(U)`: read
   `build.yml:516-540`. **This is the question worth asking instead of the dispatch.**
3. **Headroom remains `(U)`.** Nothing spent, so nothing at risk, but the next chunk that wants CI
   minutes faces the same unknown.
4. **I did not re-audit C1d's other sections** against the billing filter, only the two rows named in
   §4. Other rows in that document are `(M)` on durations already printed, but I have not re-checked
   each one.
5. **Chains traced to the end:** the FF question → `merge-base` → counts → patch-ids. The macOS
   question → every macOS job in five runs → per-job conclusion and duration → the billing filter →
   the per-push job's steps and its result at HEAD. **Not traced:** whether `cross` tests or only
   builds on macOS (§6.2); what the branch's own code does on macOS.

## 7. What is being asked, of whom

| What | Of whom | Time |
|---|---|---|
| **The dispatch is declined as unnecessary. Confirm, or overrule** if you want the branch's own macOS result rather than the merge base's | repo owner | 2 min |
| **Better use of the same curiosity:** does `cross` run `go test` on macOS, or only `go build`? If build-only, daemon/protocol/editapply have never been *tested* on macOS. Free to answer, no CI | agent, next chunk | 5 min |
| `ca96966` stays held on `ci/main-lint-pin` per your decision. No action | — | — |
| The merge still waits on C7's verdict, which is the review boundary | repo owner, after C7 | — |

Nobody above has been contacted.

---

## 8. §6.2 resolved, same session: `cross` **tests** on macOS

`build.yml:549-551` — `- name: Test` / `run: go test -count=1 ./...` /
`working-directory: ${{ matrix.module }}`. **Unconditional**, applying to both arms of the OS matrix.
`(M)`

So the lean in §6.2 was wrong and the true position is stronger than the one I declined the dispatch
on:

| module | macOS build+vet+test | when |
|---|---|---|
| `clients/tui` | **yes** | **every push** (`macos (clients/tui, every push)`) **and** `cross` on `main`/dispatch |
| `daemon` | **yes** | `cross`, on `main` or dispatch |
| `protocol` | **yes** | `cross`, on `main` or dispatch |
| `editapply` | **yes** | `cross`, on `main` or dispatch |
| `proxy` | **no** — not in the `cross` matrix | never |
| `helper` | **no** — not in the matrix, and CGO/onnxruntime makes it untestable cross-platform (R1.26) | never |

**So the four macOS `cross` jobs that were 8/8 green on `efc611d` in the last eight days were running
the full test suites of daemon, protocol, editapply and clients/tui on macOS — not merely compiling
them.** The dispatch is unnecessary by a wider margin than §3 claimed.

**What remains genuinely untested on macOS is `proxy` and `helper`**, neither of which the `cross`
matrix includes and neither of which the granted dispatch would have touched. `helper` is already
recorded as unable to join a cross-platform check (R1.26); **`proxy`'s absence is not, and is the real
gap this pre-flight surfaced.** Whether that matters is a C7 question — the proxy ships by Dockerfile
on Linux, so macOS may be irrelevant to it by design. Stated, not resolved. `(U)` — what would settle
it: whether any supported deployment runs the proxy on darwin.

---

## 5. Appendix — `main`'s eval red, measured, and two corrections to my own record

Added 2026-09-15 after the owner supplied run `34837230165`'s summary page. Everything below is
measured from that run's logs. **Remote: `Rav-2007/codeterminal-core`. Run `34837230165`. Commit
`efc611d`. Event: schedule.**

### 5.1 The per-job picture, which corroborates §3–§4

| Group | Result | |
|---|---|---|
| `go` ×6 | all success, 36–299 s | **(M)** |
| `lint` ×6 | **all failure**, 70–77 s each | **(M)** |
| `cross` windows ×4 | all success | **(M)** |
| `cross` macos ×4 | **all success**, 10–87 s | **(M)** |
| `govulncheck` ×6, `fuzz`, `vscode extension`, `proxy-image` | all success | **(M)** |
| `retrieval eval (scheduled)` | **failure**, 900 s | **(M)** |

The six `lint` failures at 70–77 s are **real durations, not the sub-15 s billing signal** — they are
the lint break `ca96966` fixes, doing real work and then failing. The four macOS `cross` successes at
10–87 s are likewise real. §3's conclusion stands on this run's own numbers.

### 5.2 The 900 s was not a timeout, and I nearly reported that it was

The eval job's duration is exactly `15m 0s`, which is a timeout-shaped number. It is not one.

```
FAIL	codeterminal/daemon	847.941s
```

**(M)** The Go test ran 847.9 s of its own accord; the job's `-timeout` is **60m**, set explicitly
and with a comment in `.github/workflows/build.yml` explaining that an earlier default 10-minute
timeout had killed this job twice. 900 s is setup + 847.9 s + teardown, rounded for display.

**M5 pair, new:** *a duration that looks like a round number* ≠ *a duration that is a limit*.

### 5.3 `main`'s eval failure is ONE query, and the test exempts it

One test failed in the whole suite **(M)**:

```
--- FAIL: TestRerankEvalRetrievalRanking (417.07s)
    rerank_eval_test.go:512: hybrid retrieval REGRESSED 1 quer(ies) that passed semantic-only:
        [where does the daemon open the unix socket]
    rerank_eval_test.go:570: KNOWN GAP (not gated, pre-existing, out of scope): query 1
        ("where does the daemon open the unix socket") still misses -- see comment above mustHit
```

Recall was **8/9 semantic-only and 8/9 hybrid** — not a collapse. The gated assertion fired because
hybrid lost query 1 while gaining query 7.

**The two lines are about the same query.** `:512` fails the build on a query that `:570`, 58 lines
later, declares *"not gated, pre-existing, out of scope."* My earlier record listed these as two
separate observations about the file; they are one contradiction. **The test exempts a query in one
check and fails on it in another** — which means `main`'s eval red is, at this commit, a
disagreement inside the eval rather than a retrieval regression.

That is a C7 input and a merge input, and it is **not** a licence to skip anything: it changes what
the red *means*, not whether it is red.

**Every line number in §5.3 and §5.4 is in `efc611d`'s frame, not HEAD's.** That file is 585 lines at
`efc611d` and 1,627 at HEAD, and line 474 holds different code in each — `exact := resolveExactChunks(...)`
there, a rate-limiter shape row here. The line numbers above are quoted CI output and are correct
**for the revision that produced them**; they are meaningless against a working tree.

`scripts/docs-coderefs.sh` cannot catch that, and did not: it resolves every reference against HEAD,
so a citation to another revision is silently "validated" against the wrong file. It also only
extracts backticked `.go:line` references. Measured both ways on 2026-09-15 with a throwaway
document, since a broken reference written out here would become a real one: three references to
line 99999 of the extension's `.ts`, the build `.yml` and a `.sh` script passed at **exit 0**, while
a single reference to line 99999 of a `.go` file failed at **exit 1**, naming the true line count.
So every `.ts`, `.yml`, `.js`, `.sh` and `.md` citation in this pass's documents is unchecked.
Filed for C4.

### 5.4 The junk files are in the eval's ground truth

The diagnostics at line 474 of `daemon/rerank_eval_test.go` **as that file stood at `efc611d`** name
`test.log` and `test_output.txt` as places the anchors leak to, on four queries **(M)**. Those are two of the files the merge deletes. They are
diagnostics, not assertions, so they are not why the job is red — but **the eval's anchor resolution
is reading repo-root junk on `main` today**, and the merge changes that input.

Whether the merge's eval fix repairs `:512` specifically is **(U)**. What would settle it: the
branch's own eval runs green (`34703641095`, `34678287935`, both success **(M)**), but the branch's
`daemon/rerank_eval_test.go` differs from `main`'s by more than a thousand lines, so *green on the
branch* is not *this assertion fixed*. The comparison that would settle it is the merge result's
first scheduled eval.

### 5.5 Two corrections to `docs/C1d_DIVERGENCE_2026-09-15.md`

Both are mine, both were reported with more confidence than they were measured with.

1. That report said runs `32002184546` / `32698022729` failed *"8 jobs, scattered across platforms."*
   **All 30 failed, each in 3–5 s.** The "8" was my own `head -8` reported as a cardinality.
2. That report said *"the last scheduled run that exercised those [macOS] jobs had three of them
   failing."* Wrong three ways: **four** jobs, **4-second billing** failures rather than code, and
   **not the last** — two later runs were 4/4 green. §3 of this document retracts it in full.

The second is the one worth carrying: **I hedged that claim instead of checking it, and the hedge
did the work the measurement should have done.** The sub-15 s billing filter was applied to the runs
I was *counting* and not to the run I was *reasoning from*.
