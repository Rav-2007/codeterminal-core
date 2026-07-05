// Command codeterminal-embedder-helper is a small subprocess spawned by the
// daemon to run the local embedding model. It exists as a separate process
// (rather than a package inside the daemon) so that CGO — needed by the
// real ONNX Runtime binding landing in a later step — never has to touch
// the daemon binary, which must stay pure Go.
//
// In this step (5a) the embed handler is a STUB: it returns correctly
// shaped, deterministic vectors so the daemon<->helper lifecycle (spawn,
// readiness, health, restart, shutdown) can be proven without needing the
// real model, ONNX Runtime, or any CGO at all yet. See embed_stub.go.
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

func main() {
	logger := log.New(os.Stderr, "codeterminal-embedder-helper: ", log.LstdFlags)

	socketPath := flag.String("socket", "", "Unix domain socket path to listen on (required; the daemon supplies this)")
	flag.Parse()

	if *socketPath == "" {
		logger.Fatal("--socket is required")
	}

	// A stale file from a previous, uncleanly-killed instance would block
	// net.Listen; since the daemon scopes this path by its own PID before
	// spawning us, a leftover here can only be our own dead predecessor.
	os.Remove(*socketPath)

	ln, err := net.Listen("unix", *socketPath)
	if err != nil {
		logger.Fatalf("listening on %s: %v", *socketPath, err)
	}
	if err := os.Chmod(*socketPath, 0600); err != nil {
		logger.Fatalf("restricting socket permissions: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Printf("received %s, shutting down", sig)
		ln.Close()
		os.Remove(*socketPath)
		os.Exit(0)
	}()

	logger.Printf("ready, listening on %s", *socketPath)

	for {
		conn, err := ln.Accept()
		if err != nil {
			// Expected once the signal handler above closes ln during
			// shutdown; nothing else should cause Accept to fail here.
			return
		}
		go handleConn(conn, logger)
	}
}

func handleConn(conn net.Conn, logger *log.Logger) {
	defer conn.Close()

	var req helperproto.Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		logger.Printf("request decode error: %v", err)
		return
	}

	resp := dispatch(req)
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		logger.Printf("response encode error: %v", err)
	}
}

func dispatch(req helperproto.Request) helperproto.Response {
	switch req.Method {
	case helperproto.MethodHealth:
		return helperproto.Response{OK: true}
	case helperproto.MethodEmbed:
		vecs := stubEmbed(req.Texts)
		return helperproto.Response{OK: true, Vectors: vecs}
	default:
		return helperproto.Response{OK: false, Error: "unknown method: " + req.Method}
	}
}
