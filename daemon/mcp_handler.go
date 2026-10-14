package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mochiii/daemon/mcp"
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
	s.refreshMCPSecrets()

	// A server changed, paused or removed: its last start no longer describes it.
	switch req.Action {
	case protocol.MCPActionUpdate, protocol.MCPActionEnable, protocol.MCPActionDisable, protocol.MCPActionRemove:
		defer s.mcpStates.forget(strings.TrimSpace(req.Name))
	}

	switch req.Action {
	case "", protocol.MCPActionList:
		return s.mcpList(ctx)
	case protocol.MCPActionServers:
		cfg, problems := s.agentConfig()
		return protocol.MCPResponse{Ok: true, AgentMode: cfg.MCP.Enabled, Servers: s.serverInfos(cfg), Notes: problems}
	case protocol.MCPActionTools:
		return s.mcpTools(ctx)
	case protocol.MCPActionAdd:
		return s.mcpStore(ctx, req, false)
	case protocol.MCPActionUpdate:
		return s.mcpStore(ctx, req, true)
	case protocol.MCPActionRemove:
		return s.mcpRemove(req)
	case protocol.MCPActionEnable:
		return s.mcpSetDisabled(ctx, req, false)
	case protocol.MCPActionDisable:
		return s.mcpSetDisabled(ctx, req, true)
	case protocol.MCPActionSetEnv:
		return s.mcpSetEnv(req)
	}
	return protocol.MCPResponse{Error: fmt.Sprintf("unknown mcp action %q; the actions are %s", truncateForClient(req.Action, 32),
		strings.Join([]string{protocol.MCPActionList, protocol.MCPActionServers, protocol.MCPActionTools, protocol.MCPActionAdd,
			protocol.MCPActionUpdate, protocol.MCPActionRemove, protocol.MCPActionEnable, protocol.MCPActionDisable,
			protocol.MCPActionSetEnv}, ", "))}
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
	if err := writeToolReport(lctx, &report, &report, cfg, s.configPathOrDefault(), registry, connectErrs); err != nil {
		return protocol.MCPResponse{Error: "listing the tools: " + err.Error()}
	}
	resp.Report = report.String()
	return resp
}

// mcpTools reports this daemon's own tools and the policy on each, for a
// client that shows them (the VS Code Customize page). Unlike mcpList it
// STARTS NOTHING: the registry holds only the built-ins, and a third-party
// server's tools are left to mcpList, which has to start the server to ask.
func (s *Server) mcpTools(ctx context.Context) protocol.MCPResponse {
	cfg, problems := s.agentConfig()
	resp := protocol.MCPResponse{Ok: true, AgentMode: cfg.MCP.Enabled, Servers: s.serverInfos(cfg), Notes: problems}
	if !cfg.MCP.Enabled {
		resp.Notes = append(resp.Notes, "agent mode is off in "+s.configPathOrDefault()+", so none of these is offered to the model")
	}
	registry := s.builtinRegistry(cfg, s.logger, &proposalSink{}, "")
	defer func() { _ = registry.Close() }()
	tools, _ := registry.Advertised(ctx)
	for _, tool := range tools {
		_, policy, err := registry.Lookup(ctx, tool.QualifiedName())
		if err != nil {
			policy = mcp.PolicyDeny
		}
		resp.Tools = append(resp.Tools, protocol.MCPToolInfo{
			Name: tool.QualifiedName(), Lane: tool.Lane, Confined: confinedLabel(tool),
			Policy: string(policy), Description: firstLine(tool.Description),
		})
	}
	if dropped := registry.Dropped(); len(dropped) > 0 {
		resp.Notes = append(resp.Notes, fmt.Sprintf("%d tool(s) are not offered because mcp.budget.max_advertised_tools is %d: %s",
			len(dropped), cfg.MCP.Budget.resolvedMaxAdvertisedTools(), strings.Join(dropped, ", ")))
	}
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
		state := s.mcpStates.get(name)
		out = append(out, protocol.MCPServerInfo{
			Name: name, Command: commandLine(srv), Source: source, Disabled: srv.Disabled,
			Program: srv.Command, Args: srv.Args, Env: srv.Env, Cwd: srv.Cwd,
			EnvMissing: s.envMissing(name, srv.Env), EnvStored: s.mcpSecrets.storedNames(name, srv.Env),
			State: state.state, StateDetail: state.detail,
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
	return grantWarning(fmt.Sprintf("Adding %q", name), srv)
}

// changeWarning is unconfinedWarning for an update: changing what a server
// runs is agreeing to the new program, so it is said the same way.
func changeWarning(name string, srv MCPServerConfig) string {
	return grantWarning(fmt.Sprintf("Changing %q", name), srv)
}

// grantWarning is the text both warnings share, after their first words.
func grantWarning(lead string, srv MCPServerConfig) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s will make this daemon run:\n\n    %s\n\n", lead, commandLine(srv))
	b.WriteString("as an ordinary program with YOUR full privileges. Mochiii cannot confine an external MCP server: " +
		"it can read and write anything you can, and the edit pipeline's gates do not apply to it. " +
		"What protects you is that each of its tools asks before it runs, and every call is in the audit log.")
	if len(srv.Env) > 0 {
		fmt.Fprintf(&b, "\n\nIt will be given these variables from the daemon's environment: %s.", strings.Join(srv.Env, ", "))
	}
	b.WriteString("\n\n" + startsWhere(srv))
	b.WriteString("\n\nOnly add a server whose program you trust.")
	return b.String()
}

// mcpStore stores a server the user asked for: a new one (add), or new parts
// for one they added before (update, replace=true).
//
// TWO STEPS, AND THE FIRST STORES NOTHING. Without the acknowledgement the
// answer is the warning; the client shows it and the user types the server's
// name to go on. models.json's rule is that nobody arrives at an unconfined
// server by copying a snippet -- they arrive by typing the word -- and a
// command that added one in a single keystroke would have quietly repealed it.
//
// AN UPDATE ASKS THE SAME QUESTION, because it is the same grant. The name is
// all that stays: the program may be a different one, and what the user agreed
// to run before is not what they are agreeing to now. It keeps the server
// paused if it was, and it does NOT keep per-tool policies -- an `allow` written
// for the old program's tool would otherwise pass, unasked, to whatever the new
// program calls by that name.
//
// THEN IT IS TRIED. A stored server that does not start is discovered, without
// this, in the middle of the user's next question, as a tool that is not
// there. So the server is started once here, asked what it offers, and shut
// down, and the answer says which of "it works" and "it is saved but did not
// start" is true -- the same honesty /connect owes a key.
func (s *Server) mcpStore(ctx context.Context, req protocol.MCPRequest, replace bool) protocol.MCPResponse {
	nothing, done := "; nothing was added", "Added"
	if replace {
		nothing, done = "; nothing was changed", "Changed"
	}
	if strings.TrimSpace(s.userServersPath) == "" {
		return protocol.MCPResponse{Error: "this daemon has nowhere to store an added server"}
	}
	name := strings.TrimSpace(req.Name)
	srv := MCPServerConfig{
		Command: strings.TrimSpace(req.Command), Args: req.Args, Env: req.Env,
		AcknowledgedUnconfined: true, Cwd: strings.TrimSpace(req.Cwd),
	}
	if err := validateUserServer(name, srv); err != nil {
		return protocol.MCPResponse{Error: err.Error() + nothing}
	}
	if err := validEnvValues(srv.Env, req.EnvValues); err != nil {
		return protocol.MCPResponse{Error: err.Error() + nothing}
	}
	if _, inConfig := s.cfg.MCP.Servers[name]; inConfig {
		if replace {
			return protocol.MCPResponse{Error: fmt.Sprintf("%s is defined in %s, not added with this command. "+
				"Change its block there and restart the daemon", name, s.configPathOrDefault())}
		}
		return protocol.MCPResponse{Error: fmt.Sprintf("a server named %s is already defined in %s; choose another name",
			name, s.configPathOrDefault())}
	}
	user, _, err := loadUserMCPServers(s.userServersPath)
	if err != nil {
		return protocol.MCPResponse{Error: err.Error() + nothing}
	}
	old, exists := user[name]
	switch {
	case exists && !replace:
		return protocol.MCPResponse{Error: fmt.Sprintf("a server named %s is already added; remove it first to change it", name)}
	case !exists && replace:
		return protocol.MCPResponse{Error: fmt.Sprintf("no server named %s has been added; /mcp-server lists them", name)}
	}
	srv.Disabled = old.Disabled

	if !req.AcknowledgedUnconfined {
		warning := unconfinedWarning(name, srv)
		if replace {
			warning = changeWarning(name, srv)
		}
		if len(req.EnvValues) > 0 {
			warning += fmt.Sprintf("\n\nThe values you gave for %s are kept for this server only, in a file only you "+
				"can read, and are never shown to the model.", strings.Join(sortedStringKeys(req.EnvValues), ", "))
		}
		return protocol.MCPResponse{
			Ok: false, AgentMode: s.cfg.MCP.Enabled, NeedsAcknowledgement: true, Detail: warning,
		}
	}

	if user == nil {
		user = make(map[string]MCPServerConfig)
	}
	user[name] = srv
	if err := saveUserMCPServers(s.userServersPath, user); err != nil {
		return protocol.MCPResponse{Error: "saving the server: " + err.Error()}
	}
	// Its own values, only now that it is agreed to. An update also drops a
	// value for a variable the server no longer reads.
	if err := s.storeServerEnv(name, srv.Env, req.EnvValues); err != nil {
		return protocol.MCPResponse{Error: fmt.Sprintf("%s was saved, but its keys were not: %v. Set them with Set key", name, err)}
	}
	s.logger.Printf("mcp: server %s %s by the user (lane=third_party, unconfined): %s",
		name, strings.ToLower(done), commandLine(srv))

	resp := protocol.MCPResponse{Ok: true, AgentMode: s.cfg.MCP.Enabled}
	cfg, _ := s.agentConfig()
	resp.Servers = s.serverInfos(cfg)
	if srv.Disabled {
		// Trying it would say nothing: a turn does not start a paused server,
		// and neither does the registry this would build.
		resp.Detail = fmt.Sprintf("%s %s. It is disabled, so it was not started; enable it to use it.", done, name)
	} else {
		resp.Detail, resp.Notes = s.tryStoredServer(ctx, done, name, srv)
	}
	if len(old.Tools) > 0 {
		resp.Notes = append(resp.Notes, fmt.Sprintf("the per-tool policies %s had were for the program it ran before, "+
			"so they were dropped: each of its tools asks again", name))
	}
	if !s.cfg.MCP.Enabled {
		resp.Notes = append(resp.Notes, agentModeOffNote(s.configPathOrDefault()))
	}
	return resp
}

// agentModeOffNote says that a stored server will not run yet, and why.
func agentModeOffNote(configPath string) string {
	return fmt.Sprintf("agent mode is off in %s, so no tool runs at all yet. Start the daemon "+
		"with agent mode on (./run-tui.sh does) and this server's tools are offered.", configPath)
}

// mcpSetDisabled pauses (disabled=true) or resumes a server the user added. The
// server stays stored either way, with its acknowledgement: its program does not
// change, so resuming it is not a new grant and does not ask again.
//
// A RESUMED SERVER IS TRIED, for add's reason: the user is about to rely on it,
// and a server that no longer starts -- uninstalled, a version gone from the
// package registry -- is better found now than mid-question.
func (s *Server) mcpSetDisabled(ctx context.Context, req protocol.MCPRequest, disabled bool) protocol.MCPResponse {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return protocol.MCPResponse{Error: "say which server to " + req.Action}
	}
	user, _, err := loadUserMCPServers(s.userServersPath)
	if err != nil {
		return protocol.MCPResponse{Error: err.Error() + "; nothing was changed"}
	}
	srv, added := user[name]
	if !added {
		if _, inConfig := s.cfg.MCP.Servers[name]; inConfig {
			return protocol.MCPResponse{Error: fmt.Sprintf("%s is defined in %s, not added with this command. "+
				"Set \"disabled\": %v on its block there and restart the daemon.", name, s.configPathOrDefault(), disabled)}
		}
		return protocol.MCPResponse{Error: fmt.Sprintf("no server named %s has been added; /mcp-server lists them", name)}
	}

	resp := protocol.MCPResponse{Ok: true, AgentMode: s.cfg.MCP.Enabled}
	if srv.Disabled == disabled {
		cfg, _ := s.agentConfig()
		resp.Servers = s.serverInfos(cfg)
		resp.Detail = fmt.Sprintf("%s is already %s.", name, map[bool]string{true: "disabled", false: "enabled"}[disabled])
		return resp
	}
	srv.Disabled = disabled
	user[name] = srv
	if err := saveUserMCPServers(s.userServersPath, user); err != nil {
		return protocol.MCPResponse{Error: "saving the change: " + err.Error()}
	}
	cfg, _ := s.agentConfig()
	resp.Servers = s.serverInfos(cfg)
	if disabled {
		s.logger.Printf("mcp: server %s disabled by the user", name)
		resp.Detail = fmt.Sprintf("Disabled %s. It is not started again until you enable it; a turn already running "+
			"keeps the copy it started until it ends.", name)
		return resp
	}
	s.logger.Printf("mcp: server %s enabled by the user", name)
	resp.Detail, resp.Notes = s.tryStoredServer(ctx, "Enabled", name, srv)
	if !s.cfg.MCP.Enabled {
		resp.Notes = append(resp.Notes, agentModeOffNote(s.configPathOrDefault()))
	}
	return resp
}

// tryStoredServer starts a just-stored server once and reports what it offers.
// done is the first word of the answer: what was just done to it.
func (s *Server) tryStoredServer(ctx context.Context, done, name string, srv MCPServerConfig) (detail string, notes []string) {
	// Every place a value can come from, not only the environment: a key
	// stored with Set key or `mcp set-key` is the usual one now.
	for _, env := range s.envMissing(name, srv.Env) {
		notes = append(notes, fmt.Sprintf("Mochiii has no value for %s, so the server starts without it. "+
			"Set it with `mcp set-key %s %s` or Set key in VS Code.", env, name, env))
	}
	if !filepath.IsAbs(srv.Command) {
		if _, err := lookPath(srv.Command); err != nil {
			return fmt.Sprintf("%s %s, but %q was not found on this daemon's PATH, so it cannot start yet. "+
				"Install it, or remove the server and add it again with the program's absolute path.", done, name, srv.Command), notes
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
			return fmt.Sprintf("%s %s, but it did NOT start when tried: %v. It is saved; fix the command "+
				"(`/mcp-server remove %s`, then add it again) or see the daemon log.", done, name, err, name), notes
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
		return fmt.Sprintf("%s %s. It started, but its tools could not be listed: %v.", done, name,
			append(connectErrs, listErrs...)[0]), notes
	case total == 0:
		return fmt.Sprintf("%s %s. It started and offers no tools.", done, name), notes
	case len(leftOut) == 0:
		return fmt.Sprintf("%s %s. It started and offers %d tool(s): %s. They are available from your next question, "+
			"and each asks before it runs.", done, name, total, someNames(offered)), notes
	}

	limit := s.cfg.MCP.Budget.resolvedMaxAdvertisedTools()
	reach := "none of them will reach the model"
	if len(offered) > 0 {
		reach = fmt.Sprintf("only %d of them will reach the model (%s)", len(offered), someNames(offered))
	}
	return fmt.Sprintf("%s %s. It started and offers %d tool(s), but %s: this daemon shows the model at most %d tools "+
		"at a time, and %d of those places are taken by tools that come first. Left out: %s. "+
		"To offer them, raise mcp.budget.max_advertised_tools in %s and restart the daemon -- each tool on the menu "+
		"costs tokens on every request, which is why there is a limit.",
		done, name, total, reach, limit, len(tools)-len(offered), someNames(leftOut), s.configPathOrDefault()), notes
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
	// Its own keys go with it; values every server reads stay.
	if err := s.mcpSecrets.forgetServer(name); err != nil {
		s.logger.Printf("mcp: removing %s's stored keys: %v", name, err)
	}
	s.rebuildCredScrubber()
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
