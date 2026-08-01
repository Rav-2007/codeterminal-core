package mcp

import (
	"context"
	"encoding/json"
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
)

// StdioClient is a Lane B MCP server: a subprocess spoken to over stdio.
type StdioClient struct {
	name string
	logf func(format string, args ...any)

	mu      sync.Mutex
	cmd     *exec.Cmd
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

	client := sdk.NewClient(&sdk.Implementation{
		Name:    "codeterminal",
		Version: "v1",
	}, nil)

	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	session, err := client.Connect(connectCtx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: server %s failed to start: %v", ErrServerUnavailable, cfg.Name, err)
	}

	return &StdioClient{name: cfg.Name, logf: cfg.Logf, cmd: cmd, session: session}, nil
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
// Idempotent -- the registry may close a server that already died.
func (c *StdioClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	session, cmd := c.session, c.cmd
	c.session, c.cmd = nil, nil
	c.mu.Unlock()

	var closeErr error
	if session != nil {
		closeErr = session.Close()
	}
	if cmd == nil || cmd.Process == nil {
		return closeErr
	}

	if gone := waitGone(cmd.Process.Pid, stopGrace); gone {
		return closeErr
	}

	// Still there after the grace. It has had its chance to exit on a closed
	// stdin; SIGKILL is not negotiable with a process that ignored that.
	_ = cmd.Process.Kill()
	if !waitGone(cmd.Process.Pid, stopGrace) {
		return fmt.Errorf("mcp server %s (pid %d) survived SIGKILL", c.name, cmd.Process.Pid)
	}
	return closeErr
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
