//go:build trace && unix

package tuitrace

import (
	"os"
	"syscall"
)

// openNoFollow opens path with O_NOFOLLOW ORed into flag, so a symlink at the
// final component is refused (ELOOP) rather than followed, in the open(2) itself:
// there is no stat-then-open window. The daemon's openNoFollow
// (daemon/platform_unix.go) is the same line.
func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flag|syscall.O_NOFOLLOW, perm)
}
