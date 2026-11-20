package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GAP FILL (gapfill.go): off by default; when on, it joins two delivered spans
// of ONE file whose gap fits the setting, and only in budget that is left.
//
// Neuter check: make gapFiller's returned function return nil.
func TestGapFillJoinsCloseSpansOfOneFileInTheRoomLeft(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&b, "line %d of a file whose middle answers the question\n", i)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	pick := func(file string, start int) Chunk {
		for _, c := range chunksOnDisk(realRoot, file, newGitignoreMatcher(realRoot)) {
			if c.StartLine == start {
				return c
			}
		}
		t.Fatalf("%s has no chunk starting at line %d", file, start)
		return Chunk{}
	}
	covers := func(chunks []Chunk, file string, line int) bool {
		for _, c := range chunks {
			if c.FilePath == file && c.StartLine <= line && line <= c.EndLine {
				return true
			}
		}
		return false
	}
	// Lines 1-40 and 121-160 of a.txt: an 80-line gap, 41-120. No widening, so
	// the gap step is the only thing that can reach line 80.
	hits := []Chunk{pick("a.txt", 1), pick("a.txt", 121)}
	deliver := func(gap, budget int, h []Chunk) []Chunk {
		return deliverWithinBudget(nil, h, root, expandPolicy{TwoPass: true, GapFill: gap}, defaultK, budget, false).Chunks
	}

	if got := deliver(0, defaultContextBudgetChars, hits); covers(got, "a.txt", 80) {
		t.Error("GapFill 0 filled a gap: the lever must be off by default")
	}
	if got := deliver(90, defaultContextBudgetChars, hits); !covers(got, "a.txt", 80) || len(got) != 1 {
		t.Errorf("GapFill 90 did not join an 80-line gap into one span: %d span(s), line 80 covered %v", len(got), covers(got, "a.txt", 80))
	}
	if got := deliver(60, defaultContextBudgetChars, hits); covers(got, "a.txt", 80) {
		t.Error("GapFill 60 filled an 80-line gap")
	}
	twoSpans := 0
	for _, c := range deliver(0, defaultContextBudgetChars, hits) {
		twoSpans += len(renderChunk(1, c, false))
	}
	if got := deliver(90, twoSpans+200, hits); covers(got, "a.txt", 80) || !covers(got, "a.txt", 121) {
		t.Error("a gap was filled with no room left for it, or it displaced a hit")
	}
	if got := deliver(1000, defaultContextBudgetChars, []Chunk{pick("a.txt", 1), pick("b.txt", 121)}); covers(got, "a.txt", 80) || covers(got, "b.txt", 80) {
		t.Error("spans of two different files were joined")
	}
}
