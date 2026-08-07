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
// against. It used to be a property of whatever the platform happened to
// provide; it is now a property this transport DECLARES, as socketBufferBytes,
// on all three platforms. See that constant for why.
//
// This comment used to read "a Unix socket carries ~200 KiB of kernel buffer".
// That is a LINUX figure written down as a Unix one, and it survived because
// Linux was the only kernel that had ever run it. macOS defaults a unix stream
// socket to net.local.stream.sendspace = 8 KiB, so the 32 KiB write below
// blocked, and the first macOS run this repository has ever had said so. Same
// shape as sun_path being 108 bytes on Linux and 104 on macOS -- and that one
// was found the same week, by the same runner.
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
// Neuter check, and it is no longer vacuous on Unix. Lowering socketBufferBytes
// to 2048 makes this FAIL on Linux -- measured, not assumed -- which is the
// evidence that setSocketBuffers actually takes effect rather than being a
// setsockopt whose result nobody checks. At Linux's ~200 KiB default it would
// have passed either way and proved nothing. Setting InputBufferSize/
// OutputBufferSize back to 0 in transport_windows.go's PipeConfig fails it on
// windows-latest, as before.
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
