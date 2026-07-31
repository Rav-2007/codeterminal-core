package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"

	"codeterminal/daemon/mcp"
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
func (s *Server) buildRegistry(ctx context.Context, logger *log.Logger) (*mcp.Registry, []error) {
	cfg := s.cfg
	registry := mcp.NewRegistry(configPolicy{cfg: cfg}, cfg.MCP.Budget.resolvedMaxAdvertisedTools())

	if !cfg.MCP.Builtin.Disabled {
		for _, b := range s.builtinTools() {
			if err := registry.RegisterBuiltin(b); err != nil {
				// A duplicate or malformed built-in is our own bug, not the
				// user's config, so it is loud rather than silent.
				logger.Printf("mcp: refusing to register built-in tool: %v", err)
			}
		}
	}

	var errs []error
	for _, name := range sortedServerNames(cfg.MCP.Servers) {
		srv := cfg.MCP.Servers[name]
		if srv.Disabled || !srv.AcknowledgedUnconfined {
			continue
		}

		client, err := mcp.Connect(ctx, mcp.LaunchConfig{
			Name:     name,
			Command:  srv.Command,
			Args:     srv.Args,
			EnvAllow: srv.Env,
			Stderr:   serverStderr(name, logger),
		})
		if err != nil {
			logger.Printf("mcp: server %s unavailable: %v", name, err)
			errs = append(errs, fmt.Errorf("server %s: %w", name, err))
			continue
		}
		if err := registry.AddServer(name, client); err != nil {
			logger.Printf("mcp: refusing server %s: %v", name, err)
			_ = client.Close()
			errs = append(errs, err)
			continue
		}
		logger.Printf("mcp: connected server %s (lane=third_party, unconfined)", name)
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
func (w *prefixWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:i]), "\r")
		w.buf = w.buf[i+1:]
		if line != "" {
			w.logger.Print(w.prefix + line)
		}
	}
	return len(p), nil
}
