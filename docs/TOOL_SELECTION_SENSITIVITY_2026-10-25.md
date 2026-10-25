# Which tool-description changes can an eval see? — 2026-10-25

**Answer: almost none, on this model. The tool's NAME decides which tool is
picked. A degraded description moved the score only when it removed a
capability the name does not imply** — run_command's "install packages (pip,
npm, cargo, brew...)". Vague descriptions, whole descriptions replaced with two
words, and the line that tells grep from search_code all moved nothing
measurable.

This is the measurement behind `daemon/prodmenu_eval_test.go`, the
production-menu selection eval, and the reason its header says a green run is
not a safety proof. Reproduce with:

```sh
go test -tags eval -run TestProductionMenuSelection -v -timeout 90m ./daemon
```

---

## Method

- **Model:** `nvidia/nemotron-3-ultra-550b-a55b`, through NVIDIA's API
  (`integrate.api.nvidia.com`), the model the connected key had chosen. No
  provider routing (that field is OpenRouter's).
- **Request:** the real daemon's first request of an ordinary agent turn,
  captured from the binary by a loopback recorder: the 10,006-byte system
  message (system.txt, the working-copy note, the WHAT YOU CAN REACH list, the
  setup section, the date) and the shipped menu. Only the user message was
  swapped per case.
- **Menus:** *unindexed* — 14 tools, no `search_code`, grep's description
  without it. *Indexed* — 15 tools, `search_code` added and grep's description
  naming it. In this preview the indexed menu was reconstructed from the two
  source strings (`daemon/grep.go`, `daemon/mcpbuiltin.go`); the eval now builds
  it through `builtinTools`.
- **Arms:** each degradation ran against the intact menu, 5 trials an arm,
  same prompts. A degradation is a text replacement in one tool's description.
- **Scored:** the first tool the model called. Transport errors (HTTP 429, one
  DNS failure) are excluded from both numerator and denominator, never counted
  as misses.

## The matrix

| # | Degradation | Menu | Cases | Intact | Degraded |
|---|---|---|---|---|---|
| 1 | grep: remove "search_code finds code by meaning, grep finds all of it exactly" (and the sentence it ends) | indexed | every_use, rename_prep | 10/10 | 9/9 |
|   | ditto | indexed | by_meaning (wants search_code) | 4/5 | 2/4 |
| 2 | grep: remove "Use it to find where a name is defined or used, where an error message comes from, or a string to change everywhere." | unindexed | exact_text, error_origin, type_defined | 15/15 | 15/15 |
| 3 | find_files: replace the whole description with "Project helper." | unindexed | file_by_name, glob, dockerfile | 14/14 | 13/13 |
| 4 | read_file: replace the whole description with "Does things with files." | unindexed | read_range | 5/5 | 4/4 |
| 5 | sandbox_exec: remove the confinement sentence and the private-copy sentence | unindexed | sandboxed_tests | 5/5 | 5/5 |
| 6 | read_file / list_directory / propose_edit: remove the sentence that says absolute and ~/ paths reach outside the project | unindexed | outside_read, outside_read_abs, outside_list, home_create | 19/19 | 18/18 |
| 7 | **run_command: remove "install packages (pip, npm, cargo, brew...), run scripts, use git, start a build."** | unindexed | **pip** | **8/10** | **1/7** |
|   | ditto | unindexed | git_log | 9/9 | 4/4 |
|   | ditto | unindexed | npm_add | 0/5 | 0/5 |

Row 7's pip figures are two runs pooled (4/5 → 0/3, the second arm cut short by
a DNS failure, and 4/5 → 1/4). npm_add is not a selection miss in either arm:
the model looks for `package.json` first (`find_files` 4/5), which is
defensible, so it was dropped from the eval.

## How to read it

- **The name decides.** `find_files` with the description "Project helper."
  was still chosen 13 times of 13 for "where is the file called ratelimit.go".
  A tool named `grep` is chosen for "every line containing X" whatever its
  description says.
- **Restated text is redundant text.** Row 6 removed the outside-path sentence
  from three tools and nothing moved. The system message's WHAT YOU CAN REACH
  list says the same thing ("READ files and list folders ANYWHERE on the
  machine ... give read_file or list_directory an absolute or ~/ path"), so
  the model still had it.
- **What moved is a capability the name hides.** Nothing in the name
  `run_command` says it installs packages, and `sandbox_exec` is also a way to
  run things. Without the line, "Install the Python package requests" went to
  `list_directory`, `repo_map` and `find_files` — looking around instead of
  acting.
- **Look-first is not a miss.** Most below-100% scores on the intact menu were
  the model inspecting before acting: `read_file` before an edit,
  `list_directory` before a delete, `find_files` for `package.json` before an
  npm install. Those are defensible, and cases that measure them were left out
  rather than scored as wrong.

## What it means for the eval

A case can show a change only if its own degradation is proven to move it.
That makes **one** prompt able to detect a change, on this model: pip, against
run_command's capability line — proven on both menus (see the runs below). Every other case is in the eval with its
pairing, is re-tested each run, and is reported NOT PROVEN until a run proves
otherwise. A change to a tool whose cases are NOT PROVEN cannot be judged by
this eval: it will be green whether the change is safe or not.

## The eval's own runs

Same model and key, 2026-10-25, through `daemon/prodmenu_eval_test.go` at
the commit that added it. Transport errors are retried (4 retries, 10 s apart)
and excluded from scores.

### The acceptance experiment: the control case, unindexed/pip

Every run is listed, including the two that did not pass.

| Run | run_command's install line | Intact | Degraded | p | Verdict |
|---|---|---|---|---|---|
| 1 (10 an arm, 2 retries 5 s apart) | present | 10/10 | 2/8 | 0.0015 | **voided**: 2 of 10 degraded trials lost to HTTP 429, over the 10% biased-sample ceiling |
| 2 (10 an arm) | present | 7/10 | 4/10 | 0.18 | **failed**: control NOT PROVEN |
| 3 (20 an arm) | present | 13/20 | 3/20 | 0.0015 | PROVEN, pass |
| 4 (20) | **removed from `daemon/runcommand.go`** | **3/20** | not run (pairing stale) | — | **failed**: BELOW FLOOR and STALE |
| 5 (20) | restored byte for byte (blob `2f38e5b`, = HEAD) | **16/20** | 6/20 | 0.0018 | PROVEN, pass |
| full run (20) | present | 15/20 | 3/20 | 0.0002 | PROVEN |

Runs 1 and 2 are why the control now runs at least 20 trials an arm
(`prodMenuControlTrials`) and why the retries are wider: at an intact rate near
80% and a degraded rate near 25%, ten trials an arm misses the effect a good
share of the time, and run 2 did. Pooled over runs 2–5 and the full run with the
line present: intact 51/70 (73%) and degraded 16/70 (23%) against the
degradation; with the line removed from source, 3/20 (15%).

Run 4 is the change a real edit would make, not the eval's own degradation:
the clause was deleted from the Go source and the eval run against the rebuilt
menu. It failed twice over, as it should: the score fell below the floor, and
the pairing reported that the text it degrades is gone. The offline check
(`TestEveryProdMenuCaseIsPairedWithTextThatExists`, in `make check`) failed on
the same edit with no key and no network.

### The full run: 23 cases, both menus, both arms

Unindexed menu: 14 tools, digest `e62d2c95…`. Indexed: 15 tools (`search_code`
added), digest `104528a2…`. System message 9,920 bytes on both (it carries the
workspace path, so its length moves with it). 8 trials an arm, the control 20.
92 minutes; 216 retried attempts, all recovered.

| | Unindexed | Indexed |
|---|---|---|
| Baseline, intact arms | **95/100 (95.0%)** | **90/96 (93.8%)** |
| Proven sensitive | pip (15/20 → 3/20, p=0.0002) | none |
| Not proven | git_log, file_by_name, glob, read_range, sandboxed_tests, outside_read, outside_list, home_create, error_origin, type_defined — each 8/8 in both arms | git_log, file_by_name, glob, read_range, sandboxed_tests, outside_read, outside_list, home_create, every_use, rename_prep (8/8 both arms); by_meaning 7/8 both arms; pip 3/8 → 1/8, p=0.28 |
| Below floor | — | **pip: 3/8** (list_directory 5) |

**indexed/pip fell below its floor, and the run failed on it.** Its 50% floor
was carried from the unindexed measurement and had never been measured on the
indexed menu; on that menu the model looked around first (`list_directory`)
five times in eight. Eight trials cannot say whether the indexed menu really
shifts this prompt toward looking first or whether this was a bad draw: the
unindexed arm of the same prompt scored 15/20 in the same run. The floor was
not lowered to pass the run. Deciding it needs the indexed case run at the
control's 20 trials.

### indexed/pip, settled at 20 trials an arm

| Run | Intact | Degraded | p | |
|---|---|---|---|---|
| indexed/pip, 20 an arm | **15/20** | **3/20** | **0.0002** | PROVEN sensitive |

The 3/8 was a short arm's bad draw: at twenty trials the indexed menu scores
exactly what the unindexed one did in the full run (15/20), and its paired
degradation moves it just as far. So the case is **proven sensitive** on both
menus, and its floor is now **measured on its own menu** rather than carried.

**The floor is 0.54, not the measured 75%, and that is a choice to state.**
0.54 is the one-sided 95% Clopper–Pearson lower bound of 15/20, rounded down —
derived from this one measurement by a fixed rule, before any later run. A
floor at the point estimate would fail an unchanged run about 38% of the time
at 20 trials (32% at 8), which would make it a gate that fails for a reason
unrelated to any change. At 0.54 an unchanged run fails about 1.4% of the
time, and a run with run_command's install line removed (true rate near 3/20)
passes under 0.01% of the time. Both pip cases now run at least 20 trials an
arm (`prodMenuSensitiveTrials`).

## Not measured

- Any model but nemotron-3-ultra-550b-a55b. On a model that reads
  descriptions more closely, more cases may prove sensitive; the eval
  re-tests every pairing on every run for that reason.
- Schema changes (a property added or removed) as opposed to descriptions.
- Turns with history, a mode, retrieved context in the user message, or an
  added MCP server.
- Tokens, as opposed to choices.
