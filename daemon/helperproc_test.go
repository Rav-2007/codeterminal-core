package main

import (
	"codeterminal/protocol"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeHelperBinPath is built LAZILY, on first use, and reused
// by every test in this file — it's an on-disk binary because
// HelperProcess.Start execs a path, not an in-process function.
var fakeHelperBinPath string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "fakehelper-build")
	if err != nil {
		panic(err)
	}

	// Registered rather than deferred, and registered FIRST so it unwinds LAST
	// -- runProcessCleanups runs in reverse, the way defer would have. Routing
	// this through the same registry the eval fixtures use means there is one
	// teardown path in this package instead of two that have to agree.
	registerProcessCleanup(func() { _ = os.RemoveAll(tmpDir) })

	// THE SAME LEAK AS THE ONE DOCUMENTED BELOW, IN A SECOND DIRECTORY.
	//
	// sandboxExecHome (mcp_exec.go) derives the sandbox_exec HOME from
	// os.UserCacheDir, which on Linux resolves XDG_CACHE_HOME and otherwise
	// $HOME/.cache. Every test that reaches the sandbox_exec handler creates
	// one directory there, named from sha256(workspace) -- and a t.TempDir()
	// workspace hashes to a fresh name on every single run. Nothing removes
	// them, so each `go test ./daemon` left a permanent directory in the
	// developer's real cache: measured at 1,709 directories and 232 MB on this
	// machine over eight days, 24 of them from one `make check`.
	//
	// That is the identical shape as the fakehelper-build leak recorded below
	// (379 directories, 1.5 GB), and it wants the identical answer: keep the
	// test's writes inside the test's own temp dir. Redirecting the cache root
	// for the whole package is one line in one place, and it covers every test
	// that reaches the handler rather than each remembering to do it.
	//
	// This bounds the TESTS only. Production reclamation of that directory is a
	// separate, still-open question -- see docs/OPEN_ITEMS.md.
	cacheDir, err := os.MkdirTemp("", "daemon-test-cache")
	if err != nil {
		runProcessCleanups()
		panic("creating the test cache dir: " + err.Error())
	}
	registerProcessCleanup(func() { _ = os.RemoveAll(cacheDir) })
	os.Setenv("XDG_CACHE_HOME", cacheDir)

	fakeHelperBinPath = filepath.Join(tmpDir, exeName("fakehelper"))

	code := m.Run()

	// NOT `defer`, AND THIS IS NOT A STYLE PREFERENCE. os.Exit does not run
	// deferred functions. The `defer os.RemoveAll(tmpDir)` that used to sit
	// above the build was therefore dead code from the day it was written, and
	// every `go test ./daemon` on every machine leaked this directory --
	// measured at 379 directories and 1.5 GB on one developer machine when it
	// was finally noticed, in a session investigating something else entirely.
	//
	// Teardown that has to survive a TestMain belongs in this window, between
	// m.Run returning and os.Exit being called. runProcessCleanups is the
	// registry the eval suite's shared corpus unwinds through, for the same
	// reason; see processcleanup_test.go.
	runProcessCleanups()
	os.Exit(code)
}

// fastHelperProcess returns a HelperProcess wired to the fake helper with
// short timeouts, so lifecycle tests (including the bounded-restart one)
// run quickly instead of waiting on production-sized delays.
func fastHelperProcess(t *testing.T) *HelperProcess {
	t.Helper()
	h := NewHelperProcess(buildFakeHelperOnce(t), "", "", discardLogger())
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

	// Only where the endpoint IS a file. A Windows named pipe leaves no
	// residue when its owner dies, so there is nothing to stat and its name is
	// not a path -- asserting otherwise here is the same Unix-only assumption
	// that kept the helper from starting on Windows at all.
	if h.addr.Transport == "" || h.addr.Transport == protocol.TransportUnix {
		if _, err := os.Stat(h.addr.Address); !os.IsNotExist(err) {
			t.Fatalf("socket file %s should be removed after Stop, stat err = %v", h.addr.Address, err)
		}
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

// TestEmbedDeadlineScalesWithTheBatch pins the fix for a failure that reached
// CI on 2026-08-30: a 40-chunk index batch exceeded the 10-second call
// timeout and the run died with `i/o timeout` (run 33321602030).
//
// The timeout was not wrong for what it was written for. It was written for a
// live query, which embeds ONE short string. An index build sends
// indexEmbedBatchSize texts in a single request, which is not more urgent, it
// is more work — and newActiveEmbedder hands that same default to the real
// `index` command, so this was a product fault and not an eval-harness one.
//
// The assertion is a PAIR, in both directions, because "make the timeout
// bigger" would pass a one-sided test while destroying what the deadline is
// for. The fake helper stalls for longer than the single-call budget and less
// than the batched one, so:
//
//   - a ONE-text embed must still time out (the deadline still bounds a hang)
//   - a BATCHED embed of the same stall must succeed (it scales with the work)
//
// A fixed deadline of any size fails one of these two.
func TestEmbedDeadlineScalesWithTheBatch(t *testing.T) {
	const (
		callBudget = 300 * time.Millisecond
		perText    = 300 * time.Millisecond
		stall      = 600 * time.Millisecond // > callBudget, < callBudget+3*perText
	)

	newHelper := func(t *testing.T) *HelperProcess {
		t.Helper()
		h := NewHelperProcess(buildFakeHelperOnce(t), "", "", discardLogger())
		h.readyTimeout = 5 * time.Second
		h.readyPollStep = 10 * time.Millisecond
		h.stopGrace = 300 * time.Millisecond
		h.callTimeout = callBudget
		h.perTextTimeout = perText
		h.extraEnv = []string{"FAKEHELPER_EMBED_DELAY=" + stall.String()}
		if err := h.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { _ = h.Stop() })
		return h
	}

	t.Run("one text still times out", func(t *testing.T) {
		h := newHelper(t)
		if _, err := h.Embed(context.Background(), []string{"a"}); err == nil {
			t.Fatalf("a %s stall must exceed the %s single-call budget: the deadline has stopped "+
				"bounding a hung helper", stall, callBudget)
		}
	})

	t.Run("a batch of the same stall succeeds", func(t *testing.T) {
		h := newHelper(t)
		texts := []string{"a", "b", "c", "d"}
		vecs, err := h.Embed(context.Background(), texts)
		if err != nil {
			t.Fatalf("a %s stall is well inside the budget for %d texts (%s), so this must "+
				"succeed — the deadline is not scaling with the batch: %v",
				stall, len(texts), callBudget+3*perText, err)
		}
		if len(vecs) != len(texts) {
			t.Fatalf("got %d vectors for %d texts", len(vecs), len(texts))
		}
	})

	// A caller's SHORTER deadline needs no defending: context.WithTimeout keeps
	// whichever deadline is earlier, so it survives on its own. The case that
	// does need the `if _, ok := ctx.Deadline(); !ok` guard is a caller whose
	// deadline is LONGER than the batch budget -- without the guard, Embed
	// silently shortens it to a budget the caller never asked for. That is the
	// direction tested here, and it is the direction a one-sided test misses.
	t.Run("a caller's longer deadline is not shortened", func(t *testing.T) {
		h := newHelper(t)
		// One text, so the batch budget is the bare callBudget (300ms) — less
		// than the 600ms stall. The caller allows far more. It must be honoured.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := h.Embed(ctx, []string{"a"}); err != nil {
			t.Fatalf("the caller allowed 10s and the stall is %s, so this must succeed. "+
				"Embed has overridden a deadline its caller set: %v", stall, err)
		}
	})

	t.Run("a caller's shorter deadline still wins", func(t *testing.T) {
		h := newHelper(t)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if _, err := h.Embed(ctx, []string{"a", "b", "c", "d"}); err == nil {
			t.Fatal("a caller that set a 50ms deadline must get it, not a batch-sized one")
		}
	})
}

// TestEmbedDeadlineArithmetic covers the boundary cases the behavioural test
// above cannot reach quickly: the empty and single-text calls, which must be
// exactly the unchanged one-call budget so interactive latency is untouched.
func TestEmbedDeadlineArithmetic(t *testing.T) {
	h := NewHelperProcess("", "", "", discardLogger())
	h.callTimeout = 10 * time.Second
	h.perTextTimeout = time.Second

	for _, tc := range []struct {
		n    int
		want time.Duration
	}{
		{0, 10 * time.Second},
		{1, 10 * time.Second},
		{2, 11 * time.Second},
		{40, 49 * time.Second},
	} {
		if got := h.embedDeadline(tc.n); got != tc.want {
			t.Errorf("embedDeadline(%d) = %s, want %s", tc.n, got, tc.want)
		}
	}
}

// buildFakeHelperOnce compiles the fakehelper fixture the first time a test
// actually needs it.
//
// MEMO ITEM 4. This build used to run unconditionally in TestMain, and that made
// CI's fuzz job contribute NOTHING to four targets. Go gathers baseline coverage
// using worker processes, each a fresh exec of the test binary, so every worker
// paid the build before it could execute a single input. Measured on this tree
// at FUZZTIME=30s, the CI budget: FuzzSplitQualifiedName reported
// "gathering baseline coverage: 0/188 completed" at 38 seconds elapsed. Not
// slow -- ZERO. Nothing finished at all, in any of the four.
//
// The gate was honest about it the whole time, printing "seed corpus only, no
// new inputs generated at FUZZTIME=30s" rather than a bare ok, which is how it
// was found. Being loud is not the same as being fixed.
//
// Deferring it costs ordinary tests nothing: they pay the same single build they
// paid before, just later. A fuzz worker pays it never, because no fuzz target
// touches the helper.
//
// sync.Once rather than a nil check: TestMain no longer serialises this, so
// parallel tests can reach it at the same moment, and two concurrent `go build`
// invocations writing one output path is a corrupt binary rather than a race the
// detector would name.
func buildFakeHelperOnce(tb testing.TB) string {
	tb.Helper()
	fakeHelperBuildOnce.Do(func() {
		cmd := exec.Command("go", "build", "-o", fakeHelperBinPath, "./testdata/fakehelper")
		if out, err := cmd.CombinedOutput(); err != nil {
			fakeHelperBuildErr = fmt.Errorf("building fakehelper fixture: %w\n%s", err, out)
		}
	})
	if fakeHelperBuildErr != nil {
		tb.Fatal(fakeHelperBuildErr)
	}
	return fakeHelperBinPath
}

var (
	fakeHelperBuildOnce sync.Once
	fakeHelperBuildErr  error
)
