package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// indexedPaths runs the real indexing walk and returns the set of file paths
// that produced chunks (i.e. were admitted into the index).
func indexedPaths(t *testing.T, root string) map[string]bool {
	t.Helper()
	scan, err := ScanWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool)
	for _, c := range scan.Chunks {
		got[c.FilePath] = true
	}
	return got
}

// S1 regression: a benign-named file excluded ONLY by a nested (non-root)
// .gitignore must NOT be indexed. Before the fix the indexer read only the
// workspace-root .gitignore, so this file — invisible to git — was indexed and
// its contents became eligible to leave the machine in a retrieval-grounded
// completion. Fails when neutered: revert newGitignoreMatcher to the old
// root-only loader and the nested-ignored file reappears in the index.
func TestNestedGitignore_ExcludesFileInSubdir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "*.log\n")
	writeFile(t, filepath.Join(root, "config", ".gitignore"), "local-settings.json\n")
	writeFile(t, filepath.Join(root, "config", "local-settings.json"), "api_token=abcdef123456\n")
	writeFile(t, filepath.Join(root, "config", "public.json"), "{\"ok\":true}\n")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")

	got := indexedPaths(t, root)
	if got[filepath.Join("config", "local-settings.json")] {
		t.Errorf("nested-ignored file was indexed; nested .gitignore not honored")
	}
	if !got[filepath.Join("config", "public.json")] {
		t.Errorf("non-ignored sibling should still be indexed; got %v", got)
	}
	if !got["main.go"] {
		t.Errorf("root file should still be indexed; got %v", got)
	}
}

// The nested pattern must be scoped to its own subtree: an identically-named
// file in a DIFFERENT subdir (no ignore rule) stays indexed. Guards against a
// naive "merge every .gitignore into one flat root-relative set" fix that would
// over-exclude across unrelated directories.
func TestNestedGitignore_ScopedToOwnSubtree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a", ".gitignore"), "notes.txt\n")
	writeFile(t, filepath.Join(root, "a", "notes.txt"), "ignored here\n")
	writeFile(t, filepath.Join(root, "b", "notes.txt"), "kept here\n")

	got := indexedPaths(t, root)
	if got[filepath.Join("a", "notes.txt")] {
		t.Errorf("a/notes.txt should be excluded by a/.gitignore")
	}
	if !got[filepath.Join("b", "notes.txt")] {
		t.Errorf("b/notes.txt has no ignore rule and must stay indexed; got %v", got)
	}
}

// Deeper .gitignore overrides a shallower one via negation ("!"), matching git's
// last-match-wins precedence across levels.
func TestNestedGitignore_NegationReincludes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "*.txt\n")
	writeFile(t, filepath.Join(root, "docs", ".gitignore"), "!keep.txt\n")
	writeFile(t, filepath.Join(root, "docs", "keep.txt"), "re-included\n")
	writeFile(t, filepath.Join(root, "docs", "drop.txt"), "still ignored\n")
	writeFile(t, filepath.Join(root, "top.txt"), "ignored at root\n")

	got := indexedPaths(t, root)
	if !got[filepath.Join("docs", "keep.txt")] {
		t.Errorf("docs/keep.txt should be re-included by docs/.gitignore negation; got %v", got)
	}
	if got[filepath.Join("docs", "drop.txt")] {
		t.Errorf("docs/drop.txt should stay ignored by the root *.txt rule")
	}
	if got["top.txt"] {
		t.Errorf("top.txt should be ignored by the root *.txt rule")
	}
}

// A directory excluded by a nested .gitignore is pruned wholesale — nothing
// beneath it is indexed.
func TestNestedGitignore_ExcludesDirInSubdir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "svc", ".gitignore"), "secretstore/\n")
	writeFile(t, filepath.Join(root, "svc", "secretstore", "data.json"), "token=zzz\n")
	writeFile(t, filepath.Join(root, "svc", "keep.go"), "package svc\n")

	got := indexedPaths(t, root)
	for p := range got {
		if strings.Contains(p, "secretstore") {
			t.Errorf("nothing under a nested-ignored dir should be indexed; got %s", p)
		}
	}
	if !got[filepath.Join("svc", "keep.go")] {
		t.Errorf("svc/keep.go should stay indexed; got %v", got)
	}
}

// Root-level behavior is unchanged by the nested-aware rewrite: a root
// .gitignore still excludes matching files at any depth (regression guard for
// the pre-existing subset that the new matcher must preserve).
func TestNestedGitignore_RootStillHonored(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitignore"), "build/\n*.tmp\n")
	writeFile(t, filepath.Join(root, "build", "out.bin"), "x\n")
	writeFile(t, filepath.Join(root, "pkg", "scratch.tmp"), "y\n")
	writeFile(t, filepath.Join(root, "pkg", "keep.go"), "package pkg\n")

	got := indexedPaths(t, root)
	if got[filepath.Join("build", "out.bin")] {
		t.Errorf("root build/ rule should prune build/ at any depth")
	}
	if got[filepath.Join("pkg", "scratch.tmp")] {
		t.Errorf("root *.tmp rule should match nested files")
	}
	if !got[filepath.Join("pkg", "keep.go")] {
		t.Errorf("pkg/keep.go should stay indexed; got %v", got)
	}
}
