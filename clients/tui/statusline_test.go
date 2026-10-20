package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"mochiii/protocol"
)

// statusText is the line under the prompt, as a reader sees it.
func statusText(m chatModel) string { return ansi.Strip(m.statusLine()) }

// THE REPORT THIS ANSWERS (OWNER, 2026-10-12): nothing on the chat screen said
// which model or provider was in use. One line under the prompt now does, from
// the moment the daemon answers.
//
// Neuter check: return help unchanged from withStatusLine -- the line is not
// drawn and this fails on its first assertion.
func TestTheLineUnderThePromptNamesTheModelAndProvider(t *testing.T) {
	m := connectedModel(t, nvidiaInForce(t))
	if got := statusText(m); got != "z-ai/glm-5.3 · NVIDIA" {
		t.Errorf("the line reads %q, want the model and the provider", got)
	}
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	last := len(lines) - 1
	if lines[last] != helpText || !strings.HasPrefix(lines[last-1], "z-ai/glm-5.3 · NVIDIA") {
		t.Errorf("the line is not directly above the help line:\n%s", strings.Join(lines[max(0, last-3):], "\n"))
	}

	// Once the session has been billed, what it cost is on the line too.
	m.sessionUsage = usageTotals{turns: 1, prompt: 3500, completion: 343, cost: 0.0027}
	if got := statusText(m); got != "z-ai/glm-5.3 · NVIDIA · 3.8K tokens · $0.0027" {
		t.Errorf("after a turn the line reads %q", got)
	}
	// A provider that reports no cost is not shown as free.
	m.sessionUsage = usageTotals{turns: 1, prompt: 3500, completion: 343, costMissing: true}
	if got := statusText(m); strings.Contains(got, "$") {
		t.Errorf("a cost nobody reported is on the line: %q", got)
	}
}

// What the line says in each state of the connection -- and, as much, what it
// does not claim.
func TestTheLineSaysOnlyWhatIsKnown(t *testing.T) {
	// Before the daemon has answered: nothing, not a guess.
	if got := statusText(newTestModel()); got != "" {
		t.Errorf("before any answer the line reads %q", got)
	}
	// The daemon could not be asked: no provider is guessed at.
	failed, _ := newTestModel().Update(connInfoMsg{err: errors.New("daemon not found")})
	if got := statusText(failed.(chatModel)); got != "" {
		t.Errorf("with the daemon unreachable the line reads %q", got)
	}
	// No key in force.
	none := nvidiaInForce(t)
	none.KeySource, none.InUse, none.ActiveMaskedKey = protocol.KeySourceNone, false, ""
	if got := statusText(connectedModel(t, none)); got != statusNoProvider {
		t.Errorf("with no key the line reads %q, want %q", got, statusNoProvider)
	}
	// An older daemon whose stored key is not the one in use: this client
	// cannot tell what is, and says nothing rather than "no provider".
	older := protocol.ConnectResponse{Ok: true, Outcome: protocol.ConnectShown, EnvOverride: true}
	if got := statusText(connectedModel(t, older)); got != "" {
		t.Errorf("an older daemon with an environment key: the line reads %q", got)
	}
	// An older daemon with its stored key in use: the provider, and no model.
	older.InUse, older.EnvOverride, older.APIBase = true, false, mustProvider(t, "groq").APIBase
	if got := statusText(connectedModel(t, older)); got != "Groq" {
		t.Errorf("an older daemon with a stored key: the line reads %q, want Groq", got)
	}
	// A tier's name stands in for a model id only when there is no id.
	tiered := nvidiaInForce(t)
	tiered.Model, tiered.ModelSlug = "fast", "anthropic/claude-haiku-4.5"
	if got := statusText(connectedModel(t, tiered)); !strings.HasPrefix(got, "anthropic/claude-haiku-4.5 · ") {
		t.Errorf("a tier is shown by its name, not the provider's id: %q", got)
	}
	// This client's own choice is what its prompts name.
	chosen := connectedModel(t, nvidiaInForce(t))
	chosen.preferredTier, chosen.preferredSlug = "kimi", "moonshotai/kimi-k3"
	if got := statusText(chosen); !strings.HasPrefix(got, "moonshotai/kimi-k3 · NVIDIA") {
		t.Errorf("a model chosen here is not the one shown: %q", got)
	}
}

// THE FRAME IS EXACTLY AS TALL AS THE TERMINAL, with the line and without it.
// The viewport is sized when the terminal is and the frame is drawn later, so
// the two must agree about whether the row exists: one row out and either the
// top of the transcript scrolls off or the bottom of the screen is blank.
func TestTheLineNeverMakesTheScreenTallerThanTheTerminal(t *testing.T) {
	for _, size := range []struct{ w, h int }{{120, 50}, {80, 24}, {60, 16}, {40, 10}, {40, statusLineMinHeight}, {40, statusLineMinHeight - 1}, {30, 5}} {
		m := connectedModel(t, nvidiaInForce(t))
		m.sessionUsage = usageTotals{turns: 3, prompt: 120000, completion: 9000, cost: 1.25}
		for i := 0; i < 60; i++ {
			m.appendTurn(turn{role: roleAssistant, text: "a line of an answer long enough to wrap in a narrow terminal, again and again"})
		}
		updated, _ := m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		m = updated.(chatModel)
		lines := strings.Split(m.View(), "\n")
		if len(lines) != size.h {
			t.Errorf("at %dx%d the screen is %d lines tall", size.w, size.h, len(lines))
		}
		// The line itself is cut to the terminal. (The help line under it is
		// not, and never was: it is a fixed 73 columns.)
		if w := lipgloss.Width(m.statusLine()); w > size.w {
			t.Errorf("at %dx%d the line is %d columns wide: %q", size.w, size.h, w, statusText(m))
		}
		shown := strings.Contains(ansi.Strip(m.View()), "NVIDIA")
		if want := size.h >= statusLineMinHeight; shown != want {
			t.Errorf("at %dx%d the line shown=%v, want %v", size.w, size.h, shown, want)
		}
	}
}

// The model's and the provider's names come from off this machine, and the
// line is one of the few things drawn outside the transcript.
func TestTheLineSanitizesWhatItWasTold(t *testing.T) {
	hostile := nvidiaInForce(t)
	hostile.Model = "evil\x1b[2Jmodel"
	hostile.ActiveAPIBase = "https://\x1b]0;x\x07host.example/v1"
	m := connectedModel(t, hostile)
	if view := m.View(); strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\x1b]0;") {
		t.Error("an escape sequence from the daemon's answer reached the screen")
	}
}

// THE LINE IS KEPT TRUE: asked at start, and again after anything that can
// change the answer -- a model chosen, a key connected.
func TestWhatIsInForceIsAskedAtStartAndAfterAChange(t *testing.T) {
	daemon := startRequestRecorder(t, []string{protocol.FeatureModelChoice}, func(json.RawMessage) any {
		return nvidiaInForce(t)
	})
	showsSoFar := func() int {
		n := 0
		for _, req := range daemon.connectRequests(t) {
			if req.Show {
				n++
			}
		}
		return n
	}

	m := newTestModel()
	if _, ok := findMsg[connInfoMsg](m.Init()); !ok || showsSoFar() != 1 {
		t.Fatalf("starting the client did not ask what is in force (%d show requests)", showsSoFar())
	}

	// A model choice answered by the daemon.
	_, cmd := m.Update(modelChoiceMsg{name: "x", resp: protocol.ConnectResponse{Ok: true, ModelSaved: true, Model: "x"}})
	if _, ok := findMsg[connInfoMsg](cmd); !ok || showsSoFar() != 2 {
		t.Errorf("a model choice did not ask again (%d show requests)", showsSoFar())
	}

	// A key the daemon took into use.
	_, cmd = m.Update(connectResultMsg{resp: protocol.ConnectResponse{
		Ok: true, Outcome: protocol.ConnectAccepted, InUse: true, APIBase: mustProvider(t, "groq").APIBase}})
	if _, ok := findMsg[connInfoMsg](cmd); !ok || showsSoFar() != 3 {
		t.Errorf("a connected key did not ask again (%d show requests)", showsSoFar())
	}

	// And the answer is what the line then reads.
	answer, _ := findMsg[connInfoMsg](refreshConnCmd(m.clientName))
	updated, _ := m.Update(answer)
	if got := statusText(updated.(chatModel)); got != "z-ai/glm-5.3 · NVIDIA" {
		t.Errorf("after the answer the line reads %q", got)
	}
}

// providerBrief is the fewest words that are still true.
func TestProviderBrief(t *testing.T) {
	for base, want := range map[string]string{
		mustProvider(t, "openrouter").APIBase: "OpenRouter",
		"http://localhost:11434/v1":           "this machine",
		"http://127.0.0.1:8080/v1":            "this machine",
		"https://llm.corp.example/v1":         "llm.corp.example",
	} {
		if got := providerBrief(base); got != want {
			t.Errorf("providerBrief(%q) = %q, want %q", base, got, want)
		}
	}
}

// EVERY PART OF THE LINE IS DRAWN IN ITS OWN COLOUR. Styling the joined line as
// one string looked right in the source and drew the provider's name in the
// terminal's default white: the model's colour ends in a reset, and nothing
// after it was grey any more (seen in the real app, 2026-10-12).
//
// Neuter check: join the plain parts and wrap the whole in statusLineStyle --
// "NVIDIA" is then preceded by a reset, not by the line's colour.
func TestEveryPartOfTheLineIsDrawnInItsColour(t *testing.T) {
	m := connectedModel(t, nvidiaInForce(t))
	m.sessionUsage = usageTotals{turns: 1, prompt: 3500, completion: 343, cost: 0.0027}
	var line string
	withColorProfile(t, termenv.ANSI256, func() { line = m.statusLine() })

	const grey, steel = "\x1b[38;5;243m", "\x1b[38;5;110m"
	if !strings.Contains(line, steel+"z-ai/glm-5.3") {
		t.Errorf("the model is not in its own colour: %q", line)
	}
	for _, part := range []string{" · ", "NVIDIA", "3.8K tokens", "$0.0027"} {
		if !strings.Contains(line, grey+part) {
			t.Errorf("%q is not drawn in the line's grey: %q", part, line)
		}
	}
}
