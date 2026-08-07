//go:build unix

package main

import (
	"errors"
	"syscall"
)

// isAddrInUse reports whether a listen failed because something else already
// holds the address -- i.e. this daemon lost a concurrent startup race.
//
// errors.Is rather than a string match on "address already in use": the text is
// the operating system's and is localised on some platforms, while the errno is
// the actual contract. net.Listen wraps it twice (*net.OpError around
// *os.SyscallError around syscall.Errno), which is exactly what errors.Is
// unwraps.
//
// TWO ERRNOS, BECAUSE UNIX IS NOT ONE KERNEL.
//
// Binding an AF_UNIX socket to a path that already exists is EADDRINUSE on
// Linux and EEXIST on macOS and the BSDs. This checked only EADDRINUSE, so on
// macOS every daemon that lost a concurrent startup race fell through to
// logger.Fatalf and exited 1 -- the generic "this daemon is broken" code -- when
// it had simply arrived second.
//
// That defeats the whole exit-code contract on that platform. The supervisor
// charges a 1 against its restart budget, backs off, retries, and after five
// attempts tells the user the daemon "will not be restarted again" about a
// daemon that was never broken. Opening a second window on one repository is
// enough to trigger it. See exitcodes.go for why the two cases have to be
// distinguishable at all.
//
// Measured on the first macOS run that got this far, from the daemon's own log:
//
//	listening on .../daemon-eee526fc925362b0.sock: listen unix ...:
//	bind: file exists
//
// EEXIST IS NOT TOO BROAD HERE, and the reason is the order of operations
// rather than the errno's general meaning. reclaimStaleSocket has already run
// by this point and has already removed a stale socket file, so a path that
// exists NOW appeared between that probe and this bind -- which is exactly what
// losing the race looks like. A non-socket file left at the address would also
// land here, and a supervisor would then probe, find nothing, wait out the
// adoption window and retry: slower than ideal, and still better than the
// current macOS behaviour, which is wrong for the common case rather than the
// rare one.
//
// This is the third instance of one mistake in a single week, all found by the
// same runner: sun_path is 108 bytes on Linux and 104 on macOS, a unix socket's
// default buffer is ~200 KiB on Linux and 8 KiB on macOS, and now this. A
// per-kernel constant is not a Unix constant, however many years it has been
// correct on the only kernel anyone ran it on.
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, syscall.EEXIST)
}
