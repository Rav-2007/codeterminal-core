package editapply

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file is the before/after evidence for Fix A: file creation existed in
// the engine (Fix 7) and was unreachable from every shipped client, because
// ParseEditBlocks refused an empty SEARCH section before the engine was ever
// consulted. Every production caller -- the CLI (daemon/apply_cmd.go), the
// daemon's EditProposals the VS Code panel renders (daemon/server.go), and the
// TUI (clients/tui/chat.go) -- enters here, so "the engine can create files"
// was true and useless at the same time.
//
// Run against the pre-fix parser, every "ParsesAsCreate" case FAILS with
// "line N: SEARCH block is empty". The refusal cases pass before and after --
// they are the Fix-4 guarantees this change had to leave standing.

// TestParseCreate_EmptySearchParsesAsCreate is the repro, at the parser.
func TestParseCreate_EmptySearchParsesAsCreate(t *testing.T) {
	response := strings.Join([]string{
		"Here is the new file:",
		"",
		"path: notes.md",
		searchMarker,
		separatorMarker,
		"# Notes",
		"hello",
		replaceMarker,
	}, "\n")

	block := mustParseOne(t, response)
	want := EditBlock{FilePath: "notes.md", Search: "", Replace: "# Notes\nhello"}
	if block != want {
		t.Errorf("block = %+v, want %+v", block, want)
	}
}

// TestParseCreate_WhitespaceOnlySearchParsesAsCreate keeps the parser and the
// engine agreeing on what "empty" means. IsEmptySearch treats a whitespace-only
// SEARCH as create intent, so a parser that accepted only a zero-line SEARCH
// would hand the engine a block it reads as a create while the parser thought
// it was an edit.
func TestParseCreate_WhitespaceOnlySearchParsesAsCreate(t *testing.T) {
	response := strings.Join([]string{
		"path: notes.md",
		searchMarker,
		"   ",
		"",
		separatorMarker,
		"content",
		replaceMarker,
	}, "\n")

	block := mustParseOne(t, response)
	if !IsEmptySearch(block.Search) {
		t.Errorf("Search = %q, want the engine to read it as create intent", block.Search)
	}
	if block.Replace != "content" {
		t.Errorf("Replace = %q, want %q", block.Replace, "content")
	}
}

// TestParseCreate_EmptyCreateContentIsAllowed: "create this file with nothing
// in it" is a coherent instruction and has exactly one reading.
func TestParseCreate_EmptyCreateContentIsAllowed(t *testing.T) {
	response := strings.Join([]string{
		"path: .keep",
		searchMarker,
		separatorMarker,
		replaceMarker,
	}, "\n")

	block := mustParseOne(t, response)
	want := EditBlock{FilePath: ".keep", Search: "", Replace: ""}
	if block != want {
		t.Errorf("block = %+v, want %+v", block, want)
	}
}

// TestParseCreate_AmbiguousCreateStillRefused is the Fix-4 preservation case
// that Fix A could most plausibly have broken. The first separator sits
// immediately after the SEARCH marker, so this LOOKS like a create -- but a
// second separator before the REPLACE marker means it reads equally well as an
// edit whose SEARCH section begins with a bare "=======". Both readings are
// available, so the parser must refuse rather than pick one, exactly as it does
// for any other multi-divider block.
func TestParseCreate_AmbiguousCreateStillRefused(t *testing.T) {
	response := strings.Join([]string{
		"path: notes.md",
		searchMarker,
		separatorMarker,
		"some content",
		separatorMarker,
		"replacement",
		replaceMarker,
	}, "\n")

	blocks, rejected := ParseEditBlocks(response)
	if len(rejected) == 0 {
		t.Fatalf("GUESSED: parser returned %+v for a block with two readings; want a refusal", blocks)
	}
	if len(blocks) != 0 {
		t.Errorf("got %d blocks alongside the refusal, want none", len(blocks))
	}
	if !strings.Contains(rejected[0].Reason, "ambiguous") {
		t.Errorf("refusal = %v, want it to name the ambiguity", rejected[0])
	}
}

// TestParseCreate_CreateBlockReachesTheEngine closes the loop the review found
// open: a block parsed from a raw response, handed to PrepareEdit exactly as
// every production caller hands it over, brings the file into existence. Fix 7
// was verified by calling PrepareEdit directly, which is why nobody noticed the
// door was locked.
func TestParseCreate_CreateBlockReachesTheEngine(t *testing.T) {
	root := realTempDir(t)
	response := strings.Join([]string{
		"path: pkg/sub/new.go",
		searchMarker,
		separatorMarker,
		"package sub",
		replaceMarker,
	}, "\n")

	block := mustParseOne(t, response)
	prepared, err := PrepareEdit(root, block)
	if err != nil {
		t.Fatalf("PrepareEdit on a parsed create block: %v", err)
	}
	if !prepared.Creates {
		t.Error("Creates = false, want true")
	}
	backupDir, _ := NewBackupSessionDir(root)
	if err := Apply(root, prepared, backupDir); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "pkg/sub/new.go")); got != "package sub" {
		t.Errorf("created file = %q, want %q", got, "package sub")
	}
}

// TestParseCreate_ProtectedTargetsStillRefusedFromTheParser is the gate that
// matters most now that creation is reachable: a create block is untrusted
// model output, and reaching the engine through the parser must not become a
// way around any Tier-1 refusal. Same table as create_test.go's engine-level
// case, entered through the production door instead.
func TestParseCreate_ProtectedTargetsStillRefusedFromTheParser(t *testing.T) {
	for _, tc := range []struct{ name, path, wantIn string }{
		{"git hook", ".git/hooks/pre-commit", "refusing"},
		{"git config", ".git/config", "refusing"},
		{"codeterminal backup", ".codeterminal/backups/x/before/f.txt", "refusing"},
		{"ssh dir", ".ssh/authorized_keys", "refusing"},
		{"secret name", ".env", "secret-file"},
		{"absolute path", "/tmp/evil.txt", "absolute"},
		{"dotdot escape", "../evil.txt", "escapes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := realTempDir(t)
			response := strings.Join([]string{
				"path: " + tc.path,
				searchMarker,
				separatorMarker,
				"pwned",
				replaceMarker,
			}, "\n")

			block := mustParseOne(t, response)
			if _, err := PrepareEdit(root, block); err == nil {
				t.Fatalf("CREATED A PROTECTED PATH through the parser: %s", tc.path)
			} else if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantIn)
			}
			if _, statErr := os.Stat(filepath.Join(root, tc.path)); statErr == nil {
				t.Errorf("%s exists on disk despite the refusal", tc.path)
			}
		})
	}
}
