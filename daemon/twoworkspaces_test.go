//go:build unix

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeterminal/protocol"
)

// TWO VS CODE WINDOWS ON TWO REPOSITORIES CANNOT BOTH WORK.
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
// What this test asserts is (1) and (3) -- the daemon-side half, which is where
// the fix belongs. The extension's restart handler is the other half and is
// tested in clients/vscode.
//
// NOT SILENT, and the test says so below: GroundingInfo.WorkspaceMismatch is
// set for exactly this case and both clients render it. The user is told their
// answer is about the wrong repository. That is honest, and it is still not a
// working product.
func TestTwoWorkspaces_SecondDaemonCannotStart(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the daemon binary")
	}
	bin := buildDaemonBinary(t)

	// One runtime dir for both, which is the point: a real user has one.
	runtimeDir := t.TempDir()
	repoA, repoB := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(repoA, "a.go"), "package a\n")
	writeFile(t, filepath.Join(repoB, "b.go"), "package b\n")

	stopA := startDaemon(t, bin, runtimeDir, repoA)
	defer stopA()

	// THE PREMISE, asserted rather than assumed: the lockfile carries no trace
	// of the workspace. If it ever does, this test is measuring something else.
	lockPath := filepath.Join(runtimeDir, "codeterminal", "daemon.lock")
	raw, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("daemon A wrote no lockfile at %s: %v", lockPath, err)
	}
	if strings.Contains(string(raw), filepath.Base(repoA)) {
		t.Skipf("NOT RUN: the lockfile now names the workspace (%s) -- the per-user "+
			"collision this test measures no longer exists", raw)
	}

	// Window B opens the OTHER repo. Same runtime dir, same lockfile.
	out, err := runDaemonOnce(t, bin, runtimeDir, repoB)

	if err == nil {
		t.Fatal("daemon B started successfully; the per-user collision is gone and this test " +
			"should be replaced by one that asserts both daemons serve their own workspace")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("daemon B failed in an unexpected way: %v\n%s", err, out)
	}

	// THE LOOP TRIGGER. The extension restarts on any non-zero exit that is not
	// a signal, so this exact code is what turns a correct refusal into an
	// infinite restart cycle in the user's editor.
	if code := exitErr.ExitCode(); code != 1 {
		t.Errorf("daemon B exit code = %d, want 1", code)
	}
	if !strings.Contains(out, "already listening") {
		t.Errorf("daemon B said %q, want it to name the collision", out)
	}

	t.Logf("REPRODUCED: window B's daemon exits %d (%q). "+
		"clients/vscode/src/extension.ts restarts on code!==0 after 3s, forever.",
		exitErr.ExitCode(), strings.TrimSpace(lastLine(out)))
}

// And the consequence for the client that could not start its own daemon: the
// lockfile it reads points at the OTHER workspace's daemon.
func TestTwoWorkspaces_TheLockfileSendsWindowBToWindowAsDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the daemon binary")
	}
	bin := buildDaemonBinary(t)

	runtimeDir := t.TempDir()
	repoA, repoB := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(repoA, "a.go"), "package a\n")

	stopA := startDaemon(t, bin, runtimeDir, repoA)
	defer stopA()

	lock := readLockFileAt(t, filepath.Join(runtimeDir, "codeterminal", "daemon.lock"))

	// Window B derives its address exactly as clients/vscode/src/daemonClient.ts
	// does -- from the per-user lockfile, with no workspace input at all.
	conn, err := protocol.DialTimeout(lock.Address, 5*time.Second)
	if err != nil {
		t.Fatalf("dialling the address window B would find: %v", err)
	}
	defer conn.Close()

	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)
	if err := enc.Encode(protocol.HandshakeRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		ClientName:      "window-b",
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

	// StatusRequest, not a prompt: it names the workspace the daemon is actually
	// grounded against, with no model call and no spend.
	if err := enc.Encode(protocol.StatusRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		Status:          true,
	}); err != nil {
		t.Fatalf("status send: %v", err)
	}
	var st protocol.StatusResponse
	if err := dec.Decode(&st); err != nil {
		t.Fatalf("status recv: %v", err)
	}

	realB, _ := filepath.EvalSymlinks(repoB)
	realA, _ := filepath.EvalSymlinks(repoA)
	if st.Workspace == realB {
		t.Fatalf("unexpectedly correct: window B reached a daemon serving its OWN workspace (%s); "+
			"the defect this test records is gone and the test should be inverted", st.Workspace)
	}
	if st.Workspace != realA {
		t.Fatalf("window B reached a daemon serving %q, which is neither repo (A=%s B=%s)",
			st.Workspace, realA, realB)
	}

	t.Logf("REPRODUCED: window B (repo %s) was routed to the daemon serving %s. "+
		"GroundingInfo.WorkspaceMismatch marks this for the user -- it is wrong, not silent.",
		filepath.Base(realB), filepath.Base(realA))
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
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting daemon: %v", err)
	}
	stop = func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}

	lockPath := filepath.Join(runtimeDir, "codeterminal", "daemon.lock")
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(lockPath); err == nil {
			return stop
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	t.Fatalf("daemon never wrote %s", lockPath)
	return stop
}

func runDaemonOnce(t *testing.T, bin, runtimeDir, workspace string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, "--workspace", workspace)
	cmd.Env = daemonEnv(runtimeDir)
	out, err := cmd.CombinedOutput()
	return string(out), err
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

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}
