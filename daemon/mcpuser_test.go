package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// userServersFile is a path for the user's servers inside a temp directory, as
// main gives a real daemon one inside ~/.mochiii.
func userServersFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "home", ".mochiii", userMCPFileName)
}

func plainUserServer(command string) MCPServerConfig {
	return MCPServerConfig{Command: command, AcknowledgedUnconfined: true}
}

// absProgram is an absolute path to a program, in the form this platform calls
// absolute -- "/usr/local/bin/x" is a RELATIVE path on Windows, and a relative
// command is exactly what an added server may not have.
func absProgram(name string) string {
	if runtime.GOOS == "windows" {
		return `C:\tools\` + name + ".exe"
	}
	return "/usr/local/bin/" + name
}

// What is saved is what is read back, the file is private, and an empty set
// leaves no file behind to be mistaken for a configured one.
func TestTheUsersServersRoundTripInAPrivateFile(t *testing.T) {
	path := userServersFile(t)
	want := map[string]MCPServerConfig{
		"github": {Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github"}, Env: []string{"GITHUB_TOKEN"}, AcknowledgedUnconfined: true},
		"local":  plainUserServer(absProgram("my-server")),
	}
	if err := saveUserMCPServers(path, want); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("the file is mode %#o, want 0600: it names programs this daemon runs", mode)
		}
		if dir, _ := os.Stat(filepath.Dir(path)); dir.Mode().Perm() != 0o700 {
			t.Errorf("its directory is mode %#o, want 0700", dir.Mode().Perm())
		}
	}
	got, problems, err := loadUserMCPServers(path)
	if err != nil || len(problems) != 0 {
		t.Fatalf("load: %v, problems %v", err, problems)
	}
	if len(got) != 2 || got["github"].Command != "npx" || len(got["github"].Args) != 2 ||
		got["github"].Env[0] != "GITHUB_TOKEN" || !got["github"].AcknowledgedUnconfined || got["local"].Command != absProgram("my-server") {
		t.Errorf("read back %+v", got)
	}

	if err := saveUserMCPServers(path, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("saving no servers left a file behind (%v)", err)
	}
	// And a file that was never there is the ordinary state, not an error.
	if got, problems, err := loadUserMCPServers(path); err != nil || got != nil || problems != nil {
		t.Errorf("a missing file loaded as %v, %v, %v", got, problems, err)
	}
	if got, _, err := loadUserMCPServers(""); err != nil || got != nil {
		t.Errorf("no path loaded as %v, %v", got, err)
	}
}

// A FILE SOMEBODY ELSE CAN WRITE NAMES PROGRAMS SOMEBODY ELSE CHOSE. It is not
// used at all -- and the daemon's own tools are untouched by that.
//
// Neuter check: drop the `Perm()&0o022` test in loadUserMCPServers.
func TestAFileOthersCanWriteIsNotTrusted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go's file mode says nothing about a Windows ACL")
	}
	path := userServersFile(t)
	if err := saveUserMCPServers(path, map[string]MCPServerConfig{"planted": plainUserServer(absProgram("evil"))}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0o666, 0o660, 0o602} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		got, _, err := loadUserMCPServers(path)
		if err == nil || got != nil {
			t.Errorf("mode %#o: the file was loaded (%d servers)", mode, len(got))
		}
		if err != nil && !strings.Contains(err.Error(), "writable by more than its owner") {
			t.Errorf("mode %#o: the refusal does not say why: %v", mode, err)
		}

		s := &Server{cfg: &Config{MCP: MCPConfig{Enabled: true}}, serverExtras: serverExtras{userServersPath: path}, logger: log.New(os.Stderr, "test: ", 0)}
		cfg, problems := s.agentConfig()
		if len(cfg.MCP.Servers) != 0 {
			t.Errorf("mode %#o: a turn would have started %v", mode, sortedServerNames(cfg.MCP.Servers))
		}
		if len(problems) != 1 || !strings.Contains(problems[0], "not loaded") {
			t.Errorf("mode %#o: the turn is not told the servers were left out: %v", mode, problems)
		}
	}
}

// A file that does not parse is not half-used, and one bad entry costs that
// entry alone.
func TestABrokenFileOrEntryNeverTakesTheRestDown(t *testing.T) {
	path := userServersFile(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"servers": {"a": `), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _, err := loadUserMCPServers(path); err == nil || got != nil {
		t.Errorf("a truncated file loaded as %v", got)
	}

	entries := `{"servers": {
		"good":      {"command": "npx", "acknowledged_unconfined": true},
		"relative":  {"command": "./server", "acknowledged_unconfined": true},
		"nocommand": {"acknowledged_unconfined": true},
		"secret":    {"command": "npx", "env": ["TOKEN=abc"], "acknowledged_unconfined": true},
		"Bad Name":  {"command": "npx", "acknowledged_unconfined": true},
		"policy":    {"command": "npx", "tools": {"x": "allowe"}, "acknowledged_unconfined": true}
	}}`
	if err := os.WriteFile(path, []byte(entries), 0o600); err != nil {
		t.Fatal(err)
	}
	got, problems, err := loadUserMCPServers(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["good"].Command != "npx" {
		t.Errorf("loaded %v, want only the usable entry", sortedServerNames(got))
	}
	if len(problems) != 5 {
		t.Errorf("%d problems reported for 5 unusable entries: %v", len(problems), problems)
	}
}

// The names and commands a user may give.
func TestWhatAnAddedServerMayBeCalledAndRun(t *testing.T) {
	for _, name := range []string{"github", "fs", "my-server", "db_2", "9lives"} {
		if err := validateUserServerName(name); err != nil {
			t.Errorf("name %q refused: %v", name, err)
		}
	}
	for _, name := range []string{"", " ", "builtin", "a__b", "GitHub", "my server", "-dash", "_under", "tab\there",
		"esc\x1b[2J", "bui1tin\u200b", strings.Repeat("a", maxUserServerName+1)} {
		if err := validateUserServerName(name); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}

	for _, command := range []string{"npx", "uvx", "my-server", absProgram("server")} {
		if err := validateUserCommand(command); err != nil {
			t.Errorf("command %q refused: %v", command, err)
		}
	}
	// A RELATIVE PATH means a different program depending on where the daemon
	// was started -- under run-tui.sh, the repository being worked on.
	for _, command := range []string{"", "./server", "bin/server", "../server", "a\nb"} {
		if err := validateUserCommand(command); err == nil {
			t.Errorf("command %q accepted", command)
		}
	}
	// This product's own credentials are not grantable to a server, whoever asks.
	if err := validateUserServer("x", MCPServerConfig{Command: "npx", Env: []string{"MOCHIII_API_KEY"}}); err == nil {
		t.Error("a server was allowed MOCHIII_API_KEY")
	}
}

// AN AGENT TURN RUNS UNDER BOTH: the config file's servers and the user's, the
// config file winning a name both define -- and the daemon's own config is not
// the thing that gets edited.
func TestAgentConfigAddsTheUsersServersToTheConfigFiles(t *testing.T) {
	path := userServersFile(t)
	if err := saveUserMCPServers(path, map[string]MCPServerConfig{
		"mine":   plainUserServer("npx"),
		"shared": plainUserServer(absProgram("users-choice")),
	}); err != nil {
		t.Fatal(err)
	}
	base := &Config{MCP: MCPConfig{Enabled: true, Servers: map[string]MCPServerConfig{
		"shared": plainUserServer(absProgram("config-choice")),
		"theirs": plainUserServer("uvx"),
	}}}
	s := &Server{cfg: base, serverExtras: serverExtras{userServersPath: path, configPath: "/etc/models.json"}, logger: log.New(os.Stderr, "test: ", 0)}

	cfg, problems := s.agentConfig()
	if got := strings.Join(sortedServerNames(cfg.MCP.Servers), " "); got != "mine shared theirs" {
		t.Fatalf("servers = %q, want mine shared theirs", got)
	}
	if cfg.MCP.Servers["shared"].Command != absProgram("config-choice") {
		t.Errorf("the user's entry beat the config file's: %q", cfg.MCP.Servers["shared"].Command)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "shared") || !strings.Contains(problems[0], "/etc/models.json") {
		t.Errorf("the clash is not reported with the file that won: %v", problems)
	}
	if len(base.MCP.Servers) != 2 {
		t.Errorf("the daemon's own config now holds %d servers; it was edited in place", len(base.MCP.Servers))
	}
	if !cfg.MCP.Enabled {
		t.Error("the rest of the config was not carried over")
	}

	// A Server that was given no path reads nothing -- which is every Server a
	// test builds, so no test starts the developer's own servers.
	bare := &Server{cfg: base}
	if got, _ := bare.agentConfig(); got != base {
		t.Error("a Server with no user file did not return its own config")
	}
	// And the prompt rule about third-party output follows the user's servers.
	onlyMine := &Server{cfg: &Config{MCP: MCPConfig{Enabled: true}}, serverExtras: serverExtras{userServersPath: path}}
	if !onlyMine.hasThirdPartyServer() {
		t.Error("a server the user added is not counted as a third-party server")
	}
	if (&Server{cfg: &Config{}}).hasThirdPartyServer() {
		t.Error("a daemon with no servers reports one")
	}
}
