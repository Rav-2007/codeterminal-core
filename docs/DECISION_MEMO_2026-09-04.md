# Decision memo — five items in `daemon/`, from the terminal-client hardening pass

<!-- coderefs: enforced -->

**Date:** 2026-09-04
**From:** the `clients/tui` production-hardening pass (branch `audit/adversarial-pass`)
**To:** whoever owns `daemon/` — **an addressee slot, not a recipient.**
**What I need:** a decision, in writing, on each of the **five** items below.

> **Count corrected 2026-09-13.** This line and the summary table said FOUR from
> 2026-09-04 until today, while item 5 has been in the body since 2026-09-09 and
> "What I need from you" has asked for five since the same day. A document whose
> header contradicts its own body is how a reader stops at item 4, and three
> readers did.

> **Delivery status, updated 2026-09-09: STILL COMMITTED; STILL HANDED TO
> NOBODY. Five days, one re-derivation, no reader.**
>
> On 2026-09-08 a later pass independently re-derived item 1 from scratch,
> measured it end to end, and filed it as a new register row (R1.22) — because
> nothing pointed at this memo. That is the cost of an undelivered document
> stated in hours: the same finding, found twice. The duplicate row now
> defers to this one.
>
> **Delivery status, added 2026-09-05: COMMITTED; RECIPIENT NOT YET IDENTIFIED.**
> This memo was committed to `audit/adversarial-pass` on 2026-09-04 and **handed
> to nobody.** Nobody has been named as the `daemon/` owner and nobody has been
> asked to read it. Other documents in this pass described it as "handed to the
> daemon's owner"; that was wrong and is corrected. If you are reading this, you
> are the first — which means the five items below have had no answer because the
> question had not been put, not because anyone declined to answer it.

---

## Correction, 2026-09-09 — three of this memo's code references had drifted

`daemon/server.go:547`, `:687` and `:595` now read `:567`, `:707` and `:615`.
The three cited lines had moved and pointed at unrelated code. **The coderefs
gate was green the whole time**, and correctly so: its own header says it cannot
catch a line that moved inside a file that is still long enough, which is
exactly what happened. A reconciliation found them, not a gate.

## Read this first

**A recorded deferral with a trigger closes the release gate exactly as well as a
fix does.** "We accept this; revisit when X happens" is a complete answer. So is
"fix it". What does not close the gate is silence, and the gate is currently
blocked on these four having no recorded answer either way.

So this is **not a work request**. None of it needs implementation time from you
today. It needs about ten minutes and a written reply — even if the reply is
"accepted, revisit at the first enterprise customer".

Each item below states **a recommendation**, not a menu. I did the enumeration
and you did not; handing you options without a recommendation would push the
analysis back onto you and stall the decision, which is the outcome this memo
exists to avoid. Disagree freely — but disagree with a position rather than
choose from a list.

**Everything here was verified by execution on 2026-09-04, not by reading code.**
Where a number appears, it was measured on this tree. The probes were temporary
and have been removed; any of them can be reproduced on request.

### The five, and what I am asking

| # | Item | Recommendation | Severity |
|---|---|---|---|
| 1 | The outbound secret scrub is bypassed by one turn | **FIX** | **Highest.** A secret the daemon redacts still reaches the provider. |
| 2 | R1.5 — `/mcp-server` puts MCP-server stderr on screen unredacted | **FIX** | Moderate. Disclosure to the credential's owner. |
| 3 | R1.6 — inbound model text is not redacted | **ACCEPT, in writing** | Low, once (1) is fixed. |
| 4 | Four `daemon/` fuzz targets never generate an input in CI | **FIX** — cause found, fix measured | Low, but CI's fuzz job contributes nothing for them. |
| 5 | Panic containment stops at the connection goroutine | **FIX the half that is ours** | Unmeasured likelihood; a panic takes the daemon, not the connection. Added 2026-09-09. |

---

## 1. The outbound scrub is bypassed by one turn — **RECOMMEND FIX**

**This was in nobody's brief.** It surfaced while answering the question that
gates item 3, and it is a defect in a control that already exists rather than a
request for a new one.

### What it does

```
what goes on the WIRE:   "deploy with [REDACTED:openai_key]"   (1 redaction)
what is on DISK (user):  "deploy with sk-BBBB…"                 <-- RAW
what is on DISK (asst):  "sure, using sk-BBBB…"                 <-- RAW
turns_fts row:           "deploy with sk-BBBB…"                 <-- RAW, and indexed
```

and separately, through the history path:

```
history message 0 (user):      "here is my key: sk-AAAA…"       <-- LEAK
history message 1 (assistant): "I will use sk-AAAA…"            <-- LEAK
prompt path, same string:      "here is my key: [REDACTED:openai_key]"
```

### Why it happens — three call sites, each individually reasonable

1. `daemon/server.go:567` computes `cleanPrompt, redactions := scrub(promptReq.Prompt, …)`
   and sends `cleanPrompt`. **Correct.**
2. `daemon/server.go:707` calls `s.persistTurn(promptReq.Prompt, full.String(), …)`
   — **`promptReq.Prompt`, not `cleanPrompt`.** The raw string is written to
   `memory.db`, and `daemon/search.go:42`'s INSERT trigger copies it into the
   `turns_fts` full-text index.
3. `daemon/history.go:136` `prepareHistory` validates, annotates, caps by count
   and caps by bytes. **It does not scrub.** Its output goes straight into
   `buildChatMessages(…, historyOutcome.Messages, …)` at `daemon/server.go:615`
   and out to the provider.

So the round trip is: **redacted on turn 1 → stored raw → re-hydrated into the
client's transcript at the next handshake → sent raw on turn 2, and on every turn
after.** `loadPersistedHistory` re-runs `prepareHistory` on the way back, which is
where it would have been caught if `prepareHistory` scrubbed.

### Why this is a defect and not a design question

The daemon has **already decided** that `sk-BBBB…` does not go to the provider.
It made that decision, logged it, and told the user about it in the header.
Sending the identical bytes sixty seconds later is not a different policy — it is
the same policy, not applied. Nothing here asks anyone to judge what a secret
looks like; that judgement was made by the existing shape list and is simply not
carried through.

### Blast radius

- The secret **reaches the model API anyway**, one turn late. That boundary
  crossing is the scrub's entire purpose and the one you cannot undo.
- It is **on disk in plaintext** in `memory.db`, and in an FTS index beside it.
  The file has mode hardening; plaintext-at-rest is still a different claim from
  what the redaction notice implies.
- It is **worse than never having scrubbed**, in one specific way: the user was
  shown a redaction notice and reasonably concluded the key did not leave.

### The fix, and its cost

| Part | Change | Cost | New judgement required? |
|---|---|---|---|
| **1a** — persistence | `persistTurn(cleanPrompt, …)` at `daemon/server.go:707` | one identifier | **None.** Applies a decision already taken about that exact string. |
| **1b** — history, user turns | scrub `Content` where `t.Role == "user"` in `prepareHistory`, **before** the byte budget | ~10 lines | **None.** Same argument. Before the budget, so the budget accounts for what is actually sent. |
| **1c** — history, assistant turns | scrub those too | ~2 lines | **Yes** — that is item 3. Strictly stronger, and your call. |

**Recommend 1a + 1b now**; 1c only if you also reject my recommendation on item 3.

### What 1a + 1b do NOT close — corrected 2026-09-13

> **RETRACTION. The version of this section added on 2026-09-12 (commit
> `d732c34`) was WRONG, and it was wrong in the direction that would have made
> your decision harder than it is.**
>
> It claimed 1b does not cover the handshake payload, because
> `HandshakeResponse.PersistedHistory` "does not go through `prepareHistory` on
> its way out". **It does.** `loadPersistedHistory` calls
> `MemoryStore.LoadRecentTurns`, and that function runs its rows through
> `prepareHistory` before returning them (`daemon/memory.go:330`). Scrubbing
> inside `prepareHistory` therefore covers the handshake payload as well as the
> outbound message list — **1b is cheaper and broader than that note claimed.**

**A METHOD FINDING, recorded here rather than as an errata line, because the
pattern is the durable part.**

This was the SECOND shallow trace of the SAME call chain. The first was filed as
finding F-1.6 — "`loadPersistedHistory` caps turns but not bytes" — which was
retracted when its own test passed, for the identical reason: the byte ceiling
is also applied inside `LoadRecentTurns`, one layer below the function being
read. **The second error was made while writing the retraction of the first.**

The diagnosis is not "insufficient care". It is a specific and repeatable habit:
**stopping at the first function that looks like the owner of a property.**
`loadPersistedHistory` reads like the place history-loading policy lives — it
has the name, it applies `validTurn`, it caps turns — so both times the trace
stopped there, and both times the actual policy sat one call deeper in a
function named after storage rather than after policy. A reviewer checking
either claim by reading the same function would have agreed.

The general form, for anyone auditing this daemon: **a function that applies
SOME of a policy is the most misleading possible place to stop**, because
partial application reads exactly like complete application. Trace to the end of
the call chain, not to the first plausible owner.

### The path that IS outside 1b, found by tracing properly

`MemoryStore.SearchTurns` (`daemon/search.go:128`), reached from the `/search`
command at `daemon/server.go:1007`, returns `snippet(turns_fts, …)` — a fragment
of the raw stored `content` column. It passes through **neither** `prepareHistory`
**nor** `scrub`.

**This SHARPENS THE MIGRATION QUESTION rather than adding a new defect**, and the
distinction matters for what you are deciding:

- After **1a**, newly written rows are scrubbed at rest, so `/search` returns
  scrubbed snippets of them. No further work is needed for new conversations.
- Rows written **before** 1a keep their raw content in `turns` and in the
  `turns_fts` index. 1b does not touch them, because 1b acts on the read path
  into the model and `/search` is a different read path.
- So without a migration, **a secret pasted before the fix stays retrievable by
  `/search`, on that machine, indefinitely** — and it is retrievable by the
  string that matches it, which is the one query an attacker with access would
  run.

**Three states, which must never be collapsed into the single word "fixed" (M5):**

| Path | After 1a + 1b, with no migration |
|---|---|
| turn N+1 to the provider | **closed** — this is the boundary crossing that leaves the machine |
| handshake payload to the client transcript | **closed**, via `LoadRecentTurns` → `prepareHistory` |
| rows written before the fix, at rest and via `/search` | **STILL RAW** — this is the migration decision, and it is the whole residual |

A note on this memo's own history, which is the argument for reading it rather
than re-deriving it a fourth time: the delivery status above predicted its own
re-derivation and was right twice. Item 1 was found by execution on 2026-09-04,
independently re-derived as R1.22 on 2026-09-08, and independently re-derived
again as F-1 on 2026-09-12. **The same finding, found three times, because the
`daemon/` owner was an unassigned ROLE rather than an unresponsive PERSON** —
nobody declined to read this; nobody had been asked.

---

## 2. R1.5 — `/mcp-server` puts MCP-server stderr on screen — **RECOMMEND FIX**

### The chain, end to end

1. `runMCPServerList` (`clients/tui/slash.go:381`) runs
   `codeterminal-daemon mcp list` with `CombinedOutput()` at
   `clients/tui/slash.go:411` and puts the result in a system turn **on the
   user's screen**.
2. `mcp list` **starts the configured servers** — that is how it enumerates
   tools. `daemon/mcp_cmd.go` builds a registry and `daemon/mcpruntime.go:208`
   connects each server with `serverStderr(name, logger)`.
3. `serverStderr` returns a `prefixWriter` that escapes and bounds each line and
   writes it to `logger`, which in the subcommand path is
   `log.New(os.Stderr, …)` (`daemon/main.go:30`).
4. `CombinedOutput()` captures exactly that stream.

**A third-party MCP server that prints a credential at startup reaches the user's
screen.** This is the only path in the client that puts daemon stderr in front of
a person.

### Where the fix belongs, and why not in the TUI

**In the daemon, at `mcp.Connect`.** That function already computes
`cmd.Env = ServerEnv(cfg.EnvAllow)` — the literal `NAME=value` pairs. Wrapping
`cfg.Stderr` there means the redactor is built from **exactly the bytes this
daemon handed this subprocess**, and every launch goes through `Connect`, so a
new call site cannot skip it.

**A TUI-side redactor was rejected, and the reason is not layering.** The client
sees an opaque blob of text. It does not know what was provisioned, so it could
only match **shapes** — and this is a *coding assistant*, whose own output
routinely contains key-format documentation, example configuration, and test
fixtures carrying deliberately fake keys. A shape matcher in the client mangles
those, and **still misses the one credential that does not look like a
credential**. Wrong in both directions, in the one place a user reads
diagnostics. The daemon is the only layer that knows the literal bytes.

### What the fix catches, and what it does not

**Catches:** every value the daemon passed under `cfg.EnvAllow`. That is where an
operator puts a server's credential by construction — it is the list they had to
write in order to grant it.

**Does not catch, and the row should say so rather than imply otherwise:**

- A credential the server reads from **its own** config file or keychain. The
  daemon never held those bytes.
- Anything a **launcher** adds. `Connect`'s own header records the measurement:
  the daemon handed a reference Node server 2 variables and the child reported
  25, the other 23 being npx's (`NODE`, `PWD`, `INIT_CWD`, eighteen
  `npm_config_*`).

That partiality is **enumerable**, which is the whole difference from a shape
matcher's. "Values this daemon provisioned" is a set you can list; "things that
look like keys" is not.

### Implementation notes and cost

- Strip values for **allow-listed names only**, not `BaselineEnvNames`. Redacting
  `PATH` would turn `cannot find node in /usr/bin:/bin` into
  `cannot find node in [REDACTED:PATH]` and destroy the diagnostic this path
  exists for.
- Skip empty and very short values — a one-character value would replace every
  occurrence of that character. A minimum length of 8 is a judgement; state it in
  the code.
- Replace with `[REDACTED:<NAME>]`, matching `scrub`'s existing placeholder shape
  so a user sees one vocabulary.
- **~40 lines plus tests.** No new dependency, no new concept, no configuration.

**The test that makes it real:** launch `testdata/echoserver` with a known
allow-listed value, have it print that value to stderr, and assert the value does
not appear in what `Connect`'s stderr writer emitted while the variable *name*
does. It fails if the wrapper is removed.

---

## 3. R1.6 — inbound model text is not redacted — **RECOMMEND ACCEPT, IN WRITING**

### The blocking question, answered: shapes, not known values

**`redactionsMsg` carries SHAPES.**

`daemon/scrub.go` is ten compiled regexes — `openai_key`, `stripe_secret_key`,
`aws_access_key`, `github_token`, `slack_token`, `google_key`,
`supabase_secret`, `supabase_publishable`, `mochiii_key`, `private_key_block` —
matched against text. `redactionKinds` reduces each match to a fixed label, and
that label list is what reaches the client and is drawn in the header. The daemon
does not compare against any value it provisioned, because on this path **it
provisioned nothing**: the text is the user's own typing and the model's own
answer.

**What that settles.** If `redactionsMsg` had matched on known provisioned
values, applying the same set inbound would be structural and cheap and I would
be recommending it. It does not. Inbound, "the same set" **is** the ten regexes,
and running them over prose is pattern-matching on the value — so **the
asymmetry is permanent**, and the honest thing is to record that rather than
leave the row reading as a to-do.

### Why an inbound redactor is rejected — three independent grounds

1. **The design constraint rules it out.** Redaction must be structural — a
   property of how the value is carried, the way `ModelError`'s unexported field
   is (R1.8). A secret arriving as a substring of a sentence has no structure to
   attach to.
2. **This product already measured the cost of the looser version and refused
   it.** Decision D5 rejected entropy/keyword redaction on measured data: 33% of
   chunks touched, zero precision. Inbound prose is a *worse* population for that
   than retrieved chunks, not a better one.
3. **It corrupts the product's own output.** A coding assistant explains key
   formats, writes example configuration, and generates test fixtures containing
   fake credentials. Redacting model output means the assistant cannot show you
   what an API key looks like — while still missing the one that does not look
   like one.

### What "accepted" should say, so the row stops reading as a to-do

Three facts, none of which the row currently states:

- The **shapes-versus-known-values question is answered**: shapes. Nobody needs
  to re-derive it.
- The asymmetry is **permanent by design**, not pending a decision.
- Its severity is materially reduced by **item 1**. Once that lands, "the model
  echoes a secret" is bounded to the transcript and the screen. Today it also
  means the disk, the FTS index, and every subsequent request.

**A trigger to write down with the acceptance**, so this is a deferral and not a
shrug: revisit if the transcript ever becomes readable by another user or
process, if transcript export is added, or if telemetry ever samples conversation
content. Any of those turns "displayed to its owner" into "disclosed to someone
else".

### If you want the stronger position anyway

Item **1c** — scrubbing assistant turns in history — is the cheapest coherent
version: ~2 lines, it never touches what is *displayed* (so the assistant can
still show you a key format on screen), and it stops an echoed secret from riding
outbound forever. It accepts a real cost: a prior answer that legitimately
contained a key-shaped string comes back to the model redacted, and the model may
then be confused about its own earlier answer. **I do not recommend it**, but it
is the one option here defensible on the evidence, and it is a far smaller step
than a full inbound redactor.

---

## 4. Four `daemon/` fuzz targets never generate an input — **RECOMMEND FIX (cheap)**

### The measurement

Four targets are registered in `scripts/fuzz.sh` and run in CI. At CI's budget
(`FUZZTIME=30s`) **all four generate zero new inputs**:

| Target | At `FUZZTIME=30s`, as the tree stands | What it actually did |
|---|---|---|
| `FuzzRenderToolResult` | **0 execs, 0 new inputs** | `gathering baseline coverage: 0/…`, elapsed 39 s |
| `FuzzVerifyApproval` | **0 execs, 0 new inputs** | same, elapsed 38 s |
| `FuzzToolCallAccumulator` | **0 execs, 0 new inputs** | same, elapsed 38 s |
| `FuzzSplitQualifiedName` | **0 execs, 0 new inputs** | `gathering baseline coverage: 0/160 completed`, elapsed 38 s |

Note the shape of that last figure: **0 of 160 completed in 38 seconds.** Not
slow — *zero*. Nothing finished at all.

For contrast, the other twelve targets in the same gate at the same budget:
`editapply/FuzzParseUnifiedDiff` 1,660,054 execs, `proxy/FuzzStreamRequested`
615,320, `clients/tui/FuzzSanitizeChunkInvariance` 567,455.

**Read every non-zero count in this memo as one sample, at one budget, on one
machine.** (Added 2026-09-05.) Fuzz throughput is time-boxed and scales with
machine load: a later run of this same gate at the same `FUZZTIME=30s` put
`FuzzSanitizeChunkInvariance` at 2,657,305 rather than 567,455 — about 4.7×
apart. **Nothing in this item's argument rests on a throughput figure.** It rests
on the zero, which is reproducible in every run and is a structural fact about
`TestMain` rather than a measurement.

**The targets themselves are fine.** Left running unmodified for 300 s,
`FuzzSplitQualifiedName` reaches **10,649,397 execs and 22 new interesting
inputs**. It is not saturated and it is not slow; it simply never starts inside
30 seconds.

### The cause, and a correction to my own earlier note

`scripts/fuzz.sh`'s comment used to attribute this to the budget going on
"replaying the seed corpus". **That was a guess and it was wrong**, and I wrote
it. The on-disk seed corpora are one file and zero files:

```
FuzzRenderToolResult       1 seed file
FuzzToolCallAccumulator    1 seed file
FuzzVerifyApproval         0
FuzzSplitQualifiedName     0
```

The real cause is **package startup, paid by every fuzz worker process**:

```go
// daemon/helperproc_test.go, in TestMain, before m.Run()
cmd := exec.Command("go", "build", "-o", fakeHelperBinPath, "./testdata/fakehelper")
```

Measured: `go test -run 'XXXNOSUCHTEST' ./daemon` takes **5.68 s with zero tests
run** — that is the fake-helper build, and a `user` time of 27.6 s shows it
compiling in parallel. Go gathers baseline coverage using **worker processes,
each a fresh exec of the test binary**, so every worker pays that before it can
execute a single input. At a 30-second budget the coordinator never gets a
baseline and fuzzing never begins.

So of the three plausible causes — slow seed corpus, expensive setup, genuinely
saturated — it is **expensive setup**, and specifically a `go build` in
`TestMain`.

### What this does and does not mean

**It does not mean the daemon has never been fuzzed.** The cached corpora date
from 2026-08-30 (188, 384, 370 and 177 inputs), so somebody has run these at a
longer budget locally. What it means is narrower and still worth fixing: **CI's
fuzz job has contributed nothing to these four**, and the gate reported `ok` for
work that did not happen until it was changed to say otherwise.

### The fix — and it is validated, not just recommended

Build `fakeHelperBinPath` **lazily, on first use**, behind a `sync.Once`, instead
of unconditionally in `TestMain`. A fuzz worker then never builds it; ordinary
tests pay the same cost they pay now, once. That is a handful of lines in
`daemon/helperproc_test.go` and touches no production code.

**I made that change temporarily and measured it, then reverted it** — the code
is yours and I am not landing changes in your module. Same targets, same
`FUZZTIME=30s`, same machine:

| Target | Before | After deferring the build |
|---|---|---|
| `FuzzRenderToolResult` | 0 execs, 0 new | **853,943 execs, 1 new input** |
| `FuzzVerifyApproval` | 0 execs, 0 new | **696,950 execs, 25 new inputs** |
| `FuzzToolCallAccumulator` | 0 execs, 0 new | **778,931 execs, 10 new inputs** |
| `FuzzSplitQualifiedName` | 0 execs, 0 new | **1,102,402 execs, 4 new inputs** |

**Forty new interesting inputs in two minutes of fuzzing that CI has never been
able to do.** Package startup also drops from 5.68 s to ~3.2 s, which every
`go test ./daemon` pays.

### If you would rather accept it

Harder to defend now that the fix is measured at a handful of lines, but
reasonable with a trigger written down: **revisit when any of these four
functions stops being a small pure function** — `renderToolResult`,
`verifyApproval`, the tool-call accumulator and `splitQualifiedName` are all
parsers of untrusted input, and the moment one grows state or allocation the
absence of real fuzzing starts to matter.

The gate is already honest about this either way: it prints *"seed corpus only,
no new inputs generated at FUZZTIME=30s"* rather than a bare `ok`, which is how
this was found. **It is visible and loud and deliberately not fatal** — failing
CI on somebody else's four targets was not mine to decide, which is why it is
here.

---

## 5. Panic containment stops at the connection goroutine — **RECOMMEND FIX (the daemon half only)**

*Added 2026-09-09 from the boundaries 6/7 reconnaissance. Ranked LAST on
purpose — see the note under "What I need from you".*

`recover()` is per-goroutine. `handleConn`'s backstop (`daemon/server.go:289`)
covers the connection goroutine and nothing else. Five layers that decode bytes
from an untrusted subprocess run outside it:

| Layer | Location | Ours? |
|---|---|---|
| Lane B connect fan-out | `daemon/mcpruntime.go:206` | **yes** |
| SDK stdio decoder | `go-sdk@v1.7.0/mcp/transport.go:461` | no |
| SDK `readIncoming` | `go-sdk@v1.7.0/internal/jsonrpc2/conn.go:259` | no |
| SDK async handler | `go-sdk@v1.7.0/internal/jsonrpc2/conn.go:683` | no |
| os/exec stderr copier | `daemon/mcp/stdioclient.go:166` | ours by wiring |

Measured: `go-sdk@v1.7.0/mcp/` contains **zero** `recover()` calls.

**What I got wrong, since it changes the recommendation.** I first reported that
Lane B tool calls were covered transitively by `handleConn` because they run on
the connection goroutine. That is true of `CallTool`'s *await* and false of the
decoding — the SDK reads the subprocess on goroutines of its own. The
correction runs from "covered" to "not covered", which is the direction that
matters.

**Recommendation: fix the one line that is ours** — a `recover` on
`mcpruntime.go:206`, about five lines with a log. **Do not chase the SDK's four
from here.** They cannot be fixed from outside the dependency, and the choice
between vendoring a patch, filing upstream, and accepting is a dependency-policy
call, not a daemon call.

**What decides the urgency is unknown and I could not establish it read-only:**
whether any input actually panics the SDK. That needs fuzzing a third-party
parser. Until someone runs it, this is a real gap of unmeasured likelihood, and
saying otherwise in either direction would be inventing evidence.

**Register:** R1.23. Related: R1.24 (the LSP header read is unbounded, the one
validation gap found on either boundary) and R1.25 (LSP teardown kills the
process, not the group).

---

## What I need from you

A written answer on each of the **five**. `Fixed`, `accepted with this trigger`,
or `deferred until X` are all complete answers. **A recorded deferral with a
trigger closes a row exactly as well as a fix does.** Recording the decision is
what closes the release gate; the shape of the decision is yours.

**Item 1 outranks the other four, including the new one.** Its blast radius is
the only one here that leaves the machine, it is confirmed by measurement rather
than predicted, and it is unblocked by everything except this reply. Item 5 is
newer and will read as more urgent; it is not. The boundaries item 5 describes
are, on measurement, better defended than the path item 1 describes — 40
adversarial tests and two purpose-built hostile harnesses against a control that
is simply not applied on the second turn.

If it helps, the shortest reply that unblocks everything looks like:

> 1 — fix, I'll take it. 2 — fix, low priority, ticket raised.
> 3 — accepted; revisit on transcript export or telemetry.
> 4 — fix, take the lazy build. 5 — fix ours, accept the SDK's, revisit on upgrade.

Cross-references: `docs/RESIDUAL_RISKS.md` (rows R1.5, R1.6, R1.22-R1.25),
`docs/TUI_PRODUCTION_READINESS_2026-09-04.md` (the release gate this blocks).
