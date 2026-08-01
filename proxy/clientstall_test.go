package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// stallingWriter is a client that does not read its own response: every write
// blocks for stallPerWrite before returning, which is what a full socket buffer
// looks like from the server's side.
//
// It writes nothing to a real connection, so nothing here depends on kernel
// buffer sizes or TCP timing — the property under test is "how long was this
// handler blocked writing to this client", and the fixture states that directly.
type stallingWriter struct {
	stallPerWrite time.Duration

	mu     sync.Mutex
	buf    bytes.Buffer
	writes int
}

func (s *stallingWriter) Header() http.Header { return http.Header{} }
func (s *stallingWriter) WriteHeader(int)     {}
func (s *stallingWriter) Flush()              {}

func (s *stallingWriter) Write(p []byte) (int, error) {
	time.Sleep(s.stallPerWrite)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	return s.buf.Write(p)
}

func (s *stallingWriter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

// sseLines builds n data chunks plus a terminator, enough that a stream which
// is NOT cut short relays every one of them.
func sseLines(n int) io.Reader {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(`data: {"choices":[{"delta":{"content":"tok"}}]}` + "\n")
	}
	b.WriteString("data: [DONE]\n")
	return strings.NewReader(b.String())
}

// A client that will not read its own response must be dropped, not carried for
// the full serverWriteTimeout.
//
// serverWriteTimeout has to be generous — a real completion streams for
// minutes — and that generosity was the hole (M10): one slow-drip reader held a
// connection, an in-flight slot and an upstream call for six minutes while
// consuming almost nothing. The in-flight caps bound how MANY such clients can
// exist at once; nothing bounded one.
//
// What is asserted is that the stream is CUT SHORT: the relay stops partway
// through chunks the upstream had already produced.
func TestSlowClientIsDroppedNotCarried(t *testing.T) {
	p := newTestProxy("", "")
	p.log = slogFromLogger(log.New(io.Discard, "", 0))
	p.maxClientStall = 50 * time.Millisecond

	w := &stallingWriter{stallPerWrite: 20 * time.Millisecond}
	outcome := &reservationOutcome{keyID: "k", reserved: defaultReservationTokens}

	const chunks = 40
	start := time.Now()
	p.streamSSE(w, sseLines(chunks), outcome, absoluteMaxRequestTokens, nil)
	elapsed := time.Since(start)

	if got := w.count(); got >= chunks {
		t.Fatalf("relayed %d of %d chunks in %v — the stream was carried to completion, so nothing bounds one non-reading client", got, chunks, elapsed)
	}
	// The bound is cumulative stall, so it must bite in roughly stall/perWrite
	// writes, not at some much later point.
	if w.count() > 10 {
		t.Errorf("relayed %d chunks before cutting off; a 50ms budget at 20ms per write should bite within a few", w.count())
	}
	if p.metrics.clientStalls.Value() != 1 {
		t.Errorf("client_stalls_total = %d, want 1", p.metrics.clientStalls.Value())
	}
}

// A slow MODEL must not be mistaken for a slow client. This is the false
// positive that makes an average-bytes-per-second bound unusable: the provider
// thinking for a long time between chunks is normal, and cutting a caller off
// for it would be a worse bug than the one being fixed. Only time blocked
// INSIDE the write counts, and a client that reads promptly accrues none of it
// however slowly the chunks arrive.
func TestSlowUpstreamIsNotMistakenForASlowClient(t *testing.T) {
	p := newTestProxy("", "")
	p.log = slogFromLogger(log.New(io.Discard, "", 0))
	p.maxClientStall = 50 * time.Millisecond

	w := &stallingWriter{stallPerWrite: 0} // reads promptly
	outcome := &reservationOutcome{keyID: "k", reserved: defaultReservationTokens}

	const chunks = 20
	p.streamSSE(w, slowReader{r: sseLines(chunks), delay: 10 * time.Millisecond}, outcome,
		absoluteMaxRequestTokens, nil)

	if got := w.count(); got < chunks {
		t.Fatalf("relayed only %d of %d chunks: a slow UPSTREAM was cut off as though the client were slow", got, chunks)
	}
	if p.metrics.clientStalls.Value() != 0 {
		t.Errorf("client_stalls_total = %d, want 0 — nothing stalled on the client", p.metrics.clientStalls.Value())
	}
}

// slowReader delivers the upstream stream slowly, standing in for a provider
// that is still generating.
type slowReader struct {
	r     io.Reader
	delay time.Duration
}

func (s slowReader) Read(p []byte) (int, error) {
	time.Sleep(s.delay)
	if len(p) > 64 {
		p = p[:64]
	}
	return s.r.Read(p)
}
