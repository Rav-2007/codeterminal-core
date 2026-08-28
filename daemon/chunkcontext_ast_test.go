package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// THE COMPILER IS THE ORACLE, AND IT IS A TEST DEPENDENCY, NOT A RUNTIME ONE.
//
// constructStarts finds top-level declarations with a column-zero heuristic
// because the indexer has to work on every language, offline, with no language
// server and no model -- an AST-backed rule is exact for the three languages
// this daemon has an LSP for and says NOTHING about a Rust or Ruby repository.
// That trade is only defensible if somebody checks the heuristic's accuracy, and
// "it looks right" is not checking.
//
// So go/parser -- stdlib, exact, free at test time -- grades it against every Go
// file in this repository. MEASURED, that grading paid for itself immediately:
//
//	97.7%  the rule as first written; all 79 misses were grouped `const (`/`var (`
//	       blocks, which declarationName cannot name and so never reported
//	99.9%  after isGroupedDeclOpener
//	100%   after finding the last four: `func (l *ledger) record(...)` was being
//	       discarded because "record" is a declaration keyword in C#, so a
//	       cross-language collision was deleting ordinary Go methods from both
//	       the header layer AND the repository map
//
// A runtime dependency on go/parser would have bought the same 100% for Go
// alone, plus a second code path and 269us per file. This buys it for every
// language the heuristic touches, and fails CI the day that stops being true.
func TestTheHeuristicAgreesWithTheCompiler(t *testing.T) {
	var files, boundaries, missed, spurious int
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

		want := map[int]bool{}
		for _, decl := range f.Decls {
			pos := decl.Pos()
			switch x := decl.(type) {
			case *ast.FuncDecl:
				if x.Doc != nil {
					pos = x.Doc.Pos()
				}
			case *ast.GenDecl:
				// An import block is a top-level declaration to the compiler and
				// a worthless place to start a chunk. Excluded deliberately:
				// counting them made the heuristic look 12% wrong when the real
				// divergence was 2.3%.
				if x.Tok == token.IMPORT {
					continue
				}
				if x.Doc != nil {
					pos = x.Doc.Pos()
				}
			}
			want[fset.Position(pos).Line-1] = true
		}

		lines := splitLines(src)
		got := map[int]bool{}
		for _, ln := range constructStarts(lines) {
			got[ln] = true
		}

		at := func(i int) string {
			if i >= 0 && i < len(lines) {
				return lines[i]
			}
			return ""
		}
		for ln := range want {
			boundaries++
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
	if files < 100 || boundaries < 1000 {
		t.Fatalf("graded only %d files / %d declarations; this test is not exercising the corpus "+
			"it claims to", files, boundaries)
	}
	if missed > 0 {
		t.Errorf("the heuristic misses %d of %d declarations the compiler finds:\n  %v",
			missed, boundaries, missedLines)
	}
	if spurious > 0 {
		t.Errorf("the heuristic invents %d boundaries the compiler does not recognise:\n  %v",
			spurious, spuriousLines)
	}
	t.Logf("%d Go files, %d top-level declarations: heuristic and go/parser agree exactly",
		files, boundaries)
}

// EXPANSION IS ONLY SAFE AS A SUPERSET, and this is the property that makes it
// so. constructExtents infers a construct's end from where the NEXT one starts,
// with no brace matching, because the indexer has to work on languages this
// daemon cannot parse. That inference can be wrong in two directions and they
// are not symmetric:
//
//   - TOO LARGE delivers a few extra lines of the following declaration. Costs
//     budget, loses nothing.
//   - TOO SMALL silently drops the tail of the very function the query was
//     about — which is the failure expandToNeighbours exists to fix, reintroduced
//     by the thing meant to fix it.
//
// So over-extension is measured and reported; under-extension fails. Graded
// against go/parser's own decl.End(), the compiler's answer.
//
// MEASURED 2026-08-28 against go/parser over this repository: exact on 3,471 of
// 3,549 top-level constructs (97.8%), over-extending on 78 (median 4 lines, max
// 34), and stopping short on NONE. So the tolerance below is zero -- the rule
// earns it, and a tolerance set above what was measured is just a place for a
// regression to hide.
//
// (An earlier estimate here said 87.2% exact with a max over-extension of 61,
// from a hand-written brace counter used before this test existed. The compiler
// is the better oracle and the rule is better than that estimate; the estimate
// is recorded only so the discrepancy is not rediscovered as a mystery.)
func TestConstructExtentsNeverStopShortOfTheCompiler(t *testing.T) {
	var graded, exact, over, under int
	var overLines []int
	var shortExamples []string

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
			return nil
		}

		lines := splitLines(src)
		// Extents keyed by their start line, so each can be matched to the
		// declaration the compiler reports beginning there.
		got := map[int][2]int{}
		for _, e := range constructExtents(lines) {
			got[e[0]] = e
		}

		for _, decl := range f.Decls {
			pos := decl.Pos()
			switch x := decl.(type) {
			case *ast.FuncDecl:
				if x.Doc != nil {
					pos = x.Doc.Pos()
				}
			case *ast.GenDecl:
				if x.Tok == token.IMPORT {
					continue
				}
				if x.Doc != nil {
					pos = x.Doc.Pos()
				}
			}
			start := fset.Position(pos).Line
			e, ok := got[start]
			if !ok {
				continue // start disagreements are TestTheHeuristicAgreesWithTheCompiler's job
			}
			graded++
			want := fset.Position(decl.End()).Line
			switch {
			case e[1] == want:
				exact++
			case e[1] > want:
				over++
				overLines = append(overLines, e[1]-want)
			default:
				under++
				if len(shortExamples) < 10 {
					shortExamples = append(shortExamples,
						fmt.Sprintf("%s:%d ends at %d, compiler says %d", p, start, e[1], want))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// ANTI-VACUITY: a walk that graded nothing agrees trivially.
	if graded < 1000 {
		t.Fatalf("graded only %d constructs; this test is not exercising the corpus it claims to", graded)
	}

	// ZERO, because zero is what was measured. A tolerance above the measured
	// value is a place for a regression to hide.
	const allowedShort = 0
	if under > allowedShort {
		t.Errorf("constructExtents stops SHORT of the compiler on %d construct(s), past the %d measured "+
			"on 2026-08-28. An extent that ends early makes expandToNeighbours deliver a function with "+
			"its tail cut off, which is the exact failure expansion exists to prevent:\n  %v",
			under, allowedShort, shortExamples)
	}

	sort.Ints(overLines)
	median, max := 0, 0
	if len(overLines) > 0 {
		median, max = overLines[len(overLines)/2], overLines[len(overLines)-1]
	}
	t.Logf("%d constructs: %d exact (%.1f%%), %d over-extend (median %d lines, max %d), %d stop short",
		graded, exact, 100*float64(exact)/float64(graded), over, median, max, under)
}
