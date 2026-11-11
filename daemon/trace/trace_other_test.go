//go:build trace && !unix

package trace

import "os"

// openNoFollowForTest on a platform without O_NOFOLLOW: a plain open. The
// symlink and mode tests are unix-only (trace_unix_test.go); this exists so the
// package's tests compile everywhere.
func openNoFollowForTest(path string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flag, perm)
}
