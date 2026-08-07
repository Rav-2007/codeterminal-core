# Decision pack — eight rulings only the founder can make

**2026-08-01. Status refreshed 2026-08-07.** **Seven are open; D4 was taken on
2026-07-27 and is kept below as a closed record, not a question.** Each brief is
one page: what is actually at stake, what the measured evidence says, a
recommendation, and what it costs to be wrong.

> **Why a taken decision stays in this file.** Deleting it would lose the
> evidence and the reasoning, and this pack is the only place either is written
> down. Leaving it *unmarked* was worse: it sat in a queue of open questions for
> ten days after it had been implemented and shipped to the wire, so every reader
> of this list counted the queue one item too long. Taken decisions are marked
> **TAKEN** in the table and banner-stamped in their own section.

They are gathered here because eight open questions scattered across a 4,300-line
backlog is eight separate sittings, and a decision queue nobody can enumerate is a
decision queue that never drains. The register (`docs/OPEN_ITEMS.md`) holds
everything engineering can clear on its own; this holds everything it cannot.

**Two of these gate other work.** D1 (socket auth) and D2 (Gate 6) are the P3
security gate, and the P3 gate blocks all new capability work. Nothing else on this
list blocks anything.

| # | Decision | Status | Recommendation | Blocks |
|---|---|---|---|---|
| D1 | Socket auth model | open | **Accept same-uid** | the P3 gate |
| D2 | Gate 6 formal closure | open | **Rule it closed** | the P3 gate |
| D3 | Gate 7 error unification | open | **Reject the unification** | the P3 gate |
| D4 | `allow_fallbacks` / F1 posture | **TAKEN 2026-07-27** | option 3, and it shipped | nothing |
| D5 | Warn-mode Design B vs C | open | **Reject B; defer C** | nothing |
| D6 | Default model | open | **Decide after the §4 measurement** | reply quality |
| D7 | Shared confinement package | open | **Do not build it yet** | nothing |
| D8 | Skills subsystem | open | **Delete it** | nothing |

**The three cheapest sittings, in order.** D1+D2+D3 are one sitting and unblock
the P3 gate, which blocks every new capability. D8 is a deletion of a subsystem
confirmed to have no callers. D5 and D7 are both "do nothing yet" and cost only
the act of saying so.

Phase 4 packaging is deliberately not on this list: its direction is already
decided and what remains is execution, not a ruling.

---

## D1 — Is same-uid-implies-trusted the socket's auth model?

**At stake.** Whether `authorizePeer`'s rule — the connecting process must run as
this daemon's uid, verified by the kernel — is the *final* answer or an interim one
pending something stronger.

**Evidence.** The credential comes from the kernel at `connect()` time
(`SO_PEERCRED` on Linux, `LOCAL_PEERCRED` on macOS as of this pass), so it cannot
be forged on the wire. It fails closed on every path: an unsupported platform, a
conn exposing no credentials, or any syscall failure is a refusal.

The question is what a *stronger* rule would buy. The Gate 4 analysis answered
this live and the answer is uncomfortable for the "compromised dependency" scenario
the stronger rule exists for: a process running as you **is** you to the kernel. It
can read `/proc/<pid>/environ` for the API base, `/proc/<pid>/cmdline` for the
workspace, the 0600 lockfile for the daemon PID, and every file the daemon can
read. Anything above same-uid — a shared secret, a token — lives in a file that
same process can read too.

**Recommendation: accept same-uid as the model, and say so in `SECURITY_MODEL.md`
rather than leaving it implicit.** It is the strongest boundary the OS offers for
a local socket without inventing key management that the threat model itself
defeats.

**Cost of being wrong.** If a future client is *not* a local same-uid process — a
remote IDE, a container, a service account — this rule is exactly wrong and the
decision has to be reopened. That is a real possibility and the reason to write the
model down: so the next person adding a transport knows they are changing an
assumption rather than adding a feature.

---

## D2 — Formal closure of Gate 6

**At stake.** A ruling, not a fix. There is no engineering task left.

**Evidence.** The in-process half (`d96794e`) serialized apply/undo per workspace
root; all five repros went to 0% with regression tests that fail when neutered.
M4 then found that half was insufficient — `applyLocks` was a `sync.Map` on
`*Server`, while the CLI and TUI are *separate processes* — and `2a389c7` completed
it cross-process with `flock`. Reconciled 2026-07-27; the four locations that
disagreed now point at one status.

**Recommendation: rule Gate 6 closed.** The engineering has been complete since
`2a389c7` and re-verified since. It stays open only because closure is a signature.

**Cost of being wrong.** Effectively none in the fix's direction — it is on `main`
and tested either way. The cost of *not* ruling is ongoing: Gate 6 sits under the
P3 gate, so an unsigned closure keeps blocking capability work for a reason that no
longer exists.

---

## D3 — Should Apply/Undo unify their error responses? (Gate 7)

**At stake.** The last engineering item under FAIL-3, and the third of the three
things holding the P3 gate.

**Evidence — measured this pass, `e4ff6c1`.** Driving the real Apply handler across
ten filesystem states yields nine distinct refusals:

| State | What the user is told |
|---|---|
| absent | does not exist; to create it, send an empty SEARCH section |
| absent nested | same, naming no directory |
| present, no match | search text not found in `<file>` |
| unreadable | permission denied |
| secret-named | matches the indexer's secret-file rules |
| symlink outside root | resolves outside the workspace root |
| a directory | is a directory |
| escapes the root | escapes the workspace root |
| protected directory | inside `.git/`, refusing to edit it |

Two facts decide this. **None of them leaks a path** — `3aeb8b6`'s scrub holds, now
pinned across all ten states. And **the only party who can reach this surface is an
authenticated same-uid peer**, who per Gate 4's live analysis can `lstat` everything
these messages disclose.

Against that, unification costs all nine. The first is *instructional* — it is how
a model learns the create protocol. The third is the commonest real failure a user
must act on. The secret-file and protected-directory refusals are policy
explanations that `editapply`'s own doc comment requires be shown verbatim.

**Recommendation: reject the unification. Close Gate 7 on the hygiene fix that
already landed.** Trading nine actionable messages for zero incremental protection
against the only reachable adversary is a bad trade, and calling it a security
improvement would be a mislabel.

**Cost of being wrong.** If socket access is ever widened beyond same-uid — a
remote transport, a shared daemon — the premise fails and this must be revisited.
The enumeration test is written so that day is cheap: it names the nine messages
that would have to collapse.

---

## D4 — `allow_fallbacks` and the proxy's ZDR posture (F1) — **TAKEN**

> ### ✅ DECIDED 2026-07-27 · option 3 · IMPLEMENTED AND ON THE WIRE
>
> `models.json` ships `provider_ignore_list: ["DeepInfra"]` and
> `provider_sort: "price"`; the per-turn served-provider surface shipped on both
> clients. F1 is **verified live by wire probe** (403 `zdr_required`).
>
> **Nothing is asked of the founder here.** The brief below is the reasoning as
> it stood before the ruling, kept because it is the only written record of the
> evidence — not because the question is still open. It went unmarked for ten
> days and was counted as open work in three separate documents; that is the
> cost this banner exists to prevent.
>
> **Residual, unchanged by the ruling:** option 3 *reveals* a non-ZDR fallback,
> it does not *prevent* one. Only `allow_fallbacks: false` prevents it, and that
> re-breaks the congestion fix. If the requirement ever becomes prevention, this
> is reopened.

**At stake.** Availability against the strongest *provable* ZDR guarantee.

**Evidence.** `models.json` ships `allow_fallbacks: true`. Our side is provably
clean and fails closed — verified live on both paths, one outbound body per attempt
carrying all three fields, retry reusing the routing object, `privacy_refused`
non-retryable, and the proxy forwarding byte-for-byte. F1 was **verified live by
wire probe** (403 `zdr_required`), and `provider_ignore_list: ["DeepInfra"]` is on
the wire.

The residual is OpenRouter-side and unverified at the exact edge: their ZDR doc
says `zdr:true` routes only to ZDR endpoints and is silent on how that interacts
with `allow_fallbacks`. Fallback is not dormant — it was turned on to escape
DeepInfra 429s, which is why option 1 is not free.

**Recommendation: option 3 — surface the served provider per turn.** The client
half already shipped (§2D), so what remains is mapping provider → ZDR verdict. It
keeps the availability that fallback bought and replaces an unverifiable assumption
with an observation the user can see.

**Cost of being wrong.** Option 3 does not *prevent* a non-ZDR fallback, it reveals
one. If the answer must be prevention, option 1 (`allow_fallbacks: false`) is the
only one that provides it, and it re-breaks the congestion fix.

---

## D5 — Warn-mode Design B vs C (opaque-secret redaction)

**At stake.** Whether to start redacting secrets with no recognisable prefix.

**Evidence.** Design B was measured and the numbers are decisive against it: it
fires on **33% of chunks** at **zero precision** on this corpus. Structural
signatures stay; entropy and keyword heuristics remain log-only, and the log-only
half was audited and passes (no raw secrets, stderr only).

**Recommendation: reject Design B. Defer Design C until there is data that
distinguishes it from B.** A redactor that corrupts a third of a user's code to
catch nothing is worse than the gap it closes — and this is a coding assistant,
where users legitimately paste key-shaped identifiers.

**Cost of being wrong.** The gap stays open and is stated: opaque secrets with no
prefix are not caught, on the chunk path or the tool-output path. That is written
into `toolresult.go`'s header and `SECURITY_MODEL.md`, not buried.

---

## D6 — The default model

**At stake.** Repeatedly named the single biggest reply-quality lever, deferred for
weeks.

**Evidence.** Currently absent, deliberately: this is item 23 in the register's §4
and needs real-model spend, now authorized under a $5 ceiling. One finding already
constrains it — the embedding discrimination ceiling (raw similarity clustering at
0.0147–0.0164, a ~1% spread deciding top-5 membership) means remaining
edit-shaped misses sit at ranks #78 and #307 and are ceiling-limited by the
*embedder*, not the model. A better model will not fix retrieval.

**Recommendation: do not decide this before the §4 measurement lands.** Deciding it
on intuition is how it stayed deferred for weeks — there was never a number to
argue with.

**Cost of being wrong.** Reply quality is the product. Choosing on vibes risks
paying more for no measurable gain, or shipping a cheaper model whose regression
nobody notices until users do.

---

## D7 — A shared confinement package

**At stake.** There are now **four** independently written confinement guards.
Consolidating means a genuinely new shared package, because `editapply` cannot
import `daemon`.

**Evidence.** No correctness gap has been found between the four. Every audit that
looked — FAIL-1, FAIL-2, S1, S2, the conformance suite — found them consistent, and
where one was wrong (`.GIT` case-fold, the undo symlink escape) the fix was local
and the others were already right.

**Recommendation: do not build it yet.** Four consistent implementations under a
conformance test are safer than one shared package written during a launch push and
depended on by four call sites. Revisit when a fifth writer appears, or when an
audit finds the four actually diverging.

**Cost of being wrong.** A future fix lands in three of four places. That is a real
maintenance risk, and the mitigation — `editapply/confinement_conformance_test.go`
— already exists and should be extended rather than replaced.

---

## D8 — The skills subsystem

**At stake.** `daemon/skills.go` is fully built (`AddSkill`/`GetSkill`/
`ListSkills`/`DeleteSkill`, a per-user SQLite store, a CLI) and **completely
unused**. Nothing calls it.

**Evidence.** It is dead code with a database, a schema, a migration path and a
maintenance cost. It appeared in this pass's security work twice — once for its
file permissions, once for its sidecar handling — which is exactly the tax dead
code charges.

**Recommendation: delete it.** The capture loop it exists for is gated behind the
P3 review and unscheduled; when it is scheduled, this will be rewritten against
whatever that design turns out to need. Git remembers it.

**Cost of being wrong.** If the self-learning loop is built soon, this is thrown-
away work — but it is work that would need rewriting against the real design
anyway, and keeping it costs an audit surface in every security pass until then.

---

## What is not on this list, and why

- **OpenRouter ZDR retention** — external, awaiting their answer. Not a decision,
  a dependency.
- **Phase 4 packaging** — direction already decided (managed-key billing, bundled
  daemon, cross-platform binaries, signed `.vsix`). What remains is execution.
- **C3's exact metering of an aborted stream** — needs either draining the upstream
  or a usage-reconciliation sweep. Both are projects, both are recorded, neither
  needs a ruling to start.
