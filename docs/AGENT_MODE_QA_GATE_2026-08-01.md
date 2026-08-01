# Agent-mode launch gate — QA, security and cost — 2026-08-01

> **REMEDIATION STATUS — appended 2026-08-01, after the review.**
>
> Both P1s and the P2 comment defect are FIXED, one isolated commit each, every
> fix neuter-verified. The findings below are left EXACTLY as written — nothing
> is edited to look as though it was never there.
>
> | Finding | Commit | Status |
> |---|---|---|
> | P1-1 mid-turn error discards work | `63d09be` | FIXED — new `IncompleteProviderError`; 2 tests, both fail when neutered in either direction |
> | P1-2 tool call with no id | `c59f8fd` | FIXED — refused at the accumulator; the crasher seed is now a passing regression test |
> | P2-1 stale rate-limit comments | `519aa5e` | CORRECTED — comments only; the limits are deliberately unchanged, and the comment now says why |
> | P2-2 advertised-tool cap vs. measured degradation | — | OPEN, needs real-model spend to quantify |
> | P2-3 RSS over 50 turns | — | OPEN, needs a longer run with a Lane B server |
>
> **`make check` is green again**, including the fuzz seed that was red on
> `dfbc647` by design.
>
> **THE VERDICT IS NOT RETRACTED.** It was correct when written. Whether the
> gate now passes is the founder's call — and note that two of the seven
> dimensions (Resources, and the P2-2 half of model behaviour) are still
> *unestablished* rather than passed, which no amount of remediation changes.

Gate on merging `feat/mcp-agent-loop` (20 commits) to `main`.

**Depth:** local build/test plus `proxy/testharness` (fake Supabase + fake
OpenRouter). No production contact, no spend.
**Evidence discipline:** `CONFIRMED` = reproduced by a script that was run.
`PLAUSIBLE` = reasoned from source, not reproduced. `NOT RUN` = attempted or
scoped, and honestly not done. Nothing is asserted without one of those.
**Repro bundle:** session scratchpad `repro/zz_qa_repro_test.go`; drop it into
`daemon/` and `go test ./daemon/ -run TestREPRO -v`.

---

## Verdict: ❌ FAIL

Two findings breach thresholds that were **declared before any measurement ran**.

| Dimension | Threshold | Result | |
|---|---|---|---|
| **Cost** | median turn ≤ 3× a single-turn prompt in tokens | context growth is **linear** (1.9× over 8 steps); request count is the driver | **PASS** |
| **Quota** | peak reservation bounded and documented; a mid-turn `quota_exceeded` **preserves prior work** | bounded (8×, now documented) — but a mid-turn failure **destroys every completed iteration** | **FAIL** |
| **Rate limit** | ≥ 3 consecutive worst-case turns without a 429 | 7 turns at a 3 s cadence, then throttled; degrades to a ~1 s retry, not a failure | **PASS** |
| **Resources** | fds, children, goroutines plateau over 50 turns | no fd or process leak; RSS +8 MB not shown to plateau | **PARTIAL** |
| **Security** | every new trust boundary fuzzed, 0 invariant violations at 60 s | 4 targets written; **1 violated in 4.5 s** | **FAIL** |
| **Egress** | tool bytes bounded and reported | bounded at a single choke point, reported per call, fuzz-clean over 2.2 M execs | **PASS** |
| **Correctness** | 0 P0, 0 unmitigated P1 | 0 P0, **2 P1** | **FAIL** |

**Decision rule as written:** *"any P0, or a Cost/Quota/Security miss → FAIL."*
Quota and Security both missed. **Agent mode does not merge as user-facing in
this state.**

**What that does not mean.** Agent mode is off by default and stays off; nothing
here is a live-user hazard today. Both P1s are small, well-understood fixes with
obvious tests. This is a gate saying "not yet", not "not this way."

### Quality score: 7.0 / 10

| | |
|---|---|
| Design and safety architecture | 9 — two-lane model, digest binding, 24 neuter-verified properties, consent path is single-entry |
| Test discipline | 8 — but the discipline was applied to the code and not to its own doctrine (see S-1) |
| Cost awareness | 4 — a registered risk went unworked to the end of the programme |
| Operational readiness | 6 — no agent-mode instrument existed until this pass built one |
| Honesty of documentation | 9 — the docs already say the uncomfortable parts out loud |

---

## P1 findings

### P1-1 — A provider error mid-turn discards every completed iteration `CONFIRMED`

`daemon/agentloop.go:141` returns `agentResult{}, err` on a stream failure,
dropping `full.String()`. Compare `budgetStop`, which returns `FinalText` and
calls the outcome `Incomplete` **precisely because** "the work done so far is
real and the user keeps it".

Repro output:

```
client already saw 94 bytes of assistant text
err = the model provider is unreachable or failing right now — this is usually temporary
res.FinalText = ""
CONFIRMED: 94 bytes of assistant text (including an edit block) were streamed
           to the user and then discarded by the error path
CONFIRMED: the edit block produced at iteration 2 is lost — the user saw a
           proposed change and will never be offered it
```

**Why this is P1, not P2.** The user watched the text arrive. The daemon then
hands back nothing, so `parseAndLogEditBlocks` sees an empty string, no
`EditProposals` reach the client, and `persistTurn` never runs — the turn is
absent from conversation memory too. And the trigger is ordinary: §4 of the cost
doc shows a normal working cadence produces 429s, and once the retry budget is
spent this is the shape.

**Fix:** return `FinalText` with an `IncompleteInfo`, as budget stops already do.
A stream error after work has been done is an incomplete turn, not a void one.

### P1-2 — `finish()` accepts a tool call with no id, and the id is what consent binds to `CONFIRMED`

Found by `FuzzToolCallAccumulator` in **4.5 seconds** on its first run
(seed `daemon/testdata/fuzz/FuzzToolCallAccumulator/bd61f0f73fd1a68a`).

`toolCallAccumulator.finish()` documents itself: *"It REFUSES a structurally
incomplete call rather than returning a partial one."* It checks `sawName` and
validates the arguments as JSON. **It never checks the id.**

```
CONFIRMED: a call with no id was assembled and is dispatchable:
           {ID: Type:function Function:{Name:builtin__list_directory Arguments:{"path":"."}}}
CONSEQUENCE: the approval prompt carries call_id=""
CONSEQUENCE: an approval collected for "list_directory" verifies as approval for
             "delete_everything" — same arguments, no id to tell them apart
```

The chain: an empty `call.ID` becomes `ToolApprovalRequest.CallID = ""`, which
makes `verifyApproval`'s `resp.CallID != req.CallID` check **vacuous**. Consent
is then bound by the argument digest alone — and the digest covers the
arguments, **not the tool name**. Two id-less calls with identical arguments are
indistinguishable to the verifier.

**Honest severity bounding.** Triggering this needs a provider that emits
tool-call fragments without ids, which OpenAI-compatible providers do not
normally do. It is a **defence-in-depth failure on the consent path**, not a
live bypass. It is P1 because the invariant is documented, load-bearing, and
does not hold — and because "the provider always sends an id" is exactly the
kind of assumption this codebase fuzzes elsewhere rather than trusting.

**Fix:** refuse a call with an empty id in `finish()`, same shape as the
`sawName` check. Two lines, and the seed corpus already tests it.

---

## P2 findings

### P2-1 — The per-key rate limiter's own comments describe a world agent mode ended `CONFIRMED`

`proxy/ratelimit.go` justifies `keyRatePerSecond = 2.0` / `keyBurst = 20.0` with
*"a developer prompting from an IDE is well under 1 req/s"*, and
`keyTokenBurst = 120000` with *"a grounded turn is a few thousand tokens"*. One
agent turn is up to 8 requests. Measured: 7 turns at a 3 s cadence before
throttling; 63 rate-limit events over 50 unpaced turns.

Behaviour degrades **gracefully** — a ~1 s retry, not a failure — so this is P2
and not P1 on its own. It becomes P1-1's trigger, which is where the harm is.

**Fix is a decision, not a patch:** either raise the burst knowing what an agent
turn now costs, or leave it and correct the comments so the next reader is not
misled by a premise the product has outgrown.

### P2-2 — `defaultMaxAdvertisedTools = 12` sits past the point degradation was measured `PLAUSIBLE`

Phase 0 measured tool-selection accuracy at **100% with 1 tool** and **85.7%
with 5**. The default advertised cap is **12**. The code comment cites that
measurement honestly and then picks a number more than twice the width at which
degradation was already observed.

Not reproduced — quantifying it needs real-model runs at 4/8/12 tools, which
this pass's zero-spend scope excludes. Listed as PLAUSIBLE, with the measurement
named rather than hand-waved.

### P2-3 — RSS grew 8 MB over 50 turns and was not shown to plateau `CONFIRMED` (as an absence)

fds 9→10, children 0, threads 8→14, RSS 13.2 MB → 21.2 MB. No fd or process
leak. The RSS number is not a finding on its own — 50 turns is too short to tell
warm-up from growth — and it is recorded rather than passed because the run also
had **no Lane B server configured**, which is the case most likely to leak.

---

## Security findings

### S-1 — `fuzz.sh` stated a rule the daemon had been exempt from `CONFIRMED`

`scripts/fuzz.sh` says: *"Every target reads bytes this codebase did not author…
Nothing else is fuzzed, because nothing else is a trust boundary."* When it was
written that was true. Agent mode then added four such readers and **the daemon
had zero fuzz targets**.

Written and now wired into the gate:

| Target | Reads | Result at 60 s |
|---|---|---|
| `FuzzRenderToolResult` | third-party subprocess output — the egress boundary | **PASS**, 2.2 M execs |
| `FuzzVerifyApproval` | a client's answer to a security question | **PASS**, 977 k execs |
| `FuzzToolCallAccumulator` | provider SSE fragments | **FAIL in 4.5 s** → P1-2 |
| `FuzzSplitQualifiedName` | a model-supplied dispatch key | **PASS**, 1.5 M execs |

The class-level lesson is the one worth keeping: the rule existed, was correct,
and was not re-applied when the surface it governs grew.

### S-2 — The consent path is single-entry `CONFIRMED`

Enumerated every caller. `registry.Call` has **exactly one** call site
(`agentloop.go:320`), immediately after `resolveExecutable` in the same function.
Lane A handlers are invoked only from inside `registry.Call`, after its own
`PolicyDeny` re-check. There is no second caller that could skip the belt.

### S-3 — The environment discipline still bites `CONFIRMED`

Re-verified after the Phase 5–7 refactors: neutering `ServerEnv` to
`os.Environ()` fails `daemon/mcp` immediately, with the child observing
variables that were never allow-listed. The ~130-variable leak is still fenced.

### S-4 — Egress fuzzing found nothing `CONFIRMED`

2.2 M executions against `renderToolResult` with planted secrets, invalid UTF-8,
multi-byte runes across every cut point, and caps from 1 byte upward. The cap
holds, output stays valid UTF-8, and no redaction placeholder was ever cut in
half — the truncate-before-scrub ordering does what its comment claims.

---

## Blocked, not skipped

- **Real-model behaviour under the production system prompt** (plan item D1) and
  **menu-width quantification at 4/8/12 tools** (P2-2). Both need real spend,
  which this pass's agreed scope excludes.
- **`search_code` in a live loop** (D2) — needs the ONNX embedder and a real
  index; the loop gate already recorded this as unmeasured.
- **A Lane B server in the soak** — no real MCP server was exercised anywhere in
  this programme. `/bin/true` covers the fails-to-start path only, so a
  misbehaving server (hangs on `initialize`, floods stdout, exits mid-call) is
  **untested** (plan item H7, `NOT RUN`).
- **Cross-process audit-log interleaving** (H8) and **`grantReadBudget`
  accumulation arithmetic** (H6) — scoped, not reached.
- **A hostile client pipelining approvals** (H5) — the daemon reads one answer
  per ask off a shared decoder; the idempotence guards are client-side. `NOT RUN`.
- **Real screen-reader passes** on the VS Code consent UI — structural
  assertions only, same limit the accessibility suite already states.

---

## What this pass added to the repo

Committed regardless of verdict, because an unreproducible measurement is an
opinion:

| Artefact | What it makes answerable |
|---|---|
| `proxy/testharness -tool-calls / -reserve-fail-after` | drives a real agent turn through the real proxy with zero spend, and kills one mid-way |
| `daemon/testdata/agentbench` | the only scriptable agent-mode client; no shipped client can be one, deliberately |
| `scripts/agent-cost-bench.sh` | the cost, quota, latency and resource tables in one command |
| `daemon/fuzz_test.go` + 4 entries in `scripts/fuzz.sh` | the daemon's trust boundaries, permanently gated |
| `daemon/testdata/fuzz/FuzzToolCallAccumulator/bd61f0f73fd1a68a` | P1-2 as a permanent regression seed |

**`make check` is RED on this commit, on purpose.** The crasher seed is
committed and fails, which is what `fuzz.sh` prescribes — *"COMMIT IT, then fix
the bug."* It goes green with P1-2's fix.

---
---

# MCP hardening pass — appended 2026-08-01 (later the same day)

> **Nothing above this line has been edited.** The findings, the FAIL verdict
> and the "Blocked, not skipped" list stand as written. This section records
> what happened when the blocked items stopped being blocked.

The gate closed with a "Blocked, not skipped" list whose first entry was the
honest one:

> **A Lane B server in the soak** — no real MCP server was exercised anywhere in
> this programme. `/bin/true` covers the fails-to-start path only, so a
> misbehaving server (hangs on `initialize`, floods stdout, exits mid-call) is
> **untested** (plan item H7, `NOT RUN`).

### First, a correction to that sentence

It was too strong. `daemon/mcp/testdata/echoserver/main.go` is a genuine SDK
stdio server driven over the real transport by nine tests in
`stdioclient_test.go`, including the credential-isolation proof. What was
missing was **misbehaviour**, **the loop end-to-end**, and **interop with a
server we did not write** — not "a real MCP server".

---

## Method

`daemon/mcp/testdata/badserver` is a real MCP server with ten argv-selected
misbehaviour modes. Eight are built on the SDK, because **a server does not have
to violate the spec to be dangerous** — flooding `tools/list`, lying in
`readOnlyHint`, exiting mid-call, leaving an orphan, putting escapes in a tool
name are all things a published server could do today, most of them by accident.
One (`hang-initialize`) is hand-rolled stdio, because the SDK will not let a
server misbehave at the handshake.

Seven hypotheses (M1–M7) were **registered before anything ran**, same discipline
as this gate's own. Two tests were written to assert properties expected to hold,
so the run could distinguish "we fixed it" from "it was already right".

---

## Results

Seven confirmed, two held. Every fix is one isolated commit with a test that
**fails when the fix is neutered** — demonstrated by neutering it, not asserted.

| # | Finding | Severity | Evidence | Fix |
|---|---|---|---|---|
| **M1a** | `tools/list` unbounded, **and read before any consent step exists** | **High** | CONFIRMED | `3bb7443` |
| **M1b** | `tools/call` result read whole, then capped | Medium | CONFIRMED | `3bb7443` |
| **M2** | *n* hung servers cost *n* × 21 s, serially, outside the turn budget | Medium | CONFIRMED | `2071793` |
| **M3** | mid-call death reported as a failed tool, not an absent server | Medium | CONFIRMED | `398a326` |
| **M4** | a grandchild **survived teardown**, once per turn | Medium | CONFIRMED | `80a3912` |
| **M5** | a lying `readOnlyHint` | — | **HELD** | n/a |
| **M6** | escapes in a **tool name** reach the approval prompt | **High** | CONFIRMED | `c47da40` |
| **M6b** | escapes in tool **output** reach the model | Low | CONFIRMED | `4ad5aac` |
| **M7** | forged approval JSON in a tool result | — | **HELD** | n/a |
| **M8** | a launcher adds its own environment on top of ours | Informational | CONFIRMED (interop) | comment corrected |

### M1a is the one to read first

Not because of the amplification, though that is the striking number:

| On the wire | Peak heap | Ratio |
|---|---|---|
| 4 MiB | 51 MB | 12.0× |
| 8 MiB | 101 MB | 12.0× |
| 32 MiB | 403 MB | 12.0× |
| 64 MiB | 806 MB | 12.0× |

Linear, no knee, so a 200 MiB response is ~2.4 GB and the OOM killer. After the
fix the same 8 MiB flood peaks at **7.8 MB**.

The reason it matters more than the ratio is **which message**. `tools/list` is
read at registry-build time — before the loop exists, before any tool is
proposed, therefore before any approval prompt could be shown. The consent
design, which is the thing this product says protects the user from an
unconfined subprocess, **does not cover it at all**. A user who configured a
server and typed one prompt had already taken whatever it sent, having
authorised nothing.

`max_tool_result_bytes` looked like the bound and was not: it caps what goes to
the *model*, applied to a string already resident. It bounds egress. Nothing
bounded memory.

### M6 has two surfaces, and the daemon log was the one nobody predicted

A tool RESULT goes only to the model — `protocol.ToolActivity` carries a byte
count, never bytes. A tool NAME goes onto the approval prompt, rendered in the
user's terminal, **before they consent**, on the same panel that carries the
`NOT SANDBOXED` line. `\x1b[1A` and `\x1b[2K` move the cursor up and erase the
line: enough to redraw the security notice above.

The repro also showed the escapes eating their way up the *daemon log* — the
refusal line printed them raw. Three surfaces, two remedies, because a name is a
**dispatch key** (refused; rewriting it would mean approving one string and
dispatching another) while a log line is **display** (escaped, and stays
readable).

---

## The three hypotheses this gate registered and never ran

| # | Registered as | Outcome |
|---|---|---|
| **H5** | a hostile client pipelining approvals | **REAL, and not the predicted shape** — fixed `e9b6440` |
| **H6** | `grantReadBudget` accumulation arithmetic | **the comment was wrong about its own bound** — fixed `e9b6440` |
| **H8** | cross-process audit-log interleaving | **HOLDS**, now measured |

**H5** predicted a consent bypass. It is not one — it fails safe. What a
duplicate answer actually does is **desynchronise the rest of the turn**: the
leftover reply is read as the answer to the *next* ask, fails the call-id check,
and denies a call the user approved; the user's real answer becomes the new
leftover and every remaining call dies the same way. One double-fired keypress
silently converts every subsequent *yes* into a *no*. Confirmed over a real
socket.

**H6** found the comment on `maxApprovalResponseBytes` claiming the aggregate was
"bounded by the turn's iteration ceiling". It was not: grants are per **ask**,
asks are per **tool call**, and calls per iteration is whatever the provider's
stream contains — `toolCallAccumulator` caps neither. The total was a function of
provider output rather than of any configured number, which is the difference
between a bound and a hope. There is now a real one: the approval channel may
extend a connection's read budget by at most **25% of it**, however many times it
is asked.

**H8** holds. Four real processes (not goroutines — those share the mutex and
prove nothing) appending 1,600 records through repeated rotations: every
surviving line complete, parseable and intact, no interleaving within a line.

---

## P2-2 and P2-3, the two the gate left open

**P2-2 — CLOSED by making the default defensible; the curve stays unmeasured.**
The Phase 0 eval measured 100% @ 1 tool → **85.7% @ 5**. The default was **12**:
more than twice the widest menu anyone had measured, and nothing is known about
12 because no run has ever been done there. It is now **5** — the widest menu
with evidence behind it.

This is not a claim that 5 is optimal. The 4/8/12 curve remains **unmeasured**
and needs real spend. The claim is the narrower one that can be defended: past
the evidence we would be guessing, and the honest place to guess is a config
file the user edits.

Lowering it makes truncation reachable in ordinary use, so it now *is* visible in
ordinary use: `Registry.Dropped()` had always recorded what the cap left out and
nothing in a live turn read it — the report existed only in `mcp list`. A dropped
tool and a tool the server never offered look identical from outside, and only
one of them is the user's configuration quietly not doing what they wrote.

**P2-3 — CLOSED, and the old number is explained.** The gate recorded "RSS 13.2
→ 21.2 MB over 50 turns, not shown to plateau" and could not settle it because
no MCP server was configured — the registry is built and closed per turn, so a
leak is only visible when there *is* a subprocess.

50 turns with a Lane B server, sampled every second:

| | first half | second half | peak | verdict |
|---|---|---|---|---|
| fds | 14 | 14 | 15 | **PLATEAU** |
| children | 1 | 1 | 1 | **PLATEAU** |
| rss_kb | 20,746 | 21,252 | 21,516 | **PLATEAU** (+2.4%) |
| threads | 13 | 16 | 16 | rose 8→16, flat from ~40 s |

Stray MCP server processes after the run: **0**.

**The 8 MB was never a slope.** It lands inside the first second (13,120 →
20,024 KB) and the remaining 50 turns add 1.5 MB. That is arena warm-up. Two
endpoint samples could not tell the difference; a series can, which is why the
bench now takes one.

fds oscillating 9↔15 and children 0↔1 across the run is the per-turn server
lifecycle, visible for the first time.

---

## Interop — the limit the hermetic fixtures could not escape

`echoserver` and `badserver` are both built on the same Go SDK this client uses,
so an SDK bug cancels itself out. Against the MCP project's own reference server
(`npx @modelcontextprotocol/server-everything`): 14 tools, every one
`Confined:false` / `lane=third_party` / schema present / name passing the
validator that gates advertising, and `echo` round-tripped.

Two things it established that no hermetic fixture could:

**A cold handshake took 11.6 s.** That is direct evidence for leaving
`DefaultConnectTimeout` at 20 s while fixing the *n*-servers problem with
parallelism instead of a shorter fuse — the honest failure case here is slowness,
not malice.

**M8:** we handed the child 2 variables and it reported **25**, the rest npx's
own. Not a hole — none is one of this product's credentials — but `Connect`'s
comment claiming the child sees "only HOME, PATH and the one allow-listed
variable" was true only of a directly-executed binary, and most real configs name
a launcher. Comment corrected.

---

## Still blocked, still stated

- **The 4/8/12 menu-width curve**, **the production system prompt delta** (D1)
  and **`search_code` in a live loop** (D2) all need real-model spend.
- **CI cadence for the agent-loop eval** (D3) is a founder decision, not a test.
- **Real screen-reader passes** on the VS Code consent UI.
- **Connect time is still outside the turn budget.** Parallelism made it a
  constant instead of a function of server count; it did not make it budgeted.

## The fuzz gate found two more, and one of them was mine

`scripts/fuzz.sh` at 60 s per target, run against the hardened code.

**`FuzzRenderToolResult` failed at 9.6 s.** The control-character strip from
M6b's fix used `strings.Map`, which decodes as UTF-8 and **re-encodes** whatever
the mapping returns — so every invalid byte became U+FFFD and grew from one byte
to three. 248 bytes of cap in, **761 bytes out**. A tool returning binary-ish
output would have had its egress silently **tripled**, after the byte cap had
already been applied.

That is precisely the inflation M6b's commit message says it chose dropping over
escaping to avoid. The fix shipped the thing it was written to prevent, in a
different form, and nothing but the fuzzer noticed — not the two unit tests
written for it, which used clean UTF-8.

**`FuzzExtractUsageAndProvider` (proxy)**: a `\r` in an upstream SSE frame's
provider field reached a log line. Rated **Low** and stated as such: the slog
`TextHandler` this proxy uses already quotes values needing quoting, so nothing
was forgeable today. Closed at the extraction boundary anyway, where it does not
depend on which handler is configured.

Both crashers committed as seeds before either fix, per `fuzz.sh`'s own rule.
The daemon seed was verified to fail against the old implementation and pass
against the new one.

The lesson is the same one this gate recorded about the daemon having no fuzz
targets at all: **a rule that is correct is not a rule that is applied.** M6b
had two hand-written tests and a documented rationale, and the case it got wrong
was the one no human would think to type.

---

## What this pass added

| Artefact | What it makes answerable |
|---|---|
| `daemon/mcp/testdata/badserver` | ten ways a Lane B server can misbehave, on demand |
| `daemon/mcp/badserver_test.go` | the adapter's behaviour against a hostile peer |
| `daemon/mcp_misbehaviour_test.go` | the same through config → registry → loop → approval prompt |
| `daemon/mcp/interop_test.go` | a server nobody here wrote (`MCP_INTEROP=1`, not in CI) |
| `daemon/jsonlsink_multiprocess_test.go` | the audit log under four real processes |
| `scripts/agent-cost-bench.sh` `MCP_SERVER=` / `SAMPLE_EVERY=` | a Lane B soak, sampled as a series |
| `docs/MCP_LANE_B_THREAT_MODEL.md` | the walked version of what `SECURITY_MODEL.md` asserts |
| 2 new fuzz seeds | the two crashers above, permanently |

## Where the dimensions stand now

The verdict above is not retracted and is not re-scored here — that is the
founder's call. What has changed is which dimensions are *established*:

| Dimension | At the gate | Now |
|---|---|---|
| Correctness | FAIL (2 P1) | both fixed; 8 more found and fixed since |
| Resources | **unestablished** | **PLATEAU**, measured with a Lane B server over 50 turns |
| Model behaviour (P2-2 half) | **unestablished** | default now matches the evidence; the 4/8/12 curve **still unmeasured** |
| Security | passed at 4 targets | 2 further crashers found at 60 s, both fixed |

**`make check` green.** Branch is local; merge remains the founder's call.
