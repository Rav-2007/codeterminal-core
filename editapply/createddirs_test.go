package editapply

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The created-dirs manifest is what lets undo remove the directories an apply
// run made without ever removing one the user made. These are its unit tests;
// the end-to-end behaviour they support lives in daemon/undo_create_test.go.

func realDir(t *testing.T) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func TestMissingAncestors_ListsOnlyWhatDoesNotExistYet(t *testing.T) {
	root := realDir(t)
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0755); err != nil {
		t.Fatal(err)
	}

	got, err := missingAncestors(root, filepath.Join(root, "pkg", "sub", "deep"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join("pkg", "sub"), filepath.Join("pkg", "sub", "deep")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("missingAncestors = %v, want %v (outermost first, and never pkg/ which already exists)", got, want)
	}
}

func TestMissingAncestors_EmptyWhenEverythingExists(t *testing.T) {
	root := realDir(t)
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0755); err != nil {
		t.Fatal(err)
	}
	got, err := missingAncestors(root, filepath.Join(root, "a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("missingAncestors = %v, want none", got)
	}
}

// The walk must stop at the workspace root and never propose creating — or
// later removing — anything above it.
func TestMissingAncestors_StopsAtTheWorkspaceRoot(t *testing.T) {
	root := realDir(t)
	got, err := missingAncestors(root, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("missingAncestors(root, root) = %v, want none: the root is not this run's to create", got)
	}
}

func TestCreatedDirs_RecordedByApplyAndReadBackDeepestFirst(t *testing.T) {
	root := realDir(t)
	backupDir, err := NewBackupSessionDir(root)
	if err != nil {
		t.Fatal(err)
	}

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "pkg/sub/deep/thing.go", Search: "", Replace: "package deep\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(root, prepared, backupDir); err != nil {
		t.Fatal(err)
	}

	dirs, err := CreatedDirsInSession(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join("pkg", "sub", "deep"),
		filepath.Join("pkg", "sub"),
		"pkg",
	}
	if strings.Join(dirs, "|") != strings.Join(want, "|") {
		t.Errorf("CreatedDirsInSession = %v, want %v — deepest first, since a parent cannot be removed before its child", dirs, want)
	}
}

// A second created file under the same tree must not re-record directories the
// first one already claimed.
func TestCreatedDirs_NotDuplicatedAcrossBlocksInOneRun(t *testing.T) {
	root := realDir(t)
	backupDir, err := NewBackupSessionDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"pkg/sub/one.go", "pkg/sub/two.go"} {
		prepared, err := PrepareEdit(root, EditBlock{FilePath: rel, Search: "", Replace: "package sub\n"})
		if err != nil {
			t.Fatal(err)
		}
		if err := Apply(root, prepared, backupDir); err != nil {
			t.Fatal(err)
		}
	}

	dirs, err := CreatedDirsInSession(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, d := range dirs {
		seen[d]++
	}
	for d, n := range seen {
		if n != 1 {
			t.Errorf("%s recorded %d times, want 1", d, n)
		}
	}
}

// An edit that does not create anything records nothing, and a run that created
// no directories reads back as none rather than as an error — which is what
// keeps every backup session written before this manifest existed valid.
func TestCreatedDirs_AbsentManifestIsNotAnError(t *testing.T) {
	root := realDir(t)
	if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	backupDir, err := NewBackupSessionDir(root)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareEdit(root, EditBlock{FilePath: "existing.txt", Search: "a", Replace: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(root, prepared, backupDir); err != nil {
		t.Fatal(err)
	}

	dirs, err := CreatedDirsInSession(backupDir)
	if err != nil {
		t.Fatalf("a session with no created directories must not error: %v", err)
	}
	if len(dirs) != 0 {
		t.Errorf("CreatedDirsInSession = %v, want none", dirs)
	}
}

// Creating a file directly in the workspace root records no directory: the root
// is not this run's to remove.
func TestCreatedDirs_RootLevelFileRecordsNothing(t *testing.T) {
	root := realDir(t)
	backupDir, err := NewBackupSessionDir(root)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareEdit(root, EditBlock{FilePath: "top.go", Search: "", Replace: "package main\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(root, prepared, backupDir); err != nil {
		t.Fatal(err)
	}

	dirs, err := CreatedDirsInSession(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 0 {
		t.Errorf("CreatedDirsInSession = %v, want none for a root-level create", dirs)
	}
}

// The manifest entry must be taken back when the write it describes fails.
//
// This is the transactional property the record shares with created-files, and
// it is the one that matters most here: a stale "this run created pkg/sub/"
// entry, left behind by an apply that never wrote anything, would have a later
// undo remove a directory this run is not responsible for. Recording happens
// before the mutation (Fix 1 ordering) precisely so a failure can undo it.
//
// The failure is arranged with a read-only parent directory, so MkdirAll fails
// with EACCES after the manifest has already been written. (Skipped as root,
// which ignores the mode.)
func TestCreatedDirs_ManifestEntryIsRolledBackWhenTheWriteFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the write cannot be made to fail this way")
	}
	root := realDir(t)
	// pkg/ exists but nothing may be created inside it.
	if err := os.Mkdir(filepath.Join(root, "pkg"), 0700); err != nil {
		t.Fatal(err)
	}
	makeDirUnwritable(t, filepath.Join(root, "pkg"))
	backupDir, err := NewBackupSessionDir(root)
	if err != nil {
		t.Fatal(err)
	}

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "pkg/sub/thing.go", Search: "", Replace: "package sub\n"})
	if err != nil {
		t.Fatalf("PrepareEdit should succeed — the path is confined and the target does not exist: %v", err)
	}
	if applyErr := Apply(root, prepared, backupDir); applyErr == nil {
		t.Fatal("Apply should have failed: its parent directory is not writable")
	}

	dirs, err := CreatedDirsInSession(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 0 {
		t.Errorf("failed apply left %v in the created-dirs manifest; a later undo would remove directories this run never made", dirs)
	}
}

// A directory recorded once stays recorded once, even when it genuinely has to
// be created twice in the same run.
//
// This is the recovery shape: the run creates pkg/sub/, something removes it,
// and a later block in the same run creates it again. missingAncestors reports
// it as missing both times, correctly — the manifest must not grow a duplicate
// entry, because undo would then try to remove the same directory twice and
// count the second failure.
func TestCreatedDirs_ReCreatedInTheSameRunIsRecordedOnce(t *testing.T) {
	root := realDir(t)
	backupDir, err := NewBackupSessionDir(root)
	if err != nil {
		t.Fatal(err)
	}

	apply := func(rel string) {
		t.Helper()
		prepared, err := PrepareEdit(root, EditBlock{FilePath: rel, Search: "", Replace: "package sub\n"})
		if err != nil {
			t.Fatal(err)
		}
		if err := Apply(root, prepared, backupDir); err != nil {
			t.Fatal(err)
		}
	}

	apply("pkg/sub/one.go")
	if err := os.RemoveAll(filepath.Join(root, "pkg")); err != nil {
		t.Fatal(err)
	}
	apply("pkg/sub/two.go")

	dirs, err := CreatedDirsInSession(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, d := range dirs {
		seen[d]++
	}
	for d, n := range seen {
		if n != 1 {
			t.Errorf("%s recorded %d times, want 1", d, n)
		}
	}
	if len(dirs) != 2 {
		t.Errorf("CreatedDirsInSession = %v, want exactly pkg and pkg/sub", dirs)
	}
}
