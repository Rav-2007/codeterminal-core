//go:build windows

package main

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// The Windows half of the two syscall-level primitives this package needs. See
// platform.go for what each is for; the POSIX half is platform_unix.go and is
// the reference implementation.
//
// NOT RUN ON HARDWARE. Compile-verified for windows/amd64 and reasoned against
// the Win32 documentation; no Windows machine has executed it. Labelled the way
// peercred_darwin.go is, until CI has a Windows runner (launch plan Stage 2.3).

// openNoFollow opens path, refusing a reparse point at the final component
// rather than following it — the O_NOFOLLOW equivalent.
//
// Windows has no open flag that FAILS on a link, so the refusal is two steps
// that are nonetheless race-free: FILE_FLAG_OPEN_REPARSE_POINT makes CreateFile
// open the link ITSELF rather than its target, so if the leaf is a symlink or a
// junction the handle refers to the link and never to what it points at. The
// attribute check then rejects it. Nothing is followed at any point.
//
// This matters more here than on POSIX: Windows directory junctions need no
// privilege to create, where symlinks do.
//
// perm is accepted for signature parity and is otherwise unused, matching
// os.OpenFile on Windows, which maps a Unix mode onto nothing more than the
// read-only attribute. Callers relying on 0600 to mean "only this user" are
// relying on the containing directory's ACL, not on this argument — see
// SocketDir and the state directory, which is where that is enforced.
func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}

	var access uint32
	switch {
	case flag&os.O_RDWR != 0:
		access = windows.GENERIC_READ | windows.GENERIC_WRITE
	case flag&os.O_WRONLY != 0:
		access = windows.GENERIC_WRITE
	default:
		access = windows.GENERIC_READ
	}
	if flag&os.O_APPEND != 0 {
		// Real append semantics. Asking for FILE_APPEND_DATA *without*
		// GENERIC_WRITE (which contains FILE_WRITE_DATA) is what makes every
		// write land at the current end of file regardless of the file pointer
		// — the property O_APPEND provides and that five of this function's
		// callers depend on, all of them log or audit sinks where an
		// interleaved write must not overwrite an earlier one.
		access &^= windows.GENERIC_WRITE
		access |= windows.FILE_APPEND_DATA | windows.SYNCHRONIZE
	}

	var disposition uint32
	switch {
	case flag&(os.O_CREATE|os.O_EXCL) == os.O_CREATE|os.O_EXCL:
		disposition = windows.CREATE_NEW
	case flag&(os.O_CREATE|os.O_TRUNC) == os.O_CREATE|os.O_TRUNC:
		disposition = windows.CREATE_ALWAYS
	case flag&os.O_CREATE != 0:
		disposition = windows.OPEN_ALWAYS
	case flag&os.O_TRUNC != 0:
		disposition = windows.TRUNCATE_EXISTING
	default:
		disposition = windows.OPEN_EXISTING
	}

	h, err := windows.CreateFile(
		p,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		disposition,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}

	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		_ = windows.CloseHandle(h)
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(h)
		return nil, &os.PathError{
			Op:   "open",
			Path: path,
			Err:  errors.New("is a reparse point (symlink or junction); refusing to follow it"),
		}
	}

	return os.NewFile(uintptr(h), path), nil
}

// stillActive is the documented GetExitCodeProcess value for a process that has
// not exited. x/sys/windows does not export the constant, so it is named here
// rather than left as a bare literal.
const stillActive = 259

// processAlive reports whether pid refers to a still-running process.
//
// The POSIX version probes with signal 0; Windows has no such probe, so this
// opens the process and asks for its exit code. PROCESS_QUERY_LIMITED_INFORMATION
// is the least privilege that answers the question.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// killProcess terminates a process unconditionally. TerminateProcess is the
// closest Windows has to SIGKILL: it cannot be caught or handled.
func killProcess(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return windows.TerminateProcess(h, 1)
}
