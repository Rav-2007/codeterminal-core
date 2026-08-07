# Backlog — what is still ahead

**Next action: [`docs/MASTER_PLAN_2026-08-07.md`](docs/MASTER_PLAN_2026-08-07.md),
Stage 3 (packaging).** The code board is clear; the product board is not. The
project sits at roughly **77% engineering robustness / 27% product readiness** —
strong code that nobody outside this machine can install.

Stages 0 (make the record true) and 2 (the daemon lifecycle) are done. Windows
compiles with every seam written and is waiting on CI. **Packaging is now the
binding constraint**: `clients/vscode/package.json` still carries
`"private": true`, which `vsce` refuses outright, and there is no `.vsix`.

This file was **split on 2026-08-01**. It had grown to 4,333 lines and was two
documents wearing one name: a work log and a register. Its own opening paragraph
had gone stale — it told readers the next action was addressing P3 FAILs that had
been fixed weeks earlier. Completed records now live in
[`docs/ARCHIVE/BACKLOG_2026-07.md`](docs/ARCHIVE/BACKLOG_2026-07.md), verbatim.

**One document per job. Keep them separate:**

| Question | Document |
|---|---|
| Where do I start? | [`docs/HANDOFF.md`](docs/HANDOFF.md) — entry point; [`docs/README.md`](docs/README.md) catalogues everything else |
| What are we doing next? | [`docs/MASTER_PLAN_2026-08-07.md`](docs/MASTER_PLAN_2026-08-07.md) — **the current plan** |
| What bugs are open? | [`docs/OPEN_ITEMS.md`](docs/OPEN_ITEMS.md) — read the Status column; §1–§2 are resolved except 7, 10, 12 |
| What needs a founder ruling? | [`docs/DECISION_PACK.md`](docs/DECISION_PACK.md) — D1–D8; **D4 taken**, seven open |
| What was already done, and why? | [`docs/ARCHIVE/BACKLOG_2026-07.md`](docs/ARCHIVE/BACKLOG_2026-07.md) |
| What is still ahead? | **this file** |

Nothing here is marked CLOSED. Implemented-and-verified is where engineering
stops; closure is the founder's call.

## Known deferred debts

Debts (a)–(e) and (g)–(i) were completed and are
[in the archive](docs/ARCHIVE/BACKLOG_2026-07.md). These two are still live.

### (f) Retrieval: implementation chunks can rank below setup chunks — REFINEMENT
Observed during the full-repo (81-file) stress test. For "where does retrieval inject
context into the prompt?", retrieval surfaced the SETUP files (retrieval_setup.go, main.go,
context.go with the delimiter constants) correctly, but did NOT pinpoint the precise
implementation/injection line — the setup chunks out-ranked the actual injection-point chunk.

- Severity: MINOR. Answer was still correct and useful; grounded ✓, no hallucination. 3/3
  stress-test questions found the right files. This is sharpening, not a bug.
- Hypothesis: for some queries, chunks that DESCRIBE/CONFIGURE a feature (setup, constants,
  naming) embed closer to the question than the chunk that IMPLEMENTS it. The code-vs-doc
  re-rank fixed prose-vs-code; this is a finer code-vs-code ranking nuance it doesn't address.
- Possible future tuning (measure first, don't guess): revisit chunk size/overlap so a whole
  function stays in one chunk; consider a light signal that favors implementation over
  config/setup for "where/how is X done" queries; or evaluate a larger/fp32 embedder.
- Do this as a MEASURED step (like the last re-rank fix): build a small eval of "where is X
  IMPLEMENTED" queries with known-correct implementation files, then tune against the number.
- When: at shipping / retrieval-quality hardening. Not blocking.

### (j) errcheck adoption — deferred from P3.5 with a measured reason

Phase 3.5 adopted `staticcheck`, `ineffassign` and `bodyclose` as hard CI gates
(`scripts/lint.sh`, commit `9abc815`). **`errcheck` was named in the same item and
is NOT wired up**, deliberately, and this entry exists so that is a decision on
record rather than something rediscovered later as an oversight.

Measured on this tree: **329 findings, 157 outside tests.** The distribution is
the problem — the top callees are `os.Remove` (17), `fmt.Fprintf` (13),
`db.Close` (10), `conn.Close` (9), `resp.Body.Close` (7): cleanup-path closes and
writes to stdout/stderr. After an exclusion list covering the conventional cases,
**~60 remain**, still dominated by `defer x.Close()` on concrete types and
`enc.Encode` on a socket write whose error genuinely cannot be acted on because
the peer is already gone.

Turning that green means either ~60 `_ =` assignments — noise that makes a real
unchecked error *harder* to spot, which is the opposite of the point — or an
exclusion list long enough that the gate becomes arbitrary. It is a deliberate
triage pass over 60 call sites with its own judgement calls, not something to
bolt onto a CI-wiring commit.

**To close:** triage the ~60 non-excluded sites, decide per site between handling
the error, `_ =` with a reason, or an exclusion entry, then wire `errcheck` into
`scripts/lint.sh` alongside the other three. Until then the three adopted tools
are hard gates, which is worth more than four adopted softly — this repo has
already learned that a check nobody must pass is a check that drifts.

> **UPDATE 2026-07-31 — the deferral was right, and it was also unguarded.**
>
> Re-measured on `perf/latency-baseline`: **365 findings, 170 outside tests**, against
> the 329 / 157 recorded above. **Thirty-six new unchecked errors arrived in two weeks**,
> for exactly the reason this entry gives for not adopting: nothing was watching.
>
> The entry posed this as adopt-or-defer. There is a third option, and it is the one
> this repo already invented for coverage: **ratchet it.**
> [`scripts/errcheck-ceiling.sh`](scripts/errcheck-ceiling.sh) +
> [`scripts/errcheck-ceilings.txt`](scripts/errcheck-ceilings.txt) grandfather the
> current per-module counts and fail the build on growth — no triage required to start,
> and the drift stops today. It runs in CI's lint job and in `make check`, fails closed
> on a module with no recorded ceiling, and **fails when a count DROPS** too, so a
> slackened ratchet is reported rather than silently tolerated.
>
> Non-test only (`-ignoretests`), deliberately: an unchecked error in a test is a real
> problem but a different one, and sharing a budget would let each hide behind the
> other. Current ceilings — `daemon` 112, `proxy` 25, `editapply` 12, `helper` 11,
> `clients/tui` 10, `protocol` 0.
>
> **The triage above is still the way to close (j).** This does not do it; it stops the
> problem getting worse while it waits, and turns "we decided not to" into something a
> build can enforce. The classification is also sharper now: of the 170, ~91 are the
> conventional ignorables (40 `Close`, 23 `Fprint*`, 17 `os.Remove`, 7 `fset.Parse`,
> 4 `io.Copy` drains). The genuinely interesting residue is **15 `enc.Encode` socket
> writes and 4 `w.Write`** — a failed response encode means the peer never got the
> answer, and today that is silent. That is where triage should start.

## Backlog — added 2026-07-08

- ~~**Daemon must run from repo root (config-path gotcha)**~~ **Fixed 2026-08-01**,
  and it was never "low priority; docs/DX" — it was a hard blocker for packaging.
  Both defaults were CWD-relative (`-config ./models.json`,
  `-system-prompt daemon/prompts/system.txt`), so a daemon bundled inside a VS Code
  extension, which has no repo root to run from, could not start at all. The system
  prompt is now `//go:embed`ed (it is a build artifact, not configuration) and
  `resolveConfigPath` finds `models.json` beside the binary first, mirroring
  `resolveHelperBinPath`. Verified live: a binary and a `models.json` alone in a
  directory, started from an unrelated `cwd`, reaches `listening on ...` and drains
  cleanly; neutering both defaults reproduces the original
  `reading config ./models.json: no such file or directory`.

- **VS Code launch.json opens Host with "No Folder Opened"** (trivial; DX polish)
  The Extension Development Host launches with no workspace folder because
  launch.json only passes --extensionDevelopmentPath. Harmless (doesn't affect
  activation) but confusing during testing. Optional fix: add a bare
  "${workspaceFolder}" arg alongside --extensionDevelopmentPath so the Host opens
  clients/vscode as its workspace. NOTE: this repeatedly caused F5 to try to
  "debug" the focused file instead of launching the extension when the whole Neww
  repo was the open root — opening clients/vscode as its own folder is the reliable
  workaround. Worth fixing to save the confusion next time.

- **VS Code extension: remote-host support** (backlog; scoped out of first slice)
  Current client assumes daemon + VS Code on the same local machine (lockfile
  discovery via $XDG_RUNTIME_DIR). Remote-SSH / WSL / devcontainer extension
  hosts live on a different side of the gap and won't find the local lockfile.
  Not needed for local dev; revisit if remote usage becomes a goal.

## Backlog — added 2026-07-08 (diff-apply session) — the two items still open

The rest of this session's work shipped and is [in the
archive](docs/ARCHIVE/BACKLOG_2026-07.md).

- **VS Code diff-apply: dispatch is presence-of-`edit`-key** (hygiene note) — the daemon
  distinguishes an ApplyEditRequest from a PromptRequest by sniffing for the "edit" JSON key
  rather than an explicit type discriminator (chosen to avoid touching the just-committed
  protocol). Works and is verified, but it's an implicit contract — worth a code comment so a
  future reader knows it's intentional, and worth considering an explicit type field if the
  protocol is ever revised.
- **VS Code extension: remaining capabilities** (future slices, rough order) — a grounding
  indicator in the panel UI; a native VS Code diff view / inline decorations (nicer than the
  current whole-block red/green); then the larger fronts (ghost text, terminal error
  interceptor).

## Backlog — added 2026-07-17 (P2 caching investigation)

The privacy half of this investigation is written up in
[SECURITY_MODEL.md](SECURITY_MODEL.md#the-inference-hop--zdr-retention-finding) — a ZDR-labeled
provider retained our prompt content across requests, corroborated by billing. Open, escalate to
OpenRouter. What follows is the cost half.

- **Provider cost variance is the real cost lever — an order of magnitude more than caching ever
  offered.** An identical 492-token request cost **3.1× more on Io Net (1.23e-04) than on DeepInfra
  (3.97e-05)**, and **2.2× more than on DigitalOcean**. Against `models.json`'s note field
  ("~$0.11/$0.80 per 1M"): DigitalOcean matches it (1.12e-07/tok); **Io Net charged 2.5e-07/tok,
  2.3× the noted price.** Live today — `zdr.allow_fallbacks: true` means any of the ~11
  ZDR-compliant providers for this model can serve any request at whatever it charges, and 6
  distinct providers were observed serving 8 prompts during the 2026-07-10 verification.
  **This is a routing decision, not a caching one**, and it is where the cost leverage actually
  sits. Not scoped here: whether to constrain `provider.order` / `provider.sort` toward the cheap
  end of the ZDR-compliant pool, and what that would cost in the congestion the fallback pool
  exists to escape. **Corroborates item (e)'s open note** ("fix the stale price comment in
  models.json note field") — the note is not merely stale, it is 2.3× off for a provider that is
  live on the route today.

- **Known tension — NOT a to-do: `allow_fallbacks` is both the congestion fix and the caching
  blocker.** `zdr.allow_fallbacks` was flipped `false` → `true` on **2026-07-10** specifically to
  escape persistent DeepInfra 429s (see the ZDR gate entry — a throughput fix, not a privacy
  change; the ZDR filter still constrains the pool). That same flag is exactly what defeats prompt
  caching, which requires **sticky routing** to whichever provider holds the cache. **The fix for
  congestion is the blocker for caching.** Recorded so nobody reopens caching-for-cost without
  knowing they would be proposing to re-break the congestion fix — and per the item above, the cost
  win is in provider choice anyway, which the same flag governs. No action; this is context.

## Backlog — added 2026-07-18 (wider-pool experiment: H6 helperproc)

- **Wider-pool experiment — NEGATIVE RESULT: pool width is NOT helperproc's bottleneck; do not
  retry pool widening for it.** Pre-registered, run against real instrumented evals, reverted.
  Two findings:
  1. **The edit eval was pool-INSENSITIVE.** `daemon/edit_eval_test.go` called
     `retrieveTopK(k=len(scan.Chunks))`, so `rerankPoolSize(k)` (rerank.go) far exceeds the
     corpus and the whole thing is always fetched — a pool-width constant provably cannot move
     its result. Its "helperproc MISS #67" is a full-corpus RERANK position, not a pool
     exclusion. Fixed by adding a production-shaped k=5 (pool-gated) verdict + a raw-semantic-
     rank diagnostic (measurement only, no retrieval logic changed); the eval now reports BOTH a
     production-shaped recall (the pool-SENSITIVE number) and the legacy full-ordering recall.
  2. **Bumping `rerankOverfetchFactor` 6→16 (production pool 30→80 at k=5) did NOT flip
     helperproc.** helperproc's raw semantic rank is #59 (re-confirmed, 869-chunk corpus), so at
     pool=80 it IS fetched (59<80) — yet it still misses the production top-5, reranking out
     (full-ordering rank #67, identical across both runs). Fetching is necessary but not
     sufficient; helperproc's real bottleneck is rerank/embedding position — the SAME structural
     class as tui, not a separate "just widen the net" case. Clean FAIL by the pre-registered bar
     (helperproc did not flip; locate eval held 8/9; editapply/zdr prod-HITs held at both pool
     sizes) → `rerankOverfetchFactor` reverted to 6. **No pool size rescues helperproc** — the
     full corpus (a maximally wide pool) already reranks it to #67, so don't reattempt widening.
  - **Still open, NOT resolved by this task:** the locate-eval saturation flag (whether the
    9-query set must grow before finer retrieval tuning is trustworthy) — sidestepped here via a
    binary pass/fail bar, not answered. helperproc AND tui both remain H6 MISSes; the real levers
    are the North Star item 3 direction (chunking / query expansion / a stronger embedder), not
    pool width.

## Phase 4 — standalone / packaging / commercialization (decided direction: capable first, then shippable)

> **This section is now planned work, not a wish.** It has been designed against the
> code and scheduled as Stages 2–5 of the master launch plan, with named files, an
> ordered build sequence and stated risks. The bullets below survive as the original
> statement of intent; the plan is the executable version. Two facts found while
> designing it, which the bullets predate: the confinement pipeline needs a **security
> re-audit** under Windows path semantics rather than a port, and per-workspace daemons
> cost ~200–300 MB RSS each until the helper is shared.

Scoped and decided this session, not started. Goal: a user installs the VS Code extension from
the marketplace and it works WITHOUT separately installing/running the daemon — the "like Claude
Code" experience. This is the packaging phase, the single largest remaining body of work.

- **Bundle + auto-manage the daemon** — the extension must ship the daemon binary and start it
  as a background process on activation, shut it down on deactivate. (Today the daemon is
  started by hand in a terminal.)
- **Cross-platform binaries** — build/bundle the daemon (and embedder helper + model files) for
  Windows, macOS (Apple Silicon + Intel), and Linux, and select the right one at runtime.
  Known gap: Intel Mac (darwin-amd64) has no prebuilt onnxruntime for on-device embeddings.
- **API key model — DECIDED: managed service.** Mochiii holds the key and ships "brain + agent"
  (wired to deepseek-v4-flash); users do NOT bring their own key. IMPLICATION: every user's
  token usage bills to the project's OpenRouter account — so this commits to billing users
  (Stripe / usage metering) rather than personally absorbing everyone's usage. The billing +
  metering build is part of this phase.
- **On-device embeddings on user machines** — the local embedder helper + model files must ship
  per-platform and start correctly on a stranger's machine.
- **Packaging/publishing** — signed binaries, the .vsix, marketplace (or private-registry)
  publishing.

## Release gates (before anyone else uses it — still open)

- **DONE: Supabase auth/RLS/grants posture — see [SECURITY_MODEL.md](SECURITY_MODEL.md).**
  Closed 2026-07-17. `api_keys.user_id` (nullable, FK -> `auth.users(id)` ON DELETE RESTRICT),
  SELECT-only RLS policies on `api_keys` and `usage`, per-column grants to `authenticated`,
  `anon` revoked to zero on both tables and both RPCs, EXECUTE revoked from PUBLIC on 
  `reserve_usage`/`increment_usage` (Postgres's implicit default is EXECUTE-to-PUBLIC — `anon`
  could have called `increment_usage(key, -999999)`), and an `api_keys_public` view with
  `security_invoker = true` that excludes `key_hash`. Verified live end-to-end with an anon key
  + a real user JWT — **not** with `service_role`, which holds BYPASSRLS and passes every check
  regardless of whether RLS works at all. The proxy is unaffected throughout (it is
  `service_role`); re-verified live against the deployed proxy after the RPC revokes.
  SECURITY_MODEL.md carries the full re-runnable verification matrix and four non-obvious traps
  — read it before touching any of this. The two most likely to bite: PostgREST's 42501 hint
  literally instructs you to re-expose `key_hash` (point the frontend at `api_keys_public`
  instead), and `grant select (user_id)` looks like dead surface but is required by
  `usage_select_own`'s subquery — revoking it silently breaks every usage read.
  **Default privileges (trap 3) — half-fixed 2026-07-17.** Supabase's bootstrap auto-granted full
  `anon` CRUD on every new relation in `public` (it fired for real: `api_keys_public` arrived
  pre-granted SELECT to `anon` that nobody wrote). `ALTER DEFAULT PRIVILEGES ... REVOKE` fixed
  **tables, views, and sequences** — verified by probe. It did **not** fix **functions**, and
  can't: Postgres's built-in EXECUTE-to-PUBLIC baseline isn't a row in `pg_default_acl`, so
  revoking the explicit entry removes the catalog row and falls back to the baseline. **The
  catalog reads clean while the hole is open** — worse than the original trap. Mechanism is
  logged UNRESOLVED (it contradicts PostgreSQL's own documented example; not guessed at). What
  replaces it is a process control: **every `SECURITY DEFINER` function in `public` must revoke
  EXECUTE from PUBLIC in the same migration.** Narrow enough to hold — on `SECURITY INVOKER`
  functions EXECUTE-to-PUBLIC is only a missing layer (the caller's own grants and RLS still
  apply, which is why the pre-revoke `increment_usage` hole was latent, not live), but on
  `SECURITY DEFINER` it's the entire perimeter. First place it bites: the deferred self-service
  revocation function.
- **Security review** — the daemon opens a local socket and the edit engine writes to user
  files; both must be reviewed before others run Mochiii. Blocking gate.
  **Engineering is complete on both axes; what remains is three founder rulings.**
  *(Corrected 2026-08-01. The text this replaces described the 2026-07-18/19 state and had
  been stale for weeks — it named two FAILs as open that were fixed in July, which is the
  single clearest reason this file needed splitting.)*
  - **P3 editapply-write-path axis — engineering complete.** The 5 structural gates hold.
    `MatchesSecretName`'s case/key-coverage gap (FAIL-1, High) is contained, and the
    unconfined undo writer `restoreOne` (FAIL-2, Moderate) is fixed (`4de7bd4`), along with
    the fifth writer `ensureGitignoreEntry` (`dbc4e6c`), both with regression tests.
  - **P3 socket axis — engineering complete.** The socket now authenticates its peer via
    kernel-supplied credentials on Linux (`517c069`, `SO_PEERCRED`) and macOS (`648d38b`,
    `LOCAL_PEERCRED`), failing closed everywhere else; resource limits landed (`ccf8b8d`);
    apply/undo is serialized in-process (`d96794e`) and across processes (`2a389c7`, `flock`),
    which completes Gate 6.
  - **Gate 7 was measured rather than fixed** (`e4ff6c1`): driving the real Apply handler
    across ten filesystem states yields nine distinct refusals, none leaking an absolute path,
    every one of them user-actionable. The recommendation is to **reject** the unification —
    see D3.
  - **What actually blocks the gate now: D1 (socket auth model), D2 (Gate 6 closure),
    D3 (Gate 7).** All three are rulings with written recommendations and evidence in
    [`docs/DECISION_PACK.md`](docs/DECISION_PACK.md). None is an engineering task.
  - **Windows adds a new axis that has never been reviewed.** The five-gate pipeline was
    audited against POSIX semantics only, and
    `editapply/confinement_conformance_test.go` skips its symlink vectors on Windows — so
    junctions, alternate data streams, 8.3 short names, trailing-dot/space stripping and
    reserved device names are all unexamined. Treated as a security workstream, not a port,
    in the master launch plan.
- **ZDR confirmation — code-complete, BOTH positive and negative paths live-verified
  (2026-07-09).**
  ⚠️ **Enforcement is verified; the guarantee it is meant to deliver is not.** A ZDR-labelled
  provider was measured retaining our prompt content across requests (2026-07-17): 98% of provably
  novel content served from cache, corroborated by a 78.6% billing discount. Everything below is
  accurate — the flags are sent and honoured exactly as described — but **do not read
  "live-verified" as "prompts are not retained."** Open question with OpenRouter; evidence and
  scope limits in
  [SECURITY_MODEL.md](SECURITY_MODEL.md#the-inference-hop--zdr-retention-finding).
  Provider-routing (`provider.zdr=true`, `data_collection="deny"`, `allow_fallbacks=false`)
  is sent on every inference request, secure-by-default (an absent/legacy "zdr" section
  in models.json resolves to the strictest enforcement, not the weakest), with refusal
  detection (`ErrZDRRefused`) surfaced as a privacy-specific error rather than a generic
  one. Code in `daemon/config.go`, `daemon/provider.go`, `daemon/server.go`,
  `models.json`; unit-tested in `daemon/config_test.go` and `daemon/provider_test.go`.
  **Positive path**: a real successful prompt against this account logged
  `model API served by provider="DeepInfra" (zdr=true data_collection=deny allow_fallbacks=false)`
  followed by `stream complete`. Confirms the ZDR flags go out on the wire, the request
  succeeds under full enforcement, the serving provider is observable (so a silent
  fallback would be detectable), and that deepseek-v4-flash is ZDR-servable on this
  account via DeepInfra. (429s seen incidentally during testing were transient upstream
  rate-limiting, unrelated to enforcement, and correctly surfaced as rate-limit errors
  rather than misclassified as ZDR refusals — confirmed again during the negative-path
  revert/recheck below.)
  **Negative path**: temporarily routed a real request at `ibm-granite/granite-4.0-h-micro`
  (a real, active OpenRouter model confirmed to have zero ZDR-compliant providers, via
  OpenRouter's public `/api/v1/endpoints/zdr` listing) with enforcement still fully
  strict. OpenRouter genuinely refused — daemon log:
  `model API error: model API returned 404 Not Found: {"error":{"message":"No endpoints found matching your data policy (Zero data retention). Configure: https://openrouter.ai/settings/privacy","code":404}}`.
  This proved enforcement really blocks non-compliant routing, but also caught a real gap:
  that exact phrasing wasn't in `zdrRefusalSubstrings` (which only knew "no allowed
  providers" / "no available model provider"), so the client saw the raw 404 JSON instead
  of the friendly `inference refused: no zero-data-retention endpoint available` message.
  Fixed same-day by adding `"zero data retention"` as a third substring (chosen over the
  full sentence, too brittle against rewording, and over the shorter "data policy", too
  generic) — verified against the exact live-observed body in
  `daemon/provider_test.go` (`realObservedZDRRefusalBody`,
  `TestIsZDRRoutingRefusal_MatchesLiveObservedDataPolicyPhrasing`,
  `TestStreamCompletion_LiveObservedDataPolicyRefusalIsDetectableViaErrorsIs`), not a
  synthetic guess. `models.json` was reverted to `deepseek/deepseek-v4-flash` immediately
  after the negative-path observation, and the positive path was re-confirmed live
  post-revert (same `provider="DeepInfra" ... stream complete` shape) before the fix was
  written. Both `zdr=true` config-value changes were temporary and never committed —
  `models.json` in git is unchanged by this work; only the matcher fix in
  `daemon/provider.go`/`daemon/provider_test.go` was committed. (Folds in item (g) above.)
  **2026-07-10 — provisioned inference route: single-provider congestion resolved,
  ZDR-constrained fallback, live-verified.** `allow_fallbacks=false` pinned every request
  to one provider (DeepInfra); DeepInfra was returning persistent 429s despite a funded
  account — a throughput blocker, not a privacy one. Fix: `models.json`'s
  `zdr.allow_fallbacks` flipped `false` → `true`; `zdr=true` and `data_collection="deny"`
  are untouched, so the fallback pool stays filtered to zero-data-retention providers only
  (OpenRouter's provider-routing docs describe `zdr`/`data_collection` with hard
  pool-membership language — "excluding"/"only routes" — distinct from soft preferences
  like `preferred_max_latency`, which the same docs explicitly call "deprioritized...
  rather than excluded entirely"; `allow_fallbacks` only governs whether routing continues
  *within* that already-filtered pool). Not trusted on doc-reading alone — re-verified live
  with the same rigor as the 2026-07-09 gate above: **negative path** — pointed a request
  at `nex-agi/nex-n2-mini`, freshly confirmed via `/api/v1/endpoints/zdr` (not the stale
  2026-07-09 pick) to have zero ZDR-compliant providers, with `allow_fallbacks=true` live.
  Still hard-refused: `model API returned 404 Not Found: {"error":{"message":"No endpoints
  found matching your data policy (Zero data retention)..."`, proving fallback cannot
  escape the ZDR filter even when the filtered pool is empty — no bypass. **Positive
  path** — 8 real prompts against `deepseek/deepseek-v4-flash` all succeeded, served by 6
  distinct ZDR-compliant providers (Novita ×2, SiliconFlow ×2, AtlasCloud, DigitalOcean,
  Parasail, Morph) out of the 11 currently ZDR-compliant for this model — zero landed on
  DeepInfra, confirming OpenRouter now actually routes around the congestion. Resolved
  wire body: `{"zdr":true,"data_collection":"deny","allow_fallbacks":true}`. No code
  changes, `models.json` only. **Separate, still-open item, not resolved by this change:**
  the account still needs to be kept funded (API credits) for requests to succeed at all —
  a billing precondition, unrelated to which provider serves the request.
- **Performance NFR — TTFT MEASURED (2026-07-17): chat ~1,450 ms warm / ~1,900 ms cold, NOT
  <400 ms.** Measured end-to-end against real infra (local daemon + live Railway proxy + real
  Supabase + real `deepseek-v4-flash` via OpenRouter), from India, n=21–50 per component, warm =
  reused connection (matches the daemon's connection pooling). Headline (proxy mode, the product
  path), keystroke→first content token: median ≈ **1,450 ms** warm, ≈ **1,900 ms** cold (first
  prompt, fresh TCP+TLS to Railway), p90 ≈ **2,270 ms**. Direct mode ≈ 1,140 ms warm. ~3.6× the old
  target; not achievable for chat. Where the time goes (proxy, warm median):
  - **Model prefill/routing ≈ 1,125 ms (~78%) — the provider's time, not ours.** Highly variable
    (p90 1,840 ms); provider routing dominates the spread. Even a direct-mode / no-retrieval /
    no-gates floor is ~1,125 ms (~2.8× the target): <400 ms was never physically reachable for a
    real generation, regardless of our code.
  - Retrieval (embed+search+rerank+budget, warm embedder) ≈ **14 ms** (embed 5.4 + search/rerank
    8.8); the embedder's 355 ms startup is once-at-daemon-boot, not per-prompt. Ours, cheap.
  - **Railway proxy hop ≈ +100–180 ms — ours.** Railway-Singapore (~102 ms warm RTT from India) vs
    OpenRouter's near-India edge (~27 ms). Removing the proxy saves this but doesn't approach 400 ms.
  - **reserveQuota ≈ +80–100 ms — ours (added this session with the quota-race fix).** What's solid
    (measured directly): it is NOT an inherent write/RPC cost — an isolated Supabase read,
    table-write, and RPC-write are all identical latency (~205 ms each from a test client), and
    authorize itself adds ~0 ms while reserveQuota, the *second* sequential Supabase call, adds ~90 ms.
    What's NOT solid: *why*. The tempting explanation is fresh-connection establishment on the second
    call, but the ~200 ms cold-connection penalty behind that was measured from India (~200 ms RTT);
    the proxy→Supabase hop is intra-region Singapore→Singapore, where a TCP+TLS handshake should cost
    ~10–20 ms, not ~90 ms. The magnitude doesn't transfer across a ~40× RTT difference, so ~80 ms of
    it remains genuinely unexplained. **Untested hypothesis (not a finding):** both `authorize()` and
    `reserveQuota()` drain the response body only on their error paths; the success path calls
    `json.NewDecoder(resp.Body).Decode(&rows)`, which stops at the first complete JSON value rather
    than reading to EOF. Go's `http.Transport` only returns a connection to the pool if the body is
    read to EOF and closed, so the success path likely drops its connection every request and the
    next call re-handshakes — which would produce a per-call penalty independent of RTT. Not verified
    against the running proxy. Likely-cheap fixes if TTFT becomes a priority: drain the body before
    close (`io.Copy(io.Discard, resp.Body)`), or collapse validate+reserve into one RPC (one round
    trip). Not doing either now.
  - **Perceived responsiveness ≫ TTFT:** the daemon sends the grounding/status message BEFORE any
    tokens, ~15–20 ms after Enter (right after retrieval), so the UI shows activity in ~20 ms even
    though the first answer token is ~1,450 ms. First *visible feedback* is fast; first *token* is
    not.
  - **On the <400 ms origin (ghost_text vs chat):** introduced (`65cd63d`) as a general
    "Performance NFR" in the launch-gate list, with no path scoping — its intended path is genuinely
    unknowable. **Retired as a chat target.** <400 ms remains the natural, still-unmeasured target
    for the **`ghost_text` tier** (fast keystroke completions, `models.json` `ghost_text`, currently
    `active:false`, Phase 3.5) — re-scope it there and measure once that tier is live.

## North-Star / Deferred Capabilities (post-launch, post-security-review)

Capabilities considered and deliberately NOT being built yet, with the reasoning, so this
doesn't get re-litigated. Nothing here is scheduled; each needs its own explicit decision to
start, gated at minimum on the security review above, and, for the two big-ticket items,
validated user demand.

### 1. Autonomous multi-step agent loop (plan → edit → run → observe → fix)
Deferred deliberately.
- Removes the human-in-the-loop safety property the whole architecture rests on today (every
  edit is proposed, reviewed, and explicitly applied/undone by a person).
- Largest single body of work in the project — bigger than anything shipped so far.
- Highest token-burn feature: a multi-step loop means multiple model calls per user action,
  which threatens the thin managed-tier margins (see Phase 4's managed-key/billing model).
- Must NOT precede the security review or validated user demand — it's new capability, not
  a fix to something broken.
- **When built:** lives in the DAEMON (per the locked "one brain, thin clients" decision),
  NOT in the terminal client.

### 2. Multi-model / user-selectable brains (e.g. DeepSeek V4 Flash, Qwen 2.5 Coder, DeepSeek
R1 Distill Qwen 32B, etc.)
Deferred deliberately.
- (a) Contradicts the managed-key cost model — different models have different per-token
  prices and would complicate metering and the fixed PPP token-cap math (see Phase 4's
  billing/metering item).
- (b) Reopens the just-closed ZDR gate above — ZDR availability is per-model. deepseek-v4-flash
  is verified ZDR-servable (positive + negative path, 2026-07-09); Qwen 2.5 Coder and DeepSeek
  R1 Distill Qwen 32B are NOT verified, and ~123 of the 343 currently-listed OpenRouter models
  have zero ZDR-compliant providers at all (per OpenRouter's public `/api/v1/endpoints/zdr`
  listing, checked live during that verification) — so every added model needs its own
  from-scratch ZDR verification, not an assumption it inherits deepseek-v4-flash's.
- (c) No user has requested it — it adds a new capability rather than improving the core
  coding loop.
- **Guardrail for when built:** every user-selectable model must pass the same ZDR
  live-verification (positive + negative path) as deepseek-v4-flash before being offered. For
  the cost-sensitive Indian market, curation ("we picked the best cheap private model for
  you") is likely a stronger position than choice — reconsider that framing before building
  this, not just the mechanics of adding it.

### 3. DONE — LIVE-VERIFIED: Hybrid lexical+semantic code retrieval — the real fix for lexical-miss defects
**Shipped 2026-07-10, commit `ae3e7a4` ("Add lexical retrieval tier fused with semantic via
max-based RRF"), tag `hybrid-retrieval-complete`.** Adds an FTS5/trigram lexical tier
(`daemon/lexicalstore.go`) fused with the existing semantic tier via max-based Reciprocal Rank
Fusion (K=60), threaded through `setupRetrieval` -> `Server` -> `retrieveTopK` so the lexical
tier degrades independently of the semantic one.

**Live-verified against the real daemon (not just the eval harness), same day:**
- "Where is the ZDR refusal string matched" — correctly answers `daemon/provider.go` /
  `isZDRRoutingRefusal`, with all three substrings named.
- "What files does SearchRequest touch" — walks the full chain (`protocol.go`, `server.go`
  `isSearchRequest`/`handleSearch`, `search.go`, both clients, test files) with a correct
  summary.

Both queries were confirmed misses this morning under semantic-only retrieval; both now pass.

**Two honest caveats stay open — do not treat this as fully closed:**
- **Eval set is saturated.** Chunk-level eval is 8/9 hits with scores bunched tightly
  (~0.0122-0.0189 weighted). That resolution is too coarse to prove the RRF K=60 fusion
  weighting is actually tuned well — it only proves it isn't broken. Grow the eval set
  (harder/more adversarial lexical-miss queries) before treating this tuning as load-bearing.
- **Token-cost/efficiency claim is MEASURED (2026-07-17): 67.4% average savings, but with a real
  failure mode.** Measured by `daemon/token_efficiency_eval_test.go` (build-tagged `eval`, same
  pattern as the other eval harnesses): the real retrieval path (`retrieveTopK` +
  `truncateToBudget`, production defaults `topK=5`/`contextBudgetChars=8000`) vs. a naive
  whole-file-inclusion baseline, over a 20-query set with pre-established ground truth against this
  actual repo. Headline: **67.4% average token savings** over the 17/20 queries where retrieval
  fully found its ground-truth file(s) (~34k vs. ~104k estimated tokens). This is NOT the ~95%
  that was floated externally with no measurement behind it — that figure never appeared anywhere
  in this repo and is not supported. **Failure mode, do not hide it:** ~15% of queries (3/20 —
  parsing edit blocks, model-tier routing, secret scrubbing) had *negative* savings — retrieval
  cost MORE tokens than just including the whole target file. Root cause is structural: injected
  context size is roughly fixed (~6.5-8k chars; `topK=5` × ~40-line chunks nearly fills the 8k
  budget regardless of query), while the naive baseline scales with the answer file's size. So the
  system wins big when the answer lives in a large file or spans multiple files (77-89% savings on
  `proxy/main.go`-touching and cross-file queries) and loses when the honest answer is one small,
  tightly-scoped file (`router.go` at 44 lines, `scrub.go` at 79). The 67.4% number is therefore
  file-size-distribution-dependent and would move on a differently-structured codebase — many
  small atomic files would show more negative cases; more large files, fewer. See the benchmark
  file's header for full methodology and caveats. **The negative-savings cases were investigated
  and deliberately NOT fixed** (closed, not pending — see `RETRIEVAL_BUDGET_DESIGN.md`): they're
  structural (fixed injected-context size vs. a baseline that scales with the answer file), and
  every mechanism that would flip them either can't work on this rank-fused architecture or
  provably regresses a cross-file winner (the poison-pill: "why does the proxy reserve tokens" has
  its top-1 chunk in the small `reserve_usage.sql` but its answer in the large `proxy/main.go`, so
  any size-cap keyed on the top file drops main.go and turns a +78% win into a MISS). The one
  provably-safe mechanism (same-file chunk consolidation) was built and measured — it fires on
  1/20 queries, saves ~600 chars, and flips none of the three cases — so it doesn't earn the
  permanent complexity it adds to the retrieval hot path. Mitigating context that makes this an
  easy call: the queries where retrieval loses are the *small single-file* answers — the ones that
  were never hard in the first place — while the big wins (77-89%) are on large and cross-file
  queries, exactly where they matter.

One pre-existing, non-gated gap remains: "where does the daemon open the unix socket" still
misses at chunk-level under both semantic-only and hybrid retrieval (see `rerank_eval_test.go`'s
comment on this query for why it's out of scope here).

- **Evidence** (from the 2026-07-10 test-file-ranking investigation): live-querying the real
  repo for "where in the code is the ZDR refusal string matched, and what substring does it
  match on?" showed the chunk that actually answers it (`daemon/provider.go`'s
  `zdrRefusalSubstrings`/`isZDRRoutingRefusal`) ranked **#220 of 697** chunks by raw embedding
  similarity — outside any pool size that's practical to overfetch. An adjacent-but-incomplete
  chunk from the same file fared only slightly better (#18-22, on the edge of a 30-candidate
  pool, and only reachable at all once test-file down-weighting stopped burying it — see item
  (b) below).
- **Root cause, distinct from the test-file-ranking bug**: pure-semantic (bi-encoder embedding)
  retrieval is structurally biased against terse implementation code. A short var/func
  definition embeds farther from a natural-language question than a verbose test name or an
  explanatory comment that happens to echo the query's own vocabulary — even when the
  definition is the literal, correct answer. This is a property of the embedding model and
  chunk granularity, not of file classification.
- **Why the intent-gated test down-weight (item (b)) does NOT fix this class of miss**: that
  fix only reorders candidates already fetched into the rerank pool. If the correct chunk was
  never fetched in the first place (as in the #220 case above), no amount of reweighting can
  recover it. Confirmed live: after shipping the down-weight fix, the ZDR query's own
  right-chunk-adjacent candidate still missed the top-5 by a hair (~0.1% weighted-score gap)
  once test-file competition was removed — the residual bottleneck is raw similarity, not class
  weight.
- **Promising angle**: the FTS5 lexical search machinery already built for cross-session
  conversation memory (`turns_fts`, bm25-ranked, see "FTS5 search session" above) is a proven,
  low-risk pattern for a lexical index — trigram tokenizer, no new dependency, already
  live-verified end-to-end. The natural next step is a *parallel* FTS5 index over code chunks
  (not turns), whose bm25 hits get merged with the existing embedding-based candidate pool
  before reranking — giving exact-token queries (identifier names, string literals like
  `"zero data retention"`) a path to the correct chunk that pure embedding similarity can't
  reliably provide.
- **Shipped as scoped**: the measured eval was reused/extended (`rerank_eval_test.go`'s
  real-repo harness now gates 9 chunk-level queries, up from 7, including the ZDR
  string/identifier query above and the SearchRequest query), fusion was implemented as
  max-based Reciprocal Rank Fusion (K=60), and none of the previously-gated queries regressed
  (6/9 -> 8/9 chunk-level, no prior hit turned into a miss). See the DONE status block at the
  top of this item for what's still open (eval saturation, unmeasured efficiency claim).

### Carried forward from earlier notes (consolidated here; full detail at their original entries)
- **Model-callable `session_search` tool over backup/session history** (referred to
  elsewhere as "Option 2") — full detail in item (i) above. Distinct from the FTS5 lexical
  search over *conversation memory* that DID ship (VS Code panel, 2026-07-09): this would be
  the model itself querying past apply/backup runs, not a human clicking Undo. Deferred until
  there's a concrete need for the model to query its own past runs.
- **Self-learning loop (autonomous skill capture)** — the skill store itself is built
  (`daemon/skills.go`: `AddSkill`/`GetSkill`/`ListSkills`/`DeleteSkill`, per-user SQLite at
  `~/.codeterminal/skills.db`), but nothing yet decides *on its own*, after a successful
  multi-step fix, to mine that session and call `AddSkill` without a human curating it. That
  autonomous capture loop is what's deferred — same shape of risk as the autonomous agent
  loop above (less human oversight of what gets written/remembered), so gate it behind the
  same security review.
- **TUI search UI** — the FTS5 lexical search feature (wire protocol + daemon dispatch + UI)
  shipped end-to-end but only for the VS Code panel ("FTS5 search session", 2026-07-09). The
  TUI client has no equivalent search box yet; adding one is mechanical (same
  `SearchRequest`/`SearchResponse` the VS Code panel already uses) but deferred as a UI-only
  gap, not a blocker.
- **Ghost text** (fast keystroke completions) — `models.json`'s `ghost_text` tier already
  names a candidate model (`qwen/qwen3-coder-30b-a3b-instruct`) but is `"active": false`; see
  "VS Code extension: remaining capabilities" above. Deferred to Phase 3.5.
- **Terminal error interceptor** — see "VS Code extension: remaining capabilities" above.
  Not started.
- **Retrieval refinements** — items (b) (down-weight test files in top-1 ranking) and (f)
  (implementation chunks ranking below setup chunks) above. Both are measured, minor,
  non-blocking sharpening of a system that's already grounded and correct; do as a MEASURED
  step against an eval, not a guess, when retrieval-quality hardening becomes the priority.
  Item (b)'s fix, once shipped, only closed the "test files dominate" failure mode — the
  deeper, separate limitation it couldn't reach (correct chunk too dissimilar in raw embedding
  space to be fetched at all) is what North Star item 3 (hybrid lexical+semantic retrieval)
  fixed; see that item's DONE status block for what's shipped vs. still open.
- **Turns retention / pruning policy** — item (h) above: cross-session memory grows
  unbounded by design (persist-all). Add an age- or count-based prune when a long-lived
  workspace actually needs it.

## Hygiene / recurring

- **Never screenshot .env / keep the API key off-screen.** The key has been exposed in
  screenshots multiple times; rotate as routine. (Rotated 2026-07-08.)

- **`api_keys.key_prefix` is NOT unique — always target rows by `id`.** It's the first 8 chars
  of the raw key, so any script deriving it from a fixed literal collides across runs. Has bitten
  twice: a `mochi_te*` filter matched two rows during a deactivation (a prefix-scoped PATCH would
  have silently written both), and the quota test script has left two identical `mochi_qt` rows.
  Use `id=eq.<uuid>`, never `key_prefix=like.<prefix>*`.
- **Rebuild affected binaries after code changes** — daemon + TUI + extension; source and
  binary drift, especially when a change spans modules.
- **Stale scratch git worktree from a prior P3-experiment session lingers on disk** (outside the
  repo, harmless) — clean up (`git worktree prune`/`remove`) or explicitly note as intentionally kept.
- **`daemon/edit_eval_test.go` is n=4, one short of its n=5 target** — case 2ee3545 ("Fix
  embedding timeout: batch buildIndex") is excluded because it doesn't revert cleanly against
  current HEAD (conflicts with later `index_cmd.go` rewrites); close by resolving that revert
  conflict or mining a clean 5th defect-fix commit.

