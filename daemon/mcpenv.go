package main

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
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
	if envNameLooksLikeKey(name) {
		return errKeyAsEnvName
	}
	if !mcpEnvNamePattern.MatchString(name) {
		return fmt.Errorf("%q is not a variable name (letters, digits and _, not starting with a digit)", truncateForClient(name, 40))
	}
	return mcp.ValidateEnvAllowList([]string{name})
}

// A KEY PASTED WHERE A VARIABLE NAME GOES.
//
// "sbp_" and forty hex digits is a legal variable name, so the name pattern
// lets a Supabase token through; it was then stored in mcp-servers.json, shown
// on the server's row as "needs sbp_…", and written into the agent's setup
// section, which goes to the model (FOUND 2026-10-15, live test). A real
// variable name is words joined by '_'; a key has a long run that mixes letters
// and digits, or one of the prefixes the big providers stamp on theirs.
var (
	keyPrefixPattern = regexp.MustCompile(`^(sbp_|gh[pousr]_|github_pat_|glpat_|sk_live_|sk_test_|rk_live_|pk_live_|AKIA[0-9A-Z]{12})`)
	keyRunPattern    = regexp.MustCompile(`[A-Za-z0-9]{16,}`)
	errKeyAsEnvName  = fmt.Errorf("that looks like a key, not a variable name. Give the variable's NAME " +
		"(for example SUPABASE_ACCESS_TOKEN), then put the key itself in with Set key")
)

// envNameLooksLikeKey reports whether name is more likely a secret than the
// name of a variable. Never repeat name in a message when it is.
func envNameLooksLikeKey(name string) bool {
	if keyPrefixPattern.MatchString(name) {
		return true
	}
	for _, run := range keyRunPattern.FindAllString(name, -1) {
		if strings.ContainsAny(run, "0123456789") && strings.IndexFunc(run, unicode.IsLetter) >= 0 {
			return true
		}
	}
	return false
}

// maskedEnvNames is names as they may be shown: one that looks like a key
// keeps its first four characters and loses the rest, so a server saved
// before envNameLooksLikeKey existed stops showing its key everywhere it is
// listed -- including the agent's setup section.
func maskedEnvNames(names []string) []string {
	if !slices.ContainsFunc(names, envNameLooksLikeKey) {
		return names
	}
	out := make([]string, len(names))
	for i, n := range names {
		if envNameLooksLikeKey(n) {
			n = n[:min(4, len(n))] + "…(a key, not a variable name: change this server)"
		}
		out[i] = n
	}
	return out
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
	// A key pasted from a web page often carries an invisible character, and
	// the server then refuses it with nothing on screen to explain why. Named
	// by code point and position, never by the text around it.
	for i, r := range []rune(value) {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\u2060', '\ufeff', '\u00a0':
			return fmt.Errorf("the value contains an invisible character (U+%04X) at position %d; paste it again "+
				"or type it", r, i+1)
		}
	}
	return nil
}

// setMCPEnv stores (or, for "", forgets) a value -- for one server, or for
// every server that reads it when server is "" -- and rebuilds the scrubber.
//
// With a key file (mcpsecrets.go) it is kept there; without one (a test, a home
// that cannot be found) it is held in memory for this daemon's life, which is
// all it ever was before the file. Forgetting also drops a value handed over
// at launch, so "forget" never leaves one behind that still works.
func (s *Server) setMCPEnv(server, name, value string) error {
	if s.mcpSecrets != nil {
		if err := s.mcpSecrets.setVar(server, name, value); err != nil {
			return err
		}
	}
	s.credMu.Lock()
	defer s.credMu.Unlock()
	switch {
	case value == "":
		delete(s.mcpEnv, name)
	case s.mcpSecrets == nil:
		if s.mcpEnv == nil {
			s.mcpEnv = map[string]string{}
		}
		s.mcpEnv[name] = value
	}
	s.rebuildCredScrubberLocked()
	return nil
}

// mcpEnvValueFor is how server finds a value for a variable it reads: its own
// stored value, then one stored for every server, then one handed over at
// launch. The daemon's own environment comes after all of these
// (mcp.ServerEnvWith).
func (s *Server) mcpEnvValueFor(server string) func(name string) (string, bool) {
	return func(name string) (string, bool) {
		if v, ok := s.mcpSecrets.lookup(server, name); ok {
			return v, true
		}
		s.credMu.RLock()
		defer s.credMu.RUnlock()
		v, ok := s.mcpEnv[name]
		return v, ok
	}
}

// mcpEnvValuesLocked is every value held, for the scrubber. Caller holds credMu.
func (s *Server) mcpEnvValuesLocked() []string {
	out := make([]string, 0, len(s.mcpEnv))
	for _, v := range s.mcpEnv {
		out = append(out, v)
	}
	return append(out, s.mcpSecrets.values()...)
}

// refreshMCPSecrets reads the key file again if another process changed it,
// and rebuilds the scrubber when it did: a key stored from the CLI or another
// window is in force -- and scrubbed -- from the next request.
func (s *Server) refreshMCPSecrets() {
	if s.mcpSecrets.refresh() {
		s.rebuildCredScrubber()
	}
}

// envMissing is the names in env that server would be given no value for.
func (s *Server) envMissing(server string, env []string) []string {
	lookup := s.mcpEnvValueFor(server)
	var out []string
	for _, name := range env {
		if _, ok := lookup(name); ok {
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

// mcpSetEnv answers MCPActionSetEnv. With a server name the value is that
// server's own; without one it is for every server that reads the variable.
func (s *Server) mcpSetEnv(req protocol.MCPRequest) protocol.MCPResponse {
	name := strings.TrimSpace(req.EnvName)
	if err := validMCPEnvName(name); err != nil {
		return protocol.MCPResponse{Error: err.Error()}
	}
	value := strings.TrimSpace(req.EnvValue)
	if err := validMCPEnvValue(value); err != nil {
		return protocol.MCPResponse{Error: name + ": " + err.Error()}
	}
	cfg, problems := s.agentConfig()
	server := strings.TrimSpace(req.Name)
	if server != "" {
		srv, ok := cfg.MCP.Servers[server]
		if !ok {
			return protocol.MCPResponse{Error: fmt.Sprintf("no server named %s is configured", server)}
		}
		if !slices.Contains(srv.Env, name) {
			return protocol.MCPResponse{Error: fmt.Sprintf("%s does not read %s; add the variable's name to it first "+
				"(Change, step 3)", server, name)}
		}
	}
	if err := s.setMCPEnv(server, name, value); err != nil {
		return protocol.MCPResponse{Error: "storing " + name + ": " + err.Error()}
	}

	var users []string
	for _, srvName := range sortedServerNames(cfg.MCP.Servers) {
		if server != "" && srvName != server {
			continue
		}
		if slices.Contains(cfg.MCP.Servers[srvName].Env, name) {
			users = append(users, srvName)
			// Its last start, without the value, says nothing about the next.
			s.mcpStates.forget(srvName)
		}
	}
	resp := protocol.MCPResponse{Ok: true, AgentMode: cfg.MCP.Enabled, Servers: s.serverInfos(cfg), Notes: problems}
	where := ""
	if s.mcpSecrets != nil && value != "" {
		where = " It is kept in " + s.mcpSecrets.path + ", readable only by you."
	}
	switch {
	case value == "" && server != "":
		resp.Detail = fmt.Sprintf("Forgot %s's own value of %s.", server, name)
	case value == "":
		resp.Detail = fmt.Sprintf("Forgot the value of %s.", name)
	case len(users) == 0:
		resp.Detail = fmt.Sprintf("%s is set, but no server reads it yet.%s", name, where)
	default:
		resp.Detail = fmt.Sprintf("%s is set for %s, from its next start.%s", name, strings.Join(users, ", "), where)
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

// validEnvValues checks the values a request gives for a server's variables:
// each for a name the server reads, each a usable value. Errors name the
// variable, never the value.
func validEnvValues(reads []string, values map[string]string) error {
	for _, name := range sortedStringKeys(values) {
		if !slices.Contains(reads, name) {
			return fmt.Errorf("a value was given for %s, which the server does not read; add it to the variables too", name)
		}
		if strings.TrimSpace(values[name]) == "" {
			return fmt.Errorf("the value given for %s is empty", name)
		}
		if err := validMCPEnvValue(strings.TrimSpace(values[name])); err != nil {
			return fmt.Errorf("%s: %v", name, err)
		}
	}
	return nil
}

// storeServerEnv keeps server's own values (trimmed) and drops its values for
// names it no longer reads. Without a key file they are held in memory.
func (s *Server) storeServerEnv(server string, reads []string, values map[string]string) error {
	trimmed := make(map[string]string, len(values))
	for k, v := range values {
		trimmed[k] = strings.TrimSpace(v)
	}
	if s.mcpSecrets == nil {
		for k, v := range trimmed {
			if err := s.setMCPEnv("", k, v); err != nil {
				return err
			}
		}
		return nil
	}
	if err := s.mcpSecrets.setServerEnv(server, reads, trimmed); err != nil {
		return err
	}
	s.rebuildCredScrubber()
	return nil
}

// sortedStringKeys is m's keys, sorted.
func sortedStringKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
