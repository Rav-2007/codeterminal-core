# Production readiness — Mochiii / codeterminal-core, 2026-09-21

<!-- coderefs: enforced -->

| Field | Value |
|---|---|
| remote | **`Rav-2007/codeterminal-core`** (canonical). `fork` is `Rav-i24/Mochiii`, where CI does not run |
| branch | `main` |
| HEAD sha | `2f4160e` — *release: v0.0.3, and five README claims that were false* |
| tree state | clean |
| local vs upstream | in sync — `main` == `origin/main` == `2f4160e`, 0 ahead / 0 behind |
| latest `build` run | `35567566808` at `2f4160e` — **success, 27/27 jobs** |
| latest `gates` run | `35567566830` at `2f4160e` — **success** |
| latest `release` run | `35569233157` at tag `v0.0.3` (`2f4160e`) — **success**. Draft release, **10 assets**, `published=null` |
| rehearsal before it | `35568490369` — dispatch on `main`, **success**: `staged 6 standalone binaries`, 10 `WOULD ATTACH`, 0 `EXCLUDED`, 0 `darwin` |

**"No build run at HEAD" is a distinct state from "build passed."** Where this
report says a thing is green it names the run that made it green, and where no
run exists at a commit it says so instead of inheriting the previous one's
verdict.

## What this is, and what it is not

A production-readiness verdict across five axes at once: the gate system, the
security model, the agent loop and orchestration, the QA surface, and a
per-surface ship/hold call. It was asked for as *"is everything clean and precise
for production"*.

**It changes nothing.** No gate was added, no open item fixed, no floor raised
while this was written. That is deliberate: a readiness verdict produced by doing
work becomes a report about the work, and the question was about the system as it
stands. Every number below is counted from the tree at the HEAD in the header or
read out of a named run — none is estimated, and none is carried forward from an
earlier report.

Prior audits covered single axes and remain the deeper source on each:
`docs/ENTERPRISE_QA_REPORT_2026-08-05.md`, `docs/VULNERABILITY_REPORT_2026-08-06.md`,
`AGENT_SECURITY_AND_CAPABILITY_AUDIT.md`, and the method itself in
`docs/ENGINEERING_METHOD.md`. This one is the join across them.

---

## Axis 1 — the gate system, and where it cannot see

**18 `check` targets, 26 shell gates, 16 of them running both locally and in CI.**
`scripts/gate-parity.sh` accounts for every one: 16 both, 2 local-only, 1 CI-only,
7 manual, and **each asymmetry carries a written reason**. That accounting is
itself a gate, so the set cannot drift silently.

### The distinction that matters: property gates versus enumeration gates

A gate that asserts a **property** fails on a case nobody thought of. A gate that
enumerates **paths** passes on every case its author did not list. The repository
contains both and does not always distinguish them, and that is the single
largest structural weakness in the gate system.

The case study was paid for this week, at a cost of one unusable release:

> `check:installpath` exists precisely to catch "a value the daemon needs that no
> user can supply". Its first three elements cover the API *base* exhaustively —
> contributed, machine-scoped, actually read. It was **green for the entire time a
> packaged install could not authenticate**, because the check was written around
> `contributes.configuration` and a credential is not a setting. Its own stated
> principle — *"a setting the user can change with no effect is worse than no
> setting at all"* — had never been applied to the one value without which nothing
> works.

That is an enumeration gate wearing a property gate's name. A fourth element now
covers the credential, and its neuter reproduces the pre-fix command list
verbatim, so the gate would have failed the v0.0.2 release had it existed. **But
the class is not closed**: the next value the daemon needs and nobody can supply
will be missed the same way, because the gate still enumerates values rather than
deriving them from what the daemon reads.

### What the gate system does unusually well

- **Ten gates carry a `--self-test`, totalling ~150 arms** — `docs-claims`,
  `fuzz`, `gate-parity`, `go-toolchain-pinned`, `reach`, the three release
  guards, `target-parity` and `verify-vsix.js`. Counted this run:
  release-signing-guard **58**, release-branch-guard **20**,
  release-version-guard **16**, target-parity **14**, docs-claims **10 cases**,
  reach **6**, verify-vsix **26** across five categories. Every one of those arms
  exists only to prove the gate can still fail — none of them checks the product.
- **Twelve gates carry a vacuity tripwire** — a check that fails when it discovers
  it checked nothing. `scripts/govulncheck.sh` refuses to report a clean scan when
  it covered fewer than six modules, and treats an unreachable database as a
  failure rather than a pass. The release signing guard fails when the set of
  targets needing a signature is empty *without* a declaration saying it should
  be.
- **The suite states its own blind spots out loud.** `gate-parity.sh --what-ci-adds`
  runs on every green `make check` and prints what that green does
  *not* promise: real Windows execution, the Extension Development Host, the proxy
  container, and the 2000-turn unraced soak. `docs-coderefs.sh` prints, on
  success, that it does not check prose, anchors, section numbers, or any citation
  written without a `:line` suffix.

### What the gate system cannot see

| Blind spot | Consequence |
|---|---|
| No gate runs the product end to end as a user installs it | Both v0.0.2 defects — no key path, no published daemon — were invisible to 18 green gates and 1,799 passing tests |
| The EDH suite cannot run on a developer machine here (`xvfb-run` absent) | CI is its only execution; a developer cannot get that verdict before pushing |
| `media/main.js` is 1,789 lines with static analysis and no runtime test | 28% of the extension renders security decisions, checked by `checkJs` only |
| Coverage floors are per-package, not per-file | A file can lose all coverage inside a package that stays above its floor |

---

## Axis 2 — the security model

The product runs a local daemon that indexes the user's source, an optional
terminal client, an optional proxy, and an agent loop that can execute code and
reach the network. The trust boundaries are drawn where they should be, and the
interesting part is that most of them are enforced by something that fails.

### Boundaries, as built

| Boundary | Mechanism | Standing |
|---|---|---|
| Who may talk to the daemon | `protocol.AuthorizePeer` over `SO_PEERCRED`, on **both** sockets — the daemon's and the helper's | Enforced. Moving `peerauth` into `protocol/` is what let one implementation cover both |
| The model-provider key vs. subprocesses | `ForbiddenEnvNames` (`daemon/mcp/mcp.go:241`) plus allow-list construction in `ServerEnv` (`daemon/mcp/mcp.go:307`), `LimiterEnv` (`daemon/mcp/sandbox.go:169`), and `helperEnv` (`daemon/helperproc.go:237`), which passes `PATH` and `HOME` only | Enforced on all four subprocess paths. Nothing in `ForbiddenEnvNames` is grantable by configuration |
| The user's source vs. the wire | `scrub()` — a **structural** allow-list of vendor credential formats (`daemon/scrub.go:43`), not entropy or keyword matching | Enforced, and deliberately narrow. D5 rejected entropy/keyword redaction on measured false-positive grounds: 33% of chunks, zero precision |
| Untrusted text vs. the model | A tag-family registry; page text is defused **before** wrapping, and attributes are no longer interpolated raw | Enforced. The ordering is the whole fix — neutralising after wrapping neutralises the fence too |
| Third-party tools vs. the user's trust | `Confined` is **false for every Lane B tool, unconditionally** (`daemon/mcp/mcp.go:56`) | Enforced by refusing to be optimistic. That value is rendered on the approval prompt a human reads, so it may never overstate |
| Prompts vs. a data-retaining provider | `zdrRoutingEnforced` (`proxy/main.go:2237`), the F1 gate | Enforced, verified on the wire (`403 zdr_required`), and **fuzzed** — a false positive there forwards a request that should have been refused |

### The pattern worth naming

Six of those boundaries are enforced by a **refusal**, not by a flag. The daemon
fails closed on an unauthorised peer; the signing guard fails the release rather
than shipping fewer platforms than the tag implies; `govulncheck.sh` fails rather
than reporting a scan it could not perform; `Confined` reports false rather than
guessing. This is the difference between a security model and a security
configuration, and it is the strongest single thing about this codebase.

### The six open residuals, and whether any blocks a release

None is High. All are CONFIRMED by measurement rather than by reading, which is
why the severities are trustworthy.

| # | What | Sev | Blocks v0.0.3? |
|---|---|---|---|
| 32 | Language-server consent fires per-turn against an already-running server, not at spawn (`daemon/lsp_bridge.go:271`) | 4.5 | **No.** The consent is real, the *timing* is wrong. Needs plumbing from the bridge back to the consent channel |
| 37 | `sandbox_exec` HOME cache is never reclaimed (`daemon/mcp_exec.go:125`) — measured 1,709 directories / 232 MB over eight days | 4.0 | **No.** Disk growth on a local cache, not a security hole. Test-isolation half already fixed |
| 24 | The audit log records the decision, not the effect (`daemon/toolaudit.go:1`) | 3.8 | **No** — open *by documented design*, recorded so the limit is visible before an incident rather than during one |
| 35 | No idle eviction for cached language servers — a live `gopls` measured at 295 MB RSS | 3.0 | **No.** Resource ceiling, deliberate cache, alternative is paying startup per lookup |
| 34 | `web_fetch` can build an envelope larger than `max_tool_result_bytes`, truncating past `</web_content>` | 2.5 | **No.** An unterminated fence puts *more* text inside it, not less — degrades integrity without granting authority |
| 36 | `keyPrefix` returns the whole key for ≤8-byte input while its comment says it never does (`proxy/main.go:1623`) | 1.5 | **No.** An 8-byte string is not a usable credential. What is wrong is that a stated invariant is false in the function whose job is that guarantee |

**Verdict on this axis: no open security finding blocks the release.** Item 36 is
two lines and should be fixed on principle — a function that exists to make a
guarantee should not break it — but it discloses nothing.

---

## Axis 3 — the agent loop and orchestration

**4,280 lines of core** across `daemon/agentloop.go` (1,166), `daemon/mcpbuiltin.go`
(636), `daemon/orchestrator.go` (546), `daemon/mcp/sandbox.go` (627),
`daemon/mcp/stdioclient.go` (578), `daemon/mcp/mcp.go` (528) and
`daemon/planmode.go` (199). All of it is on `main` — not a branch.

### The capability model

Tools declare what they *do*, not what lane they came from: `Confined`,
`ExecutesCode`, `ReachesNetwork`, `LaunchesSubprocess`. The design note in
`daemon/mcp/mcp.go:56` states the rule that makes it trustworthy — a flag that
reaches the approval prompt may never be optimistic — and the codebase has twice
been bitten by the alternative:

- `sandbox_exec` told the user "confined" when it was not. Measured, then fixed.
- The plan-mode filter tested `ExecutesCode` **alone** while its comment claimed
  it keyed on capability, so `web_search`/`web_fetch` (`ReachesNetwork`) survived
  a mode that promised read-only. Three further routes were found by auditing the
  *closure* rather than the code, including a prose SEARCH/REPLACE block that
  bypassed the tool filter entirely and was written to disk under a command whose
  summary reads *"read-only: no edits"*.

That second one is the most instructive defect in the repository: **four
independent routes to the same violation, three of them found only because
someone re-audited a fix that had already been marked closed.**

### Three strengths of evidence, and the repo knows the difference

| Evidence | What it proves | Where |
|---|---|---|
| Unit tests | The code does what the author meant | `agentloop_test.go`, `orchestrator_test.go`, `registry_test.go` |
| `-tags eval` suites (10 of them) | Retrieval and orchestration quality against a corpus | `agentloop_eval_test.go`, `orchestration_ab_eval_test.go`, `orchestrator_eval_test.go` |
| A live run against a real model | What five scripted passes missed | The 2026-08-26 orchestration run: **four defects that scripted testing did not find** |

The honest reading: the agent loop is well tested against *specified* behaviour
and thinly tested against *emergent* behaviour, and the repo has measured exactly
how large that gap is — four defects in one live run. Scripted evidence is not a
substitute here and the codebase does not pretend otherwise.

---

## Axis 4 — QA reality

**Counted at this HEAD, not estimated:**

| | |
|---|---|
| Go, non-test | 129,212 lines total; daemon 28,751, tui 5,339, proxy 4,563, editapply 4,130 |
| Go test functions | **1,799** |
| Fuzz targets | **18** — proxy 7, daemon 4, editapply 4, tui 3, protocol 0, helper 0 |
| TypeScript | 5,094 lines source; **117 tests passing** in the Extension Development Host, measured in run `35567566808` (16s). `media/main.js` a further 1,789 lines |
| `-tags eval` suites | 10 |
| Docs | 69 markdown files |

### Coverage floors: tight in seven places, loose in two

| Package | Measured | Floor | Headroom |
|---|---|---|---|
| `daemon` | 78.9% | 78.0 | +0.9 |
| `daemon/mcp` | 91.9% | 91.0 | +0.9 |
| `editapply` | 89.6% | 88.0 | +1.6 |
| `proxy` | 86.1% | 85.0 | +1.1 |
| `protocol` | 90.1% | 89.5 | +0.6 |
| `clients/tui` | 84.5% | 84.0 | +0.5 |
| **`helper`** | **60.1%** | **22.0** | **+38.1** |
| **`helper/helperproto`** | 100.0% | 75.0 | **+25.0** |

Seven floors sit within 1.6 points of measured — a regression that deletes real
coverage turns the gate red. Two do not: **`helper` could lose 38 points and stay
green.** Named here and deliberately not changed, because raising a coverage floor
is this repository's standing prohibition; it is the owner's call.

Every `errcheck` ceiling is **at** ceiling — daemon 91, proxy 25, helper 10,
editapply 9, protocol 0, tui 0 — which is the right shape: zero headroom means
one new unchecked error fails the build.

### The thing no test covers

No test in this repository installs the product and uses it. Both v0.0.2 defects
lived in that gap. `daemonRealSpawn.test.ts` closed part of it — it found item 41
on its first run, because nothing had ever started the real daemon from the
extension — and the remaining part is the owner's acceptance test.

---

## Axis 5 — the verdict, per surface

| Surface | Verdict | What would change it |
|---|---|---|
| **VS Code extension** | **Ship, with one caveat** — the caveat is that no human has installed this build. Every automated gate passes, including the first-ever execution of the credential suite | The owner's acceptance test: install the `.vsix`, set a key, ask a question needing retrieval, apply an edit and undo it |
| **Daemon** | **Ship.** 78.9% covered against a 78.0 floor, race-clean, 0 reachable vulnerabilities, peer-authenticated, fails closed on an unauthorised peer | — |
| **Terminal client (TUI)** | **Ship** — and for the first time it is *usable*, because v0.0.3 publishes the daemon it needs. v0.0.2 shipped a client that could only print "daemon not found" | — |
| **Proxy** | **Ship for its current role.** ZDR enforcement verified on the wire and fuzzed; rate limiting, model cost-authz and BOLA checks in place. It is the only network surface and the only component with an internet attack surface | Item 36 (two lines). Not a disclosure, but a false invariant |
| **Release pipeline** | **Ship, once the rehearsal passes.** It has published from a tag exactly once, and the 2→6 binary change has never been executed by an attach step | The dispatch rehearsal, then the tag asset count |
| **macOS** | **Do not ship.** Deliberately absent: no Apple signing secrets exist (`total_count: 0`), so every darwin binary is marked unsigned and the guard fails the release. Intel Macs are a separate gap — upstream onnxruntime ships no `darwin/amd64` binary at all | Apple Developer enrolment and five secrets |
| **Marketplace** | **Not shipped, by design.** The `publish` job is deliberately un-wired: marketplace publication is irreversible in practice, so there must be no automatic path from a merge to a listing | A founder decision, plus notarised macOS binaries |

### The three things a founder should know before publishing

1. **No human has run this build.** That is not a gap in the gates; it is the one
   thing gates structurally cannot supply, and it is exactly where both v0.0.2
   defects lived.
2. **Windows binaries are unsigned and SmartScreen will warn.** This is declared,
   not overlooked — `scripts/release-targets.txt` says `signing-required: none`
   and the guard carries a vacuity tripwire that fails the release if that
   declaration ever goes missing.
3. **`main` has no required status checks.** Branch protection became *available*
   when the repository went public and is still unconfigured, so CI cannot
   actually block a bad merge. Owner action.

---

## Hardness and uniqueness

Most codebases with this much testing are tested *broadly*. What is unusual here
is a set of disciplines aimed at a narrower question: **how does a green check
lie?** Each of the following exists because it failed to, once.

**1. A gate not demonstrated failing is not demonstrated.** Ten gates carry a
`--self-test`; the release signing guard alone runs 58 arms. When a gate is
changed, the change is *neutered* — deliberately broken — and the gate must go
red. `target-parity.sh` grew from 10 arms to 14 this way. The industry norm is to
write the assertion and trust it.

**2. Vacuity tripwires.** Twelve gates fail when they discover they checked
nothing. `govulncheck.sh` refuses to report clean having scanned fewer than six
modules; the signing guard fails when the set needing signature is empty without
a declaration. This defends against the most common silent failure in CI: a check
that passes because it matched zero things.

**3. Derive, do not enumerate (H10).** Earned by a real defect: a release step
asserted `[ "$N" -eq 3 ]` against a loop that had been edited down to two targets,
so **a tag would have published nothing on any platform**. The count is now
accumulated in the loop body, and `target-parity.sh` check 5 fails any literal
count threshold in the workflow — *enforced by absence*, which is a rarer and
stronger construction than enforcing a value.

**4. The delivery gap has its own gate.** `scripts/reach.sh` asks the question no
other gate asks: *did the work arrive?* Every other gate passes on a branch nobody
pushed. It examines 306 items against `origin/main`, and each of its 8 exemptions
names **the event that retires it** — not a suppression, a scheduled expiry. It
exists because the answer was no seven times, each found by accident: a lint pin
fixed on a branch while `main` stayed red for weeks; 40 commits on a stale local
ref; an eval fix green across three runs and never delivered. It is deliberately
local-only, because a CI clone has no local branches and would pass by being
irrelevant.

**5. Local and CI are kept symmetrical, and the asymmetries are enumerated.**
`gate-parity.sh` accounts for all 26 scripts and all 18 `check` targets, and every
asymmetry carries a reason. On every green run it prints what that green does
*not* promise.

**6. Registers must agree, in both directions.** `docs-claims.sh` fails the build
when `BACKLOG.md`, `docs/README.md`, `docs/OPEN_ITEMS.md` and
`docs/DECISION_PACK.md` disagree about which items are open or which decisions
are taken. Documentation drift is a build failure here. That is rare enough to be
worth stating plainly.

**7. M5 — two states meaning different things never share a phrase.** *Written* ≠
*committed* ≠ *delivered*. *Green on a branch* ≠ *green on main*. *A stated count*
≠ *a counted count*. Each pair in `docs/ENGINEERING_METHOD.md:408` was added
because conflating them cost something real.

**8. The register records what was wrong about the register.** `docs/OPEN_ITEMS.md`
carries rows documenting its own past errors — item 41 sat OPEN for five days
after being fixed because nobody re-read the lines the row itself cited; item 12's
summary contradicted its own detail table ten lines below for six days. A register
that logs its own failures is a different instrument from one that logs only the
code's.

**The cost, stated honestly.** `make check` takes ~12 minutes; a push takes ~8
more before anything leaves the machine; the race suite alone is ~7 minutes. This
discipline is expensive, and `docs/ENGINEERING_METHOD.md:270` already says so.

### What this is not

It is not a mature *product*. It is a rigorously engineered system that has never
been installed by a stranger, has no marketplace listing, no macOS build, no
required status checks on its release line, and one measured live-model run
behind its agent loop. The engineering discipline is unusual; the product surface
is early. **Those are different axes and this report does not average them.**

---

## What this report does not cover

Per `docs/ENGINEERING_METHOD.md:391`, the limits are stated rather than left to be
discovered:

- **Nothing here was re-measured by running the product.** Numbers are counted
  from the tree or read from named runs.
- **No open item was re-verified.** The six residuals are reported at the severity
  and evidence the register already carries; this report did not re-run their
  repros.
- **The retrieval quality axis is untouched.** H6 is open, recall is recorded
  elsewhere, and the `-tags eval` suites are schedule-gated.
- **No claim is made about the proxy's deployed state.** The proxy source is
  audited here; what is running is a separate question with a separate answer.
