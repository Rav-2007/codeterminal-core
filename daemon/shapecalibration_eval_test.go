//go:build eval

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// Can the daemon tell, locally and before spending anything, whether a question
// needs specialists?
//
// §14 established that the right number of phases depends on the question:
// multi-hop questions win with a pipeline, single lookups win with one agent at
// a quarter of the cost. If some cheap local signal separated those two, the
// daemon could route per turn instead of making the user pick one shape for
// every question they will ever ask.
//
// THE ANSWER, FROM THIS HARNESS, IS NO -- twice, for two different mechanical
// reasons, both recorded below and in docs/MULTI_AGENT_DESIGN.md §15. This file
// is kept because it is what killed the idea, and because it is the instrument
// anyone testing a third signal should use rather than re-deriving it.
//
// THE LABELS ARE NOT JUDGEMENTS ABOUT THE QUESTIONS. Each scenario's expected
// route is taken from which arm actually won that scenario's blind pairwise
// comparison in §14 -- so the threshold is fitted to measured outcomes, not to
// how multi-hop a question reads to me.
//
// One repository, three labelled points. Enough to falsify a signal that
// inverts the labels; nowhere near enough to validate one that does not.
//
//	CALIBRATION_INDEX_DIR=/tmp/ci CALIBRATION_DUMP=/tmp/hits.json \
//	  go test ./daemon -tags eval -run TestShapeSignal -v -timeout 30m

type calibrationCase struct {
	name string
	// wantPipeline is what §14 measured winning, NOT what the question looks
	// like.
	wantPipeline bool
	prompt       string
}

var shapeCalibration = []calibrationCase{
	{
		name:         "cross_file_mechanism",
		wantPipeline: true, // §14: the pipeline won this one in both runs
		prompt: "In this repository, how does the daemon stop two different processes from applying " +
			"an edit to the same workspace at the same time? Name the specific functions and files, " +
			"and explain what happens to the second process.",
	},
	{
		name:         "trace_a_decision",
		wantPipeline: true, // §14: the pipeline won after the starvation fix
		prompt: "In this repository, when the model asks for a tool the user has not pre-approved, " +
			"trace exactly what happens: which function decides, what the user is shown, and what " +
			"happens if nobody answers. Name the files.",
	},
	{
		name:         "why_does_this_exist",
		wantPipeline: false, // §14: the single agent won, at a quarter of the cost
		prompt: "In this repository, what is truncateHandoff and what specific failure does it prevent? " +
			"Quote the numbers from its documentation if there are any.",
	},
}

func TestShapeSignalEvidenceSpread(t *testing.T) {
	logger := discardLogger()
	ctx := context.Background()

	modelCacheDir, err := defaultModelCacheDir()
	if err != nil {
		t.Fatalf("defaultModelCacheDir: %v", err)
	}
	modelDir, err := EnsureModelFiles(ctx, modelCacheDir, bgeModelAssets, logger)
	if err != nil {
		t.Fatalf("EnsureModelFiles: %v", err)
	}
	ortCacheDir, err := defaultONNXRuntimeCacheDir()
	if err != nil {
		t.Fatalf("defaultONNXRuntimeCacheDir: %v", err)
	}
	onnxRuntimeLib, err := EnsureONNXRuntimeLib(ctx, ortCacheDir, logger)
	if err != nil {
		t.Fatalf("EnsureONNXRuntimeLib: %v", err)
	}

	helper := NewHelperProcess(buildRealHelperBinary(t), modelDir, onnxRuntimeLib, logger)
	if err := helper.Start(); err != nil {
		t.Fatalf("starting embedder helper: %v", err)
	}
	defer helper.Stop()
	embedder := NewBgeEmbedder(helper)

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolving repository root: %v", err)
	}
	// A STABLE index directory, reused across runs. Indexing this repository
	// takes about three minutes, and a calibration that costs three minutes per
	// look at the data is one nobody re-runs when the signal is questioned --
	// which is exactly when it needs re-running. Override with
	// CALIBRATION_INDEX_DIR; delete the directory to force a rebuild.
	indexDir := os.Getenv("CALIBRATION_INDEX_DIR")
	if indexDir == "" {
		indexDir = filepath.Join(t.TempDir(), "index")
	}
	if err := os.MkdirAll(indexDir, 0o700); err != nil {
		t.Fatalf("creating index dir: %v", err)
	}
	store, err := NewChromemStore(indexDir)
	if err != nil {
		t.Fatalf("NewChromemStore: %v", err)
	}
	// BOTH TIERS, because a production daemon has both and the hybrid changes
	// which chunks come back -- which is the quantity being calibrated.
	lexical, err := NewFTSChunkStore(indexDir)
	if err != nil {
		t.Fatalf("NewFTSChunkStore: %v", err)
	}
	defer lexical.Close()

	scan, err := buildIndex(ctx, root, embedder, store, lexical, logger, true /* reuse */)
	if err != nil {
		t.Fatalf("indexing the repository: %v", err)
	}
	t.Logf("indexed %s: files=%d chunks=%d", root, scan.FilesScanned, len(scan.Chunks))

	type row struct {
		calibrationCase
		files  int
		chunks int
		paths  []string
	}
	rows := make([]row, 0, len(shapeCalibration))
	var dump []map[string]any
	for _, c := range shapeCalibration {
		// defaultK and rerank ON: exactly what a default daemon runs.
		hits, err := retrieveTopK(ctx, c.prompt, defaultK, embedder, store, lexical, true)
		if err != nil {
			t.Fatalf("retrieveTopK(%s): %v", c.name, err)
		}
		seen := map[string]struct{}{}
		for _, h := range hits {
			seen[h.FilePath] = struct{}{}
		}
		paths := make([]string, 0, len(seen))
		for p := range seen {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		rows = append(rows, row{calibrationCase: c, files: len(seen), chunks: len(hits), paths: paths})

		// THE RAW HITS, dumped. The first signal this harness tested (distinct
		// files in the top k) turned out to measure how much filler retrieval
		// returns rather than how the evidence is distributed, and diagnosing
		// that from a summary table was not possible. Every candidate signal
		// after it is evaluated against this file instead of against another
		// three-minute index build.
		for _, h := range hits {
			dump = append(dump, map[string]any{
				"scenario": c.name, "pipeline": c.wantPipeline,
				"path": h.FilePath, "score": h.Score, "lines": h.StartLine,
			})
		}
	}
	if out := os.Getenv("CALIBRATION_DUMP"); out != "" {
		blob, _ := json.MarshalIndent(dump, "", "  ")
		if err := os.WriteFile(out, blob, 0o600); err != nil {
			t.Fatalf("writing calibration dump: %v", err)
		}
		t.Logf("raw hits written to %s", out)
	}

	t.Log("scenario                    spread  chunks  §14 winner   files")
	for _, r := range rows {
		winner := "single"
		if r.wantPipeline {
			winner = "pipeline"
		}
		t.Logf("%-26s  %6d  %6d  %-10s   %v", r.name, r.files, r.chunks, winner, r.paths)
	}

	// The threshold has to separate the two labels. Report the whole picture on
	// failure rather than the first row that disagrees: a threshold is fitted to
	// the set, and one row's number in isolation says nothing about where the
	// line should go.
	var pipelineMin, singleMax = 1 << 30, 0
	for _, r := range rows {
		if r.wantPipeline && r.files < pipelineMin {
			pipelineMin = r.files
		}
		if !r.wantPipeline && r.files > singleMax {
			singleMax = r.files
		}
	}
	t.Logf("separable in [%d, %d]", singleMax+1, pipelineMin)

	// FALSIFIED, and the mechanism is in the dump rather than in the summary:
	// retrieveTopK always returns k chunks, and the fused scores are
	// RECIPROCAL-RANK values (0.0147 to 0.0185 across all three scenarios, which
	// is 1/(60+rank)). Rank fusion discards relevance magnitude by construction,
	// so "distinct files in the top k" counts how much padding retrieval
	// returned, not how the evidence is distributed. Measured here: the
	// single-agent scenario spread over as many files as both pipeline ones.
	//
	// REPORTED, NOT ASSERTED. A test that fails while the signal is bad would be
	// a permanently red gate recording a settled fact; a test that fails when
	// someone finds a BETTER signal would be worse. This prints the numbers so
	// the next hypothesis can be judged against the same three points.
	if singleMax >= pipelineMin {
		t.Logf("NOT SEPARABLE: a single-agent scenario spread over %d files while a pipeline "+
			"scenario bottomed at %d. This is the recorded result -- see §15.", singleMax, pipelineMin)
	} else {
		t.Logf("SEPARABLE on this sample, which is three points. Label more before shipping a router.")
	}

}

// Hypothesis 2, run after hypothesis 1 was falsified.
//
// The first signal (distinct files in the top k) failed for a mechanical reason
// the raw dump made obvious: retrieveTopK always returns k chunks, and the
// fused scores are RECIPROCAL-RANK values -- 0.0147 to 0.0185 across every
// scenario, which is 1/(60+rank). Rank fusion discards relevance magnitude by
// construction, so the daemon has ranks and no calibrated confidence, and
// "distinct files in the top k" measures how much padding retrieval returned
// rather than how the evidence is distributed.
//
// This tests the one quantity that is NOT bounded by k: how many chunks in the
// whole index the lexical tier matches at all. A rare identifier
// ("truncateHandoff") should match a handful of chunks in one or two files; a
// conceptual question with many common terms should match hundreds across many.
//
// IT IS THE SECOND HYPOTHESIS FITTED TO THE SAME THREE LABELLED POINTS, and
// that is stated here rather than discovered later. Three points cannot
// validate a threshold; the most this can do is fail, which would end the idea,
// or survive, which would mean it is worth labelling more points before
// shipping anything.
func TestShapeSignalLexicalBreadth(t *testing.T) {
	indexDir := os.Getenv("CALIBRATION_INDEX_DIR")
	if indexDir == "" {
		t.Skip("set CALIBRATION_INDEX_DIR to an index built by TestShapeSignalEvidenceSpread")
	}
	lexical, err := NewFTSChunkStore(indexDir)
	if err != nil {
		t.Fatalf("NewFTSChunkStore: %v", err)
	}
	defer lexical.Close()

	// Large enough that the cap is not what is being measured.
	const wide = 2000

	t.Log("scenario                    matches  files  §14 winner")
	type row struct {
		name     string
		pipeline bool
		matches  int
		files    int
	}
	var rows []row
	for _, c := range shapeCalibration {
		hits, err := lexical.Search(context.Background(), c.prompt, wide)
		if err != nil {
			t.Fatalf("lexical search(%s): %v", c.name, err)
		}
		seen := map[string]struct{}{}
		for _, h := range hits {
			seen[h.FilePath] = struct{}{}
		}
		winner := "single"
		if c.wantPipeline {
			winner = "pipeline"
		}
		t.Logf("%-26s  %7d  %5d  %s", c.name, len(hits), len(seen), winner)
		rows = append(rows, row{c.name, c.wantPipeline, len(hits), len(seen)})
	}

	pipelineMin, singleMax := 1<<30, 0
	for _, r := range rows {
		if r.pipeline && r.files < pipelineMin {
			pipelineMin = r.files
		}
		if !r.pipeline && r.files > singleMax {
			singleMax = r.files
		}
	}
	// FALSIFIED TOO, and harder: all three saturate the match cap and spread
	// over roughly 380 files. buildLexicalQuery ORs the query's terms, so any
	// natural-language question matches most of the index and breadth carries no
	// information about the question at all.
	if singleMax >= pipelineMin {
		t.Logf("NOT SEPARABLE: single reached %d files, pipeline bottomed at %d", singleMax, pipelineMin)
	} else {
		t.Logf("SEPARABLE on three points -- label more before shipping anything")
	}
}
