//go:build eval

// The whole-repository index the eval suite scores against, built ONCE per
// `go test` process instead of once per test.
//
// WHY THIS FILE EXISTS. Seven eval tests index this repository. Each one used
// to scan it, embed every chunk with the real BGE model, and upsert the result
// into its own chromem + FTS5 pair -- and the embedding is ~99% of the work,
// because a run embeds ~4,700 chunks to answer between 20 and 98 queries.
// Running two of those tests therefore paid for the same 4,700 embeddings
// twice, for byte-identical vectors: embedding is deterministic given the
// model and the text, so the second build could not have produced anything the
// first had not already computed.
//
// This mattered operationally on 2026-08-30, when the scheduled eval job took
// 37m43s and the cost was initially attributed to a change in
// defaultContextBudgetChars. It was not the budget -- the goroutine dump from a
// timed-out run showed the time inside embedder.Embed during INDEX BUILD, a
// phase that takes no budget input at all. The suite was simply spending
// almost all of its wall clock re-deriving a corpus it already had, twice, on a
// runner that happened to be slow that hour. Halving the number of builds does
// not make the runner faster; it halves the exposure.
//
// WHAT IT DELIBERATELY DOES NOT DO. It does not cache anything across
// processes or across CI runs. That is a real and much larger win -- see
// BACKLOG.md -- but it turns a wrong cache key into a silently wrong
// measurement, which is the failure class this whole eval directory exists to
// prevent. In-process sharing has no such failure mode: one build, one set of
// vectors, in one process, discarded at exit.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// evalCorpus is one built index over this repository, plus the running
// embedder that built it and the scan it came from.
//
// Consumers must treat every field as READ-ONLY. Nothing here is copied per
// test, so a test that upserted into Store would be editing the corpus every
// later test is scored against -- and it would do so invisibly, because the
// eval's own output would still look like a valid run.
//
// That is not left to good intentions: sharedEvalCorpus re-checks the store's
// document count on every call after the first, so a consumer that upserted
// would be caught by the NEXT consumer to ask for the corpus, whatever order
// the tests happen to run in.
type evalCorpus struct {
	RepoRoot     string
	Embedder     Embedder
	Store        VectorStore
	LexicalStore LexicalStore
	Scan         *ScanResult

	// Phase timings, printed on build and kept so a test can report them.
	// These exist because their ABSENCE is what made the 2026-08-30 slowdown
	// un-diagnosable: `go test` without -v prints nothing for a passing
	// package, so a 38-minute job emitted one line, and which phase owned the
	// 38 minutes had to be recovered from a panic dump in a different run.
	ScanTime  time.Duration
	EmbedTime time.Duration
	Chunks    int

	// VectorFingerprint is sha256 over every vector in this index, in build
	// order. It answers, in ONE line of a CI log, the question that took a
	// download of two full run logs and a 995-line diff to answer on
	// 2026-08-30: did these two runners compute the same vectors?
	//
	// It is a diagnostic, not a gate. Vectors are ALLOWED to differ between
	// machines -- that is the open finding this instruments, not a failure --
	// so nothing asserts on this value. It exists so the next divergence is
	// one grep instead of an afternoon.
	VectorFingerprint string

	// storeCount is the vector store's document count as the build left it,
	// used to detect a consumer mutating the shared index.
	storeCount int
}

var (
	sharedEvalCorpusOnce sync.Once
	sharedEvalCorpusVal  *evalCorpus
	sharedEvalCorpusErr  error
)

// sharedEvalCorpus returns the process-wide corpus, building it on first call.
//
// It takes a *testing.T only to fail the calling test, never to own anything:
// every resource it allocates is registered with registerProcessCleanup, so
// the corpus survives the test that happened to ask for it first. Using
// t.TempDir or t.Cleanup here would tear the index down between the first
// consumer and the second, which is the entire thing this fixture exists to
// avoid.
func sharedEvalCorpus(t *testing.T) *evalCorpus {
	t.Helper()
	sharedEvalCorpusOnce.Do(func() {
		sharedEvalCorpusVal, sharedEvalCorpusErr = buildSharedEvalCorpus()
	})
	if sharedEvalCorpusErr != nil {
		t.Fatalf("building the shared eval corpus: %v", sharedEvalCorpusErr)
	}

	// THE READ-ONLY GUARD. Every consumer after the first re-checks that the
	// corpus is the one that was built. A test that upserted into the shared
	// store would otherwise silently change what every later eval is scored
	// against, and the later eval would still print a plausible-looking
	// number -- the silent-wrong-measurement failure this directory exists to
	// prevent. Checking here rather than in a dedicated test means it holds
	// for any test ORDER, and needs nobody to remember to call it.
	if got := sharedEvalCorpusVal.Store.Count(); got != sharedEvalCorpusVal.storeCount {
		t.Fatalf("the shared eval corpus has been MUTATED: vector store holds %d documents, "+
			"was %d when it was built. Some earlier test upserted into it. The corpus is "+
			"read-only: every eval in this package is scored against these exact vectors",
			got, sharedEvalCorpusVal.storeCount)
	}
	return sharedEvalCorpusVal
}

// buildSharedEvalCorpus does the work: real model, real helper subprocess,
// whole repository scanned, self-referential chunks dropped, everything else
// embedded and upserted into both tiers.
//
// It returns an error rather than taking a *testing.T because it must not be
// bound to any one test's lifetime.
func buildSharedEvalCorpus() (*evalCorpus, error) {
	logger := log.New(os.Stderr, "eval-corpus: ", log.LstdFlags)
	ctx := context.Background()
	started := time.Now()

	tmpDir, err := os.MkdirTemp("", "eval-corpus")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	registerProcessCleanup(func() { _ = os.RemoveAll(tmpDir) })

	modelCacheDir, err := defaultModelCacheDir()
	if err != nil {
		return nil, fmt.Errorf("defaultModelCacheDir: %w", err)
	}
	modelDir, err := EnsureModelFiles(ctx, modelCacheDir, bgeModelAssets, logger)
	if err != nil {
		return nil, fmt.Errorf("EnsureModelFiles (downloads only if not already cached): %w", err)
	}
	ortCacheDir, err := defaultONNXRuntimeCacheDir()
	if err != nil {
		return nil, fmt.Errorf("defaultONNXRuntimeCacheDir: %w", err)
	}
	onnxRuntimeLib, err := EnsureONNXRuntimeLib(ctx, ortCacheDir, logger)
	if err != nil {
		return nil, fmt.Errorf("EnsureONNXRuntimeLib (downloads only if not already cached): %w", err)
	}

	// The real helper module, compiled fresh -- same reason
	// buildRealHelperBinary gives: a prebuilt binary can be stale, and a stale
	// embedder scores the wrong product.
	helperBin := filepath.Join(tmpDir, exeName("mochiii-embedder-helper"))
	build := exec.Command("go", "build", "-o", helperBin, ".")
	build.Dir = "../helper"
	if out, err := build.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("building real helper binary: %w\n%s", err, out)
	}

	helper := NewHelperProcess(helperBin, modelDir, onnxRuntimeLib, logger)
	if err := helper.Start(); err != nil {
		return nil, fmt.Errorf("starting real embedder helper: %w", err)
	}
	registerProcessCleanup(func() { _ = helper.Stop() })
	embedder := NewBgeEmbedder(helper)

	repoRoot, err := filepath.Abs("..")
	if err != nil {
		return nil, fmt.Errorf("resolving repo root: %w", err)
	}
	indexDir := filepath.Join(tmpDir, "index")
	store, err := NewChromemStore(indexDir)
	if err != nil {
		return nil, fmt.Errorf("NewChromemStore: %w", err)
	}
	lexical, err := NewFTSChunkStore(indexDir)
	if err != nil {
		return nil, fmt.Errorf("NewFTSChunkStore: %w", err)
	}
	registerProcessCleanup(func() { _ = lexical.Close() })

	scanStart := time.Now()
	scan, err := ScanWorkspace(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("scanning workspace: %w", err)
	}
	filtered := scan.Chunks[:0]
	for _, c := range scan.Chunks {
		if !evalSelfReferenceFiles[c.FilePath] && !evalInstrumentFiles[c.FilePath] {
			filtered = append(filtered, c)
		}
	}
	scan.Chunks = filtered
	scanTime := time.Since(scanStart)
	logger.Printf("scanned %s: files=%d chunks=%d in %s (self-referential and instrument chunks excluded)",
		repoRoot, scan.FilesScanned, len(scan.Chunks), scanTime.Round(time.Millisecond))

	embedStart := time.Now()
	batches := 0
	fingerprint := sha256.New()
	for start := 0; start < len(scan.Chunks); start += indexEmbedBatchSize {
		end := min(start+indexEmbedBatchSize, len(scan.Chunks))
		batch := scan.Chunks[start:end]

		// embedTextsFor, not a .Content loop: embedding raw Content here would
		// measure a representation production no longer uses -- the instrument
		// silently grading the wrong product.
		vecs, err := embedder.Embed(ctx, embedTextsFor(batch))
		if err != nil {
			return nil, fmt.Errorf("embedding batch [%d:%d]: %w", start, end, err)
		}
		for i := range batch {
			batch[i].Vector = vecs[i]
		}
		// Hashed in build order, which is scan order, which is stable for a
		// given tree -- so two runs over the same commit are comparable and a
		// reordering would show up as a difference rather than hiding.
		writeVectorsToHash(fingerprint, vecs)
		if err := store.Upsert(ctx, batch); err != nil {
			return nil, fmt.Errorf("upserting batch [%d:%d]: %w", start, end, err)
		}
		if err := lexical.Upsert(ctx, batch); err != nil {
			return nil, fmt.Errorf("upserting lexical batch [%d:%d]: %w", start, end, err)
		}
		batches++
	}
	embedTime := time.Since(embedStart)

	// THE LINE THAT MAKES THE NEXT SLOWDOWN DIAGNOSABLE. Printed unconditionally
	// on stderr with a timestamp, so it survives in a CI log without anyone
	// having to have remembered to pass -v.
	logger.Printf("index built: chunks=%d batches=%d scan=%s embed=%s total=%s (embed is %.0f%% of build)",
		len(scan.Chunks), batches,
		scanTime.Round(time.Millisecond),
		embedTime.Round(time.Millisecond),
		time.Since(started).Round(time.Millisecond),
		100*embedTime.Seconds()/time.Since(started).Seconds())

	vectorFingerprint := hex.EncodeToString(fingerprint.Sum(nil))
	// Same reasoning as the line above: unconditional, timestamped, and on
	// stderr, so two CI runs can be compared without -v and without anyone
	// having planned in advance to compare them.
	logger.Printf("vector fingerprint: %s (sha256 over %d vectors in build order)",
		vectorFingerprint, len(scan.Chunks))

	return &evalCorpus{
		RepoRoot:          repoRoot,
		storeCount:        store.Count(),
		Embedder:          embedder,
		Store:             store,
		LexicalStore:      lexical,
		Scan:              scan,
		ScanTime:          scanTime,
		EmbedTime:         embedTime,
		Chunks:            len(scan.Chunks),
		VectorFingerprint: vectorFingerprint,
	}, nil
}

// evalInstrumentFiles are files the eval WRITES ABOUT ITSELF, held out of its own
// corpus.
//
// WHY THIS IS A SECOND MAP AND NOT MORE ENTRIES IN evalSelfReferenceFiles.
// That map means one specific thing -- "this file contains an eval query
// verbatim, so indexing it hands the eval its own answer key" -- and
// TestNoIndexedFileEchoesAnEvalQuery is the scan that DISCOVERS its members.
// A membership test that finds entries is a different kind of thing from a
// membership list that is maintained, and merging them would leave a set whose
// guard passes for half its contents and says nothing about the other half.
//
// The reason here is unrelated and does not involve leakage at all:
// RETRIEVAL_EVAL_TREND.md is a record of this eval's own output, appended to
// after every checkpoint, and this eval measures recall against a fixed
// character budget over a corpus that IS this repository. A file that grows
// every time the instrument is read is the instrument perturbing its own
// subject -- small, but in exactly the direction that makes the numbers worse,
// and dishonest in a way that compounds.
//
// It is deliberately not an extension rule or a directory rule. A file earns a
// place here by being about the measurement rather than about the product, and
// that is a judgement, so it is a list.
var evalInstrumentFiles = map[string]bool{
	"docs/RETRIEVAL_EVAL_TREND.md": true,
}

// TestSharedCorpusExclusionsAreNotAnswerFiles is the guard on the decision to
// merge two exclusion sets into one.
//
// Before the shared corpus, TestRerankEvalRetrievalRanking held six files out
// of the index and TestTokenEfficiencyEval held out one. One index can only
// have one exclusion set, and the union is the safe direction: holding a file
// OUT can only cost recall, never manufacture it, so a merged set cannot
// flatter either eval by admitting its own answer key.
//
// What the union CAN do is delete a file that some query legitimately expects
// as an answer, which would make that query unanswerable and look exactly like
// a retrieval regression. That is what this test forbids. It runs offline in
// microseconds, so it fails long before the fifteen-minute eval does.
func TestSharedCorpusExclusionsAreNotAnswerFiles(t *testing.T) {
	answers := map[string][]string{}
	for _, q := range rerankEvalQueries {
		for _, f := range q.expectedFiles {
			answers[f] = append(answers[f], "rerank: "+q.query)
		}
	}
	for _, q := range tokenEffQueries {
		for _, f := range q.expectedFiles {
			answers[f] = append(answers[f], "token-efficiency: "+q.query)
		}
	}

	// Anti-vacuity: if the query sets ever stop parsing into this map, an empty
	// map would make every exclusion look fine.
	if len(answers) < 10 {
		t.Fatalf("only %d answer files collected from the two query sets — this check would "+
			"pass vacuously. Expected dozens", len(answers))
	}

	// BOTH exclusion sets, because the hazard is a property of being excluded
	// and has nothing to do with WHY. A file held out as an instrument is just
	// as unretrievable as one held out as an answer-key leak.
	for _, set := range []map[string]bool{evalSelfReferenceFiles, evalInstrumentFiles} {
		for excluded := range set {
			if qs := answers[excluded]; len(qs) > 0 {
				t.Errorf("%s is excluded from the shared eval corpus AND is a declared answer file "+
					"for %d quer(y/ies): %v. An excluded file cannot be retrieved, so those queries "+
					"can never pass and the failure would read as a retrieval regression. Either "+
					"drop the exclusion (and reword the file so it does not quote its own query) or "+
					"drop the query", excluded, len(qs), qs)
			}
		}
	}
}
