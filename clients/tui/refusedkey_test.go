package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// THE REPORTED GAP. A key hit its spending limit, so every prompt failed. The
// user typed "/conn", saw "/connect" highlighted in the popup, pressed Enter --
// and got the same failure again, because after a failed turn the popup drew but
// Enter, Tab and the arrows ignored it: "/conn" went to the model as a question,
// on the dead key. With a dead key there is never a next success to leave that
// state, so the way to a new key was closed for as long as the key was dead.

const refusedDetail = "the provider rejected the API key — connect a working one with /connect"

// askAndFail submits a question and fails it the way the daemon does.
func askAndFail(t *testing.T, question string, fail streamErrMsg) chatModel {
	t.Helper()
	m := typeText(newTestModel(), question)
	m, _ = pressEnter(m)
	updated, _ := m.Update(fail)
	return updated.(chatModel)
}

var spentKey = streamErrMsg{err: errors.New(refusedDetail), sent: true, class: "quota_exceeded"}

func TestTheSlashPopupStillWorksAfterAFailedTurn(t *testing.T) {
	m := askAndFail(t, "pusha 3 release date", spentKey)
	if m.state != stateError {
		t.Fatalf("state = %v, want stateError -- the precondition of the report", m.state)
	}

	m = typeText(m, "/conn")
	if popup, _ := m.renderSlashPopup(); !strings.Contains(popup, "/connect") {
		t.Fatalf("the popup does not offer /connect:\n%s", popup)
	}
	m, _ = pressEnter(m)

	if m.state != stateConnect {
		t.Errorf("Enter on \"/conn\" with /connect highlighted did not run /connect (state %v)", m.state)
	}
	for _, tn := range m.turns {
		if tn.role == roleUser && tn.text == "/conn" {
			t.Error("\"/conn\" was sent to the model as a question")
		}
	}
}

func TestTabAndHistoryStillWorkAfterAFailedTurn(t *testing.T) {
	m := askAndFail(t, "first question", spentKey)

	m = typeText(m, "/he")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(chatModel)
	if got := m.input.Value(); got != "/help " {
		t.Errorf("Tab after a failed turn left %q, want \"/help \"", got)
	}

	m.input.SetValue("")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(chatModel)
	if got := m.input.Value(); got != "first question" {
		t.Errorf("up after a failed turn recalled %q, want the question that failed", got)
	}
}

// Better than making /connect reachable: offering it. A refused key is the same
// moment of need as a first question with no key, so it gets the same answer --
// the masked prompt, with the question held and asked again once a key works.
func TestARefusedKeyOpensTheKeyPromptAndHoldsTheQuestion(t *testing.T) {
	for _, class := range []string{"auth", "quota_exceeded"} {
		t.Run(class, func(t *testing.T) {
			const question = "pusha 3 release date"
			m := askAndFail(t, question, streamErrMsg{
				err: errors.New(refusedDetail), sent: true, class: class, keyReplaceable: true,
			})

			if m.state != stateConnect {
				t.Fatalf("a refused key did not open the key prompt (state %v)", m.state)
			}
			if m.input.EchoMode != textinput.EchoPassword {
				t.Error("the key prompt is not masked")
			}
			if m.pendingPrompt != question {
				t.Errorf("the question was not held: %q", m.pendingPrompt)
			}
			// The failure itself is still on record, in the daemon's own words.
			var recorded bool
			for _, tn := range m.turns {
				if tn.role == roleUnanswered && tn.text == refusedDetail {
					recorded = true
				}
			}
			if !recorded {
				t.Error("the refusal was not recorded under the question")
			}

			out, cmd := m.handleConnectResult(connectResultMsg{resp: protocol.ConnectResponse{
				Ok: true, Outcome: protocol.ConnectAccepted, MaskedKey: "...ijkl (24 characters)", InUse: true,
			}})
			got := out.(chatModel)
			if cmd == nil || got.state != stateSending {
				t.Fatalf("an accepted key did not ask the question again (state %v)", got.state)
			}
			var asked int
			for _, tn := range got.turns {
				if tn.role == roleUser && tn.text == question {
					asked++
				}
			}
			if asked != 2 {
				t.Errorf("the question appears %d time(s) as a sent turn, want 2 (the failed one and the retry)", asked)
			}
		})
	}
}

// Esc keeps the key that was in use, and gives the question back.
func TestARefusedKeyPromptCanBeDeclined(t *testing.T) {
	m := askAndFail(t, "what changed?", streamErrMsg{
		err: errors.New(refusedDetail), sent: true, class: "auth", keyReplaceable: true,
	})
	out, _ := m.handleConnectKey(tea.KeyMsg{Type: tea.KeyEsc})
	got := out.(chatModel)
	if got.input.Value() != "what changed?" || got.pendingPrompt != "" {
		t.Errorf("declining lost or kept holding the question: input %q, held %q", got.input.Value(), got.pendingPrompt)
	}
	if got.input.EchoMode != textinput.EchoNormal {
		t.Error("the input was left masked after declining")
	}
}

// When the daemon says a pasted key would not be used -- proxy mode, or a key in
// its environment -- it is not asked for.
func TestAKeyTheDaemonCannotUseIsNotAskedFor(t *testing.T) {
	m := askAndFail(t, "hello", streamErrMsg{err: errors.New(refusedDetail), sent: true, class: "auth"})
	if m.state != stateError {
		t.Errorf("state = %v, want stateError: the key prompt opened although a pasted key could not take effect", m.state)
	}
}

// A turn that already did something is not re-run on the user's behalf. The
// id-less case is the one activityTurns cannot see: a call the model sent with
// no id is never keyed there.
func TestAPartlyDoneTurnIsNotAskedAgain(t *testing.T) {
	for name, callID := range map[string]string{"with a call id": "c1", "with no call id": ""} {
		t.Run(name, func(t *testing.T) {
			m := typeText(newTestModel(), "fix the build")
			m, _ = pressEnter(m)
			updated, _ := m.Update(toolActivityMsg{activity: protocol.ToolActivity{
				CallID: callID, Tool: "read_file", Phase: protocol.ToolPhaseRunning,
			}})
			m = updated.(chatModel)
			updated, _ = m.Update(streamErrMsg{err: errors.New(refusedDetail), sent: true, class: "quota_exceeded", keyReplaceable: true})
			m = updated.(chatModel)

			if m.state != stateConnect {
				t.Fatalf("the key prompt did not open (state %v)", m.state)
			}
			if m.pendingPrompt != "" {
				t.Errorf("a turn that had already called a tool is held to be re-run: %q", m.pendingPrompt)
			}
		})
	}
}

// A pipeline phase marker is narration, not an action: a turn that got no
// further than naming its first specialist is still asked again.
func TestAPhaseMarkerAloneDoesNotCountAsActing(t *testing.T) {
	m := typeText(newTestModel(), "fix the build")
	m, _ = pressEnter(m)
	updated, _ := m.Update(toolActivityMsg{activity: protocol.ToolActivity{
		Tool: "coder", Phase: protocol.ToolPhaseStep, Detail: "step 1/2",
	}})
	m = updated.(chatModel)
	updated, _ = m.Update(streamErrMsg{err: errors.New(refusedDetail), sent: true, class: "auth", keyReplaceable: true})
	m = updated.(chatModel)
	if m.pendingPrompt != "fix the build" {
		t.Errorf("a turn that only announced a phase was not held: %q", m.pendingPrompt)
	}
}

// "NOTHING WAS SENT" IS A CLAIM ABOUT WHAT LEFT THE MACHINE. It was printed under
// every failure, including a provider refusing a prompt that had plainly gone
// out. It is now said only when it is true.
func TestOnlyAPromptThatNeverLeftIsSaidToHaveNeverLeft(t *testing.T) {
	notSent := askAndFail(t, "q", streamErrMsg{err: errors.New("daemon not found")})
	sent := askAndFail(t, "q", streamErrMsg{err: errors.New(refusedDetail), sent: true, class: "auth"})

	if out := renderTranscript(notSent.turns, 80); !strings.Contains(out, "nothing was sent") {
		t.Errorf("a prompt that never reached the daemon is not reported as unsent:\n%s", out)
	}
	out := renderTranscript(sent.turns, 80)
	if strings.Contains(out, "nothing was sent") || strings.Contains(out, "never reached the daemon") {
		t.Errorf("a prompt the daemon answered with an error is reported as never sent:\n%s", out)
	}
	if !strings.Contains(out, "was sent, but no answer came back") {
		t.Errorf("a prompt the daemon answered with an error carries no footnote:\n%s", out)
	}
	for _, tn := range append(sent.turns, notSent.turns...) {
		if tn.role == roleUnanswered || tn.role == roleSevered {
			for _, h := range buildHistory([]turn{tn}) {
				t.Errorf("a failure notice reached the model's history: %+v", h)
			}
		}
	}
}
