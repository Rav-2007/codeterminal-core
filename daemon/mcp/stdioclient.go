package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"codeterminal/protocol"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// This is the ONLY file in the daemon that imports the MCP SDK. Everything else
// goes through the Client interface in mcp.go, so a spec change or an SDK swap
// lands here and nowhere else.
//
// WHY THE SDK RATHER THAN ~500 LINES OF HAND-ROLLED JSON-RPC. The protocol is
// large and actively moving -- the 2026-07-28 revision deprecated logging,
// sampling and roots in favour of MRTR -- and the entire point of MCP support
// is interoperating with servers we did not write. Matching the reference
// implementation's behaviour is how that works; owning a partial reimplementation
// is how it slowly stops working. The cost is a dependency the daemon reaches
// only on the opt-in Lane B path.

const (
	// connectTimeout bounds the initialize handshake. A server that cannot
	// introduce itself in this long is not going to serve a tool call inside a
	// turn budget either.
	connectTimeout = 20 * time.Second

	// stopGrace is how long a server gets to exit after its transport closes,
	// before SIGKILL. Mirrors helperproc.go's Stop.
	stopGrace = 2 * time.Second

	// DefaultMaxMessageBytes bounds ONE JSON-RPC message from a server.
	//
	// Without it there is no bound at all. The SDK's stdio transport is a bare
	// json.Decoder over the subprocess's stdout (go-sdk v1.7.0,
	// mcp/transport.go newIOConn) -- no io.LimitReader, and nothing above it
	// caps anything either, because renderToolResult's max_tool_result_bytes
	// applies to a string the daemon has ALREADY received.
	//
	// Measured before this existed: a server returning an N MiB response drove
	// peak heap to 12.0x N, linearly, across 4/8/32/64 MiB. 64 MiB on the wire
	// was 806 MB of live heap. The sharper half is that tools/list is read at
	// registry-build time, so it lands BEFORE the loop exists and therefore
	// before any approval prompt could have been shown: a user who configured
	// a server and typed one prompt has already taken it, having authorised
	// nothing.
	//
	// 2 MiB is twice maxMaxToolResultBytes -- the largest single result the
	// daemon will ever forward -- which leaves room for JSON envelope and
	// escaping while keeping the worst case around 25 MB of heap rather than
	// unbounded.
	DefaultMaxMessageBytes = 2 * 1024 * 1024
)

// StdioClient is a Lane B MCP server: a subprocess spoken to over stdio.
type StdioClient struct {
	name string
	logf func(format string, args ...any)

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	session *sdk.ClientSession
	closed  bool
}

// logf is nil-safe: a client built without a logger discards its diagnostics
// rather than making every call site check.
func (c *StdioClient) logfSafe(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

// LaunchConfig is everything needed to start one server. Deliberately not the
// daemon's config struct: this package must not depend on the daemon's config
// shape, so the caller translates.
type LaunchConfig struct {
	Name    string
	Command string
	Args    []string
	// EnvAllow names variables to pass through. See ServerEnv -- PATH and HOME
	// come free, this product's own credentials never come at all.
	EnvAllow []string
	// Stderr receives the server's stderr, prefixed by the caller. Nil
	// discards it.
	Stderr io.Writer
	// Logf receives THIS DAEMON'S diagnostics about the server, kept separate
	// from Stderr so a line the daemon wrote is never mistaken for a line the
	// server wrote. Nil discards them.
	Logf func(format string, args ...any)
	// MaxMessageBytes bounds one JSON-RPC message from the server. Zero means
	// DefaultMaxMessageBytes; there is no way to ask for no bound.
	MaxMessageBytes int
}

// Connect starts the server and completes the MCP initialize handshake.
//
// The subprocess is constructed HERE rather than handed to the SDK to build,
// which is what makes the environment discipline enforceable: sdk.CommandTransport
// takes an *exec.Cmd we own, so cmd.Env is ours to set. Verified against a real
// server in the Phase 2 spike -- the child saw only HOME, PATH and the one
// allow-listed variable, with none of this product's credentials present.
//
// The spike also established that cmd.Process IS populated after Connect, so we
// retain the handle and can enforce our own teardown rather than trusting the
// transport's. See Close.
func Connect(ctx context.Context, cfg LaunchConfig) (*StdioClient, error) {
	if err := ValidateServerName(cfg.Name); err != nil {
		return nil, err
	}
	if err := ValidateEnvAllowList(cfg.EnvAllow); err != nil {
		return nil, fmt.Errorf("server %s: %w", cfg.Name, err)
	}
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, fmt.Errorf("server %s has no command to run", cfg.Name)
	}

	// exec.Command, never a shell. Args is a []string all the way down, so
	// there is no command string for a metacharacter to live in and nothing to
	// quote. A server name or argument containing a semicolon is just a
	// semicolon.
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = ServerEnv(cfg.EnvAllow)
	cmd.Stderr = cfg.Stderr

	// ITS OWN PROCESS GROUP, so teardown can reach what it spawned.
	//
	// helperproc.go does NOT do this, and the difference is the point: the
	// embedder helper is our own binary, we know it forks nothing, and killing
	// its pid is killing all of it. An MCP server is somebody else's program.
	// Confirmed with testdata/badserver's orphan mode -- a server that starts a
	// child and exits leaves that child running with the user's full privileges
	// after Close returns success, because Close signalled one pid.
	//
	// A grandchild of a Lane B server is as unconfined as its parent, and it
	// outlives the turn the user approved. Setpgid is what makes "the turn
	// ended" and "the programs it started are gone" the same event.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// THE PIPES ARE OURS, WHICH IS THE WHOLE POINT OF NOT USING
	// sdk.CommandTransport HERE.
	//
	// CommandTransport builds the pipes and starts the process itself, handing
	// the SDK a reader we never see -- and that reader is an unbounded
	// json.Decoder. Owning the plumbing costs about fifteen lines and buys the
	// one thing the transport cannot be asked for: a bound on how much a
	// subprocess can make this daemon allocate. It also puts cmd.Start on this
	// side of the call, which is what lets the process group be set (see Close).
	//
	// The trade is that reaping the process is now ours too. Close does it.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: server %s: %v", ErrServerUnavailable, cfg.Name, err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: server %s: %v", ErrServerUnavailable, cfg.Name, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: server %s failed to start: %v", ErrServerUnavailable, cfg.Name, err)
	}

	limit := cfg.MaxMessageBytes
	if limit <= 0 {
		limit = DefaultMaxMessageBytes
	}
	bounded := &messageLimitReader{r: stdout, max: limit, server: cfg.Name}

	client := sdk.NewClient(&sdk.Implementation{
		Name:    "codeterminal",
		Version: "v1",
	}, nil)

	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	c := &StdioClient{name: cfg.Name, logf: cfg.Logf, cmd: cmd, stdin: stdin}

	session, err := client.Connect(connectCtx,
		&sdk.IOTransport{Reader: io.NopCloser(bounded), Writer: stdin}, nil)
	if err != nil {
		// The process is already running at this point, so a failed handshake
		// must not leave it behind. Close is idempotent and does the reaping.
		_ = c.Close()
		return nil, fmt.Errorf("%w: server %s failed to start: %v", ErrServerUnavailable, cfg.Name, err)
	}

	c.mu.Lock()
	c.session = session
	c.mu.Unlock()
	return c, nil
}

// messageLimitReader fails the stream when a SINGLE newline-delimited message
// exceeds max bytes.
//
// PER MESSAGE, NOT PER STREAM, and the distinction is the whole design. An
// io.LimitReader over the connection would cap the total bytes a server may
// ever send, which would kill a long and perfectly well-behaved session after
// enough legitimate traffic. What needs bounding is one allocation: the SDK
// decodes each message into a single json.RawMessage, so the size of one
// message is the size of one buffer.
//
// Counting from the last newline is exact rather than approximate here: the
// transport is newline-delimited JSON by definition (both IOTransport and
// StdioTransport say so), so bytes-since-newline IS the length of the message
// being accumulated.
//
// Once tripped it stays tripped. A stream that has already produced an
// over-long message is not one to keep reading from hoping for a short one.
type messageLimitReader struct {
	r       io.Reader
	max     int
	server  string
	sinceNL int
	tripped bool
}

func (l *messageLimitReader) Read(p []byte) (int, error) {
	if l.tripped {
		return 0, l.err()
	}
	n, err := l.r.Read(p)
	if n > 0 {
		if i := bytes.LastIndexByte(p[:n], '\n'); i >= 0 {
			l.sinceNL = n - i - 1
		} else {
			l.sinceNL += n
		}
		if l.sinceNL > l.max {
			l.tripped = true
			return 0, l.err()
		}
	}
	return n, err
}

func (l *messageLimitReader) err() error {
	return fmt.Errorf("%w: server %s sent a single message larger than the %d byte limit; "+
		"reading it in full is how an unconfined subprocess exhausts this daemon's memory",
		ErrServerUnavailable, l.server, l.max)
}

// ListTools returns the server's advertised tools, translated into this
// package's Tool.
//
// Every tool comes back Confined:false and Lane:third_party, unconditionally
// and without consulting anything the server said. That is not a default this
// function computes -- it is a fact about what a subprocess is, and there is no
// input that changes it.
func (c *StdioClient) ListTools(ctx context.Context) ([]Tool, error) {
	c.mu.Lock()
	session := c.session
	closed := c.closed
	c.mu.Unlock()

	if closed || session == nil {
		return nil, fmt.Errorf("%w: server %s is not connected", ErrServerUnavailable, c.name)
	}

	res, err := session.ListTools(ctx, &sdk.ListToolsParams{})
	if err != nil {
		return nil, fmt.Errorf("%w: listing tools on server %s: %v", ErrServerUnavailable, c.name, err)
	}

	tools := make([]Tool, 0, len(res.Tools))
	for _, t := range res.Tools {
		if err := ValidateToolName(t.Name); err != nil {
			// Skipped for the same reason and at the same cost as an
			// unserialisable schema below: one unusable tool must not cost the
			// user the server's other tools. See ValidateToolName for why a
			// name is a security-relevant string rather than a label.
			c.logfSafe("mcp: server %s offered a tool this daemon will not advertise: %v", c.name, err)
			continue
		}
		schema, err := json.Marshal(t.InputSchema)
		if err != nil {
			// A tool whose schema will not serialise cannot be advertised to
			// the model: it would arrive as an unusable entry that only widens
			// the menu. Skipped rather than fatal -- one broken tool must not
			// cost the user the server's other tools.
			continue
		}
		tools = append(tools, Tool{
			Server:       c.name,
			Name:         t.Name,
			Description:  t.Description,
			Schema:       schema,
			Lane:         protocol.LaneThirdParty,
			Confined:     false,
			ReadOnlyHint: annotationBool(t, readOnly),
			Destructive:  annotationBool(t, destructive),
		})
	}
	SortTools(tools)
	return tools, nil
}

type annotationKind int

const (
	readOnly annotationKind = iota
	destructive
)

// annotationBool reads a server's own hint about its tool, defensively.
//
// These are DISPLAY ONLY (see Tool.ReadOnlyHint). The defaults are chosen so
// that a server saying nothing produces the least reassuring prompt: absent
// readOnlyHint reads as "not known to be read-only", absent destructiveHint as
// "not known to be destructive" -- neither is treated as a claim of safety,
// because neither is one.
func annotationBool(t *sdk.Tool, kind annotationKind) bool {
	if t.Annotations == nil {
		return false
	}
	switch kind {
	case readOnly:
		return t.Annotations.ReadOnlyHint
	case destructive:
		// The SDK models this as *bool because the spec's default is true.
		// A server that omits it is saying nothing, and "nothing" must not
		// become "safe" -- so an absent hint reads as destructive.
		if t.Annotations.DestructiveHint == nil {
			return true
		}
		return *t.Annotations.DestructiveHint
	}
	return false
}

// isTransportDeath reports whether an error means the connection is gone, as
// distinct from the call having failed.
//
// Sentinel checks first, because those are contractual. The string check is
// the ugly part and it is deliberate: the SDK surfaces a closed pipe as prose
// wrapping an unexported error, so there is nothing else to match on. It is
// narrow, and it is additive -- a future SDK that returns a proper sentinel is
// caught by the lines above without this ever being consulted.
func isTransportDeath(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe) || errors.Is(err, ErrServerUnavailable) {
		return true
	}
	text := err.Error()
	return strings.HasSuffix(text, ": EOF") ||
		strings.Contains(text, "file already closed") ||
		strings.Contains(text, "broken pipe") ||
		strings.Contains(text, "connection closed")
}

// CallTool invokes one tool and flattens its content into text for the model.
//
// args is the model's raw JSON argument object. It is decoded here rather than
// passed through opaquely because the SDK takes a map; a malformed object is
// refused as a tool error the model can see and correct, never guessed at.
func (c *StdioClient) CallTool(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	c.mu.Lock()
	session := c.session
	closed := c.closed
	c.mu.Unlock()

	if closed || session == nil {
		return Result{}, fmt.Errorf("%w: server %s is not connected", ErrServerUnavailable, c.name)
	}

	var decoded map[string]any
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &decoded); err != nil {
			return Result{
				Content: fmt.Sprintf("the arguments were not a valid JSON object: %v", err),
				IsError: true,
			}, nil
		}
	}

	res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: decoded})
	if err != nil {
		// A SERVER THAT DIED MID-CALL IS AN UNAVAILABLE SERVER, and it must say
		// so in a way the caller can branch on.
		//
		// The SDK reports this as a bare io.EOF wrapped in prose: the transport
		// noticed stdout close and had no other information. Passed through
		// unclassified, the loop could not tell "this server is gone" from "this
		// tool failed", so it reported the generic "the tool failed to run" and
		// the degradation notice the user should have seen never fired.
		//
		// This is the likeliest Lane B misbehaviour in the wild and it needs no
		// malice at all -- a panic in somebody else's tool handler does it.
		if isTransportDeath(err) {
			return Result{}, fmt.Errorf("%w: server %s stopped responding during %s: %v",
				ErrServerUnavailable, c.name, name, err)
		}
		return Result{}, fmt.Errorf("calling %s on server %s: %w", name, c.name, err)
	}

	return Result{Content: flattenContent(res.Content), IsError: res.IsError}, nil
}

// flattenContent renders MCP content blocks as text for the model.
//
// Non-text blocks are named rather than dropped: a tool that returned an image
// has told the model something, and silently returning "" would leave the model
// to conclude the call produced nothing and try again. Naming the type is
// honest and cheap; rendering it is out of scope for a text completion.
func flattenContent(content []sdk.Content) string {
	var b strings.Builder
	for _, c := range content {
		switch v := c.(type) {
		case *sdk.TextContent:
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(v.Text)
		default:
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "[%T content omitted: this tool returned a non-text result]", v)
		}
	}
	return b.String()
}

// Close shuts the server down and confirms it is gone.
//
// The Phase 2 spike found that closing the session reaps a WELL-BEHAVED server
// in about a millisecond -- the transport closes its stdin and the server
// exits. This does not stop there, because a well-behaved server exiting
// cleanly is not the case worth defending against: a buggy or hostile one can
// ignore a closed stdin and keep running with whatever the user's privileges
// allow. So Close mirrors helperproc.go's Stop: ask, wait a bounded grace, then
// make certain.
//
// Since Connect owns cmd.Start, Close owns the reaping: it closes stdin (the
// spec's shutdown sequence), waits, escalates, and calls Wait so the child does
// not stay a zombie. Nothing else calls Wait, so this is the only place it can
// happen.
//
// Idempotent -- the registry may close a server that already died.
func (c *StdioClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	session, cmd, stdin := c.session, c.cmd, c.stdin
	c.session, c.cmd, c.stdin = nil, nil, nil
	c.mu.Unlock()

	var closeErr error
	if session != nil {
		closeErr = session.Close()
	}
	// "The client SHOULD initiate shutdown by first closing the input stream to
	// the child process" -- and a well-behaved server exits on that alone.
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd == nil || cmd.Process == nil {
		return closeErr
	}

	// Wait in the background: it is what releases the process-table entry, but
	// it blocks until the child is actually gone, and the whole point of the
	// escalation below is that a hostile child might not be.
	waited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waited)
	}()

	pid := cmd.Process.Pid

	// THE GROUP IS KILLED EVEN IF THE SERVER ITSELF EXITED CLEANLY.
	//
	// This is the part that is easy to get wrong, and the orphan case is
	// exactly it: a server can start a child, hand it stdout, and exit
	// immediately. Waiting for the server to be gone and then stopping -- which
	// is what this did -- reports success while the child it left behind keeps
	// running unconfined. So the group signal is unconditional, and the wait is
	// only about how long the SERVER gets to leave politely.
	if gone := waitGone(pid, stopGrace); !gone {
		// It has had its chance to exit on a closed stdin; SIGKILL is not
		// negotiable with a process that ignored that.
		_ = syscall.Kill(pid, syscall.SIGKILL)
		if !waitGone(pid, stopGrace) {
			killGroup(pid)
			return fmt.Errorf("mcp server %s (pid %d) survived SIGKILL", c.name, pid)
		}
	}
	killGroup(pid)
	<-waited
	return closeErr
}

// killGroup SIGKILLs everything in the server's process group.
//
// Negative pid means "the group whose id is pid", which is this server's group
// because Connect set Setpgid. Errors are discarded on purpose: ESRCH means the
// group is already empty, which is the outcome being asked for.
//
// It is safe to call after the leader has exited. A process group id is not
// reused while any member remains, so a negative signal either reaches the
// survivors or reaches nobody -- it cannot land on an unrelated process.
func killGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// waitGone polls for a process's disappearance, up to a grace period. Signal 0
// probes liveness without delivering anything -- an error means the pid is no
// longer ours to signal, which is what "gone" means here.
func waitGone(pid int, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return syscall.Kill(pid, 0) != nil
}
