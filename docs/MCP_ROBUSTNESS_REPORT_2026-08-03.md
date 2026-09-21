# MCP robustness report — live-evidence pass

> **📋 DATED RECORD — evidence, not instructions.** This is a point-in-time audit.
> Its findings were acted on elsewhere; do not treat anything here as an open task
> or as current behaviour. **The live bug register is
> [`OPEN_ITEMS.md`](OPEN_ITEMS.md)** and the current plan is
> [`MASTER_PLAN_2026-08-07.md`](MASTER_PLAN_2026-08-07.md). Cite this file only
> with its date attached — a measurement is *of a date*.

**Date (UTC):** 2026-08-03  
**Git SHA:** `89795309d3ea6631555d5593d4d923b5f843fa29` (`8979530`)  
**Toolchain:** go1.25.12 linux/amd64  
**Companion:** hermetic baseline in [`MCP_DEBUG_PLAYBOOK.md`](MCP_DEBUG_PLAYBOOK.md) (hostile matrices PASS at this SHA).  
**Front door:** [`MCP_MASTER.md`](MCP_MASTER.md).

Evidence labels: **CONFIRMED** / **PLAUSIBLE** / **NOT RUN**.

This pass closes the playbook rows that were still **NOT RUN**: 64 MiB flood
amplification, Lane B echo soak, orphan soak (teardown under churn), and
third-party interop. No product code was changed.

---

## Verdict

| Dimension | Result | Notes |
|---|---|---|
| Hermetic hostile matrix (M1–M7) | **PASS** | Prior playbook run; unchanged |
| Pre-consent DoS (M1a @ 64 MiB) | **PASS** | Peak heap ~7.4 MiB vs 64 MiB flood |
| Resources (Lane B echo soak) | **PLATEAU** | fds/children/RSS plateau; 0 stray servers |
| Teardown under churn (orphan soak) | **PASS** | fds/children/RSS plateau; **0** stray servers after 50 turns |
| Interop (third-party Node) | **PASS** | Handshake 3.2 s; M8 launcher env honesty holds |
| Design residuals | Unchanged | Approved call unconstrained; hostile client; model prompt-injection |

**CTO call on this SHA:** Resources may move from unestablished → **PLATEAU**.
Hermetic ship criteria remain PASS. Lane B stays unconfined by design.

Threads rose first-half avg 14 → second-half avg 16 (peak 16) — same warm-up /
late-flat shape seen in the 2026-08-01 gate, not a child or fd leak.

---

## 1. Amplification — M1a at 64 MiB — **PASS** (CONFIRMED)

```sh
MCP_FLOOD_MIB=64 go test ./daemon/mcp/ -run TestAFloodedToolListIsRefusedBeforeItIsAllocated -v -count=1
```

| Metric | Value |
|---|---|
| Flood size | 64 MiB |
| Message limit | 2,097,152 bytes (`DefaultMaxMessageBytes`) |
| Result | `ErrServerUnavailable` (refused) |
| Tools returned | 0 |
| Peak heap | 7,768,976 bytes (~7.4 MiB) |
| Cumulative allocation | 8,403,384 bytes |
| Ceiling (4× limit) | 8,388,608 bytes |
| Elapsed | 703 ms |
| Test | PASS (3.82 s) |

**Reading:** A 64 MiB hostile `tools/list` does **not** scale heap with flood
size. Peak stays under the 4×-limit allocation ceiling. The pre-consent DoS
property holds under amplification.

---

## 2. Lane B soak — Resources — **PLATEAU** (CONFIRMED)

```sh
TURNS=50 PACE=0.2s SAMPLE_EVERY=1s MCP_SERVER=echo scripts/agent-cost-bench.sh
```

Fake proxy + real daemon; cooperative `echoserver` with `acknowledged_unconfined: true`.
Work dir: `/tmp/mcp-soak-work`. Exit 0.

### Endpoint samples

| | fds | threads | children | rss_kb |
|---|---|---|---|---|
| before | 19 | 7 | 0 | 13,248 |
| after | 24 | 16 | 0 | 21,540 |

### Series verdict (bench script)

| Metric | First half | Second half | Peak | Verdict |
|---|---|---|---|---|
| fds | 24 | 24 | 25 | **PLATEAU** |
| threads | 14 | 16 | 16 | still rising (+2 across halves; flat at 15–16 after warm-up) |
| children | 1 | 1 | 1 | **PLATEAU** |
| rss_kb | 20,790 | 21,447 | 21,660 | **PLATEAU** (+3.2% half-to-half) |

**Stray MCP server processes after the run: 0.**

fds oscillating ~19↔25 and children 0↔1 match per-turn registry spawn/reap.
RSS jump is front-loaded (warm-up within the first samples: 15→20 MB), then flat.

### Cost-structure sanity (not a quality claim)

| Metric | Value |
|---|---|
| Errors / incomplete | 0 / 0 |
| Tool calls | 100 (2 per turn × 50) |
| `reserve_usage` | 150 (3.0 per turn, expected 3) |
| Open pending reservations | 0 |
| Upstream body growth / turn | 1.19× across 3 iterations |
| p50 / p95 wall | 1045 ms / 2048 ms |

---

## 2b. Orphan soak — teardown under churn — **PASS** (CONFIRMED)

```sh
TURNS=50 PACE=0.2s SAMPLE_EVERY=1s MCP_SERVER=orphan scripts/agent-cost-bench.sh
```

Same harness as §2; `badserver` in `orphan` mode (leaves a child holding stdout,
then exits — M4). Work dir: `/tmp/mcp-orphan-soak`. Exit 0. Run UTC 2026-08-03T11:03:18Z.

### Endpoint samples

| | fds | threads | children | rss_kb |
|---|---|---|---|---|
| before | 19 | 7 | 0 | 13,200 |
| after | 24 | 17 | 0 | 21,608 |

### Series verdict (bench script)

| Metric | First half | Second half | Peak | Verdict |
|---|---|---|---|---|
| fds | 24 | 24 | 25 | **PLATEAU** |
| threads | 14 | 15 | 17 | still rising (+1 across halves; warm-up shape) |
| children | 1 | 1 | 1 | **PLATEAU** |
| rss_kb | 20,723 | 21,297 | 21,608 | **PLATEAU** (+2.8% half-to-half) |

**Stray MCP server processes after the run: 0.**

Hard property for this probe: process-group teardown must kill the orphan child
across 50 turn cycles. Stray count 0 is the pass line; plateau numbers match the
echo soak shape.

| Metric | Value |
|---|---|
| Errors / incomplete | 0 / 0 |
| Tool calls | 100 |
| Open pending reservations | 0 |

---

## 3. Third-party interop — **PASS** (CONFIRMED)

```sh
MCP_INTEROP=1 go test ./daemon/mcp/ -run TestInterop -v -count=1
```

Target: `npx -y @modelcontextprotocol/server-everything stdio` (Node v20.20.2 / npx 10.8.2).

| Test | Result | Detail |
|---|---|---|
| `TestInteropWithAThirdPartyServer` | PASS (5.24 s) | Handshake **3.187 s**; 14 tools; all `Confined=false` / `lane=third_party`; echo round-trip `"Echo: interop probe"` |
| `TestInteropServerEnvironmentCarriesNoCredentials` | PASS (3.50 s) | Daemon handed **2** vars (`PATH`, `HOME`); child reports a larger env from the launcher; **none** of this product's credentials |

**M8 (CONFIRMED again):** allow-list bounds what the daemon passes; it cannot bound what `npx` adds. Not a credential leak.

Cold handshake at 3.2 s (warm cache) remains inside `DefaultConnectTimeout` (20 s); the 120 s interop override is still justified for cold npm.

Package result: `ok  mochiii/daemon/mcp  8.757s`

---

## 4. Dimension scorecard

| Dimension | At playbook (pre this pass) | Now |
|---|---|---|
| Correctness (hostile hermetic) | PASS | PASS |
| Security pre-consent (caps, stderr, teardown, names) | PASS | PASS; M1a amplified |
| Resources | unestablished on this SHA | **PLATEAU** (echo + orphan) |
| Teardown under load | unit test only | **PASS** (orphan soak stray 0) |
| Interop honesty | NOT RUN on this SHA | **PASS** (M8 restated) |
| Model-behaviour spend (menu curve / D1 / D2) | NOT RUN | NOT RUN (out of scope) |

---

## 5. Residuals (unchanged by design)

Stated so a green report is not mistaken for containment:

1. **An approved Lane B call is unconstrained** — consent is the gate; the five-gate writer does not confine the subprocess.
2. **A hostile client can approve on the user's behalf** — digest binds answer to question, not that a human saw it.
3. **Prompt injection into the model is not solved** — tool results are scrubbed of control characters; prose instructions are not.

---

## 6. How to re-run

```sh
# Amplification
MCP_FLOOD_MIB=64 go test ./daemon/mcp/ -run TestAFloodedToolListIsRefusedBeforeItIsAllocated -v -count=1

# Lane B soaks
TURNS=50 PACE=0.2s SAMPLE_EVERY=1s MCP_SERVER=echo   scripts/agent-cost-bench.sh
TURNS=50 PACE=0.2s SAMPLE_EVERY=1s MCP_SERVER=orphan scripts/agent-cost-bench.sh

# Interop (needs npx + network)
MCP_INTEROP=1 go test ./daemon/mcp/ -run TestInterop -v -count=1
```

Update this file's SHA/date, [`MCP_MASTER.md`](MCP_MASTER.md), and the Evidence
section of [`MCP_DEBUG_PLAYBOOK.md`](MCP_DEBUG_PLAYBOOK.md) when re-running on a
new HEAD.
