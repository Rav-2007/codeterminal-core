package main

import (
	"encoding/json"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codeterminal/helper/helperproto"
	"codeterminal/protocol"
)

// handleConn must contain a panic to one connection, the way the daemon's
// equivalent has since daemon/server.go:289.
//
// WHY THE PANIC HERE IS NOT AN INVALID-UTF-8 ONE, stated plainly because the
// obvious test cannot be written. The motivating crash is sugarme/tokenizer
// panicking on invalid UTF-8, and NO PATH THROUGH THIS SOCKET CAN DELIVER THOSE
// BYTES. encoding/json replaces invalid UTF-8 with U+FFFD in BOTH directions --
// measured, with a nil error every time: raw 0x93/0x94 in the wire body, a raw
// truncated e2 82, a raw lone surrogate ed a0 80, and an escaped \ud800 all
// arrive as U+FFFD. The helper's own DECODER is what does it, so this is not a
// trust assumption about a well-behaved daemon: a hostile same-uid client
// cannot deliver them either.
//
// So containment is demonstrated with a panic from a different source rather
// than asserted about one that cannot be reached. A nil embedder nil-derefs
// inside dispatch, which is a real panic on the real path, and is the same
// panic that escaped serveConn uncaught while this file was being written.
func TestHandleConn_ContainsAPanicToOneConnection(t *testing.T) {
	// A short socket path: sun_path is 108 bytes on Linux and 104 on macOS, and
	// t.TempDir() under a long test name has overrun it before.
	dir, err := os.MkdirTemp("", "hp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "h.sock")

	ln, err := protocol.Listen(protocol.Address{Transport: protocol.TransportUnix, Address: sock})
	if err != nil {
		t.Skipf("listening on %s: %v", sock, err)
	}
	defer ln.Close()

	var mu sync.Mutex
	var logbuf strings.Builder
	// embedder is nil ON PURPOSE: dispatch calls s.embedder.Embed and nil-derefs.
	srv := &server{logger: log.New(&lockedWriter{w: &logbuf, mu: &mu}, "", 0)}

	accepted := make(chan struct{}, 2)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// No recover HERE: if handleConn does not contain the panic, it
			// escapes this goroutine and takes the test binary with it, which
			// is exactly what it does to the helper process in production.
			go func() { srv.handleConn(conn); accepted <- struct{}{} }()
		}
	}()

	embedOnce := func(t *testing.T) {
		t.Helper()
		c, err := net.DialTimeout("unix", sock, 5*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer c.Close()
		body, _ := json.Marshal(helperproto.Request{
			Method: helperproto.MethodEmbed, Texts: []string{"anything"},
		})
		c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write(append(body, '\n')); err != nil {
			t.Fatalf("write: %v", err)
		}
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		var raw json.RawMessage
		_ = json.NewDecoder(c).Decode(&raw) // the panicking connection sends nothing
	}

	// FIRST connection panics.
	embedOnce(t)
	select {
	case <-accepted:
	case <-time.After(10 * time.Second):
		t.Fatal("handleConn never returned; the panic was not contained and nothing recovered it")
	}

	mu.Lock()
	logged := logbuf.String()
	mu.Unlock()
	if !strings.Contains(logged, "recovered from panic") {
		t.Errorf("the panic was not recorded; a contained panic that is never mentioned is a "+
			"silent failure. log: %q", logged)
	}
	if !strings.Contains(logged, "goroutine") {
		t.Errorf("the recovery logged no stack, so the panic cannot be diagnosed: %q", logged)
	}

	// SECOND connection proves the blast radius was one connection: the
	// listener, the accept loop and the process are all still working. This is
	// the assertion that a bare "it did not crash the test" would miss.
	embedOnce(t)
	select {
	case <-accepted:
	case <-time.After(10 * time.Second):
		t.Fatal("a second connection was never served; the first panic took the accept loop with it")
	}
}

// The claim the recover is standing in for, pinned as a measurement: no path
// through this socket delivers invalid UTF-8, in either direction. If this ever
// fails, the recover above stops being defense-in-depth and becomes the only
// thing preventing a process-killing crash.
func TestHandleConn_NoWirePathDeliversInvalidUTF8(t *testing.T) {
	bodies := map[string][]byte{
		"raw win-1252 in the body": append(append([]byte(`{"method":"embed","texts":["he said `), 0x93, 'h', 'i', 0x94), []byte(`"]}`)...),
		"raw truncated utf-8":      append(append([]byte(`{"method":"embed","texts":["pre `), 0xe2, 0x82), []byte(`"]}`)...),
		"raw lone surrogate":       append(append([]byte(`{"method":"embed","texts":["pre `), 0xed, 0xa0, 0x80), []byte(`"]}`)...),
		"escaped lone surrogate":   []byte(`{"method":"embed","texts":["pre \ud800"]}`),
	}
	e := tokenizerOnly(t)
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			var req helperproto.Request
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(req.Texts) != 1 {
				t.Fatalf("expected one text, got %d; this case decoded to something else entirely", len(req.Texts))
			}
			if _, panicked := tokenizeCatchingPanic(e, req.Texts[0]); panicked != nil {
				t.Errorf("bytes that arrived over the wire panicked the tokenizer: %v\n"+
					"encoding/json no longer sanitises this shape, so handleConn's recover is now "+
					"the ONLY thing between a workspace file and a dead helper process.", panicked)
			}
		})
	}
}
