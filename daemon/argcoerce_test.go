package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// TOOL ARGUMENTS IN THE MODEL'S SPELLING (argcoerce.go): MiniMax's
// "regex": "true" was refused whole by grep's strict decode.
func TestArgumentsAreReadAsTheSchemaSays(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},
		"regex":{"type":"boolean"},"max_results":{"type":"integer"},"ratio":{"type":"number"},
		"either":{"type":["string","integer"]}}}`)
	for _, tc := range []struct{ in, want string }{
		{`{"pattern":"a && b","regex":"true","max_results":"5"}`, `{"max_results":5,"pattern":"a && b","regex":true}`},
		{`{"regex":"false","ratio":"0.5"}`, `{"ratio":0.5,"regex":false}`},
		// Left exactly as sent: already right, a string allowed, or not plainly the type.
		{`{"pattern":"x","regex":true,"max_results":5}`, `{"pattern":"x","regex":true,"max_results":5}`},
		{`{"either":"7"}`, `{"either":"7"}`},
		{`{"regex":"yes"}`, `{"regex":"yes"}`},
		{`{"max_results":"5 files"}`, `{"max_results":"5 files"}`},
		{`{"max_results":"2.5"}`, `{"max_results":"2.5"}`},
		{`{"unknown":"true"}`, `{"unknown":"true"}`},
		{`not json`, `not json`},
	} {
		if got, _ := coerceArgs(schema, tc.in); got != tc.want {
			t.Errorf("coerceArgs(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// Through the loop: the call runs, and what the tool receives is what was
// coerced -- the same bytes the audit and any prompt would show.
func TestALooseArgumentNoLongerRefusesTheCall(t *testing.T) {
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__strict", `{"pattern":"x","regex":"true"}`), textSSE("done"))
	s := loopServer(t, base, MCPConfig{Enabled: true, Builtin: MCPBuiltinConfig{Tools: map[string]string{"strict": PolicyAllow}}})
	registry := mcp.NewRegistry(configPolicy{cfg: s.cfg}, 50)
	t.Cleanup(func() { _ = registry.Close() })
	var got string
	if err := registry.RegisterBuiltin(mcp.Builtin{
		Tool: mcp.Tool{Name: "strict", Description: "stub", Confined: true,
			Schema: schema(`{"type":"object","properties":{"pattern":{"type":"string"},"regex":{"type":"boolean"}}}`)},
		Handler: func(_ context.Context, raw json.RawMessage) (mcp.Result, error) {
			var args struct {
				Pattern string `json:"pattern"`
				Regex   bool   `json:"regex"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return mcp.Result{Content: err.Error(), IsError: true}, nil
			}
			got = string(raw)
			return mcp.Result{Content: "ok"}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	var failed []string
	if _, err := s.runAgentLoop(context.Background(), time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "system", Content: "SYSTEM"}, {Role: "user", Content: "search"}}, providerRouting{}, nil,
		func(string) error { return nil }, func(a protocol.ToolActivity) {
			if a.Phase == protocol.ToolPhaseFailed {
				failed = append(failed, a.Detail)
			}
		}, nil, nil, func(protocol.Degradation) {}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(failed) != 0 || !strings.Contains(got, `"regex":true`) {
		t.Errorf("the call with a string boolean did not run as one: got %q, failures %v", got, failed)
	}
}
