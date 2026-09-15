# C4b — evidence integrity: what the record claims vs what runs

<!-- coderefs: enforced -->

| | |
|---|---|
| **Branch** | `audit/adversarial-pass` |
| **HEAD** | `ac29675` |
| **Tree** | clean |
| **vs `upstream/main`** | **237 ahead, 0 behind** — FF-AVAILABLE |
| **vs the pushed branch ref** | `upstream/audit/adversarial-pass` = `1013e1b`; **24 local commits unpushed** |
| **Latest `build`** | `34739694099`, commit `1013e1b`, **success**, remote **`Rav-2007/codeterminal-core`** |
| **Latest `gates`** | `34739694106`, commit `1013e1b`, **success**, same remote |

> **No CI has run on any work after `1013e1b`.** That includes all of C2, C3, C4 and this chunk.
> Everything below marked **(M)** was measured locally or read from a named CI run at a named commit.
> **"No build run at HEAD" is a distinct state from "build passed."**

Read-only except this report. Rows tagged **(M)** / **(R)** / **(U)**-with-what-would-settle-it / **(C)**.

---

## The two answers, up front

> **Can §13 be cited as it stands? No.** Of 27 rows, **three are FALSE** and **one is partly false** —
> and two of those name platform coverage that CI has been running green for months. A reader
> planning work from §13 would be sent to verify macOS peer auth, Windows SID comparison, the
> Extension-Development-Host suites and `gate-parity`'s floors, **all four of which are already
> done**. §13 is citable **only alongside this table**.

> **How many accommodations are candidate defects? One family, six sites, and it is the one C2
> already fixed.** The sweep found no second item-41-class defect. What it found instead is that the
> known family is **twice as large as C2 reported** — six sites, not three — and that the dominant
> `t.Setenv` pattern in this repository is the *opposite* shape: a planted canary proving a secret
> does **not** leak. That distinction is the sweep's main result and is stated in §Part 2.

**§13 also miscounts itself.** Its prose says the section "grew 8 → 10 → 12 → 16 → 19 → 26 → 31".
**It contains 27 numbered rows** **(M)**. The chunk that commissioned this audit inherited the 31 and
asked for 31 classifications; there are 27. *M5: a stated count ≠ a counted count.*

---

## Part 1 — the §13 companion table

**Class counts over 27 rows:** **FALSE 3** · **PARTLY FALSE 1** · **CONFIRMED UNVERIFIED 16** ·
**CONFIRMED, numbers stale 3** · **(U) 4**.

### The four rows that do not say what is true

| # | §13 says | Verdict | Evidence |
|---|---|---|---|
| **5** | macOS `LOCAL_PEERCRED`, `sun_path` 104, sockbuf, EEXIST unverified | **FALSE** | `protocol/peerauth_darwin_test.go` is `//go:build darwin` with **no `t.Skip` and no `testing.Short`** — its own comment says *"there is no third state: these tests ran and passed."* `cross (macos-latest, protocol)` runs `go test ./...` unconditionally → **`ok codeterminal/protocol 0.078s`**, run `34837230165`, job `103953714746`, commit `efc611d`, remote `Rav-2007/codeterminal-core` **(M)** |
| **14** | the 13 EDH suites and `tsc` over 27 `.ts` files unverified | **FALSE** | `build.yml` runs `xvfb-run -a npm test` on **every push to every branch**. **77 passing** at `efc611d` (job `103953714579`); **102 passing, 1 pending** on this branch at `1013e1b` (job `103677181014`). `tsc` runs twice — the `Compile (tsc)` step and `pretest`. The file count is **26**, not 27 **(M)** |
| **22** | `gate-parity`'s three vacuity floors reasoned sound, never tested | **FALSE** | `9fae051`'s own message records them neutered three ways with three distinct messages: dropping `debt-markers` from `make check` reports `ci`; stubbing `docs-links` out of `gates.yml` reports `local`; deleting the script reports the manifest entry pointing at nothing **(M)** |
| **3** | Windows peer auth (DACL + `GetNamedPipeClientProcessId`) — `crossvet` only compiles | **PARTLY FALSE** | `protocol/peerauth_check_windows_test.go` is `//go:build windows`, holds **four tests, no skips**, and `cross (windows-latest, protocol)` runs `go test ./...` on every push — so the **SID-comparison half executes**. `GetNamedPipeClientProcessId` appears in **no test anywhere** **(M)**, so that half stands |

**Row 3 is the most instructive of the four.** It is not wrong about the thing it was worried about; it
is wrong about the *mechanism* — it says `crossvet` only compiles, and forgot that `cross` also
**runs**. A row can name a real gap and still misdescribe why it is a gap, and the misdescription is
what sends the next reader to the wrong place.

### Rows that stand, with what would establish each

| # | Row | Verdict | What would settle it |
|---|---|---|---|
| 1 | no human has driven the client by hand | **CONFIRMED** | a person, at a keyboard. Nothing else |
| 2 | terminal restore Linux-only | **CONFIRMED** **(M)** | all five `clients/tui/*_pty_test.go` are `//go:build linux`, `ptysmoke_test.go` included. Needs a darwin pty helper |
| 4 | Windows subprocess reaping | **CONFIRMED** **(M)** | `daemon/mcp/procgroup_windows.go` exists; **no test** references procgroup, `JobObject` or `Setpgid` |
| 6 | `BwrapUsable` failing branch never observed | **CONFIRMED** **(M)** | `daemon/mcp/sandbox.go:128`. The dev box reads 0 and CI sets 0 for itself; needs a host with `apparmor_restrict_unprivileged_userns=1` |
| 7 | the Docker sandbox backend | **CONFIRMED** **(M)** | `TestWrapCommandDockerValidation` **stubs `lookPath`** to simulate docker. It tests argument construction; **no container has ever run** |
| 9 | whether any input panics `go-sdk` | **CONFIRMED** **(M)** | `scripts/fuzz.sh` runs **15 targets**; **`daemon/mcp` has zero fuzz targets** and none touches the go-sdk parser |
| 10 | cross-process `flock` with two real processes | **CONFIRMED** **(M)** | `editapply/applylock_unix.go` and `applylock_windows.go` implement it; `editapply/applylock_test.go` spawns **no processes at all**. The cross-process property — the entire point of `flock` — is tested in-process |
| 11 | behaviour under load | **CONFIRMED** | a load harness; no gate measures it |
| 15 | release creation and asset upload — *"a dispatch is not a tag"* | **CONFIRMED, premise corrected** | see §Part 1b — a real tag run exists and **failed** |
| 16 | a `SIGNED` marker produced outside a test | **CONFIRMED** | the only tag run never reached the signing evidence; `publish` is `if: false` |
| 17 | TypeScript and Python LSP paths | **CONFIRMED** **(M)** | no workflow installs `typescript-language-server`, `pyright-langserver` or `gopls` |
| 18 | the audit read 7% of the repository | **CONFIRMED** | a statement about a past session; unfalsifiable from here, and **H1 forbids citing the artifact as evidence about itself** |
| 21 | `helper` unobservable cross-platform | **CONFIRMED** **(M)** | not in the `cross` matrix; CGO against onnxruntime (R1.26) |
| 23 | per-gate counts do not reach the CI log | **CONFIRMED** **(M)** | `.github/workflows/build.yml:298` — `go test -race -count=1 ./...`, no `-v` |
| 24 | one `make check` run was piped through `tail -12` | **CONFIRMED** | a past event, not re-checkable |
| 25 | coverage floors not raised where environment-dependent | **CONFIRMED** — and it is a **decision**, not a gap | `helper` measures **61.5%** against a floor of **22.0** **(M)**. The floor is not raised here and is not to be |
| 26 | timing and cache-hit oracles not measured | **CONFIRMED** | stated as the weakest sweep in that pass; nothing since |
| 27 | every finding in the read-only audit is **(R)** | **CONFIRMED** | and the standing counter-example still holds |

### Rows whose numbers have drifted

| # | §13 says | At HEAD | |
|---|---|---|---|
| 19 | "320 of 321 test files were never opened" | **330** test files, **1,780** `func Test`, **18** `func Fuzz` | **(M)** |
| 14 | "27 `.ts` files" | **26** | **(M)** |
| 20 | "28,715 ignored files — `node_modules` 23,652, `.vscode-test` 6,200" | not re-counted | **(U)** — one `find` would settle it; `.vscode-test` now holds five VS Code versions locally, so the number has certainly moved |

**The direction of row 19's claim holds and its arithmetic does not.** That matters because the row is
cited as a coverage bound.

### (U) — not determined within budget

| # | Row | What would settle it |
|---|---|---|
| 8 | proxy ZDR/F1 on the wire, key auth, rate limiting, quota/outbox rows | partly addressed in CI — `proxy:FuzzZDRRoutingEnforced` **is** one of the 15 gate targets **(M)** — but wire behaviour against a live deployment needs a probe against the deployed proxy |
| 12 | corruption/truncation/absence of `memory.db`, `lexical.db`, the vector index | a fault-injection suite; none exists |
| 13 | partial failure — step 3 failing after 1 and 2 commit | a multi-step apply with an injected mid-sequence failure |
| 20 | the ignored-file count | one `find`, deliberately not spent |

**Rows not reached:** none. All 27 were classified, four of them as **(U)** with the settling test
named. The budget went instead to *depth on the rows C7 will cite* — which is where §Part 1b came from.

---

## Part 1b — the release row opened a door, and behind it is a delivery gap

Row 15's stated reason — *"a dispatch is not a tag"* — is **no longer the reason**.

**A real tag push happened.** `v0.0.1`, run **`31505527398`**, event `push`, 2026-08-11, on
**`Rav-2007/codeterminal-core`**. It is the **only** release run in this repository's history, and it
**failed** **(M)**. Zero releases have ever been published; `gh release list` is empty **(M)**.

Per job **(M)**:

| Job | Result |
|---|---|
| `binaries (linux-x64)` | success |
| `binaries (darwin-arm64)` | success |
| `binaries (win32-x64)` | success |
| **`package`** | **failure** |
| `publish (manual, founder action)` | skipped — it is `if: false` by design |

The failure, verbatim:

```
##[group]win32-x64
staging runtime into clients/vscode/daemon
stage-runtime: missing .../clients/vscode/daemon/codeterminal-daemon
  run: npm run build:daemon
##[error]Process completed with exit code 1.
```

Linux packaged cleanly first — *"21 entries · 26.2 MB unpacked · 14.5 MB on the wire / package gate
PASSED"* — so two of three targets passed before the third failed, which is why it reads like an
environment problem and is not one.

### The fix exists, is ten days old, and is not on `main`

`clients/vscode/scripts/stage-runtime.js` at HEAD keys the binary suffix to the **target**, passed as
`process.argv[2]`. At `efc611d` it keys it to **`process.platform`** — the Linux packaging host — so it
looked for a suffix-less name while the staged Windows binary was `.exe` **(M)**.

| | |
|---|---|
| Fix | `0925a3d`, 2026-09-05 — *"fix(release): stage-runtime keyed the binary suffix to the host, not the target"* |
| On `main`? | **NO** — `git merge-base --is-ancestor 0925a3d upstream/main` fails **(M)** |

**This is DELIVERY GAP instance #7, and it is the release path.** The script's own comment already
describes the failure I read out of run `31505527398`; the repair has been sitting on this branch for
ten days while `main`'s release path stays broken for `win32-x64`.

**Recipient: the owner, for C7.** Two consequences: (a) the release path on `main` is broken today and
the merge fixes it; (b) **row 16 cannot close until a tag run gets past `package`**, because the only
one that ever ran never reached the signing evidence.

---

## Part 2 — the accommodation sweep

### The distinction the sweep turned on

The dominant `t.Setenv` pattern in this repository is **not** an accommodation. **Sixteen** lines
across **seven files** plant a credential whose own value says what it is for — `CANARY`,
`must-not-leak`, `must-not-reach-the-child`, `must-not-escape` **(M)** — in
`daemon/mcp/mcp_test.go`, `daemon/mcp/stdioclient_test.go`, `daemon/mcp/interop_test.go`,
`daemon/mcp_exec_test.go`, `daemon/lsp_bridge_test.go`, `daemon/twoworkspaces_test.go` and
`clients/tui/slash_test.go`. Those supply a secret **in order
to prove it does not travel**. The test would be worthless without it.

> **An accommodation supplies something so the code under test can PROCEED. A canary supplies
> something so a leak can be DETECTED.** They look identical to a grep for `t.Setenv`, and only the
> second is legitimate. Sorting the two is most of the work in this kind of sweep.

### The table, ranked by blast radius

| Site | What it supplies | Would a user have it? | Defect it accommodates |
|---|---|---|---|
| `daemon/twoworkspaces_test.go:179` | `CODETERMINAL_API_BASE` via `daemonEnv` | **No** | **item 41** — fixed in C2 |
| `daemon/startuprace_test.go:242` | same helper | **No** | **item 41** — *not named in C2's report* |
| `daemon/startuprace_test.go:310` | same helper | **No** | **item 41** — *not named in C2's report* |
| `clients/vscode/src/test/suite/daemonRealSpawn.test.ts:47` | `CODETERMINAL_API_BASE` + `_API_KEY` inline | **No** | **item 41** — its comment names the defect |
| `clients/vscode/scripts/e3-pilot-test.js:147` | `CODETERMINAL_API_BASE` | **No** | **item 41** |
| `.github/workflows/build.yml:294` | `CODETERMINAL_REQUIRE_SANDBOX=1`, daemon only | **No** | **inverse** — see below |
| `daemon/proxy_seam_test.go:175` | `OPENROUTER_API_KEY`, `SUPABASE_URL`, `SUPABASE_SERVICE_ROLE_KEY`, `ALLOWED_MODELS`, `PORT` | **Yes** | none — the proxy is a deployed service configured by environment; requiring it is correct |
| `clients/tui/*_pty_test.go` | `TERM=xterm-256color` | **Yes** | none |
| `clients/vscode/src/test/stubDaemon.ts` | `XDG_RUNTIME_DIR` | **Yes** | none — test isolation |
| `clients/vscode/src/test/suite/localCommandsHostile.test.ts:247` | a fake daemon dir prepended to `PATH` | n/a | none — hostile-input fixture |
| 6 eval tests (`daemon/*_eval_test.go`) | **skip unless** `CODETERMINAL_API_BASE`/`KEY` are set | **No** | none — a deliberate spend decision, but **those six functions have never executed in CI** **(M)** |

**C2 reported three item-41 accommodation sites. There are six.** `daemon/startuprace_test.go` uses the
same `daemonEnv` helper at two more call sites, and C2's report did not name them. Correcting my own
count: **six (M)**, all now benign because the daemon defaults.

**`build.yml:294` is the inverse and worth its own line.** `CODETERMINAL_REQUIRE_SANDBOX=1` is set for
the `daemon` module **only**, and no user sets it. It exists because the bubblewrap sandbox tests
**skip themselves**, so a green run looked identical to one where they never executed — the failure
shape `protocol/peerauth_darwin_test.go`'s comment cites by name. So CI is *stricter* than reality
here, which is the right direction, but it means **the path a user actually runs is the less-tested
one**, and the flag is applied to one module out of six.

### No second item-41-class defect was found

The obvious candidate was the TUI, left as **(U)** in C2's report. Settled here:

> **`clients/tui` does not spawn the daemon at all** and never reads `CODETERMINAL_API_BASE` **(M)**.
> It connects to an already-running daemon, and a TUI user is in a shell where exports exist. The one
> binary it does spawn — the daemon, for `/mcp list` at `clients/tui/slash.go:410` — inherits the
> environment, and before C2 that call would have failed for a GUI-launched user for the same reason.
> **It is a second beneficiary of the C2 fix, not a second defect.**

**NEGATIVE RESULT: there is no TUI twin of item 41.** C2's (U) row closes.

---

## Three findings the sweep produced that were not in scope

1. **The fuzz gate's list is one-directional.** `scripts/fuzz.sh` validates that every **listed**
   target exists — it runs `go test -list` per entry and fails if one is missing. It does **not**
   check that every existing target is listed. Three are not **(M)**:
   `FuzzIncrementalRenderMatchesFull`, `FuzzSanitizeChunkInvariance` and `FuzzSanitizeNoEscapeSurvives`,
   all in `clients/tui`. **15 of 18 targets run.** This is the exact `(b)`-direction gap
   `gate-parity.sh` was built to close for scripts, still open for fuzz targets — and one of the three
   guards terminal-escape sanitisation, which has a vulnerability history here.
2. **24 commits are unpushed**, so no CI has seen C2, C3, C4 or this chunk. A `reach.sh` instance
   before `reach.sh` exists.
3. **§13 miscounts itself** — 31 claimed, 27 present.

## Premises that did not hold

| Premise | Outcome |
|---|---|
| "§13 is a 31-row list" (the chunk) | **27 rows** **(M)**. The chunk inherited the section's own wrong self-count |
| "Row 14 is the false entry" (the chunk) | Row 14 is false **and so are rows 5 and 22**, with row 3 partly false. **Four, not one** |
| "at least one accommodation may be another item-41-class defect" (the chunk) | **Not found.** The TUI candidate dissolved. The finding is that the *known* family is twice the reported size |

## Self-corrections

1. **C2's report said three accommodation sites. There are six** — `daemon/startuprace_test.go` uses
   `daemonEnv` at two further call sites. My C2 sweep grepped for the *variable name* and found the
   sites that mention it literally; these reach it through a helper. **The instrument was a literal
   grep where the question was about a call graph** — H2, and the same lesson C3 learned when a
   `Handler:` grep would have dropped three inline closures.
2. **I first wrote "nineteen" canary sites. It is sixteen.** Nineteen was the line count of a grep
   whose pattern was broader than the claim — it swept in `A_SERVERS_OWN_TOKEN` and a deliberately
   "asked-for-loudly" key, neither of which is a canary. Caught by re-counting on the value rather
   than the call, before this document was committed. **H2, and the same shape as `head -8` reported
   as a cardinality.**
3. I nearly classified row 5 as CONFIRMED on the strength of its own prose. What changed it was
   opening `protocol/peerauth_darwin_test.go` and finding a comment that states, in advance, exactly
   what green proves and why there is no third state. **The row was refuted by a file that had
   anticipated the question.**

## Verification

| Check | Result | |
|---|---|---|
| working tree | clean | **(M)** |
| `./scripts/docs-coderefs.sh` | exit 0 | **(M)** |
| product code changed | **none** — read-only chunk | **(M)** |
