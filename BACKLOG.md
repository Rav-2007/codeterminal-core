
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

### (c) Auto-apply-with-undo mode — PRODUCTION ENHANCEMENT
V1 of apply-edits uses confirm-every-edit (explicit [y/N] per edit, safest). For a lower-
friction production feel, add an opt-in auto-apply mode: applies behind the syntax gate +
backup without per-edit confirmation, relying on undo/restore for recovery. Deferred until
apply-edits v1 is proven. Keep confirm-every-edit as the default even after adding it.
NOTE: apply-edits v1 is now proven in BOTH clients (TUI + VS Code diff-apply, verified live),
so this enhancement is now unblocked whenever it's wanted.

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

- **VS Code extension: remaining capabilities** (future slices, rough order) — a grounding
  indicator in the panel UI; a native VS Code diff view / inline decorations (nicer than the
  current whole-block red/green); a native Undo button (undo currently only via
  `codeterminal-daemon edits undo`, same as the TUI); then the larger fronts (ghost text,
  terminal error interceptor).

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
