package main

import (
	"bufio"
	"io"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// THE TWO SITES TestNoBuiltinReachesAnUnboundedRead FOUND, each pinned by a
// behavioural test rather than by the AST guard that found it.
//
// The guard proves a chain exists. These prove the read at the end of the chain
// actually allocates what the chain implies, which is the half a reachability
// walk cannot assert -- reachability is not a threat model, and a guard whose
// only evidence is another guard is circular (H1).
//
// Both measure ALLOCATION, following builtinreadalloc_test.go: the defect is not
// the size of the result, it is how many bytes had to be resident to produce it.
// TotalAlloc is noisy, so the signal in each is ~100x the bound it violates.

// allocDuring returns the bytes allocated while fn ran.
func allocDuring(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestReadHeaders_DoesNotMaterialiseAnUnboundedHeaderLine is TB7 / R1.24, as a
// measurement.
//
// maxLSPMessageBytes (8 MiB) bounds the BODY: readLoop reads Content-Length,
// refuses a value over the cap, and then calls io.ReadFull with a slice of that
// length -- bounded by the caller, which is why the body is fine.
//
// Nothing bounds the HEADER. readHeaders loops on out.ReadString('\n'), and
// bufio.Reader.ReadString accumulates fragments until the delimiter arrives.
// A language server that sends bytes with no newline makes this allocate all of
// them. The server is untrusted by lsp_bridge.go's own header: project config can
// load plugins, so the process on the other end of that pipe is not one whose
// framing can be assumed.
func TestReadHeaders_DoesNotMaterialiseAnUnboundedHeaderLine(t *testing.T) {
	const flood = 64 << 20 // 64 MiB, 8x the body cap and ~100x any sane header

	// A header line that never ends. No '\n' anywhere.
	r := bufio.NewReader(io.LimitReader(endlessByte('x'), flood))

	var err error
	used := allocDuring(func() { _, err = readHeaders(r) })

	t.Logf("readHeaders over a %d-byte newline-free stream: allocated %d bytes, err=%v",
		flood, used, err)

	if err == nil {
		t.Errorf("readHeaders accepted a %d-byte header line with no newline and returned no "+
			"error. A header block that large is not a framing accident; it is a server "+
			"deciding how much memory this process spends.", flood)
	}
	// The bound this asserts is deliberately generous -- it is not a tuning
	// target, it is the difference between "bounded" and "whatever arrives".
	if used > flood/8 {
		t.Errorf("readHeaders allocated %d bytes reading a %d-byte newline-free stream.\n"+
			"maxLSPMessageBytes bounds the BODY; the header line is unbounded, so an untrusted "+
			"language server sets this number. Bound the line length in readHeaders.", used, flood)
	}
}

// TestReadHeaders_DoesNotLoopForeverOnEndlessHeaders is the second unbounded
// dimension, and it is a different failure from the first: each line is short, so
// nothing grows, and the LOOP never terminates. A hang rather than an OOM.
//
// Separated from the test above because they need different fixes -- a line cap
// does not bound the count, and a count cap does not bound the line -- and two
// states meaning different things must not share a phrase.
func TestReadHeaders_DoesNotLoopForeverOnEndlessHeaders(t *testing.T) {
	// GENUINELY ENDLESS, and the first version of this test was not.
	//
	// It used a repeatingReader with a 64 MiB limit, so the stream ENDED and
	// readHeaders returned the resulting EOF -- and the test PASSED, reporting
	// that the header count was bounded when nothing bounds it. A fixture that
	// terminates cannot test a loop that does not, and a passing arm built on one
	// is worse than a missing arm: it is a recorded claim that is false.
	//
	// So: no limit, and a stop flag the cleanup trips so the reader goroutine
	// cannot outlive the test spinning.
	r := &endlessRepeat{unit: []byte("X-Pad: y\r\n")}
	t.Cleanup(r.stop)

	done := make(chan error, 1)
	go func() { _, err := readHeaders(bufio.NewReader(r)); done <- err }()

	select {
	case err := <-done:
		if err == nil {
			t.Error("readHeaders consumed an endless run of well-formed headers and returned no " +
				"error. The header COUNT is unbounded: a server that never sends the blank line " +
				"keeps this goroutine in the loop for as long as it likes.")
		} else {
			t.Logf("readHeaders refused an endless header block: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("readHeaders did not return within 10s on an endless header block. The loop has " +
			"no bound on the number of header lines, so a server can hold this reader open " +
			"indefinitely -- and readLoop is the goroutine every LSP caller waits behind.")
	}
}

// endlessByte is a reader that produces the same byte forever. Used with
// io.LimitReader so a test can ask for a precise flood without holding it in
// memory -- building the fixture must not be the thing that allocates.
type endlessByte byte

func (b endlessByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

// endlessRepeat emits unit over and over and NEVER ends, until stop is called.
//
// It never ends on purpose. Its predecessor took a byte limit and therefore
// terminated, which made the endless-header test pass while asserting nothing --
// the stop flag exists so the reader can be released at the end of the test
// rather than by exhausting it.
type endlessRepeat struct {
	unit    []byte
	off     int
	stopped atomic.Bool
}

func (r *endlessRepeat) stop() { r.stopped.Store(true) }

func (r *endlessRepeat) Read(p []byte) (int, error) {
	if r.stopped.Load() {
		return 0, errEndOfRepeat
	}
	for n := range p {
		p[n] = r.unit[r.off]
		r.off = (r.off + 1) % len(r.unit)
	}
	return len(p), nil
}

type repeatError string

func (e repeatError) Error() string { return string(e) }

const errEndOfRepeat = repeatError("repeatingReader exhausted")
