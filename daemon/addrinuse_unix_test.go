//go:build unix

package main

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// isAddrInUse decides whether a concurrent startup loser exits 3 (benign,
// adopt the winner) or 1 (broken, report it). Getting it wrong in either
// direction is costly, so both directions are asserted.
//
// Driven through a REAL net.Listen collision rather than a hand-built
// syscall.Errno. The value this function receives in production is wrapped
// twice -- *net.OpError around *os.SyscallError around syscall.Errno -- and a
// hand-built errno would test errors.Is against a shape the code never actually
// sees. The wrapping is the entire reason this is errors.Is and not ==.
func TestIsAddrInUse_TrueForARealBindCollision(t *testing.T) {
	sock := filepath.Join(shortRuntimeDir(t), "s.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("first listen: %v", err)
	}
	defer ln.Close()

	_, err = net.Listen("unix", sock)
	if err == nil {
		t.Fatal("second listen on the same path SUCCEEDED; this platform does not have the " +
			"collision this test is about, and the daemon's race arbitration rests on it")
	}
	if !isAddrInUse(err) {
		t.Errorf("isAddrInUse(%#v) = false, want true.\n"+
			"A daemon losing a concurrent startup race would exit %d instead of %d, and the "+
			"supervisor would count an ordinary two-window startup against its restart budget.",
			err, exitFailure, exitAlreadyRunning)
	}
}

// The other direction, and the one that matters more: a genuinely broken daemon
// must NOT be mistaken for a race loser. A supervisor told "someone else got
// there first" adopts and stays quiet -- so a false positive here turns a real,
// reportable failure into silence, with no daemon and no message.
func TestIsAddrInUse_FalseForOtherListenFailures(t *testing.T) {
	dir := shortRuntimeDir(t)

	// A path whose parent does not exist: ENOENT, not EADDRINUSE.
	_, err := net.Listen("unix", filepath.Join(dir, "no-such-dir", "s.sock"))
	if err == nil {
		t.Fatal("listening under a nonexistent directory succeeded")
	}
	if isAddrInUse(err) {
		t.Errorf("isAddrInUse(%#v) = true for a missing directory; a broken daemon would be "+
			"silently adopted-away instead of reported", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Logf("note: the control error was %v, not ENOENT; the assertion above still holds", err)
	}

	// A directory this process cannot write: EACCES.
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0500); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() == 0 {
		t.Skip("running as root, which ignores the 0500 mode this half depends on")
	}
	if _, err := net.Listen("unix", filepath.Join(locked, "s.sock")); err == nil {
		t.Error("listening in a mode-0500 directory succeeded")
	} else if isAddrInUse(err) {
		t.Errorf("isAddrInUse(%#v) = true for a permission failure; a daemon that cannot "+
			"create its socket at all would be reported as a benign race loss", err)
	}
}
