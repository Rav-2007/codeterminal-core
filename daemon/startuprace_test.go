//go:build unix

package main

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mochiii/editapply"
	"mochiii/protocol"
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
	if broken.code != exitFailure {
		t.Errorf("a daemon that could not read its config exited %d, want %d (exitFailure).\n"+
			"The two codes must stay DIFFERENT: a supervisor that cannot separate 'another "+
			"window serves this repo' from 'this daemon is broken' has to guess, and is wrong "+
			"in one of the two cases whichever way it guesses.\nIts output:\n%s",
			broken.code, exitFailure, broken.log)
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

	// And it must still say WHY. The exit code is for the supervisor; the message
	// is for the human reading a log, who gets no exit code at all.
	//
	// Asserted against the product's OWN sentinel text rather than a copied
	// string, so rewording the message cannot silently empty this check out.
	if !strings.Contains(loser.log, errAlreadyRunning.Error()) {
		t.Errorf("the loser exited without naming the reason (want %q in its output).\n"+
			"Its output:\n%s", errAlreadyRunning.Error(), loser.log)
	}
	if !strings.Contains(loser.log, addressOf(t, repo)) {
		t.Errorf("the loser named the reason but not WHICH address was taken, which is what "+
			"tells a human whether it is the daemon they think it is.\nIts output:\n%s", loser.log)
	}
}

// THE CONCURRENT RACE, which the two tests above cannot reach.
//
// They are SEQUENTIAL: daemon A is fully up before B starts, so B loses at
// reclaimStaleSocket's probe. Two windows opening the same repo AT ONCE lose
// somewhere else entirely -- both probe, both find nothing, and both reach
// net.Listen, where the kernel arbitrates and the loser gets EADDRINUSE. That is
// a second code path (main.go's isAddrInUse branch) and without it the
// concurrent loser exits 1, burning a supervisor restart attempt on a daemon
// that was never broken.
//
// This asserts the CONTRACT under real concurrency rather than which branch
// fired -- there is no way to force a specific one of the two without a seam in
// product code that exists only for the test. What it does prove is the property
// that matters: however a daemon loses, it says so the same way.
//
// Not flaky in the failing direction. A correct implementation exits 0 or 3 on
// every schedule; only a missing branch produces a 1, and any schedule that
// reaches it fails the test.
func TestStartupRace_ConcurrentLosersAllReportTheSameWay(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the daemon binary")
	}
	bin := buildDaemonBinary(t)

	runtimeDir := shortRuntimeDir(t)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "a.go"), "package a\n")

	const racers = 6
	rs := make([]*racer, racers)
	start := make(chan struct{})
	for i := range rs {
		rs[i] = startRacer(bin, runtimeDir, repo, start)
	}
	close(start) // released together, so they contend for real

	// Long enough for every loser to lose. A loser now allocates nothing before
	// deciding (that is the other half of this fix), so this is fast.
	time.Sleep(5 * time.Second)

	var survivors []int
	for i, r := range rs {
		select {
		case code := <-r.exited:
			if code != exitAlreadyRunning {
				t.Errorf("racer %d exited %d, want %d (exitAlreadyRunning).\n"+
					"Every daemon that loses this race lost it for the same reason and must say "+
					"so the same way, whether it lost at the probe or at the bind.\nIts output:\n%s",
					i, code, exitAlreadyRunning, r.log.String())
			}
		default:
			survivors = append(survivors, i)
		}
	}

	for _, i := range survivors {
		rs[i].shutdown()
	}

	if len(survivors) != 1 {
		t.Errorf("%d of %d racers were still running, want exactly 1; the point of the race is "+
			"that exactly one daemon ends up serving the workspace", len(survivors), racers)
	}
}

// --- fixtures ---

// racer is one contender in the concurrent-startup test, and it OWNS its
// exec.Cmd completely.
//
// That ownership is the point, not a style choice. The first version of this
// test kept the []*exec.Cmd in the test goroutine and reached into
// cmds[i].Process to poll and kill. -race caught it: Cmd.Start WRITES
// cmd.Process from the racer's goroutine, and the test goroutine read it with
// no happens-before edge between them. Killing across that boundary raced again
// inside os.Process. So nothing outside this struct's own goroutine touches the
// command; the test learns everything through channels.
type racer struct {
	log    *lockedBuffer
	exited chan int      // the exit code, exactly once
	stop   chan struct{} // closed to ask this racer to stop
}

// startRacer launches one daemon that waits on `gate` before exec'ing, so a
// whole field can be released at once.
func startRacer(bin, runtimeDir, workspace string, gate <-chan struct{}) *racer {
	r := &racer{
		log:    &lockedBuffer{},
		exited: make(chan int, 1),
		stop:   make(chan struct{}),
	}

	go func() {
		<-gate
		cmd := exec.Command(bin, "--workspace", workspace)
		cmd.Env = daemonEnv(runtimeDir)
		cmd.Stdout, cmd.Stderr = r.log, r.log
		if err := cmd.Start(); err != nil {
			r.log.Write([]byte("start failed: " + err.Error()))
			r.exited <- -1
			return
		}

		// Wait in its own goroutine so this one can serve a stop request while
		// the child is still running. Killing via cmd.Process from here while
		// Wait runs there is the same arrangement exec.CommandContext uses
		// internally, and is supported; reaching in from the TEST goroutine,
		// which is what the first version did, is not.
		waited := make(chan int, 1)
		go func() {
			err := cmd.Wait()
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				waited <- exit.ExitCode()
				return
			}
			waited <- 0
		}()

		select {
		case code := <-waited:
			r.exited <- code
		case <-r.stop:
			_ = cmd.Process.Kill()
			<-waited // reaped by its own goroutine; no zombie
			r.exited <- exitKilledByTest
		}
	}()

	return r
}

// exitKilledByTest marks the winner, which never exits on its own.
const exitKilledByTest = -2

func (r *racer) shutdown() {
	close(r.stop)
	<-r.exited
}

type daemonRun struct {
	code int
	log  string
}

// addressOf derives the address a daemon for this workspace binds, the same way
// a client would -- through protocol's own helpers, never a literal.
func addressOf(t *testing.T, workspace string) string {
	t.Helper()
	real, err := editapply.ResolveRealWorkspaceRoot(workspace)
	if err != nil {
		t.Fatalf("resolving %s: %v", workspace, err)
	}
	return protocol.DefaultAddressFor(real).Address
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
