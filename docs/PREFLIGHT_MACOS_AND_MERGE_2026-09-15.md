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
