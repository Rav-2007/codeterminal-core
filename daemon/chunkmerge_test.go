package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// lineChunk builds a Chunk whose content is exactly the lines its declared
// range covers, matching what chunkContent (chunker.go) produces: line N of
// file f renders as "f line N". Two chunks built this way from the same file
// agree on every line they share, which is what makes them spliceable.
func lineChunk(file string, start, end int) Chunk {
	lines := make([]string, 0, end-start+1)
	for n := start; n <= end; n++ {
		lines = append(lines, fmt.Sprintf("%s line %d", file, n))
	}
	return Chunk{
		ID:        fmt.Sprintf("%s:%d-%d", file, start, end),
		FilePath:  file,
		StartLine: start,
		EndLine:   end,
		Content:   strings.Join(lines, "\n"),
		Class:     FileClassCode,
	}
}

func chunkIDs(chunks []Chunk) []string {
	ids := make([]string, len(chunks))
	for i, c := range chunks {
		ids[i] = fmt.Sprintf("%s:%d-%d", c.FilePath, c.StartLine, c.EndLine)
	}
	return ids
}

// TestMergeAdjacentChunks_MergeMatrix is the shape-by-shape regression for
// Fix 11: which pairs merge, which stay separate, and what the merged span's
// declared range is.
func TestMergeAdjacentChunks_MergeMatrix(t *testing.T) {
	tests := []struct {
		name  string
		in    []Chunk
		want  []string
		notes string
	}{
		{
			name:  "overlapping same-file windows merge",
			in:    []Chunk{lineChunk("server.go", 661, 700), lineChunk("server.go", 691, 712)},
			want:  []string{"server.go:661-712"},
			notes: "the measured case: consecutive indexer windows sharing 10 lines",
		},
		{
			name:  "exactly adjacent same-file windows merge",
			in:    []Chunk{lineChunk("a.go", 1, 40), lineChunk("a.go", 41, 80)},
			want:  []string{"a.go:1-80"},
			notes: "touching but not overlapping is still one contiguous span",
		},
		{
			name:  "far-apart same-file chunks stay separate",
			in:    []Chunk{lineChunk("a.go", 1, 40), lineChunk("a.go", 200, 240)},
			want:  []string{"a.go:1-40", "a.go:200-240"},
			notes: "a one-line gap would already be a lie about the range; 159 certainly is",
		},
		{
			name:  "one-line gap stays separate",
			in:    []Chunk{lineChunk("a.go", 1, 40), lineChunk("a.go", 42, 80)},
			want:  []string{"a.go:1-40", "a.go:42-80"},
			notes: "line 41 is missing, so 1-80 would be inaccurate attribution",
		},
		{
			name:  "different files never merge",
			in:    []Chunk{lineChunk("a.go", 1, 40), lineChunk("b.go", 41, 80)},
			want:  []string{"a.go:1-40", "b.go:41-80"},
			notes: "ranges touch numerically but belong to different files",
		},
		{
			name:  "fully contained chunk is absorbed",
			in:    []Chunk{lineChunk("a.go", 1, 100), lineChunk("a.go", 20, 40)},
			want:  []string{"a.go:1-100"},
			notes: "Fix 12 fusion can produce this: a direct span inside a similarity chunk",
		},
		{
			name:  "identical chunk is absorbed",
			in:    []Chunk{lineChunk("a.go", 1, 40), lineChunk("a.go", 1, 40)},
			want:  []string{"a.go:1-40"},
			notes: "dedupe, not duplication",
		},
		{
			name:  "three-window chain merges into one span",
			in:    []Chunk{lineChunk("a.go", 1, 40), lineChunk("a.go", 31, 70), lineChunk("a.go", 61, 100)},
			want:  []string{"a.go:1-100"},
			notes: "transitive: the sweep keeps extending the same span",
		},
		{
			name: "two clusters in one file stay two spans",
			in: []Chunk{
				lineChunk("a.go", 1, 40), lineChunk("a.go", 31, 70),
				lineChunk("a.go", 400, 440), lineChunk("a.go", 431, 470),
			},
			want:  []string{"a.go:1-70", "a.go:400-470"},
			notes: "merging is local to a contiguous run, not per-file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chunkIDs(mergeAdjacentChunks(tt.in))
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("mergeAdjacentChunks() = %v, want %v (%s)", got, tt.want, tt.notes)
			}
		})
	}
}

// TestMergeAdjacentChunks_PreservesLineAttribution is the acceptance check
// that a merged span's declared range is TRUE of its content: every line the
// range claims is present, exactly once, in order.
func TestMergeAdjacentChunks_PreservesLineAttribution(t *testing.T) {
	in := []Chunk{lineChunk("server.go", 661, 700), lineChunk("server.go", 691, 712)}

	merged := mergeAdjacentChunks(in)
	if len(merged) != 1 {
		t.Fatalf("expected one merged span, got %d: %v", len(merged), chunkIDs(merged))
	}
	got := merged[0]

	if got.StartLine != 661 || got.EndLine != 712 {
		t.Fatalf("merged range = %d-%d, want 661-712", got.StartLine, got.EndLine)
	}
	if got.ID != "server.go:661-712" {
		t.Fatalf("merged ID = %q, want %q", got.ID, "server.go:661-712")
	}

	lines := strings.Split(got.Content, "\n")
	if len(lines) != 712-661+1 {
		t.Fatalf("merged content has %d lines, want %d (range says 661-712)", len(lines), 712-661+1)
	}
	for i, line := range lines {
		want := fmt.Sprintf("server.go line %d", 661+i)
		if line != want {
			t.Fatalf("merged content line %d = %q, want %q (line attribution must stay true)", i, line, want)
		}
	}

	// The whole point: the shared seam appears exactly once now.
	seam := "server.go line 695"
	if n := strings.Count(got.Content, seam); n != 1 {
		t.Fatalf("overlap line %q appears %d times in the merged span, want exactly 1", seam, n)
	}
}

// TestMergeAdjacentChunks_RefusesToSpliceMismatchedOverlap covers the
// index-staleness edge: two chunks whose shared lines DISAGREE cannot both be
// true of the same file, so splicing them would invent a version of the file
// that never existed. They must stay separate instead.
func TestMergeAdjacentChunks_RefusesToSpliceMismatchedOverlap(t *testing.T) {
	a := lineChunk("a.go", 1, 40)
	b := lineChunk("a.go", 31, 70)
	b.Content = strings.Replace(b.Content, "a.go line 35", "a.go line 35 // EDITED SINCE INDEXING", 1)

	got := chunkIDs(mergeAdjacentChunks([]Chunk{a, b}))
	want := []string{"a.go:1-40", "a.go:31-70"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("mergeAdjacentChunks() = %v, want %v (mismatched overlap must not splice)", got, want)
	}
}

// TestMergeAdjacentChunks_RefusesRangeContentMismatch covers a chunk whose
// content length contradicts its declared range: line arithmetic can't splice
// it, so it must be left alone rather than spliced on a guess.
func TestMergeAdjacentChunks_RefusesRangeContentMismatch(t *testing.T) {
	a := lineChunk("a.go", 1, 40)
	b := lineChunk("a.go", 31, 70)
	b.Content = "only one line, but the range claims forty"

	got := chunkIDs(mergeAdjacentChunks([]Chunk{a, b}))
	want := []string{"a.go:1-40", "a.go:31-70"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("mergeAdjacentChunks() = %v, want %v (range/content mismatch must not splice)", got, want)
	}
}

// TestMergeAdjacentChunks_PreservesRankOrder pins the ordering contract
// truncateToBudget depends on: a merged span sits at its BEST-ranked
// constituent's position, and unrelated chunks keep their relative order.
func TestMergeAdjacentChunks_PreservesRankOrder(t *testing.T) {
	// Ranked order: a top hit in b.go, then two windows of a.go that merge.
	// The merged span must land at position 2 (where a.go:100-139 was), not
	// be hoisted to the front or pushed to the back.
	in := []Chunk{
		lineChunk("b.go", 1, 40),
		lineChunk("a.go", 100, 139),
		lineChunk("c.go", 1, 40),
		lineChunk("a.go", 130, 169),
	}
	got := chunkIDs(mergeAdjacentChunks(in))
	want := []string{"b.go:1-40", "a.go:100-169", "c.go:1-40"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("mergeAdjacentChunks() = %v, want %v (merged span keeps its best rank)", got, want)
	}
}

// TestMergeAdjacentChunks_KeepsBestScore: a span containing the best-scoring
// chunk is at least as good as that chunk, so the merged score must not be
// diluted down to the weaker constituent's.
func TestMergeAdjacentChunks_KeepsBestScore(t *testing.T) {
	a := lineChunk("a.go", 1, 40)
	a.Score, a.RawScore = 0.4, 0.5
	b := lineChunk("a.go", 31, 70)
	b.Score, b.RawScore = 0.9, 0.8

	merged := mergeAdjacentChunks([]Chunk{a, b})
	if len(merged) != 1 {
		t.Fatalf("expected one merged span, got %d", len(merged))
	}
	if merged[0].Score != 0.9 {
		t.Errorf("merged Score = %v, want 0.9 (the best constituent's)", merged[0].Score)
	}
	if merged[0].RawScore != 0.8 {
		t.Errorf("merged RawScore = %v, want 0.8 (the best constituent's)", merged[0].RawScore)
	}
}

// TestMergeAdjacentChunks_NoOpCases: merging must be a no-op — same slice
// contents, same order — when there is nothing to merge.
func TestMergeAdjacentChunks_NoOpCases(t *testing.T) {
	if got := mergeAdjacentChunks(nil); got != nil {
		t.Errorf("mergeAdjacentChunks(nil) = %v, want nil", got)
	}
	one := []Chunk{lineChunk("a.go", 1, 40)}
	if got := chunkIDs(mergeAdjacentChunks(one)); len(got) != 1 || got[0] != "a.go:1-40" {
		t.Errorf("single chunk changed: %v", got)
	}
	distinct := []Chunk{lineChunk("a.go", 1, 40), lineChunk("b.go", 1, 40), lineChunk("c.go", 1, 40)}
	got := chunkIDs(mergeAdjacentChunks(distinct))
	want := []string{"a.go:1-40", "b.go:1-40", "c.go:1-40"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("distinct files reordered: got %v, want %v", got, want)
	}
}

// TestMergeAdjacentChunks_ReclaimsRenderedBudget is the budget measurement,
// taken through renderChunk — the same function that produces the bytes
// actually sent and the same one truncateToBudget sizes with.
func TestMergeAdjacentChunks_ReclaimsRenderedBudget(t *testing.T) {
	// A 5-chunk sample shaped like the observed one: two adjacent-window
	// pairs plus one unrelated chunk.
	before := []Chunk{
		lineChunk("server.go", 661, 700),
		lineChunk("server.go", 691, 712),
		lineChunk("context.go", 1, 40),
		lineChunk("context.go", 31, 70),
		lineChunk("history.go", 1, 40),
	}
	after := mergeAdjacentChunks(before)
	if len(after) != 3 {
		t.Fatalf("expected 3 spans after merging, got %d: %v", len(after), chunkIDs(after))
	}

	beforeBytes := renderedSize(before, false)
	afterBytes := renderedSize(after, false)
	saved := mergeSavings(before, after, false)

	if saved <= 0 {
		t.Fatalf("merging reclaimed %d bytes; expected a positive saving", saved)
	}
	if saved != beforeBytes-afterBytes {
		t.Fatalf("mergeSavings()=%d disagrees with rendered sizes (%d-%d=%d)", saved, beforeBytes, afterBytes, beforeBytes-afterBytes)
	}
	t.Logf("rendered budget: before=%dB after=%dB reclaimed=%dB (%.1f%%)",
		beforeBytes, afterBytes, saved, 100*float64(saved)/float64(beforeBytes))
}

// fixedStore is a VectorStore that always returns the same ranked chunk list,
// so a test can pin exactly what retrieval hands gatherContext.
type fixedStore struct{ chunks []Chunk }

func (fixedStore) Upsert(ctx context.Context, chunks []Chunk) error { return nil }
func (s fixedStore) Query(ctx context.Context, queryVec []float32, k int) ([]Chunk, error) {
	if k > len(s.chunks) {
		k = len(s.chunks)
	}
	return s.chunks[:k], nil
}
func (fixedStore) DeleteByFilePath(ctx context.Context, relPath string) error { return nil }
func (s fixedStore) Count() int                                               { return len(s.chunks) }

// TestGatherContext_MergingReclaimsBudgetForMoreDistinctContext is the
// budget-interaction acceptance: with a char budget that is BINDING, folding
// two overlapping windows into one span frees enough room for a distinct
// lower-ranked chunk that used to be dropped. It also pins the honest half —
// what merging does NOT do: it never refetches, so the retrieved set is the
// same k either way.
func TestGatherContext_MergingReclaimsBudgetForMoreDistinctContext(t *testing.T) {
	retrieved := []Chunk{
		lineChunk("server.go", 661, 700),
		lineChunk("server.go", 691, 712),
		lineChunk("history.go", 1, 40),
	}

	// Budget = exactly what the MERGED set renders to. That makes the budget
	// genuinely binding beforehand (the unmerged set is larger by the
	// duplicated overlap) and exactly sufficient afterwards, so what the
	// reclaimed bytes bought is unambiguous.
	budget := renderedSize(mergeAdjacentChunks(retrieved), false)

	// Prove the premise rather than assert it: with the SAME budget and no
	// merging, history.go is dropped.
	if unmergedKept, unmergedTruncated := truncateToBudget(retrieved, budget, false); !unmergedTruncated || len(unmergedKept) != 2 {
		t.Fatalf("premise broken: without merging, budget %d should have truncated to 2 chunks, got %d (truncated=%t)",
			budget, len(unmergedKept), unmergedTruncated)
	}

	newServer := func() *Server {
		return &Server{
			logger:             discardLogger(),
			embedder:           &fakeEmbedder{dim: embedDim},
			store:              fixedStore{chunks: retrieved},
			lexicalStore:       nil,
			retrievalTopK:      len(retrieved),
			contextBudgetChars: budget,
			rerankDisabled:     true, // this test is about merging + budget, not ranking
			cfg:                &Config{},
		}
	}

	out := newServer().gatherContext(context.Background(), "why does the server drop connections")

	if out.Skipped {
		t.Fatalf("retrieval skipped unexpectedly: %s", out.Reason)
	}
	if out.MergedFrom != 3 {
		t.Errorf("MergedFrom = %d, want 3 (the pre-merge retrieved count)", out.MergedFrom)
	}
	if out.MergeSavedBytes <= 0 {
		t.Errorf("MergeSavedBytes = %d, want a positive reclaim", out.MergeSavedBytes)
	}

	ids := chunkIDs(out.Chunks)
	want := []string{"server.go:661-712", "history.go:1-40"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("kept spans = %v, want %v — the reclaimed bytes should let history.go fit", ids, want)
	}
	if out.Truncated {
		t.Error("Truncated = true, want false: everything fits once the overlap is reclaimed")
	}

	t.Logf("budget=%dB: unmerged set renders to %dB (history.go dropped); merged set renders to %dB, reclaiming %dB",
		budget, renderedSize(retrieved, false), renderedSize(out.Chunks, false), out.MergeSavedBytes)
}
