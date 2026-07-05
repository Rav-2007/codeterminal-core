package main

import (
	"strings"
	"testing"
)

func TestParseEditBlocks_NoBlocks(t *testing.T) {
	blocks, err := ParseEditBlocks("A goroutine is a lightweight thread managed by the Go runtime.")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(blocks) != 0 {
		t.Fatalf("expected 0 blocks, got %d", len(blocks))
	}
}

func TestParseEditBlocks_SingleBlock(t *testing.T) {
	resp := strings.Join([]string{
		"Here's the change:",
		"",
		"path: main.go",
		"<<<<<<< SEARCH",
		"func main() {",
		"=======",
		"func hello() {}",
		"",
		"func main() {",
		">>>>>>> REPLACE",
		"",
		"That adds a hello function.",
	}, "\n")

	blocks, err := ParseEditBlocks(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	b := blocks[0]
	if b.FilePath != "main.go" {
		t.Errorf("FilePath = %q, want %q", b.FilePath, "main.go")
	}
	if b.Search != "func main() {" {
		t.Errorf("Search = %q, want %q", b.Search, "func main() {")
	}
	wantReplace := "func hello() {}\n\nfunc main() {"
	if b.Replace != wantReplace {
		t.Errorf("Replace = %q, want %q", b.Replace, wantReplace)
	}
}

func TestParseEditBlocks_MultipleBlocksInOrder(t *testing.T) {
	resp := strings.Join([]string{
		"path: a.go",
		"<<<<<<< SEARCH",
		"foo",
		"=======",
		"bar",
		">>>>>>> REPLACE",
		"path: b.go",
		"<<<<<<< SEARCH",
		"baz",
		"=======",
		"qux",
		">>>>>>> REPLACE",
	}, "\n")

	blocks, err := ParseEditBlocks(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].FilePath != "a.go" || blocks[1].FilePath != "b.go" {
		t.Errorf("blocks out of order: %+v", blocks)
	}
}

func TestParseEditBlocks_MissingPathLine(t *testing.T) {
	resp := strings.Join([]string{
		"<<<<<<< SEARCH",
		"foo",
		"=======",
		"bar",
		">>>>>>> REPLACE",
	}, "\n")

	_, err := ParseEditBlocks(resp)
	if err == nil {
		t.Fatal("expected an error for missing path line, got nil")
	}
}

func TestParseEditBlocks_UnterminatedNoSeparator(t *testing.T) {
	resp := strings.Join([]string{
		"path: a.go",
		"<<<<<<< SEARCH",
		"foo",
		"bar",
	}, "\n")

	_, err := ParseEditBlocks(resp)
	if err == nil {
		t.Fatal("expected an error for unterminated SEARCH block, got nil")
	}
}

func TestParseEditBlocks_UnterminatedNoReplaceMarker(t *testing.T) {
	resp := strings.Join([]string{
		"path: a.go",
		"<<<<<<< SEARCH",
		"foo",
		"=======",
		"bar",
	}, "\n")

	_, err := ParseEditBlocks(resp)
	if err == nil {
		t.Fatal("expected an error for missing REPLACE marker, got nil")
	}
}

func TestParseEditBlocks_UnterminatedBeforeNextBlock(t *testing.T) {
	resp := strings.Join([]string{
		"path: a.go",
		"<<<<<<< SEARCH",
		"foo",
		"path: b.go",
		"<<<<<<< SEARCH",
		"baz",
		"=======",
		"qux",
		">>>>>>> REPLACE",
	}, "\n")

	_, err := ParseEditBlocks(resp)
	if err == nil {
		t.Fatal("expected an error when a new block starts before the current one terminates, got nil")
	}
}

func TestParseEditBlocks_EmptySearch(t *testing.T) {
	resp := strings.Join([]string{
		"path: a.go",
		"<<<<<<< SEARCH",
		"=======",
		"bar",
		">>>>>>> REPLACE",
	}, "\n")

	_, err := ParseEditBlocks(resp)
	if err == nil {
		t.Fatal("expected an error for empty SEARCH, got nil")
	}
}

func TestParseEditBlocks_EmptyReplaceIsValid(t *testing.T) {
	resp := strings.Join([]string{
		"path: a.go",
		"<<<<<<< SEARCH",
		"delete me",
		"=======",
		">>>>>>> REPLACE",
	}, "\n")

	blocks, err := ParseEditBlocks(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Replace != "" {
		t.Fatalf("expected 1 block with empty Replace, got %+v", blocks)
	}
}

func TestParseEditBlocks_BlankLineBetweenPathAndSearchIsMissingPath(t *testing.T) {
	resp := strings.Join([]string{
		"path: a.go",
		"",
		"<<<<<<< SEARCH",
		"foo",
		"=======",
		"bar",
		">>>>>>> REPLACE",
	}, "\n")

	_, err := ParseEditBlocks(resp)
	if err == nil {
		t.Fatal("expected an error when a blank line separates path and SEARCH, got nil")
	}
}
