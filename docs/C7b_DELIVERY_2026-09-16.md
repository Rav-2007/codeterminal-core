# C7b — delivery and reconciliation: the first push in forty commits

<!-- coderefs: enforced -->

**Recipient: the repository owner.** This chunk pushed. Nothing was tagged, no release was
dispatched, and the remote rename was not executed — it is recommended below, with the verification
already done in a throwaway clone.

| | |
|---|---|
| **Branch** | `audit/adversarial-pass` |
| **HEAD at close** | `09fe37b` |
| **HEAD at the push** | `aa81555` |
| **Tree** | clean (`git status --porcelain` empty at every commit boundary) |
| **Canonical remote** | `upstream` = **`Rav-2007/codeterminal-core`** (where CI runs) |
| **Other remote** | `origin` = `Rav-i24/Mochiii`, a **fork**. Every `gh` call below carries `--repo Rav-2007/codeterminal-core` |
| **Local vs canonical `main`** | `efc611d` is a strict ancestor of HEAD; HEAD is 0 behind. **Fast-forward available.** |
| **`gates`** | run **35054090357**, `aa81555`, **success** — on `Rav-2007/codeterminal-core` |
| **`build`** | run **35054090374**, `aa81555`, **success** — same remote, all 27 jobs |
| **`retrieval eval`** | run **35054090413**, `aa81555`, **success** — same remote |
| **`make check`** | `REAL_MAKE_CHECK_EXIT=0` at `aa81555`, run alone (H11) |

**"No build run at HEAD" is a distinct state from "build passed."** The three runs above are at
`aa81555`. HEAD is `09fe37b`, one commit later, and **there is no run at `09fe37b` yet** — the push
of that commit is the first action C6 should take.

---

## 0. What this chunk changed, commit by commit

| Commit | Kind | Subject |
|---|---|---|
| `15ffa66` | behaviour-changing | a `v*` tag may only release from the release line |
| `5090d70` | behaviour-changing | `reach.sh` described a stale pointer in the words of lost work |
| `f587147` | decision recorded | pre-place the two exemptions the remote rename will need |
| `aa81555` | behaviour-changing | `evalguard` threw away the diagnostic that said why it failed |
| `09fe37b` | behaviour-changing | a gate for the delivery gap could not see a delivery |

No commit mixes behaviour-preserving with behaviour-changing work. Four of the five are gate fixes
found by *doing* this chunk rather than by reading it.

---

## 1. Step 1 — the remote rename. Recommended, verified, not executed.

### 1a. The recommendation, unchanged

```bash
git remote rename origin fork
git remote rename upstream origin
git fetch --all
git branch -u origin/main main
git branch -u origin/audit/adversarial-pass audit/adversarial-pass
for b in ci/cross-go-test docs/readme-rewrite feat/web-grounding \
         security/ultra-vuln-pass-2026-08-06; do
  git branch -u "origin/$b" "$b"
done
# fix/ci-limiter-probe-and-interrupt-race and sec/untrusted-text-channels do NOT
# exist on the canonical remote. Measured with `git ls-remote --heads upstream`.
# They can only be deleted or pushed, not re-pointed.
```

**Owner action.** It is three lines of local configuration, fully reversible, and it is still the
owner's to keep — the decision was recorded in C5b and nothing here changes it.

### 1b. Did the rename change any script's behaviour? **Yes. It takes a gate from green to red.**

Grepping for the literal remote names over `scripts/`, `.github/`, `Makefile` and `.githooks/` returns
exactly one file: `scripts/reach.sh`, which resolves the canonical ref from a candidate list rather
than hardcoding it. By reading, the rename is safe.

**Reading was wrong, and the only way to find that out was to do it.** A throwaway clone was built
with the remotes renamed, every remote-tracking ref remapped to its post-rename meaning, and the
branches re-pointed exactly as the commands above would:

```
reach: FAIL branch 'docs/readme-rewrite' is 1 commit(s) ahead of 'origin/docs/readme-rewrite'. The work exists; it has not been delivered.
reach: FAIL branch 'main' is 40 commit(s) ahead of 'origin/main'. The work exists; it has not been delivered.
reach: 2 reach failure(s) of 268 item(s) examined, against origin/main
```

**(M)** Exit 0 → exit 1. Six `remote:*` exemptions retire as designed, and underneath two of them are
findings nothing was allowlisting.

**Both are real, and both were invisible because the branches track the fork, where `main` is
*behind*.** The `ahead` count was being measured against a repository the pipeline does not read, so
the gate reported nothing at all for a branch holding forty commits the canonical remote has never
seen. `reach.sh`'s own header names that ref as delivery-gap **instance 5**: the header named an
instance the gate, as written, could not see.

Measured in the same clone: 0 of the 40 and 0 of the 1 are unreachable from HEAD **(M)**. Stale
pointers, not lost work, carried by the merge.

### 1c. The eight `remote:*` triggers were imprecise, and that is now corrected

Every one said *"retires when: the remote rename"*. Measured in two separate stages of the same
clone: `git remote rename` rewrites `branch.<n>.remote`, so the branches follow the rename
automatically and the mismatch **persists**. What retires them is the `git branch -u` step after it.
The rename is necessary and not sufficient.

### 1d. What the owner should expect to see after running it

`make check` stays green — `f587147` pre-places the two exemptions — and `reach.sh` names the six
entries that have become dead, which is the list to delete:

```
reach: 6 allowlist entry(ies) matched NOTHING on this run. ...
reach:         remote:audit/adversarial-pass
reach:         remote:main
reach:         remote:ci/cross-go-test
reach:         remote:docs/readme-rewrite
reach:         remote:feat/web-grounding
reach:         remote:security/ultra-vuln-pass-2026-08-06
```

---

## 2. Step 2 — the pre-push local run, and what could not be run locally

### 2a. Everything runnable, run

| What | Where it lives | Result |
|---|---|---|
| `make check`, alone, un-piped | both sides | `REAL_MAKE_CHECK_EXIT=0` at `aa81555` **(M)** |
| `scripts/fuzz.sh` — **CI-only**, run anyway | `build.yml` `fuzz` | `18 of 18 target(s) ran at FUZZTIME=30s`, `REAL_FUZZ_EXIT=0` **(M)** |
| `npm ci` | `build.yml` `extension` | exit 0 |
| `npm run verify:vsix:selftest` | same | 8 credential names refused, 5 paths accepted, 4 shapes detected |
| `npm run check:webview` | same | 0 undefined symbols, 36 type findings (ceiling 36) |
| `npm run check:installpath` | same | `spawnDaemon passes an env, and 1 setting(s) are contributed and read` |
| `npm run compile` (`build:daemon` + `tsc`) | same | exit 0 |
| `evalguard` with `-v` (Row D's home) | both sides | green, and its allowlist-with-reasons disposition holds |

`evalguard` deserves its own line because Row D lives there. It is green at HEAD, and it is green in
CI at `aa81555` with `-v` (`gates` run 35054090357, step *"No committed file echoes a retrieval-eval
query": success*). The two reports listed in `evalSelfReferenceFiles` with their reasoning are the
whole disposition, and it survived contact with the runner.

### 2b. Named: every gate CI runs that could not be run here

Derived by extracting every `run:` step from all four workflows and subtracting what `make -n check`
invokes — not hand-listed.

| Surface | Why not here | Did it pass on `aa81555`? |
|---|---|---|
| `cross (windows-latest, ×4 modules)` — build, vet **and test** | no Windows machine | **yes**, 4/4 |
| `macos (clients/tui)` | no macOS machine | **yes** |
| `xvfb-run -a npm test` — Extension Development Host | `xvfb-run` absent, no display | **yes** |
| `docker build ./proxy` | no docker binary on this host | **yes** |
| `scripts/macos-sign-and-notarize.sh` | Apple credentials + a macOS runner | **not run** — release-only |
| `scripts/install-tools.sh` | installs the pinned analysis tools | **yes** (implicitly: `lint.sh` checks the versions locally) |
| `retrieval-eval.yml` — the real BGE model over the whole-repo corpus | 22m34s of embedding on a 2-vCPU runner; no local model story in the gate | **yes** |
| `build.yml`'s `eval` job | `schedule` / `workflow_dispatch` only | **skipped**, correctly |
| `TestTwoThousandTurnSoakStaysWithinItsBounds` at full unraced length | `make check` runs the reduced raced version | **yes**, inside `go (clients/tui)` |

**Five surfaces the push tested for the first time in forty commits, and all five are green.**

### 2c. A fourth workflow exists, and `make check`'s own banner cannot see it

`gate-parity.sh --what-ci-adds` is the repository's answer to "what did this green run not promise",
and it derives the script half from its manifest so a gate moving to CI-only appears with no edit.

**It contains zero mentions of the eval.** Measured: `./scripts/gate-parity.sh --what-ci-adds | grep
-icE 'eval|retrieval'` → **0** **(M)**.

`retrieval-eval.yml` fired on this push — `daemon/context.go` is on its path filter — and ran for
23 minutes. It is a whole workflow that invokes **no `scripts/*.sh`**, so it is structurally
invisible to a derivation keyed on script names. `build.yml`'s `eval` job is invisible for the same
reason.

> **This is H10's edge, stated precisely: deriving from a source of truth is only as good as the
> choice of source.** The manifest's axis is "scripts referenced in workflows". The question the
> banner asks is "steps CI runs that you did not". Those differ by two entire eval surfaces, and the
> gap is not visible from inside the derivation.

**Recommended, not implemented.** The banner's capability half is hand-listed by design and says so;
adding two lines to it is a one-line-each edit to a file this chunk already touched for another
reason, and mixing them would put an unmeasured claim in a measured commit. It needs its own change
with its own reasoning about which axis to derive from.

### 2d. And one of the banner's existing claims is false

> `    real macOS execution      macos-latest, and only on main.`

**Wrong twice.** `macos-tui` runs on **every push except `main`** — its condition is `github.ref !=
'refs/heads/main'` — and it ran on this one, green. On `main` macOS runs instead as
`cross (macos-latest, ×4 modules)`, a **wider** set of jobs. So macOS executes on every push; what
changes at `main` is which macOS jobs, not whether any run.

`(M)` — `build` run 35054090374 lists *"macos (clients/tui, every push): completed/success"* on a
non-`main` ref.

This matters for C6's precondition. The merge does switch on new macOS coverage, but it is
**`daemon`, `protocol` and `editapply` on macOS** — three modules that have never executed there —
not macOS itself.

---

## 3. Step 3 — the push

```
pre-push: ok daemon … ok clients/tui
pre-push: ok docs (docs-links: 332 links resolve)
pre-push: ok claims … ok actions (33 action reference(s), all pinned)
pre-push: ok toolchain (11 pin site(s) agree on go1.25.13)
To https://github.com/Rav-2007/codeterminal-core.git
   1013e1b..aa81555  audit/adversarial-pass -> audit/adversarial-pass
PUSH_EXIT=0
```

Pushed to the canonical remote **by name** — `git push upstream audit/adversarial-pass` — not by
bare `git push`, because the tracking ref still points at the fork and a habit that depends on
configuration is not a control. Target verified before pushing: remote URL read, remote branch
measured at `1013e1b`, `1013e1b` confirmed an ancestor of HEAD, so a fast-forward with no force.

Push access was measured first, not assumed: `gh api repos/Rav-2007/codeterminal-core` →
`"permissions":{"push":true}` on account `Rav-i24`, whose token carries the `workflow` scope this
push needed for the three workflow files it changes **(M)**.

### 3a. The results, every one naming remote, run id and commit

| Workflow | Run | Commit | Remote | Conclusion |
|---|---|---|---|---|
| `gates` | 35054090357 | `aa81555` | `Rav-2007/codeterminal-core` | **success** |
| `build` | 35054090374 | `aa81555` | same | **success** (27 jobs) |
| `retrieval eval` | 35054090413 | `aa81555` | same | **success** |

`gates` includes the new step *"Release branch guard (self-test): success"*. Every one of `build`'s
27 jobs is green, including all four `cross (windows-latest, …)`, `macos (clients/tui)`, `vscode
extension`, `proxy-image` and `fuzz`.

### 3b. The surprise, and it is not a failure

**A third workflow fired that Step 2's enumeration had not named.** `retrieval-eval.yml` is
path-filtered to retrieval source files, and this push carried `daemon/context.go`. It is the first
time the real eval has run on this branch — 674 files, 5,846 chunks, 22m34s of embedding on a
2-vCPU EPYC 7763 **(M)**.

All six of its tests PASS:

| | |
|---|---|
| top-3 recall (fixture set) | 14/15 = 0.93, threshold 0.80 |
| semantic-only chunk-level | 30/49 (61.2%) |
| hybrid chunk-level (retrieval only) | 33/49 (67.3%) |
| **DELIVERED to the prompt — the gated number** | **40/49 (81.6%)** |
| retrieved then budgeted out | 1 |
| hybrid file-level | 46/49 (93.9%) |
| right file, wrong chunk | 13 (granularity, not ranking) |
| right file never retrieved | 3 |

All **DETERMINISTIC** in the sense that matters here — one run, one fingerprint
(`abe91e22cbfe59a9`), and a re-run on the same corpus is what would confirm it. The embedding time
is **WALL-CLOCK**.

---

## 4. Step 4 — reconciliation, and the divergence table

### 4a. Local green versus CI green: no divergence

| Job CI runs | Local equivalent | Diverged? |
|---|---|---|
| `go (×6 modules)` | `make race` | no |
| `lint (×6)` | `make lint` | no |
| `govulncheck (×6)` | `make supplychain` → `govulncheck.sh` | no, and the asymmetry is recorded in the manifest |
| `fuzz` | CI-only; run by hand here | no |
| `cross (windows ×4)` | `make crossvet` **compiles only** | **not comparable** — see below |
| `macos (clients/tui)` | nothing | **not comparable** |
| `vscode extension` | 5 of 6 steps locally | **not comparable** for the EDH step |
| `proxy-image` | nothing | **not comparable** |
| `offline gates` (10 steps) | `make docs`, `make supplychain`, `make parity` | no |
| `corpus` | `make evalguard`, `supply-chain.sh` | no |
| `retrieval eval` | nothing | **not comparable** |

**Nothing that passed locally failed in CI.** That is the honest result and it is worth stating
plainly, because the expectation going in was the opposite: forty commits validated by one machine.

The rows marked *not comparable* are not a clean bill of health — they are the surface where local
has no opinion at all. `make crossvet` compiles for Windows and darwin; `cross` builds, vets **and
runs the tests**. A green `crossvet` and a green `cross` are different claims and the banner already
says so.

### 4b. `reach.sh` after the push. **The finding did not clear.**

```
upstream/audit/adversarial-pass = aa81555
HEAD                            = aa81555
configured upstream of the branch: origin/audit/adversarial-pass   (the FORK, at 3d6ea63)
```

The branch had arrived. `reach.sh` went on reporting it as in flight, plus **57 pipeline commits and
178 document citations as undelivered** — every one of which had arrived **(M)**.

**The reference frame, for the third time in this one gate.** Check 1 asks whether a branch is ahead
of its *upstream*, and "upstream" is the configured tracking ref. So the question *has this work
reached the pipeline?* was being answered about a repository the pipeline does not read. A push to
the right place could not clear it.

> **A gate for the delivery gap that cannot see a delivery is worse than no gate: it teaches you to
> ignore it.**

Fixed in `09fe37b`. `ahead` is now also asked of the canonical remote's copy of the same branch when
one exists, and a branch 0 ahead of that copy is reported as **ARRIVED**, not as a failure:

```
reach: 1 branch(es) have ARRIVED where the pipeline watches, while their
reach:       tracking ref still says otherwise. NOT a delivery failure; the
reach:       wrong-remote fact is reported separately:
reach:         audit/adversarial-pass -- 56 ahead of 'origin/audit/adversarial-pass', 0 ahead of 'upstream/audit/adversarial-pass'
```

The wrong-remote fact is **not** swallowed: `remote:audit/adversarial-pass` still fires, because
*your tracking ref points at a fork* and *your work has not arrived* are two facts and this script's
own comment forbids them sharing a phrase. What changed is that arrival stopped being reported as
its absence.

### 4c. Three defects in `reach.sh`, all found by doing rather than reading

| # | Defect | Found by |
|---|---|---|
| 1 | check 1 described a stale pointer in the words of lost work; check 2 learned that lesson ("487 commit(s)") and check 1 shipped without it | simulating the rename |
| 2 | an allowlist entry matching nothing printed nothing, so the one thing the allowlist could still hide was **itself** | pre-placing two entries and noticing they were silent |
| 3 | `ahead` measured against the fork, so a push to the canonical remote could not clear it | pushing |

Defect 2's fix earned its own self-inflicted bug worth recording: `covering_branch` runs inside a
`cover="$(…)"` command substitution, so an entry it marked was marked in a **subshell** and
discarded. The first run of the change said `audit/adversarial-pass` matched no allowlist entry two
lines under a line saying that same entry accounted for 57 commits and 178 citations — **one run
contradicting itself.** Marking now happens in the parent shell. That is the second time in this
pass that a subshell or a shadowed variable produced a wrong row.

### 4d. The other local/CI divergence, fixed

`Makefile`'s `evalguard` recipe sent the test's output to `/dev/null`. On 2026-09-15 it went red and
printed nothing but `make: *** [Makefile:174: evalguard] Error 1` — no file, no query, no remedy. The
identical assertion in `gates.yml` runs with `-v` and names every offending file in one line, so a
developer running the local gate got a **strictly worse report than CI from the same check**.

Fixed in `aa81555`, and verified both directions from a committed tree: planting query 1's text
verbatim in an untracked doc makes the gate name the file, the queries and all three remedies;
removing it returns one green line. That probe also showed **one string is a query in both sets** —
it reported `[locate#1 token-efficiency#50]` for a single line of text — so one leak contaminates
both evals, exactly as the test's header says.

---

## 5. Step 5 — the eval contradiction. **The premise dissolves, twice. (H4)**

### 5a. At HEAD there is nothing to resolve

```
1262:	mustHit := []int{5, 7, 8}
1421:	if hybridDelivered[0] {
1430:		t.Logf("KNOWN GAP (not gated, pre-existing, out of scope): query 1 …
```

Index 0 is **not in `mustHit`**, and the only statement about query 1 at HEAD is the not-gated log.
The chunk anticipated this: the branch's eval differs from the canonical main's by +1,170/−112
**(M)** and the disposition was rewritten along with it.

### 5b. And on the canonical main it was never a contradiction either

The two cited lines, read at `upstream/main`:

| Line | What it is |
|---|---|
| `:512` | `t.Errorf("hybrid retrieval REGRESSED %d quer(ies) that passed semantic-only: %v", …)` |
| `:570` | `t.Logf("KNOWN GAP (not gated, pre-existing, out of scope): query 1 …")` |

The gate's predicate, read at the same revision, is `semanticOnlyHits[i] && !hybridHits[i]` — a
**relative** condition. `:570`'s "not gated" is about the **absolute** condition `!hybridHits[0]`.
Query 1 missed under *both* tiers, so `:512` could never fire for it.

> **Two lines that gate different predicates over the same query are not a contradiction.** The file
> was never saying both things. There is no `daemon/` owner call here, and the recommendation the
> chunk asked me to present would have asked the owner to choose between two statements that do not
> conflict.

**H3: this is retracted before it reaches anything downstream.** C7 §8.2 recorded the contradiction
as an open owner decision; that row should be struck rather than answered.

### 5c. What the live run says instead, and it is more interesting

On `retrieval eval` run 35054090413, at `aa81555`, on a real BGE model over the whole repository:

```
rerank_eval_test.go:1422: NOTE: query 1 HITS. Measured 2026-08-28 at k=10: its answer chunk
  came back at RANK 7 … so on this run the gap was k=5 truncation, not the semantic
  doc-vs-code confusion described above. NOT added to mustHit on one run: the same one-run
  inference was made about this query during the 2026-08-27 embed-window work and did not
  reproduce. Add it after it holds across several runs on different corpus states.
```

Per-shape: `doc 1/1 (100%)` — query 1 is the only `doc`-shaped query in the set **(M)**.

**The known gap hits, and the test refuses to promote it on one run, for a recorded reason.** That is
the file being right about its own uncertainty, which is the opposite of the defect C7 attributed to
it. `(U)` on whether it holds; **what would settle it:** the same query hitting across several
scheduled runs on different corpus states, which is precisely what the code says.

---

## 6. Step 6 — the branch guard. Landed, and no tag was created.

### 6a. The failing case, by reading the conditions

A tag `v0.0.2` pushed on `audit/adversarial-pass`, expanded against the workflow **before**
`15ffa66`:

| Element | Value | Consequence |
|---|---|---|
| trigger | `push: tags: ["v*"]` | matches |
| `github.ref` | `refs/tags/v0.0.2` | |
| `binaries` job `if:` | **absent** | **runs**, on all three runners |
| `package` job `if:` | **absent**, `needs: binaries` | **runs** |
| signing guard step | `--ref refs/tags/v0.0.2` → tag mode, darwin marker UNSIGNED | **fails `package`** |
| *Attach to the GitHub Release* | `startsWith(github.ref, 'refs/tags/v')` → **true for a tag on any ref** | never reached, *today only* |
| `publish` | `if: false` | skipped |

**Nothing gated on a branch, anywhere in the file.** What refuses the release today is a missing
certificate: `gh api repos/Rav-2007/codeterminal-core/actions/secrets` →
`{"total_count":0,"secrets":[]}`, with a control proving the token can read repository config
(`gh api repos/…` returns `default_branch`, `private`) **(M)**.

> **A missing credential is doing an access-control job.** The moment the five `MACOS_*` secrets
> land, darwin signs, the signing guard passes, and the attach step — whose condition is satisfied by
> a tag on *any* ref — creates a draft Release with binaries built from an unreviewed branch. **The
> guard had to land before the secrets, and now it has.**

Also measured, because the `if:` expressions depend on it: `gh api
repos/Rav-2007/codeterminal-core/actions/variables` → `{"variables":[],"total_count":0}`. So
`vars.HOSTED_RUNNERS_DISABLED` is unset and every hosted-runner job in `build.yml` really did run —
the 27 green jobs are not 27 skips **(M)**.

### 6b. The expansion after

| Element | Value | Consequence |
|---|---|---|
| new `guard` job | no `if:`, runs on every event | checkout with `fetch-depth: 0` |
| the assertion | `--ref github.ref`, `--commit github.sha`, `--release-line github.event.repository.default_branch` | |
| scope | asserts only for `refs/tags/v*` | a `workflow_dispatch` rehearsal on any branch stays green |
| `binaries` | **`needs: guard`** | nothing is built until the release line is confirmed |
| `package` | `needs: binaries` | unchanged, transitively gated |

**The release line is derived, not hardcoded.** No branch name appears in the script or the workflow;
`github.event.repository.default_branch` is the repository's own declaration, read out of the event
payload. Change the default branch and the guard follows with no edit.

Scope is **exactly the attach step's own predicate**, which is the derivation that keeps the two from
drifting apart. A missing release line still fails on *every* event including dispatch, deliberately:
a wiring error is then caught by the next rehearsal rather than by the next release, which is the
whole lesson of R1.15.

### 6c. It was proven against this repository, with no tag created

```
--- a hypothetical v0.0.2 at HEAD ---
FAIL: refusing to release v0.0.2.
  tag commit    f4917e2
  release line  upstream/main at efc611d, read from upstream/main
  relationship  AHEAD OF the release line: 'upstream/main' is an ancestor of the tag,
                so the tag carries work that has not been merged.
  Remedy: merge the work into 'upstream/main' first, then re-tag. The tag is not wrong;
          its position is.
  THIS IS NOT A STATEMENT ABOUT THE CODE.

--- the same tag at the canonical main tip ---
ok -- v0.0.2 (efc611d) is on the release line 'upstream/main', read from upstream/main (efc611d)
```

Plus 20 self-test arms on a real throwaway repository holding the three ancestry shapes, green
locally and in `gates` run 35054090357. The *on-the-line* and *ahead-of-the-line* arms differ **only**
in ancestry, so their disagreement is what proves the assertion is load-bearing — no fail-open
`NEUTER` switch was added to demonstrate it.

### 6d. H2 caught the guard mid-probe, and the fix is in the guard

Its **first** real-repository probe reported:

```
ok -- v0.0.2 (efc611d) is on the release line 'main' (4b8c53e)
```

`4b8c53e` is the **fork's** main. `resolve_line_ref` tries `refs/remotes/origin/main` first — correct
on a CI runner, where `origin` *is* the repository the workflow runs in, and wrong on a developer
machine whose `origin` is a fork. **A true statement about the wrong repository, reading as a pass.**

It now prints the refname it resolved, which is the only reason that was visible. A guard that says
which reference frame it used can be checked; one that only says `ok` cannot.

---

## 7. Corrections and retractions (H3)

| # | What I said or would have said | What is true |
|---|---|---|
| 1 | *"the rename is safe; nothing is keyed to the remote name"* — the standing reason on `remote:audit/adversarial-pass` | True of the grep, false of the outcome. The rename takes `reach.sh` from exit 0 to exit 1. |
| 2 | *"`:512` gates a query `:570` excludes"* — C7 §8.2, presented as an owner decision | **Retracted.** Different predicates, relative vs absolute. Never a contradiction, on either side. |
| 3 | The eight `remote:*` triggers: *"retires when: the remote rename"* | **Corrected.** The rename is necessary and not sufficient; `git branch -u` retires them. |
| 4 | The new unconsulted-entry report, first run | Contradicted the COVERED line in the same output. Cause: bookkeeping inside a command substitution. Fixed before the commit. |
| 5 | Step 2's enumeration of what CI would run | **Incomplete.** It named two workflows; three fired. `retrieval-eval.yml` was missing. |

---

## 8. What I did not verify

Longer than the queue, as it should be.

- **`09fe37b` has no CI run.** The three green runs are at `aa81555`. C6's first act is the push.
- **The rename itself was not executed.** Everything about it is measured in a clone that mirrors the
  topology, not in this repository. The clone shares objects and remaps refs faithfully, but it is a
  model, and a model is `(M)`-about-the-model.
- **`scripts/macos-sign-and-notarize.sh`'s signed path.** Never run, by anyone, anywhere. The guard's
  handling of a *signed marker* is covered by a marker the self-test writes; the signing itself is
  not.
- **That GitHub supplies `github.event.repository.default_branch` on a tag push**, and that
  `fetch-depth: 0` leaves the default branch resolvable on the runner. Both are runner properties,
  stated at the call site, and the guard fails closed if either is false. **No tag was pushed to
  check.** `(U)` — what would settle it: one tag on the release line, which is the owner's to decide.
- **That the guard refuses a real tag.** Its verdict was proven on real commits with no tag in
  existence. The workflow wiring around it — `needs: guard`, the `fetch-depth`, the payload field —
  is verified by reading and by YAML parse, not by a release.
- **Whether the eval numbers reproduce.** One run, one fingerprint. `(U)`.
- **R1.12's 22.4 ms.** Still unresolved and still not updated. `make check` passed here, and because
  the ratchet runs `go test` without `-v`, **a passing run prints no repaint measurement at all** —
  so this chunk added no data. That is a third member of the discards-its-own-evidence family, after
  `tail -12` and `evalguard`'s `/dev/null`: a wall-clock gate whose measurement is visible only when
  it breaches cannot be trended. **Recommended, not implemented.**
- **Row C.** Untouched. Hold is the only available disposition, not a deferral: the address is in no
  file at HEAD, and what remains is authorship metadata on 194 commits, which no file delete or
  content filter reaches. `TRIGGER: before this repository or any fork of it is made public`.
- **Whether a human has been told any of this.** `reach.sh` says so itself, every run.

---

## 9. Handover — named recipients

| What | Who | How long |
|---|---|---|
| Run or decline the remote rename, then delete the six entries `reach.sh` will name | **whoever holds the working copy** | 2 minutes, plus one `make check` |
| Delete or re-point `docs/readme-rewrite`, `feat/canonical-language-table`, `feat/edit-payload-ingestion`, `ci/main-lint-pin` — all measured contained in HEAD | same | 1 minute |
| The five `MACOS_*` secrets — **the guard is now in, so this is unblocked** | **whoever holds repository settings** | 15 minutes |
| Strike C7 §8.2's eval row rather than answering it | **`daemon/` owner** | reading this section |
| The two `daemon/` TB7 rows, and whether query 1 should join `mustHit` after several runs | **`daemon/` owner** | one scheduled eval cycle |
| The repaint gate's three remedies, re-priced at 1.31× headroom, plus making the measurement visible on a pass | **`clients/tui` owner** | one test edit |
| R1.12's provenance: the commit and host the 22.4 ms was taken on | **whoever recorded it** | unknown until asked |
| Merge, then watch the first scheduled run on `main` | **repository owner** | C6 |

---

## 10. Hardening instances earned here

| Rule | Instance |
|---|---|
| **H2** (eighth) | The guard reported a pass about the **wrong repository** because it resolved `refs/remotes/origin/main` on a fork-remoted machine. A reference frame, again — and the remedy was to make the tool print which frame it used. |
| **H4** | The eval contradiction dissolved on both revisions, for two different reasons. A dissolved premise is the result. |
| **H8** | Every probe in this chunk ran from a committed tree. The `evalguard` failure arm planted and removed an untracked file only after the four preceding commits had landed. |
| **H10** (its edge) | `gate-parity`'s banner derives from a source of truth whose **axis is wrong for the question asked** — scripts referenced, not steps run — and two eval surfaces are invisible from inside the derivation. |
| **H11** | `make check` ran alone; the recorded header names what else was running: nothing. |
| **H12** | Its sharpest instance yet: the delivery-gap gate could not see a delivery, so an unpushed branch *and* a pushed one produced the same output. |
| **H13** | The signing-guard reasoning in C7 Step 0 has a sibling here: reading `reach.sh`'s candidate-list derivation gave the right answer about safety for the wrong reason, and only running it showed the outcome. |
