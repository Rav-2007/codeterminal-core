//go:build unix

package editapply

import (
	"os"
	"syscall"
)

// The flock backend. This is the original implementation and the reference the
// Windows one had to reproduce; the four properties LockWorkspaceApply depends
// on are noted against the calls that provide them.

// openApplyLockFile opens the lock file, refusing a symlink at the leaf rather
// than following it.
//
// O_NOFOLLOW makes open(2) itself fail with ELOOP when the final component is a
// symlink, so the refusal is atomic — there is no stat-then-open window for
// something to be swapped into.
func openApplyLockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
}

// lockFileExclusive blocks until the exclusive lock is held.
//
// flock(2) is held on the OPEN FILE DESCRIPTION, not the process, which gives
// two of the properties LockWorkspaceApply relies on at once: the kernel drops
// it when the descriptor closes — including on process death, so a SIGKILLed CLI
// cannot wedge the workspace — and two goroutines in one process that each
// opened their own descriptor contend with each other exactly as two processes
// would.
//
// LOCK_EX without LOCK_NB blocks, which is deliberate: see LockWorkspaceApply.
func lockFileExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// unlockFile releases the lock. Closing the descriptor would release it anyway;
// this makes the release a stated act rather than a side effect.
func unlockFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
