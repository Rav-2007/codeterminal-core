//go:build !windows

package main

import (
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// These drive installExitSignals in-process, against real signals sent to the
// test binary. The pty tests prove the behaviour in the shipped binary; these
// prove the logic, and they are what a failure points at first.

func TestInstallExitSignalsIsInertWithoutASignal(t *testing.T) {
	var quits atomic.Int32
	finish := installExitSignals(func() { quits.Add(1) })
	finish()
	if n := quits.Load(); n != 0 {
		t.Fatalf("quit was called %d times with no signal sent", n)
	}
}

func TestSIGHUPReachesTheQuitFunction(t *testing.T) {
	quit := make(chan struct{}, 4)
	finish := installExitSignals(func() { quit <- struct{}{} })
	defer finish()

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("signalling self: %v", err)
	}
	select {
	case <-quit:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGHUP never reached the quit function -- the terminal would " +
			"have been left in the alternate screen")
	}
}

// However many signals arrive, the teardown must run once. Anything else is a
// double restore, or a second shutdown racing the first.
func TestRepeatedSignalsQuitExactlyOnce(t *testing.T) {
	var quits atomic.Int32
	first := make(chan struct{})
	var once atomic.Bool
	finish := installExitSignals(func() {
		quits.Add(1)
		if once.CompareAndSwap(false, true) {
			close(first)
		}
	})
	defer finish()

	for i := 0; i < 3; i++ {
		if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
			t.Fatalf("signalling self: %v", err)
		}
	}
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("no signal reached the quit function")
	}
	time.Sleep(300 * time.Millisecond) // give any second call time to happen
	if n := quits.Load(); n != 1 {
		t.Fatalf("quit was called %d times, want exactly 1", n)
	}
}

// finish must not block when a signal is in flight, and must not leave the
// listener goroutine behind (I5).
func TestFinishIsSafeWhileASignalIsBeingHandled(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	finish := installExitSignals(func() {
		close(entered)
		<-release // a quit that is still running when finish is called
	})

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("signalling self: %v", err)
	}
	<-entered

	done := make(chan struct{})
	go func() { close(release); finish(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("finish deadlocked against an in-flight shutdown")
	}
}

func TestGoroutineDumpIsUsable(t *testing.T) {
	dump := string(goroutineDump())
	if !strings.Contains(dump, "goroutine ") {
		t.Fatalf("the dump has no goroutine headers: %.200q", dump)
	}
	if !strings.Contains(dump, "TestGoroutineDumpIsUsable") {
		t.Errorf("the dump does not contain the calling goroutine: %.400q", dump)
	}
	// all=true, so the runtime's own goroutines are in it too.
	if n := strings.Count(dump, "goroutine "); n < 2 {
		t.Errorf("only %d goroutines in an all-goroutines dump", n)
	}
}

func TestIgnoreSIGPIPEInstallsAndRestores(t *testing.T) {
	restore := ignoreSIGPIPE()
	// Delivered to a process that is handling it: dropped, not fatal. Without
	// the handler this call would kill the test binary.
	if err := syscall.Kill(os.Getpid(), syscall.SIGPIPE); err != nil {
		t.Fatalf("signalling self: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	restore()
}
