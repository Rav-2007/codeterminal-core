package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sha256Hex(t *testing.T, content string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// fixtureServer serves the given name -> content map over HTTP on loopback
// only (httptest.NewServer always binds 127.0.0.1), standing in for
// Hugging Face without touching the real network.
func fixtureServer(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		content, ok := files[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(content))
	}))
}

func TestEnsureModelFiles_DownloadsAndVerifiesOnFirstRun(t *testing.T) {
	const content = "fake onnx model bytes, small enough for a test fixture"
	srv := fixtureServer(t, map[string]string{"model.bin": content})
	defer srv.Close()

	assets := []modelAsset{
		{name: "model.bin", url: srv.URL + "/model.bin", size: int64(len(content)), sha256Hex: sha256Hex(t, content)},
	}

	cacheDir := t.TempDir()
	logger := log.New(io.Discard, "", 0)

	dir, err := EnsureModelFiles(context.Background(), cacheDir, assets, logger)
	if err != nil {
		t.Fatalf("EnsureModelFiles: %v", err)
	}
	if dir != cacheDir {
		t.Errorf("returned dir = %q, want %q", dir, cacheDir)
	}

	got, err := os.ReadFile(filepath.Join(cacheDir, "model.bin"))
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if string(got) != content {
		t.Errorf("downloaded content = %q, want %q", got, content)
	}

	if _, err := os.Stat(filepath.Join(cacheDir, "model.bin.part")); !os.IsNotExist(err) {
		t.Errorf("temp .part file should not remain after a successful download, stat err = %v", err)
	}
}

func TestEnsureModelFiles_CacheHitSkipsNetwork(t *testing.T) {
	const content = "cached content that must not be re-fetched"
	srv := fixtureServer(t, map[string]string{"model.bin": content})

	assets := []modelAsset{
		{name: "model.bin", url: srv.URL + "/model.bin", size: int64(len(content)), sha256Hex: sha256Hex(t, content)},
	}

	cacheDir := t.TempDir()
	logger := log.New(io.Discard, "", 0)

	if _, err := EnsureModelFiles(context.Background(), cacheDir, assets, logger); err != nil {
		t.Fatalf("first EnsureModelFiles (populating cache): %v", err)
	}

	// Shut the server down. If the second call tries to hit the network at
	// all, it will fail with a connection error against this dead address.
	srv.Close()

	if _, err := EnsureModelFiles(context.Background(), cacheDir, assets, logger); err != nil {
		t.Fatalf("second EnsureModelFiles should be a pure cache hit with no network access, got error: %v", err)
	}
}

func TestEnsureModelFiles_ChecksumMismatchFailsLoudly(t *testing.T) {
	const served = "this is not the content the checksum expects"
	srv := fixtureServer(t, map[string]string{"model.bin": served})
	defer srv.Close()

	assets := []modelAsset{
		{name: "model.bin", url: srv.URL + "/model.bin", size: int64(len(served)), sha256Hex: sha256Hex(t, "totally different expected content")},
	}

	cacheDir := t.TempDir()
	logger := log.New(io.Discard, "", 0)

	_, err := EnsureModelFiles(context.Background(), cacheDir, assets, logger)
	if err == nil {
		t.Fatal("expected a checksum mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error = %v, want it to mention checksum mismatch", err)
	}

	if _, statErr := os.Stat(filepath.Join(cacheDir, "model.bin")); !os.IsNotExist(statErr) {
		t.Errorf("a checksum-mismatched file must not be left cached, stat err = %v", statErr)
	}
}

func TestEnsureModelFiles_SizeMismatchFailsLoudly(t *testing.T) {
	const served = "short"
	srv := fixtureServer(t, map[string]string{"model.bin": served})
	defer srv.Close()

	assets := []modelAsset{
		{name: "model.bin", url: srv.URL + "/model.bin", size: 99999, sha256Hex: sha256Hex(t, served)},
	}

	cacheDir := t.TempDir()
	logger := log.New(io.Discard, "", 0)

	_, err := EnsureModelFiles(context.Background(), cacheDir, assets, logger)
	if err == nil {
		t.Fatal("expected a size mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "size mismatch") {
		t.Errorf("error = %v, want it to mention size mismatch", err)
	}
	if _, statErr := os.Stat(filepath.Join(cacheDir, "model.bin")); !os.IsNotExist(statErr) {
		t.Errorf("a size-mismatched file must not be left cached, stat err = %v", statErr)
	}
}

func TestEnsureModelFiles_MultipleAssetsAllVerified(t *testing.T) {
	files := map[string]string{
		"a.bin": "content of asset a",
		"b.bin": "content of asset b, slightly longer",
	}
	srv := fixtureServer(t, files)
	defer srv.Close()

	var assets []modelAsset
	for name, content := range files {
		assets = append(assets, modelAsset{
			name:      name,
			url:       srv.URL + "/" + name,
			size:      int64(len(content)),
			sha256Hex: sha256Hex(t, content),
		})
	}

	cacheDir := t.TempDir()
	logger := log.New(io.Discard, "", 0)

	if _, err := EnsureModelFiles(context.Background(), cacheDir, assets, logger); err != nil {
		t.Fatalf("EnsureModelFiles: %v", err)
	}

	for name, content := range files {
		got, err := os.ReadFile(filepath.Join(cacheDir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if string(got) != content {
			t.Errorf("%s content = %q, want %q", name, got, content)
		}
	}
}
