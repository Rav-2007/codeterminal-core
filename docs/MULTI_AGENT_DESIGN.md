# Multi-agent orchestration

Status: **Option A built, measured, and now routed per turn.** Shipped switched off.

- **`/team <question>`** in the TUI — **the recommendation.** Runs one turn through
  `researcher → coder`, the shape that won §14's A/B 2–1 at 1.02× the tokens, without
  touching a config file. The shape is a property of the question (§14), so it is chosen
  per question — by the person who asked it, because §15 tried twice to detect it
  automatically and failed both times.
- **`/team:planner,coder <question>`** — names the phases explicitly. This is how the
  Planner and Tester, fully implemented since the pipeline was written, became reachable
  without editing a config file and restarting (§17).
- `mcp.pipeline: ["researcher","coder"]` — the same shape, for every turn.
- `mcp.pipeline: ["planner",…]` or four phases — **measured losers.** Still reachable, and
  what happened when they were measured now reaches **the user** as a
  `pipeline_shape` degradation, not only the daemon log (§17).

Sections 1–7 are the original design and its constraints; 8–11 what was built and the
reviews of it; 12–14 first contact with a live model and the cost measurement; §15 the
two falsified attempts at automatic routing and what shipped instead; §16 the turn-wide
bound that had no reservation.

*Written before any of it existed, and left as written — it is the record of what the
codebase looked like when the design was made, and every claim in it about "there is no
Planner" is now false by construction:*

The gap: [`daemon/agentloop.go`](../daemon/agentloop.go) is one loop, one model, one
context, executing tools strictly serially. The only goroutine anywhere in the agent path
is [`mcpruntime.go:143`](../daemon/mcpruntime.go#L143), which parallelises *MCP server
connection* at startup so N servers cost one timeout instead of N. That is connection
setup, not orchestration. There is no Planner, Researcher, Coder or Tester, and no
role vocabulary in any module.

This document is what it would take, and — more importantly — **what the existing
architecture will and will not allow**, because three constraints found while reading the
code rule out the obvious design.

---

## 1. The three constraints that shape everything

These are not preferences. Each is load-bearing code with tests behind it.

### C1 — The approval channel is strictly one question at a time

[`connApprover`](../daemon/toolapproval.go) is a single `json.Encoder`/`json.Decoder`
pair over **one connection**. `Ask` writes a request and blocks reading its answer.

Two concurrent `Ask` calls would race on that decoder, and `answersAnotherQuestion`
would see the reply echoing the wrong request and deny it. So concurrency here does not
produce a security hole — it fails safe — but it produces **spurious denials**, which is
a worse user experience than being slow.

There is also a latch: `closed` makes every later `Ask` deny immediately after the first
read failure. Concurrent asks would trip it and poison the rest of the turn.

### C2 — "Unlisted means ask" applies to read-only tools too

This is the constraint that kills the obvious design. `policyForTool`
([`mcpconfig.go:293`](../daemon/mcpconfig.go#L293)) defaults every tool the user has not
explicitly configured to `ask`, and its doc comment says the sharing is deliberate:
*"Shared by both lanes so the 'unlisted means ask' rule has exactly one implementation --
two copies is how the lanes drift apart."*

So `read_file`, `list_directory` and `search_code` — all carrying `ReadOnlyHint: true`
in [`mcpbuiltin.go`](../daemon/mcpbuiltin.go) — **still require a human approval per
call** on a default install.

The tempting inference is "read-only tools are free, so fan out researchers in parallel."
That inference is wrong on a default config. Four concurrent researchers reading files
would generate four concurrent approval prompts into a channel that can serve one (C1).

### C3 — Writes serialise per workspace, by design

`LockWorkspaceApply` ([`editapply/applylock.go`](../editapply/applylock.go)) is an
exclusive **cross-process** flock keyed by workspace root, paired with the daemon's
in-process `Server.applyLocks` sync.Map. This is the Gate 6 fix, and it exists because
concurrent applies were measured corrupting each other.

So two agents cannot write to one workspace concurrently. Not "should not" — cannot; one
will block on the other. A Coder and a Tester racing is a lock queue, not parallelism.

---

## 2. What the goal actually is

The stated goal is **"minimizing context dilution by splitting complex coding tasks among
specialists."**

That is a statement about **context**, not about **speed**. It is worth separating them
before choosing a design, because they have completely different costs here:

| Want | Requires concurrency? | Cost under C1–C3 |
|---|---|---|
| Each specialist sees only its own relevant context | **No** | none — free today |
| Specialists run at the same time | Yes | rewrites consent + locking |

**Context isolation is achievable without any concurrency at all.** That observation is
what makes Option A below the recommendation.

---

## 3. Options

### Option A — Serial specialists with isolated contexts *(recommended)*

Sub-turns, run one after another. Each specialist is a fresh `agentTurn` with its own
message list, its own system prompt, and only the context its role needs. The Planner's
output becomes the Researcher's input; the Researcher's findings become the Coder's
context; the Coder's diff becomes the Tester's target.

- **Delivers the stated goal in full.** Context dilution is solved by isolation, and
  isolation is a property of how you build each sub-turn's message list — nothing to do
  with goroutines.
- **Zero conflict with C1, C2, C3.** One question at a time, one writer at a time,
  because there is literally one agent running at a time.
- **Budgets already fit.** `resolveBudget` ceilings (`maxIterations`, turn timeout,
  tool-byte budget) become per-phase rather than per-turn, which is a config change, not
  an architectural one.
- **Costs latency.** Four sequential phases are slower in wall-clock than one turn. This
  is the honest trade: it buys answer quality, not speed.

### Option B — Parallel read-only research, serial everything else

Option A, plus a concurrent fan-out for the Researcher phase only.

To be viable it needs **both** of:

1. **A multiplexed approver.** Replace the single encoder/decoder with a request-id
   keyed map and one reader goroutine dispatching answers to waiting callers. This is a
   real change to consent-path code that currently has a clean "one question, one answer,
   verified bound" property — and that property is the thing the whole security posture
   rests on.
2. **A pre-grant for read-only tools.** Either the user configures
   `read_file`/`search_code`/`list_directory` to `allow`, or the design introduces a
   scoped "research grant" the human approves *once* covering N read calls. The second is
   more honest UX but is a new consent concept, and consent concepts are exactly where
   this codebase has been most careful.

Worth it only if research fan-out is measured to be the bottleneck. It is not obviously
so: retrieval is ~2.4 ms and the model round trip dominates.

### Option C — Fully concurrent peer agents

Rejected. It requires C1 rewritten, C2 relaxed, and C3 worked around with per-agent
worktrees or a write-arbiter. That is a rearchitecture of the consent and locking model —
the two subsystems with the most security history in this repo (Gate 6, FAIL-2, the
approval-binding work) — in exchange for wall-clock time on a path dominated by provider
latency. Bad trade.

---

## 4. Recommended design (Option A) in concrete terms

### Roles and their context

| Role | Sees | Tools | Writes? |
|---|---|---|---|
| **Planner** | user request + repo map/skeleton | none | no |
| **Researcher** | plan step + retrieval results | `search_code`, `read_file`, `list_directory`, LSP | no |
| **Coder** | plan step + researcher findings only | `propose_edit`, `propose_ast_edit` | **proposes** into existing diff review |
| **Tester** | the diff + test command | `sandbox_exec` | no |

The Coder proposing rather than writing is not a new safety property — it is the one
`agentloop.go` already documents: *"Lane A tools cannot write -- the edit tool proposes
into the existing diff review."* The design inherits it rather than inventing anything.

### Orchestration shape

A `phase` state machine above the existing loop, not inside it. `runAgentLoop` stays
what it is — one specialist's loop — and gains a role-scoped system prompt plus a
seeded message list. A new orchestrator calls it once per phase and threads outputs
forward.

This is deliberate: the current loop is well-tested (`agentloop_test.go`,
`agentrepeat_test.go`, `agentloop_eval_test.go`, `toolconsent_test.go`). Wrapping it
preserves all of that. Rewriting it forfeits it.

### Where the work lands

| File | Change |
|---|---|
| new `daemon/orchestrator.go` | phase state machine, output threading |
| new `daemon/roles.go` | per-role system prompts, tool allowlists, context builders |
| [`agentloop.go`](../daemon/agentloop.go) | accept a role config; seed messages; scope budgets per phase |
| [`mcpconfig.go`](../daemon/mcpconfig.go) | per-role tool allowlists, per-phase budgets |
| `protocol` | phase-transition events so the TUI can show which specialist is active |
| [`clients/tui/chat.go`](../clients/tui/chat.go) | render phase transitions |

### Prerequisite worth noting

The Planner is materially weaker without a **repository map**, which is also not built
(no skeletal project outline is ever generated). A Planner given only the user's request
and hybrid-search hits will decompose worse than one given a project skeleton. These two
gaps compound, which is why the earlier ranking put repo mapping first.

---

## 5. How this could go wrong

Stated up front so they are designed against rather than discovered:

- **Approval fatigue multiplies.** Four phases each asking per tool call is more prompts
  than one turn asking per tool call, on a default `ask` config. Without a per-phase or
  per-role grant this is *worse* UX than today, and that would be the most likely reason
  a shipped version gets switched off.
- **Token cost rises, not falls.** Context isolation reduces dilution per call but adds
  handoff overhead — the plan and findings get re-sent. `agentloop.go`'s header already
  names token burn as the reason this was deferred once.
- **Failure attribution gets harder.** When a four-phase run produces a bad edit, "which
  specialist was wrong" needs to be answerable from the audit log. `toolaudit.go` is
  per-call; it would need a phase field.
- **Budget interaction.** Per-phase ceilings that each look reasonable can multiply into
  a turn nobody intended to authorise. The total needs its own ceiling, not just the parts.

---

## 6. Staging

1. **Role-scoped single agent.** One role at a time, selectable, no orchestrator. Proves
   role prompts and tool allowlists work. Fully shippable alone.
2. **Planner → Coder, two phases.** The minimum real handoff. Measure token cost and
   answer quality against single-agent on the existing eval harness.
3. **Add Researcher and Tester.** Only if step 2's measurement justifies it.
4. **Option B fan-out.** Only if research is measured as the bottleneck, and only with a
   multiplexed approver plus a designed grant model.

Each stage is independently revertible, and stage 2 is the decision point: if a two-phase
split does not beat single-agent on the eval set, stages 3–4 should not be built.

> **Settled by §14, and not in the direction this staging assumed.** Stage 2's named shape
> was `planner → coder`; the Planner has no tools, invented paths, and lost. The two-phase
> split that *did* beat the single agent is `researcher → coder`. Stage 3 is therefore
> partly moot — Researcher was promoted rather than added — and stage 4 remains unbuilt and
> unjustified. §15 adds a stage this list did not anticipate: choosing the stage per turn.

---

## 7. Decisions needed before any code

1. **Consent model for sub-agents.** Per-call ask (safe, noisy), per-phase grant (new
   concept), or config-allow read-only tools (shifts the choice to the user)? This is the
   one that blocks everything else.
2. **Is latency acceptable?** Option A is strictly slower per task than today.
3. **Does stage 2 have to beat single-agent on the eval set to proceed?** Recommended
   yes, with the existing harness as the judge.


---

## 8. What was built

Option A, as designed. `mcp.pipeline` names the phases; **unset means
unorchestrated**, so every existing turn behaves exactly as before.

```jsonc
// ~/.mochiii/config.json
"mcp": { "enabled": true, "pipeline": ["planner", "coder"] }
```

| File | What it does |
|---|---|
| [`daemon/roles.go`](../daemon/roles.go) | The four specialists: prompts, tool allowlists, iteration ceilings |
| [`daemon/orchestrator.go`](../daemon/orchestrator.go) | Sequential phase runner and the handoff |
| [`daemon/agentloop.go`](../daemon/agentloop.go) | Takes a `*agentRole`; **nil means unorchestrated** |
| [`daemon/mcpconfig.go`](../daemon/mcpconfig.go) | `mcp.pipeline`, unknown roles skipped with a log line |

Scoping is enforced in **two** places, and that is not belt-and-braces: the
advertised menu is a hint the model can ignore, so `resolveExecutable` refuses a
call to a tool the phase was not given. Naming a tool must not run it.

### Three things the implementation corrected

Each was found by a test rather than by review, and each would have failed silently.

- **`allows()` read an empty allowlist as "allow everything."** The Planner is
  defined with an empty list precisely so it can call nothing, and that reading
  handed it the entire toolbox. Unrestricted is now spelled as a *nil role*;
  a non-nil role allows exactly what it lists. Pinned by
  `TestNilRoleIsUnrestrictedAndEmptyListIsNothing`.
- **Two role tool names did not exist.** `roles.go` first named
  `lsp_definition`/`lsp_references`; the real built-ins are
  `query_compiler_definition`/`query_compiler_references`. An allowlist typo
  *denies* rather than errors, so the Researcher and Coder would simply never
  have looked anything up. `TestEveryRoleToolExistsInTheRegistry` now reads the
  live registry so this cannot recur.
- **The enforcement test passed while enforcement was neutered.** It asserted on
  `agentResult.ToolNames`, which records what the model *asked* for — denied
  calls included. It now asserts on the activity phase (`denied` vs
  `running`/`succeeded`), and fails when the guard is removed.

### Still not built

Researcher and Tester ship defined but out of the default pipeline, per §6's
gate. The repository map (§4) remains unbuilt and still weakens the Planner.


---

## 9. Adversarial review of the implementation

A self-review of §8, hunting specifically for **cross-phase leakage** and **effects on
the unorchestrated path**. Five findings, all fixed; each was reproduced by a failing
test before the fix and re-checked by neutering the fix afterwards.

### L1 — Egress ceiling reset at every phase boundary · **the serious one**

`max_total_tool_bytes` bounds how much workspace content leaves the machine because of
tools — the quantity a privacy-positioned product exists to bound. It is tracked on
`agentTurn.toolBytes`, and `runAgentLoop` builds a **fresh `agentTurn` per call**, so the
orchestrator reset it at every phase. An N-phase turn could send up to **N times** the
ceiling the user configured, and the user never raised that limit — the pipeline
multiplied it silently.

**Reproduced:** three phases reading a 600-byte file against a 1500-byte ceiling sent
**1800 bytes**.
**Fixed:** `turnLedger` (agentloop.go) carries `toolBytes` across phases; a nil ledger
means a standalone loop that owns its own accounting, so the unorchestrated path is
unchanged.

The test that caught it initially did **not**: sized so one phase alone blew the ceiling,
the pipeline stopped at phase 1 and the reset never got a chance to show. It only becomes
observable when each phase is individually under the ceiling and they sum over it. The
test now says so in a comment, because the sizing is the load-bearing part of it.

### L2 — Approve-for-turn grants reset at every phase boundary

`agentTurn.grants` records "allow this tool for the rest of the turn". Same fresh-turn
problem: the user was re-asked in every phase for something they had already granted. A
pipeline **is** one turn, so this both contradicted what they chose and multiplied the
approval prompts §5 names as the most likely reason this feature gets switched off.

**Reproduced:** asked twice after granting once. **Fixed:** the ledger shares the grants
map by reference (it must be non-nil before the first phase — `agentTurn.grant()`
allocates a new map when it finds nil, and that map would not be the ledger's).

### L3 — An all-typo pipeline answered with silence

Every role name unknown produced zero phases, and `runOrchestrated` returned an empty
result: a blank reply, indistinguishable from a crash, for a spelling mistake. Now falls
back to the unorchestrated agent, with the unrecognised names already logged.

### L4 — Empty user message sent to the provider

`splitMessages` yields an empty prompt whenever the message list does not end in a user
turn, and `buildPhaseMessages` appended `{"role":"user","content":""}` regardless — a
malformed request that costs a round trip to be told so. Now omitted.

### L5 — `answerPhase(nil)` returned −1

A negative index would panic an unguarded caller. Returns 0 now; `runOrchestrated` guards
anyway (L3), but a function returning a valid-looking index from an invalid input is the
lesser wrong.

### Checked and found sound

- **Phase context isolation holds.** A phase receives previous phases' *conclusions*,
  never their tool results, dead ends, or the files they read. That is the feature.
- **Edit proposals are unaffected.** The Coder's edits land in the same per-turn
  `proposalSink` the single agent uses, so the human review path is untouched.
- **`full` collects only the answer phase.** Non-answer phases capture rather than call
  `onToken`, so edit blocks are parsed from the Coder's text alone.
- **The turn deadline already spanned phases.** `turnStart` is passed unchanged, so
  `turn_timeout_seconds` bounds the whole pipeline.
- **No mutable state on the role values.** The four roles are package-level vars shared
  across turns; `allows()` only reads them. Per-turn state lives on `agentTurn` and the
  ledger, never there.

### Pre-existing, not introduced here

A single loop overshoots `max_total_tool_bytes` by a few dozen bytes — the truncation
notice is appended *after* the cap is applied (measured: 1563 against a 1500 ceiling, with
no pipeline involved). Small, but it means the ceiling is approximate rather than hard.
Left alone: it predates this work and deserves its own fix rather than being bundled into
a feature branch.


---

## 10. Second review pass — growth and tidiness

A CTO/QA/security pass over the three orchestration files alone, targeting memory
growth, silent leaks, and latent panics. Four findings, all fixed and neuter-verified.

### G1 — The phase handoff was completely unbounded · **the real one**

Every earlier phase's full prose is appended to every later phase's user message, and
**nothing capped it**. Context grows with `phases x output-size`.

**Measured:** three phases emitting 200KB each produced a **614,909-byte** request.

The failure this causes is expensive and late: the model refuses on context length at the
**last** phase, after the turn has already paid for every earlier one. A four-phase turn
with a verbose Researcher is exactly the shape that hits it.

**Fixed:** each handoff is capped at `max_tool_result_bytes` (32KB default) with a visible
truncation marker — a later specialist reading a plan that stops mid-sentence must be able
to tell it was cut rather than treat the fragment as the whole plan.

**This is NOT an egress leak, and the distinction matters** because the neighbouring
budget looks like it should cover it. `max_total_tool_bytes` bounds what leaves the
machine *because of tools*, and it still does — the Researcher's file reads are charged to
it in full. What gets re-sent in a handoff is the model's own prose, which that same
provider generated moments earlier; echoing it back tells the provider nothing it did not
just say. **The problem was size, not disclosure**, so the fix is a bound rather than a
charge against the privacy ledger.

### G2 — Latent nil-pointer panic in the handoff builder

`buildPhaseMessages` dereferenced `out.role.Display` directly while `incompletePhaseText`
two functions away used the nil-guarded `out.Display()` accessor. A zero-valued
`phaseOutcome` would panic the turn. Unreachable on today's paths — every outcome is
constructed with a non-nil role — but two spellings of the same access, one guarded and
one not, is how it stops being unreachable later. Now both use the accessor.

### G3 — Degradations reported once per phase

A truncated tool menu is a fact about the **turn**, but it is raised inside the loop, so a
three-phase turn told the user the same thing three times. Deduped in the orchestrator
rather than the loop: the loop is right to report it every time it runs — it is the
orchestrator that made it run repeatedly.

### G4 — `max_iterations` is per phase, and that is deliberate — the total is now bounded too · **CLOSED**

`resolveBudget` runs per `runAgentLoop` call, so an N-phase pipeline can make
`N x max_iterations` model calls. Each specialist getting its own loop budget is the right
default — a Planner and a Coder have genuinely different needs, and a role may already
tighten its own ceiling (`agentRole.MaxIterations`, clamped so it can never raise it).

What was missing is a ceiling on the **product**. `turn_timeout_seconds` bounds the
pipeline's wall-clock and is a real backstop, but nothing bounded its token spend — §5's
fourth risk arriving exactly as predicted.

**Now fixed:** `mcp.budget.max_turn_iterations` (default 24) bounds the model calls made by
a whole turn across every phase. Two properties make it safe to ship switched on:

- **It can never bind tighter than `max_iterations`.** A single unorchestrated loop is one
  "phase", so a turn ceiling below the per-phase one would silently shorten every existing
  turn — a knob nobody set changing behaviour nobody asked to change.
  `resolvedMaxTurnIterations` raises it to the per-phase value instead, which makes the
  unorchestrated path unaffected by construction.
- **It reports the ceiling that actually stopped you.** Checked before the per-phase
  ceiling, because "this step reached its limit" would send a user to raise
  `max_iterations`, which is not the setting that bit.

#### Two mistakes worth recording, both caught by the same test

**The first implementation undercounted by one per phase.** It derived the ledger from
`turn.iteration`, but the loop counter is one *ahead* at a budget stop (the check runs
before the call) and *exact* at the normal exit — the two exits disagree about what the
counter means. A 4-phase pipeline made **11** calls against a ceiling of 7. Fixed by
counting model calls explicitly (`turn.calls`), incremented *before* the call so a request
that fails mid-stream is still charged: it cost the provider the same either way, and a
ceiling that only counts successes is one a failing loop can spend past.

**The first test could not have detected the bug it was written for.** It exhausted each
phase's own ceiling — but a phase that returns `Incomplete` *stops the pipeline*, so
budget exhaustion can never demonstrate the multiplication. The product only accumulates
when every phase finishes of its own accord having used several iterations, which is the
ordinary case. The test now scripts exactly that, and neutering the ceiling produces the
predicted 12 calls.

This is the third time in this feature that a test passed for the wrong reason
(§8's enforcement test, §9's L1, and this one). All three were caught by neutering the
fix and re-running; none would have been caught by review.

### Re-checked and still sound

- **Phase isolation.** Conclusions pass forward; tool results, dead ends and read files do
  not.
- **No mutable state on role values.** The four roles are package-level vars shared across
  turns and every access is a read — confirmed again after this pass, since it is the one
  place a data race could hide.
- **Ledger write-back covers every exit.** `defer` flushes `toolBytes` on the budget-stop
  returns as well as the normal one.
- **`captured` is per phase**, declared inside the loop, so no builder outlives its phase.


---

## 11. G5 — the Tester's verdict was thrown away

Found while answering "is it clean now?", by probing the one path no test covered: what
happens to a phase that runs **after** the answering phase.

The four-phase pipeline is Planner → Researcher → Coder → Tester, and `StreamsAnswer` is
marked on the **Coder**. `answerPhase` therefore returned index 2, and only that exact
index streamed. So the Tester ran — a model call plus `sandbox_exec` executing the
project's real test suite — and its output was captured into a buffer that nothing
afterwards read.

**Measured:** a Tester emitting `TESTS_FAILED_IMPORTANT` produced a reply of exactly
`"CODE"`. Tests fail, and the user is not told.

This is worse than a silent cost. The Tester's entire purpose is to report, and the
plumbing discarded the report — the one failure mode a validation phase must not have.

**Fixed:** every phase from the answering one onward streams to the user, with a labelled
separator so a post-answer specialist's prose does not run on from the Coder's as though
one voice wrote both. Phases *before* the answer are still captured-and-threaded, which a
test also pins — the fix must not turn the pipeline back into four essays stapled
together.

### Why no earlier pass caught it

Every §9 and §10 finding was about growth, leakage or a latent panic — properties of the
mechanism. This one is about **which phase's words the user sees**, which is a property of
the *configuration* the mechanism is given, and the default four-phase configuration is
the only one that exhibits it. The tests that existed all used two- or three-phase
pipelines where the answering phase happened to be last.

The lesson is narrower than "test more": a default that no test exercises is not a
default, it is an untested code path with a friendly name.

## 12. First contact with a live model

Everything in §§8–11 was measured against scripted SSE upstreams. This section is the
first run against a real provider, over this repository, with real retrieval — and it
found three defects that five review passes had not.

Harness: `daemon/orchestrator_eval_test.go`, build tag `eval`, gates written down before
the first run in the same discipline as the loop gate. It skips without credentials
because it makes real billed calls.

```
export MOCHIII_API_BASE=... MOCHIII_API_KEY=...
export MOCHIII_HELPER_BIN=$PWD/dist/mochiii-helper
./dist/mochiii-daemon index .          # the Researcher needs search_code
go test -tags eval -run TestOrchestrationLive -v -timeout 40m ./daemon
```

### L6 — a budget-stopped pipeline streamed nothing at all

The worst of the three, and it took one run to surface.

A four-phase turn exhausted the Researcher's `max_iterations` at phase 2 of 4.
`runOrchestrated` assembled 698 bytes of partial work — the Planner's completed plan and
the Researcher's findings so far — assigned them to `combined.FinalText`, and **streamed
zero bytes**.

The `Done` message carries no text (`daemon/agentturn.go`): the user's answer is exactly
what goes through `onToken`, and `FinalText` is read only for edit-block parsing and for
the history record. So the user saw a blank reply with an incomplete badge whose detail
read *"what you see above is everything that was done"* — above which was nothing.

Two defects in one:

1. The user got a blank answer, which is precisely what `incompletePhaseText`'s own doc
   comment says it exists to prevent. The function was dead weight for its stated purpose.
2. Those 698 bytes **were** persisted to the transcript, so the next turn's history
   carried an assistant message the user had never been shown.

**Fixed:** the partial text is emitted through `onToken` before the early return, so the
screen and the transcript agree. Verified live: the same scenario now returns 522 bytes
and streams 522.

This is the same failure class as G5 — *text that reaches a return value is not text that
reaches a person* — which is worth stating as a rule, because two independent instances of
it survived five reviews.

### L7 — a refusal that named no tool

A real model asked for `search_code` rather than the qualified `builtin__search_code`,
twice in one turn. `SplitQualifiedName` cannot split an unqualified name, so the reported
activity carried an **empty** tool name and the user was shown a refusal of nothing at all.

Pre-existing in the agent loop, not caused by orchestration — but it only became visible in
a run whose whole point was watching what each specialist did.

**Fixed:** the raw name the model gave is reported when the split fails, bounded to 128
bytes because it is model-supplied. (`decision.reason` already carried the same name into
`Detail` unbounded; that pre-existing exposure is noted here rather than widened.)

### L8 — phase narration was invisible in every client

The headline finding, and the one that had been sitting in plain sight.

`narratePhase` emitted its marker with `Phase: ToolPhaseRequested`. Both shipping clients
render `requested` as the empty string **on purpose** — the user has just answered a modal
about that exact call, and narrating it back is noise. So every phase marker the daemon
sent was discarded, and the feature that tells a user which specialist is working did
nothing whatsoever.

A second bug sat behind it: `noteToolActivity` keys the transcript on `CallID`, and phase
markers have none. Had narration rendered, all four phases would have collapsed onto one
line. The same collapse hit real tool calls — measured live, the provider emitted tool
calls with no id, so the second of two refusals silently overwrote the first.

**Fixed:** a dedicated `protocol.ToolPhaseStep`, rendered by both clients; the label split
across `Tool` and `Detail` so a client formats a step rather than parsing prose; and an
empty `CallID` is never used as a transcript key.

**Why no daemon-side test could catch it.** The daemon was doing exactly what it intended
and the client was doing exactly what it intended; the defect lived only in the seam. The
test that should have caught it — `TestEachPhaseIsNarrated` — asserted that `a.Tool`
contained the substring `"step "`, which was true of the pre-formatted label and said
nothing about whether anything would render. It has been rewritten to pin the wire
contract, and its other half now lives in `clients/tui/phasenarration_test.go`, with each
half naming the other.

### What the live run confirms

- **Role scoping holds against a real model.** The Planner, given an empty tool list,
  executed nothing across every run.
- **The Researcher never wrote.** `propose_edit` is absent from its allowlist and it never
  got one past.
- **The handoff is used.** The Coder answered from what it was handed rather than
  re-deriving it.
- **G5 stays fixed live.** The Tester's verdict reaches the user with a real model
  choosing its own words, not just a scripted one.

### The standing caveat, updated

The gap is no longer "it has never run live". It is narrower and now measurable: **three
runs is not a sample.** The gates are pass/fail on a handful of scenarios, and nothing here
establishes that the pipeline answers *better* than a single agent — only that it answers,
scopes correctly, and no longer discards anyone's work. The comparison against an
unorchestrated turn on identical prompts is the next measurement, and it is the one that
decides whether the ~4× cost is earned.

## 13. The bare-name tax — decision and rationale

§12 measured it and deliberately did not fix it, because the fix is a security decision.
This is that decision, taken and implemented.

### The measurement

8 refusals across 3 live orchestrated turns; roughly **5 of one turn's 14 model calls**.
Every one was the model asking for `search_code` when the registry knows the tool as
`builtin__search_code`. Each cost a refusal, a re-prompt and a retry, out of a budget the
user bought for real work.

### The decision

> **A bare tool name resolves to the first-party builtin lane, or it does not resolve at
> all. It never reaches a third-party server.**

Implemented as `mcp.Registry.Canonicalize`, called from `resolveExecutable`.

### Why not the obvious alternative

The tempting design is "resolve a bare name to whichever configured server uniquely offers
it". It is rejected, and the reason is worth stating plainly because it looks strictly more
useful:

**It makes the meaning of a bare name depend on the set of installed servers.** Adding a
server could silently change what an existing bare name resolves to, or make a name that
worked yesterday ambiguous today. That is PATH shadowing. A security surface that mutates
with configuration is one nobody can review — least of all the user, who installed an MCP
server for an unrelated reason.

### Why Lane A is safe to alias into

Not by assertion — by an invariant this codebase already enforces. `ValidateServerName`
**refuses an external server called `builtin`**, with the stated reason that it "could
shadow them with unconfined ones". The builtin namespace is registered at startup from
compiled-in code and cannot be influenced by config, by a server, or by anything a model
says. `Canonicalize` is a second consumer of that existing guarantee rather than a new
assumption.

So the dangerous direction — the model means a confined built-in and reaches an unconfined
third-party tool — is **impossible by construction**. The only misfire left is the reverse:
a model that meant some server's `read_file` and gets the built-in one. That lands on the
*more* restricted tool (workspace-confined, policy-gated) and the result tells the model
what it got.

### The constraint that keeps it from becoming a bypass

Aliasing a name and then checking permission against the raw string is exactly how a
convenience becomes a vulnerability. So:

**One variable carries the resolved name into all four decisions** — `Lookup`, the role
allowlist, the config policy, and the per-turn grant ledger — and into dispatch. The human
was already unaffected: the approval prompt has always been built from `spec.Server` and
`spec.Name`, so what is approved and what is dispatched are the same string.

That structure is pinned by tests that fail when each is broken individually:

| Neutered | Caught by |
|---|---|
| bare name may reach any server offering it | `TestBareNameNeverReachesAThirdPartyServer` |
| aliased call treated as permitted | `TestBareNameDoesNotBypassAConfigDeny` |
| aliased call skips the role allowlist | `TestBareNameDoesNotBypassRoleScoping` |
| prompt shows what the model typed | `TestTheApprovalPromptShowsTheResolvedName` |
| grants keyed on the raw spelling | `TestATurnGrantCoversBothSpellings` |

Exact match only — no case folding, no trimming, no fuzzy matching. A tool name is
dispatched on, and this codebase has been bitten by case-insensitive matching before.

### A bug the change introduced, and the test that caught it

`dispatchToolCall` still called `registry.Call` with the **raw** name. The call resolved
for policy, was approved, reported "running", and then failed in dispatch with *"not a
qualified tool name"*. It failed closed, but it failed — and only the test that a bare name
actually **runs** could catch it, because every test that asserts a refusal passes whether
the refusal is right or wrong. Dispatch now uses `decision.tool.QualifiedName()`: the same
value every permission check was decided against.

### The residue, and observability

A bare name that is *not* a built-in must still be refused — it belongs to some server and
the model has to say which. That refusal now carries the naming convention, so it costs one
retry rather than a loop of them.

`toolNamesCanonicalized` counts every resolution. The tax stays measurable after it stops
being paid: if it climbs, the model is drifting from the advertised names and the prompt
needs attention rather than the registry.

## 14. Does the pipeline earn its cost?

§12 left this open and §13 closed the tax that was making it worse. This is the answer.

Harness: `daemon/orchestration_ab_eval_test.go`, build tag `eval`.

### Designing the measurement so it could say no

The naive A/B — pipeline against a single agent on default settings — is rigged. The single
agent gets `max_iterations`; the pipeline gets `max_iterations` **per phase**. Any win it
showed could be "more compute produces better answers", which says nothing about whether the
*structure* is worth anything.

So the single-agent arm is **budget-matched**: it gets the pipeline's whole-turn ceiling as
its own `max_iterations`. A third arm runs two phases, because "four is too much but two
earns it" is actionable and a binary verdict is not.

Judging is **blind pairwise with randomised position** — judges prefer whichever answer they
see first — on a rubric that puts groundedness above everything and explicitly refuses to
reward length or confident tone. Exact token counts come from a recording pass-through in
front of the provider; the daemon already sets `stream_options.include_usage`, so no
production code was changed to measure it.

The decision rule was written down before the first run: **a tie at materially higher cost
counts as a loss.** Paying more for the same answer is not neutral.

### First run: not earned, and the reason was a bug

| arm | wins | tokens | wall-clock |
|---|---|---|---|
| four-phase vs single | 1 W / 1 L / 1 T | 1.45× | 2.3× |
| two-phase vs single | 1 W / 1 L / 1 T | 1.54× | 1.4× |

Verdict: not earned. But the losses had one cause, and it was a defect rather than a
property of the design.

**Both pipeline arms went incomplete while the single agent finished.** `max_total_tool_bytes`
is a privacy bound on the **turn**, and a pipeline is one turn, so every phase drew on one
128 KiB pool — correct as a bound, and it starved the only phase that matters. On the
cross-file question the Researcher drank the pool and the pipeline stopped at phase 2 of 4,
handing the user raw partial research where they had asked a question. The judge's words for
the two-phase answer: *"stubs that never actually provide the requested specifics."*

The second cause was the Planner. It has **no tools**, so it cannot check anything it says.
It invented `src/agent/agent.ts` and `src/agent/tools.ts` — TypeScript paths, in a Go
repository — and the later phases carried them into the answer. Both of the four-phase
pipeline's losses on that scenario were attributed by the judge to exactly those paths. Its
role prompt already says *"say plainly when you do not know one rather than inventing a
plausible path"*; it invented anyway, which is the ordinary result of using a prompt as a
control.

### The two fixes

**The answering phase now has a guaranteed share of the turn's tool budget.** Phases before
it share a slice proportional to how many they are; the answering phase and everything after
get the remainder. A pre-answer phase hitting *its own reservation* ends that phase, not the
turn — the three turn-wide bounds are checked directly, so a genuine exhaustion still stops
everything. The reservation may only ever **tighten** a ceiling (`turnLedger.toolByteCap`
is applied as a minimum), so no pipeline shape can spend past what the user configured.

**A tool-less phase's handoff is labelled.** It now arrives as *"(this step had NO TOOLS and
verified nothing: treat any file path or symbol it names as a guess to check, not as a
finding)"*. Labelling is a property of the pipeline; the prompt was not.

### Second run: the answer changes

| arm | wins | tokens | wall-clock |
|---|---|---|---|
| **two-phase vs single** | **2 W / 1 L** | **1.02×** | 1.4× |
| four-phase vs single | 1 W / 2 L | 1.00× | 3.5× |
| four-phase vs two-phase | 2 W / 1 L | — | — |

**The cost premium disappeared entirely** — 1.45× → 1.00×. Capping early phases stopped them
over-reading, so the pipeline now costs what a single agent costs. That is the engineering
result, and it is larger than the quality result.

**Two phases earn their cost.** 2–1 against a budget-matched single agent at 1.02× the tokens.

**Four phases do not, and now there is no trade to argue about**: the same tokens, worse
answers, three and a half times the wall-clock.

### What the data also says about *when*

The wins split by question type, consistently across both runs:

- **Cross-file, multi-hop** ("how do two processes not collide?") — the pipeline wins. After
  the starvation fix, both pipeline arms beat the single agent on the scenario where they had
  previously lost or tied.
- **Single lookup** ("what does this function do?") — the single agent wins and is four times
  cheaper. The judge's reason: the pipeline *"pads with implementation detail"* while the
  single agent *"is significantly shorter while covering all required points"*.

### Honest limits

Three questions, one trial, one model, and a judge from the same family as the arms. The
second run is **intransitive** — four beats two, two beats single, single beats four — which
is what noise looks like at this sample size and is the reason none of this is switched on by
default. What the sample does support is rejecting four phases: it lost on cost before the
fix and lost on quality after it, in both runs.

## 15. The shape is a property of the question — so who decides it?

§14 ended with a verdict the config surface could not express.

> **Cross-file, multi-hop** — the pipeline wins.
> **Single lookup** — the single agent wins, and is four times cheaper.

`mcp.pipeline` is one static list for every turn. Whichever way a user sets it, that split
means they are wrong on half their questions: set the two-phase shape and every *"what does
this function do"* costs 1.4× the wall-clock for a worse answer; leave it unset and the
multi-hop wins are unavailable.

The obvious move is to route automatically. It does not work, and the two attempts are
recorded here because the failure is mechanical rather than a matter of tuning — anyone who
tries a third signal should start from these.

### Hypothesis 1 — evidence spread. Falsified.

Run the daemon's own retrieval over the prompt and count how many **distinct files** the
top-ranked chunks come from. Attractive because it measures the win/loss axis directly
rather than inferring it from vocabulary the way a keyword rule would, and because it is
local, free, and content-blind — it reads a count of paths and nothing else.

Replayed through real retrieval over this repository, with each scenario's label taken from
which arm actually won §14's blind pairwise comparison:

| scenario | §14 winner | distinct files |
|---|---|---|
| cross_file_mechanism | pipeline | 5 |
| trace_a_decision | pipeline | 4 |
| **why_does_this_exist** | **single** | **4** |

Not separable — and the raw dump says why. `retrieveTopK` **always returns k chunks**, and
the fused scores across all three scenarios span 0.0147–0.0185, which is `1/(60+rank)`:
**reciprocal-rank fusion, which discards relevance magnitude by construction.** The daemon
has ranks and no calibrated confidence. So distinct-file count over an unfiltered top-k
measures *how much padding retrieval returned*, not how the evidence is distributed —
`truncateHandoff` pulled in `platform_windows.go` and `embedderstamp.go` as filler. That
also kills every rescue built on the same scores: concentration, entropy, α-thresholding.

### Hypothesis 2 — lexical breadth. Falsified harder.

The one quantity *not* bounded by k: how many chunks in the whole index the lexical tier
matches at all. A rare identifier should match a handful in one file; a conceptual question
should match hundreds across many.

| scenario | §14 winner | matches | files |
|---|---|---|---|
| cross_file_mechanism | pipeline | 2000 (capped) | 383 |
| trace_a_decision | pipeline | 2000 (capped) | 366 |
| **why_does_this_exist** | **single** | 2000 (capped) | **394** |

All three saturate. `buildLexicalQuery` ORs the query's terms, so any natural-language
question matches most of the index and breadth carries no information about the question.

### What shipped instead: an explicit signal

This codebase already settled this exact question on the other routing axis.
`daemon/router.go` picks a **model tier** and its doc comment commits to
*"explicit signals only — never by reading or guessing at prompt content"*. The shape axis
now takes the same stance, for a stronger reason: the guessing was tried and measured.

- `protocol.PromptRequest.Pipeline` names the phases for **one turn**, overriding
  `mcp.pipeline`. Absent means use the configured shape, so every existing client is
  byte-identical.
- In the TUI, `/team <question>` — the same trailing-space parse contract as `/reason`, so a
  bare `/team` falls through as ordinary text.

**A requested shape cannot widen what a turn may do**, and that is why it is safe to accept
from a client at all. A phase is a *restriction*: the unorchestrated agent is a nil role and
unrestricted, and every named role allows exactly the tools it lists. The budgets are
whole-turn and shared through one ledger, so naming more phases buys no extra iterations, no
extra time and no extra bytes. `TestNoRequestedShapeCanReachATooltheUnorchestratedAgentCannot`
fails the moment that stops being true.

`/team` sends `["researcher","coder"]` and specifically not four phases. A command that
offers a user "more specialists" and hands them the shape that lost §14's A/B is worse than
no command.

### The measured losers are now said out loud

`pipelineWarnings` (roles.go) logs what happened when a configured shape was measured: a
tool-less first phase (1 W / 2 L at the same tokens), four or more phases (3.5× the
wall-clock for a worse answer). It does **not** refuse them — it is the user's repository and
their money — but a default that lost an A/B should not sit silently in a config file for a
year.

### Harness

`daemon/shapecalibration_eval_test.go`, build tag `eval`. It **reports rather than asserts**:
a red gate recording a settled fact is noise, and a gate that fails when someone finds a
*better* signal is worse. It reuses a persistent index (`CALIBRATION_INDEX_DIR`) so a second
look costs eight seconds instead of three minutes — a calibration nobody can afford to
re-run is one that never gets questioned.

---

## 16. Reserving the third turn-wide bound

§14's starvation fix was applied to one of the three bounds. This is the audit of the other
two.

Every budget bounds the **turn**, and a pipeline is one turn:

| bound | reserved for the answering phase? |
|---|---|
| `max_total_tool_bytes` | yes — `turnLedger.toolByteCap`, §14 |
| `max_iterations` | yes, **by accident** — it is per phase, so a pre-answer phase can spend at most its own ceiling |
| `turn_timeout_seconds` | **no. Nothing at all.** |

Every phase saw the same `turnStart + turn_timeout` deadline. A first phase that was merely
**slow** — not looping, not wrong, just talking to a slow provider — could consume the whole
turn and hand the answering phase a deadline already in the past. That is the identical
starvation the byte reservation exists to prevent, in the bound §14 measured closest to
biting: six to seven minutes of four-phase pipeline against a ten-minute default.

`turnLedger.deadlineCap` is the byte cap's twin and follows its rule exactly: taken only when
it is **earlier** than the configured deadline, so it can shorten a phase and can never
extend the turn. A phase that stops on its own reservation continues to the next phase; the
three turn-wide bounds are checked directly, so a genuine exhaustion still stops everything.

Two smaller things fell out of writing it down.

**A reservation stop no longer blames `turn_timeout_seconds`.** The message pointed at a
setting that had not been reached, which would have sent somebody to raise the wrong number.

**A pre-answer share of zero does not mean "unlimited".** `toolByteCap` reads zero as *no
phase cap*, and integer division reaches zero from a small enough configured budget — so the
reservation would have switched **itself** off in the one case where the answering phase can
least afford to be starved. Unreachable at any budget a person would set (it needs
`max_total_tool_bytes` below the phase count), so a latent hole rather than a live bug; the
floor exists so that changing the formula later cannot make it live.

All four are pinned by tests that fail when the fix is removed:

| neutered | caught by |
|---|---|
| no wall-clock reservation | `TestASlowEarlyPhaseCannotConsumeTheAnswerPhasesTime` |
| phase deadline may extend the turn | `TestThePhaseDeadlineCanOnlyTightenTheTurnDeadline` |
| the zero floor removed | `TestThePreAnswerByteShareIsNeverZero` (22 failures) |
| reservation stop blames the turn timeout | `TestAReservationStopDoesNotBlameTheTurnTimeout` |


## 17. Finishing the per-turn shape, and the lane the role filter could not see

§15 shipped the per-turn shape as a wire field, a resolver, a cap, an unknown-name path
and a warning function. Only one of those was reachable: the TUI sent one hardcoded pair.
The Planner and Tester existed and could not be run; `maxRequestedPhases` could not be
hit; `pipelineWarnings` could not fire. Completing it surfaced two defects in the role
filter that had been there since roles.go was written, both **confirmed by running code
before anything was changed**.

### F-11 — a role allowlist matched a name and could not see a lane

`agentRole.allows` compared `Tool.Name`, unqualified. A third-party server exposing a tool
called `read_file` was therefore admitted by the Researcher's allowlist, which names the
**first-party** `read_file` — a different program, in a different lane, unconfined.

Both halves of the two-part control were fooled at once, because both compared the same
lane-blind string: the menu offered `helpful__read_file`, and `resolveExecutable` resolved
a call to it as `ask` rather than `deny`. Contained rather than exploitable — policy still
resolves an unlisted tool to `ask`, and the approval prompt reports lane and confinement
honestly — but the role allowlist itself was not a control against a name it did not own.

This is the tool-level twin of a reservation the codebase already makes correctly one
level up: `ValidateServerName` refuses `builtin` as a configured server name, in its own
words, "so a user cannot shadow the confined tools with unconfined ones of the same name."
That reasoning was right and had simply not been carried down to where a role matches.

Fixed: `allowsTool` takes the tool, checks the lane, then the name. The lane-blind half is
renamed `allowsBuiltinName` so calling it by accident is no longer possible.

### F-12 — every third-party tool vanished for the length of a pipeline turn

The same unqualified match meant no role's list could ever name a Lane B tool, so **every
configured third-party tool disappeared from every phase**, silently. Measured: the
unorchestrated agent was offered the tool; the Coder was offered nothing.

The exclusion is correct — a role's list is curated first-party names, and nothing about a
third-party tool's name lets a role vouch for what it does. Doing it silently was not.
`DegradedToolMenuTruncated` had already written the rule for the advertised cap: *"a tool
that was dropped and a tool the server never offered both show up as the model not using
it. The user configured that server on purpose and deserves to know which of the two
happened."* The role filter is a second way to drop a tool and skipped that rule.

Fixed: `advertisedToolSpecs` returns what the role filter removed, and a phase that
removed anything says so — with the answer in the notice: ask without the pipeline.

### The warnings now reach the person who chose

`pipelineWarnings` had only ever gone to `s.logger`, which was defensible while choosing a
shape meant editing a file and restarting. Once `/team:` made it a per-turn decision, the
chooser became someone watching a chat window. They now arrive as
`protocol.DegradedPipelineShape`, split deliberately:

| | reported to the user | why |
|---|---|---|
| unknown role name | always | a typo is a correctness problem whatever its source |
| measured-bad shape | only when the request named it | a standing config choice re-warned every turn is nagging, and the measurement is only actionable at the moment someone typed it |

Two stale things fell out of making those strings user-visible: they said `mcp.pipeline`
regardless of where the shape came from (`source` is now a parameter — telling someone who
typed `/team:` that their `mcp.pipeline` is wrong sends them to a file that does not say
that), and they still advertised `"auto" picks per question`, which is the routing §15
falsified and killed.

### Neuter results

| neutered | test that caught it |
|---|---|
| the lane check in `allowsTool` | `TestAThirdPartyToolCannotTakeAFirstPartyRolesSlot` (menu, exclusion report, **and** the call) |
| the exclusion report | `TestAPipelinePhaseSaysWhichThirdPartyToolsItDropped` (all three roles) |
| `fromRequest` ignored | `TestAMeasuredBadShapeIsReportedToTheUserWhoAskedForIt` |
| shape cut at the first space | `TestTeamShapeNamesThePhasesExplicitly` |

The last one was a defect in the new parser, found by its own test before it shipped:
`strings.Cut(rest, " ")` reads `/team:researcher, coder why` as the shape `researcher,`
and swallows `coder` as the first word of the question. The shape now ends at the first
space that does not follow a comma.
