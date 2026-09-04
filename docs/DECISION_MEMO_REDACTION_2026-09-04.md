# Decision memo — R1.5 and R1.6, secret redaction across the daemon/client seam

<!-- coderefs: enforced -->

**Date:** 2026-09-04
**From:** the TUI production-hardening pass (`audit/adversarial-pass`)
**To:** whoever owns `daemon/`
**Status:** requires a decision. Both rows are open, neither is pinned by a test,
and the TUI pass cannot close either from its own side.

---

## Summary, and the part that was not known when these rows were written

Three things need deciding. Two are the register rows you were expecting. The
third was found while answering the question that gates this memo, it is a
**defect in a control that already exists** rather than a request for a new one,
and it is the one I would fix first.

| | What | Recommendation |
|---|---|---|
| **A** | The daemon redacts a secret from the prompt, then **writes it to disk raw and sends it to the provider verbatim one turn later**. | **FIX.** Needs no new detector and no new judgment. |
| **B** | R1.5 — `/mcp-server` puts an MCP server's stderr on screen unredacted. | **FIX.** Small, exact, in the daemon. |
| **C** | R1.6 — inbound model text is not redacted. | **REJECT the fix; accept the row in writing.** The asymmetry is permanent, and the reason is below. |

---

## The blocking question, answered first: shapes, not known values

**`redactionsMsg` carries SHAPES.**

`daemon/scrub.go` is ten compiled regexes — `openai_key`, `stripe_secret_key`,
`aws_access_key`, `github_token`, `slack_token`, `google_key`,
`supabase_secret`, `supabase_publishable`, `mochiii_key`, `private_key_block` —
matched against text. `redactionKinds` reduces each match to a fixed label, and
that label list is what reaches the client as `redactionsMsg` and is drawn in
the header. The daemon does not compare against any value it provisioned,
because on this path **it provisioned nothing**: the text is the user's own
typing and the model's own answer.

**What that settles.** Applying "the same set" inbound is not structural and not
cheap. Inbound, the set *is* the ten regexes, and running them over model output
is exactly the pattern-matching the brief rules out: a grep for key-shaped
strings will miss the key that is not key-shaped, and will hit the key-shaped
string that is not a key. **R1.6 stays outbound-only. The asymmetry is
permanent.** Section C says what follows from that.

**One place the answer is different, and it is why B is fixable.** MCP server
launch is not this path. `mcp.ServerEnv(allow)` returns literal `NAME=value`
strings — the daemon holds the exact bytes it handed that subprocess. There, and
only there, exact-match stripping is available.

---

## A — The outbound scrub is bypassed by one turn of conversation

**This was not in either row, and it is not a design question.**

### What it does, verified by execution

```
what goes on the WIRE:  "deploy with [REDACTED:openai_key]"   (1 redaction)
what is on DISK (user): "deploy with sk-BBBBBBBB…"            <-- RAW
what is on DISK (asst): "sure, using sk-BBBBBBBB…"            <-- RAW
turns_fts row:          "deploy with sk-BBBBBBBB…"            <-- RAW, and indexed
```

and separately, through the history path:

```
history message 0 (user):      "here is my key: sk-AAAA…"     <-- LEAK
history message 1 (assistant): "I will use sk-AAAA…"          <-- LEAK
prompt path, same string:      "here is my key: [REDACTED:openai_key]"
```

### Why it happens

Three call sites, each individually reasonable:

1. `daemon/server.go:547` computes `cleanPrompt, redactions := scrub(promptReq.Prompt, …)`
   and sends `cleanPrompt`. Correct.
2. `daemon/server.go:687` calls `s.persistTurn(promptReq.Prompt, full.String(), …)`
   — **`promptReq.Prompt`, not `cleanPrompt`.** The raw string is written to
   `memory.db`, and `daemon/search.go:42`'s INSERT trigger copies it into the
   `turns_fts` full-text index.
3. `daemon/history.go:136` `prepareHistory` validates, annotates, caps by count
   and caps by bytes. **It does not scrub.** Its output goes straight into
   `buildChatMessages(…, historyOutcome.Messages, …)` at `daemon/server.go:595` and out
   to the provider.

So the round trip is: **redacted on turn 1 → stored raw → re-hydrated into the
client's transcript at the next handshake → sent raw on turn 2, and on every
turn after that.** `loadPersistedHistory` re-runs `prepareHistory` on the way
back, which is where it would have been caught if `prepareHistory` scrubbed.

### Why this is a defect and not a decision

The daemon has already decided that `sk-BBBB…` does not go to the provider. It
made that decision, logged it, and told the user about it in the header. Sending
the identical bytes sixty seconds later is not a different policy — it is the
same policy, not applied. Nothing here asks anyone to judge what a secret looks
like; the judgment was made by the existing shape list and is simply not carried
through.

### Blast radius, stated plainly

- The secret reaches the model API anyway, one turn late. The scrub's entire
  purpose is that boundary crossing, and it is the one crossing you cannot undo.
- It is on disk in plaintext in `memory.db`, and in an FTS index beside it. The
  file has mode hardening; plaintext-at-rest is still a different claim from what
  the redaction notice implies to a user who has just been told their key was
  removed.
- It is **worse than never having scrubbed**, in one specific way: the user was
  shown a redaction notice and reasonably concluded the key did not leave.

### The fix, and its cost

| Part | Change | Cost | New judgment required? |
|---|---|---|---|
| A1 — persistence | `persistTurn(cleanPrompt, …)` at `daemon/server.go:687` | one identifier | **None.** Applies a decision already taken about that exact string. |
| A2 — history, user turns | scrub `Content` for `t.Role == "user"` in `prepareHistory`, before the byte budget | ~10 lines | **None.** Same argument. Before the budget, so the budget accounts for what is actually sent. |
| A3 — history, assistant turns | scrub those too | ~2 lines | **Yes** — this is C, below. Strictly stronger, and it is your call, not mine. |

Recommend **A1 + A2 now**, A3 only if you also take C.

**One consequence to weigh, so it is not a surprise.** Scrubbing history changes
what the model sees in later turns. If a user legitimately discusses a
key-shaped identifier, they already lost it on turn 1 — the prompt path redacted
it — so A1/A2 make later turns *consistent* with the first rather than newly
lossy. `scrub` is precision-first by design and its false-positive rate on
prefixed shapes is recorded as none measured.

---

## B — R1.5, the `/mcp-server` stderr path

### The full chain, end to end

1. `clients/tui/slash.go:411` runs `codeterminal-daemon mcp list` with
   `CombinedOutput()` and puts the result in a system turn on screen.
2. `mcp list` **starts the configured servers** — that is how it enumerates
   tools. `daemon/mcp_cmd.go` builds a registry, `daemon/mcpruntime.go:208`
   connects each server with `serverStderr(name, logger)`.
3. `serverStderr` returns a `prefixWriter` that escapes and bounds each line and
   writes it to `logger`, which in the subcommand path is
   `log.New(os.Stderr, …)` (`daemon/main.go:30`).
4. `CombinedOutput()` captures exactly that stream.

A third-party MCP server that prints a credential at startup therefore reaches
the user's screen. This is the only path in the client that puts daemon stderr
in front of a person.

### Where the fix belongs, and why not in the TUI

**In the daemon, at `mcp.Connect`.** That function already computes
`cmd.Env = ServerEnv(cfg.EnvAllow)` — the literal `NAME=value` pairs. Wrapping
`cfg.Stderr` there means the redactor is built from **exactly the bytes this
daemon handed this subprocess**, and every launch goes through `Connect`, so a
new call site cannot skip it.

**A TUI-side redactor was rejected, and the reason is not "layering".** The
client sees an opaque blob of text. It does not know what was provisioned, so it
could only match shapes — and this is a *coding assistant*, whose own output
routinely contains key-format documentation, example config, and test fixtures
carrying deliberately fake keys. A shape matcher in the client mangles those,
and still misses the one credential that does not look like a credential. Wrong
in both directions, in the one place a user reads diagnostics.

### What the fix catches, and what it does not

**Catches:** every value the daemon passed under `cfg.EnvAllow`. That is where an
operator puts a server's credential, by construction — it is the list they had to
write to grant it.

**Does not catch, and say so in the row rather than implying otherwise:**

- A credential the server reads from **its own** config file or keychain. The
  daemon never held those bytes.
- Anything a **launcher** adds. `Connect`'s own header records the measurement:
  the daemon handed a reference Node server 2 variables and the child reported
  25, the other 23 being npx's (`NODE`, `PWD`, `INIT_CWD`, eighteen
  `npm_config_*`).

That partiality is **describable**, which is the whole difference from a shape
matcher's. "Values this daemon provisioned" is a set you can enumerate; "things
that look like keys" is not.

### Implementation notes and cost

- Strip values for **allow-listed names only**, not `BaselineEnvNames`. Redacting
  `PATH` would turn `cannot find node in /usr/bin:/bin` into
  `cannot find node in [REDACTED:PATH]` and destroy the diagnostic this path
  exists for.
- Skip empty and very short values — a one-character value would replace every
  occurrence of that character. A minimum length of 8 is a judgment; state it in
  the code.
- Replace with `[REDACTED:<NAME>]`, matching `scrub`'s existing placeholder shape
  so a user sees one vocabulary.
- **~40 lines plus tests.** No new dependency, no new concept, no configuration.

**The test that makes it real:** launch `testdata/echoserver` with a known
allow-listed value, have it print that value to stderr, and assert the value does
not appear in what `Connect`'s stderr writer emitted while the variable name
does. It fails if the wrapper is removed.

---

## C — R1.6, inbound model text

### Recommendation: reject the fix, accept the row in writing, and record why

Given the P5.1 answer, redacting inbound text can only be the ten regexes run
over prose. That is rejected on three independent grounds, any one of which would
be enough:

1. **The brief rules it out.** Redaction must be structural — a property of how
   the value is carried, the way `ModelError`'s unexported field is (R1.8). A
   secret arriving as a substring of a sentence has no structure to attach to.
2. **This product already measured the cost of the looser version and refused
   it.** Decision D5 rejected entropy/keyword redaction on measured data: 33% of
   chunks touched, zero precision. Inbound prose is a *worse* population for that
   than retrieved chunks, not a better one.
3. **It corrupts the product's own output.** A coding assistant explains key
   formats, writes example configuration, and generates test fixtures containing
   fake credentials. Redacting model output means the assistant cannot show you
   what an API key looks like — while still missing the one that does not look
   like one.

### What "accepted" should say in the register, so the row stops reading as a to-do

Three facts, none of which the current row states:

- The **shapes/known-values question is answered**: shapes. Nobody needs to
  re-derive it.
- The asymmetry is **permanent by design**, not pending a decision.
- Its severity is materially reduced by **A**. Once A lands, "the model echoes a
  secret" is bounded to the transcript and the screen. Today it also means the
  disk, the FTS index, and every subsequent request.

### If you want the stronger position anyway

A3 — scrubbing assistant turns in history — is the cheapest coherent version:
~2 lines, it never touches what is *displayed* (so the assistant can still show
you a key format on screen), and it stops the echo from riding outbound forever.
It accepts a real cost: a prior answer that legitimately contained a key-shaped
string comes back to the model redacted, and the model may then be confused
about its own earlier answer. **I do not recommend it, but it is the one option
here that is defensible on the evidence, and it is a smaller step than a full
inbound redactor.**

---

## What I did not do, and why

I did not change `daemon/`. A is a defect and the fix is small, but it is in
someone else's module, it changes what the model receives on every multi-turn
conversation, and the byte-budget interaction in `prepareHistory` is a real
design point rather than a mechanical edit. This pass was scoped to
`clients/tui`. **Say the word and I will implement A1+A2 with tests.**

I also did not write a test pinning any of this. A probe that asserts today's
behaviour would assert a leak, and the honest form of that — a test that fails
when the gap closes, like `TestKnownGapClientOutlivesADestroyedPTY` — is worth
adding only if you decide **not** to fix A.

## Evidence

Everything above was verified by execution on 2026-09-04, not by reading:

- `prepareHistory` leaking both turn roles, against the prompt path redacting the
  identical string.
- `persistTurn` writing the raw prompt and answer to `memory.db`, and the same
  bytes appearing in `turns_fts`.
- `RejectUnprintablePath` accepting `U+009B`/`U+0080`/`U+009F` while refusing
  `0x1B`/`0x01`/`0x7F` — the C1 finding, unrelated but from the same pass.

The probes were temporary and were removed; they are three short files and I can
reproduce any of them on request.
