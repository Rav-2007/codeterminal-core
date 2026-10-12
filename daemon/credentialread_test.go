package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"mochiii/protocol"
)

// CREDENTIAL LOCATIONS ARE REFUSED EVEN WITH A YES, by every tool that reads.
//
// read_file and list_directory read anywhere on the machine after a person
// approves the exact path (outsideread.go). Some places must stay closed even
// then, because a yes given at speed is exactly what an injected "read
// ~/.mochiii/credentials.json" is fishing for -- and those places hold the
// provider key THIS daemon runs with. Every test here drives the agent loop,
// the door the model uses, with an approver that says yes to everything: a
// row passes only if the read is refused before anyone is asked, and nothing
// from the file reaches the model.
//
// HOME is a temporary folder holding a fake key; nothing real is read.

const credTestKey = "FAKE-KEY-mochiii-0451-not-a-real-key"

type credentialFixture struct {
	home, xdgState string
	keyFD, stateFD uintptr // open on the key file and on the relocated state
}

// credentialHome points HOME and XDG_STATE_HOME at temporary folders laid out
// the way a real machine is, each secret file carrying a marker.
func credentialHome(t *testing.T) credentialFixture {
	t.Helper()
	// NOT t.TempDir: it names the folder after the test, and a test named for
	// credentials gets a folder whose name the secret-name rule refuses -- so
	// every row passed whatever the deny list said (found writing this file).
	home, xdg := neutralTempDir(t, "home"), neutralTempDir(t, "state")
	t.Setenv("HOME", home)
	// The daemon's StateDir() honours XDG_STATE_HOME (memory.go): conversation
	// memory, saved chats and task ledgers live under $XDG_STATE_HOME/mochiii.
	t.Setenv("XDG_STATE_HOME", xdg)
	writeFiles(t, home, map[string]string{
		".mochiii/credentials.json":                              `{"api_key":"` + credTestKey + `"}`,
		".local/state/mochiii/memory.db":                         "FAKE-STATE-MEMORY",
		".config/Code/User/globalStorage/state.vscdb":            "FAKE-VSCODE-SECRETSTORAGE",
		".config/Code - Insiders/User/globalStorage/state.vscdb": "FAKE-INSIDERS-SECRETSTORAGE",
		".config/VSCodium/User/globalStorage/state.vscdb":        "FAKE-VSCODIUM-SECRETSTORAGE",
		".local/share/keyrings/login.keyring":                    "FAKE-KEYRING",
	})
	writeFiles(t, xdg, map[string]string{"mochiii/memory.db": "FAKE-XDG-STATE-MEMORY"})

	open := func(path string) uintptr {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f.Fd()
	}
	return credentialFixture{
		home: home, xdgState: xdg,
		keyFD:   open(filepath.Join(home, ".mochiii", "credentials.json")),
		stateFD: open(filepath.Join(xdg, "mochiii", "memory.db")),
	}
}

// neutralTempDir is a fresh, symlink-free folder whose path no deny rule can
// match by itself.
func neutralTempDir(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mochiii-"+name+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// toolResults is what the tools returned, as the model was sent it: the
// content of every "tool" message after the first request. Only results are
// searched for markers -- the model's own call, which names the path, is in
// the same request.
func toolResults(bodies *[][]byte) string {
	var b strings.Builder
	for i, body := range *bodies {
		if i == 0 {
			continue
		}
		var req struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(body, &req) != nil {
			continue
		}
		for _, m := range req.Messages {
			if m.Role != "tool" {
				continue
			}
			var s string
			if json.Unmarshal(m.Content, &s) == nil {
				b.WriteString(s)
			} else {
				b.Write(m.Content)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// readWithYes runs one turn in which the model calls tool on path and the
// user approves everything. It returns how many times the user was asked and
// what the tool returned.
func readWithYes(t *testing.T, s *Server, tool, path string) (asked int, result string) {
	t.Helper()
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", tool, `{"path":`+quote(path)+`}`),
		textSSE("done"),
	)
	s.apiBase = base
	appr := &capturingApprover{decision: protocol.ApprovalApprove}
	if _, _, err := runLoopWith(t, s, appr); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	return len(appr.prompts()), toolResults(bodies)
}

// assertRefused is the verdict for one read: refused without asking, by the
// credential gate, with nothing of the file in the result.
func assertRefused(t *testing.T, asked int, result, marker string) {
	t.Helper()
	if asked != 0 {
		t.Errorf("the user was asked %d time(s); a credential location must be refused outright", asked)
	}
	if marker != "" && strings.Contains(result, marker) {
		t.Errorf("the file's content reached the model (%q found)", marker)
	}
	if !strings.Contains(result, "never read") {
		t.Errorf("not refused by the credential gate; the tool returned: %.300q", result)
		return
	}
	// The model is told what the place is, and not to go round it.
	if !strings.Contains(result, "is a credential location") || !strings.Contains(result, "Do not try another route") {
		t.Errorf("the refusal does not name a credential location and forbid another route: %.300q", result)
	}
}

func TestKeyStoresAreRefusedEvenWithAYes(t *testing.T) {
	// The rows below are Linux credential locations (~/.config, ~/.local/state,
	// ~/.local/share/keyrings) and Linux /proc and /dev/fd semantics, read
	// through a fixture that places them under HOME -- which only governs ~
	// expansion on Linux and macOS. On Windows these are absolute paths the
	// workspace-relative guard refuses before the credential gate is reached, so
	// the vectors still stay closed; they are simply closed by a different door,
	// and the OS credential stores this test stands in for live elsewhere.
	if runtime.GOOS != "linux" {
		t.Skip("Linux-specific credential locations and /proc semantics; refused by the workspace-relative guard on other platforms")
	}
	fx := credentialHome(t)
	pid := os.Getpid()
	rows := []struct{ name, tool, path, marker string }{
		// Mochiii's own key and state.
		{"~/.mochiii/credentials.json", "read_file", "~/.mochiii/credentials.json", credTestKey},
		{"~/.mochiii listed", "list_directory", "~/.mochiii", "credentials.json"},
		{"~/.local/state/mochiii/memory.db", "read_file", "~/.local/state/mochiii/memory.db", "FAKE-STATE-MEMORY"},
		{"$XDG_STATE_HOME/mochiii/memory.db", "read_file", filepath.Join(fx.xdgState, "mochiii", "memory.db"), "FAKE-XDG-STATE-MEMORY"},
		{"$XDG_STATE_HOME/mochiii listed", "list_directory", filepath.Join(fx.xdgState, "mochiii"), "memory.db"},
		// The process's own environment, memory and descriptors: the daemon is
		// the process these tools run in, so /proc/self IS the daemon.
		{"/proc/self/environ", "read_file", "/proc/self/environ", "PATH="},
		{"/proc/<daemon pid>/environ", "read_file", fmt.Sprintf("/proc/%d/environ", pid), "PATH="},
		{"/proc/thread-self/environ", "read_file", "/proc/thread-self/environ", "PATH="},
		{"/proc/self/task/<tid>/environ", "read_file", fmt.Sprintf("/proc/self/task/%d/environ", pid), "PATH="},
		{"/proc/./self/environ", "read_file", "/proc/./self/environ", "PATH="},
		{"/proc/self/../self/environ", "read_file", "/proc/self/../self/environ", "PATH="},
		{"/proc/self/cmdline", "read_file", "/proc/self/cmdline", ""},
		{"/proc/self/maps", "read_file", "/proc/self/maps", "r-xp"},
		{"/proc/self/mem", "read_file", "/proc/self/mem", ""},
		{"/proc/self/fd listed", "list_directory", "/proc/self/fd", ""},
		{"/dev/fd/<n> open on the key", "read_file", fmt.Sprintf("/dev/fd/%d", fx.keyFD), credTestKey},
		{"/proc/self/fd/<n> open on the state", "read_file", fmt.Sprintf("/proc/self/fd/%d", fx.stateFD), "FAKE-XDG-STATE-MEMORY"},
		// VS Code's SecretStorage backing store, and the OS keyring.
		{"VS Code state.vscdb", "read_file", "~/.config/Code/User/globalStorage/state.vscdb", "FAKE-VSCODE-SECRETSTORAGE"},
		{"VS Code globalStorage listed", "list_directory", "~/.config/Code/User/globalStorage", "state.vscdb"},
		{"Code - Insiders state.vscdb", "read_file", "~/.config/Code - Insiders/User/globalStorage/state.vscdb", "FAKE-INSIDERS-SECRETSTORAGE"},
		{"VSCodium state.vscdb", "read_file", "~/.config/VSCodium/User/globalStorage/state.vscdb", "FAKE-VSCODIUM-SECRETSTORAGE"},
		{"~/.local/share/keyrings", "read_file", "~/.local/share/keyrings/login.keyring", "FAKE-KEYRING"},
	}
	// THE CONTROL: an ordinary file in the same home IS read after a yes. Without
	// it, a fixture whose own path is refused passes every row above for the
	// wrong reason -- which is what the first draft of this test did.
	writeFiles(t, fx.home, map[string]string{"Documents/notes.txt": "ORDINARY-NOTES"})
	t.Run("control: ~/Documents/notes.txt is readable", func(t *testing.T) {
		s := loopServer(t, "", allowReads())
		asked, result := readWithYes(t, s, "read_file", "~/Documents/notes.txt")
		if asked != 1 || !strings.Contains(result, "ORDINARY-NOTES") {
			t.Fatalf("an ordinary file was not read after a yes (asked %d): %.300q", asked, result)
		}
	})
	// /proc's magic links lead anywhere: judged only by where they lead, a path
	// spelt through them is never a /proc path at all.
	notes, err := os.Open(filepath.Join(fx.home, "Documents", "notes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer notes.Close()
	rows = append(rows,
		struct{ name, tool, path, marker string }{"/proc/self/fd/<n> open on an ordinary file", "read_file",
			fmt.Sprintf("/proc/self/fd/%d", notes.Fd()), "ORDINARY-NOTES"},
		struct{ name, tool, path, marker string }{"/proc/self/root/<home>/Documents/notes.txt", "read_file",
			"/proc/self/root" + filepath.Join(fx.home, "Documents", "notes.txt"), "ORDINARY-NOTES"},
	)
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			s := loopServer(t, "", allowReads())
			asked, result := readWithYes(t, s, r.tool, r.path)
			assertRefused(t, asked, result, r.marker)
		})
	}
}

// A symlink in the workspace is judged by where it leads, whichever way the
// model names it: by its project-relative name (the workspace resolver) or by
// its absolute path (the outside resolver).
func TestAWorkspaceLinkCannotReachAKeyStore(t *testing.T) {
	fx := credentialHome(t)
	targets := map[string]struct{ target, marker string }{
		"notes.txt": {filepath.Join(fx.home, ".mochiii", "credentials.json"), credTestKey},
		"env.txt":   {"/proc/self/environ", "PATH="},
	}
	for link, tg := range targets {
		for _, spelling := range []string{"relative", "absolute"} {
			t.Run(link+" "+spelling, func(t *testing.T) {
				s := loopServer(t, "", allowReads())
				if err := os.Symlink(tg.target, filepath.Join(s.workspace, link)); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				path := link
				if spelling == "absolute" {
					path = filepath.Join(s.workspace, link)
				}
				asked, result := readWithYes(t, s, "read_file", path)
				if asked != 0 {
					t.Errorf("the user was asked %d time(s) about a link into a credential location", asked)
				}
				if strings.Contains(result, tg.marker) {
					t.Errorf("the link led the read to the credential (%q found)", tg.marker)
				}
			})
		}
	}
}

// runLoopInMode drives one turn in a long-task mode, where grep and
// git_history are offered (mcpbuiltin.go).
func runLoopInMode(t *testing.T, s *Server, appr approver, mode string) {
	t.Helper()
	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, mode)
	t.Cleanup(func() { _ = registry.Close() })
	if _, err := s.runAgentLoop(context.Background(), time.Now(), registry, "m", mode,
		[]chatMessage{{Role: "system", Content: "SYSTEM"}, {Role: "user", Content: "go"}}, providerRouting{}, appr,
		func(string) error { return nil }, func(protocol.ToolActivity) {}, nil, nil,
		func(protocol.Degradation) {}, nil, nil); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
}

// callInTask runs one tool call in a long task, with every question answered
// yes, and returns what the tool returned.
func callInTask(t *testing.T, s *Server, tool, args string) string {
	t.Helper()
	base, _, bodies := agentUpstream(t, toolCallSSE("c1", tool, args), textSSE("done"))
	s.apiBase = base
	runLoopInMode(t, s, &capturingApprover{decision: protocol.ApprovalApprove}, modeTask)
	return toolResults(bodies)
}

// grep picks its own files. It must not find the key through a link in the
// project, a linked folder, or a path it is handed.
func TestGrepCannotReachAKeyStore(t *testing.T) {
	fx := credentialHome(t)
	s := loopServer(t, "", allowReads())
	writeFiles(t, s.workspace, map[string]string{"main.go": "package main\n"})
	for link, target := range map[string]string{
		"creds.json": filepath.Join(fx.home, ".mochiii", "credentials.json"),
		"dotmochiii": filepath.Join(fx.home, ".mochiii"),
		"env":        "/proc/self/environ",
		"state":      filepath.Join(fx.xdgState, "mochiii"),
	} {
		if err := os.Symlink(target, filepath.Join(s.workspace, link)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	for _, args := range []string{
		`{"pattern":` + quote(credTestKey) + `}`,
		`{"pattern":"PATH="}`,
		`{"pattern":"FAKE-XDG-STATE-MEMORY"}`,
		`{"pattern":"api_key","path":"dotmochiii"}`,
		`{"pattern":"api_key","path":"~/.mochiii"}`,
		`{"pattern":"PATH=","path":"/proc/self"}`,
	} {
		t.Run(args, func(t *testing.T) {
			result := callInTask(t, s, "grep", args)
			// A match is "path:line: text"; the search text itself is echoed
			// in "No line matches ...", so markers alone would mislead.
			if m := grepMatchUnderLink.FindString(result); m != "" {
				t.Errorf("grep searched a file behind a link (%q): %.300q", m, result)
			}
			for _, leaked := range []string{`{"api_key"`, "PATH=/", "FAKE-XDG-STATE-MEMORY\n"} {
				if strings.Contains(result, leaked) {
					t.Errorf("grep reached a key store (%q in its result): %.300q", leaked, result)
				}
			}
		})
	}
}

var grepMatchUnderLink = regexp.MustCompile(`(?m)^(creds\.json|dotmochiii/|env|state/)[^ ]*:\d+:`)

// git_history reads the project's own object store, by revision and
// project-relative path. A credential is outside both.
func TestGitHistoryCannotReachAKeyStore(t *testing.T) {
	fx := credentialHome(t)
	s := loopServer(t, "", allowReads())
	s.workspace = gitRepo(t, map[string]string{"main.go": "package main\n"})
	link := filepath.Join(s.workspace, "creds.json")
	if err := os.Symlink(filepath.Join(fx.home, ".mochiii", "credentials.json"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Committed too: git stores a link as the path it names, never the file.
	cmd := exec.Command("git", "-C", s.workspace, "add", "creds.json")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "-C", s.workspace, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "link")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	for _, args := range []string{
		`{"command":"show"}`,
		`{"command":"log","path":"creds.json"}`,
		`{"command":"blame","path":"creds.json"}`,
		`{"command":"log","path":"~/.mochiii/credentials.json"}`,
		`{"command":"log","path":"/proc/self/environ"}`,
		`{"command":"show","rev":"HEAD:creds.json"}`,
	} {
		t.Run(args, func(t *testing.T) {
			result := callInTask(t, s, "git_history", args)
			if strings.Contains(result, credTestKey) || strings.Contains(result, "PATH=") {
				t.Errorf("git_history reached a credential: %.300q", result)
			}
		})
	}
}

// Windows spellings cannot be driven through the loop on this platform, so the
// gate itself is asked -- the same function both read tools call.
func TestKeyStoreRefusalWindowsSpellings(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home folder:", err)
	}
	for _, p := range []string{
		home + `\.mochiii\credentials.json`,
		home + `\AppData\Roaming\Code\User\globalStorage\state.vscdb`,
		home + `\AppData\Roaming\Code - Insiders\User\globalStorage\state.vscdb`,
		home + `\AppData\Roaming\VSCodium\User\globalStorage\state.vscdb`,
		home + `\.local\state\mochiii\memory.db`, // StateDir() on Windows too (memory.go)
	} {
		if outsideReadRefusal(p) == "" {
			t.Errorf("outsideReadRefusal(%q) = \"\", want refused", p)
		}
	}
}
