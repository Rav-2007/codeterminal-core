package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// packingWorkspace writes three files that each hold one long function -- long
// enough that widening a hit to it costs thousands of characters, short enough
// to be under the construct cap -- and one small file, and returns the
// workspace and four hits in that rank order: the middle of each long
// function, then the small file.
func packingWorkspace(t *testing.T) (string, []Chunk) {
	t.Helper()
	root := t.TempDir()
	var hits []Chunk
	for i := 1; i <= 3; i++ {
		var b strings.Builder
		fmt.Fprintf(&b, "package x\n\n// Long%d is long on purpose.\nfunc Long%d() {\n", i, i)
		for j := 0; j < 200; j++ {
			fmt.Fprintf(&b, "\tx%d := %d // padding so widening this function is expensive\n", j, j)
		}
		b.WriteString("}\n")
		rel := fmt.Sprintf("long%d.go", i)
		if err := os.WriteFile(filepath.Join(root, rel), []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		chunks := chunkContent([]byte(b.String()), rel)
		hits = append(hits, chunks[3])
	}
	small := "package x\n\nfunc Small() int { return 4 }\n"
	if err := os.WriteFile(filepath.Join(root, "small.go"), []byte(small), 0o600); err != nil {
		t.Fatal(err)
	}
	hits = append(hits, chunkContent([]byte(small), "small.go")[0])
	return root, hits
}

func spanHolds(spans []Chunk, hit Chunk) int {
	for i, s := range spans {
		if s.FilePath == hit.FilePath && s.StartLine <= hit.StartLine && s.EndLine >= hit.EndLine {
			return i + 1
		}
	}
	return 0
}

// A RETRIEVED HIT IS NEVER LOST TO A BETTER-RANKED HIT'S SURROUNDINGS, under
// TwoPass. This is q45 of the locate eval in miniature: three hits whose
// declarations are expensive, then a small one. One-pass widens hits 1 and 2
// into the budget and has no room left for hit 4; two-pass places all four
// first and spends what remains on widening.
//
// Neuter check: make deliverWithinBudget ignore TwoPass, and hit 4 is lost.
func TestTwoPassKeepsEveryRetrievedHit(t *testing.T) {
	root, hits := packingWorkspace(t)
	one := expandPolicy{TopN: 10, ConstructCap: 300}

	// Size the budget from the one-pass spans themselves: room for the first two
	// widened functions and one byte short of the small hit after them.
	wide := deliverWithinBudget(nil, hits, root, one, 10, 1<<30, false).Chunks
	if len(wide) != 4 {
		t.Fatalf("precondition: one span per hit expected unbudgeted, got %d", len(wide))
	}
	budget := len(renderChunk(1, wide[0], false)) + len(renderChunk(2, wide[1], false)) +
		len(renderChunk(3, wide[3], false)) - 1

	got := deliverWithinBudget(nil, hits, root, one, 10, budget, false)
	if spanHolds(got.Chunks, hits[3]) != 0 {
		t.Fatalf("precondition: one-pass was expected to lose hit 4 at budget %d, and kept it", budget)
	}

	two := one
	two.TwoPass = true
	got = deliverWithinBudget(nil, hits, root, two, 10, budget, false)
	for i, h := range hits {
		if at := spanHolds(got.Chunks, h); at != i+1 {
			t.Errorf("two-pass: hit %d is at span %d, want span %d -- every hit placed, in rank order", i+1, at, i+1)
		}
	}
	if got.Truncated {
		t.Error("two-pass reported Truncated with every hit delivered; a skipped widening is not a lost result")
	}
	if first := got.Chunks[0]; first.EndLine-first.StartLine < 100 {
		t.Errorf("hit 1 was not widened (span %d-%d) although the room after placing every hit allowed it",
			first.StartLine, first.EndLine)
	}

	// WidenFirst keeps the top hit wide as it is placed, before the rest.
	keepTop := two
	keepTop.WidenFirst = 1
	got = deliverWithinBudget(nil, hits, root, keepTop, 10, budget, false)
	if spanHolds(got.Chunks, hits[0]) != 1 || spanHolds(got.Chunks, hits[3]) == 0 {
		t.Errorf("widen-first-1 should keep hit 1 wide and still place hit 4: got %d span(s)", len(got.Chunks))
	}
}

// When even the hits do not all fit, two-pass says so.
func TestTwoPassReportsALostHit(t *testing.T) {
	root, hits := packingWorkspace(t)
	two := expandPolicy{TopN: 10, ConstructCap: 300, TwoPass: true}
	tiny := len(renderChunk(1, hits[0], false)) + 10
	got := deliverWithinBudget(nil, hits, root, two, 10, tiny, false)
	if !got.Truncated {
		t.Error("only one hit fits, and Truncated is false")
	}
	if spanHolds(got.Chunks, hits[0]) != 1 {
		t.Error("the top hit must always be kept, as truncateToBudget keeps it")
	}
}

// Without TwoPass, deliverWithinBudget IS the composition it replaced, so
// moving gatherContext onto it changed nothing that ships.
func TestOnePassIsTheOldComposition(t *testing.T) {
	root, hits := packingWorkspace(t)
	direct := []Chunk{chunkContent([]byte("package x\n\nfunc Small() int { return 4 }\n"), "small.go")[0]}
	policy := expandPolicy{TopN: 10, ConstructCap: 300}
	for _, budget := range []int{2000, 9000, 32000} {
		fused := fuseDirectSpans(direct, expandToNeighbours(hits, root, policy), 10, false)
		kept, truncated := truncateToBudget(fused.Chunks, budget, false)
		got := deliverWithinBudget(direct, hits, root, policy, 10, budget, false)
		if !reflect.DeepEqual(got.Chunks, kept) || got.Truncated != truncated ||
			got.InputCount != fused.InputCount || got.SavedBytes != fused.SavedBytes || got.DirectSpans != fused.DirectSpans {
			t.Errorf("budget %d: deliverWithinBudget differs from fuseDirectSpans + truncateToBudget", budget)
		}
	}
}
