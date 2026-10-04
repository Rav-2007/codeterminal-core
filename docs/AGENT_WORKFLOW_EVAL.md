# Agent workflow eval — did the agent get the job done?

The yardstick for the agent-workflow plan (working copy → specs → build from spec →
grounded planner). One number, graded without a model judge:

> **Task success** — the share of trials where, after applying every edit the agent
> proposed (as a user pressing `y` on each), the task's *hidden* tests pass.

Harness: [`daemon/tasksuccess_eval_test.go`](../daemon/tasksuccess_eval_test.go)
(`-tags eval`). Small projects in [`daemon/testdata/tasks/`](../daemon/testdata/tasks/):
14 in the default set, and the 6 of Round 4's long set. Each has a prompt, hidden tests
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
Round 4 adds `TASK_EVAL_SET=long`, `TASK_EVAL_LONG=<mode>` (with
`TASK_EVAL_TASK_CALLS`, `_MINUTES` and `_USD`) and `TASK_EVAL_CONTINUES`.

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

### Stages C and D — amendment before they run (2026-09-28)

The owner raised the key's limit to $7.00 for these two stages. Hard cap: **$3.90**
across both, split into per-arm dollar caps. All arms run on the new default,
`deepseek_v4_pro`, in parallel.

**Stage C (`/team` shape, M4 rules unchanged)** runs 14 tasks × 2 trials each for
`researcher,coder` and `planner,coder`.

The single-agent arm is **not re-run**. It is Stage B's pro trials: the first two graded
trials of each task, in time order. That is the same code, model and harness, and the
choice depends only on order. It scored **23/28**, median 36.3k tokens. It was computed
and written here before either `/team` arm ran.

**Stage D (`/spec build`, M3 rules unchanged)** runs the 6 tasks that have a `spec.md`,
2 trials each, as two arms:

- **anchored:** an ordinary turn with the spec active;
- **build:** `/spec build`.

### Stage C result: bare `/team` stays `researcher → coder`, and `/team` is recommended by one trial (2026-09-28)

| task | single agent | researcher → coder | planner → coder |
|---|---|---|---|
| create_file | 2/2 | 2/2 | 2/2 |
| fix_failing_test | 2/2 | 2/2 | 1/2 |
| add_function_with_test | 1/2 | 2/2 | 2/2 |
| two_edits_one_file | 2/2 | 2/2 | 2/2 |
| rename_across_files | 2/2 | 2/2 | 2/2 |
| edge_case_bug | 2/2 | 2/2 | 2/2 |
| feature_from_spec | 2/2 | 2/2 | 1/2 |
| needs_iteration | 2/2 | 2/2 | 2/2 |
| npm_no_deps | 1/2 | 2/2 | 0/2 |
| bug_hunt_medium | 0/2 | 0/2 | 1/2 |
| signature_change | 2/2 | 2/2 | 2/2 |
| multi_package_feature | 2/2 | 2/2 | 2/2 |
| semver_from_spec | 1/2 | 1/2 | 2/2 |
| refactor_keep_behaviour | 2/2 | 1/2 | 1/2 |
| **total** | **23/28** | **24/28** | **22/28** |
| median tokens | 36.3k | 53.2k | 58.5k |
| median seconds | 63 | 148 | 122 |
| billed $ per solved task | 0.030 | 0.050 | 0.047 |
| trials that ran any command | 26/28 | **0/28** | **0/28** |

**By the rule:**

- **`planner,coder` does not replace `researcher,coder`.** It passes fewer trials, 22
  against 24. Its tokens are inside the limit (58.5k against 1.5 × 53.2k = 79.8k), but the
  rule needs both clauses. `teamPipeline` is unchanged.
- **`/team` earns the recommendation over the single agent.** Its better shape passes
  strictly more trials, 24 against 23. The margin is one trial, which is noise, as it was
  for F-think in Stage A, but the rule was written first and it decides. The price is 1.5×
  the tokens, 2.3× the median time and 1.7× the cost per solved task.

**What the failures show: the Coder cannot run anything.** `roleCoder`
([`daemon/roles.go`](../daemon/roles.go)) has read and edit tools and no `sandbox_exec`.
Only the Tester runs commands, and it reports rather than fixes.

- **No commands at all.** Neither `/team` arm ran a command in any of its 56 trials. The
  single agent ran one in 26 of 28.
- **`refactor_keep_behaviour`:** both `/team` failures are compile errors that `go build`
  shows: `formatTable redeclared`, and `undefined: formatTable`.
- **Planner → coder:** three failures (`npm_no_deps` ×2, `feature_from_spec`) end with the
  Coder saying it could not run the tests, then asserting that they pass.

**The other `/team` misses:**

- **`fix_failing_test` (planner → coder).** The Coder wrote its fix as a SEARCH/REPLACE
  block in its answer, not through `propose_edit`. The search text is the body of both
  `Max` and `Min`, so the edit was refused as ambiguous. That refusal is correct, and the
  turn was already over, so nothing asked again.
- **`semver_from_spec` (researcher → coder).** The trial hit the call limit before writing
  the test the task asked for.
- **`bug_hunt_medium`** is the hardest task. It fails in every arm, except one planner →
  coder trial.

**The next candidate, not built:** give the Coder `sandbox_exec`, confined and approved as
for the single agent. Under this harness it becomes what `/team` runs only if it passes
strictly more trials than today's `researcher → coder`.

### Stage D result: `/spec build` stays opt-in and unrecommended (2026-09-28)

| task (has a `spec.md`) | anchored (spec active) | `/spec build` |
|---|---|---|
| add_function_with_test | 2/2 | 2/2 |
| edge_case_bug | 2/2 | 2/2 |
| feature_from_spec | 2/2 | **0/2** |
| needs_iteration | 2/2 | 2/2 |
| multi_package_feature | 2/2 | 2/2 |
| semver_from_spec | 2/2 | 2/2 |
| **total** | **12/12** | **10/12** |
| median tokens | 46.3k | 68.2k |
| median seconds | 96 | 132 |
| billed $ per solved task | 0.036 | 0.064 |

**By the rule:** build passes fewer trials than anchored, 10 against 12. Its tokens are
inside the limit (68.2k against 2.5 × 46.3k = 115.8k), but the rule needs both clauses.
`/spec build` stays opt-in, and an ordinary turn with the spec active remains the
recommendation.

**Both misses are `feature_from_spec`:**

- **Trial 1.** It implemented `Slugify("Version 2.0")` as `"version-2-0"`, where the spec
  says `"version-20"`. Its own tests did not cover that case.
- **Trial 2 ended one step early.** It wrote the tests, ran them, and replied: "tests
  compile but fail because `Slugify` doesn't exist yet. Now I'll implement it." That reply
  had no tool call, so the turn ended, with "implement" still open on its own
  `update_tasks` list. A reply with no tool call ends a turn in build mode, as everywhere.

**A gap, not built:** build mode could send such a reply back once while `update_tasks` has
open items. It would not change this verdict: had trial 2 passed, build would be 11/12
against 12/12.

### How Stages C and D ran

- **Setup.** All four arms ran in parallel on `deepseek_v4_pro` at `2c88cfe`, 2 trials
  per task.
- **The laptop slept three times** during the runs: 10:13–10:48, 11:22–11:53, and
  11:57–17:54 (the last was a lid close).
  - Go's timers run on the monotonic clock, which stops while suspended, so the recorded
    seconds leave the sleeps out.
  - The two Stage C trials in flight across a sleep resumed and passed.
  - No trial in any arm ended on a transport error.
- **Budget stops:** 2 in each Stage C arm and 1 in each Stage D arm. All were the per-turn
  call limit, not time. Three of the six trials still passed.
- **Spend.**
  - **Per-call bills:** $3.29 in all:

    | arm | billed |
    |---|---|
    | researcher → coder | $1.19 |
    | planner → coder | $1.03 |
    | anchored | $0.44 |
    | build | $0.64 |

  - **The key:** usage rose $3.35 (from $2.71 to $6.06), inside the $3.90 cap. That is
    $0.06 more than the bills add up to. The key's figure is the ground truth.
  - **Per-arm caps** are checked before each trial, so an arm can pass its cap by one
    trial. Anchored did, $0.44 against $0.40.
- **Round 2 in all:** $2.64 (Stages A and B) + $3.35 = **$5.99**, all on the owner's key.

The raw summaries are the four `R2 Stage C` / `R2 Stage D` lines in
[`agent_workflow_eval.jsonl`](agent_workflow_eval.jsonl).

## Round 3 — the `/team` phases share their opening (rules written 2026-09-30, before the run)

**What changes.**

- **The same opening in every phase:** the same system message (the base prompt plus one
  paragraph about the pipeline), the same tool list (every tool any phase may use), and
  the same history.
  - Each phase's own instructions open its own message, in a `<step_role>` block.
  - The user's words are fenced in `<user_request>`.
  - Earlier phases' findings follow, as reference, defused like other untrusted text.
  - What a phase may *call* is still its own role's list, refused at dispatch.
- **The first empty reply goes back to the same host,** with a one-line note. Only a
  second empty reply in a row moves the turn to another host; a third ends it.

**Why.**

- **Before** (2 trials at `55cc8d7`, $0.05 on the key): the Coder's first call on
  DigitalOcean got 1,280 of 3,582 prompt tokens from the cache, the base prompt only.
  Every later Coder call got 3,300–4,900.
- **One empty reply was captured** and its exact request replayed to DigitalOcean four
  times. Three came back as ordinary tool calls, one empty again (73 output tokens billed,
  nothing delivered). The drop is intermittent, so leaving the host at the first one threw
  its cache away for nothing.

**The arm:**

- `researcher,coder` on `deepseek_v4_pro`, 14 tasks × 2 trials, as Stage C;
- two shards in parallel, `TASK_EVAL_MAX_USD` 0.65 each ($1.30 in all);
- empty replies dumped.

**Rules.**

- **Quality:** the change stays at **23/28 or more**, the single agent's Stage C score.
  Stage C's `researcher,coder` passed 24/28, and one trial either way is noise. At
  **22/28 or fewer**, the phase layout is reverted.
- **Mechanism:** over trials whose Coder opened on DigitalOcean, the median cached share of
  its first call must be above the before-arm's 36% (1,280 of 3,582). Otherwise the layout
  is reverted even if quality holds: it would carry the risk for nothing.
- **Waste:** refused calls to a tool outside a phase's role are reported. They are what
  offering every phase the pipeline's tools can cost.
- **Cost per trial** is reported against Stage C's $0.0425 as context only. Other changes
  since then also cut cost: each step now echoes only its own words, and a stuck loop stops.
- **The same-host retry** stays unless a trial ends on three empty replies in a row.

### Round 3 result: the shared opening stays, and so does the same-host retry (2026-09-30)

Run at `5d19d3a` as ruled above: $1.00 billed per the usage chunks. The key moved $0.9986,
read before and after both shards. Each shard's own "key delta" in the JSONL includes the
other shard, because they ran at the same time.

| task | Stage C researcher → coder | Round 3 |
|---|---|---|
| create_file | 2/2 | 2/2 |
| fix_failing_test | 2/2 | 2/2 |
| add_function_with_test | 2/2 | 2/2 |
| two_edits_one_file | 2/2 | 2/2 |
| rename_across_files | 2/2 | 2/2 |
| edge_case_bug | 2/2 | 2/2 |
| feature_from_spec | 2/2 | 2/2 |
| needs_iteration | 2/2 | 2/2 |
| npm_no_deps | 2/2 | 2/2 |
| bug_hunt_medium | 0/2 | 1/2 |
| signature_change | 2/2 | 2/2 |
| multi_package_feature | 2/2 | 2/2 |
| semver_from_spec | 1/2 | 1/2 |
| refactor_keep_behaviour | 1/2 | 2/2 |
| **total** | **24/28** | **26/28** |
| median tokens | 53.2k | 56.1k |
| median seconds | 148 | 131 |
| billed $ per trial | 0.0425 | 0.0357 |
| billed $ per solved task | 0.050 | 0.038 |
| Coder's first call cached on DigitalOcean (median) | 36% (before arm, 1 trial) | **77%** (26 trials) |

**By the rules:**

- **Quality holds: 26/28,** above the keep line of 23/28. The two misses:
  - **`bug_hunt_medium` trial 1.** The Coder fixed the coupon bug but left the invoice test
    failing. It said it could not run the tests: the Coder still has no `sandbox_exec`, and
    no trial ran a command. Trial 2 passed; Stage C's `researcher → coder` failed both.
  - **`semver_from_spec` trial 2** ended in a transport error, counted here as a failure.
    See "One stream cut" below.
- **The mechanism works.** The median cached share of the Coder's first call on
  DigitalOcean was 77% across 26 trials (range 58–96%), against 36% before. 25 of the 26
  cached exactly 3,072 tokens: the shared system message and tool list, in whole 256-token
  blocks.
  - **One caveat.** The before figure came from the first trial of its run, when nothing was
    cached yet. Most Round 3 trials ran right after another trial, and the cache carries
    across trials: the Researcher's first call was a median 92% cached. The old layout would
    have had some of that warmth too.
  - **The cold start is the clean comparison, and it holds.** Shard B's first trial was
    `fix_failing_test`, the same task and position as the before arm's first trial, and its
    Researcher's first call found only 256 tokens cached.
    - Before, that Coder opened at 1,280 of 3,582.
    - Now it opened at 3,072 of 3,826, reusing what its own Researcher had cached seconds
      earlier.
    - A user's `/team` question usually starts cold, so this is the case that matters.
  - Over all calls, 73.4% of tokens were cached, against 67.2% in Stage C.
- **Waste: 3 refused calls in 28 trials,** all by the Coder, in two trials that both passed.
  - The Researcher's tools are a subset of the Coder's, so the Coder's menu is unchanged.
    These calls named a tool that was in no phase's list.
  - The Researcher is the only phase now offered more than before, and it made no refused
    calls.
  - A refused call is not in the trial's tool list, so the eval has the count but not the
    tool's name.
- **Cost: $0.0357 per trial,** against Stage C's $0.0425 (−16%), for about the same tokens.
  This is context only: other changes since Stage C also cut cost.
- **The same-host retry stays.** DigitalOcean sent 25 empty replies. Each was reasoning that
  ended with `finish_reason: stop` and no text or tool call.
  - After the 19 first empties, OpenRouter sent 18 retries back to DigitalOcean and 1 to
    SiliconFlow. 12 of those 18 succeeded, keeping the host and its cache.
  - The other 6 were empty again and moved on, and all 6 succeeded elsewhere.
  - No trial saw three in a row. 12 of 28 trials still used more than one host.

**One stream cut, not changed here.**

- **What happened.** `semver_from_spec` trial 2's Coder reasoned for five minutes without
  answering: 39,046 characters, mostly test cases. The daemon's own per-request limit
  (`requestTimeout`, 5 minutes, [`daemon/provider.go`](../daemon/provider.go)) cut the stream
  at exactly 300 s, measured from the first chunk to the dump.
- **Why it was not retried.** The attempt alone had used up the 45 s retry budget, and only
  a silent stall gets a retry past it. So the turn ended with "provider unreachable".
- **A candidate for a later round.** Give a request that reaches the 5-minute limit, after
  long reasoning and no answer, the one retry a stall gets. It is a new behaviour, so it
  needs its own measurement.

---

## Round 4 — long tasks against a patient user (rules written 2026-10-01, before the run)

**What is measured.** Long tasks (`/debug`, `/fix`, `/refactor` and `/task`, built in
`9547bcd`–`194f265`) are compared with today's single agent. The question is whether they
get more multi-step jobs done than the single agent with a patient user typing "continue",
and at what price.

**The tasks** (`TASK_EVAL_SET=long`): five new fixtures plus `bug_hunt_medium`, the hardest
of Round 2.

| task | what it takes | reference fix | mode in arm B |
|---|---|---|---|
| `multi_bug_hunt` | three unrelated bugs in three packages; the existing tests pass, so they do not show them | 3 files | `debug` |
| `regression_from_history` | a boundary the last commit broke, in a real two-commit git history (`prepare.sh` builds it) | 1 line | `debug` |
| `shared_root_cause` | five failing tests in five packages, one cause in a sixth, and a comment that invites a wrong fix | 1 file | `fix` |
| `bug_hunt_medium` | a coupon honoured in one checkout path and not another | 1 file | `fix` |
| `cross_package_refactor` | a type renamed, and a function replaced by one that takes a context, across six packages | 9 files, 19 hunks | `refactor` |
| `feature_many_edits` | a currency threaded through four packages, with a new error and changed signatures | 7 files, 18 hunks | `task` |

- **Every fixture passes the validity contract** of `TestTaskFixturesAreValid` (no model,
  no key):
  - the untouched project fails;
  - the hidden tests fail on their own;
  - the reference solution, applied through the real edit path, passes.
- **Each arm-B mode is the one a user would pick from the task's words,** chosen before the
  run:
  - `debug` where the cause has to be found from symptoms or history;
  - `fix` where the report hands over a failing case;
  - `refactor` and `task` for the refactor and the feature.
- **`hunt` is not measured.** It produces findings, and no fixture grades findings.

**The arms.**

- **Common setup.** Both arms run on `deepseek_v4_pro` with the shipped
  `models.agent.json`, and `search_code` off as in every round (a fixture has no index).
  2 trials per task, so 12 per arm.
- **A: the single agent and a patient user** (`TASK_EVAL_CONTINUES=5`).
  - Each turn is an ordinary turn. The eval accepts its edits, then types "continue", up to
    5 times, while the turn was cut short by a limit or the project's own tests fail.
  - It never looks at the hidden tests. Each new turn carries the conversation as text, as
    the product does.
- **B: a long task** (`TASK_EVAL_LONG=<mode>`). One request on the shipped task budget:
  - 30 minutes, $0.50 and 150 model calls;
  - segments of 20 calls;
  - commands time out at 300 s instead of 30;
  - the finish gate needs a passing test run after the last edit.
- **Both arms are graded the same way.** Every edit a trial ends with is applied, as a user
  pressing `y`, then the hidden tests run.
- **What differs is the product.** B gets the long-task menu (`grep`, `git_history`,
  `checkpoint`, `investigate`, `rename_symbol`, ranged reads), A the ordinary one. That
  difference is what is being compared.

**Rules.**

- **B is recommended** only if it passes **strictly more** trials than A, at **no more than
  1.5×** A's billed $ per solved task. A lead of one trial meets the rule, and is reported
  as within noise.
- **`/debug`, `/fix` and `/refactor` stay long tasks whatever the result.** They are the
  commands for a complete task, by the owner's decision (see the amendment below). Round 4
  decides what may be claimed for them, not whether they stay.
- **If B is recommended,** the README says so, with the numbers.
- **If B is not recommended,** the README says plainly that long tasks have not yet been
  shown to beat continuing by hand. Every task B lost is diagnosed from its saved ledger,
  and the cause is fixed before the round runs again.
- **Defects, whatever the totals.** Each of these is fixed before any recommendation. The
  round's numbers stand as measured.
  - A B trial billed more than its $0.50 budget plus one call: the budget check failed.
  - A B trial that ended `stuck` or `blocked` while its ledger shows the work going right:
    the stuck rule or the finish gate stopped a trial it should not have.
- **Reported, not ruled:**
  - the pass count per task;
  - median calls, seconds and $;
  - the turns A used;
  - B's segments and how its trials ended (`finished`, `budget`, `stuck`, `blocked`);
  - the B trials that ended `finished` with the hidden tests failing. This shows what the
    gate's passing run is worth.
- **VOID:** an arm that stops on its $ cap before all 12 trials, or more than 10% transport
  errors (the harness's own rule). A void round decides nothing.

**Spend.**

- **Expected:** about $4–8.
- **Caps:**
  - `TASK_EVAL_MAX_USD` 2.50 for each of A's two shards.
  - For B, $0.50 per trial in each shard. Only a broken budget check can reach that.
  - That is $11 at most, plus one trial's overshoot per shard. The cap is checked before
    each trial starts.
- **Before the run,** check the key's `limit_remaining`.

**How to run.** Six shards run in parallel, with `MOCHIII_API_BASE` and `MOCHIII_API_KEY`
set as above. Keep the machine awake: a closed lid suspends the shards.

```
export PATH=$HOME/.local/go/bin:$PATH
run() { env TASK_EVAL_SET=long TASK_EVAL_TRIALS=2 TASK_EVAL_OUT=$PWD/docs/agent_workflow_eval.jsonl "$@" \
  go test -tags eval -count=1 -run 'TestTaskSuccess$' -v -timeout 480m ./daemon; }
# A
run TASK_EVAL_CONTINUES=5 TASK_EVAL_MAX_USD=2.50 TASK_EVAL_LABEL=r4-A1 \
  TASK_EVAL_ONLY=multi_bug_hunt,regression_from_history,shared_root_cause
run TASK_EVAL_CONTINUES=5 TASK_EVAL_MAX_USD=2.50 TASK_EVAL_LABEL=r4-A2 \
  TASK_EVAL_ONLY=bug_hunt_medium,cross_package_refactor,feature_many_edits
# B
run TASK_EVAL_LONG=debug    TASK_EVAL_MAX_USD=2.00 TASK_EVAL_LABEL=r4-B-debug \
  TASK_EVAL_ONLY=multi_bug_hunt,regression_from_history
run TASK_EVAL_LONG=fix      TASK_EVAL_MAX_USD=2.00 TASK_EVAL_LABEL=r4-B-fix \
  TASK_EVAL_ONLY=shared_root_cause,bug_hunt_medium
run TASK_EVAL_LONG=refactor TASK_EVAL_MAX_USD=1.00 TASK_EVAL_LABEL=r4-B-refactor \
  TASK_EVAL_ONLY=cross_package_refactor
run TASK_EVAL_LONG=task     TASK_EVAL_MAX_USD=1.00 TASK_EVAL_LABEL=r4-B-task \
  TASK_EVAL_ONLY=feature_many_edits
```

**Dry run (no key, no spend).** Both arms ran on `regression_from_history` against a
scripted loopback model.

- **B** read the file, edited it, ran a real `go test ./...` in the sandbox, and called
  `finish_task`, which the gate accepted; its summary is the reply. That is 4 calls, it
  ended `finished`, and it passed. It took 5 calls until the call that asked for the
  summary a second time was removed (2026-10-01, before any paid run).
- **A** had its turn cut to 2 calls. The eval typed "continue" once, and turn 2 fixed the
  bug: 2 turns, and it passed.
- **The dry run caught one defect before any spend.** The refactor check (no old names
  left) counted the backups under `.mochiii/` and failed the reference solution. It now uses
  `noGoFileContains`'s walk, which skips them; the M0 baseline had learned this once
  already.

This proves the harness works, not that a model can do the tasks.

### Amendment before the run (2026-10-01)

The owner decided that `/debug`, `/fix` and `/refactor` are long-task commands by design:
each is for a complete task, worked through to a verified finish.

- **Withdrawn:** as first written, a loss in Round 4 would have turned these three back
  into one-line hints on an ordinary turn.
- **Unchanged:** the comparison, the recommendation rule, the defect rules, the arms and
  the caps.
- **What a loss now does:** it limits what the README may claim, and starts a diagnosis
  instead of a revert.

### Status: not yet run (2026-10-01)

The key has $0.74 left of its $8 limit, and the run needs about $4–8.

### Amendment before the run (2026-10-04): what $7.72 buys

Written before any paid call of this campaign. The key had $7.72 left, shared
with the owner's daily use, so about $0.75 stays untouched and the rest is spent
in order of value per dollar.

1. **Reliability checks, about $0.50 together, run one at a time** with the
   key read before and after each: `TestToolCallReliability`,
   `TestToolMenuSizeCurve`, `TestAgentLoopReliability`, `TestOrchestrationLive`.
   Each judged by its own gates, as written in its file.
2. **Single-agent regression: the 14 Round-2 tasks x 2 trials** on
   `deepseek_v4_pro`, `TASK_EVAL_MAX_USD=0.90`. Stage B measured 50/56 (89%),
   which predicts 25/28. **A regression is flagged if 21/28 or fewer pass,**
   and every failing trial is diagnosed from its saved log.
3. **Round 4 at 1 trial per task per arm, not 2** -- 6 trials per arm. Arm A
   in one shard capped at $1.20; arm B with $0.50 per trial in each shard, the
   long task's own budget, so only a broken budget check reaches a cap. The
   rules, arms and defect checks are unchanged. With 6 trials per arm a lead of
   one or two is within noise and is reported that way; an arm that stops on
   its cap before 6 trials is VOID.
4. **`/team` is not re-run** unless money is left after step 3: it measured
   26/28 on 2026-09-30, and its orchestration code has not changed since.

### Results (2026-10-04)

All on the tree at `e607d24` and the shipped configs, billed from the owner's key
(which was read before and after each step).

**1. Reliability checks.**

| check | model | result | billed |
|---|---|---|---|
| `TestToolCallReliability`, 84 trials | deepseek-v4-flash | 4 of 4 gates pass: well-formed 100%, schema-valid 98.6%, right tool 98.6%, clean finish 98.8% | $0.003 |
| `TestToolMenuSizeCurve`, 105 calls | deepseek-v4-flash | 94.3% at 5 tools, 91.4% at 8, 97.1% at 12 | $0.004 |
| `TestAgentLoopReliability`, 30 turns | deepseek-v4-flash | **4 of 4 gates fail**: terminates 90.0% (95), no repeated call 76.7% (95), uses tool output 88.5% (90), within four iterations 76.7% (80) | about $0.10 |

The loop check reads `models.json`, whose primary tier is flash, so it measures
flash running the agent loop: a model a user only gets in agent mode by choosing
it, since agent mode has defaulted to `deepseek_v4_pro` since 2026-09-28. On
2026-07-31 the same check passed all four gates on the same model. Five of the 30
turns repeated an identical call and three ran out of steps while exploring; the
built-in tool set has grown a great deal since July, which that report names as
the reason to re-measure. Whether pro shows the same pattern was not measured --
the task-eval logs carry no daemon log lines to count repeats from.

**2. Single-agent regression, 14 tasks x 2 on `deepseek_v4_pro`: 26/28 (93%)**,
against 25/28 predicted from Stage B's 89%. No regression (the flag was 21 or
fewer). Both misses are `bug_hunt_medium`: the agent made coupon codes
case-insensitive and did not trim surrounding spaces, which the hidden test
types (`" SAVE10 "`, `"save10\t"`); the reference fix does both. A judgment miss,
on the task that was also the hardest in Round 2. Billed $0.42; median 6-9 calls
and 15-36 s per task.

**3. Round 4, one trial per task per arm.**

| arm | solved | billed | per solved task |
|---|---|---|---|
| A: single agent, up to 5 "continue"s | 4 of 6 | $0.13 | $0.033 |
| B: long tasks | 3 of 6 | $0.19 | $0.063 |

**B is not recommended:** the rule asks for strictly more passes. At six trials
an arm, a difference of one is within noise. No B trial came near its $0.50
budget (the dearest was $0.07).

**The defect rule fired.** Two B trials -- `/debug` on `multi_bug_hunt` and
`/refactor` on `cross_package_refactor` -- ended `stuck` after two model calls
and no tool call at all. Both replies were a sentence of intent ("I'll start by
understanding the project structure...") and nothing else, and two segments
without progress is the stuck rule. **Both trials were served entirely by the
DigitalOcean host** (2 of 2 calls each); every other B trial was served mostly or
entirely by Parasail, and none of the 28 single-agent trials reached
DigitalOcean. That is the Round 2 failure again -- a host that returns the text
before a tool call and drops the call. `dc77b85` retries an *empty* reply on
another host; these replies are not empty, so nothing retried them, and a long
task reads a text-only segment as unfinished work. By this round's rule the cause
is fixed before Round 4 runs again. Two fixes are on the table, and both are the
owner's call: exclude DigitalOcean for pro in `models.agent.json`'s provider
settings, or treat a long-task segment with no tool call as a dropped call --
retry it once on another host before it counts toward "stuck".

**4. `/team`** was not re-run: Round 3 measured it four days earlier and its
orchestration has not changed.
