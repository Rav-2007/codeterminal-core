# Ultra Master Plan — robust first, then outperforming

**2026-08-06.** Roles: CTO · Security Checker · QA Tester.
Supersedes [`MASTER_PLAN_2026-08-05.md`](MASTER_PLAN_2026-08-05.md), whose Tracks
A and B are now closed. Its Track C (Windows) and Track D (performance) survive
here unchanged in substance and re-sequenced.

Evidence for every claim below: [`VULNERABILITY_REPORT_2026-08-06.md`](VULNERABILITY_REPORT_2026-08-06.md).

---

## Where the project actually stands

The build gate is **green for the first time in this campaign** — `make check`
exit 0, covering gofmt, vet (including `-tags eval`), `-race`, four linters, the
coverage ratchet and the errcheck ceilings.

| | 08-04 (claimed) | 08-05 | **Now** |
|---|---|---|---|
| `make check` | PASS *(on a SHA behind the tree)* | exit 2 | **exit 0** |
| P0s found & fixed | 0 | 2 | **6** |
| Tests | 998 | 1,014 | **1,055** |
| Security controls proven by execution | 0 | 4 | **17** |

That last row is the one that matters. Two campaigns recorded PASS while the
bubblewrap sandbox had **never executed once**; a third found four more P0s in
code the second had already reviewed. The gap was never diligence. It was that
tests were written one layer below where the control runs.

**So the first principle of this plan is not a feature:**

> **A security control is proven by executing it through the door the user comes
> in, or it is recorded NOT RUN. It is never recorded CONFIRMED.**

---

## Standing rules (non-negotiable)

1. **Test through the user's door.** Any test that stubs a `lookPath`,
   `exec.Command`, or filesystem seam must be paired with one that does not, or
   the control it covers is **NOT RUN**.
2. **Every fix is neuter-verified** — the fix removed, the test observed
   failing, the fix restored. Asserted, never assumed.
3. **A tool's description is a security surface.** It is what the approving
   human reads.
4. **Credentials never enter a subprocess.** `mcp.ServerEnv` or a written reason.
5. **Untrusted input gets a bound.** Every reader of a subprocess, socket or
   file has a size cap, a timeout, and a death signal. Three of the six P0s were
   a missing one of those.
6. **Ratchets only tighten.** Floors up; errcheck ceilings fail in *either*
   direction. Neither is ever relaxed to make a build green.
7. **Nothing is CLOSED by engineering.** Implemented-and-verified is the stop.
8. **Local commits only.** Publishing is the founder's decision.

---

## Track A — Security (CLOSED for this pass)

All six P0s and both P1s are fixed and neuter-verified. Detail in the
vulnerability report; summary:

| | Finding | Fixed by |
|---|---|---|
| P0-1 | `sandbox_exec` exfiltrated inference credentials | real sandbox + `ServerEnv` |
| P0-2 | bubblewrap sandbox had never executed (`--nosuid`) | flag removed + 4 executing tests |
| P0-3 | one malformed LSP header killed the whole daemon | length gate + containment recover |
| P0-4 | `Content-Length` was an unbounded allocation | 8 MiB cap, tested both sides |
| P0-5 | indexer read `~/.ssh/id_rsa` through a symlink | check moved into `shouldSkipFile` |
| P0-6 | TUI executed a binary shipped by the repo | resolver anchored off the CWD |
| P1-1 | language servers held the credentials | per-language allow-list |
| P1-2 | a silent server deadlocked the whole bridge | timeouts + `done` channel |
| P1-3 | `$HOST` silently bound a network port | local transport always |

**A-next (open):** `resolveHelperBinPath`'s CWD-relative candidate — reported,
deliberately not changed, founder's ruling. See report §5.

---

## Track B — The QA system itself (the highest-leverage work here)

Track A fixed nine defects. Track B is what stops the tenth, and it is why this
plan puts it above features.

**B1 — Adversarial harnesses, not stubs. `daemon/testdata/fakelsp` is the
model.** It is installed on `PATH` as `gopls`, found by the real
`exec.Command`, and speaks real Content-Length framing. It cannot even be
configured through the environment, because `ServerEnv` scrubs it — so it reads
its mode from `$HOME`, and *that inconvenience is the credential fix proving
itself*. Every untrusted peer deserves one:

| Peer | Harness | Status |
|---|---|---|
| Lane B MCP server | `daemon/mcp/testdata/badserver` | exists |
| Language server | `daemon/testdata/fakelsp` | **built this pass** |
| Embedder helper | `daemon/testdata/fakehelper` | exists |
| Model provider | proxy test harness | exists |
| **Filesystem (hostile repo)** | — | **missing; build it** |

That last row is the gap. P0-5 and P0-6 were both *hostile repository* attacks,
found by hand. A reusable fixture — a workspace that ships symlinks out, a
`daemon/codeterminal-daemon`, a `helper/`, an ADS-shaped name, a 4 GiB sparse
file — turns that class from "found if someone thinks of it" into a suite.

**B2 — Bound-the-untrusted audit.** Rule 5 as a checklist, run over every reader
of an external process or socket: size cap, timeout, death detection. Three P0s
came from one missing element.

**B3 — Fuzz the framing.** `readHeaders` is a hand-rolled parser over untrusted
input and is exactly what fuzzing is for. The repo already runs fuzz targets.

**B4 — Make coverage mean something.** The ratchet caught real gaps this pass
(~561 untested lines shipped). Keep raising floors as tests land; the current
values are daemon 73.6 / tui 74.5 against actuals of 74.1 / 77.6.

---

## Track C — Windows and macOS (now the critical path)

**This is the largest remaining risk in the product, and it is unchanged from
the launch plan.** Everything Windows-related is written, compiles, cross-vets
clean — and **has never executed on hardware**.

C1 Windows compiles, binds a named pipe, authenticates a peer — `df0d663`,
`1f5a1aa`, `8979530`. **NOT RUN on hardware.**
C2 Confinement under Windows path semantics — `pathhazard.go` landed and is
verified on Linux; `realPath`/`GetFinalPathNameByHandleW` still outstanding.
This is where junctions, ADS, 8.3 names and reserved device names live, and the
`.GIT` case-fold bug was a preview of the class.
**C3 — LANDED (build+vet stage).** The `cross` job runs `windows-latest` and
`macos-latest` over `daemon`, `protocol`, `editapply` and `clients/tui`.

*Build and vet only, deliberately*: a first contact that runs the whole suite on
two unproven platforms produces a wall of red in which real porting defects are
indistinguishable from environment noise. Compile+vet answers one question
cleanly — does the code the seams produce actually typecheck where it claims? —
and `vet` compiles the **test** files too, which is the half that catches a
`_test.go` reaching for a syscall the platform lacks.

It earned its place before it ever ran: cross-vetting locally found
`apply_forward_symlink_test.go` failing to compile for Windows on
`syscall.Stat_t`. Its runtime `t.Skip` could never have helped — the break is at
build time. Now constrained `//go:build unix` and labelled, because the evidence
is genuinely POSIX-shaped and Windows needs its own table (that is C2).

**C3-next — LANDED.** The `cross` job now runs `go test`, not only build+vet, so
`peercred_darwin.go`, the named-pipe transport and the Windows path seams get
their first honest look. Expect red; red here is the deliverable.

Two things were decided while landing it, both corrections to this document's
own earlier advice:

- **macOS is gated on `main`, not on pull requests.** The cost case for PR-only
  was right (macOS is 40 of the 86 billable minutes a full run costs, for four
  minutes of real work) and the mechanism was wrong: this repo merges with
  `git merge --ff-only` and pushes straight to `main`, so a PR-only gate would
  have meant macOS effectively never ran. Branch pushes get Windows; the
  mainline gets both; `workflow_dispatch` is the manual override.
- **The gate shrinks the MATRIX rather than adding a job-level `if`.** The
  `matrix` context is not available to `jobs.<id>.if`, so the obvious spelling
  would evaluate against an empty context and silently do nothing — and a
  skipped macOS job still bills if the runner is requested.

**C3-after-next:** whatever the first red run reports. `filepath.EvalSymlinks`
versus junctions is the top *unknown* in this plan, and `pathhazard.go` refuses
junctions, ADS and 8.3 aliases on reasoning about Win32 that has never met
Win32.
C4 Platform `.vsix`, bundled daemon, first-run model download.

Two Windows-specific notes surfaced by this pass:

- The symlink gate (P0-5) uses `Lstat`, which is correct on Windows for symlinks
  but **not** for junctions. C2 must re-verify it there.
- `fileURI` now handles the drive-letter and escaping cases, but its Windows
  branch is explicitly **NOT RUN** — the test says so rather than asserting a
  platform answer from Linux.

---

## Track D — Outperforming, once the base is trustworthy

Deliberately last, and the ordering is the point: **a faster assistant that
leaks credentials is worse than a slow one.** Unchanged from 08-05:

**D1 Retrieval quality** is the product. The embedding ceiling (0.0147–0.0164
raw similarity clustering) is the known limit; chunking and query expansion move
it, a bigger model does not.
**D2 The agent loop** is flat between 5 and 12 tools (measured) — spend on
*better* tools, not more.
**D3 The new tools** — AST edit, LSP definitions/references — are the real
differentiator against grep-based competitors, and they now have both security
gates and end-to-end tests through a real server.
**D4 Index freshness** gates all of it. The watcher exists and is wired, but a
stale index that does not *say* it is stale remains the worst available outcome.

---

## Enterprise QA — the gate

Phases run in order. **A phase may not be skipped because a later one is more
interesting.** Phase 1 is new this pass and is the one that would have caught
the sandbox.

| Phase | Question | Evidence that counts |
|---|---|---|
| 0 Baseline | Does it build and pass its own gate? | `make check` exit 0 **on the working tree**, not a SHA behind it |
| 1 Execution | Does every security control actually run? | executed against the real OS mechanism; a stub disqualifies |
| 2 Confinement | Can it reach what it must not? | a file outside the workspace, the network, the credentials — each attempted and refused |
| 3 Credentials | What does a subprocess see? | `env` dumped from **inside** a real child |
| 4 Bounds | What does untrusted input cost? | size cap, timeout and death signal each demonstrated |
| 5 Consent | Does the prompt tell the truth? | tool description read against handler behaviour, word by word |
| 6 Platform | Does it work where it claims? | executed on that OS, or labelled **NOT RUN on hardware** |
| 7 Regression | Does the fix fail when neutered? | neutered, observed failing, restored |

**Ship rule.** Any Phase 1–5 failure is a stop. A Phase 6 gap is shippable only
if the claim is withdrawn from the README in the same change.

---

## Sequence

1. **Now — founder decisions, no engineering:** the `resolveHelperBinPath`
   ruling (report §5); whether to commit this tree.
2. **This week — Track C3.** The CI matrix. It is cheap, and it is the only
   thing that converts a large block of "NOT RUN" into evidence.
3. **Then — B1's hostile-repository fixture**, then C2's `realPath`.
4. **Then — C4 packaging**, which is what makes the product installable.
5. **After the base is trustworthy — Track D.**

---

## Risks, stated plainly

1. **Windows and macOS are unexecuted.** Not "probably fine" — unrun. C3 is the
   answer and it is not optional.
2. **Junctions vs `Lstat`/`EvalSymlinks` is the top *unknown* security risk.**
   P0-5's fix is correct on POSIX; its Windows behaviour is untested.
3. **The hostile-repository attack surface is larger than the two instances
   found.** Both P0-5 and P0-6 were found by hand in one session, which is
   evidence about the search, not about the remainder. B1 is the mitigation.
4. **This tree is uncommitted and unpushed.** CI has never run against any of
   it; branch protection is impossible on a private free-plan repo.
5. **Estimates assume this QA discipline holds.** Neuter-verification roughly
   doubles naive estimates. It is what found six P0s in reviewed code, and it is
   worth it.
