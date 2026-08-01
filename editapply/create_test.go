package editapply

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file is the before/after evidence for Fix 7: new files could not be
// created at all. An edit naming a path that did not exist died inside
// ResolveSafeTargetPath's EvalSymlinks with "resolving new.txt: lstat
// /abs/path/new.txt: no such file or directory" -- an internal fault, shown to
// the user as if it were a considered refusal, and carrying an absolute host
// path into the bargain.
//
// The A8 inconsistency is resolved in the same pass. Empty SEARCH used to mean
// three different things depending on what happened to be on disk: silent
// whole-file insertion on an EMPTY file (byte-exact matching finds the empty
// string exactly once there), an "ambiguous" refusal on a NON-EMPTY file (it
// finds it everywhere), and the lstat fault above on a path that did not exist.
// It now means one thing everywhere: write REPLACE as the file's whole content,
// creating it if absent, and refuse if the file exists with content in it.

func TestCreate_NewFileInWorkspace(t *testing.T) {
	root := realTempDir(t)

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "new.txt", Search: "", Replace: "hello\nworld\n"})
	if err != nil {
		t.Fatalf("PrepareEdit for a new file: %v", err)
	}
	if !prepared.Creates {
		t.Error("Creates = false, want true for a file that does not exist")
	}

	backupDir, _ := NewBackupSessionDir(root)
	if err := Apply(root, prepared, backupDir); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "new.txt")); got != "hello\nworld\n" {
		t.Errorf("created file = %q, want %q", got, "hello\nworld\n")
	}
}

// TestCreate_NewFileInNewNestedDir is the confinement case that could not use
// EvalSymlinks: neither the file nor its parent directories exist, so the path
// has to be confined via its deepest EXISTING ancestor.
func TestCreate_NewFileInNewNestedDir(t *testing.T) {
	root := realTempDir(t)

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "pkg/sub/deep/new.go", Search: "", Replace: "package deep\n"})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	backupDir, _ := NewBackupSessionDir(root)
	if err := Apply(root, prepared, backupDir); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "pkg/sub/deep/new.go")); got != "package deep\n" {
		t.Errorf("created file = %q", got)
	}
	if prepared.SyntaxNote != "go/parser OK" {
		t.Errorf("SyntaxNote = %q, want the Go gate to have run on created Go files too", prepared.SyntaxNote)
	}
}

// TestCreate_RefusesProtectedAndEscapingPaths is the Fix 3 interaction: being
// able to CREATE a git hook is exactly as bad as being able to overwrite one,
// and the creation path must not become a way around any Tier-1 refusal.
func TestCreate_RefusesProtectedAndEscapingPaths(t *testing.T) {
	root := realTempDir(t)
	outside := realTempDir(t)
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	for _, tc := range []struct{ name, path, wantIn string }{
		{"git hook", ".git/hooks/pre-commit", "refusing"},
		{"git config", ".git/config", "refusing"},
		{"codeterminal backup", ".codeterminal/backups/x/before/f.txt", "refusing"},
		{"ssh key dir", ".ssh/authorized_keys", "refusing"},
		{"secret name", ".env", "secret-file"},
		{"absolute path", "/tmp/evil.txt", "absolute"},
		{"dotdot escape", "../evil.txt", "escapes"},
		{"through in-tree symlink to outside", "escape/evil.txt", "outside"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := PrepareEdit(root, EditBlock{FilePath: tc.path, Search: "", Replace: "pwned\n"})
			if err == nil {
				t.Fatalf("CREATED A PROTECTED PATH: %s was allowed", tc.path)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantIn)
			}
			// Nothing may exist on disk either, inside or outside the workspace.
			if _, statErr := os.Stat(filepath.Join(root, tc.path)); statErr == nil {
				t.Errorf("file was created at %s despite the refusal", tc.path)
			}
		})
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("ESCAPE: %d entries created outside the workspace root", len(entries))
	}
}

// TestCreate_EmptySearchOnEmptyFileFills is the A8 quirk, now a defined
// behaviour rather than an accident of byte-exact matching.
func TestCreate_EmptySearchOnEmptyFileFills(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "empty.txt", "")

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "empty.txt", Search: "", Replace: "filled\n"})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	if prepared.Creates {
		t.Error("Creates = true, want false: the file already existed")
	}
	backupDir, _ := NewBackupSessionDir(root)
	if err := Apply(root, prepared, backupDir); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "empty.txt")); got != "filled\n" {
		t.Errorf("file = %q, want %q", got, "filled\n")
	}
}

// TestCreate_EmptySearchOnNonEmptyFileRefusedClearly is the other half of the
// A8 resolution: the refusal stays, but it now explains itself instead of
// reporting a bogus ambiguity.
func TestCreate_EmptySearchOnNonEmptyFileRefusedClearly(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "full.txt", "important content\n")

	_, err := PrepareEdit(root, EditBlock{FilePath: "full.txt", Search: "", Replace: "clobbered\n"})
	if err == nil {
		t.Fatal("WHOLE-FILE CLOBBER: an empty SEARCH overwrote a non-empty file")
	}
	if !strings.Contains(err.Error(), "already exists and is not empty") {
		t.Errorf("error = %v, want it to explain why an empty SEARCH is refused here", err)
	}
	if got := readFile(t, filepath.Join(root, "full.txt")); got != "important content\n" {
		t.Errorf("file = %q, want it untouched", got)
	}
}

// TestCreate_NonEmptySearchOnMissingFileExplainsItself replaces the old lstat
// fault with a refusal that tells the caller what to do instead.
func TestCreate_NonEmptySearchOnMissingFileExplainsItself(t *testing.T) {
	root := realTempDir(t)

	_, err := PrepareEdit(root, EditBlock{FilePath: "missing.txt", Search: "something", Replace: "x"})
	if err == nil {
		t.Fatal("expected a refusal for editing a file that does not exist")
	}
	if strings.Contains(err.Error(), "lstat") {
		t.Errorf("error = %v, want a refusal reason rather than a raw syscall fault", err)
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error = %v, want it to say the file does not exist", err)
	}
}

// TestCreate_RacingCreationIsRefused pins that the Fix-6 staleness check covers
// creation too: if something else puts a file there between prepare and write,
// the creating edit must not silently overwrite it.
func TestCreate_RacingCreationIsRefused(t *testing.T) {
	root := realTempDir(t)

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "new.txt", Search: "", Replace: "mine\n"})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	writeTempFile(t, root, "new.txt", "someone else got there first\n")

	backupDir, _ := NewBackupSessionDir(root)
	if err := Apply(root, prepared, backupDir); err == nil {
		t.Fatal("OVERWROTE A RACING CREATION: expected a refusal")
	}
	if got := readFile(t, filepath.Join(root, "new.txt")); got != "someone else got there first\n" {
		t.Errorf("file = %q, want the other writer's content intact", got)
	}
}

// TestCreate_ExistingFileEditsUnaffected is the negative control: ordinary
// edits to existing files must behave exactly as before.
func TestCreate_ExistingFileEditsUnaffected(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n\nfunc old() {}\n")

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "foo.go", Search: "func old() {}", Replace: "func new_() {}"})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	if prepared.Creates {
		t.Error("Creates = true for an ordinary edit")
	}
	backupDir, _ := NewBackupSessionDir(root)
	if err := Apply(root, prepared, backupDir); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "foo.go")); got != "package main\n\nfunc new_() {}\n" {
		t.Errorf("file = %q", got)
	}
}

// TestCreate_CreatedFileIsUndoable confirms a created file still lands in the
// backup session, so `edits undo` has something to act on -- and, as of Fix C,
// that the session records the one fact the snapshots cannot express: the file
// did not exist before.
//
// This test used to stop at the snapshots, and that is exactly how the 0-byte
// lie shipped. A created file's before/ snapshot is a 0-byte file, which is
// byte-for-byte identical to the snapshot of a file that existed and was empty
// -- so asserting "before/ is empty" passed while undo went on to restore an
// empty file where the correct answer was no file.
//
// The other half of the invariant -- that undo actually REMOVES it and says so
// -- is asserted in daemon/undo_create_test.go, because the undo core lives in
// daemon and editapply cannot import it without an import cycle. Both halves
// are required; neither alone would have caught this.
func TestCreate_CreatedFileIsUndoable(t *testing.T) {
	root := realTempDir(t)

	prepared, _ := PrepareEdit(root, EditBlock{FilePath: "new.txt", Search: "", Replace: "hello\n"})
	backupDir, _ := NewBackupSessionDir(root)
	if err := Apply(root, prepared, backupDir); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	before := filepath.Join(backupDir, "before", "new.txt")
	if got := readFile(t, before); got != "" {
		t.Errorf("before-snapshot = %q, want empty (the file did not exist)", got)
	}
	if got := readFile(t, filepath.Join(backupDir, "after", "new.txt")); got != "hello\n" {
		t.Errorf("after-snapshot = %q, want the created content", got)
	}

	created, err := CreatedInSession(backupDir)
	if err != nil {
		t.Fatalf("CreatedInSession: %v", err)
	}
	if !created["new.txt"] {
		t.Errorf("created set = %v, want new.txt recorded as brought into existence; "+
			"without this undo cannot tell it from a file that existed and was empty", created)
	}
}

// TestCreate_FillingAnEmptyFileIsNotRecordedAsCreated is the other side of the
// distinction, and the case that must not be conflated with it: the file was
// already there. Undoing that is a restore back to empty, never a delete of a
// file this run did not bring into existence.
func TestCreate_FillingAnEmptyFileIsNotRecordedAsCreated(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "empty.txt", "")

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "empty.txt", Search: "", Replace: "filled\n"})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	backupDir, _ := NewBackupSessionDir(root)
	if err := Apply(root, prepared, backupDir); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	created, err := CreatedInSession(backupDir)
	if err != nil {
		t.Fatalf("CreatedInSession: %v", err)
	}
	if created["empty.txt"] {
		t.Error("empty.txt recorded as created; it already existed, and undoing this must restore it, not delete it")
	}
}

// TestCreate_SessionWithNoCreationsHasNoManifest keeps ordinary edits exactly
// as they were: nothing extra written, and an older backup session (from before
// this record existed) reads back as "created nothing", which is the right
// answer for it.
func TestCreate_SessionWithNoCreationsHasNoManifest(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n\nfunc old() {}\n")

	prepared, err := PrepareEdit(root, EditBlock{FilePath: "foo.go", Search: "func old() {}", Replace: "func new_() {}"})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	backupDir, _ := NewBackupSessionDir(root)
	if err := Apply(root, prepared, backupDir); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if _, err := os.Stat(filepath.Join(backupDir, createdManifestName)); !os.IsNotExist(err) {
		t.Errorf("an edit-only run wrote a created-file record (stat err = %v)", err)
	}
	created, err := CreatedInSession(backupDir)
	if err != nil {
		t.Fatalf("CreatedInSession on a session with no record: %v", err)
	}
	if len(created) != 0 {
		t.Errorf("created = %v, want empty", created)
	}
}

// TestCreate_NewFileIsNotExecutable pins that content coming from untrusted
// model output cannot arrive with an executable bit.
func TestCreate_NewFileIsNotExecutable(t *testing.T) {
	root := realTempDir(t)

	prepared, _ := PrepareEdit(root, EditBlock{FilePath: "script.sh", Search: "", Replace: "#!/bin/sh\necho hi\n"})
	backupDir, _ := NewBackupSessionDir(root)
	if err := Apply(root, prepared, backupDir); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	info, err := os.Stat(filepath.Join(root, "script.sh"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm()&0111 != 0 {
		t.Errorf("created file mode = %v, want no executable bit", info.Mode().Perm())
	}
}

// The same bytes must be refused whether they arrive as an edit or as a create.
//
// PrepareEdit hard-refused a .go file that would not parse; prepareCreate only
// attached an advisory note and wrote it (M7). So a model whose edit was
// rejected could land the identical unparseable file by resending it with an
// empty SEARCH section — the gate was one keystroke of prompt away from being
// optional.
func TestCreateRefusesUnparseableGoJustLikeEdit(t *testing.T) {
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}

	const broken = "package main\n\nfunc broken( {\n"

	// As a CREATE: empty SEARCH section.
	_, createErr := PrepareEdit(real, EditBlock{FilePath: "new.go", Search: "", Replace: broken})
	if createErr == nil {
		t.Fatal("creating an unparseable .go file was allowed; editing one into that state is refused")
	}
	if !strings.Contains(createErr.Error(), "unparseable as Go") {
		t.Errorf("create refusal = %q, want it to name the syntax gate", createErr)
	}

	// As an EDIT of the same file: identical bytes, identical refusal wording.
	if err := os.WriteFile(filepath.Join(real, "existing.go"), []byte("package main\n\nfunc ok() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, editErr := PrepareEdit(real, EditBlock{FilePath: "existing.go", Search: "func ok() {}", Replace: "func broken( {"})
	if editErr == nil {
		t.Fatal("the edit path stopped refusing unparseable Go; the two paths are still asymmetric, just the other way round")
	}
	if !strings.Contains(editErr.Error(), "unparseable as Go") {
		t.Errorf("edit refusal = %q, want it to name the syntax gate", editErr)
	}
}

// The gate is Go-only, and that stays true: creating a file in a language this
// binary has no parser for must still work.
func TestCreateStillAllowsNonGoContentThatWouldNotParseAsGo(t *testing.T) {
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}

	prepared, err := PrepareEdit(real, EditBlock{
		FilePath: "notes.md",
		Search:   "",
		Replace:  "# heading\n\nfunc broken( {\n",
	})
	if err != nil {
		t.Fatalf("creating a markdown file was refused: %v", err)
	}
	if !strings.Contains(prepared.SyntaxNote, "no syntax check applied") {
		t.Errorf("SyntaxNote = %q, want it to say no check applied", prepared.SyntaxNote)
	}
}
