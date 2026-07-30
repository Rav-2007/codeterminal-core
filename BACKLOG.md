**Next action: address the open P3 security-review FAILs before any P3 capability work. From the
editapply-write-path review (2026-07-18): (1) `MatchesSecretName` case/key-coverage gap [High],
(2) unconfined undo writer `restoreOne` [Moderate]. From the socket-axis review (2026-07-19):
(3) the socket has no authentication and no resource limits — any same-uid process can drive the
daemon's full write/undo/inference surface with zero credentials [Moderate on a pristine single-user
box, High under supply-chain/shared-machine threat models], gated on an undecided auth-model call.
Full write-ups + scoped fix tasks in the two "P3 daemon security review" sections below. The
editapply-write-path review is done; the socket-review axis was reviewed 2026-07-19 and also FAILs —
the release gate is clear on neither axis. Do not add capability before shipping — see
[North-Star / Deferred Capabilities](#north-star--deferred-capabilities-post-launch-post-security-review)
section for deferred items and why.**

## Known deferred debts

### (a) DONE: helper embedding timeout on large repos
Fixed: buildIndex (daemon/index_cmd.go) now embeds/upserts in batches of indexEmbedBatchSize
(40), mirroring the pattern the rerank eval test proved safe, instead of one RPC carrying
every chunk. Each batch gets its own fresh defaultHelperCallTimeout (10s) window, so repo size
no longer matters. Also closed a related gap: indexWorkspace deletes the embedder stamp
before indexing starts and only rewrites it after every batch succeeds, so a batch failure
(first index or re-index) always leaves the store correctly refused by checkEmbedderStamp
instead of silently passing under a stale stamp. Verified against the real repo (81 files,
466 chunks, 12 batches, ~14s, no timeout) and with tests simulating a mid-batch failure.

### (b) DONE: Top-1 ranking: down-weight test files — intent-gated on query text
Shipped `d07bef6` ("Down-weight _test.go in retrieval ranking, intent-gated on query text"):
`_test.go` chunks are down-weighted (`testClassWeight`, `daemon/rerank.go`) relative to
implementation for an implementation-seeking query, skipped entirely for a genuinely
test-seeking one (`looksTestSeeking`). This entry was previously never updated to DONE after
shipping — wrong by omission — and stayed that way through the discovery below.

⚠️ **Regression found and fixed 2026-07-17: `looksTestSeeking` was INVERTING this down-weight
on edit-shaped prompts, the product's core query class.** `testSeekingWords`
(`\b(tests?|tested|testing|specs?)\b`) matches the word-bounded "test" inside Go's own
`go test`/`go vet` tool-output noise — most reliably the `.test` compiled-test-binary suffix
these tools print in a build failure (`codeterminal/daemon [codeterminal/daemon.test]`,
`FAIL codeterminal/editapply [build failed]`) — not just a literal "go test" command
substring. A query built from a real captured build/test failure (exactly what a
fix-this-failure prompt looks like) was therefore misread as "the user is asking about test
files," which disabled the down-weight and let `_test.go` chunks bury the implementation
chunk the query was actually asking to fix. Measured on 4 real git-derived edit-shaped cases
(`daemon/edit_eval_test.go`, build-tagged `eval`; see its header for the n=4 resolution-limit
caveat): baseline 1/4 chunk-level hit@5, one case sitting at rank #6 — one slot outside top-5,
directly explained by two down-weight-suppressed test chunks occupying #3/#4.

**Fix:** strip the `go test`/`.test` tool-output shapes out of the query before matching
`testSeekingWords` (new `goToolFailureNoise` regexp in `rerank.go`), leaving
`testFuncPattern` (a literal `TestXxx` name) untouched. Post-fix: 2/4, with the flipped case's
rank verified via a stash-revert (reverting the fix reproduces the exact original rank #6 and
top-5, bit for bit — the fix, not something ambient, moved the number). The other two misses
are unaffected (H6 — raw similarity too low, target outside the ~30-candidate overfetch pool
regardless of class weight; see North Star item 3's DONE block) and were pre-registered as
expected non-fixes before the fix was written, not discovered after the fact.

**Follow-up 2026-07-18: the distinct `testFuncPattern` gap is now closed.** A genuine test-
ASSERTION failure or panic names its failing `TestXxx` function (e.g. `--- FAIL:
TestIsZDRRoutingRefusal_...`), tripping `testFuncPattern` rather than `testSeekingWords` and so
slipping past `goToolFailureNoise`. It is now recognized structurally rather than per-case:
captured tool output OPENS with `--- FAIL`/`panic:`, which a genuine "what does `TestFoo`
check?" question never does. New `capturedFailurePrefix` regexp (`rerank.go`) short-circuits
`looksTestSeeking` to false for those, so the down-weight applies — one leading-marker signal,
not the "fragile pile of special cases" this was originally deferred over. Measured on the same
n=4 eval (real re-run, not the old numbers): `zdr-refusal-phrasing` (the one assertion-failure
shape) went from hit #3 — implementation answer buried under four un-down-weighted
`provider_test.go` chunks at #1/#2/#4/#5 — to hit #1, with all four of those test chunks
dropped out of the top-5. The three build-failure cases are unchanged (they open with `# pkg`,
untouched by the prefix gate): editapply hit #3, helperproc miss #67, tui miss #271. Chunk-
level recall stays 2/4 — zdr was already a hit; this corrects its rank, not the count. Guard:
`TestLooksTestSeeking_IgnoresCapturedAssertionFailureAndPanic`, `daemon/rerank_test.go`. (H6 —
the two misses — remains unresolved and out of scope, per North Star item 3.)

### (c) DONE: Auto-apply-with-undo mode (VS Code)
Opt-in, session-scoped auto-apply toggle in the VS Code panel, default OFF. Confirm-
every-edit stays the default; auto-apply must be explicitly turned on and resets to OFF
on reload (no VS Code setting, no disk persistence). Client-side only: no daemon or
protocol changes -- auto mode self-drives the SAME ApplyEditRequest path (and therefore
the same gates, shared backup session, summary, and Undo button) every manual click
already uses, instead of reimplementing an apply path. Gates still fire in auto mode: a
gate-refused edit is auto-skipped and reported in the summary, with no fallback to a
manual prompt. Webview renders no clickable Apply/Skip buttons during an auto run (a
non-interactive "auto-applying..." state instead), closing the click-vs-loop race that
would otherwise be possible. Verified live end-to-end: toggle defaults OFF and is
unmistakably yellow when ON; with it ON, a multi-block prompt auto-applied 2 edits
across 2 files with zero clicks and no buttons rendered; a stale 3rd edit was
auto-refused by the gate; the summary showed correct counts (2 applied, 1 refused); the
Undo button reverted the whole auto-applied batch on disk (confirmed via cat); a window
reload reset the toggle to OFF. Committed across 3 sub-slices (2342ebc, 1f1990e,
0e5f8f2).

### (d) DONE: Conversational memory (in-session + cross-session persistence)
Both layers built and verified live. In-session: PromptRequest.History threads prior turns;
the daemon carries a capped, injection-safe conversation buffer; mem:N indicator; ctrl+n
reset. Cross-session: per-workspace SQLite store at ~/.local/state/mochiii/memory.db
(persist-all, hydrate-last-12), write-through per turn, injection defense re-runs on load,
graceful degradation on corrupt/missing store. Verified across daemon restarts (TUI and
VS Code both hydrate the same shared daemon-side state). Committed. Superseded the original
"deferred" note. Remaining follow-ups tracked in (g) and (h) below.

### (e) DONE-ish: primary model swapped to deepseek/deepseek-v4-flash
Validated live: no think-blocks, grounds on real code, emits clean SEARCH/REPLACE,
writes idiomatic Go, passes syntax gate. Cheaper ($0.09/$0.18) + 1M context. Config-only
change (models.json). NOTE: fix the stale price comment in models.json note field.

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

### (g) WAL/shm sidecar permission hardening (low priority; ZDR/privacy-relevant)
SQLite's `-wal` and `-shm` sidecar files inherit default 0644 perms — only the
main `.db` file is explicitly chmod'd to 0600. The WAL can hold recently-written,
un-checkpointed conversation turns in plaintext, so 0600 on the main file
under-protects on a shared machine. Mitigated in practice by the 0700 state dir
(`~/.local/state/mochiii/`), which blocks cross-user traversal — confirm that's
sufficient, else chmod the sidecars on open. Same gap exists in `skills.go`; fix
both together. Not a regression, surfaced during memory-persistence work. Fold
into the eventual security/ZDR review.

### (h) Memory store retention / pruning policy (grows unbounded by design)
Persistence keeps full conversation history on disk forever (persist-all,
hydrate-last-12) — correct default, but the `turns` table has no cap or prune.
Long-lived workspaces will accumulate indefinitely. Add a retention policy
(age- or count-based prune, or per-workspace cap). Pairs with the Phase-4
session-buffer-compression note.

### (i) DONE: Bounded backups + multi-run undo
Backup session dirs under `<workspace>/.codeterminal/backups/` are now
self-pruning: each new apply run keeps only the newest 5 sessions (fixed
default, not configurable this slice). Pruning lives in ONE place --
`editapply.NewBackupSessionDir` calls `pruneBackupSessions` right after
minting a new session dir -- so all 3 callers (CLI, TUI, daemon) get bounded
retention for free with no per-call-site changes. Confinement is safe by
construction (backupsRoot is always the locally-computed
`<realWorkspaceRoot>/.codeterminal/backups`, and candidate names always come
from `os.ReadDir`, which can never yield `.`/`..`/path-separator-bearing
names) plus a defense-in-depth `filepath.Dir(candidate) == backupsRoot`
assertion before every `RemoveAll`, deliberately NOT reusing the daemon's
`isWorkspaceBackupSessionDir` (that validates externally-supplied,
potentially adversarial paths -- a different threat model -- and editapply
must not depend on daemon). Every prune failure is logged, never fatal:
pruning must never block the apply run that triggered it. Multi-run undo
needed no new code -- each run's summary bubble already carries its own
run's `backupDir` in its own Undo button closure, so any of the last 5 runs
stays independently undoable. The one gap this closed: undoing a run whose
backup has since been pruned used to surface the daemon's raw
`backup session "..." not found under ...` error; the panel now
string-matches the daemon's stable `"not found under"` substring and shows
an honest `"no longer undoable (backup pruned)"` instead (reactive, not
proactive, since retention is workspace-global and prunable by any of the
3 clients). Verified live end-to-end: 6 separate apply runs left exactly
the newest 5 backup session dirs on disk (oldest pruned, confirmed via
`ls`); a canary file placed outside `.codeterminal/backups` survived
pruning untouched; undoing a past (not-latest) run correctly reverted the
file on disk; undoing a run whose file had changed since correctly reported
"0 restored, left as-is" instead of clobbering later edits; undoing a
pruned-away run showed the honest "no longer undoable" message, not an
error. Committed across 2 sub-slices (49b1141, 4d7a0ea). Deliberately
deferred next capability in this area: a model-callable
`session_search`/FTS5 tool over backup/session history, once there's a
concrete need for the model itself to query past runs rather than a human
clicking Undo. NOTE: a related but distinct FTS5 search capability was since
built (see "FTS5 search session" below) -- human-facing lexical search over
*conversation memory* (turns), driven from the VS Code panel, not a
model-callable tool and not over backup/session history. This backup-history
tool is still deferred.

## Backlog — added 2026-07-08

- **Daemon must run from repo root (config-path gotcha)** (low priority; docs/DX)
  The daemon reads ./models.json relative to its working directory, not relative
  to the binary. Running it from inside daemon/ fails with "reading config
  ./models.json: no such file or directory" — models.json lives at the repo root.
  Correct invocation: `cd ~/Desktop/Neww && ./daemon/codeterminal-daemon
  --workspace .`. Either document this clearly in the run steps, or make the
  daemon resolve models.json relative to --workspace / the binary path so it can
  be launched from anywhere. Tripped me up during the VS Code slice.

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

## Backlog — added 2026-07-09 (FTS5 search session)

- **DONE: FTS5 lexical search over cross-session conversation memory (daemon +
  VS Code panel)** — the model-callable `session_search`/FTS5 tool deferred back
  in the bounded-backups note above, built out fully to a live-verified VS Code
  UI. Four sub-slices:
  1. De-risking probe (a023966): standalone test proving the pinned
     modernc.org/sqlite v1.39.0 dependency has FTS5 + the trigram tokenizer
     available with no build tag/import/driver change, before building anything
     real on that assumption.
  2. Search index (cee571f): strictly-additive `turns_fts` virtual table
     (trigram tokenizer) kept in sync with `turns` via INSERT/DELETE triggers
     (no UPDATE trigger -- nothing ever updates a turns row); one-time
     backfill on schema version 1->2; `MemoryStore.SearchTurns` (bm25-ranked,
     workspace-isolated, query always wrapped as one quoted phrase so a
     user's text can't be misread as FTS5 operators).
  3. Wire protocol (db2ad83): `SearchRequest`/`SearchResponse` following the
     existing UndoRequest presence-of-key dispatch convention; daemon always
     resolves search against its OWN configured workspace, never a
     client-supplied path; no-match is `Results` absent from the wire (not an
     error, not an empty array).
  4. VS Code panel UI (this commit): search box + results list in the webview,
     wired through `daemonClient.searchConversations` -> `ChatPanel.onSearch` ->
     `main.js` rendering. Results render via createElement/textContent (never
     innerHTML) since a past conversation snippet is unvetted text, not markup;
     FTS5's `[`/`]` snippet() match markers are turned into `<mark>` highlights
     client-side. Search and chat are fully separate paths -- a search never
     touches `this.transcript` and doesn't block or get blocked by an in-flight
     prompt/apply/undo.

  Verified live end-to-end across all four checks: keyword search returns
  role/timestamp/highlighted-snippet result cards, bm25-ranked; a code-token
  substring query (`fmt.Println`) matches inside a snippet via the trigram
  tokenizer; a query with no matches shows a plain "no results" line, not an
  error; both the Search button and Enter-in-the-input trigger a search; chat
  streaming (grounded label) is unaffected by the new panel row. Sub-slices
  1-3 committed a023966, cee571f, db2ad83; sub-slice 4 committed alongside
  this entry. Slice complete, tagged `search-complete`.

## Backlog — added 2026-07-08 (diff-apply session)

- **DONE: VS Code chat client (thinnest slice)** — webview chat panel over the
  daemon socket; streams grounded answers; hydrates cross-session memory at preflight.
  Verified live. Committed (3f36cc5).

- **DONE: In-editor diff-apply (VS Code)** — panel renders a model-proposed edit as a
  red/green diff with Apply/Skip; Apply sends the edit to the daemon, which runs the SAME
  editapply engine (all 5 gates) and writes + backs up. Verified live end-to-end: a matching
  edit applied and created a backup; a mismatched edit ("search text not found") and a bad
  edit (.env / ambiguous) were correctly REFUSED with the file untouched, showing the exact
  gate strings. Architecture: Option A (daemon applies, client only renders + confirms), so
  no gate is reimplemented in TypeScript. Committed across 4 sub-slices (b885f50, 5db804a,
  4d74f64, 0d6e80c).

- **DONE (bonus): fixed 2 latent TUI backup bugs** — surfaced while extracting the shared
  editapply.Apply() wrapper: (1) same-file multi-block runs could clobber the before-snapshot
  (TUI lacked the CLI's dedup); fixed by making BackupOriginal idempotent per (backupDir,file).
  (2) BackupAfter failures were silently swallowed by the TUI; now treated as a failure like
  the CLI. Both covered by new regression tests. Both apply paths now route through Apply().

- **DONE: VS Code diff-apply: multi-block edits** — extended the single-block slice to
  sequential review of all proposed blocks (mirrors the TUI's N-block loop): each block
  shows an Edit i/N indicator, all blocks in a response share one backup session dir, and
  an Apply against a block whose source has drifted since the proposal was generated is
  refused at apply-time (stale-edit check) rather than applied against the wrong text.
  Verified live: sequential Edit i/N, shared backup dir across blocks, stale-edit refused
  at apply-time, correct summary counts. Committed across 3 sub-slices (e60214b, c102678,
  b24dc4a).

- **VS Code diff-apply: dispatch is presence-of-`edit`-key** (hygiene note) — the daemon
  distinguishes an ApplyEditRequest from a PromptRequest by sniffing for the "edit" JSON key
  rather than an explicit type discriminator (chosen to avoid touching the just-committed
  protocol). Works and is verified, but it's an implicit contract — worth a code comment so a
  future reader knows it's intentional, and worth considering an explicit type field if the
  protocol is ever revised.

- **DONE: VS Code extension: native Undo button** — the summary bubble (shown once a run
  applies ≥1 edit) now has an "Undo this apply" button, reachable over a new additive
  UndoRequest/UndoResponse socket message that triggers the EXISTING runUndoSession logic
  (widened to return structured restored/guarded counts; no restore logic reimplemented).
  Honest partial-undo: a file changed since the apply run is never silently overwritten —
  it's reported by name as guarded rather than folded into a misleading success count.
  Verified live: button rendered on the summary bubble; clicking it reverted both applied
  files on disk (confirmed via cat); panel reported "2 file(s) restored"; the button removes
  itself after use so it can't be double-clicked; a hand-edited file was correctly left
  guarded while its sibling restored; a 0-applied run shows no Undo button. Committed across
  3 sub-slices (e28d98d, c08585c, 77fed33).

- **VS Code extension: remaining capabilities** (future slices, rough order) — a grounding
  indicator in the panel UI; a native VS Code diff view / inline decorations (nicer than the
  current whole-block red/green); then the larger fronts (ghost text, terminal error
  interceptor).

## Backlog — added 2026-07-10 (managed proxy)

- **DONE: Managed proxy Step 1** (verified live 2026-07-10, tag `managed-proxy-step1-complete`,
  fix `702101c`) — minimal pass-through proxy deployed on Railway
  (`codeterminal-core-production.up.railway.app`), OpenRouter key held server-side only.
  Verified live: daemon→proxy→OpenRouter→back streams real completions with the daemon's local
  key stripped (`env -u CODETERMINAL_API_KEY`), proving inference runs off-machine; proxy logs
  are content-free (path/status/provider only — retains nothing); ZDR held on the wire
  (`data_collection:"deny"` + `allow_fallbacks:false` sent and unit-tested, request served by
  Morph rather than refused → provider is ZDR-eligible by construction). Route fix `702101c`
  (`chatCompletionsPath` as source-of-truth for the proxied path, `/v1` alias added), on top of
  the initial proxy in `8b5aa11`.
  Not built (later steps, deliberate): auth, token metering, hard-stop caps, per-user keys.
  ⚠️ **Standing risk: proxy has NO auth yet — anyone with the URL can spend the key.** Do not
  expose the URL publicly until Step 2 (auth/metering) lands.

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

## Backlog — added 2026-07-18 (P3 daemon security review — editapply write path)

**Adversarial review of the editapply write path ran at HEAD (`be7c24d`). Result: NOT closed —
the five structural gates hold, but TWO findings FAIL. The P3 capability-work gate (orchestration
loop, editapply CREATE, any new write capability) is NOT clear until both are addressed.** Every
claim below was exercised against the real functions via live Go probes, not read-and-approved.

**PASS (mechanism that makes each hold):**
- **Gate 1 — string-form confinement (PASS).** `filepath.IsAbs` rejects absolute paths; the check
  runs on `filepath.Clean(relPath)` so `..`/`foo/../../etc` collapse and are rejected by the
  `..`-prefix test *before* any join (`editapply/apply.go:123-130`). Independent of the filesystem.
- **Gate 2 — symlink-resolved confinement (PASS).** `EvalSymlinks` canonicalizes the target, then
  `filepath.Rel(root, resolved)` must not escape (`apply.go:132-141`); PrepareEdit/Apply then read
  and write the *resolved* path (no symlink components), defusing the check-then-swap-a-dir TOCTOU.
  Live-tested dir-symlink and file-symlink escapes — both rejected. Side effect: `EvalSymlinks`
  requires existence, so this path *cannot create files* — consistent with "no CREATE yet."
- **Gate 3 — exact-match / ambiguity (PASS).** 0 matches → "not found"; >1 → "ambiguous, refusing";
  exactly 1 → applied at the unique index (`apply.go:45-54`). Empty/whitespace SEARCH rejected
  upstream (`editblock.go:60`). No silent best-guess.
- **Gate 4 — syntax gate (PASS *as scoped*, must not be over-claimed).** Runs `go/parser` for `.go`
  only and checks *parseability* only; all other extensions get "no syntax check applied"
  (`apply.go:58-64`). It prevents corrupting a Go file into non-compiling garbage. It is NOT a
  malicious-content control (valid-but-hostile Go passes) and is a no-op for non-Go. The real
  defense against malicious *content* is the human diff + `[y/N]` confirmation, not this gate.
- **Gate 5 — backup → write (PASS on durability; one low-sev caveat).** Order is
  `BackupOriginal → write → BackupAfter` (`apply.go:94-104`); a backup failure returns before the
  write. `os.WriteFile` isn't atomic, but the pre-write backup makes the original *recoverable*:
  die mid-write → `before/` exists, `after/` missing → undo *guards* (won't auto-restore) instead
  of losing data. **Caveat (low, correctness):** `BackupOriginal` snapshots `p.Original` captured
  at *prepare* time, not re-read at *apply* time — if the file changes between prepare and apply,
  the `before/` copy is stale and the concurrent modification is clobbered with no backup of the
  actual overwritten bytes. Narrow (prepare→confirm→apply is fast, human-gated) but real.

### P3-FAIL-1 (High): `MatchesSecretName` — case-fold bug CONTAINED (partial); policy breadth + chunk exit STILL OPEN
**Status: PARTIALLY CONTAINED, NOT closed.** The case-fold bug is fixed; the two structural
gaps behind the same leak remain open. Do not record FAIL-1 as resolved.

- **Case-fold bug — FIXED** (commit on 2026-07-18): `MatchesSecretName` (`editapply/secret.go:29`)
  lowercased the basename for the *substring* check but passed the **original-case** basename to
  `filepath.Match` for the *glob* check. `filepath.Match` has no case-fold mode, so every extension
  glob was case-sensitive. Fix: match the (already-lowercase) globs against the lowercased base.
  Verified live before/after against the real function — now refused: `.ENV`, `.Env`, `server.PEM`,
  `cert.Pem`, `private.KEY`, `cert.P12`, `ID_RSA`, `.ENV.local`. No regression: lowercase secrets
  (`.env`, `server.pem`, `id_rsa`, `cert.p12`, `mysecret.txt`, `credentials.json`) still refuse;
  normal files (`main.go`, `server.py`, `README.md`, `chunker.go`) still allowed.
- **Secret-name policy breadth — STILL OPEN** (scoped, reviewed follow-up): the case fix does NOT
  broaden the list. Never covered at all: `id_ed25519`/`id_ecdsa`/`id_dsa` (any non-RSA SSH key —
  `id_rsa*` doesn't match them), `.npmrc`, `.netrc`, `.pgpass`, `kubeconfig`, `.htpasswd`, `*.pfx`,
  `*.tfstate`, service-account JSON. These walk straight through today.
- **Unscrubbed chunk content POSTed to hosted provider — PARTIAL (structural signatures closed;
  opaque secrets still open).** The name gate is only a blocklist over *filenames*; the actual
  exfiltration mechanism is that indexed chunk *content* is retrieved and POSTed unscrubbed to the
  hosted RAG completion endpoint (see chunk-text-network-exit finding). **Option A landed** (commit
  on 2026-07-18, see CHUNK_SCRUB_DESIGN.md §4-A): the existing precision-first `scrub()` now runs on
  chunk `Content` at the retrieval-time choke point `renderChunk` (`daemon/context.go`), span-
  redacting structural-signature secrets (PEM private-key blocks, `AKIA…`, `sk-…`, `ghp_…`, Slack/
  Google/Supabase/Mochiii keys) out of every grounded completion before it leaves the machine.
  Retrieval-time only — the index/embeddings are untouched (no re-index; a bad rule degrades one
  send, not the corpus). Honors `--no-scrub`. Verified live before/after (secret spans redacted, no
  leak; normal code untouched — no false positives) and against both retrieval evals (no movement —
  structurally, both evals measure `retrieveTopK` ranking, which is upstream of `renderChunk`, so
  scrubbing cannot move them: locate hybrid 8/9, edit prod-k=5 1/4, both unchanged).
  **STILL OPEN — opaque/novel secrets** (bare random values with no recognizable prefix): Option A
  is structural signatures only and does NOT catch these. They await the entropy/keyword decision
  (Designs B/C), which awaits real fire-rate data. **warn-mode** (log-only, no redaction) for the
  entropy + keyword heuristics also landed in the same commit (`daemon/chunkscrub.go`, `logChunkScrub`
  in `context.go`), flagging how often each heuristic would fire on real repos with hashed indicators
  only (never raw suspected-secret content).
  - **Correction (security beta-test, 2026-07-18):** as shipped in d8fb8d5, warn-mode wrote ONLY to
    the daemon's stderr, which nothing persists — so despite the "data is now being gathered" claim it
    accumulated NOTHING (audit Gate 8), and its lines lacked the context to separate true fires from
    false positives like SHAs/UUIDs/lockfile hashes (audit Gate 5). Both now fixed:
    - **Follow-up A** (commit 064a00a, `daemon/warnsink.go`): a durable, append-only, size-rotated,
      local-only JSON-lines sink at `<workspace>/.codeterminal/logs/warnmode.jsonl` that survives
      daemon restarts, is failure-safe on the request path (a sink error never fails/delays a request),
      and adds no network egress (verified — Gates 3 & 7 re-checked against the new path).
    - **Follow-up B** (commit 6028d96): each fire now records the chunk's `FileClass` (threaded from
      the same value `logRetrieval` reports) plus a fixed-label token-shape tag
      (hex/base64/uuid-like/mixed/unknown), for true-vs-false-positive triage without re-opening source.
  - **Status: fire-rate data now durably accumulating as of 2026-07-18 (commits 064a00a, 6028d96);
    still LOG-ONLY, item NOT closed.** The B-vs-C redaction decision remains the founder's and remains
    unmade. Do NOT flip entropy/keyword to redacting without that decision. Class is not closed until
    opaque-secret coverage is decided and (if chosen) shipped.
- **`server.go:203` / `scrub.go` false "retrieval never leaves this machine" comments — CORRECTED**
  (same commit): both now state chunk content is scrubbed at `renderChunk` (structural only, partial)
  and is POSTed to the provider on every grounded turn.
- **Why High, not cosmetic:** `MatchesSecretName` is the shared single source of truth — the
  **indexer** calls it too (`daemon/chunker.go:168`) to skip secrets from the RAG index. So the
  gap isn't only "an edit block could rewrite `.ENV`"; it's that `.ENV`/`id_ed25519`/`.npmrc` get
  read into the index, embedded, retrieved as context, and **sent to the LLM/provider** — a live
  credential-exfiltration path. This is exactly the "assumed-true, never-verified security claim"
  shape this gate exists to catch: the secret gate was believed to cover secrets; it doesn't cover
  case variants or any non-RSA SSH key.
- **Remaining scoped fix task:** broaden the policy (non-RSA SSH keys, `.pfx`, `.npmrc`, `.netrc`,
  `.pgpass`, `kubeconfig`, `*.tfstate`, service-account JSON) — reviewed as one unit, and understood
  as still only a blocklist. The class is not closed until chunk-content scrubbing lands (above).
- **`id_rsa_secret.pub`-style narrow over-refusal in `MatchesSecretName` — open, unscoped, no task
  written yet** (low; surfaced with the `.pub` carve-out added in `ade065a`). The `SecretNameAllowlist`
  carve-out (`id_rsa*.pub`, `editapply/secret.go`) is applied *after* the `secret`/`credential`
  substring net, so a public-key name that *also* contains a matched substring — e.g.
  `id_rsa_secret.pub` — is still flagged: the substring match fires before the carve-out is reached.
  The carve-out reliably exempts only plain `id_rsa*.pub`-shaped names, not ones that also trip
  another matched substring. This is not a broken fix — the carve-out does exactly what it was scoped
  to do (exempt the common `id_rsa*.pub` case) and the substring net still does its job everywhere
  else; untangling substring-net-vs-carve-out ordering for every possible combination is the same
  blocklist/allowlist stacking residual as FAIL-1 itself, more scope than the triage item called for.
  Severity low: the practical effect is *occasionally over-refusing* a legitimate public key with an
  unusual name, not *under*-refusing a secret — the safe-failure direction, the same reasoning the
  audit chain applied to `id_rsa.pub` itself before this fix. Logged so it doesn't disappear; a fix
  is not implied.

### P3-FAIL-2 (Moderate): the "all writes route through `editapply.Apply()`" claim is false — undo is a 4th, unconfined workspace writer
Enumerated every workspace-source write at HEAD (not carried forward as assumed). The 3 `Apply`
callers are still exactly 3 and all routed (`daemon/server.go:308`, `clients/tui/chat.go:519`,
`daemon/apply_cmd.go:127`); the TUI's only workspace write is via `Apply`; `writeBackupCopy`
writes only into `backupDir`. **But `restoreOne` (`daemon/apply_cmd.go:312-327`) is a fourth
workspace-file writer that bypasses `Apply` entirely** — reachable via the CLI `edits undo` *and*
the daemon's `UndoRequest` handler (`server.go:417` → `runUndoSession` → `restoreOne`). It does
`os.WriteFile(filepath.Join(root, rel), …)` with **no `ResolveSafeTargetPath`, no `EvalSymlinks`,
no secret recheck.** `rel` comes from walking the backup tree (can't contain `..`) and the content
is the user's own trusted backup — so this does NOT widen the untrusted-edit-block surface. But the
destination is not symlink-resolved: proved live that a plain `os.WriteFile` to a path that is a
symlink follows it and writes outside the root (probe wrote through a symlink to a victim file
outside the temp root). So: plant a symlink at `root/config.go` (or a parent dir) after an apply
created its backup, trigger undo, and the restore writes backup content through the symlink to an
arbitrary location; `MkdirAll` can also materialize dirs.
- **Severity Moderate:** requires local workspace write access *plus* an undo trigger, and the
  attacker controls the destination, not the payload (it's the user's own backup). But it flatly
  falsifies the load-bearing single-write-path claim, and the undo writer has *none* of the
  confinement discipline the apply writer has.
- **Scoped fix task:** route `restoreOne` through the same confinement the apply writer uses
  (`EvalSymlinks` + `Rel`-inside-root check, ideally `O_NOFOLLOW` on the write) so undo inherits
  apply's discipline and "all workspace writes are confined" becomes literally true.

**Lower-severity items to log, not gate on:** Gate 5's stale-`before`-snapshot caveat (above); and
an inherent leaf-swap TOCTOU on both apply and undo (swapping the final file for a symlink between
resolve and `os.WriteFile`, which lacks `O_NOFOLLOW`) — requires local workspace write access, so
an attacker already inside the trust boundary; note as hardening.

- **Mid-ancestor-directory-swap TOCTOU in `confinedRestorePath` — open, unscoped, no task written
  yet** (lower severity than FAIL-2 itself). `confinedRestorePath` (`daemon/apply_cmd.go`, introduced
  in `4de7bd4`) confines the undo write by resolving the deepest *existing* ancestor directory,
  checking it stays inside root, then letting `MkdirAll` + `O_NOFOLLOW` create/open the leaf.
  `O_NOFOLLOW` blocks a symlink swapped into the *leaf* between check and open. It does NOT block a
  symlink swapped into a *mid-ancestor* directory in that same window — a narrow race, not a
  currently-closed hole. Closing it fully needs `openat2(RESOLVE_BENEATH)` (Linux-specific, kernel
  5.6+) or an equivalent platform-dependent confined-open primitive — a bigger, cross-platform-
  sensitive change than the FAIL-2 fix scope covered. Severity is lower than FAIL-2: it requires the
  attacker to win a race during an already-narrow window (symlink foothold + undo trigger + timing),
  not a reliable one-shot exploit like Repro A/B were. Logged so it doesn't disappear; a fix is not
  implied to be imminent.

- **SQLite `-wal`/`-shm` sidecar opens unhardened by the `147a7b3` leaf-lstat guard — open, unscoped,
  no task written yet** (low, lower than the FAIL-2/FAIL-3 findings). The leaf-lstat symlink guard
  added to the SQLite stores in `147a7b3` (`daemon/skills.go`, `daemon/memory.go`,
  `daemon/lexicalstore.go`) covers the main `.db` file only. The `modernc.org/sqlite` driver opens its
  own `-wal` and `-shm` sidecar files internally, outside the guarded call site, so those sidecar
  opens remain unhardened against a symlink swap. Closing it fully would require a custom VFS layer for
  the sqlite driver (per the commit's own note) — a materially bigger change than a leaf guard, and out
  of scope for a hardening pass on daemon-owned, non-attacker-steerable paths. Severity low: these
  writers were already bucketed as not attacker-steerable (constant/config-derived paths, not
  client-supplied), so this is defense-in-depth hardening with a known incomplete edge, not a confirmed
  exploit path. (Distinct from the sidecar *permission* gap in "(g) WAL/shm sidecar permission
  hardening" above — that is about 0644-vs-0600 file modes; this is the symlink-swap open surface.)
  Logged so it doesn't disappear; a fix is not implied.

**Standing caveat for when the FAILs are fixed:** this reviewed the *current* write path. CREATE
support removes the `EvalSymlinks`-requires-existence property that currently anchors Gate 2 —
writing to non-existent paths and parent-dir symlinks is new confinement surface that needs its
own review even after both findings above are closed.

## Backlog — added 2026-07-19 (P3 daemon security review — socket axis)

**Adversarial review of the daemon's Unix-socket axis ran against a live, isolated daemon instance
(its own workspace, its own socket). Result: NOT closed — the socket is the second axis of the
release-gate security review (the first being the editapply write path above), and it FAILS: two
hard FAILs and two PARTIALs. The P3 capability-work gate cannot be called clear on this axis until
the auth-model question below is decided.** Every claim was exercised against a running daemon over
the real socket, not read-and-approved: unauthenticated requests were sent and their on-disk /
in-memory effects observed.

**Headline finding — lead with this, do not bury it.** The daemon is an unauthenticated *confused
deputy* for the user's inference credential. The socket has no authentication of any kind, so any
same-uid process — a compromised dependency, an IDE extension, an `npm`/`pip` postinstall script, a
language server — can connect and send prompts that are **billed to the user** and that **exfiltrate
arbitrary data to the hosted provider, with zero credentials and no sight of the key.** The attacker
never needs the key: the daemon already holds the authenticated inference path, and a same-uid
process just drives it. **Severity: Moderate on a pristine single-user box, High under
supply-chain / shared-machine threat models** — stated exactly as the review framed it, not
simplified to one label.

**Status: OPEN, blocking the P3 gate. Not closed, not scheduled, not assigned a fix owner.** The
gate stays un-clear on the socket axis until the auth-model decision (below) is made.

### P3-FAIL-3: the socket has no authentication and no resource limits — any same-uid process can drive the daemon's full write/undo/inference surface with zero credentials

**The two hard FAILs:**
- **Gate 3 — no authentication on any request type (FAIL).** The handshake (`handleConn`,
  `daemon/server.go:99`) checks only `ProtocolVersion` — no credential, no peer check, no token.
  Proven live: an unauthenticated client completed the handshake and sent an `ApplyEditRequest` that
  wrote `PWNED_BY_UNAUTH_SOCKET` to a file on disk. The full write / undo / inference surface is
  reachable by anyone who can `connect()` the socket.
- **Gate 5 — no size, time, or connection limits (FAIL).** `Serve` (`server.go:86`) accepts in an
  unbounded loop, one goroutine per connection, with no ceiling; `handleConn` sets no read/idle
  deadline and `json.NewDecoder(conn)` (`server.go:103`) has no message-size cap. Proven live:
  (a) 100 pinned half-open connections each hold a goroutine indefinitely — nothing reaps them;
  (b) a single ~200 MB request drove daemon RSS to ~896 MB — an unauthenticated same-uid process
  can OOM or wedge the daemon at will.

**The two PARTIAL findings (logged at the review's stated severity — not rounded up):**
- **Gate 6 — no cross-request lock on the Apply/Undo filesystem path (PARTIAL → deep-audited f97dc09 →
  FIXED `d96794e`).** Each connection is handled on its own goroutine and the Apply / Undo write path
  takes no cross-request lock, so two concurrent requests can interleave on the same files. Originally
  flagged as a TOCTOU *amplifier* (see Gate 8); the deep audit REFUTED that framing (distinct data
  races, not symlink amplification) and the fix serializes the paths per workspace root — see the dated
  sub-sections below.
- **Gate 7 — error responses leak absolute paths / internal resolution details (PARTIAL,
  low-impact-today-but-conditional).** Error strings returned over the socket include absolute
  filesystem paths and internal path-resolution detail. Low impact on a single-user box; conditional
  value to an attacker who does not already know the layout (e.g. a sandboxed dependency), so noted
  rather than dismissed. **DEEP AUDIT DONE 2026-07-19, and the path-scrub / upstream-passthrough
  residual is now FIXED (`3aeb8b6`) — see the dated Gate 7 sub-section below.** The audit refined the
  "conditional value to an attacker who doesn't know the layout" framing: post-Gate-3 the only
  reachable audience is an authenticated same-uid peer who already has equivalent visibility via
  `/proc` + direct syscalls (verified), so the real residual is *hygiene* (paths riding along in a
  response that later travels off-box), which the fix closes. The existence-oracle distinguishability
  itself remains open by design (lower-priority, more invasive). Gate 7 overall status / FAIL-3
  closure remains the founder's call.

**Gate 8 — synthesis against the FAIL-2 fix.** This finding does **not** break the FAIL-2 fix: the
undo writer's `EvalSymlinks`+`Rel` confinement and `O_NOFOLLOW` leaf guard still hold exactly as
reviewed. What it changes is *what FAIL-2 was defending*. An attacker who can reach the socket does
not need the undo TOCTOU at all — they can send an `ApplyEditRequest` directly and write wherever the
(confined) apply path allows, with no symlink race required. Consequently the still-open
mid-ancestor-directory-swap TOCTOU item's **practical urgency is modestly raised** — the socket gives
an attacker unlimited free retries to win the race — even though its **impact ceiling is unchanged**
(it was always local-write-access-gated, and the socket is exactly a same-uid local reach).

**Source / method:** this audit, 2026-07-19 — a live daemon on its own isolated workspace and
socket, driven gate-by-gate by unauthenticated client connections. PID / session specifics are not
preserved here (they don't matter months from now); the gate-by-gate findings above are the record.

**Scoped follow-ups (seeds for future master prompts, not full task write-ups):**
- **Auth-model decision — founder-level; THIS is the item that blocks the gate.** Is
  *same-uid-implies-trusted* the accepted trust model for the socket, or does the daemon need to
  verify its peer — e.g. `SO_PEERCRED` on the Unix socket, or a per-session token minted into the
  0600 lockfile that clients must echo? Everything else in FAIL-3 is secondary until this is decided:
  the DoS and locking work all assume a trust boundary this decision defines. Left open as a founder
  call — no preference stated here.
- **DoS hardening** — a message-size cap on the JSON decoder, a read/idle deadline per connection,
  and a connection / in-flight-request ceiling. Each is small, self-contained, and independently
  fixable; none depends on the auth decision.
- **Concurrent Apply/Undo serialization** — no cross-request lock guards the filesystem write path
  today (Gate 6). Flagged as a TOCTOU amplifier; deserves the same repro-driven depth the `restoreOne`
  undo-writer audit (FAIL-2) got, not a guessed lock. **DEEP AUDIT DONE 2026-07-19 — see the dated
  sub-section immediately below; the "TOCTOU amplifier" framing was REFUTED and refined.**
- **`recover()` backstop for `handleConn`** — defense-in-depth so a future handler panic can't take
  the whole daemon down with it (a panic in a per-connection goroutine currently crashes the
  process). Not triggered by anything today; still worth closing.

### Gate 6 deep audit — 2026-07-19 (results; audit-only, nothing fixed, does NOT close Gate 6)

Ran after Gate 3 peer-auth (`517c069`) landed. Live repros against a real `Server` over a real unix
socket (and the real `handleApplyEdit`/editapply core for the tight-barrier variants), looping to
measure reproducibility rates. Harness was intentionally NOT committed (it demonstrates losing
outcomes); method + numbers are the record here.

- **Concurrency model (Gate 1) — no serialization, confirmed.** goroutine-per-connection
  (`server.go` `Serve` → `handleConn` → `serveConn`); **zero** mutex/atomic anywhere on the Apply/Undo
  path (the only locks in the tree are `helperproc.go`/`warnsink.go`, unrelated). `authorizePeer` runs
  *inside* the per-connection goroutine, so Gate 3 gates *who* connects, not *how many run at once* —
  the concurrency shape is unchanged post-`517c069`.
- **The reliably-reproducible findings are DISTINCT data races, not symlink-TOCTOU amplification:**
  - **Apply/Apply lost update — 100% (60/60), and 40/40 on a ~hundreds-of-KB file.** `PrepareEdit`
    reads the whole file, `Apply` writes the whole file, no lock between → two concurrent same-file
    edits each write the original-minus-their-own-span; last writer wins, the other edit silently
    vanishes. Large-file result is a **clean lost update, 0 corruption/torn writes observed** (full-
    buffer `os.WriteFile` overwrites wholesale) — reported as lost-update, not corruption.
  - **Backup-session collapse — ~68% (41/60).** Same-second concurrent applies both derive the same
    timestamp dir, both stat-absent, both `MkdirAll` → they SHARE one backup session dir (distinct
    `NewBackupSessionDir` collision bug). Also surfaces `pruneBackupSessions` "directory not empty"
    errors under the race.
  - **Apply/Undo — undo guard defeated ~98% (59/60).** Undo's "current == after?" check passes, a
    concurrent apply then lands `v=two`; undo reports `Restored=1` while on-disk reality is the apply's
    content. Undo's success report and the actual bytes disagree.
  - **Undo/Undo — double-restore ~88% (53/60).** Two concurrent undos of the same session both pass
    the guard and both report restoring; no consumed-marker / idempotency on a session.
  - **Prune vs. Undo — reproduced.** A burst of applies (each prunes to keep=5) racing concurrent
    undos made **legitimate undos fail `backup session "…" not found`** — the prune `RemoveAll`'d the
    session out from under the undo.
- **Gate 5 causal claim — REFUTED as stated.** Concurrency does **not widen** the mid-ancestor symlink
  TOCTOU window (it's fixed by the code between `confinedRestorePath`'s ancestor check and
  `MkdirAll`+`openNoFollow` in one `restoreOne`; no lock makes it wait mid-window). It only lets a
  symlink attacker open *more windows per second* — more shots on goal, same-size goal — and the
  dominant amplifier there is the attacker's own swap-loop, not daemon request concurrency. The precise
  multiplicative effect on symlink-win-rate was **not cleanly measured** (a reliable mid-ancestor
  swap-winner is a separate effort) — reasoned, not measured; the distinct non-symlink races above ARE
  measured at the rates shown.
- **Severity, plainly.** As a **security** finding: **Moderate and largely subsumed** — reaching it
  needs an authenticated same-uid peer (Gate 3 + 0600 socket), and a same-uid attacker can already
  write the user's files directly, so the marginal gain is small; Gate 3 narrowed *who* only modestly
  (the set was ~same-uid via socket perms even pre-Gate-3). As a **correctness/data-integrity**
  finding: **the sharper problem, and Gate 3 does nothing for it** — 100% lost-update and ~98% undo-
  guard defeat reproduce with two entirely **legitimate** clients (CLI + IDE), i.e. silent user data
  loss + an undo that reports success while leaving the wrong bytes. **Both**, predominantly correctness.
- **New follow-ups spun out (flag-only):** (1) `NewBackupSessionDir` same-second collision
  (`editapply/backup.go`); (2) `pruneBackupSessions` `RemoveAll` racing a live undo →
  spurious "session not found" (`editapply/backup.go` + `apply_cmd.go runUndoSession` WalkDir).
- **FIXED 2026-07-19 (commit `d96794e`) — Gate 6 in-process races closed; NOT formally closed (see the
  reconciled status below — the formal P3 sign-off is the founder's, and `d96794e` was later found to be
  only the in-process half, completed across processes by M4 `2a389c7`).** In-process per-workspace-root lock
  (`Server.applyLocks`, a `sync.Map` of `*sync.Mutex`; `lockWorkspace` get-or-creates it). `handleApplyEdit`
  and `handleUndo` take it right after resolving the root and hold it via `defer` across the whole
  critical section (apply: PrepareEdit read → backup-dir prune → Apply write+copies; undo: session
  validation → restore writes). Same workspace serializes; different workspaces never block (keyed by
  root, not per-file, not global). Exclusive Mutex (all three ops mutate; RWMutex would buy nothing);
  in-process only (one daemon per socket); each caller takes exactly one lock and never nests, so no
  deadlock ordering; daemon undo never prompts, so the lock is never held across a blocking read.
  Regression cover in `daemon/gate6_serialization_test.go` (real Serve/handleApplyEdit/handleUndo over
  real unix sockets, audit loop counts): the five repros now measure **0%** (Apply/Apply lost-update
  60/60→0; backup-session collapse ~68%→0; undo-guard defeat ~98%→0; double-restore ~88%→0;
  prune-vs-undo now always atomic) — verified the tests still FAIL at the audit's rates when the lock
  is neutered — plus cross-workspace non-interference, a two-workspace end-to-end concurrency proof,
  and a timeout-guarded overlapping Apply/Undo/prune stress test. Full daemon suite race-clean. **Both
  flagged follow-ups fixed as a side effect, with proof:** `NewBackupSessionDir` same-second collision
  (serialized applies get distinct dirs) and `pruneBackupSessions` vs. a live undo (now mutually
  exclusive; undo atomic). Residual, correctly UNCHANGED: an old session beyond keep=5 can still be
  legitimately pruned before an undo reaches it (correct retention policy, not a race). Gate 7 remains
  open; the mid-ancestor symlink TOCTOU (`07c59a4`) is a different bug class, untouched by this fix.
- **(historical) Not fixed. Gate 6 stays open.** Fix direction (single apply/undo mutex vs. per-workspace lockfile
  vs. `O_EXCL`/rename atomic writes) is the founder's call.

### Gate 6 closure status — RECONCILED 2026-07-27 (canonical; supersedes the four-location contradiction)

**This section is now the single source of truth for Gate 6's status.** The four locations that
previously disagreed have been reconciled to the one accurate status below. The reconciliation
separates two things the old wording conflated: **engineering completeness** (a verified fact) from
**formal P3 closure** (the founder's call). It does NOT make the founder's call.

**Final status (one line):** Gate 6 (socket concurrency, FAIL-3) is **engineering-COMPLETE and
verified across both process dimensions, but NOT formally closed** — formal P3-gate sign-off is the
founder's, and that ruling is still open.

**Engineering — complete and not in question:**
- `d96794e` (in-process per-workspace-root serialization, `Server.applyLocks`) — on `main`, all five
  audit repros 0% and fail-when-neutered (deep-audit section directly above). **This was later found to
  be only the in-process half.**
- `2a389c7` (**M4** — per-workspace `flock(2)` inside the mutation primitives) — on `main`, completes
  Gate 6 **across processes**: `d96794e`'s mutex was in-process only, but the CLI and TUI each write the
  same workspace files from their own process, so all five races reopened whenever one overlapped a
  daemon op. Cross-process exclusion proven with two real OS processes; regression tests fail-when-
  neutered. See the M4 section (`## 2026-07-24 — M4`) below. **Together `d96794e` + `2a389c7` are the
  complete fix.**

**Why it is not marked "closed":** formal closure of a P3 sub-item is the founder's sign-off, and the
P3 security-gate as a whole remains the named blocker for capability work (socket auth model + this).
The only commit that ever explicitly said "mark … CLOSED" (`8d37a6c`) is **dangling — never merged to
`main`** (re-verified). So no properly-recorded closure decision exists on `main`, and the earlier
"CLOSED" assertions were premature on two counts: they cited the dangling `8d37a6c`, *and* they predated
M4's finding that `d96794e` alone was only half the fix.

**The four locations, now reconciled to this status:**

| Location | Reconciled to |
|---|---|
| `BACKLOG.md:633` (audit header) | unchanged — "audit does not close Gate 6" was and stays accurate for that audit |
| `BACKLOG.md:681` (same section) | corrected: "in-process races closed; NOT formally closed; completed cross-process by M4" |
| `p3-security-review.md:21` (memory) | corrected: engineering complete (`d96794e`+`2a389c7`), formal closure founder-gated, NOT closed |
| `MEMORY.md:5` (memory index) | corrected: FIXED (`d96794e` in-process + `2a389c7` cross-process/M4); NOT formally closed — founder sign-off pending |

**What remains genuinely open (the only open part):** the founder's formal closure ruling on this P3
sub-item. The documentation contradiction itself is resolved by this section; there is no engineering
task left. When the founder rules "closed," update this section's one-line status only — the four
locations already point here.

### Gate 7 deep audit — 2026-07-19 (results + path-scrub / upstream-passthrough FIX; does NOT close Gate 7)

Ran after Gate 3 peer-auth (`517c069`) and Gate 6 concurrency (`d96794e`) both landed. Method was the
audit's own: the real daemon binary on an isolated workspace + socket, driven by a raw-socket client
one request per connection, capturing the actual response bytes for every error condition of every
request type (Handshake / Prompt / ApplyEdit / Undo / Search) — leakage was never inferred from
reading error-construction code alone.

- **Leakage catalog (Gates 1–2).** Three response classes leaked a daemon-side ABSOLUTE host path; the
  rest only echoed the caller's own input (no fix needed):
  - **Apply** — nonexistent file / nested-nonexistent path (`resolving X: lstat <WS>/…: no such file`),
    unreadable file (`reading X: open <WS>/…: permission denied`), and stat/backup/write I/O-fault
    paths. The nested case additionally disclosed the *deepest existing ancestor*, a directory-depth
    oracle.
  - **Undo** — unknown session dir (`backup session "…" not found under <BK>`), empty-session
    "no backups found at <BK>", restore-time I/O — all disclosing `<BK>` = `<WS>/.codeterminal/backups`.
  - **Gate 5 (out-of-original-scope find)** — the model-API failure path returned `err.Error()`
    verbatim, leaking the upstream provider **base URL** + raw transport text. `ErrZDRRefused` was
    already rewritten to a generic string; every *other* model error passed through raw. No credential
    leak (the API key is a header, not in the URL — verified). No stack traces anywhere (all errors are
    single-line `fmt.Errorf` wraps).
- **Existence-oracle finding (Gate 3) — CONFIRMED real.** The same probe path yields *distinguishable*
  responses by filesystem state (absent / readable-no-match / unreadable / secret-named / symlink-
  outside / editable), so *which* generic error fires is itself an oracle even where no path string
  prints. Called out explicitly rather than buried — but its severity is set by Gate 4, and fixing it
  means unifying error responses (a bigger behavioral change), so it is **flagged, not fixed**.
- **Severity re-assessment (Gate 4) — Informational/Low, verified not asserted.** Post-Gate-3 the only
  party that can reach this surface is an authenticated same-uid peer, and such a peer already holds
  every disclosed fact independently: the daemon PID is in the 0600 lockfile, `/proc/<pid>/cmdline`
  shows `--workspace <WS>` verbatim, `/proc/<pid>/environ` carries `CODETERMINAL_API_BASE`, the prompt
  *success* path returns `grounding.workspace = <WS>` by design, and every existence/permission/symlink
  answer is one `lstat` away. All confirmed live on the box. So the "compromised dependency that
  doesn't know the layout" scenario does **not** survive Gate 3 (a dependency running as you *is* you to
  the kernel). The real residual is **hygiene**: absolute host paths + the upstream URL ride along in a
  response that could travel off-box later (saved transcript, pasted bug report, telemetry, or a future
  change widening socket access).
- **FIXED 2026-07-19 (commit `3aeb8b6`) — the path-scrub / upstream-passthrough residual is closed.**
  Scrub at the **socket boundary only**, mirroring `ErrZDRRefused`'s existing rewrite-before-`Encode`:
  `Server.scrubPaths`/`socketSafeError` replace every workspace-root occurrence (resolved `realRoot`,
  plus `s.workspace` for pre-resolution errors) with a stable `<workspace>` token, preserving the
  workspace-relative tail so messages stay debuggable (`<workspace>/sub/x.go: no such file`, not a
  blank "an error occurred"); applied to all Apply/Undo error encodes. The model-API failure default
  changes from `err.Error()` to a generic `"calling model API failed"`, with `ErrZDRRefused`'s specific
  message unchanged. The **full unscrubbed error is still logged locally** in every case (each handler
  logs it before encoding), so operator/CLI/TUI debuggability is unchanged. Confinement/resolution
  logic and the `editapply` core are **untouched** — only the response *string* changes; the success-
  path `grounding.workspace` field is deliberately left as-is. Regression cover in
  `daemon/gate7_scrub_test.go`: the audit's own probe set driven through the real Apply/Undo handlers
  and `serveConn` over a `net.Pipe` (model error) — asserts no absolute root appears, the `<workspace>`
  token does, messages stay informative, the model error is generic while the upstream URL is still in
  the *local log*, `ErrZDRRefused` is unchanged, and `grounding.workspace` is untouched. **Verified
  fail-when-neutered** (all fail with the exact leaked strings when the scrub is reverted). Re-ran the
  audit's real-socket probe set against the fixed daemon: every `<WS>`/`<BK>` prefix gone, replaced by
  `<workspace>`; model error now `"calling model API failed"` on the wire while the daemon log still
  carries `Post "http://…/v1/chat/completions": dial tcp …: connection refused`. Full daemon suite
  race-clean; `go vet` clean.
- **Still open, by design.** The existence-oracle distinguishability (above) is NOT fixed — a
  lower-priority, more invasive change (unify error responses) not undertaken here. Gate 7 overall
  status and FAIL-3 closure remain the founder's call.

### Daemon socket-server file layout (post-reorg 2026-07-19)

Gates 3/5/6/7 all landed in `daemon/server.go` across four sequential fixes, on top of the original
dispatch logic. That monolith was split — pure reorganization, zero behavior change, same `package
main`, full suite race-clean before and after — so each concern has one home (older audit write-ups
above still cite `server.go:<line>` at their historical positions; the current homes are):
- **`server.go`** — connection lifecycle / main flow: `Serve` (accept loop + the Gate-5 conn-ceiling
  semaphore, which is inline here), `handleConn` (calls `authorizePeer` then hands off), `serveConn`
  (handshake + request-type dispatch + prompt path, incl. the inline model-error rewrite), the
  per-request handlers (`handleApplyEdit`/`handleUndo`/`handleSearch`), the `is*Request` sniffers,
  backup-session helpers, history/persist glue, and the `Server` struct itself (all fields, including
  `applyLocks`, `maxRequestBytes`/`connIdleTimeout`/`maxConns`).
- **`server_auth.go`** — Gate 3: `authorizePeer`, `checkPeerUID` (readers in `peercred_linux.go` /
  `peercred_other.go`, unchanged).
- **`server_limits.go`** — Gate 5: `limitedConn` + methods, the `defaultMax*`/`defaultConnIdleTimeout`
  constants, `errRequestTooLarge`, the `resolved*` accessors.
- **`server_workspace_lock.go`** — Gate 6: `lockWorkspace` (the `Server.applyLocks` field stays with
  the struct in `server.go`).
- **`server_errors.go`** — Gate 7: `workspacePathToken`, `scrubPaths`, `socketSafeError`.

Test files were already concern-scoped by name (`peercred*_test.go`, `server_limits_test.go`,
`gate6_serialization_test.go`, `gate7_scrub_test.go`) and were left as-is.

**Minor, non-gating (Gate 2 hardening notes — do not weight these with the four items above):**
(1) a brief permission window between `net.Listen("unix", …)` (`daemon/main.go:159`) and the
`os.Chmod(socketPath, 0600)` that follows it (`main.go:163`) — the socket exists at default perms
for that window. (2) When `XDG_RUNTIME_DIR` is unset, `RuntimeDir()` falls back to the shared OS
temp dir (`protocol/protocol.go:29-34`); the socket dir is still `MkdirAll`'d 0700, but then lives
under a world-writable parent rather than a per-user runtime dir. Both minor; note as hardening,
not gate-blockers.

## Backlog — added 2026-07-19 (confined-writer abstraction — open design question)

**Flagging only — no code, no recommendation.** Surfaced across two prior audits (the FAIL-2
undo-writer fix and the Gate-5 writer sweep): the daemon now carries three separate, independently
written confinement implementations guarding workspace/file writes. This note records what each one
is and the tradeoffs of consolidating them, and leaves the call open. It is deliberately not an
implementation plan.

**Update 2026-07-21 (Tier 2 merge): there are now FOUR, and the standing FAIL-2 caveat has
materialized.** Fix 7 (`1066d91`) added editapply CREATE, which is exactly the "future editapply
CREATE path" this entry's case-for-consolidation anticipated. It could not reuse (1) —
`EvalSymlinks` requires existence, the very property that made creation impossible — so it
re-implements (2)'s deepest-existing-ancestor walk as `resolveSafeNewPath`
(`editapply/create.go:47`), listed as (4) below. The duplication is **not** carelessness: `editapply`
must not depend on `daemon` (the same module-boundary rule that kept `pruneBackupSessions` from
reusing `isWorkspaceBackupSessionDir`, see item (i)), so sharing the walk would require hoisting it
into a package both can import. That constraint is a **new input to the tradeoff** this entry did not
have when it was written: consolidation here is not "extract a helper," it is "introduce a shared
confinement package," which is a materially larger call. Stance unchanged — still no recommendation
forced, still a founder call.

**The four implementations as they stand today:**
1. **`ResolveSafeTargetPath` (`editapply/apply.go:122`)** — the apply writer's guard. String-form
   checks (reject absolute paths, `filepath.Clean` + reject a `..`-prefix) then **full-path**
   symlink resolution: `EvalSymlinks` on the joined path and a `filepath.Rel(root, resolved)`
   inside-root check, returning the fully-resolved path so the later read/write touches no symlink
   components. Also re-applies `MatchesSecretName`. Because `EvalSymlinks` requires existence, it
   structurally **cannot create new files** — it only resolves targets that already exist.
2. **`confinedRestorePath` (`daemon/apply_cmd.go:386`)** — the undo writer's guard. Resolves the
   **deepest existing ancestor** directory and checks that stays inside root, then leaves
   `MkdirAll` + a leaf-only `openNoFollow` (`O_NOFOLLOW`) to create/open the leaf. It uses
   parent-prefix rather than full-path resolution precisely because undo may need to **recreate a
   non-existent leaf** (a deleted file being restored) — the one property `ResolveSafeTargetPath`
   cannot offer.
3. **`ensureGitignoreEntry`'s inline lstat guard (`daemon/index_cmd.go:297`)** — a single fixed
   relative path (`.gitignore`, no client-supplied component). A leaf-only `leafIsSymlink` refusal
   plus `openNoFollow` on the append open. It has no intermediate-directory attack surface, so it
   needs neither the full-path resolution of (1) nor the ancestor walk of (2).
4. **`resolveSafeNewPath` (`editapply/create.go:47`)** — the apply writer's *create* guard, added by
   Fix 7. Same shape as (2): walk up to the deepest EXISTING ancestor, `EvalSymlinks` it, require it
   inside root, re-join the not-yet-existing remainder. Differs from (2) in that it also re-checks
   the resolved ancestor against the protected-dir set (`ProtectedDirComponent`), so a symlinked
   directory cannot become a way to *create* a git hook any more than to overwrite one (Fix 3).
   Lives in `editapply`, not `daemon`, purely because of the module boundary — the logic is
   otherwise (2)'s.

(The shared leaf primitives `openNoFollow` / `leafIsSymlink` in `apply_cmd.go` are already partly
factored out and reused by (2) and (3); the divergence that would have to be reconciled is in the
*path-resolution strategy* above them, not the leaf open itself.)

**Case for consolidation:** one audited confinement primitive instead of three parallel ones — a
single place to reason about, test, and extend when the next writer appears (e.g. a future editapply
CREATE path, which the standing FAIL-2 caveat already flags as new confinement surface). Three
parallel implementations are three things to keep correct as the threat model evolves.

**Case against:** each writer has genuinely different constraints — full-path vs. deepest-existing-
ancestor vs. fixed-single-path resolution; leaf-must-exist vs. leaf-may-be-created; secret-recheck
vs. none. A shared abstraction covering all three tends to become either too rigid (forcing one
resolution strategy onto a writer that needs another — e.g. making the create-capable undo path
inherit `EvalSymlinks`-requires-existence, which would break it) or too configurable (enough
flags/options that reasoning about any single call site is no easier than reading three focused
functions). The current split keeps each guard small and locally obvious.

**Status: open design question, no recommendation forced.** A founder-level "worth doing at some
point" call, not a fix and not urgent: there is no correctness gap here — all three guards hold as
reviewed, so this is about the maintainability of parallel implementations, not a vulnerability.
Recorded so the tradeoff is on paper the next time a writer is added; deliberately left un-nudged in
either direction.

## Phase 4 — standalone / packaging / commercialization (decided direction: capable first, then shippable)

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
  **P3 editapply-write-path review ran 2026-07-18 — NOT closed (2 FAILs).** The 5 structural gates
  hold; `MatchesSecretName` case/key-coverage gap (High) and an unconfined undo writer `restoreOne`
  (Moderate) fail. Full write-up + scoped fix tasks: see "P3 daemon security review — editapply write
  path" section above. P3 capability work (orchestration loop, editapply CREATE, any new write
  capability) stays blocked until both are addressed.
  **P3 socket-axis review ran 2026-07-19 — NOT closed (2 hard FAILs + 2 PARTIALs).** The socket has
  no authentication (any same-uid process drives the full write/undo/inference surface with zero
  credentials) and no resource limits. Severity: Moderate on a pristine single-user box, High under
  supply-chain/shared-machine threat models. Full write-up + scoped fix tasks: see "P3 daemon
  security review — socket axis" section (FAIL-3) above. This axis also blocks the P3 gate; the
  undecided auth-model call is the item gating it.
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

## Backlog — added 2026-07-21 (Tier 1 + Tier 2 correctness-and-recovery merge)

**Both tiers merged to `main` 2026-07-21, fast-forward, no merge commits.** `main` was at `14502e5`;
it is now at `10ba174`. Nothing pushed — `origin/main` remains 27 commits behind by choice.

**Tier 1 — critical (`tier1-critical-fixes`, 3 commits):**
- **Fix 1+2 (`16e84a1`)** — apply and undo report what is actually on disk. Apply's ordering is now
  `BackupOriginal → BackupAfter → write`, so every fallible bookkeeping step runs while the
  workspace is still untouched and the write is the single last act; a failed write rolls the
  after-snapshot back. Closes the unreported-unrevertable-mutation window (a snapshot failure used
  to return an error to a caller whose file had already changed, with no `after/` for undo to match).
- **Fix 3 (`83244ec`)** — edits into VCS, credential and undo-state dirs are refused
  (`editapply/protected.go`). `.git/hooks` in particular: an applied edit must not be able to
  install code that later executes.
- **Fix 4 (`3f9352f`)** — the `=======` scan is bounded by the block's own `>>>>>>> REPLACE` marker
  instead of taking the first separator anywhere in the response. The old order cut a block at a
  separator line that was part of the content being searched for, producing a well-formed-looking
  block carrying the wrong SEARCH and wrong REPLACE that applied cleanly — silent corruption.
  Genuinely ambiguous blocks (multiple bare separators inside one block) are now refused by name.

**Tier 2 — correctness (`tier2-correctness-fixes`, 5 commits, stacked on Tier 1):**
- **Fix 6 (`07e97d1`)** — tiered SEARCH matching (`editapply/match.go`), with change detection split
  out; an edit no longer dies on invisible whitespace differences.
- **Fix 7 (`1066d91`)** — file creation as a real capability (`editapply/create.go`). **See the
  reachability finding below before treating this as shipped.**
- **Fix 5 (`a02485c`)** — an edited file is re-indexed after a successful apply (`daemon/reindex.go`),
  so retrieval stops reasoning about stale code.
- **Fix 8 (`0cf13cc`)** — the embedder helper resolves against the daemon's own binary
  (`daemon/helperpath.go`) rather than the CWD, and degraded retrieval says *why* it is off.
- **Fix 9 (`46d9e1c`)** — model-API failures are classified over the socket (`daemon/modelerror.go`).
- **Fix 10 (`10ba174`)** — bounded, class-aware retry with jittered backoff (`daemon/retry.go`), so a
  transient upstream error no longer dead-ends the turn.

**Validation on merged `main` (2026-07-21, real runs, not asserted):** `go build` and `go vet` clean
across all six workspace modules; `go test` green (`daemon` 11.9s, `editapply`, `clients/tui`,
`proxy`; `protocol`/`helper` have no tests); `go test -race -count=1 ./daemon/... ./editapply/...`
green (17.8s / 1.1s). Note `./...` does not work from the repo root — the root is not itself a
module, so the six `go.work` paths must be named explicitly.

### OPEN (found by live spot-check at merge time, NOT fixed): Fix 7's CREATE path is unreachable from every shipped client
**This is the finding that matters most in this section.** `editapply.PrepareEdit`/`Apply` genuinely
support creation — `IsEmptySearch` → `prepareCreate` → `Creates: true` — and `editapply/create_test.go`
covers it. But **every production path that turns a model response into edit blocks goes through
`editapply.ParseEditBlocks`, which still rejects an empty SEARCH section at `editblock.go:88`**
(`SEARCH block is empty`), a check Fix 7 did not touch. So the capability is real in the core and
unreachable in practice:
- `daemon/apply_cmd.go:68` — CLI `edits apply`. **Reproduced live:** a `path:`-prefixed block with an
  empty SEARCH fails with `parsing edit blocks: line 2: SEARCH block is empty`; no file created.
- `daemon/server.go:709` — `parseAndLogEditBlocks`, which produces the `EditProposals` the VS Code
  panel renders. Same parser, so the panel is never offered a creating edit to Apply.
- `clients/tui/chat.go:424` — same parser, same outcome.
- The *only* reachable entry is `daemon/server.go:414`, which builds an `EditBlock` straight from
  `ApplyEditRequest` fields and so bypasses the parser — but no shipped client ever originates such a
  request, because the proposals clients act on all came from the parser in the first place.

**Second-order harm, also reproduced live:** `ParseEditBlocks` fails the *whole response*
(`return nil, err`), not the offending block. A two-block response — one ordinary edit plus one
creating edit — applied **nothing**: the ordinary `existing.go` edit was silently dropped along with
the create. So a model that (correctly, per Fix 7's own doc comment) emits an empty SEARCH to create
a file also loses every other edit in that turn. That makes this a *regression risk on working
behavior*, not merely a missing capability.

Fix direction is a parser change, not a core change: `ParseEditBlocks` must let an empty SEARCH
through as the create marker, and the "empty" rejection must be re-expressed so it still catches the
malformed shapes it was written for. Deliberately **not** done at merge time — the merge was scoped
to landing verified branches, and this touches the one file Fix 4 just hardened. Needs its own slice
with its own tests.

### OPEN (deferred by design): undo of a created file leaves a 0-byte file and reports success
`prepareCreate` sets `Original: ""` (`editapply/create.go:99`), so `BackupOriginal`
(`editapply/backup.go:105`) writes a **0-byte** file to `before/<rel>`. Undo restores that snapshot
faithfully — which leaves an empty file where the correct outcome is *no file at all*.
**Reproduced live** (driving the create path directly, since the parser blocks it — see above):
`edits undo --force` printed `restored newfile.go` / `1 file(s) restored` / `0 guarded`, and left
`newfile.go` on disk at 0 bytes. The report claims the workspace was restored; it was not.

The backup format has no way to encode "this path was absent before the run", so undo cannot
distinguish *was empty* from *did not exist*. A real fix needs a backup-format absent-marker plus a
delete branch in the restore path (`restoreOne`, `daemon/apply_cmd.go`) — a format change with its
own confinement questions (deleting is a new operation for a writer that has only ever written), not
a one-liner, which is why it was deliberately left out of Fix 7 rather than rushed into it.
Severity: low on data safety (an empty file is left behind, nothing is destroyed), moderate on
honesty — undo's success report and on-disk reality disagree, which is precisely the class of defect
Fix 1+2 existed to eliminate everywhere else. `TestCreate_CreatedFileIsUndoable`
(`editapply/create_test.go:209`) asserts the 0-byte `before/` snapshot exists but never runs an undo,
so nothing currently fails because of this.

### Also open: ancestor-confinement logic is now duplicated across modules
Fix 7's `resolveSafeNewPath` re-implements `confinedRestorePath`'s ancestor walk because `editapply`
cannot import `daemon`. Folded into the existing
[confined-writer abstraction](#backlog--added-2026-07-19-confined-writer-abstraction--open-design-question)
entry as implementation (4) rather than duplicated here — see the 2026-07-21 update in that section.

## Backlog — added 2026-07-22 (Tier 2.5 + Tier 3 merge)

**Both tiers merged to `main` 2026-07-22, fast-forward, no merge commits.** `main` was at `80540de`;
it is now at `c68de0a`. Nothing pushed — `origin/main` remains 46 commits behind by choice. The
branch `tier25-creation-reachable` is left intact.

**Tier 2.5 — creation reachable (4 commits):** Fix A (`36f9bbe`) lets create blocks through
`ParseEditBlocks`; Fix B (`7215dc1`) recovers per block instead of failing the whole response;
Fix C (`bd43e90`) reverts a created file by removing it; plus an end-to-end create+edit+undo test
(`c18782c`). Together these close the two OPEN findings recorded in the 2026-07-21 section above —
see the spot-check below, which reproduced the closure live rather than trusting the tests.

**Tier 3 — retrieval and response robustness (5 commits):** Fix 11 (`fb0770c`) merges
adjacent/overlapping same-file chunks before rendering; Fix 12 (`83f1bbc`) resolves `file:line`
references in the prompt directly to spans; Fix 13 (`ef1f5a9`) bounds history by bytes, filters
empty turns, and reports when it truncated; Fix 14 (`f4ad82e`) rejects empty prompts, stops
persisting empty turns, reads `delta.reasoning`, and tolerates path-line variants; plus a
regression pinning that `host:port` in a URL is not a `file:line` reference (`c68de0a`).

**Validation on merged `main` (2026-07-22, real runs, not asserted):** `go build` and `go vet` clean
across all six workspace modules; `go test` green (`daemon` 12.8s, `editapply`, `clients/tui`,
`proxy` cached; `protocol`/`helper` have no tests); `go test -race -count=1 ./daemon/... ./editapply/...`
green (15.7s / 1.1s); `gofmt -l` clean. As before, `./...` does not work from the repo root — the
root is not itself a module, so the six `go.work` paths must be named explicitly.

### Spot-check at merge time: all four Tier 3 fixes are reachable through a real production path
The 2026-07-21 merge's most valuable output was noticing that Fix 7 passed its tests while being
unreachable from every shipped client. The same suspicion was applied here, against a **live daemon**
— the real `codeterminal-daemon` binary, a real BGE index over a 32-file workspace (65 chunks,
`top_k=5`), the real Unix socket and wire protocol, with only the model API replaced by a local
capture server so the assembled request body could be read. Not the test harness.

- **Fix 12 (`file:line` → span) — REACHABLE.** Verified by control/test pair on the *same* prompt
  text. Without the reference, retrieval returned `distract15.go`, `distract04.go`, `distract09.go`
  and no `widget.go`. With `widget.go:42:` prepended, `widget.go:22-62` was ranked **first**, the
  daemon logged `resolved 1 file:line reference(s) … ranked first`, and the referenced lines were
  present verbatim in the user-role `<retrieved_context>` block actually POSTed upstream. The span
  arrived because it was named, not because similarity found it.
- **Fix 11 (chunk merging) — REACHABLE, attribution correct.** A prompt that both named
  `widget.go:42` and matched `widget.go` by similarity produced
  `merged 6 chunk(s)/span(s) into contiguous span(s), reclaiming 3993 rendered byte(s)`, and the
  assembled prompt carried **one** `widget.go` span, not overlapping copies. Line attribution was
  checked against disk rather than assumed: the span labelled `widget.go:1-89` places file lines 41
  and 42 at exactly the bytes `widget.go` holds at lines 41 and 42.
- **Fix 13 (history-truncated flag) — REACHABLE on the wire; NOT surfaced by the shipped TUI.**
  Twelve 30 KB turns over the real socket returned
  `"history":{"turns":8,"truncated":true}` in the `TokenResponse`, with the daemon logging
  `received=12 kept=8 dropped_by_bytes=4 bytes=240064 truncated=true`; the negative control (4 small
  turns) correctly returned `"history":{"turns":4}` with no flag. The protocol half is honest. But
  `clients/tui/stream.go:195` reads only `Error`, `Grounding`, `Redactions`, `Token` and `Done` —
  it never reads `TokenResponse.History`, so the TUI cannot show it. Note this is *parity*, not a
  regression: `groundingLabel` (`clients/tui/chat.go:732`) renders chunk count but ignores
  `GroundingInfo.Truncated` too, so Fix 13 mirrors its stated counterpart exactly, including the
  part neither client renders. See the client-rendering item below.
- **Fix 14 (empty prompts, path-line variants) — REACHABLE, both halves.** An empty prompt and a
  whitespace-only prompt over the socket both returned
  `{"error":"prompt is empty","error_class":"invalid_request"}` and the capture server recorded
  **no** upstream request, confirming the rejection happens before the model is called. A model
  reply using the tolerated `**path:** \`target.go\`` + fenced variant parsed into a correct
  `edit_proposals` entry on the wire.
- **Tier 2.5 (creation) — REACHABLE end to end, and the 2026-07-21 findings reproduce as closed.**
  A model reply with an empty SEARCH block now survives the parser and reaches a client as an
  `edit_proposals` entry; applying it over the socket returned `"applied":true` and created the
  file on disk; `edits undo` then printed `removed brandnew.go (created by this apply run)` /
  `1 file(s) reverted (0 restored, 1 removed)` and the file was **gone** — not left at 0 bytes.

### OPEN: `file:line` resolution is unreachable when retrieval is disabled
Direct `file:line` resolution needs no index — it is a deterministic lookup of a named location on
disk. But `gatherContext` returns early when retrieval is unavailable
(`daemon/context.go:110-119`, the `s.embedder == nil || s.store == nil` guard), and the direct-
resolution call sits **below** it at `daemon/context.go:131` (`resolveFileLineRefs`,
`daemon/fileref.go:118`). So a user with no index who pastes a compiler error gets no context at
all, even though their prompt named the exact file and line.

That is precisely the user who most needs the help — no index built yet, hitting a build error —
getting the least. The fix is likely small: hoist the direct-resolution path above the
retrieval-disabled early return, and let the outcome report grounding from direct spans alone.
Severity: low-to-medium — a capability gap in a specific-but-common state, not a correctness bug.
Nothing is wrong when retrieval *is* enabled (verified live above). **Status: open, unscoped, no
task written yet.**

### OPEN: the retrieval eval harness is self-referential, so cross-commit comparisons drift
The eval indexes *this repo*, so every commit changes the corpus being measured. Tier 3 observed
this directly: question-shaped recall moved 7/9 → 6/9 with a **byte-identical ranking path** —
`git diff` over `rerank.go`, `index_cmd.go`, `vectorstore.go`, `lexicalstore.go`, `fileclass.go` and
`chunker.go` was empty. `daemon/router.go:1-40` scored *identically* in both runs and was displaced
#4 → #6 purely by two chunks of Tier 3's own newly-written source entering the corpus — one of them
on an exact score tie broken by sort order.

Separately, the n=4 edit eval is effectively **n=3**: `editapply-apply-extraction`
(`daemon/edit_eval_test.go:128`) can no longer revert cleanly against HEAD, because Tiers 1/2/2.5
rewrote `editapply/apply.go`. It fails loudly rather than faking a pre-fix tree, which is the
correct behaviour, but the population will keep eroding as commits rewrite the files it reverts.

Why it matters: any before/after retrieval number compared *across commits* is confounded by corpus
drift. This does not invalidate within-run comparisons — Tier 3's merged-path re-scoring column was
clean — but a recall figure from one commit is not directly comparable to one from another. Options
worth considering later: a frozen corpus snapshot, excluding the repo's own recent commits, or
explicitly documenting that cross-commit comparisons are indicative only. Severity: low as a defect,
**medium as a measurement-validity concern** — it governs how much weight any future retrieval
metric deserves. Deliberately not masked in Tier 3; masking it would be optimising the benchmark.
**Status: open, unscoped, no task written yet.**

### Noticed during Tier 3, not fixed (one line each)
- **Reasoning reaches the wire but no client renders it.** `TokenResponse.Reasoning`
  (`protocol/protocol.go:195`) is populated, but `clients/tui/stream.go:195` never reads it, so the
  ~907 ms of dead air is closed only at the protocol layer. Same shape as the Fix 13 rendering gap
  above; both are client-side work, not daemon work.
- **Python-style tracebacks aren't matched by the `file:line` resolver.** `File "x.py", line 42` is
  a different shape from `path:line:` and `parseFileLineRefs` (`daemon/fileref.go:77`) does not
  recognise it.
- **Chunk end-lines overshoot by one on files ending in a newline** (surfaced by this merge's
  spot-check, *not* introduced by Fix 11). `splitLines` (`daemon/chunker.go:288`) splits on `"\n"`,
  so a trailing newline yields a phantom final element and an 88-line file is labelled `1-89`.
  Confirmed pre-existing and independent of merging: the control run, in which nothing merged, still
  labelled a 43-line file `distract15.go:31-44`. Cosmetic — the content and its start line are
  correct, so a reader counting from the start lands on the right line — but the label is inaccurate.

### Input for the separately-scoped default-model evaluation (input, not a task)
Raw similarity scores cluster at **0.0147–0.0164 across unrelated chunks** — roughly a 1% spread
deciding top-5 membership. That points at an **embedding-model discrimination ceiling** rather than a
ranking-logic problem, and it explains why the remaining edit-shaped misses sit at ranks #78/#307
rather than just outside the cutoff: no amount of re-ranking recovers a signal the embeddings never
separated. Relevant both to the default-model decision and to how much further ranking work is worth
doing before that decision is made.

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
- **`daemon/models.json` is untracked** (not gitignored, not committed; model-tier config, no
  secrets) — decide whenever next in that area: gitignore it or commit it.
- **Stale scratch git worktree from a prior P3-experiment session lingers on disk** (outside the
  repo, harmless) — clean up (`git worktree prune`/`remove`) or explicitly note as intentionally kept.
- **`daemon/edit_eval_test.go` is n=4, one short of its n=5 target** — case 2ee3545 ("Fix
  embedding timeout: batch buildIndex") is excluded because it doesn't revert cleanly against
  current HEAD (conflicts with later `index_cmd.go` rewrites); close by resolving that revert
  conflict or mining a clean 5th defect-fix commit.

## Backlog — added 2026-07-23 (Tier 4 — the operability cluster)

**Three sub-clusters implemented on `main`, isolated commits, each verified through the real
daemon binary and real client(s) over the real Unix socket.** `main` was at `fccadbc`; it is now
at `2241daa` (C1 `0599a11`, C2 `6618dda`, C3 `2241daa`). Nothing pushed — `origin/main` stays
behind by choice. **Nothing marked closed — founder decides.**

The cluster closes the pattern the A6–A9 debug pass named: *the daemon degrades gracefully and
honestly in its logs, but presents as healthy on the wire.* Per Part 0, each catalogued finding
was reproduced live against current `main` FIRST (rule 3 — a catalogued finding is still a claim
until reproduced today), then fixed, then validated through production entry points, never by
reading code.

### Step 1 — reconfirmation (live, before any code; all 3/3 reproducible)

| Item | Reproduction | Observed on current `main` |
|---|---|---|
| C1-a `API_BASE` validity | `CODETERMINAL_API_BASE=':::not a url'`, real prompt | Starts clean; burns 3 retries with backoff on an unparseable URL; reports `"unreachable or failing right now — this is usually temporary"` / `upstream_unavailable`. Not temporary — a typo. (Truly-unset base *does* already fail fast; only validity was unchecked.) |
| C1-b nonexistent `--workspace` | `--workspace /nonexistent`, and a path naming a file | **Reproduces differently than catalogued** — not "silent" but actively *wrong*: wire says `"no index has been built for this workspace yet (run \`index\`…)"`. The path doesn't exist; indexing it fails too. Recorded as found, not as described. |
| C1-c version / unknown keys / ranges | 5 crafted `models.json` | `config_version` 0 and 99 accepted mutely; `"retreival"` (typo) silently discarded so a deliberate `top_k` never applied; negatives silently defaulted; `top_k:100000` + `context_budget_chars:100000000` applied verbatim. |
| C2-a lexical tier down | symlinked `lexical.db` (constructor refuses it); semantic healthy | stderr `lexical=false`; wire `{"grounded":true,"chunks":3}` — **byte-identical to healthy apart from the workspace label.** TUI rendered `grounded ✓ 3 chunk(s)`. The purest instance. |
| C2-b memory unavailable | `XDG_STATE_HOME` with symlinked `memory.db` | **Partially, not fully, invisible** — prompt path + handshake say nothing, turns silently stop persisting; **but the search path already reports it** (`"conversation memory is not available"`). Not rounded up to "invisible". |
| C2-c provider-served-but-degraded | capture-server body inspection | Daemon sends `allow_fallbacks:true` (the shipped `models.json` sets it); client receives zero routing/provider info; provider name reaches stderr only. |
| C3 observability | `{"status":true}` probe; `--help` | No status message type (`{"status":true}` → `"prompt is empty"`); no `--log-file`/levels; stderr the only sink. Confirmed zero. |

### C1 — fail-fast config validation (`0599a11`)
Two failure modes chosen per case, not uniformly (as the prompt required — "fail fast" is not
always "exit 1"):
- **FATAL (exit before listening):** unparseable / schemeless / hostless / non-HTTP `API_BASE`
  (incl. whitespace-only); a `--workspace` that is missing or is a regular file. These can never
  start working, so refusing with a message that names the setting beats misattributing per-request
  forever. **Reachability is deliberately NOT probed** — that is a runtime state (the daemon's
  retrieval/apply/undo/search paths all work offline), and it is reported as one.
- **WARN AND CONTINUE:** unrecognized/absent `config_version`; unknown keys (named individually —
  `"retreival"` is reported, not merely ineffective); out-of-range `top_k`/`context_budget_chars`
  (clamped to 200 / 200 000 with a warning, negatives → default). None makes the file unservable,
  and hard-failing would break forward/backward compatibility. `Config.Validate` still hard-fails
  a genuinely unservable file — a test pins that the warn path didn't soften it.
- **Verified live:** all four fatal cases exit 1 before listening; all four warn cases start with a
  named warning; the clamped values are the ones retrieval actually runs (`top_k=200`,
  `context_budget_chars=200000`); the shipped `models.json` warns nothing.

### C2 — wire-visible degraded-state signals (`6618dda`)
New `TokenResponse.Degraded []Degradation`, on the same pre-token message as `Grounding`, listing
every reduced subsystem: `lexical_retrieval`, `memory`, `provider_routing`. Detail strings follow
the Fix 8 / Gate 7 discipline (what is reduced + what it costs; never a path/host/upstream error);
a test asserts that discipline directly. Design notes:
- `lexical_retrieval` is reported ONLY when the semantic tier works — if retrieval is off entirely,
  `GroundingInfo.Reason` already gives the cause, and this would be noise.
- `provider_routing` is derived from **config**, not from any response: OpenRouter reports which
  provider served a request but not whether a fallback was used, so a per-request "this one fell
  back" claim would be fabricated. It says the honest, checkable thing — fallbacks are *permitted*.
- **Both shipped clients render it** — this is the Fix-13 trap (a correct wire field no client
  reads) and the Tier-2.5 trap (a fix unreachable from every client) avoided together. TUI: one
  header line per degradation (one line each, never joined — a joined line can overflow and
  soft-wrap, desyncing the viewport row count and hiding a notice). VS Code: a `#degraded` region
  rendered via `textContent`.
- **Verified through production entry points:** all three signals captured as raw bytes off the
  real socket; the **real TUI binary driven in a pty against the real daemon** painting
  `⚠ degraded (lexical_retrieval): …` and `⚠ degraded (memory): …`; the **real `media/main.js`
  render path** exercised against a DOM stub. A healthy daemon emits nothing (omitempty), so its
  response stays byte-identical to before.

### C3 — minimal observability surface (`2241daa`)
- **Status over the existing Unix socket** (NOT an HTTP port — the daemon's invariant is that it
  never listens on the network, and the socket is 0600 + SO_PEERCRED-authenticated; a localhost
  HTTP health endpoint would have neither property). New `StatusRequest`/`StatusResponse` via the
  same discriminator peek that routes apply/undo/search, plus a `status` CLI subcommand that
  connects as a client and prints human-readable (or `--json`). Reports the two retrieval tiers
  **separately** (a single "retrieval: ok" would re-hide the C2-a state); reuses the same
  `Degradation` values the prompt path pushes (pulled and pushed views can't drift); carries **no
  api base** (Gate 7 — a test asserts it); read-only and lock-free (can't block or perturb a busy
  daemon).
- **`--log-file`**, size-rotated at 5 MiB with one `.1` backup (the `warnsink.go` discipline),
  **tee'd to stderr** not replacing it. An unwritable path warns and continues (the same
  over-strict-startup trap C1 avoided).
- **Verified live:** `{"status":true}` raw off the socket; the real `status` CLI against a live
  daemon (healthy; lexical-down showing `DEGRADED (2)`; and the no-daemon `is it running?` case
  exiting 1); `--log-file` confirmed capturing the startup lines a detached daemon would otherwise
  lose.
- **Explicit scope call-out:** this adds a log FILE, **not log levels.** Levels would mean
  reclassifying every call site across the daemon for a filtering benefit, while the operator
  question that motivated the cluster ("is this daemon healthy?") is answered directly by the
  status surface. Left undone deliberately, not overlooked.

### Step 4 — regression (real runs, all six `go.work` modules, not asserted)
`go build`, `go vet`, `gofmt -l` clean across daemon / editapply / protocol / clients/tui / helper /
proxy. `go test` green (daemon 13.5s; editapply, clients/tui, proxy pass; protocol/helper no tests).
`go test -race -count=1` green on daemon (22.0s), editapply (1.1s), clients/tui (1.3s). VS Code
`tsc --noEmit` clean. `./...` still does not work from the repo root — the six paths were named
explicitly.

### Surfaced but NOT fixed (recorded, in the Tier-3 chunker-off-by-one spirit)
- **The shipped `models.json` sets `zdr.allow_fallbacks: true`** (flipped `false`→`true` on
  2026-07-10, see the 2026-07-17 P2 caching section). This is exactly what makes C2's
  `provider_routing` degradation fire on a stock daemon. It is real config content weakening the
  ZDR posture the docs describe — **founder's call**, not a code change, recorded here not silently
  altered. (Config, not the fix's concern; C2 only makes it *visible*.)
- **C2-b memory is still not surfaced on the handshake itself** — the degradation fires on the
  first prompt of a session, which is when a client learns memory won't persist, but a client that
  only ever handshakes (a preflight/hydrate connection) sees nothing new. Small, deferred.
- **Client-render parity gaps from Tier 3 remain open and untouched here** — `TokenResponse.History`
  (truncation) and `.Reasoning` are still read by no client; C2 deliberately did not widen its
  blast radius to fix them. Same client-side bucket as this cluster's C2 render work; a natural
  next pass.

### Closing statement
**Done:** C1, C2, C3 implemented as three isolated commits; both shipped clients render the C2
signal; a status surface and optional durable log exist. **Verified:** every fix through the real
daemon binary and real client(s) over the real socket (bytes captured, real TUI driven in a pty,
real webview render path exercised), plus the full six-module regression suite including `-race`.
**Explicitly still open:** the `allow_fallbacks` config posture (founder's call); handshake-time
memory reporting; the Tier-3 client-render parity gaps (`History`/`Reasoning`); log *levels*
(scoped out with reason). **Nothing marked closed — that is the founder's call, as always.**

## Backlog — added 2026-07-23 (fallback-provider ZDR posture — EXTENDS 3A, does not stand alone)

**This is an extension of the open OpenRouter-ZDR question in checkpoint Part 3A, not an independent
finding.** 3A asks whether the ZDR guarantee covers the *implicit prompt-caching* path on the
primary route. This asks the adjacent question Tier 4's C2 work surfaced: shipped `models.json`
sets `zdr.allow_fallbacks:true`, which permits a request to be served by a *different provider*
when the primary is unavailable — and nothing had verified whether that fallback provider is still
constrained by `zdr:true` / `data_collection:"deny"`. Investigated 2026-07-23. **Nothing changed;
nothing closed. No code touched** — the daemon's own behavior is correct and conservative; the one
residual is on OpenRouter's side and is the same *shape* of open question as 3A itself.

### D1 — current behavior, live (both routing paths)
Reproduced with the prior tiers' local-capture technique: a real daemon + real socket, only the
model API (and, for the proxy path, a stub Supabase) replaced locally so the **actual outbound
request body** could be read as bytes. Fault injection forced the two conditions `allow_fallbacks`
is about.

| Path | Trigger | zdr / data_collection / allow_fallbacks observed on the captured request | Verdict |
|---|---|---|---|
| Direct (`provider.go`) | normal | `{"zdr":true,"data_collection":"deny","allow_fallbacks":true}` | all three present, together |
| Direct | provider **outage** (503 → 3 retries) | **identical** block on **all 3** attempts | no downgrade-on-retry |
| Direct | **ZDR refusal** (404, OpenRouter's real "No endpoints … Zero data retention" body) | **1** attempt only; classified `privacy_refused`; **no** weakened re-send | **fails closed** |
| Proxy (`proxy/main.go`) | normal | same block arrives at upstream through the proxy | forwarded **byte-for-byte** |
| Proxy | ZDR refusal | 1 attempt; 404 passes through; daemon → `privacy_refused` | proxy adds no downgrade |
| Direct | config `allow_fallbacks:false` | outbound block becomes `…"allow_fallbacks":false` | the lever transmits faithfully |

**What D1 establishes (our side):** there is only ever **one** outbound body per attempt, always
carrying all three fields together; an OpenRouter "fallback" is *internal re-routing within that one
body's filters*, not a distinct weakened request the daemon or proxy constructs. `streamWithRetry`
(`daemon/retry.go:102`) reuses the same `routing` object across every attempt; `privacy_refused` is
non-retryable (`daemon/modelerror.go:72`), so a no-eligible-provider refusal fails immediately
without any weakened re-send. The proxy forwards the body unchanged (`proxy/main.go:294`,
`349`). **Our side is clean and fails closed** — confirmed live on both paths, not read from code.

**What D1 cannot establish:** what OpenRouter does *internally* when it selects a fallback provider
— because the local capture server stands in for OpenRouter. That is the D2 question.

### D2 — OpenRouter's documented position (desk research, 2026-07-23)
- The dedicated ZDR doc states verbatim: **"When `zdr` is set to `true`, the request will only be
  routed to endpoints that have a Zero Data Retention policy."** Absolute language, no fallback
  carve-out. The provider-routing reference calls `zdr` "Restrict routing to only ZDR (Zero Data
  Retention) endpoints."
- **But the exact edge is documented nowhere.** The ZDR doc does **not** state what happens when no
  ZDR endpoint is available, nor how `zdr` interacts with `allow_fallbacks`. `allow_fallbacks` is
  described only as "Whether to allow backup providers when the primary is unavailable," with no
  statement on whether backups are still filtered by `zdr`/`data_collection`. No doc example
  combines them.
- OpenRouter's data-residency blog frames the *strongest* guarantee as tied to
  `allow_fallbacks:false` ("returns an error instead of routing to a provider outside your list") —
  which is about the explicit `order`/`only` provider list, a mechanism this daemon does not use
  (it sends a bare `provider` object with no list).
- **Net:** the plain reading of OpenRouter's own ZDR wording is that `zdr:true` is an absolute
  filter that fallback does not override, i.e. `allow_fallbacks:true` most likely means "fall back
  *among ZDR-compliant endpoints*," not "abandon ZDR if none are up." But this is a **documented
  engineering position in prose, not a contractual guarantee in the DPA/ToS** — the *identical*
  limitation 3A already found for the caching question. This extends 3A; it is not a new class.
- Sources: [ZDR guide](https://openrouter.ai/docs/guides/features/zdr),
  [Provider routing](https://openrouter.ai/docs/guides/routing/provider-selection),
  [AI data residency (blog)](https://openrouter.ai/blog/insights/ai-data-residency/),
  [Model routing (blog)](https://openrouter.ai/blog/insights/model-routing/).
- **Outreach — folded into 3A's existing channels, no fourth opened.** This is the same "is the ZDR
  position contractual, and what are the exact edge semantics" question as 3A, so it belongs with
  the same contacts. The support **ticket #37409** is noted as narrowly scoped and near resolution —
  a poor fit for a new semantics sub-question; do not reopen it for this. The **Discord thread**
  (#community-help, escalated to mods) and the **LinkedIn** technical-staff contact are the right
  fits. Specific question to add, verbatim-ready: *"With the per-request provider object
  `{zdr:true, data_collection:'deny', allow_fallbacks:true}` and no `order`/`only` list, if the
  primary endpoint for a model is unavailable, is the fallback endpoint still required to satisfy
  `zdr:true`/`data_collection:'deny'` — or can `allow_fallbacks:true` cause routing to a non-ZDR
  provider? And if no ZDR endpoint is available at all, does the request error (as it does with
  `allow_fallbacks:false`) or route anyway?"*

### D3 — severity (argued from D1/D2, correctable either way)
**Low, and explicitly UNVERIFIED at the exact edge — not dismissable to zero.** Reasoning:
- *Toward lower:* our side is provably clean and fails closed on both paths (D1); the ZDR doc's own
  language ("will only be routed to ZDR endpoints") reads as absolute; the daemon has a live-observed
  hard-refusal path it does **not** downgrade around.
- *Against dismissing it:* the precise `allow_fallbacks:true` + no-ZDR-provider case is documented
  **nowhere** (D2), and our one live refusal (`provider.go:112`, **2026-07-09**) predates the
  `false`→`true` flip (**2026-07-10**), so that "hard refuse" evidence is from the
  `allow_fallbacks:false` regime and does **not** cover the current config. And the position is
  prose, not contract (same as 3A).
- **This is deliberately NOT symmetric with the 2A edge-cache finding**, which went fully "off the
  table" once *our own request code* was checked. Here, checking our code confirms our side is
  clean — but the residual lives on OpenRouter's side and stays open, exactly like the 3A caching
  question it extends. Downgrading the codebase's existing "the ZDR filter still constrains the
  pool" assertion (BACKLOG 2026-07-17) from *asserted* to *documented-but-unconfirmed-at-the-edge*
  is the honest correction — the claim is well-supported by OpenRouter's wording but was never
  verified for the fallback edge specifically.
- *Urgency note (bears on priority, not on whether it's worth resolving):* fallback is **not
  dormant** here — `allow_fallbacks:true` was set specifically to escape persistent DeepInfra 429s
  (BACKLOG 2026-07-10/07-17), i.e. a real fallback demonstrably fires for the active model in normal
  operation. So this is a live routing behavior, not a hypothetical.

### D4 — remediation options (for the founder; none selected)
1. **Ship `allow_fallbacks:false`.** Strongest ZDR posture — OpenRouter's docs put the hard
   "error-instead-of-route-outside-constraints" guarantee here. **Cost:** re-breaks the 2026-07-10
   congestion fix (DeepInfra 429s) — the known tension already recorded; availability drops when the
   primary is busy. Not free.
2. **Keep `allow_fallbacks:true` but add an explicit `order`/`only` provider allow-list** of
   endpoints independently confirmed ZDR-compliant. Keeps failover among vetted providers. **Cost:**
   a per-model allow-list to build and maintain as OpenRouter's provider set changes; the daemon
   currently sends no list, so this is new config surface.
3. **Surface the actual served provider to the client** so a user can see which endpoint served
   their turn. **Corrected 2026-07-23 — see the correction block below; the original wording of this
   option overstated what C2 provides and understated what the daemon already has.** Reality: the
   per-request serving-provider identity is *already obtained and logged today* — OpenRouter's
   streaming response carries a top-level `provider` field on its SSE chunks, the daemon already
   parses it (`chatCompletionChunk.Provider`, `daemon/provider.go:74`/`247`) and already logs it
   server-side (`server.go:379`, `model API served by provider=…`). The unbuilt part is only
   *surfacing that already-captured value to the client per turn over the protocol* — a genuinely
   small wiring item, **not** gated on option 2 and **not** a new OpenRouter capability. C2's
   `provider_routing` degradation is a *different* mechanism (a config-derived boolean, "fallbacks
   permitted") and does **not** "already position" the served-provider identity. **Cost / limits:**
   OpenRouter reports *which* provider served a request but **not** *whether* it was reached via
   fallback (D2), so "this turn fell back" still cannot be shown truthfully; only "served by X" can.
   The allow-list from option 2 is needed *only to map X→ZDR-status*, not to obtain X.
4. **Leave as-is, documented.** Defensible **iff** D2's outreach confirms the fallback set stays
   ZDR-filtered. Until then this is "accept an unverified edge," not "confirmed fine."

**No option selected — this is a product-posture decision (availability vs. strongest-provable-ZDR
tradeoff), explicitly the founder's call.**

### Correction (2026-07-23) — D4 option 3 overstated C2 and understated the daemon
The original D4 option 3 (in commit `57c95f7`) read "Tier 4's C2 `provider_routing` degradation
already positions this … only 'served by provider X' could be [shown], and mapping X→ZDR-status
needs the allow-list from option 2 anyway." That framed per-request provider identity as data gated
on option 2's allow-list, leaning on C2 to imply it was "already positioned." That was wrong in both
directions. The correction, and how it was checked:

- **What C2 actually is.** C2's `provider_routing` degradation is *config-derived* — a boolean that
  says fallbacks are *permitted*, read from `models.json`, not from any response (BACKLOG C2 note,
  line ~1498: "OpenRouter reports which provider served a request but not whether a fallback was
  used, so a per-request 'this one fell back' claim would be fabricated"). It does **not** carry a
  per-request served-provider identity, so it does not "already position" option 3.
- **What the daemon actually already has (this is the part option 3 missed entirely).** The
  per-request serving provider is **already obtained and logged today**. OpenRouter's streaming chat
  completion emits a top-level `provider` field on its SSE chunks; the daemon decodes it into
  `chatCompletionChunk.Provider` (`daemon/provider.go:74`), fires `onProvider` on first sighting
  (`provider.go:247-251`), and the handler **logs it server-side**: `server.go:379`,
  `model API served by provider=%q (zdr=… data_collection=… allow_fallbacks=…)`. So "served by X"
  is not a to-be-obtained value gated on option 2 — it is in hand every turn and already in the
  daemon log. Option 2's allow-list is needed *only* to map that X to a ZDR-status verdict, **not**
  to obtain X. The only unbuilt work in option 3 is wiring the already-captured provider value into
  the *client-facing* protocol per turn (it is server-log-only today) — a small, real scope item.
- **What remains genuinely unavailable.** OpenRouter reports *which* provider served a request but
  **not** *whether* a fallback occurred (D2 — the docs describe `allow_fallbacks` with no response
  signal for it). So "this turn fell back" stays unshowable; "served by X" is the truthful ceiling.
  That half of the original wording was correct and is retained.

- **How this was verified, and the honest limit on it (per the no-rounding-up rule).** The provider
  field's *presence in a real OpenRouter response* rests on: (a) OpenRouter's own streaming docs,
  which show a top-level `"provider"` field on SSE chunks (e.g. `"provider":"openai"`); and (b) the
  daemon's own code comment at `provider.go:67-73`, "observed in practice, not formally guaranteed on
  every chunk by OpenRouter's docs" — i.e. prior live observation by this codebase, plus the active
  `server.go:379` log that only exists because the field is seen in practice. It was **not**
  re-verified with a fresh live real-OpenRouter capture this session: no OpenRouter/proxy API key is
  available in this environment (only a Gemini key), and — the load-bearing caveat — **the D1
  capture method structurally cannot answer this question at all.** D1's "local capture point"
  (`capture_api2.py`) *replaces* OpenRouter with a stub, and that stub *fabricates* the provider
  field (`sse({"provider": "CaptureProvider", …})`) precisely because the daemon expects to read one
  — so no D1 capture, past or re-run, is evidence about what *real* OpenRouter returns. Net, honestly
  stated: the served-provider field is well-supported as present (OpenRouter docs + the daemon's
  in-practice observation and live logging) and is already parsed+logged; the one unverified residual
  is the daemon comment's own hedge — presence is observed, per-chunk completeness is *not* a
  documented OpenRouter guarantee. This corrects an open item; it closes nothing.

### Closing statement
**Verified (live, both paths):** the daemon and proxy send `zdr`/`data_collection`/`allow_fallbacks`
together in one body every time, never downgrade on retry, and fail closed on a ZDR refusal; the
`allow_fallbacks:false` lever transmits faithfully. **Documented (D2):** OpenRouter says `zdr:true`
routes "only to ZDR endpoints" but is silent on the fallback edge and the no-ZDR-provider case, and
the position is prose not contract — an extension of the open 3A question, folded into 3A's existing
channels. **Still open:** whether an `allow_fallbacks:true` fallback can reach a non-ZDR provider
(OpenRouter-side, unverified); and the D4 posture choice. **Severity:** Low but unverified-at-the-edge,
not dismissable. **Nothing marked closed — founder decides.**

## Backlog — added 2026-07-23 (per-turn provider identity surfaced to clients — D4-option-3 client half)

**This ships ONLY the client-surfacing half of D4 option 3 (log-only → wire-visible → rendered). It
does NOT resolve D4's posture question or 3A / its extension, does not touch `allow_fallbacks`, does
not build the option-2 allow-list, and adds no ZDR-status judgement.** Independent of whichever
posture the founder eventually picks: the daemon receives and logs a `provider` value regardless of
posture, and a client should be able to see it regardless (E-cluster, this commit).

The corrected D4 option 3 (commit `eee2f03`) established that per-request provider identity is data
the daemon already had every turn — it decoded OpenRouter's top-level `provider` field
(`daemon/provider.go:74`, `onProvider` at `247-251`) and logged it server-side (`server.go:379`),
but never surfaced it to any client. That was the whole gap. This closes it.

### What was built
- **E1 — wire.** New `TokenResponse.Provider` (`protocol/protocol.go`, `omitempty`). It rides on its
  OWN message, emitted from the existing `onProvider` closure (`server.go`), NOT the pre-token
  Grounding message — the provider is not known until the response stream begins. Absent is dropped
  by `omitempty` (a healthy/older response stays byte-identical). The server-side log line is kept.
- **E2 — both shipped clients render it (Fix-13 / Tier-2.5 trap avoided).**
  - **TUI:** a `providerMsg` (`stream.go`), one neutral header line `served by: <provider>` via
    `providerLabel` (`chat.go`), on its own row like the degradation lines (a joined line can
    soft-wrap and desync the viewport). Deliberately `helpStyle` (subtle), NOT the `⚠`+red
    `errorStyle` the degraded/redaction notices use — a served-by-X report is a fact, not a warning.
    Cleared at the start of every turn and on ctrl+n, like grounding/redactions/degraded.
  - **VS Code:** `onProvider` → `provider` message → `setProvider` renders `#provider` (`main.js`),
    a neutral `#grounding`-style region (`chatPanel.ts` CSS/HTML), `textContent` never `innerHTML`,
    cleared each turn in `send()`. `out/` recompiled.
- **Labelled "served by," never "fell back"/"routed to."** OpenRouter reports *who* served a request,
  not *whether* that was a fallback (D2), so fallback-vs-primary stays unshowable — carried forward,
  not rediscovered. No ZDR verdict is shown (that needs the still-open allow-list + outreach).
- **Absence is a normal, non-error state.** OpenRouter does not guarantee the field
  (`provider.go:67-73`); a turn with no provider renders as nothing in both clients, never red/degraded.

### Verification (live; stub-labelled where it must be)
- **Wire (raw socket bytes, `wire_probe.py`, shares no structs with the daemon):** present → a
  dedicated line `{"protocol_version":1,"done":false,"provider":"DeepInfra"}` crossed the socket
  (own message, after grounding, before the token), and `server.go:379` logged it; absent → **no**
  `provider` field anywhere on the wire, no log line, response byte-identical to before.
- **TUI (real `ct-tui` binary in a pty against a real daemon):** present → painted a neutral
  `served by: DeepInfra` line between grounding and the degraded line; absent → no `served by` line
  at all.
- **VS Code (real `media/main.js` render path against a DOM stub):** present → `#provider` =
  `"served by: DeepInfra"`; absent/falsy → `""`; no `⚠`/degraded/fallback connotation. `tsc --noEmit`
  clean.
- **Guards:** daemon wire (`TestProvider_IsSurfacedOnTheWire`, `TestProvider_AbsentWhenUpstreamOmitsIt`,
  `daemon/response_robustness_test.go`) and TUI render/lifecycle (`clients/tui/provider_test.go`).
  Full build/vet/gofmt/test green on the touched modules; daemon + TUI `-race` clean on the provider
  paths.
- **STUB caveat, stated plainly (per the E3 rule and the D4 correction):** the upstream in every live
  run above is a LOCAL stub that *fabricates* the `provider` value — it is evidence the daemon reads
  the wire field and both clients render it, **NOT** evidence about what real OpenRouter returns.
  No OpenRouter/proxy key is available in this environment. What still needs a real key to confirm:
  that genuine OpenRouter responses actually carry the top-level `provider` field in practice (the
  daemon's `provider.go:67-73` "observed in practice, not formally guaranteed on every chunk" comment
  and OpenRouter's streaming docs are the current, non-live basis for that — see the D4 correction).

### Surfaced but NOT fixed (recorded, not acted on)
- **No allow-list, no ZDR-status label (E4 scope hold).** Mapping a shown provider → ZDR-eligible is
  D4 option 2 and is deliberately not built here; the panel shows the fact, not a verdict.
- **Handshake-only clients see no provider.** Like C2-b memory, the value rides the first prompt of a
  turn; a preflight/hydrate-only connection never sees one. Same small deferred shape as C2-b.

**Nothing marked closed.** This is the client-surfacing half of one D4 option, shipped; the D4 posture
decision and the 3A/extension OpenRouter-side residual remain open and the founder's call.

---

## C1 + C2 ship-blocker fixes (CTO/beta-test bug-hunt follow-up) — 2026-07-23

Source: `~/.claude/plans/what-can-we-improve-snappy-music.md` (report-only pass; no code was
changed there). This section is the audit-first / fix-second / document-third record for its two
NEW criticals, C1 and C2. Nothing here is marked closed — founder confirms.

### C1 — Step 0 determination (reachability of the embedder-response deref) — WRITTEN BEFORE THE FIX

**The contradiction.** The report calls C1 a production-reachable CRITICAL: "one malformed embedder
response crashes the whole daemon," reachable "right after an edit apply during reindex." A standing
backlog item said the opposite: the missing `handleConn` `recover()` "is not triggered by anything
today." One of the two had to be wrong.

**What was reproduced, live, over the real wire.** A new fixture mode (`FAKEHELPER_SHORT_VECTORS` /
`FAKEHELPER_TRUNCATE` in `daemon/testdata/fakehelper`) drives the REAL helper protocol over a REAL
Unix socket through the REAL `HelperProcess` + `BgeEmbedder` (`daemon/embedder_boundary_test.go`).
Two shapes the report conflated separate cleanly:

1. **Truncated / partial wire read** (`FAKEHELPER_TRUNCATE`: writes a half JSON body, drops the
   conn) → `json.NewDecoder(conn).Decode` returns an ERROR → `HelperProcess.Embed` returns that
   error → every call site's existing `if err != nil` catches it. It **never becomes a short vector
   slice.** `TestEmbedderBoundary_TruncatedResponseIsError` passes before and after the fix. So the
   report's cited "truncated wire read" mechanism is **not** a deref trigger.
2. **Well-formed `ok:true` response carrying fewer vectors than texts** (`FAKEHELPER_SHORT_VECTORS`)
   → this is the ONLY shape that reaches the unchecked deref. Live result against pre-fix code:
   `retrieveTopK` panics `index out of range [0] with length 0`; that panic propagates through
   `gatherContext` → `serveConn` → `handleConn` → the `Serve` goroutine (no `recover()`), i.e. it
   would crash the whole daemon.

**Can a REAL embedder produce shape #2? No — verified by source, not memory.** The real ONNX helper
`helper/onnxembedder.go:Embed` returns `make([][]float32, batch)` fully filled (exactly `len(texts)`)
or `nil, err`; `helper/main.go:dispatch` maps any error to `ok:false`. Therefore **no `ok:true`
response the shipped helper can emit ever carries a mismatched count** (including the 0-texts→0-vecs
case). Every real failure mode of the round trip — timeout, connection drop, partial write, restart
race, dial failure — produces a decode/dial ERROR (shape #1), which is already handled. Shape #2
requires a genuinely buggy or malicious helper binary, which the shipped one is not.

**Determination.** The prior backlog assessment — "not triggered by anything today" — was **CORRECT**
for the bounds bug. The report's CRITICAL / confirmed-ship-blocker severity for the deref's
production-reachability is **OVERSTATED and is corrected here to: a latent defensive gap, not a
confirmed ship-blocker.** A hand-driven lying helper proves the bug EXISTS; it does not prove
production traffic triggers it, and the source proves production traffic cannot. Two things are
nonetheless worth fixing, on their own merits and independent of shape #2's reachability:
  - **The missing `recover()`** in the connection handler is a real, general availability gap: ANY
    panic in ANY request handler currently drops every in-flight client, not just this one. This is
    the FAIL-3 backstop already scoped in the backlog; it is fixed here on that standing merit.
  - **The boundary length-check** is cheap, closes the latent deref, and hardens the daemon against a
    future helper regression or a swapped-in third-party embedder that does not honour the contract.

### C1 — fix (two complementary, both under C1's scope)
- **Boundary validation (input validation).** `HelperProcess.Embed` now requires
  `len(resp.Vectors) == len(texts)` and returns a descriptive error otherwise, at the single point
  untrusted subprocess output crosses into the daemon (the report's named "root cause",
  `helperproc.go`). This one chokepoint protects every downstream deref (`retrieveTopK`'s `vecs[0]`,
  `buildIndex`/`reindexFile`'s `vecs[i]`) — none of them can now be reached with a short slice.
- **Panic backstop (defense-in-depth).** `handleConn` gained a `recover()` so a panic in one
  connection's handling logs + closes that one connection instead of terminating the process. The
  `Serve` comment that previously said the slot is freed "without needing a recover() (the panic
  still propagates unchanged)" was updated to reflect that a recover now exists at the handler.

### C2 — fix (forward edit-write symlink + atomicity hardening)
- `editapply/Apply` previously wrote the forward path (edit AND create) with a plain
  `os.WriteFile` (`apply.go:228`), while the undo path was hardened (temp file + atomic rename,
  symlink refusal) and its comments *assumed* a forward parity that did not exist. The forward write
  now goes through an atomic, symlink-refusing writer that mirrors the undo pattern: refuse if the
  destination leaf is a symlink, else write a temp file in the target's directory, `chmod` it to the
  prepared mode, and `os.Rename` it into place.
- Closes both halves independently: (a) **escape** — a dangling/leaf symlink planted at a
  to-be-created target during the TUI confirm window can no longer be written *through* (`os.Rename`
  never follows the link, and the pre-write `Lstat` refuses an anomalous symlink outright); (b)
  **non-atomic corruption** — a crash/interruption mid-write can no longer leave a truncated file,
  because the target is only ever swapped by an atomic rename of a fully-written temp.
- The undo-path comments that asserted forward/undo parity are now accurate.

### Validation evidence (2026-07-23)

**C1 — through the real helper wire, and the real connection door.**
- `daemon/embedder_boundary_test.go` (real `HelperProcess` + `BgeEmbedder` + fakehelper over a real
  socket): `TestEmbedderBoundary_TruncatedResponseIsError` (truncated read → error, passes both
  before and after — the report's "truncated wire read" is not a deref trigger);
  `TestEmbedderBoundary_ShortVectorCountDoesNotPanicOnRetrieve` (drives `retrieveTopK`; PANICS
  `index out of range [0] with length 0` pre-fix, clean error post-fix);
  `TestEmbedderBoundary_ShortVectorCountFailsAtBoundary` (the multi-text index/reindex `vecs[i]`
  path; `HelperProcess.Embed` returns a descriptive error post-fix). Both fail-when-neutered:
  removing the length check restores the panic and the nil error.
- `daemon/handleconn_recover_test.go`: `TestHandleConn_PanicIsContainedToOneConnection` drives a
  DIFFERENT panic (a panicking embedder) through a real `Server.Serve`/`handleConn` over a real
  socket; connection A panics and is contained, connection B still gets a well-formed response.
  Fail-when-neutered verified: with the `recover()` removed, A's panic propagates out of the Serve
  goroutine and crashes the whole test binary (stack: `retrieveTopK` → `gatherContext` → `serveConn`
  → `handleConn` → `Serve.func1`).

**C2 — through the real Apply door, and the writer unit.**
- `editapply/apply_forward_symlink_test.go`:
  `TestApply_ForwardWriteRefusesDanglingLeafSymlinkEscape` (the report's primary escape: a dangling
  symlink planted at a create target in the confirm window; pre-fix `os.WriteFile` creates the victim
  OUTSIDE the workspace, post-fix Apply refuses and nothing appears outside);
  `TestWriteFileAtomicNoFollow_RefusesSymlinkAndPreservesVictim` (the writer unit refuses a symlinked
  destination pointing at an existing outside file, leaving it byte-intact — this case is caught
  upstream by VerifyUnchanged at the Apply level, so the writer is exercised directly);
  `TestApply_ForwardWriteIsAtomicRename` (the target's inode changes on edit and no `.codeterminal-
  apply-*` temp is left — proof the commit is a temp+rename, not an in-place rewrite). All
  fail-when-neutered against the former plain `os.WriteFile`.
- Atomicity's rollback edge stays covered by the existing `TestApply_WriteFailureRollsBackAfter
  Snapshot` / `...RestoresPriorAfterSnapshot`, updated to inject the write failure via a read-only
  target DIRECTORY (an atomic rename tolerates a read-only target FILE), preserving their intent.

**Regression (six modules, explicit paths — `./...` fails from root).** `gofmt -l` clean;
`go build ./...` + `go vet ./...` clean for `daemon`, `editapply`, `protocol`, `clients/tui`,
`helper`, `proxy`; `go test -count=1 ./...` green (protocol/helper have no test files);
`go test -race -count=1 ./...` green for the four modules with tests. Commits: C1 `ee9e992`
(daemon), C2 `c7cff6c` (editapply) — separate, as required.

**Nothing marked closed.** Step 0 downgraded C1's deref reachability from confirmed-ship-blocker to
latent-defensive; the `recover()` availability gap and both C2 halves are fixed and verified. The
founder confirms closure.

---

## 2026-07-23 — M1–M3 client-UX fix batch (three CTO-report mediums; verified, NOT closed)

The three client-UX mediums from the CTO/beta-test report, in its stated priority ("the M1–M3
client-UX trio for a visibly more honest product"). Three isolated commits; each reproduced/verified
live through production entry points (real daemon, real socket, real TUI in a pty, the real compiled
VS Code `daemonClient`, and the real `media/main.js` under a DOM shim). Full six-module regression
below. **Nothing pushed; nothing marked closed — founder's call.**

### Step 0 — M3 duplication determination (done first, in writing)
**M3 ("reasoning tokens silently dropped by both clients") is the SAME bug as HANDOFF §3B's
pre-existing Tier-3 render-parity item** (`TokenResponse.Reasoning` reaches the wire, no client
renders it), verified in source: the daemon emits reasoning on its own message (`server.go:418`),
the TUI never reads `tok.Reasoning` (`stream.go`), and the VS Code `TokenResponse` had no `reasoning`
field at all. Not distinct work — one client-side fix that closes both. **Step 0 part 4:** the other
two parity gaps in that same §3B item — `HistoryInfo.Truncated` (dropped both clients) and
`GroundingInfo.Truncated` (dropped by the TUI; VS Code already rendered it) — are cheap (same files,
same dispatch-a-field→render pattern), so folded into the M3 commit and stated there. They are
DISTINCT truncations from M1 (answer cutoff): context-trimmed vs. oldest-turns-dropped vs.
answer-cut-off — kept labelled distinctly.

### M1 — truncated-stream visibility (`029d764`)
Root cause: the daemon decoded only `delta.content` and never read the SSE `finish_reason`, so a
`length` cutoff (model hit its output ceiling mid-sentence) returned a `Done` byte-identical to a
complete answer. Fix: `streamCompletion` now captures the terminal `finish_reason` and reports it
once via a new `onFinish` callback (threaded through `streamWithRetry`, success-path only);
`server.go` maps it to a new `protocol.TokenResponse.Incomplete` (`*IncompleteInfo`: stable reason
slug + client-safe detail, Gate-7 clean) on the final `Done` message — `nil` for a natural `stop`.
TUI renders a persistent `⚠ answer cut off: <detail>` roleSystem notice (dropped from history); VS
Code renders a warning-styled `.incomplete-notice` turn. **Live:** stub upstream with
`finish_reason="length"` → `incomplete{reason:"length"}` on `Done`; `"stop"` → field absent
(byte-distinct); real TUI (pty) shows the notice; real compiled `daemonClient` fires `onIncomplete`
before `onDone`. Tests: `daemon/incomplete_test.go`, `clients/tui/incomplete_test.go`
(fail-when-neutered). Connection-drop / daemon-exit mid-stream are NOT covered by this signal (the
daemon that died can't annotate its final message) — noted as a separate client-side concern.

### M2 — VS Code panel wedging on a CLEAN daemon close (`b647490`)
Root cause: `applyEdit`/`undoEdits`/`searchConversations` in `daemonClient.ts` resolved only on a
reply line and rejected only on socket `'error'` — **no `'close'` arm**. A graceful daemon shutdown
mid-request (`ln.Close()` + process exit → FIN, no reply, no error) left the promise unsettled
forever; in an auto-apply run `runAutoApply` awaits it, so `autoApplyRunInFlight` stuck `true` and
`onPrompt`'s guard then silently dropped every later prompt (the webview had re-enabled input on the
stream's `done`, so the user typed, hit Send, and nothing happened). Fix: a `'close'` handler
(guarded by a shared `settled` flag) on all three helpers → rejects with a clear message instead of
hanging. **Live (before/after):** transport level — pre-fix `applyEdit` hangs >3s, post-fix rejects;
panel level (real compiled `ChatPanel` + fake `vscode` + real `daemonClient` + stub daemon closing
mid-apply, auto-apply ON) — pre-fix WEDGED (run never summarizes, second prompt silently dropped),
post-fix RECOVERED (run reports its outcome, next prompt accepted). No in-repo VS Code test runner
exists (tsc-only bar); the scratchpad harnesses are the verification.

### M3 + folded parity — reasoning & truncation render (`6995259`)
No daemon change — purely the client read+render halves. TUI: a dimmed `💭 thinking:` block per
assistant turn, accumulated in a SEPARATE `turn.reasoning` field never spliced into the answer text
(which carries back as history / is parsed for edits); a header warning when history was dropped;
`(context truncated)` on the grounded label. VS Code: `onReasoning` → a dim italic `.turn.reasoning`
block placed above the answer (its own block); `onHistory` → a `#historyNotice` dropped-turns
warning (`historyInfo` message, kept distinct from the `history` hydration message);
grounding-truncation already rendered. **Live:** real TUI (pty) renders the thinking block; real
compiled `daemonClient` fires `onReasoning` in order before tokens; real `media/main.js` under a DOM
shim renders the thinking block (verified SEPARATE from the answer), the dropped-history warning, and
the context-truncated label. Tests: `clients/tui/reasoning_test.go` (incl. reasoning-never-in-answer,
fail-when-neutered). **This closes both the CTO report's M3 and the pre-existing §3B render-parity
item** (Reasoning + History + GroundingInfo.Truncated) — one item, not two.

### Regression (six modules, explicit paths — `./...` fails from root)
`gofmt -l` clean; `go build ./...` + `go vet ./...` clean for `daemon`, `editapply`, `protocol`,
`clients/tui`, `helper`, `proxy`; `go test -count=1 ./...` green (protocol/helper have no test
files); `go test -race -count=1 ./...` green for the modules with tests; `tsc --noEmit` clean for the
VS Code extension. Commits M1 `029d764`, M2 `b647490`, M3 `6995259` — separate, as required.

### Not touched by this batch (remain open, per the report's own remaining list)
nested-`.gitignore` secret indexing; the `.GIT` case-fold bypass; C3 (billing abort-refund); F1
(proxy ZDR-enforcement); and all founder-gated P3 / Gate-6 / ZDR items. **Nothing marked closed —
founder decides.**

## 2026-07-23 — S1 + S2 security fixes and the VS Code E2E harness (verified, NOT closed)

Three independent concerns from the S1/S2/E2E master prompt, done as three isolated commits.
Part-0 discipline throughout: each claim reconfirmed live against current `main` before any fix
(the M1-batch C1 precedent — a prior report's severity claim that didn't survive live repro — was
the explicit warning). **Nothing pushed, nothing marked closed; founder decides.**

### S1 — nested `.gitignore` secret indexing — CONFIRMED live, FIXED (`ae1104c`)

**Step 0 (reconfirmed, not inherited).** Built a real workspace through the real indexing door
(`ScanWorkspace`, the same function `buildIndex` calls): clean root `.gitignore` (`*.log`), a
`config/` subdir with its own `config/.gitignore` excluding a **benign-named** file
`local-settings.json` (deliberately NOT secret-shaped — no `secret`/`credential` substring, no glob
hit — so `MatchesSecretName` can't mask the question) whose content was a fake API token. Result:
`nested-ignored secret indexed = true`, chunk `config/local-settings.json:1-2` captured, skip counts
`map[noise:2]` with **no gitignore skip**. Confirmed: only the workspace-root `.gitignore` was ever
read (`loadGitignore(realRoot)`), exactly as the old code's own comment admitted ("does not support
… nested .gitignore files … reads only the workspace root's own .gitignore"). Per the
chunk-text-network-exit finding, an indexed chunk is retrieval-eligible and leaves the machine
unscrubbed in a RAG completion POST — so this is a real upstream-exposure path, not a local-only
cosmetic.

**Scope determination (which nested scenario triggers it).** Single-level reproduced directly;
the fix was written to git's actual semantics (each directory's `.gitignore` applies to its own
subtree, patterns relative to that file, deeper overrides shallower, later line overrides earlier =
`!` negation) rather than guessing, and the regression tests exercise single-level exclusion, subtree
scoping (an identically-named file in a *different* subdir stays indexed — guards against a naive
"flatten all `.gitignore`s into one root-relative set" fix), `!` re-include, nested-dir prune, and
root-behavior-unchanged.

**Fix.** No gitignore-matching library is vendored (checked all six `go.mod`; the environment is
offline for new modules regardless), and the project's ethos is a minimal hand-rolled subset — so the
root-only `gitignoreRules` was replaced with a per-directory `gitignoreMatcher` (`daemon/chunker.go`)
that lazily loads and caches each directory's `.gitignore`, keyed by root-relative dir, and resolves
a path by walking its ancestor layers shallow→deep with last-match-wins (git precedence, incl.
negation). The small *pattern* subset (basename/relative `filepath.Match`, dir-only trailing slash,
dir-prefix) is preserved and now applied at every level; unsupported patterns still fail toward
*excluding* (the safe direction for a secret boundary). The same matcher is now used by `reindexFile`
(`reindex.go`) and the `file:line` resolver (`fileref.go`), so no shorter path re-admits a
nested-ignored file.

**The MatchesSecretName ordering question (asked explicitly, answered).** In `shouldSkipFile`,
`MatchesSecretName` runs BEFORE ignore-resolution. So a secret-*named* file (`.env`, `id_rsa`,
anything with `secret`/`credential` in the basename, etc.) was **never** exposed by this bug — it was
already skipped regardless of gitignore. The S1 leak was therefore specifically **non-secret-named
files that contain secrets** and are excluded only by a nested `.gitignore` (config files, `.local`
overrides, etc.). Fixing indexing does not make the secret-name check moot in general (it still
guards secret-named files that no `.gitignore` excludes) — the two gates are independent and both
needed.

**Verify.** Re-ran Step 0 post-fix: `nested-ignored secret indexed = false`, skip `map[gitignored:1
noise:2]`. Regression tests (`daemon/nested_gitignore_test.go`) drive the real `ScanWorkspace`.
Fail-when-neutered proven: reverting the matcher to root-only makes the exclusion/scoping/dir-prune
tests fail with the exact leak symptom (`nested-ignored file was indexed`), restored → green.

### S2 — `.GIT` case-fold bypass — CONFIRMED live, FIXED (`40b5980`)

**Recurrence-vs-new determination (asked explicitly).** This is the **same guard Tier 3 introduced
with an incomplete fix**, not a new code path. Tier 3's Fix 3 added the shared `ProtectedDirNames` +
`ProtectedDirComponent` and unified indexer-prune and edit-writer confinement — but keyed both on a
**case-sensitive** map lookup. Because the indexer and writer share that one list, the gap hit **both
consumers** at once.

**Step 0 (reconfirmed; case-sensitivity handled per the prompt).** The dev box is case-SENSITIVE
ext4, so — per the prompt's guidance that a case-sensitive compare is broken on a case-insensitive FS
regardless of what can be locally constructed — the repro used explicit case-variant paths. Indexer:
real `.GIT/…` and `.Git/…` directories were **indexed** by `ScanWorkspace` (`ignoredDirNames[".GIT"]
= false`). Writer: `ProtectedDirComponent(".GIT/hooks/pre-commit")` returned `""` (NOT refused), as
did `.Git/…` and `.SSH/…`. On a case-insensitive filesystem (macOS APFS, Windows NTFS) the OS
resolves `.GIT`→ the real `.git`, so: for the **writer**, a writable `.GIT/hooks/pre-commit` is the
same arbitrary-code-execution surface Tier 3 closed for the lowercase form; for the **indexer**, real
VCS/credential internals get read into the index.

**Fix.** New `editapply.IsProtectedDirName` folds the component to lower before the lowercase-keyed
`ProtectedDirNames` lookup; `ProtectedDirComponent` routes through it, so every writer path
(`ResolveSafeTargetPath`, create, undo) is covered at one choke point. The indexer prune is split
into `isPrunedDir` = case-INsensitive protected dirs (via `IsProtectedDirName`) OR case-SENSITIVE
`noiseDirNames`. Noise dirs (`build`, `vendor`, …) are deliberately left case-sensitive: they are not
a security boundary, and folding them would over-prune a legitimately-cased source dir that merely
shares a name (a Go package literally named `Build`). Grepped for other security-relevant literal
path-segment compares (`.git`/`.ssh`/`.aws`/`.codeterminal` equality/lookup/prefix) across all six
Go modules — **these two choke points were the only ones**; no siblings left as follow-ons.

**Verify.** Re-ran Step 0 post-fix against both doors: `.GIT`/`.Git`/`.SSH` variants are pruned by
`ScanWorkspace` and refused by `ResolveSafeTargetPath`; lookalikes (`notgit/`, `gitignore.txt`) still
NOT refused; `Build/` (capital) still indexed. Regression tests through the real doors
(`editapply/protected_casefold_test.go` for the writer incl. the create case; `daemon/casefold_prune_test.go`
for the indexer + the noise-case-sensitivity boundary). Fail-when-neutered proven: reverting
`IsProtectedDirName` to the case-sensitive lookup fails BOTH the writer test (`admitted
.GIT/hooks/pre-commit; code-execution path open`) and the indexer test (`indexed a protected-dir case
variant`), restored → green.

### Part 3 — real VS Code Extension Development Host E2E harness — BUILT & PROVEN (`5290c08`)

**Scope + technique choice (stated).** Added an in-repo `@vscode/test-electron` runner
(`clients/vscode/src/test/runTest.ts`) that launches a REAL Extension Development Host (downloads +
caches a real VS Code build under `.vscode-test/`), loads the real extension, and runs a mocha suite
*inside* the host. This is the runner the M1-M3 report said did not exist. For the M1/M2/M3 re-run it
uses the project's **local-stub-daemon** technique (`stubDaemon.ts`: a real Unix-domain-socket server
speaking the wire protocol + the real lockfile the client reads) rather than a live daemon, so the
suite is hermetic and controllable — but it drives the **actual compiled `daemonClient.ts`** (the
shipped module) in VS Code's own Node runtime over a real socket, which is a genuine step past the
report's hand-driven stubs.

**Proof the tool works (the required deliverable).** `smoke.test.ts`, in a real EDH: the real
extension **activates**; `codeterminal.openChat` renders a real **"CodeTerminal Chat" webview panel**
(the real `ChatPanel` + `media/main.js` in a real VS Code webview); the command is idempotent (one
panel, reveal not duplicate). Not "written", *run* — 3/3 passing.

**Bonus M1/M2/M3 re-run (reported as bonus, not a blocker).** `daemonClientE2E.test.ts`, same real
host, real compiled client over the stub socket: **M1** an incomplete `finish_reason` surfaces via
`onIncomplete` before `onDone`; **M2** a clean close before reply **rejects** `applyEdit` (does not
hang — the `b647490` wedge); **M3** grounding/history/reasoning fan out to their handlers. 3/3
passing.

**Honest scope boundary (stated, not papered over).** The bonus suite validates the **client/protocol
half** of M1/M2/M3. The **webview-DOM render half** (`main.js` turning these into a visible
incomplete-notice / dropped-turns warning / thinking block) is exercised *for real* by the smoke
test's panel render, but VS Code exposes no webview DOM to the test host, so this suite does not
assert those specific pixels per-behavior. Asserting them would require a test-only hook inside
production `main.js`, deliberately not added.

**Environment blocker found and fixed (reported plainly, per the prompt).** Running the harness from
inside VS Code's integrated terminal (this session) exports `ELECTRON_RUN_AS_NODE=1` and a set of
`VSCODE_*` vars belonging to the OUTER VS Code; `@vscode/test-electron` spawns the nested VS Code
inheriting them, so Electron ran as plain Node and treated the first launch arg as a script
(two successive "Cannot find module" crashes before any test ran — diagnosed from the stacks, not
guessed). `runTest.ts` strips `ELECTRON_RUN_AS_NODE` + `VSCODE_*` before launching. Also needed
`@vscode/test-electron` `^3.0.0` (2.5.2 predates the VS Code 1.130 / Node-24 bootstrap). With a real
`DISPLAY` on this box the full run is **6 passing, exit 0**, VS Code 1.130.0. No silent fallback to
the old stub approach — the real host runs.

### Regression (six Go modules, explicit paths — `./...` fails from root — plus the extension)
`gofmt -l` clean; `go build ./...` + `go vet ./...` clean for `daemon`, `editapply`, `protocol`,
`clients/tui`, `helper`, `proxy`; `go test -count=1 ./...` green (`protocol`/`helper` have no test
files); `go test -race -count=1 ./...` green for the four modules with tests. `tsc -p ./` clean for
the extension; the new EDH harness itself: **6 passing, exit 0**. Commits S1 `ae1104c`, S2 `40b5980`,
E2E `5290c08` — three separate concerns, three separate commits, as required.

### Still open after this batch (unchanged; founder-gated)
C3 (quota abort-refund / repeatable unmetered inference), F1 (proxy not ZDR-enforcing), and the whole
founder-gated cluster (P3 socket auth model, the Gate-6 closure contradiction, the OpenRouter ZDR /
`allow_fallbacks` questions, default-model evaluation). Untouched here by design. **Nothing marked
closed — founder decides.** *(Update: C3 was picked up next and is now FIXED — see the section
immediately below. F1 and the founder-gated cluster remain.)*

## 2026-07-23 — C3 billing abort-refund fix (bug-hunt follow-up; verified, NOT closed)

The bug-hunt report's third critical (the one the M1–M3 and S1/S2/E2E batches each listed as still
open). Fixed as one isolated commit in the `proxy` module. **Nothing pushed, nothing marked closed —
founder confirms, per Part 0 rule 5.** Distinct from the two *other* things also labelled "C3" in this
repo (Tier-4 observability C3 `2241daa`; do not conflate — see the HANDOFF's C1/C2/C3 disambiguation).

### The bug (traced in code, not inherited)
The managed proxy meters usage by **reserving** an estimated token count *before* forwarding to
OpenRouter (`reserveQuota`, `proxy/main.go`) and **truing it up** *after* the response completes
(`finalizeUsage` → `correctUsage`). On the streaming path, `streamSSE` only learns the real token
count from the **terminal usage SSE chunk** (`extractUsage`), which OpenRouter emits *after* the
answer content. `streamSSE`'s relay loop returns early when a write to the client fails:

```go
defer func() { p.finalizeUsage(keyID, reserved, totalTokens) }()
for scanner.Scan() {
    line := scanner.Text()
    if _, err := io.WriteString(w, line+"\n"); err != nil { return } // client gone: totalTokens==0
    ...
    if tokens, ok := extractUsage(line); ok { totalTokens = tokens }
}
```

The old `finalizeUsage` treated `actual == 0` as "nothing was used → **full refund**"
(`delta = -reserved`). So a client that **reads the full answer and disconnects just before the
trailing usage chunk** hits the early `return` with `totalTokens == 0` → **full refund of a
completion OpenRouter already generated and billed**. Repeatable and deliberately exploitable →
free, unmetered inference on the paid tier. (Distinct from the *no-double-refund* property, which
holds.) Root-cause read confirms `QUOTA_RESERVATION_DESIGN.md` §5(c) itself wrongly folded
"SSE stream cut short (client disconnect) before the final usage chunk" into the full-refund bucket
— that conflation is the bug.

### The fix (`a703978`, `proxy` module, isolated)
`finalizeUsage` now decides **three** ways instead of two (`finalizeUsage(keyID, reserved, actual int, producedOutput bool)`):
- `actual > 0` → true up to the real figure (`delta = actual - reserved`) — unchanged.
- `actual == 0 && producedOutput` → **KEEP the reservation** (`delta 0` — `reserveQuota` already
  added `reserved` to `tokens_used`; a distinct greppable log line fires) rather than refund it. This
  is the exploit path: output was produced and billed upstream, so a refund would be free inference.
- `actual == 0 && !producedOutput` → full refund — unchanged (genuine no-output).

`streamSSE` tracks `producedOutput` via a new `sawData` flag set from `isDataChunk(line)` — a new
**envelope-only** peek (a non-empty `data:` chunk that isn't `[DONE]`) that mirrors
`extractProvider`/`extractUsage`'s one-field discipline, so **message content is still never parsed —
the zero-data-retention posture is preserved.** `sawData` is set *before* the relay write, so a
client that drops mid-stream is still known to have consumed upstream-billed output. The two
upstream-error call sites (build-request error, `client.Do` error) pass `producedOutput=false` (their
full refund stays correct); the non-SSE branch derives it from `2xx && len(body)>0`. **No schema,
RPC, or reservation-sizing change** — only the refund *decision* changes.

### Verification
- `gofmt -l` clean; `go build ./...` + `go vet ./...` clean; `go test -race -count=1 ./...` green for
  the `proxy` module.
- New tests (`proxy/main_test.go`), all fail-when-neutered:
  `TestHandleChatCompletions_ClientAbortsBeforeUsageChunk_DoesNotRefund` (the regression — an
  `abortingResponseWriter` fails the write carrying the usage chunk; asserts **no** correction fires
  and `tokens_used` stays at the reservation), `TestFinalizeUsage_ProducedNoOutput_FullRefund`, and
  `TestFinalizeUsage_TrueUpUnchanged`.
- **Fail-when-neutered proven:** reverting `finalizeUsage` to the old two-way logic makes the abort
  test fail with the exact symptom (`correction count = 1, deltas=[-4096]`); restored → green.
- Live re-run against the deployed proxy is **not** done here (no OpenRouter/proxy key in this
  environment) and needs no schema change if attempted later — reproduce the exploit (stream, kill the
  client after the answer but before `[DONE]`), confirm the "keeping reservation" log line fires and
  `usage.tokens_used` does not drop back (`QUOTA_RESERVATION_DESIGN.md` §9's backgrounded-curl method).

### Residual / named follow-up (NOT hidden, NOT built here)
A **long** completion aborted **late** is charged only the reservation floor (default 4096), not its
true (possibly higher) usage — so it under-meters, but in the **safe direction and never to zero**
(the same imprecision `QUOTA_RESERVATION_DESIGN.md` §5(e) already accepts for crash-lost
corrections). **Exact metering of an aborted stream is a follow-up, deliberately not in this pass:**
(a) on client-abort, keep draining the upstream stream to capture the real usage chunk before closing
— requires decoupling the upstream request context from `r.Context()` (today a client disconnect
cancels the upstream read too) and accepts that draining forces OpenRouter to finish generating; or
(b) a periodic reconciliation sweep comparing summed real usage against `tokens_used`. Logged so it
isn't rediscovered; a fix is not implied to be imminent.

**Nothing marked closed — founder confirms.**

## 2026-07-24 — M4: apply/undo serialized ACROSS processes (completes Gate 6; verified, NOT closed)

**Gate 6's fix was only half a fix, and the missing half was load-bearing.** Investigated and fixed
as one isolated commit (`2a389c7`) spanning `editapply` + `daemon`. **Nothing pushed, nothing marked
closed.** This is a correctness/data-integrity fix, not a security one (see severity below).

### The gap (confirmed live, not inherited)
FAIL-3 Gate 6 (`d96794e`) closed five reproduced data-integrity races on the Apply/Undo path —
Apply/Apply read-modify-write lost update (**100%**, 60/60), backup-session collapse (~68%),
undo-guard defeat (~98%), double restore (~88%), and prune-vs-undo — using the daemon's
per-workspace `sync.Mutex` (`Server.applyLocks`). That lock is **in-process only**, and
`server_workspace_lock.go`'s own comment justified it with: *"there is exactly one daemon process
behind the socket, so an in-memory lock fully covers the concurrency without a cross-process file
lock."*

**That premise was false.** There are **three** independent writers of the same workspace files, and
only one is the daemon:

| Writer | Path | Serialized by `applyLocks`? |
|---|---|---|
| daemon socket handlers | `handleApplyEdit` / `handleUndo` (`daemon/server.go:495`/`:613`) | **yes** |
| CLI `edits apply` / `edits undo` | `daemon/apply_cmd.go:145` / `:203` — dispatched at `daemon/main.go:66-70` as a one-shot subcommand that never starts `serve`, i.e. **a separate OS process** | **no** |
| Mochiii TUI review flow | `clients/tui/chat.go:612`/`:622`, `editapply.Apply` in the TUI's own process | **no** |

A repo-wide search for `flock`/`Flock`/`O_EXCL`/`LOCK_EX` found **zero** cross-process locking on the
apply or undo write path. So every one of Gate 6's five races reopened the moment a CLI or TUI run
overlapped a daemon one — which is **normal, legitimate use** (a terminal apply while the IDE is
open), and is exactly the "two entirely legitimate clients (CLI + IDE)" scenario the Gate-6 audit
itself named as the reason those races mattered. The audit fixed it for two *socket* requests only.

### The fix (`2a389c7`)
New `editapply.LockWorkspaceApply(realRoot)` — an exclusive **`flock(2)`** on
`<realRoot>/.codeterminal/apply.lock`, returning a release func.
- **Why `flock`, not an `O_EXCL` lockfile:** flock is released by the kernel when the fd closes,
  *including on process death*, so a killed CLI cannot wedge the workspace forever. An `O_EXCL`
  lockfile would need a lock-breaking heuristic and would strand on `SIGKILL`. The lock file is
  created once and **never deleted** on release (unlink-on-release lets a second process lock an
  already-unlinked inode and both "hold" it).
- **Where the lock lives:** `.codeterminal/` is already pruned by the indexer and refused by the edit
  writer (`ProtectedDirNames`) and covered by the RAG `.gitignore` entry, so the lock file is never
  indexed, never sent to a model, and not writable by an edit block. Opened `O_NOFOLLOW`, 0600.
- **Taken INSIDE the three mutation primitives, not at call sites** — deliberately, because the bug
  being fixed *is* a caller that forgot to lock. Every writer gets it whether or not it remembers:
  `editapply.Apply` (`VerifyUnchanged` → backups → write), `editapply.NewBackupSessionDir` (the
  stat/mkdir mint plus its prune), `runUndoSession` (the guard check through the commit).
- **Why those spans need not be locked *together*:** `Apply` independently re-checks staleness
  byte-for-byte (`VerifyUnchanged`), so a writer that lands between two primitives is *refused as
  stale*, never allowed to clobber. Each span being atomic w.r.t. the others is all five races need.
- **No span covers a human prompt**, so a CLI/TUI user sitting on a `[y/N]` never blocks the daemon.
  The one exception is stated: the CLI undo's rare "overwrite changed files?" prompt sits inside
  `runUndoSession`'s guarded branch, deliberately, because the guard decision it confirms would
  otherwise be re-raced before the commit. The daemon's undo never prompts.
- **The daemon's `sync.Mutex` is kept** as the in-process fast path (it serializes the daemon's own
  concurrent socket requests before they ever contend on the file lock, and spans the whole handler).
  **Lock ordering is always mutex → flock, never the reverse**, so the two cannot deadlock.
- `server_workspace_lock.go`'s false "exactly one daemon process" comment is **corrected in place**
  rather than left to mislead the next reader.

### Verification
- **Cross-process exclusion proven with two REAL OS processes** (not just goroutines, and not
  inferred from flock's documented semantics): a holder acquired at 0 ms and held 1500 ms; a second
  *process* launched 300 ms later blocked for **1196 ms** and acquired precisely when the holder
  released.
- Regression tests `editapply/applylock_test.go`, all through real doors: concurrent same-file
  Apply **never loses an applied edit** (30 rounds; asserts the honest invariant — every `Apply` that
  returned nil must have its edit present in the final file), concurrent `NewBackupSessionDir` hands
  out **distinct** sessions, the primitive genuinely **excludes** a second holder, and **different
  workspaces never block each other** (the per-root keying).
- **Fail-when-neutered proven:** with the `flock` call removed, the suite reproduces the exact
  original defect — `Apply #1 reported SUCCESS but its edit "YYY" is not in the file
  (content="XXX\nBBB\n")` — plus the session collapse and the no-exclusion failure. Restored → green.
- Six modules `gofmt`/`go build`/`go vet` clean; all four suites with tests green and **`-race`
  clean**, including the existing `gate6_serialization_test.go` socket stress test (evidence the
  added flock introduces no deadlock beneath the daemon's existing mutex).

### Scope note / severity, stated precisely
As a **security** finding this is Low and largely subsumed (it needs a same-uid local process, which
can already write the user's files directly). As a **correctness / data-integrity** finding it is the
sharp one — silent user data loss and an undo whose report disagrees with the disk, reproducing with
two entirely legitimate clients. Same framing the Gate-6 audit applied to the in-process case:
**both, predominantly correctness.** The bug-hunt report filed it as a *medium* (M4); it is recorded
here at that label, with the note that it is the same race class Gate 6 treated as serious enough to
warrant repro-driven fixing.

### Not addressed here (unchanged, still open)
The two remaining lower-severity items from the same report — `HelperProcess.Stop`'s nil-`doneCh`
hang (reachable only after a failed helper *startup*, so shutdown-path only) and created `.go` files
receiving only an advisory syntax note where the edit path hard-refuses unparseable Go
(`editapply/create.go` vs `apply.go:96-99`) — are untouched by design. **Nothing marked closed —
founder confirms.**

## 2026-07-24 — Endpoint / API security pass (5 vuln classes; audited live, 4 fixes, NOT closed)

**Role: endpoint tester.** First dedicated endpoint-security review of the **managed proxy**, which
is the project's **only network-exposed surface** and fronts a live OpenRouter key. Prior reviews
covered the local unix socket (P3 FAIL-3) and the Supabase posture (closed 2026-07-17); the HTTP API
had never had its own pass. Audit-first: every finding was **reproduced against the real compiled
proxy binary** on `127.0.0.1` before any fix, driven by a raw HTTP client sharing no code with it.
**Nothing pushed, nothing marked closed.**

### Method (local only, by choice)
Real `./proxy` binary, stub Supabase + stub OpenRouter upstream, raw-socket/urllib attacker
(harness kept in scratchpad, not committed — it demonstrates attacks). **The deployed Railway
instance was deliberately NOT probed:** floods and slowloris can then be run at full strength with
no production impact and no real credits spent. Dependency scanning used **govulncheck**
(reachability-aware) plus `npm audit`, not version eyeballing.

### Attack matrix — 14 checks. Before: 5 FAIL + 1 PARTIAL. After: 0 FAIL, 1 PARTIAL.

| # | Class | Check | Before | After |
|---|---|---|---|---|
| A1 | 1 BOLA | body-supplied `p_key_id`/`key_id`/`user_id` steering billing | PASS | PASS |
| A2 | 1 | PostgREST operator injection via bearer | PASS* | PASS |
| A3 | 1 | model / cost authorization | **FAIL** | **PASS** |
| A4 | 2 auth | 8 unauthenticated variants, fail-closed | PASS | PASS |
| A5 | 2 | both route aliases + method/path variants | PASS* | PASS |
| A6 | 2 | key via query param / `X-Api-Key` | PASS | PASS |
| H1 | 5 | `/health` unauthenticated exposure | PARTIAL | **INFO** |
| A10 | 5 | account metadata on the STREAMING path | **FAIL** | **PASS** |
| A11 | 5 | error-path / header exposure | PASS | PASS |
| A9 | 4 limits | oversized body (4 MiB cap) | PASS | PASS |
| A9b | 4 | unauthenticated `/health` flood | **FAIL** | **PASS** |
| A7 | 4 | per-key concurrency / rate limiting | **FAIL** | **PASS** |
| A8 | 4 | slowloris (dribbled headers) | PASS | PASS |
| A8b | 4 | slow-response-read pinning (M10) | **FAIL** | **PARTIAL** |

**\* Correction, recorded rather than quietly dropped.** A2 and A5 first reported FAIL; both were
**harness bugs, not vulnerabilities**, and were re-tested before being reclassified. A2's only
non-401 was status `0` — the *Python client* refused to transmit a CRLF-bearing header, so the proxy
was never reached; re-driven over a raw socket the proxy answers **400** and upstream is contacted
0×. A5's "failures" were `405` (method gate) and `301` (Go `ServeMux` path normalisation, whose
redirect target still requires auth); re-run with redirects disabled, **no variant returns 200 and
upstream is reached 0×**. A9b was also initially miscoded (a hardcoded verdict that never inspected
status codes). Reporting these as findings would have been rounding up.

### What was already sound (no change needed)
- **BOLA is structurally impossible on the key/quota object.** `apiKeyID` is always derived
  server-side in `authorize` (sha256 of the bearer → Supabase returns `id`) and never taken from
  client input; every quota call consumes that value. The PostgREST filter is a 64-char hex digest,
  so no operator can survive. Injected body ids are ignored for billing.
- **Auth fails closed** on all 8 variants, the client's `Authorization` is never forwarded upstream
  (replaced with the server key), no client headers are copied, and only the first 8 chars of a key
  are ever logged. Alternate credential channels (query param, `X-Api-Key`) do not authenticate.
- **Error/header hygiene**: generic error bodies, response headers limited to `Content-Type`
  (+`Retry-After`); no upstream URL, host path, stack trace or account id.

### Fixes (four isolated commits)

**SEC-3 — `golang.org/x/text` GO-2026-5970, reachable (`b0a1085`).** govulncheck reported an
**infinite-loop-on-invalid-input** DoS affecting **4 of 6 modules**, with a call trace landing in
`editapply.PrepareEdit → norm.Form.NextBoundaryInString`. That path runs the match ladder's Unicode
normalization over **untrusted model output** and workspace content, so malformed Unicode in an edit
block could hang the apply path across CLI, TUI and daemon — a reachable defect, not an import-only
finding. Bumped v0.26.0/v0.25.0/**v0.3.8** → **v0.39.0**; also retires the stale v0.3.8 pin in
`clients/tui`. **All six modules now scan clean.**

**SEC-5 — account metadata leaked on the SSE path (`3e37c44`, closes M9).** `stripAccountMetadata`
was wired **only to the non-streaming branch**, while the daemon **always sets `stream:true`** — so
the scrubber never ran on the only path real traffic uses. Proven live: a chunk carrying
`user_id` reached the client byte-for-byte. New `stripSSEAccountMetadata` decodes into
`map[string]json.RawMessage` (so message content stays an opaque byte slice — **never inspected,
never logged**; only top-level key names are compared) and **re-marshals only when a field was
actually removed**, so an ordinary chunk is forwarded byte-for-byte and the stream is never buffered.

**SEC-4 — no rate limiting at all (`a88b88a`).** Measured: **40 simultaneous requests on one key →
40 concurrent upstream calls, 0 throttled, 40 Supabase auth round trips**; 200 unauthenticated
`/health` requests served in 0.1s. The local-only unix socket had *stricter* limits than the
internet-facing proxy. New `proxy/ratelimit.go`, **standard library only** (proxy/go.mod keeps its
zero third-party requires — a real supply-chain property for the one internet-facing component):
token bucket (bounds requests over *time*) **plus** an in-flight semaphore (bounds them *at once* —
a rate limit alone lets long streams accumulate). Applied **pre-auth, before any Supabase call**, so
bad-key floods cannot amplify into a third party — per-source (`X-Forwarded-For`, **spoofable**)
with a global bucket as the backstop spoofing cannot evade — and **post-auth per `api_keys.id`**,
which is un-spoofable. Buckets are swept on an idle TTL so the limiter cannot itself become a
memory-exhaustion vector. After: the same flood peaks at **8** concurrent upstream calls with 32
throttled; the `/health` flood serves 18 and throttles 182.
**Honest limitation, in the code not buried: in-memory ⇒ PER-INSTANCE. N replicas allow N× these
values.** Exact cross-replica limiting needs shared state (a Postgres RPC or Redis) at the cost of a
round trip against an already ~1.45 s TTFT. Per-instance still converts *unbounded* into *bounded*.

**SEC-1 + SEC-5 — model allow-list and `/health` fingerprinting (`091ce01`).** Quota is metered in
**tokens** but billed in **dollars**, and the body was forwarded with no restriction on `model`, so
a key could select an arbitrarily expensive model within the same token budget (proven live:
relayed verbatim). Now `peekModel` (same one-field discipline as `peekMaxTokens`) + an allow-list
checked **before** the reservation and before forwarding → `403`, nothing reserved, upstream never
contacted. Defaults to the managed tier's shipped models.json set — the product's own decided
posture, not a new policy — overridable via `ALLOWED_MODELS`; an empty set means unrestricted and is
logged at startup so it is never a silent default. Separately, `/health` (the only unauthenticated
route) no longer returns the build commit SHA; `HEALTH_EXPOSE_COMMIT=1` restores it for the
deploy-verify case it existed for.

### Compatibility check — SEC-1's 403 gate vs. the quota-reservation flow
Checked because two independently-correct changes are exactly what Part-0 rule 3 exists to catch:
could a reservation ever be *sized or created* for a model that is rejected a moment later? **Read
live from `proxy/main.go`, not assumed. Confirmed moot — the 403 gate precedes all reservation logic
in both the current and the designed flow.** The shipped order in `handleChatCompletions` is: rate
limit + in-flight admission → `io.ReadAll` over the `MaxBytesReader` → **`peekModel` +
`modelAllowed` → `403`, `return` (`:447`)** → `peekMaxTokens` clamp (`:456`) → `reserveQuota`
(`:463`). The refusal returns before `reserved` is even computed, so no reservation is sized, none is
written, and no refund path is entered. `QUOTA_RESERVATION_DESIGN.md` §4's proposed sequence puts the
body read at step 2, `peekMaxTokens` at step 3 and `reserveQuota` at step 4 — the allow-list gate
slots between steps 2 and 3, so the as-designed order runs it first as well.
- **One real (documentation, not security) gap found while checking, for the founder queue:**
  `QUOTA_RESERVATION_DESIGN.md`'s header still reads *"Status: design only. No code changes yet"*,
  but that design **has landed** — `proxy/migrations/0001_reserve_usage.sql`, `reserveQuota`
  (`:647`), `peekMaxTokens` and `finalizeUsage` are all on `main`. The prompt for this batch was
  itself written from that stale line. A cold reader would conclude the TOCTOU race is still open
  when it is closed. **Not fixed here** — this batch is proxy endpoint security, and a stale status
  line in a design doc is an isolated edit belonging to whoever closes that item, per Part-0 rule 6.

### Still open / explicitly not fixed
- **A8b / M10 — slow-response-read pinning: MITIGATED, NOT ELIMINATED.** A single trickle reader can
  still hold a streaming connection (the only response-side bound remains `WriteTimeout` = 6 min).
  The new in-flight caps bound *how many* can be pinned at once (8/key, 128 total), turning an
  unbounded exhaustion vector into a bounded one. A per-connection output-rate bound remains open.
- **F1 — the proxy does not itself enforce ZDR** (it forwards byte-for-byte; ZDR is set client-side).
  Founder-gated, part of the open P3/3A gate. **Deliberately not a drive-by fix.**
  - **2026-07-25 — customer-facing ZDR claims stripped/qualified while F1 is open.** Per founder
    decision to drop ZDR from launch scope until F1 is genuinely closed, the settled-guarantee
    language ("enforced on the wire", "nothing retained", proxy "enforces zero data retention")
    was rewritten to the honest **requested-not-enforced** framing across **`PRODUCT_OVERVIEW.md`**
    (TL;DR, Quick-facts table, §2 comparison table, §3 mindmap + pillar 3, §4 intro + big-picture
    diagram, §5 sequence diagram, §7 privacy diagram + point 3 anchor caveat, §8 feature catalogue).
    **`models.json` was NOT changed** — `zdr.allow_non_zdr:false` / `allow_data_collection:false` /
    `allow_fallbacks:true` are load-bearing: `daemon/config.go`'s `resolvedProviderRouting` reads
    them into the per-request provider-routing object (consumed by `server.go`, surfaced by
    `degraded.go`). Those keys express **requested intent** (the flags the daemon sends), not
    proxy-enforced behavior; JSON permits no comment, so this note is the annotation. **When F1
    lands, restore the enforced-guarantee wording in the `PRODUCT_OVERVIEW.md` locations above.**
- **npm: 2 dev-only advisories** (`mocha` → `serialize-javascript`). **Runtime deps: 0
  vulnerabilities** — the extension ships no runtime dependencies, so nothing reaches users. The
  offered remedy is a breaking mocha downgrade; **not taken**, recorded instead.
- Deployed-Railway probing (excluded by choice), the daemon socket (already audited, local-only),
  Supabase RLS (closed 2026-07-17).

### Regression
`gofmt -l` clean; `go build ./...` + `go vet ./...` clean across all six modules; `go test -race
-count=1` green for daemon / editapply / clients/tui / proxy; **govulncheck clean on all six**.
Full matrix re-run against the fixed binary: **0 FAIL / 14, 1 PARTIAL**. Commits `b0a1085`,
`3e37c44`, `a88b88a`, `091ce01`. **Nothing marked closed — founder confirms.**

## 2026-07-24 — §5(e): durable pending-corrections outbox + reconciliation sweep (verified, NOT closed)

`QUOTA_RESERVATION_DESIGN.md` §5(e) was the one reservation-loss case that design **named and
explicitly declined to close**, recommending as follow-up "a durable outbox for pending corrections,
or a periodic reconciliation sweep." This is that follow-up. One isolated commit in the `proxy`
module (`ca7e3c4`). **Nothing pushed, nothing marked closed — founder confirms, per Part 0 rule 5.**

### What §5(e) was
The proxy reserves an estimated token count before forwarding (`reserveQuota`) and corrects it after
the response completes (`finalizeUsage` → `correctUsage`). If the **process dies between those two**
— redeploy, OOM, crash mid-stream — `tokens_used` stays at the reserved value and **nothing anywhere
records that a correction was owed**. §5(d)'s bounded retry cannot help by construction: the
goroutine that would retry died with the process. §5's own direction analysis is what makes this
matter rather than being bookkeeping noise — a lost *refund* leaves `tokens_used` too high (safe,
throttles early), but a lost *top-up* leaves it too low (**unsafe** — later requests get admitted
against quota that was really spent), and §3's real provider data shows top-ups are the normal case
for the active model, not a tail.

### What was built (`ca7e3c4`)
**`proxy/migrations/0002_pending_corrections.sql`** — new `pending_corrections` table (`id`,
`key_id`, `reserved`, `created_at` + a `created_at` index); `reserve_usage` **dropped and recreated**
(its return shape gains `pending_id`, which `CREATE OR REPLACE` cannot do) to open the outbox row via
a **data-modifying CTE inside the same statement** that reserves; new `apply_correction(p_key_id,
p_tokens, p_pending_id)` that increments **and** closes the row in one statement; new
`sweep_pending_corrections(p_stale_minutes)` as an atomic `DELETE ... RETURNING`, so a row is claimed
exactly once even across replicas. The reservation UPDATE itself is byte-identical to 0001 — the
TOCTOU closure is preserved, not re-litigated. `increment_usage` is left in place, unchanged, just no
longer called.

**Single-statement atomicity is the whole point, in both directions.** A separate `INSERT` after the
reservation would reintroduce the crash-in-the-gap hole *inside the outbox*; a separate `DELETE`
after the increment would either double-charge on the next sweep or drop a correction. **Zero added
round trips** on the request path — the outbox rides the two calls that already existed.

**`proxy/main.go`** — `reserveQuota` returns `(pendingID int64, ok bool)`; that id threads through
`handleChatCompletions` → `streamSSE` / the non-SSE branch → `finalizeUsage` → `correctUsage`, which
now posts to `rpc/apply_correction` carrying `p_pending_id` instead of `rpc/increment_usage`. New
`startReconciliationSweep` (5 min ticker, wired as `go p.startReconciliationSweep()` in `main()`
before `ListenAndServe`, so a just-restarted process sweeps what its own crash stranded) and
`sweepPendingCorrections`, claiming anything past 15 minutes.

**A bug this patch introduces and closes in the same pass, called out rather than buried:**
`finalizeUsage`'s `producedOutput` branch (C3's abort path) previously logged and **returned early**
— correct when a zero-delta correction was genuinely nothing to do. With an outbox that early return
leaves a **live row on a perfectly healthy request**, and the sweep would report the single most
common client-disconnect path as an abandoned reservation ~20 minutes later, turning the crash alarm
into noise. That branch now calls `correctUsage(keyID, 0, pendingID)`.

### What it does NOT close — read this before summarizing it as "case (e) is solved"
It does **not** recover the true usage of a request whose process died. That number only ever existed
in the response stream the dead process was reading; it is not recoverable from anywhere, and the
sweep does not invent it. What changed is the *shape* of the damage: **silent, indefinite, and
unsafe-direction → loud within ~20 minutes and resolved in the safe direction** (the reservation
stays **charged**, never refunded on missing data — the same asymmetry `finalizeUsage` already
applies). The swept reservation still over-meters that key by up to `reserved` (default 4096), and
the log line — not the accounting — is the deliverable: an operator sees which key, how much, when.

It is also **not** exact metering of an aborted stream (the C3 residual). §3F of the HANDOFF listed
"a reconciliation sweep" as one of two possible routes to that; this sweep is **not** that route — it
reconciles *abandoned reservations*, not *summed real usage vs. `tokens_used`*. **C3's residual stays
open**; the two are related but distinct and must not be conflated.

### Step-0 verification — what was actually run
1. **Regression (`proxy` module): all PASS.** `gofmt -l` clean; `go build ./...`, `go vet ./...`
   clean; `go test -count=1 ./...` and `go test -race -count=1 ./...` green; `govulncheck ./...` →
   "No vulnerabilities found."
2. **New unit tests (`proxy/main_test.go`), against `httptest` PostgREST fakes, no live Supabase.**
   The `fakeUsageStore` now models the outbox for real (open/close/sweep) rather than stubbing it —
   a stub that always closed would test nothing. Coverage: `reserveQuota` returns a nonzero
   `pendingID` on success and `(0,false)` opening no row on refusal, **asserting exactly one HTTP
   call** in both cases; `correctUsage` posts to `.../rpc/apply_correction` with `p_pending_id`
   present and correct; the `producedOutput` branch closes its row with delta 0; the sweep emits one
   `ABANDONED RESERVATION swept` line per claimed row with `pending_id`/`key_id`/`reserved`/
   `reserved_at`, leaves a *fresh* row alone, never changes `tokens_used`, and is **silent on zero
   rows**; and a crash-then-restart sequence where the correction never runs.
3. **Fail-when-neutered proven three ways** (each reverted after): pointing `correctUsage` back at
   `increment_usage` → 6 tests fail; dropping `p_pending_id` from the payload → 4 fail; restoring the
   `producedOutput` early return → the abort regression fails. Restored → green.
4. **Live crash test against the REAL compiled binary — ALL PHASES PASS.** Not the live-Supabase test
   §5(e)'s follow-up envisioned (see blockers below), but a real `proxybin` process against a
   *separate-process* fake PostgREST that outlives it, with the **real shipped 5-minute ticker**:
   - *Phase 1 (normal request):* reserved 4096 → `apply_correction delta=-3973 pending_id=1` →
     `tokens_used=123`, **open rows 0**. The healthy path leaves nothing behind.
   - *Phase 2 (`kill -9` mid-stream):* `tokens_used=4219`, **open rows 1** — the reservation record
     survived the SIGKILL, which is the entire claim.
   - *Phase 3 (restart + sweep):* proxy restarted 17:02:43, swept 17:07:43 (exactly one real 5-minute
     tick), logging
     `reconciliation: ABANDONED RESERVATION swept (pending_id=2, key_id=eeee5555-…-00000000cafe,
     reserved=4096, reserved_at=2026-07-24T16:42:39…) -- no correction was ever applied … the
     reservation stays CHARGED …` then `swept 1 abandoned reservation(s)`. Open rows **0**;
     `tokens_used` **4219 → 4219, unchanged** from its post-reservation value. Not refunded.
   - Harness note: the proxy correctly does **not** forward arbitrary client headers, so the
     slow-upstream trigger had to ride the request **body** (which *is* forwarded byte-for-byte).
     The first run failed for this reason and is reported as a fail, not smoothed over.

### Step-0 steps that could NOT be run — blocked, not skipped
- **The migration is UNAPPLIED and the SQL is UNEXECUTED.** Step 0 called for applying 0002 via the
  Supabase SQL editor and re-verifying via PostgREST OpenAPI introspection (the §1 technique). **The
  Supabase project host no longer resolves — `eshpqodurxubjigndiev.supabase.co` returns NXDOMAIN**
  (general outbound network is fine; `supabase.co` itself resolves). So the schema could not be
  applied *or* introspected, and **0001's live state could not be re-confirmed either**. There is
  additionally no `psql`, no `postgres`, and no `docker`/`podman` in this environment, so the SQL
  could not be executed against a local engine as a substitute. **The `.sql` file has never been run
  by anything.** Checked by inspection only: OUT-parameter/column ambiguity is avoided by qualifying
  every column reference (the same discipline 0001 used), data-modifying CTEs are read via their
  `RETURNING` output (the supported pattern), and `WITH … DELETE` is valid as a top-level statement.
  **Treat first application as unproven — apply it somewhere disposable first.**
- **Consequently the live re-test (real proxy → real Supabase) did not happen.** Phase 1–3 above is
  the strongest available substitute: real binary, real signal, real ticker, fake database.

### Still open after this pass
- **DEPLOY ORDER IS A HARD CONSTRAINT.** Migration 0002 must land **before** this build ships.
  `apply_correction` does not exist on an un-migrated database → 404 → **4xx is non-retryable in
  `correctUsage`** → every correction on every request is lost outright, which is strictly worse than
  the bug being fixed. `reserveQuota` logs a loud `WARNING … migration 0002 likely not applied` when
  `reserve_usage` returns no `pending_id` (exactly that state), but **deliberately still admits the
  request** — the reservation is real and atomic either way, and failing closed would turn a missing
  migration into a total outage. A compatibility fallback (call `increment_usage` when `pendingID==0`)
  was considered and **not** built: it was outside the confirmed scope and would silently mask the
  misconfiguration the warning exists to surface. Noted as an available option, not a recommendation.
- **The sweep is not a metering reconciliation.** It claims abandoned reservations; it does not
  compare summed real usage against `tokens_used`. C3's exact-metering residual is untouched.
- **A sustained Supabase outage longer than the sweep's own reach still loses corrections** — the
  outbox row survives, so it is *reported*, but nothing re-drives the correction. Re-driving would
  need the true usage figure, which is exactly what does not survive.
- **`QUOTA_RESERVATION_DESIGN.md` is now stale in a second place.** Beyond the already-tracked
  "Status: design only. No code changes yet" line, §5(e) still reads "**Recommending this as a named
  near-term follow-up**, not doing it in this pass." Folded into the existing HANDOFF §3F doc-staleness
  item rather than edited here, because that entry already assigns the design doc's status text to a
  separate isolated edit (Part-0 rule 6).

### Verification summary
`gofmt -l` clean; `go build ./...` + `go vet ./...` clean; `go test -count=1` and `go test -race
-count=1` green for the `proxy` module; `govulncheck` clean. Commit `ca7e3c4`. **Nothing marked
closed — founder confirms.**

## 2026-07-25 — Migration 0002 GENUINELY applied to live Supabase (corrects an earlier premature "applied" note) — AHEAD of the code that fully uses it (two loose ends: verified + contained)

> **Record correction (2026-07-25T09:23Z / 14:53 IST).** An earlier version of this section, written
> the same calendar day, stated migration `0002` "has now been applied." **That was false when
> written** — the SQL had been reviewed and read but never actually pasted into the Supabase SQL editor
> and run. A subsequent live check found `42P01: relation "pending_corrections" does not exist`, and a
> diff against a saved pre-migration snapshot (17 columns across `api_keys`, `api_keys_public`, `usage`)
> confirmed nothing had changed. The "prior session's live 200 post-migration" that the old note cited
> as corroboration did **not** exercise the new schema. This is the corrected, verified record.

Migration `0002_pending_corrections.sql` **has now genuinely been applied** to the live Supabase
project (`eshpqodurxubjigndiev`), confirmed by two independent live queries run directly in the
Supabase dashboard this session: the information-schema check now returns **21 columns** including
`pending_corrections(id bigint, key_id uuid, reserved integer, created_at timestamptz)`, and the
routine check shows all three of `apply_correction`, `reserve_usage`, `sweep_pending_corrections`
present as `FUNCTION`. This reverses the "UNAPPLIED / host NXDOMAIN" state the §5(e) section above
records.

But the **deployed proxy is still `f25f444`** (confirmed this session via `/health` →
`{"status":"ok","commit":"f25f4448..."}`), which predates the migration and calls `reserve_usage`
while having **zero** references to `apply_correction` / `sweep_pending_corrections`. The code that
calls those (`ca7e3c4`) is on `main` and on `origin/backup/pre-deploy-audit-2026-07-24` but **is not
deployed** — that deploy is founder-gated (§2J / Incident 2). Applying the schema ahead of that code
leaves two consequences, both handled here.

### Loose end 1 — field-tolerance of the deployed decode: **CONFIRMED SAFE (source analysis + live test)**
The source analysis below was written **in advance of confirmation** (it reads `f25f444` Go source,
which never depended on the migration being live) and is now **confirmed against the live schema** by a
real authenticated request in this session (see the live-test block at the end of this loose end).
`reserve_usage` now returns a third column (`pending_id`). Deployed `f25f444` decodes the RPC
response at `proxy/main.go` (in `f25f444`, lines 562–566) into:
```go
var rows []struct {
    TokensUsed int64 `json:"tokens_used"`
    TokenLimit int64 `json:"token_limit"`
}
json.NewDecoder(io.LimitReader(resp.Body, maxAuthResponseBytes)).Decode(&rows)
```
- **Decode method:** slice of structs with named JSON tags.
- **`DisallowUnknownFields`:** grepped the entire `f25f444` proxy tree — **0 occurrences**. Plain
  `encoding/json`, which silently ignores unknown object fields.
- The extra `pending_id` is an added *field* on each row object, not an added array *element*, so the
  `len(rows) != 1` guard is unaffected (still one row).
- **Conclusion:** the deployed proxy tolerates the new `pending_id` field.

**Live confirmation (this session, 2026-07-25T09:23Z).** A real authenticated `POST
/v1/chat/completions` to `codeterminal-core-production.up.railway.app` (model
`deepseek/deepseek-v4-flash`, `max_tokens:1`) returned **HTTP 200** with a genuine completion
(`"content":"Hello"`, `usage.total_tokens":6`, `cost:9.8e-7`). Because that request path runs
`reserveQuota` → `reserve_usage` (`f25f444` `proxy/main.go:339`), the 200 is direct evidence that the
deployed proxy decodes `reserve_usage`'s new 3-column return shape without error against the now-live
schema. This is genuine, current, live-tested confirmation — not inference from source reading alone.

### Loose end 2 — `pending_corrections` growth: **monotonic, worth monitoring (storage impact negligible)**
`reserveQuota` runs once per admitted `/v1/chat/completions` request (`f25f444` `proxy/main.go:339`),
and `reserve_usage` opens exactly one outbox row per *successful* reservation (refusals write nothing —
the migration's CTE inserts from the empty `reserved` CTE; mirrored by `proxy/main_test.go:951`).
Crucially, deployed `f25f444` **never closes any row**: its correction path (`correctUsage`) calls
`increment_usage`, *not* `apply_correction`, and nothing invokes `sweep_pending_corrections`. So until
`ca7e3c4` deploys, the table grows **monotonically = cumulative admitted requests since the migration
actually landed (2026-07-25T09:23Z — the real apply timestamp, not the earlier premature note)**; no
row is ever removed.

**Growth-clock baseline — capture, don't assume.** The clock starts at the real apply time above, not
earlier. `reserve_usage` only began writing `pending_corrections` rows once the migration replaced the
function this session, so the baseline is **not** assumed-zero — it should be captured from the live
count the founder reports. Run the MONITORING query below and record `pending_rows` / `oldest` /
`newest` here when known; `oldest` should be ≥ the apply timestamp.
- **Rate:** ~1 row per admitted request. At the observed volume (dozens to low-hundreds of req/hour,
  bursty not sustained), that is roughly **a few hundred to low-thousands of rows/day** — more than
  a "few hundred/week," so not a rounding error on count.
- **Storage:** each row is 4 small columns (~50–100 B + index). Even at 2k rows/day for a month
  (~60k rows ≈ single-digit MB) this is negligible for Postgres. The concern is **unbounded growth
  over an indefinite, founder-gated timeline**, not disk.
- Because the deploy timeline is unknown/founder-gated, this lands in the "worth monitoring" bucket:
  a read-only monitoring query is provided (below) and a **conservative, optional** manual cleanup
  query. Neither is wired into any automation. Cleanup is deliberately more conservative (24h window)
  than the real sweep (15 min per the migration's own `p_stale_minutes`), and is only for very old
  rows whose requests have long since completed — run by hand, if ever.

```sql
-- MONITORING (safe, read-only, run anytime in the Supabase SQL editor):
select count(*) as pending_rows,
       min(created_at) as oldest,
       max(created_at) as newest
from pending_corrections;
```

```sql
-- OPTIONAL MANUAL CLEANUP — only if the count above is large AND you accept that
-- every one of these reservations is long resolved. Deletes rows older than 24h
-- (far outside any live request's lifetime). Do NOT run against recent rows;
-- once ca7e3c4 deploys, sweep_pending_corrections handles this with a 15-min window.
delete from pending_corrections
where created_at < now() - interval '24 hours';
```

### What happened this session vs. what remains untouched
The migration **was** executed — by the founder, directly in the Supabase SQL editor — and verified by
two independent live queries there (this repo still has no wired DB connection, so no SQL ran *from
here*; the monitoring/cleanup queries above remain for the founder to run manually). From this repo:
source analysis, the corrected doc note, one live authenticated proxy request (read of live behaviour,
non-mutating beyond a 1-token billed completion), and one `/health` read. **No `git push`, no merge, no
Railway action.** The `f25f444`-vs-`ca7e3c4` deploy decision remains founder-gated and untouched.

## 2026-07-27 — RESOLVED: `pending_corrections` grant/EXECUTE surface — flagged, verified live, corrective `0003` applied (CLOSED 2026-07-27)

**CLOSURE (2026-07-27).** `proxy/migrations/0003_revoke_public_grants.sql` applied to
live Supabase production and verified before/after in the SQL editor. Actual finding was
**narrower than this entry's worst case**:

- **Gap #1 (table grants) was never open.** `role_table_grants` for
  `anon`/`authenticated`/`PUBLIC` on `pending_corrections` returned **zero rows both before
  and after** — Supabase's bootstrap auto-grant (trap 3) did *not* fire for this table. The
  SELECT-leak / DELETE-wipe-the-sweep / INSERT-flood integrity concerns below were latent
  possibilities, not a real exposure.
- **Gaps #2 and #3 (function EXECUTE) were real and are now closed.** `has_function_privilege('public', …, 'EXECUTE')`
  before: `reserve_usage`=**true**, `apply_correction`=**true**, `sweep_pending_corrections`=**true**.
  After the two `revoke execute … from public` statements: all three = **false**.
- **How contained it actually was while open:** all three functions are **`SECURITY INVOKER`**
  (0002 declares no SECURITY clause → Postgres default; verified `grep` — no `SECURITY DEFINER`
  anywhere in `proxy/migrations/`). So an `anon` caller ran *as* `anon`, and the inner
  `UPDATE usage` still hit `usage`'s SELECT-only grants and failed. The open EXECUTE was a
  **defense-in-depth regression, not a live write hole** — matching the reasoning predicted below.

Corrective is tracked as `0003` (dashboard-applied, not auto-run — same discipline as 0001/0002).
The RLS belt-and-suspenders was left commented/optional; the grant/EXECUTE revokes were the
load-bearing fix and are sufficient. The original "no RLS needed" note remains superseded.

---

**Original flag (2026-07-27), retained for the record:** RE-REVIEW: `pending_corrections` "no RLS needed, service-role bypasses it anyway" — the mid-incident call does NOT hold under a calm read (FLAGGED, founder SQL required)

A mid-incident decision recorded that migration `0002`'s new `pending_corrections` table needed no
RLS "because the proxy is `service_role` and bypasses RLS anyway." **On a calm re-read that reasoning
is a non-sequitur** — right answer, wrong question. RLS constrains the `anon` / `authenticated` roles
(the PostgREST/frontend-reachable path), **not** `service_role`. That `service_role` holds BYPASSRLS
is true and irrelevant: it says nothing about whether `anon`/`authenticated` can reach the table. The
call never checked the thing that actually matters, which is the **grant** surface, not RLS-vs-no-RLS.

Two concrete gaps this leaves, both consistent with the 2026-07-17 posture's own **trap 3**
("Supabase's bootstrap auto-granted full `anon` CRUD on every new relation in `public` — it fired for
real … the catalog reads clean while the hole is open"):

1. **`pending_corrections` was created with ZERO grant/revoke statements** (`proxy/migrations/0002`
   contains no GRANT/REVOKE at all; the 2026-07-17 RLS/EXECUTE posture was applied *separately in the
   dashboard*, never in these files). So unless the 2026-07-17 `ALTER DEFAULT PRIVILEGES … REVOKE`
   genuinely covers tables created *later* by the SQL-editor role, `anon`/`authenticated` may hold
   CRUD on it. Impact if so: **SELECT** leaks every key's `key_id`+`reserved`+`created_at`
   (cross-tenant metadata, no secret material — Low); **DELETE** lets an attacker wipe outbox rows and
   thereby **defeat the crash-recovery sweep that is the entire purpose of migration 0002**; **INSERT**
   floods phantom stranded-reservation rows the sweep then reports as real. The DELETE/INSERT paths are
   an **integrity** concern, not just info-leak.

2. **`0002`'s `drop function if exists reserve_usage(uuid,integer)` + recreate silently drops the
   2026-07-17 `REVOKE EXECUTE FROM PUBLIC` on `reserve_usage`** (dropping a function drops its ACL; the
   recreated function reverts to Postgres's EXECUTE-to-PUBLIC baseline). `0002` does not re-revoke.
   **This is a defense-in-depth regression, not a live hole** — `reserve_usage` is `SECURITY INVOKER`,
   so an `anon` caller runs as `anon` and the inner `UPDATE usage` still hits `usage`'s SELECT-only
   grants and fails — but it undoes an established control while the catalog reads clean, exactly trap
   3's class. (`apply_correction` / `sweep_pending_corrections` are new `SECURITY INVOKER` functions
   whose bodies write `usage`/`pending_corrections`; same invoker reasoning applies, but they too carry
   the default EXECUTE-to-PUBLIC.)

**Cannot be resolved from this repo** (no wired DB connection — `QUOTA_RESERVATION_DESIGN.md` §7; this
is dashboard-applied schema). **Founder verification (run in the Supabase SQL editor):**
```sql
-- Does anon/authenticated have ANY privilege on the new table?
select grantee, privilege_type
from information_schema.role_table_grants
where table_name = 'pending_corrections';

-- Did the reserve_usage EXECUTE revoke survive the drop+recreate?
select r.rolname as grantee
from pg_proc p
  join pg_namespace n on n.oid = p.pronamespace
  cross join lateral aclexplode(p.proacl) a
  join pg_roles r on r.oid = a.grantee
where n.nspname='public' and p.proname='reserve_usage' and a.privilege_type='EXECUTE';
-- PUBLIC shows as an empty/`=X/` acl entry; if anon/authenticated/PUBLIC can EXECUTE, the revoke was lost.
```
**Corrective migration to add (a `0003`, applied dashboard-side, NOT auto-run from here):** `revoke all
on pending_corrections from anon, authenticated;` and re-apply `revoke execute on function
reserve_usage(uuid,integer), apply_correction(uuid,integer,bigint),
sweep_pending_corrections(integer) from public;` — mirroring the 2026-07-17 discipline that every such
function revoke EXECUTE-from-PUBLIC in the same migration that (re)creates it. Consider SELECT-only RLS
parity with `usage`/`api_keys` only if belt-and-suspenders is wanted; the grant revoke is the load-
bearing fix. **Status: CLOSED 2026-07-27 — see the CLOSURE block at the top of this entry for verified before/after results; corrective `0003` applied. The original "no RLS needed" note is superseded.**

---

## 2026-07-30 — Launch-gate QA pass, all seven surfaces (1 P0 + 4 P1 CONFIRMED; verdict FAIL, NOT closed)

First whole-product QA sweep rather than the single-axis passes above. Run against
`harden/proxy-spend-and-gates` @ `17ffad6` (2 commits ahead of `main`). Full report:
[`docs/QA_LAUNCH_GATE_2026-07-30.md`](docs/QA_LAUNCH_GATE_2026-07-30.md).

**Scope by agreement:** local build + test execution only. No Railway probes, no live
Supabase, no real spend, no load testing. `CONFIRMED` below means a repro script was
written and run; `PLAUSIBLE` means reasoned from source and labelled as such.

### Baseline — measured, replaces the sibling-filename guesswork

| Module | Tests | Coverage | race | gofmt | vet | govulncheck |
|---|---:|---:|---|---|---|---|
| `daemon` | 369 | 66.9% | clean | clean | clean | 0 reachable |
| `editapply` | 86 | 87.0% | clean | clean | clean | 0 |
| `proxy` | 52 | 79.7% | clean | clean | clean | 0 |
| `clients/tui` | 77 | 66.7% | clean | clean | clean | 0 reachable |
| `protocol` | **0** | **0.0%** | — | clean | clean | 0 |
| `helper` | **0** | **0.0%** | — | clean | clean | 0 |
| `clients/vscode` | 6 (real EDH, xvfb) | — | — | `tsc` clean | — | — |

The branch's claimed "28 → 52 tests, coverage 79.7%" is **confirmed exactly**. A
pre-review gap map built on sibling filenames was **wrong** and is retracted:
`ratelimit.go` ~100%, `protected.go` 100%, `match.go` 80–100% per function. The two
real zeros are `protocol` (625 lines — the shared wire contract) and `helper`.

### P0-1 — byte-guard kill REFUNDS the reservation (CONFIRMED, money path)

`streamSSE`'s kill raises the charge to `dataChunks` (`main.go:1061`), a chunk COUNT.
On the **`byte_guard`** bound the count is by definition tiny while bytes are at the
ceiling, so `finalizeUsage` takes its `actual > 0` branch and `correctUsage` gets a
large NEGATIVE delta. The site's own comment — *"Charge what was streamed, never
refund"* — holds only when `dataChunks >= reserved`, guaranteed on a `token_ceiling`
kill and **never** on a `byte_guard` kill.

Measured (reserved 4096, headroom 100 → ceiling 4196 → guard at 2,148,352 B; upstream
streams 3 × 900 KB):

```
reserved=4096  correction delta=-4093
finalizeUsage got actual=3 vs reserved=4096
```

**4093 of 4096 tokens refunded after streaming the maximum the ceiling permits.** The
same figure is charged to the per-key token bucket (`main.go:1562-1566`), so both new
controls fall to one defect. This is the **C3 abort-refund class** (`a703978`)
reintroduced via a path C3's regression test does not cover.

Reachability is **inverted**: the guard scales with the ceiling, so the smaller a key's
headroom, the easier the guard fires and the larger the refund as a fraction of what is
left. A key at its limit is easiest to exploit and gains most. `deepseek/deepseek-r1` is
in the shipped default allow-list and its `reasoning` deltas exceed 512 B/chunk, so no
adversarial provider is required.

**Fix direction:** floor the charge at `reserved` on any kill path, or thread an
explicit `killed bool` to force the existing "keep the reservation" branch. Then a
fail-when-neutered test on the **`byte_guard`** bound specifically — the existing budget
tests exercise `token_ceiling`, which is why 52 green tests missed this.

### P1-1 — the `budget_exceeded` chunk is invisible to the daemon (CONFIRMED)

`writeBudgetExceeded` emits `data: {"error":"budget_exceeded","truncated":true}` so a
client can *"tell throttling apart from a crashed connection."* The daemon's
`chatCompletionChunk` (`provider.go:83`) has `provider`, `delta.content`,
`delta.reasoning`, `finish_reason` — and **no `error` field**. Measured:

```
decoded provider="" choices=0
incompleteInfoFor("") = <nil>
```

No content, no `finish_reason`, so M1's truncation path never fires. The daemon reads
`[DONE]` and reports `Done:true` with no Error and no IncompleteInfo — a budget-killed
answer reaches the user as a **complete, successful response**, silently truncated.
Strictly worse than the silent close the chunk exists to avoid: indistinguishable from
*success*, not from failure.

A seam defect: the proxy suite asserts the chunk is emitted, the daemon suite asserts
its own parsing, and **no test spans the two**. There is no proxy↔daemon integration
test anywhere in the repo.

### P1-2 — CI gates nothing

`.github/workflows/build.yml` is one job: `docker build ./proxy`. 584 Go tests, 6
extension E2E tests, 4 eval tests, `gofmt`, `vet`, `govulncheck`, `tsc` — none gate a
merge. Every green number in this entry was produced by hand. P0-1 and P1-1 both shipped
past a green local suite; P1-3 shows the drift cost.

### P1-3 — the project's own retrieval gate is RED

`TestRerankEvalRetrievalRanking` FAILS: semantic-only 4/9, hybrid **4/9** (no better).
Its own messages: queries 8 and 9 — *"one of the two measured live failures this feature
exists to fix"* — are still MISS. File-level `TestEvalRetrievalQuality` is perfect
(top-1 1.00, top-3 1.00 vs 0.80 threshold), so the gap is chunk-level ranking
specifically. Invisible day to day because CI never runs `-tags eval`.

### P1-4 — core billing schema is not in version control

No `create table` for `usage` or `api_keys` anywhere — the only one is
`pending_corrections` (`0002:35`). The repo cannot recreate its own database, and
constraints are unverifiable from source, including whether `usage.key_id` is unique,
which `reserve_usage`'s `select ... from reserved, opened` cross join depends on. Plus:
no migration runner, no applied-version tracking, no down scripts — while `0002`'s own
banner warns that wrong ordering loses *"every correction on every request."* That
constraint is enforced by a human reading a comment, and it has already gone wrong once
(premature "applied", corrected 2026-07-25).

### P2 — four medium findings

- **P2-1 duplicate top-level JSON keys (CONFIRMED proxy-side).** All four gates read
  through `topLevelFields` → last-wins, then forward byte-for-byte carrying both. A body
  naming a banned model FIRST and an allowed one second was **admitted**. End-to-end
  exploitability depends on OpenRouter resolving first-wins — untested, RFC 8259 leaves
  it undefined → that half stays `PLAUSIBLE`. Same shape as branch defect 3, one level
  down. Fix: reject duplicate top-level keys rather than trusting two parsers to agree.
- **P2-2 daemon dispatcher is case-insensitive (CONFIRMED).** `{"UNDO":true}`,
  `{"Undo":true}`, `{"SEARCH":true}`, `{"EDIT":{...}}` all sniff true
  (`server.go:473,583,691`) — the exact pattern `491e0f2` replaced in the proxy, left
  unfixed in the daemon, where the misroute lands in a **destructive** `undo`. Bounded
  honestly: owner-only socket + `SO_PEERCRED`, so **not** privilege escalation, and no
  `PromptRequest` field case-folds onto a sniffed key, so no accidental client
  misroute. P2 for the destructive destination and for leaving a known-wrong pattern
  fixed in one twin only.
- **P2-3 zero accessibility affordances in the webview (CONFIRMED).** No `aria-*`,
  `role=`, or `tabindex` in 647 lines of `main.js` + 678 of `chatPanel.ts`. No live
  region for streamed tokens, no label on the auto-apply switch, nothing on the diff
  surface — i.e. Gate ④ ("you see the diff and approve it") is unusable non-visually.
- **P2-4 `0003` missed `increment_usage` (CONFIRMED static).** The corrective revoke
  covers `reserve_usage` / `apply_correction` / `sweep_pending_corrections`;
  `increment_usage` appears **0 times**, yet `0001` re-creates it and `0002` leaves it
  live. It is the most dangerous of the four if reachable — unconditional
  `update usage set tokens_used = tokens_used + p_tokens`, no limit check. Contained the
  same way the others were (SECURITY INVOKER + `usage` SELECT-only grants), so
  defense-in-depth, not a live hole — but an audit written to close a class that misses
  a member of that class is trap 3's own pattern. `0003`'s verify block omits it too.
- **P2-5 zero coverage on `protocol`, `helper`, and all 8 `chunkscrub.go` functions.**
  The send-time scrubber `scrub.go` is at 100%; the gap is the chunk-level **warn-mode**
  detector, whose false-negative rate is therefore unmeasured. Note the privacy claim is
  quantitative in nature and currently carries no number.

### Verified sound — pre-registered hypotheses the code REFUTED

Recorded because a pass that lists only failures misrepresents the system:

- **Missing-env fail-open — REFUTED.** `main.go:288-290` warns; `authorize` fails closed
  on unconfigured Supabase / error / non-200 / row count ≠ 1. Never serves unmetered.
- **Webview XSS — REFUTED.** Zero `innerHTML`/`outerHTML`/`insertAdjacentHTML`/
  `document.write`/`eval`/`new Function` in `media/` or `src/`; `textContent`
  throughout; CSP `default-src 'none'` + per-load nonce; `localResourceRoots` = `media/`.
- **Webview state loss — REFUTED.** `retainContextWhenHidden: true` (`chatPanel.ts:92`).
- **Container — SOUND.** `Dockerfile:17-18` non-root `USER 10001:10001`.
- **Secrets — SOUND.** `git log --all -p` scan for `sk-or-v1-`/`sk-`/`mochi_`/`eyJ`/
  `AKIA`/`ghp_` yields only the fixture `AKIA1234567890ABCDEF`. `.env` untracked.
- **`apply_correction`'s unreferenced data-modifying CTE — REFUTED.** Postgres runs
  data-modifying `WITH` exactly once to completion regardless of reference. Comment correct.

### P3

- **README:849's `go test ./...` fails from root** (rc=1, no root module — only
  `go.work`). Fails loudly, so no false confidence, but the documented first command a
  contributor runs is broken. BACKLOG's long-standing "`./...` fails from root" note is
  correct and the README was never updated to match.
- **Gate ③ parses only `.go`** (`apply.go:33`), reporting *"no syntax check applied"*
  otherwise — honest and qualified in `PRODUCT_OVERVIEW.md:295`, but the headline "five
  gates … a change only reaches your disk if it passes all of them" reads stronger than
  what a Python/TS user gets.

### Blocked, NOT skipped

Live Supabase grants (P2-4 + `usage.key_id` uniqueness — founder SQL in the report);
production wire probes against **this branch**; multi-replica rate-limit dilution (the
in-memory limiters are per-instance, so the new token bound is not a spend bound under
horizontal scale — `PLAUSIBLE`, single-instance testing only); real screen-reader passes;
Intel Mac / linux-arm64; upstream duplicate-key resolution (decides P2-1); load/soak.

### Verification of the review itself

Repros in the session scratchpad (`repro/proxy/zz_qa_repro_test.go`,
`repro/daemon_sniffer_repro_test.go.txt`, `repro/daemon_budget_chunk_repro_test.go.txt`);
coverage machine-generated from `-coverprofile`, not estimated; two repros were run
in-tree and removed, `git status` verified clean (0 files) after each. **No product code
was changed by this pass** — findings are reported, not fixed, per this repo's
one-isolated-commit-per-fix convention.

**Status: verdict FAIL, NOT closed.** Path to PASS is small and non-architectural: fix
P0-1 (a floor + a `byte_guard` fail-when-neutered test), fix P1-1 (one struct field + one
integration test), land P1-2's pipeline, decide P1-3 explicitly (fix or re-baseline in
writing), capture P1-4's baseline schema. Founder sign-off still required.

---

## 2026-07-30 — Retrieval eval follow-ups (opened by the launch-gate remediation batch)

Two tracked items opened while resolving P1-3. Both are retrieval-quality work, deliberately
NOT bundled into the remediation batch that surfaced them.

### (a) OPEN: chunk-ID ground truth is inherently fragile — anchor expectations to symbols

`rerank_eval_test.go`'s `exactChunks` are `file:startLine-endLine` strings, so **every edit
to a named file silently invalidates them**. That is not a hypothetical: it is what made the
eval read 4/9 and look like a retrieval regression when retrieval was in fact returning the
correct chunks (see the P1-3 resolution). Five of nine queries were stale, and the two eval
files disagreed with each other because both hard-coded line ranges independently.

Mitigated, not fixed, by `assertExpectationsAreCurrent` + the new `anchor` field: staleness
is now **loud and self-diagnosing** (it names the chunk the anchor actually lives in) instead
of silently scoring correct retrieval as a miss. The expectations themselves are still line
ranges and will still go stale — the guard just makes it a 30-second correction with the
answer printed, rather than an investigation that concludes "retrieval is broken".

Real fix: derive `exactChunks` from the anchor at test time (find the chunks containing the
anchor, use those as the expectation) so line numbers never appear in the harness at all.
Deferred because it changes what the eval measures, and doing that in the same batch as a
money-path fix would make both harder to review.

### (b) OPEN: `TestEditShapedRetrievalEval` is RED at 0/4 — pre-existing, and the QA pass missed it

Not caused by the remediation batch, and **verified so**: a `git worktree` at the QA baseline
`17ffad6` reproduces the identical failure (0/4, same three subtests —
`editapply-apply-extraction`, `tui-header-collision`, `zdr-refusal-phrasing`). This is the
open **H6 chunk-level retrieval** workstream, previously recorded at recall 2/4; it is now 0/4.

Correction to the launch-gate report: `docs/QA_LAUNCH_GATE_2026-07-30.md` states that of the
four gated eval tests, `TestEvalRetrievalQuality` passes and `TestRerankEvalRetrievalRanking`
fails. **A second eval test was also red at baseline and went unreported.** The report's
"`-tags eval` adds 4 gated tests" line is accurate; its implication that only one was failing
is not.

At least part of this is the same staleness class as (a) — `edit_eval_test.go:156` expects
`daemon/provider.go:91-130` for "zdrRefusalSubstrings + isZDRRoutingRefusal", the exact stale
range corrected in `rerank_eval_test.go`. Whether correcting the ground truth recovers the
recall, or whether there is a real chunk-level ranking gap underneath, is **unmeasured** and is
the first step of this item. The `anchor` guard should be ported here at the same time.

Consequence for CI: the scheduled `eval` job (`.github/workflows/build.yml`) is scoped by
`-run` to the three green eval tests, with the reason stated inline. A permanently-red
scheduled job is a job nobody reads, which is the failure mode P1-2 exists to fix. **Remove
that filter when this item closes** — it is the only thing keeping the known-red test out of CI.

**Status: both OPEN, tracked, not scheduled. Linked to the H6 retrieval work.**

---

## 2026-07-30 — Launch-gate remediation batch (all ten findings addressed; NOT founder-closed)

Resolves the 1 P0 + 4 P1 + 5 P2 from the launch-gate QA entry above, plus P3-1.
One isolated commit per finding, per this repo's convention. Full per-finding
annotations (including where a fix departed from the recommendation, and why) are
appended inline to [`docs/QA_LAUNCH_GATE_2026-07-30.md`](docs/QA_LAUNCH_GATE_2026-07-30.md);
the original findings are left exactly as written.

| Finding | Commit | Outcome |
|---|---|---|
| P0-1 byte-guard refund | `efe2bda` | `chargeForKill` = max(measured, chunks, reserved). Delta −4093 → 0 |
| P1-1 invisible budget kill | `b1fed6b`, `2e68d9d` | New `protocol.IncompleteBudgetExceeded`; 2 integration tests, the repo's first |
| P1-2 CI gates nothing | `0f3bca2` | 6-module matrix + govulncheck + EDH + scheduled eval |
| P1-3 retrieval gate RED | `96924a7` | Ground truth was stale, not retrieval. 4/9 → **8/9** |
| P1-4 schema not in VCS | `50ff5b3` | `0000_baseline` written, RECONSTRUCTED/UNVERIFIED |
| P2-1 duplicate JSON keys | `f8416a5` | 400 `duplicate_json_key`, nested objects included |
| P2-2 daemon dispatcher | `fd8d865` | All FOUR sniffers (the report listed three) |
| P2-3 webview accessibility | `74adfea` | EDH tests 6 → 14 |
| P2-4 `increment_usage` revoke | `50ff5b3` | `0004` written, 4-function verify query |
| P2-5 zero coverage | `ae6bbfe` | protocol 88.9%, chunkscrub 100%, helper off zero |
| P3-1 broken test command | docs commit | README documents the loop CI actually runs |

### Coverage, measured before and after

| Module | Before | After |
|---|---:|---:|
| `daemon` | 66.9% | 69.2% |
| `editapply` | 87.0% | 87.0% |
| `proxy` | 79.7% | 80.7% |
| `clients/tui` | 66.7% | 66.7% |
| `protocol` | **0.0%** | **88.9%** |
| `helper` | **0.0%** | 6.5% / **75.0%** (helperproto) |
| `clients/vscode` | 6 EDH tests | **14** EDH tests |

### Whole-batch verification actually run

Six modules: `go build`, `gofmt -l`, `go vet`, `go test -race` — all green.
`govulncheck` — "No vulnerabilities found" on all six. EDH — 14/14 in a real
Extension Development Host. `-tags eval` (CI's green subset) —
`TestEvalRetrievalQuality` top-1 1.00 / top-3 1.00, rerank 8/9,
`TestTokenEfficiencyEval` pass.

Every fix has a fail-when-neutered twin, verified by removing the control,
watching the specific test fail, and restoring it. Two were neutered in **two**
ways: P1-1 both by deleting the error check and by moving it below the
zero-choices guard (the subtler regression), and P1-3 by restoring the original
stale expectation to confirm the new guard names it as staleness rather than as a
retrieval regression.

The QA repro bundle was re-run against the fixed tree. Its `/tmp` directory had
been reaped, so all three were reconstructed verbatim from the review record:

- **P0-1 repro: now PASSES** (delta 0, was −4093).
- **M2 duplicate-key repro: PASSES, inverted** — the gate now refuses with
  400 `duplicate_json_key` and upstream is never contacted.
- **Sniffer repros: both PASS** — every case variant falls through, duplicates
  rejected.
- **Budget-chunk repro: still "fails", and that is CORRECT.** It asserts on
  intermediate state — that the kill chunk decodes to zero choices and that
  `incompleteInfoFor("")` returns nil. Both remain true after the fix and *must*:
  the kill chunk genuinely has no choices, and an empty finish_reason must keep
  meaning "complete". The repro never exercises the code path that changed. Its
  premises still hold; its **conclusion** — "a budget-killed answer is reported
  as a successful, complete response" — is now false, proven by
  `TestStreamCompletion_BudgetKillIsReportedAsIncomplete` and by the two seam
  tests, all of which fail when the fix is neutered. It is not a valid post-fix
  regression test and was not made to pass.

### Still open — founder-gated or blocked, unchanged by this batch

1. ~~**Applying `0000` and `0004`**~~ — **partially CLOSED 2026-07-30, see the
   next entry.** `usage.key_id` is the PRIMARY KEY; the cross-join concern is
   structurally impossible. `0004`'s revoke and the remaining catalog queries are
   still founder-gated.
2. ~~**The first green CI run.**~~ — **CLOSED 2026-07-30**, 14/14 on the first
   attempt. See the next entry.
3. **Production wire probes against this branch.** Not re-verified since
   2026-07-27, and never against this code.
4. **Multi-replica rate-limit dilution.** In-memory limiters remain per-instance;
   a documented residual, not fixed here.
5. **Real screen-reader passes.** P2-3's assertions are structural only.
6. **Intel Mac / linux-arm64**, load/soak, and upstream duplicate-key resolution
   (P2-1 makes that last one moot rather than answering it).
7. **The two retrieval follow-ups** opened by this batch — line-range ground
   truth fragility, and `TestEditShapedRetrievalEval` red at 0/4 (pre-existing;
   the QA report missed it). See the entry above.
8. **Make CI an *enforced* gate.** `0f3bca2` makes the suite run on every push and
   PR to `main`; it cannot make a red run block a merge. Required status checks
   need branch protection, and branch protection is unavailable while this repo is
   private on a free plan (`403 Upgrade to GitHub Pro or make this repository
   public`). Two ways out, both decisions rather than work: make the repo public,
   or move it to Pro. Then require the `go`, `govulncheck`, `vscode extension` and
   `proxy-image` checks on `main`. Until one of those happens, "the suite is a
   gate" is a statement about discipline, not about GitHub.

**Status: all ten findings ADDRESSED and verified locally; NOT founder-closed.**
The QA verdict of FAIL is not retracted — it was correct when written. Whether
the gate now passes is the founder's call, and items 1 and 2 above are the two
that most plausibly still block it.

---

## 2026-07-30 — The two remaining blockers, worked

Both items the remediation batch left open were carried as far as they can go
without dashboard access. One is fully closed; the other is answered on the
question that mattered and reduced to a five-minute paste-in.

### Blocker 2 (CI): CLOSED — 14/14 green on the first run

PR [#1](https://github.com/Rav-2007/codeterminal-core/pull/1) (draft, → `main`),
run `30508475988`. The branch was **unpushed** until now — `origin` still sat at
`17ffad6`, all eleven remediation commits local — and the workflow fires only on
`push: [main]` / `pull_request: [main]`, so opening a PR was the only way to
trigger it. All fourteen jobs passed with **no fix commits required**:
`go` ×6, `govulncheck` ×6, `vscode extension`, `proxy-image`. The `eval` job
correctly did not run (`if:` gated to schedule/dispatch).

Three risks were pre-registered before pushing, and **all three failed to
materialize** — recorded because predicting them wrongly is the useful part:

- `xvfb-run -a npm test` was the one genuinely unproven command (no xvfb on the
  dev box). It worked first try: VS Code **1.131.0** downloaded and launched on a
  bare `ubuntu-latest`, **14 passing in 638ms**, no extra apt packages needed.
- `go install govulncheck@latest` runs at the repo root with `go.work` active and
  no `working-directory`. Clean on all six modules; no `GOWORK=off` needed.
- `daemon`'s `go test -race ./...` runs `TestSeam_ProxyBudgetKillReachesTheUser`,
  which **builds the proxy binary from a sibling module** mid-test and is skipped
  only under `-short`, which CI does not pass. Package green in 21.5s (job 2m23s,
  the longest). Note the log is not `-v`, so this is a package-level pass, not a
  per-test confirmation.

One cosmetic annotation on every job: `actions/checkout@v4` / `setup-go@v5` /
`setup-node@v4` target Node 20 and are force-run on Node 24. Not a failure, not
addressed here.

**Still not verified, and cannot be from a PR:** the scheduled `eval` job.
`workflow_dispatch` only lists workflows present on the **default branch**, so it
is undispatchable until `build.yml` lands on `main`. First chance to run it is
`gh workflow run build.yml -R Rav-2007/codeterminal-core` after merge.

**And a correction to what P1-2 actually bought: the pipeline RUNS, it does not
BLOCK.** "Merge gate" — the phrasing in `0f3bca2`'s own comment, in P1-2's title,
and in this entry — is wrong. Blocking a merge requires *required status checks*,
which require branch protection, which is **unavailable on this repository**:
private on a free plan, so
`GET /repos/Rav-2007/codeterminal-core/branches/main/protection` answers
`403 Upgrade to GitHub Pro or make this repository public`. What exists is the
suite running automatically on every push and PR to `main`, with the result
visible before merge. That fixes the *invisibility* half of P1-2 — the half that
let the eval gate go red unnoticed — and leaves the *enforcement* half open. Both
`build.yml` and the QA entry now say so; the residual is item 8 below.

Also worth noting for anyone running `gh` in this repo: there are two remotes, and
`upstream` is an unrelated fork parent (`NousResearch/hermes-agent`). `gh`
resolves to it, so a bare `gh pr view 1` returns a *different repository's* merged
PR. Every `gh` call needs `-R Rav-2007/codeterminal-core`; `gh repo view` takes
the repo positionally instead.

### Blocker 1 (migrations): the headline question is ANSWERED

**`usage.key_id` is the PRIMARY KEY.** The cross-join multiplication `0000` was
written to ask about is structurally impossible, not merely absent. Corroborated
independently: 15 usage rows, zero duplicate `key_id`s. The FK to `api_keys.id`
that `0000` carried as "FK GUESSED" is also real.

Method: read-only PostgREST probes from the dev box using `proxy/.env`'s
`SUPABASE_URL` + `SUPABASE_SERVICE_ROLE_KEY` — `GET /rest/v1/` (the OpenAPI
document, which stamps `<pk/>` and `<fk .../>` into column descriptions and
carries `default` and `required`) and `GET /rest/v1/usage?select=key_id`. Both
GETs. Nothing written, and **no RPC invoked** — deliberately not
`increment_usage`, because probing that function's reachability means *calling*
it, and calling it is a write against live billing on the one function with no
`token_limit` check.

`0000` is now RECONCILED rather than reconstructed. It was wrong in four places:

- `usage.token_limit` has a **default of `100000`**; the file declared none.
- `usage.period_start` (`timestamptz not null default now()`) was **missing
  entirely** — a NOT NULL column the repo did not know existed.
- `api_keys.key_prefix` (`text not null`, **no default**) was missing. This one
  would have broken a from-scratch rebuild outright.
- `api_keys.label` and `api_keys.user_id` were missing. `user_id` is the only
  column in either table implying multi-tenancy and the proxy reads it nowhere.

Everything the file had *guessed* — `gen_random_uuid()`, `default true`, the
whole `created_at` column, `tokens_used default 0` — was correct.

**New, and not previously known to this repo: a fourth relation
`api_keys_public` is exposed over PostgREST and appears in no migration.** It
projects `(id, key_prefix, label, active, created_at, user_id)` — `api_keys`
minus `key_hash` — which reads as a deliberately safe public projection. No
credential is exposed. But it is cross-tenant metadata on an unaudited relation,
and its grant surface is unknown, so it is now covered by the runbook's grant
query.

**Still founder-gated** (the SQL catalog is not reachable over PostgREST), now
collected as one paste-in block in
[`docs/MIGRATION_RUNBOOK_0000_0004.md`](docs/MIGRATION_RUNBOOK_0000_0004.md):
`0004`'s revoke and its 4-function verify; the `prosecdef` containment check
(*if any function is SECURITY DEFINER, `0004`'s severity is wrong and it is a
live hole*); whether `api_keys.key_hash` carries a **UNIQUE** constraint (absent
⇒ two rows with one hash lock that key out, fail-closed availability landmine);
the `ON DELETE` action on `usage.key_id`'s FK; and the grant surface across all
three relations.

**Status: CI blocker CLOSED. Migration blocker reduced from "unknown, most
valuable question in the repo" to "one five-minute paste-in, with the dangerous
answer already known to be safe." Still NOT founder-closed; the FAIL verdict
remains unretracted.**

---

## 2026-07-30 — PR #1 landed on `main`; CI fully verified; deploy verified in production

Both remaining blockers are now closed by evidence rather than by argument, and one
remediation claim was corrected rather than confirmed.

`main`: `2797bbd` → **`5ea25b2`**. PR #1 MERGED 03:18:55Z.

### How it was merged, and why that mattered

Fast-forward push (`git push origin harden/proxy-spend-and-gates:main`), not
squash and not rebase. This report, this file, and the migration banners all cite
these commits **by SHA**; squash and rebase-merge both rewrite them and would have
invalidated every one of those references at once. GitHub closed PR #1 as merged
on its own once the commits appeared on the base branch. `main` is still linear —
`git log --merges origin/main` is empty — which is also the pre-existing
convention here.

### CI: the last two unverifiable things, verified

| What | Run | Result |
|---|---|---|
| `push` trigger path (both earlier greens were `pull_request`) | `30510849208` | 14 green, `eval` skipped |
| **scheduled `eval` job** — undispatchable until `build.yml` reached the default branch | `30510976622` | **15/15 green**, incl. `retrieval eval (scheduled)` |

The eval job: `ok codeterminal/daemon 211.715s`, job 4m16s. The pre-registered
risk did **not** fire — the embedding model and ONNX runtime are fetched at test
time (`daemon/modelfetch.go`, `daemon/onnxruntimefetch.go`) and were only ever
cached on the dev box; a bare `ubuntu-latest` fetched both. ~211s vs ~148s locally
is that fetch. Package-level pass, not per-test: the job does not pass `-v`.

Two things to know before reading the first *scheduled* run: a `workflow_dispatch`
runs **every** job, not just `eval` (the others carry no `if:` guard), and the
`-run` filter still excludes the known-red `TestEditShapedRetrievalEval` (H6).

### Production deploy: six probes, cost-ordered

Railway picked the merge up in under two minutes. Probes 1–4 are refused
**pre-upstream on both the old and new builds**, so they cost nothing whichever is
running; probe 2 is a *proven* discriminator, baselined against `2797bbd`
immediately before the merge rather than assumed:

| Probe | Old build (`2797bbd`) | New build (`5ea25b2`) |
|---|---|---|
| `GET /health` | `200 {"status":"ok"}` | `200` — the non-root container (`USER 10001`) boots |
| duplicate `"model"` key, banned model | `403 model_not_allowed` (dup resolved last-wins, model gate sees it) | **`400 duplicate_json_key`** (ambiguity refused before any gate reads the body) |
| allowed model, `provider` routing absent | `403 zdr_required` | `403 zdr_required` — **F1 not regressed** |
| bogus bearer token | `401` | `401` — still fails closed |
| `max_tokens: 999999` | clamps to 32768 and **forwards** (would spend) | **`403 max_tokens_too_large`** — refused, not clamped |
| small streamed completion | — | `200`, SSE to `[DONE]`, `total_tokens=13`, `cost=$0.00000147` |

`HEALTH_EXPOSE_COMMIT` is **not** set on Railway, so `/health` returns
`{"status":"ok"}` with no SHA and cannot identify the running build. That is why
the discriminator exists. Setting that variable would make future deploy
verification a single zero-spend GET.

### Two items previously deferred to founder SQL, answered read-only

Both from the dev box using `proxy/.env`, both plain `GET`s, no RPC invoked:

- **`usage` records.** `tokens_used` on the probing key moved `69156 → 69183`,
  **delta +27**, against a stream that reported `total_tokens=27`. Exact match.
  This is the first live end-to-end confirmation that the metering path works —
  previously carried as "usage-in-Supabase unverified, needs founder SQL."
- **The §5(e) outbox drains.** `pending_corrections` is `[]` after two completions.
  Every reservation opened was closed; nothing stranded.

Neither touches `increment_usage`, which is still deliberately unprobed: calling it
*is* the write.

### The one claim that got corrected, not confirmed

**P1-2's pipeline RUNS; it does not BLOCK.** Branch protection is unavailable on
this repo (private, free plan — `403 Upgrade to GitHub Pro or make this repository
public`), so required status checks cannot exist. `0f3bca2` closed the
*invisibility* half of P1-2, which is the half that let P1-3 go red unnoticed. The
*enforcement* half is open and is item 8 in the previous entry's list. `build.yml`,
the QA report and this file all now say so; the "merge gate" phrasing was wrong.

### Status

CI blocker **CLOSED, fully verified**. Deploy **VERIFIED in production**. The
migration blocker is unchanged: one five-minute paste-in at
[`docs/MIGRATION_RUNBOOK_0000_0004.md`](docs/MIGRATION_RUNBOOK_0000_0004.md),
still founder-gated because the SQL catalog is not reachable over PostgREST. **The
FAIL verdict remains unretracted — that is the founder's call, not this entry's.**

---

## 2026-07-30 — Robustness program Phase 0 + Phase 1 (lifecycle & fault containment; verified, NOT founder-closed)

Opening phases of the 70% → 90% robustness program. Plan and scorecard:
`~/.claude/plans/waiting-on-the-eval-serene-sutton.md`. Baseline and
measurements: [`docs/ROBUSTNESS_BASELINE.md`](docs/ROBUSTNESS_BASELINE.md).

The program ran under one standing rule, set before any measurement: **a finding
that fails to reproduce gets struck rather than fixed on faith.** One of the two
Phase-0 findings was struck. That is recorded here rather than quietly dropped.

### Phase 0 — the baseline, measured on this tree

Coverage floors (`go test -cover`, per module, since `./...` from the root fails):
`protocol` 88.9%, `editapply` 87.0%, `proxy` 80.3%, `helper` 6.5% /
`helperproto` 75.0%, `clients/tui` 66.7%, `daemon` 69.3%.

Runtime baseline (real binaries, `/proc`): proxy idle 6 threads / 6 fds /
7.8 MB, settled after 12 concurrent completions 11 / 10 / 12.8 MB — fds
plateaued rather than growing, and all 12 reservations closed correctly. Daemon
idle 8 / 9 / 12.9 MB, unchanged after 20 socket connections.

A purpose-built harness (fake Supabase recording every RPC in order + fake
OpenRouter streaming SSE at a configurable chunk delay) drives the **real proxy
binary**. It is the basis of the Phase-3.2 integration matrix and should be
promoted into `proxy/` there.

### Finding CONFIRMED, and it was worse than the sweep's design assumed

SIGTERM mid-stream, measured against the real binary: `authorize →
reserve_usage` **and nothing else**. The proxy died instantly (no
`signal.Notify` anywhere in the module), the client got a truncated body with no
error frame (`curl` exit 18), and **3,959 of 4,096 tokens stayed charged
forever** — the sweep never refunds, deliberately, because a dead request's true
usage is unrecoverable.

The sweep treats this as a rare crash artifact. **A platform redeploy is a
SIGTERM**, so it fired on every deploy, for every request in flight. The sweep
was working correctly; its premise was wrong.

### Finding REFUTED — the ~80 ms TTFT hypothesis is not the un-drained body

The TTFT note's explicitly-untested hypothesis (un-drained Supabase response
bodies break connection pooling, costing a handshake per call) was measured at
0 B / 100 B / 4 KB / 64 KB: **1 connection across 20 sequential requests either
way.** `json.Decoder`'s buffered reader consumes through EOF while filling its
buffer, so a `Content-Length` body re-pools regardless. **The ~80 ms remains
unexplained**; the other candidate from that note (collapsing validate+reserve
into one RPC) is untouched and remains live. P1.5 was rescoped accordingly and
must not be reported as a latency fix.

### Phase 1 — five fixes, one per commit

| Item | Commit | What |
|---|---|---|
| P1.1 | `6b029e4` | One deferred exactly-once reservation finalizer |
| P1.2 | `13b36af` | Proxy graceful shutdown + sweep panic containment |
| P1.3 | `2f00ee0` | Handler panic recovery with a diagnosable 500 |
| P1.4 | `eb984a3` | Daemon waits for in-flight requests before exiting |
| P1.5 | `3aa9c50` | One capped-JSON decode helper that drains its body |
| P1.2b | `0dcfabe` | Shutdown sequence extracted so it is testable, and tested |

**P1.1** replaces four hand-placed `finalizeUsage` sites with one deferred
finalizer over a `reservationOutcome` whose zero value is the safe one (full
refund). `finalizeUsage` is untouched — it owns the charge POLICY and is tested
on it; `finalizeReservation` owns only the once-ness, guarded so nested defers
and P1.3's middleware cannot double-bill.

**P1.2** flips `/health` to 503 `draining` with `Retry-After`, stops the sweep,
then drains under a 25 s grace sized against the platform's SIGTERM→SIGKILL
window rather than `upstreamTimeout`.

### Three of our own mistakes, caught by neutering rather than by review

Recorded because each one made an assertion silently inert, and the pattern is
more reusable than the fixes:

1. **P1.1's first test faulted mid-stream** — a site `streamSSE`'s own defer
   already covered, so it would have passed pre-fix. Moved to `WriteHeader`,
   which nothing covered. The mid-stream case is kept but labelled as *not*
   evidence the refactor added anything.
2. **P1.5's first fix was self-defeating.** The drain was bounded by the same cap
   as the decode — and the only case needing a drain is the one that *exceeded*
   that cap, so it could never reach EOF. Still 20 connections with the "fix" in.
   Caught because the test asserts the **connection count**, not that a drain was
   attempted. Now bounded by `maxDrainBytes` (8 MB).
3. **P1.2b's ordering assertion could never fail**, twice over: it sampled after
   already waiting for the request, and its slow handler (600 ms) finished long
   before the 3 s pre-drain window elapsed, so `Shutdown` had nothing to wait on.

A fourth was caught by running the drill rather than by testing: the first
graceful-shutdown cut announced 503 and then called `Shutdown`, which closes the
listener **immediately** — so nothing could ever connect to observe the 503, and
a load balancer would learn about the shutdown from a connection error, which is
exactly what the flip exists to prevent. Fixed with a 3 s `preDrainDelay`.

### Phase 1 gate — the same drill, after

- Stream **ran to completion**: 42 chunks + `[DONE]`, client HTTP **200**
  (baseline: cut at 10 chunks, `curl` exit 18).
- `apply_correction(tokens=-3959, pending=1001)` applied; ledger balanced.
- **Zero** `ABANDONED RESERVATION` lines (baseline: one per SIGTERM'd request).
- Log reads `drain complete, all in-flight requests finished` → `exiting`.
- Health sequence observed live: `200 {"status":"ok"}` → SIGTERM →
  `503 {"status":"draining"}` + `Retry-After: 1` → listener closed.

Six modules green on `go build`, `gofmt -l`, `go vet`, `go test -race`. Proxy
coverage **80.3% → 81.7%** (it dipped to 78.5% first, because the new
safety-critical code was written inside `main()` where nothing could reach it —
which is what prompted P1.2b).

### Not done / carried forward

- **P1.4's live drain was not separately confirmed.** It rests on unit tests
  asserting both directions of `WaitForDrain` plus a neuter check; the `main.go`
  wiring is three lines calling that tested function. A live daemon drill is
  still worth running. **→ DONE in Phase 2 step 2.0, both directions; see below.**
- **SIGTERM-truncated streams have no incompleteness signal.**
  `IncompleteBudgetExceeded` (`b1fed6b`) covers the budget-kill case; a stream cut
  by a drain deadline is still indistinguishable to the daemon from a complete
  answer. Belongs to whichever phase next touches the stream protocol.
- Phases 2–6 (observability, test depth, security-gate closure, schema
  integrity, maintainability) are unstarted.

**Status: Phase 0 and Phase 1 complete and verified locally; NOT founder-closed.**

---

## 2026-07-30 — Robustness program Phase 2 (observability; verified, NOT founder-closed)

Plan: `~/.claude/plans/phase-0-and-phase-mighty-panda.md`. The bar was set before
any code was written: **the undiagnosed production 401 becomes diagnosable.** Not
"logging was added" — a replay drill had to end with the log trail naming the
branch. It does; the gate transcript is in §Gate below.

### Step 2.0 — Phase 1's one open residual, closed first

P1.4's live daemon drain was never separately confirmed (the first attempt's shell
took the SIGTERM before the daemon logged). Re-run properly, with the daemon under
`setsid` and signalled by PID, against a fake upstream that stalls mid-stream:

| | real build | `WaitForDrain(0)` (neutered) |
|---|---|---|
| tokens after SIGTERM | kept arriving (t=3.16s, 3.21s, 3.26s) | none |
| client | `DONE` frame, exit 0 | connection closed, no `DONE`, exit 2 |
| daemon log | `drain complete, no requests in flight` | `drain INCOMPLETE after 5s` |

So the drill has teeth, and P1.4 is confirmed rather than inferred.

### The five commits

| Item | Commit | What |
|---|---|---|
| P2.1 | `2e9a1fe` | A request id every caller can quote |
| P2.2 | `13c5685` | `log/slog` with a gate vocabulary + the secrets test |
| P2.3 | `4a6b9ff` | `expvar` counters behind `PROXY_ADMIN_TOKEN` |
| P2.4 | `9a277f2` | Daemon counters on the existing status surface |
| P2.4b | `7cdd36b` | `printStatus` made reachable; its ordering pinned |

**P2.1.** `X-Request-Id` on every response (minted, or adopted from the caller),
repeated in every JSON refusal body, present on the panic 500. Inbound ids are
adopted only in the lowercase-hex shape we mint, and otherwise **replaced, not
sanitized** — the id is echoed into error bodies and the daemon classifies proxy
errors by substring-matching those bodies (`daemon/modelerror.go`'s
`quotaPhrases`), so a looser charset would let a caller steer how its own error is
classified. Every needle there contains a non-hex letter, so hex provably cannot
spell one; hex also excludes newline and `=`, so an id cannot forge a log line.

**P2.2.** ~50 `Printf` sites became logfmt records with a stable key set
(`req_id`, `gate`, `key_prefix`, `key_id`, `status`, `latency_ms`, `reserved`,
`actual`, `pending_id`, `scope`, `provider`). The `gate` labels are one const block
shared with P2.3's counters, so a count and a line name the same thing. Naming
rule: a label names the **branch**, not the status — six causes all answered 401 in
the incident, and "401" was exactly the useless part.

**P2.3.** Counters including `sweep_last_run_unix` / `sweep_last_error_unix`, set
separately because a sweep that **runs and fails** is a different state from one
that never ran. `/admin/metrics` exists only when a token is configured.

**P2.4.** Daemon counters ride the existing `StatusResponse` over the existing
0600 + `SO_PEERCRED` socket — no new mechanism, no HTTP listener. Additive with
`omitempty`, so `ProtocolVersion` stays **1**. The daemon also folds the proxy's
`X-Request-Id` into its operator-only error detail, through a validator that
accepts only our hex shape (that header comes from whatever host `apiBase` names
and lands in a log file, so a newline in it would forge log lines).

### Three things the LIVE runs caught that reading the code did not

1. **The first ordering assertion was inert.** It checked that the panic 500
   carries the id header — and the header survives *both* middleware orders, since
   `withRequestID` sets it before the handler panics and a header set before
   `WriteHeader` is still sent. What actually breaks on a swap is `recoverPanics`
   being able to **read** the id. The panic line now carries `req_id` and the test
   asserts it equals the header; swapping the order now fails.
2. **`expvar.Handler()` disclosed the binary's path.** It renders expvar's
   *default* registry, which the package populates with `cmdline` and `memstats`.
   A scrape of the real binary returned the full filesystem path and several
   hundred lines of heap internals with the counters underneath. It now serves only
   our own map plus three runtime numbers chosen one at a time; a test asserts
   `cmdline`/`memstats` are absent. Dropping `expvar.Publish` also removed the
   duplicate-name panic hazard that had shaped the design.
3. **Coverage caught an untestable property.** Adding the daemon counters took its
   coverage 69.3% → **68.9%**, under the Phase-0 floor. The uncovered code was
   `printStatus`, unreachable because it took an `*os.File` — and it is where the
   "degraded block prints LAST" property lives, directly below where the counters
   were inserted. Now an `io.Writer`, with four tests including the ordering.

### Gate — the production 401, replayed end to end

Real proxy binary, fake Supabase answering the key lookup `200 []` (the exact
production symptom), then the real daemon pointed at that proxy:

```
client:  401 + X-Request-Id: 142cb6a6e09d41bc
         {"error":"unauthorized","request_id":"142cb6a6e09d41bc"}
proxy:   msg="auth rejected" req_id=142cb6a6e09d41bc gate=auth_row_count
                             key_prefix=mk_live_ matches=0
         msg=request req_id=142cb6a6e09d41bc status=401 latency_ms=3
counters: refusals_by_gate = {"auth_row_count": 1}
daemon:  model API error: [auth] ... (upstream req_id=ab806d854bdf7aad)
status:  since start  1 prompt(s), 0 apply(s) (0 failed), ...
```

One quoted string resolves to one request, names which of `authorize`'s eight
fail-closed branches fired, and the same cause is countable. **The bar is met.**

Also verified live, because it is the trap this phase could most easily have
introduced: a streamed completion through the wrapped handler still arrives
incrementally — 7 chunks spread over 2.00s against a 0.4s-per-chunk upstream
(a buffering wrapper would deliver all 7 within milliseconds). `statusRecorder`
forwards `http.Flusher`; without that, `streamSSE`'s type assertion fails,
`canFlush` goes false, every stream silently buffers, and every existing test
still passes. The whole request trail — `quota reserved` → `usage corrected` →
access line — now shares one `req_id`.

### Regression

Six modules green on `go build`, `gofmt -l`, `go vet`, `go test -race`. Coverage:
`protocol` 88.9%, `editapply` 87.0%, **`proxy` 81.7% → 83.6%**, `helper` 6.5% /
`helperproto` 75.0%, `clients/tui` 66.7%, **`daemon` 69.3% → 70.1%**. Every new
assertion was neuter-checked (14 neuters this phase); the ones that mattered are
recorded above.

### Scorecard

Dimension 5 (observability) **35 → ~85**: request identity end to end, structured
logs with a machine-readable refusal vocabulary, counters including
reconciliation liveness, and a secrets-in-logs rule that is now asserted rather
than commented. Not 90+: there is no metrics backend, no tracing, and no alerting
— a scrape still requires someone to scrape it.

### Not done / carried forward

- **`PROXY_ADMIN_TOKEN`'s fatal short-token path is not asserted in-process** (it
  calls `logger.Fatal`, which would kill the test binary). The boundary is
  asserted on the predicate instead. A subprocess test belongs with P3.
- **`sweep_last_run_unix` is per-instance**, like the rate limiter's buckets: with
  multiple replicas a scrape answers "is *this* replica's sweep alive". Stated
  rather than buried; same shape as the limiter's documented residual.
- **SIGTERM-truncated streams still have no incompleteness signal** (carried from
  Phase 1, untouched here).
- **The ~80 ms `reserveQuota` latency remains unexplained.** Phase 2 added
  `latency_ms` to every request, which makes it measurable in production for the
  first time, but the collapse-validate+reserve hypothesis is still untested.
- Phases 3–6 (test depth, security-gate closure, schema integrity,
  maintainability) are unstarted.

**Status: Phase 2 complete and verified locally; NOT founder-closed.**
