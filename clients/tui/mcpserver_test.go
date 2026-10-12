package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// mcpRequests decodes what a recording daemon was sent as MCP requests.
func mcpRequests(t *testing.T, d *recordingDaemon) []protocol.MCPRequest {
	t.Helper()
	var out []protocol.MCPRequest
	for _, raw := range d.requests() {
		var req protocol.MCPRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatalf("a recorded request does not decode: %v", err)
		}
		out = append(out, req)
	}
	return out
}

// mcpDaemon answers an add the way the real daemon does: the warning until the
// request is acknowledged, then "added".
func mcpDaemon(t *testing.T) *recordingDaemon {
	t.Helper()
	return startRequestRecorder(t, []string{protocol.FeatureMCPAdmin}, func(raw json.RawMessage) any {
		var req protocol.MCPRequest
		_ = json.Unmarshal(raw, &req)
		switch {
		case req.Action == protocol.MCPActionAdd && !req.AcknowledgedUnconfined:
			return protocol.MCPResponse{NeedsAcknowledgement: true, AgentMode: true,
				Detail: "Adding \"" + req.Name + "\" will make this daemon run: " + req.Command + " as an ordinary program with YOUR full privileges."}
		case req.Action == protocol.MCPActionAdd:
			return protocol.MCPResponse{Ok: true, AgentMode: true, Detail: "Added " + req.Name + ". It started and offers 2 tool(s): read, write.",
				Notes: []string{"GITHUB_TOKEN is not set in this daemon's environment"}}
		case req.Action == protocol.MCPActionRemove:
			return protocol.MCPResponse{Ok: true, AgentMode: true, Detail: "Removed " + req.Name + "."}
		}
		return protocol.MCPResponse{Ok: true, AgentMode: true, Report: "Agent mode is ON. 1 tool(s) would be offered to the model.\n"}
	})
}

// runSlash types a line and presses enter, then runs the command that came back
// and feeds its answer in -- one whole exchange with the daemon.
func runSlash(t *testing.T, m chatModel, line string) chatModel {
	t.Helper()
	m = typeText(m, line)
	m, cmd := pressEnter(m)
	if answer, ok := findMsgWithin[mcpResultMsg](cmd, 3e9); ok {
		updated, _ := m.Update(answer)
		m = updated.(chatModel)
	}
	return m
}

// ADDING A SERVER IS TWO STEPS, AND THE FIRST ADDS NOTHING. The daemon is asked
// without an acknowledgement and answers with what the server could do; only
// the server's name, typed, sends the request that adds it.
//
// Neuter check: set AcknowledgedUnconfined on the first request in
// handleMCPServerCommand.
func TestAddingAnMCPServerNeedsItsNameTyped(t *testing.T) {
	daemon := mcpDaemon(t)
	m := runSlash(t, newTestModel(), `/mcp-server add files --env GITHUB_TOKEN npx -y @modelcontextprotocol/server-filesystem "/home/me/My Notes"`)

	first := mcpRequests(t, daemon)
	if len(first) != 1 {
		t.Fatalf("%d request(s) sent, want one", len(first))
	}
	want := protocol.MCPRequest{
		ProtocolVersion: protocol.ProtocolVersion, MCP: true, Action: protocol.MCPActionAdd, Name: "files",
		Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem", "/home/me/My Notes"},
		Env: []string{"GITHUB_TOKEN"},
	}
	if !reflect.DeepEqual(first[0], want) {
		t.Fatalf("the daemon was sent\n %+v\nwant\n %+v", first[0], want)
	}
	if m.pendingMCPAdd == nil {
		t.Fatal("the client is not waiting for the name to be typed")
	}
	last := lastTurn(t, m)
	for _, s := range []string{"YOUR full privileges", "Type the server's name (files)", "esc, cancels"} {
		if !strings.Contains(last, s) {
			t.Errorf("the question does not say %q:\n%s", s, last)
		}
	}
	if !strings.Contains(m.input.Placeholder, "files") {
		t.Errorf("the prompt does not say what to type: %q", m.input.Placeholder)
	}

	// The name, typed: the same request again, acknowledged.
	m = runSlash(t, m, "files")
	all := mcpRequests(t, daemon)
	if len(all) != 2 || !all[1].AcknowledgedUnconfined {
		t.Fatalf("typing the name sent %d request(s) in all; want a second, acknowledged", len(all))
	}
	want.AcknowledgedUnconfined = true
	if !reflect.DeepEqual(all[1], want) {
		t.Errorf("the acknowledged request differs from what was shown: %+v", all[1])
	}
	if m.pendingMCPAdd != nil || m.input.Placeholder != defaultInputPlaceholder {
		t.Error("the confirmation did not close")
	}
	last = lastTurn(t, m)
	if !strings.Contains(last, "Added files. It started and offers 2 tool(s)") || !strings.Contains(last, "NOTE: GITHUB_TOKEN is not set") {
		t.Errorf("the daemon's answer and its note were not shown:\n%s", last)
	}
}

// ANYTHING ELSE CANCELS -- and gives the text back, because the likeliest
// "anything else" is the user's next question, typed without noticing that a
// confirmation was open. Esc cancels too. Neither sends a second request.
func TestNotTypingTheNameAddsNothingAndLosesNothing(t *testing.T) {
	daemon := mcpDaemon(t)
	waiting := runSlash(t, newTestModel(), "/mcp-server add files npx -y some-server")
	if waiting.pendingMCPAdd == nil {
		t.Fatal("no confirmation was opened")
	}

	other := runSlash(t, waiting, "why is the build failing")
	if other.pendingMCPAdd != nil {
		t.Error("the confirmation is still open after a different line")
	}
	if other.input.Value() != "why is the build failing" {
		t.Errorf("the question typed into the confirmation was lost: input is %q", other.input.Value())
	}
	if last := lastTurn(t, other); !strings.Contains(last, "Not added") || !strings.Contains(last, "back in the box") {
		t.Errorf("the cancel does not say what happened:\n%s", last)
	}
	for _, tr := range other.turns {
		if tr.role == roleUser {
			t.Error("the line typed into the confirmation was sent to the model as a question")
		}
	}

	escaped := press(waiting, tea.KeyEsc)
	if escaped.pendingMCPAdd != nil || escaped.input.Placeholder != defaultInputPlaceholder {
		t.Error("esc did not close the confirmation")
	}
	if last := lastTurn(t, escaped); !strings.Contains(last, "Not added") {
		t.Errorf("esc does not say nothing was added:\n%s", last)
	}

	if n := len(mcpRequests(t, daemon)); n != 1 {
		t.Errorf("%d request(s) reached the daemon; only the unacknowledged first one should have", n)
	}
}

// A VALUE TYPED WHERE A NAME BELONGS IS NOT SENT, NOT SHOWN AND NOT RECALLED.
// "TOKEN=abc npx ..." is how a shell is given a variable for one command, so it
// is what people will type; the command line is stored and listed, so the value
// must never get that far.
//
// Neuter check: drop the looksLikeAssignment test in parseMCPAdd.
func TestASecretOnTheAddLineGoesNowhere(t *testing.T) {
	const secret = "ghp_0123456789abcdefSECRETtoken"
	for name, line := range map[string]string{
		"assignment before the command": "/mcp-server add gh GITHUB_TOKEN=" + secret + " npx -y server-github",
		"--env NAME=VALUE":              "/mcp-server add gh --env GITHUB_TOKEN=" + secret + " npx -y server-github",
		"--env=NAME=VALUE":              "/mcp-server add gh --env=GITHUB_TOKEN=" + secret + " npx -y server-github",
	} {
		daemon := mcpDaemon(t)
		m := runSlash(t, newTestModel(), line)
		if n := len(daemon.requests()); n != 0 {
			t.Errorf("%s: %d request(s) were sent", name, n)
		}
		if m.pendingMCPAdd != nil {
			t.Errorf("%s: a confirmation was opened for it", name)
		}
		for _, tr := range m.turns {
			if strings.Contains(tr.text, secret) {
				t.Errorf("%s: the value is in the transcript", name)
			}
		}
		for _, p := range m.promptHistory {
			if strings.Contains(p, secret) {
				t.Errorf("%s: the value waits in the up-arrow recall", name)
			}
		}
		if strings.Contains(m.View(), secret) {
			t.Errorf("%s: the value is on screen", name)
		}
		if last := lastTurn(t, m); !strings.Contains(last, "Nothing was sent") || !strings.Contains(last, "--env NAME") {
			t.Errorf("%s: the refusal does not say what to do instead:\n%s", name, last)
		}
	}

	// An argument AFTER the command may hold "=": --port=8080 is not a secret.
	daemon := mcpDaemon(t)
	m := runSlash(t, newTestModel(), "/mcp-server add web uvx some-server --port=8080 --root=/srv")
	got := mcpRequests(t, daemon)
	if len(got) != 1 || got[0].Command != "uvx" || !reflect.DeepEqual(got[0].Args, []string{"some-server", "--port=8080", "--root=/srv"}) {
		t.Fatalf("ordinary flags were mangled or refused: %+v", got)
	}
	if len(m.promptHistory) == 0 || !strings.Contains(m.promptHistory[len(m.promptHistory)-1], "--port=8080") {
		t.Error("an ordinary add line is not kept for the up arrow")
	}
}

// Remove and the bare list reach the daemon as what they are; anything else is
// answered here with the usage.
func TestMCPServerSubcommands(t *testing.T) {
	daemon := mcpDaemon(t)
	m := runSlash(t, newTestModel(), "/mcp-server remove files")
	if last := lastTurn(t, m); !strings.Contains(last, "Removed files.") {
		t.Errorf("remove: %s", last)
	}
	m = runSlash(t, m, "/mcp-server")
	if last := lastTurn(t, m); !strings.Contains(last, "Agent mode is ON") || !strings.Contains(last, "/mcp-server add <name> <command>") {
		t.Errorf("list does not show the report and how to add a server:\n%s", last)
	}
	got := mcpRequests(t, daemon)
	if len(got) != 2 || got[0].Action != protocol.MCPActionRemove || got[0].Name != "files" || got[1].Action != protocol.MCPActionList {
		t.Fatalf("the daemon was sent %+v", got)
	}

	for _, line := range []string{"/mcp-server restart", "/mcp-server remove", "/mcp-server remove a b", "/mcp-server add", "/mcp-server add onlyname",
		`/mcp-server add x npx "unterminated`, "/mcp-server add x --env"} {
		before := len(daemon.requests())
		m = runSlash(t, newTestModel(), line)
		if len(daemon.requests()) != before {
			t.Errorf("%q was sent to the daemon", line)
		}
		if last := lastTurn(t, m); !strings.Contains(last, "/mcp-server add <name>") {
			t.Errorf("%q is not answered with the usage:\n%s", line, last)
		}
	}
}

// A daemon started before this existed is not sent the request -- it would read
// it as an empty prompt -- and the line says to restart it.
func TestAnOlderDaemonIsToldApartForMCPServer(t *testing.T) {
	daemon := startRequestRecorder(t, []string{protocol.FeatureSavedChats}, func(json.RawMessage) any {
		return protocol.TokenResponse{Error: "prompt is empty"}
	})
	m := runSlash(t, newTestModel(), "/mcp-server")
	if n := len(daemon.requests()); n != 0 {
		t.Errorf("%d request(s) reached a daemon that cannot read one", n)
	}
	if last := lastTurn(t, m); !strings.Contains(last, "older than this client") || !strings.Contains(last, "restart it") {
		t.Errorf("the line does not say the daemon is older:\n%s", last)
	}
}

// WHAT THE DAEMON SENDS IS DRAWN ON A TERMINAL, and part of it -- a tool's
// description, a server's name -- was written by a third-party program. None of
// it reaches the screen with an escape sequence in it.
func TestMCPResultsAreSanitized(t *testing.T) {
	const esc = "\x1b[2J\x1b]0;owned\x07"
	for name, resp := range map[string]protocol.MCPResponse{
		"report": {Ok: true, AgentMode: true, Report: "tool" + esc + "\n",
			Servers: []protocol.MCPServerInfo{{Name: "x" + esc, Command: "npx" + esc, Source: protocol.MCPSourceUser}}},
		"detail and notes": {Ok: true, Detail: "Added" + esc, Notes: []string{"note" + esc}},
		"error":            {Error: "refused" + esc},
	} {
		if got := formatMCPResult(mcpResultMsg{resp: resp}); strings.Contains(got, "\x1b") || strings.Contains(got, "\x07") {
			t.Errorf("%s: a control sequence reached the text: %q", name, got)
		}
	}
	if got := formatMCPResult(mcpResultMsg{err: errors.New("dial" + esc)}); strings.Contains(got, "\x1b") {
		t.Errorf("a transport error carried a control sequence: %q", got)
	}
	// The servers the user added are named under a list, with what each runs.
	got := formatMCPResult(mcpResultMsg{resp: protocol.MCPResponse{Ok: true, AgentMode: true, Report: "Agent mode is ON.\n",
		Servers: []protocol.MCPServerInfo{
			{Name: "files", Command: "npx -y server-filesystem /notes", Source: protocol.MCPSourceUser},
			{Name: "corp", Command: "uvx corp", Source: protocol.MCPSourceConfig},
		}}})
	if !strings.Contains(got, "MCP servers you added: files (npx -y server-filesystem /notes)") || strings.Contains(got, "corp (") {
		t.Errorf("the added servers are not listed as the user's own:\n%s", got)
	}
}

// The line is cut into words the way a shell user expects, and nothing more:
// quotes keep a path whole; nothing is expanded or escaped.
func TestSplitCommandLine(t *testing.T) {
	for line, want := range map[string][]string{
		``:                               nil,
		`  add   x  npx `:                {"add", "x", "npx"},
		`add x npx "/home/me/My Notes"`:  {"add", "x", "npx", "/home/me/My Notes"},
		`add x npx '/a b' --flag="c d"`:  {"add", "x", "npx", "/a b", "--flag=c d"},
		`add x npx ""`:                   {"add", "x", "npx", ""},
		`add x npx $HOME ~ *.txt ; rm x`: {"add", "x", "npx", "$HOME", "~", "*.txt", ";", "rm", "x"},
	} {
		got, err := splitCommandLine(line)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("splitCommandLine(%q) = %q, %v; want %q", line, got, err, want)
		}
	}
	for _, line := range []string{`add x "open`, `add x 'open`} {
		if _, err := splitCommandLine(line); err == nil {
			t.Errorf("splitCommandLine(%q) accepted an unclosed quote", line)
		}
	}
}
