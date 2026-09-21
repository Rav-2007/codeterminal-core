package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mochiii/daemon/mcp"
)

// propose_ast_edit shipped with zero test coverage — every function in the file
// measured 0.0%, alongside mcp_lsp.go, lsp_bridge.go and watcher.go, for ~561
// untested lines. That matters most here because this is the one that takes
// MODEL-CONTROLLED input and turns it into a filesystem path.
//
// The architecture is right and these tests pin it: the path goes through
// editapply.ResolveSafeTargetPath (the same five gates every other writer uses)
// BEFORE any language server is contacted, and the result is a PROPOSAL for
// human review, never an applied edit.

// astArgs builds the tool's wire arguments.
func astArgs(t *testing.T, path, symbol, replace string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]string{"path": path, "symbol": symbol, "replace": replace})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// THE SECURITY ASSERTION. A path that escapes the workspace, names a secret, or
// hides inside a protected directory must be refused by the shared gates —
// before an LSP server is started and before anything is read. Each of these
// returns at ResolveSafeTargetPath, so no language server is needed to run them.
func TestProposeASTEdit_PathGatesRunBeforeAnythingElse(t *testing.T) {
	ws := t.TempDir()
	real, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{logger: discardLogger(), workspace: real}
	sink := &proposalSink{}

	for _, tc := range []struct{ path, why string }{
		{"../outside.go", "escapes the workspace root"},
		{"../../etc/passwd", "escapes several levels"},
		{"/etc/passwd", "absolute"},
		{".env", "secret-named"},
		{"id_rsa", "private key"},
		{".git/hooks/pre-commit", "protected directory"},
		{".ssh/authorized_keys", "protected directory"},
		{".git:x", "alternate data stream on a protected directory"},
		{"NUL", "reserved device name"},
		{`.git\hooks\evil`, "backslash-separated protected directory"},
	} {
		res, err := s.builtinProposeASTEdit(approvedLaunch("go"), astArgs(t, tc.path, "Foo", "x"), sink)
		if err != nil {
			t.Fatalf("%s: transport error, want a tool error: %v", tc.path, err)
		}
		if !strings.Contains(res.Content, "invalid path") {
			t.Errorf("propose_ast_edit(%q) was not refused by the path gates (%s); got %q", tc.path, tc.why, res.Content)
		}
	}

	if len(sink.blocks) != 0 {
		t.Errorf("a refused path still queued %d proposal(s); nothing may reach the sink after a gate refusal", len(sink.blocks))
	}
}

func TestProposeASTEdit_RejectsMalformedArguments(t *testing.T) {
	s := &Server{logger: discardLogger(), workspace: t.TempDir()}
	sink := &proposalSink{}

	res, err := s.builtinProposeASTEdit(approvedLaunch("go"), json.RawMessage(`{"path":`), sink)
	if err != nil {
		t.Fatalf("malformed JSON should be a tool error, not a transport error: %v", err)
	}
	if !strings.Contains(res.Content, "not a valid JSON object") {
		t.Errorf("malformed JSON was not reported: %q", res.Content)
	}

	for _, tc := range []struct{ path, symbol, want string }{
		{"", "Foo", "no path was supplied"},
		{"   ", "Foo", "no path was supplied"},
		{"main.go", "", "no symbol was supplied"},
		{"main.go", "  ", "no symbol was supplied"},
	} {
		res, err := s.builtinProposeASTEdit(approvedLaunch("go"), astArgs(t, tc.path, tc.symbol, "x"), sink)
		if err != nil {
			t.Fatalf("unexpected transport error: %v", err)
		}
		if !strings.Contains(res.Content, tc.want) {
			t.Errorf("path=%q symbol=%q: got %q, want %q", tc.path, tc.symbol, res.Content, tc.want)
		}
	}
}

// findSymbol must search NESTED symbols: a method lives inside a class, and a
// tool that only looked at the top level would report "not found" for most of
// what a user asks about.
func TestFindSymbol_DescendsIntoChildren(t *testing.T) {
	tree := []documentSymbol{
		{Name: "PackageLevel"},
		{Name: "Outer", Children: []documentSymbol{
			{Name: "Inner", Children: []documentSymbol{{Name: "Deepest"}}},
		}},
	}

	for _, name := range []string{"PackageLevel", "Outer", "Inner", "Deepest"} {
		if got := findSymbol(tree, name); got == nil {
			t.Errorf("findSymbol(%q) = nil, want the symbol", name)
		} else if got.Name != name {
			t.Errorf("findSymbol(%q) returned %q", name, got.Name)
		}
	}
	if got := findSymbol(tree, "Absent"); got != nil {
		t.Errorf("findSymbol(\"Absent\") = %+v, want nil", got)
	}
	if got := findSymbol(nil, "Anything"); got != nil {
		t.Errorf("findSymbol(nil, …) = %+v, want nil", got)
	}
}

// extractLSPRange produces the SEARCH text of the edit block, so an off-by-one
// here means the edit silently matches the wrong region — or matches nothing
// and the tool reports a confusing failure.
func TestExtractLSPRange(t *testing.T) {
	const content = "package main\n\nfunc Foo() {\n\treturn\n}\n\nfunc Bar() {}\n"

	mk := func(sl, sc, el, ec int) lspRange {
		var r lspRange
		r.Start.Line, r.Start.Character = sl, sc
		r.End.Line, r.End.Character = el, ec
		return r
	}

	// Whole multi-line function: lines 2..4 inclusive.
	if got := extractLSPRange(content, mk(2, 0, 4, 1)); got != "func Foo() {\n\treturn\n}" {
		t.Errorf("multi-line extract = %q", got)
	}
	// A slice within one line.
	if got := extractLSPRange(content, mk(0, 8, 0, 12)); got != "main" {
		t.Errorf("single-line extract = %q, want %q", got, "main")
	}
	// A start line past the end of the file yields nothing rather than panicking.
	if got := extractLSPRange(content, mk(999, 0, 999, 5)); got != "" {
		t.Errorf("out-of-range start = %q, want empty", got)
	}
	// An end line past the end is clamped to the last line.
	if got := extractLSPRange(content, mk(6, 0, 999, 999)); !strings.Contains(got, "func Bar() {}") {
		t.Errorf("clamped end = %q, want it to include the last line", got)
	}
	// Reversed / absurd characters are clamped rather than slicing out of bounds.
	if got := extractLSPRange(content, mk(0, 900, 0, 2)); got != "" {
		t.Errorf("start beyond end should clamp to empty, got %q", got)
	}
}

// LSP counts in UTF-16 code units and this code counts runes; the comment in
// mcp_ast_edit.go says so. Pin the behaviour that follows from that choice, so
// the day it matters someone finds a test rather than a mystery.
func TestExtractLSPRange_CountsRunesNotBytes(t *testing.T) {
	const content = "héllo wörld\n"
	var r lspRange
	r.Start.Line, r.Start.Character = 0, 0
	r.End.Line, r.End.Character = 0, 5

	if got := extractLSPRange(content, r); got != "héllo" {
		t.Errorf("got %q, want %q — characters are counted as runes, not bytes", got, "héllo")
	}
}

// The tests above stop at the path gates, which is where the security lives.
// These drive the SAME handlers all the way through a real language server —
// the fake from lsp_bridge_test.go, found on PATH exactly as gopls would be —
// so the LSP call, the symbol search, the range extraction and the proposal are
// all exercised by the code path a model actually takes.

// astFixture writes a Go file whose shape matches the symbol tree the fake
// returns in "symbols" mode.
func astFixture(t *testing.T, ws string) {
	t.Helper()
	// Shape must match the symbol tree the fake returns in "symbols" mode, AND
	// be valid Go: PrepareEdit parses the result and refuses an edit that would
	// make the file unparseable.
	const src = "package main\n\ntype Outer struct{}\n\nfunc (o Outer) Target() {\n\treturn\n}\n"
	if err := os.WriteFile(filepath.Join(ws, "main.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newLSPServer builds a Server whose bridge talks to the fake in the given mode.
func newLSPServer(t *testing.T, mode string) *Server {
	t.Helper()
	b, _ := newFakeBridge(t, mode)
	real, err := filepath.EvalSymlinks(b.workspace)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{logger: discardLogger(), workspace: real, lspBridge: b}
}

func TestProposeASTEdit_ProducesAProposalThroughARealLanguageServer(t *testing.T) {
	s := newLSPServer(t, "symbols")
	astFixture(t, s.workspace)
	sink := &proposalSink{}

	res, err := s.builtinProposeASTEdit(approvedLaunch("go"), astArgs(t, "main.go", "Target", "func (o Outer) Target() error {\n\treturn nil\n}"), sink)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !strings.Contains(res.Content, "Proposed:") {
		t.Fatalf("no proposal was produced: %q", res.Content)
	}
	// The contract that keeps this tool safe: it PROPOSES, and the human
	// reviews a diff. It must never have applied anything itself.
	if !strings.Contains(res.Content, "NOT applied") {
		t.Errorf("the result does not tell the model the edit is unapplied: %q", res.Content)
	}
	if len(sink.blocks) != 1 {
		t.Errorf("queued %d proposals, want exactly 1", len(sink.blocks))
	}
}

// A symbol the server does not know about is a tool error naming it, not a
// crash and not a silent empty proposal.
func TestProposeASTEdit_UnknownSymbolIsAToolError(t *testing.T) {
	s := newLSPServer(t, "symbols")
	astFixture(t, s.workspace)
	sink := &proposalSink{}

	res, err := s.builtinProposeASTEdit(approvedLaunch("go"), astArgs(t, "main.go", "NoSuchSymbol", "x"), sink)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !strings.Contains(res.Content, "not found") {
		t.Errorf("got %q, want a not-found tool error", res.Content)
	}
	if len(sink.blocks) != 0 {
		t.Error("a failed lookup still queued a proposal")
	}
}

// A language server is untrusted input. A well-framed reply of the WRONG SHAPE
// must be a tool error, never a panic.
func TestProposeASTEdit_MalformedServerReplyIsAToolError(t *testing.T) {
	s := newLSPServer(t, "garbage-symbols")
	astFixture(t, s.workspace)
	sink := &proposalSink{}

	res, err := s.builtinProposeASTEdit(approvedLaunch("go"), astArgs(t, "main.go", "Target", "x"), sink)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !strings.Contains(res.Content, "failed to parse") {
		t.Errorf("got %q, want a parse failure reported as a tool error", res.Content)
	}
}

// A file the model names but that does not exist must not reach the language
// server as a confusing failure.
func TestProposeASTEdit_MissingFileIsAToolError(t *testing.T) {
	s := newLSPServer(t, "symbols")
	sink := &proposalSink{}

	res, err := s.builtinProposeASTEdit(approvedLaunch("go"), astArgs(t, "absent.go", "Target", "x"), sink)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if res.Content == "" {
		t.Error("a missing file produced no tool result")
	}
}

// An LSP-level error response must surface as a tool error carrying the
// server's own message.
func TestLSPQuery_ServerErrorSurfacesToTheModel(t *testing.T) {
	s := newLSPServer(t, "lsp-error")
	astFixture(t, s.workspace)

	res, err := s.builtinLSPDefinition(approvedLaunch("go"), lspArgs(t, "main.go", 3, 6))
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !strings.Contains(res.Content, "LSP call failed") {
		t.Errorf("got %q, want the server's error surfaced", res.Content)
	}
}

// The happy path for both query tools, through a real server.
func TestLSPQuery_ReturnsTheServerResult(t *testing.T) {
	s := newLSPServer(t, "ok")
	astFixture(t, s.workspace)

	for name, call := range map[string]func() (mcp.Result, error){
		"definition": func() (mcp.Result, error) {
			return s.builtinLSPDefinition(approvedLaunch("go"), lspArgs(t, "main.go", 3, 6))
		},
		"references": func() (mcp.Result, error) {
			return s.builtinLSPReferences(approvedLaunch("go"), lspArgs(t, "main.go", 3, 6))
		},
	} {
		res, err := call()
		if err != nil {
			t.Fatalf("%s: transport error: %v", name, err)
		}
		// THE SERVER'S OWN REPLY, byte for byte. This test used to assert only
		// that the content did not contain "failed" -- and when the bridge
		// began refusing unapproved launches (register item 32), both calls
		// came back refused and the test PASSED, because the refusal never
		// uses that word. A happy-path test that a refusal satisfies proves
		// nothing about the happy path. The fake answers [] in "ok" mode.
		if res.Content != "[]" {
			t.Errorf("%s: got %q, want the server's result []", name, res.Content)
		}
	}
}

// An unsupported extension must be refused before a server is started: there is
// no language server for a .txt file and asking for one is a confusing hang.
func TestLSPQuery_UnsupportedLanguageIsAToolError(t *testing.T) {
	s := newLSPServer(t, "ok")
	if err := os.WriteFile(filepath.Join(s.workspace, "notes.txt"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := s.builtinLSPDefinition(approvedLaunch("go"), lspArgs(t, "notes.txt", 0, 0))
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if res.Content == "" {
		t.Error("an unsupported file type produced no tool result")
	}
}

// CLASS III, instance 3: propose_ast_edit.
//
// builtinProposeASTEdit calls os.ReadFile on the resolved path with no size
// check and, unlike read_file, no output truncation either -- the full contents
// become a string, and extractLSPRange slices a range out of it.
//
// Measured by ALLOCATION rather than by result size, for the reason
// builtinreadalloc_test.go states: the result was always small, and that is
// what kept this invisible. The fixture is padded with a comment block so the
// file is large while the symbol tree the fake returns still matches.
func TestProposeASTEdit_DoesNotMaterialiseTheWholeFile(t *testing.T) {
	s := newLSPServer(t, "symbols")

	// Same shape astFixture writes, padded. The padding is a trailing comment
	// so the file stays valid Go and the symbol ranges are unchanged.
	const head = "package main\n\ntype Outer struct{}\n\nfunc (o Outer) Target() {\n\treturn\n}\n"
	padding := make([]byte, 32<<20)
	for i := range padding {
		padding[i] = 'x'
	}
	src := head + "\n// " + string(padding) + "\n"
	if err := os.WriteFile(filepath.Join(s.workspace, "main.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(src) < 8<<20 {
		t.Fatalf("vacuity floor: fixture is only %d bytes", len(src))
	}

	sink := &proposalSink{}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	res, err := s.builtinProposeASTEdit(approvedLaunch("go"),
		astArgs(t, "main.go", "Target", "func (o Outer) Target() error {\n\treturn nil\n}"), sink)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if res.Content == "" {
		t.Fatal("vacuity floor: the handler returned nothing, so this measures nothing")
	}

	// REFUSAL is the designed answer here, not truncation: a shortened buffer
	// would make extractLSPRange produce the wrong Search text. Assert the
	// behaviour, not just the number.
	if !res.IsError {
		t.Errorf("an oversized file was accepted; want a refusal, since truncating it "+
			"would yield a wrong edit rather than a smaller one. Got: %s", res.Content)
	}
	if !strings.Contains(res.Content, "propose_edit") {
		t.Errorf("the refusal does not tell the model what to do instead: %s", res.Content)
	}

	allocated := after.TotalAlloc - before.TotalAlloc
	// Bounded by maxFileSize and the LSP round trip, NOT by the file's size --
	// that is the property. Measured before the fix: 503,440,408 bytes for this
	// same 32 MiB fixture. Measured after: 5,286,648.
	const budget = 16 << 20
	if allocated > budget {
		t.Errorf("builtinProposeASTEdit allocated %d bytes for a %d-byte file; "+
			"allocation must track maxFileSize=%d, not the file.",
			allocated, len(src), maxFileSize)
	}
}
