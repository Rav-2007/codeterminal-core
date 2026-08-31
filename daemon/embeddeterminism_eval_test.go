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
// with adjacent ranks swapping. Two runners embedded identical text to different
// vectors. This test cannot see that -- it has one machine -- and that limit is
// stated here rather than left for someone to discover by trusting a green run.
//
// IT IS INTERMITTENT, and the first draft of this comment did not say so
// because there was only one pair to look at. The very next commit, 44a2ed9,
// gave a second pair: two runners, 995 of 995 compared score lines IDENTICAL,
// both reporting 31/49 semantic, 33/49 hybrid, 39/49 delivered. So the record
// is one divergent pair and one identical pair. "Runners differ" is the wrong
// summary and would have someone chasing a divergence on a run that has none;
// "runners SOMETIMES differ, and nothing here predicts which" is the finding.
//
// WHICH MECHANISM, narrowed 2026-08-31 by TestEmbeddingVariesWithThreadCount
// below. The obvious suspect was ONNX Runtime's intra-op thread pool, sized
// from the host's core count: a different pool size sums partial results in a
// different ORDER, and float addition is not associative. That is REFUTED on
// this hardware -- 1, 2, 4 and default threads all produce bit-identical
// vectors on a 16-CPU host, with the knob verified live by the 2.1x slowdown
// at one thread. Pinning SetIntraOpNumThreads would therefore have cost up to
// 2.1x on every index build and fixed nothing.
//
// What remains is the CPU: different machines dispatch different SIMD kernels
// (AVX2 vs AVX-512, different MLAS paths) and round differently. That cannot
// be decided from one machine, which is why retrieval-eval.yml now records
// nproc, the CPU model and its vector-instruction flags on every run, and why
// the corpus build logs a whole-index vector fingerprint. The next divergence
// arrives with the evidence attached instead of requiring an afternoon.
//
// What this test guards going forward is the assumption the diagnosis rests on:
// if embedding ever stops being deterministic within a single machine, the
// noise band on every floor in rerank_eval_test.go is wrong and this fails
// loudly instead of the numbers quietly drifting.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// writeVectorsToHash feeds vectors to a hash in a fixed byte encoding.
//
// Separated from hashVectors so the whole-index fingerprint in
// evalcorpus_test.go can accumulate across batches without holding every
// vector in memory, and -- the point -- so both callers encode floats
// IDENTICALLY. Two hash functions over the same data that disagree about byte
// order would produce two fingerprints that can never be compared, which is
// worse than having no fingerprint at all.
func writeVectorsToHash(h hash.Hash, vecs [][]float32) {
	var buf [4]byte
	for _, v := range vecs {
		for _, f := range v {
			bits := math.Float32bits(f)
			buf[0], buf[1], buf[2], buf[3] = byte(bits), byte(bits>>8), byte(bits>>16), byte(bits>>24)
			_, _ = h.Write(buf[:])
		}
	}
}

// hashVectors fingerprints a batch of embeddings exactly, so "identical" means
// bit-identical rather than close.
func hashVectors(vecs [][]float32) string {
	h := sha256.New()
	writeVectorsToHash(h, vecs)
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

// TestEmbeddingVariesWithThreadCount is the experiment that separates the two
// candidate mechanisms behind machine-to-machine embedding differences.
//
// THE TWO HYPOTHESES, which produce identical symptoms:
//
//   - H-THREADS. ONNX Runtime sizes its intra-op thread pool from the host's
//     core count. More threads means the partial sums of a reduction are
//     combined in a different ORDER, and float addition is not associative, so
//     the answer differs in the last bits. Two runners with different core
//     counts would then embed identical text differently -- with no bug
//     anywhere.
//   - H-KERNEL. Different CPUs dispatch different SIMD kernels (AVX2 vs
//     AVX-512, different MLAS paths), which round differently whatever the
//     thread count is.
//
// ONLY H-THREADS IS DECIDABLE ON ONE MACHINE, and that is what this test does:
// it holds the CPU fixed and varies only the thread count. H-KERNEL needs two
// different CPUs and therefore needs the runner-hardware line this commit adds
// to retrieval-eval.yml plus the next divergence; it cannot be settled here,
// and this test does not pretend to settle it.
//
// EITHER RESULT IS A RESULT. This test does not fail when the vectors differ,
// because differing is not a defect -- it is one of the two answers, and the
// one that would justify pinning the thread count. It fails only if the
// experiment could not be RUN, and it logs the finding either way. A test that
// went red on a legitimate outcome would be pressure to make the finding go
// away rather than to read it.
func TestEmbeddingVariesWithThreadCount(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping eval test in -short mode")
	}

	logger := log.New(os.Stderr, "thread-count: ", log.LstdFlags)
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

	// The same batch through a fresh helper at each thread count. Fresh every
	// time because the pool is sized once, when the session is created.
	//
	// The ELAPSED TIME is returned as well as the vectors, and it is not a
	// performance note -- it is the evidence that the knob is connected. See
	// the check below.
	embedWith := func(threads int) ([][]float32, time.Duration) {
		t.Helper()
		h := NewHelperProcess(bin, modelDir, onnxRuntimeLib, logger)
		h.intraOpThreads = threads // 0 omits the flag: the production path
		if err := h.Start(); err != nil {
			t.Fatalf("starting helper with intra-op threads=%d: %v", threads, err)
		}
		defer func() { _ = h.Stop() }()
		start := time.Now()
		v, err := h.Embed(ctx, texts)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("Embed at intra-op threads=%d: %v", threads, err)
		}
		return v, elapsed
	}

	type arm struct {
		threads int
		label   string
		vecs    [][]float32
		elapsed time.Duration
	}
	arms := []arm{
		{threads: 0, label: "default (unset -- production)"},
		{threads: 1, label: "1"},
		{threads: 2, label: "2"},
		{threads: 4, label: "4"},
	}
	for i := range arms {
		arms[i].vecs, arms[i].elapsed = embedWith(arms[i].threads)
	}

	t.Logf("host reports %d CPUs; ONNX Runtime's default pool is derived from that", runtime.NumCPU())
	base := arms[0]
	differed := false
	for _, a := range arms {
		delta := maxAbsDiff(base.vecs, a.vecs)
		same := hashVectors(base.vecs) == hashVectors(a.vecs)
		if !same {
			differed = true
		}
		t.Logf("intra-op threads=%-28s hash=%s  identical-to-default=%t  max component delta=%g  embed=%s",
			a.label, hashVectors(a.vecs)[:16], same, delta, a.elapsed.Round(time.Millisecond))
	}

	// IS THE INSTRUMENT PLUGGED IN? This test's interesting outcome is a
	// NEGATIVE one, and a negative result from a knob that never reached the
	// subprocess would look exactly the same: four identical hashes. A flag
	// typo, an arg the helper silently ignores, a field left unread in
	// spawnLocked -- each produces a confident "thread count does not matter"
	// from an experiment that never varied the thread count.
	//
	// So the timing is the corroboration. One thread on a multi-core host must
	// be measurably slower than the library default; if it is not, nothing was
	// varied and the result above means nothing. This asserts rather than
	// logging and hoping someone reads it.
	var single, dflt time.Duration
	for _, a := range arms {
		switch a.threads {
		case 0:
			dflt = a.elapsed
		case 1:
			single = a.elapsed
		}
	}
	if runtime.NumCPU() > 2 && single <= dflt {
		t.Fatalf("intra-op threads=1 took %s and the library default took %s on a %d-CPU host. "+
			"Single-threaded inference cannot be as fast as multi-threaded, so --intra-op-threads "+
			"is NOT reaching the helper and every hash comparison above is vacuous: four arms "+
			"that all ran the default configuration will of course agree. Check spawnLocked's "+
			"arg list and the helper's flag name before reading any conclusion from this test",
			single.Round(time.Millisecond), dflt.Round(time.Millisecond), runtime.NumCPU())
	}
	t.Logf("knob verified connected: threads=1 took %s vs %s at the library default (%.1fx slower), "+
		"so the arms above really did run different thread counts",
		single.Round(time.Millisecond), dflt.Round(time.Millisecond),
		single.Seconds()/dflt.Seconds())

	if differed {
		t.Logf("H-THREADS CONFIRMED: holding the CPU fixed and varying only the intra-op "+
			"thread count changes the vectors. A CI runner with a different core count is "+
			"therefore sufficient on its own to explain the %d-query spread measured on "+
			"3ada6ea, and pinning SetIntraOpNumThreads would remove that mechanism -- at a "+
			"cost to index-build time that has NOT been measured here and must be before "+
			"anything is pinned", evalFusionLossSlack)
		return
	}
	t.Logf("H-THREADS REFUTED on this machine: every thread count produced bit-identical " +
		"vectors, so the reduction order does not depend on the pool size for this model and " +
		"batch shape. Pinning the thread count would therefore fix nothing, and H-KERNEL -- " +
		"different CPUs dispatching different SIMD kernels -- is what remains. That one needs " +
		"the runner-hardware line in retrieval-eval.yml and the next divergence; it cannot be " +
		"decided from one machine.")
}
