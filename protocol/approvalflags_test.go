package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestApprovalCapabilityFlagsAreOnTheWire covers the three fields the consent
// screen answers three different questions with.
//
// The table in protocol_test.go marshals ZERO values, and both capability flags
// are omitempty, so it structurally cannot see them: it asserted the shape of a
// request in which every interesting field is absent. LaunchesSubprocess lived
// inside the daemon for a day driving the Confined stamp with no way to reach a
// client, and that table would have stayed green forever.
func TestApprovalCapabilityFlagsAreOnTheWire(t *testing.T) {
	req := ToolApprovalRequest{
		CallID:             "c1",
		Tool:               "query_compiler_definition",
		Lane:               LaneFirstParty,
		ReachesNetwork:     true,
		LaunchesSubprocess: true,
		ReadOnlyHint:       true,
		Destructive:        true,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, field := range []string{"reaches_network", "launches_subprocess", "read_only_hint", "destructive"} {
		if !strings.Contains(string(raw), `"`+field+`"`) {
			t.Errorf("%s is not on the wire; a client cannot render what it never receives.\n  got: %s", field, raw)
		}
	}

	var back ToolApprovalRequest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !back.LaunchesSubprocess || !back.ReachesNetwork {
		t.Errorf("a capability flag did not survive the round trip: %+v", back)
	}
}
