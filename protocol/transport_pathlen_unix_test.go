//go:build unix

package protocol

import (
	"path/filepath"
	"strings"
	"testing"
)

// A unix socket address is a FIXED-SIZE array, and the failure mode when you
// exceed it is bind(2) answering EINVAL -- "invalid argument" about a path that
// plainly exists, with the length nowhere in the message.
//
// This was not hypothetical: per-workspace names added 17 bytes and a runtime
// directory that had fitted the old name stopped fitting the new one, which is
// how the check came to exist at all.
func TestListen_RefusesAnOverlongSocketPathWithAReadableReason(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("x", maxUnixSocketPath), "d.sock")

	_, err := Listen(Address{Transport: TransportUnix, Address: long})
	if err == nil {
		t.Fatal("listening on an over-length socket path succeeded; the kernel would have " +
			"refused it with EINVAL")
	}
	for _, want := range []string{"bytes", "unix socket", "XDG_RUNTIME_DIR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q — a user has to be able to act on this "+
				"without knowing what sockaddr_un is", err, want)
		}
	}
	// The kernel's own message is what this replaces; it must not be all a user gets.
	if strings.Contains(err.Error(), "invalid argument") {
		t.Errorf("error = %q, which is still the kernel's unactionable wording", err)
	}
}

// And the honest case must still bind: a check that refuses everything would
// pass the test above while breaking the product.
func TestListen_AcceptsAPathAtTheLimit(t *testing.T) {
	dir := t.TempDir()
	name := strings.Repeat("a", maxUnixSocketPath-len(dir)-1)
	if len(name) < 1 {
		t.Skip("NOT RUN: this TempDir is already at the limit; nothing to test")
	}
	addr := Address{Transport: TransportUnix, Address: filepath.Join(dir, name)}
	if len(addr.Address) != maxUnixSocketPath {
		t.Fatalf("fixture is %d bytes, wanted exactly %d", len(addr.Address), maxUnixSocketPath)
	}

	ln, err := Listen(addr)
	if err != nil {
		t.Fatalf("a path of exactly %d bytes was refused: %v", maxUnixSocketPath, err)
	}
	_ = ln.Close()
}
