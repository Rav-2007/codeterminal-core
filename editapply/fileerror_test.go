package editapply

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE FIRST VERSION OF THESE TESTS WAS VACUOUS, AND THAT IS WORTH RECORDING.
//
// It fed describeFileError the error Linux actually produces and asserted the
// result said "is a directory" / "permission denied". Both passed with the
// classifier DELETED, because on Linux the errno strings already are those
// words: EISDIR renders as "is a directory", and fs.ErrPermission's own Error()
// is literally "permission denied". The tests were measuring the C library, not
// the code under test, and would have gone green forever on the one platform
// where the bug does not exist.
//
// So both now feed a WINDOWS-SHAPED error: a message that contains none of the
// words being asserted. The only route to the right wording is the
// classification itself, on every platform. Neutered (return the plain wrap),
// both fail on Linux -- measured, not asserted.
//
// windowsAccessDenied reproduces syscall.Errno(ERROR_ACCESS_DENIED) faithfully:
// a message a user cannot act on, and an Is method that maps it to the portable
// sentinel. That mapping is the real thing -- see syscall_windows.go's
// Errno.Is, which answers true to oserror.ErrPermission for ERROR_ACCESS_DENIED,
// EACCES and EPERM alike.
type windowsAccessDenied struct{}

func (windowsAccessDenied) Error() string { return "Access is denied." }

func (windowsAccessDenied) Is(target error) bool { return target == fs.ErrPermission }

// A directory is identified by asking the filesystem, because no errno answers
// it portably: Linux says EISDIR, Windows says ERROR_INVALID_FUNCTION
// ("Incorrect function."), and neither maps to a shared fs sentinel.
func TestDescribeFileError_NamesADirectoryWhateverTheErrnoSays(t *testing.T) {
	sub := filepath.Join(t.TempDir(), "adir")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}

	// What Windows returns from ReadFile on a directory handle. Deliberately
	// contains none of the words asserted below.
	cause := errors.New("Incorrect function.")

	got := describeFileError("reading", "adir", sub, cause).Error()
	if !strings.Contains(got, "is a directory") {
		t.Errorf("describeFileError = %q, want it to say \"is a directory\".\n"+
			"the cause was %q, so the only way to know is to ask the filesystem -- "+
			"which is the whole reason this is not string-matching", got, cause)
	}
	if !strings.Contains(got, "adir") {
		t.Errorf("describeFileError = %q, want the relative path preserved for debuggability", got)
	}
}

// A permission failure is identified by errors.Is against the portable
// sentinel, which is the one predicate that spans EACCES, EPERM and
// ERROR_ACCESS_DENIED.
func TestDescribeFileError_NamesAPermissionFailureWhateverTheErrnoSays(t *testing.T) {
	cause := &os.PathError{Op: "rename", Path: `C:\ws\locked.go`, Err: windowsAccessDenied{}}
	if strings.Contains(cause.Error(), "permission denied") {
		t.Fatal("the fixture already says \"permission denied\"; this test would prove nothing")
	}

	got := describeFileError("writing", "locked.go", filepath.Join(t.TempDir(), "absent"), cause).Error()
	if !strings.Contains(got, "permission denied") {
		t.Errorf("describeFileError = %q, want it to say \"permission denied\".\n"+
			"the cause was %q -- a message a user cannot act on", got, cause)
	}
}

// The cause must survive. Callers that inspect it -- and errors.Is chains
// generally -- must not be cut off by the friendlier wording.
func TestDescribeFileError_KeepsTheCauseUnwrappable(t *testing.T) {
	cause := &os.PathError{Op: "open", Path: "/whatever", Err: fs.ErrPermission}
	wrapped := describeFileError("reading", "locked.go", filepath.Join(t.TempDir(), "absent"), cause)

	if !errors.Is(wrapped, fs.ErrPermission) {
		t.Error("describeFileError swallowed the cause; errors.Is(err, fs.ErrPermission) no longer holds")
	}
}

// A plain failure is passed through unchanged: this classifier adds vocabulary
// for two specific states and must not editorialise the rest.
func TestDescribeFileError_LeavesAnUnclassifiedErrorAlone(t *testing.T) {
	cause := errors.New("disk on fire")
	got := describeFileError("reading", "x.go", filepath.Join(t.TempDir(), "absent"), cause).Error()

	if got != "reading x.go: disk on fire" {
		t.Errorf("describeFileError on an unclassified error = %q, want %q", got, "reading x.go: disk on fire")
	}
}

// The absolute path is used to ASK the filesystem a question and must never
// reach the message -- Gate 7's no-absolute-paths property depends on the
// classifier not reintroducing what the scrubber removes.
func TestDescribeFileError_NeverNamesTheAbsolutePathItself(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "adir")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}

	got := describeFileError("reading", "adir", sub, errors.New("Incorrect function.")).Error()
	if strings.Contains(got, root) {
		t.Errorf("describeFileError = %q, which discloses the absolute path %q it was given "+
			"only in order to stat it", got, root)
	}
}
