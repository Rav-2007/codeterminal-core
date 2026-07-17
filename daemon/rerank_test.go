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

// TestLooksTestSeeking_IgnoresGoToolFailureNoise is the regression guard for
// the measured false positive (see edit_eval_test.go and BACKLOG.md item
// (b)): a raw captured `go test`/`go vet` failure is an edit-shaped query
// about IMPLEMENTATION ("fix the code so this compiles/passes"), not a
// question about test files -- but it contains a word-bounded "test" via
// Go's own toolchain output shapes (the ".test" compiled-test-binary
// suffix, and the bare "go test" command name), which used to invert
// testClassWeight's down-weight and let _test.go chunks bury the
// implementation chunk the query is actually asking to fix. Deliberately
// reworded/generic rather than copied verbatim from edit_eval_test.go's
// case queries -- see TestLooksTestSeeking's own comment above on why a
// near-duplicate of real eval query text is a self-reference risk once
// this file is indexed by that eval harness.
func TestLooksTestSeeking_IgnoresGoToolFailureNoise(t *testing.T) {
	notSeeking := []string{
		"# codeterminal/daemon [codeterminal/daemon.test]\n./foo_test.go:12:4: x undefined\nFAIL\tcodeterminal/daemon [build failed]",
		"FAIL\tcodeterminal/widget [build failed]\n# codeterminal/widget [codeterminal/widget.test]\n./bar_test.go:9:2: undefined: Baz",
		"$ go test ./daemon/...\nfoo.go:20:4: undefined: Bar",
	}
	for _, q := range notSeeking {
		if looksTestSeeking(q) {
			t.Errorf("looksTestSeeking(%q) = true, want false (go-tool-output noise, not a question about tests)", q)
		}
	}
}

// TestLooksTestSeeking_StillFiresOnTestFuncNameInsideToolOutput documents a
// KNOWN, DELIBERATELY UNFIXED gap in the same family as the one the test
// above guards: testFuncPattern (unlike testSeekingWords) is left untouched
// by this fix, so a genuine test-FAILURE's own output -- which necessarily
// names the failing TestXxx function -- still suppresses the down-weight,
// same as a real query about that test would. Measured in
// edit_eval_test.go's zdr-refusal-phrasing case: harmless there (the target
// chunk still won at rank #2 despite the suppressed down-weight), but this
// is the honest boundary of this fix, not swept under the rug. A clean
// rule distinguishing "the query is ABOUT this test" from "this test's own
// name appears in pasted failure output" needs a different signal (e.g.
// whether the TestXxx name is the query's subject vs. buried in a
// multi-line log) than a regex over raw text can give without becoming
// exactly the "fragile pile of special cases" this fix is deliberately
// avoiding.
func TestLooksTestSeeking_StillFiresOnTestFuncNameInsideToolOutput(t *testing.T) {
	q := "--- FAIL: TestSomethingUnrelated (0.00s)\n    foo_test.go:20: got 1, want 2\nFAIL\tcodeterminal/daemon\t0.10s"
	if !looksTestSeeking(q) {
		t.Errorf("looksTestSeeking(%q) = false, want true (testFuncPattern still fires on a TestXxx name embedded in tool output -- known gap, see comment)", q)
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

func TestLexicalPoolSize_UsesOverfetchFactorWithFloor(t *testing.T) {
	if got := lexicalPoolSize(3); got != lexicalOverfetchFloor {
		t.Errorf("lexicalPoolSize(3) = %d, want the floor %d (3*%d=%d is below it)", got, lexicalOverfetchFloor, lexicalOverfetchFactor, 3*lexicalOverfetchFactor)
	}
	if got := lexicalPoolSize(10); got != 10*lexicalOverfetchFactor {
		t.Errorf("lexicalPoolSize(10) = %d, want %d", got, 10*lexicalOverfetchFactor)
	}
}

func TestFuseRRF_TopRankInEitherTierWins(t *testing.T) {
	semantic := []Chunk{
		{ID: "both.go:1-10", FilePath: "both.go"},
		{ID: "semantic-only.go:1-10", FilePath: "semantic-only.go"},
	}
	lexical := []Chunk{
		{ID: "both.go:1-10", FilePath: "both.go"},
		{ID: "lexical-only.go:1-10", FilePath: "lexical-only.go"},
	}

	got := fuseRRF(semantic, lexical, rrfK)
	if len(got) != 3 {
		t.Fatalf("got %d fused chunks, want 3 (deduped union)", len(got))
	}
	if got[0].FilePath != "both.go" {
		t.Errorf("top result = %q, want the chunk ranked #1 in both tiers to win", got[0].FilePath)
	}
}

// TestFuseRRF_SingleStrongTierBeatsTwoModerateTiers is the regression guard
// for the actual measured design decision: fuseRRF uses MAX, not SUM, across
// tiers (see its doc comment in rerank.go). Under summed RRF, a chunk ranked
// moderately by BOTH tiers can out-score one ranked #1 by only one tier —
// this is precisely the real-repo failure mode that motivated the switch
// (provider_test.go's prose ranking respectably on both tiers simultaneously
// out-scored provider.go's actual answer chunk, findable only lexically). If
// fuseRRF ever regresses back to summed scoring, this test must catch it:
// the chunk ranked #1 by lexical alone must beat one ranked #3 by BOTH.
func TestFuseRRF_SingleStrongTierBeatsTwoModerateTiers(t *testing.T) {
	semantic := []Chunk{
		{ID: "filler1.go:1-10", FilePath: "filler1.go"},
		{ID: "filler2.go:1-10", FilePath: "filler2.go"},
		{ID: "moderate-both.go:1-10", FilePath: "moderate-both.go"},
	}
	lexical := []Chunk{
		{ID: "strong-lexical-only.go:91-130", FilePath: "strong-lexical-only.go"},
		{ID: "filler3.go:1-10", FilePath: "filler3.go"},
		{ID: "moderate-both.go:1-10", FilePath: "moderate-both.go"},
	}

	got := fuseRRF(semantic, lexical, rrfK)
	var strongScore, moderateScore float32
	for _, c := range got {
		switch c.ID {
		case "strong-lexical-only.go:91-130":
			strongScore = c.Score
		case "moderate-both.go:1-10":
			moderateScore = c.Score
		}
	}
	if strongScore <= moderateScore {
		t.Errorf("strong-lexical-only score=%v, moderate-both score=%v; want the chunk ranked #1 by lexical alone to outscore one ranked #3 by both tiers (max fusion, not summed — under summed RRF, rank-3-in-both's combined score would exceed rank-1-in-one's)", strongScore, moderateScore)
	}
}

func TestFuseRRF_LexicalOnlyChunkStillSurfacesEvenWithNoSemanticRank(t *testing.T) {
	// The core case this exists to fix: a chunk absent from the semantic
	// pool entirely (ranked too far outside it to have been fetched) must
	// still be findable if the lexical tier ranks it well.
	semantic := []Chunk{
		{ID: "unrelated1.go:1-10", FilePath: "unrelated1.go"},
		{ID: "unrelated2.go:1-10", FilePath: "unrelated2.go"},
	}
	lexical := []Chunk{
		{ID: "exact-symbol.go:91-130", FilePath: "exact-symbol.go"},
	}

	got := fuseRRF(semantic, lexical, rrfK)
	found := false
	for _, c := range got {
		if c.ID == "exact-symbol.go:91-130" {
			found = true
		}
	}
	if !found {
		t.Fatalf("lexical-only chunk missing from fused result entirely: %+v", got)
	}
}

func TestFuseRRF_EmptyLexicalDegradesToSemanticOrder(t *testing.T) {
	semantic := []Chunk{
		{ID: "a.go:1-10", FilePath: "a.go"},
		{ID: "b.go:1-10", FilePath: "b.go"},
	}

	got := fuseRRF(semantic, nil, rrfK)
	if len(got) != 2 {
		t.Fatalf("got %d fused chunks, want 2", len(got))
	}
	if got[0].FilePath != "a.go" || got[1].FilePath != "b.go" {
		t.Errorf("got %+v, want semantic order preserved when lexical is empty", got)
	}
}
