package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

// PromptRequest.Trace is the join field for the execution trace. It is optional
// and it is allowed to change nothing on the wire for a client that does not set
// it, and these tests are what hold it to that. They need no ProtocolVersion bump
// (TestSentinelValues pins the version at 1); see the field's comment.

// The bytes a PromptRequest marshalled to BEFORE the field existed, produced by
// the protocol package of de87ff1 (the commit before this one) for exactly these
// two values. A request with no Trace must still marshal to them byte for byte:
// an always-present key would change what every existing daemon, proxy and log
// reader sees.
const (
	promptWireMinimal = `{"protocol_version":1,"prompt":"why"}`
	promptWireFull    = `{"protocol_version":1,"prompt":"why","workspace":"/w","history":[{"role":"user","content":"a"}],"reset":true,"prompt_kind":"code","tier":"t","mode":"build","reasoning_effort":"low","pipeline":["a"],"spec":"s.md","spec_grants":["g"],"task":"latest","task_action":"resume","task_budget":{"minutes":3},"chat_revision":"r1","session":"abc","budget_action":"status"}`
)

func fullPrompt() PromptRequest {
	return PromptRequest{
		ProtocolVersion: 1, Prompt: "why", Workspace: "/w", History: []Turn{{Role: "user", Content: "a"}},
		Reset: true, PromptKind: "code", Tier: "t", Mode: "build", ReasoningEffort: "low",
		Pipeline: []string{"a"}, Spec: "s.md", SpecGrants: []string{"g"}, Task: "latest", TaskAction: "resume",
		TaskBudget: &TaskBudget{Minutes: 3}, ChatRevision: "r1", Session: "abc", BudgetAction: "status",
	}
}

func TestAPromptWithNoTraceIDMarshalsToTheBytesItAlwaysDid(t *testing.T) {
	for name, tc := range map[string]struct {
		req  PromptRequest
		want string
	}{
		"minimal":               {PromptRequest{ProtocolVersion: 1, Prompt: "why"}, promptWireMinimal},
		"every other field set": {fullPrompt(), promptWireFull},
	} {
		b, err := json.Marshal(tc.req)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != tc.want {
			t.Errorf("%s: a request with no Trace marshalled to\n  %s\nwant the pre-field bytes\n  %s", name, b, tc.want)
		}
		if strings.Contains(string(b), `"trace"`) {
			t.Errorf("%s: the trace key is on the wire with no id to carry", name)
		}
	}
}

// With an id, the only difference is the one key, at the end.
func TestATraceIDIsTheOnlyDifferenceItMakes(t *testing.T) {
	req := fullPrompt()
	req.Trace = "a1b2c3d4"
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSuffix(promptWireFull, "}") + `,"trace":"a1b2c3d4"}`; string(b) != want {
		t.Errorf("marshalled to\n  %s\nwant\n  %s", b, want)
	}
	var back PromptRequest
	if err := json.Unmarshal(b, &back); err != nil || back.Trace != "a1b2c3d4" {
		t.Errorf("round trip: %+v, %v", back, err)
	}
}

// An older client's bytes, read by this package: the same request, no id.
func TestAnOlderClientsPromptDecodesWithNoTraceID(t *testing.T) {
	var req PromptRequest
	if err := json.Unmarshal([]byte(promptWireFull), &req); err != nil {
		t.Fatal(err)
	}
	if req.Trace != "" || req.Prompt != "why" || req.Session != "abc" {
		t.Errorf("decoded %+v, want the original fields and no Trace", req)
	}
	if want := fullPrompt(); req.Session != want.Session || req.BudgetAction != want.BudgetAction {
		t.Errorf("decoded %+v", req)
	}
}

// oldPromptRequest is what an older daemon decodes a prompt into: PromptRequest as
// it was at de87ff1, cut to the fields this test reads. encoding/json skips a key
// a struct does not have, so a newer client's request reads the same to it.
type oldPromptRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	Prompt          string `json:"prompt"`
	Workspace       string `json:"workspace,omitempty"`
	Session         string `json:"session,omitempty"`
	BudgetAction    string `json:"budget_action,omitempty"`
}

func TestAnOlderDaemonReadsATracedClientsRequestTheSameWay(t *testing.T) {
	traced := fullPrompt()
	traced.Trace = "a1b2c3d4"
	withID, err := json.Marshal(traced)
	if err != nil {
		t.Fatal(err)
	}
	var viaOld, viaOldWithout oldPromptRequest
	if err := json.Unmarshal(withID, &viaOld); err != nil {
		t.Fatalf("an older decoder refused a request that carries the join field: %v", err)
	}
	if err := json.Unmarshal([]byte(promptWireFull), &viaOldWithout); err != nil {
		t.Fatal(err)
	}
	if viaOld != viaOldWithout {
		t.Errorf("an older daemon reads the traced request as %+v and the untraced one as %+v", viaOld, viaOldWithout)
	}
	// No key an older client sends case-folds onto the new one (encoding/json
	// matches keys ignoring case), so an old request can never fill the join field.
	var asMap map[string]json.RawMessage
	_ = json.Unmarshal([]byte(promptWireFull), &asMap)
	for k := range asMap {
		if strings.EqualFold(k, "trace") {
			t.Errorf("the key %q case-folds onto trace, so an old client's request would decode into the join field", k)
		}
	}
}

func TestTraceIDBytesIsTheLengthOfTheIDTheDaemonMints(t *testing.T) {
	if TraceIDBytes != 8 {
		t.Errorf("TraceIDBytes = %d; the trace schema's sid and the daemon's turn ids are 8 hex digits", TraceIDBytes)
	}
}

// A bad value in the field must never fail the request: an older daemon, which has
// no such field, would have skipped the key, and a newer one must do no worse.
// A plain string field fails the whole decode on a number or an array, and the
// turn is lost; TraceID does not.
func TestAMalformedTraceFieldNeverFailsTheRequest(t *testing.T) {
	const canary = "plainzebra-joinfieldcanary-8c41d9"
	for name, v := range map[string]string{
		"number": `12345678`, "array": `["` + canary + `"]`, "object": `{"` + canary + `":1}`, "null": `null`, "true": `true`,
		"text": `"` + canary + `"`, "upper case": `"A1B2C3D4"`, "short": `"a1b2c3d"`, "long": `"a1b2c3d4e"`, "empty": `""`,
		"escaped": `"\u0061\u0031b2c3d4"`, "newline": `"a1b2c3\n4"`,
	} {
		var req PromptRequest
		body := `{"protocol_version":1,"prompt":"hi","trace":` + v + `}`
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Errorf("%s: the request failed to decode because of its trace field: %v", name, err)
			continue
		}
		want := TraceID("")
		if name == "escaped" { // decodes to a valid id: a JSON escape is not a different id
			want = "a1b2c3d4"
		}
		if req.Trace != want || req.Prompt != "hi" {
			t.Errorf("%s: decoded Trace=%q Prompt=%q, want Trace=%q", name, req.Trace, req.Prompt, want)
		}
	}
	var req PromptRequest
	if err := json.Unmarshal([]byte(`{"protocol_version":1,"prompt":"hi","trace":"a1b2c3d4"}`), &req); err != nil || req.Trace != "a1b2c3d4" {
		t.Errorf("a valid id: %+v, %v", req, err)
	}
}

func TestOnlyEightLowerCaseHexDigitsAreATraceID(t *testing.T) {
	for _, ok := range []string{"a1b2c3d4", "00000000", "ffffffff"} {
		if !ValidTraceID(ok) {
			t.Errorf("ValidTraceID(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "a1b2c3d", "a1b2c3d4e", "A1B2C3D4", "g1b2c3d4", "a1b2c3d4\n", " a1b2c3d4", "a1b2\x00c3d4", "a1b2c3d\u00e9", strings.Repeat("a", 1<<16)} {
		if ValidTraceID(bad) {
			t.Errorf("ValidTraceID(%.20q) = true", bad)
		}
	}
}
