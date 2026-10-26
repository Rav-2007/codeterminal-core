package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"mochiii/daemon/mcp"
)

// THE USER'S OWN MCP SERVERS: what `/mcp-server add` and `mochiii-daemon mcp
// add` write, and what an agent turn reads beside the config file's own.
//
// WHY THIS EXISTS (OWNER, 2026-10-11). The only way to add a server was to
// hand-edit an `mcp.servers` block into models.json, a file most users never
// open and a format nothing documents at the prompt. A product that speaks MCP
// and cannot be handed a server is, to its user, a product that does not.
//
// WHY A FILE OF ITS OWN, IN ~/.mochiii, AND NOT models.json.
//
//   - models.json is the product's configuration. It sits in a checkout or
//     beside an installed binary, is replaced on upgrade, and is committed and
//     copied between machines. A server one person added on one machine does
//     not belong in any of those lives.
//
//   - IT MUST NEVER BE A FILE A WORKSPACE CAN SHIP. This file names programs
//     the daemon will run with the user's full privileges. If it lived in the
//     repository being worked on, cloning or unzipping a project would be
//     enough to have its program started -- the hijack /mcp-server already
//     had once, by another route (CONFIRMED by execution 2026-08-08). So it is
//     per-user, beside the stored key, in the one directory the agent itself
//     is refused: outside reads (outsideread.go) and edits (editapply's
//     ProtectedDirNames) both turn `.mochiii` away, and a sandboxed command
//     can neither write outside its working copy nor open the daemon's socket.
//     A project's .mcp.json may PROPOSE a server, and it runs only once the
//     user approves that exact definition here (mcpproject.go).
//
// WHY IT IS READ AT EVERY AGENT TURN, NOT AT STARTUP. So that adding a server
// takes effect at the next question. The file is a few hundred bytes; reading
// it costs nothing next to the model call the turn is about to make, and the
// alternative -- "restart the daemon" -- is exactly the step that makes a
// feature feel broken.

// userMCPFileName is the file's name inside ~/.mochiii.
const userMCPFileName = "mcp-servers.json"

// defaultUserMCPPath is where the file lives for a real daemon: beside the
// stored credential. A Server only reads it when main (or `mcp`) hands it this
// path -- see Server.userServersPath -- so a Server built in a test never
// reaches into the developer's own home and starts their servers.
func defaultUserMCPPath() (string, error) {
	cred, err := credentialsPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(cred), userMCPFileName), nil
}

// userMCPFile is the on-disk form: the same server blocks models.json takes,
// under the same key, so the two are read by one validator and a block can be
// moved between them by hand.
type userMCPFile struct {
	Servers map[string]MCPServerConfig `json:"servers"`
}

// loadUserMCPServers reads the user's servers. A missing file is not an error:
// nobody having added a server is the ordinary state.
//
// problems are per-entry and do not stop the rest from loading -- one server
// with a bad block should cost that server, not the others. err is the file
// itself being unusable, and then nothing is returned: a file that cannot be
// trusted to say what it says is not half-used.
//
// A FILE OTHERS CAN WRITE IS REFUSED OUTRIGHT, not warned about. For the
// credential a wide mode means the key may have been read, which is bad and
// already happened. Here it means somebody else can choose a program for this
// user's daemon to run, which has not happened yet and must not.
func loadUserMCPServers(path string) (servers map[string]MCPServerConfig, problems []string, err error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return nil, nil, fmt.Errorf("%s is mode %#o: writable by more than its owner, so the programs it names "+
			"cannot be trusted to be yours. Check it, then `chmod 600` it", path, info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var file userMCPFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, nil, fmt.Errorf("%s is not readable as a list of MCP servers (%w)", path, err)
	}

	servers = make(map[string]MCPServerConfig, len(file.Servers))
	for _, name := range sortedServerNames(file.Servers) {
		srv := file.Servers[name]
		if err := validateUserServer(name, srv); err != nil {
			problems = append(problems, fmt.Sprintf("server %s in %s is not usable: %v", name, path, err))
			continue
		}
		servers[name] = srv
	}
	return servers, problems, nil
}

// saveUserMCPServers writes the user's servers, 0600 and atomically. An empty
// set removes the file rather than leaving an empty one behind.
func saveUserMCPServers(path string, servers map[string]MCPServerConfig) error {
	if len(servers) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(userMCPFile{Servers: servers}, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(path, append(data, '\n'))
}

// validateUserServer is every rule a server block must meet to be started: the
// ones models.json's blocks meet (validateServerBlock, validateToolPolicies),
// the launcher's own (a name that cannot shadow the built-ins, an environment
// list that grants none of this product's credentials), and one more for a
// block that arrives as a request rather than as a file somebody edited.
func validateUserServer(name string, srv MCPServerConfig) error {
	if err := validateUserServerName(name); err != nil {
		return err
	}
	// BEFORE validateServerBlock, whose own message for this quotes the whole
	// entry. That is right for a config file, where the value is already on
	// disk in front of the person reading the error; here the entry came from
	// a command line, and quoting it would print the secret into a transcript
	// in the act of refusing it. Only the NAME is repeated.
	for _, env := range srv.Env {
		if envName, _, hasValue := strings.Cut(env, "="); hasValue {
			return fmt.Errorf("the env entry for %q carries a value; this list takes variable NAMES only, passed "+
				"through from the daemon's environment, so that a secret is never stored or shown", envName)
		}
		if envNameLooksLikeKey(env) {
			return errKeyAsEnvName
		}
		// The rule set-key already applies to a name (validMCPEnvName), and the
		// marketplace catalog's: a name a value could never be given for is
		// not one to store. It used to be accepted here and only refused
		// later, by the key it could not be given (2026-10-26).
		if !mcpEnvNamePattern.MatchString(env) {
			return fmt.Errorf("the env entry %q is not a variable name: letters, digits and _, not starting "+
				"with a digit, at most 128 characters", truncateForClient(env, 40))
		}
	}
	if err := validateServerBlock(srv); err != nil {
		return err
	}
	if !srv.remote() {
		if err := validateUserCommand(srv.Command); err != nil {
			return err
		}
	}
	if err := mcp.ValidateEnvAllowList(srv.Env); err != nil {
		return err
	}
	return validateToolPolicies("server "+name, srv.Tools)
}

// maxUserServerName bounds a name. It is drawn on every approval prompt for the
// server's tools, where a long one pushes the question off the line.
const maxUserServerName = 32

// validateUserServerName accepts a short, plain name: lower-case letters,
// digits, '-' and '_', starting with a letter or digit.
//
// STRICTER THAN mcp.ValidateServerName, on purpose. That one refuses what would
// break the registry; this one also refuses what would mislead a person. The
// name is the user's word for the server and is printed, by this daemon, on the
// prompt that asks whether its tool may run -- so it must not be able to carry
// a space-padded sentence, a control sequence, or a look-alike of "builtin".
func validateUserServerName(name string) error {
	if err := mcp.ValidateServerName(name); err != nil {
		return err
	}
	if len(name) > maxUserServerName {
		return fmt.Errorf("server name is %d characters; the most is %d", len(name), maxUserServerName)
	}
	for i, r := range name {
		letterOrDigit := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if letterOrDigit || (i > 0 && (r == '-' || r == '_')) {
			continue
		}
		return fmt.Errorf("server name %q may hold only lower-case letters, digits, '-' and '_', and must start "+
			"with a letter or digit", name)
	}
	return nil
}

// validateUserCommand accepts a bare program name, found through PATH when the
// server starts, or an absolute path -- and nothing in between.
//
// A RELATIVE PATH IS REFUSED. "./server" or "bin/server" resolves against the
// daemon's working directory, which is whatever directory it happened to be
// started from -- under run-tui.sh, the repository being worked on. So the same
// stored line would run one program today and a different one, chosen by
// whoever wrote the next repository, tomorrow. A name or an absolute path means
// the same program wherever the daemon runs.
func validateUserCommand(command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return fmt.Errorf("has no command to run")
	}
	if strings.ContainsAny(command, "\x00\n\r") {
		return fmt.Errorf("the command holds a character no program name has")
	}
	if filepath.IsAbs(command) {
		return nil
	}
	if strings.ContainsAny(command, `/\`) {
		return fmt.Errorf("the command %q is a relative path, which would mean a different program depending on "+
			"where the daemon was started. Give the program's name (found through PATH) or its absolute path", command)
	}
	return nil
}

// agentConfig is the config an agent turn runs under: the daemon's own, plus
// the servers the user added. The config file wins a name both define -- it is
// the more deliberate of the two, and add refuses to create the clash at all.
//
// problems are what went wrong reading the user's file, in sentences a user
// can be shown; the config returned is always usable, with whatever could not
// be read left out. A broken file must never fail a turn: the user would lose
// their built-in tools to a typo in a list of extras.
func (s *Server) agentConfig() (cfg *Config, problems []string) {
	s.refreshMCPSecrets()
	if p := s.mcpSecrets.problem(); p != "" {
		problems = append(problems, p)
	}
	if s.cfg == nil {
		return s.cfg, problems
	}
	// The project's proposed servers (mcpproject.go) join after the user's
	// and the config file's, which win a name all of them use.
	defer func() {
		if cfg == nil {
			return
		}
		merged, more := s.withProjectServers(cfg.MCP.Servers)
		problems = append(problems, more...)
		if len(merged) != len(cfg.MCP.Servers) {
			out := *cfg
			out.MCP.Servers = merged
			cfg = &out
		}
	}()
	if strings.TrimSpace(s.userServersPath) == "" {
		return s.cfg, problems
	}
	user, userProblems, err := loadUserMCPServers(s.userServersPath)
	problems = append(problems, userProblems...)
	if err != nil {
		return s.cfg, append(problems, "the MCP servers you added were not loaded: "+err.Error())
	}
	if len(user) == 0 {
		return s.cfg, problems
	}
	out := *s.cfg
	out.MCP.Servers = make(map[string]MCPServerConfig, len(s.cfg.MCP.Servers)+len(user))
	for name, srv := range user {
		out.MCP.Servers[name] = srv
	}
	for name, srv := range s.cfg.MCP.Servers {
		if _, both := user[name]; both {
			problems = append(problems, fmt.Sprintf("server %s is defined both in %s and among the ones you added; "+
				"the config file's is used", name, s.configPathOrDefault()))
		}
		out.MCP.Servers[name] = srv
	}
	return &out, problems
}

// configPathOrDefault names the config file for a message.
func (s *Server) configPathOrDefault() string {
	if strings.TrimSpace(s.configPath) != "" {
		return s.configPath
	}
	return configFileName
}
