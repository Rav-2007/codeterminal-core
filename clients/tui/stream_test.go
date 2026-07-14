package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"codeterminal/protocol"
)

// setLockPathForTest points connectToDaemon at a fake lockfile path for the
// duration of one test, restoring the real lockPathFunc afterward.
func setLockPathForTest(t *testing.T, path string) (restore func()) {
	t.Helper()
	original := lockPathFunc
	lockPathFunc = func() string { return path }
	return func() { lockPathFunc = original }
}

// fakeDaemonHandshakeThenHang starts a real Unix-socket listener that
// completes exactly one handshake and then blocks forever (never sends a
// TokenResponse) — a stand-in for a daemon that's mid-generation when the
// user quits. It writes a lockfile so connectToDaemon finds it exactly the
// way it finds the real daemon. Returns a cleanup func.
func fakeDaemonHandshakeThenHang(t *testing.T) (lockPath string, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")
	lockPath = filepath.Join(dir, "daemon.lock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listening on fake daemon socket: %v", err)
	}

	lock := protocol.LockFile{SocketPath: sockPath, PID: os.Getpid()}
	data, err := json.Marshal(lock)
	if err != nil {
		t.Fatalf("marshal lockfile: %v", err)
	}
	if err := os.WriteFile(lockPath, data, 0644); err != nil {
		t.Fatalf("writing lockfile: %v", err)
	}

	done := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		dec := json.NewDecoder(conn)
		enc := json.NewEncoder(conn)

		var hsReq protocol.HandshakeRequest
		if err := dec.Decode(&hsReq); err != nil {
			return
		}
		enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true})

		var promptReq protocol.PromptRequest
		if err := dec.Decode(&promptReq); err != nil {
			return
		}

		// Now hang: never write a TokenResponse, just like a daemon that's
		// mid-generation. Block until the connection is closed out from
		// under us (which is exactly what cancellation must trigger).
		buf := make([]byte, 1)
		conn.Read(buf) //nolint:errcheck // blocks until the peer closes; that's the point
		close(done)
	}()

	cleanup = func() {
		ln.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("fake daemon goroutine never observed the connection close")
		}
	}
	return lockPath, cleanup
}

// fakeDaemonCapturingRequests starts a real Unix-socket listener that
// handles any number of sequential connections (the wire protocol is one
// prompt per connection): it completes the handshake, decodes the
// PromptRequest and pushes it onto requests, then answers with one token
// and Done. This lets a test drive several successive turns through the
// real streamPrompt and inspect exactly what each turn sent — including
// History — over the wire.
func fakeDaemonCapturingRequests(t *testing.T, requests chan<- protocol.PromptRequest) (lockPath string, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")
	lockPath = filepath.Join(dir, "daemon.lock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listening on fake daemon socket: %v", err)
	}

	lock := protocol.LockFile{SocketPath: sockPath, PID: os.Getpid()}
	data, err := json.Marshal(lock)
	if err != nil {
		t.Fatalf("marshal lockfile: %v", err)
	}
	if err := os.WriteFile(lockPath, data, 0644); err != nil {
		t.Fatalf("writing lockfile: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				dec := json.NewDecoder(conn)
				enc := json.NewEncoder(conn)

				var hsReq protocol.HandshakeRequest
				if err := dec.Decode(&hsReq); err != nil {
					return
				}
				enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true}) //nolint:errcheck

				var promptReq protocol.PromptRequest
				if err := dec.Decode(&promptReq); err != nil {
					return
				}
				requests <- promptReq

				enc.Encode(protocol.TokenResponse{Token: "ok"}) //nolint:errcheck
				enc.Encode(protocol.TokenResponse{Done: true})  //nolint:errcheck
			}(conn)
		}
	}()

	cleanup = func() {
		ln.Close()
		<-done
	}
	return lockPath, cleanup
}

// TestStreamPrompt_HistorySentOnThirdTurnContainsPriorTwoInOrder is the
// update-loop test this task requires: it drives three sequential turns
// through the real streamPrompt/wire path and proves the third turn's
// PromptRequest.History contains exactly the first two turns, oldest first,
// and never the third turn's own (current, not-yet-answered) prompt.
func TestStreamPrompt_HistorySentOnThirdTurnContainsPriorTwoInOrder(t *testing.T) {
	requests := make(chan protocol.PromptRequest, 8)
	lockPath, cleanup := fakeDaemonCapturingRequests(t, requests)
	defer cleanup()

	restoreLockPath := setLockPathForTest(t, lockPath)
	defer restoreLockPath()

	runTurn := func(prompt string, history []protocol.Turn) protocol.PromptRequest {
		t.Helper()
		ch := make(chan tea.Msg, 8)
		streamPrompt(context.Background(), "test-client", "", prompt, "", history, ch)
		for {
			msg := <-ch
			if errMsg, ok := msg.(streamErrMsg); ok {
				t.Fatalf("unexpected stream error: %v", errMsg.err)
			}
			if _, ok := msg.(streamDoneMsg); ok {
				break
			}
		}
		select {
		case req := <-requests:
			return req
		case <-time.After(2 * time.Second):
			t.Fatal("fake daemon never received a PromptRequest")
			return protocol.PromptRequest{}
		}
	}

	req1 := runTurn("first question", nil)
	if len(req1.History) != 0 {
		t.Errorf("turn 1 History = %+v, want empty (no prior turns)", req1.History)
	}

	history2 := []protocol.Turn{
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
	}
	req2 := runTurn("second question", history2)
	if !reflect.DeepEqual(req2.History, history2) {
		t.Errorf("turn 2 History = %+v, want %+v", req2.History, history2)
	}

	history3 := append(append([]protocol.Turn{}, history2...),
		protocol.Turn{Role: "user", Content: "second question"},
		protocol.Turn{Role: "assistant", Content: "second answer"},
	)
	req3 := runTurn("third question", history3)
	if !reflect.DeepEqual(req3.History, history3) {
		t.Errorf("turn 3 History = %+v, want %+v", req3.History, history3)
	}
	for _, h := range req3.History {
		if h.Content == "third question" {
			t.Fatal("turn 3's own (current) prompt must not appear inside its own History")
		}
	}
	if req3.Prompt != "third question" {
		t.Errorf("turn 3 Prompt = %q, want %q", req3.Prompt, "third question")
	}
}

// TestStreamPrompt_PromptKindReachesWire proves streamPrompt actually puts
// the promptKind argument onto PromptRequest.PromptKind -- not just that the
// TUI-side parser (see TestParsePromptKind in chat_test.go) computes the
// right value, but that it's carried all the way onto the wire the daemon
// actually reads. An empty promptKind (today's default, and everything that
// doesn't match a recognized command) must serialize as empty too.
func TestStreamPrompt_PromptKindReachesWire(t *testing.T) {
	requests := make(chan protocol.PromptRequest, 8)
	lockPath, cleanup := fakeDaemonCapturingRequests(t, requests)
	defer cleanup()

	restoreLockPath := setLockPathForTest(t, lockPath)
	defer restoreLockPath()

	send := func(promptKind string) protocol.PromptRequest {
		t.Helper()
		ch := make(chan tea.Msg, 8)
		streamPrompt(context.Background(), "test-client", "", "some question", promptKind, nil, ch)
		for {
			msg := <-ch
			if errMsg, ok := msg.(streamErrMsg); ok {
				t.Fatalf("unexpected stream error: %v", errMsg.err)
			}
			if _, ok := msg.(streamDoneMsg); ok {
				break
			}
		}
		select {
		case req := <-requests:
			return req
		case <-time.After(2 * time.Second):
			t.Fatal("fake daemon never received a PromptRequest")
			return protocol.PromptRequest{}
		}
	}

	if req := send("reason"); req.PromptKind != "reason" {
		t.Errorf("PromptKind = %q, want %q", req.PromptKind, "reason")
	}
	if req := send("refactor"); req.PromptKind != "refactor" {
		t.Errorf("PromptKind = %q, want %q", req.PromptKind, "refactor")
	}
	if req := send(""); req.PromptKind != "" {
		t.Errorf("PromptKind = %q, want empty for an ordinary prompt", req.PromptKind)
	}
}

// TestStreamPrompt_ContextCancelUnblocksBlockedRead is the mid-stream-quit
// safety net this task calls out explicitly: cancelling the context while
// streamPrompt is blocked inside dec.Decode (waiting on a daemon that's mid-
// generation and has gone silent) must make streamPrompt return promptly,
// via the watcher goroutine closing the connection — not hang forever
// reading a dead socket.
func TestStreamPrompt_ContextCancelUnblocksBlockedRead(t *testing.T) {
	lockPath, cleanup := fakeDaemonHandshakeThenHang(t)
	defer cleanup()

	restoreLockPath := setLockPathForTest(t, lockPath)
	defer restoreLockPath()

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan tea.Msg, 8)

	streamReturned := make(chan struct{})
	go func() {
		streamPrompt(ctx, "test-client", "", "hello", "", nil, ch)
		close(streamReturned)
	}()

	// Give streamPrompt time to actually reach the blocked Decode call
	// before cancelling, so this test exercises the "unblock an in-flight
	// read" path rather than a cancel-before-connect race.
	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case <-streamReturned:
		// good: the goroutine exited instead of leaking, reading a dead socket
	case <-time.After(2 * time.Second):
		t.Fatal("streamPrompt did not return within 2s of context cancellation — goroutine leaked")
	}

	select {
	case msg := <-ch:
		t.Errorf("expected no message after a deliberate cancellation, got %#v", msg)
	default:
		// good: cancellation is quiet, not reported as streamErrMsg
	}
}

// fakeDaemonForReset starts a real Unix-socket listener that handles the
// handshake then replies to the client's single PromptRequest with exactly
// one TokenResponse (Error set to failReason if non-empty) — mirroring the
// real daemon's Reset path (server.go's handleConn): a bare Done, no token
// first. Captures the received PromptRequest for assertions.
func fakeDaemonForReset(t *testing.T, failReason string, requests chan<- protocol.PromptRequest) (lockPath string, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")
	lockPath = filepath.Join(dir, "daemon.lock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listening on fake daemon socket: %v", err)
	}

	lock := protocol.LockFile{SocketPath: sockPath, PID: os.Getpid()}
	data, err := json.Marshal(lock)
	if err != nil {
		t.Fatalf("marshal lockfile: %v", err)
	}
	if err := os.WriteFile(lockPath, data, 0644); err != nil {
		t.Fatalf("writing lockfile: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		dec := json.NewDecoder(conn)
		enc := json.NewEncoder(conn)

		var hsReq protocol.HandshakeRequest
		if err := dec.Decode(&hsReq); err != nil {
			return
		}
		enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true}) //nolint:errcheck

		var promptReq protocol.PromptRequest
		if err := dec.Decode(&promptReq); err != nil {
			return
		}
		requests <- promptReq

		enc.Encode(protocol.TokenResponse{Done: true, Error: failReason}) //nolint:errcheck
	}()

	cleanup = func() {
		ln.Close()
		<-done
	}
	return lockPath, cleanup
}

// TestResetHistoryOnDaemon_SendsResetTrueWithEmptyPromptAndHistory proves
// ctrl+n's network half sends exactly PromptRequest{Reset: true} with no
// Prompt/History, and reports success (resetOkMsg) when the daemon replies
// with a bare Done.
func TestResetHistoryOnDaemon_SendsResetTrueWithEmptyPromptAndHistory(t *testing.T) {
	requests := make(chan protocol.PromptRequest, 1)
	lockPath, cleanup := fakeDaemonForReset(t, "", requests)
	defer cleanup()

	restoreLockPath := setLockPathForTest(t, lockPath)
	defer restoreLockPath()

	ch := make(chan tea.Msg, 1)
	resetHistoryOnDaemon(context.Background(), "test-client", ch)

	msg := <-ch
	if _, ok := msg.(resetOkMsg); !ok {
		t.Fatalf("got %#v, want resetOkMsg on success", msg)
	}

	select {
	case req := <-requests:
		if !req.Reset {
			t.Error("Reset = false, want true")
		}
		if req.Prompt != "" {
			t.Errorf("Prompt = %q, want empty", req.Prompt)
		}
		if len(req.History) != 0 {
			t.Errorf("History = %+v, want empty", req.History)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fake daemon never received a PromptRequest")
	}
}

// TestResetHistoryOnDaemon_SurfacesDaemonSideError proves a daemon-side
// failure (TokenResponse.Error set) surfaces as resetErrMsg rather than
// being swallowed or misreported as success.
func TestResetHistoryOnDaemon_SurfacesDaemonSideError(t *testing.T) {
	requests := make(chan protocol.PromptRequest, 1)
	lockPath, cleanup := fakeDaemonForReset(t, "clearing failed", requests)
	defer cleanup()

	restoreLockPath := setLockPathForTest(t, lockPath)
	defer restoreLockPath()

	ch := make(chan tea.Msg, 1)
	resetHistoryOnDaemon(context.Background(), "test-client", ch)

	msg := <-ch
	errMsg, ok := msg.(resetErrMsg)
	if !ok {
		t.Fatalf("got %#v, want resetErrMsg on daemon-side failure", msg)
	}
	if !strings.Contains(errMsg.err.Error(), "clearing failed") {
		t.Errorf("err = %v, want it to mention the daemon's failure reason", errMsg.err)
	}
}
