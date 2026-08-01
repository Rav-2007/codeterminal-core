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

// The refusal must PROPAGATE. A check that runs and whose verdict is discarded
// is worse than no check, because it reads as protection.
func TestSocketDir_PropagatesTheRefusal(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", base)

	// A symlink standing where the service directory should be. MkdirAll
	// SUCCEEDS against this (it follows the link to a real directory), so
	// nothing but the explicit check can catch it — which is the point.
	elsewhere := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(base, serviceDirName)); err != nil {
		t.Fatal(err)
	}

	dir, err := SocketDir()
	if err == nil {
		t.Fatalf("SocketDir returned %q for a symlinked service directory; the refusal was discarded", dir)
	}
	if dir != "" {
		t.Errorf("SocketDir returned %q alongside its error; a refused path must not also be handed back", dir)
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error = %q, want it to name the symlink", err)
	}
}

// A directory that is not there at all is an error, not a silent pass. This is
// the branch that would fire if a caller ever used ensureOwnerOnlyDir without
// the MkdirAll in front of it.
func TestEnsureOwnerOnlyDir_MissingDirectoryIsAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never-created")
	err := ensureOwnerOnlyDir(missing)
	if err == nil {
		t.Fatal("a nonexistent directory was accepted")
	}
	if !os.IsNotExist(err) {
		t.Errorf("error = %v, want a not-exist error the caller can distinguish", err)
	}
}

// An unwritable parent makes MkdirAll fail, and SocketDir must surface that
// rather than proceeding to check a directory it did not manage to create.
// (Skipped as root, which ignores directory permissions.)
func TestSocketDir_SurfacesACreateFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	base := t.TempDir()
	if err := os.Chmod(base, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(base, 0700) })
	t.Setenv("XDG_RUNTIME_DIR", base)

	if dir, err := SocketDir(); err == nil {
		t.Fatalf("SocketDir returned %q although it could not create the directory", dir)
	}
}

// THE REFUSAL ITSELF, which is the branch that does the work and the one a
// same-uid test can never reach.
//
// A directory another user owns must be refused outright rather than chmod'd:
// its real owner can change the mode back a moment later, so tightening it
// would be theatre. Arranging a genuinely foreign uid needs root, which a test
// cannot assume — so the expected owner is injected instead, which exercises
// the same comparison against the same syscall data.
func TestEnsureOwnerOnlyDir_RefusesADirectoryOwnedBySomeoneElse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}

	// This directory is ours; claim to be expecting a different owner.
	foreign := os.Getuid() + 1
	err := ensureOwnerOnlyDirAs(dir, foreign)
	if err == nil {
		t.Fatal("a directory owned by another uid was accepted; the socket would be served from a directory this user does not control")
	}
	if !strings.Contains(err.Error(), "owned by uid") {
		t.Errorf("refusal = %q, want it to name the owner mismatch", err)
	}

	// And it must be a refusal, not a repair: the mode is untouched.
	info, statErr := os.Stat(dir)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode().Perm() != 0700 {
		t.Errorf("mode changed to %04o; a foreign directory must be refused, never modified", info.Mode().Perm())
	}
}
