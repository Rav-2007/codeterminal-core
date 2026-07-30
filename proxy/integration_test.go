package main

// Integration matrix (P3.2) — the money path under FAULTS, end to end.
//
// The distinction from main_test.go is deliberate. That file tests the pieces:
// streamSSE's budget kill, finalizeUsage's charge policy, reserveQuota's row
// handling. This file drives whole requests through handleChatCompletions
// against a fake Supabase and a fake upstream, and asks the one question the
// unit tests structurally cannot: after a fault, is the reservation ledger
// still balanced?
//
// Every case here asserts the same invariant via assertLedgerBalanced --
// reservations opened == reservations finalized, and zero outbox rows left
// open. That invariant is what P0 found broken (a mid-stream SIGTERM stranded
// 3,959 tokens permanently, on every deploy) and what P1.1's deferred finalizer
// was built to guarantee. A fault that strands a row here is real money.
//
// WHAT IS COVERED WHERE, so this file does not duplicate what already exists:
//
//   - client disconnects mid-stream ..... main_test.go, C3 regression
//     (TestHandleChatCompletions_ClientAbortsBeforeUsageChunk_DoesNotRefund).
//     Already end-to-end and already asserts the open-row count; cited rather
//     than rewritten.
//   - upstream 5xx BEFORE the stream ..... main_test.go
//     (TestHandleChatCompletions_FailedUpstreamRefunds).
//   - budget kill at the streamSSE level . main_test.go (six tests).
//
// The five cases below are the ones nothing covered.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// zdrBody is the request shape the shipped daemon sends: streaming, with the
// ZDR provider flags F1 enforces. Built as a helper because a request missing
// these is refused before the money path is ever reached, which would make
// every test below pass for the wrong reason.
func zdrBody(extra string) string {
	return `{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":true,` +
		extra +
		`"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true}}`
}

// assertLedgerBalanced is the invariant every case in this file shares.
//
// It asserts the ACCOUNTING, not the amount: how much a given fault should
// charge is the individual test's business, but that the reservation was
// discharged exactly once and left no outbox row behind is universal. A fault
// that opens a reservation and never finalizes it is the P0 defect, and it is
// invisible to any assertion made only about the response the client saw.
func assertLedgerBalanced(t *testing.T, p *proxy, store *fakeUsageStore) {
	t.Helper()

	opened := p.metrics.reservationsOpened.Value()
	finalized := p.metrics.reservationsFinalized.Value()
	if opened != finalized {
		t.Errorf("reservations opened = %d but finalized = %d: %d reservation(s) were "+
			"never discharged, which is exactly the shape of the SIGTERM defect (a row "+
			"the sweep can only ever close by over-charging it)", opened, finalized, opened-finalized)
	}
	if n := store.openPendingCount(); n != 0 {
		t.Errorf("open pending_corrections rows = %d, want 0: this fault left the outbox "+
			"dirty, so the sweep will later report it as an abandoned reservation and the "+
			"tokens stay charged forever", n)
	}
}

// waitForLedgerSettled waits for the deferred finalizer's correction to land.
// The finalize happens on the handler goroutine, but correctUsage's HTTP call to
// the fake Supabase completes asynchronously from the test's point of view.
func waitForLedgerSettled(t *testing.T, store *fakeUsageStore) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if store.openPendingCount() == 0 && store.correctionCount() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// 1. Upstream dies MID-stream, after the client has already been sent output.
//
// Distinct from TestHandleChatCompletions_FailedUpstreamRefunds, which fails
// BEFORE any bytes are relayed and is therefore a clean full refund. Here the
// 200 and several chunks are already gone to the client when the connection
// breaks, so producedOutput is true and a refund would be wrong -- upstream
// generated (and will bill for) those tokens.
func TestIntegration_UpstreamDiesMidStream(t *testing.T) {
	const keyID = "aaaa1111-0000-0000-0000-000000000001"

	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tok%d \"}}]}\n\n", i)
			if fl != nil {
				fl.Flush()
			}
		}
		// Abort the connection without a usage frame and without [DONE]. This is
		// the provider-side crash: the client has real output, the proxy has no
		// measurement, and the reservation still has to be discharged.
		panic(http.ErrAbortHandler)
	}))
	defer upstream.Close()

	p := newTestProxy(supabase.URL, upstream.URL)
	rec := httptest.NewRecorder()
	p.handleChatCompletions(rec, newAuthorizedRequest(zdrBody("")))

	waitForLedgerSettled(t, store)
	assertLedgerBalanced(t, p, store)

	// Output was produced, so the reservation must be KEPT, not refunded. A
	// negative delta here would mean a caller can get free inference by having
	// the provider drop the connection before the usage frame.
	if n := store.correctionCount(); n != 1 {
		t.Fatalf("correction count = %d, want exactly 1; deltas=%v", n, store.corrections)
	}
	if got := store.corrections[0]; got < 0 {
		t.Errorf("correction delta = %d, want >= 0: upstream produced billable output "+
			"before it died, so refunding the reservation would make a mid-stream provider "+
			"crash into a way to get unmetered inference", got)
	}
}

// ---------------------------------------------------------------------------
// 2. Upstream accepts the connection and then hangs, past the request's bound.
//
// The bound under test is the one at handleChatCompletions' `ctx, cancel :=
// context.WithTimeout(r.Context(), upstreamTimeout)`. upstreamTimeout is a
// const (5 minutes), so rather than mutate it the DERIVED deadline is shortened
// from the parent: context.WithTimeout keeps whichever deadline is sooner, so a
// short deadline on r.Context() drives the identical branch -- client.Do
// returns a deadline error, nothing was relayed, and the deferred finalizer
// takes the zero-value (full refund) path.
//
// Without a bound this request would hold a goroutine, a connection, and an
// open reservation for as long as the upstream chose to stay silent.
func TestIntegration_UpstreamHangsPastDeadline(t *testing.T) {
	const keyID = "aaaa1111-0000-0000-0000-000000000002"

	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	released := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never writes a byte: accepted, then silent.
		select {
		case <-released:
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	defer close(released)

	p := newTestProxy(supabase.URL, upstream.URL)

	req := newAuthorizedRequest(zdrBody(""))
	ctx, cancel := context.WithTimeout(req.Context(), 400*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	start := time.Now()
	p.handleChatCompletions(rec, req)
	elapsed := time.Since(start)

	if elapsed > 30*time.Second {
		t.Fatalf("the request took %v: the upstream call is not bounded at all", elapsed)
	}

	waitForLedgerSettled(t, store)
	assertLedgerBalanced(t, p, store)

	// Nothing was ever relayed, so this is the full-refund case.
	if n := store.correctionCount(); n != 1 {
		t.Fatalf("correction count = %d, want exactly 1; deltas=%v", n, store.corrections)
	}
	if got, want := store.corrections[0], int64(-defaultReservationTokens); got != want {
		t.Errorf("correction delta = %d, want %d (a hung upstream produced no output, "+
			"so the whole reservation must come back)", got, want)
	}
}

// ---------------------------------------------------------------------------
// 3. Quota ceiling kills the stream mid-flight, end to end.
//
// main_test.go covers the kill at the streamSSE level. What it cannot cover is
// the wiring: that requestTokenCeiling's number actually reaches streamSSE from
// a real reservation's headroom, and that the kill still discharges the
// reservation through handleChatCompletions' deferred finalizer.
//
// max_tokens:10 makes the reservation 10 rather than the 4096 default, and a
// token_limit of 15 leaves 5 headroom, so the ceiling is 15 -- crossable by an
// upstream that keeps streaming.
func TestIntegration_QuotaCeilingKillsStreamMidFlight(t *testing.T) {
	const keyID = "aaaa1111-0000-0000-0000-000000000003"

	store := &fakeUsageStore{tokenLimit: 15}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		// Far more than the ceiling: a provider ignoring max_tokens entirely.
		for i := 0; i < 200; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tok%d \"}}]}\n\n", i)
			if fl != nil {
				fl.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			default:
			}
		}
	}))
	defer upstream.Close()

	p := newTestProxy(supabase.URL, upstream.URL)
	rec := httptest.NewRecorder()
	p.handleChatCompletions(rec, newAuthorizedRequest(zdrBody(`"max_tokens":10,`)))

	waitForLedgerSettled(t, store)
	assertLedgerBalanced(t, p, store)

	// The stream must have been CUT, not relayed in full. 200 chunks would be
	// far past the ceiling of 15.
	chunks := strings.Count(rec.Body.String(), "data: ")
	if chunks >= 200 {
		t.Errorf("relayed %d chunks with a ceiling of 15: the budget kill never fired, so "+
			"a provider that ignores max_tokens can spend past the key's quota", chunks)
	}

	// A killed stream is charged, never refunded -- that is the whole point of
	// chargeForKill.
	if n := store.correctionCount(); n != 1 {
		t.Fatalf("correction count = %d, want exactly 1; deltas=%v", n, store.corrections)
	}
	if got := store.corrections[0]; got < 0 {
		t.Errorf("correction delta = %d, want >= 0: a stream killed for exceeding its "+
			"budget must be charged, or exceeding the budget becomes the way to avoid "+
			"paying for what was already streamed", got)
	}
}

// ---------------------------------------------------------------------------
// 4. pending_id == 0 -- migration 0002 absent.
//
// Called out in the plan as "currently only reachable in production". A
// database still running 0001's reserve_usage returns a real reservation with
// no outbox row, and the proxy deliberately does NOT fail closed on that: quota
// enforcement is intact, only crash recovery is missing, and refusing would
// turn a missing migration into a total outage.
//
// What must hold: the request still succeeds, and the reservation is still
// finalized exactly once. The risk this pins is the opposite mistake -- treating
// a zero pending_id as a reason to skip finalize, which would silently stop
// billing every request in an un-migrated environment.
func TestIntegration_PendingIDZero_MigrationAbsent(t *testing.T) {
	const keyID = "aaaa1111-0000-0000-0000-000000000004"

	var corrections atomic.Int64
	var sawPendingID atomic.Int64
	sawPendingID.Store(-1)

	// A hand-built Supabase rather than newFakeSupabase: the whole point is a
	// reserve_usage that answers WITHOUT a pending_id, which the shared fake
	// (correctly) never does.
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/v1/api_keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]string{{"id": keyID}})
	})
	mux.HandleFunc("/rest/v1/rpc/reserve_usage", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 0001's shape: tokens_used and token_limit, and no pending_id at all.
		json.NewEncoder(w).Encode([]map[string]int64{{
			"tokens_used": 4096,
			"token_limit": 100000,
		}})
	})
	mux.HandleFunc("/rest/v1/rpc/apply_correction", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PendingID int64 `json:"p_pending_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		sawPendingID.Store(body.PendingID)
		corrections.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	supabase := httptest.NewServer(mux)
	defer supabase.Close()

	upstream := newFakeUpstream(123)
	defer upstream.Close()

	p := newTestProxy(supabase.URL, upstream.URL)
	rec := httptest.NewRecorder()
	p.handleChatCompletions(rec, newAuthorizedRequest(zdrBody("")))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a missing migration must not take the request "+
			"down -- the reservation itself is real and atomic, so quota is still enforced; "+
			"body=%s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && corrections.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	if got := corrections.Load(); got != 1 {
		t.Fatalf("apply_correction called %d times, want exactly 1: a zero pending_id must "+
			"not be treated as a reason to skip finalizing, or an un-migrated environment "+
			"silently stops billing every request", got)
	}
	if got := sawPendingID.Load(); got != 0 {
		t.Errorf("apply_correction carried pending_id = %d, want 0 (what an un-migrated "+
			"reserve_usage yields)", got)
	}

	// The finalizer still ran exactly once, which is the property that survives
	// the missing migration.
	if opened, finalized := p.metrics.reservationsOpened.Value(), p.metrics.reservationsFinalized.Value(); opened != finalized {
		t.Errorf("reservations opened = %d, finalized = %d", opened, finalized)
	}
}

// ---------------------------------------------------------------------------
// 5. SIGTERM mid-stream -- the P0 headline defect, as a money-path test.
//
// TestServeUntilSignal already covers the shutdown MECHANISM (ordering, the 503
// flip, that it does not return early). This covers what the mechanism exists
// for: a request in flight when the signal lands must still have its
// reservation discharged.
//
// Baseline, measured in Phase 0 against the real binary: `authorize →
// reserve_usage` and nothing else, 3,959 tokens charged forever, on every
// deploy. The assertion below is that same scenario, in process.
//
// Runs a real listener and a real HTTP client so the request is genuinely
// in-flight across the signal, not simulated. It costs preDrainDelay (3s) by
// construction -- the delay is the thing being relied on.
func TestIntegration_ShutdownMidStream_BillsRatherThanStrands(t *testing.T) {
	const keyID = "aaaa1111-0000-0000-0000-000000000005"

	store := &fakeUsageStore{tokenLimit: 100000}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	// TIMING, and it is the whole test.
	//
	// The stream must still be RUNNING at the moment srv.Shutdown is called,
	// which is preDrainDelay AFTER the signal -- otherwise Shutdown has nothing
	// to wait for and this test passes against a server that does not drain at
	// all. Phase 1 made exactly this mistake (a 600ms handler against a 3s
	// pre-drain window) and so did the first cut of this test: a 4s stream
	// signalled at 1.2s had finished before the pre-drain window elapsed, and
	// replacing Shutdown with an abrupt Close still passed.
	//
	// Derived from preDrainDelay rather than hardcoded so that tuning the
	// constant cannot silently make this inert again.
	const signalAfter = 800 * time.Millisecond
	chunkDelay := 500 * time.Millisecond
	// Long enough to outlast signalAfter + preDrainDelay, short enough to finish
	// well inside the drain grace.
	chunks := int((signalAfter+preDrainDelay+3*time.Second)/chunkDelay) + 1

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for i := 0; i < chunks; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tok%d \"}}]}\n\n", i)
			if fl != nil {
				fl.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(chunkDelay):
			}
		}
		io.WriteString(w, "data: {\"usage\":{\"total_tokens\":137}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	defer upstream.Close()

	p := newTestProxy(supabase.URL, upstream.URL)

	var draining atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", p.handleChatCompletions)
	mux.HandleFunc("/health", makeHealthHandler("testcommit", &draining))

	srv := &http.Server{Handler: mux}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	base := "http://" + ln.Addr().String()

	sigCh := make(chan os.Signal, 1)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- serveUntilSignal(srv, ln, log.New(io.Discard, "", 0), &draining,
			func() {}, sigCh)
	}()

	// Park a streaming request inside the handler.
	type result struct {
		status int
		body   string
		err    error
	}
	reqDone := make(chan result, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions",
			strings.NewReader(zdrBody("")))
		req.Header.Set("Authorization", "Bearer mochi_test_key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			reqDone <- result{err: err}
			return
		}
		body := resp.Body
		defer body.Close()
		b, err := io.ReadAll(body)
		reqDone <- result{status: resp.StatusCode, body: string(b), err: err}
	}()

	// Let the stream get genuinely under way, then signal. The handler must
	// still be mid-stream when Shutdown fires, preDrainDelay from now.
	time.Sleep(signalAfter)
	sigCh <- syscall.SIGTERM

	select {
	case res := <-reqDone:
		if res.err != nil {
			t.Fatalf("the in-flight request failed across the drain: %v -- a SIGTERM "+
				"must not cut a stream that the grace window had time to finish", res.err)
		}
		if res.status != http.StatusOK {
			t.Errorf("in-flight request got %d across the drain, want 200", res.status)
		}
	case <-time.After(shutdownGrace + 5*time.Second):
		t.Fatal("the in-flight request never completed")
	}

	select {
	case err := <-serveErr:
		if err != nil {
			t.Errorf("serveUntilSignal returned %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveUntilSignal never returned")
	}

	waitForLedgerSettled(t, store)

	// THE assertion. Phase 0's baseline left this row open forever.
	assertLedgerBalanced(t, p, store)

	if n := store.correctionCount(); n != 1 {
		t.Fatalf("correction count = %d, want exactly 1: the reservation opened before the "+
			"signal must still be discharged by the drain; deltas=%v", n, store.corrections)
	}
}
