# Decision memo — four items in `daemon/`, from the terminal-client hardening pass

<!-- coderefs: enforced -->

**Date:** 2026-09-04
**From:** the `clients/tui` production-hardening pass (branch `audit/adversarial-pass`)
**To:** whoever owns `daemon/`
**What I need:** a decision, in writing, on each of the four items below.

---

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

### The four, and what I am asking

| # | Item | Recommendation | Severity |
|---|---|---|---|
| 1 | The outbound secret scrub is bypassed by one turn | **FIX** | **Highest.** A secret the daemon redacts still reaches the provider. |
| 2 | R1.5 — `/mcp-server` puts MCP-server stderr on screen unredacted | **FIX** | Moderate. Disclosure to the credential's owner. |
| 3 | R1.6 — inbound model text is not redacted | **ACCEPT, in writing** | Low, once (1) is fixed. |
| 4 | Four `daemon/` fuzz targets never generate an input | **FIX** (cheap), or accept | Low, but it means the daemon has never actually been fuzzed. |

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

1. `daemon/server.go:547` computes `cleanPrompt, redactions := scrub(promptReq.Prompt, …)`
   and sends `cleanPrompt`. **Correct.**
2. `daemon/server.go:687` calls `s.persistTurn(promptReq.Prompt, full.String(), …)`
   — **`promptReq.Prompt`, not `cleanPrompt`.** The raw string is written to
   `memory.db`, and `daemon/search.go:42`'s INSERT trigger copies it into the
   `turns_fts` full-text index.
3. `daemon/history.go:136` `prepareHistory` validates, annotates, caps by count
   and caps by bytes. **It does not scrub.** Its output goes straight into
   `buildChatMessages(…, historyOutcome.Messages, …)` at `daemon/server.go:595`
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
| **1a** — persistence | `persistTurn(cleanPrompt, …)` at `daemon/server.go:687` | one identifier | **None.** Applies a decision already taken about that exact string. |
| **1b** — history, user turns | scrub `Content` where `t.Role == "user"` in `prepareHistory`, **before** the byte budget | ~10 lines | **None.** Same argument. Before the budget, so the budget accounts for what is actually sent. |
| **1c** — history, assistant turns | scrub those too | ~2 lines | **Yes** — that is item 3. Strictly stronger, and your call. |

**Recommend 1a + 1b now**; 1c only if you also reject my recommendation on item 3.

**One consequence to weigh, so it is not a surprise.** Scrubbing history changes
what the model sees in later turns. If a user legitimately discusses a key-shaped
identifier, they already lost it on turn 1 — the prompt path redacted it — so
1a/1b make later turns *consistent* with the first rather than newly lossy.
`scrub` is precision-first by design and its false-positive rate on prefixed
shapes is recorded as none measured.

**I did not implement this.** It is your module, it changes what the model
receives on every multi-turn conversation, and the byte-budget interaction in
`prepareHistory` is a real design point rather than a mechanical edit. Say the
word and I will do 1a+1b with tests.

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

| Target | Budget | Execs reported | New inputs | What it actually did |
|---|---|---|---|---|
| `FuzzRenderToolResult` | 30 s | **none** | **0** | `gathering baseline coverage: 0/…`, elapsed 39 s |
| `FuzzVerifyApproval` | 30 s | **none** | **0** | same, elapsed 38 s |
| `FuzzToolCallAccumulator` | 30 s | **none** | **0** | same, elapsed 38 s |
| `FuzzSplitQualifiedName` | 30 s | **none** | **0** | `gathering baseline coverage: 0/160 completed`, elapsed 38 s |

Note the shape of that last figure: **0 of 160 completed in 38 seconds.** Not
slow — *zero*. Nothing finished at all.

For contrast, the other twelve targets in the same gate at the same budget:
`editapply/FuzzParseUnifiedDiff` 1,660,054 execs, `proxy/FuzzStreamRequested`
615,320, `clients/tui/FuzzSanitizeChunkInvariance` 567,455.

### The cause, and a correction to my own earlier note

`scripts/fuzz.sh`'s comment currently attributes this to the budget going on
"replaying the seed corpus". **That is wrong**, and I wrote it. The on-disk seed
corpora are one file and zero files:

```
FuzzRenderToolResult       1 seed file
FuzzToolCallAccumulator    1 seed file
FuzzVerifyApproval         0
FuzzSplitQualifiedName     0
```

The real cause is **package startup**, paid by every fuzz worker process:

```go
// daemon/helperproc_test.go, in TestMain, before m.Run()
cmd := exec.Command("go", "build", "-o", fakeHelperBinPath, "./testdata/fakehelper")
```

Measured: `go test -run 'XXXNOSUCHTEST' .` on `daemon` takes **5.68 s** with
**zero tests run** — that is the fake-helper build, and `user` time of 27.6 s
shows it compiling in parallel. Go's fuzzing engine gathers baseline coverage
using **worker processes, each a fresh exec of the test binary**, so every worker
pays that ~5.7 s before it can execute a single input. At a 30-second budget the
coordinator never gets a baseline, so fuzzing never starts.

So of the three plausible causes — slow seed corpus, expensive setup, genuinely
saturated — it is **expensive setup**, and specifically a `go build` in
`TestMain`.

### What this means

**The daemon has never actually been fuzzed.** The four targets are useful
regression *replay* of their cached corpora and nothing more. That is not a
crisis — they are small, pure functions — but the gate has been reporting
coverage the project does not have.

### The fix

Build `fakeHelperBinPath` **lazily, on first use**, behind a `sync.Once`, instead
of unconditionally in `TestMain`. A fuzz worker then never builds it; ordinary
tests pay the same cost they pay now, once. That is a handful of lines in
`daemon/helperproc_test.go` and touches no production code.

**Then re-measure at `FUZZTIME=30s`** and see whether the targets generate
anything. If they still do not, the answer is genuinely "saturated" and the right
move is to note it and move on.

### If you would rather accept it

Reasonable, with a trigger written down: **revisit when any of these four
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

## What I need from you

A written answer on each of the four. `Fixed`, `accepted with this trigger`, or
`deferred until X` are all complete answers. Recording the decision is what
closes the release gate; the shape of the decision is yours.

If it helps, the shortest reply that unblocks everything looks like:

> 1 — fix, I'll take it. 2 — fix, low priority, ticket raised.
> 3 — accepted; revisit on transcript export or telemetry.
> 4 — accepted; revisit if any of those four functions grows state.

Cross-references: `docs/RESIDUAL_RISKS.md` (rows R1.5, R1.6),
`docs/TUI_PRODUCTION_READINESS_2026-09-04.md` (the release gate this blocks).
