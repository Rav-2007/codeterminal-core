package protocol

import (
	"strings"
	"testing"
)

// LocalAddressFor derives the endpoint for a non-daemon local process -- today
// the embedder helper. These are the properties that hold on EVERY platform;
// the filesystem-shaped ones live in localaddress_unix_test.go, because on
// Windows the address is a named pipe and there is no file to assert about.
//
// Splitting them is not tidiness. helper/main.go used to hardcode
// TransportUnix, which Windows rejects outright, and helperproto's test
// asserted a filesystem path and a 0700 directory mode unconditionally -- so
// the Unix-only assumption was baked into the test as firmly as into the code,
// and the helper could not start on Windows for as long as Windows was
// supported.

func TestLocalAddressFor_IsScopedByName(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortTempDir(t))

	first, err := LocalAddressFor("embedder-helper-4242")
	if err != nil {
		t.Fatalf("LocalAddressFor: %v", err)
	}
	second, err := LocalAddressFor("embedder-helper-4243")
	if err != nil {
		t.Fatalf("LocalAddressFor: %v", err)
	}

	if first.Address == second.Address {
		t.Errorf("two different names produced the same endpoint %q -- an endpoint "+
			"left by a dead process could be reached by a new one", first.Address)
	}
	if !strings.Contains(first.Address, "embedder-helper-4242") {
		t.Errorf("LocalAddressFor(%q) = %q, want the name to appear in the address",
			"embedder-helper-4242", first.Address)
	}
}

// The transport must be COMMITTED TO rather than left empty for the callee to
// resolve. Both the helper and the daemon derive their endpoint from this one
// function precisely so they cannot each resolve a default differently; an
// empty transport would put that decision back in two places.
func TestLocalAddressFor_CommitsToATransport(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortTempDir(t))

	a, err := LocalAddressFor("probe")
	if err != nil {
		t.Fatalf("LocalAddressFor: %v", err)
	}
	if a.Transport == "" {
		t.Error("LocalAddressFor returned an empty transport; both sides of the helper " +
			"IPC derive from this, and an unstated transport is how they drift apart")
	}
	if a.Transport != TransportUnix && a.Transport != TransportNamedPipe {
		t.Errorf("transport = %q, want one of %q or %q", a.Transport, TransportUnix, TransportNamedPipe)
	}
	if a.IsZero() {
		t.Error("LocalAddressFor returned a zero address")
	}
}

// It must agree with DefaultAddressFor about what this platform's local
// transport IS. They are separate derivations -- one for the daemon, one for
// everything else -- and a platform where they disagreed would mean a helper
// listening on a socket while the daemon dialled a pipe.
func TestLocalAddressFor_AgreesWithDefaultAddressOnTransport(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", shortTempDir(t))

	local, err := LocalAddressFor("probe")
	if err != nil {
		t.Fatalf("LocalAddressFor: %v", err)
	}
	if got, want := local.Transport, DefaultAddress().Transport; got != want {
		t.Errorf("LocalAddressFor transport = %q but DefaultAddress uses %q; the helper "+
			"and the daemon would be speaking different transports on this platform", got, want)
	}
}
