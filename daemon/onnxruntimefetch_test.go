package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// releaseTargetPlatform maps a release target (the vsce-style names in
// scripts/release-targets.txt) to the GOOS/GOARCH its helper is built for.
var releaseTargetPlatform = map[string][2]string{
	"linux-x64":    {"linux", "amd64"},
	"linux-arm64":  {"linux", "arm64"},
	"darwin-arm64": {"darwin", "arm64"},
	"win32-x64":    {"windows", "amd64"},
	"win32-arm64":  {"windows", "arm64"},
}

// TestEveryReleaseTargetHasAPinnedONNXRuntime keeps the release's target list
// and onnxRuntimePlatforms together. A target added to the release without a
// pinned library here would ship a helper that cannot load its runtime: code
// search off on that platform, discovered only by a user.
func TestEveryReleaseTargetHasAPinnedONNXRuntime(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "scripts", "release-targets.txt"))
	if err != nil {
		t.Fatalf("reading the release target list: %v", err)
	}
	defer f.Close()

	var targets []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "signing-required:") {
			continue
		}
		targets = append(targets, strings.Fields(line)[0])
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(targets) == 0 {
		t.Fatal("scripts/release-targets.txt names no targets; this check would pass by inspecting nothing")
	}

	for _, target := range targets {
		p, ok := releaseTargetPlatform[target]
		if !ok {
			t.Errorf("release target %q has no GOOS/GOARCH mapping here; add it to releaseTargetPlatform", target)
			continue
		}
		if _, err := lookupONNXRuntimePlatform(p[0], p[1]); err != nil {
			t.Errorf("release target %q (%s/%s) has no pinned onnxruntime: %v", target, p[0], p[1], err)
		}
	}
}

// TestIntelMacSaysWhyItIsNotSupported pins the one platform that is refused on
// purpose: its error must say so plainly rather than read as a bug.
func TestIntelMacSaysWhyItIsNotSupported(t *testing.T) {
	_, err := lookupONNXRuntimePlatform("darwin", "amd64")
	if err == nil || !strings.Contains(err.Error(), "Intel Mac") {
		t.Fatalf("darwin/amd64 error = %v, want it to name Intel Mac", err)
	}
}

// TestTheLibraryIsFoundWhicheverWayTheArchiveNamesIt extracts the library
// from a .tgz and a .zip that store it with and without a leading ./, as
// Microsoft's macOS and Linux archives respectively do. Until this test,
// nothing ran the extraction at all, and the macOS pin had never matched.
func TestTheLibraryIsFoundWhicheverWayTheArchiveNamesIt(t *testing.T) {
	const member = "onnxruntime-osx-arm64-1.26.0/lib/libonnxruntime.1.26.0.dylib"
	for _, stored := range []string{member, "./" + member} {
		for _, ext := range []string{".tgz", ".zip"} {
			dir := t.TempDir()
			archive := filepath.Join(dir, "onnxruntime"+ext)
			writeTestArchive(t, archive, stored, "the library")
			dest := filepath.Join(dir, "libonnxruntime.dylib")
			if err := extractArchiveMember(archive, member, dest); err != nil {
				t.Errorf("%s storing %q: %v", ext, stored, err)
				continue
			}
			if got, _ := os.ReadFile(dest); string(got) != "the library" {
				t.Errorf("%s storing %q: extracted %q", ext, stored, got)
			}
		}
	}
}

// TestASymlinkMemberIsRefused keeps a pin that names a symlink (macOS's
// libonnxruntime.1.dylib is one) from extracting as an empty library.
func TestASymlinkMemberIsRefused(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "onnxruntime.tgz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "./lib/libonnxruntime.1.dylib", Typeflag: tar.TypeSymlink,
		Linkname: "libonnxruntime.1.26.0.dylib", Mode: 0o777}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []interface{ Close() error }{tw, gz, f} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	err = extractArchiveMember(archive, "lib/libonnxruntime.1.dylib", filepath.Join(t.TempDir(), "out"))
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("extracting a symlink member = %v, want it refused as not a regular file", err)
	}
}

// writeTestArchive writes a .tgz or .zip holding one directory and one file.
func writeTestArchive(t *testing.T, archive, name, body string) {
	t.Helper()
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	dir := strings.TrimSuffix(name, filepath.Base(name))
	if strings.HasSuffix(archive, ".zip") {
		zw := zip.NewWriter(f)
		if _, err := zw.Create(dir); err != nil {
			t.Fatal(err)
		}
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(&tar.Header{Name: dir, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			t.Fatal(err)
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
