package main

import (
	"bytes"
	"strings"
	"syscall"
	"testing"
)

// epipeAfter is a writer whose reader goes away after n bytes, which is what
// `| head` does.
type epipeAfter struct {
	n   int
	buf bytes.Buffer
}

func (e *epipeAfter) Write(p []byte) (int, error) {
	if e.buf.Len() >= e.n {
		return 0, syscall.EPIPE
	}
	return e.buf.Write(p)
}

func TestOneShotExitsCleanlyWhenTheReaderGoesAway(t *testing.T) {
	oneShotPayloadDaemon(t, strings.Repeat("a long streamed answer. ", 500))

	out := &epipeAfter{n: 64}
	var errBuf bytes.Buffer
	code := runOneShotPrompt("test-client", "hi", oneShotIO{
		in: strings.NewReader(""), out: out, err: &errBuf, interactive: false,
	})

	// Exit 0: `| head` is a request for part of the answer, not a failure.
	if code != 0 {
		t.Fatalf("exit %d, want 0. stderr: %s", code, errBuf.String())
	}
	// And it must stop rather than spending the rest of the stream writing into
	// a pipe nobody is reading.
	if out.buf.Len() > 4096 {
		t.Errorf("kept writing after EPIPE: %d bytes", out.buf.Len())
	}
	if errBuf.Len() > 0 {
		t.Errorf("a broken pipe is not an error to report, but stderr got: %q", errBuf.String())
	}
}

// The wrapper must not swallow a genuine write failure into silence on the very
// first write either -- it reports zero, having written nothing, and the run
// still ends cleanly rather than looping.
func TestBrokenPipeWriterStopsAtTheFirstEPIPE(t *testing.T) {
	w := &brokenPipeWriter{w: &epipeAfter{n: 0}}
	if _, err := w.Write([]byte("first")); err == nil {
		t.Fatal("the first write should surface EPIPE")
	}
	if !w.broken {
		t.Fatal("the writer did not latch")
	}
	// Every later write is swallowed: the caller is on its way out and a second
	// EPIPE has nothing to add.
	n, err := w.Write([]byte("second"))
	if err != nil || n != len("second") {
		t.Fatalf("later writes should be swallowed, got n=%d err=%v", n, err)
	}
}
