# `audit/adversarial-pass` — release status

<!-- coderefs: enforced -->

**Written 2026-09-05. Branch `audit/adversarial-pass`, at `a959945`. Working tree clean.**

**For a reader who has not followed this work and is deciding whether to release.**
Everything below is extracted from the repository — the register, the readiness
statement, the two handoffs, commit messages, gate scripts, and CI run
conclusions read back from GitHub. Nothing is reconstructed from memory. Where a
number appears in more than one place and the values disagree, the disagreement
is reported rather than resolved.

## The short version

The branch is **117 commits ahead of `main`**. It carries two distinct passes:

| | Commits | Covered by | Scope |
|---|---|---|---|
| Adversarial re-verification | 55 (`3655730`…`94f699e`, to 2026-09-03) | `docs/ADVERSARIAL_PASS_2026-09-03.md` | mostly `daemon/` (51 files), `clients/`, `proxy/`, `editapply/` |
| Terminal-client hardening | 62 (`e335fad`…`a959945`, 2026-09-03 → 09-04) | `docs/TUI_PRODUCTION_READINESS_2026-09-04.md` + `docs/RESIDUAL_RISKS.md` | `clients/tui` |

**Sections 1–8 below cover the second pass.** The readiness statement's scope
line reads `clients/tui`, so the first 55 commits are *not* described by it, by
the register, or by the release-gate table. If you are deciding whether to
release the whole branch rather than the terminal client, that earlier report is
a separate read and is not summarized here.

**CI is green.** `gates` run `33923375561` at HEAD — success, both jobs, no
skips. The last commit carrying **code** is `0925a3d`: `build` run `33922411677`
— 27 jobs, 26 success, **0 failed**, 1 skipped (`retrieval eval (scheduled)`,
schedule-triggered). **There is no `build` run at HEAD and that is not a pass** —
the commits after `0925a3d` are markdown-only and `build.yml` carries
`paths-ignore: ["**.md"]`, so the workflow did not run at all.

**Two release-gate rows are open, and both are blocked on something smaller than
they look: nobody has been told they exist.** *(Corrected 2026-09-05.)* The
decision memo and the manual-session script were **committed to this branch and
handed to no one.** No `daemon/` owner has been named; no tester has been asked.
Neither row is waiting on a reply — **both are waiting on delivery**, and the
action available today is supplying two names, not chasing two people. Once
delivered: **a recorded deferral with a trigger closes either row exactly as well
as a fix does.**

**One stated budget is knowingly unmet:** a repaint at the transcript ceiling
costs 22.4 ms p50 against an 8 ms repaint budget and against a 16 ms hard
per-Update ceiling. Measured, recorded as R1.12, deliberately not optimized.

**The first release ships `linux-x64` and `win32-x64` only.** Ruled 2026-09-05 by
the founder. **`darwin-arm64` is deferred** until the five `MACOS_*` signing
secrets exist — found on 2026-09-05 by running `release.yml` for the first time,
which produces **unsigned** darwin binaries and warns *"Do not publish this
target."* Recorded as R1.15, **deferred by decision rather than blocked**.

**Two things to hold next to that platform set.** First, **the machinery does not
yet implement it**: on a real tag the release would still attach two unsigned
darwin assets, because the upload globs sweep them in. A fix is proposed and
deliberately unbuilt, pending the release owner. Second, **the two platforms that
ship are the two least directly tested at the terminal** — Windows ships with a
signal contract that is two no-op stubs (correct: the platform has no SIGHUP or
POSIX SIGTERM), and Linux is the only shipping platform whose terminal restore is
tested at all. Deferring macOS removes this pass's largest coverage hole; it adds
nothing to either platform that remains. See Section 4 and *The release
rehearsal* in Section 5.

---

# SECTION 1 — WHAT CHANGED

62 commits. **(D)** marks a deterministic quantity — allocation counts, ratios,
counts, code constants — which reproduces exactly on any machine. **(W)** marks
wall-clock, which varies run to run and is reported rather than gated. That split
is the readiness statement's own rule and is applied to every number below.

## Task 2.1 — terminal-escape sanitization

| Commit | Defect | Invariant it protects | Pinned by | Measured effect |
|---|---|---|---|---|
| `e335fad` | Measured before writing: `\x1b[2J`, `\x1b[H`, OSC 0 (window title), `\x1b[1;1r` (scroll region), bare `\r`, OSC 8 and a BEL flood **all reached the terminal unchanged** through the real transcript path | Allowlist, reject-by-default: only `CSI…m` survives, and only with enumerated parameters. Stateful across chunks, because the far end picks the split points. Bounded 64 B CSI / 256 B string payload | `TestHighlightedCodeRendersByteIdentically`; 43-entry corpus | Filter only — nothing wired, so filter and behaviour change revert separately **(D)** |
| `9109df3` | Every untrusted display path was unfiltered | Sanitize **at ingest**, so `m.turns` is one clean source for screen, history and anything added later. **Two parsers** for answer and reasoning, so a sequence opened in one cannot be closed by the other — a bypass built out of our own multiplexing | wiring suite | One-shot mode covered too; two surfaces (edit-review, approval) filtered at render instead, to keep bytes byte-exact for disk and for the daemon's approval digest **(D)** |
| `948314f` | Two allocation sources per escape sequence on the per-token path | Behaviour-preserving; pending buffer is a fixed array, so no two copies of a model share held bytes | 43-entry corpus, chunk-invariance, both bounds tests, both fuzzers | highlighted token 1833 ns/216 B/**23 allocs → 828 ns/24 B/1 alloc**; hostile 361 ns/28 B/4 → 210 ns/16 B/1; clean 73 ns/0 B/0 unchanged **(D for allocs, W for ns)** |
| `2daa3bf` | Every other test inspects `View()`, which is not proof bytes reached a screen | Filter proven at a real pty, hostile daemon streaming **one byte per message** | pty test | Neutered: OSC 0, OSC 8, OSC 52, DECSTBM and SGR 8 all reach the terminal → all filtered. Truecolour SGR and answer text survive **(D)** |
| `2c0231c` | Three appends still carried unfiltered bytes into `m.turns` — approval outcome line, daemon `Detail`, and the review summary's refusal reason formatting a **model-authored path** with `%s` | `appendTurn` is the only door into the transcript | `TestC1InAnEditPathCannotReachTheTranscript` | Live route: `editapply.RejectUnprintablePath` refuses `r < 0x20`, DEL and named Unicode, **but not C1 (U+0080–U+009F)**, and U+009B is the 8-bit CSI introducer. Neutering `appendTurn` fails 5 of 6 new tests **(D)** |
| `bf09951` | Two byte-slices must stay byte-exact and are filtered only at render — the third render path someone adds will not remember | AST guards over the package's own source: only `appendTurn` may append to `m.turns`; the set of functions reading the raw buffers is fixed and each is annotated with who checked it | `TestRawByteStructuresHaveNoNewReaders` + a synthetic-source detector test so a guard that stopped detecting cannot pass vacuously | Verified against the real package with a temporary offending function **(D)** |
| `ac78205` | On overflow the parser resumes at the offending byte; a resume offset one byte late re-enters mid-escape — the exact bypass the ceiling exists to prevent | Boundary pinned by enumerated shape, not by random search | `TestSGROfLengthGeneratesWhatItClaims`; 14 shapes, all seeded into both fuzzers | Boundary measured exactly: **62 parameter bytes fit (65 total), 63 overflow (66)** **(D)** |

## Exit paths

| Commit | Defect | Invariant | Pinned by | Effect |
|---|---|---|---|---|
| `4fb5e06` | Bubble Tea v1.3.4 registers SIGINT and SIGTERM only. Measured at a real pty: those two each emit **57 bytes** of restore; **SIGHUP emitted zero**. The process died with the alternate screen up, cursor hidden, mouse tracking on. SIGHUP is how an ssh drop and a closed window arrive | Both signals funnel into the library's own graceful shutdown, so exactly one code path restores a terminal. Exactly-once and deadlock-freedom are structural, not mutex-guarded | pty suite | ~200 ms signal-to-exit **(W)**; three SIGHUPs produce exactly one restore sequence **(D)**. SIGQUIT keeps its dump — re-raising was tried and measured not to work (exited 0, silently) |
| `5a9269a` | Measured: `codeterminal-tui --prompt … \| head` exited **141** — killed by SIGPIPE. Reading the first lines of an answer is ordinary | One-shot installs the handler; the chat UI deliberately does **not** — it owns a pty and there is no broken-pipe failure to prevent | in-process EPIPE test + real binary into a real pipe | Exit 0, nothing on stderr. Removing `ignoreSIGPIPE` fails the second test on "was KILLED by broken pipe" **(D)** |
| `d4f6865` | Bubble Tea's panic report prints **after** the terminal is restored, so escape-shaped output reaches a live terminal; the two raw buffers could carry a secret from a tool argument | No `panic()` in non-test source | `TestNothingPanicsInNonTestCode` | Reproduced Bubble Tea's exact formatting across a genuine runtime panic with both buffers live: neither the hostile marker, the planted key, nor any ESC byte appears — Go's traceback prints pointers and lengths, not contents. The test also asserts the report stays **useful** **(D)** |
| `dc7c2c8` | A test fixture's raw-string `func` at column 0 was scanned as text by the daemon's chunking heuristic and turned two daemon tests red | — | `TestTheHeuristicAgreesWithTheCompiler`, `TestConstructExtentsNeverStopShortOfTheCompiler` | **Self-correction recorded in the commit**: these were first reported as pre-existing and not ours. That check was invalid — `git stash` without `-u` left the untracked fixture on disk **(D)** |

## Tasks 3.1–3.4 — measurement, determinism, the render cache

| Commit | Defect | Invariant | Pinned by | Effect |
|---|---|---|---|---|
| `b5a7dd8` | No instrument existed | Allocations are asserted; wall-clock is reported with the distance to budget printed and asserted only at 3× | benchmark harness | Baseline allocs/token **23 / 203 / 705 / 1368** at 0/30/120/240 prior turns — a **59× ratio (D)**. Two earlier versions of the timing assertion flapped in both directions: the same 1 MB paste gave medians of 6.3, 13.6, 14.5, 16.6 ms **(W)** |
| `7f207c4` | Lip Gloss detects colour lazily and freezes it at the **first** render anywhere in the process, so emitted bytes depended on *when* that happened | The profile is resolved once, at one named point | determinism suite | Same state, same width, one process each: `CLICOLOR_FORCE` set before the first render **382 bytes**, after it **291** — 91 bytes apart, decided by whether something else drew first **(D)** |
| `f4deea8` | The suite's colour depended on the developer's environment | Render is a function of state, width and one pinned profile | 14 environments × 4 profiles, subprocess matrix | Before the pin the matrix produced **three distinct outputs** (291 / 382 / 436 bytes); after, identical bytes **(D)** |
| `def2cdc` | "Assert allocations, report wall-clock" was a comment at the top of one file, which makes it a preference | Structural: parses the package's own test sources, finds every package-level `time.Duration`, fails if its file never mentions `raceEnabled`. Keys on **type**, not name, so a budget in a new file is caught whatever it is called | `TestTimingBudgetsAreGuardedAgainstTheRaceDetector` | Refuses to pass vacuously if no budget is declared **(D)** |
| `7cf320e` | — | A cache of blocks is sound only while the transcript is exactly the concatenation of its parts | `TestATranscriptIsExactlyTheConcatenationOfItsTurns` — 4 roles, with and without reasoning, empty turns, 6 widths from 1 to 200 | Behaviour-preserving **(D)** |
| `86540c3` | Every streamed token re-rendered the whole conversation through Lip Gloss: linear per token, **quadratic per answer**, unbounded, inside `Update` | **Validated, not invalidated.** Each entry stores the inputs it was rendered from; a mutation nobody thought of yields a cache miss, not stale text. O(1) per turn | allocation gates | allocs/token at 0/30/120/240/400: **23/195/692/1353/2234 → 18/25/27/28/29**; growth **97× → 1.6×**. The "before" column is what these same tests report with the cache **neutered (D)** |
| `51747b3` | Nothing gated the streaming path; one such leak is already in this repo's record (`streamPrompt` parking forever on a send to a UI that stopped reading) | Snapshots goroutine **IDs**, not counts — IDs are unique and never reused, so "started since the snapshot and still running" is a statement a count cannot make. Polls to a deadline rather than sampling once | leak gate, itself tested in both directions | Written rather than taking a dependency on `go.uber.org/goleak` (~80 lines) **(D)** |
| `eb93a69` | `refreshViewport` ended in an unconditional `GotoBottom()` and runs on **every streamed token** — scrolling back through an arriving answer was not awkward but **impossible** | Follow the stream only for a reader already at the bottom; ask on every refresh rather than latching, so scrolling back down resumes following. Read **before** `SetContent`, which changes what "the bottom" means | three tests, deliberately separate | Measured before: eight wheel-up events reach `YOffset` 187, and one token puts it back at the bottom. Neutered both ways — always-follow fails the first test, never-follow fails the other two **(D)** |
| `2e32b75` | Even with the cache, every token re-wrapped the whole transcript and rebuilt the viewport line index — both linear in bytes. Tokens arrive faster than a terminal shows them | At most one repaint per 16 ms, at most one tick outstanding. `endStream` repaints unconditionally — the trailing-token hazard | `TestManyTokensBetweenTicksProduceOneRepaint` | At 240 turns before: `wrapToWidth` 2.69 ms + `SetContent` 0.61 ms against a 0.5 ms budget. After: token alone **p50 1 µs / p99 44 µs**; token + forced repaint **p50 3.36 ms / p99 5.39 ms** — both reported, because reporting only the first would let deferred work read as removed **(W)** |
| `4591790` | A cache can be quicker and wrong at once; wrong here means showing text the model did not send, with nothing to signal it | `cache.render(turns,width) == renderTranscript(turns,width)` byte for byte | 200 seeds × 40 mutation steps under ordinary `go test` (so inside the `-race` gate), across all four profiles, plus a fuzz target seeded from the sanitizer's 43-entry corpus driving payloads through `appendTurn` | 225,000 executions clean at 30 s. Generators cover CJK, ambiguous width, ZWJ emoji, regional indicators, combining marks, zero-width and non-breaking spaces, tabs, embedded newlines, a 300-character unbroken token, a long URL, empty turns; widths 0–200 **(D)** |

## Tasks 3.5–3.10 — bounds, structure, operability

| Commit | Defect | Invariant | Pinned by | Effect |
|---|---|---|---|---|
| `c2f72f8` | `m.turns` only ever grew; nothing trimmed it but an explicit `/compact` | **Two ceilings**, because either alone leaves a hole: count-only lets 50 KB answers hold 25 MB inside "500 turns"; byte-only lets thousands of one-line activity turns accumulate. An unparseable or too-small env value is **ignored, not honoured**. **Eviction is never silent** — one marker turn accumulates across evictions and counts against the ceiling itself | soak | Before, at 2,000 turns of ~1.2 KB: 1.2 MB turn text, 6.3 MB heap, **30 MB peak RSS against 8.6 MB idle**. Defaults `defaultMaxTurns = 500` (`clients/tui/transcriptbound.go:48`) and `defaultMaxTranscriptBytes = 2 << 20` (`clients/tui/transcriptbound.go:54`) **(D)** |
| `832dc24` | 1 MB paste: 0 turns median **17.1 ms**, 240 turns **15.6 ms** — over/straddling the 16 ms ceiling. **And a second, more serious defect not in any row**: the prompt box had silently dropped everything past 4,000 characters since it was written | Bound runs as the first statement of the `KeyMsg` case; the header says how many characters arrived, were kept, and were dropped | `TestNoMoreRunesReachTheInputThanCanBeKept` (deterministic — a rune count, not a stopwatch) | The cost was not where it was assumed: every branch of the key handler called `msg.String()`, so a megabyte was stringified **twice** before the input saw anything. Bounding after that point measured no improvement at all **(W)** |
| `6e2d277` | `Update` was 324 lines carrying nineteen message types | Mechanical move: every case body verbatim, dedented one tab, no reordering | AST guards | `Update` **324 → 62 lines (D)**. `TestRawByteStructuresHaveNoNewReaders` **failed immediately** — the function storing the raw approval request used to be called `Update` — and passed once its reviewed-list entry moved with it |
| `dec7a42` | Five unchecked `Close()` calls | Ceiling 5 → 0; the reasoning lives once on `daemonSession.Close`, and states explicitly that introducing a buffered writer makes all of it false | errcheck ratchet | **And a gate defect found by a broken neuter check**: `scripts/errcheck-ceiling.sh` counted lines with stderr discarded, so a module that would not build scored a perfect zero. Now fails closed **(D)** |
| `d58f93f` | `WithMouseCellMotion` silently costs the terminal's own click-drag selection — invisible until it bites, and for a coding assistant reads as "I cannot copy out of this program" | `/mouse`, a named toggle reporting the new state in terms of what it means; the idle hint carries the **symptom**, not the mechanism | `TestTheMouseToggleIsDiscoverable`, asserted on the `Cmd` rather than the flag | Absolute build paths in a binary **678 → 0** via `-trimpath` + `scripts/supply-chain.sh` **(D)** |
| `0a464b3` | Three acceptance targets were named and asserted nowhere | Resize storm is the worst input the cache has — the cache never hits | acceptance suite; coverage floor 83.0 → 84.0 | Resize storm at 240 turns over 104 resizes: median **4.19 ms**, p99 7.02 ms, worst 7.42 ms **(W)**. An 800-token answer at 400 prior turns allocates **30,452 against 24,314** with no history — **1.25×**, inside a 2× target **(D)**. File descriptors flat across 25 turns **(D)** |

## Gates, CI and release mechanics

| Commit | Defect | Invariant | Pinned by | Effect |
|---|---|---|---|---|
| `a10d785` | Two confirmed fail-opens made a class, so **all twelve gates** were probed against four questions: empty target set, module does not build, missing tool, and whether "inspected N, found 0" is distinguished from "inspected 0" | Fail closed | the gates themselves, neutered | **Nine were already sound. Three were not.** `scripts/fuzz.sh`: a target that does not exist reported `ok` — the hole that let both TUI sanitizer fuzzers go unrun from task 2.1. `scripts/supply-chain.sh`: a module with no Go files produced a silent skip and exit 0. **Debt markers: the gate did not exist** — a manual grep over one module of six. Re-run repo-wide: **156 non-test files, 0 markers (D)** |
| `9583377` | R1.12 said a repaint was unbounded and merely rate-limited — written *before* the transcript had a ceiling, so nobody had measured the bounded case | `ceilingTranscript` **fails the test if it did not reach the ceiling**; the tick test fails if the repaint it is timing did not happen | `TestRepaintCostAtTheTranscriptCeiling`, `TestRepaintAllocationsAtTheCeilingAreBounded` | At 490 turns / 2,096,839 bytes / 512 evictions: **p50 22.4 ms, p99 29.9 ms, spread 1.9× (W)**. Deterministic half: **39 allocations** for a token plus its repaint, against **2,734** with the cache neutered **(D)** |
| `5a6ed29` | The release workflow did not build the terminal client at all, so this document's own "CI green once" condition named nothing | Built on all three release runners with `-trimpath`; in the macOS signing list; asserted **out** of the `.vsix` by the packaging gate | `verify-vsix.js --self-test` | Two defects found by **running** the loops rather than reading them: `cp -a artifacts/…/. daemon/` would have swept the binary into every package, and `ls a b \| head -1` under `set -euo pipefail` dies on a missing `.exe` candidate **before** reaching its own error message **(D)** |
| `3c217f2` | Five test files are `//go:build linux`; on macOS and Windows `go test ./...` printed `ok` while that behaviour was never compiled, and **nothing said so** — the fail-open shape the gate audit removed, wearing a build tag | On Linux a **gate** (every listed suite must carry the tag; no tagged suite may go unlisted); elsewhere a **report** naming what did not run, in that platform's own log | `TestPlatformCoverageIsStated` | Neutered three ways: a listed suite losing its tag, an undeclared linux-only file appearing, and the inspection loop disabled **(D)**. *See Section 8, item 1.* |
| `0c4cf2c` | Re-reading the register found **two of its four code references drifted** — R1.7 cited `slash.go:299-305` for an echo now at 319, R1.5 cited `396-397` for a `CombinedOutput()` at 411 | Opt-in per document by a marker line; each reference now names its **function** as well as its line | `scripts/docs-coderefs.sh`, neutered four ways | Caught two ambiguous refs in the decision memo on its first run. Prints the count **not** checked — 162 across 15 unenforced documents — so the gap is visible rather than excluded **(D)** |
| `aff280f` | **Windows CI**: go-winio's pipe listener keeps one pipe pending and creates a **replacement** per connection, so every connection looked like one leaked goroutine — deterministically, and only on Windows | Match on the **creator** line, never on the stack. A leaked *client* goroutine shows the same `asyncIO` frame — matching on that would hide real leaks | `TestHarnessFilterIsNarrowerThanTheLibrary`, which proves the distinction on every platform | Rejected the alternative fix (close the listener first) because it would hide a real product leak **(D)** |
| `e1e8819` | **Extension CI**: daemon exited 1 three times — `reading config ./models.json`. `npm run compile` does not run `stage-runtime.js` | Config staged in the test's own setup | real-spawn test | **Green locally, red on a runner, for the oldest reason there is**: a developer tree has the file left beside the daemon from an earlier build. Reproduced rather than reasoned **(D)** |
| `3ecd0a6` | **Linux CI**: the package timed out at 10m0s with the soak at **7m38s**. The soak drives 2,000 exchanges through `Update` in one goroutine — no concurrency for the detector to examine — and instrumenting it costs **9×** (29.9 s unraced → 270.9 s raced locally) | Shortening it alone would have weakened the gate, so it is not all: the raced run does 400 exchanges (150 past where the turn ceiling engages) **and** a second, unraced, full-length run was added. Both run on every push | soak, at both lengths | The "evicted nothing" assertion applies to both lengths, so a shortened soak that stopped reaching the ceiling fails rather than passing quietly **(D)**. 270.9 s → 36.9 s raced **(W)** |
| `5bf497a` | A **branch** went green without macOS, so a macOS break surfaced after the merge, on the one module a user runs in a terminal | A whole job, **not** a path filter — a filter naming the signal/pty files is an enumeration, and this repo has been burned by that shape twice | push run `33901690615` | Two corrections to the premise, both from evidence: macOS was **not** held by memory (the `cross` matrix expands on `refs/heads/main`; run `33620636112`, four macOS jobs green), and adding macOS everywhere **would not buy what it looks like it buys** — the pty suites are `//go:build linux` at any trigger **(D)** |

## Corrections and the release rehearsal (2026-09-05)

Four commits added after this summary's first draft, three of them fixing claims
the summary itself caught and one fixing a defect the release rehearsal found.

| Commit | Defect | Invariant it protects | Pinned by | Measured effect |
|---|---|---|---|---|
| `6ff3fd1` | Four documented claims wider than the code: the 73/20 assertion; a security table that could not distinguish *decided* from *awaiting a decision*; item 9's exec counts stated as a measurement; and "26/27", which reads as a failure | A document's claim about a gate is checked against the **gate's code**, not against another document or a commit message | — (prose) | The security-table split is the load-bearing one: it is the document a decision-maker reads alone, and it previously implied nothing was outstanding **(D)** |
| `10b84fa` | `build.yml`'s fuzz comment said "Fifteen targets over three trust boundaries"; `scripts/fuzz.sh` holds **18 over four**, and the comment had dropped `clients/tui` — the boundary this pass exists around | The list is authoritative; the comment says so and carries a date | **Nothing — deliberately.** Gating one English number word in one YAML comment is a gate whose own failure mode is the thing it watches for | Second drift in the same comment; its own text warns about the first **(D)** |
| `0925a3d` | **Found by running the workflow.** `stage-runtime.js` keyed the binary suffix to `process.platform` — the **host** — but `release.yml` assembles all three `.vsix` packages on one Linux runner, so packaging `win32-x64` looked for a Unix name. **Pre-existing on `main` (`4453825`)**; it survived because release.yml had never run | The target is passed in, exactly as it already is to `verify-vsix.js` one line below. The host is a fallback, so local `npm run build:runtime` is unchanged. An unrecognised target is rejected rather than silently treated as non-Windows | Six-way neuter against a simulated staging tree | **Neutered form reproduces the CI line exactly**: no target → `stage-runtime: missing …/codeterminal-daemon`, exit 1. Wrong target → exit 1, so the argument is load-bearing. Unix files with no argument → exit 0, local dev unchanged **(D)** |

The remaining commits in this range are documentation: `ff0e303`, `5dae428`,
`15f3543`, `2abcb57`, `661c99c`, `1ca8f22`, `7c1cde0`, `3d0b70b`, `d841188`,
`212be6b`, `1959898`, `dbb80b5`, `882dc29`, `a207432`, `28df3d0`, `f4d8ca7`,
`08d5432`, `d210b56`, `9fffadc`, `5ba607d`, `ec21d8c`, `96bb34c`, `2bca3e1`,
`b49ac59`, `a959945`; plus `afb7a65` (coverage floor 82.0 → 83.0).

---

# SECTION 2 — THE HEADLINE NUMBERS

**The neutered-cache column is what makes the improvement claim checkable, so it
is a column and not a footnote.** "Before" here is not history recovered from an
old commit: it is what these same tests report *today* when the render cache is
disabled. That check was done before any improvement was claimed (task 3.2f) and
re-done after the `Update` refactor.

## Per-token allocations, by transcript depth

| Prior turns | Before / cache neutered | After | Provenance |
|---|---|---|---|
| 0 | 23 | **18** | (D) |
| 30 | 195 | **25** | (D) |
| 120 | 692 | **27** | (D) |
| 240 | 1,353 | **28** | (D) |
| 400 | 2,234 | **29** | (D) |
| **growth across that range** | **97×** | **1.6×** | (D) |

The ratio is the acceptance signal, not the absolute count: if it does not go
flat, the prefix is still being rebuilt whatever wall-clock says. The gate
asserts the ratio, not a constant — `TestPerTokenAllocationsAreFlatInTranscriptLength`.

**A second, independent allocation figure at the ceiling**, from the repaint
measurement: **39 allocations** for a token plus the repaint it causes, against
**2,734** with the cache neutered (D). That is the number that actually gates
this path, because it is machine-independent.

**Two baselines for the same measurement disagree.** The 3.1 benchmark harness
(`b5a7dd8`) recorded **23 / 203 / 705 / 1368** at 0/30/120/240 prior turns; the
render-cache commit's neutered "before" column (`86540c3`) records
**23 / 195 / 692 / 1353** at the same depths — lower by 8 to 15 allocations.
Both are stated as measured, the difference is not explained anywhere in the
repository, and neither figure is load-bearing: the gate asserts the **ratio**,
and 97× versus 1.6× is not in question. See Section 8, item 7.

## The repaint at the transcript bound — the one budget knowingly unmet

Every other performance figure in this work was taken at 240 prior turns of
1.2 KB — about 300 KB, **one seventh of what the bound permits**. Multiplying by
seven would have been arithmetic. Measured instead, at a transcript sitting at
**both** ceilings at once (490 turns, 2,096,839 bytes, after 512 evictions):

| | Value (W) | vs the 8 ms repaint budget | vs the 16 ms hard per-Update ceiling |
|---|---|---|---|
| p50 | **22.4 ms** | 2.8× — no headroom, −14.4 ms | **1.4× — breached** |
| p99 | **29.9 ms** | 3.7× — −21.9 ms | 1.9× — breached |
| min / max | 21.9 / 42.3 ms | spread 1.9× | |

**The spread is reported because the median alone would mislead.** A 1.9× spread
sitting entirely above both lines is a different result from one that straddles
them — the paste measurement straddled 16 ms at 2.6× and was correctly recorded
as "no reliable headroom" rather than as a breach. **This one does not straddle:
every one of 200 samples was over the 8 ms budget, and the minimum was over
16 ms.**

Where it goes, medians, cache warm: **`wrapToWidth` 17.6 ms (79%)**,
`viewport.SetContent` 4.5 ms (20%), `transcriptCache.render` 334 µs (1.5%).
Neither of the first two is ours and neither takes an incremental update. A cold
repaint — cache empty, as after a resize — is 31–47 ms.

**Disposition: R1.12 stays OPEN, not fixed, deliberately.** It is one repaint,
not a queue — coalescing means the next cannot begin until 16 ms after this one
ends, so nothing accumulates and the loop stays responsive. The cost is input
latency at the bound (a keystroke waits up to 22 ms rather than 3 ms), plus CPU
and battery. It is not correctness and it is not a stall. The two real fixes —
per-block wrap caching, or replacing the viewport — are each larger than this
pass. **Two cheap trades exist and neither was taken:** `refreshInterval`
16 ms → 33 ms halves the sustained cost, and lowering the 2 MiB byte ceiling
moves the figure proportionally. Both are written into R1.12's trigger.

The gate on this path fails only on a **3× regression against today's cost**,
not against the budget — the budget is knowingly unmet, and a permanently red
gate is indistinguishable from no gate within a week.

## The transcript bound

| | Before | After |
|---|---|---|
| Turn text at 2,000 turns of ~1.2 KB | 1.2 MB | bounded |
| Heap in use | 6.3 MB | **3.1 MB** local / **2.9 MB** on a runner |
| Peak RSS | **30 MB** against 8.6 MB idle | bounded |
| Ceiling | none | **500 turns / 2 MiB**, whichever binds first (D) |
| On eviction | — | a marker turn, accumulating, counted against the ceiling itself |

**Reproduced on a runner**, which is the point of the unraced soak step:
*"2000 exchanges: 502 turns kept, 335 KB of text, 502 cache blocks, heap-in-use
2.9 MB, evicted 3499 turns / 2.2 MB"*, in 37.18 s. `502` rather than `500` is
R1.11 — the ceiling is enforced at the start of a turn, so it can be overshot
within one; the soak asserts `len(m.turns) <= 500+2`.

## Everything else on one line each

| Property | Before | After | Held by | Provenance |
|---|---|---|---|---|
| 1 MB paste, worst input measured | 15.6 ms median | **389 µs**, identical at 0 bytes and at the 2 MiB ceiling, spread 1.1× | `TestNoMoreRunesReachTheInputThanCanBeKept` (a rune count, not a stopwatch); `TestOneMegabytePasteIntoACeilingTranscript` | W (the gate is D) |
| Oversized paste | silently truncated at 4,000 chars | reported: arrived / kept / dropped | `TestAnOversizedPasteSaysWhatItDropped` | D |
| Scrolling back during a stream | impossible — snapped to bottom every token | works, and resumes following | three separate tests | D |
| Mouse text selection | silently disabled, undiscoverable | `/mouse`, named in the idle hint | `TestTheMouseToggleIsDiscoverable` | D |
| Unchecked errors | 5 | **0** | ceiling ratchet, which no longer fails open | D |
| Absolute build paths in a binary | 678 | **0** | `scripts/supply-chain.sh`, `-trimpath` | D |
| Rendering determinism | 3 distinct byte strings for the same state | **1** | 14-environment × 4-profile subprocess matrix | D |
| Goroutines / file descriptors | ungated on the streaming path | flat across completed, interrupted and reset turns; flat across 25 turns | leak gate, FD gate | D |
| Coverage, `clients/tui` | floor 82.0 | **84.5%** measured against a floor of **84.0** | `scripts/coverage-ratchet.sh`, `scripts/coverage-floors.txt` | D |
| `-race` suite wall time | 376 s | **154 s** | — | W; the 376 s figure is explicitly superseded, taken before the soak's cost was understood |

---

# SECTION 3 — SECURITY

**Three states, and collapsing them misleads:**

- **FIXED** — a change landed and a test fails without it.
- **OPEN BY DECISION** — someone decided not to fix it, the reasoning is
  recorded, and a trigger says what reopens it. This is a closed decision, not an
  outstanding task.
- **PENDING SOMEONE ELSE'S DECISION** — nobody has decided. The analysis is
  done; a person has to write a sentence. **These are the only ones that block
  anything** — and, corrected 2026-09-05, they are blocked one step earlier than
  that: **the memo carrying them has never been delivered to anybody.**

| Issue | State | Exploit scenario | Fix | Verifying test |
|---|---|---|---|---|
| **Terminal escape injection** | **FIXED** (task 2.1) | Model or MCP output repaints the approval prompt — forged consent, not a display bug | Allowlist sanitizer, reject-by-default, one ingest door (`appendTurn`) | 43-entry corpus + 14 CSI shapes + 2 fuzzers + a real-pty test, **all registered in the fuzz gate for the first time** |
| **C1 introducer in a model-authored edit path** | **FIXED** client-side (task 2.2); **upstream gap still open** | `editapply.RejectUnprintablePath` refuses `r < 0x20`, DEL and named Unicode **but not C1 (U+0080–U+009F)**. U+009B is the 8-bit CSI introducer. A model names a file whose path carries one; it parses cleanly, survives every upstream check, and reaches a `%s` in the review summary's refusal reason | Sanitization at `appendTurn`, the only route into `m.turns`. Stands on its own; does not depend on `editapply` changing | `TestC1InAnEditPathCannotReachTheTranscript` (`clients/tui/sanitize_wiring_test.go:438`). **Re-verified 2026-09-04**: it runs rather than skips, so the upstream gap is still open. It self-skips only if `editapply` closes it. Neutering fails it immediately, with the C1 byte visible |
| **Panic path leaking secrets or escapes** | **FIXED** (task 2.2d) | Bubble Tea prints a panic value and stack **after** restoring the terminal, so escape-shaped output reaches a live terminal; the two raw buffers may hold a tool argument containing a key | Reproduced Bubble Tea's exact formatting across a real panic with both buffers live: nothing leaked. Residual (a panic **value** built from untrusted data) is enforced away | `TestNothingPanicsInNonTestCode` fails on any `panic()` added to non-test source |
| **Edit-review and approval buffers hold raw bytes** (R1.4) | **OPEN BY DESIGN** | Deliberate: edit bytes go to disk and approval bytes carry the daemon's digest, so both must stay byte-exact. Sanitized at render instead. The risk is a **new** reader rendering them raw | Structural — an AST guard names every function allowed to touch them | `TestRawByteStructuresHaveNoNewReaders`. **It fired during 3.7**: the refactor moved the reader out of `Update` and the test failed until the reviewed list moved with it |
| **One-shot stdout no longer byte-stable** (R1.3) | **OPEN BY DECISION** | A pipeline depending on control sequences passing through breaks. The promise was wrong before it was broken: an escape written to a file executes the moment anyone `cat`s it | Filtering is deliberately unconditional; gating on `isatty` would leave the sequence armed for tomorrow's reader | `TestOneShotOutputIsFiltered`; `TestRealBinaryPipedIntoAnEarlyReaderExitsCleanly` (**Linux only**) |
| **Over-long CSI leaks parameter bytes as text** (R1.2) | **OPEN** | Cosmetic. **No ESC survives by either route** | — | `TestOverLongCSIBoundary`, `TestHeldBytesNeverExceedTheCeiling`; boundary exactly 65/66 |
| **`git status` failures echo git's output** (R1.7) | **OPEN** | A remote URL with an embedded credential appears in an error line. `runGitStatus`, `clients/tui/slash.go:305` | **None** | **None** |
| **`ModelError.detail` safe by field privacy** (R1.8) | **OPEN (forward guard)** | Not a finding. `daemon/modelerror.go:204` builds `detail` safely today; nothing enforces that it keeps doing so | None — it is a property, not a mechanism | **None.** An AST guard on `Detail()`'s callers is the obvious mechanism and is not built |
| **Two dependency surfaces scanned by nothing** (R1.13) | **OPEN** | `govulncheck` covers six Go modules and nothing else. The **onnxruntime native library** and the **extension's npm tree** are scanned by no gate | None | **None.** Stated as a coverage gap |
| **`/mcp-server` shows daemon + MCP stderr unredacted** (R1.5) | **PENDING DELIVERY — no recipient identified** | A third-party MCP server prints its API key at startup; `runMCPServerList` (`clients/tui/slash.go:381`) runs `mcp list` with `CombinedOutput()` (`clients/tui/slash.go:411`) and the key lands on screen and in scrollback. `daemon/mcpruntime.go:111` wires each server's stderr into the same stream. Only path in the client that puts daemon stderr in front of a user. **Disclosure-to-owner, not exfiltration** — it becomes serious when pasted into a ticket or a screen recording, which is exactly when someone runs it | **None yet.** Memo recommends **FIX**, in the daemon: `mcp.Connect` already computes `ServerEnv(cfg.EnvAllow)` and so holds the literal bytes handed to the subprocess, making exact-match stripping available there and nowhere else. ~40 lines | **None.** A gap in coverage as well as in behaviour, stated plainly |
| **Model-emitted secrets in the transcript** (R1.6) | **PENDING DELIVERY — no recipient identified** | The scrub is asymmetric: the daemon scrubs **outbound** prompts and reports what it removed; nothing scrubs **inbound** model text. A model reads a `.env` through a tool and quotes it back — it lands on screen and is sent back as history. Bounded by the fact that the model had to read it first, via a tool call the user approved | **None.** Memo recommends **ACCEPT IN WRITING**: `redactionsMsg` matches on **shapes**, not on values the daemon provisioned, so "apply the same set inbound" means running ten regexes over prose — which the brief rules out and which decision D5 already refused on measured data (33% of chunks, zero precision) | **None.** Recorded as an accepted gap |
| **The outbound scrub is bypassed by one turn** | **PENDING DELIVERY — no recipient identified** — and this one is a **defect**, not a design question | The daemon redacts `sk-…` from the prompt **and tells the user so**. `daemon/server.go:687` then persists `promptReq.Prompt` — the **raw** string — into `memory.db` and its `turns_fts` index, and `prepareHistory` (`daemon/history.go:136`) does not scrub, so the secret reaches the provider verbatim inside the next turn's history. **The redaction notice makes it worse than never scrubbing**: the user reasonably concludes the key did not leave | **None yet — not the terminal client's to make.** `cleanPrompt` at one call site plus a scrub in `prepareHistory`. **No new detector and no new judgment** — it applies a decision the product already made | **None.** Found 2026-09-04, verified by execution twice, probes removed afterwards. Not previously recorded in any register |

**The source document now matches this table.** Until 2026-09-05 the readiness
statement's own security table used the phrase *"None. Unfixed by decision"* for
R1.5 and R1.6 — the phrase the register uses for a genuinely closed decision — so
a reader of that document alone would have concluded nothing was outstanding. It
now leads with the same three-state column used here. See Section 8 item 8.

**Scope note.** These findings are **carried forward** from earlier passes and
were not re-audited in this batch, which was performance, correctness and
operability. The two exceptions, both re-probed on 2026-09-04, are the C1
finding (`RejectUnprintablePath` re-run directly: U+009B, U+0080 and U+009F all
**accepted**; `0x1B`, `0x01`, `0x7F` refused) and the outbound-scrub bypass,
which was found during this batch.

---

# SECTION 4 — WHAT IS VERIFIED, AND WHERE

**A skip is never written as a pass.** "0 run — 14 not compiled" is the correct
form; a bare "0" is not, and neither is silence.

## Per platform

| Platform | Builds | Test suite | pty-backed tests | Signal / terminal-restore tests | What triggers it |
|---|---|---|---|---|---|
| **Linux** | pass | **327 pass**, under `-race` | **14 pass** | **12 pass** | **every push** — job `go (clients/tui)` |
| **macOS** | pass | **313 pass**, no `-race` | **0 run — 14 not compiled** | **6 pass, 6 not compiled** | **every push** — job `macos (clients/tui, every push)`; plus `cross` on `main` and on dispatch |
| **Windows** | pass | **307 pass**, no `-race` | **0 run — 14 not compiled** | **0 run — 12 not compiled** | **every push** — job `cross (windows-latest, clients/tui)` |

Test **files** compiled per platform — verified against the repo today with
`GOOS=<os> go list -f '{{len .TestGoFiles}}' ./clients/tui`, which reproduces the
readiness statement's figures exactly: **Linux 57, macOS 52, Windows 51** (D).
Fuzz targets: 3 on each. `go vet ./...` and `go test -c` succeed on each.

**Windows's zero is correct; macOS's six is the gap.** On Windows,
`exitsignals_windows.go` is two no-op stubs, because the platform has no SIGHUP
and no POSIX SIGTERM — there is no behaviour there to verify, which is why those
tests are `//go:build !windows` rather than missing. **macOS runs the same
`exitsignals_unix.go` as Linux and gets half its tests**: the six in
`exitsignals_test.go` run there (SIGHUP reaching the quit function, repeated
signals quitting exactly once, finish-during-signal, the goroutine dump, SIGPIPE
install/restore); the six in `exitsignals_pty_test.go` do not — every exit path
restoring the terminal, the 57-byte sequence, double-SIGHUP, SIGHUP during
startup, SIGHUP after the terminal is destroyed, and R1.1's known gap.

**That is a build-tag gap, not a trigger gap, and no scheduling change touches
it.** Allocating a pty is per-kernel (Linux `TIOCSPTLCK`/`TIOCGPTN`, the BSDs
`TIOCPTYUNLK`/`TIOCPTYGNAME`), so the helper is Linux-only and the five files
using it do not compile elsewhere at **any** trigger. **They cannot hang** —
the code is not in the binary, so there is no timeout to misread as flake.

**The 14 macOS does not run, all of them about the terminal itself:**

| File | Tests | Therefore unverified on macOS |
|---|---|---|
| `ptysmoke_test.go` | 2 | the real binary in a real terminal on a real socket |
| `exitsignals_pty_test.go` | 6 | terminal restored on every exit path; the SIGHUP gap (R1.1) |
| `renderprofile_pty_test.go` | 4 | the 14-environment × 4-profile determinism matrix |
| `sanitize_pty_test.go` | 1 | escape filtering measured at an actual terminal |
| `brokenpipe_pty_test.go` | 1 | an early reader closing the pipe underneath |

Windows does not run those 14 either, **plus** `exitsignals_test.go`'s 6 — 20
fewer in total — and gains `testaddr_windows_test.go` in exchange.

## What triggers what — so a green push tells you what it actually covers

| Check | Every push | `main` only | Dispatch only | Tag only | Needs a human |
|---|---|---|---|---|---|
| `clients/tui` Linux, `-race`, full suite | ✅ | | | | |
| `clients/tui` **macOS** build + vet + 313 tests | ✅ (since `5bf497a`) | | | | |
| `clients/tui` Windows build + vet + 307 tests | ✅ | | | | |
| `daemon`, `protocol`, `editapply` on **macOS** | ❌ | ✅ | ✅ | | |
| Transcript soak, 400 exchanges raced | ✅ | | | | |
| Transcript soak, 2,000 exchanges unraced | ✅ | | | | |
| Fuzz gate, 18 targets at 30 s | ✅ | | | | |
| `gates` workflow (docs, registers, links, coderefs, debt markers) | ✅ (no `paths-ignore`) | | | | |
| `build` workflow on a **markdown-only** push | ❌ — `paths-ignore: ["**.md"]` | | | | |
| **`release.yml`** — terminal-client build, 3-in-3-out staging, packaging gate | ❌ | ❌ | ✅ **run 2026-09-05, green** | ✅ | |
| **`release.yml`** — release creation and asset upload (tag-gated) | ❌ | ❌ | ❌ **did not run on dispatch** | ✅ | |
| **macOS signing and notarization in substance** | ❌ | ❌ | ❌ **dry-run: no secrets** | ❌ | ✅ someone must add the secrets |
| Terminal restore / the 57-byte sequence on macOS or Windows | **never — not compiled** | | | | ✅ (a darwin pty helper) |
| Anyone driving the client by hand | | | | | ✅ |

**The markdown skip was verified in both directions against this branch's own
runs**, not reasoned about, because `paths-ignore` on a multi-file push is
commonly misread — **it is evaluated over every file changed in the push, not
per commit**:

| Push | Files changed | `build` | `gates` |
|---|---|---|---|
| `3ecd0a6..d210b56` | `build.yml` **+ two `.md`** | **ran** — `33898773089`, success | ran, success |
| `d210b56..9fffadc` | one `.md` only | **no run exists** | **`33900470717`, success** |

A mixed push does not skip. `9fffadc`, the last markdown-only commit on this
branch, is verified by `gates` alone — and `gates` is green.

## CI conclusions, read back from GitHub today

| Run | Commit | Event | Jobs | Success | Failed | Skipped |
|---|---|---|---|---|---|---|
| `33914724042` `build` | `a959945` (HEAD) | push | 27 | 26 | **0** | 1 |
| `33901690615` `build` | `5bf497a` | push | 27 | 26 | **0** | 1 |
| `33896704671` `build` | `3ecd0a6` | workflow_dispatch | 30 | **30** | **0** | 0 |
| `gates` | `a959945` | push | — | success | — | — |
| `33922411676` `gates` | `0925a3d` (HEAD) | push | — | **success** | — | — |
| `33921667448` `release` | `a959945` | dispatch | 5 | 3 | **1** | 1 |
| `33922431985` `release` | `0925a3d` (HEAD) | dispatch | 5 | 4 | **0** | 1 (`publish`, `if: false` by design) |

**The skipped job in both push runs is `retrieval eval (scheduled)`**, which is
schedule-triggered. It is a skip, and it is named here rather than folded into a
ratio. Every one of the other 26 jobs at HEAD succeeded, including
`macos (clients/tui, every push)` on `darwin/arm64`, `fuzz`, `vscode extension`,
`proxy-image`, and lint + govulncheck across all six modules.

**A push run has no `cross (macos-latest, …)` jobs at all** — confirmed by
listing the 27 jobs at HEAD. That is why `daemon`, `protocol` and `editapply` on
macOS are marked main-/dispatch-only above.

**Why one macOS job and not the whole matrix**, measured on run `33896704671`
rather than estimated — billing is wall-clock per job, rounded up, Linux 1× /
Windows 2× / macOS 10×: 22 Linux jobs = 74 billable minutes, 4 Windows = 18,
4 macOS = **70**; 162 total against 92 without macOS. All four on every push
takes the free plan's 2,000 minutes from **~21 pushes a month to ~12**; this one
job costs **20 minutes**, for ~17 pushes. It buys the platform coverage where
the platform risk is, at 29% of the price of buying it everywhere.

**One process finding.** `workflow_dispatch` and `push` share the workflow's
concurrency group, so dispatching a run to get macOS **cancels the in-flight push
run**. Dispatch first or wait; do not do both.

## Gate inventory

Eighteen scripts under `scripts/`. Twelve were audited on 2026-09-04 against four
questions — empty target set, module does not build, missing tool, and whether
"inspected N, found 0" is distinguished from "inspected 0". **Nine were already
sound; three were fail-open and are fixed and neutered** (`fuzz.sh`,
`supply-chain.sh`, and the debt-marker check, which did not exist as a gate).
Repo-wide green at HEAD covers: vet (Linux + Windows), staticcheck / ineffassign
/ bodyclose, `-race` (154 s), coverage ratchet on nine modules, errcheck 0,
govulncheck 0 reachable, fuzz 18/18 targets **ran**, docs links (**324**,
re-run today), docs code references (**30 enforced**, re-run today), registers,
debt markers (156 files, 0 markers), supply chain. The link and reference counts
are higher than the readiness statement's 319 and 16 because this document and
the corrections of 2026-09-05 added their own; both gates carry count floors, so
the numbers move up but never silently down.

---

# SECTION 5 — WHAT IS NOT VERIFIED

**Reproduced in full from the readiness statement, at its current count of ten.**
It grew from eight during the last two work items rather than shrinking, which is
the intended direction as work closes. Nothing here is abridged.

> **1. No human has used any of this.** Every measurement here is from a test
> harness or a pty driven by a test. Nobody has typed into the client since these
> changes landed. The scroll fix, the eviction marker, the paste notice and
> `/mouse` are all judged by assertions about what the model contains, not by
> anyone looking at a screen.
>
> **2. The terminal is verified on Linux only, and that did NOT change when
> macOS joined every push.** macOS and Windows compile, link and run 313 and 307
> of the 327 tests; the 14 (macOS) and 20 (Windows) they do not run are the
> pty-backed suites and, on Windows, the signal contract that platform does not
> have. **Terminal restore and the 57-byte sequence are tested on Linux and
> nowhere else** — that is a build-tag gap, not a trigger gap, and no scheduling
> change touches it. Adding `macos (clients/tui, every push)` bought build, vet
> and the 313 tests on a branch instead of only on `main`; it bought nothing on
> the restore path. Closing that means writing a darwin pty helper. **Stated by a
> test rather than by this paragraph** — `TestPlatformCoverageIsStated` prints it
> in the failing platform's own CI log — and by the header of
> `exitsignals_unix.go`, where the person editing signal code will meet it.
>
> **3.** ~~**Not run in CI.**~~ **Resolved 2026-09-04 at P4.2** — see *CI, on a
> real runner*. Three things broke on first contact and all three were real; none
> was a product defect. ~~The **release** workflow has still never executed.~~
> **`release.yml` RAN on 2026-09-05, twice — see *The release rehearsal* below.**
>
> **4. The memory ceiling is anchored to my own measurement, and supersedes an
> earlier figure that could not be reproduced.** The numbers of record are
> **8.6 MB idle and 13.7 MB at 120 turns**, measured on this tree and identical
> before and after the render cache. An earlier baseline given to me could not be
> reproduced across two attempts, including varying the answer size from 1.2 KB to
> 8 KB per turn; the superseded figure is deliberately not repeated here so it
> cannot be picked up again by a reader who finds it in older text. See R1.11 in
> the residual-risk register for the same note.
>
> **5. Two dependency surfaces are scanned by nothing** — the onnxruntime native
> library and the extension's npm tree (R1.13).
>
> **6.** ~~**The terminal client is not built by the release workflow**
> (R1.14)~~ **Closed 2026-09-04 at P4.1.** It now builds on all three release
> runners with `-trimpath`, is in the macOS signing list, and ships as a
> standalone download with checksums. It is asserted *out* of the `.vsix` by the
> packaging gate. ~~What remains unverified is that the release workflow **has
> never been run** with these steps in it.~~ **It has now — and the run surfaced a
> release blocker: the macOS signing secrets do not exist.** See *The release
> rehearsal* below.
>
> **7. No performance measurement under memory pressure or on a slow disk**, and
> only one on a shared runner. Every wall-clock figure here is from an idle
> laptop, with one exception now: the 2,000-turn soak runs unraced in CI and
> reproduced its figures there (502 turns, 335 KB, 2.9 MB heap, 37.18 s). The
> repaint and paste medians have **not** been re-taken on a runner, and a shared
> runner is exactly where the 22 ms repaint would look worst.
>
> **8. Security posture was not re-audited.** This batch was performance,
> correctness and operability. The security findings below are carried forward
> from earlier passes, not re-verified here.
>
> **9. "Fuzz gate 18/18 targets" overstates four of them.** All eighteen *run*;
> four — every target in `daemon/` — generate **zero new inputs** at CI's
> 30-second budget, because `daemon`'s `TestMain` runs a `go build` that every
> fuzz worker process pays (`go test -run XXXNOSUCHTEST ./daemon` takes 5.68 s
> with no tests). They are regression replay of a cached corpus, not fuzzing. The
> three `clients/tui` targets are unaffected and do real work.
>
> *(Softened 2026-09-05, per Section 8 item 2.)* **Every execution count in this
> item is ONE SAMPLE, at one budget, on one machine — not a property.** The
> `clients/tui` figures first recorded here were 545,016 / 567,455 / 114,448; a
> later run of the same gate at the same `FUZZTIME=30s` gave
> 2,544,259 / 2,657,305 / 286,414 — about 4.7× apart, exceeding the 2× spread
> *Provenance of the numbers above* already warns of. What is stable, and all
> that should be quoted, is the **zero**: a structural fact about `TestMain`
> rather than a measurement of throughput.
>
> **The cause is found and the fix is measured**: deferring that build out of
> `TestMain` takes the same four targets, at the same 30-second budget, from
> **0 execs — reproducibly zero, in every run** — to figures in the high hundreds
> of thousands to low millions (one run: 853,943 / 696,950 / 778,931 /
> 1,102,402), together with **40 new interesting inputs**. The exec counts there
> carry the same one-sample caveat; the transition from zero does not. Written up
> as item 4 of the decision memo and **committed there on 2026-09-04; not handed
> to anyone.** Not landed, because it is not this client's module.
>
> **10. Neither handoff has been DELIVERED, which is a different problem from
> neither having been acted on.** *(Corrected 2026-09-05.)* Both
> `docs/MANUAL_SESSION_2026-09-04.md` and `docs/DECISION_MEMO_2026-09-04.md` were
> **committed to this branch and given to nobody.** No recipient has been
> identified for either; no tester has been asked; the `daemon/` owner has not
> been named, let alone contacted. **A document nobody was told about is
> indistinguishable from one that does not exist**, and that — not anyone's
> inattention — is the most likely reason the memo has no reply. The two states
> have different fixes: *awaiting a response* needs someone to spend ten minutes;
> *awaiting delivery* needs someone to supply two names.

**Item 10 was rewritten on 2026-09-05 and is the sharper statement.** No decision
has been recorded against the memo and no manual session has been reported — but
neither document has been shown to anybody, so neither absence measures anything
about anyone's priorities.

## The release rehearsal — `release.yml` was dispatched, and it found two things

The largest gap between "verified" and "shipped" was that every step producing
the artifact a user downloads had only ever been **simulated**. Dispatched twice
on 2026-09-05.

| Run | Commit | Result | What happened |
|---|---|---|---|
| `33921667448` | `a959945` | **failure** | three `binaries` jobs green; `package` **failed on the third target** |
| `33922431985` | `0925a3d` | **success** | all four jobs green; `publish` correctly skipped (`if: false`) |

**Finding 1 — a real defect, fixed in `0925a3d`.** `stage-runtime.js` keyed the
binary suffix to `process.platform` — the **host** — but `release.yml` assembles
all three `.vsix` packages on one Linux runner, so packaging `win32-x64` looked
for a Unix name and failed *after* the two targets whose suffix happens to match
a Linux packaging host had passed. **A workflow defect, not a product defect**:
the script runs only at package time and the runtime resolvers are unchanged.
**Pre-existing on `main`** (`4453825`); it survived only because the sole trigger
that exercises it is one nobody had pulled.

**Finding 2 — not a defect, and a release blocker: the macOS signing secrets do
not exist.** The step is green because it is written to be a no-op until the
secrets are configured, and running it is the only way to learn they are not:

```
[macos-sign] WARNING: No MACOS_CERT_P12 or MACOS_CERT_P12_BASE64 secret supplied.
[macos-sign] Signing step completed in DRY-RUN mode (unsigned).
##[warning] darwin-arm64 binaries are UNSIGNED. Gatekeeper will quarantine them
and the daemon will not start. Do not publish this target.
```

The workflow's own comment states the consequence: *"shipping an unsigned macOS
package is worse than shipping none."* **A green release run does not mean a
shippable macOS artifact today.** The warning fired exactly as designed; nobody
had ever seen it because nobody had ever run the workflow.

**What the successful run DID verify, on real runners:** the terminal client
builds with `-trimpath` on all three platforms; `verify-vsix.js` passes on all
three (`package gate PASSED` ×3); the three-in-three-out staging assertion
executes and passes — *"staged 3 terminal-client binaries"*, with SHA-256 sums,
including `codeterminal-tui-win32-x64.exe`; and `.vsix` checksums are produced.

**What it still did NOT verify, precisely.** **A dispatch is not a tag.** The
`Attach to the GitHub Release` step is gated on `startsWith(github.ref,
'refs/tags/v')` and did **not** run, so release creation, asset upload and the
draft flag remain unexercised. Signing and notarization are unexercised **in
substance** — the step ran, took the unsigned branch, and warned. Nothing was
published: the `publish` job is `if: false` by design.

---

# SECTION 6 — THE REGISTER

`docs/RESIDUAL_RISKS.md`, all fourteen rows, as re-read end to end on 2026-09-04
with **every pinning test re-run that day**. Status is one of OPEN (still true,
still unfixed), CLOSED (the situation no longer exists), or SUPERSEDED. **No row
is currently marked superseded.**

The register's own rule: a row missing any of *what it is / why deferred / blast
radius / pinned by / trigger* is not finished.

| Row | State | Evidence | Trigger — the fact that reopens it |
|---|---|---|---|
| **R1.1** pty destruction delivers no SIGHUP; the client outlives its terminal | **OPEN** | `TestKnownGapClientOutlivesADestroyedPTY` **passes** (2.05 s), so the gap is still open. It asserts current behaviour, so it **fails when the gap closes**. **Linux-only.** Blast radius: resource waste — an orphan holding ~8 MB, cleared by `pkill` | **Strong.** The moment the client holds anything with a cost while orphaned: a lockfile or advisory lock; a lease, reservation or quota hold; an open subscription or long-poll that bills; a spawned MCP subprocess that outlives it; anything metered by wall-clock |
| **R1.2** over-long CSI leaks parameter bytes as text | **OPEN** | `TestOverLongCSIBoundary` and `TestHeldBytesNeverExceedTheCeiling` both pass; 14 shapes; boundary still exactly 65/66. Cosmetic — **no ESC survives by either route** | **Weak in practice.** "A terminal emulator that acts on partial sequences before their terminator", or a legitimate SGR longer than 64 bytes in real model output. Neither is observable by anything in this repo; both would be noticed by accident |
| **R1.3** one-shot stdout not byte-stable | **OPEN BY DECISION** | `TestOneShotOutputIsFiltered` passes on every platform; `TestRealBinaryPipedIntoAnEarlyReaderExitsCleanly` **passes on Linux only**. Answer text *is* byte-stable; only control sequences are removed | **Reactive but concrete.** A user reporting a broken pipeline — and the recorded answer is *not* to re-enable pass-through but to design an escape hatch, which does not exist |
| **R1.4** review/approval buffers hold raw bytes | **OPEN BY DESIGN** | All three guards pass. Re-checked the load-bearing clause: **still no copy, export or clipboard sink in the client** — no `clipboard`, no OSC 52, no write path outside the terminal. `/mouse` does not create one | **Strong and partly self-detecting.** Adding a copy-to-clipboard binding, a transcript export or save, a crash-report uploader, structured logging of model state, or a second client rendering the same buffers. The AST guard fires on the code change itself |
| **R1.5** `/mcp-server` stderr unredacted | **OPEN — UNFIXED BY DECISION. PENDING DELIVERY: no recipient identified** | Chain re-traced end to end; both line references corrected (they had drifted). Memo recommends **FIX**, in the daemon, ~40 lines. **Nothing pins it** | **Concrete but unwatched.** Shipping a default MCP config carrying a credential; a support flow that asks users to paste `/mcp-server` output; `ModelError.Detail()` becoming reachable from a subcommand. **See the flag below** |
| **R1.6** model-emitted secrets not redacted | **OPEN — UNFIXED BY DECISION. PENDING DELIVERY: no recipient identified** | P5.1 answered the blocking question: the daemon matches on **shapes**, so the asymmetry is permanent and the memo recommends **accepting**. The row's own "sent back as history" clause turned out to hide a defect — verified by execution. **Nothing pins it** | **Flagged — see below.** Transcript persistence to disk becoming readable by another user or process; transcript export; telemetry sampling conversation content |
| **R1.7** `git status` echoes git's output | **OPEN** | Unchanged in substance. Its code reference had drifted by 14 lines, is corrected, and now names `runGitStatus` as well as the line. **Nothing pins it** | **Concrete, not mechanized.** Adding any slash command that runs a network-touching git subcommand (`fetch`, `pull`, `push`, `remote -v`, `ls-remote`), where naming the remote URL in an error is normal |
| **R1.8** `ModelError.detail` safe by field privacy | **OPEN (forward guard)** | `daemon/modelerror.go:204` re-read and still builds `detail` exactly as the row describes. Still a property, still no AST guard. Blast radius **the highest in the register** if it regresses | **Concrete, not mechanized.** Any new `Error:` assignment in the daemon's client-facing response types. The obvious mechanism — an AST guard on `Detail()`'s callers, the same shape as `TestRawByteStructuresHaveNoNewReaders` — **is not built** |
| **R1.9** 1 MB paste costs most of a frame | **CLOSED 2026-09-04** | Closed on a **corrected** figure: the 3.6 number was taken with the input blurred, so the paste was discarded. Re-taken where it lands: **389 µs, identical at 0 bytes and at the 2 MiB ceiling**, spread 1.1×. Pinned by `TestOneMegabytePasteIntoACeilingTranscript`, **which fails if the paste does not land** — the assertion that found the blur | n/a |
| **R1.10** locale reaches the input line | **OPEN** | `TestKnownGapTheInputLineFollowsTheLocale` passes, so the gap is still open. **Linux-only** | **Strong and partly self-detecting.** A CJK user reporting the input line scrolling wrongly; anything starting to cache/diff/compare the **input** line; or `bubbles` changing how `textinput` measures width — which shows up as the pinning test failing in either direction |
| **R1.11** the ceiling can be overshot within one turn | **OPEN** | Still true, still bounded to one exchange. The soak asserts `len(m.turns) <= 500+2` and passes at both lengths (400 raced, 2,000 unraced) | **Strong.** Any change that makes a single turn produce unbounded turns — streaming tool activity without a cap, or a sub-agent whose every step becomes a turn |
| **R1.12** repainting at the bound costs 22 ms, over budget | **OPEN — re-measured; the row changed shape** | Was an extrapolation from a seventh of the bound. Now **22.4 ms p50 / 29.9 ms p99 at the ceiling**, 2.8× the 8 ms repaint budget and **1.4× over the 16 ms hard per-Update ceiling**, all 200 samples above both. Pinned by `TestRepaintCostAtTheTranscriptCeiling` (reports, fails only on a 3× regression) and `TestRepaintAllocationsAtTheCeilingAreBounded` (**39 vs 2,734** allocations) | **Flagged as the weakest trigger in the register — see below.** "A report of fan noise, battery drain, or laggy typing in a long session", plus two named cheap trades (`refreshInterval` 16→33 ms; lower the 2 MiB ceiling) |
| **R1.13** onnxruntime and npm scanned by nothing | **OPEN** | Re-verified: `scripts/govulncheck.sh` names six Go modules and nothing else; no gate anywhere runs `npm audit` or scans onnxruntime. Blast radius **unknown, which is the point** | **Flagged as vague — see below.** "Shipping to anyone who performs a supply-chain review"; a published onnxruntime advisory; adding any npm dependency handling untrusted input |
| **R1.14** terminal client not a release artifact | **CLOSED 2026-09-04** | P4.1. Builds on all three release runners, in the macOS signing list, ships standalone with checksums, asserted *out* of the `.vsix` by the packaging gate. Promoted out of the register and fixed rather than accepted, because it made the readiness statement's own "CI green once" condition unsatisfiable | n/a — and as of 2026-09-05 `release.yml` **has now run** (`33922431985`, success). See *The release rehearsal* |

## Rows whose trigger I would flag

**R1.12 — the weakest trigger in the register, on the row that breaches a stated
budget.** "A report of fan noise, battery drain, or laggy typing" requires a user
in a session at the transcript bound who correctly attributes it, and there is no
telemetry that would produce such a report. This is the one row where the
recorded trigger is materially weaker than the recorded risk. The readiness
statement's condition 5 asks for something different and stronger — *accept it in
writing, or take one of the cheap trades* — and that is not what the register's
trigger says.

**R1.6 — the trigger names three routes and the defect that was actually found
took a fourth.** The trigger watches for *disk readability, export, or
telemetry*. What the memo found was the daemon persisting the raw prompt to
`memory.db` and its `turns_fts` index and re-sending it to the **provider** as
history. On the disk half, the guard exists and is tested — `memory.db` is 0600
inside a 0700 directory (`daemon/memory_test.go:243`) — so the trigger's first
clause has not fired for a *different user*. But egress to the model provider is
not among the three routes the trigger watches, so **this trigger would not have
fired on the defect that was found**. It is not wrong; it is aimed elsewhere.

**R1.13 — "shipping to anyone who performs a supply-chain review" is the release
itself.** Read literally, this trigger has already fired, or fires the moment the
decision this document supports is taken. A trigger that is satisfied by the
event it is supposed to precede is a note, not a trigger.

**R1.2 — nothing in the repository can observe either clause.** Emulator
behaviour and the length distribution of real model SGR output are both outside
anything that is measured. The row is cosmetic and correctly low-priority; the
trigger is effectively "someone notices".

**R1.5 — concrete conditions, but no owner watches them.** All three clauses are
events inside this project's control (shipping a default MCP config, adding a
support flow). None is wired to a gate or a checklist. This is the row where
"pending a decision" and "pending an observation" are easy to confuse.

**R1.7 and R1.8 both name the exact code change that reopens them and neither is
mechanized.** R1.8 explicitly names the mechanism that would do it — an AST guard
on `Detail()`'s callers, the same shape as one that already exists in this
repo — and records that it is not built. For the row the register calls its
highest blast radius if it regresses, that is worth knowing.

**Three rows are pinned on Linux and nowhere else** — R1.1, R1.10, and half of
R1.3. Their pinning tests live in `//go:build linux` files. **On macOS and
Windows those rows have no guard at all**: if any of the three gaps closed or
opened wider there, nothing would notice. **Adding macOS to every push did not
change this** — the pty files do not compile there at any trigger.

---

# SECTION 7 — WHAT IS BLOCKING RELEASE, AND ON WHOM

## The gate

| # | Condition | Status | Owner | What is needed | Decision or implementation? |
|---|---|---|---|---|---|
| 1 | **Phases 1–4 complete** | **MET** | — | — | — |
| 2 | **P5 decision recorded** | **OPEN — blocked on DELIVERY** | **Nobody — no recipient has been identified** | Somebody to **name** the `daemon/` owner. The memo was committed 2026-09-04 and handed to no one, so there is nobody to chase. Once delivered: a written reply to its four items, each with a recommendation already stated | **Neither, yet.** It is not waiting on a decision; it is waiting on a name. *Corrected 2026-09-05: this row previously named "whoever owns `daemon/`", which blamed a person who has never been told the memo exists* |
| 3 | **Manual session done** | **OPEN — blocked on DELIVERY** | **Nobody — no tester has been asked** | A name. The script has been committed since 2026-09-04 and handed to no one. Once delivered: someone runs `docs/MANUAL_SESSION_2026-09-04.md` — six steps, ~1 hour — and writes down what felt off; macOS is worth the most | **Neither.** An hour of a person's attention, from a person who has not been approached |
| 4 | **Readiness statement current** | **MET** | — | — | — |

## **A recorded deferral with a trigger closes a row exactly as well as a fix does**

This is stated plainly because it is the failure mode both open rows are exposed
to: the row sits open while everyone waits for implementation time that was never
required. **Row 2 does not need any of the four items fixed.** It needs each of
them marked FIXED, ACCEPTED WITH TRIGGER, or DEFERRED WITH TRIGGER, with a date
and a name — and an acceptance or deferral must carry the concrete condition that
reopens it, or it is a postponement rather than a decision.

**But that is the second obstacle, not the first.** *(Corrected 2026-09-05.)*
Both open rows were recorded as blocked on people — "the `daemon/` owner", "a
tester" — when **neither document has ever been sent to anyone.** They were
committed to this branch and left there. Nobody is failing to reply; nobody has
been asked. **Neither row should be reported as blocked on a person until a
person has been told.** The action available today is not chasing a decision — it
is supplying two names.

## What row 2 is actually asking

| # | Item | Memo's recommendation | Why it is cheap |
|---|---|---|---|
| 1 | The outbound scrub is bypassed by one turn | **FIX** — highest severity of the four | `cleanPrompt` at one call site (`daemon/server.go:687`) plus a scrub in `prepareHistory` (`daemon/history.go:136`). **No new detector, no new judgment** — it applies a decision the product already made |
| 2 | R1.5 — `/mcp-server` stderr unredacted | **FIX** | ~40 lines, in the daemon at `mcp.Connect`, which already holds the literal env bytes. The memo also records what such a fix **cannot** catch — a credential the server reads from its own config, and the ~23 variables a launcher like `npx` adds — because a partiality you can enumerate is a different object from a shape matcher's |
| 3 | R1.6 — inbound model text not redacted | **ACCEPT, IN WRITING**, with a trigger | The blocking question is answered: shapes, not provisioned values. An inbound redactor means ten regexes over prose, which decision D5 already refused on measured data (33% of chunks, zero precision) |
| 4 | Four `daemon/` fuzz targets generate nothing | **FIX** — cause found, fix measured | `daemon`'s `TestMain` runs a `go build` that every fuzz **worker process** pays. Deferring it took the four targets from **0 execs** to 853,943 / 696,950 / 778,931 / 1,102,402 and **40 new interesting inputs** at the same 30 s budget. Measured, then reverted, because it is not the terminal client's module |

**Items 1, 2 and 4 all touch `daemon/`.** They were deliberately not implemented
by the pass that found them. If the owner wants them, scope and ownership should
be confirmed first and each fix kept in its own commit.

## Two conditions outside the gate table

Both are recorded in the readiness statement's *Conditions on the release
decision* and neither is represented by a gate row:

- ~~**`release.yml` has never executed.**~~ **Dispatched 2026-09-05 and now green
  (`33922431985`).** The terminal-client build, the packaging gate and the
  three-in-three-out staging assertion all ran on real runners and passed. **Two
  things came out of it**: a real defect, fixed (`0925a3d`), and a **release
  blocker that is nobody's defect — the macOS signing secrets do not exist**, so
  the workflow produces UNSIGNED darwin binaries and says *"Do not publish this
  target."* Release creation and asset upload are tag-gated and still
  unexercised: **a dispatch is not a tag.**
- **R1.12 needs someone to accept it in writing, or take one of its cheap
  trades.** It is the one place where a stated budget is knowingly unmet.
  Shipping over it is defensible; shipping without anyone having decided to is
  not.

---

# SECTION 8 — WHAT I WOULD NOT TRUST

My own list, not extracted from any document. Everything here is either a claim
that reads stronger than its evidence, or a number that two places in the
repository state differently.

**1. `TestPlatformCoverageIsStated` asserts a floor of 20, not 73. — CORRECTED
2026-09-05 in the readiness statement.** Both the
readiness statement ("It asserts on the count of files inspected — 73 — so a
version that reads nothing fails rather than passes") and commit `3c217f2`
("Asserts on the count of files inspected (73)") present 73 as the asserted
number. The code is `if inspected < 20` (`clients/tui/platformcoverage_test.go:80`).
The weaker claim the docs also make — *a version that reads nothing fails* — is
true. The stronger reading a reader will take from "73" — *the file set cannot
drift* — is not: an inspection loop that silently dropped to 21 files would still
pass.

**What the correction found, which is better than the correction.** Reading the
gate's code rather than another document showed the docs had attributed the
anti-drift property to the wrong mechanism. **The property is real and the count
was never doing that work.** It comes from the two bidirectional loops — every
listed suite must carry the tag, and every `//go:build linux` file must be
listed — so a sixth pty suite fails whatever `inspected` happens to be. The 20 is
a *vacuity floor* against reading nothing, and the test file's own header comment
says exactly that and was never wrong. **Two documents and a commit message
drifted; the code and its comment agreed all along.**

**The fix, and why not the stronger option.** Option (i) — raise the floor to
today's 73 so the file set genuinely cannot drift — was rejected. It closes a
hole that needs *two* simultaneous faults (a change to the file filter **and** a
new linux-tagged suite landing in the window, because dropping any of the five
listed files fails the first loop loudly), and it buys a chore: every new `.go`
file in the package would fail an unrelated test, and a floor that fails for the
wrong reason is one that gets waived. Option (ii) was taken — the readiness
statement now states the floor as 20, calls it a vacuity floor, names the
bidirectional check as what actually prevents drift, and records the two-fault
residual. `3c217f2`'s commit message stays wrong; history is immutable and the
correction belongs where a reader will look.

This was the only place I found where a gate's stated strength exceeded its
implementation. See item 11 for the ten further gate claims checked against gate
code on 2026-09-05, all of which agreed.

**2. The fuzz execution counts disagree by about 4.7×, at the same budget. —
SOFTENED 2026-09-05 in both documents that carried the figures.** The
readiness statement's item 9 gives the three `clients/tui` targets as
**545,016 / 567,455 / 114,448** execs. The most recent run of the same gate at
the same `FUZZTIME=30s` reported **2,544,259 / 2,657,305 / 286,414**. The
document's own provenance section already warns that fuzz counts are time-boxed
and load-dependent and cites a measured **2× spread**; this is larger than that.
Neither figure is wrong and nothing depends on either, but any exec count in
these documents should be read as one sample, never as a property. The
document's own earlier correction — deleting a "1.2 million executions" claim for
exactly this reason — is the right precedent and item 9 had partially
re-introduced the shape.

**Fixed by softening, not by re-measuring.** Readiness item 9 and the decision
memo's item 4 now both say every non-zero count is one sample, at one budget, on
one machine, and both point at the part that *is* stable: **the zero.** The four
`daemon/` targets generate nothing at CI's budget in every run, which is a
structural fact about `TestMain` rather than a throughput measurement — and no
argument in either document rests on a throughput figure.

**3. `build.yml`'s fuzz job comment says "Fifteen targets"; `scripts/fuzz.sh`
lists 18. — CORRECTED 2026-09-05.** The comment then warns, in its own text, that *"this comment said
'Nine' while the array held thirteen, which is the drift that makes a number in a
comment worth less than the list it describes."* It has drifted again. Nothing
gates comments, so this will keep happening; the list is authoritative and the
number in the comment should not be quoted.

**Corrected to eighteen over four boundaries**, dated, with the sentence "THE
LIST IN `scripts/fuzz.sh` IS AUTHORITATIVE" and both prior drifts recorded above
it. The comment had also dropped `clients/tui` from its list of trust boundaries
entirely — the most recently added one, and the three targets this whole pass
exists around.

**Should a check be mechanized? No, and stated rather than skipped.** It would
need a bespoke parser for one English number word in one YAML comment, which is a
gate whose own failure mode is the thing it watches for. The structurally better
move is to stop putting a count in prose at all; the comment now carries the
number *with a date and a pointer to the list*, which is the cheapest honest
form. If it drifts a third time, delete the number rather than gate it.

**4. "26/27" reads like a failure and is not one. — CORRECTED 2026-09-05.** The readiness statement's
release-gate detail describes push run `33901690615` as "26/27, macOS green". The
actual conclusion is **27 jobs, 26 success, 0 failed, 1 skipped** — the skip
being `retrieval eval (scheduled)`, a schedule-triggered job. The same is true at
HEAD. Written as a bare ratio in a document that elsewhere insists a skip is
never a pass, it invites the opposite misreading.

**Now written out in full** — "27 jobs, 26 success, 0 failed, 1 skipped" — with
the skipped job named. Not one of the three claims Step 1 listed; corrected
alongside them because it is the same shape and a one-line fix.

**5. `release.yml` never having run is the largest gap between "verified" and
"shipped". — CLOSED 2026-09-05 by running it, and it repaid the cost twice.** The readiness statement's verdict line
says "ship-ready on the axes measured", and this condition appears twice further
down. But every step that produces the artifact a user actually downloads —
the build, `-trimpath`, the three-in-three-out staging assertion, macOS signing
and notarization — has only ever been simulated. Simulated shell loops in four
cases plus a packaging self-test is genuinely good preparation, and it is still
not a run. I would dispatch that workflow before shipping, not after.

**Dispatched. It failed the first time on a real, pre-existing defect** —
`stage-runtime.js` keying the binary suffix to the host rather than the target —
**and the second run went green.** The prediction was right and understated: the
*green* run also revealed that **the macOS signing secrets are not configured**,
so the release produces unsigned darwin binaries and warns *"Do not publish this
target"*. That is not a defect in anything; it is a fact about the repository
that no amount of reading could establish, and it is now the hardest blocker on
shipping a macOS artifact. **What replaces this item:** release creation and
asset upload are tag-gated and still unexercised, and signing is unexercised in
substance. **A dispatch is not a tag.**

**6. Every wall-clock number here except the soak comes from one idle laptop.**
That includes **22.4 ms**, the single figure that decides a budget verdict and
the disposition of R1.12. Item 7 says so, and the direction is safe — a shared
runner would make it worse, not better, so the "over budget" conclusion holds.
But the *magnitude* is one machine's, and the two cheap trades in R1.12's trigger
are sized against it.

**7. Two allocation baselines for the same measurement differ by 8–15
allocations.** Commit `b5a7dd8` records 23 / 203 / 705 / 1368 at 0/30/120/240
prior turns; commit `86540c3`'s neutered "before" column records
23 / 195 / 692 / 1353 at the same depths. Both are presented as measured and the
difference is not explained anywhere. Nothing depends on it — the gate asserts
the **ratio**, and 97× versus 1.6× is not in question — but the absolute
"before" numbers are quoted in a table that reads as though they are exact.

**8. The readiness statement's own security table cannot distinguish "decided"
from "awaiting a decision". — FIXED 2026-09-05, and the highest-consequence of
the three.** It marks R1.5 and R1.6 as *"None. Unfixed by
decision"* — the same phrase it uses for R1.3, which really is a closed decision
with reasoning recorded. Only the register carried the distinction (as "PENDING
THE DAEMON OWNER" — itself corrected on 2026-09-05, because it asserted a
delivery that never happened; see item 12). A reader who sees only the readiness statement would sort those
two rows into the wrong bucket and conclude nothing is outstanding. Section 3
above split them; **the source document now does too.** The table gained a
leading **State** column with the three values spelled out above it — FIXED /
OPEN BY DECISION / PENDING SOMEONE ELSE'S DECISION — and the two Fix cells that
said "None. Unfixed by decision" now say "None. NOT YET DECIDED … Awaiting a
written reply." This mattered most of the three because the readiness statement
is the document a decision-maker reads on its own, and the distinction existed
only in the register, in prose, under a heading they would never reach.

**9. The C1 fix is client-side, and the upstream gap is unowned.**
`editapply.RejectUnprintablePath` still accepts the entire C1 range (re-probed
2026-09-04). All four of its callers are inside `editapply` itself, so nothing
else is exposed today, and the terminal client's own door is closed and pinned.
But the pinning test **self-skips only when `editapply` closes the gap**, and no
row, memo item or backlog entry schedules that work. It is a fix that depends on
nobody adding a second terminal-rendering consumer, with no mechanism that would
notice one.

**10. Fifty-five of this branch's 117 commits are not covered by anything
summarized here.** Sections 1–7 describe the 62-commit terminal-client pass. The
earlier 55 — 51 files under `daemon/`, plus `clients/`, `proxy/`, `editapply/`,
`helper/`, `protocol/` and CI — belong to the adversarial re-verification pass and
are reported separately in `docs/ADVERSARIAL_PASS_2026-09-03.md`. **I did not
audit that document for this summary.** If the release decision is about the
branch rather than about the terminal client, it has an unread dependency.

**11. Eleven further gate claims checked against gate code on 2026-09-05, and
all eleven agree.** Item 1 was found by reading code instead of documents, so the
same check was run across every other claim this project makes about how its
gates behave. Each was verified in the script, not in prose:

| Documented claim | Code | Verdict |
|---|---|---|
| `docs-links` carries an explicit count floor | `checked -lt 100` | ✅ |
| `docs-claims` carries an explicit count floor | `total -lt FLOOR`, plus an inner `real -lt 10` on hardcoded floors "so deleting the floors above cannot also disable this" | ✅ |
| `actions-pinned` carries an explicit count floor | `found -lt 10` | ✅ |
| `go-toolchain-pinned` carries an explicit count floor | `found -lt FLOOR_MIN`, **plus** a self-test asserting a below-floor module is rejected | ✅ stronger than claimed |
| `govulncheck` asserts it scanned all six modules | `scanned -lt 6` → *"this verdict is meaningless"* | ✅ |
| `coverage-ratchet` refuses to pass with no floors parsed | `floors_loaded -eq 0` → *"refusing to pass vacuously"* | ✅ |
| `coverage-ratchet` detects a floor whose package vanished | a dedicated section for exactly that | ✅ |
| `errcheck-ceiling` fails closed when a module does not build | captures stderr rather than discarding it — the defect that made a broken module score zero | ✅ |
| `fuzz.sh` confirms each target with `go test -list` before running it | `go test -list "^${target}\$"` piped to `grep -c` | ✅ |
| `supply-chain.sh` distinguishes "this is a library" from "`go list` failed" | separates the two, with the original conflation recorded in the comment | ✅ |
| `verify-vsix.js --self-test` reports N credential names refused | reports `mustReject.length` **dynamically** — no hardcoded number to drift | ✅ |

**Is this cheap to mechanize? Mostly no, and I did not build anything.** The
general form — "a document's sentence about a gate matches that gate's
behaviour" — is semantic and not checkable by a script. One narrow subset is:
*every gate script contains a vacuity floor of some kind.* That is a grep for a
`-lt` comparison across `scripts/*.sh`, roughly ten lines, and it would have
caught none of the twelve above because they all have one — the failure it
catches is a **new** gate landing without a floor. I think that is worth having
and it is a decision for whoever owns the gate suite, not one to take silently
inside a correction pass. **Saying so rather than building it.**

**12. I recorded two documents as delivered when they had only been committed,
and it took four days to notice. — CORRECTED 2026-09-05 across nine sites.** This
is the worst error in the pass, because it is the one that made a blocked release
gate look like somebody else's slowness.

The readiness statement said the fuzz finding was *"handed to the daemon's
owner"*. The register said R1.5 and R1.6 were *"PENDING THE DAEMON OWNER"*. The
gate table named *"whoever owns `daemon/`"* in an Owner column. **All of it was
false in the same way**: `docs/DECISION_MEMO_2026-09-04.md` and
`docs/MANUAL_SESSION_2026-09-04.md` were written, committed, and shown to nobody.
No recipient was ever identified. Nobody declined to reply, because nobody was
asked.

**"Committed" and "handed to" are different states that shared a phrase** — the
same collapse as R1.5/R1.6 sharing *"unfixed by decision"* with R1.3 (item 8),
one layer further out: a status that reads as *awaiting a response* when it is
*awaiting delivery*. The two have different fixes. Awaiting a response is solved
by someone finding ten minutes; awaiting delivery is solved by someone supplying
a name, which nobody had been asked for.

**Why it survived four days:** every document agreed with every other document,
because they were all written from the same wrong premise, and the check that
would have caught it — *has anyone actually been told?* — is not a thing any gate
in this repository can ask. It is the failure mode S6 was written for, applied to
a fact about the world rather than about code, where no amount of reading the
tree can settle it.

**What I would still not trust here:** this correction is mine, about my own
record, verified only by my own knowledge that I have no way to send anything to
anyone. If some other channel did carry these documents to a person, I would not
know, and this item would be wrong in the opposite direction.

**Where I found nothing to distrust:** the deterministic figures. Allocation
counts, ratios, the 500 / 2 MiB / 4,000 constants, the 678 → 0 build paths, the
5 → 0 unchecked errors, the 3 → 1 render outputs, the per-platform test-file
counts (which I re-derived from `go list` today and which match the document
exactly), and the coverage floor and measurement. Those reproduce, and the
neutered-cache column means the improvement claims are checkable rather than
asserted.
