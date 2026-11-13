//go:build trace

package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"mochiii/clients/tui/tuitrace"
	"mochiii/protocol"
)

// THE TERMINAL CLIENT'S EMIT POINTS, ONE BY ONE, against a scripted daemon that
// records the bytes of the request it was sent. Compiled only under -tags trace
// (make trace-test runs it). The end-to-end test that drives the real client in a
// terminal against the real traced daemon, with the leak canaries, is
// daemon/tracetui_e2e_test.go.

// traceRun isolates the process's trace directory and turns the switch to value.
func traceRun(t *testing.T, value string) (traceFile string) {
	t.Helper()
	tuitrace.Reset()
	rt, err := os.MkdirTemp("/tmp", "tte")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tuitrace.Reset(); _ = os.RemoveAll(rt) })
	t.Setenv("XDG_RUNTIME_DIR", rt)
	t.Setenv("MOCHIII_TRACE", value)
	dir, err := protocol.SocketDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "trace", fmt.Sprintf("tui-%d.jsonl", os.Getpid()))
}

func readTrace(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

var reStageStatus = regexp.MustCompile(`"turn":"([0-9a-f]{8})","n":(\d+),"stage":"([a-z_]+)","status":"([a-z]+)","dur_ms":\d+,"tool":"([^"]*)","call":0,"err":"([^"]*)"`)

// shape is each line as "stage status tool err" (turn and sequence checked on the way).
func shape(t *testing.T, lines []string) (turn string, out []string) {
	t.Helper()
	for i, l := range lines {
		m := reStageStatus.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("a line that is not in the client's template: %s", l)
		}
		if turn == "" {
			turn = m[1]
		}
		if m[1] != turn {
			t.Errorf("line %d has turn %s, the first had %s", i+1, m[1], turn)
		}
		if m[2] != fmt.Sprint(i+1) {
			t.Errorf("line %d has n=%s", i+1, m[2])
		}
		out = append(out, strings.TrimSpace(fmt.Sprintf("%s %s %s %s", m[3], m[4], m[5], m[6])))
	}
	return
}

// rawDaemon is a scripted daemon: a real listener and lockfile, handshake as given,
// the request's raw bytes sent to requests, then script decides what is answered.
func rawDaemon(t *testing.T, handshake protocol.HandshakeResponse, script func(enc *json.Encoder, dec *json.Decoder, conn net.Conn)) (requests chan []byte) {
	t.Helper()
	lock, ln := fakeLock(t)
	t.Cleanup(setLockPathForTest(t, lock))
	requests = make(chan []byte, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				dec, enc := json.NewDecoder(conn), json.NewEncoder(conn)
				var hs protocol.HandshakeRequest
				if dec.Decode(&hs) != nil {
					return
				}
				if enc.Encode(handshake) != nil {
					return
				}
				var raw json.RawMessage
				if dec.Decode(&raw) != nil {
					return
				}
				requests <- append([]byte(nil), raw...)
				script(enc, dec, conn)
			}()
		}
	}()
	return requests
}

func fakeLock(t *testing.T) (string, net.Listener) {
	t.Helper()
	dir := t.TempDir()
	addr := testAddress(t)
	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	lockPath := filepath.Join(dir, "daemon.lock")
	data, _ := json.Marshal(protocol.LockFile{Address: addr, PID: os.Getpid()})
	if err := os.WriteFile(lockPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return lockPath, ln
}

var okHandshake = protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true}

func tok(s string) protocol.TokenResponse {
	return protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Token: s}
}

func done() protocol.TokenResponse {
	return protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true}
}

// stream runs one prompt turn through the real streamPromptWith, answering any
// approval with decision, and returns what the UI would have been sent, last.
func stream(t *testing.T, ctx context.Context, tr *tuitrace.Turn, decision string) tea.Msg {
	t.Helper()
	ch := make(chan tea.Msg)
	finished := make(chan struct{})
	go func() {
		streamPromptWith(ctx, "test-client", "/workspace", "say hi", "", "", "", "", nil, nil, nil, "", taskFields{trace: tr}, ch)
		close(finished)
	}()
	for {
		select {
		case msg := <-ch:
			switch m := msg.(type) {
			case toolApprovalMsg:
				m.reply <- decision
			case streamDoneMsg, streamErrMsg:
				// The goroutine writes result_rendered just after the hand-off; wait for
				// it to return so the file is complete when the test reads it.
				select {
				case <-finished:
				case <-time.After(10 * time.Second):
					t.Fatal("the stream goroutine never returned")
				}
				return msg
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the stream never ended")
			return nil
		}
	}
}

func TestASuccessfulTurnWritesDialRequestSentAndResultRendered(t *testing.T) {
	file := traceRun(t, "1")
	requests := rawDaemon(t, okHandshake, func(enc *json.Encoder, _ *json.Decoder, _ net.Conn) {
		_ = enc.Encode(tok("hello"))
		_ = enc.Encode(done())
	})
	tr := tuitrace.Begin()
	if _, isDone := stream(t, context.Background(), tr, "approve").(streamDoneMsg); !isDone {
		t.Fatal("the turn did not end in a done")
	}
	turn, got := shape(t, readTrace(t, file))
	want := []string{"dial start prompt", "dial ok prompt", "request_sent start prompt", "request_sent ok prompt", "result_rendered ok"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("the lines were\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	// The id on the lines is the id the request carried: that is the join.
	var req map[string]any
	if err := json.Unmarshal(<-requests, &req); err != nil {
		t.Fatal(err)
	}
	if req["trace"] != turn {
		t.Errorf("the request names the turn %v, the client's lines say %s", req["trace"], turn)
	}
}

func TestAnErrorDoneIsRenderedAsAFailureWithTheClosedClassOrOther(t *testing.T) {
	for class, want := range map[string]string{
		"rate_limited":                "result_rendered fail  rate_limited",
		"plainzebra-classcanary-4d1e": "result_rendered fail  other", // text is never a class
		"":                            "result_rendered fail  other",
	} {
		file := traceRun(t, "1")
		rawDaemon(t, okHandshake, func(enc *json.Encoder, _ *json.Decoder, _ net.Conn) {
			_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, Done: true, Error: "provider said no", ErrorClass: class})
		})
		if _, isErr := stream(t, context.Background(), tuitrace.Begin(), "approve").(streamErrMsg); !isErr {
			t.Fatal("the turn did not end in an error")
		}
		_, got := shape(t, readTrace(t, file))
		if last := got[len(got)-1]; last != strings.Join(strings.Fields(want), " ") && last != want {
			t.Errorf("class %q: last line %q, want %q", class, last, want)
		}
		if b, _ := os.ReadFile(file); strings.Contains(string(b), "provider said no") || strings.Contains(string(b), "classcanary") {
			t.Errorf("text from the daemon's error is in the trace:\n%s", b)
		}
	}
}

func TestAnApprovalShownIsRecordedByTheNameTheDaemonSent(t *testing.T) {
	file := traceRun(t, "1")
	rawDaemon(t, okHandshake, func(enc *json.Encoder, dec *json.Decoder, _ net.Conn) {
		_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, ToolApproval: &protocol.ToolApprovalRequest{
			CallID: "model-chose-this-plainzebra-callid", Server: "builtin", Tool: "read_file", Arguments: `{"path":"plainzebra-argcanary"}`, ArgumentsSHA256: "00",
		}})
		var resp protocol.ToolApprovalResponse
		_ = dec.Decode(&resp)
		_ = enc.Encode(tok("ok"))
		_ = enc.Encode(done())
	})
	stream(t, context.Background(), tuitrace.Begin(), protocol.ApprovalDeny)
	_, got := shape(t, readTrace(t, file))
	want := []string{"dial start prompt", "dial ok prompt", "request_sent start prompt", "request_sent ok prompt", "approval_shown ok builtin__read_file", "result_rendered ok"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("the lines were\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	b, _ := os.ReadFile(file)
	for _, leak := range []string{"plainzebra", "model-chose", "path"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("%q from the approval question is in the trace", leak)
		}
	}
}

// A tool name the daemon sent that is not an id (it cannot be, but the client must
// not trust that) is unknown_tool.
func TestAnApprovalWithAToolNameThatIsNotAnIDIsUnknownTool(t *testing.T) {
	file := traceRun(t, "1")
	rawDaemon(t, okHandshake, func(enc *json.Encoder, dec *json.Decoder, _ net.Conn) {
		_ = enc.Encode(protocol.TokenResponse{ProtocolVersion: protocol.ProtocolVersion, ToolApproval: &protocol.ToolApprovalRequest{
			CallID: "c", Server: "builtin", Tool: "read the secret file please", ArgumentsSHA256: "00",
		}})
		var resp protocol.ToolApprovalResponse
		_ = dec.Decode(&resp)
		_ = enc.Encode(done())
	})
	stream(t, context.Background(), tuitrace.Begin(), protocol.ApprovalApprove)
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), `"stage":"approval_shown","status":"ok","dur_ms":0,"tool":"unknown_tool"`) || strings.Contains(string(b), "secret") {
		t.Errorf("trace:\n%s", b)
	}
}

// THE HANG SHAPE: request_sent with nothing after it. The daemon read the request
// and said nothing; the trace says exactly that until the turn is stopped, and then
// it says the person stopped it.
func TestARequestThatIsNeverAnsweredIsRequestSentAndNothingAfter(t *testing.T) {
	file := traceRun(t, "1")
	rawDaemon(t, okHandshake, func(_ *json.Encoder, _ *json.Decoder, conn net.Conn) {
		buf := make([]byte, 1)
		_, _ = conn.Read(buf) // blocks until the client closes
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan tea.Msg)
	finished := make(chan struct{})
	go func() {
		streamPromptWith(ctx, "test-client", "/workspace", "say hi", "", "", "", "", nil, nil, nil, "", taskFields{trace: tuitrace.Begin()}, ch)
		close(finished)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for len(readTrace(t, file)) < 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond) // long enough for a line to appear if one were going to
	_, got := shape(t, readTrace(t, file))
	if want := "dial start prompt|dial ok prompt|request_sent start prompt"; strings.Join(got, "|") != want {
		t.Fatalf("a hung request should be exactly %q, got %q", want, strings.Join(got, "|"))
	}
	cancel()
	<-finished
	_, got = shape(t, readTrace(t, file))
	if last := got[len(got)-1]; last != "request_sent fail prompt user_cancelled" {
		t.Errorf("after the person stopped it the last line is %q", last)
	}
}

func TestAnAnsweredTurnThatIsStoppedFailsResultRenderedAsUserCancelled(t *testing.T) {
	file := traceRun(t, "1")
	rawDaemon(t, okHandshake, func(enc *json.Encoder, _ *json.Decoder, conn net.Conn) {
		_ = enc.Encode(tok("partial"))
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan tea.Msg)
	finished := make(chan struct{})
	go func() {
		streamPromptWith(ctx, "test-client", "/workspace", "say hi", "", "", "", "", nil, nil, nil, "", taskFields{trace: tuitrace.Begin()}, ch)
		close(finished)
	}()
	<-ch // the first token reaches the UI, and the daemon has answered
	cancel()
	<-finished
	_, got := shape(t, readTrace(t, file))
	if last := got[len(got)-1]; last != "result_rendered fail  user_cancelled" && last != "result_rendered fail user_cancelled" {
		t.Errorf("last line %q; all: %v", last, got)
	}
	if strings.Contains(strings.Join(got, "|"), "request_sent fail") {
		t.Errorf("an answered request must not be failed as unanswered: %v", got)
	}
}

// The three ways connectToDaemon fails, and the fourth class for the rest.
func TestADialFailureHasOneOfTheClosedClasses(t *testing.T) {
	type tc struct {
		name, class string
		setup       func(t *testing.T)
	}
	for _, c := range []tc{
		{"no lockfile", "no_lockfile", func(t *testing.T) {
			t.Cleanup(setLockPathForTest(t, filepath.Join(t.TempDir(), "plainzebra-absent.lock")))
		}},
		{"lockfile but nobody listening", "unreachable", func(t *testing.T) {
			lock, ln := fakeLock(t)
			_ = ln.Close()
			t.Cleanup(setLockPathForTest(t, lock))
		}},
		{"handshake refused", "handshake_rejected", func(t *testing.T) {
			rawDaemon(t, protocol.HandshakeResponse{ProtocolVersion: 99, Ok: false, Error: "plainzebra-refusal-text"}, func(*json.Encoder, *json.Decoder, net.Conn) {})
		}},
		{"the daemon hangs up during the handshake", "other", func(t *testing.T) {
			lock, ln := fakeLock(t)
			t.Cleanup(setLockPathForTest(t, lock))
			go func() {
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					_ = conn.Close()
				}
			}()
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			file := traceRun(t, "1")
			c.setup(t)
			if _, isErr := stream(t, context.Background(), tuitrace.Begin(), "approve").(streamErrMsg); !isErr {
				t.Fatal("the turn did not fail")
			}
			_, got := shape(t, readTrace(t, file))
			want := []string{"dial start prompt", "dial fail prompt " + c.class}
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("the lines were %q, want %q: a failed dial ends the turn's trace", got, want)
			}
			if b, _ := os.ReadFile(file); strings.Contains(string(b), "plainzebra") || strings.Contains(string(b), "lockfile at") {
				t.Errorf("the error's text is in the trace:\n%s", b)
			}
		})
	}
}

// The error a failed dial returns is what it was: same text, same chain.
func TestTheDialErrorsAreExactlyWhatTheyWereWithTheTagOn(t *testing.T) {
	traceRun(t, "1")
	missing := filepath.Join(t.TempDir(), "absent.lock")
	t.Cleanup(setLockPathForTest(t, missing))
	_, err := connectToDaemon("test-client")
	if err == nil || err.Error() != "daemon not found (expected a lockfile at "+missing+"; start it with: mochiii-daemon): open "+missing+": no such file or directory" {
		t.Errorf("the error is %q", err)
	}
	if !os.IsNotExist(unwrapAll(err)) {
		t.Errorf("errors.Is(os.ErrNotExist) no longer reaches through it: %v", err)
	}
	if dialClass(err) != tuitrace.ClassNoLockfile || dialClass(fmt.Errorf("x")) != tuitrace.ClassOther {
		t.Errorf("classes: %v, %v", dialClass(err), dialClass(fmt.Errorf("x")))
	}
}

func unwrapAll(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
}

// THE REQUEST'S BYTES. With the switch off the request is byte-for-byte the one a
// build with no trace code sends (no "trace" key); with it on, the same bytes plus
// that one key.
func TestTheRequestIsTheSameBytesWithTheSwitchOffAndOneKeyLongerWithItOn(t *testing.T) {
	capture := func(value string) string {
		traceRun(t, value)
		requests := rawDaemon(t, okHandshake, func(enc *json.Encoder, _ *json.Decoder, _ net.Conn) {
			_ = enc.Encode(done())
		})
		stream(t, context.Background(), tuitrace.Begin(), "approve")
		return string(<-requests)
	}
	off, on := capture(""), capture("1")
	if strings.Contains(off, `"trace"`) {
		t.Errorf("the switch is off and the request carries a trace key: %s", off)
	}
	re := regexp.MustCompile(`,"trace":"[0-9a-f]{8}"`)
	if !re.MatchString(on) {
		t.Fatalf("the switch is on and the request has no valid trace id: %s", on)
	}
	if re.ReplaceAllString(on, "") != off {
		t.Errorf("with the id removed the traced request is not the untraced one:\n on : %s\n off: %s", on, off)
	}
	// And what "off" is: the pre-field encoding of the same PromptRequest.
	want, _ := json.Marshal(protocol.PromptRequest{ProtocolVersion: protocol.ProtocolVersion, Prompt: "say hi", Workspace: "/workspace", Session: clientSession})
	if off != string(want) {
		t.Errorf("the untraced request is not what protocol.PromptRequest marshals to:\n got : %s\n want: %s", off, want)
	}
}

// --- commands -------------------------------------------------------------------

func enter(t *testing.T, m chatModel, line string) (chatModel, tea.Cmd) {
	t.Helper()
	m = typeText(m, line)
	return pressEnter(m)
}

func TestACommandIsRecordedByItsCatalogNameAndNeverItsArguments(t *testing.T) {
	file := traceRun(t, "1")
	const canary = "plainzebra-cmdargcanary-77ab"
	m := newTestModel()
	m, _ = enter(t, m, "/help "+canary)
	m, _ = enter(t, m, "/context")
	m, _ = enter(t, m, "/"+canary) // not a command at all
	m, _ = enter(t, m, "/MCP-SERVER add "+canary)
	_ = m
	lines := readTrace(t, file)
	var all []string
	for _, l := range lines {
		all = append(all, reStageStatus.FindStringSubmatch(l)[3]+" "+reStageStatus.FindStringSubmatch(l)[5])
	}
	want := []string{
		"command_parsed help", "command_local_done help",
		"command_parsed context", "command_local_done context",
		"command_parsed unknown", "command_local_done unknown",
		"command_parsed mcp-server", "command_local_done mcp-server", // the argument is not recorded
	}
	if strings.Join(all, "|") != strings.Join(want, "|") {
		t.Errorf("the lines were\n  %s\nwant\n  %s", strings.Join(all, "\n  "), strings.Join(want, "\n  "))
	}
	if b, _ := os.ReadFile(file); strings.Contains(string(b), "plainzebra") {
		t.Errorf("a typed argument or command name is in the trace:\n%s", b)
	}
}

func TestEachEnterIsItsOwnTurn(t *testing.T) {
	file := traceRun(t, "1")
	m := newTestModel()
	m, _ = enter(t, m, "/help")
	m, _ = enter(t, m, "/help")
	_ = m
	ids := map[string]int{}
	for _, l := range readTrace(t, file) {
		ids[reStageStatus.FindStringSubmatch(l)[1]]++
	}
	if len(ids) != 2 {
		t.Errorf("two commands, turn ids %v", ids)
	}
	for id, n := range ids {
		if n != 2 {
			t.Errorf("turn %s has %d lines, want command_parsed and command_local_done", id, n)
		}
	}
}

// A steered command (/fix ...) is parsed, and then it is a request: it carries the
// same turn into the dial.
func TestASteeredCommandKeepsItsTurnIntoTheRequest(t *testing.T) {
	file := traceRun(t, "1")
	t.Cleanup(setLockPathForTest(t, filepath.Join(t.TempDir(), "absent.lock")))
	m := newTestModel()
	m.needsAPIKey = false
	_, cmd := enter(t, m, "/fix the parser")
	if cmd == nil {
		t.Fatal("the command started nothing")
	}
	// startStream is a tea.Cmd that starts the goroutine and returns the first
	// message; run the batch's commands until the stream's first message arrives.
	runCmdUntil(t, cmd, func(msg tea.Msg) bool { _, ok := msg.(streamErrMsg); return ok })
	turn, got := shape(t, readTrace(t, file))
	want := []string{"command_parsed ok fix", "dial start prompt", "dial fail prompt no_lockfile"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("the lines were %q, want %q", got, want)
	}
	if turn == "" {
		t.Error("no turn id")
	}
}

// runCmdUntil runs cmd (flattening a batch) in goroutines and returns when a
// message satisfying stop arrives.
func runCmdUntil(t *testing.T, cmd tea.Cmd, stop func(tea.Msg) bool) {
	t.Helper()
	msgs := make(chan tea.Msg, 16)
	var run func(c tea.Cmd)
	var wg sync.WaitGroup
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			msg := c()
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, bc := range batch {
					run(bc)
				}
				return
			}
			msgs <- msg
		}()
	}
	run(cmd)
	deadline := time.After(10 * time.Second)
	for {
		select {
		case msg := <-msgs:
			if stop(msg) {
				return
			}
		case <-deadline:
			t.Fatal("the stream never produced the message the test waits for")
		}
	}
}

// NOTHING PER KEYSTROKE, NOTHING PER RENDER, NOTHING FROM /connect's KEY ENTRY.
func TestNoKeystrokeNoRenderAndNoConnectKeyEntryWritesALine(t *testing.T) {
	file := traceRun(t, "1")
	const key = "sk-ant-api03-CONNECTCANARYdummyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	m := newTestModel()
	m = typeText(m, "an unsent line of typing, then some more")
	for i := 0; i < 25; i++ {
		_ = m.View()
	}
	if lines := readTrace(t, file); len(lines) != 0 {
		t.Fatalf("typing and rendering wrote %d lines: %v", len(lines), lines)
	}
	m.input.SetValue("")
	m, _ = enter(t, m, "/connect")
	before := len(readTrace(t, file))
	if before != 2 {
		t.Fatalf("/connect wrote %d lines, want command_parsed and command_local_done", before)
	}
	// The key prompt itself (connect.go): opened, typed into, and submitted. This is
	// where a key is typed, and it is not a command line, so nothing here is traced.
	started, _ := m.beginConnect()
	m = started.(chatModel)
	if m.state != stateConnect {
		t.Fatalf("beginConnect left state %v", m.state)
	}
	m = typeInto(m, key)
	for i := 0; i < 10; i++ {
		_ = m.View()
	}
	next, _ := m.handleConnectKey(tea.KeyMsg{Type: tea.KeyEnter})
	_ = next
	if after := readTrace(t, file); len(after) != before {
		t.Errorf("the key entry wrote lines: %v", after[before:])
	}
	if b, _ := os.ReadFile(file); strings.Contains(string(b), "CANARY") || strings.Contains(string(b), "sk-ant") {
		t.Errorf("the key is in the trace:\n%s", b)
	}
}

// A refused /connect with a key as its argument records the command, not the key.
func TestAConnectWithAKeyAsAnArgumentRecordsTheCommandOnly(t *testing.T) {
	file := traceRun(t, "1")
	const key = "plainzebra-connectkeycanary-5a90"
	m := newTestModel()
	enter(t, m, "/connect "+key)
	b, _ := os.ReadFile(file)
	if strings.Contains(string(b), "plainzebra") {
		t.Errorf("the key is in the trace:\n%s", b)
	}
	if !strings.Contains(string(b), `"stage":"command_parsed","status":"ok","dur_ms":0,"tool":"connect"`) {
		t.Errorf("trace:\n%s", b)
	}
}

// With the switch off, nothing: no file, and a /help is the same as without tracing.
func TestWithTheSwitchOffACommandWritesNothing(t *testing.T) {
	file := traceRun(t, "")
	m := newTestModel()
	m, _ = enter(t, m, "/help")
	_ = m
	if _, err := os.Stat(filepath.Dir(file)); err == nil {
		t.Errorf("%s exists with the switch off", filepath.Dir(file))
	}
}
