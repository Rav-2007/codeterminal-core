# The spec workflow

Decide what to build, build it to that, and check the result against it — with the
agent doing the work and you approving every change.

```
            ┌────────────── /spec <goal> ──────────────┐
            │  SPECIFICATION  (specs/<name>.md)         │
            │  ☑ Behaviour  ☑ Scope  ☑ Edge cases       │
            │  ☑ Success criteria  (- [ ] C1: … check:) │
            └───────────────────┬───────────────────────┘
       spec-first               │ spec-anchored            spec-as-source
   (you accept the spec)   every prompt carries it    /spec build: tasks → tests → code
                                │
                        IMPLEMENTATION  (in a private working copy; you review the diff)
                                │
                  stays connected: /spec check grades every criterion
```

| Diagram | Command | What happens |
|---|---|---|
| **Spec-first** | `/spec add a --verbose flag` | The agent reads the project (it cannot change, run or reach anything) and writes **one** file, `specs/<name>.md`: Goal, Behaviour, Scope (in/out), Edge cases, Success criteria, Tasks. You review it like any edit; accepting it makes it the active spec. |
| **Spec-anchored** | `/spec use <name>` · `/spec off` · `/spec show` | The active spec is read from `specs/` (only there, at most 32 KB) and put in the system message of **every** turn, fenced so its text cannot pose as instructions from you. The header shows `spec: <name>`. It is remembered per project. |
| **Spec-as-source** | `/spec build` | One agent turn, in this order: plan the tasks (a live checklist on screen), write the tests for the success criteria **first** and run them, implement, re-run until they pass, tick the finished tasks in the spec, record a verdict for every criterion. Run it again to continue from the first unticked task. |
| **Stays connected** | `/spec check` | For each criterion: `met`, `unmet` or `unknown`, with evidence. **"met" is the daemon's word, not the model's** — it is kept only with evidence the daemon verifies (a command that passed in this turn, or a `file:line` that exists), otherwise it becomes `unknown` and says why. The spec's checkboxes follow the verdicts, through a reviewed edit. Commands run in a throwaway copy, so a check changes nothing. |

## The working copy — why the agent can test its own work

Every agent turn that edits or runs something works in a **private copy** of the
project (`.gitignore` respected; `.git` and other protected folders left out;
dependency folders such as `node_modules` linked, not copied). Its edits land there,
`read_file` shows them, and `sandbox_exec` builds and tests them — so a second edit can
build on the first, and "run the tests, fix what fails" happens inside one turn.

**Your files do not change until you review.** When the turn ends, the copy's net
difference from your project becomes ordinary edit proposals, and you accept or reject
each one exactly as before. Above the review you see whether the change was ever built
or tested:

```
✓ checked: go test ./... passed          ✗ checked: go test ./... FAILED
                                           --- FAIL: TestVerbose …
! checked: go test ./... passed, and the changes went on after it; the version offered was not run
not checked: the agent did not build or test these changes
not offered: out/app: created by a command, not offered
not offered: main.go: changed on disk while the agent worked, so its version is not offered (it would undo yours)
```

A check vouches only for what it ran against. If the agent changed anything after its
last command (with a tool, or by writing an edit into its answer), the line says so
instead of showing a tick.

### Approving commands during a build

A build runs the tests, fixes, and runs them again, often over several `/spec build`
turns. When the agent asks to run a command, you have three ways to say yes:

| key (TUI) · button (VS Code) | covers |
|---|---|
| `y` · **Yes** | this one call |
| `a` · **Yes for this task** | the same command again, until this turn ends |
| `s` · **Yes while this spec is active** | the same command again, in this turn and later ones, until you `/spec off`, switch spec, or quit |

`s` appears only when a spec is active and the command runs inside a real sandbox, in the
private working copy, so what it runs cannot change your files. It covers that one exact
command: a different command still asks.

The grant is kept in the client's memory only. It is never written to disk, and the
daemon keeps nothing between turns. `/spec show` lists what you have approved this way.
One thing it does not stop: like any `sandbox_exec` command, it can reach the network,
because builds fetch dependencies.

Plan mode and `/spec` (writing a spec) have no working copy — they run nothing.
`mcp.no_working_copy: true` switches it off; a project over 50 000 files or 500 MB falls
back to the old behaviour and says so.

## A spec file

```markdown
# Verbose flag

## Goal
Print each file as it is processed, so a long run shows progress.

## Behaviour
`tool -v <dir>` prints one line per file, `processing <path>`, before the summary.

## Scope
**In:** the -v flag, its help text. **Out:** log levels, colours.

## Edge cases
- An empty directory prints the summary only.

## Success criteria
- [ ] C1: -v prints one line per file -- check: go test ./cmd -run TestVerbose
- [ ] C2: without -v the output is unchanged -- check: go test ./cmd -run TestQuiet
- [ ] C3: `tool -h` documents -v -- check: go test ./cmd -run TestHelp

## Tasks
- [ ] write TestVerbose, TestQuiet, TestHelp
- [ ] add the flag and the per-file line
- [ ] update the help text
```

Criteria are the lines `- [ ] C<n>: …` — `/spec check` and `/spec build` find them by ID.

## How well it works — measured, not asserted

Every step of this workflow is graded by the task-success eval: small projects,
hidden tests the model never sees, every proposed edit applied as if you pressed `y`.
Results and the decision rules written before each run are in
[AGENT_WORKFLOW_EVAL.md](AGENT_WORKFLOW_EVAL.md).

**`/spec build` against an ordinary turn with the spec active** (2026-09-28,
`deepseek_v4_pro`, the 6 tasks that have a spec, 2 trials each):

| | passed | median tokens | median seconds |
|---|---|---|---|
| ordinary turn, spec active | **12/12** | 46.3k | 96 |
| `/spec build` | 10/12 | 68.2k | 132 |

So **an ordinary turn with the spec active is the recommendation.** `/spec build` stays
available and opt-in; it earns a recommendation only by passing strictly more trials than
an ordinary turn, which it did not.

Both of its misses were on one task:

- in one trial the model got an edge case in the spec wrong;
- in the other, the turn ended right after it wrote the tests, on a reply that said
  "Now I'll implement it" with that step still open on its own task list.
