package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeterminal/protocol"
)

// fakeDaemonForSearch is a minimal fake daemon that answers a handshake and then a SearchRequest.
func fakeDaemonForSearch(t *testing.T, resp protocol.SearchResponse) (lockPath string, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	addr := testAddress(t)
	lockPath = filepath.Join(dir, "daemon.lock")

	ln, err := protocol.Listen(addr)
	if err != nil {
		t.Fatalf("listening on fake daemon socket: %v", err)
	}

	lock := protocol.LockFile{Address: addr, PID: os.Getpid()}
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
		_ = enc.Encode(protocol.HandshakeResponse{ProtocolVersion: protocol.ProtocolVersion, Ok: true})

		var searchReq protocol.SearchRequest
		if err := dec.Decode(&searchReq); err != nil {
			return
		}

		_ = enc.Encode(resp)
	}()

	cleanup = func() {
		ln.Close()
		<-done
	}
	return lockPath, cleanup
}

func TestRunSearch_Success(t *testing.T) {
	resp := protocol.SearchResponse{
		ProtocolVersion: protocol.ProtocolVersion,
		Results: []protocol.SearchResult{
			{Role: "user", Snippet: "hello world", CreatedAt: "2026-08-11T12:00:00Z"},
			{Role: "assistant", Snippet: "hi there", CreatedAt: "2026-08-11T12:00:01Z"},
		},
	}
	lockPath, cleanup := fakeDaemonForSearch(t, resp)
	defer cleanup()

	original := lockPathFunc
	lockPathFunc = func() string { return lockPath }
	defer func() { lockPathFunc = original }()

	out := runSearch("test-client", "/workspace", "hello")
	if !strings.Contains(out, "found 2 match(es)") {
		t.Errorf("expected 'found 2 match(es)', got %q", out)
	}
	if !strings.Contains(out, "hello world") {
		t.Errorf("expected output to contain snippet, got %q", out)
	}
}

func TestRunSearch_NoResults(t *testing.T) {
	resp := protocol.SearchResponse{
		ProtocolVersion: protocol.ProtocolVersion,
	}
	lockPath, cleanup := fakeDaemonForSearch(t, resp)
	defer cleanup()

	original := lockPathFunc
	lockPathFunc = func() string { return lockPath }
	defer func() { lockPathFunc = original }()

	out := runSearch("test-client", "/workspace", "nomatch")
	if !strings.Contains(out, "no matching turns found") {
		t.Errorf("expected 'no matching turns found', got %q", out)
	}
}

func TestRunSearch_ErrorResponse(t *testing.T) {
	resp := protocol.SearchResponse{
		ProtocolVersion: protocol.ProtocolVersion,
		Error:           "database offline",
	}
	lockPath, cleanup := fakeDaemonForSearch(t, resp)
	defer cleanup()

	original := lockPathFunc
	lockPathFunc = func() string { return lockPath }
	defer func() { lockPathFunc = original }()

	out := runSearch("test-client", "/workspace", "errormatch")
	if !strings.Contains(out, "search failed: database offline") {
		t.Errorf("expected error message in output, got %q", out)
	}
}
