//go:build darwin

package protocol

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The macOS mirror of peercred_linux_test.go, deliberately asserting the same
// things about the same function so "peer authentication works" means one thing
// on both platforms rather than two.
//
// RUN ON HARDWARE, at last: macos-latest, GitHub Actions run 31204152210,
// 2026-08-07, `ok codeterminal/daemon 53.019s`. LOCAL_PEERCRED had never
// executed on any machine anywhere before that.
//
// This comment used to read "NOT RUN on the machine this was written on — there
// is no Mac here, and the implementation is compile-verified (darwin/amd64 and
// darwin/arm64) and vet-clean, nothing more." It was committed so the first
// person with a Mac would get a real answer from `go test ./daemon/` rather than
// having to go looking. A CI matrix turned out to be that person.
//
// It took four macOS runs to get here, and none of the three failures in between
// was in this file — a socket path over the 103-byte budget, a workspace
// reported by an unresolved spelling, and a bind returning EEXIST where Linux
// returns EADDRINUSE. Each one killed the package before these tests could
// execute, which is the ordinary shape of first contact with a platform: the
// thing you were trying to verify is the last thing you get to.
//
// WHAT GREEN PROVES HERE, precisely. There is no t.Skip and no testing.Short
// guard below, and the file is //go:build darwin, so on a Mac there is no third
// state: these tests ran and passed. That is worth stating because this project
// has been burned by the opposite shape — the bubblewrap sandbox tests DO skip
// themselves, so a green run looked identical to one where they never executed,
// and --nosuid survived two campaigns marked CONFIRMED.

// acceptOneUnixDarwin listens on a fresh unix socket, dials it from this same
// process, and returns both ends of the accepted connection. Both peers are
// this test process, so LOCAL_PEERCRED must report this process's own uid.
func acceptOneUnixDarwin(t *testing.T) (client, server net.Conn) {
	t.Helper()
	path := filepath.Join(shortTempDir(t), "peercred.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	errc := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			errc <- err
			return
		}
		accepted <- c
	}()

	client, err = net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	select {
	case server = <-accepted:
	case err := <-errc:
		t.Fatalf("accept: %v", err)
	}
	t.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

// The uid must come from the kernel and must be this process's own, since both
// ends of the socket are this process. This is the assertion that distinguishes
// a working LOCAL_PEERCRED from a stub that returns a zero value.
func TestReadPeerCred_Darwin_ReturnsCurrentProcessCreds(t *testing.T) {
	_, server := acceptOneUnixDarwin(t)

	sc, ok := server.(syscall.Conn)
	if !ok {
		t.Fatalf("accepted unix conn %T does not implement syscall.Conn", server)
	}
	cred, err := readPeerCred(sc)
	if err != nil {
		t.Fatalf("readPeerCred: %v", err)
	}
	if got, want := cred.uid, uint32(os.Getuid()); got != want {
		t.Errorf("peer uid = %d, want current process uid %d", got, want)
	}
	// pid is best-effort on macOS (a separate LOCAL_PEERPID query) and is used
	// only to make a refusal log line identifying. A zero is tolerated; a WRONG
	// non-zero value is not, because that would be worse than none.
	if cred.pid != 0 && cred.pid != int32(os.Getpid()) {
		t.Errorf("peer pid = %d, want either 0 (unavailable) or this process's %d", cred.pid, os.Getpid())
	}
}

// The end of authorizePeer that matters for availability: a same-uid peer —
// every real client — must be ACCEPTED. Before peercred_darwin.go this failed
// on macOS, refusing every connection, which is why the item was an
// availability bug rather than a hardening one.
func TestAuthorizePeer_Darwin_SameUIDAllows(t *testing.T) {
	_, server := acceptOneUnixDarwin(t)

	if err := AuthorizePeer(server); err != nil {
		t.Fatalf("AuthorizePeer refused a legitimate same-uid peer: %v", err)
	}
}
