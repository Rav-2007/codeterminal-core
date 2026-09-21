package main

import (
	"bytes"
	"encoding/json"

	tea "github.com/charmbracelet/bubbletea"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mochiii/editapply"
	"mochiii/protocol"
)

// ASSERTED AT THE OUTPUT WRITER, NOT AT THE FILTER.
//
// Every test in this file drives the real model and then reads the bytes the
// program would actually put on the terminal -- View() for the chat UI, the
// captured writer for one-shot. A test that called sanitizeText and checked
// its return value would prove only that the filter works; these prove it is
// REACHED, which is the half that a refactor breaks.
//
// viewViolation reports the first escape in a rendered frame that is not an
// allowed SGR. The styles this client applies itself emit SGR, so the frame is
// never escape-free -- the question is only ever whether anything OUTSIDE the
// allowlist survived.
func viewViolation(t *testing.T, frame string) string {
	t.Helper()
	return sanEscapeViolation(frame)
}

// streamTokens feeds s to a live model one byte at a time, which is the most
// hostile chunking available and the split the far end would choose.
func streamTokens(m chatModel, s string) chatModel {
	for i := 0; i < len(s); i++ {
		updated, _ := m.Update(tokenMsg(s[i : i+1]))
		m = updated.(chatModel)
	}
	return m
}

func TestHostileTokensNeverReachTheTerminal(t *testing.T) {
	for _, tc := range sanCorpus {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			m = typeText(m, "hi")
			m, _ = pressEnter(m)
			m = streamTokens(m, tc.in)

			if v := viewViolation(t, m.View()); v != "" {
				t.Fatalf("%s survived to the rendered frame: %s", tc.name, v)
			}
			// And after the stream closes, when Flush releases whatever the
			// parser was still holding.
			updated, _ := m.Update(streamDoneMsg{})
			m = updated.(chatModel)
			if v := viewViolation(t, m.View()); v != "" {
				t.Fatalf("%s survived a Flush into the rendered frame: %s", tc.name, v)
			}
		})
	}
}

func TestHostileReasoningNeverReachesTheTerminal(t *testing.T) {
	for _, tc := range sanCorpus {
		m := newTestModel()
		m = typeText(m, "hi")
		m, _ = pressEnter(m)
		for i := 0; i < len(tc.in); i++ {
			updated, _ := m.Update(reasoningMsg{text: tc.in[i : i+1]})
			m = updated.(chatModel)
		}
		if v := viewViolation(t, m.View()); v != "" {
			t.Errorf("reasoning %s: %s", tc.name, v)
		}
	}
}

// The answer and the reasoning are separate streams on one channel. Sharing a
// parser between them would let a sequence opened in one be closed by the
// other -- a bypass built out of our own multiplexing.
func TestAnswerAndReasoningDoNotShareParserState(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)

	updated, _ := m.Update(tokenMsg("\x1b[")) // answer opens a sequence
	m = updated.(chatModel)
	updated, _ = m.Update(reasoningMsg{text: "2J"}) // reasoning tries to close it
	m = updated.(chatModel)
	updated, _ = m.Update(tokenMsg("done"))
	m = updated.(chatModel)
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	if v := viewViolation(t, m.View()); v != "" {
		t.Fatalf("cross-stream sequence completion: %s", v)
	}
	if got := m.turns[1].text; strings.Contains(got, "\x1b") {
		t.Fatalf("answer turn holds a raw escape: %q", got)
	}
}

// A stream that stops mid-sequence must not silently lose the tail of the
// answer -- the held bytes are the user's text.
func TestFlushOnStreamEndDoesNotLoseText(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)
	m = streamTokens(m, "the answer\x1b[3")
	if got := m.turns[1].text; got != "the answer" {
		t.Fatalf("before end: %q", got)
	}
	updated, _ := m.Update(streamDoneMsg{})
	m = updated.(chatModel)
	if got, want := m.turns[1].text, "the answer[3"; got != want {
		t.Fatalf("after end: got %q want %q", got, want)
	}
}

// A second turn must not inherit a half-read sequence from the first.
func TestParserStateDoesNotCarryAcrossTurns(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "one")
	m, _ = pressEnter(m)
	m = streamTokens(m, "first\x1b[")
	updated, _ := m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	m = typeText(m, "two")
	m, _ = pressEnter(m)
	m = streamTokens(m, "2Jsecond")
	updated, _ = m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	last := m.turns[len(m.turns)-1]
	if !strings.Contains(last.text, "2Jsecond") {
		t.Fatalf("the second turn lost text to a stale parser: %q", last.text)
	}
	if v := viewViolation(t, m.View()); v != "" {
		t.Fatalf("%s", v)
	}
}

// THE CONSENT SURFACE. An escape here could repaint the question the user is
// answering, which is not a display bug -- it is forged consent.
func TestApprovalPanelCannotBeRepainted(t *testing.T) {
	for _, tc := range sanCorpus {
		req := protocol.ToolApprovalRequest{
			Server:        "srv" + tc.in,
			Tool:          "tool",
			Arguments:     `{"path": "` + tc.in + `"}`,
			Iteration:     1,
			MaxIterations: 5,
		}
		if v := sanEscapeViolation(renderApprovalPanel(req)); v != "" {
			t.Errorf("approval panel, %s: %s", tc.name, v)
		}
	}
}

// The request itself must stay byte-exact: the daemon binds its approval to a
// digest of exactly these bytes, so filtering the stored copy would break the
// binding rather than protect it.
func TestApprovalRequestIsNotMutated(t *testing.T) {
	const hostile = "a\x1b[2Jb"
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)
	reply := make(chan string, 1)
	updated, _ := m.Update(toolApprovalMsg{
		req:   protocol.ToolApprovalRequest{Server: "s", Tool: "t", Arguments: hostile},
		reply: reply,
	})
	m = updated.(chatModel)
	if m.pendingApproval == nil {
		t.Fatal("no pending approval")
	}
	if m.pendingApproval.Arguments != hostile {
		t.Fatalf("the stored request was mutated: %q", m.pendingApproval.Arguments)
	}
	if v := viewViolation(t, m.View()); v != "" {
		t.Fatalf("but it still reached the terminal: %s", v)
	}
}

// The other consent surface. The edit block stays byte-exact because those
// bytes get written to disk; only the rendering is filtered.
func TestReviewPanelIsFilteredButTheEditIsNot(t *testing.T) {
	const hostile = "x\x1b[2Jy"
	p := &editapply.PreparedEdit{
		Block:      editapply.EditBlock{FilePath: "a" + hostile + ".go", Search: hostile, Replace: hostile},
		StartLine:  1,
		EndLine:    2,
		MatchNote:  hostile,
		SyntaxNote: hostile,
	}
	if v := sanEscapeViolation(renderReviewPanel(0, 1, p)); v != "" {
		t.Fatalf("review panel: %s", v)
	}
	if p.Block.Replace != hostile {
		t.Fatalf("the edit that will be written to disk was altered: %q", p.Block.Replace)
	}
}

func TestDaemonErrorStringsAreFiltered(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)
	updated, _ := m.Update(streamErrMsg{err: errString("boom\x1b[2J\x1b]0;t\x07")})
	m = updated.(chatModel)
	if v := viewViolation(t, m.View()); v != "" {
		t.Fatalf("a daemon error string reached the terminal: %s", v)
	}
	if strings.Contains(m.statusErr, "\x1b") {
		t.Fatalf("statusErr holds a raw escape: %q", m.statusErr)
	}
}

func TestToolActivityLinesAreFiltered(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)
	updated, _ := m.Update(toolActivityMsg{activity: protocol.ToolActivity{
		Server: "evil\x1b[2J", Tool: "t\x1b]0;x\x07", Phase: protocol.ToolPhaseRunning, CallID: "1",
	}})
	m = updated.(chatModel)
	if v := viewViolation(t, m.View()); v != "" {
		t.Fatalf("a tool-activity line reached the terminal: %s", v)
	}
}

func TestHeaderNoticesAreFiltered(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)
	updated, _ := m.Update(providerMsg{provider: "prov\x1b[2J"})
	m = updated.(chatModel)
	updated, _ = m.Update(redactionsMsg{kinds: []string{"kind\x1b]0;x\x07"}})
	m = updated.(chatModel)
	updated, _ = m.Update(degradedMsg{items: []protocol.Degradation{{Component: "c\x1b[H", Detail: "d\x1b[2J"}}})
	m = updated.(chatModel)
	if v := viewViolation(t, m.View()); v != "" {
		t.Fatalf("a header notice reached the terminal: %s", v)
	}
}

// Hydrated history is on screen before the user has typed anything at all.
func TestHydratedHistoryIsFiltered(t *testing.T) {
	turns := turnsFromProtocol([]protocol.Turn{
		{Role: "user", Content: "q\x1b[2J"},
		{Role: "assistant", Content: "a\x1b]0;t\x07"},
	})
	for _, tn := range turns {
		if strings.Contains(tn.text, "\x1b") {
			t.Fatalf("hydrated turn holds a raw escape: %q", tn.text)
		}
	}
}

func TestPastedPromptIsFiltered(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "look at \x1b[2J this")
	m, _ = pressEnter(m)
	if len(m.turns) == 0 {
		t.Fatal("no turn was recorded")
	}
	if strings.Contains(m.turns[0].text, "\x1b") {
		t.Fatalf("the user turn holds a raw escape: %q", m.turns[0].text)
	}
	if v := viewViolation(t, m.View()); v != "" {
		t.Fatalf("%s", v)
	}
}

// One-shot mode writes to a terminal too, with no viewport in between.
func TestOneShotOutputIsFiltered(t *testing.T) {
	oneShotPayloadDaemon(t, "before\x1b[2Jafter\x1b]0;t\x07")

	var out, errBuf bytes.Buffer
	code := runOneShotPrompt("test-client", "hi", oneShotIO{
		in: strings.NewReader(""), out: &out, err: &errBuf, interactive: false,
	})
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errBuf.String())
	}
	if v := sanEscapeViolation(out.String()); v != "" {
		t.Fatalf("one-shot stdout: %s\ngot %q", v, out.String())
	}
	if got := out.String(); !strings.Contains(got, "beforeafter") {
		t.Fatalf("the answer text was lost: %q", got)
	}
}

// oneShotPayloadDaemon answers a handshake and streams payload one byte at a
// time, so the one-shot path is exercised with the same hostile chunking the
// chat path gets.
func oneShotPayloadDaemon(t *testing.T, payload string) {
	t.Helper()
	dir := t.TempDir()
	addr := testAddress(t)
	lockPath := filepath.Join(dir, "daemon.lock")

	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	data, _ := json.Marshal(protocol.LockFile{Address: addr, PID: os.Getpid()})
	if err := os.WriteFile(lockPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(setLockPathForTest(t, lockPath))

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
		var hs protocol.HandshakeRequest
		if dec.Decode(&hs) != nil {
			return
		}
		if enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true}) != nil {
			return
		}
		var req protocol.PromptRequest
		if dec.Decode(&req) != nil {
			return
		}
		for i := 0; i < len(payload); i++ {
			if enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Token: payload[i : i+1]}) != nil {
				return
			}
		}
		_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true})
	}()
}

// errString is an error whose message is exactly the string, so a test can
// plant hostile bytes on the error path.
type errString string

func (e errString) Error() string { return string(e) }

// THE FOUR APPENDS THE FIRST PASS MISSED. Each of these carries text chosen by
// something that is not the user into the transcript, and each went in raw
// until appendTurn became the only door. They are separate tests rather than a
// table so a failure names which door was left open.

func TestApprovalOutcomeLineIsFiltered(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)
	reply := make(chan string, 1)
	updated, _ := m.Update(toolApprovalMsg{
		req:   protocol.ToolApprovalRequest{Server: "srv\x1b]0;PWN\x07", Tool: "t\x1b[2J"},
		reply: reply,
	})
	m = updated.(chatModel)
	// "n" denies, which is what writes the outcome line into the transcript.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(chatModel)

	for _, tn := range m.turns {
		if strings.Contains(tn.text, "\x1b") {
			t.Fatalf("the approval outcome line carried a raw escape: %q", tn.text)
		}
	}
	if v := viewViolation(t, m.View()); v != "" {
		t.Fatalf("%s", v)
	}
}

func TestIncompleteNoticeIsFiltered(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "hi")
	m, _ = pressEnter(m)
	m = streamTokens(m, "partial")
	updated, _ := m.Update(incompleteMsg{info: &protocol.IncompleteInfo{
		Reason: "length", Detail: "cut off\x1b]0;PWN\x07\x1b[2J",
	}})
	m = updated.(chatModel)
	for _, tn := range m.turns {
		if strings.Contains(tn.text, "\x1b") {
			t.Fatalf("the incomplete notice carried a raw escape: %q", tn.text)
		}
	}
	if v := viewViolation(t, m.View()); v != "" {
		t.Fatalf("%s", v)
	}
}

// A refused edit block puts the parser's reason in the transcript. This one
// is a FORWARD guard, not a closed hole: editapply formats the offending path
// with %q, which escapes control bytes, so the notice is clean today for a
// reason that has nothing to do with this filter. appendTurn is what keeps it
// clean if that verb ever becomes %s. The assertion that the notice actually
// appears is what stops the test going vacuous, which is how it was written
// the first time.
func TestRefusedEditBlockNoticeIsFiltered(t *testing.T) {
	m := newTestModelWithRoot(realTempDir(t))
	m = typeText(m, "hi")
	m, _ = pressEnter(m)
	// A SEARCH marker alone on its line with no "path:" line before it is
	// refused by ParseEditBlocks.
	m = streamTokens(m, "<<<<<<< SEARCH\nbroken\n")
	updated, _ := m.Update(streamDoneMsg{})
	m = updated.(chatModel)

	var sawRefusal bool
	for _, tn := range m.turns {
		if strings.Contains(tn.text, "refused") {
			sawRefusal = true
		}
		if strings.Contains(tn.text, "\x1b") {
			t.Fatalf("a refusal notice carried a raw escape: %q", tn.text)
		}
	}
	if !sawRefusal {
		t.Fatal("no refusal notice was produced, so this test asserted nothing")
	}
}

// THE C1 ROUTE, which is a live hole rather than a forward guard.
//
// editapply.RejectUnprintablePath refuses r < 0x20, DEL, and a named set of
// Unicode direction and zero-width characters. It does NOT refuse C1
// (U+0080-U+009F), and U+009B is a CSI introducer on a terminal in 8-bit
// mode. So a model-authored path carrying one parses cleanly, survives every
// upstream check, and lands in the review summary's refusal line -- which
// formats it with %s, raw.
func TestC1InAnEditPathCannotReachTheTranscript(t *testing.T) {
	const hostilePath = "a\u009b2Jb.go"
	if err := editapply.RejectUnprintablePath(hostilePath); err != nil {
		t.Skipf("editapply now refuses C1 as well, closing this route upstream: %v", err)
	}
	m := newTestModel()
	m.reviewRefusals = []string{hostilePath + ": no such file"}
	updated, _ := m.finishReview()
	m = updated.(chatModel)
	last := m.turns[len(m.turns)-1]
	if strings.ContainsRune(last.text, '\u009b') {
		t.Fatalf("a C1 introducer reached the transcript: %q", last.text)
	}
}

// The review summary lists refusal reasons, which name model-authored paths.
func TestReviewSummaryIsFiltered(t *testing.T) {
	m := newTestModel()
	m.reviewRefusals = []string{"a\x1b[2Jb", "c\x1b]0;PWN\x07d"}
	m.reviewBackupDir = "/tmp/x\x1b[2J"
	updated, _ := m.finishReview()
	m = updated.(chatModel)
	last := m.turns[len(m.turns)-1]
	if strings.Contains(last.text, "\x1b") {
		t.Fatalf("the review summary carried a raw escape: %q", last.text)
	}
}

// appendTurn is the door; this is the assertion that it actually closes.
func TestAppendTurnSanitizes(t *testing.T) {
	var m chatModel
	i := m.appendTurn(turn{role: roleAssistant, text: "a\x1b[2Jb", reasoning: "r\x1b]0;t\x07s"})
	if got := m.turns[i].text; got != "ab" {
		t.Errorf("text = %q, want %q", got, "ab")
	}
	if got := m.turns[i].reasoning; got != "rs" {
		t.Errorf("reasoning = %q, want %q", got, "rs")
	}
}
