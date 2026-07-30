package main

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"codeterminal/protocol"
)

// P1.4's evidence. Shutdown used to be `ln.Close(); os.Remove(socket)` and then
// main returned, which ends the process and every handler goroutine with it.
// Serve's semaphore bounded concurrency but nothing ever waited on it, so a
// request in flight at that moment was simply cut.
//
// For a streaming prompt that is merely annoying. For an ApplyEditRequest it is
// not: that is a multi-file write, and cutting it mid-batch leaves some files
// written and some not, with the backup session half-populated.
//
// The test drives a REAL connection over a REAL unix socket through Server.Serve
// exactly as production does, with the handler parked inside retrieval, and
// asserts the accounting in BOTH directions -- that WaitForDrain refuses to
// report a clean drain while work is in flight, and that it does once the work
// finishes. Only asserting the second half would pass trivially against a
// WaitForDrain that always returned true.
//
// Neuter check: delete the `s.inFlight.Add(1)` / `defer s.inFlight.Done()` pair
// in Serve and the first assertion fails -- WaitForDrain reports a complete drain
// while a request is still running, which is exactly the pre-P1.4 behaviour.

// blockingEmbedder parks EmbedQuery until release is closed, holding a connection
// handler open at a realistic point in the request path (retrieval) rather than
// at an artificial seam.
type blockingEmbedder struct {
	entered chan struct{}
	release chan struct{}
}

func (b blockingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return b.EmbedQuery(ctx, texts)
}

func (b blockingEmbedder) EmbedQuery(ctx context.Context, texts []string) ([][]float32, error) {
	select {
	case <-b.entered:
		// already signalled
	default:
		close(b.entered)
	}
	<-b.release
	return nil, context.Canceled // any error: the request outcome is not what is under test
}

func (b blockingEmbedder) Dim() int   { return embedDim }
func (b blockingEmbedder) ID() string { return "blocking-embedder" }

func TestWaitForDrain_WaitsForInFlightRequests(t *testing.T) {
	emb := blockingEmbedder{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}

	srv := &Server{
		logger:        discardLogger(),
		workspace:     t.TempDir(),
		modelOverride: "test-model",
		embedder:      emb,
		store:         emptyStore{},
		retrievalTopK: defaultK,
	}

	path := filepath.Join(t.TempDir(), "drain.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.Serve(ln)

	// Park one request inside the handler.
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	enc, dec := json.NewEncoder(c), json.NewDecoder(c)
	if err := enc.Encode(protocol.HandshakeRequest{ProtocolVersion: protocol.ProtocolVersion, ClientName: "drain"}); err != nil {
		t.Fatalf("handshake send: %v", err)
	}
	var hs protocol.HandshakeResponse
	if err := dec.Decode(&hs); err != nil {
		t.Fatalf("handshake recv: %v", err)
	}
	if err := enc.Encode(protocol.PromptRequest{ProtocolVersion: protocol.ProtocolVersion, Prompt: "park inside retrieval"}); err != nil {
		t.Fatalf("prompt send: %v", err)
	}

	select {
	case <-emb.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("request never reached the embedder; the test's own premise is broken")
	}

	// Stop accepting, as shutdown does, so the in-flight set is fixed.
	ln.Close()

	// HALF ONE: a drain must NOT be reported while the request is still running.
	if srv.WaitForDrain(300 * time.Millisecond) {
		t.Fatal("WaitForDrain reported a complete drain while a request was still in flight. " +
			"Shutdown would proceed to exit and cut that request -- for an edit apply, mid-batch, " +
			"leaving some files written and some not")
	}

	// HALF TWO: once the request finishes, the drain must complete.
	close(emb.release)
	if !srv.WaitForDrain(10 * time.Second) {
		t.Fatal("WaitForDrain never completed after the in-flight request finished; shutdown " +
			"would always burn its full grace period and report an incomplete drain")
	}
}

// TestWaitForDrain_ReturnsImmediatelyWhenIdle pins the common case: shutting down
// an idle daemon must not sit out the grace period. Without this a plain Ctrl-C on
// an unused daemon would take shutdownGrace to exit and read as a hang.
func TestWaitForDrain_ReturnsImmediatelyWhenIdle(t *testing.T) {
	srv := &Server{logger: discardLogger(), workspace: t.TempDir()}

	start := time.Now()
	if !srv.WaitForDrain(5 * time.Second) {
		t.Fatal("an idle server reported an incomplete drain")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("draining an idle server took %s; it must return promptly rather than "+
			"waiting out the grace period, or Ctrl-C on an unused daemon reads as a hang", elapsed)
	}
}
