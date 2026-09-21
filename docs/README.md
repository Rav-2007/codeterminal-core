# Documentation index

**79 documents: 70 in `docs/`, 1 in `docs/ARCHIVE/`, and 8 at the repository root.**

**This page says which four documents you need first, and then lists every
other one.**

> **THIS INDEX IS NOW COMPLETE, AND A GATE KEEPS IT THAT WAY.** Every tracked
> document in `docs/` and every root-level design document has a row below.
> `scripts/docs-index.sh` derives the corpus from `git ls-files` and fails the
> build when a member of it is unreachable from this page.
>
> It needed one. This header read *"44 markdown files, ~16600 lines"* until
> 2026-09-21 while `docs/` held 68, and the correction was **itself off by one**
> — written as 67 without re-counting, in the commit that corrected the stale
> number. That is what `ENGINEERING_METHOD.md` means by *a stated count ≠ a
> counted count*.
>
> **It used to state a line count too, and no longer does.** That number went
> stale five times on 2026-09-21 alone, because every edit to every document
> moves it. A gate on it would fail every documentation change until someone
> updated a header, which is a gate people learn to route around. The document
> count is checked; a line count could only ever be typed. So it is gone,
> rather than being corrected a sixth time.
>
> Then the warning that replaced it said *"roughly a third of the directory is
> not listed"*. Measured, it was **36 of 69 — more than half**, including
> `RESIDUAL_RISKS.md` and `TRUST_BOUNDARIES.md`, both live coderefs-enforced
> registers, and the entire C0–C7 closing series. An estimate of how wrong a
> page is is still an estimate.
>
> The warning also said nothing could catch it: *"docs-links.sh verifies that
> links which exist resolve, and a file that was never linked has no link to
> check."* True of that gate, false as a general claim. A link checker walks the
> index and asks whether each member is real; the new gate walks the directory
> and asks whether each member is listed. Only the second question can see an
> omission — the same "derive the expected set from the tree, not from the list
> under test" move this repository keeps arriving at.

> **EVERY IDENTIFIER WAS RENAMED `codeterminal` -> `mochiii` on 2026-09-21**,
> and the historical documents in this directory were renamed with everything
> else. So a dated record describing a pre-rename artifact now names it with the
> post-rename name: the v0.0.2 draft really held `codeterminal-tui-linux-x64`,
> and the record of it here says `mochiii-tui-linux-x64`. Nothing was published
> before the rename, so no artifact a reader can obtain carries the old name —
> but the records are approximations of their own past, and saying so is better
> than letting someone discover it against a checksum. The repository itself is
> still `Rav-2007/codeterminal-core`, deliberately: that name is a live URL.

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
| 3 | [`OPEN_ITEMS.md`](OPEN_ITEMS.md) | **The bug register.** Read the Status column — this cell summarises, and summaries go stale: items 7, 10 and 12 were named here as open long after they were fixed, and then this line did it again, carrying item 22 as open after `6bbe549` closed it. It said so itself — *"docs-claims.sh checks the Status column against BACKLOG, not against this line"* — which is an accurate note about an exemption and no help at all to the reader who believed the sentence in front of it. The exemption is gone: this cell is now compared against the register on every push, the same as BACKLOG's. **open: 24, 34, 36** |
| 4 | [`DECISION_PACK.md`](DECISION_PACK.md) | The eight founder rulings. **All eight taken** — D4 on 2026-07-27, D1–D3 and D5–D8 on 2026-08-12 — and the P3 security gate is closed. This cell said *"Seven open, D4 taken"* for three weeks after that, which is the count as it stood on 2026-07-27; the rulings landed in `DECISION_PACK.md`'s Status column and the three places that restate it were not updated. **taken: D1 D2 D3 D4 D5 D6 D7 D8** |

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
| [`PRODUCTION_READINESS_2026-09-21.md`](PRODUCTION_READINESS_2026-09-21.md) | **Is this shippable?** One verdict per surface across the gate system, the security model, the agent loop and the QA surface — plus what the gates structurally cannot see. Written at `v0.0.3`. |
| [`ULTRA_MASTER_PLAN_2026-08-08.md`](ULTRA_MASTER_PLAN_2026-08-08.md) | **The current plan.** Packaging, with Stages 4–5 carried forward. |
| [`OPEN_ITEMS.md`](OPEN_ITEMS.md) | The bug register, statuses resolved in place. |
| [`DECISION_PACK.md`](DECISION_PACK.md) | D1–D8 founder rulings, one page each. |
| [`../BACKLOG.md`](../BACKLOG.md) | Forward-looking capability work only. |
| [`../SECURITY_MODEL.md`](../SECURITY_MODEL.md) | The threat model and what each gate actually promises. |
| [`../PRODUCT_OVERVIEW.md`](../PRODUCT_OVERVIEW.md) | What the product is, for a reader who has never seen it. |
| [`PROJECT_INTELLIGENCE_BLUEPRINT.md`](PROJECT_INTELLIGENCE_BLUEPRINT.md) | A capability map and a landing-page spec, written for positioning rather than for engineering. **It overlaps `../PRODUCT_OVERVIEW.md` and is the weaker of the two** — when they disagree, the overview owns the answer. |
| [`MCP_MASTER.md`](MCP_MASTER.md) | Front door for agent mode / MCP; links to its own evidence. |
| [`MCP_DEBUG_PLAYBOOK.md`](MCP_DEBUG_PLAYBOOK.md) | Operational: diagnosing a misbehaving MCP server. |
| [`MCP_LANE_B_THREAT_MODEL.md`](MCP_LANE_B_THREAT_MODEL.md) | Why Lane B is **unconfined**, stated plainly. Read before touching agent mode. |
| [`MIGRATION_RUNBOOK_0000_0004.md`](MIGRATION_RUNBOOK_0000_0004.md) | Applying Supabase migrations by hand. Needs dashboard access. |
| [`ENTERPRISE_QA_MASTER.md`](ENTERPRISE_QA_MASTER.md) | The whole-product QA campaign definition. |

### Registers the build checks

Four documents are not prose about the project — they are **state the gates read
on every push**. Editing a status cell in one of them changes what CI asserts.

| Document | Register |
|---|---|
| [`OPEN_ITEMS.md`](OPEN_ITEMS.md) | The bug register. Compared against `../BACKLOG.md` and this page by `scripts/docs-claims.sh`. The open list is stated **once**, in the *Start here* table above — restating it here created a second copy in this very commit, and `docs-claims.sh` refused the push for it. |
| [`RESIDUAL_RISKS.md`](RESIDUAL_RISKS.md) | Risk register for `clients/tui` — what is accepted, by whom, and what would retire it. Coderefs-enforced. |
| [`TRUST_BOUNDARIES.md`](TRUST_BOUNDARIES.md) | The live enumeration of every boundary the product crosses, and what is assumed on each side. Coderefs-enforced. |
| [`A_DELIVER_2026-09-20.md`](A_DELIVER_2026-09-20.md) | The delivery register: §A12 (v0.0.3) and §A13 (the rename's costs), findings carried as `A12-F*` / `A13-F*`. |

### Design notes — the reasoning behind one subsystem each

| Document | Subsystem |
|---|---|
| [`../QUOTA_RESERVATION_DESIGN.md`](../QUOTA_RESERVATION_DESIGN.md) | Proxy quota reservation, including §5(e)'s stated dishonesty budget |
| [`../RETRIEVAL_BUDGET_DESIGN.md`](../RETRIEVAL_BUDGET_DESIGN.md) | How the context budget is spent |
| [`../SIGNAL_ESCALATION_DESIGN.md`](../SIGNAL_ESCALATION_DESIGN.md) | Degradation signalling |
| [`MULTI_AGENT_DESIGN.md`](MULTI_AGENT_DESIGN.md) | Multi-agent orchestration: the pipeline, and the `/team` command that replaced automatic shape routing |
| [`SYNTAX_GATE_TIER_C_DESIGN.md`](SYNTAX_GATE_TIER_C_DESIGN.md) | LSP diagnostics as a third syntax tier. **Design, not shipped.** |

---

## Releases

`RELEASE_NOTES.md` is the changelog a user reads. The per-version files are the
bodies applied to each GitHub Release, kept here because a release body is an
editable field on GitHub and a file in git is not.

| Document | What it is |
|---|---|
| [`RELEASE_NOTES.md`](RELEASE_NOTES.md) | **The changelog.** Newest first, user-observable changes only. |
| [`RELEASE_NOTES_v0.0.4.md`](RELEASE_NOTES_v0.0.4.md) | **The current release.** The Mochiii rename, the API key path, and the first published daemon. A draft until someone publishes it. |
| [`RELEASE_NOTES_v0.0.2.md`](RELEASE_NOTES_v0.0.2.md) | Kept as a record. **Never published**, and its GitHub Release was deleted on 2026-09-21 — the file says so, because the tag URL still returns 200. |
| [`RELEASE_PLAN_2026-09-17.md`](RELEASE_PLAN_2026-09-17.md) | Release planning and depth QA: the 231-cell ledger, and the inverted `origin`/`upstream` topology. |

**There is no `RELEASE_NOTES_v0.0.3.md`.** It was renamed to `_v0.0.4.md` rather
than copied: it had begun telling users to install a `.vsix` filename the v0.0.3
draft does not contain, and two files disagreeing about one release is the drift
this directory exists to prevent.

---

## Architecture decision records

One decision each, with the alternatives that were rejected and why.

| Document | Decision |
|---|---|
| [`ADR-001-mouse-capture.md`](ADR-001-mouse-capture.md) | Mouse capture in the terminal client, and the selection it costs |
| [`ADR-002-vendoring-and-sbom.md`](ADR-002-vendoring-and-sbom.md) | Vendoring and SBOM generation |

---

## Measurements — point-in-time, still cited

Not stale, because a measurement is *of a date* and says so. Cite them with their
date attached.

| Document | Measured |
|---|---|
| [`LATENCY_BASELINE.md`](LATENCY_BASELINE.md) | TTFT and index timings, with the harness |
| [`INDEXING_PERFORMANCE_2026-08-26.md`](INDEXING_PERFORMANCE_2026-08-26.md) | Indexing throughput, measured 2026-08-26 |
| [`ROBUSTNESS_BASELINE.md`](ROBUSTNESS_BASELINE.md) | The robustness numbers, promoted out of a scratchpad |
| [`CHUNK_SCRUB_FIRE_RATE.md`](CHUNK_SCRUB_FIRE_RATE.md) | The 33%-of-chunks / zero-precision data that killed warn-mode Design B |
| [`AGENT_MODE_COST_2026-08-01.md`](AGENT_MODE_COST_2026-08-01.md) | What an agent turn costs |
| [`TOOL_MENU_SIZE_2026-08-01.md`](TOOL_MENU_SIZE_2026-08-01.md) | Selection accuracy vs menu width: flat between 5 and 12 |
| [`TOOLCALL_RELIABILITY_2026-07-31.md`](TOOLCALL_RELIABILITY_2026-07-31.md) | Tool-selection degradation as the menu widens |
| [`AGENT_LOOP_RELIABILITY_2026-07-31.md`](AGENT_LOOP_RELIABILITY_2026-07-31.md) | Agent-loop failure modes that did **not** occur |
| [`RETRIEVAL_EVAL_CHECKPOINT_2026-08-08.md`](RETRIEVAL_EVAL_CHECKPOINT_2026-08-08.md) | The gated eval suite run by hand at `23550e4`: 15/15, 8/9, 90.1% — and what the excluded fourth test actually measures |
| [`ADVERSARIAL_PASS_2026-09-03.md`](ADVERSARIAL_PASS_2026-09-03.md) | **Re-verification pass, 2026-09-03 at `d1a82a5`.** Three cache-cleared shuffled `make check` runs, five gate neuters, three cold end-to-end product runs with the provider request captured verbatim, and the hot-path benchmark table. Opened items 37–40 and part-fixed 37; read the statuses in [`OPEN_ITEMS.md`](OPEN_ITEMS.md), not here |
| [`../AGENT_SECURITY_AND_CAPABILITY_AUDIT.md`](../AGENT_SECURITY_AND_CAPABILITY_AUDIT.md) | **The AI-agent security axis, 2026-08-26 at `efc611d`.** Consent truthfulness, tool-description injection, budget enforcement — the surfaces the filesystem and socket passes did not cover. Its live findings are mirrored into [`OPEN_ITEMS.md`](OPEN_ITEMS.md) items 20–25, which is the register the build actually checks; read the statuses there, not the ones in the report |
| [`RETRIEVAL_EVAL_TREND.md`](RETRIEVAL_EVAL_TREND.md) | **A series, not a point.** One line per locate-eval run, because the thing it tracks — a self-referential corpus growing against a fixed budget — only exists as a slope. Appended by hand; the reason is in the file |
| [`SYNTAX_GATE_TIER_C_DESIGN.md`](SYNTAX_GATE_TIER_C_DESIGN.md) | **Design, not shipped.** LSP diagnostics as a third syntax tier, and the two things that make it larger than it looks: `publishDiagnostics` has no id so the bridge drops it today, and a clean file is indistinguishable from a slow one |

---

## Audits and readiness reviews

Each answers *is this shippable, and what would make it not so* — at a stated
commit, on a stated date. A readiness verdict is a measurement like any other:
cite it with its date, and re-run it rather than re-reading it.

| Document | Scope |
|---|---|
| [`PRODUCTION_READINESS_2026-09-21.md`](PRODUCTION_READINESS_2026-09-21.md) | **The current one.** Five axes — gate system, security model, agent loop, QA reality, per-surface verdict — plus what the gates structurally cannot see. Written at `v0.0.3`; the rename and `v0.0.4` landed the same day, and the coverage incident it predicted is recorded in it. |
| [`TUI_PRODUCTION_READINESS_2026-09-04.md`](TUI_PRODUCTION_READINESS_2026-09-04.md) | The terminal client alone, which ships with no daemon of its own and is the surface most often exercised by hand. |
| [`ADVERSARIAL_PASS_2026-09-03.md`](ADVERSARIAL_PASS_2026-09-03.md) | Re-verification at `d1a82a5`: three cache-cleared shuffled `make check` runs, five gate neuters, three cold end-to-end runs with the provider request captured verbatim. Opened items 37–40. |
| [`ADVERSARIAL_PASS_2026-09-09.md`](ADVERSARIAL_PASS_2026-09-09.md) | The same discipline turned on `daemon`, `editapply`, `helper` and `proxy`. |
| [`../AGENT_SECURITY_AND_CAPABILITY_AUDIT.md`](../AGENT_SECURITY_AND_CAPABILITY_AUDIT.md) | The AI-agent axis at `efc611d`: consent truthfulness, tool-description injection, budget enforcement. Live findings are mirrored into `OPEN_ITEMS.md` 20–25 — **read the statuses there, not here.** |

---

## The C-series — the closing campaign, 2026-09-13 to 2026-09-20

**One campaign, read in order.** It starts from "what is actually true of this
tree" and ends at a delivery register. The lettered suffixes (`C0b`, `C1c`,
`C3b`) are continuations written when the original chunk turned out to be wrong
or incomplete — they are kept separate rather than folded in, because the
correction is the evidence.

| # | Document | What it settled |
|---|---|---|
| C0 | [`C0_GROUND_TRUTH_2026-09-14.md`](C0_GROUND_TRUTH_2026-09-14.md) | Ground truth, delivery, and the doc corpus |
| C0b | [`C0b_DELIVERY_2026-09-14.md`](C0b_DELIVERY_2026-09-14.md) | Delivery, taxonomy, and two untracked reports |
| C1 | [`C1_CI_DETERMINISM_2026-09-15.md`](C1_CI_DETERMINISM_2026-09-15.md) | *Is "CI green at HEAD" evidence, and for what?* |
| C1c | [`C1c_MAIN_RESTORED_2026-09-15.md`](C1c_MAIN_RESTORED_2026-09-15.md) | Restoring `main` — verified, committed, **not pushed** |
| C1d | [`C1d_DIVERGENCE_2026-09-15.md`](C1d_DIVERGENCE_2026-09-15.md) | The divergence, the second red, and what "green" does not mean |
| C2 | [`C2_ITEM41_2026-09-15.md`](C2_ITEM41_2026-09-15.md) | Item 41 — the extension was dead on arrival for 72 days |
| C3 | [`C3_CLASSIII_SCOPE_2026-09-15.md`](C3_CLASSIII_SCOPE_2026-09-15.md) | The Class III guard: derived, not listed |
| C3b | [`C3b_UNBOUNDED_READ_2026-09-16.md`](C3b_UNBOUNDED_READ_2026-09-16.md) | `reachesUnboundedRead` — a chain property a file-scoped guard cannot see |
| C4 | [`C4_CLOSURES_2026-09-15.md`](C4_CLOSURES_2026-09-15.md) | The cheap closures |
| C4b | [`C4b_EVIDENCE_INTEGRITY_2026-09-15.md`](C4b_EVIDENCE_INTEGRITY_2026-09-15.md) | Evidence integrity: what the record claims vs what runs |
| C5 | [`C5_METHOD_2026-09-15.md`](C5_METHOD_2026-09-15.md) | Method hardening, `reach.sh`, and gate scope |
| C5b | [`C5b_TOPOLOGY_2026-09-15.md`](C5b_TOPOLOGY_2026-09-15.md) | Remote topology, orphan branches, the reach allowlist |
| C6 | [`C6_CLOSING_2026-09-17.md`](C6_CLOSING_2026-09-17.md) | The closing chunk: the divergence on `main` |
| C7 | [`C7_RELEASE_READINESS_2026-09-15.md`](C7_RELEASE_READINESS_2026-09-15.md) | *Is this packed clean to ship?* |
| C7b | [`C7b_DELIVERY_2026-09-16.md`](C7b_DELIVERY_2026-09-16.md) | Delivery and reconciliation — the first push in forty commits |
| A | [`A_DELIVER_2026-09-20.md`](A_DELIVER_2026-09-20.md) | **Where it lands.** The delivery register the series feeds. |

---

## Working records — dated, and not instructions

Session notes, checkpoints and handbacks. They are the record of what was known
on a given day, which makes them useful for *why* and unreliable for *what is
true now*. **Every status in them is superseded by the registers above.**

| Document | Record of |
|---|---|
| [`PROJECT_CHECKPOINT_2026-09-13.md`](PROJECT_CHECKPOINT_2026-09-13.md) | Whole-project checkpoint |
| [`CHECKPOINT_ADDENDUM_2026-09-16.md`](CHECKPOINT_ADDENDUM_2026-09-16.md) | What the adversarial pass changed about that checkpoint |
| [`NEXT_ACTIONS_2026-09-14.md`](NEXT_ACTIONS_2026-09-14.md) | Reconnaissance of `audit/adversarial-pass` |
| [`HANDBACK_2026-09-12.md`](HANDBACK_2026-09-12.md) | Handback of `audit/adversarial-pass` |
| [`BRANCH_STATUS_2026-09-05.md`](BRANCH_STATUS_2026-09-05.md) | ⛔ Branch release status, superseded |
| [`PREFLIGHT_MACOS_AND_MERGE_2026-09-15.md`](PREFLIGHT_MACOS_AND_MERGE_2026-09-15.md) | The merge vehicle, and the macOS dispatch — before `darwin-arm64` was dropped |
| [`DECISION_MEMO_2026-09-04.md`](DECISION_MEMO_2026-09-04.md) | Five `daemon/` items raised from the terminal-client work |
| [`MANUAL_SESSION_2026-09-04.md`](MANUAL_SESSION_2026-09-04.md) | A terminal client driven **by hand** — the thing gates cannot substitute for |

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

## Kept beside the code, on purpose

These are **not** indexed by the gate, and the exemption is deliberate: a README
that sits next to the thing it describes is correct structure, not an unindexed
document. They are listed here so they are findable, not because this page owns
them.

| Document | Describes |
|---|---|
| [`../clients/vscode/README.md`](../clients/vscode/README.md) | Building and packaging the extension |
| [`../proxy/README.md`](../proxy/README.md) | Running the proxy |
| [`../mcp-servers/README.md`](../mcp-servers/README.md) | The bundled MCP servers |
| [`../daemon/CHUNK_SCRUB_DESIGN.md`](../daemon/CHUNK_SCRUB_DESIGN.md) | Chunk scrubbing, beside the code that does it |
| [`../proxy/F1_ENFORCEMENT_DESIGN.md`](../proxy/F1_ENFORCEMENT_DESIGN.md) | F1 / ZDR enforcement, beside the proxy that enforces it |

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
