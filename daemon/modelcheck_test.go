package main

import (
	"os"
	"path/filepath"
	"testing"
)

// `download-model --check` exists so a CALLER can ask whether retrieval will
// work without triggering a 43-110 MB download as a side effect of asking. The
// VS Code extension asks it on first activation to decide whether to offer the
// download.
//
// The property worth testing is not "does it notice a missing file" -- any
// implementation does that. It is that the check and the DOWNLOAD agree on what
// "cached" means, which is why modelAssetsCached calls the same verifyAsset
// EnsureModelFiles calls.
//
// NEUTER CHECK: replace modelAssetsCached's verifyAsset call with a bare
// os.Stat and reportsNotCachedForACorruptAsset FAILS -- measured. That is the
// cheaper check someone would reach for, and it answers "ready" for a file
// EnsureModelFiles would re-download, so the extension would skip the prompt
// and the user would sit with retrieval permanently off and no way to find out
// why.

func writeAsset(t *testing.T, dir string, a modelAsset, body []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, a.name), body, 0o644); err != nil {
		t.Fatalf("write %s: %v", a.name, err)
	}
}

// oneAsset is a real entry from bgeModelAssets, small enough to materialise in
// a test. Using a real one keeps the fixture honest: a hand-made asset with a
// hand-made checksum would prove the test agrees with itself.
func oneAsset(t *testing.T) modelAsset {
	t.Helper()
	for _, a := range bgeModelAssets {
		if a.size < 4096 {
			return a
		}
	}
	t.Skip("no small asset in bgeModelAssets to build a fixture from")
	return modelAsset{}
}

func TestModelAssetsCached_FalseWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	if modelAssetsCached(dir, bgeModelAssets) {
		t.Fatal("reported cached against an empty directory")
	}
}

// The one that distinguishes a real check from a cheap one. The file EXISTS and
// has the RIGHT SIZE, and is still not the asset -- only the checksum says so.
func TestModelAssetsCached_ReportsNotCachedForACorruptAsset(t *testing.T) {
	dir := t.TempDir()
	a := oneAsset(t)

	corrupt := make([]byte, a.size) // right size, wrong bytes
	for i := range corrupt {
		corrupt[i] = 'x'
	}
	writeAsset(t, dir, a, corrupt)

	if modelAssetsCached(dir, []modelAsset{a}) {
		t.Fatalf("reported %s cached: it is the right SIZE but the wrong BYTES. "+
			"A size-or-existence check passes here; EnsureModelFiles would re-download it. "+
			"The check and the download must not disagree about what cached means", a.name)
	}
}

// A truncated file is the shape a cancelled download leaves behind, and is the
// most likely real-world corruption.
func TestModelAssetsCached_ReportsNotCachedForATruncatedAsset(t *testing.T) {
	dir := t.TempDir()
	a := oneAsset(t)
	writeAsset(t, dir, a, []byte("partial"))

	if modelAssetsCached(dir, []modelAsset{a}) {
		t.Fatalf("reported truncated %s cached", a.name)
	}
}

// Absence of the extracted library must read as "not cached" rather than as an
// error, so the extension offers a download instead of reporting a fault.
func TestONNXRuntimeLibCached_FalseWhenAbsent(t *testing.T) {
	if onnxRuntimeLibCached(t.TempDir()) {
		t.Fatal("reported the onnxruntime library cached against an empty directory")
	}
}
