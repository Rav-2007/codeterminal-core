# Agent workflow eval — did the agent get the job done?

The yardstick for the agent-workflow plan (working copy → specs → build from spec →
grounded planner). One number, graded without a model judge:

> **Task success** — the share of trials where, after applying every edit the agent
> proposed (as a user pressing `y` on each), the task's *hidden* tests pass.

Harness: [`daemon/tasksuccess_eval_test.go`](../daemon/tasksuccess_eval_test.go)
(`-tags eval`). Eight small Go projects in
[`daemon/testdata/tasks/`](../daemon/testdata/tasks/), each with a prompt, hidden tests
the model never sees, and a reference solution. `TestTaskFixturesAreValid` (no model, no
key) proves every check **fails** on the untouched project and **passes** on the solution
applied through the real edit path — so no task can be passed by doing nothing, and no
check is impossible.

The run uses the product's own configuration: `models.agent.json` (tool policies,
budgets), the production system prompt, the turn setup `runAgentTurn` uses
(`newTurnSink`), and the real model. Raw results, one JSON line per run:
[`agent_workflow_eval.jsonl`](agent_workflow_eval.jsonl).

```
export PATH=$HOME/.local/go/bin:$PATH
MOCHIII_API_BASE=https://openrouter.ai/api/v1 MOCHIII_API_KEY=... \
  go test -tags eval -run 'TestTaskSuccess$' -v -timeout 120m ./daemon
```

Knobs: `TASK_EVAL_TRIALS`, `TASK_EVAL_ONLY`, `TASK_EVAL_TIER`, `TASK_EVAL_MAX_TOKENS`,
`TASK_EVAL_MAX_ITERATIONS`, `TASK_EVAL_NO_WORKING_COPY`, `TASK_EVAL_LABEL`, `TASK_EVAL_OUT`.

---

## M0 — baseline (2026-09-27)

`deepseek/deepseek-v4-flash`, `max_iterations` 8, no working copy, 8 tasks × 2 trials.

| task | pass |
|---|---|
| create_file | 2/2 |
| two_edits_one_file | 2/2 |
| add_function_with_test | 1/2 |
| fix_failing_test | 0/2 |
| rename_across_files | 0/2 † |
| edge_case_bug | 0/2 |
| feature_from_spec | 0/2 |
| needs_iteration | 0/2 |
| **total** | **5/16 (31%)** |

Median 8 model calls, 35.8k tokens, 198 s per trial. **10 of 16 trials were stopped by
the model-call budget**, and **9 of the 11 failures proposed no edit at all**: the agent
spent all eight calls reading files and running commands, and the turn ended before it
changed anything. The first bottleneck is not edit quality; it is getting to an edit.

† Trial 1 of `rename_across_files` was failed by a **grader bug**, not the agent: the
check for leftover `CalcTotal` read the backup of the original file in `.mochiii/`.
Fixed in `7112301`; the fixture test now applies solutions through the edit path, which
would have caught it. That task is re-run for the baseline below.

---

## Decision rules — written before the runs they judge

**M1 (working copy).** Two arms, same tasks and trials:

- **A** — working copy, production budget (`max_iterations` 8).
- **B** — working copy, `max_iterations` 16.

The working copy stays **on by default** if A's task success is **≥ the baseline's**. If A
is *below* the baseline, it is switched off by default and the result is written here.
The per-turn budget for working-copy turns is raised to 16 **only if** B beats A by at
least 3 trials **and** B's median tokens are within 2× A's; otherwise it stays at 8, and
the budget question moves to the prompting that decides when the agent edits.

A tie at materially higher cost counts as a loss.

---

## M1 — the working copy (2026-09-27)

| arm | pass | median tokens | median calls | budget stops |
|---|---|---|---|---|
| M0 baseline (no working copy, 8 calls) | 5/16 (31%) | 35.8k | 8 | 10 |
| **A** — working copy, 8 calls | **6/16 (38%)** | 36.3k | 8 | 12 |
| **B** — working copy, 16 calls | **11/16 (69%)** | 44.9k | 8 | 6 |

The baseline's `rename_across_files` was re-run after the grader fix: still 0/2 (one trial
missed `cart_test.go`, one ran out of budget), so the baseline stands at 5/16.

**Decisions, by the rules above.** A ≥ baseline, so the working copy **stays on by
default**. B beats A by 5 trials at 1.24× the median tokens, so `models.agent.json`'s
`max_iterations` goes **8 → 16**. The median trial still makes 8 calls — the extra budget
is spent by the trials that need it, which is the shape a budget increase should have.

Per task, B vs baseline: fix_failing_test 2/2 (was 0/2), edge_case_bug 2/2 (0/2),
needs_iteration 1/2 (0/2) — the three tasks that need the test run against the change,
which only the working copy makes possible. feature_from_spec is 0/2 in every arm.

**What the traces show** (failing trials now log every call with its arguments). The agent
re-reads files it already has — the same file three times in one turn, `repo_map` twice —
and calls `sandbox_exec` as if it were a shell (`cat -n units/units.go && echo …`), which is
refused and costs a call. Two changes follow, measured next:

- **`sandbox_exec` says it is not a shell** — its argument was described as "the shell
  command to execute". Now it names the four programs it runs and says to use `read_file`.
- **The act nudge** (`daemon/actnudge.go`) — once, at half the turn's calls, a turn that
  was asked for a change and has neither edited nor run anything is told so, and told that
  what it read is already in front of it. Never in plan or check mode, never on a question.

## M1b — decision rule, written before the run

Arm **C**: working copy, 16 calls, the nudge and the `sandbox_exec` wording. Compared with
**B** (11/16, 44.9k median tokens). The **nudge stays only if C > B, or C = B at lower
median tokens**; otherwise it is removed. The `sandbox_exec` wording stays either way: its
old description was false.

**Result: C = 10/16 (45.8k median tokens) against B = 11/16 (44.9k). The nudge is
removed**, as the rule says. Within noise it did nothing measurable — and the traces show
one reason: it counted *running a command* as having acted, so a turn that ran `go test`
early and then re-read the same file four times without editing was never nudged. The
`sandbox_exec` wording stays.

## The model — the biggest lever (2026-09-27)

Same configuration as arm C (working copy, 16 calls), only the tier changed:

| tier | pass | median calls | median tokens | median seconds | ≈ $ / trial |
|---|---|---|---|---|---|
| `primary` — deepseek-v4-flash (arm C) | 10/16 (62%) | 8 | 45.8k | 297 | 0.003 |
| **`deepseek_v4_pro`** | **14/16 (88%)** | **4** | **17.6k** | **40** | **0.007** |
| `gemini_36_flash` | 14/16 (88%) | 9 | 31.3k | 25 | 0.03 |

Prices from OpenRouter's public model list on the day (flash $0.047/M in, v4-pro $0.348/M,
gemini $0.75/M). **`deepseek_v4_pro` passes 88% at a quarter of flash's calls and 7× the
speed, for about two and a half times the cost per task** — and about a quarter of
gemini's. Changing the default tier is a cost decision, so it is left to the owner.

`gemini_36_flash`'s two failures were one bug, since fixed (`c1abc3d`): it made a change
with `propose_edit`, then restated it as a SEARCH/REPLACE block in its answer, and the turn
offered both, so the function was added twice. Answer-text edits now go into the working
copy first, and a restatement of an edit already made is dropped. `deepseek_v4_pro`'s two
failures were turns that ended after 0–2 model calls with no edit; not yet explained.

## M3 — the spec arms (4 tasks with a `spec.md` × 2 trials)

| arm | pass | median tokens | median seconds |
|---|---|---|---|
| anchored — an ordinary turn (8 calls), spec active | 1/8 | 40.8k | 168 |
| build — `/spec build` (24 calls), spec active | 1/8 | 104.1k | 640 |
| *for reference:* M1 B on the same four tasks (16 calls, no spec) | 4/8 | — | — |

**Not earned, on this model.** `/spec build` ties the anchored turn at 2.5× the tokens, and
does worse than an ordinary 16-call turn with no spec at all. It stays what it already
was — an explicit, opt-in command — and is not recommended over a plain request yet.

**Why** — the traces are unambiguous, and it is not the build's own ceremony:
`update_tasks` took 1–4 calls a trial and `record_criterion` was never reached. What took
the budget was `read_file`: **10 to 18 reads per trial on projects of three or four
files**, the same files again and again. With `deepseek-v4-flash` on the price-sorted
zero-data-retention route, each model call took 25–50 s, so six of eight builds hit the
10-minute turn deadline before finishing. A longer, more structured turn gives a model
that re-reads more room to re-read.

The loop does not drop earlier tool results — everything read stays in the conversation
(checked: nothing trims them) — so the re-reading is the model's. That makes the model the
next variable: see the tier arms below.

---

## Decision rules for the next runs — written before them (all on `deepseek_v4_pro`)

The flash runs above were limited by the model; these repeat the open questions on the tier
that passes 88%, at the current code (no nudge, answer-text edits absorbed).

**M4 — the Planner that can look.** Arms: single agent, `researcher,coder` (what a bare
`/team` runs), `planner,coder`. `planner,coder` becomes what a bare `/team` runs **only if**
it passes strictly more trials than `researcher,coder` at no more than 1.5× its median
tokens. `/team` itself earns a recommendation over the single agent only if its better
shape passes strictly more trials than the single agent.

**M3 again — `/spec build`.** Arms: anchored (an ordinary turn with the spec active) and
build. `/spec build` is recommended over an ordinary turn with a spec **only if** it passes
strictly more trials at no more than 2.5× the median tokens; otherwise it stays opt-in and
unrecommended, as now.

### Result: VOID — the key's spending limit ran out mid-run

All five runs above stopped part-way: the provider began refusing every request with HTTP 403
"Key limit exceeded (total limit)". The key used for these evals has a **$1.00 total
limit**, and this day's runs spent it ($1.02). The trials that completed before that —
single 2/2, researcher→coder 1/1, planner→coder 2/2, build 0/3, anchored 0/0 — are far too
few to decide anything, so **no decision is taken: `/team` stays researcher→coder and
`/spec build` stays opt-in and unrecommended**, exactly as before. The rules above stand
for when the runs can be repeated.

Two product bugs came out of it, both fixed:

- the 403 read as "the configured API credentials were rejected — check the API key",
  sending the user to replace a key that was valid, only spent (`54b69c1`: it is now "your
  credit or spending limit is used up", and not retried);
- a provider reply with no text and no tool call ended the turn silently — the two
  unexplained `deepseek_v4_pro` failures above were exactly this (`b861b89`: asked once
  more; twice empty is reported as incomplete).

---

## Round 2 — which model is the default (rules written 2026-09-27, before the runs)

**Why a second round.** The owner prefers `deepseek-v4-flash` because it is cheap, and wants
`deepseek-v4-pro` as the default only if it is *really needed*. The eight tasks above cannot
say: both strong models are at or near their ceiling. Pro's two failures there were
empty replies, which are now retried (`b861b89`). Gemini's two failures were the
double-applied edit, now fixed (`c1abc3d`).

**What changed in the harness:**

- **Six harder tasks**, each proved by `TestTaskFixturesAreValid`. On the untouched
  project the hidden tests fail *on their own*, not only a side check. On the reference
  solution, applied through the edit path, everything passes.

  | task | what it asks |
  |---|---|
  | `bug_hunt_medium` | a 23-file shop library; the prompt gives only the symptom (a coupon ignored when typed in capitals); the cause is two packages away from where it shows |
  | `signature_change` | add a `ctx` parameter to `store.Get` and pass it through 8 call sites in 4 packages |
  | `multi_package_feature` | a priority field through model → file store → handler, with ordering and validation (has a `spec.md`) |
  | `semver_from_spec` | semantic-version comparison from a written spec: pre-release ordering, build metadata, numbers wider than 64 bits, 16 invalid forms (has a `spec.md`) |
  | `refactor_keep_behaviour` | pull three copies of table layout into one function; output byte-identical; the hidden test parses the code to check the copies are gone |
  | `npm_no_deps` | RFC 4180 quoted fields in a Node project, graded by `npm test` in the sandbox |

- **Cost is the provider's bill.** Each call's usage chunk gives its dollar cost, cached
  tokens, reasoning tokens and serving provider. On OpenRouter, the key's total usage is
  also read before and after each run. A race that could drop a trial's last call from
  the count is fixed, with a test that fails without the fix.
- **Per-tier settings, as the product sends them.** `reasoning_effort` and `provider_sort`
  on a tier in `models.json`, and `TASK_EVAL_REASONING` / `TASK_EVAL_PROVIDER_SORT` for a
  run.

**Budget:** $3.00 in total for Stages A–D, approved by the owner. Before each run the
key's remaining limit is checked. A run does not start unless its estimate plus a $0.50
reserve for the owner's own use fits.

**Stage A — what makes flash better** (14 tasks × 2 trials each):

- **F0** flash as it ships;
- **F-think** flash with `reasoning_effort: medium`;
- **F-fast** flash with `provider_sort: throughput`;
- **P** pro as it ships.

A setting joins **flash-best** if either:

- its arm passes strictly more trials than F0, at no more than 2× F0's measured cost per trial; or
- for F-fast only: it passes at least as many as F0, with median seconds at most half of
  F0's, at no more than 2× the cost.

If flash-best is a combination that has not been run, it is run fresh.

**Stage B — the default** (flash-best and P, taken to 4 trials per task, 56 each):

- **Pro becomes the default** if any one of these holds:
  - it passes **≥ 5 more trials of 56** than flash-best (about 9 points);
  - on some task, pro passes **≥ 3 of 4** where flash-best passes **≤ 1 of 4** (a class of
    task flash cannot do);
  - the two pass rates are within noise, but flash-best's median seconds are **more than
    3×** pro's (the owner's rule: cost first, but not at any speed).
- **Otherwise flash-best stays the default**, with its winning settings on the `primary` tier.

Cost per solved task and median seconds are reported either way.

**Stages C and D** repeat the M4 (`/team` shape) and M3 (`/spec build`) rules written
above, unchanged, on whichever model is the default after Stage B.

### Status: not yet run (2026-09-27)

Everything the runs need is built and committed:

- the six new tasks;
- cost read from the provider's bill;
- the per-tier settings;
- the rules above.

None of the runs has started. The key they would spend had used up its $1.00 limit
earlier the same day. Later that day it stopped authenticating (HTTP 401 from the key
endpoint). The runs start when a funded key is connected, within the $3.00 cap, in
stage order.

**Until then nothing changes:**

- the default stays `primary` (deepseek-v4-flash);
- `/team` stays researcher → coder;
- `/spec build` stays opt-in.

### Amendment before Stage B (2026-09-28)

The owner connected a new key and raised its limit to $3.00. Eval spending stops at
**$2.50**, keeping $0.50 for the owner's own use.

**What Stage A's pro arm showed.** It passed 21 of 28 trials for $0.47. Five of its
seven failures had nothing to do with the task:

- **The failure:** the turn ended after 2–3 calls on an empty reply, twice in a row.
- **The captured streams** (`TASK_EVAL_DUMP_EMPTY`) show the model deciding to call a
  tool. The bill counts 48–113 output tokens past its thinking.
- **The host,** DigitalOcean, delivered neither the call nor any text, and the retry
  went back to it.

`dc77b85` fixes this: the retry and the rest of the turn avoid the host that sent the
empty reply. No flash trial was served by that host.

**So Stage B runs both finalists fresh, 56 trials each (4 per task), on the fixed
code,** instead of adding 28 trials to Stage A's. Otherwise pro's number would mix
two versions of the loop. The rules above are unchanged. Stage A's numbers stay
reported as they are.

**Budget consequence:** Stage B costs about $1.20 of what is left. Stages C and D
run only if the remainder covers them; they are not squeezed into a smaller,
undecidable form.

### Stage A result (14 tasks × 2 trials, run in parallel, 2026-09-28)

| arm | pass | median s | median calls | $ / trial (billed) | budget stops |
|---|---|---|---|---|---|
| **P** — pro as shipped | **21/28 (75%)** | **46** | 6 | 0.0169 | 5 (all empty replies — see the amendment) |
| F0 — flash as shipped | 9/28 (32%) | 342 | 11 | 0.0049 | 16 |
| F-think — flash, `reasoning_effort: medium` | 10/28 (36%) | 412 | 11 | 0.0066 | 17 |
| F-fast — flash, `provider_sort: throughput` | 8/28 (29%) | 384 | 13 | 0.0060 | 18 |

**By the rule:**

- **F-think joins flash-best.** It passes strictly more than F0, 10 against 9, at 1.35×
  F0's cost, within the 2× limit. The margin is one trial, which is noise, but the rule was
  written first and it decides.
- **F-fast does not join.** It passes fewer trials than F0, and the faster host did not make
  the turn faster: it made more calls and took longer.

Flash-best is therefore flash with `reasoning_effort: medium`.

**What the flash trials show.** Flash's failures are the ones measured in round 1:

- a turn spends its 16 calls reading and running, and never edits (16–18 budget stops per
  arm);
- five of the six harder tasks are 0/2 in every flash arm.

Pro fails `bug_hunt_medium` too. It passes `semver_from_spec`, `signature_change` and
`refactor_keep_behaviour`.

### Stage B result: **pro becomes the default** (2026-09-28)

Both finalists ran on the fixed loop, 4 trials per task.

| task | pro | flash-best |
|---|---|---|
| create_file | 4/4 | 4/4 |
| fix_failing_test | 4/4 | 3/4 |
| add_function_with_test | 3/4 | 2/4 |
| two_edits_one_file | 4/4 | 4/4 |
| rename_across_files | 4/4 | 2/4 |
| edge_case_bug | 4/4 | 1/3 |
| feature_from_spec | 4/4 | 0/2 |
| needs_iteration | 4/4 | 0/2 |
| npm_no_deps | 3/4 | 0/2 |
| bug_hunt_medium | 1/4 | 0/1 |
| signature_change | 4/4 | 0/1 |
| multi_package_feature | 4/4 | 0/2 |
| semver_from_spec | 3/4 | 0/1 |
| refactor_keep_behaviour | 4/4 | — |
| **total** | **50/56 (89%)** | **16/34, at most 38/56** |
| median seconds | ~51 | 234 |
| billed $ per trial | ~0.022 | ~0.004 |

**How pro's 56 trials ran.** Pro's run reached its dollar cap after 50 trials, one of
them a transport error. With the owner's approval, the 7 missing graded trials were run
separately, on the same code and tier, and merged: 3 of `semver_from_spec` (2 passed)
and 4 of `refactor_keep_behaviour` (4 passed). None of pro's Stage B trials ended on an
empty reply; that failure was 5 of 28 in Stage A, before `dc77b85`.

**How flash-best ran.** It ran one trial at a time, at 2–8 minutes a trial.

- **Split for speed.** After 20 trials, its 9 remaining tasks were split into parallel
  runs with the same settings and code.
- **One extra trial.** One `edge_case_bug` trial finished in the sequential run before it
  was stopped. Trials were counted in time order, 4 per task, which is a rule about
  order, not outcome.
- **Stopped early.** The runs were stopped once the rule's outcome could no longer
  change. At that point flash-best could reach at most 38/56, even by passing every
  remaining trial.

**By the rule,** two clauses each decide it:

- pro passes at least 12 more trials of 56, and the rule needs 5;
- flash-best's median is 4.6× pro's, over the owner's 3× limit.

**`default_tier` is now `deepseek_v4_pro`** in `models.json` and `models.agent.json`.
Flash stays selectable with `/model primary`, at about a fifth of the cost.

**Spend.** Round 2 cost $2.64 of the approved $3.00, all on the owner's key.

**Stages C and D (`/team` shape, `/spec build`) did not run.** The $0.37 left on the key
cannot cover them on the new default. Until they run:

- `/team` stays researcher → coder;
- `/spec build` stays opt-in and unrecommended.

Their rules above still stand.
