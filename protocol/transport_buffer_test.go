package protocol

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

var bufTestSeq atomic.Int64

// localTestAddress returns an address on whatever local transport this platform
// actually speaks. Inline rather than in a build-tagged pair, matching
// TestDial_RefusesAForeignTransport's existing style in transport_test.go.
func localTestAddress(t *testing.T) Address {
	t.Helper()
	if runtime.GOOS == "windows" {
		return Address{
			Transport: TransportNamedPipe,
			Address:   fmt.Sprintf(`\\.\pipe\codeterminal-buftest-%d-%d`, os.Getpid(), bufTestSeq.Add(1)),
		}
	}
	return Address{Transport: TransportUnix, Address: filepath.Join(shortTempDir(t), "d.sock")}
}

// THE TRANSPORT MUST BUFFER A WRITE THE PEER HAS NOT READ.
//
// This is the property every caller on both sides of this seam was written
// against, and it is a property of the SOCKET, not of anything protocol/ does.
// A Unix socket carries ~200 KiB of kernel buffer, so a small write returns
// immediately whether or not anyone is reading. A Windows named pipe created
// with a zero quota carries NONE, and every write blocks until the peer drains
// it.
//
// WHAT THAT COSTS, and why this is a transport test rather than a caller's
// problem. A client that stops reading -- crashed, suspended, or merely slow --
// leaves the daemon's handler goroutine parked in a write it can never finish.
// Serve's concurrency semaphore is finite, so enough such clients stop the
// daemon accepting at all, and WaitForDrain can never complete because inFlight
// never falls to zero. That is an availability failure with no Unix analogue,
// produced entirely by a default in the pipe's creation parameters.
//
// The 20 s approval-pipelining timeouts and the hung drain test on the first
// Windows CI run were all this one default. This test is the direct probe for
// it, so the next person sees the cause rather than three unrelated symptoms.
//
// Neuter check: set InputBufferSize/OutputBufferSize back to 0 in
// transport_windows.go's PipeConfig and this fails on windows-latest. On Unix it
// passes either way -- there is nothing to neuter there, which is precisely the
// point. It is not a vacuous assertion on Linux: it pins the CONTRACT that the
// Windows backend has to meet, and Linux is where that contract came from.
func TestTransport_BuffersAWriteThePeerHasNotRead(t *testing.T) {
	addr := localTestAddress(t)
	ln, err := Listen(addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c // held open and DELIBERATELY never read from
	}()

	c, err := Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	peer, ok := <-accepted
	if !ok {
		t.Fatal("accept failed; the test's own premise is broken")
	}
	defer peer.Close()

	// Comfortably over any frame either side sends in one write, and under the
	// buffer the transport is configured with.
	payload := make([]byte, 32*1024)

	done := make(chan error, 1)
	go func() {
		_, werr := c.Write(payload)
		done <- werr
	}()

	select {
	case werr := <-done:
		if werr != nil {
			t.Fatalf("writing %d bytes to an unread peer: %v", len(payload), werr)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("writing %d bytes BLOCKED because the peer had not read them. "+
			"This transport has no buffer, so one slow or wedged client parks a daemon "+
			"handler goroutine indefinitely: Serve's concurrency limit fills, the daemon "+
			"stops accepting, and WaitForDrain can never reach zero in flight", len(payload))
	}
}
