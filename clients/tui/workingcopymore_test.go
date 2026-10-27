package tui

import (
	"strings"
	"testing"

	"mochiii/protocol"
)

// What a command changed in the real project, outside the review, is named
// above it: the review below cannot take it back.
func TestTheReviewNamesChangesOutsideIt(t *testing.T) {
	text := workingCopyText(&protocol.WorkingCopyInfo{Checked: "make test", Passed: true,
		OutsideReview: []string{"package.json (npm install lodash)"}})
	if !strings.Contains(text, "! changed in your real project, outside review: package.json (npm install lodash)") {
		t.Errorf("the change outside review is not named:\n%s", text)
	}
}

// A pass after the agent changed existing tests is never a tick: the pass may
// come from the changed test (the daemon's testedits.go).
func TestAPassAfterChangedTestsIsNoTick(t *testing.T) {
	text := workingCopyText(&protocol.WorkingCopyInfo{Checked: "ctest --test-dir build", Passed: true,
		TestsChanged: []string{"tests/misc_tests.c"}})
	if strings.Contains(text, "✓") || !strings.Contains(text, "! checked: ctest --test-dir build passed") ||
		!strings.Contains(text, "! the agent changed existing tests: tests/misc_tests.c") {
		t.Errorf("a pass after a changed test is shown as:\n%s", text)
	}
}
