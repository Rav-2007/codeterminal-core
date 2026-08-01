//go:build unix

package protocol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// RuntimeDir falls back to os.TempDir() when XDG_RUNTIME_DIR is unset, which is
// the normal state on macOS — a platform the daemon now supports. That puts the
// socket directory in a world-writable, shared /tmp, where another user can
// create our directory name before we do.
//
// os.MkdirAll accepts an existing directory without changing its mode or
// checking its owner, so every guarantee downstream (the socket's 0600, the
// lockfile's O_NOFOLLOW) would be a guarantee about a file inside a directory
// somebody else can rename things in.

func TestEnsureOwnerOnlyDir_TightensAPermissiveExistingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	if err := os.MkdirAll(dir, 0777); err != nil {
		t.Fatal(err)
	}

	if err := ensureOwnerOnlyDir(dir); err != nil {
		t.Fatalf("our own directory should be fixed in place, not refused: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0077 != 0 {
		t.Errorf("directory left at %04o; MkdirAll does not touch an existing directory's mode, so without this it stays permissive forever", perm)
	}
}

func TestEnsureOwnerOnlyDir_RefusesASymlink(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(real, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "runtime")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	err := ensureOwnerOnlyDir(link)
	if err == nil {
		t.Fatal("a symlink standing where the runtime directory should be was accepted; it must be refused rather than followed")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("refusal = %q, want it to name the symlink", err)
	}
}

func TestEnsureOwnerOnlyDir_RefusesANonDirectory(t *testing.T) {
	p := filepath.Join(t.TempDir(), "runtime")
	if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureOwnerOnlyDir(p); err == nil {
		t.Fatal("a regular file standing where the runtime directory should be was accepted")
	}
}

// The ownership check is the one that cannot be satisfied by fixing the mode:
// a directory another user owns can have its mode changed back at any moment,
// so the answer is refusal rather than a chmod. It needs a second uid to
// exercise directly, which a test cannot arrange — so this asserts the property
// that makes the check meaningful: our OWN directory passes, which is what
// stops the check being a blanket refusal nobody would notice was broken.
func TestEnsureOwnerOnlyDir_AcceptsOurOwnDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ensureOwnerOnlyDir(dir); err != nil {
		t.Fatalf("a 0700 directory owned by this user must be accepted: %v", err)
	}
}

// SocketDir must actually apply the check, not merely have it available.
func TestSocketDir_AppliesTheOwnerOnlyCheck(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", base)

	// Pre-create the service directory world-writable, exactly as a hostile or
	// careless earlier run would leave it.
	pre := filepath.Join(base, serviceDirName)
	if err := os.MkdirAll(pre, 0777); err != nil {
		t.Fatal(err)
	}

	dir, err := SocketDir()
	if err != nil {
		t.Fatalf("SocketDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0077 != 0 {
		t.Errorf("SocketDir returned a directory at %04o; the socket and lockfile inside it are only as protected as it is", perm)
	}
}
