
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

### (b) Top-1 ranking: down-weight test files — OPTIONAL POLISH
Top-3 recall is 5/5, but top-1 is 3/5: for some queries a *_test.go file or an adjacent code
file edges out the canonical implementation at rank #1. Not the prose-vs-code bug (that's
fixed). Could down-weight _test.go files for retrieval. Purely a nicety; may never be needed.

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
clicking Undo.

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

- **Security review** — the daemon opens a local socket and the edit engine writes to user
  files; both must be reviewed before others run Mochiii. Blocking gate.
- **ZDR confirmation** — verify zero-data-retention is enforced on the inference provider;
  the whole privacy pitch depends on it. Currently unverified. (Folds in item (g) above.)
- **Performance NFR** — TTFT < 400ms is a target, not yet measured.

## Hygiene / recurring

- **Never screenshot .env / keep the API key off-screen.** The key has been exposed in
  screenshots multiple times; rotate as routine. (Rotated 2026-07-08.)
- **Rebuild affected binaries after code changes** — daemon + TUI + extension; source and
  binary drift, especially when a change spans modules.
