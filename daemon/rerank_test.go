package main

import "testing"

func TestClassWeight_OrderingIsCodeOverOtherOverConfigOverDoc(t *testing.T) {
	if !(classWeight(FileClassCode) > classWeight(FileClassOther) &&
		classWeight(FileClassOther) > classWeight(FileClassConfig) &&
		classWeight(FileClassConfig) > classWeight(FileClassDoc)) {
		t.Fatalf("expected code > other > config > doc, got code=%v other=%v config=%v doc=%v",
			classWeight(FileClassCode), classWeight(FileClassOther), classWeight(FileClassConfig), classWeight(FileClassDoc))
	}
}

func TestEffectiveClass_PrefersStoredClassOverRecomputing(t *testing.T) {
	// A stored (possibly stale-by-path-rename) class should win over what
	// the path would reclassify to.
	c := Chunk{FilePath: "README.md", Class: FileClassCode}
	if got := effectiveClass(c); got != FileClassCode {
		t.Errorf("effectiveClass = %q, want the stored class %q to be honored", got, FileClassCode)
	}
}

func TestEffectiveClass_FallsBackToClassifyFileWhenEmpty(t *testing.T) {
	// Chunks built by hand (as in many existing tests) never set Class —
	// they must not be silently treated as some arbitrary default; the path
	// is reclassified on the fly instead.
	c := Chunk{FilePath: "daemon/router.go"}
	if got := effectiveClass(c); got != FileClassCode {
		t.Errorf("effectiveClass = %q, want %q (recomputed from FilePath)", got, FileClassCode)
	}
}

func TestRerankChunks_BoostsCodeOverDocAtComparableSimilarity(t *testing.T) {
	// The core bug being fixed: a doc chunk with a slightly higher raw
	// score than a code chunk should lose after weighting, when the gap is
	// small enough for the tilt to close it.
	candidates := []Chunk{
		{FilePath: "README.md", Score: 0.60, Class: FileClassDoc},
		{FilePath: "daemon/editblock.go", Score: 0.55, Class: FileClassCode},
	}
	got := rerankChunks(candidates, 2)
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2", len(got))
	}
	if got[0].FilePath != "daemon/editblock.go" {
		t.Errorf("top result = %q, want the code chunk to win after weighting", got[0].FilePath)
	}
	// RawScore must be preserved (the original similarity), Score becomes
	// the weighted value actually used for ordering.
	if got[0].RawScore != 0.55 {
		t.Errorf("RawScore = %v, want the original raw similarity 0.55", got[0].RawScore)
	}
	if got[0].Score == got[0].RawScore {
		t.Errorf("Score should differ from RawScore once class-weighted, got both = %v", got[0].Score)
	}
}

func TestRerankChunks_DocCanStillWinAtHighEnoughSimilarity(t *testing.T) {
	// Tilt, not ban: a doc chunk with a much higher raw score must still be
	// able to win — this is the "don't make docs unfindable" guarantee.
	candidates := []Chunk{
		{FilePath: "README.md", Score: 0.95, Class: FileClassDoc},
		{FilePath: "daemon/editblock.go", Score: 0.30, Class: FileClassCode},
	}
	got := rerankChunks(candidates, 2)
	if got[0].FilePath != "README.md" {
		t.Errorf("top result = %q, want the doc to win when its raw similarity is overwhelmingly higher", got[0].FilePath)
	}
}

func TestRerankChunks_TruncatesToK(t *testing.T) {
	candidates := []Chunk{
		{FilePath: "a.go", Score: 0.9, Class: FileClassCode},
		{FilePath: "b.go", Score: 0.8, Class: FileClassCode},
		{FilePath: "c.go", Score: 0.7, Class: FileClassCode},
	}
	got := rerankChunks(candidates, 2)
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2 (truncated to k)", len(got))
	}
	if got[0].FilePath != "a.go" || got[1].FilePath != "b.go" {
		t.Errorf("got %+v, want [a.go, b.go] (best two by weighted score)", got)
	}
}

func TestRerankPoolSize_UsesOverfetchFactorWithFloor(t *testing.T) {
	if got := rerankPoolSize(3); got != rerankOverfetchFloor {
		t.Errorf("rerankPoolSize(3) = %d, want the floor %d (3*%d=%d is below it)", got, rerankOverfetchFloor, rerankOverfetchFactor, 3*rerankOverfetchFactor)
	}
	if got := rerankPoolSize(10); got != 10*rerankOverfetchFactor {
		t.Errorf("rerankPoolSize(10) = %d, want %d", got, 10*rerankOverfetchFactor)
	}
}
