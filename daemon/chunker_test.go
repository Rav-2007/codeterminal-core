package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func linesOf(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = "line"
	}
	return strings.Join(lines, "\n")
}

func TestScanWorkspace_ChunksTextFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), linesOf(100))

	res, err := ScanWorkspace(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FilesScanned != 1 {
		t.Fatalf("FilesScanned = %d, want 1", res.FilesScanned)
	}

	// 100 lines, 40-line windows, 10-line overlap -> stride 30:
	// [1-40] [31-70] [61-100], stopping once a window reaches the last
	// line rather than emitting a final, fully-redundant [91-100] window.
	want := []struct{ start, end int }{
		{1, 40}, {31, 70}, {61, 100},
	}
	if len(res.Chunks) != len(want) {
		t.Fatalf("got %d chunks, want %d: %+v", len(res.Chunks), len(want), res.Chunks)
	}
	for i, w := range want {
		c := res.Chunks[i]
		if c.StartLine != w.start || c.EndLine != w.end {
			t.Errorf("chunk %d = [%d-%d], want [%d-%d]", i, c.StartLine, c.EndLine, w.start, w.end)
		}
		if c.FilePath != "a.go" {
			t.Errorf("chunk %d FilePath = %q, want %q", i, c.FilePath, "a.go")
		}
		wantID := "a.go:" + strconv.Itoa(w.start) + "-" + strconv.Itoa(w.end)
		if c.ID != wantID {
			t.Errorf("chunk %d ID = %q, want %q", i, c.ID, wantID)
		}
	}
}

func TestScanWorkspace_SkipsSecretsBinaryAndOversized(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), "SECRET_TOKEN=abc123\n")
	writeFile(t, filepath.Join(dir, "key.pem"), "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----\n")
	writeFile(t, filepath.Join(dir, "id_rsa"), "not-really-a-key-but-named-like-one\n")
	writeFile(t, filepath.Join(dir, "my_credentials.txt"), "user:pass\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")

	// Binary file: a null byte in the first few bytes.
	if err := os.WriteFile(filepath.Join(dir, "image.bin"), []byte{0x00, 0x01, 0x02, 0x03}, 0644); err != nil {
		t.Fatal(err)
	}

	// Oversized text file.
	big := strings.Repeat("x", maxFileSize+1)
	writeFile(t, filepath.Join(dir, "huge.txt"), big)

	res, err := ScanWorkspace(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.FilesScanned != 1 {
		t.Fatalf("FilesScanned = %d, want 1 (only main.go)", res.FilesScanned)
	}
	if res.Skipped[SkipSecret] != 4 {
		t.Errorf("SkipSecret count = %d, want 4 (.env, key.pem, id_rsa, my_credentials.txt)", res.Skipped[SkipSecret])
	}
	if res.Skipped[SkipBinary] != 1 {
		t.Errorf("SkipBinary count = %d, want 1", res.Skipped[SkipBinary])
	}
	if res.Skipped[SkipTooLarge] != 1 {
		t.Errorf("SkipTooLarge count = %d, want 1", res.Skipped[SkipTooLarge])
	}

	for _, c := range res.Chunks {
		if strings.Contains(c.Content, "SECRET_TOKEN") || strings.Contains(c.Content, "PRIVATE KEY") {
			t.Fatalf("secret content leaked into chunk %s: %q", c.ID, c.Content)
		}
		if strings.HasSuffix(c.FilePath, ".env") || strings.HasSuffix(c.FilePath, ".pem") {
			t.Fatalf("a secret file was chunked: %s", c.FilePath)
		}
	}
}

func TestScanWorkspace_PrunesIgnoredDirs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")
	writeFile(t, filepath.Join(dir, "node_modules", "pkg", "index.js"), "SHOULD_NOT_BE_INDEXED\n")
	writeFile(t, filepath.Join(dir, "vendor", "lib", "file.go"), "SHOULD_NOT_BE_INDEXED\n")
	writeFile(t, filepath.Join(dir, ".git", "HEAD"), "SHOULD_NOT_BE_INDEXED\n")
	writeFile(t, filepath.Join(dir, ".codeterminal", "index", "junk.gob"), "SHOULD_NOT_BE_INDEXED\n")

	res, err := ScanWorkspace(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FilesScanned != 1 {
		t.Fatalf("FilesScanned = %d, want 1", res.FilesScanned)
	}
	if res.Skipped[SkipIgnoredDir] != 4 {
		t.Errorf("SkipIgnoredDir count = %d, want 4", res.Skipped[SkipIgnoredDir])
	}
	for _, c := range res.Chunks {
		if strings.Contains(c.Content, "SHOULD_NOT_BE_INDEXED") {
			t.Fatalf("content from an ignored dir leaked into chunk %s", c.ID)
		}
	}
}

func TestScanWorkspace_GitignoreLite(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".gitignore"), "ignored_file.txt\nignored_dir/\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")
	writeFile(t, filepath.Join(dir, "ignored_file.txt"), "SHOULD_NOT_BE_INDEXED\n")
	writeFile(t, filepath.Join(dir, "ignored_dir", "nested.go"), "SHOULD_NOT_BE_INDEXED\n")

	res, err := ScanWorkspace(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// main.go only: .gitignore itself is hard-excluded as noise (it can
	// never usefully answer a code question), and ignored_file.txt /
	// ignored_dir/ are the two paths this test is actually about excluding
	// via .gitignore's own rules.
	if res.FilesScanned != 1 {
		t.Fatalf("FilesScanned = %d, want 1 (main.go only)", res.FilesScanned)
	}
	if res.Skipped[SkipGitignore] != 2 {
		t.Errorf("SkipGitignore count = %d, want 2 (ignored_file.txt + ignored_dir)", res.Skipped[SkipGitignore])
	}
	if res.Skipped[SkipNoise] != 1 {
		t.Errorf("SkipNoise count = %d, want 1 (.gitignore itself)", res.Skipped[SkipNoise])
	}
	for _, c := range res.Chunks {
		if strings.Contains(c.Content, "SHOULD_NOT_BE_INDEXED") {
			t.Fatalf("gitignored content leaked into chunk %s", c.ID)
		}
		if c.FilePath == "ignored_file.txt" || strings.HasPrefix(c.FilePath, "ignored_dir"+string(filepath.Separator)) {
			t.Fatalf("a gitignored path was indexed: %s", c.FilePath)
		}
	}
}

func TestScanWorkspace_SymlinkEscapeNotIndexed(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), "OUTSIDE_SECRET_VALUE\n")

	workspace := t.TempDir()
	writeFile(t, filepath.Join(workspace, "main.go"), "package main\n")

	// Absolute-target symlink escaping the workspace.
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(workspace, "escape_abs")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}
	// Relative-target ("../outside") symlink escaping the workspace.
	if err := os.Symlink(filepath.Join("..", filepath.Base(outside), "secret.txt"), filepath.Join(workspace, "escape_rel")); err != nil {
		t.Fatalf("creating relative symlink: %v", err)
	}

	res, err := ScanWorkspace(workspace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FilesScanned != 1 {
		t.Fatalf("FilesScanned = %d, want 1 (only main.go)", res.FilesScanned)
	}
	if res.Skipped[SkipSymlink] != 2 {
		t.Errorf("SkipSymlink count = %d, want 2", res.Skipped[SkipSymlink])
	}
	for _, c := range res.Chunks {
		if strings.Contains(c.Content, "OUTSIDE_SECRET_VALUE") {
			t.Fatalf("content from outside the workspace leaked into chunk %s via a symlink", c.ID)
		}
	}
}

func TestScanWorkspace_SymlinkedDirEscapeNotIndexed(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "nested", "secret.txt"), "OUTSIDE_DIR_SECRET\n")

	workspace := t.TempDir()
	writeFile(t, filepath.Join(workspace, "main.go"), "package main\n")
	if err := os.Symlink(filepath.Join(outside, "nested"), filepath.Join(workspace, "escape_dir")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	res, err := ScanWorkspace(workspace)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.FilesScanned != 1 {
		t.Fatalf("FilesScanned = %d, want 1", res.FilesScanned)
	}
	for _, c := range res.Chunks {
		if strings.Contains(c.Content, "OUTSIDE_DIR_SECRET") {
			t.Fatalf("content from a symlinked outside dir leaked into chunk %s", c.ID)
		}
	}
}
