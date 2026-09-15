package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ROW 6.4: ONE PLACE DECIDES WHETHER TO SCRUB.
//
// s.noScrub() encodes the safe direction -- `s.cfg != nil && s.cfg.NoScrub`, so
// an absent config scrubs. Three call sites read s.cfg.NoScrub directly instead,
// which is not merely untidy: it is the exact shape that has already produced a
// bug here once. The first draft of the persistTurn scrub RE-DERIVED the
// condition as `s.cfg == nil || s.cfg.NoScrub` and inverted the polarity, so no
// config would have meant no scrubbing. It was caught before landing (see
// TestPersistTurn_NilConfigScrubsRatherThanSkips) and the lesson recorded there
// was that the bug lived in re-deriving rather than reusing.
//
// A direct read cannot invert, but it can panic, and it is the raw material the
// inversion is made from. The cheap close is to leave exactly one expression in
// the package that names the field.
//
// WHAT THIS IS NOT. Both production constructors set cfg (daemon/main.go and
// daemon/mcp_cmd.go), so no shipped path reaches these sites with a nil config
// today. This is a consistency and fail-closed-by-construction row, not a live
// nil dereference, and it is worth saying so rather than inflating it.

// directNoScrubReads returns every `<x>.cfg.NoScrub` expression in the package's
// non-test sources, as "file:line", excluding the accessor that is allowed to
// have one. The count of files parsed is returned so the caller can refuse to
// pass on an empty scan.
func directNoScrubReads(t *testing.T) (hits []string, parsed int) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package dir: %v", err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			continue
		}
		parsed++
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			// The accessor is the one expression allowed to name the field.
			if fd.Name.Name == "noScrub" {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "NoScrub" {
					return true
				}
				inner, ok := sel.X.(*ast.SelectorExpr)
				if !ok || inner.Sel.Name != "cfg" {
					return true
				}
				pos := fset.Position(sel.Pos())
				hits = append(hits, name+":"+itoa(pos.Line)+" in "+fd.Name.Name)
				return true
			})
		}
	}
	return hits, parsed
}

func TestScrubDecisionHasExactlyOneHome(t *testing.T) {
	hits, parsed := directNoScrubReads(t)

	// Vacuity floors. A scan that parsed nothing, or a detector that cannot
	// find the one expression it is supposed to tolerate, proves nothing by
	// reporting zero.
	if parsed < 50 {
		t.Fatalf("vacuity floor: parsed only %d non-test files; the scan is not seeing the package", parsed)
	}
	if !detectorSeesTheAccessor(t) {
		t.Fatal("vacuity floor: the detector cannot find the accessor's own s.cfg.NoScrub, " +
			"so a zero result would mean the matcher is broken rather than the package clean")
	}

	if len(hits) > 0 {
		t.Errorf("%d site(s) read s.cfg.NoScrub directly instead of calling s.noScrub():\n  %s\n\n"+
			"The accessor exists because an absent config must fail CLOSED. Re-deriving the "+
			"condition inverted it once already (see TestPersistTurn_NilConfigScrubsRatherThanSkips); "+
			"leaving one expression in the package is what stops a third occurrence.",
			len(hits), strings.Join(hits, "\n  "))
	}
}

// detectorSeesTheAccessor runs the same matcher WITHOUT the accessor exemption
// and requires it to fire. This is the other side of the derivation: it proves a
// clean result above came from a clean package, not a matcher that matches
// nothing.
func detectorSeesTheAccessor(t *testing.T) bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(".", "context.go"), nil, 0)
	if err != nil {
		t.Fatalf("parsing context.go: %v", err)
	}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "NoScrub" {
			return true
		}
		if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "cfg" {
			found = true
		}
		return true
	})
	return found
}

// A NIL CONFIG MUST SCRUB ON THE CHUNK PATH TOO.
//
// The sibling of TestPersistTurn_NilConfigScrubsRatherThanSkips, for the site
// that logChunkScrub owns. With a direct s.cfg.NoScrub read this panics; through
// the accessor it scrubs, which is the safe direction.
func TestLogChunkScrub_NilConfigScrubsRatherThanPanicking(t *testing.T) {
	logger, buf := bufferLogger()
	srv := &Server{logger: logger} // cfg deliberately nil, warnSink deliberately nil

	const secret = "AKIAIOSFODNN7EXAMPLE"
	srv.logChunkScrub([]Chunk{{
		FilePath:  "creds.go",
		StartLine: 1,
		EndLine:   3,
		Content:   "const key = \"" + secret + "\"\n",
	}})

	out := buf.String()
	if out == "" {
		t.Fatal("vacuity floor: logChunkScrub logged nothing at all, so this asserts nothing")
	}
	if strings.Contains(out, secret) {
		t.Errorf("the raw secret reached the log: %q", out)
	}
	if !strings.Contains(out, "redacted") {
		t.Errorf("a nil config skipped structural redaction on the chunk path.\n"+
			"An absent config must fail CLOSED -- use s.noScrub(), not a direct read.\nLog: %s", out)
	}
}
