package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
