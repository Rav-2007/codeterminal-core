<!-- coderefs: enforced -->
# Trust boundaries — the live enumeration

**Status: CURRENT. This is the canonical list.** Twelve boundaries. Established 2026-09-14 by
reconciling `docs/PROJECT_CHECKPOINT_2026-09-13.md` §1.3 (which lists thirteen) against the eight
prose references in `docs/ADVERSARIAL_PASS_2026-09-09.md`, `docs/HANDBACK_2026-09-12.md`,
`docs/RESIDUAL_RISKS.md` and `docs/DECISION_MEMO_2026-09-04.md` (which number MCP and LSP as
**6 and 7**). Owner decision, 2026-09-14: the canonical count is twelve; the prose is correct and
was not edited.

**Notation.** Boundaries are referred to in prose as "boundary 6", unprefixed, which is how all
eight existing references are written. Where a prefix is needed, use **`TB<n>`**. Do **not** use
`B<n>` — see §4.

---

## 1. The twelve

Every anchor below was verified at HEAD `1013e1b` on 2026-09-14 by reading the named line. `(M)`

| # | Crossing | Site | was §1.3 |
|---|---|---|---|
| TB1 | peer → daemon control socket | `daemon/server.go:241` accept → `daemon/server.go:276` handleConn; peer identity checked at `daemon/server.go:295` `protocol.AuthorizePeer` | B1 + B2 |
| TB2 | peer → helper socket | `helper/main.go:197` `protocol.AuthorizePeer` | B3 |
| TB3 | daemon → completion provider (egress) | `daemon/provider.go:579` POST | B4 |
| TB4 | provider → daemon (SSE / model output) | `daemon/provider.go` stream path | B5 |
| TB5 | model output → filesystem writes | `editapply/editpayload.go:68`, `editapply/editblock.go:59`, `editapply/unifieddiff.go:36` | B6 |
| **TB6** | **MCP server (stdio subprocess) → daemon** | `daemon/mcp/stdioclient.go:164` | B7 |
| **TB7** | **LSP server → daemon** | `daemon/lsp_bridge.go:257` | B8 |
| TB8 | web content → daemon | `daemon/webfetch.go:207`; SSRF guard at `daemon/webfetch.go:129` `Dialer.Control` | B9 |
| TB9 | workspace files → index → prompt | `daemon/index_cmd.go`, `daemon/context.go` renderChunk | B10 |
| TB10 | chunk text → egress (scrub choke) | `daemon/scrub.go:87` | B11 |
| TB11 | client → proxy (API-key authz) | `proxy/authcache.go`, `proxy/main.go` | B12 |
| TB12 | proxy → upstream (ZDR/F1 gate) | `proxy/main.go`, `FuzzZDRRoutingEnforced` | B13 |

**TB6 = MCP and TB7 = LSP, which is what the eight prose references already say.** That is the
test this renumbering had to pass, and it passes exactly.

**Caveat, carried verbatim from §1.3 and NOT upgraded by being copied here:** *"derived from
entry-point greps, not an exhaustive call graph. Completeness is (U)."* Copying a claim does not
measure it. The twelve `file:line` anchors are `(M)`; that twelve is *all* of them is `(U)`.
What would settle completeness: an exhaustive call-graph pass from every process entry point to
every sink, which nobody has done.

---

## 2. Why thirteen became twelve: B1 and B2 are one boundary

§1.3's own column header is **"Crossing"**. Twelve of its thirteen rows name a crossing in the
form `X → Y`. **One does not: B2, "peer identity check", names a *control*, not a crossing.** It is
the sole exception in its own table.

The table's own convention for a crossing that has a named control is to put the control in the
**Site** column beside it, and it does this twice:

- **B3** — `peer → helper socket` with `protocol.AuthorizePeer` in the Site cell. The same socket
  and the same check as B1/B2, filed as **one** row.
- **B9** — `web content → daemon` with the SSRF guard at `Dialer.Control` in the Site cell.

So B1/B2 is not a thirteenth boundary; it is the socket boundary with its control promoted into a
row of its own, against the table's practice elsewhere in the same table. Merging them restores
the convention and yields twelve.

---

## 3. The competing hypothesis, tested and rejected

Two observations ("MCP = 6", "LSP = 7") constrain only *"exactly one merge somewhere in B1–B7"*.
A second candidate fits the arithmetic equally well and had to be ruled out on evidence rather
than on preference:

**Candidate: merge B4 and B5** — `daemon → provider (POST)` and `provider → daemon (SSE)` are two
directions of one HTTP connection. Merging those also gives MCP = 6 and LSP = 7.

**Rejected, on three grounds:**

1. **Both are crossings.** Each is written `X → Y` and each satisfies the column header. Merging
   them deletes a real crossing; merging B1/B2 deletes a row that was never a crossing.
2. **They carry different risks with different controls.** Egress is where *secrets leave* — it is
   the reason TB10's scrub choke exists. Ingress is where *untrusted model output arrives* and is
   parsed. One control does not cover both, so one row cannot either.
3. **The table already treats direction as significant.** For MCP, LSP and web content it lists
   only the untrusted-**inbound** direction and no outbound row. Where it lists both directions —
   the provider — that is a deliberate statement that both matter, not a duplication.

**Note on the discriminator this reconciliation was told to use.** The instruction named
`AGENT_SECURITY_AND_CAPABILITY_AUDIT.md`'s two four-item lists as the discriminator. **They cannot
discriminate, and relying on them would have been a coin flip:** its diagram boundary 1 bundles
the unix socket with SO_PEERCRED (which supports merging B1/B2) *and* its diagram boundary 4
bundles the entire proxy network surface in both directions (which equally supports merging
B4/B5). A partition into four necessarily bundles more than a partition into twelve, so its
bundling is evidence about its own granularity and about nothing else. The real discriminator was
inside §1.3 the whole time: the column header, and the one row that does not satisfy it.

(That document is also located at the repository **root**, not under `docs/`.)

---

## 4. `B<n>` is not the boundary namespace, and must not become it

`B<n>` is already **four** unrelated things in this repository. It was not available to claim:

| Use | Where | Range | Live? |
|---|---|---|---|
| **Blockers** | `BACKLOG.md:88-91` | B1–B4 | **YES — B3, Apple Developer enrolment, is open** |
| **Bug-hunt defects** (2026-08-07 pass) | `docs/OPEN_ITEMS.md:354-365` | B1–B12 | closed, cross-referenced at `:367` |
| **Sentinel sweep section ids** | `daemon/sentinel_rows_test.go:57`, `daemon/sentinel_secrets_test.go:115,154,198` | B2.2c, B2.2e, B2.3a, B2.3b | **YES — in test comments** |
| **Trust boundaries** | `docs/PROJECT_CHECKPOINT_2026-09-13.md` §1.3 only | B1–B13 | superseded by this document |

Boundaries are the **newest** of the four and the only one whose live prose does not use the prefix
at all — all eight references say "boundaries 6 and 7", never "B6". So this document uses `TB<n>`
and leaves the three incumbents untouched.

**M5 phrase pairs recorded:**
- `B<n>` as trust boundary ≠ `B<n>` as blocker ≠ `B<n>` as bug-hunt defect ≠ `B<n>` as sentinel
  sweep section. One notation, four meanings, three of them live.
- `thirteen boundaries` ≠ `thirteen crossings` — §1.3's count included one non-crossing.
- `an anchor resolves` ≠ `the list is complete` — this document is `(M)` on the first and `(U)` on
  the second.

**On "eleven".** An "eleven boundaries" figure circulated in older tasking documents. It is a
conflation; per §1.3's own note, every other "eleven" in this repo counts something else (gate
claims, pin sites, model tiers, neuters). Do not propagate it.

---

## 5. What this document does not do

- It does not edit `docs/PROJECT_CHECKPOINT_2026-09-13.md`. That file was committed with
  "contents unaltered" in its message, and this repo deliberately leaves dated records unfixed.
  §1.3 stands as written; this document supersedes it and says so.
- It does not edit the eight prose references. They are correct.
- It does not renumber `BACKLOG.md`, `docs/OPEN_ITEMS.md`, or the sentinel test comments.
- It does not establish completeness. See the caveat in §1.
