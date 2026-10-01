package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/editapply"
)

func grepIn(t *testing.T, s *Server, ctx context.Context, args map[string]any) (string, bool) {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := s.builtinGrep(ctx, raw)
	if err != nil {
		return err.Error(), false
	}
	return res.Content, !res.IsError
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// EVERY USE, EXACTLY: literal text by default, a regular expression on request,
// either case on request -- as path:line: text.
func TestGrepFindsEveryUseExactly(t *testing.T) {
	s := builtinTestServer(t)
	writeFiles(t, s.workspace, map[string]string{
		"a/total.go": "package a\n\nfunc Total(x int) int { return x }\n",
		"b/use.go":   "package b\n\nvar t = a.Total(1) + a.Total(2)\n",
		"b/other.go": "package b\n\n// totals are summed elsewhere\n",
	})
	out, ok := grepIn(t, s, context.Background(), map[string]any{"pattern": "Total("})
	if !ok || !strings.Contains(out, "a/total.go:3:") || !strings.Contains(out, "b/use.go:3:") || strings.Contains(out, "other.go") {
		t.Errorf("literal grep = %q", out)
	}
	if !strings.HasPrefix(out, "2 matching line(s) in 2 file(s)") {
		t.Errorf("the count is wrong: %q", out)
	}
	out, _ = grepIn(t, s, context.Background(), map[string]any{"pattern": `Total\(\d\)`, "regex": true})
	if !strings.Contains(out, "b/use.go:3:") || strings.Contains(out, "a/total.go") {
		t.Errorf("regex grep = %q", out)
	}
	out, _ = grepIn(t, s, context.Background(), map[string]any{"pattern": "TOTALS", "ignore_case": true})
	if !strings.Contains(out, "b/other.go:3:") {
		t.Errorf("case-insensitive grep = %q", out)
	}
	if out, ok := grepIn(t, s, context.Background(), map[string]any{"pattern": "(unclosed", "regex": true}); ok {
		t.Errorf("an invalid expression was accepted: %q", out)
	}
}

// IT READS ONLY WHAT read_file COULD READ: a secret-shaped file, a protected
// folder, a dependency folder and an ignored build output all hold the token,
// and none of them is searched.
//
// Neuter check: drop the ResolveSafeTargetPath gate in grepScan.visit, and the
// .env line appears.
func TestGrepSearchesOnlyWhatReadFileCanRead(t *testing.T) {
	s := builtinTestServer(t)
	token := "NEEDLE_7f3a"
	writeFiles(t, s.workspace, map[string]string{
		".gitignore":            "build/\n",
		"app.go":                "package app // " + token + "\n",
		".env":                  "KEY=" + token + "\n",
		"id_rsa":                token + "\n",
		".git/config":           token + "\n",
		"node_modules/x/x.js":   "// " + token + "\n",
		"build/out.txt":         token + "\n",
		"secrets/credential.go": "package secrets // " + token + "\n",
	})
	out, ok := grepIn(t, s, context.Background(), map[string]any{"pattern": token})
	if !ok || !strings.Contains(out, "app.go:1:") {
		t.Fatalf("the ordinary file was not found: %q", out)
	}
	for _, leak := range []string{".env", "id_rsa", ".git", "node_modules", "build/", "credential"} {
		if strings.Contains(out, leak) {
			t.Errorf("grep searched %s:\n%s", leak, out)
		}
	}
}

// IT SEES THE WORKING COPY: an edit the agent made in its copy is found, and
// the project's own (unchanged) text is not.
func TestGrepSearchesTheWorkingCopy(t *testing.T) {
	s := builtinTestServer(t)
	writeFiles(t, s.workspace, map[string]string{"calc.go": "package calc\n\nfunc Add() { return a - b }\n"})
	root, _ := editapply.ResolveRealWorkspaceRoot(s.workspace)
	p := &proposalSink{stageFrom: root}
	st, err := p.workingCopy()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.discard)
	if _, err := st.apply(editapply.EditBlock{FilePath: "calc.go", Search: "a - b", Replace: "a + b"}); err != nil {
		t.Fatal(err)
	}
	out, _ := grepIn(t, s, p.readCtx(context.Background()), map[string]any{"pattern": "a + b"})
	if !strings.Contains(out, "calc.go:3:") {
		t.Errorf("grep did not see the working copy's edit: %q", out)
	}
	if out, _ := grepIn(t, s, p.readCtx(context.Background()), map[string]any{"pattern": "a - b"}); !strings.HasPrefix(out, "No line") {
		t.Errorf("grep read the project instead of the copy: %q", out)
	}
}

// BOUNDED, AND SAYS SO: past the match limit it stops and tells the model to
// narrow the search, and a path outside the project is refused.
func TestGrepIsBoundedAndStaysInTheProject(t *testing.T) {
	s := builtinTestServer(t)
	var b strings.Builder
	for i := 0; i < grepMaxMatches+50; i++ {
		fmt.Fprintf(&b, "hit %d\n", i)
	}
	writeFiles(t, s.workspace, map[string]string{"many.txt": b.String()})
	out, ok := grepIn(t, s, context.Background(), map[string]any{"pattern": "hit"})
	if !ok || strings.Count(out, "many.txt:") != grepMaxMatches || !strings.Contains(out, "stopped early") {
		t.Errorf("a search past the limit returned %d lines; want %d and a note", strings.Count(out, "many.txt:"), grepMaxMatches)
	}
	for _, path := range []string{"../", "/etc", "~/.ssh"} {
		if out, ok := grepIn(t, s, context.Background(), map[string]any{"pattern": "x", "path": path}); ok {
			t.Errorf("path %q was searched: %q", path, out)
		}
	}
}
