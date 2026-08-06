# Enterprise QA Report — 2026-08-05

**Base SHA:** `8979530` · **Scope:** the ~3,500 uncommitted lines on top of it
**Roles:** CEO · CTO · Security Patcher · Security Improver · Tester
Evidence labels: **CONFIRMED** (reproduced by execution) · **PLAUSIBLE** (reasoned from source) · **NOT RUN**

---

## 1. Executive verdict — **FAIL, with four defects fixed**

The previous report ([`ENTERPRISE_QA_REPORT_2026-08-04.md`](ENTERPRISE_QA_REPORT_2026-08-04.md))
recorded **PASS** on SHA `8979530`, including "native Bubblewrap/Docker
subprocess sandbox isolation" as **CONFIRMED**.

That verdict was measured against a **committed SHA**, while ~3,500 lines of new
work sat uncommitted on top of it. Against the actual working tree:

| Gate | Prior report | Measured today |
|---|---|---|
| `gofmt` | clean | **FAILED** — 5 files, 2 clean at HEAD |
| tests | pass | **FAILED** — 2 TUI tests |
| ratchet | floors raised | **FAILED** — daemon −4.3, tui −1.1 |
| sandbox isolation | **CONFIRMED** | **never executed once** |

**The single most important sentence in this report:** the bubblewrap sandbox
passed an invalid flag and failed 100% of the time, and the test suite reported
`ok` throughout — because all six of its tests stub `lookPath` and assert on the
*argument list* rather than running bwrap.

**Ship rule: do not ship.** Two P0s are fixed; the ratchet is still red.

---

## 2. Defects found and fixed

### P0-1 — `sandbox_exec` exfiltrated the user's inference credentials
**CONFIRMED by execution.**

A Lane A builtin whose description told the approving human *"Runs in a
restricted sandbox with a 30s timeout."* It did neither of the things that
implies:

- It called `exec.CommandContext` directly. It never touched
  `daemon/mcp/sandbox.go`, the sandbox this same campaign had added.
- `cmd.Env` was never set, so the child inherited the daemon's **entire**
  environment — `OPENROUTER_API_KEY`, `CODETERMINAL_MOCHIII_KEY`,
  `CODETERMINAL_API_KEY`.

Its "strict whitelist for allowed binaries to prevent RCE sandbox escape" was
`go`, `npm`, `make`, `cargo` — every one of which runs project-supplied shell by
design. Reproduced with a two-line Makefile:

```
child saw: OPENROUTER_API_KEY=sk-or-v1-[REDACTED] MOCHIII=mochi_[REDACTED]
```

**The chain:** hostile repo → `sandbox_exec {"command":"make check"}` → one
human approval → the user's OpenRouter key leaves the machine. `ForbiddenEnvNames`
exists precisely to stop this for Lane B subprocesses; the daemon's own tool
walked around it.

**Fixed:** routed through `mcp.WrapCommand` (the real sandbox), `cmd.Env =
mcp.ServerEnv(nil)`, and a description that states what is and is not confined.
5 regression tests drive the real handler against a real hostile Makefile.

### P0-2 — the bubblewrap sandbox had never executed
**CONFIRMED by execution.**

`daemon/mcp/sandbox.go` passed `--nosuid`. That is a `mount(2)` option, not a
bwrap flag. Real bwrap (0.9.0):

```
bwrap: Unknown option --nosuid
```

It refuses the **entire invocation**, so every sandboxed Lane B server and every
sandboxed command failed before executing an instruction. Nothing was lost by
removing it: bubblewrap mounts its binds `MS_NOSUID` inherently.

**Why no test caught it:** all six sandbox tests stub `lookPath` and assert on
the constructed argument slice. Not one runs bwrap. Demonstrated by restoring
the flag:

| | broken sandbox |
|---|---|
| existing arg-list suite | **`ok`** |
| the 4 tests added today | **FAIL** — "bwrap rejected an argument WrapCommand generated" |

**Fixed:** flag removed; 4 tests added that execute bwrap and assert the
properties that actually matter — arguments accepted, command runs, **a file
outside the workspace is unreadable**, `--unshare-net` really removes the
network. All four pass; all four fail when the flag returns.

### P1-1 — language servers held the inference credentials
**CONFIRMED by source + the same execution evidence as P0-1.**

Found by sweeping every `exec.Cmd` in the repo for a nil `Env` — the defect
class P0-1 belongs to. `daemon/lsp_bridge.go:59` spawns `gopls`,
`typescript-language-server` or `pyright-langserver` with `cmd.Dir` set and
`cmd.Env` nil.

These are third-party binaries that read **project-supplied configuration**:
`tsconfig.json` can load plugins, `pyrightconfig.json` can name an interpreter.
A hostile repository therefore gets code execution inside a process holding the
user's keys.

**Fixed:** `cmd.Env = mcp.ServerEnv(lspToolchainEnv[cmdName])` — a per-language
allow-list of toolchain variables (`GOPATH`, `NODE_PATH`, `VIRTUAL_ENV`, …) with
`ForbiddenEnvNames` ungrantable. `NODE_OPTIONS` is deliberately excluded: it
accepts `--require`, which is a code-injection vector.

### P2-1 — slash commands could not be run in one keystroke
**CONFIRMED — this is what the two failing TUI tests were reporting.**

The `enter` key handler was a byte-for-byte copy of the `tab` handler, so
whenever the autocomplete popup had matches Enter *accepted a completion*
instead of submitting. Every no-argument command — `/help`, `/clear`, `/git`,
`/init`, `/context`, `/exit` — needed two Enters.

**Fixed:** Enter submits; Tab completes; Enter accepts a completion only after
the user has navigated the popup with the arrow keys.

### P2-2 — a nil `lspBridge` panicked the daemon
**CONFIRMED — found by a new test, which crashed with SIGSEGV.**

`main.go:294` sets the bridge, so production was safe — but there was **no nil
guard anywhere**, so any other `Server` construction reached `GetServer` on a nil
pointer. `handleConn`'s `recover()` would have contained it, at the cost of the
user's turn and a counted panic. **Fixed** in both call sites: it is now a tool
error.

---

## 3. What improved, measured

| | Before | After |
|---|---|---|
| `sandbox_exec` credentials visible to child | **all three keys** | none |
| `sandbox_exec` actually sandboxed | never | bwrap/docker when installed, stated when not |
| bwrap invocations that succeed | **0%** | 100% |
| Tests that execute a sandbox | **0** | 4 |
| LSP subprocess credentials | all three keys | none |
| `gofmt` | 5 files failing | clean |
| Test suite | 2 failing | passing |
| Tests in repo | 998 | **1,014** (+16) |
| `daemon` coverage | 69.3% | 70.6% |

**Confinement now proven, not asserted:** a file outside the workspace is
unreadable from inside the sandbox, and `--unshare-net` removes the network.
Both executed.

---

## 4. Still failing — the honest blocker

`make check` **exits 2**. `gofmt`, `vet`, `-race` and lint are clean; the
**ratchet is red**:

```
FAIL  codeterminal/daemon     — 70.6% below the 73.6% floor (down 3.0)
FAIL  codeterminal/clients/tui — 73.4% below the 74.5% floor (down 1.1)
```

Cause: **~561 lines shipped with zero test coverage.** `lsp_bridge.go` (7
functions), `watcher.go` (1), `mcp_ast_edit.go` (3), `mcp_lsp.go` (3) all
measured 0.0%. Today's tests closed `mcp_ast_edit.go`, `mcp_lsp.go` and
`mcp_exec.go`; **8 functions across `lsp_bridge.go` and `watcher.go` remain at
0%.**

The floors were raised by the previous campaign and **may only be raised**.
Closing this needs a fake stdio LSP server (the `testdata/badserver` pattern
applies directly) and an fsnotify test for the watcher. That is a scoped task,
not a judgement call.

**Not fixed, decision required — the TCP transport.** `protocol` gained a TCP
backend that activates whenever `HOST` is set. `daemon/main.go:1-3` still reads
*"It never listens on a network port."* Measured: `SO_PEERCRED` on a TCP socket
returns uid `4294967295` (UID_INVALID), so `authorizePeer` refuses — but that is
**defence by coincidence**, it is **NOT RUN** on macOS, and the switch is silent
and environment-triggered. Delete it, or gate it explicitly with a documented
auth story.

---

## 5. Process finding — why a PASS missed two P0s

Not carelessness. A measurement layer.

Every missed defect sat **one layer below where the test looked**: the sandbox
was tested at argument construction, not execution; `sandbox_exec` was reviewed
as a whitelist, not as a process with an environment; the LSP bridge was never
executed at all.

This is PART 0 rule 4 of the project's own discipline — *"a fix's test must
enter through the same door the user does"* — and it has now been learned three
times here (Tier 2's Fix 7, the launch-plan chunk-key bug, and this).

**Rule added to [`MASTER_PLAN_2026-08-05.md`](MASTER_PLAN_2026-08-05.md):** any
test that stubs a `lookPath`, `exec.Command` or filesystem seam must be paired
with one that does not, or the control it covers is recorded **NOT RUN** rather
than CONFIRMED.
