package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"mochiii/protocol"
)

// A PROJECT'S OWN SERVERS: .mcp.json at the project root, in Claude Code's
// format, so a team can share the servers a repository is meant to be worked
// with -- which the user chose (2026-10-14) as Claude Code does it.
//
// A PROJECT MAY PROPOSE A SERVER; IT CAN NEVER GRANT ONE. Until this file, a
// workspace could not name a server at all, because cloning a repository must
// not be enough to have its program started (mcpuser.go). That stays true:
//
//   - A server from .mcp.json is PENDING until the user approves it -- the
//     same warning and the same typed name as any add -- and a pending server
//     is never started: not by a turn, not by a list, not by a check.
//   - The approval is kept in ~/.mochiii/mcp-approvals.json, beside the key
//     files, which no workspace can write and the agent is refused. A file
//     the repository ships cannot approve itself.
//   - It is BOUND TO A DIGEST of the exact definition and the project folder.
//     Change one argument -- by hand, by a pull, or by the agent's own edit --
//     and the server is pending again.
//   - A project cannot set a tool's policy: every one of its tools asks.
//   - A secret cannot travel in it: an env or header value is a ${VAR}
//     template, or plain text the scrubber finds nothing in; a key goes in
//     the user's key file with Set key.

const (
	projectMCPFileName   = ".mcp.json"
	maxProjectMCPBytes   = 256 << 10
	mcpApprovalsFileName = "mcp-approvals.json"
)

// projectServers reads root's .mcp.json. A missing file is no servers; a file
// that is a link, too large or unreadable is a problem, never a guess.
func projectServers(root string) (servers map[string]MCPServerConfig, path string, problems []string) {
	if strings.TrimSpace(root) == "" {
		return nil, "", nil
	}
	path = filepath.Join(root, projectMCPFileName)
	info, err := os.Lstat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			problems = append(problems, fmt.Sprintf("%s could not be read: %v", projectMCPFileName, err))
		}
		return nil, path, problems
	}
	if !info.Mode().IsRegular() {
		return nil, path, []string{projectMCPFileName + " is not a regular file (a link, perhaps); its servers were not read"}
	}
	if info.Size() > maxProjectMCPBytes {
		return nil, path, []string{fmt.Sprintf("%s is larger than %d KiB; its servers were not read", projectMCPFileName, maxProjectMCPBytes>>10)}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, path, []string{fmt.Sprintf("%s could not be read: %v", projectMCPFileName, err)}
	}
	var file struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, path, []string{projectMCPFileName + " is not valid JSON; its servers were not read"}
	}
	servers = map[string]MCPServerConfig{}
	for _, name := range sortedRawKeys(file.MCPServers) {
		srv, err := projectServerBlock(name, file.MCPServers[name])
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s's server %s is not usable: %v", projectMCPFileName, truncateForClient(name, 32), err))
			continue
		}
		srv.project = path
		servers[name] = srv
	}
	return servers, path, problems
}

func sortedRawKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// projectServerBlock is one .mcp.json entry as this daemon's block. Its
// values stay as written -- templates, or plain text with nothing secret in it.
func projectServerBlock(name string, raw json.RawMessage) (MCPServerConfig, error) {
	if err := validateUserServerName(name); err != nil {
		return MCPServerConfig{}, err
	}
	var entry clientServerEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return MCPServerConfig{}, fmt.Errorf("its entry has a field of the wrong kind")
	}
	if secretLooking(entry.Env) || secretLooking(entry.Headers) {
		return MCPServerConfig{}, fmt.Errorf("it holds what looks like a key in plain text; write it as ${NAME} and " +
			"give the value with Set key, so the key is not in the repository")
	}
	srv, _, _, _, err := blockFromClientEntry(entry)
	if err != nil {
		return MCPServerConfig{}, err
	}
	// Plain, non-secret values were split out as if they were keys; in a
	// project file they are part of the definition, kept as written.
	srv.Env, srv.EnvMap = nil, nil
	for _, k := range sortedStringKeys(entry.Env) {
		if v := entry.Env[k]; v == "${"+k+"}" || v == "" {
			srv.Env = append(srv.Env, k)
		} else {
			if srv.EnvMap == nil {
				srv.EnvMap = map[string]string{}
			}
			srv.EnvMap[k] = v
		}
	}
	if srv.remote() {
		srv.Headers = map[string]string{}
		for k, v := range entry.Headers {
			srv.Headers[k] = strings.TrimSpace(v)
		}
	}
	srv.AcknowledgedUnconfined, srv.Tools = false, nil
	if err := validateUserServer(name, srv); err != nil {
		return MCPServerConfig{}, err
	}
	return srv, nil
}

// secretLooking reports whether a plain value (not a ${VAR}) looks like a key.
func secretLooking(values map[string]string) bool {
	for k, v := range values {
		if strings.Contains(v, "${") || strings.TrimSpace(v) == "" {
			continue
		}
		// A value under a name that says it is one (API_KEY, GITHUB_TOKEN,
		// CLIENT_SECRET, Authorization), whatever it looks like.
		if secretFieldName(k) || strings.EqualFold(k, "authorization") || strings.HasSuffix(strings.ToLower(k), "_key") {
			return true
		}
		if _, found := scrub(v, false); len(found) > 0 {
			return true
		}
		// "Bearer <anything>" is a credential whatever it looks like.
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "bearer ") {
			return true
		}
	}
	return false
}

// projectDigest is the identity of one proposed server: the project folder,
// its name, and every part of its definition. An approval is for this, not for
// the name.
func projectDigest(root, name string, srv MCPServerConfig) string {
	shape := struct {
		V         int               `json:"v"`
		Workspace string            `json:"workspace"`
		Name      string            `json:"name"`
		Type      string            `json:"type"`
		Command   string            `json:"command"`
		Args      []string          `json:"args"`
		Env       []string          `json:"env"`
		EnvMap    map[string]string `json:"env_map"`
		URL       string            `json:"url"`
		Headers   map[string]string `json:"headers"`
		Cwd       string            `json:"cwd"`
	}{1, root, name, srv.Type, srv.Command, srv.Args, srv.Env, srv.EnvMap, srv.URL, srv.Headers, srv.Cwd}
	data, _ := json.Marshal(shape)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// THE APPROVALS FILE, in ~/.mochiii beside the key files.

type mcpApprovalsFile struct {
	Version  int                                     `json:"version"`
	Projects map[string]map[string]mcpApprovalRecord `json:"projects,omitempty"`
}

type mcpApprovalRecord struct {
	Digest   string    `json:"digest"`
	Decision string    `json:"decision"`
	At       time.Time `json:"at"`
}

// approvalsPath is beside the key file; "" when there is none (a test, or no home).
func (s *Server) approvalsPath() string {
	if s.mcpSecrets == nil {
		return ""
	}
	return filepath.Join(filepath.Dir(s.mcpSecrets.path), mcpApprovalsFileName)
}

func readApprovals(path string) mcpApprovalsFile {
	var f mcpApprovalsFile
	if path == "" {
		return f
	}
	info, err := os.Stat(path)
	if err != nil || (info.Mode().Perm()&0o022 != 0 && runtime.GOOS != "windows") {
		// Missing is none; a file others can write approves nothing.
		return f
	}
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &f)
	}
	return f
}

// decide records the user's decision on one definition.
func (s *Server) decide(root, name, digest, decision string) error {
	path := s.approvalsPath()
	if path == "" {
		return fmt.Errorf("this daemon has nowhere to keep an approval")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := lockFile(lock); err != nil {
		return err
	}
	defer func() { _ = unlockFile(lock) }()
	f := readApprovals(path)
	f.Version = 1
	if f.Projects == nil {
		f.Projects = map[string]map[string]mcpApprovalRecord{}
	}
	if f.Projects[root] == nil {
		f.Projects[root] = map[string]mcpApprovalRecord{}
	}
	f.Projects[root][name] = mcpApprovalRecord{Digest: digest, Decision: decision, At: time.Now().UTC()}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(path, append(data, '\n'))
}

// withProjectServers merges root's proposed servers into servers (which wins
// a name both use), each acknowledged only by a matching approval.
func (s *Server) withProjectServers(servers map[string]MCPServerConfig) (map[string]MCPServerConfig, []string) {
	proposed, _, problems := projectServers(s.workspace)
	if len(proposed) == 0 {
		return servers, problems
	}
	approvals := readApprovals(s.approvalsPath()).Projects[s.workspace]
	out := make(map[string]MCPServerConfig, len(servers)+len(proposed))
	for k, v := range servers {
		out[k] = v
	}
	for name, srv := range proposed {
		if _, taken := out[name]; taken {
			problems = append(problems, fmt.Sprintf("%s proposes a server named %s, but you already have one by that "+
				"name; the project's was left out", projectMCPFileName, name))
			continue
		}
		srv.digest = projectDigest(s.workspace, name, srv)
		srv.approval = protocol.MCPApprovalPending
		if rec, ok := approvals[name]; ok && rec.Digest == srv.digest {
			srv.approval = rec.Decision
		}
		srv.AcknowledgedUnconfined = srv.approval == protocol.MCPApprovalApproved
		out[name] = srv
	}
	return out, problems
}

// mcpApprove answers approve (two steps, like add) and reject.
func (s *Server) mcpApprove(req protocol.MCPRequest, decision string) protocol.MCPResponse {
	cfg, problems := s.agentConfig()
	name := strings.TrimSpace(req.Name)
	srv, ok := cfg.MCP.Servers[name]
	if !ok || srv.project == "" {
		return protocol.MCPResponse{Error: fmt.Sprintf("no server named %s is proposed by this project's %s", name, projectMCPFileName)}
	}
	if decision == protocol.MCPApprovalRejected {
		if err := s.decide(s.workspace, name, srv.digest, decision); err != nil {
			return protocol.MCPResponse{Error: "keeping the decision: " + err.Error()}
		}
		cfg, _ = s.agentConfig()
		return protocol.MCPResponse{Ok: true, AgentMode: cfg.MCP.Enabled, Servers: s.serverInfos(cfg), Notes: problems,
			Detail: fmt.Sprintf("Rejected %s. It is not started; if the project changes it, you are asked again.", name)}
	}
	if !req.AcknowledgedUnconfined {
		warning := grantWarning(fmt.Sprintf("Approving %q, which %s proposes,", name, srv.project), srv)
		if vars := serverTemplateVars(srv); len(vars) > 0 {
			warning += fmt.Sprintf("\n\nIts definition reads these values: %s.", strings.Join(vars, ", "))
		}
		warning += "\n\nThis approval is for this exact definition in this folder: if the project changes it, you are asked again."
		return protocol.MCPResponse{NeedsAcknowledgement: true, AgentMode: cfg.MCP.Enabled, Detail: warning, Digest: srv.digest, Notes: problems}
	}
	if req.Digest != srv.digest {
		return protocol.MCPResponse{Error: fmt.Sprintf("%s changed since its warning was shown; look at it again before approving", name)}
	}
	if err := s.decide(s.workspace, name, srv.digest, protocol.MCPApprovalApproved); err != nil {
		return protocol.MCPResponse{Error: "keeping the approval: " + err.Error()}
	}
	s.logger.Printf("mcp: project server %s approved by the user (%s)", name, srv.digest)
	cfg, _ = s.agentConfig()
	return protocol.MCPResponse{Ok: true, AgentMode: cfg.MCP.Enabled, Servers: s.serverInfos(cfg), Notes: problems,
		Detail: fmt.Sprintf("Approved %s. It starts with your next question, and each of its tools asks before it runs.", name)}
}
