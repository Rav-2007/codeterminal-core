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
	got := rerankChunks(candidates, 2, "")
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
	got := rerankChunks(candidates, 2, "")
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
	got := rerankChunks(candidates, 2, "")
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2 (truncated to k)", len(got))
	}
	if got[0].FilePath != "a.go" || got[1].FilePath != "b.go" {
		t.Errorf("got %+v, want [a.go, b.go] (best two by weighted score)", got)
	}
}

func TestClassWeight_TestGetsSameBaseWeightAsCode(t *testing.T) {
	// classWeight itself is query-blind; the test down-weight is applied
	// conditionally in rerankChunks, not baked into this class-only mapping.
	if classWeight(FileClassTest) != classWeight(FileClassCode) {
		t.Errorf("classWeight(FileClassTest) = %v, want equal to classWeight(FileClassCode) = %v", classWeight(FileClassTest), classWeight(FileClassCode))
	}
}

func TestLooksTestSeeking(t *testing.T) {
	// Deliberately reworded away from this repo's real rerank_eval_test.go
	// query strings: this file is itself indexed by that real-repo eval
	// harness, and a near-verbatim copy of an eval query embeds as a
	// near-duplicate of it, artificially dominating that query's raw
	// candidate pool and crowding out the chunk the eval is trying to rank.
	seeking := []string{
		"what does the test coverage look like for the router package",
		"what does this test verify?",
		"walk me through the test suite for this module",
		"is there a spec describing this behavior",
		"what does TestHandlerAcceptsValidInput check?",
	}
	for _, q := range seeking {
		if !looksTestSeeking(q) {
			t.Errorf("looksTestSeeking(%q) = false, want true", q)
		}
	}

	notSeeking := []string{
		"explain how requests get routed to a backend",
		"summarize the retry backoff strategy",
		"what is the newest model we route to",
		"describe the daemon's startup sequence",
	}
	for _, q := range notSeeking {
		if looksTestSeeking(q) {
			t.Errorf("looksTestSeeking(%q) = true, want false", q)
		}
	}
}

func TestRerankChunks_DownWeightsTestFileForImplementationQuery(t *testing.T) {
	// A _test.go chunk with a modest raw-score edge over the real
	// implementation should lose once down-weighted, for a query that
	// isn't itself asking about tests. Query text kept generic/unrelated to
	// this repo's real eval corpus (see TestLooksTestSeeking comment).
	candidates := []Chunk{
		{FilePath: "daemon/widget_test.go", Score: 0.70, Class: FileClassTest},
		{FilePath: "daemon/widget.go", Score: 0.66, Class: FileClassCode},
	}
	got := rerankChunks(candidates, 2, "explain how the widget subsystem processes a request")
	if got[0].FilePath != "daemon/widget.go" {
		t.Errorf("top result = %q, want the implementation to win once the test chunk is down-weighted", got[0].FilePath)
	}
}

func TestRerankChunks_SkipsTestDownWeightForTestSeekingQuery(t *testing.T) {
	// The same candidates, but a query that's genuinely about tests must
	// not have its test chunk penalized — raw-score ordering should win
	// unchanged (both get the same codeClassWeight).
	candidates := []Chunk{
		{FilePath: "daemon/widget_test.go", Score: 0.70, Class: FileClassTest},
		{FilePath: "daemon/widget.go", Score: 0.66, Class: FileClassCode},
	}
	got := rerankChunks(candidates, 2, "how is the widget subsystem tested")
	if got[0].FilePath != "daemon/widget_test.go" {
		t.Errorf("top result = %q, want the test file to win for a test-seeking query (no down-weight applied)", got[0].FilePath)
	}
}

func TestRerankChunks_TestCanStillWinAtHighEnoughSimilarity(t *testing.T) {
	// Tilt, not ban: a test chunk with an overwhelmingly higher raw score
	// must still be able to win even for an implementation-seeking query.
	candidates := []Chunk{
		{FilePath: "daemon/widget_test.go", Score: 0.95, Class: FileClassTest},
		{FilePath: "daemon/widget.go", Score: 0.30, Class: FileClassCode},
	}
	got := rerankChunks(candidates, 2, "explain how the widget subsystem processes a request")
	if got[0].FilePath != "daemon/widget_test.go" {
		t.Errorf("top result = %q, want the test file to win when its raw similarity is overwhelmingly higher", got[0].FilePath)
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
