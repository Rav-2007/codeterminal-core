// Command fakehelper is a test-only fixture that speaks the exact same wire
// protocol as the real embedder helper (codeterminal/helper), used by
// helperproc_test.go to exercise HelperProcess's spawn/health/restart/
// shutdown logic without needing to build the real helper binary or touch
// ONNX/CGO at all. It is not part of the daemon build — go tooling ignores
// "testdata" directories by convention — and is compiled on demand by
// TestMain via `go build`.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"codeterminal/helper/helperproto"
	"codeterminal/protocol"
)

const fakeDim = 384

func main() {
	logger := log.New(os.Stderr, "fakehelper: ", log.LstdFlags)

	socketPath := flag.String("socket", "", "address to listen on")
	// TAKES THE SAME FLAG PAIR AS THE REAL HELPER, and that is the whole point.
	//
	// This fixture used to call net.Listen("unix", ...) directly while the real
	// helper called protocol.Listen. Go supports AF_UNIX on Windows 10 1803+, so
	// the fixture WORKED on Windows where the real helper could not bind at all
	// -- protocol.Listen rejects TransportUnix there and the helper's next line
	// is a Fatalf. Every Windows daemon test went green against a stand-in that
	// took a different code path than the code it was standing in for, and the
	// one defect that mattered on the platform was invisible.
	//
	// A fixture that cannot fail the way production fails is not a fixture.
	transport := flag.String("transport", "", "transport for --socket (unix|npipe; empty means this platform's default)")
	flag.Parse()
	if *socketPath == "" {
		logger.Fatal("--socket is required")
	}

	// FAKEHELPER_FAIL, if set, makes this instance exit immediately without
	// ever binding its socket — used to prove HelperProcess's restart policy
	// is bounded (it must give up rather than loop forever) when the helper
	// can never become healthy.
	if os.Getenv("FAKEHELPER_FAIL") != "" {
		os.Exit(1)
	}

	addr := protocol.Address{Transport: *transport, Address: *socketPath}
	removeStale(addr)
	ln, err := protocol.Listen(addr)
	if err != nil {
		logger.Fatalf("listening on %s: %v", addr, err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		ln.Close()
		removeStale(addr)
		os.Exit(0)
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go handleConn(conn)
	}
}

func handleConn(conn net.Conn) {
	defer conn.Close()

	var req helperproto.Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}

	// FAKEHELPER_TRUNCATE, if set, makes an embed reply write a deliberately
	// truncated (incomplete) JSON body and then drop the connection, standing in
	// for a helper that dies or has its socket read cut off mid-response. It
	// exists to prove what such a partial wire read actually does on the daemon
	// side: a json.Decode error, not a short vector slice. Health still answers
	// normally so waitReady succeeds.
	if os.Getenv("FAKEHELPER_TRUNCATE") != "" && req.Method == helperproto.MethodEmbed {
		_, _ = conn.Write([]byte(`{"ok":true,"vectors":[[1.0,`))
		return
	}

	var resp helperproto.Response
	switch req.Method {
	case helperproto.MethodHealth:
		resp = helperproto.Response{OK: true}
	case helperproto.MethodEmbed:
		// FAKEHELPER_EMBED_DELAY, if set to a Go duration, stalls the embed
		// response by that long. It exists to test that the call deadline
		// SCALES WITH THE BATCH: a stall longer than the one-call budget but
		// shorter than the batched budget must fail a single-text embed and
		// succeed a batched one. Health is deliberately unaffected, so the
		// helper still starts normally.
		if d, err := time.ParseDuration(os.Getenv("FAKEHELPER_EMBED_DELAY")); err == nil && d > 0 {
			time.Sleep(d)
		}
		n := len(req.Texts)
		// FAKEHELPER_SHORT_VECTORS, if set, returns ONE FEWER vector than there
		// were input texts while still reporting ok:true — the precise
		// count-mismatch shape the CTO report claims a malformed embedder
		// response could take ("3 vectors for a 4-chunk batch"). The real ONNX
		// helper cannot produce this (its Embed returns exactly len(texts)
		// vectors or an error); this fixture fabricates it so the daemon's
		// boundary handling of a lying helper can be exercised directly.
		if os.Getenv("FAKEHELPER_SHORT_VECTORS") != "" && n > 0 {
			n--
		}
		vecs := make([][]float32, n)
		for i := range vecs {
			vecs[i] = make([]float32, fakeDim)
			vecs[i][0] = 1
		}
		resp = helperproto.Response{OK: true, Vectors: vecs}
	default:
		resp = helperproto.Response{OK: false, Error: "unknown method: " + req.Method}
	}

	_ = json.NewEncoder(conn).Encode(resp)
}

// removeStale mirrors the real helper's cleanup: unlink only where the endpoint
// is a file. See helper/main.go.
func removeStale(a protocol.Address) {
	if a.Transport == "" || a.Transport == protocol.TransportUnix {
		os.Remove(a.Address)
	}
}
