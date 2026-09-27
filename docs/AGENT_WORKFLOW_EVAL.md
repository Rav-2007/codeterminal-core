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

## M3 — the spec arms (4 tasks with a `spec.md` × 2 trials)

| arm | pass |
|---|---|
| anchored — an ordinary turn (8 calls), spec active | 1/8 |
| build — `/spec build`, spec active | *(running)* |
