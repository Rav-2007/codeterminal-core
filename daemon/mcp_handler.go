package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mochiii/protocol"
)

// The daemon side of `/mcp-server`: say what tools THIS daemon offers, and take
// or drop a server the user adds.
//
// WHY THE DAEMON ANSWERS AND NOT A SECOND COPY OF ITSELF (FOUND 2026-10-11). The
// terminal client used to answer /mcp-server by running `mochiii-daemon mcp
// list` as a new process. That process loaded a config for itself -- the one
// beside the binary, else ./models.json -- which need not be the one the
// running daemon was started with. Under run-tui.sh it was not: the daemon ran
// models.agent.json with its tools on, the second copy read ./models.json, and
// the user was told "Agent mode is OFF". The owner's report was "MCP is not
// working in the CLI"; what was not working was the question.
//
// The only process that knows what this daemon offers is this daemon, so it is
// asked, over the socket it already serves.

// isMCPRequest recognises the request by its discriminator, like every other
// typed request on this socket.
func isMCPRequest(raw json.RawMessage) bool {
	fields, ok := requestFields(raw)
	if !ok {
		return false
	}
	return hasBoolKey(fields, "mcp")
}

// mcpListGrace is added to the configured connect timeout to bound a whole
// list: every server gets its handshake, and then there is this long to ask
// them all what they offer.
const mcpListGrace = 15 * time.Second

// handleMCP answers an MCPRequest.
func (s *Server) handleMCP(ctx context.Context, enc *json.Encoder, req protocol.MCPRequest) {
	resp := s.mcpResult(ctx, req)
	resp.ProtocolVersion = protocol.ProtocolVersion
	if err := enc.Encode(resp); err != nil {
		s.logger.Printf("mcp response encode error: %v", err)
	}
}

// mcpResult is the whole decision, apart from the encoding, so each branch is
// testable without a socket.
func (s *Server) mcpResult(ctx context.Context, req protocol.MCPRequest) protocol.MCPResponse {
	if s.cfg == nil {
		return protocol.MCPResponse{Error: "this daemon has no configuration loaded"}
	}
	s.mcpAdminMu.Lock()
	defer s.mcpAdminMu.Unlock()

	switch req.Action {
	case "", protocol.MCPActionList:
		return s.mcpList(ctx)
	case protocol.MCPActionAdd:
		return s.mcpAdd(ctx, req)
	case protocol.MCPActionRemove:
		return s.mcpRemove(req)
	}
	return protocol.MCPResponse{Error: fmt.Sprintf("unknown mcp action %q; the actions are %s, %s and %s",
		req.Action, protocol.MCPActionList, protocol.MCPActionAdd, protocol.MCPActionRemove)}
}

// mcpList reports the tools this daemon would offer the model on its next
// agent turn, in the words `mochiii-daemon mcp list` uses (writeToolReport).
//
// IT STARTS THE CONFIGURED SERVERS TO ASK THEM, exactly as an agent turn does
// and as `mcp list` always has: a tool list read from a config file would be a
// list of what was hoped for. They are shut down again before this returns.
func (s *Server) mcpList(ctx context.Context) protocol.MCPResponse {
	cfg, problems := s.agentConfig()
	resp := protocol.MCPResponse{
		Ok: true, AgentMode: cfg.MCP.Enabled,
		Servers: s.serverInfos(cfg), Notes: problems,
	}

	var report strings.Builder
	fmt.Fprintf(&report, "This daemon's config: %s\n", s.configPathOrDefault())
	if !cfg.MCP.Enabled {
		report.WriteString(agentModeOffReport(s.configPathOrDefault()))
		if n := len(resp.Servers); n > 0 {
			fmt.Fprintf(&report, "%d MCP server(s) are configured and will not run until agent mode is on.\n", n)
		}
		resp.Report = report.String()
		return resp
	}

	lctx, cancel := context.WithTimeout(ctx, cfg.MCP.Budget.resolvedConnectTimeout()+mcpListGrace)
	defer cancel()
	registry, connectErrs := s.buildRegistry(lctx, s.logger, &proposalSink{}, "")
	defer func() {
		if err := registry.Close(); err != nil {
			s.logger.Printf("shutting down MCP servers after a list: %v", err)
		}
	}()
	report.WriteString("\n")
	if err := writeToolReport(lctx, &report, &report, cfg, registry, connectErrs); err != nil {
		return protocol.MCPResponse{Error: "listing the tools: " + err.Error()}
	}
	resp.Report = report.String()
	return resp
}

// serverInfos lists the third-party servers configured, config file's first,
// each marked with where it came from.
func (s *Server) serverInfos(cfg *Config) []protocol.MCPServerInfo {
	var out []protocol.MCPServerInfo
	for _, name := range sortedServerNames(cfg.MCP.Servers) {
		srv := cfg.MCP.Servers[name]
		source := protocol.MCPSourceUser
		if _, fromConfig := s.cfg.MCP.Servers[name]; fromConfig {
			source = protocol.MCPSourceConfig
		}
		out = append(out, protocol.MCPServerInfo{
			Name: name, Command: commandLine(srv), Source: source, Disabled: srv.Disabled,
		})
	}
	return out
}

// commandLine is a server's program and arguments as one line, for a person.
func commandLine(srv MCPServerConfig) string {
	return strings.TrimSpace(srv.Command + " " + strings.Join(srv.Args, " "))
}

// unconfinedWarning is what a user is agreeing to when they add a server. It is
// the same fact models.json's acknowledged_unconfined gate states, said at the
// moment someone is about to accept it rather than in a validation error.
func unconfinedWarning(name string, srv MCPServerConfig) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Adding %q will make this daemon run:\n\n    %s\n\n", name, commandLine(srv))
	b.WriteString("as an ordinary program with YOUR full privileges. Mochiii cannot confine an external MCP server: " +
		"it can read and write anything you can, and the edit pipeline's gates do not apply to it. " +
		"What protects you is that each of its tools asks before it runs, and every call is in the audit log.")
	if len(srv.Env) > 0 {
		fmt.Fprintf(&b, "\n\nIt will be given these variables from the daemon's environment: %s.", strings.Join(srv.Env, ", "))
	}
	b.WriteString("\n\nOnly add a server whose program you trust.")
	return b.String()
}

// mcpAdd stores a server the user asked for.
//
// TWO STEPS, AND THE FIRST STORES NOTHING. Without the acknowledgement the
// answer is the warning; the client shows it and the user types the server's
// name to go on. models.json's rule is that nobody arrives at an unconfined
// server by copying a snippet -- they arrive by typing the word -- and a
// command that added one in a single keystroke would have quietly repealed it.
//
// THEN IT IS TRIED. A stored server that does not start is discovered, without
// this, in the middle of the user's next question, as a tool that is not
// there. So the server is started once here, asked what it offers, and shut
// down, and the answer says which of "it works" and "it is saved but did not
// start" is true -- the same honesty /connect owes a key.
func (s *Server) mcpAdd(ctx context.Context, req protocol.MCPRequest) protocol.MCPResponse {
	if strings.TrimSpace(s.userServersPath) == "" {
		return protocol.MCPResponse{Error: "this daemon has nowhere to store an added server"}
	}
	name := strings.TrimSpace(req.Name)
	srv := MCPServerConfig{
		Command: strings.TrimSpace(req.Command), Args: req.Args, Env: req.Env,
		AcknowledgedUnconfined: true,
	}
	if err := validateUserServer(name, srv); err != nil {
		return protocol.MCPResponse{Error: err.Error() + "; nothing was added"}
	}
	if _, inConfig := s.cfg.MCP.Servers[name]; inConfig {
		return protocol.MCPResponse{Error: fmt.Sprintf("a server named %s is already defined in %s; choose another name",
			name, s.configPathOrDefault())}
	}
	user, _, err := loadUserMCPServers(s.userServersPath)
	if err != nil {
		return protocol.MCPResponse{Error: err.Error() + "; nothing was added"}
	}
	if _, exists := user[name]; exists {
		return protocol.MCPResponse{Error: fmt.Sprintf("a server named %s is already added; remove it first to change it", name)}
	}

	if !req.AcknowledgedUnconfined {
		return protocol.MCPResponse{
			Ok: false, AgentMode: s.cfg.MCP.Enabled, NeedsAcknowledgement: true,
			Detail: unconfinedWarning(name, srv),
		}
	}

	if user == nil {
		user = make(map[string]MCPServerConfig)
	}
	user[name] = srv
	if err := saveUserMCPServers(s.userServersPath, user); err != nil {
		return protocol.MCPResponse{Error: "saving the server: " + err.Error()}
	}
	s.logger.Printf("mcp: server %s added by the user (lane=third_party, unconfined): %s", name, commandLine(srv))

	resp := protocol.MCPResponse{Ok: true, AgentMode: s.cfg.MCP.Enabled}
	cfg, _ := s.agentConfig()
	resp.Servers = s.serverInfos(cfg)
	resp.Detail, resp.Notes = s.tryAddedServer(ctx, name, srv)
	if !s.cfg.MCP.Enabled {
		resp.Notes = append(resp.Notes, fmt.Sprintf("agent mode is off in %s, so no tool runs at all yet. Start the daemon "+
			"with agent mode on (./run-tui.sh does) and this server's tools are offered.", s.configPathOrDefault()))
	}
	return resp
}

// tryAddedServer starts a just-added server once and reports what it offers.
func (s *Server) tryAddedServer(ctx context.Context, name string, srv MCPServerConfig) (detail string, notes []string) {
	for _, env := range srv.Env {
		if _, set := os.LookupEnv(env); !set {
			notes = append(notes, fmt.Sprintf("%s is not set in this daemon's environment, so the server starts without it. "+
				"Export it (or put it in .env) and restart the daemon.", env))
		}
	}
	if !filepath.IsAbs(srv.Command) {
		if _, err := lookPath(srv.Command); err != nil {
			return fmt.Sprintf("Added %s, but %q was not found on this daemon's PATH, so it cannot start yet. "+
				"Install it, or remove the server and add it again with the program's absolute path.", name, srv.Command), notes
		}
	}

	// THE MENU THE NEXT TURN WILL BUILD, built now: the same registry, the same
	// policy, the same cap. Asking the server directly would say what it
	// OFFERS, which is not the question -- seen in the real app (2026-10-11),
	// a server offering four tools was reported as "offers 4 tool(s)" while the
	// model was going to be shown one of them, because this daemon's own tools
	// had taken eleven of the twelve places on the menu.
	tctx, cancel := context.WithTimeout(ctx, s.cfg.MCP.Budget.resolvedConnectTimeout()+mcpListGrace)
	defer cancel()
	registry, connectErrs := s.buildRegistry(tctx, s.logger, &proposalSink{}, "")
	defer func() { _ = registry.Close() }()
	for _, err := range connectErrs {
		if strings.HasPrefix(err.Error(), "server "+name+":") {
			return fmt.Sprintf("Added %s, but it did NOT start when tried: %v. It is saved; fix the command "+
				"(`/mcp-server remove %s`, then add it again) or see the daemon log.", name, err, name), notes
		}
	}
	tools, listErrs := registry.Advertised(tctx)
	var offered, leftOut []string
	for _, tool := range tools {
		if tool.Server == name {
			offered = append(offered, tool.Name)
		}
	}
	for _, qualified := range registry.Dropped() {
		if tool, ok := strings.CutPrefix(qualified, name+"__"); ok {
			leftOut = append(leftOut, tool)
		}
	}
	total := len(offered) + len(leftOut)
	switch {
	case total == 0 && len(connectErrs)+len(listErrs) > 0:
		return fmt.Sprintf("Added %s. It started, but its tools could not be listed: %v.", name,
			append(connectErrs, listErrs...)[0]), notes
	case total == 0:
		return fmt.Sprintf("Added %s. It started and offers no tools.", name), notes
	case len(leftOut) == 0:
		return fmt.Sprintf("Added %s. It started and offers %d tool(s): %s. They are available from your next question, "+
			"and each asks before it runs.", name, total, someNames(offered)), notes
	}

	limit := s.cfg.MCP.Budget.resolvedMaxAdvertisedTools()
	reach := "none of them will reach the model"
	if len(offered) > 0 {
		reach = fmt.Sprintf("only %d of them will reach the model (%s)", len(offered), someNames(offered))
	}
	return fmt.Sprintf("Added %s. It started and offers %d tool(s), but %s: this daemon shows the model at most %d tools "+
		"at a time, and %d of those places are taken by tools that come first. Left out: %s. "+
		"To offer them, raise mcp.budget.max_advertised_tools in %s and restart the daemon -- each tool on the menu "+
		"costs tokens on every request, which is why there is a limit.",
		name, total, reach, limit, len(tools)-len(offered), someNames(leftOut), s.configPathOrDefault()), notes
}

// someNames lists names for a sentence, cutting a long list short.
func someNames(names []string) string {
	const show = 8
	if len(names) > show {
		return strings.Join(names[:show], ", ") + fmt.Sprintf(", and %d more", len(names)-show)
	}
	return strings.Join(names, ", ")
}

// mcpRemove drops a server the user added. One the config file defines is not
// this command's to remove, and the answer says where it lives instead.
func (s *Server) mcpRemove(req protocol.MCPRequest) protocol.MCPResponse {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return protocol.MCPResponse{Error: "say which server to remove"}
	}
	user, _, err := loadUserMCPServers(s.userServersPath)
	if err != nil {
		return protocol.MCPResponse{Error: err.Error() + "; nothing was removed"}
	}
	if _, added := user[name]; !added {
		if _, inConfig := s.cfg.MCP.Servers[name]; inConfig {
			return protocol.MCPResponse{Error: fmt.Sprintf("%s is defined in %s, not added with this command. "+
				"Delete its block there, or set \"disabled\": true on it, and restart the daemon.", name, s.configPathOrDefault())}
		}
		return protocol.MCPResponse{Error: fmt.Sprintf("no server named %s has been added; /mcp-server lists them", name)}
	}
	delete(user, name)
	if err := saveUserMCPServers(s.userServersPath, user); err != nil {
		return protocol.MCPResponse{Error: "saving the change: " + err.Error()}
	}
	s.logger.Printf("mcp: server %s removed by the user", name)
	cfg, _ := s.agentConfig()
	return protocol.MCPResponse{
		Ok: true, AgentMode: s.cfg.MCP.Enabled, Servers: s.serverInfos(cfg),
		Detail: fmt.Sprintf("Removed %s. It is not started again; a turn already running keeps the copy it started until it ends.", name),
	}
}

// absoluteOrAsGiven makes a path absolute for display, leaving it alone when
// that cannot be done.
func absoluteOrAsGiven(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// userServersPathOrNone is where a real daemon keeps the user's servers, or ""
// -- with a line in the log -- when the home directory cannot be found.
func userServersPathOrNone(logger *log.Logger) string {
	path, err := defaultUserMCPPath()
	if err != nil {
		logger.Printf("warning: MCP servers added with /mcp-server are unavailable: %v", err)
		return ""
	}
	return path
}

// lookPath is exec.LookPath, as a variable a test can replace.
var lookPath = exec.LookPath
