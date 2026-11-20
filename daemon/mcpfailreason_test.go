package main

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// What server-filesystem@2.4.0 wrote on Node 20 (2026-11-19), and what a Node
// server that throws for a missing key writes. Both end in Node's banner.
var (
	nodeTooOldStderr = []string{
		"npm warn EBADENGINE Unsupported engine {",
		"npm warn EBADENGINE   package: '@modelcontextprotocol/server-filesystem@2.4.0',",
		"npm warn EBADENGINE   required: { node: '>=24' },",
		"npm warn EBADENGINE   current: { node: 'v20.20.2', npm: '10.8.2' }",
		"npm warn EBADENGINE }",
		"file:///home/u/.npm/_npx/6672/node_modules/@modelcontextprotocol/server-filesystem/dist/pinned-directory.js:49",
		`import { getSystemErrorMessage } from "util";`,
		"         ^^^^^^^^^^^^^^^^^^^^^",
		"SyntaxError: The requested module 'util' does not provide an export named 'getSystemErrorMessage'",
		"    at ModuleJob._instantiate (node:internal/modules/esm/module_job:213:21)",
		"    at async ModuleJob.run (node:internal/modules/esm/module_job:320:5)",
		"",
		"Node.js v20.20.2",
	}
	nodeThrewStderr = []string{
		"/home/u/.npm/_npx/1/node_modules/server-x/dist/index.js:12",
		"    throw new Error(\"PERPLEXITY_API_KEY environment variable is required\");",
		"    ^",
		"",
		"Error: PERPLEXITY_API_KEY environment variable is required",
		"    at Object.<anonymous> (/home/u/.npm/_npx/1/node_modules/server-x/dist/index.js:12:11)",
		"    at Module._compile (node:internal/modules/cjs/loader:1504:14)",
		"",
		"Node.js v20.20.2",
	}
)

// Neuter check: return the last line in failureReason.
func TestFailureReasonFindsTheLineAPersonCanActOn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
		want  string
	}{
		{"node too old", nodeTooOldStderr, "it needs Node.js >=24, and this computer has v20.20.2"},
		{"node threw", nodeThrewStderr, "Error: PERPLEXITY_API_KEY environment variable is required"},
		{"python traceback", []string{"Traceback (most recent call last):", `  File "/x/server.py", line 3, in <module>`,
			"    import mcp", "ModuleNotFoundError: No module named 'mcp'"}, "ModuleNotFoundError: No module named 'mcp'"},
		{"one plain line", []string{"listening is not possible here"}, "listening is not possible here"},
		{"only a banner", []string{"Node.js v20.20.2"}, ""},
		{"nothing", nil, ""},
	} {
		got := failureReason(tc.lines)
		if !strings.HasPrefix(got, tc.want) || (tc.want == "" && got != "") {
			t.Errorf("%s: failureReason = %q, want it to start %q", tc.name, got, tc.want)
		}
	}
}

func TestAppendRecentKeepsTheLastLines(t *testing.T) {
	var lines []string
	for i := 0; i < recentStderrLines+15; i++ {
		lines = appendRecent(lines, strings.Repeat("x", i%3)+"line")
	}
	lines = appendRecent(lines, "the last one")
	if len(lines) != recentStderrLines || lines[len(lines)-1] != "the last one" {
		t.Errorf("kept %d lines ending %q, want %d ending with the last", len(lines), lines[len(lines)-1], recentStderrLines)
	}
}

// The add answer: a Node too old is not a wrong command; anything else keeps
// "fix the command", now with the server's own words.
func TestFailedStartTextSaysWhatTheServerSaid(t *testing.T) {
	err := errors.New(`server fs: mcp server unavailable: connection closed: calling "initialize": EOF`)
	old := failedStartText("Added", "fs", err, failureReason(nodeTooOldStderr))
	for _, want := range []string{"it needs Node.js >=24", "v20.20.2", "its command is likely fine", "update Node.js"} {
		if !strings.Contains(old, want) {
			t.Errorf("node-too-old answer lacks %q:\n%s", want, old)
		}
	}
	if strings.Contains(old, "fix the command") {
		t.Errorf("node-too-old answer still says to fix the command:\n%s", old)
	}
	threw := failedStartText("Added", "px", err, failureReason(nodeThrewStderr))
	if !strings.Contains(threw, "It said: Error: PERPLEXITY_API_KEY environment variable is required.") ||
		!strings.Contains(threw, "fix the command (`/mcp-server remove px`, then add it again)") {
		t.Errorf("answer = %s", threw)
	}
	if silent := failedStartText("Added", "q", err, ""); strings.Contains(silent, "It said") {
		t.Errorf("a server that said nothing was quoted: %s", silent)
	}
}

// End to end through the add: a program that writes what server-filesystem
// wrote on Node 20 and exits. The answer and the server's row both name the
// Node version, and nothing says to fix the command.
//
// Neuter check: put `b.lastLine[name] = []string{line}` back in noteLine.
func TestAnAddWhoseNodeIsTooOldSaysSo(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if runtime.GOOS == "windows" || err != nil {
		t.Skip("needs sh to stand in for a server that crashes")
	}
	script := "for l in \"$@\"; do printf '%s\\n' \"$l\" >&2; done; exit 1"
	s := mcpAdminTestServer(t)
	req := addRequest("fs", sh, append([]string{"-c", script, "sh"}, nodeTooOldStderr...)...)
	req.AcknowledgedUnconfined = true
	resp := s.mcpResult(context.Background(), req)
	if !resp.Ok || !strings.Contains(resp.Detail, "did NOT start when tried: it needs Node.js >=24, and this computer has v20.20.2") ||
		strings.Contains(resp.Detail, "fix the command") {
		t.Fatalf("ok=%v err=%q\n%s", resp.Ok, resp.Error, resp.Detail)
	}
	if got := serverInfo(t, s, "fs").StatusLine; !strings.Contains(got, "it needs Node.js >=24") {
		t.Errorf("the server's row says %q, want the Node version it needs", got)
	}
}
