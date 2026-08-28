package main

import (
	"log"
	"os"
	"path/filepath"
)

// Client-facing reasons retrieval is unavailable (Fix 8). Every one of these
// used to collapse into a single "retrieval disabled (no embedder/index
// configured for this daemon)" on the wire, which was actively misleading: the
// most common cause by far was the helper binary not being found because the
// daemon had been launched from somewhere other than the repo root, and the
// message blamed the index, which was fine.
//
// These strings travel to clients, so they carry NO paths, hosts, or internal
// detail — that stays in the daemon log, which keeps the full diagnostic
// including the absolute index directory. Same split as the Gate-7 scrub: the
// client learns WHAT is wrong and what to do, never WHERE anything lives.
const (
	reasonNoContextFlag         = "retrieval disabled for this daemon (--no-context)"
	reasonDisabledInConfig      = "retrieval disabled for this daemon (retrieval.disabled in config)"
	reasonWorkspaceUnresolvable = "retrieval unavailable: this daemon's workspace path could not be resolved"
	reasonNoIndex               = "retrieval unavailable: no index has been built for this workspace yet (run `index` to enable grounded answers)"
	reasonIndexUnreadable       = "retrieval unavailable: the workspace index exists but could not be opened"
	reasonEmbedderUnavailable   = "retrieval unavailable: the embedding helper could not be started (check that `download-model` has been run and the helper binary is built)"
	// Covers BOTH stamp mismatches, and says so. It used to name only the
	// embedding model, which became a lie when checkEmbedderStamp started
	// refusing a chunk-BOUNDARY mismatch too -- it would have told a user their
	// model changed when their chunker had. The precise cause is in the log line
	// immediately above the caller; this is the one-line status a client shows.
	reasonIndexModelMismatch = "retrieval unavailable: the index was built by a different embedder or chunker and needs rebuilding (run `index`)"
)

// retrievalSetup is everything the live prompt path needs for retrieval, plus
// an honest account of what is missing when it is unavailable. It replaced a
// six-value positional return specifically so DisabledReason could not be the
// thing a caller forgets to thread through.
type retrievalSetup struct {
	Embedder           Embedder
	Store              VectorStore
	LexicalStore       LexicalStore
	Stop               func()
	TopK               int
	ContextBudgetChars int
	// DisabledReason is the specific, client-safe explanation of why the
	// semantic tier is unavailable, and "" when retrieval is working.
	DisabledReason string
}

// disabledRetrieval builds the "no retrieval, and here is exactly why" result.
func disabledRetrieval(reason string) retrievalSetup {
	return retrievalSetup{Stop: func() {}, DisabledReason: reason}
}

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
// Each of those cases also sets a SPECIFIC DisabledReason (Fix 8) rather than
// leaving the caller to invent one; see the reason constants above for why
// that mattered.
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
) retrievalSetup {
	if disabled {
		logger.Print("retrieval disabled via --no-context")
		return disabledRetrieval(reasonNoContextFlag)
	}
	if cfg.Retrieval.Disabled {
		logger.Print("retrieval disabled via config (retrieval.disabled=true in models.json)")
		return disabledRetrieval(reasonDisabledInConfig)
	}

	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		logger.Printf("retrieval disabled: resolving workspace %s: %v", workspace, err)
		return disabledRetrieval(reasonWorkspaceUnresolvable)
	}

	indexDir := filepath.Join(absWorkspace, indexDirName)
	if _, err := os.Stat(indexDir); err != nil {
		logger.Printf("retrieval disabled: no index found at %s (run `index %s` to enable retrieval-augmented answers)", indexDir, workspace)
		return disabledRetrieval(reasonNoIndex)
	}

	vs, err := NewChromemStore(indexDir)
	if err != nil {
		logger.Printf("retrieval disabled: opening vector store: %v", err)
		return disabledRetrieval(reasonIndexUnreadable)
	}

	e, stopEmbedder, err := newEmbedder(logger)
	if err != nil {
		logger.Printf("retrieval disabled: starting embedder: %v", err)
		return disabledRetrieval(reasonEmbedderUnavailable)
	}

	if err := checkEmbedderStamp(indexDir, e, vs.Count() == 0); err != nil {
		logger.Printf("retrieval disabled: %v", err)
		stopEmbedder()
		return disabledRetrieval(reasonIndexModelMismatch)
	}

	// Opened last and independently: a lexical-index failure here must never
	// undo the semantic retrieval already confirmed usable above.
	var ls LexicalStore
	if fts, ftsErr := NewFTSChunkStore(indexDir); ftsErr != nil {
		logger.Printf("lexical retrieval tier disabled: opening lexical index: %v", ftsErr)
	} else {
		ls = fts
	}

	stop := func() {
		stopEmbedder()
		if ls != nil {
			if err := ls.Close(); err != nil {
				logger.Printf("closing lexical index: %v", err)
			}
		}
	}

	logger.Printf("retrieval enabled: workspace=%s top_k=%d context_budget_chars=%d lexical=%t",
		absWorkspace, cfg.Retrieval.resolvedTopK(), cfg.Retrieval.resolvedContextBudgetChars(), ls != nil)

	return retrievalSetup{
		Embedder:           e,
		Store:              vs,
		LexicalStore:       ls,
		Stop:               stop,
		TopK:               cfg.Retrieval.resolvedTopK(),
		ContextBudgetChars: cfg.Retrieval.resolvedContextBudgetChars(),
	}
}
