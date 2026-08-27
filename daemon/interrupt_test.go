package main

import (
	"testing"
	"time"

	"codeterminal/protocol"
)

// STOPPING A TURN HAS TO STOP THE WORK, not just the watching.
//
// An interrupt reaches the daemon as a closed connection -- the clients' stop
// key (interruptTurn in clients/tui/chat.go) cancels the request context, which
// closes the socket. Before runAgentTurn cancelled its own context on a failed
// write, that hung-up client cost real money and real time: the tool in flight
// ran to completion and the loop paid for ANOTHER model call to narrate the
// result to a socket nobody was reading, because the only thing that could
// notice was the token callback, and no tokens flow while a tool runs.
//
// One upstream call is the whole assertion: the first one, the one that asked
// for the tool. A second means the turn carried on past the hang-up.
func TestAClientThatHangsUpMidTurnStopsTheAgentTurn(t *testing.T) {
	base, calls, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`),
		textSSE("inside.txt says HELLO."),
	)
	sockAddr, _, _ := agentSocketServer(t, base, MCPConfig{
		Enabled: true,
		// Allowed rather than asked, so the turn is stopped by the hang-up
		// itself and not by an approval question going unanswered.
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": PolicyAllow}},
	})
	_, dec, conn := agentClient(t, sockAddr, "what is in inside.txt?", []string{protocol.CapToolApproval})

	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// One message is enough to know the turn is genuinely under way; what it is
	// does not matter (a tool-activity notice, in practice).
	var tok protocol.TokenResponse
	if err := dec.Decode(&tok); err != nil {
		t.Fatalf("the turn never started: %v", err)
	}

	// The interrupt, exactly as a client performs one.
	if err := conn.Close(); err != nil {
		t.Fatalf("closing the client connection: %v", err)
	}

	// Long enough that a turn which did NOT stop would have finished its tool
	// and made its second call well inside the window.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() > 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("upstream model calls = %d after the client hung up, want 1 -- the turn kept working for a client that had gone", got)
	}
}
