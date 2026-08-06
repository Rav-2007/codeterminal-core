package protocol

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

// The lockfile is the compatibility seam between a daemon and every client, and
// the two halves are versioned independently in practice — a user updates the
// VS Code extension without rebuilding the daemon, or the reverse. These pin
// what each side may assume about the other.

// A lockfile written by a daemon built BEFORE Address existed carries only
// socket_path. A current client must still find that daemon.
func TestAddressFromLock_LegacyLockfileStillResolves(t *testing.T) {
	legacy := LockFile{SocketPath: "/run/user/1000/codeterminal/daemon.sock", PID: 42}

	got := AddressFromLock(legacy)
	if got.Transport != TransportUnix {
		t.Errorf("transport = %q, want %q — a lockfile with no address predates named pipes and can only be a socket",
			got.Transport, TransportUnix)
	}
	if got.Address != legacy.SocketPath {
		t.Errorf("address = %q, want %q", got.Address, legacy.SocketPath)
	}
}

// When both are present, Address wins. It is the authoritative field, and on a
// platform where they could disagree the path is the stale one.
func TestAddressFromLock_AddressWinsOverSocketPath(t *testing.T) {
	l := LockFile{
		SocketPath: "/stale/path.sock",
		Address:    Address{Transport: TransportNamedPipe, Address: `\\.\pipe\codeterminal-abc`},
	}
	got := AddressFromLock(l)
	if got.Transport != TransportNamedPipe || got.Address != `\\.\pipe\codeterminal-abc` {
		t.Errorf("got %+v, want the explicit address, not the legacy socket_path", got)
	}
}

// A daemon on Unix must keep writing socket_path, or a client built before
// Address existed cannot find it.
func TestNewLockFile_UnixKeepsWritingSocketPathForOldClients(t *testing.T) {
	addr := Address{Transport: TransportUnix, Address: "/run/x/daemon.sock"}
	l := NewLockFile(addr, 7)

	if l.SocketPath != addr.Address {
		t.Errorf("socket_path = %q, want %q — dropping it strands every client built before Address",
			l.SocketPath, addr.Address)
	}
	if l.Address != addr {
		t.Errorf("address = %+v, want %+v", l.Address, addr)
	}
	if l.PID != 7 {
		t.Errorf("pid = %d, want 7", l.PID)
	}
}

// A named pipe has no path, so socket_path stays empty rather than carrying
// something a Unix-shaped client would try to open as a file.
func TestNewLockFile_NamedPipeLeavesSocketPathEmpty(t *testing.T) {
	addr := Address{Transport: TransportNamedPipe, Address: `\\.\pipe\codeterminal-abc`}
	l := NewLockFile(addr, 7)

	if l.SocketPath != "" {
		t.Errorf("socket_path = %q, want empty — a pipe name is not a filesystem path and must not be offered as one",
			l.SocketPath)
	}
}

// The wire shape is what actually crosses between the daemon and a TypeScript
// client, so it is pinned by name. daemonClient.ts reads exactly these keys.
func TestLockFile_JSONShape(t *testing.T) {
	l := NewLockFile(Address{Transport: TransportUnix, Address: "/run/x.sock"}, 99)
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"socket_path"`, `"pid"`, `"address"`, `"transport"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("lockfile JSON is missing %s; clients read this by name\ngot: %s", key, b)
		}
	}

	// omitempty on Address must not fire for a populated address.
	var back LockFile
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Address.Address != "/run/x.sock" {
		t.Errorf("address did not survive a round trip: %+v", back)
	}
}

// A lockfile with neither field is not a usable address, and callers must be
// able to tell rather than dialling the empty string.
func TestAddress_IsZero(t *testing.T) {
	if !(Address{}).IsZero() {
		t.Error("an empty Address should report IsZero")
	}
	if (Address{Transport: TransportUnix, Address: "/x"}).IsZero() {
		t.Error("a populated Address should not report IsZero")
	}
	if got := AddressFromLock(LockFile{}); got.Address != "" {
		t.Errorf("an empty lockfile resolved to %q, want an empty address the caller can reject", got.Address)
	}
}

func TestAddress_String(t *testing.T) {
	// A Unix address reads as a bare path, unchanged from what logs showed
	// before the transport existed.
	if got := (Address{Transport: TransportUnix, Address: "/run/x.sock"}).String(); got != "/run/x.sock" {
		t.Errorf("got %q, want the bare path", got)
	}
	// A pipe is qualified, because a bare pipe name in a log looks like a typo.
	want := `npipe:\\.\pipe\codeterminal-abc`
	if got := (Address{Transport: TransportNamedPipe, Address: `\\.\pipe\codeterminal-abc`}).String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// DefaultAddress is what the daemon binds and every client derives. The two
// must agree, and the transport must match the platform.
func TestDefaultAddress_MatchesPlatform(t *testing.T) {
	got := DefaultAddress()
	if got.IsZero() {
		t.Fatal("DefaultAddress returned an empty address")
	}

	switch runtime.GOOS {
	case "windows":
		if got.Transport != TransportNamedPipe {
			t.Errorf("transport = %q, want %q on Windows", got.Transport, TransportNamedPipe)
		}
		if !strings.HasPrefix(got.Address, `\\.\pipe\`) {
			t.Errorf("address = %q, want a \\\\.\\pipe\\ name", got.Address)
		}
	default:
		if got.Transport != TransportUnix {
			t.Errorf("transport = %q, want %q", got.Transport, TransportUnix)
		}
		if got.Address != SocketPath() {
			t.Errorf("DefaultAddress = %q but SocketPath = %q; the daemon and its clients would disagree",
				got.Address, SocketPath())
		}
	}
}

// Dialling with a transport this platform cannot speak must be refused, not
// silently attempted as the local kind.
func TestDial_RefusesAForeignTransport(t *testing.T) {
	foreign := Address{Transport: TransportNamedPipe, Address: `\\.\pipe\x`}
	if runtime.GOOS == "windows" {
		foreign = Address{Transport: TransportUnix, Address: "/tmp/x.sock"}
	}
	if _, err := Dial(foreign); err == nil {
		t.Error("dialling a transport this platform does not support should fail loudly")
	}
	if _, err := Listen(foreign); err == nil {
		t.Error("listening on a transport this platform does not support should fail loudly")
	}
}

// THE GUARANTEE. daemon/main.go's package comment says the daemon "never
// listens on a network port", and $HOST used to silently make that false --
// $HOST is set by containers, CI runners, PaaS platforms and tcsh, none of
// which are asking for a remote daemon.
//
// It could not work either: authorizePeer runs on every accepted connection and
// fails closed, and SO_PEERCRED on a TCP socket reports uid 4294967295, so every
// TCP client was refused before its token was read. Setting $HOST bought a bound
// port and a broken daemon.
func TestDefaultAddress_IsNeverTCPHowever_HOST_IsSet(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "0.0.0.0", "example.com", "localhost"} {
		t.Setenv("HOST", host)
		t.Setenv("PORT", "9999")
		if addr := DefaultAddress(); addr.Transport == TransportTCP {
			t.Errorf("HOST=%s selected a NETWORK transport: %+v", host, addr)
		}
	}
}

// The TCP backend still has to work when asked for EXPLICITLY -- it is retained
// for tests and for any future, deliberately-designed remote mode. What changed
// is that nothing selects it by accident.
func TestTransportTCP(t *testing.T) {
	addr := Address{Transport: TransportTCP, Address: "127.0.0.1:0"}

	ln, err := Listen(addr)
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()

	addr.Address = ln.Addr().String()

	conn, err := Dial(addr)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	conn.Close()
}
