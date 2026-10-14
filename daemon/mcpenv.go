package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// A SERVER'S KEY, GIVEN RATHER THAN INHERITED.
//
// An MCP server reads its key from a variable it is passed (perplexity's
// PERPLEXITY_API_KEY, supabase's SUPABASE_ACCESS_TOKEN). Until this file the
// only place such a value could come from was the environment the daemon was
// started in -- under VS Code, the environment VS Code itself was started in.
// Launched from a desktop icon there is no shell to export it from, so a server
// the user added and saw marked "Active" exited the moment it started, and the
// only trace was a line in the daemon log (FOUND 2026-10-14: both of a user's
// servers failing every turn with "PERPLEXITY_API_KEY environment variable is
// required").
//
// Now a client can hand the daemon the value: at launch, on stdin with the
// model key (launchcred.go's mcp_env), and while it runs, with
// MCPActionSetEnv. It is held in memory only -- the client keeps it in the
// editor's secret store -- and given to a server only when that server's own
// Env names the variable (mcp.ServerEnvWith). It is a credential like the
// daemon's own key: it joins the scrubber, so it is cut from anything the
// model is shown and refused in an outbound web call.

// maxMCPEnvValueBytes bounds one value: a token is a few hundred bytes.
const maxMCPEnvValueBytes = 8 << 10

var mcpEnvNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// validMCPEnvName reports why name cannot be given a value, or nil.
func validMCPEnvName(name string) error {
	if !mcpEnvNamePattern.MatchString(name) {
		return fmt.Errorf("%q is not a variable name (letters, digits and _, not starting with a digit)", truncateForClient(name, 40))
	}
	return mcp.ValidateEnvAllowList([]string{name})
}

// validMCPEnvValue reports why value cannot be stored, or nil. The error never
// repeats the value.
func validMCPEnvValue(value string) error {
	if len(value) > maxMCPEnvValueBytes {
		return fmt.Errorf("the value is longer than %d bytes", maxMCPEnvValueBytes)
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("the value contains a line break or another control character")
	}
	return nil
}

// setMCPEnv stores (or, for "", forgets) a value and rebuilds the scrubber.
func (s *Server) setMCPEnv(name, value string) {
	s.credMu.Lock()
	defer s.credMu.Unlock()
	if value == "" {
		delete(s.mcpEnv, name)
	} else {
		if s.mcpEnv == nil {
			s.mcpEnv = map[string]string{}
		}
		s.mcpEnv[name] = value
	}
	s.rebuildCredScrubberLocked()
}

// mcpEnvValue is the value given for name, if one was.
func (s *Server) mcpEnvValue(name string) (string, bool) {
	s.credMu.RLock()
	defer s.credMu.RUnlock()
	v, ok := s.mcpEnv[name]
	return v, ok
}

// mcpEnvValuesLocked is every value given, for the scrubber. Caller holds credMu.
func (s *Server) mcpEnvValuesLocked() []string {
	out := make([]string, 0, len(s.mcpEnv))
	for _, v := range s.mcpEnv {
		out = append(out, v)
	}
	return out
}

// envMissing is the names in env that no server would be given a value for.
func (s *Server) envMissing(env []string) []string {
	var out []string
	for _, name := range env {
		if _, ok := s.mcpEnvValue(name); ok {
			continue
		}
		// Set but empty is no key either.
		if v, ok := os.LookupEnv(name); ok && v != "" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// mcpSetEnv answers MCPActionSetEnv.
func (s *Server) mcpSetEnv(req protocol.MCPRequest) protocol.MCPResponse {
	name := strings.TrimSpace(req.EnvName)
	if err := validMCPEnvName(name); err != nil {
		return protocol.MCPResponse{Error: err.Error()}
	}
	value := strings.TrimSpace(req.EnvValue)
	if err := validMCPEnvValue(value); err != nil {
		return protocol.MCPResponse{Error: name + ": " + err.Error()}
	}
	s.setMCPEnv(name, value)

	cfg, problems := s.agentConfig()
	var users []string
	for _, server := range sortedServerNames(cfg.MCP.Servers) {
		for _, n := range cfg.MCP.Servers[server].Env {
			if n == name {
				users = append(users, server)
				// Its last start, without the value, says nothing about the next.
				s.mcpStates.forget(server)
			}
		}
	}
	resp := protocol.MCPResponse{Ok: true, AgentMode: cfg.MCP.Enabled, Servers: s.serverInfos(cfg), Notes: problems}
	switch {
	case value == "":
		resp.Detail = fmt.Sprintf("Forgot the value of %s.", name)
	case len(users) == 0:
		resp.Detail = fmt.Sprintf("%s is set, but no server reads it yet.", name)
	default:
		resp.Detail = fmt.Sprintf("%s is set for %s, from its next start.", name, strings.Join(users, ", "))
	}
	s.logger.Printf("mcp: a value for %s was %s by the user", name, map[bool]string{true: "forgotten", false: "given"}[value == ""])
	return resp
}

// MCP SERVER STATES: what happened the last time each server was started, so
// the list can say "failed: PERPLEXITY_API_KEY environment variable is
// required" instead of "Active" for a server that has never once run.

type mcpServerState struct {
	state  string // protocol.MCPStateRunning or MCPStateFailed
	detail string
}

type mcpStateBook struct {
	mu sync.Mutex
	// states is keyed by server name.
	states map[string]mcpServerState
	// lastLine is the last line each server wrote to stderr: the reason a
	// server gives when it exits ("Error: X is required") is what a person
	// needs, and the connect error only says the pipe closed.
	lastLine map[string]string
}

func (b *mcpStateBook) noteLine(name, line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lastLine == nil {
		b.lastLine = map[string]string{}
	}
	b.lastLine[name] = line
}

// started records a start: err nil is running.
func (b *mcpStateBook) started(name string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.states == nil {
		b.states = map[string]mcpServerState{}
	}
	if err == nil {
		b.states[name] = mcpServerState{state: protocol.MCPStateRunning}
		return
	}
	detail := b.lastLine[name]
	if detail == "" {
		detail = err.Error()
	}
	b.states[name] = mcpServerState{state: protocol.MCPStateFailed, detail: truncateForClient(detail, 240)}
}

// beforeStart forgets the last stderr line, so a reason is this start's own.
func (b *mcpStateBook) beforeStart(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.lastLine, name)
}

// forget drops what is known about a server: changed, paused or removed, its
// last start says nothing about the next one.
func (b *mcpStateBook) forget(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.states, name)
	delete(b.lastLine, name)
}

func (b *mcpStateBook) get(name string) mcpServerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.states[name]
}
