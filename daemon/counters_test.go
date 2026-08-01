package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestCountersRideTheStatusSurface asserts the parity claim over the REAL socket
// round trip (statusOverSocket): the counters are reported through the existing
// StatusResponse, so a client that can already ask "how are you" can also ask
// "what have you done" -- no second mechanism, no new listener.
func TestCountersRideTheStatusSurface(t *testing.T) {
	s := statusServer()
	s.counters = &counters{}
	s.count(func(c *counters) { c.prompts.Add(2) })
	s.count(func(c *counters) { c.applies.Add(3) })
	s.count(func(c *counters) { c.appliesFailed.Add(1) })
	s.count(func(c *counters) { c.peerAuthRefused.Add(4) })

	resp := statusOverSocket(t, s)

	if resp.Counters == nil {
		t.Fatal("status response carries no counters")
	}
	if resp.Counters.Prompts != 2 || resp.Counters.Applies != 3 ||
		resp.Counters.AppliesFailed != 1 || resp.Counters.PeerAuthRefused != 4 {
		t.Errorf("counters did not survive the round trip: %+v", resp.Counters)
	}
	// The status request itself went through the dispatcher, so it must have been
	// counted -- proof the increments are wired into serveConn and not just into
	// the struct.
	if resp.Counters.Statuses < 1 {
		t.Errorf("statuses = %d after a status request; the dispatch site is not counting",
			resp.Counters.Statuses)
	}
}

// TestCountersAreNilSafe pins the property every increment relies on: a Server
// built without counters -- which is how most of this suite builds one -- behaves
// exactly as it did before they existed, and omits the field entirely.
func TestCountersAreNilSafe(t *testing.T) {
	var nilServer *Server
	nilServer.count(func(c *counters) { c.prompts.Add(1) })

	s := statusServer() // no counters field set
	s.count(func(c *counters) { c.prompts.Add(1) })

	resp := statusOverSocket(t, s)
	if resp.Counters != nil {
		t.Errorf("counters = %+v for a daemon that has none, want nil", resp.Counters)
	}

	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "counters") {
		t.Errorf("the key is present even though there are no counters: %s", encoded)
	}
}

// TestStatusCountersAreAdditive is the compatibility claim behind leaving
// ProtocolVersion at 1: a client built before this field existed must decode a
// response that carries it, unchanged.
func TestStatusCountersAreAdditive(t *testing.T) {
	s := statusServer()
	s.counters = &counters{}
	s.count(func(c *counters) { c.prompts.Add(7) })

	encoded, err := json.Marshal(statusOverSocket(t, s))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var oldClient struct {
		ProtocolVersion int    `json:"protocol_version"`
		DaemonVersion   string `json:"daemon_version"`
	}
	if err := json.Unmarshal(encoded, &oldClient); err != nil {
		t.Fatalf("a client that predates counters can no longer decode a status "+
			"response (%v): %s", err, encoded)
	}
	if oldClient.ProtocolVersion != 1 {
		t.Errorf("protocol_version = %d; adding an omitempty field must not bump it",
			oldClient.ProtocolVersion)
	}
	if oldClient.DaemonVersion != daemonVersion {
		t.Errorf("daemon_version = %q, want %q", oldClient.DaemonVersion, daemonVersion)
	}
}

// TestUpstreamRequestIDRejectsAnythingItCannotTrust is the log-injection guard.
//
// The value comes from whatever host CODETERMINAL_API_BASE points at and is
// written straight into the daemon's log file, so a header carrying a newline
// would let an upstream forge lines in it.
func TestUpstreamRequestIDRejectsAnythingItCannotTrust(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want string
	}{
		{"our proxy's shape", "0123456789abcdef", "0123456789abcdef"},
		{"trimmed", "  0123456789abcdef  ", "0123456789abcdef"},
		{"absent", "", ""},
		{"newline (log forging)", "abc\nmodel API error: all fine", ""},
		{"uppercase", "ABCDEF0123456789", ""},
		{"not hex", "req-12345-abcdef", ""},
		{"absurdly long", strings.Repeat("a", 65), ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.id != "" {
				h.Set("X-Request-Id", tc.id)
			}
			if got := upstreamRequestID(h); got != tc.want {
				t.Errorf("upstreamRequestID(%q) = %q, want %q", tc.id, got, tc.want)
			}
		})
	}
}

// TestWithUpstreamRequestIDStaysOffTheWire is the Gate-7 half: the id joins the
// OPERATOR detail, and the socket-facing message stays exactly as generic as it
// was.
func TestWithUpstreamRequestIDStaysOffTheWire(t *testing.T) {
	before := classifyHTTPError(http.StatusUnauthorized, "401 Unauthorized", `{"error":"unauthorized"}`)
	clientMessage := before.Error()

	withID := before.withUpstreamRequestID("0123456789abcdef")

	if !strings.Contains(withID.Detail(), "0123456789abcdef") {
		t.Errorf("the operator detail lost the correlation id: %q", withID.Detail())
	}
	if withID.Error() != clientMessage {
		t.Errorf("the client-facing message changed from %q to %q; the id must never "+
			"cross the socket", clientMessage, withID.Error())
	}
	if withID.Class != before.Class {
		t.Errorf("classification changed when the id was attached: %s -> %s", before.Class, withID.Class)
	}
}
