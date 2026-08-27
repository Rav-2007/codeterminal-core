package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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
// them into store (and, when non-nil, lexicalStore) in batches of
// indexEmbedBatchSize so a large workspace's chunk set never exceeds a
// single embedding call's timeout. It takes embedder/store as interfaces so
// a test can inject a fake Embedder and/or VectorStore without touching a
// real model or disk-backed store. lexicalStore may be nil (e.g. it failed
// to open) — buildIndex then simply skips the lexical upsert, populating
// only the vector store, exactly as it did before the lexical tier existed.
//
// If a batch fails (either store), buildIndex returns immediately with an
// error identifying which batch failed; chunks from batches that already
// succeeded remain durably upserted. That partial state is intentionally not
// treated as a valid index by the rest of the system — see indexWorkspace,
// which wraps this with embedder-stamp handling so a partial store is always
// refused by checkEmbedderStamp rather than silently trusted.
func buildIndex(ctx context.Context, root string, embedder Embedder, store VectorStore, lexicalStore LexicalStore, logger *log.Logger, reuse bool) (*ScanResult, error) {
	scan, err := ScanWorkspace(root)
	if err != nil {
		return nil, fmt.Errorf("scanning workspace: %w", err)
	}
	if scan.LimitExceeded || len(scan.Chunks) == 0 {
		return scan, nil
	}

	reused, inSync := carryOverUnchanged(ctx, scan.Chunks, store, lexicalStore, reuse)
	if reused > 0 {
		logger.Printf("index: %d/%d chunk(s) are unchanged since the last build and keep their existing embedding (%d already correct in both stores)",
			reused, len(scan.Chunks), countTrue(inSync))
	}

	// The batches are formed over the chunks that still NEED embedding, not over
	// every chunk, which is the whole point: an unchanged chunk must not occupy a
	// slot in an embedding call. Re-indexing a repository where three files moved
	// then costs three files' worth of model time instead of the whole tree's.
	pending := make([]int, 0, len(scan.Chunks))
	for i := range scan.Chunks {
		if scan.Chunks[i].Vector == nil {
			pending = append(pending, i)
		}
	}

	// A carried-over vector still has to be WRITTEN unless BOTH stores already
	// hold it correctly -- its class metadata may have moved even though its text
	// did not, and the lexical store may simply not have the row.
	//
	// Skipping the ones that are already right is not a micro-optimisation. A
	// re-upsert deletes and re-inserts a trigram-tokenised FTS row, which is the
	// single most expensive write in this file: MEASURED at 515µs/chunk against
	// 132µs for the vector store and 1µs for the reuse lookup itself. Rewriting
	// 3,000 rows that were already correct cost ~1.9s of a ~2s rebuild.
	for start := 0; start < len(scan.Chunks); start += indexEmbedBatchSize {
		end := min(start+indexEmbedBatchSize, len(scan.Chunks))
		batch := carriedNeedingWrite(scan.Chunks[start:end], inSync[start:end])
		if len(batch) == 0 {
			continue
		}
		if err := upsertBatch(ctx, batch, store, lexicalStore); err != nil {
			return nil, fmt.Errorf("upserting carried-over chunks [%d:%d]: %w", start, end, err)
		}
	}

	// EMBED THEN UPSERT, ONE BATCH AT A TIME, and the interleaving is deliberate
	// rather than tidier-looking than the alternative.
	//
	// Doing every embed first and every upsert afterwards reads better and is
	// wrong: a failure in the last batch would then discard every vector the
	// preceding batches had paid the model for, so a run that died at 80/83 would
	// re-embed all 83 next time. Upserting each batch as it lands means a failed
	// run leaves its completed work durable, and -- now that carryOverUnchanged
	// exists -- the retry picks that work straight back up instead of buying it
	// twice. The partial index is still refused by checkEmbedderStamp, because
	// indexWorkspace writes no stamp on a failure; durable is not the same as
	// trusted.
	totalBatches := (len(pending) + indexEmbedBatchSize - 1) / indexEmbedBatchSize
	for start := 0; start < len(pending); start += indexEmbedBatchSize {
		end := min(start+indexEmbedBatchSize, len(pending))
		idxs := pending[start:end]
		batchNum := start/indexEmbedBatchSize + 1

		logger.Printf("index: embedding batch %d/%d (%d chunk(s))", batchNum, totalBatches, len(idxs))

		texts := make([]string, len(idxs))
		for i, ci := range idxs {
			texts[i] = scan.Chunks[ci].Content
		}
		vecs, err := embedder.Embed(ctx, texts)
		if err != nil {
			return nil, fmt.Errorf("embedding batch %d/%d (%d chunk(s)): %w", batchNum, totalBatches, len(idxs), err)
		}
		if len(vecs) != len(idxs) {
			return nil, fmt.Errorf("embedding batch %d/%d: embedder returned %d vector(s) for %d chunk(s)",
				batchNum, totalBatches, len(vecs), len(idxs))
		}

		batch := make([]Chunk, len(idxs))
		for i, ci := range idxs {
			scan.Chunks[ci].Vector = vecs[i]
			batch[i] = scan.Chunks[ci]
		}
		if err := upsertBatch(ctx, batch, store, lexicalStore); err != nil {
			return nil, fmt.Errorf("upserting batch %d/%d: %w", batchNum, totalBatches, err)
		}
	}
	return scan, nil
}

// upsertBatch writes one batch to both stores, so the two call sites above
// cannot drift in which stores they remember to update.
func upsertBatch(ctx context.Context, batch []Chunk, store VectorStore, lexicalStore LexicalStore) error {
	if err := store.Upsert(ctx, batch); err != nil {
		return err
	}
	if lexicalStore != nil {
		if err := lexicalStore.Upsert(ctx, batch); err != nil {
			return err
		}
	}
	return nil
}

// carriedNeedingWrite returns the chunks in window that already have a vector
// (carryOverUnchanged filled them in) but are NOT already correct on disk.
//
// A nil Vector is the single marker for "still needs the model", set by nothing
// else on this path; inSync[i] is the separate question of whether both stores
// already hold this chunk exactly as it is now.
func carriedNeedingWrite(window []Chunk, inSync []bool) []Chunk {
	out := make([]Chunk, 0, len(window))
	for i := range window {
		if window[i].Vector != nil && !inSync[i] {
			out = append(out, window[i])
		}
	}
	return out
}

func countTrue(flags []bool) int {
	n := 0
	for _, f := range flags {
		if f {
			n++
		}
	}
	return n
}

// vectorReader is the optional half of VectorStore that buildIndex needs to
// reuse work: given a chunk id, hand back what is already stored under it.
//
// Optional -- a type assertion rather than a method on VectorStore -- so that
// every fake store in the test suite, and any future implementation, keeps
// working unchanged and simply gets no reuse. A store that cannot answer the
// question is not broken; it just pays full price, which is what every store
// paid before this existed.
type vectorReader interface {
	Existing(ctx context.Context, id string) (Chunk, bool)
}

// lexicalHaser is the same optional shape for the lexical store: it answers
// whether a chunk id is already present, so a chunk that both stores already
// hold correctly can be skipped instead of rewritten. A store that does not
// implement it is treated as "does not have it", so every chunk is written --
// the pre-existing behaviour, and the safe direction.
type lexicalHaser interface {
	Has(ctx context.Context, id string) bool
}

// carryOverUnchanged fills in the Vector of every chunk whose stored copy is
// byte-identical, and reports how many it filled plus which of them need no
// write at all.
//
// The equality test is on CONTENT, not on the id, and not on a hash of the
// content. Ids encode line ranges and therefore collide across edits (see
// ChromemStore.Existing); a hash would be a second thing to get wrong for no
// gain, since the store already hands back the text itself and a string compare
// over a few KB is nothing beside a model call.
//
// CLASS IS COMPARED TOO, and separately from content, because it is derived from
// the path by rules that live in this binary rather than in the file: classifyFile
// can be corrected without any chunk's text changing, and a chunk carried over on
// text alone would keep a class the current rules no longer assign. Content
// decides whether the VECTOR is still valid; class decides whether the stored
// METADATA is.
//
// The returned inSync flags mark chunks that both stores already hold exactly as
// they now are, so the caller can skip rewriting them. It is deliberately AND-ed
// with the lexical store's own answer rather than inferred from the vector
// store's: the two are separate databases and a build that ran while lexical.db
// was unavailable populated only one of them.
//
// reuse=false disables it entirely, which is how the caller expresses "the
// vectors on disk were made by a different embedder and mean nothing to this
// one" -- see indexWorkspace.
func carryOverUnchanged(ctx context.Context, chunks []Chunk, store VectorStore, lexicalStore LexicalStore, reuse bool) (int, []bool) {
	inSync := make([]bool, len(chunks))
	if !reuse {
		return 0, inSync
	}
	reader, ok := store.(vectorReader)
	if !ok {
		return 0, inSync
	}
	// Three cases, and the middle one is the reason this is a closure rather than
	// an inline `&&`: with no lexical tier configured at all there is no second
	// store to keep in step, so "both stores are correct" collapses to the vector
	// store's answer. A tier that exists but cannot be asked is the opposite case
	// and must resolve to "write it".
	lexicalHasIt := func(id string) bool {
		if lexicalStore == nil {
			return true
		}
		haser, ok := lexicalStore.(lexicalHaser)
		if !ok {
			return false
		}
		return haser.Has(ctx, id)
	}

	carried := 0
	for i := range chunks {
		prior, found := reader.Existing(ctx, chunks[i].ID)
		if !found || prior.Content != chunks[i].Content {
			continue
		}
		chunks[i].Vector = prior.Vector
		carried++

		// Only now, having established the text is identical, is it worth asking
		// the cheaper questions that decide whether a write can be skipped.
		if prior.Class == chunks[i].Class && lexicalHasIt(chunks[i].ID) {
			inSync[i] = true
		}
	}
	return carried, inSync
}

// indexWorkspace builds a fresh index for root into indexDir, wrapping
// buildIndex with the embedder-stamp lifecycle. It removes any existing
// stamp before indexing starts and only (re)writes one after every batch has
// embedded and upserted successfully — so a batch failure partway through,
// whether on a first index or a re-index, always leaves indexDir stamp-less
// and therefore refused by checkEmbedderStamp, instead of quietly passing
// under a stamp left over from an earlier, unrelated successful run.
func indexWorkspace(ctx context.Context, indexDir, root string, embedder Embedder, store VectorStore, lexicalStore LexicalStore, logger *log.Logger) (*ScanResult, error) {
	// ASKED BEFORE THE STAMP IS REMOVED, and the order is the safety property.
	//
	// Carrying a vector over from the previous build is only sound if that build
	// used the same embedder and the same chunk schema, and the stamp is the only
	// record of which one it used. Removing it first -- which the line below does,
	// deliberately, so a build that dies half-way leaves an index nothing will
	// trust -- destroys the evidence. So the question is asked first, and its
	// answer travels into buildIndex.
	//
	// checkEmbedderStamp is reused rather than reimplemented: it already encodes
	// every reason a prior index is incomparable (missing stamp, corrupt stamp,
	// different embedder id, different dimensionality, older chunk schema), and a
	// second opinion on that would be a second thing to keep in sync. Any error at
	// all means full re-embed, which is exactly the pre-existing behaviour.
	reuse := checkEmbedderStamp(indexDir, embedder, false) == nil
	if !reuse {
		logger.Print("index: no comparable previous index, embedding every chunk")
	}

	_ = os.Remove(filepath.Join(indexDir, embedderStampFileName))

	scan, err := buildIndex(ctx, root, embedder, store, lexicalStore, logger, reuse)
	if err != nil {
		return nil, err
	}

	if err := writeEmbedderStamp(indexDir, embedder); err != nil {
		return nil, fmt.Errorf("writing embedder stamp: %w", err)
	}
	return scan, nil
}

// retrieveTopK embeds query via embedder and returns the k nearest chunks
// from store, optionally fused with lexicalStore's keyword/substring matches
// for the same query. Dependencies are interfaces so tests can inject fakes.
// It calls EmbedQuery, not Embed — this is the one line where the
// query/document asymmetry actually gets applied (see BgeEmbedder).
//
// When rerank is true, it fetches a wider raw semantic candidate pool
// (rerankPoolSize, rerank.go) than k, fuses it with lexicalStore's own
// candidate pool via reciprocal rank fusion (fuseRRF, rerank.go — skipped
// entirely when lexicalStore is nil, e.g. it failed to open, so retrieval
// degrades to semantic-only rather than failing), reweights the fused pool
// by file-class, and truncates to k. When rerank is false, it fetches
// exactly k from the vector store alone and returns its raw ranking
// unchanged — the A/B path for comparing against re-ranking, deliberately
// untouched by the lexical tier.
//
// A lexical search error degrades this one call to semantic-only rather
// than failing retrieval outright — the same "must never block generation"
// contract gatherContext (context.go) already applies at a higher level.
func retrieveTopK(ctx context.Context, query string, k int, embedder Embedder, store VectorStore, lexicalStore LexicalStore, rerank bool) ([]Chunk, error) {
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

	semanticCandidates, err := store.Query(ctx, vecs[0], rerankPoolSize(k))
	if err != nil {
		return nil, err
	}

	var lexicalCandidates []Chunk
	if lexicalStore != nil {
		if hits, lexErr := lexicalStore.Search(ctx, query, lexicalPoolSize(k)); lexErr == nil {
			lexicalCandidates = hits
		}
	}

	fused := fuseRRF(semanticCandidates, lexicalCandidates, rrfK)
	return rerankChunks(fused, k, query), nil
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

	lexicalStore, err := NewFTSChunkStore(indexDir)
	if err != nil {
		return fmt.Errorf("opening lexical index: %w", err)
	}
	defer lexicalStore.Close()

	embedder, stopEmbedder, err := newActiveEmbedder(logger)
	if err != nil {
		return err
	}
	defer stopEmbedder()

	start := time.Now()
	scan, err := indexWorkspace(context.Background(), indexDir, absRoot, embedder, store, lexicalStore, logger)
	if err != nil {
		return err
	}

	if scan.LimitExceeded {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		payload := fmt.Sprintf(`{"file_count": %d, "limit_exceeded": true, "time_ms": %d, "mem_mb": %d}`,
			scan.FilesScanned, time.Since(start).Milliseconds(), m.Alloc/1024/1024)
		_ = os.WriteFile(filepath.Join(indexDir, "TOO_LARGE"), []byte(payload), 0644) // TOO_LARGE metadata
		logger.Printf("index: workspace too large (> %d files), search disabled", maxFilesScanned)
		return nil
	}

	// Remove TOO_LARGE marker if we successfully indexed (e.g. after files were deleted)
	_ = os.Remove(filepath.Join(indexDir, "TOO_LARGE")) // clear previous TOO_LARGE

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

	lexicalStore, err := NewFTSChunkStore(indexDir)
	if err != nil {
		return fmt.Errorf("opening lexical index: %w", err)
	}
	defer lexicalStore.Close()

	embedder, stopEmbedder, err := newActiveEmbedder(logger)
	if err != nil {
		return err
	}
	defer stopEmbedder()

	if err := checkEmbedderStamp(indexDir, embedder, store.Count() == 0); err != nil {
		return err
	}

	results, err := retrieveTopK(context.Background(), query, *k, embedder, store, lexicalStore, !*raw)
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

	// Refuse to follow a symlinked .gitignore: appending through it would write
	// the ignore entry to a file outside the workspace root (a confirmed
	// unconfined-writer escape). A single fixed relative path with no
	// client-supplied component, so a leaf-only symlink check is sufficient
	// here — no intermediate-directory attack surface as in restoreOne. The
	// early lstat gives a consistent refusal (independent of the target's
	// contents) before the dedup read even runs; O_NOFOLLOW on the open below
	// closes the same-name check-to-write window.
	if sym, err := leafIsSymlink(path); err != nil {
		return err
	} else if sym {
		return fmt.Errorf("%s is a symlink; refusing to write the ignore entry through it", path)
	}

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == entry {
			return nil
		}
	}

	f, err := openNoFollow(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
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
