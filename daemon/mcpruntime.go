package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"

	"mochiii/daemon/mcp"
)

// The bridge between the daemon's config and the tool registry.
//
// mcp/ deliberately knows nothing about models.json -- it takes a
// PolicyResolver and a set of connected clients. This file is the only place
// that translates one into the other, so there is exactly one answer to "what
// is this tool allowed to do?" and it is readable in one screen.

// configPolicy resolves a tool policy from the loaded config. It is the
// PolicyResolver the registry consults before advertising or dispatching
// anything.
type configPolicy struct{ cfg *Config }

// PolicyFor implements mcp.PolicyResolver.
//
// THE DEFAULTS ARE THE POINT. Every path that is not an explicit, readable
// "allow" in the user's config resolves to ask or deny:
//
//   - agent mode off            -> deny (nothing runs at all)
//   - built-ins disabled        -> deny for the builtin server
//   - unknown server            -> deny (it is not configured, so it is not ours)
//   - disabled server           -> deny
//   - tool unlisted             -> ask
//   - anything unrecognised     -> deny
//
// A resolver that returned allow on a path its author had not thought about is
// how a tool runs that nobody authorised, so there is no fallthrough here that
// reaches allow.
func (p configPolicy) PolicyFor(server, tool string) mcp.Policy {
	if p.cfg == nil || !p.cfg.MCP.Enabled {
		return mcp.PolicyDeny
	}

	if server == mcp.BuiltinServerName {
		if p.cfg.MCP.Builtin.Disabled {
			return mcp.PolicyDeny
		}
		return toRegistryPolicy(p.cfg.MCP.Builtin.policyFor(tool))
	}

	srv, ok := p.cfg.MCP.Servers[server]
	if !ok || srv.Disabled {
		return mcp.PolicyDeny
	}
	// A server that never cleared the acknowledgement gate must not have its
	// tools resolve to anything runnable, even if validation is somehow
	// bypassed -- Validate refuses such a config, and this is the second lock.
	if !srv.AcknowledgedUnconfined {
		return mcp.PolicyDeny
	}
	return toRegistryPolicy(srv.policyFor(tool))
}

// toRegistryPolicy maps a config string to a registry Policy. An unrecognised
// value is deny: the config loader already refuses those, so arriving here with
// one means something invented a permission.
func toRegistryPolicy(s string) mcp.Policy {
	switch s {
	case PolicyAllow:
		return mcp.PolicyAllow
	case PolicyAsk:
		return mcp.PolicyAsk
	case PolicyDeny:
		return mcp.PolicyDeny
	default:
		return mcp.PolicyDeny
	}
}

// laneBLaunchConfig builds the launch parameters for one third-party MCP server.
//
// EXTRACTED SO THE ABSENCE CAN BE ASSERTED. mcp.LaunchConfig has a Sandbox
// field, WrapCommand honours it, and this function deliberately does not set
// it -- so ResolveMode sees a zero config and returns SandboxNone, and a Lane B
// server runs with the user's own privileges. That is the documented posture
// (SECURITY_MODEL.md, "Lane B is unconfined... accepted deliberately"), not an
// oversight, but read inline it looks exactly like one: the plumbing is right
// there, unused.
//
// A DECISION NOBODY CAN SEE IS ONE SOMEBODY WILL QUIETLY REVERSE, in either
// direction -- a future edit "fixing" the omission would change the product's
// security posture without anyone reviewing that change, and a future reader
// assuming containment would misjudge what an approval is worth. Both are
// closed by TestLaneBLaunchesUnconfinedOnPurpose, which fails the moment this
// stops being true and sends whoever changed it to the document that says why.
//
// What DOES contain a Lane B server is consent, twice: AcknowledgedUnconfined
// at config time (the caller checks it before ever reaching here) and
// Tool.Confined == false at approval time, which is what the client prints
// over the prompt. Neither is a sandbox and neither is described as one.
func laneBLaunchConfig(name string, srv MCPServerConfig, cfg *Config, stderr io.Writer, logf func(string, ...any)) mcp.LaunchConfig {
	return mcp.LaunchConfig{
		Name:     name,
		Command:  srv.Command,
		Args:     srv.Args,
		EnvAllow: srv.Env,
		Stderr:   stderr,
		Logf:     logf,
		// The one budget field that bounds ALLOCATION rather than egress: what
		// this daemon will hold on behalf of somebody else's process.
		MaxMessageBytes: cfg.MCP.Budget.resolvedMaxMessageBytes(),
		ConnectTimeout:  cfg.MCP.Budget.resolvedConnectTimeout(),
	}
}

// buildRegistry assembles the tool surface for this daemon: the built-in Lane A
// tools, plus a connected client for each enabled Lane B server.
//
// Servers are connected LAZILY, by the caller, at the start of the first
// agent-mode turn -- not at daemon startup. A user who never uses agent mode
// never runs a third-party process, which is the difference between "you can
// configure MCP servers" and "we start programs on your machine".
//
// A server that fails to connect does not fail the build: its error is
// collected and surfaced as a protocol.DegradedMCPServer, because losing one
// server's tools should cost the user those tools and not their turn.
func (s *Server) buildRegistry(ctx context.Context, logger *log.Logger, proposals *proposalSink, mode string) (*mcp.Registry, []error) {
	cfg := s.cfg
	registry := mcp.NewRegistry(configPolicy{cfg: cfg}, cfg.MCP.Budget.resolvedMaxAdvertisedTools())

	if !cfg.MCP.Builtin.Disabled {
		for _, b := range s.builtinTools(proposals, mode) {
			if err := registry.RegisterBuiltin(b); err != nil {
				// A duplicate or malformed built-in is our own bug, not the
				// user's config, so it is loud rather than silent.
				logger.Printf("mcp: refusing to register built-in tool: %v", err)
			}
		}
	}

	// LANE B IS WITHHELD ENTIRELY IN PLAN MODE, and this is the half of the
	// plan-mode filter that was missing.
	//
	// The built-in filter above (builtinTools) withdrew propose_edit and
	// sandbox_exec and was recorded as closing the issue. It could not have:
	// it operates on the []mcp.Builtin slice and nothing else, while the
	// third-party servers are connected and registered HERE, sixty lines below
	// it, in a loop that never saw `mode`. So plan mode removed the reviewed,
	// confined, first-party write path and kept the unreviewed, unconfined,
	// third-party one -- which is, precisely, the inversion the built-in fix
	// was written to correct, one lane over.
	//
	// A user with a filesystem or shell MCP server configured got that server's
	// full tool surface in the mode whose entire promise is that nothing will be
	// touched, gated only by the same `ask` policy that governs normal mode.
	//
	// RETURNING BEFORE THE CONNECT LOOP, not filtering after it. Connecting is
	// not a read: mcp.Connect launches the server's command as a subprocess with
	// the user's privileges. Starting a third-party process is a side effect,
	// and a turn that promised not to act should not cause one -- so plan mode
	// must not reach the launch, rather than launch and then decline to
	// advertise.
	if isPlanMode(mode) {
		logger.Printf("mcp: plan mode -- %d third-party server(s) not connected", eligibleLaneBServers(cfg))
		return registry, nil
	}

	// CONNECTED IN PARALLEL, and the reason is a measurement rather than a
	// preference.
	//
	// This used to be a serial loop, and mcp.Connect bounds a handshake at
	// connect_timeout_seconds. So n servers that start but never answer cost
	// n x that timeout -- measured at 21s for one and 42s for two -- and every
	// second of it is spent BEFORE runAgentLoop creates the turn deadline, with
	// no token streamed and not even the degradation notice sent yet. A user
	// with three wedged servers waited a minute at a blank screen while
	// turn_timeout_seconds sat there not applying to any of it.
	//
	// In parallel the worst case is ONE timeout regardless of how many servers
	// are configured, which is a number that can be stated and reasoned about.
	// It is still not charged to the turn budget -- that remains true and is
	// recorded as such -- but it is now bounded by a constant instead of by how
	// many servers the user happens to have.
	type connectResult struct {
		name   string
		client *mcp.StdioClient
		err    error
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []connectResult
	)
	for _, name := range sortedServerNames(cfg.MCP.Servers) {
		srv := cfg.MCP.Servers[name]
		if srv.Disabled || !srv.AcknowledgedUnconfined {
			continue
		}

		wg.Add(1)
		go func(name string, srv MCPServerConfig) {
			defer wg.Done()
			// MEMO ITEM 5. recover() is per-goroutine, and this is a goroutine
			// handleConn's backstop cannot see. Without this, a panic while
			// decoding an untrusted subprocess's first bytes takes the whole
			// DAEMON down rather than the one connection that asked -- every
			// other workspace and every in-flight turn with it.
			//
			// OURS ONLY, and the limit is deliberate. go-sdk@v1.7.0/mcp has
			// zero recover() calls across its stdio decoder, readIncoming and
			// async handler; those goroutines are still uncovered and cannot be
			// covered from here. That remains open as a dependency-policy
			// decision, with the honest unknown attached: nobody has
			// established whether any input actually panics the SDK.
			defer recoverLaneBConnect(name, logger.Printf)
			client, err := mcp.Connect(ctx, laneBLaunchConfig(name, srv, cfg, serverStderr(name, logger), logger.Printf))
			mu.Lock()
			results = append(results, connectResult{name: name, client: client, err: err})
			mu.Unlock()
		}(name, srv)
	}
	wg.Wait()

	// Registration is serial and name-ordered even though connection was not.
	// Advertised() already sorts, but the daemon LOG should read the same way
	// twice for the same config -- an ordering that depends on which server
	// happened to answer first makes two runs look different when nothing is.
	sort.Slice(results, func(i, j int) bool { return results[i].name < results[j].name })

	var errs []error
	for _, r := range results {
		if r.err != nil {
			logger.Printf("mcp: server %s unavailable: %v", r.name, r.err)
			errs = append(errs, fmt.Errorf("server %s: %w", r.name, r.err))
			continue
		}
		if err := registry.AddServer(r.name, r.client); err != nil {
			logger.Printf("mcp: refusing server %s: %v", r.name, err)
			_ = r.client.Close()
			errs = append(errs, err)
			continue
		}
		logger.Printf("mcp: connected server %s (lane=third_party, unconfined)", r.name)
	}

	return registry, errs
}

// serverStderr prefixes an MCP server's stderr with its configured name before
// it reaches the daemon log, the same treatment helperproc.go gives the
// embedder. Without a prefix, three servers' diagnostics interleave into one
// unattributable stream.
func serverStderr(name string, logger *log.Logger) io.Writer {
	return &prefixWriter{prefix: "mcp/" + name + ": ", logger: logger}
}

type prefixWriter struct {
	prefix string
	logger *log.Logger
	buf    []byte
}

// Write splits on newlines so one log line per server line, rather than one per
// arbitrary read boundary. A partial line is held until its newline arrives.
//
// Every line is escaped before it is logged. The daemon's log is read in a
// terminal, and this is a stream an unconfined third-party subprocess writes
// whatever it likes to -- so without escaping, a server controls the cursor of
// anyone tailing the log. Same reasoning as ValidateToolName, different
// remedy: a log line is displayed rather than dispatched on, so it can be
// escaped and stay readable instead of being refused.
//
// BOUNDED, because this is a hostile stream. The server on the other end is
// unconfined and can write whatever it likes, including megabytes with no
// newline in them -- and a buffer that only drains on a newline would grow
// without limit while it did. That is M1a's exact shape: unbounded allocation
// driven by a misbehaving server, before any consent step exists. The M1a fix
// capped stdio MESSAGES (max_message_bytes); stderr does not go through that
// path and reaches here instead, so it is capped here.
//
// bytes.IndexByte rather than strings.IndexByte(string(w.buf), ...): the latter
// copies the entire buffer on every write, which turns a large accumulation
// into quadratic work on top of the unbounded memory.
func (w *prefixWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:i]), "\r")
		w.buf = w.buf[i+1:]
		if line != "" {
			w.logger.Print(w.prefix + mcp.SanitizeForDisplay(line))
		}
	}
	if len(w.buf) >= maxLogLineBytes {
		w.logger.Print(w.prefix + mcp.SanitizeForDisplay(string(w.buf)) + " [continues]")
		w.buf = w.buf[:0]
	}
	return len(p), nil
}

// recoverLaneBConnect contains a panic raised while connecting one Lane B
// server, so one misbehaving subprocess cannot end the daemon.
//
// It LOGS rather than swallowing silently: a contained panic that says nothing
// is indistinguishable from a server that simply did not connect, and those are
// different facts (M5). The server is left absent from the registry, which is
// the same outcome as a failed connection and is already handled downstream.
func recoverLaneBConnect(name string, logf func(string, ...any)) {
	if r := recover(); r != nil {
		logf("mcp: server %s PANICKED during connect and was contained: %v — "+
			"the server is unavailable; the daemon is still running", name, r)
	}
}

// sprintfLike is the formatting a logf-shaped callback applies, extracted so a
// test can capture what would have been logged without installing a logger.
func sprintfLike(format string, args ...any) string { return fmt.Sprintf(format, args...) }
