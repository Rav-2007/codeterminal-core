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

	"codeterminal/helper/helperproto"
)

const fakeDim = 384

func main() {
	logger := log.New(os.Stderr, "fakehelper: ", log.LstdFlags)

	socketPath := flag.String("socket", "", "socket to listen on")
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

	os.Remove(*socketPath)
	ln, err := net.Listen("unix", *socketPath)
	if err != nil {
		logger.Fatalf("listening on %s: %v", *socketPath, err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		ln.Close()
		os.Remove(*socketPath)
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

	var resp helperproto.Response
	switch req.Method {
	case helperproto.MethodHealth:
		resp = helperproto.Response{OK: true}
	case helperproto.MethodEmbed:
		vecs := make([][]float32, len(req.Texts))
		for i := range req.Texts {
			vecs[i] = make([]float32, fakeDim)
			vecs[i][0] = 1
		}
		resp = helperproto.Response{OK: true, Vectors: vecs}
	default:
		resp = helperproto.Response{OK: false, Error: "unknown method: " + req.Method}
	}

	_ = json.NewEncoder(conn).Encode(resp)
}
