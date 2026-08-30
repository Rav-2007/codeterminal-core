package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A .rs FILE USED TO BE HANDED TO gopls.
//
// builtinLSPDefinition and builtinProposeASTEdit each opened with `lang := "go"`
// and then switched on the extension. Rust matched no case, so it kept the
// DEFAULT and went to the Go language server, which correctly reported that it
// held no symbols for the file -- and propose_ast_edit turned that into
//
//	symbol "main" not found in lib.rs
//
// telling the user their code was wrong. The failure is indistinguishable from a
// genuine typo, which is what made it expensive: there was nothing to debug from.
//
// These tests assert the property that fixes it, not the message that reports
// it: a file we cannot identify must reach NO language server at all.
func TestRustFileIsNotRoutedToGopls(t *testing.T) {
	s := newLSPServer(t, "symbols")
	writeWorkspaceFile(t, s.workspace, "lib.rs", "fn main() {}\n")

	res, err := s.builtinProposeASTEdit(context.Background(),
		astArgs(t, "lib.rs", "main", "fn main() { todo!() }"), &proposalSink{})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("a .rs file produced a successful AST edit: %q", res.Content)
	}

	// THE LOAD-BEARING ASSERTION. Not the wording -- the fact that nothing was
	// started. If a server ever appears in this map for an unidentifiable file,
	// the default is back whatever the message says.
	if n := len(s.lspBridge.servers); n != 0 {
		t.Errorf("%d language server(s) were started for a .rs file; it must reach none", n)
	}

	if strings.Contains(strings.ToLower(res.Content), "not found") {
		t.Errorf("the refusal still blames the user's symbol: %q\n\n"+
			"That is the old failure exactly: gopls answered honestly that it had no "+
			"symbols for a Rust file, and we reported it as the symbol not existing.", res.Content)
	}
	if !strings.Contains(res.Content, ".rs") {
		t.Errorf("the refusal does not name the extension it could not handle: %q", res.Content)
	}
	for _, lang := range []string{"go", "typescript", "javascript", "python"} {
		if !strings.Contains(res.Content, lang) {
			t.Errorf("the refusal does not tell the user %q is supported: %q", lang, res.Content)
		}
	}
}

func TestUnidentifiableFileReachesNoLanguageServer(t *testing.T) {
	for _, name := range []string{"lib.rs", "Widget.java", "app.rb", "notes.txt", "LICENSE"} {
		t.Run(name, func(t *testing.T) {
			s := newLSPServer(t, "ok")
			writeWorkspaceFile(t, s.workspace, name, "content\n")

			res, err := s.builtinLSPDefinition(context.Background(), lspArgs(t, name, 0, 0))
			if err != nil {
				t.Fatalf("transport error: %v", err)
			}
			if !res.IsError {
				t.Fatalf("%s produced a successful LSP query: %q", name, res.Content)
			}
			if n := len(s.lspBridge.servers); n != 0 {
				t.Errorf("%s started %d language server(s); it must start none", name, n)
			}
		})
	}
}

// THE POSITIVE CONTROL, and it is not optional. Every assertion above is of the
// form "no server was started", which a broken harness satisfies for free. This
// is the test that proves the harness can start one at all -- without it the
// whole file could pass against a bridge that never works.
func TestAGoFileStillReachesItsLanguageServer(t *testing.T) {
	s := newLSPServer(t, "symbols")
	astFixture(t, s.workspace)

	res, err := s.builtinProposeASTEdit(context.Background(),
		astArgs(t, "main.go", "Target", "func (o Outer) Target() error {\n\treturn nil\n}"), &proposalSink{})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("a .go file was refused: %q", res.Content)
	}
	if len(s.lspBridge.servers) == 0 {
		t.Fatal("no language server was started for a .go file, so every \"none was started\" " +
			"assertion in this file is vacuous")
	}
}

func writeWorkspaceFile(t *testing.T, workspace, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
