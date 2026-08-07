//go:build unix

package main

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// LOSING THE STARTUP RACE, AND WHAT IT COSTS.
//
// Two VS Code windows on the SAME repository is the ordinary case: one project,
// one window per branch, or simply a second window opened by habit. The
// per-workspace lockfile (4ae4778) fixed two windows on two DIFFERENT repos; it
// deliberately did not change this one, because two windows on one repo SHOULD
// share a daemon -- same root, same index, same answers.
//
// What happens instead is that the second window starts a daemon anyway, and
// that daemon does the expensive half of startup BEFORE it checks whether it is
// allowed to run at all. main() currently reads:
//
//	setupRetrieval(...)        <- spawns the embedder helper subprocess
//	setupMemoryStore(...)      <- opens the SQLite conversation store
//	reclaimStaleSocket(addr)   <- ...and only HERE does it find out it lost
//	  -> logger.Fatal          <- which is os.Exit(1), skipping every defer
//
// MEASURED, not reasoned. Baseline 0 helpers; daemon A running, 1; daemon B
// started on the same workspace, lost, and exited 1 -- leaving 2. Stopping A
// cleanly went back to 1, not 0. B's embedder helper (81 MB RSS, holding the
// BGE model) was reparented and never collected, because os.Exit does not run
// `defer retrieval.Stop()`.
//
// Before 200308e bounded it, the extension restarted that daemon every three
// seconds forever: 81 MB orphaned every three seconds, for as long as the
// window stayed open.
//
// TWO DEFECTS, ASSERTED SEPARATELY BELOW:
//
//  1. ORDERING. Mutual exclusion is the cheapest operation in startup and the
//     one that decides everything, and it runs last. Every resource acquired
//     before it is acquired speculatively.
//  2. INDISTINGUISHABILITY. The loser exits 1, which is also what an unreadable
//     models.json exits, and what a missing API base exits. A supervising client
//     cannot tell "another window already serves this repo" (benign -- adopt it)
//     from "this daemon is broken" (report it), so it must treat both the same
//     and is wrong in one of the two cases whichever it picks.
//
// These tests were committed as a REPRODUCTION before the fix. They assert the
// fixed behaviour; run against the parent commit, both fail.
func TestStartupRace_LoserIsDistinguishableFromABrokenDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the daemon binary")
	}
	bin := buildDaemonBinary(t)

	runtimeDir := shortRuntimeDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n")

	stopA := startDaemon(t, bin, runtimeDir, repo)
	defer stopA()

	// THE LOSER: same workspace, so the same address, which A already holds.
	loser := runDaemonToCompletion(t, bin, runtimeDir, repo)
	if loser.code != exitAlreadyRunning {
		t.Errorf("a daemon that lost the startup race exited %d, want %d (exitAlreadyRunning).\n"+
			"Its output:\n%s", loser.code, exitAlreadyRunning, loser.log)
	}

	// THE CONTROL, and the half that makes the assertion above mean anything: a
	// genuinely broken daemon must NOT share that code. Without this, "exits 3"
	// could be satisfied by making every failure exit 3, which would leave a
	// client exactly as unable to tell the two apart as it is now.
	broken := runDaemonToCompletion(t, bin, runtimeDir, repo, "--config", filepath.Join(repo, "nope.json"))
	if broken.code == exitAlreadyRunning {
		t.Errorf("a daemon that could not read its config ALSO exited %d; the code must "+
			"distinguish losing the race from being broken, or a supervisor cannot act on it.\n"+
			"Its output:\n%s", broken.code, broken.log)
	}
	if broken.code == 0 {
		t.Fatalf("the broken-config control exited 0; it is not testing what it claims.\nIts output:\n%s", broken.log)
	}
}

// The ordering defect, which is the one that leaks.
//
// Asserted through the daemon's OWN LOG rather than by counting processes,
// deliberately: counting helpers needs the BGE model present, which no CI runner
// has, so that test would skip exactly where it is most needed. The log proves
// the same thing and proves it everywhere -- if setupRetrieval ran at all, it
// said so, whether it succeeded ("retrieval enabled") or degraded ("retrieval
// disabled: starting embedder: ..."). Either line appearing BEFORE the refusal
// is the inversion, and on a machine that does have the model, that same line is
// the helper being spawned.
func TestStartupRace_LoserExitsBeforeAcquiringResources(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the daemon binary")
	}
	bin := buildDaemonBinary(t)

	runtimeDir := shortRuntimeDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n")

	stopA := startDaemon(t, bin, runtimeDir, repo)
	defer stopA()

	loser := runDaemonToCompletion(t, bin, runtimeDir, repo)

	// Every line the loser wrote that shows a resource was acquired or attempted.
	// "retrieval" covers both the enabled and the degraded path; "conversation
	// memory" is setupMemoryStore opening SQLite.
	for _, marker := range []string{"retrieval", "conversation memory"} {
		if idx := strings.Index(loser.log, marker); idx >= 0 {
			t.Errorf("a daemon that lost the startup race still ran %q before finding out.\n"+
				"Mutual exclusion is the cheapest step in startup and the one that decides "+
				"everything; anything acquired before it is acquired speculatively, and "+
				"os.Exit skips the defer that would release it -- which is how the embedder "+
				"helper (81 MB) is orphaned.\nIts output:\n%s", marker, loser.log)
		}
	}

	// And it must still say WHY, or the exit code is the only diagnostic and a
	// human reading a log learns nothing.
	if !strings.Contains(loser.log, "already listening") {
		t.Errorf("the loser exited without naming the reason; the code is for the supervisor, "+
			"the message is for the human.\nIts output:\n%s", loser.log)
	}
}

// --- fixtures ---

type daemonRun struct {
	code int
	log  string
}

// runDaemonToCompletion runs a daemon in the FOREGROUND and returns how it
// ended. Unlike startDaemon it expects the process to exit on its own, which is
// the whole subject of this file.
func runDaemonToCompletion(t *testing.T, bin, runtimeDir, workspace string, extra ...string) daemonRun {
	t.Helper()
	args := append([]string{"--workspace", workspace}, extra...)
	cmd := exec.Command(bin, args...)
	cmd.Env = daemonEnv(runtimeDir)
	var log lockedBuffer
	cmd.Stdout, cmd.Stderr = &log, &log

	err := cmd.Run()
	if err == nil {
		return daemonRun{code: 0, log: log.String()}
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("running the daemon: %v\n%s", err, log.String())
	}
	return daemonRun{code: exit.ExitCode(), log: log.String()}
}
