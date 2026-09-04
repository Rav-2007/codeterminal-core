//go:build !windows

package main

import (
	"os"
	"os/signal"
	"runtime"
	"sync/atomic"
	"syscall"
)

// EXIT SIGNALS BUBBLE TEA DOES NOT HANDLE.
//
// Bubble Tea v1.3.4 registers SIGINT and SIGTERM and nothing else (tea.go:278).
// MEASURED at a real pty before this file existed: SIGINT and SIGTERM each emit
// 57 bytes of restore -- exit alt-screen (?1049l), show cursor (?25h), mouse
// tracking off (?1002l, ?1003l, ?1006l) -- and SIGHUP emitted ZERO. The process
// died with the alternate screen still up, the cursor hidden and the terminal
// still reporting mouse motion, leaving a shell the user could not see.
//
// SIGHUP is not an exotic signal for this program. It is what arrives when an
// ssh session drops and when a terminal window is closed -- the ordinary ways a
// long-lived terminal client actually ends.
//
// WRAPPED, NOT FORKED. Nothing here reimplements the restore: every signal is
// funnelled into the same graceful shutdown Bubble Tea already performs for
// SIGTERM, so there is exactly one code path that puts a terminal back and it
// is the library's own. Measured from signal to exit: about 200ms with the
// terminal alive, and the same with the pty already destroyed.

// installExitSignals routes SIGHUP and SIGQUIT into quit, and returns a
// function to call once Run has returned.
//
// SAFE TO CALL LATE, AND SAFE TO SIGNAL TWICE, both by construction rather than
// by a mutex:
//
//   - Program.Quit is Program.Send, which selects on the program's context.
//     shutdown cancels that context, so a Quit arriving during a panic unwind,
//     or after Run has already returned, takes the cancelled branch and returns
//     immediately. It cannot deadlock against whatever the restore is holding.
//   - The goroutine reads at most ONE signal and then stops listening. Further
//     signals land in the buffer and are never acted on, so the teardown runs
//     exactly once however many times a user leans on the key. Verified at a
//     pty: three SIGHUPs in a row produce exactly one restore sequence.
//   - A signal arriving before Run has started is not lost. The send blocks
//     until the message loop begins and then quits it, which is why the
//     startup race exits cleanly rather than hanging.
//
// There is deliberately NO watchdog here. One was written, on the strength of a
// measurement that turned out to be a broken diagnostic -- /proc/<pid>/stat
// survives a zombie, so an unreaped process read as "still running" and the
// graceful path looked like it was hanging when it had already finished in
// 200ms. Re-measured against process state, both the live-terminal and
// dead-terminal cases exit promptly, so the timeout-and-os.Exit ladder was
// guarding a failure that does not occur. It is not here because nothing
// measured asks for it.
func installExitSignals(quit func()) (finish func()) {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGHUP, syscall.SIGQUIT)

	ran := make(chan struct{}) // closed by finish: Run has returned
	fin := make(chan struct{})
	var caught atomic.Int32

	go func() {
		defer close(fin)
		select {
		case s := <-ch:
			if sig, ok := s.(syscall.Signal); ok {
				caught.Store(int32(sig))
			}
			quit()
		case <-ran:
		}
	}()

	return func() {
		signal.Stop(ch)
		close(ran)
		<-fin // no goroutine outlives the session (I5)

		if sig := syscall.Signal(caught.Load()); sig == syscall.SIGQUIT {
			dumpAndExit(sig)
		}
	}
}

// dumpAndExit gives SIGQUIT what it asked for, AFTER the terminal has been put
// back. Swallowing it would take away the one tool someone debugging a hung
// client has; taking it without restoring first would print the dump onto an
// alternate screen that is about to disappear.
//
// THE STACKS ARE PRINTED HERE RATHER THAN BY RE-RAISING, and that is not the
// obvious choice, so: re-raising was tried first and MEASURED not to work.
// signal.Reset(SIGQUIT) followed by Kill(self, SIGQUIT) returned a nil error
// and produced nothing -- the process exited 0, silently, because the Go
// runtime does not hand the disposition back in a way a later kill can use.
// Printing the stacks directly is what actually delivers the dump.
//
// This is not byte-identical to the runtime's own crash output: it carries
// every goroutine's stack, which is the part a person debugging a hang is
// after, and not the register state.
func dumpAndExit(sig syscall.Signal) {
	_, _ = os.Stderr.Write(goroutineDump())
	// 128+n is what a shell reports for a process killed by signal n, which is
	// what the caller asked for and would otherwise have received.
	os.Exit(128 + int(sig))
}

// goroutineDump returns every goroutine's stack, growing the buffer until they
// fit. Separated from dumpAndExit so it can be tested: the only thing left in
// that function is a write and an exit, neither of which a test can survive.
func goroutineDump() []byte {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return buf[:n]
		}
		if len(buf) >= 1<<24 { // 16MB of stacks is already unreadable
			return buf
		}
		buf = make([]byte, 2*len(buf))
	}
}

// ignoreSIGPIPE stops the Go runtime from killing this process when it writes
// to a stdout whose reader has gone away, and returns a function to undo it.
//
// THE DECISION, since a handler is not obviously right: the chat UI does NOT
// install this. It owns a terminal, its stdout is a pty, and there is no
// broken-pipe failure for a handler to prevent -- adding one would be a
// mechanism guarding nothing.
//
// One-shot mode does, because `codeterminal-tui --prompt ... | head` is an
// ordinary thing to type and MEASURED, before this existed, it exited 141:
// killed by SIGPIPE. The reader closing the pipe early is normal pipeline
// behaviour, not a failure of ours, so the correct answer is a quiet exit 0 --
// see the broken-pipe handling in oneshot.go, which needs the write to return
// EPIPE rather than the process to be shot.
func ignoreSIGPIPE() (restore func()) {
	// Buffered and never drained on purpose: os/signal drops signals it cannot
	// deliver, and "not dying" is the entire effect being asked for here.
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGPIPE)
	return func() { signal.Stop(ch) }
}
