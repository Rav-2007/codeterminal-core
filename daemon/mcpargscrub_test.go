package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"mochiii/protocol"
)

// argScrubToken has a shape scrub() recognises, built the way scrub_test.go
// builds its samples.
var argScrubToken = "ghp_" + strings.Repeat("x9Y8", 6)

// A SECRET THE MODEL WRITES INTO A THIRD-PARTY TOOL'S ARGUMENTS IS NEITHER SHOWN
// NOR SENT (mcpargscrub.go). Found live 2026-11-19: it was both.
//
// Two separate proofs, because each half could fail alone: the approval prompt
// is what the person saw, and the digest is what the server RECEIVED -- an echo
// cannot prove that, since tool output is scrubbed on its way back anyway.
//
// Neuter check: make scrubLaneBCall return at once.
func TestAThirdPartyToolNeverSeesASecretTheModelWrote(t *testing.T) {
	text := "deploy with " + argScrubToken
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", "vault__digest", `{"text":"`+text+`"}`),
		textSSE("done"),
	)
	cfg := MCPConfig{Enabled: true, Servers: map[string]MCPServerConfig{"vault": {
		Command:                buildEchoServer(t),
		Args:                   []string{"--digest"},
		Tools:                  map[string]string{"digest": PolicyAsk},
		AcknowledgedUnconfined: true,
	}}}
	s := loopServer(t, base, cfg)
	appr := &recordingApprover{answer: protocol.ApprovalApprove}
	if _, _, err := runLoopWith(t, s, appr); err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}

	var asked *protocol.ToolApprovalRequest
	for i := range appr.requests {
		if appr.requests[i].Tool == "digest" {
			asked = &appr.requests[i]
		}
	}
	if asked == nil {
		t.Fatalf("the call was never put to the user; approvals: %+v", appr.requests)
	}
	if strings.Contains(asked.Arguments, argScrubToken) || !strings.Contains(asked.Arguments, "[REDACTED:github_token]") {
		t.Errorf("the approval prompt showed %s, want the token redacted", asked.Arguments)
	}
	if asked.ArgumentsSHA256 != argumentsDigest(asked.Arguments) {
		t.Error("the digest binding what was shown to what runs does not match the arguments shown")
	}

	redacted, _ := scrub(text, false)
	if len(*bodies) < 2 {
		t.Fatalf("the model was asked %d time(s), want 2", len(*bodies))
	}
	back := string((*bodies)[1])
	if raw := sha256.Sum256([]byte(text)); strings.Contains(back, hex.EncodeToString(raw[:])) {
		t.Fatal("THE SERVER RECEIVED THE TOKEN: its digest of the raw text came back")
	}
	if want := sha256.Sum256([]byte(redacted)); !strings.Contains(back, "sha256 "+hex.EncodeToString(want[:])) {
		t.Errorf("the server did not receive the redacted text %q; the model was sent:\n%s", redacted, back)
	}
}

func TestScrubbedLaneBArguments(t *testing.T) {
	t.Run("a secret anywhere in the arguments, value or key, is redacted", func(t *testing.T) {
		args := `{"n":12345678901234567890,"opts":{"headers":["x",{"auth":"Bearer ` + argScrubToken + `"}]},"` + argScrubToken + `":true,"note":"<a&b>"}`
		got, kinds := scrubbedLaneBArguments(args, false)
		if strings.Contains(got, argScrubToken) || len(kinds) != 2 {
			t.Fatalf("got %s with kinds %v, want both copies redacted", got, kinds)
		}
		// A number is sent as written, and markup is not HTML-escaped.
		if !strings.Contains(got, "12345678901234567890") || !strings.Contains(got, "<a&b>") {
			t.Errorf("the rest of the arguments changed: %s", got)
		}
	})
	t.Run("arguments with no secret are left byte for byte", func(t *testing.T) {
		args := `{ "path" : "src/a.go", "n": 1.50 }`
		if got, kinds := scrubbedLaneBArguments(args, false); got != args || kinds != nil {
			t.Errorf("got %q, %v; want the arguments unchanged", got, kinds)
		}
	})
	t.Run("arguments that are not JSON are scrubbed as text", func(t *testing.T) {
		if got, _ := scrubbedLaneBArguments(`{"text": "`+argScrubToken, false); strings.Contains(got, argScrubToken) {
			t.Errorf("a malformed call carried the token past the scrub: %s", got)
		}
	})
	t.Run("--no-scrub leaves them alone, as on every other path", func(t *testing.T) {
		args := `{"text":"` + argScrubToken + `"}`
		if got, _ := scrubbedLaneBArguments(args, true); got != args {
			t.Errorf("got %s with scrubbing disabled", got)
		}
	})
}

// THE NAME RULE (mcpargscrub.go): a value whose own name says it is a secret
// is redacted from a third-party call's arguments, and nothing that merely
// looks like one is. The second half is the one that matters for users: a
// redacted pagination token is a broken tool.
//
// Neuter check: make namedSecretKey return false, and redactNamedAssignments
// return its input.
func TestTheNameRuleRedactsNamedSecretsAndNothingElse(t *testing.T) {
	for _, tc := range []struct{ name, args, gone string }{
		{"a password key", `{"password":"opaque-9f8e7d6c"}`, "opaque-9f8e7d6c"},
		{"a camelCase client secret, nested", `{"db":{"clientSecret":"Zq8vB2xLmN4pR7tY"}}`, "Zq8vB2xLmN4pR7tY"},
		{"an assignment in text", `{"text":"connect with DB_PASSWORD=opaque-9f8e7d6c please"}`, "opaque-9f8e7d6c"},
		{"a long value after a colon", `{"note":"password: correcthorsebatterystaple"}`, "correcthorsebatterystaple"},
	} {
		got, kinds := scrubbedLaneBArguments(tc.args, false)
		if strings.Contains(got, tc.gone) || !strings.Contains(got, namedSecretRedaction) || len(kinds) != 1 {
			t.Errorf("%s: got %s with kinds %v, want %q redacted once", tc.name, got, kinds, tc.gone)
		}
	}
	if got, _ := scrubbedLaneBArguments(`{"text":"connect with DB_PASSWORD=opaque-9f8e7d6c please"}`, false); !strings.Contains(got, "DB_PASSWORD=") {
		t.Errorf("the name was redacted with the value: %s", got)
	}
	for _, args := range []string{
		`{"page_token":"CAESBggBEAEYAQ"}`,
		`{"cursor":"eyJpZCI6MTIzNDU2fQ=="}`,
		`{"token_count":12345678}`,
		`{"secret_name":"prod-database-creds"}`,
		`{"password":"<your-password>"}`,
		`{"api_key":"${API_KEY}"}`,
		`{"apiKey":"your-api-key-here"}`,
		`{"is_secret":true}`,
		`{"text":"Set the API key: configure it in settings"}`,
		`{"text":"the secret: implementation details"}`,
	} {
		if got, kinds := scrubbedLaneBArguments(args, false); got != args || kinds != nil {
			t.Errorf("%s was changed to %s (%v); want it byte for byte", args, got, kinds)
		}
	}
}
