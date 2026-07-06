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
func setupRetrieval(
	cfg *Config,
	workspace string,
	disabled bool,
	logger *log.Logger,
	newEmbedder func(*log.Logger) (Embedder, func(), error),
) (embedder Embedder, store VectorStore, stop func(), topK int, contextBudgetChars int) {
	noop := func() {}

	if disabled {
		logger.Print("retrieval disabled via --no-context")
		return nil, nil, noop, 0, 0
	}
	if cfg.Retrieval.Disabled {
		logger.Print("retrieval disabled via config (retrieval.disabled=true in models.json)")
		return nil, nil, noop, 0, 0
	}

	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		logger.Printf("retrieval disabled: resolving workspace %s: %v", workspace, err)
		return nil, nil, noop, 0, 0
	}

	indexDir := filepath.Join(absWorkspace, indexDirName)
	if _, err := os.Stat(indexDir); err != nil {
		logger.Printf("retrieval disabled: no index found at %s (run `index %s` to enable retrieval-augmented answers)", indexDir, workspace)
		return nil, nil, noop, 0, 0
	}

	vs, err := NewChromemStore(indexDir)
	if err != nil {
		logger.Printf("retrieval disabled: opening vector store: %v", err)
		return nil, nil, noop, 0, 0
	}

	e, stopEmbedder, err := newEmbedder(logger)
	if err != nil {
		logger.Printf("retrieval disabled: starting embedder: %v", err)
		return nil, nil, noop, 0, 0
	}

	if err := checkEmbedderStamp(indexDir, e, vs.Count() == 0); err != nil {
		logger.Printf("retrieval disabled: %v", err)
		stopEmbedder()
		return nil, nil, noop, 0, 0
	}

	logger.Printf("retrieval enabled: workspace=%s top_k=%d context_budget_chars=%d",
		absWorkspace, cfg.Retrieval.resolvedTopK(), cfg.Retrieval.resolvedContextBudgetChars())

	return e, vs, stopEmbedder, cfg.Retrieval.resolvedTopK(), cfg.Retrieval.resolvedContextBudgetChars()
}
