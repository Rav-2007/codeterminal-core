package editapply

import (
	"os"
	"path/filepath"
	"testing"
)

// S2 unit guard: protected-directory matching must be case-insensitive at every
// component, so a case-varied VCS/credential dir cannot slip past the guard on a
// case-insensitive filesystem (where ".GIT" resolves to the real .git).
func TestProtectedDirComponent_CaseFold(t *testing.T) {
	refused := []string{
		".git/hooks/pre-commit",
		".GIT/hooks/pre-commit",
		".Git/hooks/pre-commit",
		".gIt/config",
		"src/.GIT/hooks/post-merge", // at depth
		".SSH/id_rsa",
		".Ssh/authorized_keys",
		".AWS/credentials",
		".Mochiii/backups/x",
	}
	for _, p := range refused {
		if got := ProtectedDirComponent(p); got == "" {
			t.Errorf("ProtectedDirComponent(%q) = \"\" (not refused); case-fold bypass", p)
		}
	}
	// A name that merely resembles a protected dir must still NOT be refused.
	for _, p := range []string{"notgit/file", "gitignore.txt", "mygit/x", "sshconfig/x"} {
		if got := ProtectedDirComponent(p); got != "" {
			t.Errorf("ProtectedDirComponent(%q) = %q; over-refused a lookalike", p, got)
		}
	}
}

// S2 through the real writer door: an edit targeting a case-varied .git/hooks
// path must be refused by ResolveSafeTargetPath, exactly like the lowercase
// form — this is the arbitrary-code-execution surface (a writable git hook runs
// on the next commit). Fails when neutered: revert IsProtectedDirName to the
// case-sensitive map lookup and the .GIT variant resolves instead of refusing.
func TestResolveSafeTargetPath_RefusesCaseVariantGitHook(t *testing.T) {
	root := t.TempDir()
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	// Create a real .GIT/hooks/pre-commit inside the workspace (as a
	// case-insensitive FS would surface the real .git, or as model output could
	// create outright).
	hookDir := filepath.Join(realRoot, ".GIT", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(hookDir, "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolveSafeTargetPath(realRoot, ".GIT/hooks/pre-commit"); err == nil {
		t.Fatal("ResolveSafeTargetPath admitted .GIT/hooks/pre-commit; code-execution path open")
	}
	// The create case (path does not yet exist) must be refused too — the
	// pre-existence ProtectedDirComponent gate runs before EvalSymlinks.
	if _, _, err := resolveSafeTarget(realRoot, ".Git/hooks/post-commit"); err == nil {
		t.Fatal("resolveSafeTarget admitted a to-be-created .Git hook; code-execution path open")
	}
}
