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
  FIXED `442d018`).** Each connection is handled on its own goroutine and the Apply / Undo write path
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
- **FIXED 2026-07-19 (commit `442d018`) — Gate 6 now closed.** In-process per-workspace-root lock
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

**The three implementations as they stand today:**
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
