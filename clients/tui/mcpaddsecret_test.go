package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"

	"mochiii/protocol"
)

// FOUND 2026-11-17 (custom-MCP test, a real pty run): "/mcp-server add" starts
// the server before it asks for the server's key, and the box was ordinary
// input meanwhile. A key typed in that gap went to the model provider as a chat
// message, and a slash command typed then was stored as the key.

// sendingCleanup resets the add-in-flight state these tests leave behind.
func sendingCleanup(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { addKeys.sending, addKeys.sendingName = false, "" })
}

// rawRequestsContaining counts what the daemon was sent, of any kind (an MCP
// request or a chat prompt), that holds s.
func rawRequestsContaining(d *recordingDaemon, s string) int {
	n := 0
	for _, raw := range d.requests() {
		if strings.Contains(string(raw), s) {
			n++
		}
	}
	return n
}

// A key typed after the server's name, before its hidden prompt is on screen,
// is never sent -- not to the model, not to the daemon -- and never becomes
// the key. It is not shown while typed, enter says why nothing was sent, and
// the key is taken only from the prompt once it opens.
//
// Neuter check: in startTurn, drop "|| addKeys.sending" -- the early enter
// starts a chat turn with the key as its question.
func TestAKeyTypedBeforeItsPromptIsNeverSentNorStored(t *testing.T) {
	daemon := keyedAddDaemon(t)
	sendingCleanup(t)
	const early = "dummy-not-a-key-typed-early"
	const real = "dummy-not-a-key"
	m := enterLine(newTestModel(), "/mcp-server add gh --env GITHUB_TOKEN docker run -i ghcr.io/example/server")

	// The name, agreed to: the add is now on its way, its answer not back yet.
	m, addCmd := pressEnter(typeText(m, "gh"))
	if addCmd == nil {
		t.Fatal("typing the name sent no add")
	}
	m = typeText(m, early)
	if strings.Contains(m.View(), early) {
		t.Fatal("what was typed while the add was on its way is shown on screen")
	}
	before := m.turnInput
	m, cmd := pressEnter(m)
	if cmd != nil {
		t.Fatal("enter while the add was on its way sent something")
	}
	if m.state != stateIdle || m.turnInput != before {
		t.Fatalf("enter while the add was on its way started a turn (state %v, turn input %q)", m.state, m.turnInput)
	}
	if !strings.Contains(lastTurn(t, m), "Not sent: Mochiii is still adding gh") {
		t.Errorf("the refusal does not say why:\n%s", lastTurn(t, m))
	}

	// Typed again, enter not pressed, before the answer: the prompt opens empty.
	m = typeText(m, early)
	m = follow(m, addCmd)
	if m.pendingMCPAdd == nil || m.pendingMCPAdd.Action != protocol.MCPActionSetEnv || m.pendingMCPAdd.EnvName != "GITHUB_TOKEN" ||
		m.input.EchoMode != textinput.EchoPassword {
		t.Fatalf("the hidden prompt did not open: pending %+v", m.pendingMCPAdd)
	}
	if m.input.Value() != "" {
		t.Fatal("what was typed before the prompt was carried into it")
	}

	m = enterLine(m, real)
	var setEnv []protocol.MCPRequest
	for _, r := range mcpRequests(t, daemon) {
		if r.Action == protocol.MCPActionSetEnv {
			setEnv = append(setEnv, r)
		}
	}
	if len(setEnv) != 1 || setEnv[0].EnvValue != real {
		t.Fatalf("the key was stored as %+v, want one set_env with the value typed into the prompt", setEnv)
	}
	if n := rawRequestsContaining(daemon, early); n != 0 {
		t.Fatalf("%d request(s) carried what was typed before the prompt", n)
	}
	if turnsContaining(m, early) != 0 || turnsContaining(m, real) != 0 {
		t.Fatal("a value is in the transcript")
	}
	for _, h := range m.promptHistory {
		if strings.Contains(h, early) || strings.Contains(h, real) {
			t.Fatal("a value is in the up-arrow recall")
		}
	}
	if addKeys.sending || m.input.EchoMode != textinput.EchoNormal || m.input.Placeholder != defaultInputPlaceholder {
		t.Error("the box did not go back to normal after the add")
	}
}

// A slash command typed while an add is on its way is not run and not stored;
// typed into the open hidden prompt it is refused in one line and the prompt
// stays open. A value that starts with "/" (a path) is still a value.
//
// Neuter check: in answerMCPAdd, drop the looksLikeCommand refusal -- the
// command is sent as the key's value.
func TestASlashCommandTypedDuringAnAddIsNotTheKey(t *testing.T) {
	daemon := keyedAddDaemon(t)
	sendingCleanup(t)
	const cmdLine = "/mcp-server get gh"
	m := enterLine(newTestModel(), "/mcp-server add gh --env GITHUB_TOKEN docker run -i ghcr.io/example/server")
	m, addCmd := pressEnter(typeText(m, "gh"))

	// While the add is on its way.
	m, cmd := pressEnter(typeText(m, cmdLine))
	if cmd != nil {
		t.Fatal("a slash command typed while the add was on its way was run")
	}
	if !strings.Contains(lastTurn(t, m), "Not sent") {
		t.Errorf("no one-line refusal:\n%s", lastTurn(t, m))
	}
	m = follow(m, addCmd)

	// At the open hidden prompt.
	m, cmd = pressEnter(typeText(m, cmdLine))
	if cmd != nil {
		t.Fatal("a slash command typed at the hidden prompt was sent")
	}
	if m.pendingMCPAdd == nil || m.pendingMCPAdd.EnvName != "GITHUB_TOKEN" || m.input.EchoMode != textinput.EchoPassword {
		t.Fatal("the hidden prompt closed on a command")
	}
	if !strings.Contains(lastTurn(t, m), "That is a command, not the value of GITHUB_TOKEN: nothing was stored") {
		t.Errorf("the refusal does not say so:\n%s", lastTurn(t, m))
	}
	for _, r := range mcpRequests(t, daemon) {
		if r.Action == protocol.MCPActionSetEnv || r.Action == protocol.MCPActionGet {
			t.Fatalf("the command reached the daemon: %+v", r)
		}
	}

	// A path is a value.
	const path = "/home/me/memory.json"
	m = enterLine(m, path)
	var stored []string
	for _, r := range mcpRequests(t, daemon) {
		if r.Action == protocol.MCPActionSetEnv {
			stored = append(stored, r.EnvValue)
		}
	}
	if len(stored) != 1 || stored[0] != path {
		t.Fatalf("stored %q, want the path once", stored)
	}
}

// The other add paths are unchanged: an add whose server needs no key leaves
// the box as it was, and an ordinary question after it is sent.
func TestAnAddWithNoKeyGivesTheBoxBack(t *testing.T) {
	mcpDaemon(t)
	sendingCleanup(t)
	m := runSlash(t, newTestModel(), "/mcp-server add files npx -y @modelcontextprotocol/server-filesystem /home/me/notes")
	m = runSlash(t, m, "files")
	if addKeys.sending || m.pendingMCPAdd != nil || m.input.EchoMode != textinput.EchoNormal ||
		m.input.Placeholder != defaultInputPlaceholder {
		t.Fatalf("the box was not given back: sending %v, pending %+v, echo %v", addKeys.sending, m.pendingMCPAdd, m.input.EchoMode)
	}
	m, cmd := pressEnter(typeText(m, "what does doc.go say?"))
	if cmd == nil || m.turnInput != "what does doc.go say?" {
		t.Fatal("a question after the add was not sent")
	}
}
