package main

import (
	"fmt"
	"go/ast"
	"runtime/debug"
	"strings"
	"testing"

	"mochiii/editapply"
	"mochiii/protocol"
)

// 2.2d: THE PANIC PATH IS A SECURITY SURFACE, NOT A TIDINESS ONE.
//
// Bubble Tea's recovery stays on, and it reports a panic by printing the panic
// value with %s and then debug.PrintStack(). Both go to the user's terminal
// AFTER the terminal has been restored -- which means the alternate screen is
// gone and anything escape-shaped in that output is interpreted by a live
// terminal rather than a torn-down one.
//
// m.turns is sanitized at ingest, so a dump of ordinary model state is clean by
// construction. The two buffers that are NOT are the ones deliberately kept
// byte-exact: the pending approval request and the prepared edit. If a panic
// formatter reached them, it would splat model-chosen bytes, and any secret
// sitting in a tool argument, straight past every filter in this package.
//
// So this reproduces what Bubble Tea does, with those buffers live, and looks
// for them in the output.

// panicReport formats a panic exactly the way bubbletea's recoverFromPanic
// does, so what is asserted is the real output rather than a paraphrase.
func panicReport(f func()) (report string) {
	defer func() {
		if r := recover(); r != nil {
			report = fmt.Sprintf("Caught panic:\n\n%s\n\nRestoring terminal...\n\n", r) +
				string(debug.Stack())
		}
	}()
	f()
	return ""
}

func TestPanicOutputCarriesNoUntrustedBytesOrSecrets(t *testing.T) {
	const (
		hostile = "PWNEDMARKER\x1b[2J\x1b]0;title\x07"
		secret  = "sk-ant-SECRETVALUE-0123456789abcdef"
	)
	m := newTestModel()
	req := protocol.ToolApprovalRequest{
		Server:    "srv" + hostile,
		Tool:      "tool",
		Arguments: `{"token": "` + secret + `", "path": "` + hostile + `"}`,
	}
	m.pendingApproval = &req
	m.reviewPrepared = &editapply.PreparedEdit{
		Block:      editapply.EditBlock{FilePath: hostile, Search: secret, Replace: hostile + secret},
		MatchNote:  hostile,
		SyntaxNote: secret,
	}

	report := panicReport(func() {
		// A genuine runtime panic, with the buffers live across it so that any
		// formatter that could reach them would.
		var idx []int
		_ = idx[7]
		_ = m.pendingApproval.Arguments
		_ = m.reviewPrepared.Block.Replace
	})
	if report == "" {
		t.Fatal("nothing panicked, so this test asserted nothing")
	}

	if strings.Contains(report, "PWNEDMARKER") {
		t.Errorf("the panic report carried model-authored bytes:\n%s", report)
	}
	if strings.Contains(report, secret) {
		t.Errorf("the panic report carried a secret from a tool argument:\n%s", report)
	}
	if strings.Contains(report, "\x1b") {
		t.Errorf("the panic report carried a raw escape:\n%q", report)
	}
	// The report must still be useful, or a filter would be the easy way to
	// pass every check above.
	if !strings.Contains(report, "index out of range") {
		t.Errorf("the panic report lost the reason for the panic:\n%s", report)
	}
	if !strings.Contains(report, "panicsafety_test.go") {
		t.Errorf("the panic report lost the location of the panic:\n%s", report)
	}
}

// The same question for a panic value that DOES carry hostile bytes. Nothing in
// this package panics with formatted data -- there is no panic() outside tests
// at all -- but if one were ever added, this says what would reach the
// terminal, so the answer is recorded rather than assumed.
func TestPanicValueCarryingHostileBytesIsReported(t *testing.T) {
	report := panicReport(func() {
		panic("edit refused: " + "x\x1b]0;PWNED\x07y")
	})
	if !strings.Contains(report, "\x1b") {
		t.Skip("a panic value's bytes no longer reach the report; the note below is stale")
	}
	t.Log("RECORDED: a panic VALUE containing escapes would reach the terminal " +
		"verbatim, because Bubble Tea prints it with a plain string verb and " +
		"nothing in this package sees it first. Nothing panics with " +
		"formatted data today (there is no panic() outside tests), which " +
		"is what keeps this theoretical. Adding one would need " +
		"sanitizeText on the message.")
}

// The claim above -- "nothing in this package panics with formatted data" -- is
// what keeps a hostile panic value theoretical, so it is enforced rather than
// asserted. A panic built from a model string would put those bytes on a
// restored, live terminal, past every filter in this package.
func TestNothingPanicsInNonTestCode(t *testing.T) {
	fset, files := parsePackageSource(t)
	var offenders []string
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "panic" {
				return true
			}
			offenders = append(offenders,
				fset.Position(call.Pos()).String()+" in "+enclosingFunc(f, call.Pos())+"()")
			return true
		})
	}
	if len(offenders) > 0 {
		t.Fatalf("this package now panics, at:\n  %s\n\n"+
			"Bubble Tea prints a panic value straight to the terminal, after the\n"+
			"terminal has been restored -- so any escape sequence in the message is\n"+
			"executed by a live terminal. If the message is built from model, tool or\n"+
			"file bytes, wrap it in sanitizeText() first. Then add the site here with\n"+
			"a note saying which. Returning an error is almost always better.",
			strings.Join(offenders, "\n  "))
	}
}
