package protocol

import (
	"os"
	"testing"
)

// shortTempDir returns a temporary directory short enough that a Unix socket
// created inside it fits in sockaddr_un.
//
// NEITHER t.TempDir() NOR os.MkdirTemp("", ...) IS SHORT ENOUGH, and the reason
// is the BASE, not the leaf. Both honour $TMPDIR, and on macOS that is a
// per-session path shaped like
//
//	/var/folders/k1/2b3c4d5e6f7g8h9i0j0k0l0m/T/
//
// — roughly 49 bytes before a test name is even appended. sun_path is 104 bytes
// there (103 usable; see protocol.checkSocketPathLength), so a socket beneath it
// does not fit and bind(2) answers EINVAL.
//
// MEASURED, on the first macOS run this project has ever done (CI run
// 31168478415): 15 tests failed across daemon, protocol and clients/tui with
// socket paths of 106-132 bytes. Every one was a FIXTURE fault, not a product
// fault — the length guard was correctly reporting a real macOS limit that
// Linux's 108-byte sun_path and short /tmp had hidden.
//
// Two earlier attempts at this shortened the wrong end and are superseded by
// this one: protocol's own shortTempDir called os.MkdirTemp("", ...) while its
// doc comment named "a long TMPDIR" as the hazard, and testAddress chose the
// filename "d.sock" over "daemon.sock" to save six bytes. Shortening the leaf
// cannot help when the base alone is half the budget.
//
// /tmp is the shortest base guaranteed present and writable on every Unix,
// macOS included — there it is a symlink to /private/tmp, which does not matter
// because bind(2) stores the path AS GIVEN, so the four-byte spelling is what
// counts against the limit.
//
// Falls back rather than failing, and is deliberately not build-tagged so that
// callers which compile on every platform can use it: on Windows /tmp does not
// exist, t.TempDir() is returned, and a named pipe has no path-length limit for
// it to violate.
//
// MIRRORED in daemon/, protocol/ and clients/tui/. Go test helpers do not cross
// module boundaries, so the duplication is deliberate and labelled, exactly as
// testAddress and the confinement vector tables already are.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ct")
	if err != nil {
		return t.TempDir()
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
