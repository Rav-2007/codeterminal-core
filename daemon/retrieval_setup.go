package main

import (
	"log"
	"os"
	"path/filepath"
)

// setupRetrieval resolves and starts everything the live prompt path needs
// for retrieval-augmented context — reusing the exact same primitives the
// CLI `index`/`retrieve` commands use (NewChromemStore, checkEmbedderStamp,
// and an embedder built via newEmbedder) instead of duplicating any of that
// logic. Unlike the CLI's one-shot open/close-per-invocation pattern, the
// daemon calls this once at startup and holds the result for its lifetime.
//
// It never fails the caller: any problem (retrieval disabled, no index yet,
// embedder unavailable, stale-index mismatch) is logged as a clear one-line
// reason and reported back via a nil embedder/store — the daemon must still
// start and serve prompts without local context in every one of these
// cases. newEmbedder is a parameter (normally newActiveEmbedder) so tests
// can inject a fake without needing the real model or a helper subprocess.
//
// The lexical (keyword/FTS5) tier degrades independently and more loosely
// than the semantic tier: if the lexical index fails to open, that alone
// never disables retrieval — it only disables the lexical half of it,
// leaving semantic-only retrieval exactly as it behaved before the lexical
// tier existed. lexicalStore is nil in that case, and retrieveTopK already
// treats a nil lexicalStore as "skip the lexical tier".
func setupRetrieval(
	cfg *Config,
	workspace string,
	disabled bool,
	logger *log.Logger,
	newEmbedder func(*log.Logger) (Embedder, func(), error),
) (embedder Embedder, store VectorStore, lexicalStore LexicalStore, stop func(), topK int, contextBudgetChars int) {
	noop := func() {}

	if disabled {
		logger.Print("retrieval disabled via --no-context")
		return nil, nil, nil, noop, 0, 0
	}
	if cfg.Retrieval.Disabled {
		logger.Print("retrieval disabled via config (retrieval.disabled=true in models.json)")
		return nil, nil, nil, noop, 0, 0
	}

	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		logger.Printf("retrieval disabled: resolving workspace %s: %v", workspace, err)
		return nil, nil, nil, noop, 0, 0
	}

	indexDir := filepath.Join(absWorkspace, indexDirName)
	if _, err := os.Stat(indexDir); err != nil {
		logger.Printf("retrieval disabled: no index found at %s (run `index %s` to enable retrieval-augmented answers)", indexDir, workspace)
		return nil, nil, nil, noop, 0, 0
	}

	vs, err := NewChromemStore(indexDir)
	if err != nil {
		logger.Printf("retrieval disabled: opening vector store: %v", err)
		return nil, nil, nil, noop, 0, 0
	}

	e, stopEmbedder, err := newEmbedder(logger)
	if err != nil {
		logger.Printf("retrieval disabled: starting embedder: %v", err)
		return nil, nil, nil, noop, 0, 0
	}

	if err := checkEmbedderStamp(indexDir, e, vs.Count() == 0); err != nil {
		logger.Printf("retrieval disabled: %v", err)
		stopEmbedder()
		return nil, nil, nil, noop, 0, 0
	}

	// Opened last and independently: a lexical-index failure here must never
	// undo the semantic retrieval already confirmed usable above.
	var ls LexicalStore
	if fts, ftsErr := NewFTSChunkStore(indexDir); ftsErr != nil {
		logger.Printf("lexical retrieval tier disabled: opening lexical index: %v", ftsErr)
	} else {
		ls = fts
	}

	stop = func() {
		stopEmbedder()
		if ls != nil {
			if err := ls.Close(); err != nil {
				logger.Printf("closing lexical index: %v", err)
			}
		}
	}

	logger.Printf("retrieval enabled: workspace=%s top_k=%d context_budget_chars=%d lexical=%t",
		absWorkspace, cfg.Retrieval.resolvedTopK(), cfg.Retrieval.resolvedContextBudgetChars(), ls != nil)

	return e, vs, ls, stop, cfg.Retrieval.resolvedTopK(), cfg.Retrieval.resolvedContextBudgetChars()
}
