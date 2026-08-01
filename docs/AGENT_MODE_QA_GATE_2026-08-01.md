# Agent-mode launch gate — QA, security and cost — 2026-08-01

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
