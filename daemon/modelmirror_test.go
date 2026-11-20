package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// countingServer serves content at /<name> (or 404 when content is ""), and
// counts every request it gets.
func countingServer(t *testing.T, name, content string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if content == "" || strings.TrimPrefix(r.URL.Path, "/") != name {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(content))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

const mirrorTestContent = "the bytes the pin was taken from"

func mirroredAsset(t *testing.T, own, copy *httptest.Server) modelAsset {
	return modelAsset{
		name: "runtime.tgz", url: own.URL + "/runtime.tgz",
		size: int64(len(mirrorTestContent)), sha256Hex: sha256Hex(t, mirrorTestContent),
		mirrors: []string{copy.URL + "/runtime.tgz"},
	}
}

// THE 2026-11-19 CASE: the file's own address answers 404 and a copy of the
// same bytes is used.
//
// Neuter check: try only asset.url in fetchVerified.
func TestAPinnedFileComesFromItsCopyWhenItsAddressIsGone(t *testing.T) {
	own, _ := countingServer(t, "runtime.tgz", "")
	copySrv, copyHits := countingServer(t, "runtime.tgz", mirrorTestContent)
	dir := t.TempDir()

	if _, err := EnsureModelFiles(context.Background(), dir, []modelAsset{mirroredAsset(t, own, copySrv)}, log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("EnsureModelFiles: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "runtime.tgz")); string(got) != mirrorTestContent || copyHits.Load() != 1 {
		t.Errorf("cached %q after %d request(s) to the copy, want the pinned bytes from one", got, copyHits.Load())
	}
}

// A copy is trusted for nothing: bytes that are not the pinned file are
// refused and not left cached, and the error says what each source did.
func TestACopyThatIsNotThePinnedFileIsRefused(t *testing.T) {
	own, _ := countingServer(t, "runtime.tgz", "")
	copySrv, _ := countingServer(t, "runtime.tgz", "something else, same length!!!!!")
	dir := t.TempDir()

	_, err := EnsureModelFiles(context.Background(), dir, []modelAsset{mirroredAsset(t, own, copySrv)}, log.New(io.Discard, "", 0))
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "did not match its pin") {
		t.Fatalf("err = %v, want both the 404 and the copy's mismatch", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "runtime.tgz")); !os.IsNotExist(statErr) {
		t.Errorf("a file that did not match its pin was left cached (stat: %v)", statErr)
	}
}

// While the file's own address works, no copy is asked for anything.
func TestTheOwnAddressIsUsedWhileItWorks(t *testing.T) {
	own, ownHits := countingServer(t, "runtime.tgz", mirrorTestContent)
	copySrv, copyHits := countingServer(t, "runtime.tgz", mirrorTestContent)
	if _, err := EnsureModelFiles(context.Background(), t.TempDir(), []modelAsset{mirroredAsset(t, own, copySrv)}, log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("EnsureModelFiles: %v", err)
	}
	if ownHits.Load() != 1 || copyHits.Load() != 0 {
		t.Errorf("requests: own %d, copy %d; want 1 and 0", ownHits.Load(), copyHits.Load())
	}
}

// Wrong bytes from the file's own address are treated like no answer: the
// copy is tried, and the wrong file is not what ends up cached.
func TestAWrongFileFromTheOwnAddressFallsToTheCopy(t *testing.T) {
	own, _ := countingServer(t, "runtime.tgz", "tampered bytes of the same size!")
	copySrv, _ := countingServer(t, "runtime.tgz", mirrorTestContent)
	dir := t.TempDir()
	if _, err := EnsureModelFiles(context.Background(), dir, []modelAsset{mirroredAsset(t, own, copySrv)}, log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("EnsureModelFiles: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "runtime.tgz")); string(got) != mirrorTestContent {
		t.Errorf("cached %q, want the pinned bytes", got)
	}
}

// The copies of the ONNX Runtime archives are this project's release, one per
// archive, by the archive's own name -- and every platform has one. Windows got
// its copy last (2026-11-19, from the npmmirror package mirror, equal to the pin).
func TestEveryONNXRuntimeCopyIsTheArchiveByItsOwnName(t *testing.T) {
	if !strings.HasPrefix(onnxRuntimeCopies, "https://github.com/Rav-2007/codeterminal-core/releases/download/onnxruntime-"+onnxRuntimeVersion) {
		t.Fatalf("onnxRuntimeCopies = %q", onnxRuntimeCopies)
	}
	for _, p := range onnxRuntimePlatforms {
		for _, m := range p.archive.mirrors {
			if m != onnxRuntimeCopies+p.archive.name {
				t.Errorf("%s/%s: copy %q is not the release asset named %q", p.goos, p.goarch, m, p.archive.name)
			}
		}
		if len(p.archive.mirrors) == 0 { // a platform added later needs its copy too
			t.Errorf("%s/%s has no copy of its ONNX Runtime archive", p.goos, p.goarch)
		}
	}
}

// LIVE, run by .github/workflows/onnxruntime-copies.yml (weekly, and on any PR
// that touches the pins): every copy, fetched ALONE by the code a first index
// runs, is still there and still its pinned file. The test above only sees
// that the code NAMES a copy; a version bump whose copies were never uploaded,
// or a copy since replaced, passes it and fails this.
func TestEveryONNXRuntimeCopyServesItsPinnedFile(t *testing.T) {
	if os.Getenv("MOCHIII_CHECK_COPIES") == "" {
		t.Skip("downloads every archive (~123 MB); the onnxruntime-copies workflow sets MOCHIII_CHECK_COPIES=1")
	}
	for _, p := range onnxRuntimePlatforms {
		for _, m := range p.archive.mirrors {
			copyOnly := p.archive
			copyOnly.url, copyOnly.mirrors = m, nil
			if _, err := EnsureModelFiles(context.Background(), t.TempDir(), []modelAsset{copyOnly}, log.New(io.Discard, "", 0)); err != nil {
				t.Errorf("%s/%s: %v", p.goos, p.goarch, err)
			} else {
				t.Logf("%s/%s: %s is its pinned file", p.goos, p.goarch, m)
			}
		}
	}
}
