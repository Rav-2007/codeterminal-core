package editapply

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// THE APPLY ORACLE SPOKE POSIX ERRNO.
//
// Gate 7's guarantee is that Apply distinguishes its refusal states in language
// a user can act on, and that the exact wording is pinned so collapsing two
// states is visible in a diff rather than only in behaviour. That guarantee was
// being met by ACCIDENT: the wording came from whatever string the C library
// happened to attach to the errno, and on Linux those strings read well.
//
// On Windows they do not. The first Windows CI run produced, for the two states
// that reach the filesystem:
//
//	reading adir: read <workspace>\adir: Incorrect function.
//	writing unreadable.go: rename <workspace>\.mochiii-apply-… : Access is denied.
//
// "Incorrect function." is what ERROR_INVALID_FUNCTION renders as when you call
// ReadFile on a directory handle. It is not wrong; it is simply not something a
// user can do anything with, and it is not the "is a directory" the oracle
// promises. Two of the nine distinguished states were, on the platform almost
// everyone uses, indistinguishable noise.
//
// describeFileError puts the vocabulary in OUR hands rather than the platform's.
// Both classifications are made from portable predicates, not from string
// matching on the message:
//
//   - is-a-directory: asked of the filesystem with Stat, because no errno
//     answers it portably. Linux says EISDIR, Windows says
//     ERROR_INVALID_FUNCTION, and neither maps to a shared fs sentinel.
//   - permission: errors.Is(err, fs.ErrPermission), which the syscall package
//     already maps from EACCES, EPERM AND ERROR_ACCESS_DENIED
//     (syscall_windows.go's Errno.Is). This is the portable predicate that
//     exists, so it is the one used.
//
// The underlying error is still wrapped, so %w chains and errors.Is keep
// working for callers that want the cause. Only the leading, user-facing clause
// is ours.
//
// The path argument is the ABSOLUTE resolved path and is used only to ask the
// filesystem a question — it never reaches the message. relPath is what the
// caller sent and is the only path the message names, which is what keeps Gate
// 7's no-absolute-paths property intact.
func describeFileError(op, relPath, path string, err error) error {
	if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
		return &dirError{op: op, path: relPath, cause: err}
	}
	if errors.Is(err, fs.ErrPermission) {
		return &quietCause{msg: fmt.Sprintf("%s %s: permission denied", op, relPath), cause: err}
	}
	return fmt.Errorf("%s %s: %w", op, relPath, err)
}

// ErrIsDirectory is in the chain of every refusal whose path names an existing
// folder, so a caller can tell that state apart without reading the wording.
var ErrIsDirectory = errors.New("is a directory")

// dirError is an edit aimed at a folder. FOUND 2026-10-15: asked to write a
// login page "in the new_file", the model sent ~/Desktop/new_file -- an empty
// FOLDER of that name -- and was told only "is a directory ... read
// /home/<user>/Desktop/new_file: is a directory". That names the absolute path
// this file promises never to name, and gives no next step, so the model
// stopped. The message now says what to send instead.
type dirError struct {
	op, path string
	cause    error
}

func (e *dirError) Error() string {
	op := ""
	if e.op != "" {
		op = e.op + " "
	}
	return fmt.Sprintf("%s%s: is a directory, not a file -- an edit names one file. To create a file in "+
		"this folder, give the file's own path, such as %s/<name>", op, e.path, strings.TrimRight(e.path, `/\`))
}

func (e *dirError) Unwrap() []error {
	if e.cause == nil {
		return []error{ErrIsDirectory}
	}
	return []error{ErrIsDirectory, e.cause}
}

// quietCause keeps its cause for errors.Is but out of the message: an
// *os.PathError's text carries the absolute path (see describeFileError).
type quietCause struct {
	msg   string
	cause error
}

func (e *quietCause) Error() string { return e.msg }
func (e *quietCause) Unwrap() error { return e.cause }
