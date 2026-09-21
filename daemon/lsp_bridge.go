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

	"mochiii/daemon/mcp"
	"mochiii/editapply"
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

	// THE HEADER BLOCK, AND THE THIRD CATEGORY THIS FILE DID NOT HAVE.
	//
	// maxLSPMessageBytes above bounds the BODY: readHeaders returns a length,
	// readLoop refuses one over the cap, and io.ReadFull then reads into a slice
	// of exactly that size -- bounded by the caller, which is why the body was
	// never the problem.
	//
	// Nothing bounded the HEADER. readHeaders looped on out.ReadString('\n'),
	// and bufio.Reader.ReadString accumulates fragments until the delimiter
	// arrives. MEASURED 2026-09-16: 135,962,152 bytes allocated over a 64 MiB
	// newline-free stream -- twice the input, sixteen times the body cap, and a
	// number the language server chose. This file's own header calls that server
	// UNTRUSTED INPUT, because project config can load plugins.
	//
	// TWO DIMENSIONS, TWO CONSTANTS, and they are not interchangeable. A line cap
	// does not bound how MANY lines arrive, and a count cap does not bound how
	// long one line is. Measured separately: an endless run of well-formed
	// "X-Pad: y\r\n" headers held readHeaders in its loop past a 10s deadline
	// with no allocation growth at all -- a hang, not an OOM, and the goroutine
	// it hangs is readLoop, which every LSP caller waits behind.
	//
	// The values are generous rather than tuned. LSP in practice sends two
	// headers, Content-Length and optionally Content-Type, neither over ~40
	// bytes. 8 KiB and 64 lines are both far past any legitimate server and far
	// short of anything that matters to this process.
	maxLSPHeaderLineBytes = 8 * 1024
	maxLSPHeaderLines     = 64

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

// errLaunchNotApproved reports that no language server was running for the
// language and nobody approved starting one.
//
// THE BRIDGE FAILS CLOSED, and this is the whole of register item 32's fix at
// the one place a server starts. It used to spawn whenever asked, and the only
// consent anywhere was to the tool CALL, three frames up -- so the first
// approved lookup started gopls without the user ever being asked about gopls,
// and every later prompt asked about a process already running. Now GetServer
// starts nothing unless the context carries an approval for exactly this launch
// (withApprovedLaunch), which only the agent loop grants, and only from the
// answer to a prompt that said "STARTS <program>".
//
// The bridge never asks. Asking means waiting on a human, possibly for minutes,
// and every other connection's language-server calls would wait with it behind
// b.mu. It refuses instead, and the question is asked from the one place that
// already asks every other question (resolveExecutable).
var errLaunchNotApproved = errors.New("the language server is not running, and starting it was not approved")

// approvedLaunchKey is the context key an approved launch travels under.
type approvedLaunchKey struct{}

// withApprovedLaunch records that the user approved starting the language
// server identified by key (an editapply.Language) for the call ctx is for.
//
// One key per context: a call is about one file, so it can start at most one
// server, and a set would only be a place for a second approval to hide.
func withApprovedLaunch(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, approvedLaunchKey{}, key)
}

// launchApproved reports whether ctx carries an approval to start the server
// for key.
func launchApproved(ctx context.Context, key string) bool {
	got, ok := ctx.Value(approvedLaunchKey{}).(string)
	return ok && key != "" && got == key
}

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
	servers   map[editapply.Language]*LSPServer
	mu        sync.Mutex
}

func NewLSPBridge(workspace string) *LSPBridge {
	return &LSPBridge{
		workspace: workspace,
		servers:   make(map[editapply.Language]*LSPServer),
	}
}

// Close shuts down all managed language servers.
func (b *LSPBridge) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, srv := range b.servers {
		srv.Close()
	}
	b.servers = make(map[editapply.Language]*LSPServer)
}

// serverCommand maps a language to the binary that serves it.
//
// THE PARAMETER IS TYPED, AND THAT IS THE ENFORCEMENT. It used to take a bare
// string and accept the aliases "ts", "js" and "py" alongside the real names --
// aliases that existed only because callers derived a language themselves, from
// their own private copy of an extension switch. Two such copies existed, byte
// for byte identical, and both opened by DEFAULTING an unrecognised file to Go
// (see editapply.Language). Taking editapply.Language here does not merely
// forbid a third copy; it makes one impossible to write, because there is now no
// way to reach this function except through the one table that produces the
// type. The aliases went with it: nothing can spell a language any more, so
// nothing can misspell one.
func serverCommand(lang editapply.Language) (string, error) {
	switch lang {
	case editapply.LangGo:
		return "gopls", nil
	case editapply.LangTypeScript, editapply.LangJavaScript:
		return "typescript-language-server", nil
	case editapply.LangPython:
		return "pyright-langserver", nil
	default:
		return "", fmt.Errorf("unsupported language for LSP: %s", lang)
	}
}

// lspServerForFile resolves the language server that can answer for full, or a
// descriptive error saying why none can.
//
// THIS IS THE SITE THE DUPLICATE SWITCHES USED TO OCCUPY. builtinLSPDefinition,
// builtinLSPReferences and builtinProposeASTEdit each carried their own copy of
// "extension -> language -> bridge -> GetServer", and the two extension switches
// were identical down to the byte. One function now, so the next tool that needs
// a language server inherits the LangUnknown refusal rather than reinventing the
// default that caused the bug.
//
// ctx is the tool call's own context, and it matters: it is what carries an
// approval to START a server (withApprovedLaunch). A server already running
// needs none.
func (s *Server) lspServerForFile(ctx context.Context, full string) (*LSPServer, error) {
	lang := editapply.LanguageOf(full)
	if lang == editapply.LangUnknown {
		// NAMED, NOT DEFAULTED. The old code sent this file to gopls, which
		// answered honestly that it held no symbols -- and the caller reported
		// that as "symbol not found", telling the user their code was wrong when
		// the truth was that we had asked the wrong compiler.
		return nil, fmt.Errorf("no language server is configured for %s; language-server tools cover %s",
			describeFileExt(full), englishList(languageNames()))
	}

	// A nil bridge is a configuration state, not a crash. main.go always sets
	// one, but nothing enforced that: every other Server construction -- a
	// one-shot subcommand, a test, whatever is written next -- reached GetServer
	// on a nil pointer and took the daemon's goroutine down with it. handleConn's
	// recover() would have contained it, at the cost of the user's turn and a
	// counted panic.
	if s.lspBridge == nil {
		return nil, fmt.Errorf("language-server support is not available in this daemon")
	}

	srv, err := s.lspBridge.GetServer(ctx, lang)
	if errors.Is(err, errLaunchNotApproved) {
		// Reachable in one ordinary way: the loop saw the server running when
		// it decided not to ask, and it exited before this call reached it. The
		// model is told what happened and what the next attempt will do, rather
		// than "failed to get language server", which reads like a broken
		// install.
		program, _ := serverCommand(lang)
		return nil, fmt.Errorf("%s is not running and was not approved to start for this call; "+
			"calling the tool again will ask the user to start it", program)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get language server for %s: %v", lang, err)
	}
	return srv, nil
}

// lspLaunchPlan is the Launch probe for every tool that reaches
// lspServerForFile: it says, before the call runs, which language server the
// call would talk to and whether it would have to start it.
//
// It RESOLVES THE PATH EXACTLY AS THE HANDLERS DO -- realWorkspaceRoot, then
// editapply.ResolveSafeTargetPath, then editapply.LanguageOf -- so the program
// the user is asked about is the program the handler will reach. Anything the
// handler would refuse (bad JSON, a path outside the workspace, an unsupported
// extension, no bridge) is a zero plan here: the call starts nothing, and the
// handler reports its own error in its own words.
//
// No side effects, by contract: this runs before consent exists.
func (s *Server) lspLaunchPlan(raw json.RawMessage) mcp.LaunchPlan {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &args); err != nil || strings.TrimSpace(args.Path) == "" {
		return mcp.LaunchPlan{}
	}
	if s.lspBridge == nil {
		return mcp.LaunchPlan{}
	}
	realRoot, err := s.realWorkspaceRoot()
	if err != nil {
		return mcp.LaunchPlan{}
	}
	full, err := editapply.ResolveSafeTargetPath(realRoot, args.Path)
	if err != nil {
		return mcp.LaunchPlan{}
	}
	lang := editapply.LanguageOf(full)
	program, err := serverCommand(lang)
	if err != nil {
		return mcp.LaunchPlan{}
	}
	return mcp.LaunchPlan{
		Needed:  !s.lspBridge.Running(lang),
		Program: program,
		Key:     string(lang),
	}
}

// describeFileExt names a path's extension for a refusal message, in the same
// shape editapply's own gate messages use.
func describeFileExt(path string) string {
	if ext := filepath.Ext(path); ext != "" {
		return ext + " files"
	}
	return "files with no extension"
}

// languageNames renders the language table's own contents, so a message about
// what is supported cannot drift from what is supported.
func languageNames() []string {
	langs := editapply.KnownLanguages()
	names := make([]string, 0, len(langs))
	for _, l := range langs {
		names = append(names, string(l))
	}
	return names
}

// englishList joins names as a person would read them: "a, b and c".
func englishList(names []string) string {
	switch len(names) {
	case 0:
		return "no languages"
	case 1:
		return names[0]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}

// Running reports whether a live language server for lang exists right now,
// without starting one. It is how the loop learns, before asking, whether a call
// would launch anything (see lspLaunchPlan).
func (b *LSPBridge) Running(lang editapply.Language) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	srv, ok := b.servers[lang]
	return ok && srv.alive()
}

// GetServer returns the running language server for lang, or starts one -- but
// ONLY IF ctx carries an approval for that launch (withApprovedLaunch). Without
// one it returns errLaunchNotApproved and starts nothing. See that error for why
// the bridge refuses rather than asks.
//
// A server already running needs no approval: the user consented when it
// started, and handing it out again launches nothing.
func (b *LSPBridge) GetServer(ctx context.Context, lang editapply.Language) (*LSPServer, error) {
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

	// Unsupported first, so an unknown language is still reported as unknown
	// rather than as a launch nobody approved.
	cmdName, err := serverCommand(lang)
	if err != nil {
		return nil, err
	}

	// THE GATE. Everything below this line starts a third-party process that
	// reads project-supplied configuration, and nothing below it may run on
	// behalf of a caller who was not told that is what would happen.
	if !launchApproved(ctx, string(lang)) {
		return nil, errLaunchNotApproved
	}

	cmd := exec.Command(cmdName)
	if cmdName == "typescript-language-server" || cmdName == "pyright-langserver" {
		cmd.Args = append(cmd.Args, "--stdio")
	}

	cmd.Dir = b.workspace
	// NEVER nil. A nil Env hands this third-party subprocess the daemon's whole
	// environment, including OPENROUTER_API_KEY and MOCHIII_PROXY_KEY.
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
	for lines := 0; ; lines++ {
		if lines >= maxLSPHeaderLines {
			return 0, fmt.Errorf("header block reached %d lines with no terminating blank line",
				maxLSPHeaderLines)
		}
		line, err := readBoundedLine(out, maxLSPHeaderLineBytes)
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

// readBoundedLine reads up to and including the next '\n', allocating at most
// max bytes, and refuses rather than growing past it.
//
// ReadByte rather than ReadString, and that is the entire fix. ReadString's
// length is whatever arrives before the delimiter; this one's is max. The reads
// are still buffered -- bufio.Reader fills 4 KiB at a time underneath -- so the
// syscall count is unchanged and only the allocation is bounded.
//
// The newline is consumed and not returned, matching what the caller did with
// ReadString's result: it TrimSpace'd it, so "\r\n" and "\n" were already
// equivalent here.
func readBoundedLine(out *bufio.Reader, max int) (string, error) {
	var b strings.Builder
	for {
		c, err := out.ReadByte()
		if err != nil {
			return "", err
		}
		if c == '\n' {
			return b.String(), nil
		}
		if b.Len() >= max {
			return "", fmt.Errorf("header line reached %d bytes with no newline", max)
		}
		b.WriteByte(c)
	}
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
