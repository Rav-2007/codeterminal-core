package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"codeterminal/daemon/mcp"
)

// A language server is UNTRUSTED INPUT, and this file treats it that way.
//
// It is a third-party binary, started because a repository contains a .go or
// .ts file, that reads project-supplied configuration: tsconfig.json can load
// plugins, pyrightconfig.json can name an interpreter. So a hostile repository
// can influence — and in the plugin case, execute code inside — the process on
// the far end of this pipe. Everything it sends crosses a trust boundary.
//
// Three bounds follow from that, and each one closed a defect that was live:
//
//	maxLSPMessageBytes   a header no longer decides the size of an allocation
//	lspCallTimeout       a silent server no longer hangs a turn forever
//	the done channel     a dead server no longer hangs one either
//
// The sibling of this file is daemon/mcp/stdioclient.go, which reached the same
// conclusion for Lane B servers and documents the heap measurements behind its
// own cap. This file had none of it.

const (
	// maxLSPMessageBytes bounds ONE message from a language server.
	//
	// Before it, readLoop did make([]byte, l) with whatever integer arrived in
	// the Content-Length header — no ceiling, and no requirement that the
	// server ever send that many bytes. A header of 17179869184 asked for 16
	// GiB. A NEGATIVE one panicked the goroutine outright, and because readLoop
	// runs in a bare `go` statement, an unrecovered panic there terminates the
	// whole daemon: handleConn's recover is on a different goroutine and cannot
	// see it.
	//
	// 8 MiB rather than stdioclient's 2 MiB, deliberately. That cap is sized to
	// a single rendered tool result; an LSP reply can legitimately be a whole
	// document's symbol tree, which is bigger. Both are bounded, which is the
	// property that matters — the exact number only has to be large enough not
	// to break honest servers and small enough that the worst case is a failed
	// request rather than a dead machine.
	maxLSPMessageBytes = 8 * 1024 * 1024

	// lspCallTimeout bounds one request/response exchange.
	//
	// Without it Call blocked on an unbuffered receive with no other case: a
	// server that accepted a request and never answered wedged the caller
	// permanently. GetServer made that worse by holding the bridge mutex across
	// the initialize handshake, so ONE unresponsive server deadlocked every
	// language for the rest of the process's life.
	lspCallTimeout = 30 * time.Second

	// lspInitTimeout bounds the startup handshake, which happens under the
	// bridge lock and so is kept tighter than a steady-state call.
	lspInitTimeout = 15 * time.Second
)

// errServerGone reports that the language server exited, or that its stream was
// abandoned because it violated the framing rules above.
var errServerGone = errors.New("language server is no longer running")

// lspToolchainEnv names the environment variables each language server needs to
// find its own toolchain, and nothing else.
//
// See the trust note at the top of this file for why a language server is not a
// trusted peer. Before this list existed, cmd.Env was nil and every one of them
// held this daemon's inference credentials.
//
// mcp.ServerEnv passes PATH and HOME unconditionally, grants exactly the names
// below, and refuses mcp.ForbiddenEnvNames however they are asked for. It is
// the same scrubber Lane B MCP servers and the embedder helper already use.
var lspToolchainEnv = map[string][]string{
	"gopls": {
		"GOPATH", "GOROOT", "GOMODCACHE", "GOCACHE", "GOFLAGS",
		"GOPROXY", "GOPRIVATE", "GONOSUMCHECK", "GOOS", "GOARCH", "GOTOOLCHAIN",
	},
	"typescript-language-server": {
		// Deliberately NOT NODE_OPTIONS: it accepts --require, which is a code
		// injection vector into the server process.
		"NODE_PATH", "NPM_CONFIG_PREFIX",
	},
	"pyright-langserver": {
		"PYTHONPATH", "VIRTUAL_ENV", "CONDA_PREFIX", "PYENV_ROOT",
	},
}

// fileURI renders a filesystem path as a file:// URI.
//
// Not string concatenation, which is what all three call sites used to do.
// "file://" + `C:\src\main.go` is not a URI on Windows — the drive letter reads
// as a hostname and the backslashes are not separators — and on any platform a
// path containing a space or a '#' produced something a server was entitled to
// misparse. url.URL does the escaping; the leading-slash step is what turns
// C:/src into the /C:/src that a file URI's path component requires.
func fileURI(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	return u.String()
}

// LSPBridge manages a pool of language servers for the workspace.
type LSPBridge struct {
	workspace string
	servers   map[string]*LSPServer
	mu        sync.Mutex
}

func NewLSPBridge(workspace string) *LSPBridge {
	return &LSPBridge{
		workspace: workspace,
		servers:   make(map[string]*LSPServer),
	}
}

// Close shuts down all managed language servers.
func (b *LSPBridge) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, srv := range b.servers {
		srv.Close()
	}
	b.servers = make(map[string]*LSPServer)
}

// serverCommand maps a language to the binary that serves it.
func serverCommand(lang string) (string, error) {
	switch lang {
	case "go":
		return "gopls", nil
	case "typescript", "javascript", "ts", "js":
		return "typescript-language-server", nil
	case "python", "py":
		return "pyright-langserver", nil
	default:
		return "", fmt.Errorf("unsupported language for LSP: %s", lang)
	}
}

// GetServer spins up and initializes a language server if one doesn't exist.
func (b *LSPBridge) GetServer(lang string) (*LSPServer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if srv, ok := b.servers[lang]; ok {
		// A server that has since died must not be handed out again, or every
		// caller gets errServerGone forever and the language never recovers.
		if srv.alive() {
			return srv, nil
		}
		delete(b.servers, lang)
	}

	cmdName, err := serverCommand(lang)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(cmdName)
	if cmdName == "typescript-language-server" || cmdName == "pyright-langserver" {
		cmd.Args = append(cmd.Args, "--stdio")
	}

	cmd.Dir = b.workspace
	// NEVER nil. A nil Env hands this third-party subprocess the daemon's whole
	// environment, including OPENROUTER_API_KEY and CODETERMINAL_MOCHIII_KEY.
	// See lspToolchainEnv for why a language server is not a trusted peer.
	cmd.Env = mcp.ServerEnv(lspToolchainEnv[cmdName])
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start %s (is it installed in PATH?): %w", cmdName, err)
	}

	srv := &LSPServer{
		cmd:     cmd,
		in:      in,
		out:     bufio.NewReader(out),
		pending: make(map[int64]chan []byte),
		done:    make(chan struct{}),
	}
	go srv.readLoop()

	initReq := map[string]any{
		"processId":    nil,
		"rootUri":      fileURI(b.workspace),
		"capabilities": map[string]any{},
	}
	// Bounded, and bounded tighter than a normal call, because this runs with
	// b.mu held: an unbounded handshake here is a bridge-wide deadlock, not
	// just a slow request.
	ctx, cancel := context.WithTimeout(context.Background(), lspInitTimeout)
	defer cancel()
	if _, err := srv.CallContext(ctx, "initialize", initReq); err != nil {
		srv.Close()
		return nil, fmt.Errorf("initialize failed: %w", err)
	}
	if err := srv.Notify("initialized", map[string]any{}); err != nil {
		srv.Close()
		return nil, fmt.Errorf("initialized notification failed: %w", err)
	}

	b.servers[lang] = srv
	return srv, nil
}

type LSPServer struct {
	cmd     *exec.Cmd
	in      io.WriteCloser
	out     *bufio.Reader
	nextID  int64
	mu      sync.Mutex
	pending map[int64]chan []byte

	// done is closed exactly once, by readLoop, when the server's stream ends —
	// whether it exited, was killed, or was abandoned for violating the framing
	// rules. It is what turns "wait forever" into "fail immediately" for every
	// caller blocked in CallContext.
	done     chan struct{}
	doneOnce sync.Once
	closed   atomic.Bool

	// closeOnce guards the process teardown specifically. exec.Cmd.Wait must be
	// called exactly once — two concurrent Waits race on the Cmd's internal
	// state, which the race detector flags at os/exec/exec.go:926. Close is
	// reached from several directions (a failed handshake, bridge shutdown, a
	// dead-server replacement), so "called twice" is the normal case, not an
	// edge one.
	closeOnce sync.Once
}

// Call issues a request bounded by lspCallTimeout.
func (s *LSPServer) Call(method string, params any) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), lspCallTimeout)
	defer cancel()
	return s.CallContext(ctx, method, params)
}

// CallContext issues a request and waits for the matching response, the
// server's death, or ctx expiry — whichever comes first. Every exit path
// removes the pending entry, so a timed-out request cannot leak its channel.
func (s *LSPServer) CallContext(ctx context.Context, method string, params any) ([]byte, error) {
	id := atomic.AddInt64(&s.nextID, 1)
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding %s request: %w", method, err)
	}

	ch := make(chan []byte, 1)

	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		return nil, errServerGone
	default:
	}
	s.pending[id] = ch
	_, werr := fmt.Fprintf(s.in, "Content-Length: %d\r\n\r\n%s", len(body), body)
	s.mu.Unlock()

	if werr != nil {
		s.forget(id)
		return nil, fmt.Errorf("writing %s request: %w", method, werr)
	}

	select {
	case res := <-ch:
		var r map[string]json.RawMessage
		if err := json.Unmarshal(res, &r); err != nil {
			return nil, err
		}
		if errRaw, ok := r["error"]; ok && string(errRaw) != "null" {
			return nil, fmt.Errorf("LSP error: %s", string(errRaw))
		}
		return r["result"], nil
	case <-s.done:
		s.forget(id)
		return nil, errServerGone
	case <-ctx.Done():
		s.forget(id)
		return nil, fmt.Errorf("%s timed out after %s: the language server accepted the request and did not answer",
			method, lspCallTimeout)
	}
}

func (s *LSPServer) forget(id int64) {
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
}

// alive reports whether the server's stream is still open.
func (s *LSPServer) alive() bool {
	select {
	case <-s.done:
		return false
	default:
		return !s.closed.Load()
	}
}

func (s *LSPServer) Notify(method string, params any) error {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = fmt.Fprintf(s.in, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return err
}

// readLoop demultiplexes responses onto their waiting callers until the stream
// ends or breaks a framing rule.
func (s *LSPServer) readLoop() {
	// A containment boundary, not a substitute for the length checks below.
	// This goroutine has no parent to recover for it, so a panic here is
	// process death for the daemon. The negative-length panic that motivated
	// this is fixed at its source; this stops the NEXT one from being fatal.
	defer func() {
		if r := recover(); r != nil {
			// Deliberately silent about the payload: it originates from an
			// untrusted process and this is a shutdown path.
			_ = r
		}
	}()
	defer s.shutdown()

	for {
		length, err := readHeaders(s.out)
		if err != nil {
			return
		}

		body := make([]byte, length)
		if _, err := io.ReadFull(s.out, body); err != nil {
			return
		}

		var r map[string]json.RawMessage
		if json.Unmarshal(body, &r) != nil {
			continue // a message we cannot parse is skipped, not fatal
		}
		idRaw, ok := r["id"]
		if !ok {
			continue // a notification from the server; nothing is waiting
		}
		var id int64
		if json.Unmarshal(idRaw, &id) != nil {
			continue
		}
		s.mu.Lock()
		if ch, ok := s.pending[id]; ok {
			ch <- body // buffered, so this cannot block even if the caller left
			delete(s.pending, id)
		}
		s.mu.Unlock()
	}
}

// readHeaders consumes one message's header block and returns its body length.
//
// It reads until the blank line that terminates the block, rather than assuming
// Content-Length is last — a server that sends the equally legal Content-Type
// after it used to desynchronise the stream permanently.
//
// A length that is absent, negative, or over maxLSPMessageBytes ends the
// stream. Following stdioclient's reasoning: a stream that has already produced
// an impossible frame is not one to keep reading from hoping for a valid one,
// and there is no way to resynchronise without trusting the number that was
// just rejected.
func readHeaders(out *bufio.Reader) (int, error) {
	length := -1
	for {
		line, err := out.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if v, ok := strings.CutPrefix(line, "Content-Length:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return 0, fmt.Errorf("unparseable Content-Length %q", v)
			}
			length = n
		}
	}
	switch {
	case length < 0:
		return 0, fmt.Errorf("message with a missing or negative Content-Length (%d)", length)
	case length > maxLSPMessageBytes:
		return 0, fmt.Errorf("message of %d bytes exceeds the %d byte limit", length, maxLSPMessageBytes)
	}
	return length, nil
}

// shutdown closes done exactly once, releasing every blocked caller.
func (s *LSPServer) shutdown() {
	s.doneOnce.Do(func() { close(s.done) })
}

// Close kills the server and releases everyone waiting on it.
func (s *LSPServer) Close() {
	s.closed.Store(true)
	s.shutdown()
	s.closeOnce.Do(func() {
		if s.in != nil {
			_ = s.in.Close()
		}
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
			go func() { _ = s.cmd.Wait() }() // reap, so it does not become a zombie
		}
	})
}
