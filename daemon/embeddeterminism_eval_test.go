//go:build eval

// Is the embedder deterministic?
//
// The locate eval's floors are all calibrated with "two queries of slack",
// justified by CORPUS sensitivity: rerank_eval_test.go records that a 0.1%
// change in the corpus moved the gated number by two queries, and states that
// "retrieval here is deterministic" on the strength of an experiment that
// indexed ONCE and ran the identical query pass three times over that one
// index.
//
// That experiment holds the VECTORS fixed. It cannot see nondeterminism in the
// step that produces them.
//
// On 2026-08-30 two CI runs of commit 3ada6ea -- same code, and a byte-identical
// corpus of 4757 chunks over 558 files -- reported:
//
//	                  branch run   main run
//	semantic-only     35/49        32/49
//	hybrid            34/49        33/49
//	DELIVERED         41/49        42/49
//	budgeted out      1            0
//
// Three queries apart on semantic-only, with nothing whatsoever to attribute it
// to. One of those runs went red on `semanticOnlyCount > hybridCount`, a gate
// with no slack at all.
//
// This file is the probe for the obvious suspect: the embedding step.
//
// THE ANSWER, measured 2026-08-30 on a 12-core i5-1340P: WITHIN ONE MACHINE
// EMBEDDING IS BIT-IDENTICAL. Same process twice, and a freshly spawned helper
// process: max component delta 0, hashes equal. So this test passes, and its
// passing is the load-bearing half of the diagnosis rather than a null result --
// it is what makes the CI difference attributable to the MACHINE. If embedding
// were unstable within a process, nothing about the eval would be trustworthy
// and the investigation would have ended somewhere else entirely.
//
// The cross-machine half was then settled from the two CI runs' own output: 308
// of 400 compared per-chunk score lines differ, in the third and fourth decimal,
// with adjacent ranks swapping. Two runners embed identical text to different
// vectors. This test cannot see that -- it has one machine -- and that limit is
// stated here rather than left for someone to discover by trusting a green run.
//
// What it guards going forward is the assumption the diagnosis rests on: if
// embedding ever stops being deterministic within a single machine, the noise
// band on every floor in rerank_eval_test.go is wrong and this fails loudly
// instead of the numbers quietly drifting.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// hashVectors fingerprints a batch of embeddings exactly, so "identical" means
// bit-identical rather than close.
func hashVectors(vecs [][]float32) string {
	h := sha256.New()
	var buf [4]byte
	for _, v := range vecs {
		for _, f := range v {
			bits := math.Float32bits(f)
			buf[0], buf[1], buf[2], buf[3] = byte(bits), byte(bits>>8), byte(bits>>16), byte(bits>>24)
			_, _ = h.Write(buf[:])
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// maxAbsDiff is how far apart two vector sets are at their worst single
// component -- the difference between "a different reduction order" and "a
// different model".
func maxAbsDiff(a, b [][]float32) float64 {
	worst := 0.0
	for i := range a {
		if i >= len(b) {
			break
		}
		for j := range a[i] {
			if j >= len(b[i]) {
				break
			}
			if d := math.Abs(float64(a[i][j] - b[i][j])); d > worst {
				worst = d
			}
		}
	}
	return worst
}

// TestEmbeddingIsDeterministic embeds the same real batch twice in the SAME
// helper process, and then again in a SECOND, freshly spawned helper.
//
// The two questions are different and the answers can differ:
//
//   - Same process, twice: is a single ONNX session's output stable?
//   - Fresh process: does a new session, which may size its thread pool
//     differently, reduce in a different order and land on different floats?
//
// A CI runner that hands the helper a different core count than the last one is
// exactly the second case, and it is what two runs of one commit differ by.
func TestEmbeddingIsDeterministic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping eval test in -short mode")
	}

	logger := log.New(os.Stderr, "embed-determinism: ", log.LstdFlags)
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

	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	scan, err := ScanWorkspace(repoRoot)
	if err != nil {
		t.Fatalf("ScanWorkspace: %v", err)
	}
	if len(scan.Chunks) < indexEmbedBatchSize {
		t.Fatalf("need at least %d chunks", indexEmbedBatchSize)
	}
	texts := embedTextsFor(scan.Chunks[:indexEmbedBatchSize])

	bin := buildRealHelperBinary(t)
	embedOnce := func() [][]float32 {
		t.Helper()
		h := NewHelperProcess(bin, modelDir, onnxRuntimeLib, logger)
		if err := h.Start(); err != nil {
			t.Fatalf("starting helper: %v", err)
		}
		defer func() { _ = h.Stop() }()
		v, err := h.Embed(ctx, texts)
		if err != nil {
			t.Fatalf("Embed: %v", err)
		}
		return v
	}

	// Same process, twice.
	h := NewHelperProcess(bin, modelDir, onnxRuntimeLib, logger)
	if err := h.Start(); err != nil {
		t.Fatalf("starting helper: %v", err)
	}
	a1, err := h.Embed(ctx, texts)
	if err != nil {
		t.Fatalf("Embed #1: %v", err)
	}
	a2, err := h.Embed(ctx, texts)
	if err != nil {
		t.Fatalf("Embed #2: %v", err)
	}
	_ = h.Stop()

	// A completely fresh helper subprocess.
	b1 := embedOnce()

	sameProc := hashVectors(a1) == hashVectors(a2)
	crossProc := hashVectors(a1) == hashVectors(b1)

	t.Logf("same process, two calls:  identical=%t  max component delta=%g", sameProc, maxAbsDiff(a1, a2))
	t.Logf("fresh process:            identical=%t  max component delta=%g", crossProc, maxAbsDiff(a1, b1))

	if !sameProc {
		t.Errorf("THE SAME TEXT EMBEDDED TWICE IN ONE PROCESS GAVE DIFFERENT VECTORS "+
			"(worst component delta %g). Every eval number in this package is then noisy at "+
			"a level nothing has measured, and the floors were calibrated without "+
			"knowing it", maxAbsDiff(a1, a2))
	}
	if !crossProc {
		t.Errorf("A FRESH HELPER PROCESS EMBEDDED THE SAME TEXT TO DIFFERENT VECTORS "+
			"(worst component delta %g). This is the candidate explanation for two CI "+
			"runs of one commit, on a byte-identical corpus, reporting semantic-only "+
			"recall three queries apart. It does not make the eval useless -- the "+
			"differences are tiny and only flip near-ties -- but it does mean every "+
			"floor in rerank_eval_test.go needs slack for a noise source none of them "+
			"were calibrated against, and that `semanticOnlyCount > hybridCount`, which "+
			"has NO slack, cannot stay a hard gate", maxAbsDiff(a1, b1))
	}
}
