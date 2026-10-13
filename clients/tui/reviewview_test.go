package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// longEditReview puts the model in front of one edit whose diff is far taller
// than a 24-row terminal: a one-line function replaced by an 84-line one.
func longEditReview(t *testing.T) (chatModel, string) {
	t.Helper()
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n\nfunc old() {}\n")

	var body strings.Builder
	body.WriteString("func new_() {\n\tfirst := 1 // FIRST-LINE\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&body, "\t_ = %d\n", i)
	}
	body.WriteString("\t_ = first // LAST-LINE\n}")

	m := newTestModelWithRoot(root)
	m = typeText(m, "fix")
	m, _ = pressEnter(m)
	updated, _ := m.Update(tokenMsg(editBlockText("foo.go", "func old() {}", body.String())))
	updated, _ = updated.(chatModel).Update(streamDoneMsg{})
	m = updated.(chatModel)
	if m.state != stateEditReview {
		t.Fatalf("precondition failed: state = %v, want the edit under review", m.state)
	}
	return m, root
}

// AN EDIT TALLER THAN THE SCREEN IS REVIEWED FROM ITS BEGINNING.
//
// The same defect as the approval question's (approvalview.go), one panel over:
// the diff was the last thing in the transcript and the view followed it to
// the bottom, so a long edit was put to the user as its last screenful and
// "y apply" -- which file, which lines, and what was being REMOVED had scrolled
// away, and only the mouse wheel brought them back.
func TestALongEditIsReviewedFromItsBeginning(t *testing.T) {
	m, _ := longEditReview(t)
	screen := ansi.Strip(m.View())
	for _, want := range []string{"--- edit 1/1: foo.go", "- func old() {}", "FIRST-LINE"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the screen asking to apply the edit does not show %q:\n%s", want, screen)
		}
	}
}

// AN EDIT THAT WAS NOT ALL SHOWN IS NOT APPLIED. Skipping it, and stopping the
// review, stay one key away throughout.
func TestALongEditIsNotAppliedBeforeItWasAllShown(t *testing.T) {
	m, root := longEditReview(t)
	m = pressRune(t, m, 'y')
	if m.state != stateEditReview {
		t.Fatalf("y left the review (state %v) with most of the edit never shown", m.state)
	}
	if got := readFileString(t, filepath.Join(root, "foo.go")); strings.Contains(got, "new_") {
		t.Fatalf("the edit was written to disk before it had been shown")
	}
	screen := ansi.Strip(m.View())
	for _, want := range []string{"more line", "n skip", "q cancel"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the key line does not show %q:\n%s", want, screen)
		}
	}

	skipped := pressRune(t, m, 'n')
	if skipped.state != stateIdle {
		t.Fatalf("n did not skip the edit; state = %v", skipped.state)
	}
	if summary := lastSystemText(skipped.turns); !strings.Contains(summary, "0 applied, 1 skipped") {
		t.Errorf("summary = %q, want the edit skipped", summary)
	}
	// And the view is back at the bottom: what happens next is not off screen,
	// and the screen is not left scrolled past the end of a transcript that
	// has just lost eighty lines.
	if screen := ansi.Strip(skipped.View()); !strings.Contains(screen, "0 applied, 1 skipped") {
		t.Errorf("the review's summary is not on screen after it ended:\n%s", screen)
	}
	if skipped.viewport.PastBottom() || !skipped.viewport.AtBottom() {
		t.Errorf("after the review the view is not at the bottom of the transcript (offset %d of %d lines, %d rows)",
			skipped.viewport.YOffset, skipped.viewport.TotalLineCount(), skipped.viewport.Height)
	}
}

// READING IT TO THE END IS WHAT ALLOWS APPLYING IT.
func TestReadingAnEditToItsEndAllowsApplyingIt(t *testing.T) {
	m, root := longEditReview(t)

	sawLast := false
	for i := 0; i < 100 && m.approvalUnread() > 0; i++ {
		m = pressKey(t, m, tea.KeyPgDown)
		sawLast = sawLast || strings.Contains(ansi.Strip(m.View()), "LAST-LINE")
	}
	if m.approvalUnread() != 0 || !sawLast {
		t.Fatalf("paging down did not show the whole edit: %d line(s) unread, last line seen = %v", m.approvalUnread(), sawLast)
	}
	if screen := ansi.Strip(m.View()); !strings.Contains(screen, "y apply") {
		t.Errorf("the keys are not offered once the edit has been read:\n%s", screen)
	}

	m = pressRune(t, m, 'y')
	if m.state != stateIdle {
		t.Fatalf("y did not apply the edit after it had all been shown; state = %v", m.state)
	}
	if got := readFileString(t, filepath.Join(root, "foo.go")); !strings.Contains(got, "LAST-LINE") {
		t.Errorf("foo.go does not hold the edit:\n%s", got)
	}
}
