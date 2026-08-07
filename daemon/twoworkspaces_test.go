//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"codeterminal/editapply"
	"codeterminal/protocol"
)

// TWO VS CODE WINDOWS ON TWO REPOSITORIES MUST BOTH WORK.
//
// This is the normal case, not an edge case: a developer with two projects
// open. It has never been executed until now -- the defect was found by reading
// three files that are individually correct.
//
// The chain, and every link is deliberate behaviour:
//
//  1. protocol.LockPath() is RuntimeDir()/codeterminal/daemon.lock. PER USER.
//     There is no workspace in that path.
//  2. clients/vscode/src/extension.ts spawns a daemon unconditionally on
//     activate(). It never probes the lockfile and never adopts a running
//     daemon.
//  3. The second daemon's reclaimStaleSocket finds the first one ALIVE and
//     calls logger.Fatal -> exit 1.
//  4. The extension's exit handler restarts on any non-zero exit that is not a
//     signal, after 3 s. Forever, with a warning toast each cycle.
//
// And the client that spawned the loop reads the same per-user lockfile, so it
// reaches the FIRST window's daemon and asks it about the SECOND window's repo.
//
// This file was committed one commit earlier as a REPRODUCTION, asserting each
// of those failures. The fix -- per-workspace socket and lockfile names, keyed
// on WorkspaceTag(resolved root) -- inverted every assertion, which is why the
// tests now read the other way round. The original messages are preserved in
// b1bfa5e.
//
// NEUTER CHECK: change daemon/main.go back to protocol.DefaultAddress() /
// protocol.LockPath() and the second daemon fails to start again. Measured, not
// asserted -- that is exactly what these tests did before the fix landed.
//
// Worth stating because it bounds what this fixes: the old failure was NOT
// silent. GroundingInfo.WorkspaceMismatch was set and both clients rendered it,
// so the user was told the answer was about the wrong repository. The fix is
// that they are no longer told anything, because nothing is wrong.
func TestTwoWorkspaces_BothDaemonsStart(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the daemon binary")
	}
	bin := buildDaemonBinary(t)

	runtimeDir := shortRuntimeDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	repoA, repoB := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(repoA, "a.go"), "package a\n")
	writeFile(t, filepath.Join(repoB, "b.go"), "package b\n")

	stopA := startDaemon(t, bin, runtimeDir, repoA)
	defer stopA()
	stopB := startDaemon(t, bin, runtimeDir, repoB)
	defer stopB()

	// Two lockfiles, two names. Before the fix the second daemon never got this
	// far -- it exited 1 with "a daemon is already listening".
	lockA, lockB := lockPathIn(t, runtimeDir, repoA), lockPathIn(t, runtimeDir, repoB)
	if lockA == lockB {
		t.Fatalf("both workspaces derived the SAME lockfile (%s); they would collide exactly as before", lockA)
	}
	for _, p := range []string{lockA, lockB} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("no lockfile at %s: %v", p, err)
		}
	}

	// The tag must not be the workspace path in disguise: it goes into a
	// sockaddr_un capped at ~104 bytes, and a real project path is longer than
	// the budget leaves.
	if base := filepath.Base(lockA); len(base) > 40 {
		t.Errorf("lockfile name %q is %d chars; the socket beside it shares a ~104-byte "+
			"sockaddr_un budget with the runtime directory", base, len(base))
	}
	if strings.Contains(filepath.Base(lockA), filepath.Base(repoA)) {
		t.Errorf("lockfile name %q embeds the workspace path; it must be a bounded hash",
			filepath.Base(lockA))
	}
}

// Each window's client reaches ITS OWN daemon. This is the half that was
// silently wrong: window B used to read the one per-user lockfile and be
// answered by window A's daemon about window A's code.
func TestTwoWorkspaces_EachClientReachesItsOwnDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the daemon binary")
	}
	bin := buildDaemonBinary(t)

	runtimeDir := shortRuntimeDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	repoA, repoB := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(repoA, "a.go"), "package a\n")
	writeFile(t, filepath.Join(repoB, "b.go"), "package b\n")

	stopA := startDaemon(t, bin, runtimeDir, repoA)
	defer stopA()
	stopB := startDaemon(t, bin, runtimeDir, repoB)
	defer stopB()

	for _, tc := range []struct{ name, repo string }{{"window A", repoA}, {"window B", repoB}} {
		want, err := filepath.EvalSymlinks(tc.repo)
		if err != nil {
			t.Fatal(err)
		}
		// Derived the way a client derives it: from the workspace it has open,
		// with no knowledge of any other daemon.
		lock := readLockFileAt(t, lockPathIn(t, runtimeDir, tc.repo))
		got := statusWorkspace(t, lock)
		if got != want {
			t.Errorf("%s (repo %s) was answered by the daemon serving %s; a client must reach "+
				"its own workspace's daemon", tc.name, want, got)
		}
	}
}

// --- fixtures ---

// buildDaemonBinary compiles the REAL daemon and stages a models.json beside
// it, which is how resolveConfigPath finds one in a shipped install (see
// helperpath.go's note on why CWD-relative lookup was the wrong default).
func buildDaemonBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, exeName("codeterminal-daemon"))
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the daemon: %v\n%s", err, out)
	}

	cfg, err := os.ReadFile(filepath.Join("..", "models.json"))
	if err != nil {
		t.Fatalf("reading the repo's models.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.json"), cfg, 0600); err != nil {
		t.Fatal(err)
	}
	return bin
}

// daemonEnv isolates a daemon into its own runtime dir. XDG_RUNTIME_DIR is what
// protocol.RuntimeDir reads on Unix, so this is the same knob a real session
// uses -- not a test seam.
//
// The API base is a dead port ON PURPOSE. These tests never prompt, so nothing
// reaches it, and pointing it anywhere real would risk spend from a test. The
// daemon requires the variable to be set at all, which is why it is here.
func daemonEnv(runtimeDir string) []string {
	env := append(os.Environ(),
		"XDG_RUNTIME_DIR="+runtimeDir,
		"CODETERMINAL_API_BASE=http://127.0.0.1:1",
	)
	// Whatever the developer's shell holds must not reach a test daemon.
	return append(env, "CODETERMINAL_API_KEY=", "CODETERMINAL_MOCHIII_KEY=", "OPENROUTER_API_KEY=")
}

func startDaemon(t *testing.T, bin, runtimeDir, workspace string) (stop func()) {
	t.Helper()
	cmd := exec.Command(bin, "--workspace", workspace)
	cmd.Env = daemonEnv(runtimeDir)
	// Captured so a startup failure reports the daemon's OWN reason rather than
	// only "it never wrote a lockfile", which is the symptom and not the cause.
	var log lockedBuffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting daemon: %v", err)
	}
	stop = func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}

	lockPath := lockPathIn(t, runtimeDir, workspace)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(lockPath); err == nil {
			return stop
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	t.Fatalf("daemon for %s never wrote %s. Its output:\n%s", workspace, lockPath, log.String())
	return stop
}

func readLockFileAt(t *testing.T, path string) protocol.LockFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading lockfile: %v", err)
	}
	var lf protocol.LockFile
	if err := json.Unmarshal(raw, &lf); err != nil {
		t.Fatalf("parsing lockfile %q: %v", raw, err)
	}
	return lf
}

// lockPathIn derives the lockfile a client with this workspace open would read,
// through protocol's own helper and the same canonicalisation the daemon uses.
func lockPathIn(t *testing.T, runtimeDir, workspace string) string {
	t.Helper()
	real, err := editapply.ResolveRealWorkspaceRoot(workspace)
	if err != nil {
		t.Fatalf("resolving %s: %v", workspace, err)
	}
	// Through protocol's own helper, never a literal, so a change to the naming
	// convention cannot leave this test asserting the old one. runtimeDir is
	// already in the environment; see each test's t.Setenv.
	_ = runtimeDir
	return protocol.LockPathFor(real)
}

// statusWorkspace asks a daemon which workspace it is grounded against.
// StatusRequest, not a prompt: no model call, no spend.
func statusWorkspace(t *testing.T, lock protocol.LockFile) string {
	t.Helper()
	conn, err := protocol.DialTimeout(protocol.AddressFromLock(lock), 5*time.Second)
	if err != nil {
		t.Fatalf("dial %v: %v", lock.Address, err)
	}
	defer conn.Close()

	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)
	if err := enc.Encode(protocol.HandshakeRequest{
		ProtocolVersion: protocol.ProtocolVersion, ClientName: "workspace-probe",
	}); err != nil {
		t.Fatalf("handshake send: %v", err)
	}
	var hs protocol.HandshakeResponse
	if err := dec.Decode(&hs); err != nil {
		t.Fatalf("handshake recv: %v", err)
	}
	if !hs.Ok {
		t.Fatalf("handshake refused: %s", hs.Error)
	}
	if err := enc.Encode(protocol.StatusRequest{
		ProtocolVersion: protocol.ProtocolVersion, Status: true,
	}); err != nil {
		t.Fatalf("status send: %v", err)
	}
	var st protocol.StatusResponse
	if err := dec.Decode(&st); err != nil {
		t.Fatalf("status recv: %v", err)
	}
	return st.Workspace
}

// lockedBuffer is an io.Writer safe for a child's stdout and stderr at once.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// shortRuntimeDir is t.TempDir() with the test's NAME left out of the path.
//
// Not a convenience. t.TempDir() embeds the test function name, and a unix
// socket address is capped at 103 bytes (see protocol's checkSocketPathLength),
// so a longer test name meant a shorter budget for the socket -- and the
// per-workspace name added 17 bytes to it. TestTwoWorkspaces_BothDaemonsStart
// fitted and TestTwoWorkspaces_EachClientReachesItsOwnDaemon did not, which is
// the sort of difference that reads as flakiness. A real $XDG_RUNTIME_DIR is
// /run/user/1000, so this is also the more faithful fixture.
func shortRuntimeDir(t *testing.T) string {
	t.Helper()
	// Delegates: this used to call os.MkdirTemp("", "ctrt"), which honours
	// $TMPDIR and so was still ~49 bytes deep on macOS. See shortTempDir.
	return shortTempDir(t)
}
