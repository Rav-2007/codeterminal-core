package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeterminal/daemon/mcp"
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
		res, err := s.builtinProposeASTEdit(context.Background(), astArgs(t, tc.path, "Foo", "x"), sink)
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

	res, err := s.builtinProposeASTEdit(context.Background(), json.RawMessage(`{"path":`), sink)
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
		res, err := s.builtinProposeASTEdit(context.Background(), astArgs(t, tc.path, tc.symbol, "x"), sink)
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

	res, err := s.builtinProposeASTEdit(context.Background(), astArgs(t, "main.go", "Target", "func (o Outer) Target() error {\n\treturn nil\n}"), sink)
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

	res, err := s.builtinProposeASTEdit(context.Background(), astArgs(t, "main.go", "NoSuchSymbol", "x"), sink)
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

	res, err := s.builtinProposeASTEdit(context.Background(), astArgs(t, "main.go", "Target", "x"), sink)
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

	res, err := s.builtinProposeASTEdit(context.Background(), astArgs(t, "absent.go", "Target", "x"), sink)
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

	res, err := s.builtinLSPDefinition(context.Background(), lspArgs(t, "main.go", 3, 6))
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
			return s.builtinLSPDefinition(context.Background(), lspArgs(t, "main.go", 3, 6))
		},
		"references": func() (mcp.Result, error) {
			return s.builtinLSPReferences(context.Background(), lspArgs(t, "main.go", 3, 6))
		},
	} {
		res, err := call()
		if err != nil {
			t.Fatalf("%s: transport error: %v", name, err)
		}
		if strings.Contains(res.Content, "failed") {
			t.Errorf("%s: unexpected failure: %q", name, res.Content)
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

	res, err := s.builtinLSPDefinition(context.Background(), lspArgs(t, "notes.txt", 0, 0))
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if res.Content == "" {
		t.Error("an unsupported file type produced no tool result")
	}
}
