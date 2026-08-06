package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// query_compiler_definition and query_compiler_references shipped untested.
// Both funnel into handleLSPQuery, which takes a MODEL-CONTROLLED path and
// turns it into a `file://` URI handed to a language server.
//
// The gate that matters runs before that: editapply.ResolveSafeTargetPath, the
// same five gates every writer in this repo uses. Without it a model could ask
// a language server to open /etc/shadow and read the answer back through the
// tool result — a read primitive rather than a write one, which is exactly the
// kind of surface that gets overlooked because "it only reads".

func lspArgs(t *testing.T, path string, line, char int) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{"path": path, "line": line, "character": char})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Both tools must refuse a hostile path before contacting any language server.
func TestLSPQuery_PathGatesRunBeforeTheLanguageServer(t *testing.T) {
	ws := t.TempDir()
	real, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{logger: discardLogger(), workspace: real}

	hostile := []struct{ path, why string }{
		{"../outside.go", "escapes the workspace"},
		{"/etc/shadow", "absolute path to a system secret"},
		{"../../etc/passwd", "escapes several levels"},
		{".env", "secret-named"},
		{"id_rsa", "private key"},
		{".ssh/id_ed25519", "protected directory and a key"},
		{".git/config", "protected directory"},
		{".env:leak", "alternate data stream past the secret gate"},
		{"CON", "reserved device name"},
	}

	for _, tc := range hostile {
		for name, call := range map[string]func(context.Context, json.RawMessage) (mcpResult, error){
			"definition": func(ctx context.Context, raw json.RawMessage) (mcpResult, error) {
				r, err := s.builtinLSPDefinition(ctx, raw)
				return mcpResult{r.Content}, err
			},
			"references": func(ctx context.Context, raw json.RawMessage) (mcpResult, error) {
				r, err := s.builtinLSPReferences(ctx, raw)
				return mcpResult{r.Content}, err
			},
		} {
			res, err := call(context.Background(), lspArgs(t, tc.path, 1, 1))
			if err != nil {
				t.Fatalf("%s(%q): transport error, want a tool error: %v", name, tc.path, err)
			}
			if !strings.Contains(res.content, "invalid path") {
				t.Errorf("%s(%q) was not refused by the path gates (%s); got %q",
					name, tc.path, tc.why, res.content)
			}
		}
	}
}

// mcpResult keeps the table above readable without importing the mcp package
// just for a field name.
type mcpResult struct{ content string }

func TestLSPQuery_RejectsMalformedArguments(t *testing.T) {
	s := &Server{logger: discardLogger(), workspace: t.TempDir()}

	res, err := s.builtinLSPDefinition(context.Background(), json.RawMessage(`{"path":`))
	if err != nil {
		t.Fatalf("malformed JSON should be a tool error, not a transport error: %v", err)
	}
	if !strings.Contains(res.Content, "not a valid JSON object") {
		t.Errorf("malformed JSON was not reported: %q", res.Content)
	}

	// An empty path is NOT refused by the gates: filepath.Clean("") is ".", so
	// it resolves to the workspace root. That is not an escape, so it is not a
	// confinement bug -- but it used to reach a nil lspBridge and PANIC the
	// daemon's goroutine. It must now be a tool error like any other.
	res, err = s.builtinLSPReferences(context.Background(), lspArgs(t, "", 0, 0))
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res.Content == "" {
		t.Error("an empty path produced no tool result at all")
	}
}
