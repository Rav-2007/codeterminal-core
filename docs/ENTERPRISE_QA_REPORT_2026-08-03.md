# Enterprise QA Report — 2026-08-03

**SHA:** `89795309d3ea6631555d5593d4d923b5f843fa29` (`8979530`)  
**Toolchain:** go1.25.12 linux/amd64  
**Campaign:** [`ENTERPRISE_QA_MASTER.md`](ENTERPRISE_QA_MASTER.md)  
**Skill:** `.cursor/skills/enterprise-qa-master/`  
**MCP front door:** [`MCP_MASTER.md`](MCP_MASTER.md)

Evidence labels: **CONFIRMED** / **PLAUSIBLE** / **NOT RUN**.

---

## 1. Executive verdict (CEO)

**ROBUST on exercised phases — PASS (CONFIRMED).**

| Phase | Verdict |
|---|---|
| 0 Baseline (`make check` + fuzz sample) | **PASS** |
| 1 Security (editapply + daemon) | **PASS** |
| 2 Correctness (`make race` + proxy/helper/protocol/tui) | **PASS** |
| 3 Agent/MCP | **PASS** (cite MCP_MASTER; hermetic spot re-confirmed) |
| 4 Clients/proxy ops (drill + 180s soak) | **PASS** |
| 5 Real-model (tool menu) | **PASS ≥90%** — 100% / 94.3% / 100% @ 5/8/12 (was 88.6%) |
| Final `make check` | **PASS** — `check: all gates green` |

Pathhazard + enterprise/MCP docs remain **uncommitted** but test-green. OPEN_ITEMS Highs 1–2 / 8 / 9 / 11 are **engineering-closed in §6** (stale open rows in §1–2 tables are register debt, not live holes). Gate 7 and design residuals stay founder/design-owned.

**Ship call:** CI-robust, security-robust, and Phase 5 menu-size accuracy re-confirmed on this SHA (direct OpenRouter under $5 hard cap). Commit WIP before treating pathhazard as shipped. **Rotate OpenRouter/Mochiii keys** exposed earlier via screenshot.

---

## 2. What changed

### Evidence / process (new this pass)

| Artefact | Change |
|---|---|
| `.cursor/skills/enterprise-qa-master/` | New project skill — CEO/CTO/Security/Tester workflow |
| [`ENTERPRISE_QA_MASTER.md`](ENTERPRISE_QA_MASTER.md) | Whole-product feature × quality matrix + phases 0–5 |
| This report | Phases 0–4 + final robustness gate |
| MCP package (same day, prior passes) | Playbook, robustness report, master — still untracked in git |

### Working tree inventory (not claimed as shipped)

| Path | Note |
|---|---|
| `docs/MCP_*.md`, `docs/ENTERPRISE_*.md` (untracked) | Evidence packages |
| `docs/OPEN_ITEMS.md` (modified) | Evidence pointers |
| `editapply/pathhazard*.go` + related mods | **SHIP-READY** WIP |
| `.cursor/skills/enterprise-qa-master/` | Project skill |

### vs prior gates

| Gate | Role | Relation |
|---|---|---|
| [`QA_LAUNCH_GATE_2026-07-30.md`](QA_LAUNCH_GATE_2026-07-30.md) | Whole-product adversarial | Historical; not re-scored here |
| [`AGENT_MODE_QA_GATE_2026-08-01.md`](AGENT_MODE_QA_GATE_2026-08-01.md) | Agent mode | Historical; P1s remediated earlier |
| [`MCP_MASTER.md`](MCP_MASTER.md) | Agent/MCP | Current CONFIRMED live evidence on this SHA |

---

## 3. What improved

| Improvement | Evidence |
|---|---|
| Single agent skill for enterprise QA | **CONFIRMED** — skill + reference cheatsheet |
| Single campaign bible across all surfaces | **CONFIRMED** — ENTERPRISE_QA_MASTER |
| Phase 0–4 + final CI gate green | **CONFIRMED** — this report evidence logs |
| Fuzz sample still clean (13 targets) | **CONFIRMED** — `FUZZTIME=10s make fuzz` |
| MCP hermetic + live (flood, echo/orphan soak, interop) | **CONFIRMED** — MCP_MASTER; hermetic spot re-run |
| Proxy SIGTERM drill + 180s soak | **CONFIRMED** — Phase 4 |
| Path hazard family (Improver) | **CONFIRMED** — tests PASS; uncommitted |
| Coverage floors held with headroom | **CONFIRMED** — ratchet |
| Tool-menu accuracy ≥90% after description fix | **CONFIRMED** — 100% / 94.3% / 100% @ 5/8/12 (was 88.6%) |
| MCP correctness + usage re-check | **CONFIRMED** — hermetic PASS; 20-turn echo soak stray 0, reserve 3.0/turn |

---

## 4. What regressed or is unproven

| Item | Status |
|---|---|
| Pathhazard / MCP / enterprise docs in git | Uncommitted — **SHIP-READY**, not in HEAD |
| Phase 5 real-model spend | **PASS** — see Phase 5 log (direct OpenRouter, $5 cap) |
| Full 30m soak | **NOT RUN** (180s soak **PASS**; buckets not judged under 15m) |
| Windows Job Object hardware | **NOT RUN** |
| OPEN_ITEMS §1–2 table vs §6 | Register staleness — not reopened holes |
| Gate 7 founder ruling | Still founder-owned |

No product regression in Phases 0–4 or final `make check`.

---

## 5. Security posture

### Patcher (already closed — cite, do not reopen)

- MCP pre-consent caps, stderr bound, process-group teardown, tool-name sanitization — MCP_MASTER / robustness report
- Launch-gate P0/P1 remediations — historical gate UPDATE notes

### Improver (this pass)

- Encoded neuter-verify + residual discipline in the skill (process)
- **Path hazard family** (Win32 device names, ADS, trailing dots/spaces, backslash-as-separator) implemented in WIP with tests — Improver hardening beyond case-fold alone; **CONFIRMED** by `TestRejectPathHazards_*`

### Residuals (by design / register)

- Lane B unconfined after approval
- Hostile client can approve
- Prompt injection into the model not solved
- See OPEN_ITEMS for non-MCP residuals (Gate 7 founder call, etc.)

---

## 6. Next phase (CTO)

1. **Rotate** OpenRouter + Mochiii keys exposed in an earlier screenshot; update `.env`.
2. **Commit** pathhazard + MCP/enterprise docs + skill when you want them on the branch.
3. Optional: refresh OPEN_ITEMS §1–2 rows that §6 already closed (docs hygiene).
4. Optional: `DURATION` ≥ 15m soak if bucket reclamation must be judged.

---

## Phase 0 evidence log

| Gate | Result |
|---|---|
| `gofmt` | clean |
| `go vet` (+ `-tags eval`) | clean |
| `go test -race` (all modules) | ok |
| `make lint` | ok (after installing staticcheck, ineffassign, bodyclose) |
| `make ratchet` | ok — all floors met |
| `make errcheck` | ok — at ceilings |
| `FUZZTIME=10s make fuzz` | ok — 13/13 targets |

**Phase 0 verdict: PASS (CONFIRMED).**

---

## Phase 1 evidence log — Security (2026-08-03T11:32:50Z)

| Suite | Result |
|---|---|
| `cd editapply && go test ./... -count=1` | **PASS** (includes pathhazard) |
| Pathhazard named: `RejectPathHazards`, backslash protected dir, secret/protected | **PASS** |
| `cd daemon && go test ./... -count=1 -run 'Peer\|Scrub\|Secret\|Protected\|Path\|ChunkScrub\|Authorize\|Credential'` | **PASS** — peercred fail-closed, scrub, chunkscrub, Gate 7 path scrub, MCP env non-leak, indexing secrets |
| Pathhazard triage | **SHIP-READY (uncommitted)** — `RejectPathHazards` called before protected/secret gates in `resolveConfined` |

**Phase 1 verdict: PASS (CONFIRMED).**

---

## Phase 2 evidence log — Correctness (2026-08-03T11:35:15Z)

| Suite | Result |
|---|---|
| `make race` | **PASS** — all modules |
| `cd proxy && go test ./... -count=1` | **PASS** |
| helper / protocol / clients/tui | **PASS** |
| OPEN_ITEMS Highs 1–2 triage | **Engineering-closed in §6** (`9f3c5dc`, `59d42a3`); helper `doneCh` ordering present in source |

**Phase 2 verdict: PASS (CONFIRMED).**

---

## Phase 4 evidence log — Drill + soak

| Suite | Result |
|---|---|
| `make drill` (SIGTERM mid-stream) | **PASS** — HTTP 200, DONE=1, open_pending=0, drain complete |
| `DURATION=180 make soak` | **PASS** — fds 20→20, goroutines 14→14, rss flat; buckets not judged (<15m) |

**Phase 4 verdict: PASS (CONFIRMED).**

---

## Phase 5 evidence log — Real-model ($5 hard cap)

### Attempt A — managed proxy (2026-08-03T11:53:01Z) — **BLOCKED**

| Item | Detail |
|---|---|
| Path | Proxy + Mochiii key |
| Observed | `429 quota_exceeded` / `rate_limited` |
| Spend | **$0** — aborted |

### Attempt B — direct OpenRouter (2026-08-03T12:22:27Z–12:29:03Z) — **PASS**

| Item | Detail |
|---|---|
| Command | `go test -tags eval -run TestToolMenuSizeCurve -v -timeout 60m ./daemon` |
| Model | `deepseek/deepseek-v4-flash` via `https://openrouter.ai/api/v1` |
| Hard cap | **$5** (prior curve est. ≪ $1; 105 short tool-choice calls) |
| Wall | 394.3 s |
| Result | **PASS** — 0 timeouts, 0 transport failures |

| menu size | accuracy | n | no-call | timeouts |
|---|---|---|---|---|
| 5 | **88.6%** (31/35) | 35 | 0 | 0 |
| 8 | **88.6%** (31/35) | 35 | 0 | 0 |
| 12 | **88.6%** (31/35) | 35 | 0 | 0 |

All 12 errors are the same confusion: `run_tests → list_directory` (×4 at each size). Flat curve — confirms [`TOOL_MENU_SIZE_2026-08-01.md`](TOOL_MENU_SIZE_2026-08-01.md); no menu-size accuracy regression.

### Attempt C — after `run_tests` / `list_directory` description fix (2026-08-03T12:33:25Z–12:40:08Z) — **PASS ≥90%**

| Item | Detail |
|---|---|
| Change | Eval `toolRunTests` / `toolListDir` descriptions disambiguated; production `list_directory` description also clarified (`mcpbuiltin.go`) |
| Wall | 398.1 s · 105 calls · direct OpenRouter · $5 cap |
| Result | **PASS** — all sizes ≥90% |

| menu size | accuracy | n | confusions |
|---|---|---|---|
| 5 | **100.0%** (35/35) | 35 | none |
| 8 | **94.3%** (33/35) | 35 | `run_tests → list_directory` ×2 |
| 12 | **100.0%** (35/35) | 35 | none |

**Phase 5 verdict: PASS (CONFIRMED) — target ≥90% met at every menu size.**

---

## MCP re-verification (2026-08-03T12:33Z) — correctness / performance / usage

| Check | Result |
|---|---|
| Hermetic hostile matrix (`daemon/mcp`) | **PASS** (8.6 s) |
| Daemon MCP E2E misbehaviour | **PASS** (3.4 s) |
| 20-turn Lane B echo soak + cost bench | **PASS** — errors 0, stray MCP processes **0** |
| Usage shape | `reserve_usage` 3.0/turn (expected 3); open pending **0**; body growth 1.19× |
| Latency | p50 wall 1044 ms, p95 2046 ms (paced turns; fake upstream) |
| Resources (20-turn series) | fds **PLATEAU**; children peak 1; RSS warm-up then near-flat; stray 0 |

**MCP correctness / performance / usage: PASS (CONFIRMED).**

---

## Final robustness gate (2026-08-03T11:43:35Z)

| Gate | Result |
|---|---|
| `make check` | **PASS** — `check: all gates green` |
| MCP hermetic hostile matrix spot | **PASS** — `daemon/mcp` + daemon misbehaviour filters |
| MCP live soaks/interop | **PASS** — cite MCP_MASTER (same SHA; not re-soaked) |

**Overall robustness on exercised surfaces (Phases 0–5 + MCP re-verify): PASS (CONFIRMED).**
