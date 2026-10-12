//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A DAEMON STARTED THE WAY THE VS CODE EXTENSION NOW STARTS IT -- the key on
// stdin, nothing in the environment -- authenticates with that key, and its
// /proc/<pid>/environ holds no MOCHIII_ variable at all.
//
// Linux-only because /proc is. What it guards: a key in a process's starting
// environment is readable from /proc/<pid>/environ by anything running as the
// same user for the process's whole life, and os.Unsetenv cannot take it back
// (the file is the original environment block). The only defence is that it
// never goes there.

// procNames returns the variable NAMES in /proc/<pid>/environ. Values are never
// returned, so a failing assertion cannot print one.
//
// IT WAITS FOR A POPULATED BLOCK, because an empty one proves nothing and reads
// as a pass. Linux releases a vfork parent during exec BEFORE the new image's
// environment pointers are set, so a read taken as cmd.Start returns sees an
// empty environ -- measured: 195 of 200 immediate reads on this kernel. An
// absence check over that empty read would pass for any process whatever it
// holds; this is what TestProcEnvironReaderSeesAVariableThatIsThere caught.
func procNames(t *testing.T, pid int) []string {
	t.Helper()
	var raw []byte
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		raw, err = os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
		if err != nil {
			t.Fatalf("reading /proc/%d/environ: %v", pid, err)
		}
		if len(raw) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("/proc/%d/environ stayed empty, so nothing about it can be asserted", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var names []string
	for _, kv := range bytes.Split(raw, []byte{0}) {
		if len(kv) == 0 {
			continue
		}
		name, _, _ := bytes.Cut(kv, []byte("="))
		names = append(names, string(name))
	}
	return names
}

// THE READER CAN SEE A VARIABLE. Without this, a procNames that silently read
// the wrong process, or nothing, would pass the absence check below for free.
func TestProcEnvironReaderSeesAVariableThatIsThere(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "MOCHIII_ENVIRON_PROBE=1"}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	for _, n := range procNames(t, cmd.Process.Pid) {
		if n == "MOCHIII_ENVIRON_PROBE" {
			return
		}
	}
	t.Fatal("procNames did not find a variable the process was started with")
}

// authModel is a provider that records the Authorization header of every
// completion request and answers "ok".
type authModel struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []string
}

func newAuthModel(t *testing.T) *authModel {
	t.Helper()
	m := &authModel{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		m.mu.Lock()
		m.seen = append(m.seen, r.Header.Get("Authorization"))
		m.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range []string{
			`data: {"choices":[{"delta":{"content":"ok"}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
		} {
			_, _ = fmt.Fprintf(w, "%s\n\n", line)
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *authModel) authorizations() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.seen...)
}

// startStdinDaemon starts the real daemon with --credentials-from-stdin and
// writes line to its stdin, with an environment from which every MOCHIII_
// variable has been removed -- what extension.ts's daemonEnvironment builds.
func startStdinDaemon(t *testing.T, line string) (*e2eDaemon, *exec.Cmd) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and starts the real daemon")
	}
	bin := e2eBinary(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "mochiii-daemon")
	if err := os.Link(bin, exe); err != nil {
		raw, rerr := os.ReadFile(bin)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if werr := os.WriteFile(exe, raw, 0o755); werr != nil {
			t.Fatal(werr)
		}
	}
	cfg, err := os.ReadFile(filepath.Join("..", "models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.json"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}

	ws := realTempDir(t)
	runtimeDir := shortRuntimeDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	home := t.TempDir()

	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "MOCHIII_") || name == "OPENROUTER_API_KEY" ||
			name == "HOME" || strings.HasPrefix(name, "XDG_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"XDG_RUNTIME_DIR="+runtimeDir,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
	)

	cmd := exec.Command(exe, "--workspace", ws, "--"+credentialsFromStdinFlag)
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	log := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		if t.Failed() {
			t.Logf("daemon output:\n%s", log.String())
		}
	})
	// Exactly what the extension does: one line, then close.
	if _, err := io.WriteString(stdin, line+"\n"); err != nil {
		t.Fatalf("writing the credentials line: %v", err)
	}
	_ = stdin.Close()

	lockPath := lockPathIn(t, runtimeDir, ws)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(lockPath); err == nil {
			return &e2eDaemon{workspace: ws, lock: readLockFileAt(t, lockPath), log: log}, cmd
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the daemon never wrote %s. Its output:\n%s", lockPath, log.String())
	return nil, nil
}

func TestADaemonGivenItsKeyOnStdinUsesItAndHoldsNoKeyInItsEnviron(t *testing.T) {
	model := newAuthModel(t)
	// The base travels on stdin too, so the environment needs no MOCHIII_
	// variable at all -- the strongest form of the assertion.
	line, err := json.Marshal(map[string]string{
		"api_key":  launchFake,
		"api_base": model.srv.URL + "/api/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	d, cmd := startStdinDaemon(t, string(line))

	// 1. THE KEY WORKS: a real prompt reaches the provider carrying it.
	d.prompt(t, "say ok", nil)
	auths := model.authorizations()
	if len(auths) == 0 {
		t.Fatal("the prompt never reached the provider, so nothing about the key was shown")
	}
	for i, a := range auths {
		if a != "Bearer "+launchFake {
			t.Errorf("completion request %d did not carry the stdin key (Authorization present: %v)", i, a != "")
		}
	}

	// 2. NO MOCHIII_ VARIABLE IN /proc/<pid>/environ.
	names := procNames(t, cmd.Process.Pid)
	if len(names) == 0 {
		t.Fatal("the daemon's environ is empty, so its absence of MOCHIII_ variables proves nothing")
	}
	for _, n := range names {
		if strings.HasPrefix(n, "MOCHIII_") {
			t.Errorf("/proc/%d/environ holds %s", cmd.Process.Pid, n)
		}
	}

	// 3. NOT ON THE COMMAND LINE EITHER: /proc/<pid>/cmdline is as readable as
	// environ, and moving a key from one to the other would fix nothing.
	rawArgs, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rawArgs, []byte(launchFake)) {
		t.Error("the key is in the daemon's command line")
	}
	if !bytes.Contains(rawArgs, []byte("--"+credentialsFromStdinFlag)) {
		t.Errorf("the daemon was not started with --%s", credentialsFromStdinFlag)
	}

	// 4. THE LOG SAYS WHERE THE KEY CAME FROM, MASKED.
	out := d.log.String()
	if !strings.Contains(out, "using the key supplied on stdin") {
		t.Error("the daemon log does not say the key came from stdin")
	}
	if strings.Contains(out, launchFake) {
		t.Error("the daemon log carries the raw key")
	}
}
