package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempFile(t *testing.T, dir, rel, content string) string {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return full
}

func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return real
}

func TestPrepareEdit_UniqueSearchApplies(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n\nfunc old() {}\n")

	block := EditBlock{FilePath: "foo.go", Search: "func old() {}", Replace: "func new_() {}"}
	prepared, err := prepareEdit(root, block)
	if err != nil {
		t.Fatalf("prepareEdit: %v", err)
	}
	want := "package main\n\nfunc new_() {}\n"
	if prepared.newContent != want {
		t.Errorf("newContent = %q, want %q", prepared.newContent, want)
	}
	if prepared.startLine != 3 || prepared.endLine != 3 {
		t.Errorf("lines = %d-%d, want 3-3", prepared.startLine, prepared.endLine)
	}
}

func TestPrepareEdit_MissingSearchRefused(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n")

	block := EditBlock{FilePath: "foo.go", Search: "func nonexistent() {}", Replace: "x"}
	_, err := prepareEdit(root, block)
	if err == nil {
		t.Fatal("expected an error for absent search text, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want it to mention search text not found", err)
	}
}

func TestPrepareEdit_AmbiguousSearchRefused(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "x := 1\nx := 1\n")

	block := EditBlock{FilePath: "foo.go", Search: "x := 1", Replace: "x := 2"}
	_, err := prepareEdit(root, block)
	if err == nil {
		t.Fatal("expected an error for ambiguous (2x) search text, got nil")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error = %v, want it to mention ambiguity", err)
	}
}

func TestPrepareEdit_InvalidGoSyntaxRefused(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n\nfunc old() {}\n")

	block := EditBlock{FilePath: "foo.go", Search: "func old() {}", Replace: "func broken( {"}
	_, err := prepareEdit(root, block)
	if err == nil {
		t.Fatal("expected an error for an edit that produces unparseable Go, got nil")
	}
	if !strings.Contains(err.Error(), "unparseable") {
		t.Errorf("error = %v, want it to mention unparseable Go", err)
	}
}

func TestPrepareEdit_ValidGoSyntaxApplies(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n\nfunc old() {}\n")

	block := EditBlock{FilePath: "foo.go", Search: "func old() {}", Replace: "func fixed() {\n\treturn\n}"}
	prepared, err := prepareEdit(root, block)
	if err != nil {
		t.Fatalf("prepareEdit: %v", err)
	}
	if prepared.syntaxNote != "go/parser OK" {
		t.Errorf("syntaxNote = %q, want %q", prepared.syntaxNote, "go/parser OK")
	}
}

func TestPrepareEdit_NonGoFileSkipsSyntaxGateWithNote(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "notes.txt", "hello world\n")

	block := EditBlock{FilePath: "notes.txt", Search: "hello world", Replace: "goodbye world"}
	prepared, err := prepareEdit(root, block)
	if err != nil {
		t.Fatalf("prepareEdit: %v", err)
	}
	if !strings.Contains(prepared.syntaxNote, "no syntax check applied") {
		t.Errorf("syntaxNote = %q, want it to note no check was applied", prepared.syntaxNote)
	}
}

func TestResolveSafeTargetPath_AbsolutePathRefused(t *testing.T) {
	root := realTempDir(t)
	if _, err := resolveSafeTargetPath(root, "/etc/passwd"); err == nil {
		t.Fatal("expected an error for an absolute path, got nil")
	}
}

func TestResolveSafeTargetPath_DotDotEscapeRefused(t *testing.T) {
	root := realTempDir(t)
	if _, err := resolveSafeTargetPath(root, "../outside.txt"); err == nil {
		t.Fatal("expected an error for a \"..\" escape, got nil")
	}
}

func TestResolveSafeTargetPath_SymlinkEscapeRefused(t *testing.T) {
	root := realTempDir(t)
	outsideDir := t.TempDir()
	outsideFile := writeTempFile(t, outsideDir, "secret.txt", "hi\n")

	if err := os.Symlink(outsideFile, filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	if _, err := resolveSafeTargetPath(root, "link.txt"); err == nil {
		t.Fatal("expected an error for a symlink escaping the workspace root, got nil")
	}
}

func TestResolveSafeTargetPath_SecretFileRefused(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, ".env", "SECRET=1\n")

	if _, err := resolveSafeTargetPath(root, ".env"); err == nil {
		t.Fatal("expected an error for a secret-named file (.env), got nil")
	}
}

func TestResolveSafeTargetPath_OrdinaryFileAllowed(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "src/foo.go", "package main\n")

	got, err := resolveSafeTargetPath(root, "src/foo.go")
	if err != nil {
		t.Fatalf("resolveSafeTargetPath: %v", err)
	}
	want := filepath.Join(root, "src/foo.go")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
