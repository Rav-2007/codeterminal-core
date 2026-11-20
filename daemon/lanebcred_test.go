package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"mochiii/protocol"
)

// A CALL TO A THIRD-PARTY TOOL THAT CARRIES THIS MACHINE'S OWN KEY IS REFUSED
// BEFORE ANYONE IS ASKED (lanebcred.go). The key here has no shape scrub()
// knows, which is the case the shape rules cannot catch: only the exact-value
// match can. Raw and base64, the two forms a model most plausibly produces.
//
// Neuter check: make laneBCredentialRefusal return false at once.
func TestAThirdPartyCallCarryingThisMachinesKeyIsRefusedBeforeAnyoneIsAsked(t *testing.T) {
	const key = "opaque-provider-key-7f3a9c2e5b1d4e8a"
	if cleaned, reds := scrub(key, false); len(reds) != 0 || cleaned != key {
		t.Fatal("fixture is wrong: the key must have no shape scrub() recognises")
	}
	for _, tc := range []struct{ name, carried string }{
		{"raw", key},
		{"base64", base64.StdEncoding.EncodeToString([]byte(key))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, _, bodies := agentUpstream(t,
				toolCallSSE("c1", "vault__digest", `{"text":"deploy with `+tc.carried+`"}`),
				textSSE("done"),
			)
			s := loopServer(t, base, MCPConfig{Enabled: true, Servers: map[string]MCPServerConfig{"vault": {
				Command:                buildEchoServer(t),
				Args:                   []string{"--digest"},
				Tools:                  map[string]string{"digest": PolicyAsk},
				AcknowledgedUnconfined: true,
			}}})
			s.apiKey = key
			s.rebuildCredScrubber()
			appr := &recordingApprover{answer: protocol.ApprovalApprove}
			if _, _, err := runLoopWith(t, s, appr); err != nil {
				t.Fatalf("runAgentLoop: %v", err)
			}
			for _, r := range appr.requests {
				if r.Tool == "digest" {
					t.Errorf("the user was asked to approve sending this machine's key: %s", r.Arguments)
				}
			}
			if len(*bodies) < 2 {
				t.Fatalf("the model was asked %d time(s), want 2", len(*bodies))
			}
			back := string((*bodies)[1])
			if strings.Contains(back, "sha256 ") {
				t.Error("THE SERVER WAS CALLED: its digest came back")
			}
			if !strings.Contains(back, "own credentials") {
				t.Errorf("the model was not told why the call was refused:\n%s", back)
			}
		})
	}
}
