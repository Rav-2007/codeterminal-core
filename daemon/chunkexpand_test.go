package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

	expanded := expandToNeighbours([]Chunk{middle}, root, expandNeighbourTopN)
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
	expanded := expandToNeighbours(hits, root, expandNeighbourTopN)

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
	got := expandToNeighbours([]Chunk{stale}, root, expandNeighbourTopN)
	if len(got) != 1 || got[0].ID != stale.ID {
		t.Errorf("a hit whose line range the chunker no longer produces was expanded anyway: %v", chunkIDsOf(got))
	}
}

// Disabled means disabled, and an absent workspace must not panic or read.
func TestExpansionIsOffWhenNotConfigured(t *testing.T) {
	root := expandWorkspace(t)
	all := chunksOf(t, root, "long.go")
	if got := expandToNeighbours(all[:1], root, 0); len(got) != 1 {
		t.Errorf("topN=0 must not expand, got %d chunks", len(got))
	}
	if got := expandToNeighbours(all[:1], "", expandNeighbourTopN); len(got) != 1 {
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
