//go:build eval

// Does the deadline we grant a batch actually cover what the batch costs?
//
// WHY THIS EXISTS AS A SEPARATE TEST. On 2026-08-30 the scheduled eval died with
// `i/o timeout` embedding batch [0:40] of an index build, ~10s after the helper
// reported ready — defaultHelperCallTimeout was 10s and one batch of forty real
// chunks did not fit inside it. The fix (defaultHelperPerTextTimeout, see
// helperproc.go) is pinned by TestEmbedDeadlineScalesWithTheBatch, which proves
// the ARITHMETIC with a fake helper that sleeps on command.
//
// That is not the same claim as "the real model fits". The unit test would pass
// unchanged if BGE got ten times slower tomorrow. And the retrieval eval passing
// does not close the gap either: the original failure hit ONE OF TWO runs on
// identical commits, so a green eval is consistent with a fast runner and proves
// nothing about the margin.
//
// So this measures the real thing — real model, real helper subprocess, the
// exact batch size and the exact embed text the index build uses, on a COLD
// session — and asserts it against the deadline the production code actually
// grants it. It is the only test in the repo that can see the margin shrink.
package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFirstEmbedBatchFitsItsDeadline embeds the first indexEmbedBatchSize chunks
// of this repository through a freshly started helper and reports the margin.
//
// THE FIRST BATCH SPECIFICALLY, because it is the expensive one: it pays the
// ONNX session's cold start on top of the inference, and it is the batch that
// actually failed in CI. A steady-state batch would understate the cost and
// measure the wrong thing.
func TestFirstEmbedBatchFitsItsDeadline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping eval test in -short mode")
	}

	logger := log.New(os.Stderr, "batch-deadline: ", log.LstdFlags)
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

	// Real chunks, not synthetic strings: token count drives inference cost, and
	// a batch of "hello" forty times would measure nothing.
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolving repo root: %v", err)
	}
	scan, err := ScanWorkspace(repoRoot)
	if err != nil {
		t.Fatalf("ScanWorkspace: %v", err)
	}
	if len(scan.Chunks) < indexEmbedBatchSize {
		t.Fatalf("scanned only %d chunks, need at least %d to measure a full batch",
			len(scan.Chunks), indexEmbedBatchSize)
	}
	batch := scan.Chunks[:indexEmbedBatchSize]
	texts := embedTextsFor(batch)

	helper := NewHelperProcess(buildRealHelperBinary(t), modelDir, onnxRuntimeLib, logger)
	if err := helper.Start(); err != nil {
		t.Fatalf("starting real embedder helper: %v", err)
	}
	defer func() { _ = helper.Stop() }()

	granted := helper.embedDeadline(len(texts))
	oldFixed := helper.callTimeout // what every batch used to get

	start := time.Now()
	vecs, err := helper.Embed(ctx, texts)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("the FIRST batch of %d chunks failed after %s: %v\n\n"+
			"This is the exact failure that took down CI run 33321602030. The deadline "+
			"granted was %s.", len(texts), elapsed.Round(time.Millisecond), err, granted)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("got %d vectors for %d texts", len(vecs), len(texts))
	}

	t.Logf("cold first batch: %d chunks in %s | granted %s (%.1fx margin) | "+
		"the old fixed deadline was %s (%.1fx)",
		len(texts), elapsed.Round(time.Millisecond),
		granted, granted.Seconds()/elapsed.Seconds(),
		oldFixed, oldFixed.Seconds()/elapsed.Seconds())

	// TWO ASSERTIONS, BECAUSE THEY CATCH DIFFERENT REGRESSIONS AND ONE OF THEM
	// MUST NOT DEPEND ON THIS MACHINE'S SPEED.
	//
	// MEASURED 2026-08-30 on a 12-core i5-1340P, real model, real helper:
	//
	//	condition                    cold batch   vs old 10s   vs granted 49s
	//	idle                         3.966s       2.5x         12.4x
	//	~2.5x CPU oversubscribed     9.139s       1.1x          5.4x
	//
	// The second row is the failure reproduced: 0.86 seconds from tripping the
	// deadline that actually tripped in CI run 33321602030. It corroborates the
	// runner-variance figure measured separately that day from a completely
	// different experiment -- 3.966s x 2.63 is 10.4s, just past 10s.
	//
	// A RATIO ALONE WOULD NOT HAVE CAUGHT THE BUG, and this was found by
	// neutering rather than by reasoning. The first draft of this test asserted
	// only `margin >= 2.0`. Reverting defaultHelperPerTextTimeout to zero -- the
	// exact pre-fix configuration that failed in CI -- gives a 10s deadline
	// against a 3.966s idle batch, a margin of 2.52x, which PASSES a 2.0 floor.
	// The floor had been set below the configuration it existed to reject.
	//
	// Hence the first assertion, which is deterministic and cannot flake: the
	// deadline itself, independent of how fast this machine happens to be.
	// 25s is derived, not chosen -- the worst cold batch measured anywhere is
	// 9.139s, runners vary by up to 2.63x on identical work, and 9.139 x 2.63
	// is 24.0s. A deadline that does not cover the worst case times runner
	// variance is one waiting for a slow afternoon.
	const minBatchDeadline = 25 * time.Second
	if granted < minBatchDeadline {
		t.Errorf("a %d-chunk batch is granted only %s. The worst cold batch measured is "+
			"9.139s and runners vary by up to 2.63x on identical work, so anything under "+
			"%s does not cover the worst case and WILL time out on a loaded runner -- "+
			"which is exactly what happened in run 33321602030 at %s. "+
			"defaultHelperPerTextTimeout has been reduced.",
			len(texts), granted, minBatchDeadline, oldFixed)
	}

	// And the ratio, which catches the other direction: the deadline untouched
	// but the WORK getting slower -- a heavier model, a larger sequence cap, a
	// bigger batch. 3.0 needs elapsed to exceed 16.3s before it fires, four
	// times the idle measurement and well past the 9.139s seen under heavy
	// contention, so a loaded CI runner does not trip it.
	const minMargin = 3.0
	if margin := granted.Seconds() / elapsed.Seconds(); margin < minMargin {
		t.Errorf("cold first batch took %s against a %s deadline — only %.2fx of margin, "+
			"below the %.1fx floor. The deadline is unchanged, so the WORK got slower: a "+
			"heavier model, a larger sequence cap, or a bigger indexEmbedBatchSize. "+
			"Re-measure and re-derive minBatchDeadline above rather than lowering this",
			elapsed.Round(time.Millisecond), granted, margin, minMargin)
	}
}
