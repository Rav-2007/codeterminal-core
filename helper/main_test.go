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
	_, err := loadEmbedder("", "")
	if err == nil || !strings.Contains(err.Error(), "--model-dir and --onnxruntime-lib are required") {
		t.Errorf("expected missing args error, got: %v", err)
	}

	// Missing library file
	nonExistentLib := filepath.Join(t.TempDir(), "nonexistent.so")
	_, err = loadEmbedder(t.TempDir(), nonExistentLib)
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
		_, err := loadEmbedder("dir", "lib")
		if err == nil || !strings.Contains(err.Error(), "Intel Mac") {
			t.Errorf("expected Intel Mac error on darwin/amd64, got %v", err)
		}
	}
}
