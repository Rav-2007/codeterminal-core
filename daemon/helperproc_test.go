package main

import (
	"context"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// fakeHelperBinPath is built once (by TestMain) into a temp dir and reused
// by every test in this file — it's an on-disk binary because
// HelperProcess.Start execs a path, not an in-process function.
var fakeHelperBinPath string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "fakehelper-build")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmpDir)

	fakeHelperBinPath = filepath.Join(tmpDir, exeName("fakehelper"))
	cmd := exec.Command("go", "build", "-o", fakeHelperBinPath, "./testdata/fakehelper")
	if out, err := cmd.CombinedOutput(); err != nil {
		panic("building fakehelper fixture: " + err.Error() + "\n" + string(out))
	}

	os.Exit(m.Run())
}

// fastHelperProcess returns a HelperProcess wired to the fake helper with
// short timeouts, so lifecycle tests (including the bounded-restart one)
// run quickly instead of waiting on production-sized delays.
func fastHelperProcess(t *testing.T) *HelperProcess {
	t.Helper()
	h := NewHelperProcess(fakeHelperBinPath, "", "", discardLogger())
	h.maxRestarts = 2
	h.restartDelay = 20 * time.Millisecond
	h.readyTimeout = 500 * time.Millisecond
	h.readyPollStep = 10 * time.Millisecond
	h.stopGrace = 300 * time.Millisecond
	h.callTimeout = 2 * time.Second
	return h
}

func TestHelperProcess_SpawnReadyCallResponse(t *testing.T) {
	h := fastHelperProcess(t)
	if err := h.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer h.Stop()

	if err := h.Health(context.Background()); err != nil {
		t.Fatalf("Health after Start should succeed: %v", err)
	}

	vecs, err := h.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("got %d vectors, want 2", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != 384 {
			t.Errorf("vector %d length = %d, want 384", i, len(v))
		}
	}
}

func TestHelperProcess_KillDetectedAndRestarted(t *testing.T) {
	h := fastHelperProcess(t)
	if err := h.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer h.Stop()

	h.mu.Lock()
	oldPID := h.cmd.Process.Pid
	h.mu.Unlock()

	if err := killProcess(oldPID); err != nil {
		t.Fatalf("killing helper pid %d: %v", oldPID, err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := h.Health(context.Background()); err == nil {
			lastErr = nil
			break
		} else {
			lastErr = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("helper never became healthy again after being killed: %v", lastErr)
	}

	h.mu.Lock()
	newPID := h.cmd.Process.Pid
	h.mu.Unlock()
	if newPID == oldPID {
		t.Fatal("expected a new PID after restart, got the same one")
	}
	if h.Restarts() < 1 {
		t.Fatalf("Restarts() = %d, want >= 1", h.Restarts())
	}
}

func TestHelperProcess_RestartPolicyIsBounded(t *testing.T) {
	h := fastHelperProcess(t)
	if err := h.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer h.Stop()

	h.mu.Lock()
	pid := h.cmd.Process.Pid
	h.mu.Unlock()

	// From here on, every respawn attempt will exit immediately without
	// becoming healthy: FAKEHELPER_FAIL only affects spawnLocked's *next*
	// invocation, which we can't mutate on an already-running exec.Cmd — so
	// instead we set it via h.extraEnv (appended to the real helper's
	// minimal env allowlist in spawnLocked; see HelperProcess.extraEnv)
	// before killing the current instance, and every subsequent respawn
	// picks it up.
	h.mu.Lock()
	h.extraEnv = []string{"FAKEHELPER_FAIL=1"}
	h.mu.Unlock()

	if err := killProcess(pid); err != nil {
		t.Fatalf("killing helper pid %d: %v", pid, err)
	}

	// Give the monitor generous time to exhaust its bounded retries. This
	// must complete well before it would if retries were unbounded (the
	// test would simply never reach here) — that's the actual assertion:
	// the goroutine gives up rather than hot-looping forever.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && h.Restarts() <= h.maxRestarts {
		time.Sleep(20 * time.Millisecond)
	}

	if h.Restarts() != h.maxRestarts+1 {
		t.Fatalf("Restarts() = %d, want exactly %d (initial failure + %d bounded retries, then stop)", h.Restarts(), h.maxRestarts+1, h.maxRestarts)
	}

	// Give it a further moment to prove it really has stopped trying, not
	// just paused.
	time.Sleep(200 * time.Millisecond)
	if h.Restarts() != h.maxRestarts+1 {
		t.Fatalf("Restarts() kept climbing to %d after the bound was reached; restart policy is not actually bounded", h.Restarts())
	}

	if err := h.Health(context.Background()); err == nil {
		t.Fatal("expected Health to fail once restarts are exhausted and the helper can never come back up")
	}
}

func TestHelperProcess_StopLeavesNoOrphanProcess(t *testing.T) {
	h := fastHelperProcess(t)
	if err := h.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	h.mu.Lock()
	pid := h.cmd.Process.Pid
	h.mu.Unlock()

	if !processAlive(pid) {
		t.Fatalf("helper pid %d should be alive right after Start", pid)
	}

	if err := h.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if processAlive(pid) {
		t.Fatalf("helper pid %d is still alive after Stop; orphaned process", pid)
	}

	if _, err := os.Stat(h.socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket file %s should be removed after Stop, stat err = %v", h.socketPath, err)
	}
}

func TestHelperProcess_StopIsIdempotent(t *testing.T) {
	h := fastHelperProcess(t)
	if err := h.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := h.Stop(); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := h.Stop(); err != nil {
		t.Fatalf("second Stop should be a safe no-op, got: %v", err)
	}
}

// discardLogger returns a logger that writes nowhere, keeping test output
// focused on t.Log/t.Error rather than the helper's own operational chatter.
func discardLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

// A helper that never becomes ready must not hang daemon shutdown.
//
// Start creates doneCh only after waitReady succeeds, so the failure path used
// to return with h.cmd set and h.doneCh nil. Stop's "never started" guard is
// h.cmd == nil, so it would sail past, SIGTERM an already-reaped process, wait
// out stopGrace, and then receive on a nil channel — a permanent block, on the
// shutdown path, triggered by nothing more exotic than a helper binary that
// cannot start.
//
// The timeout is the assertion. Without the fix this does not fail slowly, it
// does not return at all.
func TestHelperProcess_StopAfterFailedStartDoesNotHang(t *testing.T) {
	h := fastHelperProcess(t)
	h.extraEnv = []string{"FAKEHELPER_FAIL=1"}

	if err := h.Start(); err == nil {
		t.Fatal("Start should fail: FAKEHELPER_FAIL makes the helper exit before it can ever report healthy")
	}

	stopped := make(chan error, 1)
	go func() { stopped <- h.Stop() }()

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop after a failed Start: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after a failed Start — it is blocked on a nil doneCh, and this is the daemon's shutdown path")
	}

	// Stop must stay a no-op rather than becoming one only the first time.
	if err := h.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}
