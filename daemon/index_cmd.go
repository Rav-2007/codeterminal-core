package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// indexDirName is where a workspace's index lives, relative to its root.
const indexDirName = ".codeterminal/index"

// gitignoreEntry is appended to a workspace's .gitignore so the index is
// never committed.
const gitignoreEntry = ".codeterminal/"

// defaultK is how many chunks "retrieve" returns when --k isn't given.
const defaultK = 5

// indexEmbedBatchSize caps how many chunks go into a single Embed() RPC call
// to the embedder helper. The full CodeTerminal repo is ~334 chunks;
// embedding them all in one call exceeds the helper's fixed
// defaultHelperCallTimeout (helperproc.go) — a batch of 40 was proven safe
// against the real repo and the real helper by the rerank eval test before
// this constant existed in production code. Batching, not raising the
// timeout, is the fix: each batch gets its own fresh defaultHelperCallTimeout
// window (see HelperProcess.call in helperproc.go), so this stays comfortably
// under it regardless of overall repo size.
const indexEmbedBatchSize = 40

// buildIndex scans root, embeds every chunk found via embedder, and upserts
// them into store, in batches of indexEmbedBatchSize so a large workspace's
// chunk set never exceeds a single embedding call's timeout. It takes both
// dependencies as interfaces so a test can inject a fake Embedder and/or
// VectorStore without touching a real model or disk-backed store.
//
// If a batch fails, buildIndex returns immediately with an error identifying
// which batch failed; chunks from batches that already succeeded remain
// durably upserted in store. That partial state is intentionally not treated
// as a valid index by the rest of the system — see indexWorkspace, which
// wraps this with embedder-stamp handling so a partial store is always
// refused by checkEmbedderStamp rather than silently trusted.
func buildIndex(ctx context.Context, root string, embedder Embedder, store VectorStore, logger *log.Logger) (*ScanResult, error) {
	scan, err := ScanWorkspace(root)
	if err != nil {
		return nil, fmt.Errorf("scanning workspace: %w", err)
	}
	if len(scan.Chunks) == 0 {
		return scan, nil
	}

	totalBatches := (len(scan.Chunks) + indexEmbedBatchSize - 1) / indexEmbedBatchSize
	for start := 0; start < len(scan.Chunks); start += indexEmbedBatchSize {
		end := min(start+indexEmbedBatchSize, len(scan.Chunks))
		batch := scan.Chunks[start:end]
		batchNum := start/indexEmbedBatchSize + 1

		logger.Printf("index: embedding batch %d/%d (chunks %d-%d)", batchNum, totalBatches, start, end-1)

		texts := make([]string, len(batch))
		for i, c := range batch {
			texts[i] = c.Content
		}
		vecs, err := embedder.Embed(ctx, texts)
		if err != nil {
			return nil, fmt.Errorf("embedding batch %d/%d (chunks [%d:%d]): %w", batchNum, totalBatches, start, end, err)
		}
		for i := range batch {
			batch[i].Vector = vecs[i]
		}

		if err := store.Upsert(ctx, batch); err != nil {
			return nil, fmt.Errorf("upserting batch %d/%d (chunks [%d:%d]): %w", batchNum, totalBatches, start, end, err)
		}
	}
	return scan, nil
}

// indexWorkspace builds a fresh index for root into indexDir, wrapping
// buildIndex with the embedder-stamp lifecycle. It removes any existing
// stamp before indexing starts and only (re)writes one after every batch has
// embedded and upserted successfully — so a batch failure partway through,
// whether on a first index or a re-index, always leaves indexDir stamp-less
// and therefore refused by checkEmbedderStamp, instead of quietly passing
// under a stamp left over from an earlier, unrelated successful run.
func indexWorkspace(ctx context.Context, indexDir, root string, embedder Embedder, store VectorStore, logger *log.Logger) (*ScanResult, error) {
	_ = os.Remove(filepath.Join(indexDir, embedderStampFileName))

	scan, err := buildIndex(ctx, root, embedder, store, logger)
	if err != nil {
		return nil, err
	}

	if err := writeEmbedderStamp(indexDir, embedder); err != nil {
		return nil, fmt.Errorf("writing embedder stamp: %w", err)
	}
	return scan, nil
}

// retrieveTopK embeds query via embedder and returns the k nearest chunks
// from store. Like buildIndex, both dependencies are interfaces so tests can
// inject fakes. It calls EmbedQuery, not Embed — this is the one line where
// the query/document asymmetry actually gets applied (see BgeEmbedder).
//
// When rerank is true, it fetches a wider raw candidate pool
// (rerankPoolSize, rerank.go) than k, reweights it by file-class (a code
// chunk ranked just outside the raw top-k gets a chance to win after its
// class boost), and truncates the reweighted result to k. When rerank is
// false, it fetches exactly k and returns the store's raw ranking unchanged
// — the A/B path for comparing against re-ranking.
func retrieveTopK(ctx context.Context, query string, k int, embedder Embedder, store VectorStore, rerank bool) ([]Chunk, error) {
	vecs, err := embedder.EmbedQuery(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}

	if !rerank {
		hits, err := store.Query(ctx, vecs[0], k)
		if err != nil {
			return nil, err
		}
		// RawScore mirrors Score here (no reweighting happened) so logging
		// can treat RawScore/Score identically regardless of which path ran.
		for i := range hits {
			hits[i].RawScore = hits[i].Score
		}
		return hits, nil
	}

	candidates, err := store.Query(ctx, vecs[0], rerankPoolSize(k))
	if err != nil {
		return nil, err
	}
	return rerankChunks(candidates, k), nil
}

// runIndexCommand implements `codeterminal-daemon index [path]`. It builds
// (or replaces) the index for the workspace rooted at path (default ".")
// and logs a scan summary. This only runs when explicitly invoked — never
// on daemon start, never per-prompt.
func runIndexCommand(args []string, logger *log.Logger) error {
	fset := flag.NewFlagSet("index", flag.ExitOnError)
	fset.Parse(args)

	root := "."
	if fset.NArg() > 0 {
		root = fset.Arg(0)
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolving %s: %w", root, err)
	}

	indexDir := filepath.Join(absRoot, indexDirName)
	store, err := NewChromemStore(indexDir)
	if err != nil {
		return fmt.Errorf("opening vector store: %w", err)
	}

	embedder, stopEmbedder, err := newActiveEmbedder(logger)
	if err != nil {
		return err
	}
	defer stopEmbedder()

	scan, err := indexWorkspace(context.Background(), indexDir, absRoot, embedder, store, logger)
	if err != nil {
		return err
	}

	if err := ensureGitignoreEntry(absRoot, gitignoreEntry); err != nil {
		return fmt.Errorf("updating .gitignore: %w", err)
	}

	logger.Printf("index: root=%s scanned=%d chunks=%d", absRoot, scan.FilesScanned, len(scan.Chunks))
	logger.Printf("index: skipped: %s", formatSkipCounts(scan.Skipped))
	logger.Printf("index: written to %s", indexDir)
	return nil
}

// runRetrieveCommand implements
// `codeterminal-daemon retrieve [--workspace path] [--k n] <query...>`. It
// embeds query with the active embedder (see newActiveEmbedder) and logs
// the top-k hits. It does not feed results into any model prompt — that
// wire-in is a later step.
func runRetrieveCommand(args []string, logger *log.Logger) error {
	fset := flag.NewFlagSet("retrieve", flag.ExitOnError)
	workspace := fset.String("workspace", ".", "workspace root containing an existing .codeterminal/index")
	k := fset.Int("k", defaultK, "number of results to return")
	raw := fset.Bool("raw", false, "bypass file-class re-ranking and show raw vector-similarity order (A/B comparison)")
	fset.Parse(args)

	if fset.NArg() < 1 {
		return fmt.Errorf("usage: retrieve [--workspace path] [--k n] <query...> (flags must come before the query)")
	}
	query := strings.Join(fset.Args(), " ")

	absRoot, err := filepath.Abs(*workspace)
	if err != nil {
		return fmt.Errorf("resolving %s: %w", *workspace, err)
	}

	indexDir := filepath.Join(absRoot, indexDirName)
	if _, err := os.Stat(indexDir); err != nil {
		return fmt.Errorf("no index found at %s; run `index %s` first", indexDir, *workspace)
	}

	store, err := NewChromemStore(indexDir)
	if err != nil {
		return fmt.Errorf("opening vector store: %w", err)
	}

	embedder, stopEmbedder, err := newActiveEmbedder(logger)
	if err != nil {
		return err
	}
	defer stopEmbedder()

	if err := checkEmbedderStamp(indexDir, embedder, store.Count() == 0); err != nil {
		return err
	}

	results, err := retrieveTopK(context.Background(), query, *k, embedder, store, !*raw)
	if err != nil {
		return err
	}

	logger.Printf("retrieve: query=%q k=%d rerank=%t results=%d", query, *k, !*raw, len(results))
	for i, r := range results {
		logger.Printf("  %d. %s:%d-%d class=%s score=%.4f weighted=%.4f", i+1, r.FilePath, r.StartLine, r.EndLine, r.Class, r.RawScore, r.Score)
	}
	return nil
}

// formatSkipCounts renders skip counts in a fixed, readable order so log
// output doesn't jitter between runs (map iteration order is random).
func formatSkipCounts(skipped map[SkipReason]int) string {
	order := []SkipReason{SkipSecret, SkipNoise, SkipBinary, SkipTooLarge, SkipSymlink, SkipIgnoredDir, SkipGitignore}

	var parts []string
	for _, reason := range order {
		if n, ok := skipped[reason]; ok {
			parts = append(parts, fmt.Sprintf("%s=%d", reason, n))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " ")
}

// ensureGitignoreEntry appends entry to root's .gitignore as its own line,
// unless it's already present. Idempotent: running this repeatedly does not
// duplicate the line. Creates .gitignore if it doesn't exist.
func ensureGitignoreEntry(root, entry string) error {
	path := filepath.Join(root, ".gitignore")

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil
		}
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	prefix := ""
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		prefix = "\n"
	}
	_, err = f.WriteString(prefix + entry + "\n")
	return err
}
