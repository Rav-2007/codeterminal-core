package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"strings"
	"testing"
	"time"

	"codeterminal/protocol"
)

// A request whose digest is genuinely the digest of its arguments, so a test
// that wants to break the binding has to break it on purpose.
func approvalRequest(callID, args string) protocol.ToolApprovalRequest {
	return protocol.ToolApprovalRequest{
		CallID:          callID,
		Server:          "builtin",
		Tool:            "read_file",
		Arguments:       args,
		ArgumentsSHA256: argumentsDigest(args),
		Lane:            protocol.LaneFirstParty,
		Confined:        true,
	}
}

func answer(req protocol.ToolApprovalRequest, decision string, approval bool) json.RawMessage {
	raw, _ := json.Marshal(protocol.ToolApprovalResponse{
		ProtocolVersion: protocol.ProtocolVersion,
		Approval:        approval,
		CallID:          req.CallID,
		ArgumentsSHA256: req.ArgumentsSHA256,
		Decision:        decision,
	})
	return raw
}

// THE CORE CONSENT PROPERTY: nothing but a correctly-bound, explicitly
// approving answer produces an approval.
//
// Every row here is a way a "yes" could be faked, garbled, or misdirected, and
// every one of them must come out as a denial. The neuter check is the table
// itself: delete any single check in verifyApproval and its rows flip to
// approved.
func TestOnlyACorrectlyBoundYesApproves(t *testing.T) {
	req := approvalRequest("call-1", `{"path":"secrets.env"}`)

	cases := []struct {
		name string
		raw  string
		want string
		// cause is checked only when non-empty, because the audit distinguishes
		// a human saying no from a client that could not answer.
		cause string
	}{
		{"a plain approve", string(answer(req, protocol.ApprovalApprove, true)), protocol.ApprovalApprove, ""},
		{"approve for turn", string(answer(req, protocol.ApprovalApproveForTurn, true)), protocol.ApprovalApproveForTurn, ""},
		{"an explicit deny", string(answer(req, protocol.ApprovalDeny, false)), protocol.ApprovalDeny, denyByUser},

		// Cancel is honoured whatever the boolean says: stopping is never the
		// unsafe direction.
		{"cancel with approval false", string(answer(req, protocol.ApprovalCancelTurn, false)), protocol.ApprovalCancelTurn, denyByUser},

		// The two fields disagreeing must resolve the RESTRICTIVE way, or the
		// boolean is decorative.
		{"approve with approval false", string(answer(req, protocol.ApprovalApprove, false)), protocol.ApprovalDeny, denyByMismatch},

		// An answer to a different question.
		{"wrong call id", `{"protocol_version":1,"approval":true,"call_id":"other","arguments_sha256":"` + req.ArgumentsSHA256 + `","decision":"approve"}`, protocol.ApprovalDeny, denyByMismatch},

		// THE DIGEST BINDING. The client approved something, but not this.
		{"digest of different arguments", `{"protocol_version":1,"approval":true,"call_id":"call-1","arguments_sha256":"` + argumentsDigest(`{"path":"harmless.txt"}`) + `","decision":"approve"}`, protocol.ApprovalDeny, denyByMismatch},
		{"empty digest", `{"protocol_version":1,"approval":true,"call_id":"call-1","arguments_sha256":"","decision":"approve"}`, protocol.ApprovalDeny, denyByMismatch},

		// Invented verbs are not permissions.
		{"decision yes", string(answer(req, "yes", true)), protocol.ApprovalDeny, denyByMalformed},
		{"decision y", string(answer(req, "y", true)), protocol.ApprovalDeny, denyByMalformed},
		{"decision uppercase", string(answer(req, "APPROVE", true)), protocol.ApprovalDeny, denyByMalformed},
		{"decision empty", string(answer(req, "", true)), protocol.ApprovalDeny, denyByMalformed},

		// Not recognisably an approval at all. The middle one is the exact-key
		// discipline: encoding/json would case-fold "APPROVAL" onto the field,
		// and requestfields.go is why it does not sniff.
		{"not an approval message", `{"prompt":"hello"}`, protocol.ApprovalDeny, denyByMalformed},
		{"case-folded key", `{"APPROVAL":true,"call_id":"call-1","decision":"approve"}`, protocol.ApprovalDeny, denyByMalformed},
		{"duplicate keys", `{"approval":false,"approval":true,"call_id":"call-1","decision":"approve"}`, protocol.ApprovalDeny, denyByMalformed},
		{"not an object", `"approve"`, protocol.ApprovalDeny, denyByMalformed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, cause := verifyApproval(req, json.RawMessage(tc.raw))
			if got != tc.want {
				t.Errorf("verifyApproval(%s) = %q, want %q", tc.raw, got, tc.want)
			}
			if tc.cause != "" && cause != tc.cause {
				t.Errorf("deny cause = %q, want %q", cause, tc.cause)
			}
		})
	}
}

// A hex digest has two spellings and neither is wrong -- the bytes are what the
// check is about. A client that upper-cases its hex must not be told no.
func TestDigestComparisonIsCaseInsensitive(t *testing.T) {
	req := approvalRequest("call-1", `{"path":"a.txt"}`)
	raw, _ := json.Marshal(protocol.ToolApprovalResponse{
		Approval:        true,
		CallID:          req.CallID,
		ArgumentsSHA256: strings.ToUpper(req.ArgumentsSHA256),
		Decision:        protocol.ApprovalApprove,
	})
	if got, _ := verifyApproval(req, raw); got != protocol.ApprovalApprove {
		t.Errorf("an upper-case hex digest was rejected: got %q", got)
	}
}

// approvalPipe wires a connApprover to one end of a net.Pipe and hands back the
// other end, so the client half can be scripted (or made to say nothing at all).
func approvalPipe(t *testing.T, idle time.Duration) (*connApprover, net.Conn) {
	t.Helper()
	clientEnd, daemonEnd := net.Pipe()
	t.Cleanup(func() { _ = clientEnd.Close(); _ = daemonEnd.Close() })

	lc := &limitedConn{Conn: daemonEnd, remaining: 1 << 20, idleTimeout: idle}
	return &connApprover{
		enc:         json.NewEncoder(lc),
		dec:         json.NewDecoder(lc),
		lc:          lc,
		logger:      log.New(io.Discard, "", 0),
		idleTimeout: idle,
	}, clientEnd
}

// Over the real wire: the daemon writes a question, the client writes an
// answer, and the answer is bound to what was asked.
func TestConnApproverRoundTrip(t *testing.T) {
	appr, client := approvalPipe(t, 2*time.Second)
	req := approvalRequest("c1", `{"path":"a.txt"}`)

	go func() {
		var msg protocol.TokenResponse
		if err := json.NewDecoder(client).Decode(&msg); err != nil {
			return
		}
		// The prompt must carry the FULL arguments, not a summary: a consent
		// prompt that shows less than what will run is not consent.
		if msg.ToolApproval == nil || msg.ToolApproval.Arguments != req.Arguments {
			return
		}
		_ = json.NewEncoder(client).Encode(protocol.ToolApprovalResponse{
			ProtocolVersion: protocol.ProtocolVersion,
			Approval:        true,
			CallID:          msg.ToolApproval.CallID,
			ArgumentsSHA256: msg.ToolApproval.ArgumentsSHA256,
			Decision:        protocol.ApprovalApprove,
		})
	}()

	got := appr.Ask(context.Background(), req)
	if !got.approved() {
		t.Fatalf("a correct approval over the wire was not honoured: %+v", got)
	}
	if appr.closed {
		t.Errorf("a successful ask closed the channel")
	}
}

// SILENCE IS NOT CONSENT, AND IT IS ALSO NOT A HANG.
//
// The second half is the part that is easy to get wrong. encoding/json caches
// a Decoder's first error and returns it forever after, so once a read has
// timed out this channel can never produce another answer. Without the closed
// latch, every remaining call in the turn would wait out the full human
// deadline again -- one user walking away turning into a daemon blocked for the
// rest of the iteration budget.
func TestSilenceDeniesAndTheChannelStopsAsking(t *testing.T) {
	appr, client := approvalPipe(t, 50*time.Millisecond)

	// The client reads the question and then says nothing at all.
	go func() {
		var msg protocol.TokenResponse
		_ = json.NewDecoder(client).Decode(&msg)
		select {}
	}()

	first := appr.Ask(context.Background(), approvalRequest("c1", `{"path":"a.txt"}`))
	if first.approved() {
		t.Fatal("a timed-out approval was treated as a yes")
	}
	if first.Cause != denyByTimeout {
		t.Errorf("cause = %q, want %q", first.Cause, denyByTimeout)
	}
	if !appr.closed {
		t.Error("the channel stayed open after a read failure, so the next ask will wait out the whole deadline again")
	}

	started := time.Now()
	second := appr.Ask(context.Background(), approvalRequest("c2", `{"path":"b.txt"}`))
	if second.approved() {
		t.Fatal("the second ask on a dead channel was treated as a yes")
	}
	if elapsed := time.Since(started); elapsed > 20*time.Millisecond {
		t.Errorf("the second ask waited %s on a channel that can never answer", elapsed)
	}
	if second.Cause != denyByNoChannel {
		t.Errorf("cause = %q, want %q", second.Cause, denyByNoChannel)
	}
}

// A nil approver is the ordinary state of every caller with no client on the
// other end (the loop eval, most unit tests). It must deny, immediately, rather
// than panic.
func TestNilApproverDenies(t *testing.T) {
	if got := askApproval(context.Background(), nil, approvalRequest("c1", "{}")); got.approved() {
		t.Errorf("a nil approver approved a call: %+v", got)
	}
	var typed *connApprover
	if got := askApproval(context.Background(), typed, approvalRequest("c1", "{}")); got.approved() {
		t.Errorf("a nil *connApprover approved a call: %+v", got)
	}
}

// A shutdown mid-turn must not open a five-minute window on a connection that
// is about to be torn down.
func TestCancelledContextDeniesWithoutAsking(t *testing.T) {
	appr, _ := approvalPipe(t, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := appr.Ask(ctx, approvalRequest("c1", "{}"))
	if got.approved() {
		t.Fatal("a cancelled context still produced an approval")
	}
	if !appr.closed {
		t.Error("a cancelled context should close the channel rather than leave it to time out per call")
	}
}

// A SHUTDOWN MID-QUESTION MUST NOT COST THE HUMAN DEADLINE.
//
// The daemon drains for seconds, not minutes. A turn parked on an approval
// prompt holds its connection open for as long as the prompt stands, so
// without something to unblock the read, Ctrl-C on a daemon that happened to
// be asking would report an incomplete drain and abandon the connection rather
// than closing it.
//
// Nothing can interrupt a blocked socket read from the outside, so the fix is
// to move the deadline rather than to signal: see the watcher in Ask.
func TestShutdownDuringAnApprovalDoesNotWaitOutTheHumanDeadline(t *testing.T) {
	// A deliberately long human deadline, so the only thing that can end this
	// wait quickly is the cancellation itself.
	appr, client := approvalPipe(t, time.Minute)

	go func() {
		var msg protocol.TokenResponse
		_ = json.NewDecoder(client).Decode(&msg)
		select {} // read the question, then never answer
	}()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	got := appr.Ask(ctx, approvalRequest("c1", `{"path":"a.txt"}`))
	elapsed := time.Since(started)

	if got.approved() {
		t.Fatal("a cancelled approval was treated as a yes")
	}
	if elapsed > 5*time.Second {
		t.Errorf("the ask took %s to notice a shutdown; the daemon drains in seconds", elapsed)
	}
}
