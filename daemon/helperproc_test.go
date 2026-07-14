package main

import (
	"context"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
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

	fakeHelperBinPath = filepath.Join(tmpDir, "fakehelper")
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

	if err := syscall.Kill(oldPID, syscall.SIGKILL); err != nil {
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

	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
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
