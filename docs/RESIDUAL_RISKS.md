# Residual risk register — clients/tui

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

**What it is.** `clients/tui/slash.go:396-397` runs `codeterminal-daemon mcp
list` with `CombinedOutput()` and displays the result. That captures the
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

---

## R1.7 — `git status` failures echo git's own output

*(From the 2.3a enumeration. Lowest severity row here.)*

**What it is.** `clients/tui/slash.go:299-305` runs `git status -sb` and, on
failure, prints `git status failed: %v` plus the combined output. A git error
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

## R1.9 — A 1 MB paste costs most of a frame, and 3.2 will not fix it

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
