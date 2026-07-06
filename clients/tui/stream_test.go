package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
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
		streamPrompt(ctx, "test-client", "", "hello", ch)
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
