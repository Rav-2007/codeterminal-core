package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// A TIMING BUDGET THAT RUNS UNDER THE RACE DETECTOR IS A FLAPPING GATE.
//
// THE MEASUREMENT THAT PRODUCED THIS GUARD: the same 1MB paste, same machine,
// same commit, sampled four times, gave medians of 6.3ms, 13.6ms, 14.5ms and
// 16.6ms -- a 2.6x spread straddling the 16ms frame budget. Two versions of that
// assertion flapped, once in each direction: one failed on a machine that was
// merely busy, and one passed where it should have caught a regression. Under
// `-race`, which is how this repository's own test gate runs, the same numbers
// are 5-20x slower again.
//
// So the policy, recorded in docs/ENGINEERING_METHOD.md section 12: allocations
// are what a performance test FAILS on, because they are deterministic and
// machine-independent; wall-clock is reported with its distance to the budget
// and asserted only at a wide multiple; and timing tests skip under -race via
// the raceEnabled build-tag constant.
//
// A policy in a document is a preference. This is the part that is enforced.
//
// STRUCTURAL, NOT NAME-DRIVEN, on purpose. It keys on the TYPE of the
// declaration -- any constant or variable in this package's tests whose value is
// a time.Duration -- so a new budget in a new file is caught whatever it is
// called. Keying on a name like "*Ceiling" would only ever find the constants
// somebody remembered to name that way, which is the same enumerated-paths
// mistake as guarding a list of call sites instead of the property.
//
// WHAT IT DOES NOT CATCH, stated so the gate is not trusted past its reach: a
// timing assertion written inline with no named constant (`if elapsed >
// 16*time.Millisecond`). Finding those needs dataflow, not a declaration scan.
func TestTimingBudgetsAreGuardedAgainstTheRaceDetector(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	// A guard that scans for a shape must prove it found the shape. Without
	// this, a change to durationBudgetsIn that stopped matching anything would
	// leave a test that passes and checks nothing.
	scanned := 0

	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		budgets := durationBudgetsIn(f)
		if len(budgets) == 0 {
			continue
		}
		scanned++
		// The build-tag pair only ever needs to be REFERENCED, not called: a
		// file that mentions raceEnabled is a file whose author knew the
		// measurement is not comparable under the detector.
		if strings.Contains(string(src), "raceEnabled") {
			continue
		}
		t.Errorf("%s declares a time budget (%s) but never mentions raceEnabled.\n"+
			"Wall-clock under -race is 5-20x slower, and this repository's test gate runs -race, "+
			"so a budget asserted here either fails there or has to be loosened until it measures "+
			"nothing. Assert allocations and skip the timing under raceEnabled -- see "+
			"docs/ENGINEERING_METHOD.md section 12.",
			name, strings.Join(budgets, ", "))
	}

	if scanned == 0 {
		t.Fatal("no test file in this package declares a time budget, so this guard " +
			"asserted nothing. Either the budgets moved, or durationBudgetsIn stopped " +
			"recognising them.")
	}
}

// durationBudgetsIn returns the names of package-level constants and variables
// whose value is built from time.Millisecond and friends. A duration declared at
// package level in a test is a threshold; a duration passed inline to
// time.Sleep or a deadline is not, which is why only declarations are scanned.
func durationBudgetsIn(f *ast.File) []string {
	var names []string
	for _, d := range f.Decls {
		gen, ok := d.(*ast.GenDecl)
		if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, v := range vs.Values {
				if i < len(vs.Names) && isDurationExpr(v) {
					names = append(names, vs.Names[i].Name)
				}
			}
		}
	}
	return names
}

// isDurationExpr reports whether e mentions a time package duration unit. It is
// syntactic -- there is no type information here -- which is why it looks for
// the selector rather than trying to resolve the type.
func isDurationExpr(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "time" {
			return true
		}
		switch sel.Sel.Name {
		case "Nanosecond", "Microsecond", "Millisecond", "Second", "Minute", "Hour", "Duration":
			found = true
		}
		return true
	})
	return found
}
