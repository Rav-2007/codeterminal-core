package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- parsing -----------------------------------------------------------------

func TestParseFileLineRefs_RecognizesRealToolOutputShapes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []fileLineRef
	}{
		{
			name: "go build failure, package-relative with column",
			input: "# codeterminal/daemon [codeterminal/daemon.test]\n" +
				"./helperproc_test.go:136:4: h.extraEnv undefined (type *HelperProcess has no field or method extraEnv)\n" +
				"FAIL\tcodeterminal/daemon [build failed]",
			want: []fileLineRef{{Path: "helperproc_test.go", Line: 136}},
		},
		{
			name:  "go test assertion failure, no column",
			input: "    provider_test.go:291: isZDRRoutingRefusal(...) = false, want true",
			want:  []fileLineRef{{Path: "provider_test.go", Line: 291}},
		},
		{
			name:  "workspace-relative path",
			input: "daemon/helperproc.go:142:2: undefined: helperEnv",
			want:  []fileLineRef{{Path: "daemon/helperproc.go", Line: 142}},
		},
		{
			name:  "node/TS stack frame in parentheses",
			input: "    at ChatPanel.render (clients/vscode/src/chatPanel.ts:88:17)",
			want:  []fileLineRef{{Path: "clients/vscode/src/chatPanel.ts", Line: 88}},
		},
		{
			name:  "several refs keep first-seen order and dedupe",
			input: "./chat_test.go:354:14: x\n./chat_test.go:360:14: y\n./chat_test.go:354:14: x again\n",
			want:  []fileLineRef{{Path: "chat_test.go", Line: 354}, {Path: "chat_test.go", Line: 360}},
		},
		{
			name:  "clock times and bare numbers are not refs",
			input: "started at 10:30:15, retried 3:2 times, exit:1",
			want:  nil,
		},
		{
			name:  "line 0 is not a ref",
			input: "foo.go:0: something",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseFileLineRefs(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("parseFileLineRefs() = %+v, want %+v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ref %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseFileLineRefs_CapsRunawayInput(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&b, "./gen.go:%d:1: error %d\n", i, i)
	}
	if got := len(parseFileLineRefs(b.String())); got != maxParsedRefs {
		t.Errorf("parsed %d refs from 500, want the cap of %d", got, maxParsedRefs)
	}
}

// --- resolution --------------------------------------------------------------

// refWorkspace builds a throwaway workspace with a numbered source file, so a
// resolved span's line attribution can be checked exactly.
func refWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&b, "package main // line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, "daemon", "target.go"), []byte(strings.TrimRight(b.String(), "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestResolveFileLineRefs_ResolvesValidReferenceToTheRightSpan(t *testing.T) {
	root := refWorkspace(t)

	spans := resolveFileLineRefs("daemon/target.go:100:2: undefined: thing", root, discardLogger())
	if len(spans) != 1 {
		t.Fatalf("resolved %d spans, want 1", len(spans))
	}
	got := spans[0]

	if got.FilePath != "daemon/target.go" {
		t.Errorf("FilePath = %q, want daemon/target.go", got.FilePath)
	}
	if got.StartLine != 80 || got.EndLine != 120 {
		t.Errorf("span = %d-%d, want 80-120 (line 100 +/- %d)", got.StartLine, got.EndLine, directSpanContextLines)
	}
	lines := strings.Split(got.Content, "\n")
	if len(lines) != 41 {
		t.Fatalf("span content has %d lines, want 41", len(lines))
	}
	// Line attribution must be exact — the model is shown this range.
	if want := "package main // line 80"; lines[0] != want {
		t.Errorf("first line = %q, want %q", lines[0], want)
	}
	if want := "package main // line 120"; lines[40] != want {
		t.Errorf("last line = %q, want %q", lines[40], want)
	}
}

func TestResolveFileLineRefs_ResolvesPackageRelativePathBySuffix(t *testing.T) {
	root := refWorkspace(t)

	// What `go test` prints when run inside daemon/: a path that means nothing
	// from the workspace root.
	spans := resolveFileLineRefs("./target.go:100:2: undefined: thing", root, discardLogger())
	if len(spans) != 1 {
		t.Fatalf("resolved %d spans, want 1 (package-relative path should resolve by unique suffix)", len(spans))
	}
	if spans[0].FilePath != "daemon/target.go" {
		t.Errorf("FilePath = %q, want daemon/target.go", spans[0].FilePath)
	}
}

func TestResolveFileLineRefs_RefusesAmbiguousSuffix(t *testing.T) {
	root := refWorkspace(t)
	// A second target.go elsewhere makes the bare name ambiguous.
	if err := os.MkdirAll(filepath.Join(root, "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "other", "target.go"), []byte("package other\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if spans := resolveFileLineRefs("./target.go:1:1: x", root, discardLogger()); len(spans) != 0 {
		t.Errorf("resolved %d spans for an ambiguous name, want 0 — ambiguity must be refused, not guessed", len(spans))
	}
}

// TestResolveFileLineRefs_DegradesGracefully is the robustness acceptance: a
// reference that can't be honoured is skipped, and the ones that can still
// resolve. None of these may error the request.
func TestResolveFileLineRefs_DegradesGracefully(t *testing.T) {
	root := refWorkspace(t)

	tests := []struct {
		name   string
		prompt string
		want   int
	}{
		{"nonexistent file", "no/such/file.go:12:1: boom", 0},
		{"line past end of file", "daemon/target.go:99999:1: boom", 0},
		{"directory, not a file", "daemon:12:1: boom", 0},
		{"escape above the workspace", "../../../etc/passwd:1:1: boom", 0},
		{"absolute path outside the workspace", "/etc/passwd:1:1: boom", 0},
		{"no refs at all", "why is the daemon slow?", 0},
		{"bad ref alongside a good one", "no/such/file.go:12:1: boom\ndaemon/target.go:100:2: real", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveFileLineRefs(tt.prompt, root, discardLogger())
			if len(got) != tt.want {
				t.Errorf("resolved %d spans, want %d (%v)", len(got), tt.want, chunkIDs(got))
			}
		})
	}
}

// TestResolveFileLineRefs_AppliesIndexerEligibilityGates is the containment
// check: direct resolution must never reach a file the INDEXER would refuse,
// because whatever it resolves is POSTed to the hosted model provider. A
// pasted ".env:1" is otherwise a one-line recipe for exfiltrating a secret.
func TestResolveFileLineRefs_AppliesIndexerEligibilityGates(t *testing.T) {
	root := refWorkspace(t)

	mustWrite := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite(".env", "OPENAI_API_KEY=sk-live-do-not-send-this\n")
	mustWrite("id_rsa", "-----BEGIN OPENSSH PRIVATE KEY-----\n")
	mustWrite("secrets.txt", "ignored by gitignore\n")
	mustWrite(".gitignore", "secrets.txt\n")
	mustWrite("bin/blob.dat", "head\x00binary\n")

	for _, ref := range []string{".env:1:1: x", "id_rsa:1:1: x", "secrets.txt:1:1: x", "bin/blob.dat:1:1: x"} {
		t.Run(ref, func(t *testing.T) {
			if spans := resolveFileLineRefs(ref, root, discardLogger()); len(spans) != 0 {
				t.Errorf("resolved %v for %q — direct resolution must not reach files indexing excludes", chunkIDs(spans), ref)
			}
		})
	}

	// The gates must not have broken ordinary resolution.
	if spans := resolveFileLineRefs("daemon/target.go:100:1: x", root, discardLogger()); len(spans) != 1 {
		t.Errorf("eligible file no longer resolves (%d spans)", len(spans))
	}
}

func TestResolveFileLineRefs_RefusesSymlinkEscape(t *testing.T) {
	root := refWorkspace(t)
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package outside // secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.go")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if spans := resolveFileLineRefs("link.go:1:1: x", root, discardLogger()); len(spans) != 0 {
		t.Errorf("resolved %v through a symlink pointing outside the workspace, want 0", chunkIDs(spans))
	}
}

// --- fusion ------------------------------------------------------------------

// TestFuseDirectSpans_DirectSpansRankFirst is the headline behavioural claim:
// an exact pointer is not left to fuzzy matching, and lands at the top.
func TestFuseDirectSpans_DirectSpansRankFirst(t *testing.T) {
	direct := []Chunk{lineChunk("daemon/helperproc_test.go", 116, 156)}
	similar := []Chunk{
		lineChunk("daemon/helperpath.go", 1, 40),
		lineChunk("daemon/helperproc_cmd.go", 31, 70),
	}

	got := fuseDirectSpans(direct, similar, 5, false)
	ids := chunkIDs(got.Chunks)
	if len(ids) == 0 || ids[0] != "daemon/helperproc_test.go:116-156" {
		t.Fatalf("fused order = %v, want the directly-resolved span first", ids)
	}
	if got.DirectSpans != 1 {
		t.Errorf("DirectSpans = %d, want 1", got.DirectSpans)
	}
}

// TestFuseDirectSpans_DedupesAgainstSimilarityHit is the Fix 11/Fix 12
// interaction: when direct resolution and similarity both cover the same
// lines, the prompt must carry ONE span, not two overlapping copies.
func TestFuseDirectSpans_DedupesAgainstSimilarityHit(t *testing.T) {
	direct := []Chunk{lineChunk("a.go", 80, 120)}
	similar := []Chunk{
		lineChunk("a.go", 91, 130), // overlaps the direct span
		lineChunk("b.go", 1, 40),
	}

	got := fuseDirectSpans(direct, similar, 5, false)
	ids := chunkIDs(got.Chunks)
	want := []string{"a.go:80-130", "b.go:1-40"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("fused = %v, want %v (direct + similarity over the same lines must merge)", ids, want)
	}
	if got.SavedBytes <= 0 {
		t.Errorf("SavedBytes = %d, want a positive reclaim from the dedupe", got.SavedBytes)
	}
}

// TestFuseDirectSpans_NeverCrowdsOutSimilarity: a prompt pasting many compiler
// errors must not turn retrieval into "only the lines the compiler named".
func TestFuseDirectSpans_NeverCrowdsOutSimilarity(t *testing.T) {
	var direct []Chunk
	for i := 0; i < 10; i++ {
		direct = append(direct, lineChunk(fmt.Sprintf("d%d.go", i), 1, 40))
	}
	similar := []Chunk{lineChunk("answer.go", 1, 40)}

	for _, k := range []int{2, 3, 5} {
		got := fuseDirectSpans(direct, similar, k, false)
		if got.DirectSpans > k-1 {
			t.Errorf("k=%d: %d direct spans, want at most %d so similarity keeps a slot", k, got.DirectSpans, k-1)
		}
		if got.DirectSpans > maxDirectSpans {
			t.Errorf("k=%d: %d direct spans exceeds maxDirectSpans=%d", k, got.DirectSpans, maxDirectSpans)
		}
		if len(got.Chunks) > k {
			t.Errorf("k=%d: returned %d chunks, want at most k", k, len(got.Chunks))
		}
		if ids := chunkIDs(got.Chunks); ids[len(ids)-1] != "answer.go:1-40" {
			t.Errorf("k=%d: similarity hit was crowded out entirely: %v", k, ids)
		}
	}
}

func TestFuseDirectSpans_NoRefsIsUnchangedRetrieval(t *testing.T) {
	similar := []Chunk{lineChunk("a.go", 1, 40), lineChunk("b.go", 1, 40)}
	got := fuseDirectSpans(nil, similar, 5, false)
	if strings.Join(chunkIDs(got.Chunks), ",") != "a.go:1-40,b.go:1-40" {
		t.Errorf("fused = %v, want the similarity order unchanged", chunkIDs(got.Chunks))
	}
	if got.DirectSpans != 0 || got.SavedBytes != 0 {
		t.Errorf("DirectSpans=%d SavedBytes=%d, want both zero", got.DirectSpans, got.SavedBytes)
	}
}

// --- end to end through gatherContext ----------------------------------------

// TestGatherContext_DirectReferenceReachesThePrompt is the wire-shaped
// acceptance: a prompt carrying a real compiler error gets the referenced span
// into the rendered <retrieved_context> block, first, even though similarity
// retrieval (here deliberately pointed somewhere else entirely) never finds it.
func TestGatherContext_DirectReferenceReachesThePrompt(t *testing.T) {
	root := refWorkspace(t)

	// Similarity returns an unrelated file — the "matches nothing well" case
	// this fix exists for.
	irrelevant := lineChunk("daemon/unrelated.go", 1, 40)
	s := &Server{
		logger:             discardLogger(),
		embedder:           &fakeEmbedder{dim: embedDim},
		store:              fixedStore{chunks: []Chunk{irrelevant}},
		retrievalTopK:      defaultK,
		contextBudgetChars: 1 << 20,
		rerankDisabled:     true,
		workspace:          root,
		cfg:                &Config{},
	}

	prompt := "# codeterminal/daemon [codeterminal/daemon.test]\n./target.go:100:2: undefined: thing\nFAIL"
	out := s.gatherContext(context.Background(), prompt)

	if out.Skipped {
		t.Fatalf("retrieval skipped: %s", out.Reason)
	}
	if out.DirectRefSpans != 1 {
		t.Errorf("DirectRefSpans = %d, want 1", out.DirectRefSpans)
	}
	ids := chunkIDs(out.Chunks)
	if len(ids) == 0 || ids[0] != "daemon/target.go:80-120" {
		t.Fatalf("kept spans = %v, want the referenced span first", ids)
	}

	msg := buildAugmentedUserMessage(prompt, out.Chunks, false)
	if !strings.Contains(msg, "[1] daemon/target.go:80-120") {
		t.Errorf("rendered prompt does not label the referenced span first:\n%s", msg[:min(600, len(msg))])
	}
	if !strings.Contains(msg, "package main // line 100") {
		t.Error("rendered prompt does not contain the referenced line itself")
	}
}

// TestGatherContext_BadReferenceStillGrounds: a broken pointer degrades to
// ordinary retrieval rather than failing or emptying the request.
func TestGatherContext_BadReferenceStillGrounds(t *testing.T) {
	root := refWorkspace(t)
	s := &Server{
		logger:             discardLogger(),
		embedder:           &fakeEmbedder{dim: embedDim},
		store:              fixedStore{chunks: []Chunk{lineChunk("daemon/unrelated.go", 1, 40)}},
		retrievalTopK:      defaultK,
		contextBudgetChars: 1 << 20,
		rerankDisabled:     true,
		workspace:          root,
		cfg:                &Config{},
	}

	out := s.gatherContext(context.Background(), "no/such/file.go:999999:1: boom")
	if out.Skipped {
		t.Fatalf("a bad file:line reference skipped retrieval entirely: %s", out.Reason)
	}
	if out.DirectRefSpans != 0 {
		t.Errorf("DirectRefSpans = %d, want 0", out.DirectRefSpans)
	}
	if ids := chunkIDs(out.Chunks); len(ids) != 1 || ids[0] != "daemon/unrelated.go:1-40" {
		t.Errorf("kept spans = %v, want ordinary similarity retrieval unchanged", ids)
	}
}
