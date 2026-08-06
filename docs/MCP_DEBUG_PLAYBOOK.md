# MCP Server Debugging Playbook (QA + CTO + Security)

**Status 2026-08-03.** Operational playbook for CodeTerminal agent-mode MCP.
**Front door:** [`MCP_MASTER.md`](MCP_MASTER.md) (merge readiness + scorecard).
Threat model of record: [`MCP_LANE_B_THREAT_MODEL.md`](MCP_LANE_B_THREAT_MODEL.md).
Robustness numbers: [`MCP_ROBUSTNESS_REPORT_2026-08-03.md`](MCP_ROBUSTNESS_REPORT_2026-08-03.md).
Agent-mode QA gate: [`AGENT_MODE_QA_GATE_2026-08-01.md`](AGENT_MODE_QA_GATE_2026-08-01.md).
Open register: [`OPEN_ITEMS.md`](OPEN_ITEMS.md).

Evidence labels: **CONFIRMED** (script/run or read off the cited line),
**PLAUSIBLE** (reasoned from source), **NOT RUN** (honestly not attempted).

---

## Scope

This playbook covers **this repo’s agent-mode MCP surface** — not Cursor IDE
MCP client configs.

| Lane | What | Entry points |
|---|---|---|
| **A** (builtin, confined) | Go tools compiled into the daemon; five-gate writer on the propose path | [`daemon/mcpbuiltin.go`](../daemon/mcpbuiltin.go), [`daemon/mcp/registry.go`](../daemon/mcp/registry.go) |
| **B** (stdio, unconfined) | User-configured subprocesses; consent + audit only | [`daemon/mcp/stdioclient.go`](../daemon/mcp/stdioclient.go), [`daemon/mcpruntime.go`](../daemon/mcpruntime.go), [`daemon/mcpconfig.go`](../daemon/mcpconfig.go) |

Hostile peer: [`daemon/mcp/testdata/badserver`](../daemon/mcp/testdata/badserver/main.go).
Cooperative peer: [`daemon/mcp/testdata/echoserver`](../daemon/mcp/testdata/echoserver/main.go).

**Out of scope:** implementing servers under `mcp-servers/` (stub README), and
confining Lane B (explicitly not claimed).

```mermaid
flowchart TD
  symptom[Symptom] --> lane{Lane A or B?}
  lane -->|A| gates[Five-gate / builtin path]
  lane -->|B| phase{Failure phase}
  phase --> init[initialize / connect]
  phase --> list[tools/list]
  phase --> approve[approval prompt]
  phase --> call[tools/call]
  phase --> tear[teardown]
  init --> badserver[badserver mode]
  list --> badserver
  call --> badserver
  tear --> badserver
  badserver --> isolate[Package test vs daemon E2E]
  isolate --> patch[Security patch if pre-consent]
  patch --> neuter[Neuter-verify + gate]
```

---

## Role split

| Role | Owns | Stops when |
|---|---|---|
| **QA Tester** | Reproduce, classify phase, run hermetic + E2E matrices, label evidence | Every failing symptom maps to a phase + a named test or `NOT RUN` reason |
| **Security Patcher** | Pre-consent DoS / injection / env / teardown; refuse optimistic `Confined` | Fix is neuter-verified; stdout/stderr bounds hold; annotations never become gates |
| **CTO** | Merge/ship gate: consent invariants, budget honesty, residual register | Gate verdict with thresholds — no “looks fine” |

Nothing ships as “fixed” without a script that was run.

---

## Phase 1 — Triage tree (QA first)

Map the bug to **when** it happens (pre-consent vs post-consent).

| Symptom | Likely phase | First probe |
|---|---|---|
| Agent turn stalls before any tool prompt | `initialize` / connect | `hang-initialize`; check `connect_timeout_seconds` + turn clock in agent loop |
| Huge memory / OOM before approvals | `tools/list` flood | `flood-list`; expect refuse past `max_message_bytes` (2 MiB) |
| Tool runs but model gets truncated / stripped output | `tools/call` egress | `flood-call`, `ansi`; check [`daemon/toolresult.go`](../daemon/toolresult.go) |
| “Tool failed” vs “server gone” confusion | mid-call exit | `exit-midcall` → must surface `ErrServerUnavailable` |
| Zombie processes after turn | teardown | `orphan` → process-group kill ([`daemon/mcp/procgroup_unix.go`](../daemon/mcp/procgroup_unix.go)) |
| Approval UI shows escapes / forged allow | name / result injection | `ansi`, `inject`; names refused at ingest; forged JSON must not skip next ask |
| Server never starts / config rejected | config polarity | missing `acknowledged_unconfined`, missing `enabled`, unknown tool → ask/deny |
| Lane A write refused (secret/protected/path) | editapply gates (not Lane B) | [`editapply/secret.go`](../editapply/secret.go), [`editapply/protected.go`](../editapply/protected.go), [`editapply/pathhazard.go`](../editapply/pathhazard.go) |

**Isolate layer:**

1. Package seam: `go test ./daemon/mcp/ -run 'Flooded|Hanging|ExitingMidCall|Teardown|Lying|Control|Injected'`
2. Production path: `go test ./daemon/ -run 'ToolName|ControlSequences|ForgedApproval|HungServers|ServerDying'`
3. Only if both green and live broken: real config in `models.json` + `MCP_INTEROP=1` interop (network, not CI)

---

## Phase 2 — Hermetic QA matrix (must stay green)

```sh
go test ./daemon/mcp/ -run 'Flooded|Hanging|ExitingMidCall|Teardown|Lying|Control|Injected'
go test ./daemon/ -run 'ToolName|ControlSequences|ForgedApproval|HungServers|ServerDying'
MCP_FLOOD_MIB=64 go test ./daemon/mcp/ -run TestAFloodedToolList -v   # amplification spot-check
```

| ID | Property under test | Pass means |
|---|---|---|
| M1a/b | Message size caps | Flood refused at transport; no unbounded heap on list/call |
| M2 | Hung initialize | One connect timeout total, inside turn budget |
| M3 | Exit mid-call | `ErrServerUnavailable`, not bare EOF to the model |
| M4 | Orphan child | Process group killed on teardown |
| M5 | Lying `readOnlyHint` | `Confined` stays false; policy unchanged |
| M6/M6b | Control chars in name/output | Name refused; output stripped at egress |
| M7 | Forged approval in result | Next call still prompts |
| M8 | Launcher env (interop) | Daemon allow-list holds; document launcher-added vars |

**Config polarity (QA):** absent `mcp` / `enabled` unset / no ack → nothing runs.
Unlisted tool → `ask`. Forbidden env names never reach child (`mcp.ForbiddenEnvNames` /
`ServerEnv`).

**Soak (Resources):** 50-turn Lane B RSS plateau —

```sh
TURNS=50 PACE=0.2s SAMPLE_EVERY=1s MCP_SERVER=echo   scripts/agent-cost-bench.sh
TURNS=50 PACE=0.2s SAMPLE_EVERY=1s MCP_SERVER=orphan scripts/agent-cost-bench.sh
```

Re-measured 2026-08-03 on SHA `8979530` — echo **PLATEAU**; orphan stray **0**
(see [`MCP_ROBUSTNESS_REPORT_2026-08-03.md`](MCP_ROBUSTNESS_REPORT_2026-08-03.md) §2 / §2b).

---

## Phase 3 — Security patcher checklist

Work only what MCP owns; do not pretend Lane B is sandboxed.

**Pre-consent (highest priority — no human gate yet):**

- Caps: `max_message_bytes`, connect timeout, advertised-tool cap
- Stderr log writer bound (`prefixWriter` in [`daemon/mcpruntime.go`](../daemon/mcpruntime.go) — same shape as M1a on stderr; cap is `maxLogLineBytes` = 64 KiB, shared with helper)
- Process-group teardown on every Close path ([`daemon/mcp/procgroup.go`](../daemon/mcp/procgroup.go) + unix/windows backends)
- Tool-name sanitization before approval UI / logs

**Consent channel:**

- Digest-bound approval; tool-result JSON cannot forge allow
- Annotations (`readOnlyHint`, `destructive`) display-only — never lower policy or set `Confined`

**Credentials / env:**

- Child gets HOME, PATH + allow-listed names only from daemon; API keys in `ForbiddenEnvNames`
- M8 honesty: launchers (`npx`/`uvx`) may add their own vars — document, do not claim daemon bounds launcher

**Lane A adjacency** (if debugging “MCP wrote a secret”): that is the five-gate
writer + path hazards, not Lane B. Validate `editapply/` separately; Lane B writes
bypass those gates by design once approved.

**Patch rule:** every security fix gets a `badserver` mode or daemon E2E test, then
**neuter-verify** (break the fix, watch the test fail) — same discipline as
[`OPEN_ITEMS.md`](OPEN_ITEMS.md) §6.

---

## Phase 4 — CTO acceptance gate

Ship / merge agent-mode MCP changes only if:

1. **Polarity:** agent mode off by default; Lane B requires typed `acknowledged_unconfined: true`
2. **Hermetic hostile matrix green** (Phase 2 commands)
3. **No new pre-consent unbounded allocation** (stdout + stderr)
4. **Teardown kills the process group** (no orphan privilege inheritance)
5. **Honesty:** docs still say Lane B is unconfined; no PR language that implies sandbox
6. **Residuals registered** with severity + evidence — known design residuals stay explicit: approved call unconstrained; hostile *client* can approve; prompt-injection into the model is not solved
7. **Optional live:** `MCP_INTEROP=1` + Lane B soak before calling Resources “PASS”

**Fail the gate on:** any pre-consent DoS regression, consent bypass,
`Confined=true` for Lane B, or missing ack gate.

---

## Residual spot-check (stderr bound + process group)

### Stderr `prefixWriter` bound — **CONFIRMED** (code + test)

| Check | Where | Finding |
|---|---|---|
| Cap constant | `daemon/helperproc.go` `maxLogLineBytes = 64 << 10` (64 KiB); used by `mcpruntime.go` `prefixWriter` | Present |
| Flush at cap | `daemon/mcpruntime.go` `Write`: if `len(w.buf) >= maxLogLineBytes`, emit with `[continues]` and clear | Present |
| Index without copy | uses `bytes.IndexByte`, not `strings.IndexByte(string(w.buf), …)` | Present |
| Regression tests | `TestPrefixWriterBoundsANewlineFreeFlood`, `TestPrefixWriterStillBuffersPartialLines` in `daemon/mcp_misbehaviour_test.go` | See Evidence |

Item 11 in `OPEN_ITEMS.md` recorded the unbound writer; commit `ffdd553` closed it.
This spot-check re-confirms the bound is still on the path that hosts hostile stderr.

### Process-group teardown — **CONFIRMED** (code + test)

| Check | Where | Finding |
|---|---|---|
| Lifecycle | `daemon/mcp/procgroup.go` — prepare / adopt / killAll / release | Documented seam |
| POSIX | `procgroup_unix.go`: `Setpgid: true`, `killAll` → `SIGKILL(-pid)` with `pid <= 0` guard | Present |
| Windows | `procgroup_windows.go` Job Object backend | Present (hardware NOT RUN unless CI has Windows) |
| Property test | `TestTeardownReachesWhatTheServerSpawned` (`orphan` badserver mode) | See Evidence |

---

## Evidence — hermetic matrix run

**Date (UTC):** 2026-08-03T10:48:24Z
**Git SHA:** `89795309d3ea6631555d5593d4d923b5f843fa29` (`8979530 feat(windows): peer authentication…`)
**Toolchain:** go1.25.12 linux/amd64

### Package `./daemon/mcp/` — hostile peer — **PASS** (CONFIRMED)

Command:

```sh
go test ./daemon/mcp/ -run 'Flooded|Hanging|ExitingMidCall|Teardown|Lying|Control|Injected' -count=1
```

Result: `ok  codeterminal/daemon/mcp  8.169s`

| Test | Result | Maps to |
|---|---|---|
| `TestAFloodedToolListIsRefusedBeforeItIsAllocated` | PASS | M1a |
| `TestAFloodedToolResultIsRefused` | PASS | M1b |
| `TestHangingInitializeCostsTheConfiguredTimeout` | PASS | M2 |
| `TestServerExitingMidCall` | PASS | M3 |
| `TestTeardownReachesWhatTheServerSpawned` | PASS | M4 |
| `TestALyingAnnotationDoesNotBuyConfinement` | PASS | M5 |
| `TestControlSequencesArriveIntact` | PASS | adapter fidelity (egress is daemon-side) |
| `TestInjectedInstructionsArriveVerbatim` | PASS | adapter fidelity (consent is daemon-side) |
| `TestAToolNameWithControlCharactersIsNotAdvertised` | PASS | M6 |

### Package `./daemon/` — production path + stderr bound — **PASS** (CONFIRMED)

Command:

```sh
go test ./daemon/ -run 'ToolName|ControlSequences|ForgedApproval|HungServers|ServerDying|PrefixWriterBounds|PrefixWriterStill' -count=1
```

Result: `ok  codeterminal/daemon  3.156s`

| Test | Result | Maps to |
|---|---|---|
| `TestAServerSuppliedToolNameNeverReachesTheApprovalPrompt` | PASS | M6 (E2E) |
| `TestControlSequencesInToolOutputAreStripped` | PASS | M6b |
| `TestForgedApprovalInToolOutputIsNotConsent` | PASS | M7 |
| `TestHungServersCostOneTimeoutNotOnePerServer` | PASS | M2 (multi-server) |
| `TestAServerDyingMidCallIsReportedAsAnUnavailableServer` | PASS | M3 (E2E) |
| `TestPrefixWriterBoundsANewlineFreeFlood` | PASS | stderr bound (item 11) |
| `TestPrefixWriterStillBuffersPartialLines` | PASS | stderr ordinary path |

### Amplification / interop / soak

Full tables: [`MCP_ROBUSTNESS_REPORT_2026-08-03.md`](MCP_ROBUSTNESS_REPORT_2026-08-03.md) (same SHA).

| Check | Status |
|---|---|
| `MCP_FLOOD_MIB=64` flood amplification | **PASS** (CONFIRMED) — peak heap 7.4 MiB vs 64 MiB flood; under 4×-limit ceiling |
| `MCP_INTEROP=1` reference Node server | **PASS** (CONFIRMED) — handshake 3.187 s; 14 tools; echo OK; M8 launcher env restated |
| 50-turn Lane B echo soak | **PLATEAU** (CONFIRMED) — fds/children/RSS plateau; stray servers 0 |
| 50-turn Lane B orphan soak | **PASS** (CONFIRMED) — stray servers 0 under M4 churn; fds/children/RSS plateau |

---

## CTO gate verdict (this pass)

| # | Criterion | Verdict | Evidence |
|---|---|---|---|
| 1 | Polarity (off by default; ack required) | **PASS** | `mcpconfig.go` polarity rule — CONFIRMED by reading |
| 2 | Hermetic hostile matrix green | **PASS** | Both packages ok at SHA `8979530`, 2026-08-03T10:48:24Z |
| 3 | No pre-consent unbounded alloc (stdout + stderr) | **PASS** | M1a @ 64 MiB + hermetic M1b + PrefixWriter tests |
| 4 | Teardown kills process group | **PASS** | Teardown test + echo/orphan soak stray 0 |
| 5 | Honesty (Lane B unconfined) | **PASS** | `MCP_LANE_B_THREAT_MODEL.md`, `SECURITY_MODEL.md` |
| 6 | Residuals registered | **PASS** | Design residuals in threat model + robustness report §5 |
| 7 | Live interop + soak | **PASS** | Robustness report — echo **PLATEAU**, orphan stray 0, interop green |

**Overall: PASS** — hermetic ship criteria, Resources **PLATEAU**, orphan teardown under churn green, interop green.
See [`MCP_MASTER.md`](MCP_MASTER.md) for the merge-ready front door.
Lane B remains unconfined by design; see robustness report residuals.

---

## How to use this on a live bug

1. Classify with the triage tree → name the phase and a `badserver` mode.
2. Add or re-run the failing test at the right layer (package vs daemon E2E).
3. Patch; neuter-verify; update the threat-model row if a property moves.
4. Re-run Phase 2 commands; update Evidence + CTO gate in this file.
