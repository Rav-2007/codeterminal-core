//go:build unix

package protocol

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The seam's whole job: something bound by Listen is reachable by Dial, and the
// bytes survive the trip.
func TestListenAndDial_RoundTrip(t *testing.T) {
	addr := Address{Transport: TransportUnix, Address: filepath.Join(shortTempDir(t), "d.sock")}

	ln, err := Listen(addr)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	done := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		_, err = c.Write([]byte("pong"))
		done <- err
	}()

	conn, err := Dial(addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if string(got) != "pong" {
		t.Errorf("got %q, want %q", got, "pong")
	}
	if err := <-done; err != nil {
		t.Fatalf("server side: %v", err)
	}
}

// THE SECURITY PROPERTY THAT MOVED. The 0600 chmod used to sit in
// daemon/main.go, in the open, right after net.Listen. Moving it inside
// protocol.Listen is what let the daemon stop caring which transport it has —
// but it also moved it out of sight, so this pins it here.
//
// A Unix socket is created under the ambient umask. Without the chmod, a
// permissive umask leaves it world-connectable.
func TestListen_SocketIsOwnerOnly(t *testing.T) {
	// A deliberately permissive umask, so a missing chmod actually shows.
	old := syscallUmask(0)
	defer syscallUmask(old)

	path := filepath.Join(shortTempDir(t), "d.sock")
	ln, err := Listen(Address{Transport: TransportUnix, Address: path})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("socket mode = %04o, want 0600 — a socket left readable by other users is connectable by them", perm)
	}
}

func TestDialTimeout_ReachesAListener(t *testing.T) {
	addr := Address{Transport: TransportUnix, Address: filepath.Join(shortTempDir(t), "d.sock")}
	ln, err := Listen(addr)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Close()
		}
	}()

	conn, err := DialTimeout(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("DialTimeout: %v", err)
	}
	conn.Close()
}

// Nothing listening must be an error, not a hang. This is the probe
// reclaimStaleSocket depends on to tell a live daemon from a leftover socket
// file, so a false "connected" here would refuse a legitimate startup.
func TestDialTimeout_NothingListening(t *testing.T) {
	addr := Address{Transport: TransportUnix, Address: filepath.Join(shortTempDir(t), "absent.sock")}
	if conn, err := DialTimeout(addr, 500*time.Millisecond); err == nil {
		conn.Close()
		t.Error("dialling a path with no listener should fail")
	}
}

// A stale socket FILE with no listener behind it is the case reclaimStaleSocket
// exists for: the path exists, so os.Stat succeeds, but nothing answers.
func TestDial_StaleSocketFileIsRefused(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "d.sock")
	ln, err := Listen(Address{Transport: TransportUnix, Address: path})
	if err != nil {
		t.Fatal(err)
	}
	ln.Close() // leaves the file behind

	if _, err := os.Stat(path); err != nil {
		t.Skip("this platform removes the socket file on Close; the stale case cannot arise")
	}
	if conn, err := Dial(Address{Transport: TransportUnix, Address: path}); err == nil {
		conn.Close()
		t.Error("dialling a stale socket file should fail; reclaimStaleSocket relies on that to reclaim it")
	}
}

// An empty transport is treated as unix, so a hand-written or legacy-derived
// address still works.
func TestListen_EmptyTransportMeansUnix(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "d.sock")
	ln, err := Listen(Address{Address: path})
	if err != nil {
		t.Fatalf("an address with no transport should be treated as unix: %v", err)
	}
	ln.Close()
}
