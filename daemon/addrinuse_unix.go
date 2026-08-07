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
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}
