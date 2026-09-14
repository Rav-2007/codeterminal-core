<!-- coderefs: enforced -->
# C0b — delivery, taxonomy, and the two untracked reports

**Written 2026-09-14.** Four commits. Tree clean. All doc gates green.

**Who has now been told, and by what means:** nobody has been told by any means other than this
repository. The checkpoint, both prior reports, the canonical boundary list and the quarantine
notice are now committed on `audit/adversarial-pass` and pushed nowhere. **That is delivery into
the repo, not delivery to a person** — the distinction this chunk exists to enforce. §7 names who
still needs telling and about what.

---

## 1. Step 1 — verification before commit

| Check | Result | Evidence |
|---|---|---|
| Branch / HEAD at start | `audit/adversarial-pass` @ `1013e1b`, tree had 2 untracked reports | `(M)` |
| `1e48e3c` exists | **YES** — *"docs: five verdicts recorded, and the role that was never assigned"* | `(M)` |
| `9fae051` modifies the sweep | **YES** — `scripts/coverage-ratchet.sh` +39/−8, adds `belongs_to_module()` and both unconditional `FAIL` paths | `(M)` |
| **Every SHA the checkpoint cites resolves here** | **YES — 7 of 7, 0 unresolved** | `(M)`, table below |
| Appendix A anchors | **12 of 12**, one off-by-one (`daemon/server.go:707` blank, statement at `:708`) | `(M)` — HEAD was byte-identical to when they were measured, so this is the same tree state, not a carried claim |
| Contents unaltered | `diff -q` against the source file: **byte-identical** before `git add` | `(M)` |

**The seven cited commits:**

| SHA | Subject |
|---|---|
| `1013e1b` | test(daemon/mcp): the item 2 wiring test was green because it won a race |
| `1e48e3c` | docs: five verdicts recorded, and the role that was never assigned |
| `3ff9ee2` | feat(tui): implement /search via daemon SearchRequest |
| `9987985` | fix(daemon): item 1 — the scrub now holds past turn one, and pre-fix rows are purged |
| `ab5fbf6` | fix(daemon): item 5 — contain a panic in the Lane B connect fan-out |
| `d15c808` | fix(daemon): item 4 — build the fakehelper fixture lazily so fuzz workers can run |
| `e44f217` | fix(daemon/mcp): item 2 — strip provisioned credentials from server stderr |

**H2 — the extractor was validated before its output was trusted.** `\b[0-9a-f]{7}\b` over-matches:
a seven-digit decimal such as `1734900` satisfies it. Known-answer tests run before reporting:
(a) all six expected SHAs found in the file; (b) `1734900` confirmed to match the pattern, proving
the over-match. Of 9 candidates: 7 commits, **2 pure-decimal artifacts of my own pattern**, 0
unresolved. Had I not split those out I would have reported two missing SHAs and blocked the chunk
on my own regex.

**Previously-circular fact, now checkable.** C0 correctly refused to accept "the document cites
`1e48e3c`" as proof the document existed, since only the document could witness it. With the
document in hand it checks out: `1e48e3c` appears three times, in §6's decision table, for rows
*3 — inbound model text not redacted* and *4 — fuzz gate fatal?*. **H1 satisfied — but note the
fact was worth nothing until the artifact arrived, which is exactly why H1 exists.**

---

## 2. Steps 2 and 3 — the commits

| SHA | What |
|---|---|
| `06158f0` | the checkpoint, contents unaltered, with the write/deliver gap in the message |
| `06d8e16` | the two reports that were written and left untracked |
| `bb7e166` | `docs/TRUST_BOUNDARIES.md` + the ambiguous-ref fix the gate caught |
| `4c9355e` | the scope notice on `AGENT_SECURITY_AND_CAPABILITY_AUDIT.md` |

**There is no `build` run at HEAD and that is not a pass.** All four commits are markdown-only;
`build.yml:43` carries `paths-ignore: ["**.md"]`, so a push produces `gates` and no `build`. Nothing
here touches product code, so that is the correct outcome — but it is an absence, not a green.

**The 972/973 discrepancy.** The supplement describes the file as 973 lines; it is **972**
(`wc -l`). Trailing-newline counting, almost certainly. Recorded because a one-line difference in a
document whose provenance was in question is exactly the thing not to wave through.

---

## 3. Step 4 — the offset: mechanism verified, decision upheld

§1.3 numbers **MCP = B7** and **LSP = B8**. All eight prose references across four documents say
**boundaries 6 and 7**. Offset by exactly one. `(M)`

**The owner's decision — canonical twelve, B1+B2 merged — is upheld, and the mechanism is
confirmed. But not by the discriminator the instruction named.**

### 3.1 The named discriminator cannot discriminate

The instruction pointed at `AGENT_SECURITY_AND_CAPABILITY_AUDIT.md`'s two four-item lists. Reading
them: **List A's item 1 bundles the unix socket with SO_PEERCRED** (supports merging B1/B2) **and
its item 4 bundles the entire proxy network surface in both directions** (equally supports merging
B4/B5). A partition into four necessarily bundles more than a partition into twelve, so its
bundling is evidence about its own granularity and nothing else. Relying on it would have been a
coin flip that happened to land right.

### 3.2 The real discriminator was inside §1.3

Its column header is **"Crossing"**. Twelve of thirteen rows are of the form `X → Y`. **B2, "peer
identity check", is the only row that names a control instead of a crossing.** And the table's own
practice for a crossing with a named control is to put the control in the *Site* column beside it —
it does this twice, at **B3** (`helper/main.go:197 protocol.AuthorizePeer`) and **B9** (webfetch
plus the SSRF guard at `daemon/webfetch.go:129`). B2's content belongs in B1's Site cell, where
B1 already carries the accept→handleConn path.

### 3.3 The competing hypothesis, tested and rejected

**Merge B4/B5** (`daemon → provider` POST and `provider → daemon` SSE, two directions of one
connection) fits the arithmetic identically. Rejected on three grounds: both are genuine crossings
satisfying the column header, so merging deletes a real crossing; they carry different risks with
different controls (egress is where secrets leave — the reason TB10's scrub choke exists — ingress
is where untrusted model output is parsed); and the table already treats direction as significant,
listing only the inbound direction for MCP, LSP and web, so listing both for the provider is a
statement rather than a duplication.

**Per instruction: the evidence supports the B1/B2 reading, so the decision does not need
revisiting.**

### 3.4 The mapping, in full

| §1.3 | canonical | crossing |
|---|---|---|
| B1 + B2 | **TB1** | peer → daemon control socket, with its identity check |
| B3 | TB2 | peer → helper socket |
| B4 | TB3 | daemon → completion provider (egress) |
| B5 | TB4 | provider → daemon (SSE / model output) |
| B6 | TB5 | model output → filesystem writes |
| **B7** | **TB6** | **MCP server → daemon** ← prose "boundary 6" ✓ |
| **B8** | **TB7** | **LSP server → daemon** ← prose "boundary 7" ✓ |
| B9 | TB8 | web content → daemon |
| B10 | TB9 | workspace files → index → prompt |
| B11 | TB10 | chunk text → egress (scrub choke) |
| B12 | TB11 | client → proxy (API-key authz) |
| B13 | TB12 | proxy → upstream (ZDR/F1 gate) |

All twelve `file:line` anchors re-verified at HEAD by reading the named line: **12/12 resolve.**
`(M)` Completeness stays `(U)`, carried verbatim and **not** upgraded by copying.

### 3.5 The namespace instruction rested on a wrong premise, and I did not carry it out

The instruction was to rename `OPEN_ITEMS.md:365`'s **B12** row and declare `B<n>` to mean a trust
boundary and nothing else. Both halves fail on measurement:

- **It is not one row.** `docs/OPEN_ITEMS.md:354-365` is a full **B1–B12 series** — the twelve
  defects of the 2026-08-07 bug-hunt pass — with a cross-reference at `:367` (*"B1's class is…"*).
  Renaming only B12 would leave B1–B11 colliding.
- **`B<n>` was never available.** It is already four things:

| Use | Where | Range | Live? |
|---|---|---|---|
| Blockers | `BACKLOG.md:88-91` | B1–B4 | **YES — B3, Apple Developer enrolment, is open** |
| Bug-hunt defects | `docs/OPEN_ITEMS.md:354-365` | B1–B12 | closed, cross-referenced |
| Sentinel sweep ids | `daemon/sentinel_rows_test.go:57`, `daemon/sentinel_secrets_test.go:115,154,198` | B2.2c/e, B2.3a/b | **YES — in test comments** |
| Trust boundaries | the checkpoint's §1.3 only | B1–B13 | superseded by `docs/TRUST_BOUNDARIES.md` |

Boundaries are the **newest** claimant and the only one whose live prose omits the prefix entirely
— all eight references say "boundaries 6 and 7", never "B6". Claiming `B<n>` for boundaries would
evict three incumbents, one of them a live blocker, on behalf of the one use that does not need it.

**So: `TB<n>` for boundaries; the three incumbents untouched.** Cheap to reverse if you disagree —
one document, one prefix.

**M5 pairs recorded:** `B<n>` as boundary ≠ as blocker ≠ as bug-hunt defect ≠ as sentinel sweep
section · `thirteen boundaries` ≠ `thirteen crossings` · `an anchor resolves` ≠ `the list is
complete` · `the registers agree` ≠ `the registers are right` (see §5.3).

---

## 4. Step 5 — the quarantine notice

Inserted at `AGENT_SECURITY_AND_CAPABILITY_AUDIT.md:13`, above the body, below the existing
method block. Nothing renumbered, edited or removed. It states:

- The document audits `efc611d`, **which is `upstream/main`** — so it is **not stale in the ordinary
  sense**; it is the audit of record for the branch that would actually ship. It is **216 commits
  behind** `audit/adversarial-pass`. That framing is a correction to my own plan, which called it
  merely "213 commits back" and implied abandonment.
- Its two four-item lists are tabulated side by side — List A crossings at `:56-73`, List B
  transitions at `:91-104` — with the explicit statement that neither is a subset of the other and
  that neither can settle a count.
- `docs/TRUST_BOUNDARIES.md` is the current enumeration; prose uses unprefixed "boundary 6";
  `B<n>` is not the boundary namespace.
- "Nothing below this notice has been renumbered, edited, or removed."

---

## 5. Premises that did not hold (H4)

1. **`B12` is one stray row.** It is a twelve-row series with a cross-reference. §3.5.
2. **`B<n>` is available to claim.** It has four live-or-referenced meanings. §3.5.
3. **`AGENT_SECURITY_AND_CAPABILITY_AUDIT.md`'s four-item lists are the discriminator.** They
   support both candidate merges equally. §3.1.
4. **The audit document lives at `docs/`.** It is at the **repository root**. Both the revision and
   the supplement cite it as `docs/AGENT_SECURITY_AND_CAPABILITY_AUDIT.md`.
5. **The checkpoint is 973 lines.** 972. §2.
6. **The checkpoint cites `9fae051`.** It does not — zero occurrences. The `9fae051` fact is true
   and came from the supplement's own prose plus the repository, not from the checkpoint. A small
   thing, but it was offered as one of the three facts about the document.

---

## 6. What I did not verify

1. **Completeness of the twelve.** Carried `(U)`. What would settle it: an exhaustive call-graph
   pass from every process entry point to every sink. Nobody has done it; I did not either. The
   twelve anchors are `(M)`; that they are *all* of them is not.
2. **Whether the working numbering ever was twelve.** My reconstruction explains the offset and
   survives a competing hypothesis, but no document predating §1.3 enumerates twelve, so I cannot
   show the prose authors held the list I rebuilt. What would settle it: the author of
   `docs/ADVERSARIAL_PASS_2026-09-09.md`'s boundary-6/7 reconnaissance.
3. **The checkpoint's other claims.** I verified its SHAs, its anchors, its stated branch/HEAD/tree,
   and §1.3. **I did not audit the remaining ~940 lines** — its coverage tables, its LOC figures,
   its §9/§13/§15 lists. Committing it is not endorsing it. One known delta already: §1.3's framing
   text claims *"Goroutine launch sites in product code: daemon 13, tui 4"*; I measured **daemon 12,
   tui 2** for `go func` (28 and 6 if `go ident(...)` launches count). The counting rule is not
   stated, so I cannot say which of us is wrong — `(U)`, and now citable against a committed source.
4. **No CI was run or triggered.** No `gh run rerun`, no `workflow_dispatch`. The `gates`-only
   consequence of four markdown commits is read from `build.yml:43`, not observed.
5. **I ran four gates, not `make check`.** `make docs`, `make debtmarkers`, `make parity` and
   `docs-coderefs.sh`/`docs-links.sh` directly — all exit 0. I did **not** run `race`, `lint`,
   `ratchet`, `errcheck`, `evalguard`, `supplychain`, `webview`, `fmt`, `vet`, `crossvet`, or
   `hookcheck`. No product code changed, but that is a reason to expect them green, not evidence.
6. **Chains traced to the end:** every cited SHA to a commit object; all twelve boundary anchors to
   the named line; the `B<n>` collision to all four of its uses; the doc-gate failure to its cause
   at `scripts/docs-coderefs.sh:67`. **Not traced:** what `daemon/provider.go`'s stream path
   actually does (TB4's Site cell has no line number and I did not give it one); whether TB9's
   `index_cmd.go`/`context.go` pair is one crossing or two, which is the same question B1/B2 posed
   and which I did not re-ask of TB9.

### Self-corrections (H3 — one caught before reporting, two not)

1. **Caught before reporting.** My SHA extractor's first pass would have reported two unresolvable
   seven-character strings. Known-answer testing identified them as decimals before the number
   reached this document. H2 earned its place in the method on its first use.
2. **Withdrawn during the chunk.** I planned to follow the instruction and rename `OPEN_ITEMS`'s
   B12. Wrong evidence: I had grepped for the single line the revision named rather than for the
   series around it. Reading `:354-365` showed twelve rows. The instruction is not carried out and
   §3.5 says why.
3. **Withdrawn during the chunk.** My plan called the audit document "213 commits back" and treated
   it as stale. Two errors: the count is **216** at current HEAD, and `efc611d` **is** `upstream/main`,
   so the document is the audit of record for the shipping branch rather than an abandoned artefact.
   The quarantine notice says the latter; my plan did not.

---

## 7. Still needs a person, not a repository

Unchanged from the recon report except where C0b closed something. Nobody below has been contacted.

| What | Of whom | Time |
|---|---|---|
| Confirm `TB<n>` over evicting three `B<n>` incumbents (§3.5) — or tell me to reverse it | repo owner | 5 min |
| Whether the twelve need a completeness pass, or stay `(U)` by decision | whoever sets priorities | 10 min |
| The manual terminal session, still handed to no one since 2026-09-04 | a named tester on Linux | ~40–60 min |
| The five `MACOS_*` secrets, or accepting unsigned darwin in writing | whoever holds repo settings | 15 min |
| `daemon/CHUNK_SCRUB_DESIGN.md`'s status line — five Go files cite it as the decision it disclaims | the `daemon/` owner | 10 min |

**Next chunk: C1, in its reduced form.** Its Step 1 quota check must run before anything spends
Actions minutes. Owner grant on record: **at most three reruns, Linux only, zero Windows**, and
zero is an acceptable outcome.
