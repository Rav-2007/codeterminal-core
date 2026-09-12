package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"codeterminal/protocol"
)

// What threading a context into the request path actually buys, pinned as
// measurements rather than left as an assumption.
//
// The assumption worth disproving is that threading a context makes a slow
// query stoppable. It does not, and the difference is not in the plumbing --
// modernc.org/sqlite wires sqlite3_interrupt to ctx.Done (interruptOnDone,
// sqlite.go:800) and that mechanism works. It is that sqlite3_interrupt is
// polled in the VDBE loop, and an FTS5 trigram match over a huge quoted phrase
// spends its time somewhere that never polls it.
//
// Both halves are asserted, in opposite directions, so the boundary between
// them cannot move without a test noticing.
//
// THE RETRIEVAL BOUND SITS IN FRONT OF SearchTurns, so a test that wants to
// observe ENGINE behaviour must go around it. When maxLexicalQueryChars landed,
// all three tests below still used a 200,000-character query and two of them
// went on PASSING -- for the wrong reason, because the bound refused the query
// in 171 microseconds and no FTS5 match ever ran. One failed loudly and that is
// the only reason the other two were looked at. Queries here are therefore
// either deliberately UNDER the bound (when the point is cancellation) or issued
// straight against the database (when the point is what SQLite does).
//
// EVERY DURATION HERE IS WALL-CLOCK and the bounds are deliberately loose:
// each compares an interrupted run against a cancel point an order of magnitude
// away, so machine speed cannot decide the outcome.

// The plumbing half. An already-cancelled context must stop a search before it
// starts, which is what makes daemon shutdown able to reach this work at all --
// it previously ran on context.Background() and could not be reached.
// alreadyCancelledCeiling bounds a call made with a context that was cancelled
// BEFORE it started. There is no work to interrupt: the driver should observe
// the dead context and return, so anything approaching a second means it ran
// the query first and checked afterwards.
//
// A stated constant rather than a derivation, because there is nothing to derive
// from -- no timeout, no delay, no server. What it protects is the difference
// between "checked the context" and "ran, then noticed", and two seconds is
// roughly 30x the measured return while staying far below the multi-second cost
// of actually executing these queries.
const alreadyCancelledCeiling = 2 * time.Second

// cteCancelAfter is when the recursive-CTE test cancels; the ceiling below is a
// multiple of it, so moving one moves the other.
const cteCancelAfter = 500 * time.Millisecond

func TestSQLiteCancellation_ACancelledContextStopsASearchBeforeItRuns(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	if err := mem.AppendTurn(t.Context(), "/ws", "user", "hello goroutines"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// UNDER the bound on purpose: an over-bound query is refused before the
	// context is ever consulted, which would make this pass without the
	// cancellation it claims to test.
	start := time.Now()
	_, err := mem.SearchTurns(ctx, "/ws", strings.Repeat("x", maxLexicalQueryChars/2), 5)
	elapsed := time.Since(start)

	if errors.Is(err, errLexicalQueryTooLong) {
		t.Fatal("this query is over the lexical bound, so it never reached the driver; " +
			"the test is measuring the bound rather than cancellation")
	}
	if err == nil {
		t.Fatal("a search on a cancelled context returned no error; the context is not reaching the driver")
	}
	// THE PROPERTY, NOT THE CLOCK. "It returned fast" is satisfied by a driver
	// that failed for an unrelated reason. Only the error identity says the
	// cancellation is what stopped it.
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled -- something other than the context ended this query", err)
	}
	if elapsed > alreadyCancelledCeiling {
		t.Errorf("a search on an already-cancelled context still took %s; the query ran anyway", elapsed)
	}
}

// The half that works. sqlite3_interrupt is polled in the VDBE loop, so a
// recursive CTE abandons its work promptly.
func TestSQLiteCancellation_ReachesTheVDBELoop(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(cteCancelAfter); cancel() }()

	start := time.Now()
	var n int
	err := mem.db.QueryRowContext(ctx,
		`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 2000000000)
		 SELECT count(*) FROM c`).Scan(&n)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a two-billion-row recursive CTE completed; it was supposed to be cancelled")
	}
	// Uncancelled this runs for minutes. Measured 511 ms when cancelled at 500 ms,
	// so 20x the cancel point separates "the interrupt worked" from "it did not"
	// without asserting how fast any particular machine is.
	if elapsed > 20*cteCancelAfter {
		t.Errorf("cancelled at 500ms, the CTE ran for %s; sqlite3_interrupt is no longer reaching "+
			"the VDBE loop, and every claim about cancellation in serveConn depends on it", elapsed)
	}
}

// The half that does not work, asserted so it cannot quietly stop being true.
//
// A FAILURE HERE IS GOOD NEWS, not a regression: it means a newer SQLite or
// driver polls the interrupt inside FTS5, and serveConn's comment plus the
// reasoning behind the retrieval bound should be revisited.
func TestSQLiteCancellation_DoesNotReachAnFTS5PhraseMatch(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	for i := range 50 {
		if err := mem.AppendTurn(t.Context(), "/ws", "user",
			fmt.Sprintf("turn %d about goroutines and channels", i)); err != nil {
			t.Fatal(err)
		}
	}
	// ISSUED DIRECTLY AGAINST THE DATABASE, not through SearchTurns, because
	// SearchTurns now refuses this length outright. What is characterised here is
	// SQLite, not the daemon's policy in front of it, so the policy is bypassed
	// rather than worked around. The phrase is built exactly as SearchTurns
	// builds one.
	//
	// 200k measured at ~3.6 s uncancelled ON THIS LINUX BOX, which is a
	// wall-clock number about one machine and not a property of SQLite.
	const cancelAfter = 200 * time.Millisecond
	// DERIVED FROM cancelAfter, not chosen. If the interrupt were honoured the
	// query would return within a small multiple of the cancel; requiring 5x
	// separates "ignored the interrupt" from "honoured it slowly" without
	// asserting anything about how fast this particular machine is. The previous
	// form compared against a flat 1500 ms, and that is what broke on Windows.
	const honouredWithin = 5 * cancelAfter
	phrase := `"` + strings.Repeat("x", 200000) + `"`
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(cancelAfter); cancel() }()

	start := time.Now()
	var role string
	err := mem.db.QueryRowContext(ctx,
		`SELECT t.role FROM turns_fts JOIN turns t ON t.id = turns_fts.rowid
		 WHERE turns_fts MATCH ? LIMIT 1`, phrase).Scan(&role)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("the query reported no error at all; database/sql is not observing the context")
	}
	// THE ERROR MUST BE CANCELLATION BEFORE ELAPSED TIME MEANS ANYTHING.
	//
	// This check did not exist, and its absence is what made this test wrong
	// rather than merely fragile. It asserted only that SOME error came back
	// quickly, then concluded SQLite had started honouring the interrupt.
	//
	// Measured on windows-latest in CI 2026-09-09: the query returned an error
	// after 120.2 ms -- BEFORE the 200 ms cancel had even fired, so the error
	// could not possibly have been the interrupt. That platform refuses a
	// 200,000-character phrase for its own reasons, and the test read it as a
	// change in SQLite's behaviour and advised acting on it.
	if !errors.Is(err, context.Canceled) {
		t.Skipf("the query failed after %s for a reason that is not cancellation (%v).\n"+
			"This platform refuses the phrase before the interrupt is relevant, so there is nothing "+
			"to characterise here -- it is NOT evidence that SQLite's interrupt behaviour changed.", elapsed, err)
	}

	if elapsed < honouredWithin {
		t.Errorf("cancelled at %s, the FTS5 match returned after %s (under the %s bar) with a genuine "+
			"context.Canceled -- SQLite now HONOURS the interrupt inside a phrase match.\n"+
			"This is an improvement, not a failure. Re-measure, then update serveConn's cancellation comment.\n"+
			"IT DOES NOT ON ITS OWN JUSTIFY REMOVING THE LEXICAL BOUND, and the previous wording of this "+
			"message said it might. Cancellation only helps a client that actually disconnects; the bound "+
			"also covers the caller that waits, and gatherContext, which is not cancelled mid-turn.",
			cancelAfter, elapsed, honouredWithin)
	}
}

// The plumbing reaches the handler, not just the store: handleSearch takes the
// connection's context now, so a daemon shutting down does not wait on a search.
func TestHandleSearch_UsesTheContextItIsGiven(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	if err := mem.AppendTurn(t.Context(), "/ws", "user", "hello"); err != nil {
		t.Fatal(err)
	}
	srv := &Server{workspace: "/ws", memory: mem, logger: discardLogger()}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// UNDER the bound, for the reason given at the top of this file: an
	// over-bound query is refused before the context matters, and this test would
	// then pass with no context threading at all.
	var buf strings.Builder
	start := time.Now()
	srv.handleSearch(ctx, json.NewEncoder(&buf), protocol.SearchRequest{
		Search: true, Query: strings.Repeat("x", maxLexicalQueryChars/2), Limit: 5,
	})
	elapsed := time.Since(start)

	var resp protocol.SearchResponse
	if err := json.Unmarshal([]byte(buf.String()), &resp); err != nil {
		t.Fatalf("decoding response: %v (raw %q)", err, buf.String())
	}
	if resp.Error == "" {
		t.Error("a search on a cancelled context answered as though it had succeeded")
	}
	if elapsed > alreadyCancelledCeiling {
		t.Errorf("handleSearch ran for %s on an already-cancelled context; it is not using the "+
			"context it was given", elapsed)
	}
}
