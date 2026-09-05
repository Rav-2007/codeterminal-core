# Residual risk register — clients/tui

<!-- coderefs: enforced -->

Opened 2026-09-04 during the production-hardening pass on the TUI.

**Its scope widened on 2026-09-05 and the title has not.** R1.15 is release
machinery and R1.16/R1.17 are `daemon/`, `editapply/` and `scripts/`. The
heading still says "clients/tui", which is now narrower than the contents.
Recorded rather than silently retitled: a reader who came here for TUI risks
needs to know the file grew, and renaming it would break every inbound
reference. The next pass that touches the heading should widen it.

Every entry here is something **known, measured, and deliberately not fixed**.
That is the point of the file: a risk that was reasoned about and accepted is a
different object from one nobody noticed, and only the first kind can be
re-decided later. An empty register does not mean a clean system; it means the
register is not being used.

Each row carries the same five fields, and a row missing one of them is not
finished:

| Field | Why it is mandatory |
|---|---|
| **What it is** | Stated as the failure, not as the code |
| **Why deferred** | The reasoning, so it can be disagreed with |
| **Blast radius** | What actually happens — separates waste from compromise |
| **Pinned by** | The test that fails if the situation changes |
| **Trigger** | The specific fact that makes this urgent. Written in advance, because the moment it arrives is the moment nobody has time to re-derive it |

---

## Reconciliation — 2026-09-04, end of the pass

**Read end to end against the tree, and every pinning test re-run today.** The
earlier reconciliation (after tasks 3.5–3.10) is superseded by this one; where a
row changed since, the note says how.

**Status is one of: OPEN (still true, still unfixed), CLOSED (the situation no
longer exists), or SUPERSEDED (replaced by a different row).**

| Row | Status | Evidence today |
|---|---|---|
| R1.1 pty destruction delivers no SIGHUP | **OPEN** | `TestKnownGapClientOutlivesADestroyedPTY` PASSES (2.05 s), so the gap is still open. **Linux-only** — see the note below. |
| R1.2 over-long CSI leaks parameter bytes | **OPEN** | `TestOverLongCSIBoundary` and `TestHeldBytesNeverExceedTheCeiling` both PASS. 14 shapes, boundary still exactly 65/66. |
| R1.3 one-shot stdout not byte-stable | **OPEN by decision** | `TestOneShotOutputIsFiltered` PASSES on every platform; `TestRealBinaryPipedIntoAnEarlyReaderExitsCleanly` PASSES on Linux only. |
| R1.4 review/approval buffers hold raw bytes | **OPEN by design** | All three guards PASS. Re-checked the row's load-bearing clause: **still no copy, export or clipboard sink in the client** — no `clipboard`, no OSC 52, no write path outside the terminal. `/mouse` does not create one; it hands selection back to the terminal, which copies already-filtered text. |
| R1.5 `/mcp-server` stderr unredacted | **OPEN — UNFIXED BY DECISION. PENDING DELIVERY: no recipient has been identified** | Chain re-traced end to end and both line references corrected (they had drifted). [Decision memo](DECISION_MEMO_2026-09-04.md) recommends **FIX**, in the daemon, ~40 lines. Still nothing pinning it. |
| R1.6 model-emitted secrets not redacted | **OPEN — UNFIXED BY DECISION. PENDING DELIVERY: no recipient has been identified** | P5.1 answered: the daemon matches on **shapes**, so the asymmetry is permanent and the memo recommends **accepting** it. But the row's own "sent back as history" clause turned out to hide a defect: nothing scrubs it on the way back out. Verified by execution. |
| R1.7 `git status` echoes git's output | **OPEN** | Unchanged in substance. Its code reference had drifted by 14 lines and is corrected; it now names `runGitStatus` as well. Still nothing pinning it. |
| R1.8 `ModelError.detail` safe by field privacy | **OPEN (forward guard)** | `daemon/modelerror.go:204` re-read today and still builds `detail` exactly as the row describes. Still a property, still no AST guard. |
| R1.9 1 MB paste costs most of a frame | **CLOSED** | Stays closed, on a **corrected figure**: the 3.6 number was measured with the input blurred, so the paste was discarded. Re-taken where it lands: **389 µs, identical at 0 bytes and at the 2 MiB ceiling**, spread 1.1×. |
| R1.10 locale reaches the input line | **OPEN** | `TestKnownGapTheInputLineFollowsTheLocale` PASSES, so the gap is still open. **Linux-only.** |
| R1.11 the ceiling can be overshot within one turn | **OPEN** | Still true and still bounded to one exchange; the soak asserts `len(m.turns) <= 500+2` and passes at both lengths. |
| R1.12 repainting a deep transcript costs real CPU | **OPEN — re-measured, and the row changed shape** | Was an extrapolation from a seventh of the bound. Now **22.4 ms p50 / 29.9 ms p99 at the ceiling**, over the 8 ms repaint budget at 2.8× **and over D-1's 16 ms hard per-Update ceiling at 1.4×**. Bounded, and over budget. |
| R1.13 onnxruntime and npm are scanned by nothing | **OPEN** | Re-verified: `scripts/govulncheck.sh` names six Go modules and nothing else; no gate anywhere runs `npm audit` or scans onnxruntime. |
| R1.14 the terminal client is not a release artifact | **CLOSED 2026-09-04** | P4.1. Builds on all three release runners, in the macOS signing list, ships as a standalone download with checksums, and asserted *out* of the `.vsix` by the packaging gate. It was never a residual risk: it made this document's own "CI green once" condition unsatisfiable. |
| R1.15 the release signs nothing — macOS secrets absent | **DEFERRED BY DECISION 2026-09-05 (founder)** | Found by the first-ever dispatch of `release.yml` (run `33922431985`): the signing step is a no-op without the five `MACOS_*` secrets, so darwin binaries are **unsigned** and Gatekeeper kills them. **The run is green either way** — fail-open, one layer up from the gate scripts. **Ruling: the first release ships Linux and Windows only; `darwin-arm64` is deferred until the secrets exist.** The machinery now enforces it — `scripts/release-signing-guard.sh`, keyed on **distributable**, verified on run `33938839311`. Three items remain **expected-unverified**; see the row. |
| R1.16 a gate whose expected count comes from the list it validates | **OPEN — a CLASS, two instances** | Opened 2026-09-05. Not a coincidence: `platformcoverage_test.go` (floor 20, 73 suites) and `scripts/fuzz.sh` (`TARGETS` is its own source of truth) share one general form. The fuzz gate has no equivalent of the TUI test's bidirectional loops. |
| R1.17 an ambiguous request body is refused with the wrong message | **OPEN — rough edge, deliberate** | Opened 2026-09-05 alongside the fix in `026fe48`. The refusal is correct; the message a client sees is `"prompt is empty"`, inherited from the duplicate-key precedent it was deliberately made to match. |

### Three of these rows are pinned only on Linux

`TestKnownGapClientOutlivesADestroyedPTY` (R1.1),
`TestKnownGapTheInputLineFollowsTheLocale` (R1.10) and
`TestRealBinaryPipedIntoAnEarlyReaderExitsCleanly` (R1.3) live in
`//go:build linux` files, because allocating a pty is per-kernel. **On macOS and
Windows those three rows have no guard at all** — if any of the three gaps
closed on one of those platforms, or opened wider, nothing here would notice.

That is now said out loud rather than left implicit: `TestPlatformCoverageIsStated`
runs everywhere and prints, in the failing platform's own test output, which
suites did not run there. See *Per-platform status* in
[the readiness statement](TUI_PRODUCTION_READINESS_2026-09-04.md).

**Adding macOS to every push did not change this**, and the distinction matters
if someone reads the CI matrix and concludes otherwise. Since 2026-09-04 a
`macos (clients/tui, every push)` job builds, vets and runs the suite on darwin
for every branch push — proved on run `33901690615` — but the five pty-backed
files are `//go:build linux` and do not compile there at any trigger. **These
three rows are pinned on Linux and nowhere else, before and after.** Closing that
means a darwin pty helper, which is engineering rather than scheduling and is not
done. `exitsignals_unix.go`'s header now says so at the point of edit.

### What this re-read found in the register itself

**Two of its four code references had drifted** — R1.7's by 14 lines, R1.5's by
15 — pointing at unrelated code in files still long enough that nothing noticed.
Both are fixed, both now name their function as well as their line, and
`scripts/docs-coderefs.sh` fails the build if a reference in this document points
at a line that does not exist. That gate found two more ambiguous references in
the decision memo on its first run.

---

### R1.5 and R1.6 are unfixed BY DECISION, and PENDING DELIVERY

**Corrected 2026-09-05, and the correction is the whole point of these two rows.**
They previously read "PENDING THE DAEMON OWNER", which asserts that an owner was
told and has not answered. **That was never true.**
[docs/DECISION_MEMO_2026-09-04.md](DECISION_MEMO_2026-09-04.md) was **committed**
on 2026-09-04 and **handed to nobody**. No recipient has been identified, nobody
has been asked, and no one outside this branch knows the file exists.

**"Committed" and "handed to" are different states and must never share a
phrase.** A document nobody was told about is indistinguishable from one that
does not exist, so the honest status is not *awaiting a reply* but **awaiting
delivery** — and the two have different fixes. Awaiting a reply is solved by
someone finding ten minutes. Awaiting delivery is solved by somebody naming a
recipient, which nobody has been asked to do.

The memo is *written for* whoever owns `daemon/`, states a recommendation on
each item, and needs only a reply **once it reaches them**.
**A recorded deferral with a trigger closes the release gate exactly as well as a
fix does** — what does not close it is silence, and silence is currently
guaranteed, because the question has not been put to anyone.

### Why they were unfixed in the first place

Stated separately because the difference matters to whoever reads this next, and
a register cannot show it in a status column.

Both were **enumerated in full** during task 2.3a — every sink, whether it is
reachable, what could reach it, and its current state — and the enumeration was
reported. What did not happen is the fix, because **no go-ahead was given for
2.3c**, the structural redaction helper. That is the whole reason they are open.

So: someone who finds these rows later is not looking at something nobody
noticed. They are looking at a decision to defer, taken with the sinks known.
The constraint recorded for whoever does the work is that redaction must be
**structural** — a property of how the type is built — and not a pattern match
on the value, because a helper that greps for things that look like keys will
miss the one that does not. `ModelError` (R1.8) already has that shape and is
the model to copy.

Neither has a test pinning it. That is stated in each row and is not an
oversight: there is nothing yet to pin.

**A decision memo now exists for both:**
[docs/DECISION_MEMO_2026-09-04.md](DECISION_MEMO_2026-09-04.md).
It answers the question that gates them — `redactionsMsg` matches on **shapes**,
not on values the daemon provisioned, so R1.6 stays outbound-only and the
asymmetry is permanent — recommends **fixing R1.5** in the daemon by exact-match
stripping of provisioned env values, and records a **third finding neither row
contained**: the outbound scrub is bypassed by one turn. A secret the daemon
redacts from the prompt is persisted raw to `memory.db`, indexed in `turns_fts`,
and sent to the provider verbatim in the next turn's history. Verified by
execution. That one is a defect in an existing control rather than a request for
a new one.

---

## R1.1 — Destroying the pty delivers no SIGHUP; the client outlives its terminal

**What it is.** When the controlling terminal is destroyed outright rather than
sending a hangup, the client is never notified and keeps running. Diagnosed by
SIGABRT against the hung process: goroutine 1 parked in
`bubbletea.(*Program).eventLoop`, and the signal goroutine still sitting at its
first `select` having received nothing at all. No signal is delivered, so none
of the handling in `exitsignals_unix.go` ever runs — this predates that file and
is not caused by it.

**Why deferred.** The fix is either polling the tty for liveness or working
around Bubble Tea's event loop. Both are speculative changes to the render/input
path, and interleaving them with security work would mean shipping a terminal
behaviour change inside a commit series about escape filtering. Neither belongs
there.

**Blast radius.** Resource waste, not compromise. An orphaned process holding
about 8 MB, with no terminal to find it from. `pkill codeterminal-tui` clears
it. It holds no lock, no lease, and no network subscription while orphaned, and
that is the only reason this is a low-severity row rather than a high one.

**Pinned by.** `TestKnownGapClientOutlivesADestroyedPTY`
(`clients/tui/exitsignals_pty_test.go`). It asserts the CURRENT behaviour, so it
**fails when the gap closes** and tells the reader to delete it and take the
win. Upstream would close this for free: a Bubble Tea release that registers
SIGHUP itself, or that exits when its input reader dies, resolves this with no
change here.

**Trigger that makes this urgent — write it down, do not re-derive it.** The
moment the client holds anything with a cost while orphaned:

- a lockfile, a flock, or any advisory lock (an orphan then blocks a live client);
- a lease, a reservation, or a quota hold against the proxy or daemon;
- an open subscription, a websocket, or a long-poll that bills or counts;
- a spawned MCP subprocess that outlives it, turning one orphan into a tree;
- anything metered by wall-clock rather than by request.

Any one of those converts this from wasted memory into a correctness or billing
defect, and it must be fixed before that change lands, not after.

---

## R1.2 — An over-long CSI leaks its parameter bytes as literal text

**What it is.** When an escape sequence exceeds the 64-byte ceiling, the parser
drops what it held and resumes reading at the offending byte. Those parameter
bytes — digits and semicolons — then render as ordinary visible text.

**Why deferred.** This is an accepted trade rather than a bug. The alternative
is to keep consuming until a terminator arrives, which lets a single
unterminated sequence swallow the entire rest of the conversation: a denial of
service that is strictly worse than some visible garbage. Buffering without
bound is not an option either, which is what the ceiling exists for.

**Blast radius.** Cosmetic. **No ESC survives by either route**, which is the
property that matters — the leaked bytes are inert text, and any new ESC
re-enters the parser.

**Pinned by.** `TestOverLongCSIBoundary` (`clients/tui/sanitize_test.go`), 14
shapes across the boundary, measured exactly: **65 bytes is the last that fits,
66 the first dropped**. Includes ESC at the resume point by both routes into it,
two over-long sequences back to back, and an over-long CSI followed by an OSC.
Each asserts chunk-invariance at every cut point and byte-at-a-time. All are
seeded into both fuzzers. `TestHeldBytesNeverExceedTheCeiling` pins the bound
itself.

**Trigger.** Two things reopen this: a terminal emulator that acts on partial
sequences before their terminator (the leaked bytes would stop being inert), or
a legitimate SGR sequence longer than 64 bytes appearing in real model output
(it would be dropped, and colour would be lost mid-answer). The second is worth
re-measuring if syntax highlighting is ever generated locally rather than by the
model.

---

## R1.3 — One-shot stdout is no longer byte-stable for control sequences

**What it is.** `codeterminal-tui --prompt ...` filters its stdout
unconditionally. A caller piping that output no longer receives the exact bytes
the model sent. A comment in `oneshot.go` previously promised it would.

**Why deferred — this is a decision, not a deferral.** The promise was wrong
before it was broken. An escape written to a file is not defused, only deferred:
it executes the moment anyone `cat`s or `less`es that file, and one-shot output
landing in a log that someone greps later is exactly that path. Gating on
`isatty` would leave the sequence armed for whoever reads it tomorrow, so the
filtering is deliberately unconditional.

**Blast radius.** A pipeline that depended on control sequences passing through
breaks. Nothing that reads the answer as text is affected: **the text of the
answer is byte-stable** — every printable character, newline and tab, in order,
unchanged — and only control sequences are removed, colour excepted.

**Pinned by.** `TestOneShotOutputIsFiltered`
(`clients/tui/sanitize_wiring_test.go`) asserts stdout is clean and the answer
text survives. `TestRealBinaryPipedIntoAnEarlyReaderExitsCleanly` covers the
pipe path end to end.

**User-facing.** Recorded in `docs/RELEASE_NOTES.md`, which is where a user
will look; this row exists so the *reasoning* survives, not to replace that.

**Trigger.** A user reporting a broken pipeline. The answer is not to re-enable
pass-through but to give them a documented escape hatch, which does not exist
today and should be designed rather than added reactively.

---

## R1.4 — The edit-review and approval buffers hold raw bytes by design

**What it is.** Two structures are deliberately **not** sanitized at ingest:
`m.reviewPrepared` (a prepared edit) and `m.pendingApproval` (a tool-approval
request). Both are filtered only at render.

**Why deferred — again a decision.** Each has a reason it cannot be cleaned at
ingest. The edit block's bytes are **written to disk** if approved, and a file
may legitimately contain escape sequences (this repository has such a fixture),
so filtering on the way in would corrupt the edit it is about to apply. The
approval request's bytes are what the **daemon binds its approval digest to**,
so altering the stored copy breaks the binding rather than protecting it.

**Blast radius.** If a future render path forgets to filter, model-authored
bytes reach the terminal on the approval screen — the screen where a user
decides whether a tool may run. That is forged consent, not a display bug. It is
the highest-consequence display path in the client.

**Pinned by.** `TestRawByteStructuresHaveNoNewReaders`
(`clients/tui/sanitize_guard_test.go`) reads the package's own AST and fails on
any new function touching either field, with a per-entry note saying which of
them sanitizes and which does not render at all.
`TestApprovalPanelCannotBeRepainted` and
`TestReviewPanelIsFilteredButTheEditIsNot` assert both halves: filtered on
screen, byte-exact in store.

**Constraint that must survive the guard being edited — this is why the row
exists.** The AST guard is a list a human maintains, so it can be widened by
adding a name to it. The rule the list is enforcing is:

> Any new path that puts either buffer in front of a person, or into any sink
> other than the file writer and the approval digest, must sanitize it first.
> **There is no copy, export, or clipboard sink in this client today** —
> `atotto/clipboard` is an indirect dependency of Bubble Tea that this package
> never calls, and no slash command writes a transcript. That fact is load
> bearing: it is *why* guarding the render paths is sufficient. **A new sink
> invalidates that reasoning and needs its own filtering.**

**Trigger.** Adding any of: a copy-to-clipboard binding, a transcript export or
save command, a crash-report uploader, structured logging of model state, or a
second client rendering the same buffers.

---

## R1.5 — The `/mcp-server` path puts daemon and MCP-server stderr on screen unredacted

*(From the 2.3a enumeration. No fix was authorised; recorded rather than dropped.)*

**What it is.** `runMCPServerList` (`clients/tui/slash.go:381`) runs
`codeterminal-daemon mcp list` with `CombinedOutput()` at
`clients/tui/slash.go:411` and displays the result. That captures the
daemon's **stderr**, and `mcp list` starts the configured MCP servers —
`daemon/mcpruntime.go:111` wires each server's own stderr into the same stream.
A third-party MCP server that prints its API key at startup surfaces it on the
user's screen. This is the only path in the client that puts daemon stderr in
front of a user.

**Why deferred.** No go-ahead was given for 2.3c, and the fix is a judgement
call rather than a mechanism: redacting a third party's stderr means either
pattern-matching on values — which the brief explicitly rules out, because a
grep for key-shaped strings misses the key that is not key-shaped — or dropping
stderr entirely, which removes the diagnostics that make `/mcp-server` useful
when a server fails to start.

**Blast radius.** A secret belonging to an MCP server the user configured is
displayed to that same user. It does not leave the machine and it is the user's
own credential, so this is disclosure-to-owner, not exfiltration. It becomes
serious when the output is pasted into a ticket or a screen recording — which is
exactly when someone runs `/mcp-server`.

**Pinned by.** Nothing yet. **This row is a gap in coverage, not just in
behaviour**, and that is stated plainly rather than implied.

**Decision memo, 2026-09-04.** The full chain is traced end to end in
[the memo](DECISION_MEMO_2026-09-04.md), which recommends FIXING this
one: `mcp.Connect` already computes `ServerEnv(cfg.EnvAllow)` and therefore holds
the literal bytes it handed the subprocess, so exact-match stripping is available
there and nowhere else. ~40 lines. The memo also records what such a fix cannot
catch — a credential the server reads from its own config, and the 23 variables a
launcher like npx adds on top — because a partiality you can enumerate is a
different object from a shape matcher's.

**Trigger.** Any of: shipping a default MCP server configuration that carries a
credential; a support flow that asks users to paste `/mcp-server` output;
or the daemon's own `ModelError.Detail()` becoming reachable from a subcommand
the TUI shells out to.

---

## R1.6 — Model-emitted secrets in the transcript are not redacted

*(From the 2.3a enumeration.)*

**What it is.** The scrubbing is asymmetric. The daemon scrubs **outbound**
prompts and reports what it removed (`redactionsMsg`, shown in the header).
Nothing scrubs **inbound** model text. If the model reads a `.env` through a
tool and quotes it back, that value lands in `m.turns`, on screen, and is sent
back to the daemon as history on the next turn.

**Why deferred.** Same reason as R1.5, and more sharply: redaction here can only
be pattern-matching, because the bytes arrive as ordinary prose with no
structure marking them as secret. The structural approach that works for
`ModelError` — an unexported field with a single named accessor — has nothing to
attach to when the secret is a substring of a sentence.

**Blast radius.** The user is shown their own secret, and it is re-sent to the
model as conversation history. Bounded by the fact that the model had to read it
first, which required a tool call the user approved.

**Pinned by.** Nothing. Recorded as an accepted gap.

**Trigger.** Transcript persistence to disk becoming readable by another user or
process; transcript export; or telemetry that samples conversation content.

**Decision memo, 2026-09-04 — and this row understated its own blast radius.**
[The memo](DECISION_MEMO_2026-09-04.md) recommends REJECTING an inbound
redactor and accepting this row in writing: `redactionsMsg` matches on shapes, so
"apply the same set inbound" means running ten regexes over prose — which the
brief rules out, which decision D5 already refused on measured data (33% of
chunks, zero precision), and which would stop a coding assistant from being able
to show you what an API key looks like.

The row says a model-emitted secret "is sent back to the daemon as history on the
next turn". What it does not say, and what was verified by execution on
2026-09-04, is that **nothing scrubs it on the way back out**: `prepareHistory`
caps and annotates but does not scrub, and `persistTurn` writes the RAW prompt —
not `cleanPrompt` — to `memory.db` and its `turns_fts` index. So a secret the
daemon redacted on turn 1 goes to the provider verbatim on turn 2. That is a hole
in the control that exists, not the absence of one that does not, and the memo
treats it separately and recommends fixing it.

---

## R1.7 — `git status` failures echo git's own output

*(From the 2.3a enumeration. Lowest severity row here.)*

**What it is.** `runGitStatus` (`clients/tui/slash.go:305`) runs `git status
-sb` and, on failure, prints `git status failed: %v` plus the combined output at
`clients/tui/slash.go:319`. A git error
can name a remote URL, and a URL with embedded credentials
(`https://user:token@host`) would be echoed verbatim.

**Why deferred.** `status -sb` is local-only and does not print remotes, so
reaching this requires a git error that names one. Non-zero likelihood, very
low.

**Blast radius.** A credential embedded in a git remote is displayed to its
owner. Same disclosure-to-owner shape as R1.5.

**Pinned by.** Nothing.

**Trigger.** Adding any slash command that runs a network-touching git
subcommand (`fetch`, `pull`, `push`, `remote -v`, `ls-remote`), where naming the
remote URL in an error is normal rather than exceptional.

---

## R1.8 — `ModelError.detail` is safe by field privacy, not by redaction

*(Forward guard, not a finding — recorded because the thing it guards is the
highest-value leak in the system.)*

**What it is.** `daemon/modelerror.go:204` builds `detail` as `"model API
returned %s: %s"` with the raw upstream response body. A provider's 401 body
commonly echoes a partial API key. That string is **client-unreachable today**:
`detail` is unexported, `Error()` returns a fixed per-class message from the
`clientMessages` map with no interpolation, and `Detail()`'s only six callers
are all `logger.Printf` into daemon stderr.

**Why it is here.** The safety is one line of code deep. A single
`Error: modelErr.Detail()` on any wire path sends a provider's response body —
and whatever key fragment it echoes — straight to `errors.New(tok.Error)` in the
TUI, into `m.turns`, and onto the screen.

**Blast radius if it regresses.** The highest of anything in this register: a
live credential fragment on screen and in conversation history sent back
upstream.

**Pinned by.** Nothing in this repository yet. `Detail()`'s doc comment says
"never put it on the wire", which is a convention, and this pass has already
measured that a convention fails inside the commit that establishes it.

**Trigger.** Any new `Error:` assignment in the daemon's client-facing response
types. An AST guard on `Detail()`'s callers — the same shape as
`TestRawByteStructuresHaveNoNewReaders` — is the obvious mechanism and is not
built.

---

## R1.9 — A 1 MB paste costs most of a frame, and 3.2 will not fix it — **CLOSED 2026-09-04**

*(Opened by the 3.1 benchmark harness, 2026-09-04.)*

**What it is.** `Update` handling a 1 MB paste runs at **40–100% of D-1's 16 ms
frame budget**, measured on the reference machine (13th Gen i5-1340P). It is the
most expensive single input the client accepts.

**Why it is a register row and not a filed violation.** It does not reproduce as
a breach. Four samplings of the same input, same machine, same code gave medians
of **6.3 ms, 13.6 ms, 14.5 ms and 16.6 ms** — a 2.6× run-to-run spread that
straddles the ceiling. Stating it as "over budget" would be as wrong as stating
it as "within budget"; what is true is that it has no reliable headroom.

**The part that matters for planning:** this was measured on an **empty
transcript**. The cost is in handling a million runes of input, not in the
transcript render. **Task 3.2's render cache will not close it.** Closing it
needs its own bound on pasted input — truncation with a visible notice, or
moving the work off the event loop.

**Blast radius.** One dropped frame on paste. Input latency, not correctness. On
a slower machine — a CI runner, an older laptop — it is over budget rather than
near it.

**Pinned by.** `TestNoUpdateExceedsOneFrame/1MB_paste`
(`clients/tui/renderbench_test.go`). D-1's 16 ms is **reported with the distance
printed on every run**; what is asserted is 3× the budget, because the
measurement cannot resolve finer than its own noise and two earlier versions of
this test flapped — once in each direction — trying. The deterministic gate on
this path is `TestPerTokenAllocationsAreBounded`.

**Trigger.** Any of: a report of paste lag; adding syntax highlighting or
validation on the input path, both of which multiply per-rune cost; or the 3×
assertion firing, which would mean the cost has grown past anything noise
explains.

**CLOSED 2026-09-04 by task 3.6.** Measured at 1 MB: **15.6 ms → 0.30 ms**
median, 2% of a frame, at 0 and at 240 prior turns. The row's own prediction
held — the render cache did not touch it, because the cost was linear in the
paste and independent of the transcript. The cost was not where it was assumed
to be either: the key handler called `msg.String()` twice before the input saw
anything, building a megabyte string each time, and bounding the paste after
that point measured no improvement at all.

It also closed a **second, more serious defect that was not in this row**: the
prompt box had silently dropped everything past 4,000 characters since it was
written. The header now says how many characters arrived, were kept and were
dropped. The gate is deterministic — no more runes reach the input than can be
kept — because neutering proved a timing assertion would not reliably catch its
own removal at a 15.6 ms median against a 16 ms ceiling.

**Corrected 2026-09-04 at P3.3, and the correction is not cosmetic.** The 3.6
measurement above was taken mid-stream, and `startTurn` **blurs the input** — so
`textinput` ignored the keys and that "0.30 ms" is the cost of a paste that was
bounded, reported to the user, and then dropped on the floor. The delta stands
(both halves were measured in the same state, and the cost removed —
stringifying a megabyte twice — was paid regardless of focus), but the absolute
figure was not the cost of a paste that lands.

Re-taken in the state where a paste is accepted, and at depth as well as empty:

| State | median | spread |
|---|---|---|
| Idle, transcript at the ceiling (accepted, 4,000 of 1,048,560 runes kept) | **389 µs** | 1.1× |
| Idle, empty transcript (the 3.6 comparison, re-taken) | **389 µs** | 1.1× |
| Streaming, at the ceiling (input blurred, paste dropped) | 28 µs | 1.6× |

**2% of a frame, and identical at 0 bytes and at 2 MiB** — the paste cost is
independent of transcript depth, which is what the row predicted and what the
bound guarantees: nothing about a paste touches the transcript render. The row
stays CLOSED, now on a number that describes the case a user is actually in, and
the spread is 1.1× rather than the 2.6× that made the original measurement
unusable as a gate. Pinned by `TestOneMegabytePasteIntoACeilingTranscript`, which
**fails if the paste does not land** — that assertion is what found the blurred
input.

That blur is a real product behaviour and is not a defect being recorded here:
keystrokes typed while an answer streams are discarded, deliberately. It is
worth knowing that a paste is among them.

---

## R1.10 — The input line still follows the locale; the transcript does not

**What it is.** `go-runewidth` decides the display width of **ambiguous** runes
(`→`, `±`, `·`, `※`, and the rest of Unicode's East Asian Ambiguous class) from
the locale, in a package-level variable set in `init()` from `RUNEWIDTH_EASTASIAN`
or, failing that, `LC_ALL` / `LC_CTYPE` / `LANG`. `bubbles`' `textinput` uses it
to decide how far to scroll an input line that overflows its width. So two
clients with identical state, identical width and identical terminal draw
**different bytes** if their locales disagree. Measured on the full `View()` with
an overflowing line of ambiguous runes: **892 bytes at `EastAsianWidth=0`, 865 at
`1`**.

**Why deferred.** It is genuinely ambiguous which answer is right — that is what
the Unicode class is called. Forcing the variable to a constant would make the
input line scroll *wrongly* for CJK users, who are the people the East Asian
width rules exist for, in exchange for determinism on a line that nothing caches
and nothing compares. That is a bad trade made on no evidence, and it would be a
behaviour regression in a locale this pass has no way to test properly.

**Blast radius.** Cosmetic, and confined to the input line: how far the text
scrolls when you type past the right edge. It does not reach the transcript.
`renderTranscript` resolves widths through `ansi.Wrap` and `lipgloss.Width`,
both of which use `x/ansi`'s `GraphemeWidth` method and **never call
`runewidth`** — verified by reading the call path and confirmed by measurement.
That matters more than the cosmetic part: the transcript is what task 3.2 caches
and what 3.4 compares byte-for-byte, and it is locale-independent.

**Pinned by.** `TestKnownGapTheInputLineFollowsTheLocale`
(`clients/tui/renderprofile_test.go`), which asserts **both** halves: that the
transcript renders identically under `RUNEWIDTH_EASTASIAN=0` and `=1`, and that
the full `View()` does not. The second assertion fails if the gap ever closes —
at which point this row and the note in `renderprofile.go` should be deleted and
the win taken.

**Trigger.** Any of: a CJK user reporting the input line scrolling wrongly;
anything starting to cache, diff or compare the **input** line the way 3.2
caches the transcript; or `bubbles` changing how `textinput` measures width,
which would show up as the pinning test failing in either direction.

---

## R1.11 — The transcript ceiling can be exceeded within a single turn

**What it is.** `enforceTranscriptBound` runs at the START of a turn, not inside
`appendTurn`. Between two turns the transcript can therefore exceed both
ceilings by whatever one exchange produces — one user turn, one assistant answer
and any tool-activity turns alongside them.

**Why deferred.** It is not a deferral so much as the price of correctness.
`streamAssistant` and every value in `activityTurns` are **indexes into
`m.turns`**, so evicting from the front shifts what they point at. Evicting from
inside `appendTurn` was the obvious placement and would have silently repointed
the streaming turn at somebody else's answer — a wrong-content bug, which is
strictly worse than a transient overshoot. The start of a turn is the one moment
no live index exists.

**Blast radius.** Bounded and small: the ceiling is a bound on SESSIONS, and a
single answer is bounded by the daemon and the model. A 2 MiB ceiling overshot
by one 50 KB answer is a 2.5% excursion that the next turn corrects.

**Pinned by.** `TestTwoThousandTurnSoakStaysWithinItsBounds`
(`clients/tui/transcriptbound_test.go`) allows exactly `defaultMaxTurns + 2` and
would fail if the overshoot grew beyond one exchange.

**Trigger.** Any change that makes a single turn able to produce unbounded
turns — streaming tool activity without a cap, or a sub-agent whose every step
becomes a turn. At that point the eviction has to move inside the turn and the
two index fields have to be rebased with it.

**The memory numbers this ceiling is anchored to, and a superseded figure.**
The ceiling derives from measurements taken on this tree: **8.6 MB idle,
13.7 MB at 120 turns** (consistent before and after the render cache — peak RSS
was not what the cache changed), and **30 MB at 2,000 unbounded turns** against
3.1 MB of heap in use once the bound is applied.

An earlier baseline figure was supplied for 120 turns and **could not be
reproduced across two attempts**, including varying the per-turn answer size
from 1.2 KB to 8 KB. It is deliberately not repeated in this register or in the
readiness statement, so that a reader who encounters it in older text — a
commit message, a superseded report — does not re-derive a ceiling from it.
**If a memory bound is ever recomputed, recompute it; do not inherit a number
from prose.**

---

## R1.12 — Repainting a transcript at the bound costs 22 ms, over budget

**Status: OPEN. Re-measured 2026-09-04 at the ceiling; the number below replaces
an extrapolation.** The row previously said the cost was unbounded and merely
rate-limited. That was written before task 3.5 put a ceiling on the transcript,
and linear-in-bytes with bytes bounded is bounded — so the risk had changed shape
and nobody had measured it. It is now bounded **and over budget**, which is a
different row from the one this was.

**What it is.** Coalescing (3.3) capped repaints at one per `refreshInterval`
(16 ms) but did not make a repaint cheaper. At the transcript ceiling —
**490 turns, 2,096,839 bytes, after 512 evictions**, both ceilings binding at
once — one repaint measures:

| | value |
|---|---|
| p50 | **22.4 ms** |
| p99 | **29.9 ms** |
| min / max | 21.9 ms / 42.3 ms |
| spread (max/min) | 1.9× |
| against the 8 ms repaint budget | p50 **2.8×**, p99 **3.7×** |
| against D-1's 16 ms hard per-Update ceiling | p50 **1.4×** |

**Both lines are exceeded, and the second one matters more.** 8 ms is the budget
for a repaint specifically — half a frame, leaving the other half for the input
that shares it. 16 ms is D-1's hard ceiling on *any single Update*, and a repaint
is an Update. **At the transcript bound the client breaches D-1's hard ceiling.**
That is stated here rather than softened: it was not true at the 240 prior turns
everything else in this pass was measured at, and it is true at the bound the
product actually permits.

**Where it goes**, medians at the ceiling, cache warm:

| Stage | Cost | Share | At 240 turns / ~300 KB, for comparison |
|---|---|---|---|
| `transcriptCache.render` | 334 µs | 1.5 % | 11 µs |
| `wrapToWidth` (`ansi.Wrap`) | **17.6 ms** | **79 %** | 2.69 ms |
| `viewport.SetContent` | 4.5 ms | 20 % | 0.61 ms |

The wrap and the viewport are both linear in total bytes to within 8 % over a 7×
range, so the shape was right — what needed measuring was which side of the
budget it lands on, and it lands on the wrong side. A **cold** repaint, cache
empty, as after a resize, is 31–47 ms.

**Why still deferred.** Neither remaining stage is ours. `ansi.Wrap` re-parses
the whole transcript; `bubbles`' viewport re-splits it and re-measures every
line, with `m.lines` unexported so there is no way to hand it an incremental
update. The two available fixes — caching the wrapped output per block, measured
byte-identical to whole-string wrapping at widths 1, 2, 20, 40, 80 and 200 so it
is available; or replacing the viewport — are both larger than this pass, and
this measurement was explicitly taken to inform that decision rather than to
start it.

**Blast radius.** CPU and battery during streaming in a session that has run to
the bound, plus **one frame of input latency at the bound**: a keystroke arriving
during a repaint waits up to 22 ms rather than the 3 ms it waits at 240 turns.
Not correctness, and not a stall — it is one repaint, not a queue, because
coalescing means the next repaint cannot start until 16 ms after this one
finishes. Nothing accumulates.

**Pinned by.** `TestRepaintCostAtTheTranscriptCeiling`
(`clients/tui/repaintceiling_test.go`) reports all of the above on every run with
the distance to the budget printed, and fails only on a 3× regression against
today's cost — the budget is knowingly unmet, and a permanently red gate is
indistinguishable from no gate within a week. The deterministic half is
`TestRepaintAllocationsAtTheCeilingAreBounded`: **39 allocations** per token plus
its repaint at the bound, against **2,734** with the render cache neutered.
`ceilingTranscript` fails the test if the transcript it built is not actually at
the ceiling, so the measurement cannot quietly run at half scale.

**Trigger.** A report of fan noise, battery drain, or laggy typing in a long
session. Two cheap moves exist and neither has been made: raising
`refreshInterval` from 16 ms to 33 ms halves the sustained CPU at a cost nobody
is likely to see in streaming text, and lowering the byte ceiling from 2 MiB
moves this figure proportionally. The expensive move — per-block wrap caching —
is the one that fixes it rather than trading against it.

---

## R1.13 — Two dependency surfaces are scanned by nothing

**What it is.** `govulncheck` covers the six Go modules and nothing else. Two
things ship or run alongside them and are scanned by no gate in this repository:
the **onnxruntime native library** the embedder helper links against via CGO,
and the **VS Code extension's npm dependencies**.

**Why deferred.** Both need a different tool from the one in place, and picking
one is a decision about what the project intends to promise. Recorded here
rather than hidden inside `docs/ADR-002-vendoring-and-sbom.md`, which recommends
against an SBOM today partly *because* an SBOM covering only the Go modules
would omit precisely the component an auditor would care most about — and a
partial claim presented as complete is worse than no claim.

**Blast radius.** Unknown, which is the point: a reachable flaw in either is
invisible to every gate that currently reports green.

**Pinned by.** Nothing. This row is a coverage gap, stated as one.

**Trigger.** Shipping to anyone who performs a supply-chain review; a published
onnxruntime advisory; or adding any npm dependency to the extension that
handles untrusted input.

---

## R1.14 — The terminal client is not built by the release workflow — **CLOSED 2026-09-04**

**What it was.** `.github/workflows/release.yml` built `codeterminal-daemon` and
`codeterminal-embedder-helper`. It did not build `clients/tui`. The terminal
client was compiled and tested by CI but produced as a release artifact by
nothing.

**Why it was recorded as a risk and is now closed as a blocker instead.** The row
called this "a packaging decision rather than a defect" and declined to invent a
distribution channel. That reading was too generous, and the thing that exposed
it was one of this document's own release conditions: *"CI runs green once before
release."* That condition cannot be met for a target the release does not build.
A risk that makes a release condition unsatisfiable is not a residual risk; it
sits underneath the conditions.

**What was done.** `clients/tui` now builds on all three release runners —
`CGO_ENABLED=0`, verified pure Go, `-trimpath` like the other two — and ships as
a **standalone download** attached to the GitHub Release with a SHA256SUMS file,
not inside the `.vsix`. The extension neither references nor launches it, and
three copies of a 7.5 MB binary nothing runs would have added 22 MB of package
for nothing.

It is also in `scripts/macos-sign-and-notarize.sh`'s binary list. A standalone
binary a user downloads and runs from a terminal is precisely what gets
`com.apple.quarantine` attached; unsigned, Gatekeeper kills it with no
explanation — the same failure that made signing a hard gate for the daemon.

**Two gates, because one of them is editable by hand.** The staging loop asserts
**three targets in, three files out** and fails the release rather than
publishing a version with no terminal client in it. And `verify-vsix.js` now
lists `codeterminal-tui` under MUST_NOT_MATCH, so the "it is not in the package"
half is asserted against the archive itself rather than trusted to a `cp` line —
the packaging self-test refuses it, and neutering the pattern turns the
self-test red.

**One real defect found while doing it.** The vsix staging used
`cp -a "artifacts/binaries-$TARGET/." daemon/`, a wildcard that would have
silently swept the new binary into every package. Replacing it with a named copy
introduced a second: `ls a b | head -1` under `set -euo pipefail` dies on the
missing `.exe` candidate — `ls` exits 2 and pipefail propagates it — so the job
would have failed *before reaching its own error message*. Both found by running
the loop rather than by reading it.

**Superseded fields.** *Blast radius* and *Trigger* no longer apply; the trigger
("any decision to distribute the terminal client as a binary") has fired and been
answered.

---

## R1.15 — macOS is deferred from the first release: nothing signs the darwin binaries

**Status: DEFERRED BY DECISION. Ruled 2026-09-05 by the founder** (the role this
repository's `docs/DECISION_PACK.md` uses for all eight prior rulings; no personal
name was supplied and none is invented here). **The first release ships
`linux-x64` and `win32-x64` only. `darwin-arm64` is deferred until the five
`MACOS_*` signing secrets exist.**

**Which state this row is in, stated once.** It is **deferred by decision** — it
is **not** "blocked on secrets". Those are different objects and this row must not
read as both, for the same reason R1.5 and R1.6 could not go on sharing a phrase
with R1.3. Nothing about the first release is now waiting on the secrets: the
platform set was decided and the release can proceed without them. What the
secrets govern is *when macOS rejoins*, which is the trigger, not the blocker.

*(Opened 2026-09-05, by the first dispatch of `release.yml` in the workflow's
existence. It could not have been found by reading anything.)*

**What it is.** `.github/workflows/release.yml`'s **Sign & Notarize (macOS)**
step runs `scripts/macos-sign-and-notarize.sh`, which is written to be a **no-op
when the five `MACOS_*` secrets are absent**. They are absent. The step takes its
dry-run branch, the job goes green, and the release produces **unsigned
darwin-arm64 binaries**. Gatekeeper attaches `com.apple.quarantine` to a
downloaded unsigned Mach-O and kills it, so the daemon never starts and the user
sees an unexplained "daemon not running".

Measured, not inferred — run `33922431985`, job `binaries (darwin-arm64)`:

```
[macos-sign] WARNING: No MACOS_CERT_P12 or MACOS_CERT_P12_BASE64 secret supplied.
[macos-sign] Signing step completed in DRY-RUN mode (unsigned).
[macos-sign] WARNING: darwin-arm64 binaries will remain UNSIGNED.
##[warning] darwin-arm64 binaries are UNSIGNED. Gatekeeper will quarantine them
and the daemon will not start. Do not publish this target.
```

The step's own env block shows all five secrets resolving empty.

**Why unfixed.** The secrets are **repository-admin scope**, not this branch's
and not this pass's. Nothing in the tree can supply them, and inventing a
signing identity is not a change a code branch gets to make. The workflow half is
already correct: the script, the signing list and the warning all exist and all
behave as designed. What is missing is a credential somebody has to install.

**Blast radius.** A **published macOS artifact that does not run at all** — the
worst shape available, because it fails after download with no diagnostic the
user can act on. The workflow's own comment states the trade in as many words:
*"shipping an unsigned macOS package is worse than shipping none."* **Linux and
Windows are unaffected**; their binaries are unsigned by design and only
SmartScreen warns.

**Pinned by.** The workflow's **own warning**, which fires on every release run
until the secrets exist — both from the script and from the `Record signing
status` step, which emits a GitHub `::warning` naming the consequence.

**And the reason this went unnoticed until today is worth more than the row.**
**The run is green either way.** A step written to be a no-op reports *success*
for the state in which it did nothing, so every reading of `release.yml` — and
there were several during P4.1 — saw a signing step present, correct and wired
into the binary list, and concluded signing was handled. It is the **fail-open
pattern the gate audit removed twelve instances of**, appearing one layer up, in
the release pipeline rather than in a gate script: *inspected nothing, reported
ok*. The audit checked `scripts/`; nobody had pointed the same four questions at
a workflow step. **A warning is not a gate.** Making this fail closed — refusing
to produce a darwin artifact at all without the secrets — is the obvious
mechanism and is deliberately not built here, because it would turn every branch
dispatch of `release.yml` red and that is the release owner's call, not this
branch's.

**Trigger — unchanged by the deferral, and it now carries two meanings.** This
row closes when the five `MACOS_*` secrets are added and the warning stops firing
on a release run. No judgement is required and nothing has to be re-derived: the
workflow says which state it is in, on every run, in its own log. **And since
2026-09-05 the same condition also means "this reopens when someone wants macOS
shipped"** — the deferral and the technical gap close together, because the only
thing standing between the current state and a shippable darwin artifact is the
credential.

### The machinery now matches the ruling — the signing guard, 2026-09-05

**It did not, and that was the gap.** On a real tag `release.yml` would have
attached **two unsigned darwin assets** — the `darwin-arm64` `.vsix` and the
standalone `codeterminal-tui-darwin-arm64` — because the `files:` list globs
`out-vsix/*.vsix` and `out-bin/codeterminal-tui-*`, and both swept darwin in,
with **both checksum manifests naming them**. The release is a **draft**, so a
human still clicks publish, but a draft with the assets already built, named and
checksummed is not a control. **A decision the pipeline does not implement is a
decision in one place only.**

`scripts/release-signing-guard.sh` closes it, approved and built 2026-09-05.

**The predicate is "was this signed?", not "is this darwin?"**, and that is the
whole design. A hard exclusion was **rejected**: it would encode *"we do not ship
macOS"* in the machinery when the ruling is that macOS is **deferred**, leaving a
line somebody must find and revert the day the secrets land with nothing that
notices if they do not — a second place the ruling lives, and this repository has
been burned by that shape twice (the debt-marker "gate" that checked one module
of six; the register checker that missed a fourth register).

#### The marker asserts DISTRIBUTABLE, not CODESIGNED

**This distinction is the row's most reusable sentence and is written down so a
fifth path is judged against the right question.** `SIGNED-<target>` is written
**only when the binaries are both codesigned AND notarized**, because
**Gatekeeper blocks a signed but un-notarized download exactly as it blocks an
unsigned one** — the user's failure is identical, so the release predicate must
treat them identically. A marker meaning merely "codesign exited zero" would be
technically accurate and operationally wrong, and a reader seeing
`SIGNED-darwin-arm64` would reasonably conclude it ships.

**Four paths end without that marker.** Enumerated so a fifth added later is
visibly not covered:

| # | Path | `reason=` |
|---|---|---|
| 1 | no target binaries found in `DIST_DIR` | `no-binaries` |
| 2 | no signing certificate supplied | `no-certificate` |
| 3 | host is not macOS | `not-macos` |
| 4 | **signed, but notarization credentials incomplete** | `not-notarized` |

Path 4 was **not** in the approved specification, which said the predicate was
"signed". It was added because that branch previously fell through to
*"completed successfully"*, so a marker written there would have said *signed*
and reproduced this row's own blast radius one step further along. Flagged at
the time rather than done silently, and confirmed by the founder the same day:
**the marker's meaning is distributable.**

**Fail closed.** The guard keys on the **presence of `SIGNED-<target>`**, never
on the absence of `UNSIGNED-<target>`. Reject-by-default, the same rule as the
terminal sanitizer: a path added later that forgets to write anything is treated
as **unsigned**, which is the safe direction. `UNSIGNED-<target>` carries a
machine-readable `reason=` for diagnostics only and the guard never needs it to
reach the right answer. `SIGN_TARGET` is required and deliberately not defaulted
— the marker's name decides what may ship, so a guess is worst there.

**Two behaviours, split on the trigger**, and the split is what made this
buildable at all:

| Trigger | Marker absent |
|---|---|
| `workflow_dispatch` | **Exclude** that target's assets, warn loudly, job stays **green** — a dispatch is a rehearsal and must stay usable. A guard that turned every branch dispatch red is why this was left unbuilt before. |
| tag `refs/tags/v*` | **Fail the job** — a tag is a release, and silently shipping fewer platforms than the tag implies is its own fail-open. |

**Manifests are regenerated, not edited.** Both `SHA256SUMS` and `SHA256SUMS-tui`
named the darwin assets; a manifest listing a file the release does not carry
reads as a missing download rather than a deliberate omission, and hand-pruning
lines is how a checksum and its file drift apart. Rebuilding from what is on disk
cannot disagree with what is on disk.

**The build and staging jobs are untouched.** Darwin is still built, packaged and
staged, and both `upload-artifact` steps still carry it — rehearsing the build is
the point of a dispatch. The guard runs after them and controls only what is
**released**.

#### What this buys the person who adds the secrets

**`darwin-arm64` ships with ZERO EDITS the day the five `MACOS_*` secrets land.**
No line to revert, no flag to flip, no second place the decision lives: the
signing step produces the marker, the guard stops excluding, and the target is in
the release. That property is the reason this shape was chosen over a hard
exclusion, and it is recorded here because this row is where somebody adding
those secrets will look.

**And the same mechanism covers Windows Authenticode later without a second
guard.** `win32-x64` moves from `NO_SIGNING_NEEDED` to `NEEDS_SIGNING`, its
signing step writes `SIGNED-win32-x64`, and the release side already understands
it. Every built target must appear in exactly one of those two lists; an
unclassified target **fails** rather than defaulting to "no signing needed",
which is the same rule `scripts/coverage-floors.txt` applies to packages.

#### Neutered before it was trusted

40 assertions pass, and **six neuters were each demonstrated RED** rather than
reasoned about:

| Neuter | Assertions that went red |
|---|---|
| A — `is_signed` always returns true | 13 |
| **B — key on the ABSENCE of `UNSIGNED` instead of the PRESENCE of `SIGNED`** | **2** |
| C — drop the manifest regeneration | 1 |
| D — treat a tag like a dispatch | 9 |
| E — signing path 2 forgets to write its marker | 4 |
| F — signer defaults `SIGN_TARGET` instead of failing | 1 |

**B is the one to notice.** It is the fail-open inversion the entire design turns
on — the plausible, tidy-looking refactor a future reader might make, reasoning
that "no UNSIGNED marker means it must be fine". It is not fine: a signing path
that writes nothing at all then reads as signed. **That inversion was tested, not
assumed**, and it fails two assertions including the reject-by-default case. Do
not "simplify" it back.

#### Verified on a real runner — dispatch branch

**Run `33938839311`, commit `d48530d`, `event: workflow_dispatch`: success**, all
four jobs, `publish` skipped by design. The guard's own log:

```
[signing-guard] ref=refs/heads/audit/adversarial-pass (tag release: no)
[signing-guard] WARNING: EXCLUDING darwin-arm64 from the release: unsigned (no-certificate).
[signing-guard] regenerated out-vsix/SHA256SUMS
[signing-guard] regenerated out-bin/SHA256SUMS-tui
[signing-guard] excluded from release: darwin-arm64
[signing-guard] --- release input, after the guard ---
[signing-guard]   WOULD ATTACH  codeterminal-vscode-linux-x64-0.0.1.vsix
[signing-guard]   WOULD ATTACH  codeterminal-vscode-win32-x64-0.0.1.vsix
[signing-guard]   WOULD ATTACH  SHA256SUMS
[signing-guard]   WOULD ATTACH  codeterminal-tui-linux-x64
[signing-guard]   WOULD ATTACH  codeterminal-tui-win32-x64.exe
[signing-guard]   WOULD ATTACH  SHA256SUMS-tui
```

**Zero darwin assets reach the release. `linux-x64` and `win32-x64` are
untouched** — both `.vsix` packages and both terminal-client binaries are still
there, including the `.exe`. The run agrees with the local reproduction in every
particular.

**On the manifests, stated as the chain it actually is rather than as a direct
observation.** The `regenerated` lines print only when `sha256sum` succeeded, and
it regenerated from the very directories the listing then enumerates — which
contain no darwin file. Zero darwin lines follows from those two facts. The
assertion itself is made directly by the self-test (`SHA256SUMS drops darwin`,
`SHA256SUMS-tui drops darwin`) and neuter C, which removes the regeneration,
turns it red. The manifest *contents* are not echoed in this run's log.

**And the run proved one thing the local test could not.** The
`reason=no-certificate` in that warning was read from a marker written by
`macos-sign-and-notarize.sh` **on a real macOS runner**, then carried through
`upload-artifact` → `download-artifact` into the `package` job on Linux. The
marker round-trip across jobs and platforms is the part a simulated staging tree
cannot exercise, and it worked on first contact.

#### Three things remain unverified, and all three are EXPECTED

**Written down in advance so whoever cuts the first real tag meets them as
foreseen rather than as surprises.** Unverified-and-anticipated is a different
state from unverified-and-unnoticed, and only the first can be planned for.

**(i) The tag branch has never run.** The guard's failing path — the one that
blocks a release — is selected by `case "$ref" in refs/tags/v*)`. Reaching it
needs a tag push, which was ruled out for this branch. **The self-test drives
that branch by passing `refs/tags/v1.0.0` directly, and that is worth having, but
it is not a tag-triggered run**: it proves the branch logic, not that GitHub sets
`github.ref` to what the guard expects. *What will exercise it:* the first real
`v*` tag. **Expect it to fail loudly if the `MACOS_*` secrets are still absent —
that is the guard working, not a broken pipeline**, and the failure message says
so in as many words.

**(ii) Unsigned path 4 — signed but not notarized — has never run.** It needs a
macOS runner **and** a valid signing certificate, with notarization credentials
missing or incomplete. No run has ever had the first of those. *What will
exercise it:* adding `MACOS_CERT_P12` without the three `MACOS_NOTARY_*` values —
a plausible half-configuration, and exactly the state this path exists to catch.

**(iii) The signed path itself has never run — nobody has ever produced a
`SIGNED-<target>` marker outside a test.** Every release run to date, including
`33921667448`, `33922431985` and `33938839311`, took an unsigned branch, because
the secrets have never existed. The guard's handling *of* a signed marker is
covered by the self-test using a marker the test writes; what has never happened
is the signing script writing one. *What will exercise it:* the first run with
all five secrets present. **Expect the first such run to be the real test of this
whole mechanism** — and note that if it goes green and darwin appears in the
release, that is the design working as intended, with no edit required.

### What a dispatch still did NOT exercise

Recorded here because "release.yml is green" is now true and is easy to
over-read.

**A dispatch is not a tag.** The `Attach to the GitHub Release` step is gated on
`startsWith(github.ref, 'refs/tags/v')` and did not run, so **release creation,
asset upload and the draft flag have still never executed**. The `publish` job is
`if: false` and was skipped by design. Signing and notarization are unexercised
**in substance** — the step ran and took the branch in which it does nothing.

What the run *did* verify, on real runners: the terminal client builds with
`-trimpath` on all three platforms; `verify-vsix.js` passes on all three
(`package gate PASSED` ×3); the three-in-three-out staging assertion executes and
passes — *"staged 3 terminal-client binaries"*, with SHA-256 sums, including
`codeterminal-tui-win32-x64.exe`; and `.vsix` checksums are produced.

---

---

## R1.16 — A gate whose expected count is derived from the list it validates

**What it is.** Two gates in this repository compute "how many things should I
have inspected?" from the very list that decides what they inspect. Such a gate
detects a *missing* member and cannot detect a *removed* one, because removing
it changes the expectation by exactly as much as it changes the reality.

The general form, stated once so a third instance is recognisable:

> A vacuity floor derived from the target set is not a floor. It proves the
> gate ran; it cannot prove the set is still the right set. A real floor is
> either an independently-derived count, or a bidirectional correspondence
> against a source the list does not control.

Two instances, both measured:

1. **`clients/tui/platformcoverage_test.go:80`** — the floor is `inspected < 20`
   while 73 suites carry the tag. Two documents and commit `3c217f2` all
   described it as asserting on 73. **The anti-drift property is real but comes
   from somewhere else**: two bidirectional loops (every listed suite must carry
   the tag; every `//go:build linux` file must be listed) check the list against
   the source tree, which the list does not control. The count is nearly
   decorative. Corrected in the documents on 2026-09-05.
2. **`scripts/fuzz.sh`** — `TARGETS` is a hand-edited array of
   `module:FuzzName` strings. A listed target that no longer exists **is**
   caught (`status=1`, "no such fuzz target"), and I over-stated this gap once
   before correcting it: the gate is stricter than I first said. What it cannot
   catch is rows being *deleted from the array*, which shrinks
   `${#TARGETS[@]}` so the run reports "N of N" and exits 0. Its only vacuity
   floor is `ran == 0` — a floor of **one**. It has no equivalent of the TUI
   test's bidirectional loops.

**Why deferred.** The fix for the fuzz gate is known and small — derive
`TARGETS` from the source (`grep -rn '^func Fuzz' <module>`) instead of hand
editing it, which supplies exactly the missing property: the list stops being
its own source of truth and starts being checked against the code. It is
deferred because it is a **gate change during an audit that is using that gate**,
and changing a measuring instrument mid-measurement costs more than it buys.
The TUI instance needs no code change at all; its property is already sound and
only its description was wrong.

**Blast radius.** Bounded and specific, not general. For the fuzz gate: someone
deleting targets from `TARGETS` — to quiet a flaky run, or during a refactor
that moves a fuzz function — leaves CI reporting a clean fuzz pass over a
smaller surface, with the reduction visible only in a number nobody compares
across runs. The eighteen targets are the only thing fuzzing the parsers, the
wire formats and the proxy's routing. For the TUI instance: none, today — the
loops hold.

**Pinned by.** **Nothing, for the class.** `platformcoverage_test.go`'s two
bidirectional loops pin *that* instance's real property. No test anywhere
asserts that `scripts/fuzz.sh` inspects the number of targets the source
defines, and no test asserts the general form. This row is a coverage gap and
says so.

**Trigger.** Any of: a fuzz target being removed from `TARGETS` in a commit that
does not also delete its `func Fuzz`; a third instance of the form being found;
or the next change to `scripts/fuzz.sh` for any reason, at which point deriving
the list costs a few lines and should just be done.

---

## R1.17 — An ambiguous request body is refused with a message about something else

**What it is.** After `026fe48`, a request body naming two request types —
`{"undo":true,"edit":{...}}` — is correctly refused instead of being dispatched
by declaration order into the destructive handler. But the refusal it gets is
the fall-through to the prompt path, so the client is told **`"prompt is
empty"`**. That is true and useless. The client asked an ambiguous question and
is told it asked no question at all.

**Why deferred.** Deliberately, and the reasoning is the fixed file's own. The
refusal was made *identical* to the one a duplicate-key body already receives,
because both are the same judgement — "this body has no single meaning" — and
`daemon/requestfields.go` warns in its own header that "two predicates claiming
the same thing is how the two drift apart". Giving the new case a distinct
error would put one rule in two places, which is the defect that file exists to
prevent. **The improvement is real but it is not this fix's to make**: the fix
that gives ambiguity a proper message must change the duplicate-key case too,
so that one rule keeps one answer. That is a separate, wider change to the
dispatch contract and it belongs in its own commit.

**Blast radius.** Diagnostic only, and small. No wrong action is taken — the
destructive path is closed, which was the defect. A client author debugging a
malformed request is sent looking at their prompt field instead of at their
duplicate discriminator. The shipped clients cannot produce this body: every
message `protocol` defines carries exactly one discriminator, verified by
`TestDispatch_OneDiscriminatorStillRoutes`.

**Pinned by.** `daemon/dispatch_ambiguous_test.go` —
`TestDispatch_TwoDiscriminatorsNeverReachTheDestructiveHandler` (7 rows) pins
the *refusal*, asserting the file on disk is unchanged and no
`"applied":true` is returned. **Nothing pins the message**, deliberately: a
test asserting `"prompt is empty"` would make this rough edge a contract and
make the eventual fix look like a regression.

**Trigger.** A client author or user reporting confusion at the message; or any
work on the duplicate-key refusal path, which must fix both together.
