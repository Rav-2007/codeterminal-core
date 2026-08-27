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

// THE COMPILER IS THE ORACLE, AND IT IS A TEST DEPENDENCY, NOT A RUNTIME ONE.
//
// declarationName finds top-level declarations with a column-zero heuristic
// because the repository map has to work on every language, offline, with no
// language server and no model. An AST-backed extractor is exact for the three
// languages this daemon has an LSP for and says NOTHING about a Rust or Ruby
// repository -- a map with no symbols at all is worse than an imperfect one.
//
// That trade is only defensible if somebody checks the heuristic's accuracy,
// and "it looks right" is not checking. So go/parser -- stdlib, exact, free at
// test time -- grades it against every Go file here. MEASURED, that grading
// paid for itself immediately: it found `func (l *ledger) record(...)` and
// `func (s *warnScanDetectorStats) record(...)` missing from the map entirely,
// because "record" is a declaration keyword in C# and the keyword-collision
// guard was rejecting any identifier that matched one in ANY language.
//
// A runtime dependency on go/parser would buy exactness for Go alone, plus a
// second code path. This buys it for every language the heuristic touches, and
// fails the day that stops being true.
func TestTheMapsExtractorAgreesWithTheCompiler(t *testing.T) {
	var files, decls, missed, spurious int
	var missedLines, spuriousLines []string

	err := filepath.WalkDir("..", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if isPrunedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(p) != ".go" {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, p, src, parser.ParseComments|parser.SkipObjectResolution)
		if f == nil || perr != nil {
			return nil // not valid Go; the heuristic is on its own there by design
		}
		files++
		lines := splitLines(src)

		want := map[int]bool{}
		for _, decl := range f.Decls {
			switch x := decl.(type) {
			case *ast.FuncDecl:
			case *ast.GenDecl:
				// Imports are declarations to the compiler and say nothing a
				// reader of a repository map wants. A PARENTHESISED block --
				// `const (` -- has no single name for the map to show, so the
				// map legitimately reports nothing for it.
				if x.Tok == token.IMPORT || x.Lparen.IsValid() {
					continue
				}
			}
			want[fset.Position(decl.Pos()).Line-1] = true
		}

		got := map[int]bool{}
		for i, line := range lines {
			if line == "" || line[0] == ' ' || line[0] == '\t' {
				continue
			}
			if declarationName(line) != "" {
				got[i] = true
			}
		}

		at := func(i int) string {
			if i >= 0 && i < len(lines) {
				return strings.TrimSpace(lines[i])
			}
			return ""
		}
		for ln := range want {
			decls++
			if !got[ln] {
				missed++
				if len(missedLines) < 10 {
					missedLines = append(missedLines, p+": "+at(ln))
				}
			}
		}
		for ln := range got {
			if !want[ln] {
				spurious++
				if len(spuriousLines) < 10 {
					spuriousLines = append(spuriousLines, p+": "+at(ln))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// ANTI-VACUITY: a walk that parsed nothing agrees with the compiler trivially.
	if files < 100 || decls < 1000 {
		t.Fatalf("graded only %d files / %d declarations; this test is not exercising the "+
			"corpus it claims to", files, decls)
	}
	if missed > 0 {
		t.Errorf("the map's extractor misses %d of %d declarations the compiler finds, so they "+
			"are absent from the repository map:\n  %v", missed, decls, missedLines)
	}
	t.Logf("%d Go files, %d named top-level declarations: heuristic and go/parser agree "+
		"(%d heuristic-only, e.g. declarations inside raw strings)", files, decls, spurious)
}
