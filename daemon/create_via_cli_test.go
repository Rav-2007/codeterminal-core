package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/editapply"
)

// This file tests file creation and parser recovery THROUGH THE PRODUCTION
// ENTRY POINT -- the raw model response, parsed by editapply.ParseEditBlocks
// and driven by the CLI's own confirm loop, exactly as `edits apply` does it.
//
// That is the lesson of the Fix 7 spot-check. Fix 7's regression tests called
// PrepareEdit directly and passed, while the parser sitting in front of every
// shipped client rejected the same blocks before the engine was ever reached.
// A fix verified at the engine layer proves the engine works; it proves nothing
// about whether a user can get there. These tests enter through the same door
// the user does.

// applyResponse runs a raw model response through the real parse-then-apply
// path the CLI uses, answering every confirmation prompt with answers.
func applyResponse(t *testing.T, root, response, answers string) (string, error) {
	t.Helper()
	blocks, rejected := editapply.ParseEditBlocks(response)
	var out bytes.Buffer
	err := applyEditBlocks(root, blocks, rejected, strings.NewReader(answers), &out, discardLogger())
	return out.String(), err
}

// TestCLI_CreateBlockCreatesTheFile is the original failure: a create block sent
// through `edits apply` died at "line N: SEARCH block is empty" and no file was
// ever written.
func TestCLI_CreateBlockCreatesTheFile(t *testing.T) {
	root := realTempDir(t)
	response := strings.Join([]string{
		"Here is the new file:",
		"",
		"path: notes.md",
		"<<<<<<< SEARCH",
		"=======",
		"# Notes",
		"hello",
		">>>>>>> REPLACE",
	}, "\n")

	out, err := applyResponse(t, root, response, "y\n")
	if err != nil {
		t.Fatalf("applying a create block through the CLI path: %v\n%s", err, out)
	}
	if got := readFileString(t, filepath.Join(root, "notes.md")); got != "# Notes\nhello" {
		t.Errorf("created file = %q, want the REPLACE content", got)
	}
	if !strings.Contains(out, "1 applied, 0 skipped, 0 refused") {
		t.Errorf("summary = %q, want 1 applied", out)
	}
}

// TestCLI_CreateIntoNewNestedDir: the create path brings parent directories
// into existence, and confinement still resolves through the deepest EXISTING
// ancestor rather than failing on the missing leaf.
func TestCLI_CreateIntoNewNestedDir(t *testing.T) {
	root := realTempDir(t)
	response := strings.Join([]string{
		"path: pkg/sub/deep/new.go",
		"<<<<<<< SEARCH",
		"=======",
		"package deep",
		">>>>>>> REPLACE",
	}, "\n")

	if out, err := applyResponse(t, root, response, "y\n"); err != nil {
		t.Fatalf("applyResponse: %v\n%s", err, out)
	}
	if got := readFileString(t, filepath.Join(root, "pkg/sub/deep/new.go")); got != "package deep" {
		t.Errorf("created file = %q", got)
	}
}

// TestCLI_CreateOfProtectedTargetRefused is the gate that has to hold now that
// creation is reachable from untrusted model output: being able to CREATE a git
// hook is exactly as bad as being able to overwrite one, and going through the
// parser must not become a way around the Fix-3 refusal.
func TestCLI_CreateOfProtectedTargetRefused(t *testing.T) {
	for _, tc := range []struct{ name, path string }{
		{"git hook", ".git/hooks/pre-commit"},
		{"git config", ".git/config"},
		{"backup dir", ".mochiii/backups/x/before/f.txt"},
		{"ssh dir", ".ssh/authorized_keys"},
		{"secret name", ".env"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := realTempDir(t)
			response := strings.Join([]string{
				"path: " + tc.path,
				"<<<<<<< SEARCH",
				"=======",
				"pwned",
				">>>>>>> REPLACE",
			}, "\n")

			out, err := applyResponse(t, root, response, "y\n")
			if err != nil {
				t.Fatalf("applyResponse: %v", err)
			}
			if !strings.Contains(out, "REFUSED") {
				t.Errorf("output = %q, want a refusal", out)
			}
			if _, statErr := os.Stat(filepath.Join(root, tc.path)); statErr == nil {
				t.Errorf("CREATED A PROTECTED PATH through the CLI: %s", tc.path)
			}
		})
	}
}

// TestCLI_MixedResponseAppliesValidBlockAndReportsBadOne is the reproduced data
// loss (Fix B), at the CLI. A two-block response carrying one good edit and one
// unparseable block used to apply NOTHING: the parse error failed the whole
// response, and the good edit -- already computed, already correct -- was
// thrown away without ever being mentioned.
func TestCLI_MixedResponseAppliesValidBlockAndReportsBadOne(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "main.go", "package main\n\nfunc old() {}\n")

	response := strings.Join([]string{
		"path: main.go",
		"<<<<<<< SEARCH",
		"func old() {}",
		"=======",
		"func renamed() {}",
		">>>>>>> REPLACE",
		"",
		"path: notes.md",
		"<<<<<<< SEARCH",
		"alpha",
		"=======",
		"beta",
		"=======",
		"gamma",
		">>>>>>> REPLACE",
	}, "\n")

	out, err := applyResponse(t, root, response, "y\n")
	if err != nil {
		t.Fatalf("applyResponse: %v\n%s", err, out)
	}

	got := readFileString(t, filepath.Join(root, "main.go"))
	if !strings.Contains(got, "func renamed() {}") {
		t.Errorf("DATA LOSS: main.go = %q, want the valid edit applied despite the bad sibling", got)
	}
	// The bad block is reported, not silently dropped -- with the reason and
	// the line, so the user can see what they did not get and why.
	if !strings.Contains(out, "REFUSED") || !strings.Contains(out, "ambiguous") {
		t.Errorf("output = %q, want the refused block reported with its reason", out)
	}
	if !strings.Contains(out, "1 applied, 0 skipped, 1 refused") {
		t.Errorf("summary = %q, want 1 applied and 1 refused", out)
	}
}

// TestCLI_AllBlocksUnparseableFailsClearly is the control: recovery must not
// quietly turn a hopeless response into a successful run. Nothing was applied,
// so the command has to say so and exit non-zero.
func TestCLI_AllBlocksUnparseableFailsClearly(t *testing.T) {
	root := realTempDir(t)
	response := strings.Join([]string{
		"path: a.go",
		"<<<<<<< SEARCH",
		"one",
		"=======",
		"two",
		"=======",
		"three",
		">>>>>>> REPLACE",
	}, "\n")

	out, err := applyResponse(t, root, response, "y\n")
	if err == nil {
		t.Fatalf("expected a non-nil error when nothing was parseable; output = %q", out)
	}
	if !strings.Contains(out, "0 applied, 0 skipped, 1 refused") {
		t.Errorf("summary = %q, want it to report nothing applied", out)
	}
}

// TestCLI_CreateBesideEditBothApply is the shape the panel and the TUI will
// actually see once a model starts using creation: an ordinary edit and a new
// file in one turn.
func TestCLI_CreateBesideEditBothApply(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "main.go", "package main\n\nfunc old() {}\n")

	response := strings.Join([]string{
		"path: main.go",
		"<<<<<<< SEARCH",
		"func old() {}",
		"=======",
		"func renamed() {}",
		">>>>>>> REPLACE",
		"",
		"path: README.md",
		"<<<<<<< SEARCH",
		"=======",
		"# Project",
		">>>>>>> REPLACE",
	}, "\n")

	out, err := applyResponse(t, root, response, "y\ny\n")
	if err != nil {
		t.Fatalf("applyResponse: %v\n%s", err, out)
	}
	if got := readFileString(t, filepath.Join(root, "main.go")); !strings.Contains(got, "func renamed() {}") {
		t.Errorf("main.go = %q, want the edit applied", got)
	}
	if got := readFileString(t, filepath.Join(root, "README.md")); got != "# Project" {
		t.Errorf("README.md = %q, want the created content", got)
	}
	if !strings.Contains(out, "2 applied, 0 skipped, 0 refused") {
		t.Errorf("summary = %q, want 2 applied", out)
	}
}
