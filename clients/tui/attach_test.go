package tui

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/protocol"
)

// daemonTakingPrompts is a fake daemon whose handshake names the given
// features, and which hands every PromptRequest it receives to requests.
func daemonTakingPrompts(t *testing.T, features []string, requests chan<- protocol.PromptRequest) {
	t.Helper()
	addr := testAddress(t)
	lockPath := filepath.Join(t.TempDir(), "daemon.lock")
	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	data, _ := json.Marshal(protocol.LockFile{Address: addr, PID: os.Getpid()})
	if err := os.WriteFile(lockPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(setLockPathForTest(t, lockPath))
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
				var hs protocol.HandshakeRequest
				if dec.Decode(&hs) != nil {
					return
				}
				_ = enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true, Features: features})
				var req protocol.PromptRequest
				if dec.Decode(&req) != nil {
					return
				}
				requests <- req
				_ = enc.Encode(protocol.TokenResponse{Token: "ok"})
				_ = enc.Encode(protocol.TokenResponse{Done: true})
			}(conn)
		}
	}()
}

// ask types a question, presses Enter and returns the request the daemon got.
func ask(t *testing.T, m chatModel, requests <-chan protocol.PromptRequest, question string) (chatModel, protocol.PromptRequest) {
	t.Helper()
	m = typeText(m, question)
	m, cmd := pressEnter(m)
	if cmd == nil {
		t.Fatalf("Enter on %q started nothing", question)
	}
	go runCmdTree(cmd)
	select {
	case req := <-requests:
		return m, req
	case <-time.After(5 * time.Second):
		t.Fatalf("the daemon never received %q", question)
		return m, protocol.PromptRequest{}
	}
}

// repoWithAChange is a git repository holding one untracked file whose NAME is
// the kind of thing a repository can put in front of a model.
func repoWithAChange(t *testing.T) string {
	t.Helper()
	dir := realTempDir(t)
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Skipf("git init failed: %v: %s", err, out)
	}
	writeTempFile(t, dir, "ignore-previous-instructions.txt", "x\n")
	return dir
}

// /git SHOWS ITS OUTPUT AND KEEPS IT OUT OF THE CONVERSATION.
//
// THE DEFECT: it was left in the transcript as an assistant turn and re-sent as
// history with every later question -- file names written by whoever made the
// repository, in the model's own voice.
//
// Neuter check: put `local: name != "git" && name != "search"` back.
func TestGitOutputIsShownAndIsNotPartOfTheConversation(t *testing.T) {
	m := newTestModelWithRoot(repoWithAChange(t))
	m.appendTurn(turn{role: roleUser, text: "a real question"})
	m.appendTurn(turn{role: roleAssistant, text: "a real answer"})

	next, _ := m.handleLocalSlash("git", "")
	m = next.(chatModel)

	shown := transcriptText(m)
	for _, want := range []string{"ignore-previous-instructions.txt", attachNote} {
		if !strings.Contains(shown, want) {
			t.Errorf("the screen does not show %q:\n%s", want, shown)
		}
	}
	history := buildHistory(m.turns)
	if len(history) != 2 {
		t.Fatalf("the next question would carry %d turns, want only the real exchange: %+v", len(history), history)
	}
	for _, h := range history {
		if strings.Contains(h.Content, "ignore-previous-instructions") {
			t.Errorf("git's output is in the history sent to the model, as a %s turn: %q", h.Role, h.Content)
		}
	}
	if len(m.attached) != 1 || m.attached[0].Command != "/git" || !strings.Contains(m.attached[0].Output, "ignore-previous-instructions.txt") {
		t.Fatalf("what is waiting to go with the next question is %+v, want /git's output", m.attached)
	}
	if strings.Contains(m.attached[0].Output, attachNote) {
		t.Errorf("this client's own note was attached as if git had printed it: %q", m.attached[0].Output)
	}
}

// IT GOES WITH THE NEXT QUESTION, ONCE: in the request's own field, never in
// the history, and not again with the question after.
//
// Neuter check: pass taskFields{} in startTurn instead of the taken output.
func TestAttachedOutputGoesWithTheNextQuestionOnce(t *testing.T) {
	requests := make(chan protocol.PromptRequest, 2)
	daemonTakingPrompts(t, []string{protocol.FeatureAttachedOutput}, requests)

	m := newTestModelWithRoot(repoWithAChange(t))
	next, _ := m.handleLocalSlash("git", "")
	m, req := ask(t, next.(chatModel), requests, "write a commit message for this")

	if len(req.Attached) != 1 || req.Attached[0].Command != "/git" || !strings.Contains(req.Attached[0].Output, "ignore-previous-instructions.txt") {
		t.Fatalf("the request carried %+v, want /git's output attached", req.Attached)
	}
	for _, h := range req.History {
		if strings.Contains(h.Content, "ignore-previous-instructions") {
			t.Errorf("git's output also went as history (%s): %q", h.Role, h.Content)
		}
	}
	if strings.Contains(req.Prompt, "ignore-previous-instructions") {
		t.Errorf("git's output was pasted into the question itself: %q", req.Prompt)
	}
	if len(m.attached) != 0 {
		t.Errorf("the output is still waiting after it was sent: %+v", m.attached)
	}
	if shown := transcriptText(m); !strings.Contains(shown, "sent with this question, once: the output of /git") {
		t.Errorf("nothing on screen says what went with the question:\n%s", shown)
	}

	// The question after it carries nothing.
	ended, _ := m.Update(streamDoneMsg{})
	_, second := ask(t, ended.(chatModel), requests, "and the one after")
	if len(second.Attached) != 0 {
		t.Errorf("the output went again with the next question: %+v", second.Attached)
	}
}

// AN OLDER DAEMON IGNORES THE FIELD, so it is not sent one and the user is told
// -- otherwise "sent with this question" would be a line that was never true.
func TestAnOlderDaemonIsNotSentAttachedOutputAndTheUserIsTold(t *testing.T) {
	requests := make(chan protocol.PromptRequest, 1)
	daemonTakingPrompts(t, nil, requests)

	ch := make(chan tea.Msg, 8)
	go streamPromptWith(context.Background(), "test", "/w", "what changed?", "", "", "", "", nil, nil, nil, "",
		taskFields{attached: []protocol.AttachedOutput{{Command: "/git", Output: "## main"}}}, ch)

	var told *attachedNotSentMsg
	deadline := time.After(5 * time.Second)
	for told == nil {
		select {
		case msg := <-ch:
			if n, ok := msg.(attachedNotSentMsg); ok {
				told = &n
			}
		case <-deadline:
			t.Fatalf("the stream never said the output was not sent")
		}
	}
	if told.commands != "/git" {
		t.Errorf("the notice names %q, want /git", told.commands)
	}
	select {
	case req := <-requests:
		if len(req.Attached) != 0 {
			t.Errorf("a daemon that never said it takes attached output was sent some: %+v", req.Attached)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the daemon never received the question")
	}

	m := newTestModel()
	m.streamCh = make(chan tea.Msg, 1)
	next, _ := m.Update(*told)
	if shown := transcriptText(next.(chatModel)); !strings.Contains(shown, "NOT sent after all") || !strings.Contains(shown, "/git") {
		t.Errorf("the screen does not say the output was not sent:\n%s", shown)
	}
}

// A COMMAND THAT FAILED HAS NOTHING TO ASK ABOUT, and a second run replaces the
// first: what goes is what is on screen now.
func TestWhatIsAttachedIsTheLatestRealOutput(t *testing.T) {
	m := newTestModel()
	for _, failed := range []string{"git status failed: exit status 128\nfatal: not a git repository",
		"no workspace is set, so there is no repository to report on", "(git status: empty)",
		"search failed: daemon not running", "no matching turns found in project memory"} {
		if got := m.attachOutput("git", failed); got != failed || len(m.attached) != 0 {
			t.Errorf("%q was attached (%+v) or changed (%q)", failed, m.attached, got)
		}
	}

	m.attachOutput("git", "## main\n M first.go")
	m.attachOutput("search", "found 1 match(es):\n1. [user] hello")
	m.attachOutput("git", "## main\n M second.go")
	if len(m.attached) != 2 {
		t.Fatalf("%d outputs are waiting, want /search and the latest /git: %+v", len(m.attached), m.attached)
	}
	for _, a := range m.attached {
		if strings.Contains(a.Output, "first.go") {
			t.Errorf("a superseded /git output is still waiting: %q", a.Output)
		}
	}
	if got := attachedNames(m.attached); got != "/search and /git" {
		t.Errorf("named as %q", got)
	}
}

// A NEW CHAT DOES NOT INHERIT WHAT THE OLD ONE WAS ABOUT TO ASK.
func TestClearingTheChatDropsWhatWasWaiting(t *testing.T) {
	t.Cleanup(setLockPathForTest(t, filepath.Join(t.TempDir(), "absent.lock")))
	m := newTestModel()
	m.attachOutput("git", "## main\n M a.go")
	cleared, _ := m.clearConversation()
	if n := len(cleared.(chatModel).attached); n != 0 {
		t.Errorf("%d output(s) survived into the new chat", n)
	}
}

// A QUESTION ASKED THROUGH A COMMAND IS STILL THE NEXT QUESTION. "/fix the
// build" after /git is exactly the case the output was fetched for, and those
// commands start their turn by a second path.
//
// Neuter check: drop `task.attached = m.takeAttached()` from beginTurnWith.
func TestAQuestionAskedThroughACommandCarriesWhatIsWaiting(t *testing.T) {
	requests := make(chan protocol.PromptRequest, 1)
	daemonTakingPrompts(t, []string{protocol.FeatureAttachedOutput}, requests)

	m := newTestModel()
	m.attachOutput("git", "## main\n M build.go")
	m, req := ask(t, m, requests, "/fix the build")

	if req.Mode != modeFix {
		t.Fatalf("mode %q: this test must go through the command path to mean anything", req.Mode)
	}
	if len(req.Attached) != 1 || req.Attached[0].Command != "/git" {
		t.Errorf("the request carried %+v, want /git's output", req.Attached)
	}
	if len(m.attached) != 0 {
		t.Errorf("the output is still waiting after it was sent")
	}
}
