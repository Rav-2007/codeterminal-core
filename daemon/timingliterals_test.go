package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A TIMING BOUND WRITTEN AS A BARE LITERAL IS A CONSTANT TUNED ON ONE LAPTOP.
//
// THE MEASUREMENT THAT PRODUCED THIS GUARD. On 2026-09-09 cross
// (windows-latest, daemon) failed on TestSQLiteCancellation_DoesNotReachAnFTS5PhraseMatch:
// the query returned in 120.2ms against a flat `elapsed < 1500*time.Millisecond`
// bar, and the test concluded SQLite had started honouring the interrupt. It had
// not -- 120ms is BEFORE the 200ms cancel even fired, so the error could not
// have been cancellation at all. A number measured on one Linux box had become a
// semantic claim about a database on a different operating system.
//
// WHY A NEW GUARD RATHER THAN EXTENDING clients/tui's (M8).
// clients/tui/testpolicy_guard_test.go is real prior art and its TECHNIQUE is
// reused here -- AST scan, vacuity floor, structural rather than name-driven.
// Its CHECK is not, for two reasons:
//
//  1. It scans package-level DECLARATIONS of time.Duration and asserts they
//     mention raceEnabled. Its own doc states the gap: "a timing assertion
//     written inline with no named constant" is invisible to it. Every one of
//     daemon's instances was exactly that shape, so extending it would have
//     meant adding an unrelated check to a function whose entire structure is
//     built around finding declarations.
//  2. Its policy -- skip timing under the race detector -- is a clients/tui
//     policy about repaint budgets. daemon's timing tests assert that bounds
//     FIRE, which is not a measurement the detector invalidates. Importing that
//     policy here would force skips on tests that should run.
//
// Different property, different gate. Same discipline.
//
// WHY THIS FAILS IN A WAY A RE-RUN CANNOT CLEAR. A timing test that flakes gets
// re-run and goes green, so the trigger fires and nobody notices. This gate is
// STATIC: it parses source and reaches the same verdict every time, on every
// machine. Converting a class of flaky runtime failures into one deterministic
// compile-time-shaped failure is the point.
//
// THE RULE. In a comparison between a measured duration (`elapsed`, or
// `time.Since(...)`) and a bound, the bound must reference at least one
// identifier. `2*timeout`, `grace/5`, `lspCallTimeout` and `honouredWithin` all
// pass; `2*time.Second` does not. An identifier has a definition to read and a
// derivation to audit. A literal has neither.
func TestTimingBoundsAreDerivedNotLiteral(t *testing.T) {
	root := repoRoot(t)

	type finding struct{ file, expr string }
	var findings []finding
	scannedFiles, scannedCmps := 0, 0
	perModule := map[string]int{}

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable subtree is not this gate's business
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "vendor", "ARCHIVE":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		scannedFiles++
		perModule[moduleOf(rel)]++

		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return nil // not compiling is another gate's failure, not this one's
		}
		ast.Inspect(f, func(n ast.Node) bool {
			be, ok := n.(*ast.BinaryExpr)
			if !ok {
				return true
			}
			switch be.Op {
			case token.LSS, token.GTR, token.LEQ, token.GEQ:
			default:
				return true
			}
			bound, isCmp := boundSideOf(be)
			if !isCmp {
				return true
			}
			scannedCmps++
			if mentionsTimeUnit(bound) && !mentionsAnyIdent(bound) {
				findings = append(findings, finding{rel, exprText(bound)})
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// VACUITY FLOORS, and both halves are needed. A walk that found no files, or
	// found files but recognised no comparisons, would report a clean repository
	// from a gate that had stopped looking -- which is how scripts/go-toolchain-pinned.sh
	// printed "7 pin site(s) agree" while checking nothing.
	if scannedFiles < 200 {
		t.Fatalf("walked only %d _test.go files from %s; this gate cannot have checked anything", scannedFiles, root)
	}
	if scannedCmps < 10 {
		t.Fatalf("recognised only %d duration comparisons across %d files; boundSideOf has gone blind",
			scannedCmps, scannedFiles)
	}

	// SCOPE, REPORTED RATHER THAN ASSUMED. clients/tui's guard covers one
	// package and says so nowhere; a reader has to open it to find out. This one
	// walks every module in the workspace and prints what it saw, so a module
	// that silently stopped being scanned is visible in the log.
	mods := make([]string, 0, len(perModule))
	for m := range perModule {
		mods = append(mods, m)
	}
	sort.Strings(mods)
	var parts []string
	for _, m := range mods {
		parts = append(parts, m+":"+strconv.Itoa(perModule[m]))
	}
	t.Logf("timing-bound scan: %d comparison(s) in %d test file(s) across %d module(s) [%s]",
		scannedCmps, scannedFiles, len(mods), strings.Join(parts, " "))

	for _, f := range findings {
		t.Errorf("%s: timing bound `%s` is a bare literal.\n"+
			"    A number tuned on one machine becomes a claim about every machine. Derive it from "+
			"something in scope -- the timeout under test, the delay the server asked for, the grace "+
			"passed in -- or name a constant whose comment says what it protects and why that value.\n"+
			"    Prior art in this repository: `2*timeout` (mcp_misbehaviour_test.go), `2*delay` "+
			"(websearchrobust_test.go), `lspCallTimeout` (lsp_bridge_test.go).", f.file, f.expr)
	}
}

// TestTimingGuardCanStillFail is the self-test, and it exists because the guard
// above FAILED ITS OWN NEUTER.
//
// Making mentionsAnyIdent return true unconditionally left the scan walking 321
// files, counting 25 comparisons, finding nothing, and reporting GREEN. Both
// vacuity floors passed, because they count what was INSPECTED and a blinded
// detector still inspects everything. That is precisely the shape
// scripts/go-toolchain-pinned.sh was caught in -- "7 pin site(s) agree" printed
// by a gate that had stopped checking -- and it was caught here the same way,
// by neutering the fix rather than the code it guards.
//
// A floor on inspection cannot detect a broken detector. Only a case with a
// known answer can. Following the self-test pattern the shell gates already use:
// the checker must demonstrate it can still fail.
func TestTimingGuardCanStillFail(t *testing.T) {
	// INDENTED ON PURPOSE. Go ignores the leading whitespace, but
	// chunkcontext_ast_test.go walks every .go file in the repository and its
	// declaration heuristic is line-oriented -- a `func` at column zero inside a
	// raw string reads to it as a real top-level declaration. This fixture
	// broke TestTheHeuristicAgreesWithTheCompiler and
	// TestConstructExtentsNeverStopShortOfTheCompiler on its first run, which is
	// a genuine (pre-existing, and separately recorded) limit of that heuristic
	// rather than a defect here. Indenting sidesteps it without weakening either.
	const fixture = `
	package p

	import "time"

	func f(elapsed time.Duration, timeout time.Duration) {
		_ = elapsed > 2*time.Second  // BARE: must be flagged
		_ = elapsed > 2*timeout      // derived: must not be
		_ = elapsed < honouredWithin // named: must not be
		_ = elapsed > timeout/5      // derived: must not be
	}
	`
	f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", fixture, 0)
	if err != nil {
		t.Fatal(err)
	}

	var flagged []string
	seen := 0
	ast.Inspect(f, func(n ast.Node) bool {
		be, ok := n.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		switch be.Op {
		case token.LSS, token.GTR, token.LEQ, token.GEQ:
		default:
			return true
		}
		bound, isCmp := boundSideOf(be)
		if !isCmp {
			return true
		}
		seen++
		if mentionsTimeUnit(bound) && !mentionsAnyIdent(bound) {
			flagged = append(flagged, exprText(bound))
		}
		return true
	})

	if seen != 4 {
		t.Fatalf("recognised %d of 4 comparisons in the fixture; boundSideOf no longer sees this shape", seen)
	}
	if len(flagged) != 1 || flagged[0] != "2*time.Second" {
		t.Errorf("flagged %v, want exactly [2*time.Second].\n"+
			"    Too few means the detector has gone blind and the repo-wide scan above is "+
			"reporting clean from a gate that stopped checking. Too many means derived bounds are "+
			"being rejected, which would push people back toward literals.", flagged)
	}
}

// boundSideOf returns the non-measurement side of a comparison, and whether the
// comparison involves a measured duration at all.
//
// Keyed on the SHAPE of the measurement (`elapsed`, or a `time.Since` call)
// rather than on a list of test names, for the same reason the clients/tui guard
// keys on the type rather than on a `*Ceiling` naming convention: a list of
// known call sites is the enumerated-paths mistake this repository has found
// seven times.
func boundSideOf(be *ast.BinaryExpr) (ast.Expr, bool) {
	if isMeasuredDuration(be.X) {
		return be.Y, true
	}
	if isMeasuredDuration(be.Y) {
		return be.X, true
	}
	return nil, false
}

func isMeasuredDuration(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name == "elapsed"
	case *ast.CallExpr:
		sel, ok := v.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		return ok && pkg.Name == "time" && sel.Sel.Name == "Since"
	}
	return false
}

func mentionsTimeUnit(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" {
			switch sel.Sel.Name {
			case "Nanosecond", "Microsecond", "Millisecond", "Second", "Minute", "Hour":
				found = true
			}
		}
		return true
	})
	return found
}

// mentionsAnyIdent reports whether the bound references anything with a
// definition -- a constant, a variable, a field. `time` itself does not count:
// it is the unit, not the derivation.
func mentionsAnyIdent(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			if pkg, ok := v.X.(*ast.Ident); ok && pkg.Name == "time" {
				return false // skip the whole time.X selector
			}
			found = true
			return false
		case *ast.Ident:
			if v.Name != "time" {
				found = true
			}
		}
		return true
	})
	return found
}

func exprText(e ast.Expr) string {
	var b strings.Builder
	writeExpr(&b, e)
	return b.String()
}

func writeExpr(b *strings.Builder, e ast.Expr) {
	switch v := e.(type) {
	case *ast.BasicLit:
		b.WriteString(v.Value)
	case *ast.Ident:
		b.WriteString(v.Name)
	case *ast.SelectorExpr:
		writeExpr(b, v.X)
		b.WriteString(".")
		b.WriteString(v.Sel.Name)
	case *ast.BinaryExpr:
		writeExpr(b, v.X)
		b.WriteString(v.Op.String())
		writeExpr(b, v.Y)
	case *ast.ParenExpr:
		b.WriteString("(")
		writeExpr(b, v.X)
		b.WriteString(")")
	case *ast.CallExpr:
		writeExpr(b, v.Fun)
		b.WriteString("(...)")
	default:
		b.WriteString("?")
	}
}

func moduleOf(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) > 1 && parts[0] == "clients" {
		return "clients/" + parts[1]
	}
	if len(parts) > 1 {
		return parts[0]
	}
	return "."
}

// repoRoot walks up until it finds go.work, so this gate does not depend on
// being run from any particular directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find go.work above the working directory; this gate scans the whole workspace")
	return ""
}
