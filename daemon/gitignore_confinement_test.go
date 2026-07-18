package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureGitignoreEntry_RefusesSymlinkedGitignore is the regression for the
// FAIL-2 follow-up finding: ensureGitignoreEntry opened root/.gitignore for
// append with no O_NOFOLLOW, so a symlinked .gitignore appended the ignore
// entry to a file outside the workspace root. It must now refuse.
func TestEnsureGitignoreEntry_RefusesSymlinkedGitignore(t *testing.T) {
	root := realTempDir(t)
	outside := realTempDir(t)

	victim := filepath.Join(outside, "victim.gitignore")
	writeAt(t, victim, "# pre-existing\n")
	if err := os.Symlink(victim, filepath.Join(root, ".gitignore")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	err := ensureGitignoreEntry(root, gitignoreEntry)
	if err == nil {
		t.Errorf("expected ensureGitignoreEntry to refuse the symlinked .gitignore, got nil")
	}
	if got := readFileString(t, victim); strings.Contains(got, gitignoreEntry) {
		t.Fatalf("ESCAPE: ignore entry appended to out-of-root file: %q", got)
	}
}

// TestEnsureGitignoreEntry_NormalAppendWorks confirms the fix does not break
// the ordinary path: appending to an existing regular .gitignore, idempotency,
// and creating .gitignore when absent.
func TestEnsureGitignoreEntry_NormalAppendWorks(t *testing.T) {
	// Append to an existing regular .gitignore.
	root := realTempDir(t)
	gi := filepath.Join(root, ".gitignore")
	writeAt(t, gi, "node_modules\n")

	if err := ensureGitignoreEntry(root, gitignoreEntry); err != nil {
		t.Fatalf("append to regular .gitignore failed: %v", err)
	}
	if got := readFileString(t, gi); !strings.Contains(got, gitignoreEntry) {
		t.Errorf(".gitignore = %q, want it to contain %q", got, gitignoreEntry)
	}

	// Idempotent: a second call does not duplicate the entry.
	if err := ensureGitignoreEntry(root, gitignoreEntry); err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if n := strings.Count(readFileString(t, gi), gitignoreEntry); n != 1 {
		t.Errorf("entry appears %d times, want 1", n)
	}

	// Creates .gitignore when absent.
	fresh := realTempDir(t)
	if err := ensureGitignoreEntry(fresh, gitignoreEntry); err != nil {
		t.Fatalf("create-from-absent failed: %v", err)
	}
	if got := readFileString(t, filepath.Join(fresh, ".gitignore")); !strings.Contains(got, gitignoreEntry) {
		t.Errorf("created .gitignore = %q, want it to contain %q", got, gitignoreEntry)
	}
}
