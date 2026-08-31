package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"codeterminal/helper/helperproto"
)

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestLoadEmbedderValidation(t *testing.T) {
	// Missing args
	_, err := loadEmbedder("", "", 0)
	if err == nil || !strings.Contains(err.Error(), "--model-dir and --onnxruntime-lib are required") {
		t.Errorf("expected missing args error, got: %v", err)
	}

	// Missing library file
	nonExistentLib := filepath.Join(t.TempDir(), "nonexistent.so")
	_, err = loadEmbedder(t.TempDir(), nonExistentLib, 0)
	if err == nil || !strings.Contains(err.Error(), "onnxruntime shared library not found") {
		t.Errorf("expected missing lib file error, got: %v", err)
	}
}

func TestServerDispatchHealthAndUnknown(t *testing.T) {
	var buf safeBuffer
	logger := log.New(&buf, "", 0)
	srv := &server{embedder: nil, logger: logger}

	// Health check
	resp := srv.dispatch(helperproto.Request{Method: helperproto.MethodHealth})
	if !resp.OK {
		t.Errorf("health dispatch failed: %+v", resp)
	}

	// Unknown method
	resp = srv.dispatch(helperproto.Request{Method: "invalid_method"})
	if resp.OK || !strings.Contains(resp.Error, "unknown method") {
		t.Errorf("unknown method dispatch failed to return error: %+v", resp)
	}
}

func TestServerHandleConn(t *testing.T) {
	var buf safeBuffer
	logger := log.New(&buf, "", 0)
	srv := &server{embedder: nil, logger: logger}

	clientConn, serverConn := net.Pipe()

	errCh := make(chan error, 1)
	go func() {
		srv.serveConn(serverConn)
		errCh <- nil
	}()

	// Send Health Request
	req := helperproto.Request{Method: helperproto.MethodHealth}
	if err := json.NewEncoder(clientConn).Encode(req); err != nil {
		t.Fatalf("encode req error: %v", err)
	}

	var resp helperproto.Response
	if err := json.NewDecoder(clientConn).Decode(&resp); err != nil {
		t.Fatalf("decode resp error: %v", err)
	}
	_ = clientConn.Close()

	if !resp.OK {
		t.Errorf("handleConn health response ok = false")
	}

	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("handleConn did not exit after client close")
	}
}

func TestServerHandleConnDecodeError(t *testing.T) {
	var buf safeBuffer
	logger := log.New(&buf, "", 0)
	srv := &server{embedder: nil, logger: logger}

	clientConn, serverConn := net.Pipe()

	done := make(chan struct{})
	go func() {
		srv.serveConn(serverConn)
		close(done)
	}()

	// Send garbage data
	_, _ = clientConn.Write([]byte("not valid json\n"))
	_ = clientConn.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleConn timed out")
	}

	if !strings.Contains(buf.String(), "request decode error") {
		t.Errorf("expected decode error logged, got: %s", buf.String())
	}
}

func TestLoadEmbedderPlatformCheck(t *testing.T) {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "amd64" {
		_, err := loadEmbedder("dir", "lib", 0)
		if err == nil || !strings.Contains(err.Error(), "Intel Mac") {
			t.Errorf("expected Intel Mac error on darwin/amd64, got %v", err)
		}
	}
}

// TestLoadEmbedderRejectsNegativeThreadCount pins the one thing about
// --intra-op-threads that can be checked without ONNX Runtime present: a
// negative value is refused, by name, before anything expensive happens.
//
// The ordering is the point. ONNX Runtime does reject n < 0 -- but only inside
// SetIntraOpNumThreads, which runs after the platform check, both path checks,
// the dlopen of the shared library and the environment init. On a machine
// without the model downloaded, the operator would see "run download-model
// first" for what is actually a typo in a flag.
func TestLoadEmbedderRejectsNegativeThreadCount(t *testing.T) {
	_, err := loadEmbedder("dir", "lib", -1)
	if err == nil {
		t.Fatal("expected a negative --intra-op-threads to be refused")
	}
	if !strings.Contains(err.Error(), "--intra-op-threads") {
		t.Errorf("the error does not name the flag, so the operator cannot act on it: %v", err)
	}
	// Refused BEFORE the path checks, which is what makes the message useful:
	// "dir" and "lib" do not exist, and if those were reported first this
	// message would never be seen.
	if strings.Contains(err.Error(), "not found") {
		t.Errorf("a path error won the race, so the flag check runs too late: %v", err)
	}
}

// TestHandleConnAuthorizesThenServes drives handleConn over a REAL Unix socket.
//
// WHY A REAL SOCKET. handleConn's whole body is protocol.AuthorizePeer followed
// by serveConn, and AuthorizePeer reads the peer's credentials out of the
// kernel (SO_PEERCRED on Linux, LOCAL_PEERCRED on the BSDs). A net.Pipe has no
// peer credentials to read, so a piped test would exercise the error path and
// call it coverage of the success path.
//
// WHAT IT PINS. This socket is the one L7 was about: the helper listened without
// authorising its peer until peerauth moved into protocol/ so both sockets could
// share it. serveConn has had tests since; handleConn -- the function that
// actually calls the guard -- had none, so nothing failed if the guard were
// deleted. A same-uid connection must be served, and that is exactly what the
// daemon's connection is.
func TestHandleConnAuthorizesThenServes(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "h.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listening on %s: %v", sockPath, err)
	}
	defer ln.Close()

	var buf safeBuffer
	srv := &server{logger: log.New(&buf, "", 0)}

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		srv.handleConn(conn)
	}()

	client, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}
	defer client.Close()

	// Health, because it needs no model: this test is about the connection
	// being accepted and dispatched, not about inference.
	if err := json.NewEncoder(client).Encode(helperproto.Request{Method: helperproto.MethodHealth}); err != nil {
		t.Fatalf("sending request: %v", err)
	}
	var resp helperproto.Response
	if err := json.NewDecoder(client).Decode(&resp); err != nil {
		t.Fatalf("reading response: %v (log: %s)", err, buf.String())
	}
	if !resp.OK {
		t.Errorf("a same-uid peer was not served: OK=%v error=%q log=%s", resp.OK, resp.Error, buf.String())
	}
	<-done
}
