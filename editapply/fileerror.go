package editapply

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
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
		return fmt.Errorf("%s %s: is a directory; an edit block names a file, not a directory: %w", op, relPath, err)
	}
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%s %s: permission denied: %w", op, relPath, err)
	}
	return fmt.Errorf("%s %s: %w", op, relPath, err)
}
