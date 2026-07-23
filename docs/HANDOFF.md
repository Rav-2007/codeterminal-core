# Mochiii — Handoff Checkpoint v5

**Date:** 2026-07-23
**Repo state at synthesis:** `main` @ `099c165`; local is **55 commits ahead of `origin/main`** (nothing pushed — by choice, as every prior tier has been). *(HEAD advanced from `3d77ce9` → `099c165` mid-synthesis: a concurrent session committed the provider-identity-surfacing slice — see §3G. This document reflects the post-commit state.)*
**Purpose:** Complete context transfer. A fresh session should be able to read only this document and know what Mochiii is, what's done, what's open, and what to do next — without re-deriving it from `BACKLOG.md`'s running log.
**Supersedes:** v4 (2026-07-21) and earlier. v4 was never tracked in the repo; this one is (see "Where this document lives").
**What this document is NOT:** a closure event. It is a synthesis of state. Nothing here closes an item or resolves an open founder decision — it records that they are open. Closure is the founder's call, as always.

---

## Where this document lives, and why

**Decision: v5 is committed into the repo at `docs/HANDOFF.md`.** This is a deliberate change from v4, which was a repo-external artifact in `~/Downloads/` referenced from inside `BACKLOG.md` with no trace of where to find it. That absence had a concrete, measured cost: the last full accounting of remaining work had to be reconstructed from `BACKLOG.md` alone — a running log, not a synthesis — because there was no durable synthesis to read. Committing v5 gives the checkpoint a canonical, discoverable, versioned home that moves with the code and can't silently drift or vanish. Future checkpoints should update this file (or supersede it in place) rather than spawning another external copy. Older external handoffs (`~/Downloads/mochiii-handoff-checkpoint-v4.md` and predecessors) remain as historical artifacts but are no longer the source of record.

---

## Verification standard for this synthesis (live-verified vs. inherited)

Per this project's discipline (Part 0, rule 3), claims are not carried forward as fact from memory or from v4's text. This document separates the two:

- **Live-verified this session (2026-07-23):** repo/branch state and unpushed count; presence on `main` of every commit cited below (`099c165`, `d96794e`, `3d77ce9`, `eee2f03`, `57c95f7`, `2241daa`, `6618dda`, `0599a11`, `517c069`, `ccf8b8d`, `3aeb8b6` — all confirmed ancestors of `main`); that `8d37a6c` is **dangling** (not an ancestor of `main`) and its own message reads "mark FAIL-3 Gate 6 CLOSED … 442d018"; that shipped `daemon/models.json` sets `"allow_fallbacks": true`; that its `primary` note still reads "~$0.11/$0.80 per 1M" (the flagged-stale price); that the provider-surfacing slice (`TokenResponse.Provider` + both clients + tests + its BACKLOG entry) **landed as commit `099c165` on `main` during this synthesis** — 10 files, 393 insertions (see §3G); and that the Gate-6 closure contradiction still reads as four disagreeing locations.
- **Inherited (attributed, not re-run):** the audit reproduction rates (100%/68%/98%/88% → 0%), the TTFT measurements, the ZDR-retention/caching finding, the token-efficiency 67.4% figure, the live daemon/pty/webview verification of Tier 4 and Tier 3 — all from `BACKLOG.md`'s dated entries and the prior correction reports. These were verified live *when written*; this synthesis did not re-execute them. Where a number is load-bearing, its source entry is named.
- **Could not be re-read:** the "remaining-work inventory report (as of `main@57c95f7`)" referenced in the synthesis brief was **not recoverable on disk** — it lived in a prior conversation, and the task-output file that referenced it had been regenerated. Its bucket structure (§3 below) is therefore reconstructed directly from `BACKLOG.md` plus live checks, not copied from that report. Stated so the reconstruction isn't mistaken for a faithful transcription.

---

## PART 0 — READ THIS FIRST (how to work on this project)

This project runs on a specific, consistent discipline. Following it is why the codebase is in good shape.

1. **Audit first, fix second, document third.** An independent "beta tester" pass proves claims with *live execution* (not code reading), produces a PASS/FAIL/PARTIAL findings table with reproducibility rates, and hands findings back. Fixing is a separate task; documenting a third.
2. **Never round up.** "Mostly works" is not "works." A 3%-of-the-time bug is not an always-bug. 4-of-5 fixed is reported as 4-of-5. Severity is corrected in *both* directions (FAIL-2 went Moderate→High once exploited live; the OpenRouter concern got *less* severe once the actual request code was checked).
3. **Verify against real execution, never memory or comments.** This project has a documented history of doc/comment claims that were false and only caught by running the code.
4. **A fix's test must enter through the same door the user does.** Learned the hard way: Tier 2's Fix 7 (file creation) passed every engine-layer test while being *unreachable from every shipped client* because a parser rejected the input first. Test through production entry points.
5. **Nobody but the founder closes an item.** Every task — audit or fix — stops at "implemented and verified." Closure is the founder's call.
6. **Isolated commits per concern.** Don't bundle unrelated changes.
7. **Two recurring hygiene hazards:** `daemon/models.json` / root `models.json` are **untracked** (model-tier config, no secrets) and have been swept into commits twice via `git add -A` — keep them untracked, stage explicitly. And pasted-transcript drift accumulates in `BACKLOG.md` — check before editing.

**Build note:** `./...` fails from the repo root — the root isn't a module. Name the six `go.work` module paths explicitly (`daemon`, `editapply`, `protocol`, `clients/tui`, `helper`, `proxy`). Rebuild affected binaries after cross-module changes (daemon + TUI + extension drift from source otherwise).

---

## PART 1 — WHAT MOCHIII IS

A **security-first, retrieval-grounded AI coding assistant** that runs as a local daemon on the developer's own machine. Product thesis: **trust is a feature** — an agent with write access to your code and your inference credential should be held to safety-critical engineering standards.

**Architecture:**
- **Local Unix-socket daemon** (`daemon/`) — serves CLI, TUI, and VS Code clients from one shared indexed understanding of the workspace ("one brain, thin clients"). Socket is `0600`, peer-authenticated via `SO_PEERCRED` (Linux; macOS intentionally refuses all connections pending its own implementation).
- **Retrieval/indexing** — chunks and indexes the codebase; hybrid retrieval (vector + FTS5 lexical, RRF-max fusion K=60, class-aware rerank).
- **Edit engine** (`editapply/`) — applies model-proposed edits with path confinement, backups, and multi-run undo. Now supports file creation end-to-end (Tier 2.5).
- **Secret detection** ("warn-mode") — filename-pattern gate + entropy/keyword content heuristics. Structural-signature scrubbing is live at retrieval time; entropy/keyword layers are **log-only** pending a founder redaction decision.
- **Inference routing** — two paths to OpenRouter: direct (`daemon/provider.go`) and a managed proxy (`proxy/main.go`, on Railway). Both send `zdr:true` / `data_collection:"deny"` / `allow_fallbacks:true` on every request.

---

## PART 2 — WHAT'S DONE (verified with live execution; commits on `main` unless noted)

### 2A. Security review — implemented & verified across two axes (NOT closed — founder's gate)
The P3 security review is the **blocking release gate for all new capability work**. Both axes were reviewed and hardened; neither is *closed* (that's the founder's ruling), and one sub-item carries an unresolved documentation contradiction (§3E).

| Item | Result | Commits |
|---|---|---|
| Warn-mode fire-rate accumulation | Durable JSONL sink + classification | `064a00a`, `6028d96` |
| FAIL-1 — secret-name policy breadth | Broadened (non-RSA SSH keys, `.npmrc`/`.netrc`/`.pgpass`, kubeconfig, `*.pfx`, `*.tfstate`, service-account JSON); case-fold bug fixed | `959a882`, `ade065a` |
| Chunk-content scrub at retrieval (structural signatures) | Live at `renderChunk`; opaque/novel secrets still open (Designs B/C, log-only) | (2026-07-18) |
| FAIL-2 — undo-path confinement (`restoreOne`) | Fixed; severity corrected Moderate→High | `4de7bd4`, `07c59a4` |
| `ensureGitignoreEntry` (5th writer) | Fixed (found in FAIL-2 writer sweep) | `dbc4e6c` |
| `O_NOFOLLOW` hardening, all 8 writers | Fixed | `147a7b3` |
| FAIL-3 Gate 3 — socket peer auth | `SO_PEERCRED`, fail-closed (Linux; macOS refuses all) | `517c069` |
| FAIL-3 Gate 5 — DoS limits | 16 MiB cap, 60s idle deadline, 128-conn ceiling | `ccf8b8d` |
| FAIL-3 Gate 6 — concurrency | Per-workspace in-process lock; reframed from "TOCTOU amplifier" to five real data-integrity races (100%/68%/98%/88% → all 0%, fail-when-neutered) | `d96794e` |
| FAIL-3 Gate 7 — error leakage | Paths scrubbed at socket boundary, upstream errors generalized; severity verified Informational/Low | `3aeb8b6` |
| `server.go` reorg into per-concern files | Zero behavior change | `14502e5` |

**Still open on this axis (see §3):** the auth-model founder decision (is same-uid-implies-trusted the accepted model?), the Gate-6 closure contradiction, Gate-7 existence-oracle distinguishability (flagged, not fixed), and several low-severity hardening residuals (mid-ancestor TOCTOU, `-wal`/`-shm` sidecar symlink surface, `id_rsa_secret.pub` over-refusal).

### 2B. Correctness & effectiveness — Tiers 1, 2, 2.5, 3 merged to `main`
All merged fast-forward, no merge commits, validated with real `go build`/`vet`/`test`/`-race` across all six modules.
- **Tier 1 (critical):** partial-apply/undo atomicity ("report must match disk", both directions); sensitive-target exclusion (model output could write `.git/hooks/*` → code execution, or into `.codeterminal/backups/before/*` → the undo net itself); `=======` parser split that silently applied *wrong* edits with `applied:true`. (`16e84a1`, `83244ec`, `3f9352f`)
- **Tier 2 (high):** re-index after apply; tiered SEARCH matching with change-detection split out; file-creation engine; helper-binary path via `os.Executable()`; classified error taxonomy; bounded jittered retry with body-read 429 classification. (`07e97d1`, `1066d91`, `a02485c`, `0cf13cc`, `46d9e1c`, `10ba174`)
- **Tier 2.5 (the merge spot-check catch):** Fix 7 file-creation was **unreachable from every client** (parser rejected empty SEARCH before the engine saw it); worse, one create block failed the whole response, silently dropping every other edit. Fixed: parser accepts create blocks, per-block recovery, undo of a created file now *removes* it (was leaving a 0-byte file while reporting "restored"). (`36f9bbe`, `7215dc1`, `bd43e90`, `c18782c`)
- **Tier 3 (reply quality):** chunk merging; `file:line` → span resolution (referenced-location recall 0/6 → 6/6, verified reachable on a live daemon); history byte-budget + empty-turn poison closed both sides + truncation flag on wire; empty-prompt rejection, `delta.reasoning`, path-line variants. (`fb0770c`, `83f1bbc`, `ef1f5a9`, `f4ad82e`, `c68de0a`) Doc: `fccadbc`.

### 2C. Tier 4 — the operability cluster (NEW since v4; done & verified, NOT closed)
Closes the A6–A9 pattern: *the daemon degrades gracefully and honestly in its logs, but presents as healthy on the wire.* Three isolated commits, each reproduced live on current `main` first, then fixed, then validated through production entry points (raw socket bytes, the **real TUI driven in a pty**, the **real `media/main.js` render path**), plus the full six-module regression incl. `-race`. Doc commit `e779326`.
- **C1 — fail-fast config validation (`0599a11`).** Unparseable/schemeless/hostless/non-HTTP `API_BASE` and a missing/regular-file `--workspace` now **FATAL before listening**; unknown/out-of-range config keys **warn and continue** (clamped, named individually). Reachability deliberately not probed (it's runtime state).
- **C2 — wire-visible degraded-state signals (`6618dda`).** New `TokenResponse.Degraded []Degradation` on the pre-token message: `lexical_retrieval`, `memory`, `provider_routing`. `provider_routing` is **config-derived** (fallbacks *permitted*), not a per-request fallback claim (that would be fabricated — see §3D). **Both shipped clients render it** — the Fix-13 and Tier-2.5 traps avoided together.
- **C3 — minimal observability surface (`2241daa`).** `StatusRequest`/`StatusResponse` over the existing 0600 + `SO_PEERCRED` socket (NOT an HTTP port — the daemon never listens on the network) + a `status` CLI; reports the two retrieval tiers separately; carries no API base (Gate-7 discipline). Plus `--log-file`, 5 MiB size-rotated, tee'd to stderr. **Scope call-out:** a log *file*, **not log levels** (deliberate).

### 2D. Other done, load-bearing context
- **Supabase auth/RLS/grants posture — closed 2026-07-17** (`SECURITY_MODEL.md`), verified with anon key + real user JWT (not `service_role`). One residual mechanism logged unresolved: `ALTER DEFAULT PRIVILEGES` does not fix *functions* — the process control is "every `SECURITY DEFINER` function in `public` must revoke EXECUTE from PUBLIC in the same migration."
- **Managed proxy Step 1** — pass-through proxy on Railway, OpenRouter key server-side only, content-free logs, ZDR held on the wire (`702101c`, `8b5aa11`). **Standing risk: no auth yet — do not expose the URL publicly** until Step 2 (auth/metering).
- **Hybrid lexical+semantic retrieval** (`ae3e7a4`) — live-verified; two honest caveats stay open (eval-set saturation; token-efficiency measured at **67.4%** average, not the un-measured ~95% floated externally, with a real ~15%-of-queries negative-savings failure mode, investigated and deliberately not fixed — `RETRIEVAL_BUDGET_DESIGN.md`).
- **Conversational memory** (in-session + cross-session SQLite), **bounded backups + multi-run undo**, **FTS5 conversation search** (VS Code panel), **auto-apply-with-undo** (VS Code) — all built and live-verified (see `BACKLOG.md` "Known deferred debts" (c)/(d)/(i) and the 2026-07-09 sections).

---

## PART 3 — WHAT'S OPEN (reconstructed bucket structure — see verification note above)

The synthesis brief asked for the remaining-work inventory's five buckets. That report wasn't recoverable on disk, so these buckets are rebuilt from `BACKLOG.md` + live checks.

### 3A. The one external blocker — OpenRouter ZDR (open)
The single external dependency. Two linked sub-questions, both "documented engineering position in prose, not a contractual guarantee in the DPA/ToS":
1. **Implicit prompt-caching path** — does the ZDR guarantee cover it? A ZDR-labelled provider was *measured* retaining prompt content across requests (2026-07-17: ~98% of provably-novel content served from cache, corroborated by a 78.6% billing discount). The worst surface (OpenRouter's 24h edge response cache, explicit `cache_control`) is confirmed **off** — verified from the actual outbound request code, both paths. What remains is derived KV-cache tensors with short TTLs. Details: `SECURITY_MODEL.md#the-inference-hop--zdr-retention-finding`.
2. **Fallback-provider ZDR posture (NEW since v4 — extends 3A, `57c95f7`/`eee2f03`).** Shipped `models.json` sets `allow_fallbacks:true`. **Our side is provably clean and fails closed** (D1, live both paths): exactly one outbound body per attempt always carrying all three fields together; retry reuses the same routing object; `privacy_refused` is non-retryable; the proxy forwards byte-for-byte; the `allow_fallbacks:false` lever transmits faithfully. The **residual is OpenRouter-side and unverified at the exact edge** (D2): the ZDR doc says `zdr:true` routes "only to ZDR endpoints" (absolute language) but is silent on the `allow_fallbacks` interaction and the no-ZDR-provider case. Severity **Low but unverified-at-the-edge, not dismissable** — fallback is not dormant (it was flipped on specifically to escape DeepInfra 429s, so it fires in normal operation).
   - **D4 remediation options (no option selected — founder's product-posture call):** (1) ship `allow_fallbacks:false` (strongest, re-breaks the congestion fix); (2) keep `true` + add an `order`/`only` ZDR-vetted allow-list (new maintained config); (3) surface the served provider per turn to the client; (4) leave as-is, documented (defensible only if outreach confirms the fallback set stays ZDR-filtered).
   - **Correction carried forward (`eee2f03`):** D4 option 3 originally overstated C2 and understated the daemon. Reality: **per-request provider identity is already obtained and logged today** — OpenRouter's SSE chunks carry a top-level `provider` field, decoded at `daemon/provider.go:74`, logged at `server.go:379`. It is **not** gated on the option-2 allow-list; the allow-list is needed only to map provider→ZDR-verdict, not to obtain the provider. What OpenRouter does *not* report is *whether* a fallback occurred, so "served by X" is truthful but "this turn fell back" is not.
- **Channels (unchanged):** support ticket **#37409** (narrowly scoped, near resolution — a poor fit for the new fallback-semantics sub-question, do not reopen it for that); a **Discord** `#community-help` thread escalated to mods; a **LinkedIn** technical-staff contact. The fallback sub-question folds into these same channels — a verbatim-ready question is recorded in the D2 backlog entry. Keep chasing; don't treat silence as a crisis.

### 3B. Actionable now (no external dependency, no founder decision required)
- **Provider-identity surfacing (D4 option 3, client half) — just landed as commit `099c165` on `main` (§3G). No longer "actionable"; its remaining residual is a real-OpenRouter confirmation, not code.**
- **`file:line` resolution unreachable when retrieval is disabled** — `gatherContext` returns early on `s.embedder==nil || s.store==nil`, and direct resolution sits below it, so a user with no index who pastes a compiler error gets nothing though they named the exact line. Likely small (hoist direct resolution above the early return). Open, unscoped.
- **Fix the stale `models.json` price note** — still reads "~$0.11/$0.80 per 1M"; the P2 cost investigation found it is ~2.3× off for at least one live provider (Io Net). Verified still stale this session.
- **TUI FTS5 search UI** — the wire+daemon half shipped for VS Code; TUI has no search box (mechanical, same `SearchRequest`/`SearchResponse`).
- **Client-render parity gaps (Tier 3):** `TokenResponse.History` (truncation) and `.Reasoning` reach the wire but no client renders them (~907 ms of dead air closed only at the protocol layer). Same client-side bucket as C2's render work.

### 3C. Blocked (gated on the P3 security review, i.e. on 3E + the auth decision)
All new *capability* work: the autonomous multi-step agent loop (largest single body of work, removes the human-in-the-loop safety property, highest token burn); editapply CREATE's own confinement review (CREATE removes the `EvalSymlinks`-requires-existence property Gate 2 leans on); the self-learning/autonomous skill-capture loop (skill store built, capture loop deferred). None is scheduled; each needs its own decision *after* the gate.

### 3D. Founder decisions (no task written; explicitly the founder's call)
- **Socket auth model** — is same-uid-implies-trusted accepted, or must the daemon verify its peer beyond `SO_PEERCRED`? This is the item that gates the socket axis of the P3 review.
- **Gate-6 closure ruling** — see §3E (packaged, not resolved).
- **`allow_fallbacks` posture** — the D4 choice above (availability vs. strongest-provable ZDR).
- **Warn-mode Design B vs. C** — redaction of opaque/novel secrets. Data is now durably accumulating (log-only) to inform it; the decision is the founder's. Do not flip entropy/keyword to redacting without it.
- **Default-model evaluation** — flagged repeatedly as likely the single biggest reply-quality lever; made *more* urgent by the embedding-discrimination-ceiling finding (raw similarity clusters at 0.0147–0.0164, a ~1% spread deciding top-5 membership → remaining edit-shaped misses sit at ranks #78/#307, not just outside the cutoff — further ranking/fusion/chunking is ceiling-limited until the embedder improves).
- **Shared confinement package** — there are now **four** independently-written confinement guards; consolidating means introducing a *new shared package* (`editapply` can't import `daemon`), not a helper extraction. No correctness gap; a maintainability call.
- **Skills subsystem** — fully built, completely unused: wire it in or delete it.
- **Self-hosting / Together AI** — brief exists; dedicated GPUs and ZDR are independent settings, only VPC/on-prem removes the vendor from the data path. Not worth the ops burden until volume/contract justifies it.
- **Phase 4 packaging (decided direction, not started)** — bundle+auto-manage the daemon, cross-platform binaries (Intel-Mac onnxruntime gap known), **managed-key billing model DECIDED** (Mochiii holds the key, users don't BYOK → commits to Stripe/metering), on-device embeddings per platform, signed `.vsix` publishing.

### 3E. The Gate-6 closure contradiction — OPEN founder decision (packaged, not resolved)
Packaged at `BACKLOG.md:703` ("FOUNDER SIGN-OFF NEEDED"). **Not in question:** the engineering fix (`d96794e`) is real, on `main`, verified live (all five repros 0%, fail-when-neutered). This is a **documentation/closure-process contradiction**, not an open engineering task. Four locations disagree:

| Location | Says |
|---|---|
| `BACKLOG.md:633` (audit header) | audit explicitly does **NOT** close Gate 6 |
| `BACKLOG.md:681` (same section) | "Gate 6 now closed" |
| `p3-security-review.md` (memory) | "Gate 6 CLOSED" |
| `MEMORY.md` (memory index) | "FIXED+CLOSED `d96794e`" |

The only commit that ever said "mark … CLOSED" (`8d37a6c`) is **dangling — never merged to `main`** (live-verified: not an ancestor of `main`; its message reads "mark FAIL-3 Gate 6 CLOSED (per-workspace serialization, 442d018)"). So the claim's origin was walked back, yet "closed" propagated into three of four locations. It matters because Gate 6 sits under the P3 gate that blocks all capability work. **The founder rules one way; docs then reconcile the four locations above (plus the dangling `8d37a6c` citation). v5 takes no position — it records the contradiction as open.**

### 3F. Small deferred / hygiene (all logged, none urgent)
- Two protocol-honesty gaps from Tier 2.5: `EditProposals` doesn't carry refused blocks; `UndoResponse.Restored` doesn't split removals from restores.
- Python tracebacks (`File "x.py", line 42`) unmatched by the `file:line` resolver; chunk end-lines overshoot by one on files ending in a newline (cosmetic label).
- Mid-ancestor TOCTOU (needs `openat2(RESOLVE_BENEATH)`); `-wal`/`-shm` sidecar symlink hardening (needs custom VFS) *and* separately the 0644-vs-0600 sidecar permission gap; `id_rsa_secret.pub`-style over-refusal (safe-failure direction).
- macOS peer-credential support (daemon refuses all non-Linux connections); undo leaves empty parent dirs a create brought into existence; C2-b memory not surfaced at handshake (only first prompt).
- **Quota fully refunded on aborted streams = repeatable unmetered inference on the paid tier** (billing-integrity; separately scoped) — flag this one for Phase 4.
- Retrieval-eval self-referentiality (indexes this repo → cross-commit comparisons drift; medium as a *measurement-validity* concern); the edit eval is effectively n=3 of its n=5 target.
- `models.json` (root + `daemon/`) untracked — decide whenever next in that area; keep untracked or commit, but do it deliberately, never via `git add -A`.
- Stale scratch git worktree from a prior P3-experiment session lingers on disk (harmless); the account must be kept funded (API credits) for any inference to succeed.

### 3G. Provider-identity surfacing task — ACTUAL STATUS: built, verified live (stub-labelled), **committed as `099c165` on `main`, NOT founder-closed**
The synthesis brief asked whether this was ever run, without assuming either way. **It was — and it landed on `main` as commit `099c165` during this very synthesis** (HEAD moved `3d77ce9` → `099c165`, verified live; earlier in this session the same work was still sitting uncommitted in the working tree, so this is a genuinely fresh landing worth pinning precisely). The commit is 10 files, 393 insertions:
- `protocol/protocol.go` (+18) — new `TokenResponse.Provider string json:"provider,omitempty"` with a full doc comment; `daemon/server.go` (+9) — emits it from the existing `onProvider` closure on its own message; `clients/tui/{stream,chat}.go` — a neutral `served by: <provider>` header line; `clients/vscode/{media/main.js,src/chatPanel.ts,src/daemonClient.ts}` — a `#provider` region via `textContent`; `daemon/response_robustness_test.go` (+55) and `clients/tui/provider_test.go` (+117) — guards; plus the ~104-line `BACKLOG.md` entry documenting it.
- Its own entry states: this ships **only the client-surfacing half of D4 option 3** (log-only → wire → rendered); it does **not** touch `allow_fallbacks`, build the option-2 allow-list, add any ZDR-status verdict, or resolve 3A/D4. Both traps avoided (a wire field no client reads; a fix unreachable from clients).
- **Verification is live but stub-labelled** (per the D4-correction honesty rule): every run used a **local stub that fabricates the `provider` value** — evidence the daemon reads the wire field and both clients render it, **not** evidence about what real OpenRouter returns (no OpenRouter/proxy key in this environment). The remaining real-key confirmation is that genuine OpenRouter responses carry the top-level `provider` field in practice (currently resting on the daemon's own `provider.go:67-73` "observed in practice, not formally guaranteed" comment + OpenRouter's streaming docs).
- **Net status:** implemented, locally verified, and now on `main` (`099c165`) — but, like every prior tier, **not founder-closed.** The one open residual is the real-OpenRouter provider-field confirmation above; it is documentation/verification, not code. v5 did not author, alter, or close this — it records the state it found (which changed under it, hence this precise note).

### 3H. The two hash-drift corrections (done; one line each, so they aren't rediscovered)
- **`442d018` → `d96794e`** — the Gate-6 fix-commit citation was wrong; corrected by `3d77ce9` (on `main`). Low-stakes hygiene.
- **`8d37a6c` dangling-commit citation** — the only "mark Gate 6 CLOSED" commit was never merged to `main`; live-verified dangling this session. Its walked-back status is exactly why §3E's contradiction exists. Low-stakes hygiene, but load-bearing for the contradiction.

---

## PART 4 — WHAT TO DO NEXT

- **Immediate, no dependency:** the provider-surfacing slice has landed (`099c165`, §3G) — its only follow-up is the real-OpenRouter provider-field confirmation (needs a live key). Fix the stale `models.json` price note while in that area.
- **The gate:** the P3 security review's founder decisions (§3D/§3E) — the socket auth model and the Gate-6 closure ruling — are what unblock all capability work (§3C). Nothing new should ship ahead of them.
- **In parallel, no dependency:** chase the OpenRouter ZDR reply (Discord mods, ticket #37409's verification step, LinkedIn follow-up), now carrying the fallback-semantics sub-question too.
- **When there's appetite:** the default-model evaluation (§3D) — the embedding-ceiling finding makes it the highest-leverage open lever — and the other founder-level design decisions.
- **The large body of work:** Phase 4 packaging (managed-key billing/metering, cross-platform bundling) — the "install from marketplace, no separate daemon" experience. Decided direction, not started; gated behind the security review for the parts that expose others to the daemon.

---

## PART 5 — WHERE THINGS LIVE

- **Status of record:** `BACKLOG.md` (running log — `P3-FAIL-1/2/3` with dated per-gate audit/fix subsections, Tier 1/2/2.5/3/4 sections, the D1–D4 fallback-ZDR entry, the Gate-6 sign-off note at `:703`, the confined-writer design-question entry, plus the provider-surfacing entry committed in `099c165`). **This file (`docs/HANDOFF.md`) is the synthesis of record.**
- **Security & design docs:** `SECURITY_MODEL.md` (Supabase posture + ZDR-retention finding), `CHUNK_SCRUB_DESIGN.md`, `RETRIEVAL_BUDGET_DESIGN.md`, `QUOTA_RESERVATION_DESIGN.md`, `SIGNAL_ESCALATION_DESIGN.md`.
- **Confinement:** `editapply/secret.go`, `editapply/apply.go` (`ResolveSafeTargetPath`), `editapply/create.go` (`resolveSafeNewPath`), `daemon/apply_cmd.go` (`restoreOne`, `confinedRestorePath`), `daemon/index_cmd.go` (`ensureGitignoreEntry`).
- **Edit parsing:** `editapply/editblock.go` (hardened `=======`; accepts create blocks; per-block recovery).
- **Warn-mode / scrub:** `daemon/chunkscrub.go`, `daemon/warnsink.go`, `daemon/context.go`.
- **Socket/daemon (post-reorg):** `daemon/server.go` (lifecycle/dispatch/handlers, `Serve` conn ceiling, `applyLocks`), `server_auth.go` (Gate 3), `server_limits.go` (Gate 5), `server_workspace_lock.go` (Gate 6), `server_errors.go` (Gate 7). Peer-cred readers: `peercred_linux.go` / `peercred_other.go`.
- **Inference:** `daemon/provider.go` (direct; `chatCompletionChunk.Provider`/`onProvider`), `daemon/retry.go` (`streamWithRetry`), `daemon/modelerror.go`, `proxy/main.go` (managed proxy).
- **Retrieval:** `daemon/chunker.go`, `retrieval_setup.go`, `rerank.go`, `vectorstore.go`, `lexicalstore.go`, `fileclass.go`, `fileref.go` (`file:line`), `reindex.go`.
- **Config:** root `models.json` / `daemon/models.json` (both untracked); `daemon/config.go` (`Config.Validate`, C1 warnings).
- **Memory (this project's notes):** `~/.claude/projects/-home-ravi-kiran-Desktop-Neww/memory/` — `MEMORY.md` index plus per-topic files (`p3-security-review.md`, `tier4-operability.md`, `chunk-text-network-exit.md`, `fail2-undo-unconfined-writer.md`, `warnmode-fire-rate-review.md`, `retrieval-diagnosis-h4-h6.md`). Note: `p3-security-review.md` and `MEMORY.md` are two of the four locations in the §3E contradiction.

---

## PART 6 — ONE-PARAGRAPH SUMMARY

Mochiii's security surface has been audited and hardened across both axes — editapply write-path (5 gates hold, FAIL-1/FAIL-2 fixed) and the Unix socket (peer auth, DoS caps, per-workspace concurrency serialization, error-path scrubbing) — and four tiers of correctness and reply-quality work are complete, including findings that would have been catastrophic in production (model output writing executable git hooks, a `=======` silently applying *wrong* edits under `applied:true`, apply/undo leaving a changed workspace while reporting nothing, file-creation shipped unreachable from every client). Newest since v4: **Tier 4's operability cluster** (fail-fast config, wire-visible degraded-state signals both clients render, a status surface + optional log file — done and verified, not closed) and the **fallback-provider ZDR investigation** (our side provably fails closed; the residual is an unverified edge on OpenRouter's side, folded into the same open 3A channels). What's left is not fixes but decisions: the founder-level P3 gate (socket auth model + the Gate-6 closure contradiction, which four locations still disagree about because the only "CLOSED" commit is dangling) blocks all new capability; the OpenRouter ZDR question is the one external blocker; and the highest-leverage lever is the default-model evaluation, which the ~1% embedding-discrimination-ceiling finding made more urgent. The provider-identity surfacing slice — built, locally (stub-)verified — **landed on `main` as `099c165` during this synthesis**, leaving only a real-OpenRouter confirmation residual. Nothing here is closed; this is a synthesis of state, and closure is the founder's call.
