package protocol

import (
	"encoding/json"
	"testing"
)

// Capability negotiation is what lets agent mode exist without a
// ProtocolVersion bump, and its entire safety rests on one property: absent
// means NO. Every branch below is a way that property could be lost silently.
func TestHasCapability(t *testing.T) {
	cases := []struct {
		name string
		req  HandshakeRequest
		cap  string
		want bool
	}{
		{
			// The case that matters most: a client built before capabilities
			// existed sends no field at all. It must not be treated as capable
			// of answering a question it has never heard of.
			"nil capabilities is not capable",
			HandshakeRequest{ProtocolVersion: 1, ClientName: "old-client"},
			CapToolApproval, false,
		},
		{
			"empty capabilities is not capable",
			HandshakeRequest{Capabilities: []string{}},
			CapToolApproval, false,
		},
		{
			"declared capability is capable",
			HandshakeRequest{Capabilities: []string{CapToolApproval}},
			CapToolApproval, true,
		},
		{
			"declared among several is capable",
			HandshakeRequest{Capabilities: []string{"something_else", CapToolApproval, "another"}},
			CapToolApproval, true,
		},
		{
			"unknown capabilities are inert, not capable",
			HandshakeRequest{Capabilities: []string{"tool_approval_v2", "toolapproval", "TOOL_APPROVAL"}},
			CapToolApproval, false,
		},
		{
			// Matched exactly, like every sniffed key on this wire. A client
			// that shouts its capability has still not declared this one.
			"capability matching is case-sensitive",
			HandshakeRequest{Capabilities: []string{"Tool_Approval"}},
			CapToolApproval, false,
		},
		{
			"asking for a capability nobody declared",
			HandshakeRequest{Capabilities: []string{CapToolApproval}},
			"some_future_capability", false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.req.HasCapability(tc.cap); got != tc.want {
				t.Errorf("HasCapability(%q) = %t, want %t. Capabilities are %v -- "+
					"an absent or unrecognised capability must read as NOT capable, "+
					"because the daemon uses this to decide whether it may ask a "+
					"question the client has to answer",
					tc.cap, got, tc.want, tc.req.Capabilities)
			}
		})
	}
}

// A handshake from a client that predates this field must decode cleanly and
// arrive not-capable. This is the on-the-wire form of the test above: the
// struct gaining a field must not change how an old client's bytes are read.
func TestOldHandshakeBytesDecodeAsNotCapable(t *testing.T) {
	oldWire := []byte(`{"protocol_version":1,"client_name":"tui"}`)

	var req HandshakeRequest
	if err := json.Unmarshal(oldWire, &req); err != nil {
		t.Fatalf("a pre-capabilities handshake must still decode: %v", err)
	}
	if req.ClientName != "tui" || req.ProtocolVersion != 1 {
		t.Fatalf("decoded %#v, want the original fields intact", req)
	}
	if req.HasCapability(CapToolApproval) {
		t.Error("a handshake with no capabilities field reported itself capable of tool approval; " +
			"agent mode would then send it a question it cannot answer and stall the turn")
	}
}

// The daemon must never emit a capabilities key it was not given, so that a
// handshake from an old client is byte-identical going out as it was coming
// in. omitempty is what guarantees that, and this pins it.
func TestCapabilitiesAreOmittedWhenAbsent(t *testing.T) {
	b, err := json.Marshal(HandshakeRequest{ProtocolVersion: 1, ClientName: "tui"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(b); got != `{"protocol_version":1,"client_name":"tui"}` {
		t.Errorf("a capability-less handshake marshalled to %s, want the pre-capabilities bytes exactly. "+
			"An always-present key changes what every existing peer sees", got)
	}
}

// A ToolApprovalResponse that cannot prove which question it answers is not an
// answer. These are the shapes the loop must read as denials rather than as
// approvals; the enforcement lives daemon-side, but the VALUES it compares are
// this package's contract, so the vocabulary is pinned here.
func TestApprovalDecisionVocabularyIsClosed(t *testing.T) {
	// Exactly these four, and no synonyms. A client sending "yes" or "ok" or
	// "" must not be interpreted as approval by anything downstream.
	valid := map[string]bool{
		ApprovalApprove:        true,
		ApprovalDeny:           true,
		ApprovalApproveForTurn: true,
		ApprovalCancelTurn:     true,
	}
	if len(valid) != 4 {
		t.Fatalf("the four decision constants collided: %v", valid)
	}

	for _, notADecision := range []string{"", "yes", "y", "ok", "true", "APPROVE", "Approve", "allow"} {
		if valid[notADecision] {
			t.Errorf("%q is being accepted as a decision; the vocabulary must stay closed so "+
				"a near-miss reads as a denial rather than as consent", notADecision)
		}
	}
}
