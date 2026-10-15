package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

const testGitHubToken = "ghp_TESTTOKENxxxxxxxxxxxxxxxxxxxxxx0001"

// githubDaemon is a fake daemon that answers every GitHubRequest with answer.
func githubDaemon(t *testing.T, answer func(protocol.GitHubRequest) protocol.GitHubResponse) *recordingDaemon {
	t.Helper()
	return startRequestRecorder(t, []string{protocol.FeatureGitHub}, func(raw json.RawMessage) any {
		var req protocol.GitHubRequest
		_ = json.Unmarshal(raw, &req)
		resp := answer(req)
		resp.ProtocolVersion = protocol.ProtocolVersion
		return resp
	})
}

func githubRequests(t *testing.T, d *recordingDaemon) []protocol.GitHubRequest {
	t.Helper()
	var out []protocol.GitHubRequest
	for _, raw := range d.requests() {
		var req protocol.GitHubRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatalf("a recorded request does not decode: %v", err)
		}
		out = append(out, req)
	}
	return out
}

// deliverGitHub runs cmd and hands its answer to the model, when it has one.
func deliverGitHub(t *testing.T, m chatModel, cmd tea.Cmd) chatModel {
	t.Helper()
	if answer, ok := findMsgWithin[githubResultMsg](cmd, 3e9); ok {
		updated, _ := m.Update(answer)
		m = updated.(chatModel)
	}
	return m
}

// runGitHub types a command, presses Enter, and hands the daemon's answer back.
func runGitHub(t *testing.T, m chatModel, line string) chatModel {
	t.Helper()
	m = typeText(m, line)
	m, cmd := pressEnter(m)
	return deliverGitHub(t, m, cmd)
}

// THE TOKEN IS TYPED INTO A HIDDEN BOX AND SENT ONCE. It is never on the
// screen, in the transcript, in the prompt history (↑ would bring it back), or
// in the answer this client keeps.
//
// Neuter check: drop the githubTokenEntry branch from handleConnectKey -- the
// token is then taken as a provider key and sent as a ConnectRequest.
func TestGitHubConnectTakesTheTokenFromAHiddenBoxAndKeepsNoCopy(t *testing.T) {
	daemon := githubDaemon(t, func(req protocol.GitHubRequest) protocol.GitHubResponse {
		return protocol.GitHubResponse{Text: "Connected to GitHub as mochiii-bot.", Connected: true, Login: "mochiii-bot"}
	})
	m := typeText(newTestModel(), "/github connect")
	m, _ = pressEnter(m)
	if m.state != stateConnect || !m.githubTokenEntry || m.input.EchoMode != textinput.EchoPassword ||
		m.input.Placeholder != githubTokenPrompt {
		t.Fatalf("/github connect did not open the hidden box: state %v, entry %v, echo %v, placeholder %q",
			m.state, m.githubTokenEntry, m.input.EchoMode, m.input.Placeholder)
	}
	if len(daemon.requests()) != 0 {
		t.Fatal("something was sent before a token was entered")
	}

	m = typeText(m, testGitHubToken)
	if strings.Contains(m.View(), testGitHubToken) {
		t.Error("the token is on the screen while it is typed")
	}
	m, cmd := pressEnter(m)
	answer, ok := findMsgWithin[githubResultMsg](cmd, 3e9)
	if !ok {
		t.Fatal("no answer came back")
	}
	if answer.req.Token != "" {
		t.Error("the answer this client keeps still holds the token")
	}
	updated, _ := m.Update(answer)
	m = updated.(chatModel)

	got := githubRequests(t, daemon)
	if len(got) != 1 || !got[0].GitHub || got[0].Action != protocol.GitHubActionConnect || got[0].Token != testGitHubToken {
		t.Fatalf("the daemon was sent %+v", got)
	}
	if m.githubTokenEntry || m.input.EchoMode != textinput.EchoNormal || m.state == stateConnect {
		t.Error("the box was not closed after the token was sent")
	}
	if !strings.Contains(lastTurn(t, m), "Connected to GitHub as mochiii-bot") {
		t.Errorf("the daemon's answer is not shown: %s", lastTurn(t, m))
	}
	for _, turn := range m.turns {
		if strings.Contains(turn.text, testGitHubToken) {
			t.Errorf("the token is in the transcript: %q", turn.text)
		}
	}
	for _, p := range m.promptHistory {
		if strings.Contains(p, testGitHubToken) {
			t.Error("the token is in the prompt history, where ↑ would bring it back")
		}
	}
	if strings.Contains(m.View(), testGitHubToken) {
		t.Error("the token is on the screen")
	}
}

// Esc leaves the box with nothing sent, and the next thing typed is a question
// again, shown as typed.
func TestGitHubConnectEscSendsNothing(t *testing.T) {
	daemon := githubDaemon(t, func(protocol.GitHubRequest) protocol.GitHubResponse { return protocol.GitHubResponse{} })
	m := typeText(newTestModel(), "/github connect")
	m, _ = pressEnter(m)
	m = typeText(m, "ghp_half")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(chatModel)
	if len(daemon.requests()) != 0 {
		t.Error("esc sent something")
	}
	if m.githubTokenEntry || m.input.EchoMode != textinput.EchoNormal || m.input.Value() != "" {
		t.Error("esc did not close the box and empty it")
	}
	if !strings.Contains(lastTurn(t, m), "cancelled; nothing was sent or stored") {
		t.Errorf("esc does not say nothing was stored: %s", lastTurn(t, m))
	}
}

// A token pasted on the command line itself is neither sent nor printed back:
// the hidden box is the only way in.
func TestATokenTypedAfterGitHubConnectIsNotEchoed(t *testing.T) {
	daemon := githubDaemon(t, func(protocol.GitHubRequest) protocol.GitHubResponse { return protocol.GitHubResponse{} })
	m := runGitHub(t, newTestModel(), "/github connect "+testGitHubToken)
	if len(daemon.requests()) != 0 {
		t.Error("a token typed on the command line was sent")
	}
	for _, turn := range m.turns {
		if strings.Contains(turn.text, testGitHubToken) {
			t.Errorf("the token was printed back: %q", turn.text)
		}
	}
	if !strings.Contains(lastTurn(t, m), "hidden box") {
		t.Errorf("the reply does not say how to connect: %s", lastTurn(t, m))
	}
}

// AN ISSUE IS SHOWN AND GOES WITH THE NEXT QUESTION, once, as command output.
//
// Neuter check: drop the attachOutput call in handleGitHubResult.
func TestAnIssueIsShownAndAttachedToTheNextQuestion(t *testing.T) {
	daemon := githubDaemon(t, func(req protocol.GitHubRequest) protocol.GitHubResponse {
		return protocol.GitHubResponse{Text: "Issue #104 · open · \"Crash on start\"\nby mallory (no relationship to the repository)", Attach: true}
	})
	m := runGitHub(t, newTestModel(), "/issue 104")
	got := githubRequests(t, daemon)
	if len(got) != 1 || got[0].Action != protocol.GitHubActionIssue || got[0].Number != 104 {
		t.Fatalf("the daemon was sent %+v", got)
	}
	last := m.turns[len(m.turns)-1]
	if !strings.Contains(last.text, "Crash on start") || !strings.Contains(last.text, attachNote) {
		t.Errorf("the issue is not shown with the attach note:\n%s", last.text)
	}
	if !last.local {
		t.Error("the reply is not marked local, so it would be replayed as the assistant's own words")
	}
	if len(m.attached) != 1 || m.attached[0].Command != "/issue" || !strings.Contains(m.attached[0].Output, "Crash on start") {
		t.Errorf("attached = %+v", m.attached)
	}
}

// Each command asks for the right thing, and what cannot be read as one is
// answered here with how to say it, and never sent.
func TestTheGitHubCommandsAskForTheRightThing(t *testing.T) {
	daemon := githubDaemon(t, func(protocol.GitHubRequest) protocol.GitHubResponse { return protocol.GitHubResponse{Text: "ok"} })
	for line, want := range map[string]protocol.GitHubRequest{
		"/github":            {Action: protocol.GitHubActionStatus},
		"/github use":        {Action: protocol.GitHubActionUse},
		"/github disconnect": {Action: protocol.GitHubActionDisconnect},
		"/issues":            {Action: protocol.GitHubActionIssues},
		"/issues good first": {Action: protocol.GitHubActionIssues, Label: "good first"},
		"/pr":                {Action: protocol.GitHubActionPRs},
		"/pr 31":             {Action: protocol.GitHubActionPR, Number: 31},
		"/pr 31 log":         {Action: protocol.GitHubActionLog, Number: 31},
	} {
		before := len(daemon.requests())
		runGitHub(t, newTestModel(), line)
		got := githubRequests(t, daemon)
		if len(got) != before+1 {
			t.Errorf("%q sent %d request(s)", line, len(got)-before)
			continue
		}
		req := got[len(got)-1]
		if req.Action != want.Action || req.Number != want.Number || req.Label != want.Label || req.Token != "" {
			t.Errorf("%q sent %+v, want %+v", line, req, want)
		}
	}
	before := len(daemon.requests())
	for line, want := range map[string]string{
		"/issue abc":     "/issue <number>",
		"/issue 0":       "/issue <number>",
		"/pr x":          "/pr 31 log says why",
		"/pr 31 merge":   "/pr 31 log says why",
		"/github delete": "/github takes connect, use or disconnect",
	} {
		m := runGitHub(t, newTestModel(), line)
		if last := lastTurn(t, m); !strings.Contains(last, want) {
			t.Errorf("%q is not answered with %q:\n%s", line, want, last)
		}
	}
	if after := len(daemon.requests()); after != before {
		t.Errorf("%d request(s) were sent for commands that could not be read", after-before)
	}
}

// A daemon started before it could reach GitHub is not sent the request, and
// the line says to restart it.
func TestAnOlderDaemonIsToldApartForGitHub(t *testing.T) {
	daemon := startRequestRecorder(t, []string{protocol.FeatureRules}, func(json.RawMessage) any {
		return protocol.TokenResponse{Error: "prompt is empty"}
	})
	m := runGitHub(t, newTestModel(), "/pr 31")
	if n := len(daemon.requests()); n != 0 {
		t.Errorf("%d request(s) reached a daemon that cannot read one", n)
	}
	if last := lastTurn(t, m); !strings.Contains(last, "older than this client") || !strings.Contains(last, "restart it") {
		t.Errorf("the line does not say the daemon is older:\n%s", last)
	}
}

// Everything shown was written on GitHub or by the daemon, and none of it
// reaches the terminal with an escape sequence in it.
func TestGitHubRepliesAreSanitized(t *testing.T) {
	const esc = "\x1b[2J\x1b]0;owned\x07"
	for name, msg := range map[string]githubResultMsg{
		"text":    {resp: protocol.GitHubResponse{Text: "Issue #1" + esc}},
		"refused": {resp: protocol.GitHubResponse{Error: "no" + esc}},
		"failed":  {err: errors.New("dial" + esc)},
	} {
		if got := formatGitHubResult(msg); strings.Contains(got, "\x1b") || strings.Contains(got, "\x07") {
			t.Errorf("%s: a control sequence reached the text: %q", name, got)
		}
	}
}

// The commands are in the catalog the popup and /help are drawn from.
func TestTheGitHubCommandsAreInTheCatalog(t *testing.T) {
	for _, name := range []string{"github", "issue", "issues", "pr"} {
		found := false
		for _, c := range slashCatalog {
			found = found || c.Name == name && c.Kind == slashLocal
		}
		if !found {
			t.Errorf("/%s is not in the catalog as a local command", name)
		}
	}
}
