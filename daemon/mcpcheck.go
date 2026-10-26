package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// CHECK AND GET: what `claude mcp list` and `claude mcp get` give a user --
// "is this server working, and if not, why" -- without asking a question and
// waiting for a turn to find out.

// handshakeBound is how long a server's handshake may take: its own setting,
// the budget's, or the connect default.
func handshakeBound(srv MCPServerConfig, budget MCPBudgetConfig) time.Duration {
	if d := srv.connectTimeout(budget); d > 0 {
		return d
	}
	return mcp.DefaultConnectTimeout
}

// listDeadline bounds starting the given servers and listing their tools:
// the longest handshake among them, then the grace for the listing. It used
// to be the budget's timeout plus the grace, and an unset budget is 0 -- so a
// list gave every server 15 seconds while its own handshake was allowed 20,
// and a first `npx` run, which downloads the package, was cut off by the
// list rather than by its own bound.
func listDeadline(cfg *Config) time.Duration {
	longest := handshakeBound(MCPServerConfig{}, cfg.MCP.Budget)
	for _, srv := range cfg.MCP.Servers {
		longest = max(longest, handshakeBound(srv, cfg.MCP.Budget))
	}
	return longest + mcpListGrace
}

// launchers download what they run on first use, which is the usual reason a
// server times out once and then starts.
var downloadingLaunchers = map[string]bool{"npx": true, "uvx": true, "docker": true, "bunx": true, "pnpx": true}

// timeoutHint is said with a start that timed out under a launcher.
func timeoutHint(srv MCPServerConfig, err error) string {
	if err == nil || !downloadingLaunchers[strings.TrimSuffix(filepath.Base(srv.Command), ".exe")] {
		return ""
	}
	text := strings.ToLower(err.Error())
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(text, "timed out") && !strings.Contains(text, "deadline") {
		return ""
	}
	return fmt.Sprintf("%s downloads the package the first time it runs, which can take longer than the %s allowed. "+
		"Check again with a longer wait (`mcp check <name> --timeout 120`, or Check in VS Code), or set "+
		"connect_timeout_seconds on the server.", filepath.Base(srv.Command), handshakeBound(srv, MCPBudgetConfig{}))
}

// mcpCheck starts the named server -- or every enabled one, for "" -- now,
// lists its tools, shuts it down, and answers with how each fared. timeout,
// when set, is a longer wait for this check only.
func (s *Server) mcpCheck(ctx context.Context, req protocol.MCPRequest) protocol.MCPResponse {
	cfg, problems := s.agentConfig()
	name := strings.TrimSpace(req.Name)
	if name != "" {
		if _, ok := cfg.MCP.Servers[name]; !ok {
			return protocol.MCPResponse{Error: fmt.Sprintf("no server named %s is configured", name)}
		}
	}
	timeout := req.ConnectTimeoutSeconds
	if timeout < 0 || timeout > maxConnectTimeoutSeconds {
		return protocol.MCPResponse{Error: fmt.Sprintf("the wait is 1 to %d seconds", maxConnectTimeoutSeconds)}
	}

	// The servers to try: the one asked for, or all of them, each with the
	// longer wait when one was asked for. The built-ins come too, so the tool
	// count is what the model would be offered.
	only := *cfg
	only.MCP.Servers = map[string]MCPServerConfig{}
	for n, srv := range cfg.MCP.Servers {
		if (name != "" && n != name) || srv.Disabled {
			continue
		}
		if timeout > 0 {
			srv.ConnectTimeoutSeconds = timeout
		}
		only.MCP.Servers[n] = srv
	}
	resp := protocol.MCPResponse{Ok: true, AgentMode: cfg.MCP.Enabled, Notes: problems}
	if len(only.MCP.Servers) > 0 {
		cctx, cancel := context.WithTimeout(ctx, listDeadline(&only))
		defer cancel()
		registry, errs := s.buildRegistryFrom(cctx, &only, s.logger, &proposalSink{}, "")
		_, _ = registry.Advertised(cctx)
		s.mcpStates.listed(registry.Listed())
		_ = registry.Close()
		for _, err := range errs {
			for n, srv := range only.MCP.Servers {
				if strings.HasPrefix(err.Error(), "server "+n+":") {
					if hint := timeoutHint(srv, err); hint != "" {
						resp.Notes = append(resp.Notes, n+": "+hint)
					}
				}
			}
		}
	}
	resp.Servers = s.serverInfos(cfg)
	var lines []string
	for _, info := range resp.Servers {
		if name == "" || info.Name == name {
			lines = append(lines, info.Name+": "+info.StatusLine)
		}
	}
	resp.Detail = strings.Join(lines, "\n")
	return resp
}

// mcpGet answers with one server, described in full.
func (s *Server) mcpGet(req protocol.MCPRequest) protocol.MCPResponse {
	cfg, problems := s.agentConfig()
	name := strings.TrimSpace(req.Name)
	srv, ok := cfg.MCP.Servers[name]
	if !ok {
		return protocol.MCPResponse{Error: fmt.Sprintf("no server named %s is configured", name)}
	}
	var info protocol.MCPServerInfo
	for _, i := range s.serverInfos(cfg) {
		if i.Name == name {
			info = i
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s:\n  Status: %s\n  Command: %s\n", name, info.StatusLine, commandLine(srv))
	if info.Source == protocol.MCPSourceConfig {
		fmt.Fprintf(&b, "  Defined in: %s\n", s.configPathOrDefault())
	} else {
		fmt.Fprintf(&b, "  Defined in: %s\n", s.userServersPath)
	}
	if len(srv.Env) > 0 {
		var parts []string
		unset := s.envMissing(name, srv.Env)
		for _, n := range srv.Env {
			switch {
			case containsString(info.EnvMissing, n):
				parts = append(parts, n+" (no value)")
			case containsString(srv.EnvOptional, n) && containsString(unset, n):
				parts = append(parts, n+" (optional, no value)")
			case containsString(info.EnvStored, n):
				parts = append(parts, n+" (stored)")
			default:
				parts = append(parts, n+" (from the environment)")
			}
		}
		fmt.Fprintf(&b, "  Variables: %s\n", strings.Join(parts, ", "))
	}
	fmt.Fprintf(&b, "  Starts in: %s\n", map[bool]string{true: "the project", false: "a private folder of its own"}[srv.Cwd == ServerCwdWorkspace])
	if info.State == protocol.MCPStateFailed && info.StateDetail != "" {
		fmt.Fprintf(&b, "  Issue: %s\n", info.StateDetail)
	}
	if info.ToolsError != "" {
		fmt.Fprintf(&b, "  Issue: %s\n", info.ToolsError)
	}
	for _, w := range info.Warnings {
		fmt.Fprintf(&b, "  Warning: %s\n", w)
	}
	return protocol.MCPResponse{Ok: true, AgentMode: cfg.MCP.Enabled, Servers: []protocol.MCPServerInfo{info},
		Detail: strings.TrimRight(b.String(), "\n"), Notes: problems}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
