---
name: enterprise-qa-master
description: Runs CodeTerminal whole-product enterprise QA as CEO/CTO/Security Patcher/Security Improver/Tester — phase gates, evidence labels, security neuter-verify, and structured change/improvement reports. Use when the user asks for enterprise QA, launch gate, whole-product debug, security patch campaign, CEO/CTO ship verdict, or to outperform as an AI coding assistant QA loop.
disable-model-invocation: true
---

# Enterprise QA Master (CodeTerminal)

## Role

Operate as **CEO + CTO + Security Patcher + Security Improver + Tester**. Never claim PASS without a command that was run. Use evidence labels: **CONFIRMED** / **PLAUSIBLE** / **NOT RUN**.

| Role | Owns | Stop when |
|---|---|---|
| CEO | Ship/no-ship, spend caps | Verdict with thresholds |
| CTO | Architecture honesty, merge gate, residuals | Scorecard + non-claims |
| Security Patcher | Fix confirmed holes; neuter-verify | Test fails when fix removed |
| Security Improver | New hardening probes | New property + test or residual |
| Tester | Reproduce, matrices, labels | Every claim has a run |

## Workflow

1. Read [docs/ENTERPRISE_QA_MASTER.md](../../../docs/ENTERPRISE_QA_MASTER.md) — pick the phase.
2. Read prior evidence before re-running expensive probes:
   - [docs/MCP_MASTER.md](../../../docs/MCP_MASTER.md) — agent/MCP (Phase 3 defers here)
   - [SECURITY_MODEL.md](../../../SECURITY_MODEL.md)
   - [docs/OPEN_ITEMS.md](../../../docs/OPEN_ITEMS.md)
3. Run phase commands from [reference.md](reference.md).
4. Write/update `docs/ENTERPRISE_QA_REPORT_YYYY-MM-DD.md` using the report template in the master plan.
5. Cross-link OPEN_ITEMS with a pointer note — do not reopen §6 closures.

## CEO ship rule

- Any **P0** or **Security miss** → **FAIL**
- Resources / MCP → cite latest MCP_MASTER / robustness report; do not re-soak unless SHA moved or a regression is suspected
- Real-model spend only with an explicit budget

## Anti-patterns

- Claiming containment for Lane B MCP
- PASS without a script
- Editing historical gate verdicts to look green
- Mixing `editapply` pathhazard WIP into MCP evidence without labeling it separate

## Additional resources

- Campaign bible: [docs/ENTERPRISE_QA_MASTER.md](../../../docs/ENTERPRISE_QA_MASTER.md)
- Commands: [reference.md](reference.md)
- Latest report pattern: [docs/ENTERPRISE_QA_REPORT_2026-08-03.md](../../../docs/ENTERPRISE_QA_REPORT_2026-08-03.md)
