# Tool-calling reliability of `deepseek/deepseek-v4-flash` — measured 2026-07-31

Phase 0 of the MCP / agentic-loop plan. This is the gate every later phase
depends on: if the `primary` tier cannot emit well-formed tool calls, agent mode
has to route to the reasoning tier (~3x output cost) and the economics of the
whole feature change.

Harness: `daemon/toolcall_eval_test.go` (`//go:build eval`).

```
export CODETERMINAL_API_BASE=https://openrouter.ai/api/v1 CODETERMINAL_API_KEY=...
go test -tags eval -run TestToolCallReliability -v ./daemon
```

28 fixed scenarios x 3 trials = 84 live calls, over six categories: single tool
available, choice among five, required-argument fidelity, nested-object
arguments, correct refusal when no tool fits, and the first call of a dependent
multi-step chain. Routing came from the real `models.json` (`zdr=true`,
`ignore=[DeepInfra]`), so this measures the model through the configuration we
actually ship.

## Verdict: PASS — Flash carries agent mode

All four gates cleared the thresholds that were written down **before** the
first run.

| Gate | Result | Threshold | |
|---|---|---|---|
| `well_formed_arguments` | **100.0%** (69/69) | ≥ 98% | PASS |
| `schema_valid` | **95.7%** (66/69) | ≥ 95% | PASS |
| `correct_tool` | **95.7%** (66/69) | ≥ 90% | PASS |
| `clean_termination` | **98.8%** (83/84) | ≥ 95% | PASS |

Per-category end-to-end pass rate (every applicable gate passing on the same
trial):

| Category | Rate |
|---|---|
| `single_tool` | 100.0% (15/15) |
| `required_args` | 100.0% (12/12) |
| `nested_args` | 100.0% (9/9) |
| `multistep` | 100.0% (12/12) |
| `refusal` | 93.3% (14/15) |
| `tool_choice` | **85.7%** (18/21) |

Cost: ~$0.000052 per call, so the full 84-call run is well under a cent.
Wall clock ~7 minutes.

## Two findings that are design inputs, not noise

**1. Tool selection degrades as the menu widens.** 100% with one tool available,
85.7% with five. This is the single most actionable number here, and it argues
for advertising a *small* tool set per turn rather than the union of every
configured server's tools. Phase 3 should treat the advertised set as something
to be curated, not merely assembled.

**2. `list_directory` is an attractor.** Every failure in the run involved it
being reached for when something else was right:

- `choice/tests` failed **all three trials, deterministically** — "Please run the
  editapply test suite" selected `list_directory` instead of `run_tests`. That
  one scenario accounts for all 3 `correct_tool` misses and all 3 `schema_valid`
  misses (a wrong tool has none of the expected arguments). Its near-twin
  `single/run_tests` ("Run the tests for the protocol package") passed 3/3 with
  only one tool on the menu — so this is menu width, not prompt wording.
- `refusal/general_question` called `list_directory` once out of three for "what
  is the difference between a mutex and a semaphore?".

Read together: when uncertain, Flash reaches for the cheapest-looking read tool
rather than declining. That is the benign direction to fail in, and it is
**exactly the failure mode per-call consent absorbs** — a spurious
`list_directory` becomes a prompt the user declines, not a silent action. The
measurement supports the consent design rather than undermining it.

`schema_valid` at 95.7% sits closest to its floor. It has no independent
failures — remove the `choice/tests` scenario and it is 100% — but it is the
number to watch on any re-measurement.

## The first run was VOID, and the harness now says so

The first attempt went through the managed proxy and lost **33 of 84 probes** to
`429 quota_exceeded` partway through. It reported `PASS`, "all 4 gates passed".

It should not have. The lost trials were not a random sample — they were
whatever came after the budget ran out, which is the tail of a fixed scenario
list. Two whole categories, `refusal` and `multistep`, got **zero** graded
trials and printed as `100.0% (0/0)`, because `gateTally.rate()` returned 1 for
an empty tally. `refusal` is the category that carries `clean_termination`'s
most important case — "call nothing when nothing fits" — so that gate passed
without ever being exercised on the thing it exists to test.

Three changes make that unrepresentable:

- `gateTally.rate()` **panics** on an empty tally instead of returning 1.
- Any category or gate with zero graded trials fails the test as `VOID`, with
  the word "absences, not passes".
- A transport-error rate above 10% (`maxTransportErrorRate`) fails the run as a
  biased sample, before any verdict is printed.

The valid run above went direct to OpenRouter, bypassing the managed quota,
and had **zero** transport errors across all 84 trials.

## Proxy: no change required (verified live)

A `tools`-bearing body was sent through the production proxy at
`codeterminal-core-production.up.railway.app`. Result: **HTTP 200**, streamed a
correct `read_file` tool call, ZDR routing intact (served by `Morph`, not the
deny-listed DeepInfra).

This confirms the read of `costSurfaceRefusal` (`proxy/main.go`): its deny-list
is `{models, plugins, transforms, provider.only/order/sort}`, and neither `tools`
nor `tool_choice` appears in it. `zdrRoutingEnforced` only inspects
`provider.zdr` / `provider.data_collection` and is unaffected. **Phase 3 needs no
proxy change.**

Observed streaming shape, which the Phase 3 accumulator must handle:

```
delta.tool_calls[0] = {index:0, id:"call_…", type:"function",
                       function:{name:"read_file", arguments:""}}   <- name here, args empty
delta.tool_calls[0] = {index:0, function:{arguments:"{\"path\":…"}} <- args in later frames
finish_reason = "tool_calls"
```

Name and id arrive once on the first fragment; arguments arrive as string
fragments that are only valid JSON once concatenated. `daemon/incomplete_test.go`
currently treats `tool_calls` as an *unrecognised* finish reason — Phase 3 must
stop it being reported as a truncated answer.

## Decision

**Agent mode routes to `primary` (`deepseek/deepseek-v4-flash`).** No reasoning-tier
escalation, no cost delta to absorb, Phase 4 proceeds as planned.

Re-measure when the `primary` slug changes, and treat `tool_choice` and
`schema_valid` as the two numbers that decide.
