//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// THE INSTALL PATH: what a user who has only ever double-clicked an icon gets.
//
// Every other test in this package that starts a real daemon hands it
// MOCHIII_API_BASE (see daemonEnv in twoworkspaces_test.go, whose own
// comment says "The daemon requires the variable to be set at all, which is why
// it is here"). The VS Code extension does not, and cannot: spawnDaemon in
// clients/vscode/src/extension.ts passes no env, so the daemon inherits the
// extension host's -- and a VS Code launched from a desktop icon carries none
// of a login shell's exports. There is no setting to supply one either;
// package.json contributes only commands.
//
// So the variable is set in every test and in no install. The tests were
// measuring a configuration no user has.
//
// This spawns the shipped binary the way an install does -- a minimal
// environment with no MOCHIII_* in it at all -- and asserts it reaches the
// state a client probes for. The daemon is ready when it has written its
// lockfile; that is the same signal probeDaemon and the supervisor key on, not
// a proxy invented here.
func TestInstallPathDaemonStartsWithNoShellEnvironment(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the daemon binary")
	}
	bin := buildDaemonBinary(t)
	runtimeDir := shortRuntimeDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	workspace := t.TempDir()
	writeFile(t, filepath.Join(workspace, "a.go"), "package a\n")

	// THE CONTROL COMES FIRST, and it is the difference between this test being
	// evidence and being a broken harness reporting a defect. If a daemon
	// cannot be started here even WITH the variable, a failure below says
	// nothing about the install path.
	if out, err := startForInstallTest(t, bin, installEnv(runtimeDir, true), workspace); err != nil {
		t.Fatalf("CONTROL ARM FAILED: a daemon would not start even with MOCHIII_API_BASE set, "+
			"so this harness cannot measure the install path at all: %v\nDaemon output:\n%s", err, out)
	}

	env := installEnv(runtimeDir, false)
	// Vacuity floor. The whole point is an environment with no MOCHIII_*
	// in it; if one leaked in from the developer's shell the test would pass by
	// being handed the very thing it is supposed to prove is unnecessary.
	for _, kv := range env {
		if strings.HasPrefix(kv, "MOCHIII_") {
			t.Fatalf("vacuity floor: the install environment contains %q, so this asserts nothing", kv)
		}
	}

	out, err := startForInstallTest(t, bin, env, t.TempDir())
	if err != nil {
		t.Fatalf("the daemon did not reach a ready state from an ordinary install environment: %v\n"+
			"This is what the VS Code extension gets on a machine where nobody exported anything.\n"+
			"Daemon output:\n%s", err, out)
	}
}

// installEnv builds the environment a GUI-launched editor would hand a child:
// the few variables any process needs, and nothing a login shell exported.
// withAPIBase is the control knob -- true reproduces what every other test
// supplies, false reproduces what an install actually has.
//
// The base is a dead port on purpose, matching daemonEnv's reasoning: these
// tests never prompt, and pointing it anywhere real would risk spend.
func installEnv(runtimeDir string, withAPIBase bool) []string {
	env := []string{
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
		"XDG_RUNTIME_DIR=" + runtimeDir,
	}
	if withAPIBase {
		env = append(env, "MOCHIII_API_BASE=http://127.0.0.1:1")
	}
	return env
}

// startForInstallTest spawns the daemon and waits for the lockfile. It returns
// the daemon's own output either way, because "it never wrote a lockfile" is
// the symptom and the reason is in the log.
func startForInstallTest(t *testing.T, bin string, env []string, workspace string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, "--workspace", workspace)
	cmd.Env = env
	var log lockedBuffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		return log.String(), err
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	lockPath := lockPathIn(t, os.Getenv("XDG_RUNTIME_DIR"), workspace)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(lockPath); err == nil {
			return log.String(), nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return log.String(), errTimedOutWaitingForLockfile{lockPath}
}

type errTimedOutWaitingForLockfile struct{ path string }

func (e errTimedOutWaitingForLockfile) Error() string {
	return "no lockfile at " + e.path + " within 20s"
}
