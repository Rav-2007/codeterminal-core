package tui

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"mochiii/protocol"
)

// inProjectRequest is a call auto-approve answers: a command in the private copy.
func inProjectRequest() protocol.ToolApprovalRequest {
	return protocol.ToolApprovalRequest{CallID: "c-auto", Server: "builtin", Tool: "run_command",
		Arguments: `{"command":"go test ./..."}`, ArgumentsSHA256: "abc", Lane: protocol.LaneFirstParty, Confined: true}
}

// CTRL+SPACE TURNS AUTO-APPROVE ON AND OFF (owner, 2026-11-21), in any state,
// and types nothing. The terminal sends NUL for it, which Bubble Tea names
// ctrl+@.
func TestCtrlSpaceTurnsAutoApproveOnAndOff(t *testing.T) {
	key := tea.KeyMsg{Type: tea.KeyCtrlAt}
	if key.String() != autoApproveKey {
		t.Fatalf("ctrl+space arrives as %q, not %q", key.String(), autoApproveKey)
	}
	m := newTestModel()
	if m.autoApprove {
		t.Fatal("auto-approve is on at the start")
	}
	next, _ := m.Update(key)
	m = next.(chatModel)
	if !m.autoApprove || m.input.Value() != "" {
		t.Fatalf("ctrl+space did not turn it on (on=%t), or typed %q", m.autoApprove, m.input.Value())
	}
	next, _ = m.Update(key)
	if next.(chatModel).autoApprove {
		t.Error("a second ctrl+space did not turn it off")
	}
}

// WHILE IT IS ON, A QUESTION IT COVERS IS ANSWERED AS Y WOULD ANSWER IT, before
// it is drawn, and the chat says so. The two it leaves to the person -- a read
// outside the project, a command in the project folder itself -- are drawn and
// wait, and the row under the prompt says why.
//
// Neuter checks: drop the hook in handleToolApproval and the first case waits;
// make autoSkips return "" and the outside read is answered.
func TestAutoApproveAnswersWhatItCoversAndAsksTheRest(t *testing.T) {
	ask := func(req protocol.ToolApprovalRequest) (chatModel, chan string) {
		reply := make(chan string, 1)
		m := newTestModel()
		m.autoApprove = true
		m.state, m.streamCh = stateStreaming, make(chan tea.Msg, 4)
		m.turns = []turn{{role: roleUser, text: "go"}}
		next, _ := m.Update(toolApprovalMsg{req: req, reply: reply})
		return next.(chatModel), reply
	}

	m, reply := ask(inProjectRequest())
	select {
	case d := <-reply:
		if d != autoApproveDecision {
			t.Errorf("answered %q, want auto-approve's yes", d)
		}
	default:
		t.Fatal("a call auto-approve covers was left waiting")
	}
	if m.state != stateStreaming || m.pendingApproval != nil {
		t.Errorf("the turn did not carry on: state %v", m.state)
	}
	// No "✓ auto-approved …" line (owner, 2026-11-21): the call's own line is
	// the record in the chat, the audit log the rest.
	if len(m.turns) != 1 {
		t.Errorf("auto-approve added to the chat: %q", lastSystemText(m.turns))
	}

	outside := inProjectRequest()
	outside.Tool, outside.OutsidePath = "read_file", "/home/someone/notes.txt"
	inPlace := inProjectRequest()
	inPlace.InPlace = true
	for name, req := range map[string]protocol.ToolApprovalRequest{"outside the project": outside, "in the project folder": inPlace} {
		m, reply := ask(req)
		if len(reply) != 0 || m.state != stateToolApproval {
			t.Errorf("%s: auto-approve answered it (state %v)", name, m.state)
		}
		if row := ansi.Strip(m.autoRow()); !strings.Contains(row, "this one asks: "+autoSkips(req)) {
			t.Errorf("%s: the auto-approve row does not say why it asks: %q", name, row)
		}
	}
}

// TURNED ON WHILE A QUESTION IT COVERS IS WAITING, it answers it; one it does
// not cover keeps waiting.
func TestTurningAutoApproveOnAnswersTheWaitingQuestion(t *testing.T) {
	m, reply := pendingApprovalModel(t, inProjectRequest())
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlAt})
	select {
	case d := <-reply:
		if d != autoApproveDecision || next.(chatModel).state != stateStreaming {
			t.Errorf("turning it on answered %q and left state %v", d, next.(chatModel).state)
		}
	default:
		t.Error("turning it on left the waiting question unanswered")
	}

	m, reply = pendingApprovalModel(t, laneBRequest()) // reads /etc/passwd: not in the project
	req := laneBRequest()
	req.OutsidePath = "/etc/passwd"
	m.pendingApproval = &req
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlAt})
	if len(reply) != 0 || next.(chatModel).state != stateToolApproval {
		t.Error("turning it on answered a read outside the project")
	}
}

// THE ANSWER ON THE WIRE IS Y'S, MARKED AUTO, so the daemon audits it as
// auto_approve; a person's y is not marked.
func TestTheWireAnswerSaysAuto(t *testing.T) {
	for _, tc := range []struct {
		sent string
		auto bool
	}{{autoApproveDecision, true}, {protocol.ApprovalApprove, false}} {
		clientEnd, daemonEnd := net.Pipe()
		sess := &daemonSession{conn: clientEnd, enc: json.NewEncoder(clientEnd), dec: json.NewDecoder(clientEnd)}
		req := inProjectRequest()
		ch := make(chan tea.Msg, 1)
		go func() { _ = askForApproval(context.Background(), sess, req, ch) }()
		(<-ch).(toolApprovalMsg).reply <- tc.sent
		var resp protocol.ToolApprovalResponse
		if err := json.NewDecoder(daemonEnd).Decode(&resp); err != nil {
			t.Fatal(err)
		}
		if resp.Decision != protocol.ApprovalApprove || !resp.Approval || resp.Auto != tc.auto {
			t.Errorf("sent %q: the daemon got %+v", tc.sent, resp)
		}
		_ = clientEnd.Close()
		_ = daemonEnd.Close()
	}
}

// EDITS ARE APPLIED AS Y WOULD APPLY THEM (owner's choice), backups and all,
// and the summary names each file, since nobody read the diff.
func TestAutoApproveAppliesEditsAndNamesThem(t *testing.T) {
	root := realTempDir(t)
	writeTempFile(t, root, "foo.go", "package main\n\nfunc old() {}\n")
	m := newTestModelWithRoot(root)
	m = typeText(m, "fix")
	m, _ = pressEnter(m)
	m.autoApprove = true
	next, _ := m.Update(tokenMsg(editBlockText("foo.go", "func old() {}", "func new_() {}")))
	next, _ = next.(chatModel).Update(streamDoneMsg{})
	m = next.(chatModel)
	if m.state != stateIdle {
		t.Fatalf("the edit waited for review: state %v", m.state)
	}
	if got := readFileString(t, filepath.Join(root, "foo.go")); !strings.Contains(got, "func new_() {}") {
		t.Errorf("foo.go was not edited: %q", got)
	}
	summary := lastSystemText(m.turns)
	if !strings.Contains(summary, "1 applied") || !strings.Contains(summary, "auto-applied: foo.go") || !strings.Contains(summary, "edits undo") {
		t.Errorf("the summary does not name what auto-approve applied:\n%s", summary)
	}
}

// /auto does what ctrl+space does, where the terminal or the system takes that
// key, and says what it now does.
func TestTheAutoCommand(t *testing.T) {
	m := newTestModel()
	for _, step := range []struct {
		typed string
		on    bool
		says  string
	}{
		{"/auto on", true, "auto-approve is on"},
		{"/auto", false, "auto-approve is off"},
		{"/auto", true, "auto-approve is on"},
		{"/auto off", false, "auto-approve is off"},
		{"/auto maybe", false, "usage: /auto"},
	} {
		m = typeText(m, step.typed)
		m, _ = pressEnter(m)
		if m.autoApprove != step.on || !strings.Contains(finalTurnText(m.turns), step.says) {
			t.Errorf("%q: on=%t, said %q", step.typed, m.autoApprove, finalTurnText(m.turns))
		}
	}
}

func finalTurnText(turns []turn) string {
	if len(turns) == 0 {
		return ""
	}
	return turns[len(turns)-1].text
}

// THE ROWS UNDER THE PROMPT (after Cline's): the model line, auto-approve,
// then the keys -- fewer as the window gets shorter. In the bottom right corner
// (owner, 2026-11-21) the project's folder name -- never a git branch -- and
// under it who is running the client. Auto-approve on is said above the prompt too, in every
// window.
//
// Neuter check: draw the label on the model line always and the second
// assertion fails.
func TestTheRowsUnderThePrompt(t *testing.T) {
	const who = "ravi-kiran@SCARX"
	m := newTestModelWithRoot("/home/someone/english_bot")
	m.whoami = who
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(chatModel)
	rows := bottomRows(m, 3)
	if !strings.HasPrefix(rows[1], autoMarkOff+" auto-approve off (ctrl+space)") || !strings.HasPrefix(rows[2], helpText) {
		t.Errorf("the rows under the prompt:\n%s", strings.Join(rows, "\n"))
	}
	if !strings.HasSuffix(rows[1], "  english_bot") || !strings.HasSuffix(rows[2], "  "+who) || strings.Contains(rows[0], "english_bot") {
		t.Errorf("the bottom right corner is not the project above who is running it:\n%s", strings.Join(rows, "\n"))
	}
	// Against the right edge, one column in: the last cell of the last row is
	// erased by the renderer in a real terminal (rightEdgeGap, whoami.go) --
	// measured in VTE 0.76, which showed "ravi-kiran@SCAR".
	if w := lipgloss.Width(rows[2]); w >= m.width {
		t.Errorf("the last row reaches the window's last column, which a real terminal loses: %q", rows[2])
	}
	for i, r := range rows[1:] {
		if w := lipgloss.Width(r); w != m.width-rightEdgeGap {
			t.Errorf("row %d is %d columns wide, want %d", i+1, w, m.width-rightEdgeGap)
		}
	}
	if got := len(strings.Split(m.View(), "\n")); got != m.height {
		t.Errorf("the screen is %d rows, want %d", got, m.height)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlAt})
	m = next.(chatModel)
	if row := bottomRows(m, 2)[0]; !strings.HasPrefix(row, autoMarkOn+" auto-approve on") {
		t.Errorf("on, the row says %q", row)
	}
	for _, h := range []int{30, statusAutoMinHeight, statusLineMinHeight, statusLineMinHeight - 1} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: h})
		v := ansi.Strip(next.(chatModel).View())
		if !strings.Contains(v, autoMarkOn+" auto") {
			t.Errorf("at %d rows, auto-approve being on is not on the screen:\n%s", h, v)
		}
		if strings.Contains(v, "english_bot") != (h >= statusLineMinHeight) {
			t.Errorf("at %d rows the project is drawn=%t", h, strings.Contains(v, "english_bot"))
		}
		if got := len(strings.Split(v, "\n")); got != h {
			t.Errorf("at %d rows the screen is %d rows", h, got)
		}
	}
}

// HOW FULL THE CONTEXT IS: a bar and the count against the model's limit,
// amber from three quarters, red from nine tenths; the count alone when the
// limit is unknown; nothing before a turn has said.
func TestContextGauge(t *testing.T) {
	m := newTestModel()
	if m.contextGauge() != "" {
		t.Error("a gauge before any turn")
	}
	for _, tc := range []struct {
		tokens, window int
		want           string
		colour         lipgloss.TerminalColor
	}{
		{3827, 128000, "3.8K/128.0K", colorGray},
		{100000, 128000, "100.0K/128.0K", colorAmber},
		{120000, 128000, "120.0K/128.0K", colorRed},
		{3827, 0, "3.8K context", nil},
	} {
		m.lastUsage = &protocol.TurnUsage{ContextTokens: tc.tokens, ContextWindow: tc.window}
		g := m.contextGauge()
		if !strings.Contains(ansi.Strip(g), tc.want) {
			t.Errorf("%d/%d: %q", tc.tokens, tc.window, g)
		}
		if tc.colour != nil {
			if got := gaugeFill(float64(tc.tokens) / float64(tc.window)).GetForeground(); got != tc.colour {
				t.Errorf("%d/%d: the bar is %v, want %v", tc.tokens, tc.window, got, tc.colour)
			}
		}
		if tc.window > 0 && strings.Count(ansi.Strip(g), "█") != 8 {
			t.Errorf("%d/%d: the bar is not 8 cells: %q", tc.tokens, tc.window, ansi.Strip(g))
		}
	}
}
