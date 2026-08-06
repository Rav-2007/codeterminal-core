package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// requireCaseSensitiveFS skips unless dir's filesystem distinguishes "a" from
// "A", by asking the filesystem rather than by guessing from GOOS.
//
// It exists because TestCaseFoldPrune_NoiseStaysCaseSensitive needs build/ and
// Build/ to be two separate directories, and on a case-INSENSITIVE filesystem
// they are one. CI's first Windows run reported the whole index as empty:
//
//	capital Build/ is real source and must stay indexed; got map[]
//
// Empty, not merely missing Build/, which is the tell. The setup created build/
// first, so Build/engine.go landed in that same directory, and the directory was
// then correctly pruned as noise. THE PRODUCT WAS RIGHT -- isPrunedDir matches
// the name actually on disk, so a project whose real source lives in Build/ is
// not pruned -- and the test premise was unrepresentable there.
//
// DETECTED, NOT BUILD-TAGGED, and the difference matters: macOS defaults to
// case-insensitive APFS, so //go:build unix would have kept this test failing on
// the macOS runner that now gates main. It is also not a property of the OS at
// all -- a case-sensitive volume on Windows and a case-insensitive one on Linux
// both exist -- so the only honest check is to try it.
func requireCaseSensitiveFS(t *testing.T, dir string) {
	t.Helper()
	probe := filepath.Join(dir, "casecheck")
	if err := os.Mkdir(probe, 0o700); err != nil {
		t.Fatalf("probing case sensitivity in %s: %v", dir, err)
	}
	defer func() { _ = os.Remove(probe) }()
	if _, err := os.Stat(filepath.Join(dir, "CASECHECK")); err == nil {
		t.Skip("NOT RUN: this filesystem is case-insensitive, so build/ and Build/ cannot both exist; " +
			"the noise-pruning case distinction is UNVERIFIABLE here")
	}
}

// S2 through the real indexing door: a case-varied protected directory (.GIT,
// .SSH, .CodeTerminal) must be pruned exactly like its lowercase form, so real
// VCS/credential/undo internals are never read into the index on a
// case-insensitive filesystem (where ".GIT" resolves to the real .git). Fails
// when neutered: revert isPrunedDir/IsProtectedDirName to the case-sensitive map
// lookup and these variant dirs get indexed.
func TestCaseFoldPrune_ProtectedDirsPruned(t *testing.T) {
	root := t.TempDir()
	variants := []string{".git", ".GIT", ".Git", ".SSH", ".AWS", ".CodeTerminal", ".hg", ".HG"}
	for _, v := range variants {
		writeFile(t, filepath.Join(root, v, "internal", "data.txt"), "MARKER internal state\n")
	}
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")

	got := indexedPaths(t, root)
	for p := range got {
		first := strings.Split(p, string(filepath.Separator))[0]
		if strings.EqualFold(first, ".git") || strings.EqualFold(first, ".ssh") ||
			strings.EqualFold(first, ".aws") || strings.EqualFold(first, ".codeterminal") ||
			strings.EqualFold(first, ".hg") {
			t.Errorf("indexed a protected-dir case variant: %s", p)
		}
	}
	if !got["main.go"] {
		t.Errorf("main.go should still be indexed; got %v", got)
	}
}

// Deliberate scope boundary: NOISE directories (build output, deps) stay
// case-SENSITIVE — they are not a security boundary, and folding them would risk
// pruning a legitimately-cased source directory that merely shares a name. A dir
// named "Build" (capital B) is real source and must still be indexed; lowercase
// "build" is pruned as noise.
func TestCaseFoldPrune_NoiseStaysCaseSensitive(t *testing.T) {
	root := t.TempDir()
	// The scenario needs build/ and Build/ to be TWO directories, which is only
	// true on a case-sensitive filesystem.
	requireCaseSensitiveFS(t, root)
	writeFile(t, filepath.Join(root, "build", "out.o"), "generated\n")
	writeFile(t, filepath.Join(root, "Build", "engine.go"), "package Build\n")

	got := indexedPaths(t, root)
	if got[filepath.Join("build", "out.o")] {
		t.Errorf("lowercase build/ should be pruned as noise")
	}
	if !got[filepath.Join("Build", "engine.go")] {
		t.Errorf("capital Build/ is real source and must stay indexed; got %v", got)
	}
}
