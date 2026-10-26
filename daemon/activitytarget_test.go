package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"mochiii/protocol"
)

// WHAT A CALL WAS ABOUT (activitytarget.go). The client's line for a tool call
// says what was done to what -- "Read clients/tui/chat.go" -- and the "what"
// can only come from here, because an activity carries no arguments.

func TestEveryBuiltinCallNamesItsTarget(t *testing.T) {
	for _, tc := range []struct{ tool, args, want string }{
		{"read_file", `{"path":"clients/tui/chat.go"}`, "clients/tui/chat.go"},
		{"read_file", `{"path":"a.go","start_line":120,"end_line":180}`, "a.go:120-180"},
		{"read_file", `{"path":"a.go","start_line":120}`, "a.go:120-"},
		{"list_directory", `{"path":"clients/tui"}`, "clients/tui"},
		{"search_code", `{"query":"where is the retry budget"}`, `"where is the retry budget"`},
		{"grep", `{"pattern":"func main","path":"daemon"}`, `"func main" in daemon`},
		{"grep", `{"pattern":"TODO","path":"."}`, `"TODO"`},
		{"find_files", `{"pattern":"*_test.go"}`, "*_test.go"},
		{"sandbox_exec", `{"command":"go test ./...","timeout_seconds":60}`, "go test ./..."},
		{"run_command", `{"command":"cd build && ctest","cwd":"."}`, "cd build && ctest"},
		{"web_search", `{"query":"bubbletea mouse"}`, `"bubbletea mouse"`},
		{"web_fetch", `{"url":"https://go.dev/doc"}`, "https://go.dev/doc"},
		{"propose_edit", `{"path":"main.go","search":"a","replace":"b"}`, "main.go"},
		{"propose_ast_edit", `{"path":"main.go","symbol":"run","replace":"x"}`, "main.go run"},
		{"rename_symbol", `{"path":"a.go","symbol":"old","new_name":"fresh"}`, "old → fresh"},
		{"query_compiler_definition", `{"path":"a.go","line":12,"character":4}`, "a.go:12"},
		{"git_history", `{"command":"log","path":"daemon/server.go"}`, "log daemon/server.go"},
		{"github", `{"what":"issue","number":42}`, "issue #42"},
		{"investigate", `{"question":"who reads the budget?"}`, `"who reads the budget?"`},
		{"repo_map", `{}`, ""},
	} {
		if got := activityTarget("builtin", tc.tool, tc.args); got != tc.want {
			t.Errorf("%s %s: target %q, want %q", tc.tool, tc.args, got, tc.want)
		}
	}
}

// The target is the MODEL'S text: one line, no control characters, bounded.
// And an MCP server's arguments are not guessed at.
func TestATargetIsOneSafeLine(t *testing.T) {
	got := activityTarget("builtin", "sandbox_exec", `{"command":"go test\n  ./...\t-run \u001b[31mX\u0007"}`)
	if got != "go test ./... -run [31mX" {
		t.Errorf("target %q: want newlines and tabs as single spaces and control characters dropped", got)
	}
	long := activityTarget("builtin", "read_file", `{"path":"`+strings.Repeat("a/", 300)+`x.go"}`)
	if n := utf8.RuneCountInString(long); n != maxTargetRunes || !strings.HasSuffix(long, "…") {
		t.Errorf("a 600-character path came out as %d runes (%q…), want %d ending in an ellipsis", n, long[:20], maxTargetRunes)
	}
	if got := activityTarget("github-mcp", "read_file", `{"path":"x"}`); got != "" {
		t.Errorf("an MCP server's call got target %q: its arguments mean what that server says, not what ours do", got)
	}
	if got := activityTarget("builtin", "read_file", `not json`); got != "" {
		t.Errorf("unreadable arguments gave target %q", got)
	}
}

// End to end through the agent loop: the activity a real built-in call reports
// carries the path it read, on every phase from running on.
func TestTheActivityOfARealCallCarriesItsTarget(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"notes/todo.txt"}`),
		textSSE("done"),
	)
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": "allow"}},
	})
	if err := os.MkdirAll(filepath.Join(s.workspace, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.workspace, "notes", "todo.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, activity, err := runLoop(t, s)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if !hasPhase(activity, protocol.ToolPhaseSucceeded) {
		t.Fatalf("the read did not succeed; activity = %+v", activity)
	}
	for _, a := range activity {
		if a.Phase == protocol.ToolPhaseRunning || a.Phase == protocol.ToolPhaseSucceeded {
			if a.Target != "notes/todo.txt" {
				t.Errorf("%s activity has target %q, want the path the call read", a.Phase, a.Target)
			}
		}
	}
}

// An edit with no search makes a new file, and the client is told so -- it
// reads "Write", not "Edit". Only for the built-in edit: an MCP server's tool of
// the same name means whatever that server says.
func TestAnEditWithNoSearchCreatesAFile(t *testing.T) {
	for _, tc := range []struct {
		server, tool, args string
		want               bool
	}{
		{"builtin", "propose_edit", `{"path":"login.html","search":"","replace":"<form>"}`, true},
		{"builtin", "propose_edit", `{"path":"login.html","search":"<form>","replace":"<form id=x>"}`, false},
		{"builtin", "propose_edit", `{"path":"login.html","replace":"<form>"}`, false},
		{"builtin", "read_file", `{"path":"login.html","search":""}`, false},
		{"other-mcp", "propose_edit", `{"path":"login.html","search":"","replace":"x"}`, false},
	} {
		a := withCallTarget(protocol.ToolActivity{CallID: "c1"}, tc.server, tc.tool, tc.args)
		if a.Creates != tc.want {
			t.Errorf("%s/%s %s: Creates = %v, want %v", tc.server, tc.tool, tc.args, a.Creates, tc.want)
		}
		if a.CallID != "c1" || a.Server != tc.server || a.Tool != tc.tool {
			t.Errorf("withCallTarget lost the call's identity: %+v", a)
		}
	}
}
