//go:build trace && !unix

package tuitrace

import (
	"errors"
	"os"
)

// openNoFollow on a platform without O_NOFOLLOW (Windows). The daemon's version
// there opens with FILE_FLAG_OPEN_REPARSE_POINT and is long; this client's trace is
// a development aid and the traced build is not shipped, so this is the smaller
// check, and weaker, and says so: refuse a leaf that is already a symlink or other
// non-regular file, open it, then require that what was opened is the file that
// was looked at. A link planted between the two checks is the window the daemon's
// version does not have.
func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return nil, &os.PathError{Op: "open", Path: path, Err: errors.New("not a regular file (a link is refused)")}
	}
	f, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if after, err := os.Lstat(path); err != nil || !os.SameFile(opened, after) || !after.Mode().IsRegular() {
		_ = f.Close()
		return nil, &os.PathError{Op: "open", Path: path, Err: errors.New("changed while it was opened (a link is refused)")}
	}
	return f, nil
}
