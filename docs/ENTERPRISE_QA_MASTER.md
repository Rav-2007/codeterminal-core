# Enterprise QA Master — CodeTerminal

**Whole-product campaign** for an outperforming local-first AI coding assistant.
Roles: CEO · CTO · Security Patcher · Security Improver · Tester.

| | |
|---|---|
| **Skill** | `.cursor/skills/enterprise-qa-master/` |
| **Latest report** | [`ENTERPRISE_QA_REPORT_2026-08-05.md`](ENTERPRISE_QA_REPORT_2026-08-05.md) — the third and last pass of this campaign |
| **Earlier passes** | [`-08-03`](ENTERPRISE_QA_REPORT_2026-08-03.md) (Phases 0–5 **PASS**, `make check` green) · [`-08-04`](ENTERPRISE_QA_REPORT_2026-08-04.md). All three ran against SHA `8979530`; keep all three, because what changed *between* them is the finding |
| **MCP front door** | [`MCP_MASTER.md`](MCP_MASTER.md) (Phase 3 defers here) |
| **Enterprise campaign** | this file |
| **Security position** | [`../SECURITY_MODEL.md`](../SECURITY_MODEL.md) |
| **Open register** | [`OPEN_ITEMS.md`](OPEN_ITEMS.md) |

Evidence labels: **CONFIRMED** / **PLAUSIBLE** / **NOT RUN**.

---

## How to use

1. Invoke the `enterprise-qa-master` skill (or follow this doc).
2. Run **Phase 0** on every new SHA before claiming readiness.
3. Advance phases in order; do not skip Security for Correctness cosmetics.
4. Publish/update a dated `ENTERPRISE_QA_REPORT_YYYY-MM-DD.md`.
5. CEO applies the ship rule below.

---

## Feature × quality matrix

| Surface | Paths | Qualities under test |
|---|---|---|
| Daemon / transport | `daemon/` peercred, socket, lock | Fail-closed auth, no TCP listener, shutdown |
| Edit safety | `editapply/` | Path, exact-match, syntax, confirm, backup; secret/protected/path hazard |
| Retrieval | index, vector/lexical, `chunkscrub` | Grounding, scrub, file perms |
| Agent / MCP | Lane A/B | Consent, budgets, pre-consent DoS, teardown — **see MCP_MASTER** |
| Proxy / quota | `proxy/` | Spend, ZDR, rate limit, reservation honesty |
| Clients | TUI, VS Code, CLI | Consent UX, no silent approve, a11y residuals |
| Helper / embedder | `helper/` | Ready/shutdown, log bounds |
| Supply chain / CI | `make check`, fuzz, soak, drill | fmt/vet/race/lint/ratchet/errcheck |

---

## Phases

### Phase 0 — Baseline (required every SHA)

**Entry:** clean enough tree to interpret failures (note WIP separately).  
**Exit:** `make check` green (or each red gate named); SHA/date recorded; WIP inventory.

```sh
make check
git rev-parse HEAD && git status --short
```

### Phase 1 — Security

**Exit:** editapply + daemon security-shaped tests CONFIRMED; residuals listed; any new hole gets a failing test before a patch.

### Phase 2 — Correctness / availability

**Exit:** race + module tests; OPEN_ITEMS Highs triaged (fix or register).

### Phase 3 — Agent / MCP

**Do not duplicate** live soaks/interop if [`MCP_MASTER.md`](MCP_MASTER.md) is green on this SHA.  
**Exit:** cite MCP scorecard, or re-run only on SHA change / suspected regression.

### Phase 4 — Clients / proxy ops

**Exit:** TUI tests; drill; soak (DURATION may be shortened — label NOT RUN if skipped).

### Phase 5 — Perf / cost / model quality

**Exit:** agent-cost-bench zero-spend numbers; real-model evals only with budget (else NOT RUN).

---

## CEO ship rule

| Condition | Verdict |
|---|---|
| Any P0, or Security miss | **FAIL** |
| Phase 0 red | **FAIL** |
| MCP/Resources unproven on this SHA | Do not claim Resources PASS — cite or re-measure |
| Design residuals only (Lane B unconfined, hostile client, prompt injection) | Allowed if **stated** |

---

## Report template

Copy into `docs/ENTERPRISE_QA_REPORT_YYYY-MM-DD.md`:

```markdown
# Enterprise QA Report — YYYY-MM-DD

**SHA:** …  **Toolchain:** …

## 1. Executive verdict (CEO)
PASS / FAIL / PARTIAL — one paragraph.

## 2. What changed
- Docs / evidence packages
- Code WIP (separate from shipped)
- vs launch gate / agent gate / prior report

## 3. What improved
- Bullet + evidence label + command/cite

## 4. What regressed or is unproven
- …

## 5. Security posture
- Patcher (closed with neuter-verify)
- Improver (new probes / residuals)

## 6. Next phase (CTO)
- Single recommended next move
```

---

## Prior gates (historical — do not rewrite)

- [`QA_LAUNCH_GATE_2026-07-30.md`](QA_LAUNCH_GATE_2026-07-30.md)
- [`AGENT_MODE_QA_GATE_2026-08-01.md`](AGENT_MODE_QA_GATE_2026-08-01.md)
- [`MCP_ROBUSTNESS_REPORT_2026-08-03.md`](MCP_ROBUSTNESS_REPORT_2026-08-03.md)
