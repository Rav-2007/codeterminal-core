//go:build linux

package protocol

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// acceptOneUnix listens on a fresh unix socket, dials it from this same
// process, and returns both ends of the accepted connection. Both peers are
// this test process, so SO_PEERCRED must report this process's own uid/pid.
func acceptOneUnix(t *testing.T) (client, server net.Conn) {
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

// TestReadPeerCred_ReturnsCurrentProcessCreds proves the real SO_PEERCRED
// syscall path works live: both ends of the socket are this test process, so
// the kernel must report this process's own uid and pid.
func TestReadPeerCred_ReturnsCurrentProcessCreds(t *testing.T) {
	_, server := acceptOneUnix(t)

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
	if got, want := cred.pid, int32(os.Getpid()); got != want {
		t.Errorf("peer pid = %d, want current process pid %d", got, want)
	}
}

// TestAuthorizePeer_SameUID_Allows proves the happy path end of authorizePeer:
// a same-uid unix peer (the common, legitimate case for every real client) is
// accepted.
func TestAuthorizePeer_SameUID_Allows(t *testing.T) {
	_, server := acceptOneUnix(t)

	if err := AuthorizePeer(server); err != nil {
		t.Fatalf("AuthorizePeer refused a legitimate same-uid peer: %v", err)
	}
}
