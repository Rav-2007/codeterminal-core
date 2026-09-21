# MCP Master — agent-mode readiness

**Front door for Mochiii MCP.** Start here; follow links for evidence.

| | |
|---|---|
| **Status** | **MERGED AND ON `main`.** Agent mode ships **off by default** (`mcp.enabled` unset ⇒ inert) |
| **Assessed** | 2026-08-03 at SHA `8979530`, when this read "MERGE-READY". That was ~76 commits ago; the numbers below are **of that date** and are not re-measured per commit |
| **Git SHA** | `89795309d3ea6631555d5593d4d923b5f843fa29` |
| **Lane B claim** | Consent + audit. **Not** confined. |
| **Whole-product QA** | [`ENTERPRISE_QA_MASTER.md`](ENTERPRISE_QA_MASTER.md) · [`ENTERPRISE_QA_REPORT_2026-08-03.md`](ENTERPRISE_QA_REPORT_2026-08-03.md) |

Evidence labels: **CONFIRMED** / **PLAUSIBLE** / **NOT RUN**.

---

## Doc map

| Doc | Role |
|---|---|
| **This file** | Merge readiness + quick re-runs |
| [`MCP_DEBUG_PLAYBOOK.md`](MCP_DEBUG_PLAYBOOK.md) | Symptom triage, role split, hermetic matrix, CTO gate detail |
| [`MCP_ROBUSTNESS_REPORT_2026-08-03.md`](MCP_ROBUSTNESS_REPORT_2026-08-03.md) | Flood 64 MiB, echo soak, orphan soak, interop numbers |
| [`MCP_LANE_B_THREAT_MODEL.md`](MCP_LANE_B_THREAT_MODEL.md) | M1–M8 walked against hostile peers |
| [`../SECURITY_MODEL.md`](../SECURITY_MODEL.md) | Canonical Lane A / Lane B security position |
| [`OPEN_ITEMS.md`](OPEN_ITEMS.md) | Engineering register (MCP pointer below) |
| [`AGENT_MODE_QA_GATE_2026-08-01.md`](AGENT_MODE_QA_GATE_2026-08-01.md) | Original agent-mode gate (historical verdict preserved) |

---

## Scorecard (this SHA)

| Dimension | Result | Where |
|---|---|---|
| Hermetic hostile matrix (M1–M7) | **PASS** | Playbook Evidence |
| Pre-consent DoS (M1a @ 64 MiB) | **PASS** | Robustness §1 — peak heap ~7.4 MiB |
| Resources (echo soak) | **PLATEAU** | Robustness §2 — stray 0 |
| Teardown under churn (orphan soak) | **PASS** | Robustness §2b — stray 0 |
| Interop (Node reference server) | **PASS** | Robustness §3 — handshake 3.2 s; M8 |
| Model-behaviour spend (menu curve) | **PASS** | Re-measured 2026-08-03: 100% / 94.3% / 100% @ 5/8/12 after run_tests description fix |
| Windows Job Object hardware | **NOT RUN** | Compile-verified; no Windows runner |

---

## CTO checklist (7)

| # | Criterion | Verdict |
|---|---|---|
| 1 | Polarity (off by default; `acknowledged_unconfined` required) | **PASS** |
| 2 | Hermetic hostile matrix green | **PASS** |
| 3 | No pre-consent unbounded alloc (stdout + stderr) | **PASS** |
| 4 | Teardown kills process group (no orphan privilege inheritance) | **PASS** (unit + orphan soak) |
| 5 | Honesty (Lane B unconfined in docs / PRs) | **PASS** |
| 6 | Residuals registered | **PASS** |
| 7 | Live interop + soak | **PASS** |

**Overall: PASS — MERGE-READY** against hermetic + live evidence on this SHA.

---

## Explicit non-claims

1. Lane B is an ordinary subprocess with the user's full privileges after approval.
2. A hostile *client* can approve on the user's behalf (digest ≠ human).
3. Prompt injection into the model is not solved (control chars stripped; prose is not).
4. Real-model D1/D2 (system-prompt delta / live `search_code` loop) remain **NOT RUN** unless separately budgeted.
5. Windows process-group backend is not hardware-proven in this pass.

---

## Quick re-run

```sh
# Hermetic hostile matrix
go test ./daemon/mcp/ -run 'Flooded|Hanging|ExitingMidCall|Teardown|Lying|Control|Injected' -count=1
go test ./daemon/ -run 'ToolName|ControlSequences|ForgedApproval|HungServers|ServerDying|PrefixWriterBounds|PrefixWriterStill' -count=1

# Amplification
MCP_FLOOD_MIB=64 go test ./daemon/mcp/ -run TestAFloodedToolListIsRefusedBeforeItIsAllocated -v -count=1

# Lane B soaks (zero model spend)
TURNS=50 PACE=0.2s SAMPLE_EVERY=1s MCP_SERVER=echo   scripts/agent-cost-bench.sh
TURNS=50 PACE=0.2s SAMPLE_EVERY=1s MCP_SERVER=orphan scripts/agent-cost-bench.sh

# Third-party interop (needs npx + network)
MCP_INTEROP=1 go test ./daemon/mcp/ -run TestInterop -v -count=1
```

After re-running on a new HEAD: update the robustness report SHA/date, then this status block.
