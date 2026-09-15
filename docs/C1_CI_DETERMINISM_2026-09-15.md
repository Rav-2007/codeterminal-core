<!-- coderefs: enforced -->
# C1 — Is CI green at HEAD evidence, and for what?

| | |
|---|---|
| **Branch** | `audit/adversarial-pass` |
| **HEAD** | `f099de6cf77058a2b5708a6ba9597d2902c01a3d` |
| **Tree** | clean |
| **vs `upstream/audit/adversarial-pass`** | ahead 5, behind 0 |
| **vs `origin/audit/adversarial-pass`** (the fork) | ahead 21, behind 0 |
| **Latest `build`** | `34739694099` @ `1013e1b` — **success** — on `Rav-2007/codeterminal-core` |
| **Latest `gates`** | `34739694106` @ `1013e1b` — **success** — on `Rav-2007/codeterminal-core` |
| **Build at HEAD** | **NO.** The five commits since `1013e1b` are markdown-only; `build.yml:43` carries `paths-ignore: ["**.md"]`. **There is no build run at HEAD and that is not a pass.** |

**Read-only.** No CI was triggered. Zero reruns were performed — §4 says why, and why that is the
correct outcome rather than a shortfall.

---

## 0. The answer, in one sentence

**CI green at `1013e1b` is evidence that the code at `1013e1b` passed 26 jobs under a pinned
toolchain on 2026-09-13 — and it is not evidence about two of those jobs, whose pass the diff cannot
explain, nor about `main`, whose CI is a different and currently red configuration.**

The single most useful thing this chunk found was not in the three-job table. It was a run nobody
had looked at: **`main` has been red since 2026-09-14**, for a defect this branch fixed on
2026-09-09. §2.

---

## 1. Step 1 — billing filter

Thirty most recent runs on `Rav-2007/codeterminal-core`. Elapsed computed from `createdAt` →
`updatedAt`. All durations **WALL-CLOCK**. `(M)`

**Eight failures under 15 s — excluded as suspected billing, not code:**

| run id | workflow | elapsed | sha |
|---|---|---|---|
| `33390351465` | build | **7 s** | `efc611d` |
| `33206751485` | retrieval eval | **5 s** | `b6cd7c9` |
| `33206751473` | build | **6 s** | `b6cd7c9` |
| `33205640840` | build | **6 s** | `9e39ad5` |
| `33203295013` | build | **7 s** | `4340cc9` |
| `33200776159` | retrieval eval | **5 s** | `d33d6b3` |
| `33200775923` | build | **6 s** | `d33d6b3` |
| `33189035407` | build | **7 s** | `adee244` |

All eight fall on 2026-08-28 and 2026-08-31, which is the known billing-block window ("recent
account payments have failed"). They are excluded from every determinism statement below. A 5–13 s
failure is a billing signal.

**Remaining free-tier headroom: `(U)`.** `GET /users/Rav-2007/settings/billing/actions` returns
`404` and `gh` reports the token lacks the `user` scope. **Not estimated.** What would settle it:
`gh auth refresh -h github.com -s user`, or the owner reading Settings → Billing → Actions.
Multipliers for planning: Linux 1×, Windows 2×, macOS 10×, 2,000 min/month free.

**Real failures after the filter (≥ 15 s):** `34837230165` (905 s, `efc611d`) · `34738752316`
(736 s, `1e48e3c`) · `34703641093` (825 s, `d732c34`) · `34314941707` (811 s, `4c782df`) ·
`34114544830` (877 s, `efc611d`).

---

## 2. `main` is red, and it is red for something this branch already fixed

**This was not in the chunk's scope. It fell out of Step 1 and outranks the rest of the report.**

`34837230165` — **workflow `build`, event `schedule`, branch `main`, head `efc611d`,
2026-09-14T11:14:49Z, 905 s, conclusion failure**, on `Rav-2007/codeterminal-core`. Seven jobs
failed: `lint` on **all six modules**, plus `retrieval eval (scheduled)`. `(M)`

Lint failing on six modules simultaneously is an environmental signature, not six code defects. The
cause, from `lint (daemon)`'s log:

```
go: finding module for package golang.org/x/sys/execabs
go: toolchain upgrade needed to resolve golang.org/x/sys/execabs
go: golang.org/x/sys@v0.48.0 requires go >= 1.26.0 (running go 1.25.14)
```

`bodyclose` imports `golang.org/x/sys/execabs` and does not list `x/sys` in its own `go.mod`, so
`go install` resolves that **missing** package at latest and lands on a version requiring Go ≥ 1.26.

**This exact failure already happened once, on 2026-09-09, and was fixed on this branch.**
`.github/workflows/build.yml` says so in its own words: *"@latest on four tools was the one unpinned
surface left… It broke on 2026-09-09 exactly the way unpinned surfaces break: upstream moved and
nothing here changed. Six lint jobs went red, the `Lint` step never ran, and the job name said
'lint' — which is how a gate gets waived."*

**`main` does not have the fix.** `(M)`

| | this branch | `main` @ `efc611d` |
|---|---|---|
| `scripts/tool-pins.txt` | present, `GOTOOLCHAIN go1.26.0+auto` | **absent** |
| `scripts/install-tools.sh` | present | **absent** |
| how lint tools are installed | `install-tools.sh`, five pinned specs | **`go install …@latest` ×5**, `build.yml:232-236`, `:406` |

The fix is `f5551ca` *"ci: remove the host-dependence — pinned tools, a pinned runner, no test
cache"*. `git merge-base --is-ancestor f5551ca efc611d` → **not an ancestor.** It is on this branch
only.

**The fix works, verified by execution rather than by reading.** With local `go1.25.13` — the same
version the pin names — every one of the five pinned tools installs cleanly under
`GOTOOLCHAIN=go1.26.0+auto`, into a throwaway `GOBIN`:

```
install staticcheck  OK      install ineffassign  OK      install bodyclose  OK
install errcheck     OK      install govulncheck  OK
```

`(M)` DETERMINISTIC (module resolution, not timing).

**So HEAD is not exposed and `main` is.** The floor in `tool-pins.txt` is exactly the mechanism its
own comment says is required: *"Pinning bodyclose cannot fix that; only a toolchain floor can."*

**One drift worth recording:** `build.yml:77` pins `GO_VERSION: "1.25.13"`, and the failing job
reported *"running go 1.25.14"*. The five `go.mod` files all say `go 1.25.13`. Whether `setup-go`
resolved the patch upward or `GOTOOLCHAIN=auto` did is `(U)`; what would settle it is one line of
the failing run's `setup-go` output, which I did not extract.

---

## 3. Step 2 — diff attribution

`git diff --stat 1e48e3c..1013e1b` → **one file**, `daemon/mcp/stderrredact_test.go`, +22/−3. `(M)`

Three jobs flipped FAIL→PASS from build `34738752316` @ `1e48e3c` to build `34739694099` @
`1013e1b`, both on `Rav-2007/codeterminal-core`:

| job | test | can the diff explain it? |
|---|---|---|
| `go (daemon)` | `TestConnect_RedactsProvisionedValuesFromServerStderr` | **YES.** The diff *is* that test file. See §3.1 — and the disposition is the opposite of what was expected |
| `fuzz` | `FuzzPeekUsageTotal` (proxy) | **NO.** No proxy file changed. Known intermittent `(C)` |
| `cross (windows-latest, clients/tui)` | `TestAResizeStormStaysWithinTheFrameBudget` | **NO.** No TUI file changed. Known wall-clock flake `(C)` |

**Two of the three passes at `1013e1b` are not attributable to the code.** That is the determinism
verdict, and it cost no Actions minutes.

### 3.1 The race-winning test was seen to fail — by its own vacuity floor

The chunk's hypothesis: *if the test was never seen to fail before the repair, it is a Class II
instance — scope sound, judgement broken — and belongs beside the `mentionsAnyIdent` guard.*

**The premise does not hold, and the disposition inverts.** `(M)`

The test was added at `e44f217` on 2026-09-12 and **failed on CI's first run of it**, the next day,
in `go (daemon)` of build `34738752316` @ `1e48e3c`. What failed was not the assertion — it was the
test's **own vacuity floor**:

```
vacuity floor: the fixture wrote no startup line, so this asserts nothing. Got: ""
```

The mechanism, from `1013e1b`'s message: the test read the stderr sink immediately after `Connect`
returned, assuming a completed handshake meant the startup line had been copied. `os/exec` copies
`cmd.Stderr` on a goroutine of its own with no ordering against the handshake. The assumption held
on a developer machine and lost on a loaded runner. The repair polls to a 10 s deadline.

**This is a vacuity floor working exactly as designed, not a Class II defect.** Without the floor the
test would have gone green while asserting nothing — the silent false-pass this repository keeps
finding. With it, the flaw announced itself within 24 hours of being written, on the first CI run,
and was fixed the same day.

So it does **not** belong beside `mentionsAnyIdent`. It belongs in the column of methods that paid
for themselves. **Report it as a negative result with equal weight**: the guard caught its own
author.

**A precision correction to an earlier record.** The recon pass called `go (daemon)` "genuinely
repaired by HEAD", which reads as though the product fix was broken. It was not. `e44f217`'s
redaction was correct throughout; what `1013e1b` repaired was the **test's determinism**. "The fix
was repaired" and "the test that proves the fix was repaired" are different states.

---

## 4. Step 3 — reruns: zero, and why that is the right answer

Owner grant: **at most three, Linux only, zero Windows.** Performed: **zero.** No `gh run rerun`, no
`gh workflow run`, no tag.

| job | Linux? | rerun? | reasoning |
|---|---|---|---|
| `cross (windows-latest, clients/tui)` | no | **0, by grant** | Windows is excluded outright, and bills 2× |
| `fuzz` / `FuzzPeekUsageTotal` | yes | **0, by judgement** | The determinism question is **already answered by runs that exist**: the proxy source is byte-identical between `1e48e3c` and `1013e1b`, and the job **failed** at the first and **passed** at the second. One fail, one pass, unchanged code, is non-determinism demonstrated. Three reruns would refine a rate nobody has asked for, at ~12 Linux minutes each |

**Both flaky jobs are therefore reported `(C)` from their existing records, not `(M)`.** I did not
measure either this session and do not present carried characterisation as measurement.

The one thing I would have spent a rerun on — confirming `main`'s lint break is current and
deterministic — turned out to need no CI at all: §2 establishes it from `main`'s own workflow source
plus a local reproduction of the fix. Zero minutes spent, stronger evidence than a rerun.

---

## 5. Step 4 — wall-clock exposure

`git grep -nE 'time\.Since|Elapsed|\.Seconds\(\)|Milliseconds\(\)'` over `*_test.go`:
**65 lines.** `(M)` DETERMINISTIC (a count).

| module | lines |
|---|---|
| `daemon` | 47 |
| `clients/tui` | 15 |
| `proxy` | **3** |

**H2 — my filter was worthless and I am reporting that rather than its output.** The chunk's
`rg -v 'derived'` removed **0 of 65** lines, because call sites do not contain the word "derived" —
they reference named constants. So "65 non-derived assertions" would have been a fabricated number.
The authoritative test is not a grep; it is the two AST guards, and **both pass**:

- `daemon/timingliterals_test.go` — walks from its module root (`:62`, `:336`), scans for **bare
  literals** in timing bounds. `go test -run 'TestTimingLiterals|TestTimingGuardCanStillFail'` →
  `ok codeterminal/daemon 0.022s`. `(M)`
- `clients/tui/testpolicy_guard_test.go:40` — `TestTimingBudgetsAreGuardedAgainstTheRaceDetector`,
  reads `"."` (`:42`), scans **package-level `time.Duration` declarations** for `raceEnabled`.
  → `ok codeterminal/clients/tui 93.301s`. `(M)`

The two guards have **deliberately different policies**, with an M8 justification written into
`daemon/timingliterals_test.go` for not merging them: tui's skips timing under the race detector
because its budgets are repaint budgets; daemon's asserts that bounds *fire*, which the detector
does not invalidate. Each documents its own gap — tui's is blind to "a timing assertion written
inline with no named constant", which was the shape of every daemon instance.

**The finding: `proxy` has three wall-clock assertions and no timing guard of any kind.** `(M)`

```
proxy/clientstall_test.go:80    elapsed := time.Since(start)
proxy/integration_test.go:199   elapsed := time.Since(start)
proxy/stagetimer_test.go:154    total  := time.Since(start)
```

`git grep -lE 'timingliteral|testpolicy|raceEnabled|derived bound' -- 'proxy/*_test.go'` → **no
hits.** Neither guard's scope reaches `proxy`: one walks `daemon/`, the other reads `clients/tui`'s
own directory. Whether those three assertions are *safe* is `(U)` — I counted them and did not read
them. Converted nothing.

Three representative examples from the 65, for the record: `clients/tui/acceptance_test.go:38`
(`ds = append(ds, time.Since(start))`, the frame-budget storm), `proxy/stagetimer_test.go:154`,
`daemon/timingliterals_test.go`'s own fixtures.

---

## 6. Step 5 — the frame-budget options memo

**Its trigger has fired.** The register recorded "will flake again"; it flaked, at run
`34738752316` @ `1e48e3c`, job `cross (windows-latest, clients/tui)`, on
`Rav-2007/codeterminal-core`. So this needs a verdict, not another deferral.

**Premise correction first: the bound is already a derived bound.** `(M)`
`clients/tui/renderbench_test.go:101` — `updateAssertedCeiling = 3 * updateCeilingAnyInput`, where
`:83` sets `updateCeilingAnyInput = 16 * time.Millisecond`. So 48 ms is 3× D-1's frame budget, with a
measured rationale at `:88-94`: the same 1 MB paste on one machine gave medians of 6.3, 13.6, 14.5
and 16.6 ms — a 2.6× spread straddling the 16 ms line — and *"two earlier versions of this test
asserted at or near the budget and both flapped, once in each direction."* **Option (a) as the chunk
phrases it — "convert it to a derived bound" — is already done.** That is why it needs a different
option set.

**The actual fragility is the statistic, not the constant.** The failing run reported
`median=5.455 ms  p99=9.286 ms  worst=49.723 ms` over 104 resizes. **Median is 34% of the 16 ms
budget and p99 is 58% of it; only the single slowest sample of 104 breached.** The assertion at
`clients/tui/acceptance_test.go:52` is on `worst` — a max-order statistic, which grows with sample
count and with a shared runner's neighbour noise, and is the least stable number the test computes.
All WALL-CLOCK.

| option | what changes | cost | risk |
|---|---|---|---|
| **(a) assert on `p99`, keep printing `worst`** ← **recommended** | `acceptance_test.go:52` compares `p99` rather than `worst` against the unchanged 48 ms | one line, plus the same change in `pastebound_test.go:115` if the shape is shared | Stops catching a single catastrophic frame. Acceptable on this test's own stated grounds: `renderbench_test.go:98` says *"the deterministic gate on this path is `TestPerTokenAllocationsAreBounded`; that is where the real acceptance signal lives"* |
| **(b) record known-flaky with a reopening condition** | no code change; a register row saying it fails on Windows at ~3.1× and reopens if **median** ever exceeds 16 ms, or if `worst` exceeds 3× on **Linux** | minutes | Leaves a red build recurring on a shared runner. A gate that flaps gets waived — the exact outcome `renderbench_test.go:93` warns about |
| **(c) raise the multiple, or make it platform-conditional** | `3 *` → `4 *`, or a Windows-only constant | one line | **Rejected.** Raising the bound was already declined, and a platform-conditional timing constant is precisely what the 17-literal conversion existed to remove |

**Recommendation: (a).** It preserves the gate's stated purpose — catching an order-of-magnitude
regression — removes the max-order-statistic fragility, and does **not** raise the bound. It is also
consistent with what the file already does for D-1's 16 ms ceiling: report the distance, assert the
order of magnitude.

**Implemented: nothing.** Someone else's module; the bound was deliberately not raised before; the
choice is the owner's.

---

## 7. Premises that did not hold (H4)

1. **"The race-winning test may never have been seen to fail, making it Class II."** It failed on
   CI's first run, and what failed was its own vacuity floor. The disposition inverts from defect to
   method-success. §3.1
2. **"Convert the frame budget to a derived bound"** — it already is one, `3 × 16 ms`, with a
   measured justification. The option set had to be rebuilt around the statistic instead. §6
3. **`rg -v 'derived'` distinguishes derived from bare assertions.** It removed 0 of 65 lines. The
   real arbiters are two AST guards with different scopes and policies. §5
4. **The chunk's scope is the three flipped jobs.** The most consequential finding is a fourth run,
   on `main`, that nobody had opened. §2
5. **Quota headroom is obtainable.** The billing endpoint 404s for this token. `(U)`, not estimated.

## 8. Self-corrections (H3)

1. **Retracted before it reached this document.** On reading `x/sys@v0.48.0 requires go >= 1.26.0` I
   inferred *"HEAD is exposed too — rerun HEAD's build today and lint fails identically."* I named
   the falsifying observation (does this branch pin the toolchain floor, and does the pin actually
   work?) and looked for it: `main` lacks `tool-pins.txt` entirely and installs `@latest`, this
   branch pins `GOTOOLCHAIN go1.26.0+auto`, and all five installs succeed locally under go1.25.13.
   **HEAD is not exposed.** Had I reported the inference, C7 would have inherited a false blocker.
2. **Withdrawn during the chunk.** I described `go (daemon)`'s flip as the daemon being "repaired",
   carried from the recon pass. The product fix was never broken; the *test* was non-deterministic.
   §3.1.
3. **Withdrawn during the chunk.** I was going to report "65 non-derived wall-clock assertions". The
   filter that produced "non-derived" removed nothing. §5.

## 9. What I did not verify

Longer than the verdict, deliberately.

1. **No CI was run.** Zero reruns, zero dispatches. Every CI fact is read from run history. Both
   flaky jobs stay `(C)`.
2. **I did not establish either flaky job's rate.** "Non-deterministic" is supported by one
   fail/one pass on unchanged code. A rate would need the reruns I declined to spend.
3. **I did not confirm `main`'s lint failure reproduces today in CI.** I reproduced the *fix*
   working locally and read `main`'s `@latest` installs. Whether a rerun of `34837230165` fails
   identically is `(U)`; what would settle it is one Linux rerun of `lint (daemon)` on `main`, ~1
   minute — cheap, and worth doing if anyone doubts §2.
4. **`retrieval eval (scheduled)` also failed in `34837230165` and I did not diagnose it.** Six lint
   failures had one cause; whether the eval shares it is `(U)`. Its log is in the same run.
5. **The `1.25.13` vs `1.25.14` drift is unexplained.** `(U)` — one line of `setup-go` output.
6. **`proxy`'s three wall-clock assertions were counted, not read.** Whether any is fragile is `(U)`.
7. **I did not read the other 62 wall-clock lines** either. The two AST guards passing is my evidence
   that the ones in their scope are compliant; I am relying on the guards, not on inspection.
8. **The `worst`-vs-`p99` recommendation is untested.** I did not run the frame-budget test on any
   platform, so "p99 has headroom" rests on the single failing run's own log output.
9. **`make check` was not run.** Four gates were, in C0b. Nothing here changed product code.
10. **Chains traced to the end:** the lint failure from job name → log → `x/sys@v0.48.0` → the
    missing-dependency mechanism in `tool-pins.txt`'s comment → a local reproduction of all five
    installs → `main`'s absence of the fix → `merge-base` confirming `f5551ca` is not an ancestor.
    The frame budget from the failing assertion → `updateAssertedCeiling` → `3 × updateCeilingAnyInput`
    → the measured rationale. **Chains not traced:** what `retrieval eval (scheduled)` failed on;
    how `PATH`/`GOTOOLCHAIN` reach the lint job in each of CI's three runner types; whether
    `pastebound_test.go:115` shares the `worst` shape and so would need the same one-line change.

## 10. Recipients

| What | Of whom | Time |
|---|---|---|
| **`main` is red and lacks `f5551ca`.** Decide: merge this branch, cherry-pick the pin, or accept a red shipping branch | repo owner | 15 min to decide |
| The frame-budget verdict — option (a), (b) or (c) | `clients/tui` owner | 10 min |
| `proxy` has three ungoverned wall-clock assertions — extend a guard, or record the gap | `proxy` owner | 20 min |
| Free-tier headroom, currently `(U)` | owner, via Settings → Billing, or `gh auth refresh -s user` | 2 min |

Nobody above has been contacted. This document is in the repository; that is not the same as being
delivered.
