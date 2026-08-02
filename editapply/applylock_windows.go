//go:build windows

package editapply

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// The LockFileEx backend. Every property LockWorkspaceApply documents had to be
// reproduced with a different primitive, so each one is named against the call
// that provides it rather than assumed to carry over from the flock version.
//
// NOT RUN ON HARDWARE. This is compile-verified for windows/amd64 and reasoned
// against the Win32 documentation; no Windows machine has executed it. Treated
// the same way peercred_darwin.go was, and it stays labelled this way until CI
// has a Windows runner (launch plan Stage 2.3).

// openApplyLockFile opens the lock file, refusing a reparse point at the leaf
// rather than following it — the O_NOFOLLOW equivalent.
//
// Windows has no open flag that FAILS on a link, so the refusal is done in two
// steps that are nonetheless race-free: FILE_FLAG_OPEN_REPARSE_POINT makes
// CreateFile open the link ITSELF rather than its target, so if the path is a
// symlink or a junction the handle refers to the link and never to whatever it
// points at. The attribute check then rejects it. Nothing is followed at any
// point, so there is no window in which a swapped-in link could be traversed.
//
// This matters more on Windows than on Unix: directory junctions need no
// privilege to create, where symlinks do.
//
// OPEN_ALWAYS matches O_CREATE: open if present, create if not.
// FILE_SHARE_READ|WRITE is required so that OTHER processes can open the file at
// all — without it the second process fails at CreateFile with a sharing
// violation and never reaches the lock, turning a lock into an outage.
func openApplyLockFile(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}

	h, err := syscall.CreateFile(
		p,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		nil,
		syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}

	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		_ = syscall.CloseHandle(h)
		return nil, err
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = syscall.CloseHandle(h)
		return nil, fmt.Errorf("%s is a reparse point (symlink or junction); refusing to follow it", path)
	}

	return os.NewFile(uintptr(h), path), nil
}

// applyLockRegion is the byte range the lock covers. LockFileEx locks a RANGE
// rather than a whole file, so a fixed, agreed one-byte region at offset 0 is
// the standard idiom — every acquirer must name the identical range or they do
// not contend. The file itself stays empty; the byte need not exist.
const applyLockRegionBytes = 1

// lockFileExclusive blocks until the exclusive lock is held.
//
// LOCKFILE_EXCLUSIVE_LOCK *without* LOCKFILE_FAIL_IMMEDIATELY blocks, matching
// flock's LOCK_EX without LOCK_NB — deliberate, see LockWorkspaceApply.
//
// The lock is associated with the FILE HANDLE, which reproduces both of flock's
// load-bearing properties: Windows releases every lock held by a handle when
// that handle closes, and the kernel closes a dead process's handles, so a
// killed CLI cannot wedge the workspace; and two goroutines that each opened
// their own handle contend with each other exactly as two processes would.
func lockFileExclusive(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, // reserved, must be zero
		applyLockRegionBytes,
		0, // high-order length word
		&overlapped,
	)
}

// unlockFile releases the lock over the same range it was taken on. Closing the
// handle would release it anyway; this makes the release a stated act rather
// than a side effect.
func unlockFile(f *os.File) {
	var overlapped windows.Overlapped
	_ = windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0, // reserved, must be zero
		applyLockRegionBytes,
		0,
		&overlapped,
	)
}
