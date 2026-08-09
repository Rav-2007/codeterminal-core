package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// BOTH CLIENTS MUST CARRY THE CUT-OFF FACT, NOT JUST THIS ONE.
//
// The TUI and the VS Code client had the SAME bug in the same shape: each
// rendered "answer cut off" for the user, and each then sent the truncated text
// back as ordinary history with nothing marking it. Fixing one and not the
// other would leave a user of the unfixed client with a model that quietly
// treats its own half-finished answers as complete -- and nothing would say so.
//
// This is the third mirror in this project to get a test instead of a comment,
// and the reason is the record: the TUI/VS Code pair has already drifted twice
// (4918a0c, 76a87d0), both times because a fix landed in one client and the
// other was left behind. See slash_clientparity_test.go for the same argument
// at length.
//
// These are SOURCE-LEVEL checks, deliberately coarse. They cannot prove the VS
// Code client's flag is correct -- clients/vscode/src/test/suite covers behavior
// where it can reach it -- but they do fail the build if the half that carries
// the fact is deleted, which is the failure this project keeps actually having.
const (
	vscodeDaemonClient = "../vscode/src/daemonClient.ts"
	vscodeChatPanel    = "../vscode/src/chatPanel.ts"
)

// incompleteField matches the FIELD `incomplete:` and nothing else -- not the
// bare word.
//
// Both looser forms were tried and both were measured letting a real regression
// through, which is why this is one shared, tested matcher rather than three
// strings.Contains calls:
//
//	"incomplete" in the Turn interface   matched the field's own DOC COMMENT
//	"incomplete" in the onDone handler   matched `incompleteReason`, the local
//	                                     variable holding the value, so deleting
//	                                     the field it is assigned to changed nothing
//
// The `\b...\s*:` shape is what separates a use of the name from a declaration
// of the field. TestCutOffParityParsersAreNotVacuous drives it against both
// near-misses above.
var incompleteField = regexp.MustCompile(`\bincomplete\??\s*:`)

func readClientSource(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v -- this test enforces a cross-client mirror and cannot run "+
			"without it", path, err)
	}
	// ANTI-VACUITY: an empty or truncated read would make every strings.Contains
	// below fail loudly rather than pass quietly, but a MISSING file would not,
	// so the size floor guards the case where the path silently stops resolving.
	if len(raw) < 2000 {
		t.Fatalf("%s is only %d bytes; the path must have changed and these checks are "+
			"meaningless", path, len(raw))
	}
	return string(raw)
}

// The wire type must have somewhere to put it. Without this field the VS Code
// client cannot express a cut-off turn at all, whatever chatPanel does.
func TestVSCodeTurnTypeCarriesTheCutOffFlag(t *testing.T) {
	src := readClientSource(t, vscodeDaemonClient)

	block := regexp.MustCompile(`(?s)export interface Turn \{(.*?)\}`).FindStringSubmatch(src)
	if block == nil {
		t.Fatal("could not find `export interface Turn { ... }` in daemonClient.ts; the parser " +
			"anchor has moved and this check is no longer looking at anything")
	}
	// Match the DECLARATION, not the word. The field is documented in a comment
	// that also contains "incomplete", so a substring check passes on the prose
	// alone -- measured: deleting the field left a plain Contains() green.
	if !incompleteField.MatchString(block[1]) {
		t.Errorf("the VS Code client's Turn type has no `incomplete` field, so a cut-off answer "+
			"goes back to the daemon indistinguishable from a finished one "+
			"(protocol.Turn.Incomplete is how the fact travels):\n%s", block[1])
	}
}

// Capturing the reason and attaching it to the transcript are two separate
// steps, and either one missing loses the fact. They are checked separately so
// a failure names which half went.
func TestVSCodeChatPanelCarriesTheCutOffFlagIntoHistory(t *testing.T) {
	src := readClientSource(t, vscodeChatPanel)

	// Step 1: onIncomplete must do more than post to the webview -- posting is
	// the SCREEN half, which was never the broken one.
	handler := regexp.MustCompile(`(?s)onIncomplete: \(info: IncompleteInfo\) => \{(.*?)\n\s*\},`).
		FindStringSubmatch(src)
	if handler == nil {
		t.Fatal("could not find the onIncomplete handler in chatPanel.ts; the parser anchor has " +
			"moved and this check is no longer looking at anything")
	}
	if !strings.Contains(handler[1], "info.reason") {
		t.Errorf("chatPanel's onIncomplete never reads info.reason, so the cut-off fact reaches "+
			"the webview and stops there -- exactly the bug this guards:\n%s", handler[1])
	}

	// Step 2: the transcript turn must carry it. The transcript is what becomes
	// the next request's history (`const history = [...this.transcript]`).
	//
	// Anchored INSIDE the onDone handler, not searched file-wide. A file-wide
	// match is what this test did first, and adding the second push site (in
	// onError, checked below) silently defeated it: neutering the onDone push
	// left the onError one matching, and the neuter matrix reported NOT CAUGHT
	// for a live regression. Each push site is now checked where it lives.
	handlerDone := regexp.MustCompile(`(?s)onDone: \(\) => \{(.*?)\n\s*\},`).FindStringSubmatch(src)
	if handlerDone == nil {
		t.Fatal("could not find the onDone handler in chatPanel.ts; the parser anchor has moved")
	}
	if !incompleteField.MatchString(handlerDone[1]) {
		t.Errorf("chatPanel's onDone pushes an assistant turn with no `incomplete` field, so a "+
			"cut-off answer reaches the next request looking finished:\n%s", handlerDone[1])
	}

	// Step 3: the ERROR path too. A stream that fails partway has already shown
	// the user real text; both clients keep it and mark it, and neither may
	// silently drop it (which loses it from the model's view) or keep it
	// unmarked (which presents it as finished). The TUI's half is
	// TestChat_APartialAnswerThatThenErroredIsMarkedInHistory.
	handlerErr := regexp.MustCompile(`(?s)onError: \(err: Error\) => \{(.*?)\n\s*\},`).
		FindStringSubmatch(src)
	if handlerErr == nil {
		t.Fatal("could not find the onError handler in chatPanel.ts; the parser anchor has moved")
	}
	if !incompleteField.MatchString(handlerErr[1]) {
		t.Errorf("chatPanel's onError does not mark the partial answer it keeps, so a stream that "+
			"failed partway either vanishes from history or returns to the model looking "+
			"finished:\n%s", handlerErr[1])
	}
}

// A guard on the guards. Both regexes above are anchored on real syntax, and a
// silent non-match would turn either test into a t.Fatal rather than a false
// pass -- but only if the anchors are actually present TODAY. This drives them
// against known-good and known-bad input so a rewrite that breaks the parser is
// caught as a parser failure rather than mistaken for a code failure.
func TestCutOffParityParsersAreNotVacuous(t *testing.T) {
	turnBlock := regexp.MustCompile(`(?s)export interface Turn \{(.*?)\}`)
	if turnBlock.FindStringSubmatch("export interface Turn {\n  role: string;\n}") == nil {
		t.Error("the Turn-interface parser does not match a minimal valid declaration")
	}
	if turnBlock.FindStringSubmatch("interface NotATurn { role: string; }") != nil {
		t.Error("the Turn-interface parser matches a declaration it should not")
	}

	// The field-declaration matcher must reject a doc comment that merely
	// mentions the name -- the hole the neuter matrix found in this very test.
	if !incompleteField.MatchString("  incomplete?: string;") {
		t.Error("the field matcher does not match a real declaration")
	}
	if !incompleteField.MatchString("{ role: 'assistant', content: answer, incomplete: reason }") {
		t.Error("the field matcher does not match an inline object property")
	}
	if incompleteField.MatchString("  // incomplete mirrors protocol.Turn.Incomplete: a slug") {
		t.Error("the field matcher matches a COMMENT mentioning the name, so deleting the field " +
			"would leave this test green -- the exact vacuity it exists to prevent")
	}
	if incompleteField.MatchString("incompleteReason\n  ? { role: 'assistant' }") {
		t.Error("the field matcher matches the local variable `incompleteReason`, so neutering " +
			"the field it feeds would leave this test green -- measured, it did")
	}

	// The handler-block matchers must isolate ONE handler. A matcher that runs
	// past its own closing brace would pull in the next handler's body, and a
	// neighbour carrying `incomplete` would then satisfy a check for this one --
	// which is exactly how the file-wide version of step 2 was defeated when the
	// onError push was added.
	done := regexp.MustCompile(`(?s)onDone: \(\) => \{(.*?)\n\s*\},`)
	twoHandlers := "        onDone: () => {\n          pushWithoutTheField();\n        },\n" +
		"        onError: (err: Error) => {\n          push({ incomplete: r });\n        },"
	m := done.FindStringSubmatch(twoHandlers)
	if m == nil {
		t.Fatal("the onDone matcher does not match a realistic handler pair")
	}
	if incompleteField.MatchString(m[1]) {
		t.Error("the onDone matcher runs past its own handler and captures the NEXT one, so a " +
			"neighbouring push would satisfy a check meant for this handler")
	}
}
