// Command codeterminal-embedder-helper is a small subprocess spawned by the
// daemon to run the local embedding model. It exists as a separate process
// (rather than a package inside the daemon) so that CGO — needed by the
// ONNX Runtime binding this binary uses — never has to touch the daemon
// binary, which must stay pure Go.
//
// CGO in this binary is dlopen-based (github.com/yalue/onnxruntime_go loads
// the onnxruntime shared library manually at runtime rather than linking
// it at compile time), so `go build` here succeeds regardless of whether
// that library is present on the build machine. The failure necessarily
// happens at startup instead, when this binary actually tries to load it —
// see loadEmbedder below, which is written to fail with an actionable
// message rather than a raw dlopen error.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	ort "github.com/yalue/onnxruntime_go"

	"codeterminal/helper/helperproto"
)

func main() {
	logger := log.New(os.Stderr, "codeterminal-embedder-helper: ", log.LstdFlags)

	socketPath := flag.String("socket", "", "Unix domain socket path to listen on (required; the daemon supplies this)")
	modelDir := flag.String("model-dir", "", "directory containing model_int8.onnx + tokenizer files (required; the daemon supplies this after `download-model`)")
	onnxRuntimeLib := flag.String("onnxruntime-lib", "", "path to the onnxruntime shared library (required; the daemon supplies this after `download-model`)")
	flag.Parse()

	if *socketPath == "" {
		logger.Fatal("--socket is required")
	}

	embedder, err := loadEmbedder(*modelDir, *onnxRuntimeLib)
	if err != nil {
		logger.Fatal(err)
	}
	defer embedder.Close()
	defer ort.DestroyEnvironment()

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

	srv := &server{embedder: embedder, logger: logger}
	for {
		conn, err := ln.Accept()
		if err != nil {
			// Expected once the signal handler above closes ln during
			// shutdown; nothing else should cause Accept to fail here.
			return
		}
		go srv.handleConn(conn)
	}
}

// loadEmbedder validates the platform, the required flags, and the
// on-disk paths they name, then initializes ONNX Runtime and loads the
// model. Every failure path here is written to be actionable: what's
// missing and how to fix it, not a raw library error.
func loadEmbedder(modelDir, onnxRuntimeLib string) (*OnnxEmbedder, error) {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "amd64" {
		return nil, fmt.Errorf("Intel Mac (darwin/amd64) is not supported: no prebuilt onnxruntime shared library exists for this platform (upstream onnxruntime v1.26.0 ships no darwin/amd64 release); this is a known, open platform-coverage gap, not a bug — see \"Known platform gaps\" in the project README")
	}

	if modelDir == "" || onnxRuntimeLib == "" {
		return nil, fmt.Errorf("--model-dir and --onnxruntime-lib are required (run `codeterminal-daemon download-model` first, then let the daemon supply these paths)")
	}
	if _, err := os.Stat(onnxRuntimeLib); err != nil {
		return nil, fmt.Errorf("onnxruntime shared library not found at %s (run `codeterminal-daemon download-model` first): %w", onnxRuntimeLib, err)
	}

	ort.SetSharedLibraryPath(onnxRuntimeLib)
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("initializing ONNX Runtime using shared library %s: %w", onnxRuntimeLib, err)
	}

	embedder, err := NewOnnxEmbedder(modelDir)
	if err != nil {
		ort.DestroyEnvironment()
		return nil, fmt.Errorf("loading BGE model from %s (run `codeterminal-daemon download-model` first): %w", modelDir, err)
	}
	return embedder, nil
}

// server dispatches wire requests to the loaded embedder.
type server struct {
	embedder *OnnxEmbedder
	logger   *log.Logger
}

// The helper's own admission limits, mirroring the daemon's limitedConn rather
// than inventing a second policy (L3).
//
// The trust boundary here is real but narrow: this socket lives in a per-user
// runtime directory and the only thing that dials it is the daemon that spawned
// this process. Nothing crosses a uid boundary. What these close is the
// asymmetry — the daemon caps and deadlines every connection it accepts, and
// this one did neither, so a wedged or malfunctioning peer could hold a helper
// connection open indefinitely or make it buffer without limit. A same-uid
// boundary is a reason for the numbers to be generous, not a reason to have
// none.
const (
	// maxHelperRequestBytes bounds one request. An embed batch is text, and
	// 16 MiB is the same ceiling the daemon applies to a socket request.
	maxHelperRequestBytes = 16 << 20
	// helperConnTimeout bounds one whole request/response exchange. Embedding a
	// large batch is CPU-bound work measured in seconds, so this is sized for a
	// wedged peer rather than a slow one.
	helperConnTimeout = 2 * time.Minute
)

func (s *server) handleConn(conn net.Conn) {
	defer conn.Close()

	// One deadline over the read, the embed, and the write. A peer that stops
	// reading its own response cannot pin this goroutine past it either.
	if err := conn.SetDeadline(time.Now().Add(helperConnTimeout)); err != nil {
		s.logger.Printf("setting connection deadline: %v", err)
		return
	}

	var req helperproto.Request
	if err := json.NewDecoder(io.LimitReader(conn, maxHelperRequestBytes)).Decode(&req); err != nil {
		s.logger.Printf("request decode error: %v", err)
		return
	}

	resp := s.dispatch(req)
	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		s.logger.Printf("response encode error: %v", err)
	}
}

func (s *server) dispatch(req helperproto.Request) helperproto.Response {
	switch req.Method {
	case helperproto.MethodHealth:
		return helperproto.Response{OK: true}
	case helperproto.MethodEmbed:
		vecs, err := s.embedder.Embed(req.Texts)
		if err != nil {
			return helperproto.Response{OK: false, Error: err.Error()}
		}
		return helperproto.Response{OK: true, Vectors: vecs}
	default:
		return helperproto.Response{OK: false, Error: "unknown method: " + req.Method}
	}
}
