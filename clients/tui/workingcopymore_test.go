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
