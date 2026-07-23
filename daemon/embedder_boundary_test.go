package main

import (
	"context"
	"strings"
	"testing"
)

// This file is the before/after evidence for C1's embedder-output-boundary
// half. It drives the REAL helper wire protocol over a REAL Unix socket (via
// HelperProcess + BgeEmbedder + the fakehelper fixture), not a hand-called
// vulnerable function, so it speaks to reachability rather than mere existence
// of the bug.
//
// Two distinct malformed-response shapes the CTO report conflated are pinned
// apart here:
//
//   - A truncated/partial wire read (FAKEHELPER_TRUNCATE) surfaces as a
//     json.Decode ERROR, already handled by every call site's `if err != nil`.
//     It never yields a short vector slice, so it can never reach an
//     out-of-range deref. TestEmbedderBoundary_TruncatedResponseIsError pins
//     that — it passes before and after the fix, refuting the report's claim
//     that a truncated read is a deref trigger.
//   - A well-formed ok:true response carrying FEWER vectors than input texts
//     (FAKEHELPER_SHORT_VECTORS) is the only shape that actually reaches the
//     unchecked deref. The real ONNX helper cannot produce it (see
//     helper/onnxembedder.go: Embed returns exactly len(texts) vectors or an
//     error), so this fixture fabricates a lying helper to exercise the
//     daemon's boundary handling. Run against the pre-fix code, the two
//     ShortVectorCount tests FAIL (panic / missing error); after the boundary
//     length-check they pass (a clean error, no panic).

// lyingHelperEmbedder starts the fake helper in the named adversarial mode and
// returns a BgeEmbedder speaking to it over a real socket. Stop is registered
// via t.Cleanup.
func lyingHelperEmbedder(t *testing.T, mode string) *BgeEmbedder {
	t.Helper()
	h := fastHelperProcess(t)
	h.extraEnv = []string{mode + "=1"}
	if err := h.Start(); err != nil {
		t.Fatalf("Start helper (%s): %v", mode, err)
	}
	t.Cleanup(func() { h.Stop() })
	return NewBgeEmbedder(h)
}

// TestEmbedderBoundary_TruncatedResponseIsError proves a truncated wire read is
// caught as an error, not turned into a short slice. This is the report's
// "truncated wire read" mechanism, shown to be a non-issue for the deref.
func TestEmbedderBoundary_TruncatedResponseIsError(t *testing.T) {
	emb := lyingHelperEmbedder(t, "FAKEHELPER_TRUNCATE")

	_, err := emb.Embed(context.Background(), []string{"alpha", "beta"})
	if err == nil {
		t.Fatal("expected an error from a truncated helper response, got nil (a partial wire read must never surface as a usable short slice)")
	}
}

// TestEmbedderBoundary_ShortVectorCountDoesNotPanicOnRetrieve drives the real
// query path (retrieveTopK -> EmbedQuery -> vecs[0]) against a helper that
// returns one fewer vector than texts. A single-text query yields ZERO vectors,
// so the pre-fix `vecs[0]` panics; the recover here converts that into a test
// failure. After the boundary length-check, EmbedQuery returns a clean error
// and retrieveTopK propagates it.
func TestEmbedderBoundary_ShortVectorCountDoesNotPanicOnRetrieve(t *testing.T) {
	emb := lyingHelperEmbedder(t, "FAKEHELPER_SHORT_VECTORS")
	store := fixedStore{chunks: []Chunk{{FilePath: "x.go", Content: "package x"}}}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("retrieveTopK panicked on a short embedder response (C1 bug present): %v", r)
		}
	}()

	_, err := retrieveTopK(context.Background(), "some query", defaultK, emb, store, nil, false)
	if err == nil {
		t.Fatal("expected retrieveTopK to return an error when the embedder returns too few vectors, got nil")
	}
}

// TestEmbedderBoundary_ShortVectorCountFailsAtBoundary pins the fix at its
// chokepoint: HelperProcess.Embed must reject a count mismatch for the
// multi-text (indexing/reindex) path too, where the unchecked deref was
// vecs[i]. Pre-fix, Embed returns (shortSlice, nil) and this fails on the nil
// error; post-fix it returns a descriptive error.
func TestEmbedderBoundary_ShortVectorCountFailsAtBoundary(t *testing.T) {
	emb := lyingHelperEmbedder(t, "FAKEHELPER_SHORT_VECTORS")

	_, err := emb.Embed(context.Background(), []string{"a", "b", "c"})
	if err == nil {
		t.Fatal("expected Embed to reject a response with fewer vectors than texts, got nil error")
	}
	if !strings.Contains(err.Error(), "vector") {
		t.Errorf("error = %v, want it to name the vector-count mismatch", err)
	}
}
