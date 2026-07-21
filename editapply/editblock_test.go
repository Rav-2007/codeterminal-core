package editapply

import (
	"strings"
	"testing"
)

func TestParseEditBlocks_NoBlocks(t *testing.T) {
	blocks, rejected := ParseEditBlocks("A goroutine is a lightweight thread managed by the Go runtime.")
	if len(rejected) != 0 {
		t.Fatalf("expected no refusals, got %v", rejected)
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

	blocks, rejected := ParseEditBlocks(resp)
	if len(rejected) != 0 {
		t.Fatalf("unexpected refusals: %v", rejected)
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

	blocks, rejected := ParseEditBlocks(resp)
	if len(rejected) != 0 {
		t.Fatalf("unexpected refusals: %v", rejected)
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

	_, rejected := ParseEditBlocks(resp)
	if len(rejected) == 0 {
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

	_, rejected := ParseEditBlocks(resp)
	if len(rejected) == 0 {
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

	_, rejected := ParseEditBlocks(resp)
	if len(rejected) == 0 {
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

	_, rejected := ParseEditBlocks(resp)
	if len(rejected) == 0 {
		t.Fatal("expected an error when a new block starts before the current one terminates, got nil")
	}
}

// TestParseEditBlocks_EmptySearchIsACreateBlock is this test INVERTED (Fix A).
// It used to assert the refusal that made file creation unreachable: the engine
// gained the capability in Fix 7, but every shipped client enters through this
// parser, and the parser threw the block away before the engine ever saw it. An
// empty SEARCH is now a create instruction and must parse.
func TestParseEditBlocks_EmptySearchIsACreateBlock(t *testing.T) {
	resp := strings.Join([]string{
		"path: a.go",
		"<<<<<<< SEARCH",
		"=======",
		"bar",
		">>>>>>> REPLACE",
	}, "\n")

	blocks, rejected := ParseEditBlocks(resp)
	if len(rejected) != 0 {
		t.Fatalf("unexpected refusal for a create block: %v", rejected)
	}
	want := EditBlock{FilePath: "a.go", Search: "", Replace: "bar"}
	if len(blocks) != 1 || blocks[0] != want {
		t.Fatalf("blocks = %+v, want exactly %+v", blocks, want)
	}
	// The engine keys creation off exactly this predicate, so the parser's
	// output has to satisfy it or the block is a no-op edit instead.
	if !IsEmptySearch(blocks[0].Search) {
		t.Error("parsed block does not read as a create block to the engine")
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

	blocks, rejected := ParseEditBlocks(resp)
	if len(rejected) != 0 {
		t.Fatalf("unexpected refusals: %v", rejected)
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

	_, rejected := ParseEditBlocks(resp)
	if len(rejected) == 0 {
		t.Fatal("expected an error when a blank line separates path and SEARCH, got nil")
	}
}
