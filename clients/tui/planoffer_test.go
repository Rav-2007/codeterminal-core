package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// PLAN MODE OFFERS NO EDITS IN THE TERMINAL APP, THE WAY IT OFFERS NONE IN
// VS CODE.
//
// The daemon enforces plan mode's "no edits": it withholds every edit block it
// parsed out of the answer and says so in EditRejections. The terminal app
// never read that field, and with no proposals to use it took its local
// fallback -- re-parsing the answer text -- and opened a review in which "y"
// wrote the file (found 2026-11-10, backlog item 5).
//
// These run a whole turn through Update over a fake daemon that answers as the
// real one does in plan mode: the answer text WITH the edit block, no
// proposals, and the rejection.

const planOffered = "Plan:\n1. Change the package comment.\n\n"
const planWithheld = "plan mode: withheld a proposed edit to doc.go. This mode does not change files; re-send without /plan to propose it."

// planWorkspace is a real folder holding doc.go, and a model rooted in it, on a
// clock the test moves: a question's answer key is ignored until it has been on
// screen a moment (promptguard.go), so a y that should apply has to come after.
func planWorkspace(t *testing.T) (chatModel, string, *fakeClock) {
	t.Helper()
	root := realTempDir(t)
	doc := filepath.Join(root, "doc.go")
	if err := os.WriteFile(doc, []byte("// Package x does a thing.\npackage x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := newFakeClock()
	m := newTestModelWithRoot(root)
	m.clock = c.now
	return m, doc, c
}

func docEdit() string {
	return editBlockText("doc.go", "// Package x does a thing.", "// Package x does a different thing.")
}

// pressTheKey is one deliberate keypress, a long moment after whatever was on
// screen: the kind that answers a review.
func pressTheKey(m chatModel, c *fakeClock, r rune) chatModel {
	c.advance(time.Hour)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return updated.(chatModel)
}

func systemLines(m chatModel) []string {
	var out []string
	for _, tn := range m.turns {
		if tn.role == roleSystem {
			out = append(out, tn.text)
		}
	}
	return out
}

// A plan turn whose answer carries an edit block opens no review, says the
// daemon's reason, and cannot change the file -- "y" included.
//
// Neuter check: delete the isPlanMode guard in checkForEditBlocks and the turn
// ends in stateEditReview, and "y" rewrites doc.go.
func TestAPlanTurnOpensNoReviewAndCannotWriteTheFile(t *testing.T) {
	fakeDaemonReplying(t,
		protocol.TokenResponse{Token: planOffered + docEdit() + "\n"},
		protocol.TokenResponse{Done: true, EditRejections: []protocol.EditRejectionWire{{Reason: planWithheld}}},
	)
	m, doc, clk := planWorkspace(t)
	before := readFileString(t, doc)

	m, cmd := pressEnter(typeText(m, "/plan change the package comment"))
	m = driveTurn(t, m, cmd)

	if m.state == stateEditReview || len(m.reviewBlocks) != 0 {
		t.Fatalf("a plan turn opened a review: state %v, %d block(s)", m.state, len(m.reviewBlocks))
	}
	if m.state != stateIdle {
		t.Errorf("state %v, want a ready prompt", m.state)
	}
	if lines := systemLines(m); len(lines) != 1 || !strings.Contains(lines[0], planWithheld) {
		t.Errorf("the notice lines = %q, want exactly one holding the daemon's reason", lines)
	}
	if v := m.View(); !strings.Contains(v, "withheld a proposed edit to doc.go") {
		t.Errorf("the notice is not on screen:\n%s", v)
	}
	for _, key := range []rune{'y', 'y', 'q'} {
		m = pressTheKey(m, clk, key)
	}
	if got := readFileString(t, doc); got != before {
		t.Errorf("doc.go changed after y:\n%s", got)
	}
}

// The notice is shown, not stored, not sent: a system turn, which buildHistory
// drops, so the next request carries the plan as the model wrote it and nothing
// of ours.
//
// Neuter check: make noteRejections append a roleAssistant turn and the history
// carries the notice.
func TestThePlanNoticeIsNeverSentToTheModel(t *testing.T) {
	fakeDaemonReplying(t,
		protocol.TokenResponse{Token: planOffered + docEdit() + "\n"},
		protocol.TokenResponse{Done: true, EditRejections: []protocol.EditRejectionWire{{Reason: planWithheld}}},
	)
	m, _, _ := planWorkspace(t)
	m, cmd := pressEnter(typeText(m, "/plan change the package comment"))
	m = driveTurn(t, m, cmd)

	for _, h := range buildHistory(m.turns) {
		if strings.Contains(h.Content, "withheld") || strings.Contains(h.Content, "edits:") {
			t.Errorf("the history carries our notice: %+v", h)
		}
	}
	h := buildHistory(m.turns)
	if len(h) != 2 || h[1].Role != "assistant" || !strings.Contains(h[1].Content, "<<<<<<< SEARCH") {
		t.Errorf("history = %+v, want the user turn and the model's answer unchanged", h)
	}
}

// The guard does not wait for the daemon to say so: a plan turn whose Done
// carries no rejection (an older daemon, or an interrupted turn) still opens no
// review, because the REQUEST was a plan request.
//
// Neuter check: key the guard on m.gotDaemonProposals or on a rejection being
// present and this opens a review.
func TestAPlanTurnOpensNoReviewEvenWithoutARejection(t *testing.T) {
	fakeDaemonReplying(t,
		protocol.TokenResponse{Token: planOffered + docEdit() + "\n"},
		protocol.TokenResponse{Done: true},
	)
	m, doc, clk := planWorkspace(t)
	before := readFileString(t, doc)
	m, cmd := pressEnter(typeText(m, "/plan change the package comment"))
	m = driveTurn(t, m, cmd)

	if m.state == stateEditReview {
		t.Fatalf("a plan turn opened a review without a rejection")
	}
	m = pressTheKey(m, clk, 'y')
	if got := readFileString(t, doc); got != before {
		t.Errorf("doc.go changed: %s", got)
	}
}

// "Sent in plan mode" is the request's, not the screen's: the turn after a plan
// turn is judged by its own request. A plain question whose answer carries an
// edit block is offered as ever, and "y" applies it.
//
// Neuter check: judge the turn by whether the chat ever held a /plan, instead
// of by the request it sent, and the second turn is refused its review.
func TestTheTurnAfterAPlanTurnIsOfferedItsEditsAsBefore(t *testing.T) {
	fakeDaemonReplying(t,
		protocol.TokenResponse{Token: planOffered + docEdit() + "\n"},
		protocol.TokenResponse{Done: true, EditRejections: []protocol.EditRejectionWire{{Reason: planWithheld}}},
	)
	m, doc, clk := planWorkspace(t)
	m, cmd := pressEnter(typeText(m, "/plan change the package comment"))
	m = driveTurn(t, m, cmd)
	if m.state == stateEditReview {
		t.Fatal("the plan turn opened a review")
	}

	fakeDaemonReplying(t,
		protocol.TokenResponse{Token: "Here is the change:\n" + docEdit() + "\n"},
		protocol.TokenResponse{Done: true},
	)
	m, cmd = pressEnter(typeText(m, "now make the change"))
	m = driveTurn(t, m, cmd)
	if m.turnMode != "" {
		t.Fatalf("turnMode = %q after an ordinary question", m.turnMode)
	}
	if m.state != stateEditReview || len(m.reviewBlocks) != 1 {
		t.Fatalf("an ordinary turn was not offered its edit: state %v, %d block(s)", m.state, len(m.reviewBlocks))
	}
	m = pressTheKey(m, clk, 'y')
	if got := readFileString(t, doc); !strings.Contains(got, "does a different thing") {
		t.Errorf("y did not apply the offered edit:\n%s", got)
	}
}

// Every other mode keeps the fallback: a turn that is not a plan turn -- an
// ordinary question, or /fix, which sends a mode of its own -- still parses the
// answer text locally when the daemon sent no list.
//
// Neuter check: widen the guard to any non-empty mode and the /fix case opens
// no review; drop the mode test altogether and the ordinary case does not.
func TestAModeThatIsNotPlanStillTakesTheLocalFallback(t *testing.T) {
	for _, raw := range []string{"/explain this package", "/fix the package comment"} {
		t.Run(raw, func(t *testing.T) {
			fakeDaemonReplying(t,
				protocol.TokenResponse{Token: "Fixed:\n" + docEdit() + "\n"},
				protocol.TokenResponse{Done: true},
			)
			m, _, _ := planWorkspace(t)
			m, cmd := pressEnter(typeText(m, raw))
			m = driveTurn(t, m, cmd)
			if m.state != stateEditReview || len(m.reviewBlocks) != 1 {
				t.Fatalf("the fallback did not run: state %v, %d block(s), turnMode %q", m.state, len(m.reviewBlocks), m.turnMode)
			}
		})
	}
}

// Rejections are said in any mode, as VS Code says them: a daemon that sends a
// list AND a refused block shows the refusal above the review of the rest.
//
// Neuter check: drop the editRejectionsMsg delivery in stream.go and no notice
// appears.
func TestARejectionAlongsideProposalsIsShownAboveTheReview(t *testing.T) {
	fakeDaemonReplying(t,
		protocol.TokenResponse{Token: "Two edits, one unreadable.\n"},
		protocol.TokenResponse{Done: true,
			EditProposals:  []protocol.EditBlockWire{{FilePath: "doc.go", Search: "// Package x does a thing.", Replace: "// Package x does a different thing."}},
			EditRejections: []protocol.EditRejectionWire{{Line: 7, Reason: "no closing marker"}}},
	)
	m, _, _ := planWorkspace(t)
	m, cmd := pressEnter(typeText(m, "/explain this package"))
	m = driveTurn(t, m, cmd)

	if m.state != stateEditReview {
		t.Fatalf("the readable block was not offered: state %v", m.state)
	}
	lines := systemLines(m)
	if len(lines) == 0 || lines[0] != "(edit block at line 7 refused: no closing marker)" {
		t.Errorf("the refusal notice = %q", lines)
	}
}

// The reason is the daemon's text and holds a file name the model chose: control
// bytes in it do not reach the terminal.
func TestARejectionNoticeIsSanitised(t *testing.T) {
	got := rejectionNote(protocol.EditRejectionWire{Reason: "withheld \x1b[2Jdoc.go\x07"})
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("control bytes survived: %q", got)
	}
}
