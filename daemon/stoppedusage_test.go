package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"mochiii/protocol"
)

// A STOPPED TURN WAS BILLED, AND /usage MUST SAY SO.
//
// A turn the user stops sends no Done -- the client hung up -- and the bill
// used to ride only on the Done. So every call a stopped agent turn had already
// made vanished from /usage, and the call the stop cut off (which never gets
// its usage chunk) with it. The daemon now keeps the stopped turn's bill and
// sends it on the next Done: the finished calls exactly, the cut-off one
// counted with its cost unknown.

// finalMessage reads a turn to its Done.
func finalMessage(t *testing.T, sockAddr protocol.Address, prompt string, caps []string) protocol.TokenResponse {
	t.Helper()
	_, dec, conn := agentClient(t, sockAddr, prompt, caps)
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("reading the stream: %v", err)
		}
		if tok.Done {
			return tok
		}
	}
}

// stopAfter sends prompt, reads until until() accepts a message, and hangs up.
func stopAfter(t *testing.T, sockAddr protocol.Address, prompt string, caps []string, until func(protocol.TokenResponse) bool) {
	t.Helper()
	_, dec, conn := agentClient(t, sockAddr, prompt, caps)
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("reading the stream: %v", err)
		}
		if tok.Done {
			t.Fatalf("the turn finished before it could be stopped: %+v", tok)
		}
		if until(tok) {
			break
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}

// waitForStoppedBill waits until the daemon has put a stopped turn's bill by.
func waitForStoppedBill(t *testing.T, srv *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		srv.stopped.mu.Lock()
		n := len(srv.stopped.turns)
		srv.stopped.mu.Unlock()
		if n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the stopped turn's bill was never kept")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Neuter check: drop the s.stopped.hold in runAgentTurn's interrupt branch and
// the next Done carries nothing; drop addUnreported in streamCompletion and the
// cut-off call is not counted.
func TestAStoppedAgentTurnIsBilledOnTheNextDone(t *testing.T) {
	base, _, _ := agentUpstream(t,
		// Turn 1, step 1: a tool call, billed.
		withUsage(toolCallSSE("c1", "builtin__no_such_tool", `{}`), 1000, 20, 0.001),
		// Turn 1, step 2: stopped while its answer streams, so no bill arrives.
		[]string{
			`data: {"choices":[{"delta":{"content":"the answer starts"}}]}`,
			ssePause,
			`data: {"choices":[{"delta":{"content":" and goes on"}}]}`,
			ssePause,
			`data: {"choices":[{"delta":{"content":" and ends"},"finish_reason":"stop"}]}`,
			`data: [DONE]`,
		},
		// Turn 2 and turn 3 finish normally.
		withUsage(textSSE("second answer"), 500, 10, 0.0005),
		withUsage(textSSE("third answer"), 600, 10, 0.0006),
	)
	sockAddr, _, srv := agentSocketServer(t, base, MCPConfig{Enabled: true})
	caps := []string{protocol.CapToolApproval}

	stopAfter(t, sockAddr, "go", caps, func(tok protocol.TokenResponse) bool { return tok.Token != "" })
	waitForStoppedBill(t, srv)

	done := finalMessage(t, sockAddr, "again", caps)
	if len(done.StoppedUsage) != 1 {
		t.Fatalf("the next Done carried %d stopped bill(s), want 1: %+v", len(done.StoppedUsage), done.StoppedUsage)
	}
	s := done.StoppedUsage[0]
	if s.Calls != 2 || s.PromptTokens != 1000 || fmt.Sprintf("%.4f", s.CostUSD) != "0.0010" || !s.CostMissing {
		t.Errorf("stopped bill = %+v, want step 1's bill plus the cut-off call, with the cost a lower bound", s)
	}
	if u := done.Usage; u == nil || u.Calls != 1 || u.CostMissing {
		t.Errorf("the turn's own bill = %+v, want its one call, cost known", u)
	}

	if again := finalMessage(t, sockAddr, "once more", caps); len(again.StoppedUsage) != 0 {
		t.Errorf("a stopped bill was sent twice: %+v", again.StoppedUsage)
	}
}

// The plain path, stopped while the model thinks: one call, cost unknown.
//
// Neuter check: drop the s.stopped.hold in the plain path's interrupt branch.
func TestAStoppedPlainTurnIsBilledOnTheNextDone(t *testing.T) {
	think := `data: {"choices":[{"delta":{"reasoning":"hmm"}}]}`
	base, _, _ := agentUpstream(t,
		[]string{think, ssePause, think, ssePause, think, `data: {"choices":[{"delta":{"content":"late"},"finish_reason":"stop"}]}`, `data: [DONE]`},
		withUsage(textSSE("second answer"), 500, 10, 0.0005),
	)
	sockAddr, _, srv := agentSocketServer(t, base, MCPConfig{})

	stopAfter(t, sockAddr, "why?", nil, func(tok protocol.TokenResponse) bool { return tok.Reasoning != "" })
	waitForStoppedBill(t, srv)

	done := finalMessage(t, sockAddr, "and?", nil)
	if len(done.StoppedUsage) != 1 || done.StoppedUsage[0].Calls != 1 || !done.StoppedUsage[0].CostMissing {
		t.Errorf("stopped bills = %+v, want one call with its cost unknown", done.StoppedUsage)
	}
}

// A stream that ends normally without a usage chunk -- a local server that
// sends none -- still reports no usage at all. Only a call that was CUT OFF is
// counted without a bill.
func TestOnlyACutOffCallIsCountedWithoutABill(t *testing.T) {
	base, _, _ := agentUpstream(t, textSSE("complete, no usage"), textSSE("cut off"))
	msgs := []chatMessage{{Role: "user", Content: "hi"}}

	ctx, tally := withUsageTally(context.Background())
	if _, err := streamCompletion(ctx, base, "k", "m", msgs, nil, providerRouting{},
		func(string) error { return nil }, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if u := tally.report("m", 0); u != nil {
		t.Errorf("a complete stream with no usage reported %+v, want none", u)
	}

	ctx, tally = withUsageTally(context.Background())
	_, err := streamCompletion(ctx, base, "k", "m", msgs, nil, providerRouting{},
		func(string) error { return errors.New("broken pipe") }, nil, nil, nil)
	if err == nil {
		t.Fatal("a failed token write did not fail the call")
	}
	if u := tally.report("m", 0); u == nil || u.Calls != 1 || !u.CostMissing {
		t.Errorf("a cut-off call reported %+v, want one call with its cost unknown", u)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// A Done that cannot be written loses nothing: the stopped bills it carried,
// and its own, wait for the next one.
func TestADoneThatCannotBeWrittenKeepsItsBills(t *testing.T) {
	s := &Server{}
	s.stopped.hold(&protocol.TurnUsage{Calls: 1, CostUSD: 0.001})
	err := s.sendDone(json.NewEncoder(failingWriter{}), protocol.TokenResponse{Done: true,
		Usage: &protocol.TurnUsage{Calls: 2, CostUSD: 0.002}})
	if err == nil {
		t.Fatal("the write did not fail")
	}
	got := s.stopped.take()
	if len(got) != 2 || got[0].Calls != 1 || got[1].Calls != 2 {
		t.Errorf("held after a failed Done = %+v, want the earlier stopped bill, then this turn's", got)
	}
}
