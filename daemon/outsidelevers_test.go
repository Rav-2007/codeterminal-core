package main

// The three levers the outside-repository eval was built to test
// (externaleval_test.go): test files beyond Go, setup files beyond Go, and
// widening to the method inside an oversized class. Each is off by default;
// these pin what each one does when it is on.

import (
	"fmt"
	"strings"
	"testing"
)

func TestTestPathsBeyondGoFollowEachConvention(t *testing.T) {
	for path, want := range map[string]bool{
		"tests/test_basic.py":                                true,
		"pkg/helpers_test.py":                                true,
		"src/middleware/cors/index.test.ts":                  true,
		"web/app.spec.jsx":                                   true,
		"gson/src/test/java/com/google/gson/FooTest.java":    true,
		"lib/src/main/java/com/example/ParserTests.java":     true,
		"tests/regression.rs":                                true,
		"src/__tests__/render.js":                            true,
		"src/flask/app.py":                                   false,
		"src/main/java/com/example/Latest.java":              false,
		"crates/ignore/src/walk.rs":                          false,
		"src/contest.ts":                                     false,
		"daemon/testdata/unboundedread_fixture.gotxt":        false,
		"daemon/server_test.go":                              false, // isTestFile's job, not this one
		"clients/vscode/src/testing/notATestDirectory.ts":    false,
		"src/middleware/serve-static/index.ts":               false,
		"gson/src/main/java/com/google/gson/JsonParser.java": false,
	} {
		if got := isTestPathBeyondGo(path); got != want {
			t.Errorf("isTestPathBeyondGo(%q) = %t, want %t", path, got, want)
		}
	}
}

// The class stored in an index is set when the file was indexed, so a policy
// read only at index time would leave every existing index ranked the old way.
// Neuter check: drop the TestPathsBeyondGo override from rerankChunksWith and
// the test chunk keeps its code weight.
func TestTheTestPolicyReachesAChunkIndexedAsCode(t *testing.T) {
	candidates := []Chunk{
		{ID: "tests/test_app.py:1-40", FilePath: "tests/test_app.py", Class: FileClassCode, Score: 1.0},
		{ID: "src/app.py:1-40", FilePath: "src/app.py", Class: FileClassCode, Score: 1.0},
	}
	const query = "how does the app start"

	off := rerankChunksWith(candidates, 2, query, rankPolicy{})
	if off[0].FilePath != "tests/test_app.py" {
		t.Fatalf("without the policy the stored class should rule and the order stay as given; got %s first", off[0].FilePath)
	}
	on := rerankChunksWith(candidates, 2, query, rankPolicy{TestPathsBeyondGo: true})
	if on[0].FilePath != "src/app.py" || on[1].Class != FileClassTest {
		t.Errorf("with the policy the Python test should rank as a test, below the code: got %s (%s) first, %s (%s) second",
			on[0].FilePath, on[0].Class, on[1].FilePath, on[1].Class)
	}
	// A question about tests still reaches them at full weight.
	seeking := rerankChunksWith(candidates, 2, "which test covers the app start", rankPolicy{TestPathsBeyondGo: true})
	if seeking[0].Score != seeking[1].Score {
		t.Errorf("a test-seeking question should not down-weight tests: scores %v and %v", seeking[0].Score, seeking[1].Score)
	}
}

func TestSetupFilesBeyondGoNameTheSameStems(t *testing.T) {
	for path, want := range map[string]bool{
		"src/config.py":         true,
		"src/main.rs":           true,
		"web/src/configure.ts":  true,
		"lib/initializers.js":   true,
		"src/main.go":           false, // setupFilesPattern's job
		"docs/config.md":        false,
		"src/domain.py":         false,
		"src/flask/__init__.py": false,
		"src/maintenance.java":  false,
	} {
		if got := isSetupFileBeyondGo(path); got != want {
			t.Errorf("isSetupFileBeyondGo(%q) = %t, want %t", path, got, want)
		}
	}
	if !isSetupFile("src/config.py", rankPolicy{SetupFilesBeyondGo: true}) || isSetupFile("src/config.py", rankPolicy{}) {
		t.Error("isSetupFile does not follow the policy for a Python config file")
	}
}

func TestBlockOpenersAreDeclarationsNotStatements(t *testing.T) {
	for line, want := range map[string]bool{
		"public void run() {":                            true,
		"private <T> T read(Class<T> c) {":               true,
		"async fetch(req: Request): Promise<Response> {": true,
		"pub fn search(&self) -> Result<()> {":           true,
		") -> Result<()> {":                              true,
		"it('Preflight default', async () => {":          true,
		"if (x) {":                                       false,
		"} else {":                                       false,
		"for (const a of b) {":                           false,
		"match value {":                                  false,
		"return {":                                       false,
		"{":                                              false,
		"foo(bar);":                                      false,
	} {
		if got := isBlockOpener(line, false); got != want {
			t.Errorf("isBlockOpener(%q) = %t, want %t", line, got, want)
		}
	}
	if !isBlockOpener("def open_session(self, app, request):", true) || isBlockOpener("if session is None:", true) {
		t.Error("Python openers are def and class lines, and nothing else")
	}
}

// pythonClass writes a class of methods long methods, each with a decorator,
// and returns its lines and where each method's decorator and last line are.
func pythonClass(methods, bodyLines int) ([]string, [][2]int) {
	lines := []string{"import os", "", "", "class Big(Base):", `    """A class far past the cap."""`, ""}
	var spans [][2]int
	for m := range methods {
		start := len(lines) + 1
		lines = append(lines, "    @property", fmt.Sprintf("    def method_%d(self, value):", m))
		for b := range bodyLines {
			lines = append(lines, fmt.Sprintf("        value = value + %d  # body line", b))
		}
		lines = append(lines, "        return value")
		spans = append(spans, [2]int{start, len(lines)})
		lines = append(lines, "")
	}
	return lines, spans
}

func TestAHitInsideAPythonClassFindsItsMethod(t *testing.T) {
	lines, spans := pythonClass(6, 70)
	extents := constructExtents(lines)
	third := spans[2]
	hitStart, hitEnd := third[0]+20, third[0]+59

	got := nestedBlocks(lines, extents, hitStart, hitEnd, 300, true)
	if len(got) != 1 || got[0] != third {
		t.Fatalf("nestedBlocks = %v, want just the third method %v, decorator included", got, third)
	}
	// A class under the cap is the top level's to widen; nothing nested is asked.
	if got := nestedBlocks(lines, extents, hitStart, hitEnd, 1000, true); got != nil {
		t.Errorf("with the class under the cap, nestedBlocks = %v, want nil", got)
	}
}

// A Java class declared "public final class" is not a declaration keyword, so
// constructExtents finds nothing and the whole file is searched.
func TestAHitInsideAFinalJavaClassFindsItsMethod(t *testing.T) {
	src := []string{"package com.example;", "", "import java.io.IOException;", "", "/** Docs. */", "public final class Big {"}
	var want [2]int
	for m := range 5 {
		start := len(src) + 1
		src = append(src, "  @Override")
		if m == 3 {
			src = append(src, fmt.Sprintf("  public String method%d(", m), "      String a,", "      String b)", "      throws IOException {")
		} else {
			src = append(src, fmt.Sprintf("  public String method%d(String a) {", m))
		}
		for b := range 80 {
			src = append(src, fmt.Sprintf("    a = a + \"%d\";", b))
		}
		src = append(src, "    return a;", "  }")
		if m == 3 {
			want = [2]int{start, len(src)}
		}
		src = append(src, "")
	}
	src = append(src, "}")

	if ext := constructExtents(src); len(ext) != 0 {
		t.Logf("constructExtents found %v; the test still holds, but its premise moved", ext)
	}
	got := nestedBlocks(src, constructExtents(src), want[0]+30, want[0]+69, 300, false)
	if len(got) != 1 || got[0][1] != want[1] {
		t.Fatalf("nestedBlocks = %v, want the fourth method ending at line %d", got, want[1])
	}
	if !strings.Contains(src[got[0][0]-1], "@Override") && !strings.Contains(src[got[0][0]-1], "public String method3(") {
		t.Errorf("the block starts at %q, not at the method or its annotation", src[got[0][0]-1])
	}
}

// End to end through widenHits: a hit inside an oversized Python class widens
// to its whole method with the policy on, and to fixed neighbours without it.
// Neuter check: drop the NestedConstructs branch from widenHits and the
// widened span stops covering the method.
func TestNestedWideningDeliversTheMethodNotItsNeighbours(t *testing.T) {
	// Methods longer than a hit and its two neighbours cover, so the two
	// policies deliver different spans.
	lines, spans := pythonClass(6, 200)
	root := realTempDir(t)
	writeTempFile(t, root, "pkg/big.py", strings.Join(lines, "\n")+"\n")
	all := chunksOf(t, root, "pkg/big.py")
	third := spans[2]
	var hit Chunk
	for _, c := range all {
		if c.StartLine > third[0] && c.EndLine < third[1] {
			hit = c
			break
		}
	}
	if hit.ID == "" {
		t.Fatal("no chunk lies inside the third method")
	}

	on := mergeAdjacentChunks(expandToNeighbours([]Chunk{hit}, root, expandPolicy{TopN: 1, ConstructCap: 300, NestedConstructs: true}))
	if len(on) != 1 || on[0].StartLine > third[0] || on[0].EndLine < third[1] {
		t.Errorf("with nested widening the span is %d-%d, want it to cover the method %v", on[0].StartLine, on[0].EndLine, third)
	}
	off := expandToNeighbours([]Chunk{hit}, root, expandPolicy{TopN: 1, ConstructCap: 300})
	if len(off) != 3 {
		t.Errorf("without it the hit should widen to its two fixed neighbours, got %d chunks", len(off))
	}
}

// Rust puts a long signature's closing ") -> T {" at the function's own
// indentation; it continues the head and must not end the block there.
func TestARustSignatureOverSeveralLinesIsOneBlock(t *testing.T) {
	src := []string{"impl Searcher {"}
	var want [2]int
	for m := range 4 {
		start := len(src) + 1
		src = append(src, "    #[inline]", fmt.Sprintf("    pub fn search_%d(", m), "        &self,", "        haystack: &[u8],", "    ) -> Result<()> {")
		for b := range 90 {
			src = append(src, fmt.Sprintf("        let x%d = %d;", b, b))
		}
		src = append(src, "        Ok(())", "    }")
		if m == 2 {
			want = [2]int{start, len(src)}
		}
		src = append(src, "")
	}
	src = append(src, "}")

	got := nestedBlocks(src, constructExtents(src), want[0]+40, want[0]+79, 300, false)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("nestedBlocks = %v, want the third function %v, attribute included", got, want)
	}
}
