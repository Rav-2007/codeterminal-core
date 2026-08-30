package editapply

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE SYNTAX GATE JUDGES THE DELTA, NOT THE RESULT.
//
// It used to look only at what an edit produced, so an edit to an ALREADY-broken
// .go file was refused with "edit would make foo.go unparseable as Go" -- a
// false statement about what the edit did, and one that made a broken file
// unfixable through edit blocks. A model asked to repair a syntax error could
// not, because its repair was judged against a standard the file did not meet
// before it started.
//
// The four cells below are the whole contract. Two of them (the BROKEN-before
// rows) were refusals until this change and are the reason it exists; the other
// two must be exactly what they always were, because loosening the gate's actual
// job -- do not break a WORKING file -- is the failure mode this test is really
// guarding against.

const (
	goOK     = "package main\n\nfunc ok() {}\n"
	goBroken = "package main\n\nfunc broken( {\n"
)

func TestSyntaxGate_DeltaMatrix(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after string
		wantRefused   bool
		wantNote      string
	}{
		{
			name:   "parses -> parses: allowed, and says so",
			before: goOK, after: "package main\n\nfunc fixed() {}\n",
			wantRefused: false, wantNote: "go/parser OK",
		},
		{
			// The gate's whole purpose. If this row ever flips, the gate is gone.
			name:   "parses -> BROKEN: refused, which is the point of the gate",
			before: goOK, after: goBroken,
			wantRefused: true,
		},
		{
			name:   "BROKEN -> parses: allowed, because this is a repair",
			before: goBroken, after: goOK,
			wantRefused: false, wantNote: "go/parser OK",
		},
		{
			name:   "BROKEN -> BROKEN: allowed, and the note says the file was already broken",
			before: goBroken, after: "package main\n\nfunc stillBroken( {\n",
			wantRefused: false, wantNote: "did not parse before this edit either",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			note, err := checkEditSyntax("foo.go", tc.before, tc.after)
			if tc.wantRefused {
				if err == nil {
					t.Fatalf("checkEditSyntax allowed the one transition the gate exists to refuse; note = %q", note)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkEditSyntax refused: %v", err)
			}
			if !strings.Contains(note, tc.wantNote) {
				t.Errorf("note = %q, want it to contain %q", note, tc.wantNote)
			}
		})
	}
}

// The user-facing point of the delta rule, driven through the real door rather
// than the helper: a file with a syntax error must be repairable by an edit.
func TestBrokenGoFileCanBeFixedByAnEdit(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n\nfunc broken( {}\n")

	block := EditBlock{FilePath: "foo.go", Search: "func broken( {}", Replace: "func fixed() {}"}
	prepared, err := PrepareEdit(root, block)
	if err != nil {
		t.Fatalf("an edit REPAIRING a broken Go file was refused: %v\n\n"+
			"This is the bug the delta rule exists to close. Judging the result alone "+
			"made a broken file unfixable through edit blocks, and told the user the "+
			"edit had caused breakage that was already there.", err)
	}
	if prepared.SyntaxNote != "go/parser OK" {
		t.Errorf("SyntaxNote = %q, want the repaired file to be reported as parsing", prepared.SyntaxNote)
	}
}

// A partial repair -- still broken afterwards -- must also be allowed, or a
// multi-step fix stalls on its first step. What it must NOT do is pretend the
// result is fine.
func TestPartialRepairIsAllowedButHonestlyNoted(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n\nfunc a( {\n\nfunc b( {\n")

	block := EditBlock{FilePath: "foo.go", Search: "func a( {", Replace: "func a() {}"}
	prepared, err := PrepareEdit(root, block)
	if err != nil {
		t.Fatalf("a partial repair was refused: %v", err)
	}
	if !strings.Contains(prepared.SyntaxNote, "did not parse before this edit either") {
		t.Errorf("SyntaxNote = %q, want it to say the file was already broken -- allowing the "+
			"edit is right, but reporting it as clean would be a second lie in place of the first",
			prepared.SyntaxNote)
	}
}

// The gate still does its job. If this test can be made to pass with the delta
// rule widened to allow everything, the rule has eaten the gate.
func TestEditIntoBrokenIsStillRefusedWhenTheFileParsed(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n\nfunc old() {}\n")

	block := EditBlock{FilePath: "foo.go", Search: "func old() {}", Replace: "func broken( {"}
	_, err := PrepareEdit(root, block)
	if err == nil {
		t.Fatal("an edit that BROKE a working Go file was allowed; the delta rule has been " +
			"widened past the transition the gate exists to refuse")
	}
	if !strings.Contains(err.Error(), "unparseable") {
		t.Errorf("error = %v, want it to mention unparseable Go", err)
	}
}

// THE ASYMMETRY, AND THE CASE THAT MAKES IT SHARP.
//
// An empty .go file does not parse ("expected 'package', found 'EOF'"), and an
// empty SEARCH section against an existing empty file routes to prepareCreate.
// So a delta rule applied naively to the CREATE path would read "before" as
// broken and wave through any content at all -- reopening the M7 laundering
// route the shared gate was written to close, through the one door where the
// prior state is always broken.
//
// prepareCreate is absolute for exactly this reason: "this file's content is, or
// should be, nothing", and nothing has no prior brokenness to inherit.
func TestFillingAnEmptyGoFileWithGarbageIsStillRefused(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "empty.go", "")

	block := EditBlock{FilePath: "empty.go", Search: "", Replace: "func broken( {\n"}
	_, err := PrepareEdit(root, block)
	if err == nil {
		t.Fatal("unparseable Go was written into an existing EMPTY .go file.\n\n" +
			"An empty file does not parse, so a delta rule applied to the create path " +
			"reads its prior state as \"already broken\" and excuses anything. That is " +
			"the M7 laundering route: a model whose edit was refused resends the same " +
			"bytes with an empty SEARCH section. The create path must stay absolute.")
	}
	if !strings.Contains(err.Error(), "unparseable as Go") {
		t.Errorf("error = %v, want the same wording the edit path uses", err)
	}
}

func TestCreateOfUnparseableGoIsStillRefused(t *testing.T) {
	root := realTempDir(t)

	block := EditBlock{FilePath: "new.go", Search: "", Replace: "func broken( {\n"}
	if _, err := PrepareEdit(root, block); err == nil {
		t.Fatal("creating an unparseable .go file was allowed; the create path has picked up " +
			"the delta rule, which it must not -- there is no prior state to excuse it with")
	}

	if _, err := os.Stat(filepath.Join(root, "new.go")); !os.IsNotExist(err) {
		t.Error("a refused create left a file behind")
	}
}

// A language with no parser in this binary is never refused -- on either side
// of the delta.
//
// SPLIT 2026-08-30, WHEN TIER B ARRIVED. This test used to assert that .py and
// .js also say "no syntax check applied", and that assertion was correct right
// up until those two languages started getting a delimiter check. It failed on
// the Tier B branch, which is the test doing its job: the note is a claim about
// what this build did, and what it does changed.
//
// The invariant that survives untouched is the one in the name -- NEVER
// REFUSED. Both groups below assert it. What splits is the note, because
// "nothing checked this" and "the brackets balance" are different claims and
// collapsing them is how a gate starts overstating itself.
func TestUncheckedLanguagesAreNeverRefusedByTheGate(t *testing.T) {
	// No tier at all: no parser, no delimiter check. The note must say so.
	for _, path := range []string{"main.rs", "notes.txt"} {
		note, err := checkEditSyntax(path, "anything at all", "{[(<<< not balanced")
		if err != nil {
			t.Errorf("checkEditSyntax(%q) refused: %v; only Go has a parser here", path, err)
		}
		if !strings.Contains(note, "no syntax check applied") {
			t.Errorf("note for %q = %q, want it to say plainly that nothing was checked", path, note)
		}
	}

	// Tier B: checked, still never refused, and the note says which it was.
	for _, path := range []string{"app.py", "index.js"} {
		note, err := checkEditSyntax(path, "anything at all", "{[(<<< not balanced")
		if err != nil {
			t.Errorf("checkEditSyntax(%q) refused: %v; Tier B is advisory and must never refuse",
				path, err)
		}
		if !strings.Contains(strings.ToLower(note), "advisory") {
			t.Errorf("note for %q = %q, want it marked advisory", path, note)
		}
		if strings.Contains(note, "no syntax check applied") {
			t.Errorf("note for %q = %q, but a delimiter check DID run -- claiming nothing was "+
				"checked understates the build", path, note)
		}
	}
}

// ---------------------------------------------------------------------------
// TIER B: delimiter balance, advisory only.
// ---------------------------------------------------------------------------

// TestTierBNeverRefuses is the load-bearing test of Tier B.
//
// Every case here is an edit that leaves the file structurally worse. All of
// them must be APPLIED. A bracket count is a heuristic, and a heuristic that
// refuses blocks correct edits on constructs it failed to model -- exactly the
// fault the delta rule removed from Tier A, reintroduced by a weaker check with
// more confidence than it has earned.
//
// If any case here starts returning an error, Tier B has become a gate and the
// design is broken, whatever the note says.
func TestTierBNeverRefuses(t *testing.T) {
	for _, tc := range []struct {
		name, file, before, search, replace string
	}{
		{
			name: "edit unbalances a balanced file", file: "a.ts",
			before: "function f() { return 1; }\n",
			search: "function f() { return 1; }", replace: "function f() { return 1;",
		},
		{
			name: "edit adds a stray closer", file: "a.ts",
			before: "const a = 1;\n",
			search: "const a = 1;", replace: "const a = 1; }",
		},
		{
			name: "edit leaves an unterminated string", file: "a.js",
			before: "const a = 1;\n",
			search: "const a = 1;", replace: "const a = \"oops;",
		},
		{
			name: "edit leaves an unterminated block comment", file: "a.ts",
			before: "const a = 1;\n",
			search: "const a = 1;", replace: "/* oops",
		},
		{
			name: "python bracket left open", file: "a.py",
			before: "d = {'a': 1}\n",
			search: "d = {'a': 1}", replace: "d = {'a': 1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := realTempDir(t)
			writeTempFile(t, root, tc.file, tc.before)

			prepared, err := PrepareEdit(root, EditBlock{
				FilePath: tc.file, Search: tc.search, Replace: tc.replace,
			})
			if err != nil {
				t.Fatalf("TIER B REFUSED AN EDIT: %v\n\n"+
					"Tier B is a bracket count, not a parser. It is allowed to say what it "+
					"noticed and nothing more. A refusal here blocks a correct edit whenever "+
					"the scan mismodels a construct, which is the failure mode the delta rule "+
					"was written to remove from the tier that DOES have a parser.", err)
			}
			if !strings.Contains(strings.ToLower(prepared.SyntaxNote), "advisory") {
				t.Errorf("SyntaxNote = %q, want the note to mark itself advisory so a reader "+
					"does not take a bracket count for a syntax error", prepared.SyntaxNote)
			}
		})
	}
}

// A balanced edit to a Tier B file gets a note saying so, and says which tier
// produced it -- "delimiters balanced" and "no syntax check applied" are very
// different claims and the user is entitled to know which one they got.
func TestTierBReportsABalancedEditAsChecked(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "a.ts", "function f() { return 1; }\n")

	prepared, err := PrepareEdit(root, EditBlock{
		FilePath: "a.ts", Search: "return 1;", Replace: "return [1, 2];",
	})
	if err != nil {
		t.Fatalf("PrepareEdit: %v", err)
	}
	if !strings.Contains(prepared.SyntaxNote, "balanced") {
		t.Errorf("SyntaxNote = %q, want it to report that the delimiters were checked and balanced",
			prepared.SyntaxNote)
	}
	if strings.Contains(prepared.SyntaxNote, "no syntax check applied") {
		t.Errorf("SyntaxNote = %q -- this file IS checked now, at Tier B. Reporting the "+
			"pre-Tier-B message understates what the build did", prepared.SyntaxNote)
	}
}

// Tier A is untouched by Tier B's arrival, and so is Tier "none".
func TestTierAIsUnchangedByTierB(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n\nfunc old() {}\n")

	if _, err := PrepareEdit(root, EditBlock{
		FilePath: "foo.go", Search: "func old() {}", Replace: "func broken( {",
	}); err == nil {
		t.Fatal("Go must still REFUSE an edit that breaks a working file -- Tier B's " +
			"advisory contract must not have leaked onto the tier that has a parser")
	}

	writeTempFile(t, root, "a.rs", "fn main() {}\n")
	prepared, err := PrepareEdit(root, EditBlock{
		FilePath: "a.rs", Search: "fn main() {}", Replace: "fn main() {",
	})
	if err != nil {
		t.Fatalf("an unchecked language must never be refused: %v", err)
	}
	if !strings.Contains(prepared.SyntaxNote, "no syntax check applied") {
		t.Errorf("SyntaxNote = %q, want .rs to still report that nothing checked it -- "+
			"Tier B covers the table's languages, and Rust is deliberately not one",
			prepared.SyntaxNote)
	}
}
