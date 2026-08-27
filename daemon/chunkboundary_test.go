package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// EVERY LINE MUST SURVIVE. This is the invariant the whole rewrite could most
// easily break and the one nothing would notice: a chunker that silently drops
// a line drops it from search, and the index still reports itself healthy. It
// is checked against this repository's own sources rather than a fixture,
// because the shapes that break a windowing loop -- a file shorter than one
// window, a file of one enormous function, a file that is all comments -- are
// already here in quantity.
func TestEveryLineOfEveryFileLandsInSomeChunk(t *testing.T) {
	checked := 0
	err := filepath.WalkDir("..", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		// WALK WHAT THE INDEXER WALKS. Without this the corpus was whatever
		// happened to be on disk: node_modules alone put 11,099 files through
		// here, so the test's strength depended on whether anyone had run `npm
		// install`, and the "enough files" guard below would still have passed
		// on a checkout without one. isPrunedDir is the indexer's own rule, so
		// this measures exactly the files that can really reach chunkContent.
		if d.IsDir() {
			if isPrunedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(p) {
		case ".go", ".ts", ".js", ".py", ".md":
		default:
			return nil
		}
		content, err := os.ReadFile(p)
		if err != nil || len(content) == 0 {
			return nil
		}
		lines := splitLines(content)
		if len(lines) == 0 {
			return nil
		}
		checked++
		seen := make([]bool, len(lines))
		for _, c := range chunkContent(content, p) {
			if c.StartLine < 1 || c.EndLine > len(lines) || c.StartLine > c.EndLine {
				t.Fatalf("%s: chunk %s is outside the file (%d lines)", p, c.ID, len(lines))
			}
			for i := c.StartLine - 1; i < c.EndLine; i++ {
				seen[i] = true
			}
		}
		for i, ok := range seen {
			if !ok {
				t.Fatalf("%s: line %d is in no chunk, so it cannot be retrieved at any k", p, i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 100 {
		t.Fatalf("only %d files checked; this test is not exercising the corpus it claims to", checked)
	}
	t.Logf("every line of %d real files lands in a chunk", checked)
}

// The point of the change: a cut lands on a construct boundary when one is in
// reach, so a chunk opens with a declaration rather than in the middle of a body.
func TestChunksStartAtConstructBoundaries(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n\n")
	// Twelve functions of eight lines each: boundaries are always in reach.
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&b, "// Fn%02d does a thing.\nfunc Fn%02d() {\n", i, i)
		for j := 0; j < 5; j++ {
			fmt.Fprintf(&b, "\tstep(%d)\n", j)
		}
		b.WriteString("}\n\n")
	}
	content := []byte(b.String())
	lines := splitLines(content)

	chunks := chunkContent(content, "p/x.go")
	if len(chunks) < 2 {
		t.Fatalf("one chunk holds the whole fixture, so boundaries were never chosen: %d", len(chunks))
	}
	for _, c := range chunks[1:] { // the first chunk starts at line 1 by definition
		first := lines[c.StartLine-1]
		if strings.HasPrefix(first, " ") || strings.HasPrefix(first, "\t") {
			t.Errorf("chunk %s opens mid-body on %q", c.ID, first)
		}
	}
}

// A DOC COMMENT BELONGS TO WHAT IT DOCUMENTS. Cutting at the `func` line
// strands the comment on the previous chunk, describing something that is not
// there, and opens the new chunk with a signature stripped of its explanation.
func TestADocCommentLeadsItsDeclaration(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n\n")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&b, "// Target%02d is documented right here, and this sentence is the only\n", i)
		fmt.Fprintf(&b, "// thing that explains what Target%02d is actually for.\nfunc Target%02d() {\n", i, i)
		for j := 0; j < 6; j++ {
			fmt.Fprintf(&b, "\twork(%d)\n", j)
		}
		b.WriteString("}\n\n")
	}
	content := []byte(b.String())
	lines := splitLines(content)

	for _, c := range chunkContent(content, "p/x.go") {
		first := lines[c.StartLine-1]
		if !strings.HasPrefix(first, "func Target") {
			continue
		}
		t.Errorf("chunk %s opens on %q, orphaning the comment block that documents it "+
			"onto the end of the previous chunk", c.ID, first)
	}
}

// A clean cut needs no overlap; a forced one still does. The overlap is the
// only thing that keeps a construct larger than one window whole SOMEWHERE, so
// dropping it unconditionally would trade one defect for another.
func TestOverlapIsSpentOnlyWhereTheCutIsForced(t *testing.T) {
	var clean strings.Builder
	clean.WriteString("package p\n\n")
	for i := 0; i < 14; i++ {
		fmt.Fprintf(&clean, "func Small%02d() {\n\tx(%d)\n}\n\n", i, i)
	}
	cleanChunks := chunkContent([]byte(clean.String()), "p/clean.go")

	// One enormous function: no boundary is ever in reach.
	var long strings.Builder
	long.WriteString("package p\n\nfunc Enormous() {\n")
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&long, "\tstatement(%d)\n", i)
	}
	long.WriteString("}\n")
	longChunks := chunkContent([]byte(long.String()), "p/long.go")

	overlaps := func(cs []Chunk) int {
		n := 0
		for i := 1; i < len(cs); i++ {
			if cs[i].StartLine <= cs[i-1].EndLine {
				n++
			}
		}
		return n
	}
	if got := overlaps(cleanChunks); got != 0 {
		t.Errorf("%d overlapping seams among %d chunks cut at real boundaries; the duplicated "+
			"lines cost prompt budget and exist to solve a problem a clean cut does not have",
			got, len(cleanChunks))
	}
	if len(longChunks) < 3 {
		t.Fatalf("the long-function fixture produced %d chunks, too few to have any seams", len(longChunks))
	}
	if got := overlaps(longChunks); got == 0 {
		t.Errorf("a 300-line function was cut into %d chunks with NO overlap, so a construct "+
			"larger than one window is now whole in none of them", len(longChunks))
	}
}

// A line count bounds nothing when the lines are not human-sized. A minified
// bundle or an embedded blob must not assemble a chunk the embedder reads half of.
func TestAChunkIsBoundedInBytesNotJustLines(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "const blob%02d = \"%s\";\n", i, strings.Repeat("x", 3000))
	}
	content := []byte(b.String())
	chunks := chunkContent(content, "web/bundle.js")
	if len(chunks) == 0 {
		t.Fatal("no chunks")
	}
	for _, c := range chunks {
		if len(c.Content) > maxChunkBytes+3200 {
			t.Errorf("chunk %s is %d bytes, past the %d-byte ceiling by more than one long line",
				c.ID, len(c.Content), maxChunkBytes)
		}
	}
}

// Adversarial shapes must terminate. The loop advances by a computed end rather
// than a fixed stride now, so "does it always move forward" stopped being
// obvious from reading it.
func TestChunkingTerminatesOnAdversarialInput(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"one line":         "package p\n",
		"only comments":    strings.Repeat("// nothing here\n", 200),
		"only blanks":      strings.Repeat("\n", 200),
		"every line decls": strings.Repeat("func A() {}\n", 200),
		"one long line":    strings.Repeat("z", 200000) + "\n",
		// The shape that actually broke it: MANY long lines. Each chunk is cut
		// short by the byte ceiling, and subtracting the overlap from a chunk
		// shorter than the overlap sent the next start backwards -- negative,
		// in fact, which panicked on a slice index before it could loop forever.
		"many long lines": strings.Repeat(strings.Repeat("z", 9000)+"\n", 20),
		"no trailing nl":  "package p\nfunc A() {}",
	}
	for name, src := range cases {
		done := make(chan int, 1)
		go func() { done <- len(chunkContent([]byte(src), "x.go")) }()
		select {
		case n := <-done:
			t.Logf("%-16s -> %d chunks", name, n)
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: chunkContent did not terminate", name)
		}
	}
}
