# Documentation index

**44 markdown files, ~16600 lines. This page says which four you need and what
every other one is for.**

Written 2026-08-07, because the seven stale claims corrected that day were not
seven mistakes — they were one missing page. There was no way to tell a live plan
from a superseded one without opening it, so every session opened all of them,
and the ones it skipped are the ones that went stale.

---

## Start here — four documents, in this order

| # | Read | For |
|---|---|---|
| 1 | [`HANDOFF.md`](HANDOFF.md) | How to work on this project, what the architecture is, where the code lives. **The entry point.** |
| 2 | [`ULTRA_MASTER_PLAN_2026-08-08.md`](ULTRA_MASTER_PLAN_2026-08-08.md) | **The current plan.** Stage 3 packaging, and the P0 that blocks it. |
| 3 | [`OPEN_ITEMS.md`](OPEN_ITEMS.md) | **The bug register.** Read the Status column; §1–§2 are all resolved except items 7, 10, 12. L2 was fixed 2026-08-09. |
| 4 | [`DECISION_PACK.md`](DECISION_PACK.md) | The eight founder rulings. Seven open, D4 taken. |

Then [`../BACKLOG.md`](../BACKLOG.md) — what shipped and when, then what is left in
dependency order.

**Working on the code rather than reading about it?**
[`ENGINEERING_METHOD.md`](ENGINEERING_METHOD.md) is the fifth document you may
read and the only one exempt from the rule below, because it describes *practices*
rather than *state*: it cannot drift from a tree it makes no claims about.

**Do not add a fifth to that list.** The failure this project keeps hitting is a
second copy of the truth that drifts from the first. When something changes,
change it in the one document that owns it.

**Coming from outside?** [`../README.md`](../README.md) is the front door — what
the product is, how to build and run it, and what it does not do. It is written
for someone evaluating the project; everything in *this* directory is written for
someone working on it. The four above assume you have read it.

---

## The rule that keeps this set honest

**Exactly one document is the current plan. Every other plan carries a
`⛔ SUPERSEDED` banner at the very top, naming its successor.**

A superseded document is **annotated, never rewritten.** Its reasoning is the
record of why a decision was made, and editing it to match today's truth destroys
the only evidence that the decision was ever reasoned about. When a superseded
document contains a claim that is now false and would mislead someone acting on
it, the banner says so explicitly and the body is left alone.

**Cite `file.go` and a symbol, not `file.go:142`.** Line numbers rot on the next
commit. Three of the seven corrections on 2026-08-07 were line numbers that had
already moved — `daemon/main.go:300` became 375 in the same session that wrote it
down, and `extension.ts:33` moved *and* changed behaviour. A symbol name survives
a refactor or fails loudly; a line number silently points at something else.

---

## Current — plans, registers, and living references

| Document | What it is |
|---|---|
| [`HANDOFF.md`](HANDOFF.md) | Entry point. Architecture, working discipline, where things live. |
| [`ENGINEERING_METHOD.md`](ENGINEERING_METHOD.md) | **How this codebase is made robust.** Each technique, the bug that made it necessary, and what it costs. |
| [`ULTRA_MASTER_PLAN_2026-08-08.md`](ULTRA_MASTER_PLAN_2026-08-08.md) | **The current plan.** Packaging, with Stages 4–5 carried forward. |
| [`OPEN_ITEMS.md`](OPEN_ITEMS.md) | The bug register, statuses resolved in place. |
| [`DECISION_PACK.md`](DECISION_PACK.md) | D1–D8 founder rulings, one page each. |
| [`../BACKLOG.md`](../BACKLOG.md) | Forward-looking capability work only. |
| [`../SECURITY_MODEL.md`](../SECURITY_MODEL.md) | The threat model and what each gate actually promises. |
| [`../PRODUCT_OVERVIEW.md`](../PRODUCT_OVERVIEW.md) | What the product is, for a reader who has never seen it. |
| [`MCP_MASTER.md`](MCP_MASTER.md) | Front door for agent mode / MCP; links to its own evidence. |
| [`MCP_DEBUG_PLAYBOOK.md`](MCP_DEBUG_PLAYBOOK.md) | Operational: diagnosing a misbehaving MCP server. |
| [`MCP_LANE_B_THREAT_MODEL.md`](MCP_LANE_B_THREAT_MODEL.md) | Why Lane B is **unconfined**, stated plainly. Read before touching agent mode. |
| [`MIGRATION_RUNBOOK_0000_0004.md`](MIGRATION_RUNBOOK_0000_0004.md) | Applying Supabase migrations by hand. Needs dashboard access. |
| [`ENTERPRISE_QA_MASTER.md`](ENTERPRISE_QA_MASTER.md) | The whole-product QA campaign definition. |

### Design notes — the reasoning behind one subsystem each

| Document | Subsystem |
|---|---|
| [`../QUOTA_RESERVATION_DESIGN.md`](../QUOTA_RESERVATION_DESIGN.md) | Proxy quota reservation, including §5(e)'s stated dishonesty budget |
| [`../RETRIEVAL_BUDGET_DESIGN.md`](../RETRIEVAL_BUDGET_DESIGN.md) | How the context budget is spent |
| [`../SIGNAL_ESCALATION_DESIGN.md`](../SIGNAL_ESCALATION_DESIGN.md) | Degradation signalling |

---

## Measurements — point-in-time, still cited

Not stale, because a measurement is *of a date* and says so. Cite them with their
date attached.

| Document | Measured |
|---|---|
| [`LATENCY_BASELINE.md`](LATENCY_BASELINE.md) | TTFT and index timings, with the harness |
| [`ROBUSTNESS_BASELINE.md`](ROBUSTNESS_BASELINE.md) | The robustness numbers, promoted out of a scratchpad |
| [`CHUNK_SCRUB_FIRE_RATE.md`](CHUNK_SCRUB_FIRE_RATE.md) | The 33%-of-chunks / zero-precision data that killed warn-mode Design B |
| [`AGENT_MODE_COST_2026-08-01.md`](AGENT_MODE_COST_2026-08-01.md) | What an agent turn costs |
| [`TOOL_MENU_SIZE_2026-08-01.md`](TOOL_MENU_SIZE_2026-08-01.md) | Selection accuracy vs menu width: flat between 5 and 12 |
| [`TOOLCALL_RELIABILITY_2026-07-31.md`](TOOLCALL_RELIABILITY_2026-07-31.md) | Tool-selection degradation as the menu widens |
| [`AGENT_LOOP_RELIABILITY_2026-07-31.md`](AGENT_LOOP_RELIABILITY_2026-07-31.md) | Agent-loop failure modes that did **not** occur |
| [`RETRIEVAL_EVAL_CHECKPOINT_2026-08-08.md`](RETRIEVAL_EVAL_CHECKPOINT_2026-08-08.md) | The gated eval suite run by hand at `23550e4`: 15/15, 8/9, 90.1% — and what the excluded fourth test actually measures |

---

## Historical — evidence, not instructions

**Do not act on anything in this section.** Each is kept for the reasoning or the
evidence trail; the ones that are plans carry a `⛔ SUPERSEDED` banner.

| Document | Kept because |
|---|---|
| [`MASTER_PLAN_2026-08-07.md`](MASTER_PLAN_2026-08-07.md) | ⛔ Superseded 2026-08-08. Its Stages 0–2 shipped. Its Stage 3 named the wrong install directory and assumed no `.vsix` existed; both are corrected in the successor's §1. |
| [`ULTRA_MASTER_PLAN_2026-08-06.md`](ULTRA_MASTER_PLAN_2026-08-06.md) | ⛔ Superseded. Named junctions as the top unknown security risk — and was right (`c17e14c`). |
| [`MASTER_PLAN_2026-08-05.md`](MASTER_PLAN_2026-08-05.md) | ⛔ Superseded. Its Track A found a QA campaign that recorded PASS on a sandbox that did not exist; that is why this project audits by execution. |
| [`QA_LAUNCH_GATE_2026-07-30.md`](QA_LAUNCH_GATE_2026-07-30.md) | The launch-gate audit and same-day remediation, commit by commit. 951 lines of transcript. |
| [`AGENT_MODE_QA_GATE_2026-08-01.md`](AGENT_MODE_QA_GATE_2026-08-01.md) | The agent-mode gate, with its remediation appended. |
| [`VULNERABILITY_REPORT_2026-08-06.md`](VULNERABILITY_REPORT_2026-08-06.md) | Evidence behind the 08-06 plan's six P0s. |
| [`MCP_ROBUSTNESS_REPORT_2026-08-03.md`](MCP_ROBUSTNESS_REPORT_2026-08-03.md) | The MCP numbers `MCP_MASTER.md` cites. |
| [`ENTERPRISE_QA_REPORT_2026-08-03.md`](ENTERPRISE_QA_REPORT_2026-08-03.md) · [`-04`](ENTERPRISE_QA_REPORT_2026-08-04.md) · [`-05`](ENTERPRISE_QA_REPORT_2026-08-05.md) | Three consecutive QA passes on SHA `8979530`. |
| [`ARCHIVE/BACKLOG_2026-07.md`](ARCHIVE/BACKLOG_2026-07.md) | 3,839 lines of verbatim July record: SHAs, transcripts, measured numbers. Moved, never summarised. |

---

## Vocabulary

Used consistently across every document here; they are not interchangeable.

| Label | Means |
|---|---|
| **CONFIRMED** | Reproduced by something actually run, or read directly off the line cited |
| **PLAUSIBLE** | Reasoned from source; the failing path was never executed |
| **NOT RUN** | Honestly not attempted — no hardware, no spend, out of scope |
| **FIXED** | Implemented *and* neuter-verified: the fix was removed and the test demonstrated to fail |
| **CLOSED** | A founder signature. **Engineering never writes this.** Implemented-and-verified is where engineering stops. |
