package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

const pickBase = "http://127.0.0.1:11434/v1"

// pickingModel is the app with a local server just connected: its first model
// is in force, and the model list is open on rows.
func pickingModel(t *testing.T, rows ...pickerRow) chatModel {
	t.Helper()
	resetLocalState(t)
	m := newTestModel()
	m.state = stateSetup
	m.setup = setupFlow{step: setupModel, inForce: "qwen-ctx16k:latest", picker: newListPicker(setupModelTitle, rows)}
	next, _ := m.Update(connInfoMsg{resp: protocol.ConnectResponse{Ok: true, Model: "qwen-ctx16k:latest",
		ConnectInForce: protocol.ConnectInForce{KeySource: protocol.KeySourceNone, ActiveAPIBase: pickBase}}})
	return next.(chatModel)
}

func fittedKey() string {
	localState.mu.Lock()
	defer localState.mu.Unlock()
	return localState.fitKey
}

// THE MODEL A SERVER LISTS FIRST IS NOT FITTED WHILE THE LIST IS OPEN. A fit
// loads the model into memory, so connecting to Ollama loaded whichever model
// it listed first -- measured 2026-10-24, 2.5 GB for ten minutes, beside the
// 7B then picked, on a laptop with no swap.
//
// Neuter check: drop the `m.setup.step == setupModel` return from
// afterConnInfo, and the first model is fitted while the list is open.
func TestTheFirstListedModelIsNotFittedWhileTheListIsOpen(t *testing.T) {
	m := pickingModel(t, pickerRow{ID: "qwen2.5-coder:7b", Label: "qwen2.5-coder:7b"},
		pickerRow{ID: "qwen-ctx16k:latest", Label: "qwen-ctx16k:latest"})
	if !localActive() {
		t.Fatal("the local server in force was not noted")
	}
	if key := fittedKey(); key != "" {
		t.Fatalf("fitted %q while the model list was open", key)
	}
	// Picking another model fits that one when its choice lands, not this one.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if key := fittedKey(); key != "" {
		t.Errorf("picking qwen2.5-coder:7b fitted %q", key)
	}
	m = next.(chatModel)
	if m.setup.step != setupNone {
		t.Fatalf("the list is still open after Enter: step %v", m.setup.step)
	}
}

// A LIST CLOSED WITH NO NEW CHOICE FITS THE MODEL IN FORCE, since no new
// connInfo comes to do it: with Esc, and with Enter on the model in force.
//
// Neuter check: make fitAfterPicking return focus alone, and the model in
// force is never fitted.
func TestAListClosedWithNoNewChoiceFitsTheModelInForce(t *testing.T) {
	want := pickBase + "\x00" + "qwen-ctx16k:latest"
	for _, tc := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{"esc", tea.KeyMsg{Type: tea.KeyEsc}},
		{"enter on the model in force", tea.KeyMsg{Type: tea.KeyEnter}},
	} {
		m := pickingModel(t, pickerRow{ID: "qwen-ctx16k:latest", Label: "qwen-ctx16k:latest"})
		if _, cmd := m.Update(tc.key); cmd == nil {
			t.Errorf("%s: nothing was asked for", tc.name)
		}
		if key := fittedKey(); key != want {
			t.Errorf("%s: fitted %q, want the model in force", tc.name, key)
		}
	}
}
