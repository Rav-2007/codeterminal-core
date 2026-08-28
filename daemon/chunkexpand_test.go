package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixedNeighbourPolicy is the pre-construct behaviour: widen the top hits by one
// window either side and never to a declaration. The tests below were written
// against it and still pin it, because it is the FALLBACK the construct policy
// uses whenever no declaration qualifies -- which is most of the time.
var fixedNeighbourPolicy = expandPolicy{TopN: expandNeighbourTopN}

func expandWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var long strings.Builder
	long.WriteString("package main\n")
	for i := 1; i <= 500; i++ {
		long.WriteString("// line filler so several windows form\n")
	}
	write("long.go", long.String())
	write(".env", "SECRET=sk-must-not-appear\n")
	write(".gitignore", "ignored.go\n")
	write("ignored.go", "package main\n\nfunc Ignored() {}\n")
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func chunksOf(t *testing.T, root, rel string) []Chunk {
	t.Helper()
	return chunksOnDisk(root, rel, newGitignoreMatcher(root))
}

// THE PROPERTY THE FEATURE EXISTS FOR: a hit brings its neighbours with it, and
// after merging they are ONE span rather than three, because the indexer's
// window overlap makes siblings overlap.
func TestExpansionWidensAHitIntoOneContiguousSpan(t *testing.T) {
	root := expandWorkspace(t)
	all := chunksOf(t, root, "long.go")
	if len(all) < 5 {
		t.Fatalf("fixture produced %d chunks; need several to have neighbours", len(all))
	}
	middle := all[2]

	expanded := expandToNeighbours([]Chunk{middle}, root, fixedNeighbourPolicy)
	if len(expanded) != 3 {
		t.Fatalf("expected the hit plus both neighbours, got %d: %v", len(expanded), chunkIDsOf(expanded))
	}
	merged := mergeAdjacentChunks(expanded)
	if len(merged) != 1 {
		t.Errorf("the hit and its neighbours must fold into ONE span, got %d: %v", len(merged), chunkIDsOf(merged))
	}
	// ANTI-VACUITY: the span has to actually be wider than the hit was.
	if got, was := merged[0].EndLine-merged[0].StartLine, middle.EndLine-middle.StartLine; got <= was {
		t.Errorf("the merged span covers %d lines, no more than the original %d", got+1, was+1)
	}
	if merged[0].StartLine > middle.StartLine || merged[0].EndLine < middle.EndLine {
		t.Errorf("the merged span %d-%d does not contain the original hit %d-%d",
			merged[0].StartLine, merged[0].EndLine, middle.StartLine, middle.EndLine)
	}
}

// Only the top N are widened. Expanding everything was MEASURED worse than
// expanding nothing at the shipped budget (26/49 against 25/49 while collapsing
// the span count to 2.8), because widening the tail crowds out whole files.
func TestExpansionOnlyWidensTheTopRankedHits(t *testing.T) {
	root := expandWorkspace(t)
	all := chunksOf(t, root, "long.go")
	if len(all) < expandNeighbourTopN+3 {
		t.Fatalf("fixture too small: %d chunks", len(all))
	}
	// Hits spaced THREE apart, not two. At two the neighbourhoods touch --
	// sibling i+1 of one hit is sibling i-1 of the next -- so a sibling's
	// presence cannot be attributed to the hit that pulled it in, and the
	// assertion below fires on a correct implementation. (It did.)
	var hits []Chunk
	for i := 0; i < len(all) && len(hits) < expandNeighbourTopN+1; i += 3 {
		hits = append(hits, all[i])
	}
	if len(hits) < expandNeighbourTopN+1 {
		t.Fatalf("fixture yields only %d spaced hits; need %d", len(hits), expandNeighbourTopN+1)
	}
	expanded := expandToNeighbours(hits, root, fixedNeighbourPolicy)

	last := hits[len(hits)-1]
	li := -1
	for i := range all {
		if all[i].ID == last.ID {
			li = i
		}
	}
	for _, c := range expanded {
		for _, off := range []int{-1, 1} {
			j := li + off
			if j >= 0 && j < len(all) && c.ID == all[j].ID {
				// Only a violation if that sibling is not itself a hit.
				isHit := false
				for _, hh := range hits {
					if hh.ID == all[j].ID {
						isHit = true
					}
				}
				if !isHit {
					t.Errorf("the rank-%d hit (past the top %d) was widened: %s came along",
						len(hits), expandNeighbourTopN, c.ID)
				}
			}
		}
	}
	if len(expanded) <= len(hits) {
		t.Errorf("nothing was expanded at all (%d in, %d out), so this proves nothing", len(hits), len(expanded))
	}
}

// Expansion reads from disk, so it is a second way for a file to reach a
// prompt. It must be gated exactly as the indexer is -- shouldSkipFile -- or it
// becomes a weaker parallel exclusion surface, which is the failure repomap.go
// documents at length for the map.
func TestExpansionRefusesWhatTheIndexerRefusesToRead(t *testing.T) {
	root := expandWorkspace(t)
	for _, forbidden := range []string{".env", "ignored.go"} {
		if got := chunksOf(t, root, forbidden); len(got) != 0 {
			t.Errorf("expansion would read %s, which the indexer refuses: %v", forbidden, chunkIDsOf(got))
		}
	}
	// ANTI-VACUITY: an implementation that read nothing would pass the above.
	if got := chunksOf(t, root, "long.go"); len(got) == 0 {
		t.Fatal("expansion reads nothing at all, so the refusals above are meaningless")
	}
}

// A file edited since indexing no longer produces the hit's line range. Widening
// around a position that does not exist would be guessing, so the hit is left
// alone rather than paired with whatever now sits at those lines.
func TestExpansionLeavesAHitAloneWhenItsFileHasMovedUnderIt(t *testing.T) {
	root := expandWorkspace(t)
	stale := Chunk{ID: "long.go:7-59", FilePath: "long.go", StartLine: 7, EndLine: 59, Content: "old"}
	got := expandToNeighbours([]Chunk{stale}, root, fixedNeighbourPolicy)
	if len(got) != 1 || got[0].ID != stale.ID {
		t.Errorf("a hit whose line range the chunker no longer produces was expanded anyway: %v", chunkIDsOf(got))
	}
}

// Disabled means disabled, and an absent workspace must not panic or read.
func TestExpansionIsOffWhenNotConfigured(t *testing.T) {
	root := expandWorkspace(t)
	all := chunksOf(t, root, "long.go")
	if got := expandToNeighbours(all[:1], root, expandPolicy{}); len(got) != 1 {
		t.Errorf("topN=0 must not expand, got %d chunks", len(got))
	}
	if got := expandToNeighbours(all[:1], "", fixedNeighbourPolicy); len(got) != 1 {
		t.Errorf("an empty workspace root must not expand, got %d chunks", len(got))
	}
}

func chunkIDsOf(cs []Chunk) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

// constructWorkspace writes a file holding one function far longer than a single
// 40-line window, followed by a second declaration, so a hit landing inside the
// long one can be checked for whether widening stops at the right place.
func constructWorkspace(t *testing.T, bodyLines int) string {
	t.Helper()
	root := t.TempDir()
	var b strings.Builder
	b.WriteString("package main\n\n")
	b.WriteString("// Long is the construct under test.\n")
	b.WriteString("func Long() {\n")
	for i := 1; i <= bodyLines; i++ {
		b.WriteString("\tstep()\n")
	}
	b.WriteString("}\n\n")
	b.WriteString("// After must never be pulled in by widening Long.\n")
	b.WriteString("func After() {\n\treturn\n}\n")
	if err := os.WriteFile(filepath.Join(root, "long.go"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// THE PROPERTY THE CONSTRUCT POLICY EXISTS FOR: a hit in the middle of a long
// function brings back the WHOLE function, not one window either side.
//
// This is what the +-1 policy could not do. Measured over the locate eval, most
// of the remaining misses are the right file with the wrong forty lines of it,
// and for six of them the answer sits inside the same declaration as a chunk
// that was already retrieved.
func TestWideningReachesTheWholeEnclosingConstruct(t *testing.T) {
	root := constructWorkspace(t, 150)
	all := chunksOf(t, root, "long.go")
	if len(all) < 5 {
		t.Fatalf("fixture produced %d chunks; need several windows", len(all))
	}
	mid := all[len(all)/2]

	expanded := expandToNeighbours([]Chunk{mid}, root, expandPolicy{TopN: 1, ConstructCap: 300})
	spans := mergeAdjacentChunks(expanded)
	if len(spans) != 1 {
		t.Fatalf("widening produced %d spans, want 1 contiguous region", len(spans))
	}
	got := spans[0]

	lines := strings.Split(readFileForTest(t, root, "long.go"), "\n")
	var want [2]int
	for _, e := range constructExtents(lines) {
		if e[0] <= mid.StartLine && mid.StartLine <= e[1] {
			want = e
		}
	}
	if want[0] == 0 {
		t.Fatal("the fixture's long function was not found by constructExtents")
	}
	// Chunks are 40-line windows, so the span rounds outward to window edges; it
	// must COVER the declaration, which is what containment scoring asks.
	if got.StartLine > want[0] || got.EndLine < want[1] {
		t.Errorf("widened span is %d-%d, which does not cover the construct at %d-%d",
			got.StartLine, got.EndLine, want[0], want[1])
	}
	if !strings.Contains(got.Content, "func Long() {") {
		t.Error("the widened span does not contain the declaration it was widened to")
	}
}

// AN OVERSIZED CONSTRUCT FALLS BACK, IT DOES NOT WIDEN TO NOTHING.
//
// A construct past the cap is exactly where a hit is most likely to be one
// window away from its answer, so refusing to widen there would regress hits the
// pre-construct policy already caught. The fallback is what stops this change
// from being a trade instead of an addition.
func TestAConstructPastTheCapFallsBackToFixedNeighbours(t *testing.T) {
	root := constructWorkspace(t, 400) // far past any cap under test
	all := chunksOf(t, root, "long.go")
	mid := all[len(all)/2]

	capped := expandToNeighbours([]Chunk{mid}, root, expandPolicy{TopN: 1, ConstructCap: 50})
	if len(capped) != 3 {
		t.Fatalf("got %d chunks, want the hit plus its two fixed neighbours; widening to an "+
			"oversized construct must fall back, not return the hit alone", len(capped))
	}
	uncapped := expandToNeighbours([]Chunk{mid}, root, expandPolicy{TopN: 1, ConstructCap: 1000})
	if len(uncapped) <= len(capped) {
		t.Fatalf("with the cap raised past the construct's size, widening returned %d chunks and "+
			"the capped run returned %d; the cap is not actually binding", len(uncapped), len(capped))
	}
}

// THE CAP IS A BUDGET GUARD, so it has to bound what is actually delivered.
func TestTheCapBoundsHowMuchWideningCanDeliver(t *testing.T) {
	root := constructWorkspace(t, 400)
	all := chunksOf(t, root, "long.go")
	mid := all[len(all)/2]
	for _, capLines := range []int{50, 120, 1000} {
		spans := mergeAdjacentChunks(expandToNeighbours([]Chunk{mid}, root, expandPolicy{TopN: 1, ConstructCap: capLines}))
		total := 0
		for _, s := range spans {
			total += s.EndLine - s.StartLine + 1
		}
		// One window plus its two neighbours is the floor; the cap plus a window
		// of rounding is the ceiling.
		if ceiling := capLines + 2*chunkLines; total > ceiling {
			t.Errorf("cap %d delivered %d lines, past the %d ceiling", capLines, total, ceiling)
		}
	}
}

func readFileForTest(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// THE PROPERTY: the expansion policy a Server is built with is the one
// gatherContext actually uses.
//
// This is a wiring test, and it exists because the wiring is the part that
// silently rots. context.go called the package-level defaultExpandPolicy
// directly until the policy became per-Server; had the field been added and the
// call site left alone, everything would still compile, every existing test
// would still pass, and the A/B that motivated the field would have measured one
// arm twice and reported a dead heat. Neuter s.resolvedExpandPolicy() back to
// defaultExpandPolicy and this fails.
func TestTheServersOwnExpansionPolicyIsTheOneThatRuns(t *testing.T) {
	root := expandWorkspace(t)
	all := chunksOf(t, root, "long.go")
	if len(all) < 3 {
		t.Fatalf("premise broken: long.go produced %d chunks, need at least 3 to observe widening", len(all))
	}
	// A middle chunk, so there is a sibling on each side to gain.
	hit := all[len(all)/2]

	newServer := func(pol *expandPolicy) *Server {
		return &Server{
			logger:             discardLogger(),
			workspace:          root,
			embedder:           &fakeEmbedder{dim: embedDim},
			store:              fixedStore{chunks: []Chunk{hit}},
			retrievalTopK:      1,
			contextBudgetChars: 1 << 20, // never binding: this is about expansion, not budget
			rerankDisabled:     true,
			expandPolicy:       pol,
			cfg:                &Config{},
		}
	}

	widened := newServer(nil).gatherContext(context.Background(), "anything")
	if widened.Skipped {
		t.Fatalf("retrieval skipped: %s", widened.Reason)
	}
	// The default policy widens, and the siblings overlap by construction, so
	// they fold into one span covering strictly more lines than the hit alone.
	if got := widened.Chunks[0].EndLine - widened.Chunks[0].StartLine; got <= hit.EndLine-hit.StartLine {
		t.Errorf("with the default policy the delivered span covers %d lines, no more than the "+
			"un-widened hit's %d -- expansion did not run at all",
			got+1, hit.EndLine-hit.StartLine+1)
	}

	// TopN 0 is "expansion off", and it must be reachable -- which is why the
	// field is a pointer. A plain value could not tell "off" from "unset".
	off := newServer(&expandPolicy{}).gatherContext(context.Background(), "anything")
	if off.Skipped {
		t.Fatalf("retrieval skipped: %s", off.Reason)
	}
	if len(off.Chunks) != 1 || off.Chunks[0].StartLine != hit.StartLine || off.Chunks[0].EndLine != hit.EndLine {
		t.Errorf("with expansion off the delivered set is %v, want exactly the one un-widened hit %s -- "+
			"the Server's policy is being ignored in favour of the package default",
			chunkIDs(off.Chunks), fmt.Sprintf("%s:%d-%d", hit.FilePath, hit.StartLine, hit.EndLine))
	}
}
