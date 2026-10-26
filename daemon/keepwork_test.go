package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mochiii/protocol"
)

// A PROVIDER FAILURE KEEPS THE WORK (agentloop.go). A model that writes no text
// before its tool calls -- MiniMax, on a real-repo run -- reached the provider
// failure after 41 calls with nothing in `full`, and the loop took that for "no
// work done": it returned an error, the working copy was discarded, and none of
// the turn's edits was offered.
func TestAProviderFailureAfterToolCallsKeepsTheEdits(t *testing.T) {
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, line := range toolCallSSE("c1", "builtin__propose_edit", `{"path":"a.txt","search":"old","replace":"new"}`) {
				_, _ = w.Write([]byte(line + "\n\n"))
			}
			return
		}
		http.Error(w, "upstream down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	s := loopServer(t, srv.URL, MCPConfig{Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"propose_edit": PolicyAllow}}})
	if err := os.WriteFile(filepath.Join(s.workspace, "a.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sink := &proposalSink{stageFrom: s.workingCopySource("auto")}
	t.Cleanup(sink.discard)
	registry, _ := s.buildRegistry(context.Background(), s.logger, sink, "auto")
	t.Cleanup(func() { _ = registry.Close() })

	res, err := s.runAgentLoop(context.Background(), time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "system", Content: "SYSTEM"}, {Role: "user", Content: "fix a.txt"}}, providerRouting{}, nil,
		func(string) error { return nil }, func(protocol.ToolActivity) {}, nil, nil, func(protocol.Degradation) {}, nil, nil)
	if err != nil {
		t.Fatalf("a failure after a tool call ended the turn as an error, dropping its work: %v", err)
	}
	if res.Incomplete == nil || res.Incomplete.Reason != protocol.IncompleteProviderError || len(res.ToolNames) != 1 {
		t.Fatalf("not reported as a turn the provider stopped part-way: %+v", res)
	}
	blocks, _, _ := sink.finish()
	if len(blocks) != 1 || blocks[0].FilePath != "a.txt" || !strings.Contains(blocks[0].Replace, "new") {
		t.Errorf("the edit the turn made was not offered: %+v", blocks)
	}
}

// THE WRAP-UP SURVIVES THE TIME LIMIT. A turn that will end with an answer
// stops taking steps a little before its deadline, so the call that answers
// from what was read still has time to happen.
func TestATurnStopsInTimeToAnswer(t *testing.T) {
	s := &Server{cfg: &Config{MCP: MCPConfig{Budget: MCPBudgetConfig{TurnTimeoutSeconds: 600}}}, logger: quietLogger()}
	turn := &agentTurn{iteration: 3, calls: 2}
	bud := budget{maxIterations: 40, maxTurnIterations: 60, maxTotalToolByte: 1 << 20,
		deadline: time.Now().Add(20 * time.Second), wrapUpInside: true}
	if stop := s.budgetStop(turn, bud); stop == nil || !strings.Contains(stop.Detail, "ran out of time") {
		t.Errorf("20 s before the deadline, with a wrap-up to make: %+v", stop)
	}
	bud.wrapUpInside = false // a phase that hands on instead of answering
	if stop := s.budgetStop(turn, bud); stop != nil {
		t.Errorf("a phase with no wrap-up of its own stopped early: %+v", stop)
	}
	bud.wrapUpInside, bud.deadline = true, time.Now().Add(5*time.Minute)
	if stop := s.budgetStop(turn, bud); stop != nil {
		t.Errorf("five minutes before the deadline: %+v", stop)
	}
}
