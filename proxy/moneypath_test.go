package main

// Money-path invariant (P3.3).
//
// P0-1 was a violated invariant, not a broken function: a reservation could be
// opened and never discharged, and no single unit test was wrong. The defect
// lived in the space BETWEEN the branches -- which branch bills, which returns
// early, which one a panic skips. This file encodes the invariant itself, over
// generated combinations of (reserved, measured, outcome), so it cannot regress
// no matter which branch a future change adds.
//
// THE INVARIANTS, and why each one is money rather than bookkeeping:
//
//  I1. Every reservation is finalized EXACTLY once.
//      Zero times = the P0 defect: charged forever at the reserved amount, and
//      only the sweep ever notices. Twice = double-billing.
//
//  I2. The outbox ends empty.
//      A row left open is reported ~20 minutes later as an abandoned
//      reservation that never happened.
//
//  I3. tokens_used never ends negative.
//      A refund larger than the reservation is free quota, repeatable.
//
//  I4. A request that produced output but was never MEASURED is not refunded
//      below its reservation. "Make the usage frame go missing" must not be a
//      way to get unmetered inference. This is the C3 abort-refund bug as a
//      general law.
//
//      The measured qualifier is load-bearing, and the first cut of this file
//      omitted it -- asserting instead that any request producing output must
//      end at or above its reservation. Eight completed-and-measured cases
//      failed, correctly: settling a 4096-token reservation at a measured 137
//      is the reservation model working, not a leak. The test was wrong, and it
//      would have forbidden the ordinary refund the design exists to perform.
//
//  I5. A request that produced NO output is refunded in full.
//      The other direction: a failure before any bytes were relayed must not
//      leave the caller charged for a completion they never got.
//
// SIGTERM is in the plan's outcome list but is NOT generated here: each case
// would cost preDrainDelay (3s), and it is already covered end to end by
// TestIntegration_ShutdownMidStream_BillsRatherThanStrands. Stated rather than
// silently dropped.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// outcome is how a generated request ends. These are the five ways a request
// can leave the money path, not five arbitrary error injections.
type outcome string

const (
	// outcomeComplete: upstream streams and reports usage. The ordinary path.
	outcomeComplete outcome = "complete"
	// outcomeAbort: the client disconnects before the usage frame. Output was
	// produced, so the reservation must be kept (C3).
	outcomeAbort outcome = "client-abort"
	// outcomeUpstreamDie: upstream aborts the connection mid-stream. Output was
	// produced but never measured.
	outcomeUpstreamDie outcome = "upstream-die"
	// outcomeBudgetKill: the stream crosses its ceiling and is cut by us.
	outcomeBudgetKill outcome = "budget-kill"
	// outcomePanic: the relay panics mid-write. Exercises the claim that the
	// deferred finalizer runs during panic unwinding.
	outcomePanic outcome = "panic"
	// outcomeNoUpstream: upstream is unreachable, so nothing is ever relayed.
	outcomeNoUpstream outcome = "no-upstream"
)

// The panic and abort writers already exist in main_test.go
// (panickingResponseWriter, abortingResponseWriter) and are reused here rather
// than reimplemented -- they model exactly the two faults this matrix needs, and
// a second copy would be one more thing to keep in step with the relay.

// TestMoneyPathInvariant drives every (reserved, measured, outcome) combination
// through the real handler and asserts I1-I5 on each.
func TestMoneyPathInvariant(t *testing.T) {
	// Reserved is driven through max_tokens, which is what actually sizes a
	// reservation. Values span one token to the default, including awkward ones.
	reservedCases := []int{1, 7, 64, 999, defaultReservationTokens}
	// Measured is what upstream reports in its usage frame: nothing, a sliver,
	// under, exact, and an overshoot that forces a true-up rather than a refund.
	measuredFor := func(reserved int) []int {
		return []int{0, 1, reserved / 2, reserved, reserved * 3}
	}
	outcomes := []outcome{
		outcomeComplete, outcomeAbort, outcomeUpstreamDie,
		outcomeBudgetKill, outcomePanic, outcomeNoUpstream,
	}

	for _, oc := range outcomes {
		for _, reserved := range reservedCases {
			for _, measured := range measuredFor(reserved) {
				name := fmt.Sprintf("%s/reserved=%d/measured=%d", oc, reserved, measured)
				t.Run(name, func(t *testing.T) {
					runMoneyPathCase(t, oc, reserved, measured)
				})
			}
		}
	}
}

func runMoneyPathCase(t *testing.T, oc outcome, reserved, measured int) {
	t.Helper()
	const keyID = "bbbb2222-0000-0000-0000-000000000001"

	// tokenLimit sets the ceiling: requestTokenCeiling is reserved+headroom, and
	// headroom is tokenLimit-tokensUsed after the reservation, so the ceiling
	// works out to tokenLimit. A budget kill therefore needs a limit the stream
	// can cross; every other outcome needs one it cannot.
	tokenLimit := int64(1_000_000)
	if oc == outcomeBudgetKill {
		tokenLimit = int64(reserved) + 5
	}

	store := &fakeUsageStore{tokenLimit: tokenLimit}
	supabase, _ := newFakeSupabase(store, keyID)
	defer supabase.Close()

	upstream := newOutcomeUpstream(oc, measured, tokenLimit)
	defer upstream.Close()

	upstreamURL := upstream.URL
	if oc == outcomeNoUpstream {
		// A port nothing is listening on: the forward fails before a byte moves.
		upstreamURL = "http://127.0.0.1:1"
	}

	p := newTestProxy(supabase.URL, upstreamURL)
	req := newAuthorizedRequest(zdrBody(fmt.Sprintf(`"max_tokens":%d,`, reserved)))

	var w http.ResponseWriter
	switch oc {
	case outcomeAbort:
		w = &abortingResponseWriter{failOn: "usage"}
	case outcomePanic:
		// Faults on the chunk carrying "hi", i.e. mid-relay, after the
		// reservation exists and after streamSSE has been entered.
		w = &panickingResponseWriter{panicOn: "hi"}
	default:
		w = httptest.NewRecorder()
	}

	// The panic case must escape the handler (there is no middleware here), be
	// caught, and STILL have finalized -- that is the property under test, not an
	// incidental detail of how this test is written.
	func() {
		defer func() {
			r := recover()
			if oc == outcomePanic && r == nil {
				t.Fatalf("expected the synthetic relay fault to panic")
			}
			if oc != outcomePanic && r != nil {
				t.Fatalf("unexpected panic: %v", r)
			}
		}()
		p.handleChatCompletions(w, req)
	}()

	waitForLedgerSettled(t, store, 500*time.Millisecond)

	// --- I1: finalized exactly once -----------------------------------------
	opened := p.metrics.reservationsOpened.Value()
	finalized := p.metrics.reservationsFinalized.Value()
	if opened != 1 {
		t.Fatalf("reservations opened = %d, want 1 (the case did not reach the money path at all)", opened)
	}
	if finalized != 1 {
		t.Errorf("I1 violated: reservation opened %d time(s) but finalized %d -- "+
			"a reservation that is never discharged stays charged at the RESERVED amount "+
			"forever, which is the P0 defect", opened, finalized)
	}

	// --- I2: the outbox ends empty ------------------------------------------
	if n := store.openPendingCount(); n != 0 {
		t.Errorf("I2 violated: %d open pending_corrections row(s) left behind -- the sweep "+
			"will report this as an abandoned reservation ~20 minutes from now", n)
	}

	store.mu.Lock()
	finalUsed := store.tokensUsed
	corrections := append([]int64(nil), store.corrections...)
	store.mu.Unlock()

	if len(corrections) != 1 {
		t.Fatalf("expected exactly 1 correction, got %d: %v", len(corrections), corrections)
	}
	delta := corrections[0]

	// --- I3: tokens_used never ends negative --------------------------------
	if finalUsed < 0 {
		t.Errorf("I3 violated: tokens_used ended at %d. A refund larger than the "+
			"reservation is free, repeatable quota", finalUsed)
	}
	if want := int64(reserved) + delta; finalUsed != want {
		t.Errorf("ledger does not reconcile: tokens_used = %d, but reserved(%d) + delta(%d) = %d",
			finalUsed, reserved, delta, want)
	}

	// --- I4 / I5 --------------------------------------------------------------
	//
	// The split that matters is NOT produced-output vs not. It is whether a
	// MEASUREMENT was observed:
	//
	//   measured output    -> settle at the measurement, refunding the rest. This
	//                         is the reservation model working as designed
	//                         (reserve 4096, use 137, refund 3959) and it is
	//                         correct for the final figure to fall below the
	//                         reservation.
	//   unmeasured output  -> keep the whole reservation. There is no honest
	//                         number to settle at, and refunding would make
	//                         "lose the usage frame" a way to get free inference.
	//   no output at all   -> refund in full.
	//
	// The first cut of this file collapsed the first two into "produced output =>
	// never below reserved", and the matrix failed on eight completed-and-measured
	// cases. The test was wrong, not the proxy: that assertion would have forbidden
	// the ordinary refund the whole design exists to perform.
	producedOutput := oc != outcomeNoUpstream
	// Only a completed stream carries its usage frame all the way through the
	// relay; every other outcome cuts the stream before or instead of it.
	measurementObserved := oc == outcomeComplete && measured > 0

	switch {
	case measurementObserved:
		if finalUsed != int64(measured) {
			t.Errorf("a completed, measured request must settle at its measured usage: "+
				"tokens_used = %d, want %d", finalUsed, measured)
		}

	case producedOutput:
		if finalUsed < int64(reserved) {
			t.Errorf("I4 violated: %s produced output that was never measured, but ended at "+
				"tokens_used = %d, below its reservation of %d. Losing the usage frame must "+
				"not be a way to get unmetered inference", oc, finalUsed, reserved)
		}

	default:
		if finalUsed != 0 {
			t.Errorf("I5 violated: %s relayed nothing, so the whole reservation must come "+
				"back; tokens_used = %d, want 0", oc, finalUsed)
		}
		if delta != int64(-reserved) {
			t.Errorf("I5 violated: delta = %d, want %d (a full refund)", delta, -reserved)
		}
	}
}

// newOutcomeUpstream builds the fake upstream for one outcome.
func newOutcomeUpstream(oc outcome, measured int, tokenLimit int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)

		emit := func(s string) {
			io.WriteString(w, s)
			if fl != nil {
				fl.Flush()
			}
		}

		switch oc {
		case outcomeBudgetKill:
			// Comfortably past the ceiling (which equals tokenLimit here).
			for i := int64(0); i < tokenLimit+50; i++ {
				emit(fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":\"t%d \"}}]}\n\n", i))
				select {
				case <-r.Context().Done():
					return
				default:
				}
			}

		case outcomeUpstreamDie:
			emit("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
			emit("data: {\"choices\":[{\"delta\":{\"content\":\"there\"}}]}\n\n")
			// No usage frame, no [DONE]: the provider drops the connection.
			panic(http.ErrAbortHandler)

		default:
			// complete / abort / panic all get a well-formed stream; what differs
			// is what the CLIENT side does with it.
			emit("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
			if measured > 0 {
				emit(fmt.Sprintf("data: {\"usage\":{\"total_tokens\":%d}}\n\n", measured))
			}
			emit("data: [DONE]\n\n")
		}

		// Keeps the handler alive briefly so an aborting client's write error is
		// observed by the relay rather than racing the handler's return.
		time.Sleep(5 * time.Millisecond)
	}))
}
