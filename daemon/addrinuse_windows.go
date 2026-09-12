//go:build windows

package main

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isAddrInUse, for named pipes.
//
// NOT RUN ON HARDWARE. Compile-verified for windows/amd64 and reasoned against
// go-winio's source and the Win32 error mapping; labelled the way
// protocol/transport_windows.go and peerauth_darwin.go are.
//
// WHY THIS CANNOT BE THE UNIX ONE. syscall.EADDRINUSE exists on Windows but is
// SYNTHETIC -- the syscall package defines the POSIX errno names above
// APPLICATION_ERROR (1<<29) so that portable code compiles, and no real Win32
// call ever returns one. errors.Is(err, syscall.EADDRINUSE) is therefore
// reachable, always false, and silently wrong: the concurrent loser would exit 1
// and burn a restart attempt on a daemon that was never broken. A seam, not a
// shared line.
//
// WHAT A PIPE COLLISION ACTUALLY RETURNS. go-winio creates the first instance
// with NtCreateNamedPipeFile disposition FILE_CREATE (pipe.go:378-381), the
// NT-level "create new, fail if it exists". A name already taken fails the
// STATUS, which RtlNtStatusToDosError maps into the DOS space:
//
//   - ERROR_ALREADY_EXISTS   -- STATUS_OBJECT_NAME_COLLISION, the ordinary case:
//     our own other daemon holds the name.
//   - ERROR_ACCESS_DENIED    -- the name is held by a process whose pipe DACL
//     excludes us. Another USER's daemon, or a squatter. Both mean "not
//     available to this process", which is the question being asked here.
//   - ERROR_PIPE_BUSY        -- every instance is in use. Reachable only against
//     an instance-limited pipe; ours is not, and it is listed because it is the
//     same condition wearing a third code.
//
// ERROR_ACCESS_DENIED is the uncomfortable one, because a squatted name is a
// security event and this treats it as a benign "someone else got here first".
// That is the RIGHT call for this function and the wrong place to fix it: the
// supervisor's response is to probe, and the probe performs a full protocol
// handshake against whatever holds the name. A squatter fails it, the adoption
// does not happen, and the exit is reported. Confusing the two here would only
// downgrade a real conflict into a restart loop.
//
// WSAEADDRINUSE is included for the TCP transport, which is not reachable in
// production (see protocol/transport.go) but is constructible in tests.
func isAddrInUse(err error) bool {
	for _, target := range []error{
		windows.ERROR_ALREADY_EXISTS,
		windows.ERROR_ACCESS_DENIED,
		windows.ERROR_PIPE_BUSY,
		windows.WSAEADDRINUSE,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
