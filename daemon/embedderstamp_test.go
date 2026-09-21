package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/helper/helperproto"
)

func TestEmbedderStamp_RoundTripMatches(t *testing.T) {
	dir := t.TempDir()
	embedder := &fakeEmbedder{dim: 384}

	if err := writeEmbedderStamp(dir, embedder); err != nil {
		t.Fatalf("writeEmbedderStamp: %v", err)
	}
	if err := checkEmbedderStamp(dir, embedder, false); err != nil {
		t.Fatalf("checkEmbedderStamp should accept a matching stamp, got: %v", err)
	}
}

func TestEmbedderStamp_MismatchedIDRefused(t *testing.T) {
	dir := t.TempDir()
	builtWith := NewPlaceholderEmbedder(embedDim)
	queriedWith := &fakeEmbedder{dim: embedDim} // same Dim, different ID — this is the case Dim alone can't catch

	if err := writeEmbedderStamp(dir, builtWith); err != nil {
		t.Fatalf("writeEmbedderStamp: %v", err)
	}

	err := checkEmbedderStamp(dir, queriedWith, false)
	if err == nil {
		t.Fatal("expected a mismatch error when IDs differ despite equal Dim, got nil")
	}
	if !strings.Contains(err.Error(), "re-index required") {
		t.Errorf("error = %v, want it to mention re-index required", err)
	}
}

func TestEmbedderStamp_MissingStampOnNonEmptyStoreRefused(t *testing.T) {
	dir := t.TempDir() // no stamp file written at all — simulates a pre-existing 5a index

	err := checkEmbedderStamp(dir, &fakeEmbedder{dim: embedDim}, false)
	if err == nil {
		t.Fatal("expected an error for a missing stamp on a non-empty store, got nil")
	}
	if !strings.Contains(err.Error(), "re-index required") {
		t.Errorf("error = %v, want it to mention re-index required", err)
	}
}

func TestEmbedderStamp_MissingStampOnEmptyStoreAllowed(t *testing.T) {
	dir := t.TempDir()

	err := checkEmbedderStamp(dir, &fakeEmbedder{dim: embedDim}, true)
	if err != nil {
		t.Fatalf("an empty store should never be refused for a missing stamp, got: %v", err)
	}
}

// TestEmbedderStamp_OldSchemaVersionRefused proves the index-schema staleness
// guard added alongside file-class metadata: a stamp written before
// IndexSchemaVersion existed (or with an explicitly older value) must be
// refused even though the embedder identity itself matches, since its
// chunks predate class metadata entirely.
func TestEmbedderStamp_OldSchemaVersionRefused(t *testing.T) {
	dir := t.TempDir()
	embedder := &fakeEmbedder{dim: embedDim}

	// Simulate a stamp written by a pre-class-metadata binary: same
	// embedder identity, but IndexSchemaVersion absent (zero value) rather
	// than the field just not existing in the JSON, since both decode the
	// same way.
	oldStamp := embedderStamp{EmbedderID: embedder.ID(), Dim: embedder.Dim(), IndexSchemaVersion: 0}
	data, err := json.Marshal(oldStamp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, embedderStampFileName), data, 0644); err != nil {
		t.Fatalf("writing old stamp: %v", err)
	}

	err = checkEmbedderStamp(dir, embedder, false)
	if err == nil {
		t.Fatal("expected an error for an old index-schema-version stamp, got nil")
	}
	if !strings.Contains(err.Error(), "re-index required") {
		t.Errorf("error = %v, want it to mention re-index required", err)
	}
}

func TestEmbedderStamp_CorruptStampRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, embedderStampFileName), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	err := checkEmbedderStamp(dir, &fakeEmbedder{dim: embedDim}, false)
	if err == nil {
		t.Fatal("expected an error for a corrupt stamp file, got nil")
	}
	if !strings.Contains(err.Error(), "re-index required") {
		t.Errorf("error = %v, want it to mention re-index required", err)
	}
}

// THE SEQUENCE LENGTH IS PART OF WHAT A VECTOR MEANS.
//
// The helper truncates every input to helperproto.MaxSequenceLength before
// embedding it, so that number decides which part of a chunk the vector
// actually describes. An index built at 256 and queried at 512 is comparing
// vectors of half-chunks against vectors of whole ones, and embedderStamp is
// the only thing that can notice -- but only if the identity it stamps moves
// when the cap moves. It did not: ID() was a fixed string that named the model
// and the runtime and said nothing about how much of the input was read.
func TestTheEmbedderIdentityCoversTheSequenceLength(t *testing.T) {
	id := (&BgeEmbedder{}).ID()
	if !strings.Contains(id, fmt.Sprintf("seq%d", helperproto.MaxSequenceLength)) {
		t.Errorf("the embedder ID %q does not name the sequence cap (%d), so changing the cap "+
			"leaves every existing index looking valid while its vectors describe different text",
			id, helperproto.MaxSequenceLength)
	}
	// ANTI-VACUITY: the ID must still identify the model and runtime too, or
	// this "fix" traded one blind spot for another.
	for _, want := range []string{"bge-small-en-v1.5-int8", "onnxruntime-1.26.0"} {
		if !strings.Contains(id, want) {
			t.Errorf("the embedder ID %q no longer names %q", id, want)
		}
	}
}

// A workspace indexed by an embedder that has since changed its sequence cap
// must be REFUSED, not queried. This drives the real guard rather than
// asserting on the ID string.
func TestAnIndexBuiltAtADifferentSequenceCapIsRefused(t *testing.T) {
	dir := t.TempDir()
	stale := embedderStamp{
		EmbedderID:         "bge-small-en-v1.5-int8+onnxruntime-1.26.0+seq256",
		Dim:                embedDim,
		IndexSchemaVersion: currentIndexSchemaVersion,
		// Current, so this test keeps measuring the SEQUENCE CAP. Every other
		// field has to be what today's binary writes, or the refusal below
		// could be coming from any of them.
		ChunkerID: chunkerID,
	}
	data, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, embedderStampFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkEmbedderStamp(dir, &BgeEmbedder{}, false); err == nil {
		t.Error("an index whose vectors were built reading only the first 256 tokens of each chunk " +
			"is accepted by an embedder that now reads 512")
	}
	// ANTI-VACUITY: the same stamp at the CURRENT cap has to be accepted, or
	// the check above is just refusing everything.
	fresh := stale
	fresh.EmbedderID = (&BgeEmbedder{}).ID()
	data, err = json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, embedderStampFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkEmbedderStamp(dir, &BgeEmbedder{}, false); err != nil {
		t.Errorf("a matching stamp is refused too (%v), so the refusal above proves nothing", err)
	}
}
