//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// THE TERMINAL IS PUT BACK ON EVERY EXIT PATH, PROVEN AT A REAL PTY.
//
// MEASURED before installExitSignals existed: SIGINT and SIGTERM each emitted
// 57 bytes of restore and SIGHUP emitted ZERO -- the process died with the
// alternate screen up, the cursor hidden and mouse tracking still on, leaving a
// shell the user could not see. SIGHUP is how an ssh drop and a closed window
// arrive, so that was the common case, not the exotic one.
//
// The reference for "restored" is what SIGTERM produces, rather than a byte
// literal copied into this file. A literal would be a second, silent copy of
// Bubble Tea's teardown that drifts the day the library changes its escapes;
// comparing signals against each other stays true, and the explicit checks
// below still name the five sequences that must be in it so a library change
// that dropped one is not quietly accepted.

// restoreTail launches the client, drives it to idle, sends sig, and returns
// everything the terminal received after the signal.
func restoreTail(t *testing.T, sig syscall.Signal) string {
	t.Helper()
	sc, cmd, master := launchForSignal(t)
	toIdleForSignal(t, sc, master)

	before := sc.size()
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signalling: %v", err)
	}
	waitOrFail(t, cmd, 10*time.Second, sig)
	time.Sleep(400 * time.Millisecond) // let the pty drain

	out := sc.text()
	if before > len(out) {
		return ""
	}
	return out[before:]
}

func waitOrFail(t *testing.T, cmd *exec.Cmd, d time.Duration, sig syscall.Signal) {
	t.Helper()
	done := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("the client did not exit within %v of %v -- a shutdown that hangs is as bad as one that leaks", d, sig)
	}
}

func launchForSignal(t *testing.T) (*screen, *exec.Cmd, *os.File) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a binary and starts a subprocess")
	}
	workspace := t.TempDir()
	real, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	bin := buildTUI(t)
	hostileDaemonForPTY(t, real, "an ordinary answer")

	master, slaveName := ptyPair(t)
	setWinsize(t, master, 30, 100)
	slave, err := os.OpenFile(slaveName, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "--workspace", real)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "GOTRACEBACK=all")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = slave.Close()
	sc := &screen{}
	go sc.pump(master)
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = master.Close()
	})
	return sc, cmd, master
}

func toIdleForSignal(t *testing.T, sc *screen, master *os.File) {
	t.Helper()
	if !sc.waitFor("Mochiii", 20*time.Second) {
		t.Fatalf("the client never drew. Screen:\n%q", sc.text())
	}
	_, _ = master.Write([]byte("x\x7f"))
	if !sc.waitFor(helpText, 15*time.Second) {
		t.Fatalf("never reached idle. Screen:\n%q", sc.text())
	}
}

// restoreSequences are the five the teardown must contain. Named individually
// so a failure says which one went missing.
var restoreSequences = []struct{ name, seq string }{
	{"exit alt-screen", "\x1b[?1049l"},
	{"show cursor", "\x1b[?25h"},
	{"mouse cell-motion off", "\x1b[?1002l"},
	{"mouse all-motion off", "\x1b[?1003l"},
	{"mouse SGR-encoding off", "\x1b[?1006l"},
}

func assertRestored(t *testing.T, name, tail string) {
	t.Helper()
	for _, r := range restoreSequences {
		if !strings.Contains(tail, r.seq) {
			t.Errorf("%s: the terminal was not restored -- %s (%q) was never written.\n"+
				"Bytes after the signal: %q", name, r.name, r.seq, tail)
		}
	}
}

func TestEveryExitSignalRestoresTheTerminal(t *testing.T) {
	// SIGTERM is the reference: it worked before this change and is what the
	// other signals are required to match.
	reference := restoreTail(t, syscall.SIGTERM)
	assertRestored(t, "SIGTERM", reference)
	if len(reference) != 57 {
		t.Logf("NOTE: the restore is now %d bytes, not the 57 measured when this "+
			"test was written. That is a Bubble Tea teardown change, not "+
			"necessarily a regression -- the per-sequence checks are what "+
			"decide. Update this number deliberately.", len(reference))
	}

	for _, sig := range []syscall.Signal{syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT} {
		t.Run(sig.String(), func(t *testing.T) {
			tail := restoreTail(t, sig)
			assertRestored(t, sig.String(), tail)
			if !strings.HasPrefix(tail, reference) {
				t.Errorf("%v restored differently from SIGTERM.\n got %q\nwant prefix %q", sig, tail, reference)
			}
		})
	}
}

// SIGQUIT means "dump and die". Swallowing it would take away the only tool
// someone debugging a hung client has, so the terminal is restored first and
// the goroutine stacks are printed after -- see dumpAndExit, which explains why
// re-raising the signal does not work.
func TestSIGQUITStillProducesItsGoroutineDump(t *testing.T) {
	tail := restoreTail(t, syscall.SIGQUIT)
	assertRestored(t, "SIGQUIT", tail)
	if !strings.Contains(tail, "goroutine ") {
		t.Fatalf("SIGQUIT restored the terminal but swallowed the dump. "+
			"Bytes after the signal: %q", tail)
	}
	// And the dump must come AFTER the restore, or it lands on an alt-screen
	// that is about to be torn down and the developer never sees it.
	if strings.Index(tail, "goroutine ") < strings.Index(tail, "\x1b[?1049l") {
		t.Error("the goroutine dump was written before the terminal was restored")
	}
}

// A second signal must not produce a second teardown, and must not deadlock
// against whatever the first one is holding.
func TestDoubleSIGHUPRestoresExactlyOnce(t *testing.T) {
	sc, cmd, master := launchForSignal(t)
	toIdleForSignal(t, sc, master)

	before := sc.size()
	_ = cmd.Process.Signal(syscall.SIGHUP)
	_ = cmd.Process.Signal(syscall.SIGHUP)
	_ = cmd.Process.Signal(syscall.SIGHUP)
	waitOrFail(t, cmd, 10*time.Second, syscall.SIGHUP)
	time.Sleep(400 * time.Millisecond)

	tail := sc.text()[before:]
	assertRestored(t, "triple SIGHUP", tail)
	if n := strings.Count(tail, "\x1b[?1049l"); n != 1 {
		t.Errorf("the terminal was restored %d times, want exactly 1: %q", n, tail)
	}
}

// A signal arriving before the alt-screen is even entered must still exit
// cleanly rather than blocking forever on a message loop that has not started.
func TestSIGHUPDuringStartupDoesNotHang(t *testing.T) {
	sc, cmd, _ := launchForSignal(t)
	// No wait for "Mochiii": the signal races startup on purpose.
	_ = cmd.Process.Signal(syscall.SIGHUP)
	waitOrFail(t, cmd, 15*time.Second, syscall.SIGHUP)

	// Whether anything was drawn at all is a race, so the only thing asserted
	// is what must hold either way: if the alternate screen was ever entered,
	// it was left.
	out := sc.text()
	if strings.Contains(out, "\x1b[?1049h") && !strings.Contains(out, "\x1b[?1049l") {
		t.Fatalf("entered the alternate screen and never left it: %q", out)
	}
}

// A SIGHUP that arrives when the terminal is ALREADY GONE must still exit
// promptly. The restore bytes cannot be asserted -- there is no terminal left
// to write them to, which is a fact about ptys and not a gap in the client --
// so what is required here is that the process notices and goes rather than
// waiting on a dead fd.
func TestSIGHUPAfterTheTerminalIsDestroyedStillExits(t *testing.T) {
	sc, cmd, master := launchForSignal(t)
	toIdleForSignal(t, sc, master)

	_ = master.Close() // destroys the pty
	time.Sleep(300 * time.Millisecond)
	_ = cmd.Process.Signal(syscall.SIGHUP)
	waitOrFail(t, cmd, 10*time.Second, syscall.SIGHUP)
}

// A KNOWN GAP, PINNED SO IT CANNOT CHANGE SILENTLY.
//
// Destroying the pty does not, on this kernel and in this configuration,
// deliver SIGHUP to the client at all. MEASURED with SIGABRT against the hung
// process: goroutine 1 is parked in bubbletea.(*Program).eventLoop and the
// signal goroutine is still sitting at its first select, having received
// nothing. The client therefore waits forever for messages that cannot come,
// as an invisible process its user has no terminal left to find it from.
//
// This predates the signal handling in exitsignals_unix.go and is not caused by
// it: with no signal delivered, that code never runs. Fixing it means either
// polling the tty for liveness or working around Bubble Tea's event loop, which
// is a larger decision than this change.
//
// The test asserts the CURRENT behaviour on purpose. If it starts failing, the
// gap has closed -- delete it and take the win.
func TestKnownGapClientOutlivesADestroyedPTY(t *testing.T) {
	sc, cmd, master := launchForSignal(t)
	toIdleForSignal(t, sc, master)

	_ = master.Close()
	time.Sleep(2 * time.Second)

	if !processIsRunning(t, cmd.Process.Pid) {
		t.Fatal("the client now exits when its pty is destroyed. This gap has " +
			"closed -- delete this test and the note in exitsignals_unix.go.")
	}
	t.Log("known gap: the client outlives a destroyed pty because no SIGHUP is delivered")
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	_ = sc
}

// processIsRunning distinguishes a live process from a zombie. /proc/<pid>/stat
// survives an exited-but-unreaped child, which is exactly how an earlier
// version of this measurement concluded, wrongly, that a shutdown taking 200ms
// was hanging for 15 seconds.
func processIsRunning(t *testing.T, pid int) bool {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	f := strings.Fields(string(b))
	return len(f) > 2 && f[2] != "Z"
}
