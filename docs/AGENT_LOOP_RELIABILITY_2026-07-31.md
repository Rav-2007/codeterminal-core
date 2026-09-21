# Agent-loop reliability of `deepseek/deepseek-v4-flash` — measured 2026-07-31

Phase 4.5 of the MCP / agentic-loop plan, and the gate that unblocks Phases 5–7
(consent UX across three clients).

Harness: `daemon/agentloop_eval_test.go` (`//go:build eval`).

```
export MOCHIII_API_BASE=https://openrouter.ai/api/v1 MOCHIII_API_KEY=...
go test -tags eval -run TestAgentLoopReliability -v ./daemon
```

15 scenarios × 2 trials = **30 real agent turns**, running the real loop with
the real built-in tools over this repository, direct to OpenRouter. 8m49s total,
zero transport failures.

## Why this existed

Phase 0 measured whether Flash can **emit** a tool call. It did not measure
whether Flash can run a **loop** — every `multistep` scenario there said "Start
with the first step only". Consuming a tool result, deciding whether to
continue, and knowing when to stop are different and harder properties, and
they are the ones that decide whether agent mode works.

Two Phase 0 findings said the risk was real rather than theoretical:
`list_directory` is an attractor the model reaches for when uncertain, and tool
selection degrades as the menu widens. In a loop the context grows with every
tool result, so both pressures compound.

## Verdict: PASS — Flash can carry the agent loop

| Gate | Result | Threshold | |
|---|---|---|---|
| `terminates` | **100.0%** (30/30) | ≥95% | PASS |
| `no_repeated_call` | **100.0%** (30/30) | ≥95% | PASS |
| `uses_tool_output` | **100.0%** (26/26) | ≥90% | PASS |
| `within_four_iterations` | **96.7%** (29/30) | ≥80% | PASS |

Per-category end-to-end (every applicable gate passing on the same trial):

| Category | Rate |
|---|---|
| `chain` (find then read) | 100.0% (8/8) |
| `multifile` (two reads before answering) | 100.0% (4/4) |
| `propose` (read then propose an edit) | 100.0% (2/2) |
| `termination` (tools available, none needed) | 100.0% (10/10) |
| `explore` (navigate unknown structure) | 83.3% (5/6) |

**The two failure modes we were most worried about did not occur once.** No turn
failed to terminate; no turn repeated an identical call. The `termination`
category — where tools are on the menu but the task needs none, the exact shape
of Phase 0's `list_directory` over-calling — passed 10/10.

## The economics are better than the plan assumed

**Median 2 iterations and 1 tool call per turn.** The plan budgeted for up to 8
iterations and warned about "8× quota reservations per turn"; the real
distribution is ~2× a normal turn, not 8×. `max_iterations: 8` is a genuine
safety ceiling rather than an operating point.

~17.6s per agent turn wall-clock, which is the number to watch for the UX
concern — a multi-second turn needs the `ToolActivity` progress messages to not
feel broken.

## The one failure

`explore/find_prompts` trial 2 took 5 iterations (4 tool calls) to locate the
system-prompt file under `daemon/`. It got the right answer; it just wandered.
That is the `within_four_iterations` gate's only miss, and it is a cost
observation rather than a correctness one — which is why that gate's threshold
was set at 80% rather than 95%.

Its sibling trial passed, so this is variance in exploration depth, not a
systematic inability to navigate.

## Honest limits of this measurement

- **30 turns is a modest sample.** 30/30 does not mean 100% in the wild; the
  95% confidence interval on 30/30 runs to roughly [88%, 100%]. The gates are
  passed, comfortably, but "no failures observed in 30 turns" is the honest
  claim rather than "never fails".
- **The scenarios are ours, over our own repository.** They were written to
  require genuine chaining and to be checkable without an LLM judge, but they
  are not a user population.
- **`search_code` was denied** for this run: it needs the ONNX embedder the
  harness does not start, so leaving it advertised would have measured the model
  burning an iteration on an unavailable tool. The advertised menu was therefore
  3 tools, not the production 4+.
- **A minimal system prompt was used**, not the production one (which is written
  for the single-turn edit-block flow). Production loop behaviour with the real
  system prompt is not measured here.

## Decision

**Phases 5–7 are unblocked.** No reasoning-tier escalation, no prompt redesign,
no reduction in scope. Proceed to live consent (Phase 5), then the CLI and VS
Code approval surfaces (Phase 6), then the positioning corrections (Phase 7).

Re-measure when the `primary` slug changes, when the built-in tool set grows
(menu width is the known degradation axis), or when the production system prompt
starts carrying tool instructions.
