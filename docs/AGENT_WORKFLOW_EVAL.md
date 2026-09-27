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
