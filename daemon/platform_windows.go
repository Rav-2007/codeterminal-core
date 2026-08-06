//go:build windows

package main

import (
	"errors"
	"fmt"
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
// WHY NO SUPERSEDING DISPOSITION. The obvious mapping for O_CREATE|O_TRUNC is
// CREATE_ALWAYS, and it is wrong here: on an existing reparse point,
// CREATE_ALWAYS *replaces the link with a fresh regular file* rather than
// opening it. The handle then has no FILE_ATTRIBUTE_REPARSE_POINT, the check
// below passes, and the call SUCCEEDS. Nothing is followed and no victim is
// touched -- but the contract is "refuse", and Windows silently clobbered
// instead. Measured, not reasoned: on the first Windows CI run both
// TestWriteEmbedderStamp_RefusesSymlink and TestWriteExtractedFile_RefusesSymlink
// failed their refusal assertion while PASSING their victim-untouched assertion,
// which is that behaviour exactly.
//
// So a truncating open is split into two steps that no disposition can
// short-circuit: open NON-destructively (OPEN_ALWAYS / OPEN_EXISTING, neither of
// which can destroy a reparse point), check the attribute on the handle we are
// actually holding, and only then truncate with SetEndOfFile. That is also what
// POSIX does -- O_NOFOLLOW is evaluated before O_TRUNC takes effect -- so the
// two platforms now refuse in the same order.
//
// There is no TOCTOU here, because there is no window: the attribute is read
// from the handle, not from the path. A link planted between resolution and
// CreateFile is opened AS the link and rejected.
//
// O_APPEND|O_TRUNC is the one combination this cannot serve: append strips
// GENERIC_WRITE (see below) and SetEndOfFile needs FILE_WRITE_DATA. No caller
// combines them -- verified across all nine call sites -- and if one ever does
// it fails loudly with ACCESS_DENIED rather than quietly not truncating.
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

	// truncate is deferred to after the reparse-point check; see the note above
	// on why no disposition here may be a superseding one.
	var disposition uint32
	truncate := false
	switch {
	case flag&(os.O_CREATE|os.O_EXCL) == os.O_CREATE|os.O_EXCL:
		// CREATE_NEW already fails on an existing link, so it needs no help.
		disposition = windows.CREATE_NEW
	case flag&(os.O_CREATE|os.O_TRUNC) == os.O_CREATE|os.O_TRUNC:
		disposition = windows.OPEN_ALWAYS
		truncate = true
	case flag&os.O_CREATE != 0:
		disposition = windows.OPEN_ALWAYS
	case flag&os.O_TRUNC != 0:
		disposition = windows.OPEN_EXISTING
		truncate = true
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

	// Only now, on a handle proven to refer to a real file. SetEndOfFile cuts at
	// the current file pointer, which CreateFile leaves at 0.
	if truncate {
		if err := windows.SetEndOfFile(h); err != nil {
			_ = windows.CloseHandle(h)
			return nil, &os.PathError{Op: "truncate", Path: path, Err: err}
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

// openControllingTerminal opens this process's console for reading prompts when
// stdin has already been consumed by something else.
//
// There is no /dev/tty here, and the POSIX path was not a portability wart that
// merely failed to compile -- it compiled fine and failed at RUNTIME, on every
// Windows machine, for every invocation of `edits apply -`. The user got
// "confirmation prompts need a controlling terminal, but none is available",
// which is a true sentence about the wrong thing: there was a console, we asked
// for it by a name this OS has never had.
//
// CONIN$ is the console's own name for its input buffer, and it is the exact
// analogue: it resolves to the attached console regardless of what stdin was
// redirected from, and it fails when a process has no console at all -- which
// is the case the caller's message is actually for.
//
// O_RDWR matches the POSIX side. CONIN$ requires GENERIC_READ|GENERIC_WRITE to
// open even for reading, because a console handle is bidirectional.
func openControllingTerminal() (*os.File, error) {
	return os.OpenFile("CONIN$", os.O_RDWR, 0)
}

// restrictToOwner makes path readable by its owner and nobody else.
//
// perm is ignored, and that is the whole point. os.Chmod on Windows maps a Unix
// mode onto FILE_ATTRIBUTE_READONLY and nothing else, and os.Stat maps it back
// out as a flat 0666 or 0777 — so the five os.Chmod(…, 0600) calls this
// replaces were silent no-ops here, and the tests asserting them were reading a
// constant. The files in question hold indexed source text, conversation
// transcripts and the daemon's log.
//
// Access control on Windows is the DACL, so that is what gets set:
//
//   - ONE explicit ACE, GENERIC_ALL, to the current user's SID. The SID comes
//     from the process token's User, not its Owner: on an elevated token the
//     owner is BUILTIN\Administrators, and granting that would grant every
//     administrator on the machine where the point is one individual. Exactly
//     the asymmetry protocol/transport_windows.go documents for the pipe.
//   - PROTECTED_DACL_SECURITY_INFORMATION, so inherited ACEs from the parent
//     directory are DROPPED rather than merged. Without it the workspace's own
//     permissive inheritance survives and the restriction is decorative.
//   - SUB_CONTAINERS_AND_OBJECTS_INHERIT on directories, so files SQLite
//     creates later (the -wal and -shm sidecars, which in WAL mode hold
//     committed rows the main database does not have yet) are born restricted.
//
// SYSTEM and Administrators can still take ownership and read anything, exactly
// as root can on POSIX. That is the same boundary, not a weaker one.
func restrictToOwner(path string, _ os.FileMode) error {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("determining this user's SID to restrict %s: %w", path, err)
	}

	inheritance := uint32(windows.NO_INHERITANCE)
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}

	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}, nil)
	if err != nil {
		return fmt.Errorf("building the owner-only ACL for %s: %w", path, err)
	}

	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil,
	)
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
