
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
