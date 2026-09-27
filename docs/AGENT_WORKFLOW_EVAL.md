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
