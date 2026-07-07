
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

### (d) Conversational memory — DEFERRED (needs daemon + protocol change)
The TUI is "multi-turn" in UI only: each turn opens a fresh connection and sends ONLY the
latest prompt. PromptRequest has no history field and handleConn is one-prompt-per-connection,
so the model has no memory of prior turns. Real conversation memory requires adding a history
field to the protocol and threading it through the daemon. Deferred intentionally; the thin-
slice TUI ships without it.

### (e) DONE-ish: primary model swapped to deepseek/deepseek-v4-flash
Validated live: no think-blocks, grounds on real code, emits clean SEARCH/REPLACE,
writes idiomatic Go, passes syntax gate. Cheaper ($0.09/$0.18) + 1M context. Config-only
change (models.json). NOTE: fix the stale price comment in models.json note field.
