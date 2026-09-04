package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// STRUCTURAL GUARDS, NOT CONVENTIONS.
//
// Two kinds of bytes in this program are unsafe to render and must stay
// byte-exact where they are stored: an edit block (those bytes get written to
// disk) and a tool-approval request (the daemon binds its approval digest to
// them). Both are therefore filtered at RENDER rather than at ingest, which
// means the only thing keeping them off the terminal is that today's render
// paths remember to call the filter.
//
// That is exactly the guarantee that decays. The commit which introduced the
// filter, written while paying attention to this specific problem, still
// missed several appends into the transcript. A convention that fails inside
// the commit that establishes it will not survive six months of unrelated
// work.
//
// So these tests read the package's own source. They enumerate the statements
// and functions that can put untrusted bytes in front of a user, and fail on
// any that is not on a list a human had to edit deliberately. The failure
// message is the review: it tells the author what to do rather than only that
// something is wrong.

func parsePackageSource(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatal("no non-test source files found; this guard would pass vacuously")
	}
	return fset, files
}

// enclosingFunc returns the name of the function declaration containing pos.
func enclosingFunc(f *ast.File, pos token.Pos) string {
	name := "<file scope>"
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Pos() <= pos && pos <= fn.End() {
			name = fn.Name.Name
		}
	}
	return name
}

// isSel reports whether n is a selector expression ending in field.
func isSel(n ast.Node, field string) bool {
	sel, ok := n.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == field
}

// GUARD 1: appendTurn is the only door into the transcript.
//
// Every turn shown to the user, carried back to the daemon as history, and
// searched by /search passes through m.turns. appendTurn sanitizes; a bare
// `m.turns = append(...)` does not. This fails on any new one.
func TestOnlyAppendTurnWritesTheTranscript(t *testing.T) {
	// The one place allowed to assign m.turns from an append outside
	// appendTurn: /compact re-slices turns that appendTurn already cleaned.
	// Nothing new enters the transcript there.
	allowed := map[string]bool{
		"appendTurn":       true,
		"handleLocalSlash": true, // /compact's re-slice; see the comment there
	}
	fset, files := parsePackageSource(t)
	var offenders []string
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			writesTurns := false
			for _, lhs := range as.Lhs {
				if isSel(lhs, "turns") {
					writesTurns = true
				}
			}
			if !writesTurns {
				return true
			}
			for _, rhs := range as.Rhs {
				call, ok := rhs.(*ast.CallExpr)
				if !ok {
					continue
				}
				if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "append" {
					continue
				}
				if fn := enclosingFunc(f, as.Pos()); !allowed[fn] {
					offenders = append(offenders, fset.Position(as.Pos()).String()+" in "+fn+"()")
				}
			}
			return true
		})
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("a turn enters the transcript without being sanitized, at:\n  %s\n\n"+
			"Use m.appendTurn(turn{...}) instead of appending to m.turns directly. It\n"+
			"sanitizes text and reasoning (sanitize.go), which is what keeps model- and\n"+
			"server-authored bytes from reaching the terminal as control sequences.\n"+
			"If this site genuinely cannot use it, add its function to the allowlist in\n"+
			"this test and say in the commit message why.",
			strings.Join(offenders, "\n  "))
	}
}

// GUARD 2: the two raw-byte structures are read by a known, small set of
// functions, every one of which sanitizes what it renders.
//
// A new function reading m.reviewPrepared or m.pendingApproval is a new path
// to the terminal for bytes that are deliberately stored unfiltered. The list
// is not "functions that are correct"; it is "functions a human checked".
func TestRawByteStructuresHaveNoNewReaders(t *testing.T) {
	// Reviewed on 2026-09-03. Each of these either sanitizes what it renders
	// (renderReviewPanel, renderApprovalPanel, View, refreshViewport) or does
	// not render at all (the state transitions and the writer).
	reviewed := map[string]bool{
		"renderReviewPanel":      true, // sanitizes every field it draws
		"renderApprovalPanel":    true, // sanitizes every field it draws
		"View":                   true, // sanitizes the two status lines
		"refreshViewport":        true, // delegates to the two panels above
		"checkForEditBlocks":     true, // assigns; renders nothing
		"advanceReview":          true, // assigns; renders nothing
		"finishReview":           true, // clears; the summary goes via appendTurn
		"applyCurrentReviewEdit": true, // writes to disk, deliberately unfiltered
		"answerApproval":         true, // sends the decision; line goes via appendTurn
		"Update":                 true, // stores the request; renders nothing
	}
	fset, files := parsePackageSource(t)
	seen := map[string]string{}
	for _, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if isSel(n, "reviewPrepared") || isSel(n, "pendingApproval") {
					if _, dup := seen[fn.Name.Name]; !dup {
						seen[fn.Name.Name] = fset.Position(fn.Pos()).String()
					}
				}
				return true
			})
		}
	}
	var novel []string
	for name, pos := range seen {
		if !reviewed[name] {
			novel = append(novel, pos+" "+name+"()")
		}
	}
	if len(novel) > 0 {
		sort.Strings(novel)
		t.Fatalf("a new function reads bytes that are stored UNFILTERED on purpose:\n  %s\n\n"+
			"m.reviewPrepared holds an edit that must stay byte-exact because those bytes\n"+
			"get written to disk; m.pendingApproval holds a request the daemon binds its\n"+
			"approval digest to. Neither is safe to put on a terminal as-is. If this\n"+
			"function renders any of it, wrap every field in sanitizeText() first. Then\n"+
			"add it to the reviewed list in this test, with a note saying which.",
			strings.Join(novel, "\n  "))
	}
}

// The guards must fail when the thing they guard is removed, or they are
// decoration. This checks the detector itself against synthetic source rather
// than against the real package, so it cannot pass by accident.
func TestGuardsDetectAViolation(t *testing.T) {
	// INDENTED, AND CONCATENATED RATHER THAN A RAW STRING, ON PURPOSE.
	//
	// This snippet is Go source to the parser below, but the file it lives in is
	// also scanned AS TEXT by the daemon's chunking heuristic (see
	// TestTheHeuristicAgreesWithTheCompiler in codeterminal/daemon). A `func` at
	// column 0 inside a raw string is indistinguishable from a real top-level
	// declaration to any line-based scanner, and go/parser correctly disagrees.
	// The first version of this test was written as a raw string and turned two
	// daemon tests red -- "the heuristic invents 1 boundary" and "constructExtents
	// stops SHORT" -- for a function that does not exist.
	//
	// Go ignores the indentation; the scanner does not.
	const bad = "package main\n" +
		"\tfunc somethingNew(m *chatModel) {\n" +
		"\t\tm.turns = append(m.turns, turn{role: roleSystem, text: \"unfiltered\"})\n" +
		"\t\t_ = m.pendingApproval.Arguments\n" +
		"\t}\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic.go", bad, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sawAppend, sawReader bool
	ast.Inspect(f, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok {
			for _, lhs := range as.Lhs {
				if isSel(lhs, "turns") {
					for _, rhs := range as.Rhs {
						if call, ok := rhs.(*ast.CallExpr); ok {
							if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "append" {
								sawAppend = true
							}
						}
					}
				}
			}
		}
		if isSel(n, "pendingApproval") {
			sawReader = true
		}
		return true
	})
	if !sawAppend {
		t.Error("the transcript-append detector did not fire on source that appends directly")
	}
	if !sawReader {
		t.Error("the raw-structure-reader detector did not fire on source that reads one")
	}
	if got := enclosingFunc(f, f.Decls[0].Pos()); got != "somethingNew" {
		t.Errorf("enclosingFunc = %q, want %q", got, "somethingNew")
	}
}
