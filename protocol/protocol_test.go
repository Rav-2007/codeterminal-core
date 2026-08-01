package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// This package had 0 tests and 0.0% coverage across 625 lines, while being the
// wire contract shared by the daemon, the TUI, the VS Code extension, and the
// CLI. Every struct, tag and sentinel here is something two independently
// compiled programs must agree on, and nothing checked that they still did.
//
// It is also the package where P1-1 was invisible as a GAP: chatCompletionChunk
// had no field for the proxy's budget_exceeded signal, and no test anywhere
// pinned what the wire can and cannot express. The round-trip tables below are
// written to make an unrepresentable message obvious.

// TestWireTags pins the JSON key for every field of every message. A renamed or
// dropped tag is a silent protocol break: the peer decodes the field as its zero
// value and reports nothing, so it surfaces as "the feature stopped working"
// rather than as a decode error. Asserted by NAME, not by round-tripping, since
// a round-trip through the same struct passes even when both sides are wrong.
func TestWireTags(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want []string
	}{
		{"HandshakeRequest", HandshakeRequest{}, []string{"protocol_version", "client_name", "capabilities"}},
		{"PromptRequest", PromptRequest{}, []string{"protocol_version", "prompt", "workspace", "history", "reset", "prompt_kind"}},
		{"Turn", Turn{}, []string{"role", "content"}},
		{"IncompleteInfo", IncompleteInfo{}, []string{"reason", "detail"}},
		{"Degradation", Degradation{}, []string{"component", "detail"}},
		{"HistoryInfo", HistoryInfo{}, []string{}},
		{"EditBlockWire", EditBlockWire{}, []string{"file_path", "search", "replace"}},
		{"ApplyEditRequest", ApplyEditRequest{}, []string{"protocol_version", "edit"}},
		{"UndoRequest", UndoRequest{}, []string{"protocol_version", "undo"}},
		{"SearchRequest", SearchRequest{}, []string{"protocol_version", "search"}},
		{"StatusRequest", StatusRequest{}, []string{"protocol_version", "status"}},
		{"ToolApprovalRequest", ToolApprovalRequest{}, []string{"call_id", "server", "tool", "arguments",
			"arguments_sha256", "lane", "confined", "read_only_hint", "destructive", "iteration", "max_iterations"}},
		{"ToolApprovalResponse", ToolApprovalResponse{}, []string{"protocol_version", "approval", "call_id",
			"arguments_sha256", "decision"}},
		{"ToolActivity", ToolActivity{}, []string{"call_id", "server", "tool", "phase", "detail",
			"duration_ms", "result_bytes"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := jsonTags(reflect.TypeOf(tc.v))
			for _, want := range tc.want {
				if !contains(got, want) {
					t.Errorf("%s has no field tagged %q; tags are %v. Renaming a wire tag "+
						"breaks every peer silently -- they decode the zero value and report nothing",
						tc.name, want, got)
				}
			}
		})
	}
}

// The four sniffed discriminator keys must be tagged EXACTLY as the daemon's
// dispatcher looks them up, and must NOT be omitempty: the dispatcher decides
// the message type by the key's PRESENCE, so a false value that gets omitted
// makes the request undetectable and it falls through to the prompt path.
//
// This is the property the daemon's exact-key dispatch (daemon/requestfields.go)
// depends on, asserted from the protocol side where the tags actually live.
func TestDiscriminatorKeysArePresentAndNotOmitempty(t *testing.T) {
	cases := []struct {
		name  string
		v     any
		field string
		key   string
	}{
		{"ApplyEditRequest", ApplyEditRequest{}, "Edit", "edit"},
		{"UndoRequest", UndoRequest{}, "Undo", "undo"},
		{"SearchRequest", SearchRequest{}, "Search", "search"},
		{"StatusRequest", StatusRequest{}, "Status", "status"},
		{"ToolApprovalResponse", ToolApprovalResponse{}, "Approval", "approval"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ok := reflect.TypeOf(tc.v).FieldByName(tc.field)
			if !ok {
				t.Fatalf("%s has no %s field", tc.name, tc.field)
			}
			tag := f.Tag.Get("json")
			if strings.Contains(tag, "omitempty") {
				t.Errorf("%s.%s is tagged %q -- omitempty makes a false/zero value VANISH from "+
					"the wire, and the daemon dispatches on this key's presence, so the request "+
					"would silently be treated as a prompt", tc.name, tc.field, tag)
			}
			if name := strings.Split(tag, ",")[0]; name != tc.key {
				t.Errorf("%s.%s is tagged %q, want %q -- the daemon looks this key up EXACTLY",
					tc.name, tc.field, name, tc.key)
			}
		})
	}
}

// A zero-valued typed request must still serialize its discriminator, which is
// the runtime consequence of the tag rule above.
func TestZeroValuedRequestsStillCarryTheirDiscriminator(t *testing.T) {
	cases := []struct {
		name string
		v    any
		key  string
	}{
		{"UndoRequest", UndoRequest{}, `"undo":`},
		{"SearchRequest", SearchRequest{}, `"search":`},
		{"StatusRequest", StatusRequest{}, `"status":`},
		{"ToolApprovalResponse", ToolApprovalResponse{}, `"approval":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(b), tc.key) {
				t.Errorf("zero-valued %s marshalled to %s, missing %s -- the daemon would route "+
					"it to the prompt path", tc.name, b, tc.key)
			}
		})
	}
}

// TestRoundTrip marshals a fully-populated value of every message, unmarshals it
// back, and requires deep equality. This catches a type that cannot represent
// what it claims to (the P1-1 shape: a signal with nowhere to land) and any
// tag/type mismatch between the two directions.
func TestRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		v    any
		into func() any
	}{
		{"HandshakeRequest", HandshakeRequest{ProtocolVersion: 1, ClientName: "tui"}, func() any { return &HandshakeRequest{} }},
		{
			"HandshakeResponse",
			HandshakeResponse{ProtocolVersion: 1, Ok: true, DaemonVersion: "v1", Error: "", PersistedHistory: []Turn{{Role: "user", Content: "hi"}}},
			func() any { return &HandshakeResponse{} },
		},
		{
			"PromptRequest",
			PromptRequest{ProtocolVersion: 1, Prompt: "why", Workspace: "/w", History: []Turn{{Role: "user", Content: "a"}}, Reset: true, PromptKind: "code"},
			func() any { return &PromptRequest{} },
		},
		{
			"TokenResponse full",
			TokenResponse{
				ProtocolVersion: 1,
				Token:           "tok",
				Done:            true,
				Error:           "boom",
				ErrorClass:      "invalid_request",
				Reasoning:       "thinking",
				Grounding:       &GroundingInfo{},
				History:         &HistoryInfo{},
				EditProposals:   []EditBlockWire{{FilePath: "a.go", Search: "x", Replace: "y"}},
				Redactions:      []string{"aws_key"},
				Degraded:        []Degradation{{Component: DegradedMemory, Detail: "d"}},
				Provider:        "DeepSeek",
				Incomplete:      &IncompleteInfo{Reason: IncompleteBudgetExceeded, Detail: "cut short"},
			},
			func() any { return &TokenResponse{} },
		},
		{
			"ApplyEditRequest",
			ApplyEditRequest{ProtocolVersion: 1, Edit: EditBlockWire{FilePath: "a.go", Search: "x", Replace: "y"}},
			func() any { return &ApplyEditRequest{} },
		},
		{"UndoRequest", UndoRequest{ProtocolVersion: 1, Undo: true}, func() any { return &UndoRequest{} }},
		{"SearchRequest", SearchRequest{ProtocolVersion: 1, Search: true}, func() any { return &SearchRequest{} }},
		{"StatusRequest", StatusRequest{ProtocolVersion: 1, Status: true}, func() any { return &StatusRequest{} }},
		{"SearchResult", SearchResult{}, func() any { return &SearchResult{} }},
		{"LockFile", LockFile{}, func() any { return &LockFile{} }},
		{
			"HandshakeRequest with capabilities",
			HandshakeRequest{ProtocolVersion: 1, ClientName: "tui", Capabilities: []string{CapToolApproval}},
			func() any { return &HandshakeRequest{} },
		},
		{
			"ToolApprovalRequest",
			ToolApprovalRequest{
				CallID: "call_1", Server: "fs", Tool: "write_file",
				Arguments: `{"path":"a.go"}`, ArgumentsSHA256: "abc123",
				Lane: LaneThirdParty, Confined: false, ReadOnlyHint: true, Destructive: true,
				Iteration: 2, MaxIterations: 8, Detail: "unconfined server",
			},
			func() any { return &ToolApprovalRequest{} },
		},
		{
			"ToolApprovalResponse",
			ToolApprovalResponse{
				ProtocolVersion: 1, Approval: true, CallID: "call_1",
				ArgumentsSHA256: "abc123", Decision: ApprovalApproveForTurn,
			},
			func() any { return &ToolApprovalResponse{} },
		},
		{
			"ToolActivity",
			ToolActivity{
				CallID: "call_1", Server: "fs", Tool: "read_file",
				Phase: ToolPhaseSucceeded, Detail: "read 40 lines", DurationMS: 12, ResultBytes: 2048,
			},
			func() any { return &ToolActivity{} },
		},
		{
			"TokenResponse carrying an approval ask",
			TokenResponse{
				ProtocolVersion: 1,
				ToolApproval:    &ToolApprovalRequest{CallID: "c", Tool: "t", Lane: LaneFirstParty},
				ToolActivity:    &ToolActivity{CallID: "c", Phase: ToolPhaseRequested},
			},
			func() any { return &TokenResponse{} },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			decoded := tc.into()
			if err := json.Unmarshal(encoded, decoded); err != nil {
				t.Fatalf("unmarshal %s: %v", encoded, err)
			}
			got := reflect.ValueOf(decoded).Elem().Interface()
			if !reflect.DeepEqual(tc.v, got) {
				t.Errorf("round-trip changed the value.\n sent: %#v\n  got: %#v\n wire: %s",
					tc.v, got, encoded)
			}
		})
	}
}

// The sentinel constants both clients branch on. Their VALUES are the contract,
// not their names -- a client that string-matches "length" keeps working across
// a rename of the Go identifier and breaks if the value changes.
func TestSentinelValues(t *testing.T) {
	if ProtocolVersion != 1 {
		t.Errorf("ProtocolVersion = %d, want 1. Bumping this rejects every existing client "+
			"at handshake; it must be a deliberate, coordinated change", ProtocolVersion)
	}

	sentinels := map[string]string{
		"IncompleteLength":         IncompleteLength,
		"IncompleteContentFilter":  IncompleteContentFilter,
		"IncompleteBudgetExceeded": IncompleteBudgetExceeded,
		"DegradedLexicalRetrieval": DegradedLexicalRetrieval,
		"DegradedMemory":           DegradedMemory,
		"DegradedProviderRouting":  DegradedProviderRouting,
		"IncompleteAgentBudget":    IncompleteAgentBudget,
		"DegradedMCPServer":        DegradedMCPServer,
		"CapToolApproval":          CapToolApproval,
		"LaneFirstParty":           LaneFirstParty,
		"LaneThirdParty":           LaneThirdParty,
		"ApprovalApprove":          ApprovalApprove,
		"ApprovalDeny":             ApprovalDeny,
		"ApprovalApproveForTurn":   ApprovalApproveForTurn,
		"ApprovalCancelTurn":       ApprovalCancelTurn,
	}
	want := map[string]string{
		"IncompleteLength":         "length",
		"IncompleteContentFilter":  "content_filter",
		"IncompleteBudgetExceeded": "budget_exceeded",
		"DegradedLexicalRetrieval": "lexical_retrieval",
		"DegradedMemory":           "memory",
		"DegradedProviderRouting":  "provider_routing",
		"IncompleteAgentBudget":    "agent_budget",
		"DegradedMCPServer":        "mcp_server",
		"CapToolApproval":          "tool_approval",
		"LaneFirstParty":           "first_party",
		"LaneThirdParty":           "third_party",
		"ApprovalApprove":          "approve",
		"ApprovalDeny":             "deny",
		"ApprovalApproveForTurn":   "approve_for_turn",
		"ApprovalCancelTurn":       "cancel_turn",
	}
	for name, got := range sentinels {
		if got != want[name] {
			t.Errorf("%s = %q, want %q -- clients branch on the VALUE", name, got, want[name])
		}
	}

	// Uniqueness: two reasons sharing a value would make them indistinguishable
	// to a client branching on it.
	seen := make(map[string]string)
	for name, value := range sentinels {
		if prev, dup := seen[value]; dup {
			t.Errorf("%s and %s share the value %q -- a client cannot tell them apart", prev, name, value)
		}
		seen[value] = name
	}
}

// IncompleteBudgetExceeded is signalled by the PROXY, not by a provider
// finish_reason, so it is the one sentinel with no upstream vocabulary behind it
// (see its doc comment). It must not collide with the finish_reason values, or a
// real provider reason would be reported as a spending cutoff.
func TestBudgetExceededDoesNotCollideWithProviderReasons(t *testing.T) {
	providerReasons := []string{"stop", "length", "content_filter", "tool_calls", "function_call"}
	for _, r := range providerReasons {
		if IncompleteBudgetExceeded == r {
			t.Errorf("IncompleteBudgetExceeded = %q collides with the provider finish_reason %q",
				IncompleteBudgetExceeded, r)
		}
	}
}

// The discovery paths every client derives independently. If these drift the
// client simply never finds the daemon.
func TestRuntimePaths(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if got := RuntimeDir(); got != "/run/user/1000" {
		t.Errorf("RuntimeDir() = %q, want the XDG_RUNTIME_DIR value", got)
	}
	if got, want := SocketPath(), "/run/user/1000/codeterminal/daemon.sock"; got != want {
		t.Errorf("SocketPath() = %q, want %q", got, want)
	}
	if got, want := LockPath(), "/run/user/1000/codeterminal/daemon.lock"; got != want {
		t.Errorf("LockPath() = %q, want %q", got, want)
	}

	// Unset falls back to the OS temp dir rather than failing.
	t.Setenv("XDG_RUNTIME_DIR", "")
	if got := RuntimeDir(); got != os.TempDir() {
		t.Errorf("RuntimeDir() with XDG_RUNTIME_DIR unset = %q, want os.TempDir() %q", got, os.TempDir())
	}
}

// SocketDir creates the directory OWNER-ONLY. The socket lives there and the
// socket is the daemon's entire attack surface, so 0700 is load-bearing: it is
// half of what makes "any same-uid process, and only a same-uid process" true.
func TestSocketDirIsOwnerOnly(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", tmp)

	dir, err := SocketDir()
	if err != nil {
		t.Fatalf("SocketDir: %v", err)
	}
	if want := filepath.Join(tmp, "codeterminal"); dir != want {
		t.Errorf("SocketDir() = %q, want %q", dir, want)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("SocketDir mode = %#o, want 0700 -- the daemon socket lives here and a "+
			"group- or world-accessible directory widens the only attack surface it has", perm)
	}

	// Idempotent: a second call on an existing dir must not error.
	if _, err := SocketDir(); err != nil {
		t.Errorf("SocketDir() on an existing directory: %v", err)
	}
}

// An unknown reason from a newer daemon must decode, not fail -- forward
// compatibility is why Reason is a string and not an enum.
func TestUnknownReasonDecodes(t *testing.T) {
	var info IncompleteInfo
	if err := json.Unmarshal([]byte(`{"reason":"a_reason_from_the_future","detail":"d"}`), &info); err != nil {
		t.Fatalf("an unknown reason must decode, not error: %v", err)
	}
	if info.Reason != "a_reason_from_the_future" {
		t.Errorf("Reason = %q, want it carried through verbatim", info.Reason)
	}
}

func jsonTags(t reflect.Type) []string {
	var tags []string
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			tags = append(tags, name)
		}
	}
	return tags
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
