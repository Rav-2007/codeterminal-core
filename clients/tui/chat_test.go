package main

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func newTestModel() chatModel {
	m := newChatModel("test-client")
	// Drive past the splash and give the model a size, exactly as the real
	// runtime would via its initial WindowSizeMsg, so viewport/input are
	// usable in assertions below.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(chatModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}) // dismiss splash
	m = updated.(chatModel)
	return m
}

func typeText(m chatModel, s string) chatModel {
	for _, r := range s {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(chatModel)
	}
	return m
}

func pressEnter(m chatModel) (chatModel, tea.Cmd) {
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return updated.(chatModel), cmd
}

func TestChat_SplashDismissedByAnyKey(t *testing.T) {
	m := newChatModel("test-client")
	if m.state != stateSplash {
		t.Fatalf("state = %v, want stateSplash before any key", m.state)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	m = updated.(chatModel)
	if m.state != stateIdle {
		t.Errorf("state = %v, want stateIdle after a keypress dismisses the splash", m.state)
	}
}

func TestChat_EnterOnEmptyInputDoesNothing(t *testing.T) {
	m := newTestModel()
	before := len(m.turns)

	m, _ = pressEnter(m)

	if len(m.turns) != before {
		t.Errorf("turns = %d, want unchanged %d after Enter on empty input", len(m.turns), before)
	}
	if m.state != stateIdle {
		t.Errorf("state = %v, want stateIdle to remain unchanged", m.state)
	}
}

func TestChat_EnterWithTextStartsSending(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hello")

	m, cmd := pressEnter(m)

	if m.state != stateSending {
		t.Fatalf("state = %v, want stateSending", m.state)
	}
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd (spinner tick + stream start) after Enter")
	}
	if len(m.turns) != 1 || m.turns[0].role != roleUser || m.turns[0].text != "hello" {
		t.Errorf("turns = %+v, want a single user turn with text %q", m.turns, "hello")
	}
	if m.input.Value() != "" {
		t.Errorf("input value = %q, want cleared after send", m.input.Value())
	}
	if m.input.Focused() {
		t.Error("input should be blurred (disabled) while a request is in flight")
	}
	if m.streamCancel == nil {
		t.Error("streamCancel should be set once a stream starts")
	}
}

func TestChat_TokenMsgAppendsToTranscriptAndStartsStreaming(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	updated, cmd := m.Update(tokenMsg("Hel"))
	m = updated.(chatModel)
	if m.state != stateStreaming {
		t.Fatalf("state = %v, want stateStreaming after the first token", m.state)
	}
	if cmd == nil {
		t.Error("expected waitForNext to be re-issued after a token")
	}
	if len(m.turns) != 2 || m.turns[1].role != roleAssistant || m.turns[1].text != "Hel" {
		t.Fatalf("turns = %+v, want a second assistant turn with text %q", m.turns, "Hel")
	}

	updated, _ = m.Update(tokenMsg("lo"))
	m = updated.(chatModel)
	if m.turns[1].text != "Hello" {
		t.Errorf("assistant turn text = %q, want accumulated %q", m.turns[1].text, "Hello")
	}
}

func TestChat_StreamDoneReenablesInput(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)
	updated, _ := m.Update(tokenMsg("hi there"))
	m = updated.(chatModel)

	updated, cmd := m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	if m.state != stateIdle {
		t.Errorf("state = %v, want stateIdle after streamDoneMsg", m.state)
	}
	if m.streamCancel != nil {
		t.Error("streamCancel should be cleared once the stream is done")
	}
	if m.streamCh != nil {
		t.Error("streamCh should be cleared once the stream is done")
	}
	if cmd == nil {
		t.Error("expected a Cmd to re-focus the input (Focus() returns a blink Cmd)")
	}
}

func TestChat_StreamErrShowsErrorStateAndReenablesInput(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	updated, _ := m.Update(streamErrMsg{errors.New("boom")})
	m = updated.(chatModel)

	if m.state != stateError {
		t.Fatalf("state = %v, want stateError", m.state)
	}
	if m.statusErr != "boom" {
		t.Errorf("statusErr = %q, want %q", m.statusErr, "boom")
	}
	if m.streamCancel != nil {
		t.Error("streamCancel should be cleared after an error")
	}
}

func TestChat_EnterAfterErrorStartsANewTurn(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "first")
	m, _ = pressEnter(m)
	updated, _ := m.Update(streamErrMsg{errors.New("boom")})
	m = updated.(chatModel)

	m = typeText(m, "second")
	m, cmd := pressEnter(m)

	if m.state != stateSending {
		t.Fatalf("state = %v, want stateSending; Enter after an error should be able to start a new turn", m.state)
	}
	if cmd == nil {
		t.Fatal("expected a Cmd starting the new stream")
	}
	if len(m.turns) != 2 || m.turns[1].text != "second" {
		t.Errorf("turns = %+v, want the second prompt appended", m.turns)
	}
}

func TestChat_EnterWhileSendingOrStreamingIsANoOp(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "first")
	m, _ = pressEnter(m)
	if m.state != stateSending {
		t.Fatalf("precondition failed: state = %v, want stateSending", m.state)
	}

	before := len(m.turns)
	m = typeText(m, "second") // typing while sending should also be ignored (input is blurred)
	m, cmd := pressEnter(m)

	if len(m.turns) != before {
		t.Errorf("turns = %d, want unchanged %d — Enter while busy must be a no-op", len(m.turns), before)
	}
	if cmd != nil {
		t.Error("expected a nil Cmd for a no-op Enter while busy")
	}
}

// TestChat_CtrlCCancelsAndQuits is the mid-stream-quit safety net: it
// confirms Ctrl-C actually invokes streamCancel (not just that it returns
// tea.Quit), which is the mechanism that unblocks streamPrompt's blocked
// read (see TestStreamPrompt_ContextCancelUnblocksBlockedRead in
// stream_test.go for the full end-to-end proof).
func TestChat_CtrlCCancelsAndQuits(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	ctx, cancel := context.WithCancel(context.Background())
	m.streamCancel = cancel

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(chatModel)

	if ctx.Err() == nil {
		t.Error("expected streamCancel to have been called (context should be Done)")
	}
	if cmd == nil {
		t.Fatal("expected a non-nil Cmd (tea.Quit)")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("expected the returned Cmd to produce a tea.QuitMsg")
	}
}

func TestChat_TokenAfterStreamAbandonedIsIgnored(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)
	updated, _ := m.Update(streamDoneMsg{}) // stream ends; streamCh cleared
	m = updated.(chatModel)

	before := len(m.turns)
	updated, cmd := m.Update(tokenMsg("stray"))
	m = updated.(chatModel)

	if len(m.turns) != before {
		t.Errorf("turns = %+v, want unchanged after a stray token from an abandoned stream", m.turns)
	}
	if cmd != nil {
		t.Error("expected a nil Cmd for a stray token (no waitForNext re-issued)")
	}
}
