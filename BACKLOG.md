
## Known deferred debts

### (a) Helper embedding timeout on large repos — MUST FIX BEFORE SHIPPING
The embedding helper has a fixed per-call timeout. Indexing a large repo (e.g. thousands of
chunks) in one RPC exceeds it. The rerank eval already had to batch embedding calls in
test-only scaffolding to work around this. The REAL index path needs the same batching
before CodeTerminal is pointed at any large/production codebase. Not urgent for dev on this
repo (~334 chunks); blocking for real users.

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

### (a) UPGRADED TO NEXT-UP: helper embedding timeout blocks full-repo indexing
Hit live twice — full repo (334 chunks) times out; only subdirectories index today.
Fix: batch embedding calls in the real index path (helper has a fixed per-call timeout).
This is now the blocker for real full-codebase use. Do next.
