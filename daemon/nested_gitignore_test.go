package main

import (
	"path/filepath"
	"sort"
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

// assertIndexedExactly compares the WHOLE index against want, instead of probing
// it one key at a time.
//
// THE PROBING FORM WENT VACUOUS ON WINDOWS, AND THAT IS WHY THIS EXISTS. Index
// keys are forward-slash on every platform -- Chunk.FilePath is built through
// filepath.ToSlash in chunker.go, and reindex.go converts back with FromSlash at
// the single point it touches the filesystem. The probes here were built with
// filepath.Join, which yields `config\local-settings.json` on Windows. So the
// NEGATIVE probes -- "the ignored file must be absent" -- asked whether a key
// that could never exist was present, and passed. Permanently green, proving
// nothing, on the assertions that keep a gitignored file out of an index whose
// chunk text leaves the machine in a completion request.
//
// The positive probes failed loudly, which is the only reason this was caught.
// Repairing just those would have left every negative one silent forever, so the
// shape is replaced rather than the spelling corrected.
//
// A whole-set comparison cannot go vacuous: a key spelled wrong is reported
// twice, once as missing and once as unexpected.
func assertIndexedExactly(t *testing.T, root string, want ...string) {
	t.Helper()
	got := indexedPaths(t, root)

	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}
	for w := range wantSet {
		if !got[w] {
			t.Errorf("%q is missing from the index but should be there; indexed = %v", w, sortedKeys(got))
		}
	}
	for g := range got {
		if !wantSet[g] {
			t.Errorf("%q IS INDEXED and must not be; indexed = %v", g, sortedKeys(got))
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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

	// config/local-settings.json is absent BY OMISSION, which is the point: it
	// cannot be forgotten the way a dropped negative probe can.
	assertIndexedExactly(t, root, "config/public.json", "main.go")
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

	// a/notes.txt excluded by a/.gitignore; the identically-named b/notes.txt
	// kept, which is the scoping this test exists for.
	assertIndexedExactly(t, root, "b/notes.txt")
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

	// Only the negated file survives the root *.txt rule; docs/drop.txt and
	// top.txt stay out.
	assertIndexedExactly(t, root, "docs/keep.txt")
}

// A directory excluded by a nested .gitignore is pruned wholesale — nothing
// beneath it is indexed.
func TestNestedGitignore_ExcludesDirInSubdir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "svc", ".gitignore"), "secretstore/\n")
	writeFile(t, filepath.Join(root, "svc", "secretstore", "data.json"), "token=zzz\n")
	writeFile(t, filepath.Join(root, "svc", "keep.go"), "package svc\n")

	// Nothing under svc/secretstore/ appears, and an exact comparison says so
	// for the whole subtree rather than for the one filename this test happened
	// to create.
	assertIndexedExactly(t, root, "svc/keep.go")
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

	// Both root rules still bite at depth: build/out.bin and pkg/scratch.tmp are
	// absent.
	assertIndexedExactly(t, root, "pkg/keep.go")
}
