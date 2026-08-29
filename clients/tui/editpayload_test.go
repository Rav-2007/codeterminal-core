package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"codeterminal/protocol"
)

// oneEditBlock is a complete, well-formed block against the file the tests
// write. A .txt target keeps the Go syntax gate out of the way -- these tests
// are about which blocks REACH review, not about what the gates do to them.
func oneEditBlock(path string) string {
	return strings.Join([]string{
		"path: " + path,
		"<<<<<<< SEARCH",
		"old line",
		"=======",
		"new line",
		">>>>>>> REPLACE",
	}, "\n")
}

// reviewReady starts a turn in a real workspace containing one editable file.
func reviewReady(t *testing.T) chatModel {
	t.Helper()
	root := realTempDir(t)
	writeTempFile(t, root, "a.txt", "old line\n")
	m := newTestModelWithRoot(root)
	m = typeText(m, "change it")
	m, _ = pressEnter(m)
	return m
}

// TestChat_InterruptOffersBlocksTheModelFinished is the regression for a bug
// that quietly contradicted the interrupt's own documented rule.
//
// interruptTurn keeps whatever streamed ("deleting it on a keypress would make
// the key frightening to press") but returned straight to an idle prompt --
// so the TEXT of a finished edit block stayed on screen while the EDIT was
// thrown away. Stopping a turn three files in meant re-running the whole turn
// to recover the two that had already finished.
func TestChat_InterruptOffersBlocksTheModelFinished(t *testing.T) {
	m := reviewReady(t)

	// One complete block, then the model is cut off part-way through a second.
	updated, _ := m.Update(tokenMsg(oneEditBlock("a.txt") + "\n\npath: b.txt\n<<<<<<< SEARCH\nhalf a bl"))
	m = updated.(chatModel)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(chatModel)

	if m.state != stateEditReview {
		t.Fatalf("state = %v, want stateEditReview: the completed block is a real edit the user should still be offered", m.state)
	}
	if len(m.reviewBlocks) != 1 {
		t.Fatalf("reviewBlocks = %d, want exactly 1 (the finished block; the half-written one must not be offered)", len(m.reviewBlocks))
	}
	if m.reviewBlocks[0].FilePath != "a.txt" {
		t.Errorf("offered %q, want a.txt", m.reviewBlocks[0].FilePath)
	}
	// The interrupt's existing guarantees must survive the new behaviour.
	if got := lastAssistantText(m.turns); !strings.Contains(got, "old line") {
		t.Error("the streamed text was not kept verbatim")
	}
	if !transcriptContains(m, "you interrupted this turn") {
		t.Error("the stopped notice is missing")
	}
}

// TestChat_InterruptWithNoEditsStillJustStops is the other half: routing an
// interrupt through the review check must not invent a review, or change what
// stopping an ordinary answer does.
func TestChat_InterruptWithNoEditsStillJustStops(t *testing.T) {
	m := reviewReady(t)
	updated, _ := m.Update(tokenMsg("Here is an explanation, no edits proposed."))
	m = updated.(chatModel)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(chatModel)

	if m.state != stateIdle {
		t.Errorf("state = %v, want stateIdle", m.state)
	}
	if len(m.reviewBlocks) != 0 {
		t.Errorf("reviewBlocks = %d, want 0", len(m.reviewBlocks))
	}
}

// TestChat_DaemonProposalsReachReview is the regression for the divergence that
// made daemon/agentturn.go's own comment false in this client.
//
// The daemon merges TWO sources into EditProposals: blocks in the assistant
// text, and edits filed through the propose_edit tool. Only the first is in the
// text. This client parsed the text and ignored the field, so in agent mode
// every propose_edit proposal was invisible -- no review, no diff, no mention.
// The assistant text here deliberately contains NO edit markup, so the only way
// to reach review is by reading the field.
func TestChat_DaemonProposalsReachReview(t *testing.T) {
	m := reviewReady(t)

	updated, _ := m.Update(tokenMsg("I filed that change through the edit tool."))
	m = updated.(chatModel)

	updated, _ = m.Update(editProposalsMsg{[]protocol.EditBlockWire{
		{FilePath: "a.txt", Search: "old line", Replace: "new line"},
	}})
	m = updated.(chatModel)

	if !m.gotDaemonProposals {
		t.Fatal("editProposalsMsg was not recorded")
	}

	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	if m.state != stateEditReview {
		t.Fatalf("state = %v, want stateEditReview: a propose_edit proposal must reach the user as a diff", m.state)
	}
	if len(m.reviewBlocks) != 1 || m.reviewBlocks[0].FilePath != "a.txt" {
		t.Fatalf("reviewBlocks = %+v, want the daemon's single a.txt proposal", m.reviewBlocks)
	}
}

// TestChat_OlderDaemonWithoutProposalsStillReviews pins the fallback. A daemon
// that never sends the field must still get a review from the local parse --
// otherwise adopting the field would trade one silent failure for another.
func TestChat_OlderDaemonWithoutProposalsStillReviews(t *testing.T) {
	m := reviewReady(t)

	updated, _ := m.Update(tokenMsg(oneEditBlock("a.txt")))
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	if m.gotDaemonProposals {
		t.Fatal("precondition: no editProposalsMsg was sent, so the fallback must be what runs")
	}
	if m.state != stateEditReview {
		t.Fatalf("state = %v, want stateEditReview from the local parse", m.state)
	}
	if len(m.reviewBlocks) != 1 {
		t.Fatalf("reviewBlocks = %d, want 1", len(m.reviewBlocks))
	}
}

// TestChat_UnifiedDiffAnswerReachesReview is the Stage 1 headline in the TUI: a
// model that answers with a patch instead of edit blocks is understood, not
// merely named. Before ingestion this response reached an idle prompt, and
// before Stage 0 it did so in complete silence.
func TestChat_UnifiedDiffAnswerReachesReview(t *testing.T) {
	m := reviewReady(t)

	diff := "Here is the change:\n\ndiff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old line\n+new line\n"
	updated, _ := m.Update(tokenMsg(diff))
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	if m.state != stateEditReview {
		t.Fatalf("state = %v, want stateEditReview: a patch is an edit the user should be offered", m.state)
	}
	if len(m.reviewBlocks) != 1 || m.reviewBlocks[0].FilePath != "a.txt" {
		t.Fatalf("reviewBlocks = %+v, want one block for a.txt", m.reviewBlocks)
	}
	if m.reviewBlocks[0].Search != "old line" || m.reviewBlocks[0].Replace != "new line" {
		t.Errorf("block = %+v, want the hunk's minus/plus lines", m.reviewBlocks[0])
	}
}

// TestChat_UnreadableEditPayloadIsReported is the TUI half of the silence fix,
// now covering what is STILL unreadable after ingestion. A diff header with no
// hunk is recognised as edit-shaped and deliberately never parsed — requiring a
// hunk header is what stops a markdown rule being read as a patch — so it must
// reach the user as a notice rather than as an idle prompt.
func TestChat_UnreadableEditPayloadIsReported(t *testing.T) {
	m := reviewReady(t)

	updated, _ := m.Update(tokenMsg("Here you go:\n\ndiff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n"))
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	if m.state != stateIdle {
		t.Errorf("state = %v, want stateIdle (nothing is applicable)", m.state)
	}
	if !transcriptContains(m, "no edits offered") {
		t.Fatalf("the user was told nothing about an unreadable edit payload; transcript:\n%s", transcriptText(m))
	}
	if !transcriptContains(m, "unified diff") {
		t.Errorf("the notice does not name what was seen; transcript:\n%s", transcriptText(m))
	}
}

// TestChat_PlainAnswerIsStillSilent is the false-positive guard. An ordinary
// answer must not acquire a warning about an edit nobody proposed.
func TestChat_PlainAnswerIsStillSilent(t *testing.T) {
	m := reviewReady(t)

	updated, _ := m.Update(tokenMsg("The retrieval budget is 24000 characters.\n\n---\n\nSee the design doc."))
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	if transcriptContains(m, "no edits offered") {
		t.Errorf("fired an unreadable-payload notice on an ordinary answer; transcript:\n%s", transcriptText(m))
	}
	if m.state != stateIdle {
		t.Errorf("state = %v, want stateIdle", m.state)
	}
}

func transcriptText(m chatModel) string {
	var b strings.Builder
	for _, tn := range m.turns {
		b.WriteString(tn.text)
		b.WriteString("\n")
	}
	return b.String()
}

func transcriptContains(m chatModel, want string) bool {
	return strings.Contains(transcriptText(m), want)
}
