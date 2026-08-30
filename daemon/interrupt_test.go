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
// for the tools. A second means the turn carried on past the hang-up.
//
// A PAUSE IN THE MODEL RESPONSE, AND THAT IS THE TEST'S CORRECTNESS ARGUMENT.
//
// The daemon learns the client is gone from a WRITE THAT FAILS -- there is no
// cancel message on this wire, and agentturn.go says so. Written naively, this
// test races the daemon: the client decodes one message and closes, and the
// daemon must not have already finished its instant read_file and dialled the
// next model call. It was green 40 times here and red on a CI runner.
//
// MEASURED, by injecting a delay between the client's decode and its close:
// with a single tool call, TWO MILLISECONDS of client slowness flipped it to
// 6 failures in 10, and five milliseconds to 9 in 10. Batching eight tool calls
// to buy more writes moved that to five milliseconds -- still far inside what a
// loaded runner does to a goroutine, which is why the batch was not the fix.
//
// So the response now PAUSES, at a point the test chooses: a text token first,
// then ssePause, then the tool call. The client gets its token and closes
// during a 300ms window in which the daemon cannot proceed, because the model
// response it is reading has not finished arriving. The next thing the daemon
// writes is the tool-activity notice, and by then the close landed 300ms ago.
// The race is not narrowed, it is removed -- the ordering is now enforced by
// the test rather than hoped for.
//
// The tool path is still what is under test: the failing write is the tool
// notice, and what stops the second model call is agentloop re-checking
// ctx.Err() after the write cancelled the turn.
func TestAClientThatHangsUpMidTurnStopsTheAgentTurn(t *testing.T) {
	// A token, then a gap the daemon must wait out, then the tool call.
	firstResponse := append([]string{
		`data: {"choices":[{"delta":{"content":"looking at that file..."}}]}`,
		ssePause,
	}, toolCallSSE("c1", "builtin__read_file", `{"path":"inside.txt"}`)...)

	base, calls, _ := agentUpstream(t,
		firstResponse,
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
	// READ UNTIL THE TOKEN, not just once.
	//
	// The first message on this wire is a Degraded notice (conversation memory
	// is unavailable in this harness), and it is sent BEFORE the model is even
	// called. The version of this test that decoded exactly one message was
	// therefore hanging up before the turn had started, while its comment said
	// "a tool-activity notice, in practice" -- so what it actually exercised was
	// never what it claimed, and the daemon had the whole model call and tool
	// still ahead of it. Waiting for a real token is what puts the hang-up
	// inside the pause.
	var tok protocol.TokenResponse
	for tok.Token == "" {
		tok = protocol.TokenResponse{}
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("the turn never produced a token: %v", err)
		}
		if tok.Done {
			t.Fatalf("the turn finished before streaming a token: %+v", tok)
		}
	}

	// The interrupt, exactly as a client performs one, inside the gap.
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
		t.Errorf("upstream model calls = %d after the client hung up, want 1 -- the turn kept "+
			"working for a client that had gone. The tool-activity write should have failed and "+
			"cancelled the turn; check that writeToClient still calls cancelTurn and that "+
			"agentloop still re-checks ctx.Err() before dispatching.", got)
	}
}
