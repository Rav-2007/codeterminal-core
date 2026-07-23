package main

import (
	"path/filepath"
	"strings"
	"testing"
)

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
