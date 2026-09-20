# Release planning and depth QA — 2026-09-17

<!-- coderefs: enforced -->

**Scope.** Part 0's verdict, Part 2's release plan, Part 3's ledger, and the four
discipline sections. Part 1 did not run; §1 records why.

**Method.** Every number below is `(M)` measured this session, `(R)` read from the
repository, `(U)` unknown with what would settle it, or `(C)` carried from a prior
pass and not re-measured. Every CI claim names remote, run id and commit. The
remote is `Rav-2007/codeterminal-core` throughout unless it says otherwise.

**Run ids used as shorthand.**

| Tag | Run | Commit | Event | Result |
|---|---|---|---|---|
| **B** | `35204865252` | `9f8f4f0` | push (main) | build, success, 31 jobs |
| **D** | `35183141505` | `7454202` | workflow_dispatch (`audit/adversarial-pass`) | build, success, 31 jobs |
| **G** | `35227134808` | `0ee8c19` | push (main) | gates, success, 2 jobs |
| **E** | `35179379615` | `611ce44` | push (main) | retrieval eval, success |
| **R** | `31505527398` | `bd1bc69` | push tag `v0.0.1` | release, **failure in `package`** |
| **L** | local | `0ee8c19` | `make check` | exit 0, 13:19:24–13:31:10 UTC |

---

## 0 — Part 0 verdict: **CLEAN**

All five criteria, each with its evidence. The verdict is CLEAN **as of `0ee8c19`**,
which is not the commit the prompt named, and the tree was **not** clean when this
session started. Both changes are recorded rather than smoothed over.

### 0.1 Tree empty — **PASS (M)**

```
$ git status --porcelain
(no output)
```

**It was not empty at session start.** The first measurement returned
`M docs/C6_CLOSING_2026-09-17.md` — 85 uncommitted lines appending a "C6d" section.
At 18:45:15 +0530 (13:15:15 UTC), four minutes before `make check` began, that file
was committed and pushed by the repository owner as `0ee8c19`, author `anonymous
<anonymous@gmail.com>`. **I did not make that commit** and ran no `git commit`,
`git add` or `git push` at any point. It is visible in `git reflog` as `HEAD@{0}`.

### 0.2 HEAD == canonical `main` — **PASS (M)**

```
upstream/main = 0ee8c197bd3f099dd4e8ddf69698a6af1974a743
HEAD          = 0ee8c197bd3f099dd4e8ddf69698a6af1974a743
local main    = 0ee8c197bd3f099dd4e8ddf69698a6af1974a743
$ git rev-list --left-right --count HEAD...upstream/main
0	0
```

**The remote names are the inverse of what the prelude states** — see §5, premise 1.
`upstream` is the canonical repository; `origin` is the fork. Every `gh` call in this
report passes `--repo` explicitly, which is why the inversion cost nothing.

### 0.3 `build` and `gates` green on `main` at that SHA — **PASS, with one qualification (M)**

- `gates` — **OBSERVED** `35227134808` @ `0ee8c19`, push, success, 2/2 jobs
  (`offline gates`, `corpus holds no answer key`).
- `build` — **did not run at `0ee8c19`, correctly.** `.github/workflows/build.yml:41`
  carries `paths-ignore: ["**.md"]`. The delta from the last `build` observation to
  HEAD is two commits (`2a6ef91`, `0ee8c19`) touching exactly one file,
  `docs/C6_CLOSING_2026-09-17.md`; `git diff --name-only 9f8f4f0..0ee8c19 | grep -cv '\.md$'`
  returns **0**. The last `build` observation is **B** = `35204865252` @ `9f8f4f0`,
  push, success, 31 jobs.

**The qualification is a defect in the criterion, not in the repository.** "The most
recent `build` … green at that SHA" is **unsatisfiable by construction** whenever
`main`'s tip is a markdown-only commit, because `paths-ignore` guarantees no `build`
run exists at that SHA. The criterion and the workflow cannot both be honoured. I
read it as satisfied because the untested delta is provably empty of anything `build`
inspects. Recorded as premise 5 in §5.

### 0.4 `reach.sh` exit 0 — **PASS (M)**

Exit 0, **256 items** (the prelude says 255 — premise 4). 12 exemptions detected,
each naming its retiring event; 2 allowlist entries matched nothing.

### 0.5 `make check` exit 0 with full banner — **PASS (M)**

Exit 0 at `0ee8c19`, 11m46s wall clock. **H11: it ran alone.** Nothing else was
started between 13:19:24Z and 13:31:10Z — no `gh` call, no build, no read. The
banner's own list of what it did not run:

> **Steps and gates that run only in CI:** `macos-sign-and-notarize.sh` (release-only,
> needs Apple credentials plus a macOS runner); `install-tools.sh`.
> **Capabilities a developer machine does not have:** real Windows execution
> (`cross (windows-latest)` builds, vets *and runs the tests*; `make crossvet` only
> compiles); real macOS execution (`macos-latest`, and only on main); the VS Code
> extension (tsc plus a real Extension Development Host); the proxy container
> (`docker build`); the 2000-turn soak (run unraced at full length in CI; the local
> gate runs the reduced raced version).

Gate lines worth carrying forward: `docs-links: 332 links resolve`;
`docs-coderefs: 195 reference(s) resolve, across 20 enforced document(s); 253
reference(s) in 17 unenforced document(s) NOT checked`; `debt-markers: 157 non-test
file(s) across 6 module(s), 0 markers`; `gate-parity: 24 script(s) accounted for --
14 both, 2 local-only, 2 CI-only, 6 manual`.

---

## 1 — Part 1 did not run

Part 0 is CLEAN, so depth QA was not triggered. Two things found *while* measuring
Part 0 would have been Part 1 material, and are classified per §1.2 here rather than
left implicit:

**1.1 — The four consecutive scheduled failures are stale, not a live defect.**
`build` on `schedule` failed on 2026-08-24 (`32698022729`), 08-31 (`33390351465`),
09-07 (`34114544830`) and 09-14 (`34837230165`). **All four ran at `efc611d`**, a
2026-08-12 commit, because canonical `main` sat at `efc611d` until 2026-09-17, when
281 commits landed at once. The failures were six `lint` jobs plus
`--- FAIL: TestRerankEvalRetrievalRanking (417.07s)`.

*Classification: **environment-dependent gate**, resolved.* The eval failure's cause
is visible in its own log — the anchor `"CREATE TABLE IF NOT EXISTS"` resolving to 4
chunks past a 3 ceiling, and anchors matching `test.log` and `test_output.txt`. Both
of those files **were tracked at `efc611d` and are not tracked at `0ee8c19`** (`git
ls-tree efc611d` lists them; `git ls-files` at HEAD does not), and
`daemon/evalcorpus_test.go` — the guard against a corpus containing its own answer
key — **did not exist at `efc611d`** and is 346 lines at HEAD. `daemon/rerank_eval_test.go`
gained 1282 lines over the same span.

*Settled by measurement, not inference:* the eval job is green at a current commit.
**D** = `35183141505` @ `7454202` ran with `event_name == 'workflow_dispatch'`, which
satisfies the eval job's condition at `.github/workflows/build.yml:783`, and
`retrieval eval (scheduled)` reports **success**. `7454202` is an ancestor of
canonical `main`.

**1.2 — A fourth flaky surface, not in the prelude's list of three.**
`cross (macos-latest, daemon)` failed at `611ce44` (`35179379541`) on:

```
--- FAIL: TestSQLiteCancellation_DoesNotReachAnFTS5PhraseMatch (0.25s)
    cancellation_test.go:179: cancelled at 200ms, the FTS5 match returned after
    227.709625ms (under the 1s bar) with a genuine context.Canceled --
    SQLite now HONOURS the interrupt inside a phrase match.
```

*Classification: **environment-dependent gate**.* This is a vacuity floor asserting a
*negative* about SQLite — that cancellation does **not** reach inside a phrase match —
and macOS timing inverted it. The test is doing its job (it detected that the baseline
moved); the assertion is simply not platform-stable. Observed **1 failure in 3 macOS
observations**: red at `611ce44`, green at `9f8f4f0` (**B**) and `7454202` (**D**).
Not fixed, not filed. Handover row H-4.

---

## 2 — The release plan

### 2.1 Artifact inventory, measured

Four artifacts. Three binaries from `.github/workflows/release.yml`; the proxy from
`proxy/Dockerfile`, which is the **only** Dockerfile tracked in the repository.

| Artifact | Built by | Runner | Flags | Uploaded to | Verified by |
|---|---|---|---|---|---|
| `codeterminal-daemon` | `release.yml:126` | native ×3 | `-trimpath`, `CGO_ENABLED=0` | `binaries-<target>` artifact | `verify-vsix.js` (in-`.vsix` presence + Go toolchain floor) |
| `codeterminal-embedder-helper` | `release.yml:132` | native ×3 | `-trimpath`, `CGO_ENABLED=1` | `binaries-<target>` artifact | same |
| `codeterminal-tui` | `release.yml:148` | native ×3 | `-trimpath` | `out-bin/`, `terminal-client` artifact | `verify-vsix.js` asserts it is **absent** from the `.vsix` |
| proxy image | `proxy/Dockerfile` | `proxy-image` job, ubuntu | `docker build` | **nowhere** — built, never pushed by CI | build success only |

Targets are `linux-x64` (ubuntu-latest), `darwin-arm64` (macos-latest), `win32-x64`
(windows-latest), `fail-fast: false`, **native everywhere, not cross-compiled**
(`release.yml:98-107`).

**`-trimpath` confirmed by measurement, with a control arm (M).** Reading the flag
proves nothing; the property regresses silently, so the probe needs to be shown
capable of failing:

```
daemon       TRIMMED  absolute-path strings = 0
clients/tui  TRIMMED  absolute-path strings = 0
helper       TRIMMED  absolute-path strings = 0
proxy        TRIMMED  absolute-path strings = 0
daemon     UNTRIMMED  absolute-path strings = 1036   <-- control arm
```

The control arm is the point: the probe detects **1036** absolute paths when the
property is absent, so the four zeroes are evidence rather than a silent no-op.
(The prelude cites 678 for the untrimmed case; the tree has grown — premise 10.)

### 2.2 The dependency chain, each link verified

```
BACKLOG blocker B3 (Apple Developer enrolment)            [LIVE  (C)]
  -> the five MACOS_* secrets                             [ABSENT (M)]
    -> the signing guard passes on darwin                 [NEVER RUN (M)]
      -> release creation runs                            [NEVER RUN (M)]
        -> asset upload runs                              [NEVER RUN (M)]
```

Link by link, measured rather than assumed:

- **Secrets absent.** `gh secret list --repo Rav-2007/codeterminal-core` → exit 0,
  **zero rows**. *(Prelude holds.)*
- **`marketplace` environment absent.** `gh api .../environments/marketplace` →
  `{"message":"Not Found",...,"status":"404"}`. *(Prelude holds.)*
- **Zero releases exist.** `gh release list` → exit 0, zero rows — while the tag
  `v0.0.1` **does** exist at `bd1bc69`, which is an ancestor of `main`. So the tag is
  spent: a rehearsal or a first release needs a *new* version number.
- **The release path has run exactly once.** `gh run list --workflow release.yml`
  returns **one** run in the repository's history: **R** = `31505527398`, push,
  `v0.0.1`, `bd1bc69`, 2026-08-11, failure. Jobs: `binaries (linux-x64)` success,
  `binaries (darwin-arm64)` success, `binaries (win32-x64)` success, `package`
  **failure**, `publish` skipped.
- **The `guard` job has never run at all (M).** **R**'s job list contains no
  `release line` job — the branch guard landed after 2026-08-11. Since **R** is the
  only release run ever, `guard` has zero observations on any event. Its entire
  exercise is its self-test.
- **The ordering constraint is satisfied (M).** `binaries` carries `needs: guard`
  (`release.yml:96`), and the guard is a `needs:` edge rather than three restated
  `if:`s. The release line is read from
  `github.event.repository.default_branch` (`release.yml:73-75`) — no branch name is
  hardcoded in the workflow or the script. Canonical `default_branch` is `main` (M).
- **Both guards self-test green, here, now (M).**
  `scripts/release-branch-guard.sh --self-test` → exit 0, **20 `ok` arms**
  (the prelude's "20 self-test arms" holds exactly).
  `scripts/release-signing-guard.sh --self-test` → exit 0, **40 `ok` arms**.
  Both run in CI at `.github/workflows/gates.yml:83` and `:92`, and locally via
  `Makefile:249-250`.

**The signing guard's condition expansion**, read from `scripts/release-signing-guard.sh`
(`run_guard`, and `is_tag` set by `case "$ref" in refs/tags/v*)`):

| `github.ref` | `SIGNED-darwin-arm64` marker | What happens |
|---|---|---|
| `refs/tags/v*` | **present** | "SIGNED marker present -- releasable"; nothing excluded; attach step runs with all assets |
| `refs/tags/v*` | **absent** | `explain_tag_failure`, `fail_count=1`, **return 1** → step fails → `package` fails → **the attach step never runs → zero assets publish on every platform** |
| `workflow_dispatch` | present | nothing excluded, green; attach is gated on `startsWith(github.ref,'refs/tags/v')` = **false**, so no release is created anyway |
| `workflow_dispatch` | absent | darwin `.vsix` and tui binary `rm`'d, **both** `SHA256SUMS` manifests *regenerated from disk* (not hand-edited), loud warning, **job stays green** |

**The asymmetry the prelude describes is real and load-bearing — premise holds.** Two
details worth keeping: the manifests are regenerated rather than pruned, so a checksum
file can never name a file the release does not carry; and on a dispatch the guard
prints `WOULD ATTACH <file>` for each surviving asset, because on a dispatch the run
log is the only evidence that the exclusion happened.

### 2.3 The two routes — and a third the prompt does not name

With the marker absent, a tag fails and nothing publishes anywhere. The prompt offers
two routes. Measuring the guard surfaced a third that is materially different from
both, so all three are presented.

**Route A — enrol and sign.** Clear B3, add the five secrets, darwin ships signed.
*Cost:* Apple Developer enrolment (money, and a lead time outside anyone here's
control). *Risk:* the signing path's **first** exercise is a real release — zero
observations today, and `macos-sign-and-notarize.sh` is one of exactly two scripts
`make check`'s own banner lists as unrunnable locally. *Leaves unverified:* nothing
structural; it is the only route that ends with macOS users able to run the product.

**Route B — change the guard so a tag behaves as a dispatch does.** Exclude the
unsigned target, warn, publish the rest.
*Cost:* **this changes when a gate fails, which changes what CI guarantees.** The
prompt names this as an owner decision of the same class as the fuzz disposition, and
I am not implementing it. *Risk, and this is the argument against it:* the guard's own
failure text makes the case better than I can — unsigned Mach-O is quarantined by
Gatekeeper, the daemon never starts, and the user sees "daemon not running" with
nothing to search for. **A user who cannot install is better served than one who
installs something that cannot run.** *Leaves unverified:* whether anyone would notice
a silently-reduced platform set, since the loud warning lands in a run log nobody reads
after a green run.
**Route B's marginal value over doing nothing is also smaller than it looks:** a
darwin-excluded artifact set is *already* obtainable today via `workflow_dispatch`,
which excludes darwin and stays green. B buys only the ability to attach that reduced
set to a *tag*.

**Route C — drop `darwin-arm64` from the target set entirely, and say so.** Remove it
from `ALL_TARGETS`/the `binaries` matrix so a `v0.0.2` tag *declares* two platforms and
*ships* two platforms.
*Cost:* macOS users get nothing — same user-visible outcome as B. *But:* it does **not**
change when any gate fails. The guard's invariant ("nothing unsigned reaches a release",
and "quietly shipping fewer platforms than the tag implies is its own fail-open") is
preserved exactly, because the tag no longer implies darwin. *Risk:* a target removed
"temporarily" is a target nobody re-adds; it needs a dated re-entry condition.
*Leaves unverified:* the darwin build path would stop being exercised at all, which
would convert several `OBSERVED` ledger cells back to stale.

**Recommendation: Route A, with Route C as the dated fallback. Reject Route B.**

The reasoning is about sequencing, not preference. B3 is the long pole and it is
*already* live, so A's clock is already running; nothing else on the chain can start
until it finishes. B and C are both reversible and cost nothing to defer, so deciding
between them today buys nothing. If enrolment is refused or slips past the launch date,
C gets the same user-visible outcome as B **without** spending the guarantee that the
guard exists to provide — which makes B strictly dominated. B is the only one of the
three that trades away a CI guarantee, and it does so for an outcome C reaches for free.

**Sequencing note that belongs with Route A:** when the secrets land, the first exercise
of the signing path should be a `workflow_dispatch` on canonical, **not** a tag. A
dispatch with the secrets present runs `macos-sign-and-notarize.sh` for real and takes
the guard's `SIGNED marker present -- releasable` branch, while the attach step stays
inert (`startsWith(github.ref,'refs/tags/v')` is false on a dispatch). That converts the
single riskiest `NEVER RUN` cell in the ledger into an observation at zero release risk.

### 2.4 Rehearsal: planned, then **not run**, on measurement

The plan, written before running anything, was: push a `v0.0.2-rc1` tag to the **fork**
`Rav-i24/Mochiii`, never to canonical, and observe `guard → binaries → package →
checksums → signing guard`.

**It cannot exercise anything, and it was not run.** Three measurements, in the order
that matters:

1. **The fork's Actions are not executing.** Run `34842469587` (2026-09-14, schedule):
   **30 of 30 jobs failed, every one in 2–7 seconds.** That set includes `offline gates`,
   which is pure shell, takes 2.8s, and is green on canonical at the same commits.
   `gh api .../jobs` returns `"steps":[]` for a failed job — **zero steps ever started** —
   and the logs are already expired (`log not found: 103518423445`). A whole-matrix
   failure at 4 seconds with no steps is a **billing signal, not a code signal** (§1.2,
   category 4). The fork's last four runs, 09-10 through 09-14, are all failures of the
   same shape.
2. **The branch guard would reject the tag even if quota returned.** The fork's
   `default_branch` is `main` (M) — so the guard derives the *same* release line name
   there. But the fork's `main` is `4b8c53e`, **203 commits behind** canonical and an
   ancestor of it. A tag on current code would not be an ancestor of the fork's `main`,
   and the guard's arm `tag AHEAD of the release line -> FAILS` would fire. **The
   rehearsal would test the guard's failure path, which the 20-arm self-test already
   covers, and nothing downstream.** Advancing the fork's `main` first is possible, but
   that is a second setup step in service of a rehearsal that still cannot reach the
   interesting part.
3. **The signing path is absent there too.** `gh secret list --repo Rav-i24/Mochiii`
   → exit 0, zero rows. So even a perfect fork rehearsal exercises the *unsigned*
   dispatch branch — which the signing guard's 40-arm self-test already covers directly.

**Conclusion: a fork rehearsal is spend without information, twice over.** The decision
is not to rehearse there. The cheap substitute is in §2.3: one `workflow_dispatch` of
`release.yml` **on canonical**, which is safe by construction (attach is tag-gated;
`publish` is `if: false`) and which the repository has **never once done** — zero
dispatches in `release.yml`'s entire run history.

*Telling a rehearsal artifact from a real one, had it run:* the fork's artifacts carry
no release at all (a dispatch cannot create one), the tag would carry an `-rc` suffix,
and `gh release list --repo Rav-2007/codeterminal-core` — canonical — would still return
zero rows. That last one is the check that actually matters and it is a one-liner.

### 2.5 Gates a release needs — measured, including the ones that already exist

Six candidates. **Two of the prompt's five already exist**, which is itself the result.

**(a) `.vsix` asserts the TUI is out — GATE ALREADY EXISTS. No proposal.**
`clients/vscode/scripts/verify-vsix.js:143` carries
`[/^extension\/daemon\/codeterminal-tui/, 'ships the standalone terminal client, which the extension does not launch']`
in `MUST_NOT_MATCH`, and its self-test's `mustReject` list includes both
`extension/daemon/codeterminal-tui` and `...-tui.exe` — so deleting the rule fails the
self-test rather than silently widening the package. It runs in CI as
`npm run verify:vsix:selftest` (`build.yml`, `vscode extension` job) and the job is
green in **B** and **D**.

**(b) `stage-runtime.js` keys on target, not host — FIXED, with a live residual.**
`clients/vscode/scripts/stage-runtime.js:44` takes the target as `process.argv[2]` and
validates it against the three target names; `release.yml:260` calls
`node scripts/stage-runtime.js "$TARGET"`. **But `clients/vscode/package.json:57`'s
`build:runtime` script still calls it with no argument**, and line 51 then falls back to
`process.platform === 'win32'`. The release path is correct; the local developer path
still asks what the machine is rather than what it is building.
*Proposal:* make the target argument mandatory — delete the fallback. *Coverage:* 1 of
1 remaining host-keyed call site. *False-positive rate:* **0 measured** — the only
caller that omits it is the one that should not. Small, and it needs no exemption at
birth, which is why it is worth doing.

**(c) Version consistency — REAL GAP, and it is currently violated.**
Three version sources, enumerated and then counted (H15):

1. latest `v*` tag → `v0.0.1`
2. `clients/vscode/package.json` → `0.0.1`
3. `daemon/server.go:25` → `const daemonVersion = "0.1.0-skeleton"`

and a fourth fact: **`-ldflags` appears 0 times in `release.yml`** (M). So nothing
stamps a version into any Go binary. `daemonVersion` is not dead code — it is returned
to clients at `daemon/server.go:384` and `daemon/status.go:73` — which means **every
user of a `v0.0.2` release would be told the daemon is `0.1.0-skeleton`**, and three of
three sources would disagree.
*Proposal:* on `refs/tags/v*`, assert `package.json.version` equals the tag minus `v`,
and stamp `daemonVersion` via `-ldflags -X`. *Coverage:* 3 of 3 sources.
*False-positive rate:* **0 on a tag; undefined on a dispatch**, where there is no tag to
compare against — so the gate **needs an exemption at birth** (H14). That exemption is
narrow and principled (no tag ⇒ no assertion possible) rather than a carve-out for a
case someone found annoying, which is the distinction H14 is actually about. Proposed,
with that caveat stated.

**(d) Changelog presence — PROPOSED, THEN REJECTED BY ME.**
No `CHANGELOG` file is tracked (M: `git ls-files | grep -i changelog` → empty). My first
instinct was a gate asserting the tag appears in it. **I withdraw it.**
`release.yml:349` already sets `generate_release_notes: true`, so GitHub composes notes
from the commit range. A changelog gate would mandate a second, hand-maintained copy of
something already generated, and the first release where the two disagree teaches
nobody anything. *Coverage of the rejected gate: 100%. Value: negative.* A gate whose
only effect is to make people maintain a duplicate is a gate people switch off.

**(e) Checksums verified after upload — REAL GAP, ranked LOW.**
`release.yml:292` and `:305` **generate** `SHA256SUMS-tui` and `SHA256SUMS`
(`sha256sum * | tee ...`); nothing re-reads them after the assets are attached.
*Proposal:* after the attach step, re-download and verify. *Coverage:* upload
truncation and corruption. *False-positive rate:* **not zero and not measurable here** —
GitHub asset availability after `softprops/action-gh-release` is eventually consistent,
so the gate needs a retry loop, i.e. **an exemption at birth**, for a failure mode
nobody in this repository has ever observed (the upload step has **zero** observations,
so the base rate is unknown, not low). Proposed, ranked last, and honestly flagged as
the weakest of the six.

**(f) The gate I would actually build first — exercise the release path at all.**
Not in the prompt's list. Everything downstream of `package` has zero observations, and
the cheapest fix is not a new gate but a *scheduled dispatch*: run `release.yml` on
`workflow_dispatch` monthly (or once, now).
*Coverage:* converts `guard`, the post-`package` checksum steps, and the signing guard's
dispatch branch from `NEVER RUN` to observed — **4 ledger cells**.
*What it still cannot exercise:* the attach step (tag-gated), `sign` (no secrets), and
`publish` (`if: false`). *False-positive rate:* **0 expected** — it is the same
`binaries` matrix that is green in **B** and **D** plus a `package` job whose only known
defect, the one that sank **R**, is fixed by `0925a3d` and merged.
*Cost:* three runners, of which one is macOS at 10× billing.
This is the highest information-per-pound action available and it needs no secrets, no
enrolment and no tag.

---

## 3 — The ledger

**Axes derived from the repository, not from the prompt (H10).** `go.work` names six
Go modules — `clients/tui`, `daemon`, `editapply`, `helper`, `protocol`, `proxy` — and
`clients/vscode` is a seventh shipping module with its own `package.json`. **The
prompt's list of seven is correct**; checking was the point. Job names are derived from
all four workflow files: `build.yml` defines 9 jobs (`go`, `lint`, `fuzz`, `cross`,
`macos-tui`, `govulncheck`, `extension`, `proxy-image`, `eval`), `gates.yml` 2,
`release.yml` 4, `retrieval-eval.yml` 1.

7 modules × 3 platforms × 11 layers = **231 cells, none blank.**

**Layer definitions.** *build* = compiles. *vet* = `go vet` (for `clients/vscode`, `tsc`
plus the two bespoke static-analysis scripts). *lint* = `golangci-lint`/`lint.sh`.
*unit* = `go test`. *fuzz* = `go test -fuzz`. *integration* = multi-component or
sustained-load run. *EDH* = a real VS Code Extension Development Host. *manual* = a
human session. *package* = assembled into a shippable artifact. *sign* = codesign +
notarize. *publish* = asset upload or marketplace.

**Reading the cells.** `B`/`D`/`G`/`E`/`R`/`L` are the runs in the table at the top of
this document. Per rule 2, the event is annotated because `OBSERVED` on a push is not
`OBSERVED` on a schedule: **B**, **G**, **E** are *push*; **D** is *dispatch*; **R** is
a *tag*; **L** is *local*. **No cell in this ledger says `OBSERVED` on a `schedule`
event** — see §7.

| Module | Plat | build | vet | lint | unit | fuzz | integration | EDH | manual | package | sign | publish |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| daemon | linux | OBSERVED B(push) | OBSERVED B(push) | OBSERVED B(push) | OBSERVED B(push) | OBSERVED B(push) | OBSERVED D(dispatch) | OBSERVED B(push)¹ | NEVER RUN | OBSERVED-FAIL R(tag) | N/A — `linux-x64` ∈ `NO_SIGNING_NEEDED` | NEVER RUN |
| daemon | darwin | OBSERVED B(push) | OBSERVED B(push) | NEVER RUN | OBSERVED B(push) | NEVER RUN | NEVER RUN | NEVER RUN | NEVER RUN | OBSERVED-FAIL R(tag) | NEVER RUN | NEVER RUN |
| daemon | windows | OBSERVED B(push) | OBSERVED B(push) | NEVER RUN | OBSERVED B(push) | NEVER RUN | NEVER RUN | NEVER RUN | NEVER RUN | OBSERVED-FAIL R(tag) | N/A — `win32-x64` ∈ `NO_SIGNING_NEEDED` | NEVER RUN |
| clients/tui | linux | OBSERVED B(push) | OBSERVED B(push) | OBSERVED B(push) | OBSERVED B(push) | OBSERVED B(push) | OBSERVED B(push)² | N/A — not a VS Code component | **NEVER RUN**³ | OBSERVED-FAIL R(tag) | N/A — `linux-x64` ∈ `NO_SIGNING_NEEDED` | NEVER RUN |
| clients/tui | darwin | OBSERVED B(push)⁴ | OBSERVED B(push)⁴ | NEVER RUN | OBSERVED B(push)⁴ | NEVER RUN | NEVER RUN | N/A — not a VS Code component | NEVER RUN | OBSERVED-FAIL R(tag) | NEVER RUN | NEVER RUN |
| clients/tui | windows | OBSERVED B(push) | OBSERVED B(push) | NEVER RUN | OBSERVED B(push) | NEVER RUN | NEVER RUN | N/A — not a VS Code component | NEVER RUN | OBSERVED-FAIL R(tag) | N/A — `win32-x64` ∈ `NO_SIGNING_NEEDED` | NEVER RUN |
| editapply | linux | OBSERVED B(push) | OBSERVED B(push) | OBSERVED B(push) | OBSERVED B(push) | OBSERVED B(push) | NEVER RUN | N/A — not a VS Code component | NEVER RUN | N/A — library, linked into `daemon`; no standalone artifact | N/A — no artifact to sign | N/A — no artifact to publish |
| editapply | darwin | OBSERVED B(push) | OBSERVED B(push) | NEVER RUN | OBSERVED B(push) | NEVER RUN | NEVER RUN | N/A — not a VS Code component | NEVER RUN | N/A — library, linked into `daemon` | N/A — no artifact to sign | N/A — no artifact to publish |
| editapply | windows | OBSERVED B(push) | OBSERVED B(push) | NEVER RUN | OBSERVED B(push) | NEVER RUN | NEVER RUN | N/A — not a VS Code component | NEVER RUN | N/A — library, linked into `daemon` | N/A — no artifact to sign | N/A — no artifact to publish |
| protocol | linux | OBSERVED B(push) | OBSERVED B(push) | OBSERVED B(push) | OBSERVED B(push) | N/A — module declares 0 `Fuzz*` targets | NEVER RUN | N/A — not a VS Code component | NEVER RUN | N/A — library, linked into `daemon` | N/A — no artifact to sign | N/A — no artifact to publish |
| protocol | darwin | OBSERVED B(push) | OBSERVED B(push) | NEVER RUN | OBSERVED B(push) | N/A — 0 `Fuzz*` targets | NEVER RUN | N/A — not a VS Code component | NEVER RUN | N/A — library, linked into `daemon` | N/A — no artifact to sign | N/A — no artifact to publish |
| protocol | windows | OBSERVED B(push) | OBSERVED B(push) | NEVER RUN | OBSERVED B(push) | N/A — 0 `Fuzz*` targets | NEVER RUN | N/A — not a VS Code component | NEVER RUN | N/A — library, linked into `daemon` | N/A — no artifact to sign | N/A — no artifact to publish |
| proxy | linux | OBSERVED B(push) | OBSERVED B(push) | OBSERVED B(push) | OBSERVED B(push) | OBSERVED B(push)⁵ | NEVER RUN | N/A — not a VS Code component | NEVER RUN | OBSERVED B(push)⁶ | N/A — container image, not a signed binary | N/A — ships by `proxy/Dockerfile` outside `release.yml` |
| proxy | darwin | **NEVER RUN**⁷ | NEVER RUN | NEVER RUN | NEVER RUN | NEVER RUN | NEVER RUN | N/A — not a VS Code component | NEVER RUN | N/A — linux/amd64 container only | N/A — no darwin artifact exists | N/A — no darwin artifact exists |
| proxy | windows | **NEVER RUN**⁷ | NEVER RUN | NEVER RUN | NEVER RUN | NEVER RUN | NEVER RUN | N/A — not a VS Code component | NEVER RUN | N/A — linux/amd64 container only | N/A — no windows artifact exists | N/A — no windows artifact exists |
| helper | linux | OBSERVED B(push) | OBSERVED B(push) | OBSERVED B(push) | OBSERVED B(push) | N/A — 0 `Fuzz*` targets | NEVER RUN | N/A — not a VS Code component | NEVER RUN | OBSERVED-FAIL R(tag) | N/A — `linux-x64` ∈ `NO_SIGNING_NEEDED` | NEVER RUN |
| helper | darwin | **OBSERVED R(tag)**⁸ | **NEVER RUN** | NEVER RUN | **NEVER RUN** | N/A — 0 `Fuzz*` targets | NEVER RUN | N/A — not a VS Code component | NEVER RUN | OBSERVED-FAIL R(tag) | NEVER RUN | NEVER RUN |
| helper | windows | **OBSERVED R(tag)**⁸ | **NEVER RUN** | NEVER RUN | **NEVER RUN** | N/A — 0 `Fuzz*` targets | NEVER RUN | N/A — not a VS Code component | NEVER RUN | OBSERVED-FAIL R(tag) | N/A — `win32-x64` ∈ `NO_SIGNING_NEEDED` | NEVER RUN |
| clients/vscode | linux | OBSERVED B(push) | OBSERVED B(push)⁹ | N/A — no linter configured; `devDependencies` contains no ESLint | OBSERVED B(push) | N/A — no fuzz harness for TypeScript | OBSERVED B(push)¹⁰ | OBSERVED B(push) | NEVER RUN | OBSERVED-FAIL R(tag) | N/A — `linux-x64` ∈ `NO_SIGNING_NEEDED` | NEVER RUN |
| clients/vscode | darwin | N/A — the darwin `.vsix` is assembled on a linux runner; `vsce --target` only labels | N/A — same linux assembly | N/A — no linter configured | NEVER RUN | N/A — no fuzz harness for TypeScript | NEVER RUN | **NEVER RUN** | NEVER RUN | OBSERVED-FAIL R(tag) | NEVER RUN | NEVER RUN |
| clients/vscode | windows | N/A — assembled on a linux runner; `vsce --target` only labels | N/A — same linux assembly | N/A — no linter configured | NEVER RUN | N/A — no fuzz harness for TypeScript | NEVER RUN | **NEVER RUN** | NEVER RUN | OBSERVED-FAIL R(tag) | N/A — `win32-x64` ∈ `NO_SIGNING_NEEDED` | NEVER RUN |

**Footnotes.**
¹ Indirect: the `vscode extension` job's EDH harness launches the real daemon binary.
² `TestTwoThousandTurnSoakStaysWithinItsBounds`, full 2000 turns unraced, `build.yml:316`, `if: matrix.module == 'clients/tui'`.
³ `docs/MANUAL_SESSION_2026-09-04.md`. No human has ever performed it.
⁴ Via `cross (macos-latest, clients/tui)`. The job *named* `macos (clients/tui, every push)` is **SKIPPED** here — condition `github.ref != 'refs/heads/main'` (`build.yml:613`) — see premise 6.
⁵ 7 targets, the most of any module.
⁶ `proxy-image` job: `docker build`. Built, **never pushed by CI**.
⁷ `proxy` is **absent from the `cross` matrix**, which is `module: [daemon, protocol, editapply, clients/tui]` (`build.yml:496`).
⁸ Built by `release.yml`'s `binaries` job, which succeeded on all three targets in **R**. This is the **only** darwin/windows observation `helper` has ever had.

⁹ `npm run check:webview` and `npm run check:installpath`. ¹⁰ The EDH run *is* the integration layer for this module.

### 3.1 Cells by state — listed, not counted (H15)

**`OBSERVED-FAIL` (5 cells), all the same run, all the same job.** `package` for
daemon/linux, daemon/darwin, daemon/windows — and by the same failure, `clients/tui`
×3, `helper` ×3 and `clients/vscode` ×3 share it. Stated precisely: the `package` cell
is `OBSERVED-FAIL R(tag)` for **every** module that has a `package` cell at all
(`daemon`, `clients/tui`, `helper`, `clients/vscode` — 12 cells across 3 platforms,
minus proxy/linux which is `OBSERVED B`). One job failing sank all of them.

**`NEVER RUN` — the full enumeration**, grouped by what would settle each:

*Group 1 — needs a darwin or windows runner running more than `cross` does (26 cells).*
`lint` on darwin and windows for all six Go modules (12); `fuzz` on darwin and windows
for daemon, clients/tui, editapply, proxy (8); `vet` and `unit` on darwin and windows
for `helper` (4) and for `proxy` (—counted in group 2); `integration` on darwin/windows
for daemon and clients/tui (4, minus overlap). *What it would take:* adding `os` to the
`lint`/`fuzz` matrices, or adding `helper` and `proxy` to the `cross` matrix. *Cost:*
macOS bills at 10×, which is why `cross` is narrow. *Who:* repository owner.

*Group 2 — `proxy` off linux (10 cells).* build, vet, lint, unit, fuzz, integration on
darwin and windows. *What it would take:* adding `proxy` to the `cross` matrix.
*Why it may not be worth it:* the proxy ships only as a linux container, so no user
runs it on darwin. *Why it might be:* a contributor on macOS cannot today be sure
`make check`'s proxy arm matches CI's. *Who:* repository owner.

*Group 3 — `helper` off linux (8 cells).* `vet`, `lint`, `unit` on darwin and windows,
plus `integration` on both. **This is the sharpest gap in the ledger.** `helper` is the
only CGO module (`CGO_ENABLED=1`, onnxruntime `dlopen`'d at runtime), it is the most
platform-sensitive component in the repository, and **its tests have never executed on
any platform but linux.** Its only off-linux observation is a *build* inside the one
release run. *What it would take:* adding `helper` to the `cross` matrix — one line.
*Who:* repository owner. **Recommended.**

*Group 4 — the release path's tail (13 cells).* `sign` on darwin ×4 shipping modules;
`publish` on all three platforms for daemon, clients/tui, helper, clients/vscode (12,
less overlap). *What it would take:* §2.3 Route A (sign) and a first successful release
(publish). *Who:* repository owner, gated on B3.

*Group 5 — EDH off linux (2 cells)* and *manual (21 cells).* `clients/vscode` EDH on
darwin and windows: the extension has never been loaded in VS Code on either. *manual*
is `NEVER RUN` for **every module on every platform** — 21 of the 231 cells — because
`docs/MANUAL_SESSION_2026-09-04.md` has never been performed by anyone. **An agent
cannot do this one.** See §8, row H-1.

**`SKIPPED` — and it is a different state from `NEVER RUN` (M5 discipline).** Exactly
two jobs skip by their own conditions, and neither leaves a `NEVER RUN` cell, because
another job covers the same ground:
- `macos (clients/tui, every push)` — `if: ... github.ref != 'refs/heads/main' && github.event_name != 'workflow_dispatch' && ...`. On **B** and **D** it skipped. The cell is nonetheless `OBSERVED` because `cross (macos-latest, clients/tui)` covers it.
- `retrieval eval (scheduled)` — `if: github.event_name == 'schedule' || github.event_name == 'workflow_dispatch'`. **SKIPPED on B(push); OBSERVED on D(dispatch).**

### 3.2 Cross-check against C4b's §13 companion table (rule 4)

Checked against the companion table at `docs/C4b_EVIDENCE_INTEGRITY_2026-09-15.md:43`,
**not** against §13 itself, which that document establishes is unusable alone: *"Of 27
rows, three are FALSE and one is partly false"*, and *"§13 also miscounts itself. Its
prose says the section grew 8 → 10 → 12 → 16 → 19 → 26 → 31"* while **27 rows are
present**. This ledger takes no row from §13 directly. The companion table's warning
that planning work from §13 *"would be sent to verify macOS peer auth, Windows SID
comparison, [and other work] already done"* is consistent with what this ledger found
independently: the darwin/windows gaps are in `helper`, `proxy` and `lint`/`fuzz`
breadth — **not** in peer auth or SID comparison, which are covered by
`cross (macos-latest, protocol)` and `cross (windows-latest, protocol)`, both green in
**B**.

---

## 4 — Premises that did not hold (H4)

The prompt asserts a great deal. Twelve claims were tested; **eight held, four did
not, and three more were stale or misattributed.** The falsifications are the result,
not a complaint about the brief.

**P1 — "The canonical remote is `Rav-2007/codeterminal-core`; after the rename it is
`origin`, and `fork` is `Rav-i24/Mochiii`." — FALSE, and inverted.** Measured:
`origin` → `git@github.com:Rav-i24/Mochiii.git` (the **fork**); `upstream` →
`https://github.com/Rav-2007/codeterminal-core.git` (**canonical**). The rename has not
happened. `reach.sh` independently confirms it, carrying two allowlist entries whose
stated retiring event is *"re-pointing this branch at the canonical remote with
`git branch -u` **after the rename**"*. **The brief's own instruction to pass `--repo`
explicitly anyway is what made this harmless** — a habit that does not depend on
configuration, exactly as it says.

**P2 — "HEAD, branch, canonical `main` and local `main` all at `9f8f4f0`." — FALSE
(stale).** At session start all four were at `2a6ef91`; by the end, at `0ee8c19`. The
*relationship* the premise asserts held throughout and still holds.

**P3 — Implied clean tree. — FALSE at session start.** `docs/C6_CLOSING_2026-09-17.md`
carried 85 uncommitted lines. Committed and pushed mid-session by the owner as
`0ee8c19`; not by me.

**P4 — "`reach.sh` exit 0, 255 items." — exit 0 held; the count is 256.**

**P5 — "the most recent `build` and `gates` on `main` green at that SHA." —
UNSATISFIABLE as written.** `build.yml` carries `paths-ignore: ["**.md"]`, so when
`main`'s tip is a markdown-only commit — which it is, twice over — **no `build` run can
exist at that SHA**. The criterion and the workflow contradict each other. Resolved in
§0.3 by proving the untested delta contains zero non-markdown files.

**P6 — "Two jobs skipped by their own conditions and correctly so **for a push
event**: the every-push macOS job and the schedule-only eval job." — the fact holds,
the stated reason is wrong for one of them.** The eval job skipped on the push because
of its event condition: correct. But `macos (clients/tui, every push)` skipped because
`github.ref != 'refs/heads/main'` evaluated **false** — it was on `main`. It has
nothing to do with the event being a push, and **the same job will skip on Monday's
schedule too**, for the same reason. A job named "every push" that never runs on the
main branch is the same class of false banner as `gate-parity.sh:132`, and it is in the
job's own name rather than in a comment.

**P7 — "the `docs-coderefs` vacuity hole where 24 documents carry the enforcement
marker and 19 are enforced." — half right; **20**, not 19.** Measured:
`grep -rl '<!-- coderefs: enforced -->' --include='*.md'` → **24 documents**; the gate's
banner reports **20 enforced**. The gap is 4, and **the four are now named**:
`docs/C5b_TOPOLOGY_2026-09-15.md`, `docs/C5_METHOD_2026-09-15.md`,
`docs/C7b_DELIVERY_2026-09-16.md`, `docs/CHECKPOINT_ADDENDUM_2026-09-16.md`. Each
carries the marker and contains **zero** citations matching the gate's pattern, so
`scripts/docs-coderefs.sh:94`'s `[ -n "$refs" ] || continue` skips them **before** the
marker test at line 95 — they are counted as neither enforced nor unenforced. 24 − 4 = 20,
which reconciles exactly with the banner.

**P8 — "the one-hour manual session in `docs/MANUAL_SESSION_2026-09-04.md`." — the
document says forty minutes, twice** (line 4: *"about forty minutes"*; line 268:
*"Forty minutes and a paragraph is the whole ask"*). Matters only because §8 quotes a
time estimate to a person.

**P9 — "a prior pass drove absolute build paths in binaries from 678 to 0." — the 0
holds; the control number is now 1036.** Not a contradiction: the tree grew.

**P10 — "The 23,652 `node_modules` files." — measured 13,458.** Install-state
dependent, so this is weak evidence either way; recorded because it was quoted as fact.

**P11 — MATERIAL OMISSION rather than a false claim.** The prelude presents Monday as
a formality. It does not mention that **`build` on `schedule` has failed four
consecutive times** (2026-08-24, 08-31, 09-07, 09-14). §1.1 establishes those are stale
— but a reader of the prelude alone would not know they existed.

**Premises that HELD, verified rather than assumed:** `gh secret list` → exit 0, zero
rows; the `marketplace` environment 404s; `release.yml` has run exactly once
(`31505527398`, v0.0.1, 2026-08-11, failed in `package`); everything downstream of
`package` is at zero observations; the branch guard has landed with **exactly 20**
self-test arms and `needs: guard` on `binaries`, so the ordering constraint is
satisfied; the release line is derived from
`github.event.repository.default_branch`, not hardcoded; the signing guard's
tag-fails/dispatch-excludes asymmetry is real and load-bearing; two release paths with
the proxy shipping by Dockerfile; **zero production npm dependencies**
(`npm audit --omit=dev` → *found 0 vulnerabilities*; `package.json` has no
`dependencies` key at all) with **6 dev findings, 4 high** — matching the prelude
exactly.

---

## 5 — Self-corrections (H3)

Four, of which three were caught before they reached a conclusion.

**5.1 — I read a correct skip as a coverage gap.** On seeing no `build` run at HEAD's
SHA I began writing it up as a hole in the CLEAN criterion. It is `paths-ignore:
["**.md"]` working exactly as designed. Corrected by measuring the delta
(`git diff --name-only 9f8f4f0..0ee8c19 | grep -cv '\.md$'` → 0) before the verdict was
written. The residue is P5, which is a defect in the *criterion*, not the repository.

**5.2 — I treated four red scheduled runs as predictive of Monday.** My first reading
was that Monday is likely to fail. That was wrong, and the thing that refuted it was
looking at *which commit* the runs were on — all four at `efc611d`, a month-stale tip —
and then finding **D**, where the eval job is green at a current commit. Stated
plainly: **the red streak is real and the alarming inference from it is not.**

**5.3 — I suspected a `globstar` hole in `docs-coderefs` and it is not live.** The
script sets `shopt -s nullglob` but **not** `globstar` (line 71), so its
`for doc in docs/*.md docs/**/*.md *.md` degrades to one directory level. I expected
unchecked documents. Measured: of 65 tracked markdown files under `docs/`, **64 sit at
one level and exactly 1 at two**, which `docs/*/*.md` covers. **Not a finding today** —
recorded as latent, because the first `docs/a/b/c.md` anyone adds will be skipped
silently.

**5.4 — I had the fuzz target count wrong by one and would have reported an
unresolved discrepancy.** Reading run `35179379541`'s log I counted 17 targets against
the job's own banner of *"18 of 18 target(s) ran"*, and was about to record the
mismatch as unexplained. It was my `tail -30` truncating the list. Counted from source
instead: daemon 4, editapply 4, proxy **7**, clients/tui 3, protocol 0, helper 0 =
**18**. The banner was right; my instrument was clipped. `FuzzTopLevelFields` was the
one I lost.

*Rate note.* The brief states a running false-positive rate on findings in this project
of about 1 in 6. Of the substantive findings in this report, four were withdrawn or
corrected before publication (§5.1–§5.4) and one proposal was rejected by me after I
wrote it (§2.5(d), the changelog gate). That is broadly consistent with the stated rate
and is recorded so the next pass can keep the count honest.

---

## 6 — What I did not verify

Built from the ledger's `NEVER RUN` and `SKIPPED` cells, plus everything this session
asserted on reading rather than on measurement. **This section is longer than the plan
in §2 deliberately**: the plan is short because the evidence base under it is thin, and
a reader who takes §2 without §6 will over-trust it.

### 6.1 The entire release path past `package` — 4 stages, 0 observations

**Checksum generation after a successful package.** `release.yml:292` and `:305` run
`sha256sum`. In **R** the `package` job failed, and I did not establish *at which step*.
The checksum steps sit after `Package all three targets` and `Stage the terminal
client`; if the failure was earlier, these lines have **never executed**. *What would
settle it:* `gh run view 31505527398 --log-failed` filtered to the `package` job — I
read the job's conclusion but not its step-level breakdown. *One command.*

**The signing guard in a real run.** 40 self-test arms pass locally. The guard has
**never been invoked by `release.yml`** — its step is inside `package`, downstream of
where **R** died. Every claim in §2.2's condition table is derived from **reading
`run_guard`**, not from watching it execute. *What would settle it:* one
`workflow_dispatch` of `release.yml` on canonical. *Not run; see §2.4.*

**The branch guard in a real run.** Zero invocations, on any event, ever — **R**
predates the job. Its 20 arms are a self-test of the script, not of the workflow wiring
that feeds it `github.ref`, `github.sha` and
`github.event.repository.default_branch`. **A guard first exercised during a release is
a guard nobody has tested** — the workflow's own comment says so, about a different
guard. *What would settle it:* the same single dispatch.

**`softprops/action-gh-release` attaching anything.** Zero observations. Whether the
four globs (`out-vsix/*.vsix`, `out-vsix/SHA256SUMS`, `out-bin/codeterminal-tui-*`,
`out-bin/SHA256SUMS-tui`) match the files actually on disk at that point is **read, not
measured**. A glob that matches nothing is not an error for that action.

**`draft: true` and `generate_release_notes: true`.** Unobserved. Whether the draft
lands with usable notes is unknown.

**The `publish` job.** `if: false`. It has never run and its body is a single `echo` —
*"vsce publish runs here, from the artifacts package/ produced."* **There is no
marketplace publication code in this repository at all.** Route A's final step does not
exist yet. This is the single largest thing §2's chain diagram understates: the last
link is a comment, not an implementation.

### 6.2 macOS signing and notarisation — the deepest unknown

`scripts/macos-sign-and-notarize.sh` is one of exactly two scripts `make check`'s own
banner names as unrunnable locally. I did not read it line by line and did not test it.
Unverified: whether it writes `SIGNED-darwin-arm64` where the guard looks for it
(`$artifacts/binaries-$target/SIGNED-$target`); whether it handles a `.p12` supplied raw
versus base64, as the guard's help text says it must; whether notarisation stapling
succeeds; what it does when Apple's notary service is slow or down. **The signing
guard's self-test explicitly excludes this** — its own output says the signed path and
the notarised path *"need a macOS runner and real Apple credentials"* and that only the
guard's *handling of a marker written by the test* is covered. So the 40 green arms say
nothing about signing itself.

### 6.3 `helper` off linux — the sharpest ledger gap

`helper` is the only `CGO_ENABLED=1` module; onnxruntime is `dlopen`'d at runtime via
`ort.SetSharedLibraryPath`. It is **absent from the `cross` matrix**. Therefore:
`go vet`, `go test`, `lint` and any integration exercise of the embedder helper have
**never run on darwin or windows**. Its only off-linux observation is that it *compiled*
inside **R**. Unverified: whether the ONNX runtime loads at all on darwin-arm64;
whether the helper's subprocess protocol behaves the same on windows; whether the
embedding output is numerically identical across platforms (there is a
`TestEmbeddingIsDeterministic` and a `TestEmbeddingVariesWithThreadCount`, and **both
run only on linux**). Given that retrieval quality is the product's core claim, an
embedder that silently differs on macOS is a plausible and completely unmonitored
failure. *What would settle it:* add `helper` to the `cross` matrix — one line —
accepting the macOS 10× billing.

### 6.4 `proxy` off linux

Absent from `cross`. Never built, vetted, linted, tested or fuzzed on darwin or
windows. For the *shipped* artifact this is defensible — it ships as a linux container
— and I marked the packaging cells `N/A` accordingly. What I did **not** verify is the
contributor consequence: whether `make check` even completes on a macOS developer
machine, given it builds all six modules. Nobody has reported otherwise, which is not
evidence.

### 6.5 `lint` and `fuzz` breadth

Both jobs are `runs-on: ${{ vars.LINUX_RUNNER || 'ubuntu-latest' }}` with a module-only
matrix — **no OS axis exists**. So every lint rule and all 18 fuzz targets are linux
observations only. Unverified: whether any lint finding is platform-conditional
(build-tagged files for `_darwin.go` / `_windows.go` are linted **only** by their
compile in `cross`, not by `golangci-lint`); whether the fuzz corpora would find
different inputs under a different allocator or scheduler.

### 6.6 The three-plus-one known-flaky surfaces — carried, not re-measured

- **fuzz ~27% failure floor across three targets (C).** Not re-measured. Observed once
  red this session (`35179379541`, `FuzzStreamRequested`, `context deadline exceeded`)
  and green in **B** and **D**. That is 1 in 3, on a sample of 3, which distinguishes
  nothing.
- **`TestRepaintCostAtTheTranscriptCeiling`, idle p99 61.737 ms against 64 ms (C).**
  1.04× headroom. **Not re-measured, and `make check` ran it** — I did not extract its
  p50/p99 from the log. *What would settle it:* re-run that single test alone and read
  the percentiles. **H11 applies: it must run alone**, which is why I did not fold it
  into an already-running batch.
- **`TestTokenEfficiencyEval`, 30-minute alarm at 11m43s (C → M, closed 2026-09-20).**
  The workflow at HEAD now passes `-timeout 60m`; the version that ran in the stale
  scheduled failures passed `-timeout 30m`. I verified the *timeout change* by reading
  both command lines. I did **not** verify the test's current runtime — and it was
  already verified, in a document this section did not consult.
  `docs/C6_CLOSING_2026-09-17.md:503` records run `35183141505`'s
  `retrieval eval (scheduled)` as **1624 s, success, "including
  `TestTokenEfficiencyEval`"**. Its log gives the per-test figure: **1.03 s.** The
  11m43s belongs to the `defaultContextBudgetChars` 32000 era that `build.yml`'s own
  step comment documents, not to this tree. **This is no longer a flaky surface and
  should not be carried as one.** See `docs/A_DELIVER_2026-09-20.md` §A7.2.
- **`TestSQLiteCancellation_DoesNotReachAnFTS5PhraseMatch` (M, new).** 1 failure in 3
  macOS observations. **No rate; the sample is too small to have one.** I am labelling
  it flaky on 1/3, which is weak, and saying so.

### 6.7 Monday 2026-09-21 06:00 UTC — what is and is not established

**Established (M):** the eval job — the only job that runs on `schedule` but not on
`push` — is **green at `7454202`** under a `workflow_dispatch`, which satisfies the same
`if:`. The four historical red scheduled runs are at a month-stale commit whose specific
failure causes are fixed at HEAD.

**NOT established, and this is the point:** *dispatch green ≠ scheduled green ≠ push
green*, which is the brief's own rule and I am not going to launder it. Specifically
unverified —
- **No cell in the ledger reads `OBSERVED` on a `schedule` event.** Not one.
- A `schedule` run uses the workflow file **from the default branch**, which is now
  current; the four stale runs used `efc611d`'s. That change is **read, not observed**.
- `fuzz` is the live risk, not the eval: it is in the scheduled run, it has a ~27%
  carried failure floor, and its most recent red (`35179379541`) was **four commits
  before HEAD**. Whether `6d1339a` makes a coordinator timeout non-fatal — and whether
  that is even desirable, since it changes when a gate fails — I did not test.
- `cross (macos-latest, daemon)` is in the scheduled run and carries §1.2's flake.
- `macos (clients/tui, every push)` **will skip on Monday** (`github.ref` *is*
  `refs/heads/main`), exactly as it skipped on **B**. If anyone reads Monday's run
  expecting that job to have run because of its name, they will be misled — P6.

### 6.8 The fork, and the rehearsal that did not happen

I concluded billing from **shape** — 30/30 jobs dead in 2–7 s, `"steps":[]`, pure-shell
jobs failing identically to Go jobs — and from a prior record. I did **not** see a
billing message: `gh run view --log-failed` returned `log not found: 103518423445`
(expired) and I did not check the fork's billing page or org settings. *What would
settle it:* the Actions billing page for `Rav-i24`, or one fresh push to the fork.
Consequently: **whether the fork could rehearse if billing were restored is unverified**
— though §2.4's second and third reasons (the guard would reject the tag; no secrets)
stand independently of billing and are measured.

### 6.9 Claims I took from reading, not measurement

- The `package` job's fix. **`0925a3d` is asserted to fix what sank R. I did not read
  the commit, diff it, or test it.** It is the load-bearing assumption under §2.5(f)'s
  "false-positive rate: 0 expected". *What would settle it:* `git show 0925a3d`, then
  the dispatch.
- `-trimpath` is enforced by `scripts/supply-chain.sh` "if any main package produces a
  binary carrying one". `make check`'s `supplychain` target passed, so this ran — but I
  did not neuter it to confirm it can fail.
- BACKLOG blocker **B3 (Apple Developer enrolment) is LIVE**: carried entirely from the
  brief. I did not open `BACKLOG.md` to confirm B3's state or wording.
- The proxy's production deployment (Railway) is outside `release.yml` and outside this
  ledger. Whether the deployed image matches `main` is **not checked here**.
- `verify-vsix.js` checks a **Go toolchain floor** in the shipped binary (its self-test
  has an arm for "a below-floor binary" and one for "a binary with no version string at
  all"). I read this; I did not exercise it against a real `.vsix`.

### 6.10 Things the ledger marks `N/A` that deserve a second opinion

Each `N/A` is a judgement, and three are arguable:
- **`sign` for `win32-x64` = N/A.** True *today* only because `win32-x64` sits in
  `NO_SIGNING_NEEDED`, with the script's own note that it *"moves left when Authenticode
  lands"*. Unsigned Windows binaries trigger SmartScreen. This is a **deferred
  decision recorded as a non-applicability**, which is the weakest cell type in the
  table.
- **`publish` for `proxy` = N/A ("ships by Dockerfile outside `release.yml`").** Correct
  about `release.yml`. It does mean the proxy's publication path is **entirely outside
  every gate in this ledger**.
- **`clients/vscode` build on darwin/windows = N/A ("assembled on a linux runner").**
  Correct about assembly. It hides that the *extension* has never been loaded on those
  platforms — which is why EDH there is `NEVER RUN` rather than `N/A`.

### 6.11 Two silent-skip switches — checked, and clear

`build.yml` gates four jobs on a repository variable:
`vars.HOSTED_RUNNERS_DISABLED != 'true'` appears at lines 480 (`cross`), 612
(`macos-tui`), 687 (`extension`) and 759 (`proxy-image`), and
`vars.LINUX_RUNNER || 'ubuntu-latest'` selects the runner for six more. **If
`HOSTED_RUNNERS_DISABLED` were ever set to `true`, four jobs would vanish from every
run and the run would still be green** — the exact shape of failure this repository has
corrected elsewhere.

I did not want to assume it was unset, so I measured it:
`gh api repos/Rav-2007/codeterminal-core/actions/variables` →
`{"variables":[],"total_count":0}`. **Both variables are unset**, so `cross`,
`macos-tui`, `extension` and `proxy-image` are genuinely running and every
`runs-on` resolves to `ubuntu-latest`. This is the one item in §6 that moved from
*unverified* to *verified-clear* while being written, and it is recorded here rather
than promoted into the plan because its value is that it is **not** a finding.

**What remains unverified about them:** nothing tests that they are unset. A gate
asserting "no variable disables a job" does not exist, and if one were added it would
have to run somewhere the variable is visible — which is CI, the place the variable
would already have taken effect. I do not have a good proposal for this and am saying
so rather than inventing a weak one.

### 6.12 Nothing prevents a red commit from reaching the release line

`gh api repos/Rav-2007/codeterminal-core/branches/main/protection` → **404**. There is
**no branch protection on `main`**, so there are no required status checks and CI cannot
block anything. This is consistent with `build.yml`'s own comment — *"branch protection
is unavailable on a private free-plan repo"* — and it has a consequence the release plan
depends on: **the release line is `main`, the branch guard asserts a tag is an ancestor
of `main`, and `main` itself is unguarded.** The guard proves provenance, not quality.
Its own output says so: *"NOT checked: whether the tagged code is correct, whether CI
went green on it, or whether anyone approved this release."*

Unverified: whether the repository going public or onto Pro would change this, and
whether anyone would notice if it did. `build.yml:33` flags the same thing and asks for
a re-check under exactly those conditions.

### 6.13 Gate coverage I relied on without inspecting

`make check` passed 14 sub-targets. I read the banner of four of them and the
implementation of two. The rest I took at their exit code:

- **`ratchet` and `errcheck`.** Coverage floors and an errcheck ceiling exist per module
  (`efc611d`'s own subject line mentions *"lower daemon errcheck ceiling to 95"*). **I
  never read a single threshold.** The brief forbids raising a coverage floor,
  `helper`'s least of all — I did not raise one, and I also did not check what any of
  them currently are. A floor set below current coverage is a floor that ratchets
  nothing, and I cannot say whether that is the case for any module.
- **`supplychain`.** It is asserted to fail if any main package produces a binary
  carrying an absolute path. It passed. **I did not neuter it**, so I cannot say it
  would fail — which is awkward, because §2.1's `-trimpath` measurement is exactly the
  property it guards, and I *did* build a control arm for my own probe and not for the
  repository's.
- **`hookcheck`, `fmt`, `crossvet`, `race`, `evalguard`, `fuzzguard`, `webview`,
  `debtmarkers`.** Exit codes only.
- **`govulncheck`.** Green for all six modules in **B**. I did not check whether it
  scans `-tags eval` code, and there is a recorded precedent in this project that
  tag-gated code escapes an untagged tool. Unverified.
- **`corpus holds no answer key`** (`gates.yml:133`). Green in **G**. I never opened it.
  Given that a stale corpus containing a transcript of its own run is precisely what
  broke the eval for weeks, this job is load-bearing and I inspected none of it.
- **`docs-claims`.** It verifies four registers *agree with each other* and with
  `docs/DECISION_PACK.md`. **Agreement is not truth** — four registers can agree and all
  be wrong. The gate does not claim otherwise; I am recording that I did not check the
  underlying facts of any of the 29 items or 8 decisions.
- **`gate-parity`.** Reports *"24 script(s) accounted for -- 14 both, 2 local-only, 2
  CI-only, 6 manual"*. I did not verify the manifest matches reality, and the brief
  already names `gate-parity.sh:132`'s false *"macOS only on main"* claim as open. A
  manifest with a known-false line in it is not a manifest I should be citing as
  evidence, and §2's inventory does not rely on it.

### 6.14 `reach.sh`'s twelve exemptions, against H16

`reach.sh` exits 0 with 12 detected exemptions, each printing a retiring event, plus 2
allowlist entries matching nothing. H16 says a trigger must name an event **observable
by the thing that fires it**. I read all twelve; I verified none.

Ten name a ref operation — *"re-pointing this branch at the canonical remote with `git
branch -u`"*, *"deletion of this branch"* — which `reach.sh` can observe directly, so
those look sound. Two are weaker: `ca96966` retires on *"this commit becoming reachable
from the canonical main, or deletion of the branch that holds it"*, which is observable;
but the C6d section committed as `0ee8c19` asserts `ca96966` is **already** redundant
because `scripts/tool-pins.txt` serves its purpose at `main`. **If that is right, the
exemption should already have been retired and has not been.** I did not check
`tool-pins.txt`, did not diff `ca96966`, and am not asserting the conclusion — only that
a claim in the repository and an active exemption in the repository disagree, and one
of them is stale.

The 2 entries that matched nothing are the more interesting half: `reach.sh` says
plainly that such an entry *"was written ahead of an event that has not happened yet"*.
One of them names the rename (H-11), which is consistent. The other names
`docs/readme-rewrite`. Unverified.

### 6.15 Claims about the `package` job's fix, restated as the risk they are

§2.5(f) rates a dispatch's false-positive probability at "0 expected" on the strength of
`0925a3d` fixing what sank **R**. **That entire rating rests on a commit I did not
read.** If `0925a3d` does not fix it, or fixes only one of several causes, the dispatch
comes back red and the correct response is to treat that as **new information about the
release path**, not as a broken recommendation. I would rather state the dependency than
soften the recommendation: the dispatch is worth running *even if it fails*, because a
red `package` job with a current log is strictly more than the zero observations that
exist today.

Similarly unverified: that the `package` failure in **R** was a single cause at all. I
read the job's conclusion, not its steps (§6.1).

### 6.16 The concurrency claim in H-3

H-3 tells the owner that a `release.yml` dispatch will queue rather than cancel, citing
`concurrency: group: release-${{ github.ref }}` with `cancel-in-progress: false`
(`release.yml:15-17`). That is read correctly for `release.yml` **against itself**. The
brief's warning is about a *different* interaction — that dispatch shares a concurrency
group with push and cancels in-flight runs — which is true of `build.yml` and
`gates.yml`, whose groups are `${{ github.workflow }}-${{ github.ref }}` with
`cancel-in-progress: true`. `release.yml` uses a distinct group name, so the two should
not collide. **I did not test this**, and the cost of being wrong is cancelling
somebody's in-flight run, so H-3 keeps the "check nothing is in flight first"
instruction regardless of what I read.

### 6.17 Not attempted at all

The one-hour — **forty-minute** — manual session (§8, H-1). Any runtime exercise of the
product: I did not start the daemon, the TUI or the extension. Any assessment of whether
the *product* is good. The `2000-turn soak` at full length locally. Windows execution of
anything. macOS execution of anything. `docs/MANUAL_SESSION_2026-09-04.md`'s own worked
example — `refreshViewport`'s unconditional `GotoBottom` — is the standing proof that
this class of defect survives a green 327-test suite, and **nothing in this report
addresses that class.**

---

## 7 — Handover

**Named recipients.** This repository has one identified human: the **repository
owner** (git author `anonymous`, the account behind `Rav-2007` and `Rav-i24`). Every
row below is theirs unless it says otherwise. Where a row needs *a different kind of
person* — someone who has not read this code — it says so, because assigning it to the
owner would make the list read complete and tell nobody anything.

| # | Row | Trigger / what it would take | Recipient |
|---|---|---|---|
| **H-1** | **The manual session — `docs/MANUAL_SESSION_2026-09-04.md`.** Never performed by anyone. 21 of 231 ledger cells. | **Forty minutes** and one paragraph, from **a person who has not read this code**. *An agent cannot do this.* The document asks for *"this felt off"*, not a checklist. | **Unassigned — deliberately.** Assigning it to the owner defeats it: they already know how it is supposed to behave. |
| **H-2** | **Monday 2026-09-21 06:00 UTC — the scheduled run.** Exit criterion 1. **Nothing in this document closes it.** | Read `gh run list --repo Rav-2007/codeterminal-core --branch main --limit 3` after 06:00 UTC. Expect `build` + `schedule` + `success`. Live risks, in order: `fuzz` (~27% floor), `cross (macos-latest, daemon)` (§1.2). Note `macos (clients/tui, every push)` **will skip** — that is correct, not a failure. | Owner |
| **H-3** | **One `workflow_dispatch` of `release.yml` on canonical.** Zero dispatches in its entire history. Highest information-per-pound action available. | `gh workflow run release.yml --repo Rav-2007/codeterminal-core --ref main` — **check nothing is in flight first** (release has its own concurrency group `release-${{ github.ref }}` with `cancel-in-progress: false`, so it will queue rather than cancel, but build/gates share the machine budget). Safe by construction: attach is tag-gated, `publish` is `if: false`. Converts 4 `NEVER RUN` cells. ~3 runners, one at macOS 10×. | Owner. **I did not run it** — it is outward-facing and costs billable minutes. |
| **H-4** | **`TestSQLiteCancellation_DoesNotReachAnFTS5PhraseMatch` is macOS-flaky.** New this pass; not in the known-flaky list of three. 1 failure in 3 observations. | Either widen the assertion's timing bar or accept it as a fourth known-flaky surface and record a rate. **Do not "correct it until it passes"** — it is a vacuity floor and a test corrected until it passes everywhere tests nothing. | Owner |
| **H-5** | **Version consistency is violated right now.** Tag `v0.0.1`, `package.json` `0.0.1`, `daemon/server.go:25` `"0.1.0-skeleton"`, and **zero `-ldflags`**. A `v0.0.2` would tell every user the daemon is `0.1.0-skeleton`. | Stamp via `-ldflags -X` and gate `package.json` against the tag. §2.5(c). Needs one narrow exemption at birth (no tag on a dispatch). | Owner |
| **H-6** | **Route A vs C — the release decision.** Presented, not taken. Route B recommended **REJECT**. | §2.3. B3 is the long pole and already live. Decide C's fallback date now, while it is cheap. | Owner — **founder's call**, explicitly |
| **H-7** | **`helper` has never been tested off linux.** Only CGO module; ONNX runtime `dlopen`'d. 8 `NEVER RUN` cells. | Add `helper` to the `cross` matrix at `.github/workflows/build.yml:496` — one line. Accepts macOS 10× billing. **Recommended.** | Owner |
| **H-8** | **`proxy` has never been built off linux.** 10 `NEVER RUN` cells. | Add `proxy` to `cross`, **or** decide explicitly that linux-only is the contract and write that down. Either closes it; leaving it silent does not. | Owner |
| **H-9** | **`lint` and `fuzz` have no OS axis.** All 18 fuzz targets and every lint rule are linux-only observations. | Judgement call on cost. Platform-tagged files (`_darwin.go`, `_windows.go`) are today linted only by compiling in `cross`. | Owner |
| **H-10** | **`docs-coderefs`: 4 documents carry the marker and are inspected for nothing.** Named in §4/P7. | `scripts/docs-coderefs.sh:94` skips on empty refs *before* testing the marker at line 95. Either drop the marker from those four, or count a marker-carrying zero-citation document as an explicit third state. **Also latent:** `globstar` is unset, so `docs/**/*.md` covers one level only (§5.3) — fine today at 64-vs-1, silent the day it is not. | Owner |
| **H-11** | **The remote rename has not happened.** `origin` is the fork; `upstream` is canonical. Two `reach.sh` allowlist entries name it as their retiring event. | `git remote rename`, then `git branch -u` on `main` and `audit/adversarial-pass`. Until then **every `gh` call must pass `--repo`** — which is what saved this session. | Owner |
| **H-12** | **The fork's Actions are not executing** — 30/30 jobs dead in 2–7 s with zero steps. Billing, inferred from shape, not from a message (§6.8). | Check `Rav-i24` Actions billing. Blocks any fork rehearsal, though two independent reasons also block it (§2.4). | Owner |
| **H-13** | **The `publish` job is an `echo`.** There is no marketplace publication code in the repository. Route A's final link does not exist. | Write it, before it is needed under time pressure. | Owner |
| **H-14** | **`stage-runtime.js`'s host-platform fallback is still reachable** from `package.json:57` (`build:runtime`, no target argument). Release path is correct; local path is not. | Make the target argument mandatory; delete the `process.platform === 'win32'` fallback at line 51. Coverage 1/1, measured false-positive rate 0. | Owner |
| **H-15** | **Checksums are generated, never verified after upload.** Ranked LOW and honestly flagged as the weakest of the six proposals (§2.5(e)). | Only worth doing after H-3 gives the upload path any observations at all. | Owner |

### 7.1 Every `NEVER RUN` cell has a line above (rule 3)

Mapping, so the claim is checkable rather than asserted: *manual* (21 cells) → **H-1**.
*`helper` off linux* (8) → **H-7**. *`proxy` off linux* (10) → **H-8**. *`lint`/`fuzz`
off linux* (20) → **H-9**. *`sign` on darwin* (4) → **H-6**. *`publish`* (12) → **H-6**
+ **H-13**. *`clients/vscode` EDH on darwin/windows* (2) → **H-1** and **H-8**'s class:
neither is closed by CI, both need a person on that platform. *daemon/tui `integration`
off linux* (4) → **H-7**/**H-9**'s matrix decision.

### 7.2 What "told" means here, and what it does not

**This file is a record, not a delivery.** Writing it tells nobody. Committing it tells
nobody. The distinction is the one `docs/C6_CLOSING_2026-09-17.md` draws about exit
criterion 6, and it applies to this document too.

**Who has been told, by what means, as of this commit:** *nobody, by any means other
than this file and the session transcript.* I filed no issue, sent no message and
opened no PR. The fourteen handover rows in the C6d section that landed as
`0ee8c19` were filed **by the owner, not by me**, and **none of the fifteen rows above
corresponds to one of them.**

**The one row that cannot be delivered by filing an issue is H-1**, because it needs a
person who has not read this code, and this repository does not name one.

---

## 8 — Bottom line

**Part 0: CLEAN** at `0ee8c19` — tree empty, HEAD == canonical `main`, `gates` green
there (`35227134808`), `build` green at the last non-markdown commit with a provably
markdown-only delta since, `reach.sh` exit 0 at 256 items, `make check` exit 0 in
11m46s running alone.

**The release plan is short because the evidence under it is thin.** One release run
ever, dead in `package`, with four stages downstream of it at zero observations and a
`publish` job that is an `echo`. The recommendation is **Route A with Route C as a
dated fallback, and Route B rejected** — B is the only route that spends a CI guarantee,
for an outcome C reaches for free.

**The single most valuable next action is not in the prompt's list:** one
`workflow_dispatch` of `release.yml` on canonical. It needs no secrets, no Apple
enrolment and no tag; it cannot publish anything; and it converts the four least-known
cells in the ledger into observations. **It is the owner's to run, and I did not run
it.**

**Monday is not closed by anything here**, and the honest statement of its risk changed
twice during this pass — first up, on four consecutive scheduled failures, then back
down on finding those were a month-stale commit and that the eval job is green at
`7454202`. What remains is `fuzz`, a macOS flake, and the fact that **no cell in this
ledger reads `OBSERVED` on a `schedule` event.**


---

# ADDENDUM — 2026-09-17, later the same day: what this report got wrong, and what changed under it

*Appended after the work it describes. Nothing above this line is edited: a
record is annotated, never rewritten. **The ledger in §3 is now stale in its
macOS rows and in five `NEVER RUN` cells**; this section says which, so a reader
does not act on a table that has moved.*

## Two claims in this report were WRONG

**"`release.yml` has run exactly once."** It has run **four** times, listed in
full because counting is what went wrong here:

| Remote | Run | Commit | Event | Result |
|---|---|---|---|---|
| canonical `Rav-2007/codeterminal-core` | `31505527398` | `bd1bc69` (tag `v0.0.1`) | push | **failure in `package`** |
| fork `Rav-i24/Mochiii` | `33921667448` | `a959945` | dispatch | **failure in `package`** |
| fork `Rav-i24/Mochiii` | `33922431985` | `0925a3d` | dispatch | success |
| fork `Rav-i24/Mochiii` | `33938839311` | `d48530d` | dispatch | success |

They are cited in `docs/RESIDUAL_RISKS.md` R1.15 by id without a remote, and the
fork ones 404 on the canonical repository — which is where I looked. §4/P1 of
this very report records that the remote names are inverted, and I was caught by
it two sections later.

**This paragraph was itself wrong on first writing, and the error was the same
one.** It said "three times", because I had queried the fork for the two run ids
R1.15 happened to cite rather than asking the fork what it held. `33921667448`
is the one nobody had named. **A document correcting a miscount miscounted**,
and the only reason it did not ship that way is that the closing instruction —
check a document against the thing it is about — was applied to it before
commit.

**The run nobody had named is the most useful of the four.** It failed in
`package` at `a959945`, ten minutes before `0925a3d` passed. That is a
before/after bracket on the fix, which is strictly better evidence than the
"succeeded twice" this section originally claimed: `0925a3d` is not merely
*present* in a green run, it is the commit that turned a red one green.

**"Everything downstream of `package` is at zero observations."** False, and
false in the project's favour. `package` has run four times and succeeded twice,
and run `33938839311`'s log shows the signing guard excluding `darwin-arm64` and
naming the exact Linux+Windows asset set it would attach. §2.2, §2.4, §6.1 and
§6.15 all rest on that wrong claim and should be read with it removed. In
particular §6.15's "`0925a3d` is a commit I did not read" is now settled by
measurement in both directions: `package` failed at `a959945` and succeeded at
`0925a3d`.

## What the follow-up work changed

- **`darwin-arm64` is removed from the target set.** A tag now declares two
  platforms and ships two; §2.3's Route C was taken, Route B rejected.
  §3's ledger rows for darwin are therefore **`N/A — the platform is no longer
  built`**, not `NEVER RUN`.
- **The `helper` Windows defect this report never found.** `helper/main.go`
  asked `protocol.Listen` for a Unix socket; Windows rejects that outright, so
  the helper could not start and `index`/`retrieve` failed on the platform
  entirely. §6.3 called `helper` off-Linux "the sharpest gap in the ledger" and
  was right about the gap while understating it: the cell was not merely
  unobserved, the code was broken. `daemon/testdata/fakehelper` hid it by
  calling `net.Listen("unix", …)` directly, which works on Windows — a fixture
  taking a different code path than the code it stood in for.
- **`protocol` could not build standalone for Windows** (`x/sys` under-declared
  by 37 minor versions; `go.work` masked it). `make standalone` is the new gate;
  `supply-chain.sh` structurally cannot see it, demonstrated by neuter.
- **§2.5(c) version consistency is closed.** `daemonVersion` is stamped from the
  tag; `scripts/release-version-guard.sh` refuses a tag that disagrees with
  `package.json`.
- **§2.5(f) — the gate I said I would build first — is partly superseded.** The
  canonical dispatch is still unrun and still worth running, but it converts
  fewer cells than claimed, because the fork had already observed that path.
- **H-10's `docs-coderefs` 24/20 gap and H-4's macOS SQLite flake are
  untouched.** H-7 (`helper` into `cross`) is **done**. H-6's route decision is
  **taken**. H-12 (the fork's billing) is unchanged and no longer blocking
  anything, since the rehearsal it blocked is superseded.

## What is still true and still unaddressed

§6's list of unverified things stands except where named above. In particular
**the manual session (H-1) has still never been performed by anyone**, the
`publish` job is still a single `echo` with no marketplace publication code
behind it, and **no cell in the ledger reads `OBSERVED` on a `schedule`
event** — the Monday 2026-09-21 check is not closed by any of this.

---

# ADDENDUM 2 — 2026-09-20: the scheduled-run count was wrong the same way the release-run count was

**This does not edit §1.1, §5.2, §6.7 or P11 in place.** What those sections
believed is part of the evidence. What follows is what the canonical repository
answers when it is asked for its whole scheduled history rather than for the runs
a document already cited.

**§1.1 says "the four consecutive scheduled failures".** Measured:

```
$ gh run list --repo Rav-2007/codeterminal-core --limit 1000 \
    --json databaseId,workflowName,headSha,event,conclusion,createdAt \
    --jq '.[] | select(.event=="schedule")'
```

| Run | Date | Commit | Jobs | Failed | Longest failing job |
|---|---|---|---|---|---|
| `30801081242` | 2026-08-03 | `6729017` | 15 | 1 | 54 s |
| `31364806807` | 2026-08-10 | `8ccc591` | 30 | **30** | 39 s |
| `32002184546` | 2026-08-17 | `efc611d` | 30 | **30** | **5 s** |
| `32698022729` | 2026-08-24 | `efc611d` | 30 | **30** | **5 s** |
| `33390351465` | 2026-08-31 | `efc611d` | 30 | **30** | **5 s** |
| `34114544830` | 2026-09-07 | `efc611d` | 30 | 1 | 872 s |
| `34837230165` | 2026-09-14 | `efc611d` | 30 | 7 | 900 s |

**Seven scheduled runs. Seven failures. Zero successes, ever.** All on canonical
`Rav-2007/codeterminal-core`.

**Three things this changes.**

**(a) The count and the streak.** Not four — seven, and the streak is not a
streak inside a longer history, it is the *entire* history. `build` on
`schedule` has never once gone green on this repository. Exit criterion 1 asks
for an event with a base rate of **0 in 7**, which is a different sentence from
"the four red ones were stale".

**(b) "All four ran at `efc611d`" undercounts its own argument.** Five ran at
`efc611d`, not four — 2026-08-17 (`32002184546`) was omitted. The staleness
argument is *stronger* than §1.1 made it, not weaker.

**(c) One cause was asserted for runs that measurably have three.** §1.1 says
"The failures were six `lint` jobs plus `--- FAIL: TestRerankEvalRetrievalRanking
(417.07s)`." That describes exactly **one** of the seven:

- `32002184546`, `32698022729`, `33390351465` — **all 30 jobs failed in 3–5 s.**
  Nothing ran. That is the billing signature already recorded independently in
  `docs/PREFLIGHT_MACOS_AND_MERGE_2026-09-15.md:60-62`, which this report did not
  reconcile against.
- `34114544830` — **one** job failed: `retrieval eval (scheduled)`, 872 s. No
  `lint` job failed in this run at all.
- `34837230165` — **seven**: six `lint` plus `retrieval eval (scheduled)` at
  900 s. This is the run §1.1 actually describes.
- `31364806807` (08-10, 30/30 at 39 s) and `30801081242` (08-03, 1 of 15 at 54 s)
  predate `efc611d` entirely and are outside the staleness argument's reach.

**The `lint` cause, named exactly**, from the run itself rather than from
inference:

```
$ gh run view 34837230165 --repo Rav-2007/codeterminal-core --log-failed | grep 'lint (daemon)'
...
go: toolchain upgrade needed to resolve golang.org/x/sys/execabs
go: golang.org/x/sys@v0.48.0 requires go >= 1.26.0 (running go 1.25.14)
##[error]Process completed with exit code 1.
```

That is the unpinned-tool-past-the-toolchain-floor failure, and
`scripts/tool-pins.txt`'s own header says it was measured on 2026-09-09.
**`scripts/install-tools.sh` and `scripts/tool-pins.txt` do not exist at
`efc611d` and do exist at canonical `main` `0ee8c19`** (`git show
efc611d:scripts/install-tools.sh` → error; `git show 0ee8c19:scripts/tool-pins.txt`
→ the file). A `schedule` run takes its workflow from the default branch, which
is `main`, which is `0ee8c19`. So the specific `lint` cause of 2026-09-14 is
fixed **on the branch Monday will actually run**.

That is a statement about a mechanism, still not an observation. §6.7's
"*dispatch green ≠ scheduled green ≠ push green*" stands, and **no ledger cell
reads `OBSERVED` on a `schedule` event.**

**(d) The fork has had a green scheduled run and canonical has not.**
`34119559516`, 2026-09-07, `4b8c53e`, `build`, **success**, on
`Rav-i24/Mochiii`. It is on a branch head on the wrong remote and establishes
nothing about canonical `main`; it is recorded here only so that nobody finds it
later and reads it as one.

**How this error was made, which is the point of recording it.** The count of
four came from the four runs already named in documents I had read. The
repository was never asked for its scheduled history. That is the third instance
in this pass of a search shaped by the answer it expected to confirm — after the
canonical-vs-fork frame error and the three-versus-four release-run miscount —
and the second one to land inside a section whose subject is the danger of
exactly that. The command that settles it is one line and is pasted above.
