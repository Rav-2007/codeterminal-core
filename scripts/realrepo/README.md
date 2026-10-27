# Real-repo eval: real bugs from real GitHub projects

`daemon/realrepo_eval_test.go` (`-tags eval`) runs the agent on real fixes from real repositories in C,
C++, Java and Python, SWE-bench style, and grades each with the fix's own tests. The scripts here
build the tasks, run arms of a round, and compare them. The data (clones, tasks, tools, results) lives
in `$REALREPO_HOME`, by default `~/.cache/mochiii-realrepo`, never in this repository. It also stays
out of `/tmp`, which a reboot empties.

Rounds and their pre-registered rules are in [`docs/AGENT_WORKFLOW_EVAL.md`](../../docs/AGENT_WORKFLOW_EVAL.md).

## Set the machine up once

The agent's commands run in a sandbox that mounts each program's own toolchain folder, read-only, as
found on the daemon's `PATH`. So put the toolchains in `$REALREPO_HOME/tools` and let `run_eval.py`
put them first on `PATH`:

- **Python**: a venv at `tools/py` with `pytest<9`, `cmake<4` and `simplejson` (marshmallow's tests import it).
  - pytest 9 fails collecting click's tests (`PytestRemovedIn10Warning` under `filterwarnings=error`).
  - CMake 4 refuses the `cmake_minimum_required` of 2018-era projects such as cJSON.
  - A `.pth` file in the venv that adds the current folder and `./src` makes each project importable
    as an editable install would.
- **Java**: a JDK and Maven at `tools/java`. The sandbox is read-only and offline, so `bin/mvn` is a
  wrapper that adds `-o -Dmaven.repo.local=/tmp/m2-head-<uid> -Dmaven.repo.local.tail=<ro m2>`.
  Fill that m2 once, online, by building each Java task with its fix applied.
  - A download that stalls (the JDK did, at 170 MB) resumes with `curl -C - --retry 5 --retry-all-errors`.
- **C and C++**: the system's `gcc`, `g++`, `make`.

## Build and prove the tasks

```
python3 -I build_tasks.py --validate
```

Each task is the repository at the commit before its fix, with a fresh one-commit history so the fix
cannot be looked up. It also holds the bug as a user reports it (`task.txt`), the fix's test changes
(`hidden.patch`, used only to grade) and `check.sh`. `--validate` proves each task fails as given
and passes with the real fix. Grading is SWE-bench style: the files the hidden tests change are put
back to the task's own version first, so an agent that edited those tests cannot stop them applying.

A task counts only if it is proven. For a round, at least half of the tasks should be fixes merged
after the models' training data (after 2026-06-01 for the models of Round 6). A famous fix can be
recalled rather than found: in the first run, two fixes came out identical to upstream.

## Run a round

Each arm runs from a Mochiii tree of its own (`REALREPO_SRC`). An arm of today's code is a checkout of
it. An arm of the code before one view -- Round 6's A -- is built from its commit, because the eval and
what it measures with came later:

```
./baseline_tree.sh <commit> ~/Neww-main
```

That is the commit as it was, plus this eval and the two files it measures with
(`daemon/turnevidence.go`, `daemon/shellline.go`), which nothing in the commit calls. Mochiii's record
under the answer stays out: that code wrote none. The script refuses a commit that already has one
view, and checks that the eval builds there.

Write an arms file, one arm a line: its name, then the environment it runs with (`run_round.sh`
says more). First a pilot, every arm once on one task, to measure $ per trial and how often the
provider says 429. That sets the cap, and whether the arms can share the provider at all:

```
REALREPO_MODEL=<provider model id> ./run_round.sh arms.txt 1 py-moreitertools-numeric-range
```

Then the round, every arm on every task, the arms in turn on each task:

```
REALREPO_MODEL=<provider model id> REALREPO_MAX_USD=<cap per arm run> ./run_round.sh arms.txt 3
```

And the comparison, by the metrics registered before the round ran:

```
python3 -I compare.py A B D
```

## Traps, each of which cost a run

- **Call the provider directly.** The request's form is chosen by its base URL (`providerdialect.go`),
  so a recording proxy in front of NVIDIA sent OpenRouter's `provider` field and every call came back
  400. Calls, tokens and cost are counted by the turn's own usage tally instead.
- **One session per model on a rate-limited key.** NVIDIA's free tier limits per model: eight MiniMax
  sessions at once had every task rate-limited within minutes. Different models may run side by side.
- **Keep the machine awake.** A closed lid suspended a run for 6.6 hours mid-task. `run_round.sh`
  holds sleep off with `systemd-inhibit`; the lid must stay open all the same. Go's monotonic clock
  leaves suspend out, so a "239 s" call that took an hour was asleep, not hung.
- **Freeze the harness.** Record the commit of this folder and of `daemon/realrepo_eval_test.go` in the
  round's pre-registration before the first trial. Four harness bugs found during one day voided the
  runs each touched.
- **A provider stop is not an error.** A rate limit or a stall that outlasts the turn's patience ends
  the turn with `IncompleteProviderError` and the work kept. The eval asks again without spending the
  agent's continues, and runs a trial the provider ended again from scratch (`REALREPO_PROVIDER_RETRIES`).
- **`mawk` buffers its input.** Watch a log with `sed -u`, not an `awk` prefixer.
- **Every arm by one measure.** The code before one view records no `run_command` result: it ran in
  the real project, and its exit code went only to the model. Yet once a turn's edits are accepted,
  the next turn's run there tests them. So the eval stands a wrapper in for the user's shell (bash or
  zsh, as `run_command` would pick; it runs that shell with the same arguments and `SHELL`) and reads
  each run's folder and exit code from its log. What changed in the real project outside review is
  read the same way in every arm: what git shows changed while a turn ran, before anything it offered
  was applied (build output the project ignores left out).
- **Stale bytecode.** A run in the real project leaves `.pyc` files, and a one-character fix applied
  in the same second keeps the source's size and time: Python then runs the old code. The grader keeps
  Python's bytecode apart (`PYTHONPYCACHEPREFIX`).
