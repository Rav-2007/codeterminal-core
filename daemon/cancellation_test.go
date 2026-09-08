package main

import (
	"context"
	"encoding/json"
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
// EVERY DURATION HERE IS WALL-CLOCK and the bounds are deliberately loose:
// each compares an interrupted run against a cancel point an order of magnitude
// away, so machine speed cannot decide the outcome.

// The plumbing half. An already-cancelled context must stop a search before it
// starts, which is what makes daemon shutdown able to reach this work at all --
// it previously ran on context.Background() and could not be reached.
func TestSQLiteCancellation_ACancelledContextStopsASearchBeforeItRuns(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	if err := mem.AppendTurn(t.Context(), "/ws", "user", "hello goroutines"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := mem.SearchTurns(ctx, "/ws", strings.Repeat("x", 200000), 5)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a search on a cancelled context returned no error; the context is not reaching the driver")
	}
	if elapsed > 2*time.Second {
		t.Errorf("a search on an already-cancelled context still took %s; the query ran anyway", elapsed)
	}
}

// The half that works. sqlite3_interrupt is polled in the VDBE loop, so a
// recursive CTE abandons its work promptly.
func TestSQLiteCancellation_ReachesTheVDBELoop(t *testing.T) {
	mem, _ := openTestMemoryStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(500 * time.Millisecond); cancel() }()

	start := time.Now()
	var n int
	err := mem.db.QueryRowContext(ctx,
		`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 2000000000)
		 SELECT count(*) FROM c`).Scan(&n)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a two-billion-row recursive CTE completed; it was supposed to be cancelled")
	}
	// Uncancelled this runs for minutes. Measured 511 ms when cancelled at 500 ms.
	if elapsed > 10*time.Second {
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
	// 200k measured at ~3.6 s uncancelled, so a cancel at 200 ms is unambiguous.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()

	start := time.Now()
	_, err := mem.SearchTurns(ctx, "/ws", strings.Repeat("x", 200000), 5)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("the search reported no error at all; database/sql is not observing the context")
	}
	if elapsed < 1500*time.Millisecond {
		t.Errorf("cancelled at 200ms, the FTS5 match returned after %s -- it now HONOURS the interrupt.\n"+
			"This is an improvement, not a failure. Re-measure, then update serveConn's cancellation "+
			"comment and reconsider whether the retrieval bound still needs to be a bound.", elapsed)
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

	var buf strings.Builder
	start := time.Now()
	srv.handleSearch(ctx, json.NewEncoder(&buf), protocol.SearchRequest{
		Search: true, Query: strings.Repeat("x", 200000), Limit: 5,
	})
	elapsed := time.Since(start)

	var resp protocol.SearchResponse
	if err := json.Unmarshal([]byte(buf.String()), &resp); err != nil {
		t.Fatalf("decoding response: %v (raw %q)", err, buf.String())
	}
	if resp.Error == "" {
		t.Error("a search on a cancelled context answered as though it had succeeded")
	}
	if elapsed > 2*time.Second {
		t.Errorf("handleSearch ran for %s on an already-cancelled context; it is not using the "+
			"context it was given", elapsed)
	}
}
