# Residual risk register — clients/tui

<!-- coderefs: enforced -->

Opened 2026-09-04 during the production-hardening pass on the TUI.

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

## Reconciliation — 2026-09-04, after tasks 3.5 through 3.10

Every row re-read against the finished tree. **Status is one of: OPEN (still
true, still unfixed), CLOSED (the situation no longer exists), or SUPERSEDED
(replaced by a different row).**

| Row | Status | Note |
|---|---|---|
| R1.1 pty destruction delivers no SIGHUP | **OPEN** | Unchanged. Still pinned by a test that fails if the gap closes. |
| R1.2 over-long CSI leaks parameter bytes | **OPEN** | Unchanged. Bounded, measured, pinned at 14 shapes. |
| R1.3 one-shot stdout not byte-stable | **OPEN by decision** | A deliberate trade, in the release notes. Not a defect awaiting a fix. |
| R1.4 review/approval buffers hold raw bytes | **OPEN by design** | Its AST guard did its job during 3.7: the decomposition moved the reader out of `Update` and the guard failed until the reviewed list moved with it. |
| R1.5 `/mcp-server` stderr unredacted | **OPEN, and UNFIXED BY DECISION** | See below. |
| R1.6 model-emitted secrets not redacted | **OPEN, and UNFIXED BY DECISION** | See below. |
| R1.7 `git status` echoes git's output | **OPEN** | Unchanged. No work in this batch touched it. |
| R1.8 `ModelError.detail` safe by field privacy | **OPEN (forward guard)** | Unchanged. Still a property, not a redactor. |
| R1.9 1 MB paste costs most of a frame | **CLOSED** | Task 3.6. **Figure corrected at P3.3:** the 3.6 number was measured with the input blurred, so the paste was dropped. Re-taken where it lands: **389 µs, identical at 0 bytes and at the 2 MiB ceiling.** Details in the row. |
| R1.10 locale reaches the input line | **OPEN** | Unchanged, still pinned both ways. |
| R1.11 the transcript ceiling can be overshot within one turn | **OPEN (new)** | The price of index safety; bounded to one exchange and asserted. |
| R1.12 repainting a deep transcript costs real CPU | **OPEN, re-measured at the ceiling** | Was an extrapolation from 240 turns; now 22.4 ms p50 measured at the bound, over both the 8 ms repaint budget and D-1's 16 ms hard ceiling. Bounded, and over budget. |
| R1.13 onnxruntime and npm are scanned by nothing | **OPEN (new)** | A coverage gap in the supply-chain story, stated as one. |
| R1.14 the terminal client is not a release artifact | **CLOSED 2026-09-04** | P4.1. It builds on all three release runners, ships as a signed standalone download, and is asserted *out* of the `.vsix`. It was not a residual risk: it made this document's own "CI green once" condition unsatisfiable. |

### R1.5 and R1.6 are unfixed BY DECISION, not unexamined

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
[docs/DECISION_MEMO_REDACTION_2026-09-04.md](DECISION_MEMO_REDACTION_2026-09-04.md).
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
[the memo](DECISION_MEMO_REDACTION_2026-09-04.md), which recommends FIXING this
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
[The memo](DECISION_MEMO_REDACTION_2026-09-04.md) recommends REJECTING an inbound
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
