package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// An empty reply from a named host, as DigitalOcean sent them (see the
// empty-reply comment in runAgentLoop).
func emptyFromSSE(provider string) []string {
	return []string{`data: {"provider":"` + provider + `","choices":[{"delta":{"content":""},"finish_reason":"stop"}]}`, `data: [DONE]`}
}

// sentIgnore decodes the provider.ignore list a captured request carried.
func sentIgnore(t *testing.T, body []byte) []string {
	t.Helper()
	var req struct {
		Provider struct {
			Ignore []string `json:"ignore"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	return req.Provider.Ignore
}

// sentMessages decodes the messages a captured request carried.
func sentMessages(t *testing.T, body []byte) []chatMessage {
	t.Helper()
	var req struct {
		Messages []chatMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	return req.Messages
}

// The first retry after an empty reply goes back to the SAME host, with a note
// -- the prompt is in that host's cache, and its drop is intermittent (measured:
// one in four replays of a captured request). Only a second empty reply in a
// row moves the turn to another host, on top of the configured ignore list,
// which is itself left untouched for the next turn.
//
// Neuter check: route the first retry elsewhere (avoidProvider in case 1) and
// the second request's ignore list gains the host.
func TestAnEmptyReplyIsAskedAgainOnTheSameHostThenElsewhere(t *testing.T) {
	base, requests, bodies := agentUpstream(t, emptyFromSSE("FlakyHost"), emptyFromSSE("FlakyHost"), textSSE("the answer"))
	s := loopServer(t, base, MCPConfig{Enabled: true})
	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	t.Cleanup(func() { _ = registry.Close() })

	configured := []string{"DeepInfra"}
	routing := providerRouting{ZDR: true, DataCollection: "deny", Ignore: configured}
	res, err := s.runAgentLoop(context.Background(), time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "system", Content: "S"}, {Role: "user", Content: "go"}}, routing, nil,
		func(string) error { return nil }, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.FinalText != "the answer" || res.Incomplete != nil || requests.Load() != 3 {
		t.Fatalf("text %q, incomplete %+v, %d request(s)", res.FinalText, res.Incomplete, requests.Load())
	}
	for i, want := range [][]string{{"DeepInfra"}, {"DeepInfra"}, {"DeepInfra", "FlakyHost"}} {
		if got := sentIgnore(t, (*bodies)[i]); !reflect.DeepEqual(got, want) {
			t.Errorf("request %d ignored %v, want %v", i+1, got, want)
		}
	}
	retry := sentMessages(t, (*bodies)[1])
	if last := retry[len(retry)-1]; last.Role != "user" || last.Content != emptyReplyNote {
		t.Errorf("the same-host retry ended with %+v, want the empty-reply note", last)
	}
	if !reflect.DeepEqual(configured, []string{"DeepInfra"}) || !reflect.DeepEqual(routing.Ignore, []string{"DeepInfra"}) {
		t.Errorf("the configured ignore list was changed in place: %v", configured)
	}
}

// Two empty replies APART are each asked again; only three in a row end the
// turn (which TestThreeEmptyRepliesInARowAreReported covers).
func TestEmptyRepliesApartAreEachAskedAgain(t *testing.T) {
	base, requests, _ := agentUpstream(t,
		emptyFromSSE("HostA"),
		toolCallSSE("c1", "builtin__list_directory", `{"path":"."}`),
		emptyFromSSE("HostB"),
		textSSE("done"))
	s := loopServer(t, base, MCPConfig{Enabled: true, Budget: MCPBudgetConfig{MaxIterations: 8}})
	res, _, err := runLoop(t, s)
	if err != nil {
		t.Fatal(err)
	}
	if res.FinalText != "done" || res.Incomplete != nil || requests.Load() != 4 {
		t.Errorf("text %q, incomplete %+v, %d request(s)", res.FinalText, res.Incomplete, requests.Load())
	}
}

func TestAvoidProviderNarrowsWithoutAliasing(t *testing.T) {
	base := []string{"DeepInfra", "Relace"}
	r := providerRouting{Ignore: base[:1]} // capacity to spare: an in-place append would clobber base[1]
	got := avoidProvider(r, "DigitalOcean")
	if !reflect.DeepEqual(got.Ignore, []string{"DeepInfra", "DigitalOcean"}) || base[1] != "Relace" {
		t.Errorf("avoidProvider = %v, base now %v", got.Ignore, base)
	}
	if again := avoidProvider(got, "digitalocean"); len(again.Ignore) != 2 {
		t.Errorf("a host already avoided was added twice: %v", again.Ignore)
	}
	if same := avoidProvider(r, ""); !reflect.DeepEqual(same.Ignore, r.Ignore) {
		t.Errorf("an unnamed host changed the list: %v", same.Ignore)
	}
}
