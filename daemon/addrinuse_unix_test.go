//go:build unix

package main

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
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

// BOTH ERRNOS, PINNED DIRECTLY, because the real-collision test above cannot
// reach the second one.
//
// Binding an AF_UNIX socket over an existing path is EADDRINUSE on Linux and
// EEXIST on macOS and the BSDs. isAddrInUse checked only the first, so on macOS
// a daemon that lost a concurrent startup race exited 1 -- "this daemon is
// broken" -- instead of 3, and the supervisor charged an ordinary two-window
// startup against its restart budget. From the daemon's own log on the first
// macOS run that got this far:
//
//	listening on .../daemon-eee526fc925362b0.sock: listen unix ...:
//	bind: file exists
//
// WHY THIS IS HAND-BUILT, which is the opposite of the test above and needs
// saying. TestIsAddrInUse_TrueForARealBindCollision drives a real collision
// precisely so the WRAPPING is real, and that is still the right way to test the
// common path. But it PASSED on the macOS runner in the same job where the
// startup race failed with EEXIST -- so whatever the macOS kernel returns for
// two listens on one path in one process, it is not what a losing racer gets,
// and that test therefore cannot defend this property on the platform that
// needs it.
//
// Why the two differ on macOS is NOT established here. What is established is
// that one of them produces EEXIST in production, so the contract is asserted
// against both errnos on every platform rather than left to whichever one the
// local kernel happens to raise. A hand-built error is the weaker form of
// evidence and it is used deliberately, for a property a real collision was
// observed not to reach.
//
// The wrapping mirrors what net.Listen actually produces: *net.OpError around
// *os.SyscallError around syscall.Errno. Nothing here is asserting on a bare
// errno the code never sees.
func TestIsAddrInUse_AcceptsBothPlatformsErrno(t *testing.T) {
	for _, tc := range []struct {
		name  string
		errno syscall.Errno
		where string
	}{
		{"EADDRINUSE", syscall.EADDRINUSE, "Linux"},
		{"EEXIST", syscall.EEXIST, "macOS and the BSDs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := &net.OpError{
				Op:  "listen",
				Net: "unix",
				Err: os.NewSyscallError("bind", tc.errno),
			}
			if !isAddrInUse(err) {
				t.Errorf("isAddrInUse(%v) = false, but that is what a losing racer's bind "+
					"returns on %s. Every daemon there would exit %d instead of %d, so the "+
					"supervisor counts an ordinary second window against its restart budget and "+
					"eventually reports a daemon that was never broken.",
					err, tc.where, exitFailure, exitAlreadyRunning)
			}
		})
	}
}

// And the guard that keeps the widening honest: EEXIST is accepted at the BIND,
// where reclaimStaleSocket has already cleared a stale socket, so a path that
// exists appeared during the race. It must not swallow unrelated failures.
func TestIsAddrInUse_StillRejectsUnrelatedErrnos(t *testing.T) {
	for _, errno := range []syscall.Errno{
		syscall.ENOENT, syscall.EACCES, syscall.ENOTDIR, syscall.ENAMETOOLONG,
	} {
		err := &net.OpError{Op: "listen", Net: "unix", Err: os.NewSyscallError("bind", errno)}
		if isAddrInUse(err) {
			t.Errorf("isAddrInUse(%v) = true; a genuinely broken daemon would be silently "+
				"adopted-away instead of reported, which is the costlier direction", err)
		}
	}
}
