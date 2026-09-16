# C7 — release readiness: is this packed clean to ship?

<!-- coderefs: enforced -->

**Recipient: the repository owner.** This is a verdict with enumerated blockers. Nothing here was
shipped, tagged, or dispatched.

*Evidence gathered 2026-09-15; the final clean gate run and the verdict were completed 2026-09-16.*

| | |
|---|---|
| **Branch** | `audit/adversarial-pass` |
| **HEAD** | `060f0a3` |
| **Tree** | clean |
| **vs canonical `main`** (`upstream/main` = `efc611d`) | **247 ahead, 0 behind** — FF-AVAILABLE |
| **Latest `build`** | `34739694099`, commit `1013e1b`, **success**, remote **`Rav-2007/codeterminal-core`** |
| **Latest `gates`** | `34739694106`, commit `1013e1b`, **success**, same remote |
| **CI at HEAD** | **none.** No `build` run exists for any commit after `1013e1b` |

> **"No build run at HEAD" is a distinct state from "build passed."** Every green below is local.

Rows are **(M)** measured / **(R)** read / **(U)** unknown-plus-what-would-settle-it / **(C)** carried.

---

# THE VERDICT

## **SHIP WITH NAMED EXCLUSIONS** — but not from `main`, and not today.

Read precisely, because the qualifier carries the weight:

> **The code on this branch is in a shippable state. The repository is not in a releasable state.**
> Every blocker below is about *delivery* — where the code is, what the release path does when it
> runs, and which platform's artifacts are refused — and **not one is about the code being wrong.**

### The exclusions, each with who decided and what a user gets

| Exclusion | What a user on that platform gets | Decided by |
|---|---|---|
| **darwin assets** | **nothing, and neither does anyone else** — the signing guard fails the job before the release is created, so a tag today produces **zero** assets on every platform | fail-closed by design; the owner has never decided otherwise |
| **the `publish` job** | the extension is never pushed to the Marketplace | `if: false`, "flip this on deliberately, never as a side effect" |
| everything in §12 | — | not decided; not verified |

### And two conditions on the verdict itself

1. **It is a verdict about this branch, not about `main`.** From `main`, today, the release
   reproduces run `31505527398` exactly — `0925a3d` is not there.
2. **The green it rests on is machine-dependent** (Row A). The same `make check`, same commit,
   exited 2 earlier in this session under load. §7 records what was running.

---

# 1. Step 0 — the release path has already run once, and it failed

§13 rows 15 and 16 said release creation and asset upload had never run *because "a dispatch is not a
tag"*. **That reason is retired.**

**Remote `Rav-2007/codeterminal-core`, run `31505527398`, tag `v0.0.1`, head `bd1bc69`, 2026-08-11.**
The only release run in this repository's history **(M)**. `gh release list` is empty **(M)**.

| Job | Result | Duration |
|---|---|---|
| `binaries (linux-x64)` | **success** | 58 s |
| `binaries (darwin-arm64)` | **success** | 45 s |
| `binaries (win32-x64)` | **success** | 100 s |
| **`package`** | **FAILURE** | 35 s |
| `publish (manual, founder action)` | skipped — `if: false` | — |

All **(M)**, WALL-CLOCK.

## 1.1 How much of the release path has actually been exercised

The `package` job is a sequence, and it died partway. Everything after the packaging loop is at
**zero observations** **(M)**:

| Step, in order | Observed? |
|---|---|
| package loop — `linux-x64` | **PASSED** — 21 entries, 26.7 MB unpacked |
| package loop — `darwin-arm64` | **PASSED** — 21 entries, 26.2 MB unpacked, vsix produced |
| package loop — `win32-x64` | **FAILED** |
| Stage the terminal client | **never ran** |
| `upload-artifact` (terminal-client, vsix-packages) | **never ran** |
| Checksums | **never ran** |
| **Signing guard** | **never ran** |
| **Attach to the GitHub Release** (creation + asset upload) | **never ran** |

**§13 rows 15 and 16 restated, which is what the chunk asked for rather than closing them:** release
creation, asset upload and a `SIGNED` marker are still at **zero observations** — not because no tag
has been pushed, but because **the one tag that was pushed died two steps earlier.**

## 1.2 Does `0925a3d` fix the whole failure, or only the win32 symptom?

**It fixes the whole failure, and the chunk's hypothesis is refuted.**

The hypothesis was that the other two platforms might have been failing for the same host/target
confusion and never got far enough to show it. **They got all the way through and passed** **(M)** —
the log shows `package gate PASSED` for both `linux-x64` and `darwin-arm64` before `win32-x64` was
reached.

The mechanism, read at both revisions **(M)**:

| | at `bd1bc69` (the failed run) | at HEAD |
|---|---|---|
| binary suffix | `process.platform === 'win32' ? '.exe' : ''` — **what the script runs on** | `target === 'win32-x64' ? '.exe' : ''` — **what it builds for** |
| the loop's call | `node scripts/stage-runtime.js` | `node scripts/stage-runtime.js "$TARGET"` |
| per-target isolation | `rm -rf daemon` + `cp -a artifacts/binaries-$TARGET/.` — **already correct** | same, but named files rather than a wildcard |

On a Linux packaging host `process.platform` is `linux`, so the suffix was `''`. That is **the right
answer for linux and darwin and the wrong answer for win32** — which is why two targets passed. Their
pass was correct, not accidental: each read from its own `artifacts/binaries-$TARGET/` directory, so
the darwin vsix contains the darwin binary **(M)**.

**The derivation was wrong for all three and the answer was wrong for one.** That is the sharpest
form of "passes for the wrong reason" — it *was* passing for the wrong reason on two platforms, and
only the third made the reason visible.

| | |
|---|---|
| Fix | `0925a3d`, 2026-09-05 — touches `release.yml` (+7) and `stage-runtime.js` (+25/−1) |
| On canonical `main`? | **NO** **(M)** |
| On the fork's `main`? | **NO** **(M)** |

**DELIVERY GAP instance #7, on the release path itself.** `main`'s release is broken for `win32-x64`
today, and has been since the workflow was written.

---

# 2. Release path and artifact inventory

## 2.1 Where a release comes from

```yaml
on:
  push:
    tags: ["v*"]
  workflow_dispatch:
```

**There is no branch restriction anywhere in `release.yml`** **(M)**. A tag matching `v*` on *any*
branch produces a full release run. A release does **not** have to come from `main`.

**That cuts both ways and the second way is the important one.** It means the merge is not technically
required to ship. It also means **nothing structural stops a tag on an unreviewed branch from
producing a draft release** — see §6.3.

## 2.2 Two paths, one repository

| Path | Built where | With what | Uploaded where |
|---|---|---|---|
| **binaries** | native runners — `ubuntu-latest`, `macos-latest`, `windows-latest`, one per target | `go build -trimpath`, `CGO_ENABLED` explicit in both directions | `binaries-<target>` artifact → consumed by `package` |
| **vsix** | one `ubuntu-latest` host packages all three targets | `vsce package --no-dependencies --target <target>` | `vsix-packages`, `terminal-client` artifacts → release assets |
| **proxy** | `docker build ./proxy` in `build.yml` | the same Dockerfile Railway uses | not a release asset — deploys separately |

**Three binaries** per target: `codeterminal-daemon`, `codeterminal-embedder-helper`,
`codeterminal-tui`. The TUI is **asserted out of the vsix** by `clients/vscode/scripts/verify-vsix.js`
**(M)** — it is a separate download, not part of the extension.

## 2.3 `-trimpath` holds, measured both ways

| Module | Absolute build paths with `-trimpath` | |
|---|---|---|
| `daemon` | **0** | **(M)** |
| `clients/tui` | **0** | **(M)** |
| `helper` | **0** | **(M)** |
| `protocol` | **0** | **(M)** |
| `editapply` | **0** | **(M)** |
| **control — `daemon` WITHOUT `-trimpath`** | **1,036** | **(M)** |

The control is the point: a probe that reports zero against a binary that has none proves nothing
about the probe. This one sees 1,036 when they are there.

`./scripts/supply-chain.sh` independently reports **6 modules tidy, 4 binaries clean under -trimpath**,
exit 0 **(M)**.

---

# 3. Signing status — no longer `(U)`

The chunk carried two `(U)` rows. **Both are now measured, and one of them is worse than `(U)` suggested.**

## 3.1 The five secrets do not exist

```
$ gh secret list --repo Rav-2007/codeterminal-core
(exit 0, zero rows)

$ gh secret list --repo Rav-2007/codeterminal-core --env marketplace
failed to get secrets: HTTP 404: Not Found
```

**(M)** Zero repository secrets are set. The `marketplace` environment the `publish` job references
**does not exist**. The five the workflow names are `MACOS_CERT_P12`, `MACOS_CERT_PASSWORD`,
`MACOS_NOTARY_ISSUER_ID`, `MACOS_NOTARY_KEY_BASE64`, `MACOS_NOTARY_KEY_ID` **(M)**.

## 3.2 The guard behaves as described

`scripts/release-signing-guard.sh --self-test` exits **0** **(M)**. The workflow comment states the
asymmetry and the code matches it: on a dispatch an unsigned target is **excluded** and the job stays
green; on a tag it **fails**, because "quietly shipping fewer platforms than the tag implies is its
own fail-open". It keys on the **presence** of `SIGNED-<target>`, never the absence of `UNSIGNED-`.

## 3.3 The consequence, stated plainly

> **On a real tag today, the release job fails on darwin, and therefore no release is created at
> all.** Not "darwin is excluded" — the `Attach to the GitHub Release` step is downstream of the
> guard, so a guard failure means zero assets are published.

Whether that is a blocker or the intended fail-closed behaviour is the owner's call. **What is not a
judgement call is §6.3.**

---

# 4. Supply chain

## 4.1 The Go side

| Check | Result | |
|---|---|---|
| `./scripts/supply-chain.sh` | exit 0 — 6 modules tidy, 4 binaries `-trimpath`-clean | **(M)** |
| `./scripts/go-toolchain-pinned.sh` | exit 0 — **11 pin sites agree on go1.25.13** | **(M)** |
| `govulncheck` (via `make check` → `supplychain`) | see §7 banner | **(M)** |

## 4.2 The pin is exact at HEAD and a RANGE on `main`

| | `GO_VERSION` |
|---|---|
| HEAD | `"1.25.13"` — exact **(M)** |
| `efc611d` (canonical `main`) | `"1.25.x"` — **a range** **(M)** |

And CI on `main` resolved that range to **go1.25.14** **(M)** — read from run `34837230165`'s job logs
(`hostedtoolcache/go/1.25.14`, `Setup go version spec 1.25.`).

`f5551ca` tightened it, 2026-09-09, and is **not on `main`** **(M)**. So **`main` builds today with
whatever the newest 1.25.x happens to be** — the exact host-dependence class this pin exists to close.
One more thing the merge delivers.

**I nearly reported this as a live gap at HEAD.** The local gate says 11 sites agree on 1.25.13 and a
CI log said 1.25.14; the reconciliation is that they are different revisions, not a contradiction.
H2 — the reference frame again.

## 4.3 The npm surface is smaller than the checkpoint feared, in the way that matters

| | |
|---|---|
| `npm audit --omit=dev` | **found 0 vulnerabilities**, exit 0 **(M)** |
| `npm ls --omit=dev --depth=0` | **`(empty)`** — **zero production dependencies** **(M)** |
| `npm audit` (full tree) | **6 vulnerabilities: 2 moderate, 4 high** — all `mocha` → `serialize-javascript` **(M)** |

**None of the 23,652 files ship.** The extension has no production dependencies at all, and the vsix
is packaged `--no-dependencies` — 21 entries, 26 MB, which is the daemon and helper binaries, not a
node tree.

**They do run at build and test time**, so a compromised devDependency could alter what is built. That
is a real surface and it is a *different* surface from the one §13 row 20 describes. **Not fixed here:
`npm audit fix` is a behaviour change and out of scope.**

## 4.4 The fourth unscanned surface, per the chunk's amendment

`go-toolchain-pinned.sh` checks the **Go version** across its pin sites. It does **not** check that
**tool dependencies** are pinned. That gap is what let `bodyclose`'s undeclared `x/sys` transitive
resolve at latest and turn `main` red for seven consecutive scheduled runs **(C)**.

**Four surfaces, one theme:** `onnxruntime` native, the npm dev tree, the analysis-tool transitives,
and the toolchain range on `main`. Each is a place where *what is checked* and *what is executed* are
resolved separately.

---

# 5. History and publication

## 5.1 The secret sweep

**129 raw pattern hits** in the working tree over `AKIA|BEGIN … PRIVATE KEY|xox[baprs]-|ghp_|sk-`
**(M)**. That number is **not comparable** to the prior pass's 33 — different instrument, and `sk-`
alone contributes 83 of them by matching inside ordinary words.

**The comparison that is at the resolution of the question:** after excluding tests, documents and the
scrubber's own pattern tables, **exactly two hits remain, and both are prose comments** —
`daemon/context.go:377` and `daemon/websearch.go:67`, each explaining the scrubber by naming a
pattern it detects **(M)**.

> **No credential is present in the working tree.** The distribution is unchanged in kind from the
> prior pass: scrubber tests, the packaging gate's own pattern list, and documentation about
> scrubbing.

## 5.2 The email — superseded by Row C

An earlier draft of this section reported *"a real personal email address in history, on both remotes
and on HEAD"*. **That was imprecise in a way that overstated it.** It is not in any tracked file at
HEAD; where it appears in history it was a licence contact, published on purpose.

**See Row C (§7d) for the measured three-way disambiguation, the retraction, and the disposition.**
The repository is **PRIVATE** today **(M)**, and the decision is **hold, with a trigger**.

---

# 6. Does the thing run for a new user?

## 6.1 The install path — C2 landed

```
$ env -i PATH=$PATH HOME=$HOME go test -count=1 -run TestInstallPath .
ok  	codeterminal/daemon	19.210s

$ node clients/vscode/scripts/install-path-check.js
install-path-check: ok — spawnDaemon passes an env, and 1 setting(s) are contributed and read
```

Both **(M)**, exit 0. **The product's primary GUI now starts on an ordinary install.** It did not, at
any commit, from 2026-07-05 until `22b3021`.

**This is the single largest change in shippability in this pass**, and it is on the branch only.

## 6.2 Packaging

The vsix gate asserts contents both ways — what must be present and what must not. `codeterminal-tui`
is explicitly refused: *"ships the standalone terminal client, which the extension does not launch"*
**(M)**. `stage-runtime.js` keys on the **target**, verified at both revisions in §1.2.

## 6.3 The branch guard that is missing, and the accidental protection hiding it

**This is the finding I would most want an owner to read.**

- `release.yml` has **no branch restriction** — any `v*` tag, on any branch, runs a release **(M)**.
- Today that is harmless, because **the signing guard fails the job on darwin** for want of secrets.
- **The moment the five `MACOS_*` secrets are added, that accidental protection disappears**, and a
  tag on any branch — including one nobody reviewed — produces a draft release with signed macOS
  binaries.

> **The branch guard must land before the secrets do.** Right now a missing credential is doing a
> branch guard's job, and nothing in the repository records that it is. This is *screen-not-model*:
> the system appears to have a control it does not have.

**Not fixed here.** C7 is read-only and this is a workflow change with a release-policy decision
inside it.

---

# 7. The gate set, run whole

**Invocation: `make check`, one command, output not piped.** A prior run piped it through `tail -12`
and destroyed its own per-gate output; exit 0 was authoritative and everything else was lost.

**What else was running: nothing** (H11). No background tasks, no concurrent probes. That sentence
is part of the measurement — see Row A.

```
REAL_MAKE_CHECK_EXIT=0
check: all gates green
```

Every gate, in order, exit 0 **(M)**:

| Gate | Result |
|---|---|
| `hookcheck` | `core.hooksPath -> .githooks`, all hooks executable |
| `gofmt` | clean |
| `vet` | clean, including `-tags eval` and `-tags warnscan` |
| `crossvet` | clean — windows + darwin, 5 modules |
| `race` | 9 packages green; `daemon` 170.6 s, `clients/tui` 233.4 s WALL-CLOCK |
| `lint` | 18 checks — staticcheck, ineffassign, bodyclose × 6 modules |
| `ratchet` | 9 packages, all above floor; `helper` 61.5% vs floor 22.0 |
| `errcheck` | 6 modules, all **at** ceiling |
| `evalguard` | no committed file echoes a retrieval-eval query — **see Row D** |
| `supplychain` | signing-guard self-test **40 passed, 0 failed**; 32 actions pinned to SHAs; **11 toolchain pin sites agree on go1.25.13**; 6 modules tidy; 4 binaries `-trimpath`-clean; **govulncheck: 6 modules, 0 reachable vulnerabilities** |
| `webview` | verify-vsix self-test ok; webview-check 0 undefined symbols, 36 findings **at** ceiling 36 |
| `docs` | 332 links resolve; **186 references across 18 enforced documents**, extensions `go,js,md,sh,ts,yml`; all four registers agree |
| `debtmarkers` | 157 non-test files, 6 modules, **0 markers** |
| `parity` | 23 scripts accounted for — 12 both, 2 local, 3 CI, 6 manual |
| `reach` | **266 items examined, 13 exemptions each with a retiring trigger** |

## The banner's own list of what it did not run — the honest scope of this green

```
make check is green. CI STILL CHECKS THINGS THIS RUN DID NOT:

  Steps and gates that run only in CI (derived from this script's manifest):
    fuzz.sh                    30s per target is too slow for a pre-push gate
    macos-sign-and-notarize.sh Release-only; needs Apple credentials + a macOS runner
    install-tools.sh           Installs the pinned analysis tools

  Capabilities a developer machine does not have (NOT derived -- see above):
    real Windows execution    cross (windows-latest) BUILDS, VETS and RUNS the tests.
                              `make crossvet` only COMPILES. Every runtime
                              platform defect is invisible here, by construction.
    real macOS execution      macos-latest, and only on main.
    the VS Code extension     tsc + a real Extension Development Host.
    the proxy container       docker build.
    the 2000-turn soak        run unraced at full length in CI; the local
                              gate runs the reduced raced version.

  Nothing above is a reason not to push. It is what a green local run does
  NOT promise, stated where it is cheap to act on.
```

**Two of those absences matter to this verdict specifically:** the Extension Development Host — which
is where C2's inverted test lives and it has never executed — and real macOS execution, which is
where the signing path would run.

**And per Row A, the green itself is machine-dependent.** An earlier run of this same command on this
same commit exited **2**.

---

# 7b. Row A — the gate set is not reproducible, and the verdict must say so

**The named class, third instance: a check whose result depends on something other than the code it
checks.**

| # | Instance | The dependence |
|---|---|---|
| 1 | `helper`'s coverage floor | CI measures ~22%; a host with the ONNX model cached measures **61.5%** **(M)** |
| 2 | `docs-coderefs`'s ambiguity check | a bare basename resolved to 1 file in CI and 3 on a machine that had applied an edit. **Closed in C4** by deriving against `git ls-files` |
| 3 | **`TestRepaintCostAtTheTranscriptCeiling`** | **idle p50 48.674 ms → PASS; under load p50 71.675 ms → FAIL**, same commit **(M)** |

## The measurement, both ways

| Condition | p50 | p99 | min | max | Verdict |
|---|---|---|---|---|---|
| **idle** | **48.674 ms** | **61.737 ms** | 22.123 ms | 68.377 ms | **PASS** |
| **under three concurrent probes** | **71.675 ms** | 127.893 ms | 25.425 ms | 134.509 ms | **FAIL** |

All WALL-CLOCK, 200 samples each, same commit, same test **(M)**. The concurrent probes were mine:
`gh` API calls, `npm audit`, and several `go build` runs alongside a backgrounded `make check`.

## The headroom, measured

The test fails past **64 ms** (8× the 8 ms repaint budget).

| | Idle value | Against the 64 ms threshold |
|---|---|---|
| p50 | 48.674 ms | **1.31×** |
| **p99** | **61.737 ms** | **1.04×** |

> **The p99 is inside 5% of the failure threshold on an idle machine.** That is not margin. It is a
> gate waiting for a busy afternoon — and this session supplied one.

## The consequence for this verdict, stated plainly

> **`make check` exit 0 is a statement about the machine as much as about the code.** A reader is
> entitled to know that the gate set cited below as the scope of a release verdict **is not
> reproducible**: the same commit produces a different verdict on a loaded host. The green in §7 was
> obtained on an otherwise idle machine, and §7 says what was running.

## The remedy, already designed in this pass for a different gate

The revised frame-budget memo prescribes exactly this cure for the Windows timing assertion:

- assert **p99** against the derived bound;
- assert **`worst`** against a loose stall ceiling (10× budget), so a catastrophic hang still fails;
- keep printing both either way.

It removes the cliff without weakening the gate, and **raises nothing**.

**This repository now has two wall-clock gates with the same disease and one prescription that fits
both.** `clients/tui` is someone else's module and the pattern has an owner: **recommended, not
implemented.** Cost: one test edit, no floor movement, no behaviour change.

---

# 7c. Row B — the measurement contradicts the register

`R1.12` records **22.4 ms** p50 repaint against an 8 ms budget, with two cheap trades named and
deliberately untaken (`refreshInterval` 16→33 ms; lowering the 2 MiB ceiling).

**Measured idle this session: p50 48.674 ms, p99 61.737 ms, min 22.123 ms, over 200 samples** **(M)**.
That is **2.2× the recorded figure.**

Three possibilities, all **(U)**:

1. the register row is stale and was never re-measured;
2. the two figures come from different machine classes — both are WALL-CLOCK, so this is likely, and
   **the likelihood is itself the finding**;
3. something regressed between the recording and now.

**The `min` is the interesting number. 22.123 ms is almost exactly R1.12's recorded p50** — which is
suggestive of a machine-class difference rather than a regression, and possibly of a recorded figure
that was a single best-case sample rather than a p50 over many.

**Suggestive is not measured. H9 applies and I am not resolving it.**

**What would settle it:** the commit and host the 22.4 ms was taken on, plus a re-run at that commit
on this host. Cheap if the provenance was recorded; **(U)** if it was not.

> **R1.12 is NOT updated.** Updating a register row to match the newest measurement, without
> establishing which figure is right, is how a stale record becomes a confident wrong one.

This belongs in the verdict because C7 would otherwise report a passing gate on a number that
contradicts the residual-risk register it exists to reconcile.

---

# 7d. Row C — the email: hold, with a trigger

**Owner decision on record: hold. History is not rewritten.**

## The argument that decides it, and it is not the one I had weighed

A rewrite from `3ff9ee2` forward changes **every descendant SHA**. That invalidates the checkpoint's
seven cited SHAs, C0b's verification of them, `docs/TRUST_BOUNDARIES.md`'s anchors, `reach.sh`'s
ancestry checks, and every commit SHA in every report committed during this pass.

> **The evidentiary chain this pass exists to build is the thing a rewrite destroys** — and that cost
> is **already incurred**. It does not begin at the merge. My earlier framing, that a rewrite gets
> more expensive after the merge, understated it: the expense landed the moment the first report
> cited a SHA.

Against that: the repository is **private** **(M)**, and the exposure is conditional on publication.

## Recorded as a real trigger

> **`TRIGGER: before this repository or any fork of it is made public`**

This converts a `TRIGGER: NONE STATED` row into one with an actual firing condition — the shape this
pass has been eliminating everywhere else.

## The disambiguation, because "on both remotes and on HEAD" was imprecise

**"On both remotes and on HEAD" was imprecise, and the precise version is a weaker finding.**
Three cases, and this is the third:

| Question | Answer |
|---|---|
| In a tracked file at **HEAD**? | **NO** **(M)**. The cheap remedy does not apply — it is already gone |
| In file **content** in history? | **Yes**, in four commits — including `af389e2`, *"legal: add a proprietary license"* |
| In commit **authorship metadata**? | **Yes — 194 commits** carry it as author and committer **(M)** |

## What the file-content occurrence actually is

At `af389e2` the address sits at `LICENSE:116` — **as the license's contact address** **(M)**. That is
a *deliberate publication*, not a leak. At HEAD, `LICENSE` names a different address in both the
copyright line and the contact section, so the project already publishes a contact by intent and the
older one was superseded in the ordinary way.

> **RETRACTION.** I reported this as "a real personal email address in history, on both remotes and
> on HEAD", which reads as an accidental exposure. It is not on HEAD in any file, and where it is in
> history it was the intentionally published contact for the licence. **The finding is weaker than I
> stated and I am correcting it before it reaches a verdict.**

## What remains, stated accurately

1. **Authorship metadata on 194 commits.** This is ordinary git data, visible to anyone who can
   clone, and **neither a file delete nor a content filter reaches it** — only a full rewrite with an
   author filter or a published `.mailmap`. A `.mailmap` changes what tools *display*; it does not
   change what is stored.
2. **A superseded contact address in an old `LICENSE`**, which was public-by-intent when written.

**Neither is a release blocker.** Both are publication-posture questions, and the trigger above is
the right shape for them.

**The rewrite argument still stands and still decides it:** rewriting from `3ff9ee2` forward would
invalidate every SHA this pass has cited, and that cost is already incurred. **Hold.**

---

# 7e. Row D — I poisoned the eval corpus, and the gate that caught it discards its own evidence

Not in any chunk spec. Found by running `make check` whole, which is the only reason it was found.

## What happened

```
--- FAIL: TestNoIndexedFileEchoesAnEvalQuery (0.27s)
  docs/C7_RELEASE_READINESS_2026-09-15.md is in the indexed corpus and contains
  eval queries [locate#1 token-efficiency#50] verbatim.
  docs/PREFLIGHT_MACOS_AND_MERGE_2026-09-15.md is in the indexed corpus and contains
  eval queries [locate#1 token-efficiency#50] verbatim.
```

**(M)** Both files are mine, written this session. Both quote the eval's own failure output, which
contains the locate#1 query string. **Writing the report about the eval put the eval's answer key
into the corpus the eval indexes** — the same class of defect the guard was built for, committed by
the person auditing it.

`docs/PREFLIGHT_MACOS_AND_MERGE_2026-09-15.md` entered at `21a0854` **(M)**, so `make check` has been
red since that commit and I did not know, because I had not run it since.

## Why rewording was the wrong remedy

The guard names three remedies in preference order and the first is "reword". **It does not apply
here, by the guard's own rule**, which reserves rewording for *product source that a query declares
as an answer* — excluding that would delete a real answer from the corpus.

These are write-ups, and the quoted string is a **verbatim CI transcript line**. Paraphrasing a
quotation to satisfy a lexical check would falsify evidence to make a gate green, which is a worse
outcome than a listed exemption. Both files are now in `evalSelfReferenceFiles` with that reasoning
recorded inline, matching the provenance style of every other entry there. Guard re-run: **exit 0**
**(M)**.

## The two findings underneath it

**1. The gate discards its own evidence.** `Makefile`'s `evalguard` target runs the test with
`>/dev/null`, so a failure prints *nothing* — `make` reported only
`make: *** [Makefile:174: evalguard] Error 1` with no diagnostic. The reason had to be recovered by
running the test by hand. A gate that fails without saying why costs a debugging cycle every time it
fires, and it is the same family as the `tail -12` that destroyed a previous run's per-gate output.
**Recommended, not implemented** — it is a one-line change to a target I did not otherwise touch, and
the report's job is to name it.

**2. CI would have caught this, and the reason it did not is the finding `reach.sh` exists for.**

| | |
|---|---|
| Does `evalguard` run in CI? | **Yes** — `.github/workflows/gates.yml:144`, with `-v`, so the diagnostic *would* have been visible **(M)** |
| Does `gates.yml` skip markdown-only pushes? | **No.** It has **no `paths-ignore`** and fires on `branches: ["**"]` **(M)** |

So a single push would have turned this red within a minute, with a readable message. It stayed
hidden because **the branch has not been pushed since `1013e1b`** — which is exactly what
`reach.sh` reports as its first finding.

> **The unpushed-commits row is not hypothetical bookkeeping. It hid a real, already-committed gate
> failure for the length of this session.** That is the strongest evidence in this pass that the
> delivery gap is a live defect class and not a filing convention.

---

# 8. The verdict must dispose of eight things

## 8.1 `0925a3d` is not on `main`

**Disposition: BLOCKING for a release from `main`. Not blocking for a release from this branch.**

`main`'s release path is broken for `win32-x64` and always has been. The merge fixes it. A tag on
`main` today reproduces run `31505527398` exactly.

## 8.2 The `main` eval contradiction — both readings, no choice made

At `efc611d`, `daemon/rerank_eval_test.go` fails on **one query**, with recall **8/9 on both sides**
**(M)**:

```
:512  hybrid retrieval REGRESSED 1 quer(ies) that passed semantic-only:
      [where does the daemon open the unix socket]
:570  KNOWN GAP (not gated, pre-existing, out of scope): query 1 ... still misses
```

**Reading A — `:512` is right and `:570` is narrower than it reads.** The exemption at `:570` is from
`mustHit`, a different assertion. The regression check is a general invariant — hybrid must not lose
what semantic-only found — and query 1 is not exempt from it. The build is correctly red.

**Reading B — `:570` is right and `:512` is over-broad.** The known-gap comment describes a mechanism
that *necessarily* produces this exact outcome: `main.go`'s package doc contains both "unix" and
"socket", so it wins on the semantic tier *and* the lexical tier and displaces the real
`net.Listen("unix", …)`. Hybrid losing query 1 is not a regression; **it is the documented limitation
happening.** The regression assertion should exclude the known-gap index the way `mustHit` does.

**A third observation that belongs to neither reading:** `:512` counts **losses and not gains**.
Hybrid lost query 1 and gained query 7; recall is unchanged at 8/9. A gate that fails on a net-zero
change is measuring direction, not quality.

> **This is a `daemon/` owner decision and it determines whether `main`'s seven-run red was ever
> real.** Presented, not chosen.

## 8.3 The `MACOS_*` secrets and the missing branch guard

**Disposition: ORDERING CONSTRAINT, and it is the one thing in this report with a wrong order of
operations.** See §6.3. The branch guard lands first, or the accidental protection is removed before
the real one exists.

## 8.4 §13 cannot be cited as it stands

**Disposition: use `docs/C4b_EVIDENCE_INTEGRITY_2026-09-15.md`'s companion table.** §13 has **27 rows,
not 31**; three are false and one partly false. §"What I did not verify" below is built from the
companion table, not from §13.

## 8.5 The merge

**The merge is the delivery vehicle, and C7 is its review boundary.** FF-AVAILABLE: canonical `main`
is a strict ancestor of HEAD, 0 behind **(M)**.

**What it carries, each verified present on the branch and absent from `main`:**

| | |
|---|---|
| the lint pin (`142e57d`) | `main` has been red for seven consecutive scheduled runs without it |
| the Go toolchain pin, exact | `main` has a `1.25.x` range resolving to whatever is newest |
| **the release packaging fix (`0925a3d`)** | `main`'s release is broken for win32 without it |
| **the extension install-path fix (`22b3021`)** | the primary GUI does not start without it |
| the deletion of `test.log` / `test_output.txt` | they pollute the eval's anchor resolution |
| the eval fix | disposition depends on §8.2 |
| every gate written in this pass | including the one that found §8.1 |

## 8.6 Row A — the gate set is not reproducible

**Disposition: NOT a blocker, and a caveat on every other disposition in this report.**

`make check` exit 0 is a claim about the machine as much as the code. The same command on the same
commit exited **2** earlier today under load. The green in §7 was taken idle, and §7 says so.
**Recommended (not implemented):** the two-tier p99/stall-ceiling fix already designed in this pass
for the Windows frame-budget gate. Two wall-clock gates, one prescription, raises nothing.

## 8.7 Row B — the measurement contradicts R1.12

**Disposition: NOT a blocker. `(U)`, and R1.12 is NOT updated.**

Idle p50 48.674 ms against a recorded 22.4 ms. The `min` of 22.123 ms is suggestive of a
machine-class difference rather than a regression, and suggestive is not measured. **What would
settle it:** the commit and host the 22.4 ms was taken on.

## 8.8 Row C — the email

**Disposition: NOT a blocker. HOLD, with `TRIGGER: before this repository or any fork of it is made
public`.** And a **retraction** — the exposure is weaker than I first reported. See §7d.

## 8.9 Row D — the eval corpus, and the gate that hid its own reason

**Disposition: FIXED in this chunk** (two documents added to `evalSelfReferenceFiles` with reasons).
**Two recommendations, neither implemented:** `evalguard` should not run its test under `>/dev/null`;
and the fact that CI would have caught this in under a minute, but could not because the branch is
unpushed, is the strongest argument in this pass for acting on `reach.sh`'s first finding.

## 8.10 The npm surface — R1.13 reclassified, not closed

**Disposition: the SHIPPED half retires; the BUILD-TIME half stays open with a trigger.**

Zero production dependencies **(M)**, so none of the 23,652 files reach a user. The 6 findings
(2 moderate, 4 high) are all devDependencies — `mocha` → `serialize-javascript`.

> **`TRIGGER: any devDependency gains a postinstall script, or the extension gains its first
> production dependency.`** Either changes what the numbers above mean. Until then this is
> build-integrity surface, not shipped surface, and calling it closed would be the same error as
> calling it critical.

---

# 9. The tagger's checklist

Ordered, with the traps in place.

1. **Resolve §8.2** — the eval reading. It decides whether `main` going green is a fix or a
   suppression.
2. **Land the branch guard on `release.yml`** (§6.3) — *before* any secret is added.
3. **Merge**, `--ff-only`, to **`Rav-2007/codeterminal-core`** — the canonical remote. Note that
   `git push` with no arguments goes to `origin`, **which is the fork** (`docs/C5b_TOPOLOGY_2026-09-15.md`).
4. **Wait for `build` to go green on `main`.** A markdown-only commit produces **no `build` run at
   all** — `paths-ignore: ["**.md"]`. "No run" is not "passed".
5. **Do not dispatch while a push run is in flight.** `build.yml`'s concurrency group is
   `<workflow>-<ref>` with `cancel-in-progress: true`, so a dispatch cancels the push run.
   `release.yml` is `cancel-in-progress: false` and is not affected.
6. **Decide the darwin question** (§3.3). Adding the secrets without step 2 is the wrong order.
7. **Then tag.** `release.yml` fires on `refs/tags/v*`, `publish` is `if: false`, and the release is
   created as a **draft** — a human decides when it is public.
8. **Re-run `make reach`** after the merge. Most of its allowlist entries name "the merge lands" as
   their retiring trigger; if it is still green afterwards, an entry was not removed.

---

# 10. Premises that did not hold (H4)

| Premise | Outcome |
|---|---|
| "release creation has never run because a dispatch is not a tag" | **A real tag ran.** It failed two steps earlier, which is a different and more useful fact |
| "the other two platforms may have been failing for the same reason and never got far enough to show it" | **Refuted.** Both packaged cleanly and their passes were correct |
| "the five `MACOS_*` secrets do not exist" — carried as `(R)` | **Confirmed by measurement**, and extended: *zero* repository secrets exist and the `marketplace` environment 404s |
| "23,652 npm files have never been enumerated and a supply-chain finding would live there" | **Recast.** Zero of them ship. The finding, if any, is a build-time surface |
| the secret sweep's "33 hits, unchanged" | **Not comparable** — 129 by my instrument. The comparison that holds is the distribution, and that is unchanged |

# 11. Self-corrections

0. **I reported a release-blocking gate failure that I had created.** The first `make check` failed
   at `ratchet` on a timing test; I had backgrounded it and then run `gh` calls, `npm audit` and
   several `go build` probes alongside. Idle, the same test passes. **Earned H11: wall-clock gates
   run alone, and the report states what else was running.**
0b. **I trusted a harness's success field over a recorded exit status.** The task notification said
   *"exit code 0"* for a run that exited **2** — the reported status belonged to my own trailing
   `echo`. Now the real status is appended into the output file and read from there. **H2's seventh
   instance, and the first where the lying instrument was tooling outside my control.**
0c. **I overstated the email finding** and retracted it in §7d before it reached the verdict: not on
   HEAD in any file, and where it is in history it was a published licence contact.
0d. **I broke `make check` and did not notice for several commits**, by quoting the eval's own
   failure output into two reports. See Row D.

1. **I nearly reported the Go toolchain pin as broken at HEAD.** The gate says 11 sites agree on
   1.25.13; a CI log says 1.25.14. Both are true *of different revisions*. H2, the reference frame,
   for the third time in this pass.
2. **A `&&` chain swallowed my first trimpath measurement.** `grep -c` exits non-zero when the count
   is zero, so the success case printed nothing and looked like a failed probe. Re-measured with the
   status captured separately — the same `$?`-after-a-pipe family that C1b nearly published.

---

# 12. What I did not verify

**Built from `docs/C4b_EVIDENCE_INTEGRITY_2026-09-15.md`'s companion table, NOT from §13 of the
checkpoint** — §13 has 27 rows rather than the 31 its prose claims, three are false and one partly
false, and a release verdict that cited it would send a reader to re-do four things already done.

This section is deliberately longer than the verdict. **A verdict shorter than its own list of
unknowns is a verdict that has not been read carefully enough.**

## 12.1 The release path itself — the largest cluster, and it is the one this verdict is about

| Row | State | What would settle it |
|---|---|---|
| Release **creation** | **zero observations** | a tag run that gets past `package` |
| Asset **upload** | **zero observations** | the same run |
| Checksums step | **zero observations** | the same run |
| The signing guard **in a real run** | **zero observations** — its 40-case self-test passes, which is a different claim | the same run |
| A `SIGNED` marker produced **outside a test** | **never** | a macOS runner with the five credentials |
| The `publish` job | **never run**, `if: false`, and its `marketplace` environment **404s** **(M)** | a deliberate flip, after the environment exists |
| The macOS **signed** path, and the signed-but-not-notarized path | not covered by the self-test, and the self-test **says so** | Apple credentials + a macOS runner |

**Every row above is downstream of the single step that failed in run `31505527398`.** The release
path has been exercised as far as "package two of three targets" and no further, ever.

## 12.2 Platform execution

| Row | State | Notes |
|---|---|---|
| Terminal restore on macOS / Windows | **CONFIRMED unverified** | all five `clients/tui/*_pty_test.go` are `//go:build linux` **(M)**. Needs a darwin pty helper |
| Windows named-pipe peer auth (`GetNamedPipeClientProcessId`) | **CONFIRMED unverified** | appears in no test anywhere **(M)**. The *SID comparison* half does run on every push — §13 row 3 misdescribed the mechanism |
| Windows subprocess reaping | **CONFIRMED unverified** | `daemon/mcp/procgroup_windows.go` has no test referencing it **(M)** |
| macOS `LOCAL_PEERCRED` | **VERIFIED — §13 row 5 is false** | ran green: `ok codeterminal/protocol 0.078s`, run `34837230165`, job `103953714746` **(M)** |
| The Extension Development Host | **runs in CI, 77/102 passing** — but **not at HEAD** | C2's inverted test has never executed anywhere. This is the single largest untested change in this report |
| `proxy` and `helper` on macOS | **not in any matrix** | `helper` is CGO/onnxruntime (R1.26); **`proxy`'s absence is unrecorded** |

## 12.3 Environments and load

| Row | State |
|---|---|
| `BwrapUsable`'s failing branch | **never observed anywhere** — the dev box reads 0 and CI sets 0 for itself **(M)** |
| The Docker sandbox backend | **no container has ever run.** `TestWrapCommandDockerValidation` stubs `lookPath` **(M)** |
| Behaviour under load | no harness — and Row A is a live demonstration that load changes verdicts |
| Cross-process `flock` apply/undo | implemented in `editapply/applylock_unix.go`; `editapply/applylock_test.go` **spawns no processes at all** **(M)** |
| Corruption/truncation of `memory.db`, `lexical.db`, the index | no fault-injection suite exists |
| Partial failure — step 3 failing after 1 and 2 commit | untested |

## 12.4 Fuzzing

| Row | State |
|---|---|
| Whether any input panics the MCP `go-sdk` | **CONFIRMED unverified.** `daemon/mcp` has **zero** fuzz targets **(M)** |
| Fuzz coverage of what exists | **15 of 18 targets run** **(M)**. `scripts/fuzz.sh` validates that every *listed* target exists and **not** that every existing target is listed — the `(b)` direction `gate-parity` closed for scripts, still open here |
| The three unrun targets | `FuzzIncrementalRenderMatchesFull`, `FuzzSanitizeChunkInvariance`, `FuzzSanitizeNoEscapeSurvives` — all `clients/tui`, one guarding terminal-escape sanitisation, which has a vulnerability history here |

## 12.5 Supply chain

| Row | State |
|---|---|
| The `onnxruntime` native library | scanned by nothing |
| The npm **dev** tree | 6 findings, 4 high **(M)**; see §8.10 — build-integrity surface with a trigger |
| Analysis-tool transitives | **not checked by `go-toolchain-pinned.sh`**, which is what let `bodyclose`'s undeclared `x/sys` resolve at latest and turn `main` red |
| `govulncheck` on a machine at the toolchain floor vs CI | the two scan different standard libraries; CI was clean while this machine found ten reachable vulns on the same commit **(C)** |

## 12.6 This report's own limits

| Row | State |
|---|---|
| **No CI has run at HEAD** | the newest `build`/`gates` are at `1013e1b`, many commits behind. Everything green here is local **(M)** |
| **`make check` is machine-dependent** | Row A. Same command, same commit, exit 0 and exit 2 within one session |
| The 8/9-vs-8/9 eval reading | presented both ways, **not chosen** — §8.2 |
| Whether `0925a3d` produces a *correct* win32 vsix | it fixes the *staging*; that the resulting archive is correct is asserted by `verify-vsix.js` and has **never been observed on a real run** |
| The `22.4 ms` provenance | `(U)` — Row B |

---

# 13. One sentence

> **The code is shippable and the repository is not**, and every blocker is about where the code is
> rather than whether it is right — which is the same finding this entire pass has been producing,
> arriving one last time at the release path.
