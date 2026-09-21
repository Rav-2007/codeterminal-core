//go:build unix

package protocol

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// On Unix the endpoint IS a file, and these are the properties that only make
// sense where that is true.

func TestLocalAddressFor_LivesBesideTheDaemonSocket(t *testing.T) {
	tmp := shortTempDir(t)
	t.Setenv("XDG_RUNTIME_DIR", tmp)

	a, err := LocalAddressFor("embedder-helper-4242")
	if err != nil {
		t.Fatalf("LocalAddressFor: %v", err)
	}
	if a.Transport != TransportUnix {
		t.Fatalf("transport = %q, want %q on unix", a.Transport, TransportUnix)
	}

	want := filepath.Join(tmp, "mochiii", "embedder-helper-4242.sock")
	if a.Address != want {
		t.Errorf("Address = %q, want %q", a.Address, want)
	}

	// It shares the daemon's own runtime directory, which SocketDir creates
	// owner-only. The helper endpoint is an unauthenticated local listener, so
	// the directory mode is what confines it.
	info, err := os.Stat(filepath.Dir(a.Address))
	if err != nil {
		t.Fatalf("stat socket dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("socket directory mode = %#o, want 0700", perm)
	}
}

// The address it returns must actually be bindable. A derivation that produces
// a plausible-looking string nothing can listen on is the defect this whole
// function was introduced to fix, just in the other direction.
func TestLocalAddressFor_ProducesABindableAddress(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortTempDir(t))

	a, err := LocalAddressFor("bindable")
	if err != nil {
		t.Fatalf("LocalAddressFor: %v", err)
	}
	ln, err := Listen(a)
	if err != nil {
		t.Fatalf("Listen(%s): %v -- LocalAddressFor produced an address that cannot be bound", a, err)
	}
	defer ln.Close()

	c, err := net.Dial("unix", a.Address)
	if err != nil {
		t.Fatalf("dialling the address it produced: %v", err)
	}
	c.Close()
}

// THE LENGTH CHECK HAPPENS HERE, not at Listen.
//
// sockaddr_un.sun_path is a fixed 108 bytes on Linux and 104 on macOS. Checking
// at derivation rather than at bind means the daemon finds out before it spawns
// a helper that is guaranteed to die, and the error names the variable to
// change rather than surfacing as a bind failure in a child process's stderr.
func TestLocalAddressFor_RefusesAnOverlongPath(t *testing.T) {
	deep := filepath.Join(shortTempDir(t), strings.Repeat("d", 120))
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", deep)

	a, err := LocalAddressFor("embedder-helper-4242")
	if err == nil {
		t.Fatalf("LocalAddressFor returned %q for a path over the sun_path limit; "+
			"the helper would have been spawned only to die on bind", a.Address)
	}
	if !strings.Contains(err.Error(), "XDG_RUNTIME_DIR") {
		t.Errorf("error = %q, want it to name XDG_RUNTIME_DIR as the thing to change", err)
	}
}

// The SocketDir failure path, which is the difference between "the helper could
// not be reached" and "the helper was never spawned".
//
// A FILE standing where a directory must be, rather than a chmod: a test that
// depends on permission bits passes vacuously when the suite runs as root, and
// CI containers routinely do. ENOTDIR is refused for everyone.
func TestLocalAddressFor_PropagatesASocketDirFailure(t *testing.T) {
	base := shortTempDir(t)
	blocker := filepath.Join(base, "f")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	// XDG_RUNTIME_DIR points BENEATH a regular file, so MkdirAll cannot create
	// the service directory and SocketDir returns an error.
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(blocker, "under"))

	if a, err := LocalAddressFor("embedder-helper-4242"); err == nil {
		t.Fatalf("LocalAddressFor returned %q when its runtime directory could not be "+
			"created; the caller would spawn a helper that cannot bind", a.Address)
	}
}
