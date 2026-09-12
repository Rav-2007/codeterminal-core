//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The embedder stamp is the third file in the index directory and was the only
// one still written world-readable.
//
// 2.3-A. lexical.db is 0600, chromem's collection directory is 0700, and the
// stamp was 0644 -- with no stated reason; the write's comment addresses
// O_NOFOLLOW and is silent on the mode. It is NOT a live disclosure, because
// the containing directory is 0700 and nobody else can traverse to it, and it
// is filed as the defence-in-depth inconsistency it is rather than inflated.
//
// UNIX ONLY, AND NOT BECAUSE WINDOWS IS UNTESTABLE. The first version of this
// test carried no build tag and asserted info.Mode().Perm() directly. It passed
// here and FAILED the windows-latest daemon job in CI, for exactly the reason
// ownerperm_windows_test.go already documents at length: Go synthesises 0666 or
// 0777 on Windows from one FILE_ATTRIBUTE_READONLY bit, so a Perm() assertion
// there reads a CONSTANT. That helper exists because five tests once made this
// mistake; this was the sixth, and it was written without looking for the prior
// art that had already solved it.
//
// The Windows property is real but DIFFERENT, and is deliberately not asserted
// here rather than asserted badly: restrictToOwner(indexDir, 0700)
// (lexicalstore.go:131) sets SUB_CONTAINERS_AND_OBJECTS_INHERIT, so a file
// created in that directory inherits an owner-only DACL from a protected
// parent. assertOwnerOnly requires the "P" flag on the object ITSELF, which an
// inheriting child does not carry -- so calling it here would report a leak
// about a descriptor that is correct, which is the failure mode that helper's
// own comment warns is not the harmless direction. Asserting the inherited case
// needs a Windows runner to develop against; this file states the gap instead
// of papering over it.
func TestEmbedderStampIsNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	embedder := &PlaceholderEmbedder{}
	if err := writeEmbedderStamp(dir, embedder); err != nil {
		t.Fatalf("writeEmbedderStamp: %v", err)
	}

	path := filepath.Join(dir, embedderStampFileName)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stamp was not written: %v", err)
	}
	// Vacuity floor: a zero-length or absent file would satisfy a mode check
	// without the stamp ever having been written.
	if fi.Size() == 0 {
		t.Fatal("vacuity floor: the stamp is empty, so its mode proves nothing")
	}
	assertOwnerOnly(t, path, "it records the model build and when the workspace was indexed")
}
