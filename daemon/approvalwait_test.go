package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"mochiii/protocol"
)

// AN APPROVAL QUESTION WAITS FOR ITS ANSWER, HOWEVER LONG THAT TAKES (owner's
// decision, 2026-11-21; see the comment in connApprover.Ask). These tests hold
// the three things that make that safe: no deadline is armed while the
// question stands, a client that goes away is a no at once, and a shutdown
// still ends the wait -- including the shutdown that lands just before a read
// re-arms its deadline.

// mustFinish runs f and fails the test if it has not returned within limit.
// The properties below are about waits ending; one that does not end must show
// up as a failure, not as a test run that never finishes.
func mustFinish(t *testing.T, limit time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s did not end within %s", what, limit)
	}
}

// deadlineRecorder records every read deadline armed on the connection.
type deadlineRecorder struct {
	net.Conn
	mu    sync.Mutex
	reads []time.Time
}

func (d *deadlineRecorder) SetReadDeadline(at time.Time) error {
	d.mu.Lock()
	d.reads = append(d.reads, at)
	d.mu.Unlock()
	return d.Conn.SetReadDeadline(at)
}

func (d *deadlineRecorder) armed() []time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.reads)
}

// productionApprover is connApprover as serveConn builds it -- no idleTimeout
// of its own -- over a connection whose MACHINE idle timeout is machineIdle.
func productionApprover(t *testing.T, machineIdle time.Duration) (*connApprover, *limitedConn, *deadlineRecorder, net.Conn) {
	t.Helper()
	clientEnd, daemonEnd := net.Pipe()
	t.Cleanup(func() { _ = clientEnd.Close(); _ = daemonEnd.Close() })
	rec := &deadlineRecorder{Conn: daemonEnd}
	lc := &limitedConn{Conn: rec, remaining: 1 << 20, idleTimeout: machineIdle}
	return &connApprover{
		enc:    json.NewEncoder(lc),
		dec:    json.NewDecoder(lc),
		lc:     lc,
		logger: log.New(io.Discard, "", 0),
	}, lc, rec, clientEnd
}

// No deadline is armed while the question stands, and an answer given long
// after the connection's ordinary idle limit still counts. Before, every read
// of the answer armed a five-minute deadline.
func TestAnApprovalQuestionHasNoTimeLimit(t *testing.T) {
	const machineIdle = 20 * time.Millisecond
	appr, lc, rec, client := productionApprover(t, machineIdle)
	req := approvalRequest("c1", `{"path":"a.txt"}`)

	go func() {
		var msg protocol.TokenResponse
		if err := json.NewDecoder(client).Decode(&msg); err != nil {
			return
		}
		time.Sleep(10 * machineIdle) // the person reads the arguments
		_, _ = client.Write(append(answer(req, protocol.ApprovalApprove, true), '\n'))
	}()

	var got approvalDecision
	mustFinish(t, 5*time.Second, "an answered question", func() { got = appr.Ask(context.Background(), req) })
	if !got.approved() {
		t.Fatalf("an answer given after %s was not honoured: %+v -- the question had a time limit", 10*machineIdle, got)
	}
	armed := rec.armed()
	if len(armed) == 0 {
		t.Fatal("no read was made for the answer; this test cannot see the deadline")
	}
	for i, at := range armed {
		if !at.IsZero() {
			t.Errorf("read %d of the answer armed a deadline %s ahead; a question must wait for as long as the person takes",
				i, time.Until(at).Round(time.Millisecond))
		}
	}
	// And the machine-scale deadline is back for whatever is read next.
	if lc.noReadDeadline {
		t.Error("the connection was left with no read deadline after the question was answered")
	}
}

// A client that goes away -- a terminal closed, a TUI quit, a chat panel
// closed -- is a no AT ONCE. This is what makes having no limit safe: a limit
// was never what ended this wait.
func TestAClientThatGoesAwayIsANoAtOnce(t *testing.T) {
	appr, _, _, client := productionApprover(t, time.Minute)
	go func() {
		var msg protocol.TokenResponse
		_ = json.NewDecoder(client).Decode(&msg)
		_ = client.Close()
	}()

	var got approvalDecision
	mustFinish(t, 5*time.Second, "a question whose client went away", func() {
		got = appr.Ask(context.Background(), approvalRequest("c1", `{"path":"a.txt"}`))
	})
	if got.approved() {
		t.Fatal("a client that went away approved a call")
	}
	if !appr.closed {
		t.Error("the channel stayed open after its client went away")
	}
}

// A shutdown ends a question that has no time limit -- with no deadline to
// fall back on, nothing else would.
func TestShutdownEndsAQuestionThatHasNoTimeLimit(t *testing.T) {
	appr, _, _, client := productionApprover(t, time.Minute)
	go func() {
		var msg protocol.TokenResponse
		_ = json.NewDecoder(client).Decode(&msg)
		select {} // read the question, never answer
	}()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	var got approvalDecision
	mustFinish(t, 5*time.Second, "a question during a shutdown", func() {
		got = appr.Ask(ctx, approvalRequest("c1", `{"path":"a.txt"}`))
	})
	if got.approved() {
		t.Fatal("a question ended by a shutdown approved a call")
	}
}

// THE GAP A LIMIT USED TO COVER. A shutdown moves the read deadline into the
// past; a read that arms its own deadline a moment later used to overwrite
// that, and then waited out the five minutes. With no limit it would have
// waited forever. cancelReads latches, and Read checks the latch after arming,
// so the order the two happen in no longer matters. This is the losing order:
// cancelled first, armed second.
func TestACancelledConnectionCannotBeReArmed(t *testing.T) {
	clientEnd, daemonEnd := net.Pipe()
	defer clientEnd.Close()
	defer daemonEnd.Close()
	lc := &limitedConn{Conn: daemonEnd, remaining: 1 << 20, idleTimeout: time.Minute}
	defer lc.withoutReadDeadline()()

	lc.cancelReads()

	var err error
	mustFinish(t, 5*time.Second, "a read after a shutdown", func() {
		_, err = lc.Read(make([]byte, 16))
	})
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("read after cancelReads = %v, want a deadline error", err)
	}
}

// A client finds out from the handshake that this daemon waits, which is what
// lets it say an unsendable answer did not expire (clients/tui/approvallost.go).
func TestTheHandshakeSaysApprovalsWaitForTheirAnswer(t *testing.T) {
	s := &Server{logger: discardLogger()}
	if !slices.Contains(s.features(), protocol.FeatureApprovalWaitsForAnswer) {
		t.Errorf("features %q do not include %q", s.features(), protocol.FeatureApprovalWaitsForAnswer)
	}
}

// Over the real socket, with the connection's ordinary idle limit made short:
// the person answers after several of them and the call still runs. The wiring
// half of TestAnApprovalQuestionHasNoTimeLimit -- serveConn's own limitedConn,
// its own approver.
func TestAnAnswerAfterTheIdleLimitStillRunsTheCall(t *testing.T) {
	const machineIdle = 150 * time.Millisecond
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("It says HELLO."),
	)
	sockAddr, _, srv := agentSocketServerConfigured(t, base, MCPConfig{Enabled: true}, discardLogger(), "",
		func(s *Server) { s.connIdleTimeout = machineIdle })
	enc, dec, conn := agentClient(t, sockAddr, "what is in inside.txt?", []string{protocol.CapToolApproval})
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}

	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("reading the stream: %v", err)
		}
		if tok.ToolApproval != nil {
			time.Sleep(4 * machineIdle)
			if err := enc.Encode(protocol.ToolApprovalResponse{
				ProtocolVersion: protocol.ProtocolVersion,
				Approval:        true,
				CallID:          tok.ToolApproval.CallID,
				ArgumentsSHA256: tok.ToolApproval.ArgumentsSHA256,
				Decision:        protocol.ApprovalApprove,
			}); err != nil {
				t.Fatalf("sending the approval: %v", err)
			}
		}
		if tok.Done {
			if tok.Error != "" {
				t.Fatalf("the turn ended in an error: %s", tok.Error)
			}
			break
		}
	}
	if got := srv.counters.snapshot().ToolCallsApproved; got != 1 {
		t.Errorf("ToolCallsApproved = %d, want 1 -- an answer given after %s did not count", got, 4*machineIdle)
	}
}
