package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"mochiii/protocol"
)

// statusAndChoiceDaemon answers a status request with tiers and a connect
// request with resp -- the two things /model <name> asks a daemon.
func statusAndChoiceDaemon(t *testing.T, features []string, resp protocol.ConnectResponse) *recordingDaemon {
	t.Helper()
	return startRequestRecorder(t, features, func(raw json.RawMessage) any {
		var probe struct {
			Status bool `json:"status"`
		}
		_ = json.Unmarshal(raw, &probe)
		if probe.Status {
			return protocol.StatusResponse{AvailableTiers: tierFixture()}
		}
		return resp
	})
}

// choiceRequests is the connect requests among what a daemon recorded.
func choiceRequests(t *testing.T, d *recordingDaemon) []protocol.ConnectRequest {
	t.Helper()
	var out []protocol.ConnectRequest
	for _, req := range d.connectRequests(t) {
		if req.Connect {
			out = append(out, req)
		}
	}
	return out
}

// /model <name> USED TO LAST UNTIL THE WINDOW CLOSED. It now also asks the daemon
// to keep the choice, and says "remembered" only when the daemon saved it.
//
// Neuter check: return `m, nil` from chooseModel instead of chooseModelCmd.
func TestModelCommandAsksTheDaemonToRememberTheChoice(t *testing.T) {
	daemon := statusAndChoiceDaemon(t, []string{protocol.FeatureModelChoice},
		protocol.ConnectResponse{Ok: true, Outcome: protocol.ConnectModelChosen, Model: "fast", ModelSaved: true})

	got, cmd := newTestModel().handleModelCommand("fast")
	m := got.(chatModel)
	if m.preferredTier != "fast" {
		t.Fatalf("preferredTier = %q, want fast", m.preferredTier)
	}
	answer, ok := findMsg[modelChoiceMsg](cmd)
	if !ok {
		t.Fatal("/model fast sent the daemon nothing")
	}
	reqs := choiceRequests(t, daemon)
	if len(reqs) != 1 || reqs[0].ChooseModel != "fast" || reqs[0].ClearModel || reqs[0].APIKey != "" {
		t.Fatalf("the daemon was sent %d choice request(s); want one for fast", len(reqs))
	}
	updated, _ := m.Update(answer)
	if last := lastTurn(t, updated); !strings.Contains(last, "Remembered") || !strings.Contains(last, "fast") {
		t.Errorf("a saved choice was not reported as remembered: %q", last)
	}
}

// /model clear drops what the daemon was keeping, too -- otherwise the next
// start would bring back a choice the user had just cleared.
func TestModelClearAsksTheDaemonToForget(t *testing.T) {
	daemon := statusAndChoiceDaemon(t, []string{protocol.FeatureModelChoice},
		protocol.ConnectResponse{Ok: true, Outcome: protocol.ConnectModelChosen, Model: "primary"})

	m := newTestModel()
	m.preferredTier = "fast"
	got, cmd := m.handleModelCommand("clear")
	answer, ok := findMsg[modelChoiceMsg](cmd)
	if !ok {
		t.Fatal("/model clear sent the daemon nothing")
	}
	reqs := choiceRequests(t, daemon)
	if len(reqs) != 1 || !reqs[0].ClearModel || reqs[0].ChooseModel != "" {
		t.Fatalf("the daemon was sent %d request(s); want one clearing the choice", len(reqs))
	}
	updated, _ := got.(chatModel).Update(answer)
	if last := lastTurn(t, updated); !strings.Contains(last, "primary") || !strings.Contains(last, "before one was chosen") {
		t.Errorf("clearing does not say which model prompts go back to: %q", last)
	}
}

// A DAEMON STARTED BEFORE THIS EXISTED would read the request as "connect with
// no key" and answer with an error about keys. It is not sent one; the choice
// holds for the session and the line says how to get it remembered.
//
// Neuter check: drop the hasFeature test in sendChooseModel.
func TestAnOlderDaemonIsNotSentAModelChoice(t *testing.T) {
	daemon := statusAndChoiceDaemon(t, nil, protocol.ConnectResponse{Error: "the key is empty; nothing was saved"})

	got, cmd := newTestModel().handleModelCommand("fast")
	m := got.(chatModel)
	answer, ok := findMsg[modelChoiceMsg](cmd)
	if !ok {
		t.Fatal("no answer came back")
	}
	if !errors.Is(answer.err, errDaemonPredatesModelChoice) {
		t.Fatalf("err = %v, want errDaemonPredatesModelChoice", answer.err)
	}
	if n := len(choiceRequests(t, daemon)); n != 0 {
		t.Errorf("%d choice request(s) reached a daemon that cannot read one", n)
	}
	updated, _ := m.Update(answer)
	cm := updated.(chatModel)
	if cm.preferredTier != "fast" {
		t.Error("the choice did not hold for this session")
	}
	if last := lastTurn(t, updated); !strings.Contains(last, "this session only") || !strings.Contains(last, "Restart") {
		t.Errorf("the line does not say the choice is temporary and why: %q", last)
	}
}

// "REMEMBERED" IS SAID ONLY WHEN IT WAS SAVED, and each way it was not says why.
func TestModelChoiceLineWordsEachOutcomeDistinctly(t *testing.T) {
	chosen := func(resp protocol.ConnectResponse, err error) string {
		return modelChoiceLine(modelChoiceMsg{name: "fast", resp: resp, err: err})
	}
	saved := chosen(protocol.ConnectResponse{Ok: true, Model: "fast", ModelSaved: true}, nil)
	if !strings.Contains(saved, "Remembered") {
		t.Errorf("saved: %q", saved)
	}
	unsaved := chosen(protocol.ConnectResponse{Ok: true, Model: "fast",
		Detail: "kept until this daemon stops: its key comes from the environment, so there is no stored key to save the choice beside"}, nil)
	if strings.Contains(unsaved, "Remembered") || !strings.Contains(unsaved, "Kept until this daemon stops") || !strings.Contains(unsaved, "environment") {
		t.Errorf("not saved: %q", unsaved)
	}
	refused := chosen(protocol.ConnectResponse{Error: `no model named "fast" is offered here`}, nil)
	if strings.Contains(refused, "Remembered") || !strings.Contains(refused, "did not take that") {
		t.Errorf("refused: %q", refused)
	}
	unreachable := chosen(protocol.ConnectResponse{}, errors.New("daemon not found"))
	if strings.Contains(unreachable, "Remembered") || !strings.Contains(unreachable, "this session only") {
		t.Errorf("unreachable: %q", unreachable)
	}
	// Forgetting on an older daemon has nothing to undo, so it says nothing.
	if got := modelChoiceLine(modelChoiceMsg{err: errDaemonPredatesModelChoice}); got != "" {
		t.Errorf("clearing against an older daemon said %q", got)
	}
	// What the daemon sends is drawn on a terminal: no escapes get through.
	hostile := chosen(protocol.ConnectResponse{Ok: true, Model: "fast\x1b[2J", ModelSaved: true}, nil)
	if strings.Contains(hostile, "\x1b") {
		t.Errorf("a control sequence from the daemon reached the line: %q", hostile)
	}
}
