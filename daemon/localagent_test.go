package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// Tests for the agent that fits the model (1.4.0): localcheck.go,
// localprofile.go and localtextcall.go. Every server is a stand-in on
// 127.0.0.1 that answers the way that kind of server documents.

// scriptedLocal is a stand-in Ollama whose chat answers come from reply, with
// the capabilities and loaded window given.
type scriptedLocal struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
}

func (s *scriptedLocal) base() string { return s.srv.URL + "/v1" }

func (s *scriptedLocal) requests() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.bodies...)
}

// newScriptedLocal answers chat with reply: an HTTP status and body for a
// refusal, or status 0 and SSE events for an answer.
func newScriptedLocal(t *testing.T, capabilities []string, window int, reply func(n int, req map[string]any) (int, []string)) *scriptedLocal {
	t.Helper()
	sl := &scriptedLocal{}
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, map[string]any{"version": "0.40.2"}) })
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, modelList("m")) })
	mux.HandleFunc("POST /api/show", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"capabilities": capabilities, "details": map[string]any{"parameter_size": "7.6B"},
			"model_info": map[string]any{"qwen2.context_length": 32768}})
	})
	mux.HandleFunc("POST /api/generate", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, map[string]any{"done": true}) })
	mux.HandleFunc("GET /api/ps", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"models": []any{map[string]any{"name": "m", "model": "m", "context_length": window}}})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		sl.mu.Lock()
		sl.bodies = append(sl.bodies, req)
		n := len(sl.bodies)
		sl.mu.Unlock()
		status, events := reply(n, req)
		if status != 0 {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, strings.Join(events, ""))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			_, _ = fmt.Fprintf(w, "%s\n\n", e)
		}
	})
	sl.srv = httptest.NewServer(mux)
	t.Cleanup(sl.srv.Close)
	return sl
}

// ollamaNoTools is Ollama's refusal of a request with tools, for a model whose
// template has none.
const ollamaNoTools = `{"error":{"message":"registry.ollama.ai/library/codellama:latest does not support tools","type":"api_error","param":null,"code":null}}`

// localTools registers stand-ins for the built-ins the compact agent offers and
// for two it does not, and counts each one's calls.
func localTools(t *testing.T, s *Server) (*mcp.Registry, map[string]*atomic.Int64) {
	t.Helper()
	registry := mcp.NewRegistry(configPolicy{cfg: s.cfg}, 30)
	t.Cleanup(func() { _ = registry.Close() })
	counts := map[string]*atomic.Int64{}
	for _, tool := range []mcp.Tool{
		{Name: "read_file", ReadOnlyHint: true, Confined: true},
		{Name: "search_code", ReadOnlyHint: true, Confined: true},
		{Name: "list_directory", ReadOnlyHint: true, Confined: true},
		{Name: "propose_edit", Confined: true},
		{Name: "run_command", ExecutesCode: true},
		{Name: "repo_map", ReadOnlyHint: true, Confined: true},
		{Name: "web_search", ReadOnlyHint: true, ReachesNetwork: true},
	} {
		tool.Description, tool.Schema = "stub", schema(`{"type":"object"}`)
		n := &atomic.Int64{}
		counts[tool.Name] = n
		if err := registry.RegisterBuiltin(mcp.Builtin{Tool: tool,
			Handler: func(context.Context, json.RawMessage) (mcp.Result, error) {
				n.Add(1)
				return mcp.Result{Content: "a " + tool.Name + " result"}, nil
			}}); err != nil {
			t.Fatal(err)
		}
	}
	return registry, counts
}

func localLoopServer(t *testing.T, base string) *Server {
	t.Helper()
	return loopServer(t, base, MCPConfig{Enabled: true, Builtin: MCPBuiltinConfig{Tools: map[string]string{
		"read_file": PolicyAllow, "search_code": PolicyAllow, "list_directory": PolicyAllow, "propose_edit": PolicyAllow,
		"run_command": PolicyAllow, "repo_map": PolicyAllow, "web_search": PolicyAllow,
	}}})
}

func runLocalLoop(t *testing.T, s *Server, ctx context.Context, registry *mcp.Registry) (agentResult, error) {
	t.Helper()
	return s.runAgentLoop(ctx, time.Now(), registry, "m", "auto",
		[]chatMessage{{Role: "system", Content: "SYSTEM"}, {Role: "user", Content: "what does greet.go do?"}}, providerRouting{},
		&capturingApprover{decision: protocol.ApprovalDeny},
		func(string) error { return nil }, func(protocol.ToolActivity) {}, nil, nil,
		func(protocol.Degradation) {}, nil, nil)
}

func offeredNames(req map[string]any) []string {
	var names []string
	tools, _ := req["tools"].([]any)
	for _, t := range tools {
		if f, ok := t.(map[string]any)["function"].(map[string]any); ok {
			names = append(names, strings.TrimPrefix(fmt.Sprint(f["name"]), "builtin__"))
		}
	}
	sort.Strings(names)
	return names
}

// A MODEL THAT CANNOT TAKE TOOLS IS ANSWERED WITHOUT THEM, not failed. Ollama
// refuses any request carrying tools for such a model, so before 1.4.0 every
// agent-mode question to it failed with "calling model API failed". Now the
// question is asked again with none, the person is told once, and later
// questions skip the agent.
//
// Neuter check: make refusedForTools return false, and the turn fails.
func TestAModelThatCannotTakeToolsIsAnsweredWithoutThem(t *testing.T) {
	forgetLocalModels(t)
	local := newScriptedLocal(t, []string{"completion"}, 4096, func(_ int, req map[string]any) (int, []string) {
		if _, withTools := req["tools"]; withTools {
			return http.StatusBadRequest, []string{ollamaNoTools}
		}
		return 0, textSSE("answered without tools")
	})
	s := localLoopServer(t, local.base())
	registry, _ := localTools(t, s)
	var notices []protocol.Degradation
	ctx := withClientCaps(context.Background(), protocol.HandshakeRequest{Capabilities: []string{protocol.CapToolApproval}})
	result, err := s.withoutToolsIfRefused(ctx, "m", func(d protocol.Degradation) { notices = append(notices, d) },
		func(ctx context.Context) (agentResult, error) { return runLocalLoop(t, s, ctx, registry) })
	if err != nil {
		t.Fatalf("the question failed: %v", err)
	}
	if result.FinalText != "answered without tools" {
		t.Errorf("answer = %q", result.FinalText)
	}
	reqs := local.requests()
	if len(reqs) != 2 || len(offeredNames(reqs[1])) != 0 {
		t.Errorf("%d requests, the second offering %v; want two, the second with no tools", len(reqs), offeredNames(reqs[len(reqs)-1]))
	}
	if len(notices) != 1 || notices[0].Component != protocol.DegradedLocalNoTools {
		t.Errorf("notices = %+v, want one saying the model cannot use tools", notices)
	}
	if s.localModelTakesTools("m") {
		t.Error("the model is still asked with tools after its server refused them")
	}
}

// A REFUSAL OF ANYTHING ELSE IS NOT ANSWERED WITHOUT TOOLS: it is returned as
// it was, and the model is still asked with tools next time.
func TestOnlyAToolRefusalIsAnsweredWithoutTools(t *testing.T) {
	forgetLocalModels(t)
	local := newScriptedLocal(t, []string{"completion", "tools"}, 4096, func(int, map[string]any) (int, []string) {
		return http.StatusBadRequest, []string{`{"error":{"message":"messages: field required"}}`}
	})
	s := localLoopServer(t, local.base())
	registry, _ := localTools(t, s)
	ctx := withClientCaps(context.Background(), protocol.HandshakeRequest{Capabilities: []string{protocol.CapToolApproval}})
	_, err := s.withoutToolsIfRefused(ctx, "m", func(protocol.Degradation) {},
		func(ctx context.Context) (agentResult, error) { return runLocalLoop(t, s, ctx, registry) })
	if err == nil {
		t.Fatal("a refusal that has nothing to do with tools was answered anyway")
	}
	if len(local.requests()) != 1 || !s.localModelTakesTools("m") {
		t.Errorf("%d requests, takes tools %v; want one request, and tools still on", len(local.requests()), s.localModelTakesTools("m"))
	}
}

// THE COMPACT AGENT OFFERS FIVE TOOLS AND REFUSES THE REST, even when the
// model names one; the internet tools join the five only when the turn
// allows them.
//
// Neuter check: drop the compact case from dropsForTurn, and repo_map is on
// the menu and runs.
func TestTheCompactAgentOffersFiveToolsAndRefusesTheRest(t *testing.T) {
	forgetLocalModels(t)
	run := func(t *testing.T, caps ...string) ([]string, int64) {
		t.Helper()
		local := newScriptedLocal(t, []string{"tools"}, 8192, func(n int, _ map[string]any) (int, []string) {
			if n == 1 {
				return 0, toolCallSSE("c1", "builtin__repo_map", `{}`)
			}
			return 0, textSSE("done")
		})
		s := localLoopServer(t, local.base())
		registry, counts := localTools(t, s)
		ctx := withClientCaps(context.Background(), protocol.HandshakeRequest{Capabilities: caps})
		if _, err := runLocalLoop(t, s, ctx, registry); err != nil {
			t.Fatalf("runAgentLoop: %v", err)
		}
		return offeredNames(local.requests()[0]), counts["repo_map"].Load()
	}
	offered, mapped := run(t, protocol.CapToolApproval, protocol.CapCompactAgent, protocol.CapNoNetworkTools)
	if want := []string{"list_directory", "propose_edit", "read_file", "run_command", "search_code"}; fmt.Sprint(offered) != fmt.Sprint(want) {
		t.Errorf("the compact agent offered %v, want %v", offered, want)
	}
	if mapped != 0 {
		t.Errorf("repo_map, which the compact agent does not offer, ran %d time(s) when the model named it", mapped)
	}
	offered, _ = run(t, protocol.CapToolApproval, protocol.CapCompactAgent)
	if !strings.Contains(fmt.Sprint(offered), "web_search") || strings.Contains(fmt.Sprint(offered), "repo_map") {
		t.Errorf("with web tools allowed the compact agent offered %v, want the five and web_search", offered)
	}
	offered, mapped = run(t, protocol.CapToolApproval)
	if !strings.Contains(fmt.Sprint(offered), "repo_map") || mapped != 1 {
		t.Errorf("control: the full agent offered %v and ran repo_map %d time(s)", offered, mapped)
	}
}

func TestTheCompactAgentGetsShortInstructions(t *testing.T) {
	messages := []chatMessage{{Role: "system", Content: "the full instructions"}, {Role: "user", Content: "q"}}
	compact := withTurnMenu(context.Background(), turnMenu{compact: true})
	if got := compactTurnMessages(compact, messages); got[0].Content != compactAgentPrompt || got[1].Content != "q" {
		t.Errorf("compact turn messages = %+v", got)
	}
	if messages[0].Content != "the full instructions" {
		t.Error("the caller's messages were changed in place")
	}
	if got := compactTurnMessages(context.Background(), messages); got[0].Content != "the full instructions" {
		t.Error("a full agent's turn lost its instructions")
	}
	if len(compactAgentPrompt) > 1600 {
		t.Errorf("the compact instructions are %d characters; they exist to be short", len(compactAgentPrompt))
	}
}

// A CALL A LOCAL MODEL WRITES AS TEXT RUNS AS THE CALL IT NAMES, and the text
// is taken out of the answer. qwen2.5-coder:3b wrote exactly this (2026-10-24).
//
// Neuter check: make rescueTextCall return calls, and read_file never runs.
func TestACallWrittenAsTextRunsAsTheCall(t *testing.T) {
	forgetLocalModels(t)
	local := newScriptedLocal(t, []string{"tools"}, 8192, func(n int, _ map[string]any) (int, []string) {
		if n == 1 {
			return 0, textSSE(`{"name": "builtin__read_file", "arguments": {"path": "greet.go"}}`)
		}
		return 0, textSSE("greetingFor welcomes a visitor")
	})
	s := localLoopServer(t, local.base())
	registry, counts := localTools(t, s)
	ctx := withClientCaps(context.Background(), protocol.HandshakeRequest{Capabilities: []string{protocol.CapToolApproval}})
	result, err := runLocalLoop(t, s, ctx, registry)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if counts["read_file"].Load() != 1 {
		t.Errorf("read_file ran %d time(s), want 1", counts["read_file"].Load())
	}
	if result.FinalText != "greetingFor welcomes a visitor" {
		t.Errorf("answer = %q, want the answer without the written call", result.FinalText)
	}
}

func TestTextToolCall(t *testing.T) {
	menu := offeredTool([]toolSpec{{Type: "function", Function: toolSpecFunction{Name: "builtin__read_file"}}})
	for _, tc := range []struct {
		name, text, wantArgs string
		ok                   bool
	}{
		{"qwen's bare JSON", `{"name": "builtin__read_file", "arguments": {"path": "greet.go"}}`, `{"path": "greet.go"}`, true},
		{"llama's parameters, a bare name, prose around it", "Sure.\n{\"name\": \"read_file\", \"parameters\": {\"path\": \"a.go\"}}\n",
			`{"path": "a.go"}`, true},
		{"arguments as a string", `<tool_call>{"name":"read_file","arguments":"{\"path\":\"b.go\"}"}</tool_call>`, `{"path":"b.go"}`, true},
		{"a tool not on the menu", `{"name": "greetingFor", "arguments": {"visits": 5}}`, "", false},
		{"code with braces", "func main() { fmt.Println(\"hi\") }", "", false},
		{"arguments that are not an object", `{"name": "read_file", "arguments": "greet.go"}`, "", false},
	} {
		call, ok := textToolCall(tc.text, menu)
		if ok != tc.ok || (ok && (call.Function.Name != "builtin__read_file" || call.Function.Arguments != tc.wantArgs)) {
			t.Errorf("%s: textToolCall = %+v, %v", tc.name, call, ok)
		}
	}
	var full strings.Builder
	full.WriteString(`{"name": "read_file", "arguments": {}}`)
	s := &Server{logger: log.New(io.Discard, "", 0)}
	if got := s.rescueTextCall("https://api.example.com/v1", []toolSpec{{Function: toolSpecFunction{Name: "builtin__read_file"}}}, nil, &full, 0); got != nil {
		t.Error("a provider not on this machine had its text run as a call")
	}
}

// THE TOOL CHECK: the server's own "no" settles it untried; otherwise the model
// is tried, and how it works follows from the calls, the instructions they
// were made under, and the window.
//
// Neuter checks: drop the localToolsNo branch from checkLocal, and the
// no-tools model is tried; make localCheckTry return true, and the model that
// never calls a tool passes; drop the full-instructions stage, and the model
// that calls only under short instructions is made the full agent.
func TestTheToolCheckDecidesHowAModelWorks(t *testing.T) {
	call := toolCallSSE("c1", "builtin__read_file", `{"path":"notes/todo.txt"}`)
	written := textSSE(`{"name": "read_file", "arguments": {"path": "notes/todo.txt"}}`)
	prose := textSSE("Could you paste the file?")
	always := func(events []string) func(map[string]any) []string {
		return func(map[string]any) []string { return events }
	}
	// What qwen2.5-coder:7b did on 2026-10-24: a call under short instructions,
	// none under the full agent's.
	shortOnly := func(req map[string]any) []string {
		messages, _ := req["messages"].([]any)
		if len(messages) > 0 && messages[0].(map[string]any)["content"] == compactAgentPrompt {
			return written
		}
		return prose
	}
	for _, tc := range []struct {
		name                  string
		capabilities          []string
		window                int
		reply                 func(map[string]any) []string
		way                   string
		tried, passed         int
		fullTried, fullPassed int
	}{
		{"the server says no tools", []string{"completion"}, 32768, always(call), protocol.LocalWayChat, 0, 0, 0, 0},
		{"calls under both, in a big window", []string{"completion", "tools"}, 32768, always(call), protocol.LocalWayFull, 2, 2, 2, 2},
		{"calls under short instructions only", []string{"tools"}, 32768, shortOnly, protocol.LocalWayCompact, 2, 2, 2, 0},
		{"calls, in a middling window", []string{"tools"}, 8192, always(call), protocol.LocalWayCompact, 2, 2, 0, 0},
		{"calls written as text count", []string{"tools"}, 16384, always(written), protocol.LocalWayCompact, 2, 2, 0, 0},
		{"calls, but the window is too small", []string{"tools"}, 4096, always(call), protocol.LocalWayChat, 2, 2, 0, 0},
		{"never calls", []string{"tools"}, 32768, always(prose), protocol.LocalWayChat, 2, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forgetLocalModels(t)
			forgetDialects(t)
			local := newScriptedLocal(t, tc.capabilities, tc.window, func(_ int, req map[string]any) (int, []string) { return 0, tc.reply(req) })
			s := &Server{apiBase: local.base(), modelOverride: "m", contextBudgetChars: 32000, cfg: &Config{},
				logger: log.New(io.Discard, "", 0)}
			pointKindsAt(t, map[string]int{})
			resp := s.checkLocal(context.Background())
			c := resp.Check
			if !resp.Ok || c == nil {
				t.Fatalf("check = %+v", resp)
			}
			if c.Way != tc.way || c.Tried != tc.tried || c.Passed != tc.passed || c.FullTried != tc.fullTried || c.FullPassed != tc.fullPassed {
				t.Errorf("way %q after %d of %d (full: %d of %d), want %q after %d of %d (full: %d of %d) -- %s",
					c.Way, c.Passed, c.Tried, c.FullPassed, c.FullTried, tc.way, tc.passed, tc.tried, tc.fullPassed, tc.fullTried, c.Reason)
			}
			if c.Params != "7.6B" || c.Window != tc.window {
				t.Errorf("params %q, window %d", c.Params, c.Window)
			}
			if got := len(local.requests()); got != tc.tried+tc.fullTried {
				t.Errorf("%d test requests reached the server, want %d", got, tc.tried+tc.fullTried)
			}
			if c.Reason == "" {
				t.Error("no reason given")
			}
			if (tc.capabilities[0] == "completion" && len(tc.capabilities) == 1) == s.localModelTakesTools("m") {
				t.Errorf("takes tools = %v", s.localModelTakesTools("m"))
			}
		})
	}
}

// socketTurnBody asks one question over the real socket declaring caps, reads
// the turn to its Done, and returns the first request the provider received.
func socketTurnBody(t *testing.T, upstream string, bodies *[][]byte, caps []string) (system string, tools []string, raw map[string]any) {
	t.Helper()
	sockAddr, _, _ := agentSocketServerWith(t, upstream, MCPConfig{Enabled: true}, discardLogger(), "BASE PROMPT")
	before := len(*bodies)
	_, dec, conn := agentClient(t, sockAddr, "what does greet.go do?", caps)
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("reading the stream: %v", err)
		}
		if tok.Done {
			break
		}
	}
	if len(*bodies) <= before {
		t.Fatal("no request reached the provider")
	}
	var body struct {
		Messages []chatMessage `json:"messages"`
		Tools    []toolSpec    `json:"tools"`
	}
	if err := json.Unmarshal((*bodies)[before], &body); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal((*bodies)[before], &raw)
	for _, spec := range body.Tools {
		tools = append(tools, strings.TrimPrefix(spec.Function.Name, "builtin__"))
	}
	if len(body.Messages) > 0 {
		system = body.Messages[0].Content
	}
	return system, tools, raw
}

// OVER THE REAL SOCKET: a compact agent's question carries the short
// instructions and none but the five tools; a full agent's, the base prompt.
//
// Neuter check: drop compactTurnMessages from runAgentTurn, and the compact
// question carries the full instructions.
func TestACompactAgentQuestionCarriesShortInstructionsOverTheSocket(t *testing.T) {
	forgetLocalModels(t)
	upstream, _, bodies := agentUpstream(t, textSSE("an answer"), textSSE("an answer"))
	system, tools, _ := socketTurnBody(t, upstream, bodies, []string{protocol.CapToolApproval, protocol.CapCompactAgent, protocol.CapNoNetworkTools})
	// The date line is added to every agent request after the instructions.
	if !strings.HasPrefix(system, compactAgentPrompt) || strings.Contains(system, "BASE PROMPT") || strings.Contains(system, machineReachHeader) {
		t.Errorf("a compact agent's question opened with:\n%s", system)
	}
	if len(tools) == 0 {
		t.Error("a compact agent's question carried no tools at all")
	}
	for _, name := range tools {
		if !compactAgentTools[name] {
			t.Errorf("a compact agent's question offered %s", name)
		}
	}
	if system, _, _ = socketTurnBody(t, upstream, bodies, []string{protocol.CapToolApproval}); !strings.HasPrefix(system, "BASE PROMPT") {
		t.Errorf("a full agent's question lost its instructions:\n%s", system)
	}
}

// OVER THE REAL SOCKET: once a model's server has refused tools, its questions
// go without the agent, so the refusal is not met again on every question.
//
// Neuter check: drop `&& s.localModelTakesTools(decision.Slug)` from the agent
// switch in server.go, and the question carries tools again.
func TestAModelWhoseServerRefusedToolsIsNotAskedWithThemAgain(t *testing.T) {
	forgetLocalModels(t)
	upstream, _, bodies := agentUpstream(t, textSSE("first"), textSSE("second"))
	if _, _, raw := socketTurnBody(t, upstream, bodies, []string{protocol.CapToolApproval}); raw["tools"] == nil {
		t.Fatal("control: an agent question carried no tools")
	}
	localKnown.noTools.Store(dialectKey(upstream, "test-model"), true)
	if _, _, raw := socketTurnBody(t, upstream, bodies, []string{protocol.CapToolApproval}); raw["tools"] != nil {
		t.Error("a model whose server refused tools was asked with them again")
	}
}

// OVER THE REAL SOCKET: a question to a model whose server refuses tools is
// answered, without them, rather than failed.
//
// Neuter check: call runAgentLoop directly in runAgentTurn, without
// withoutToolsIfRefused, and the turn ends in the refusal.
func TestAToolRefusalOverTheSocketIsAnsweredWithoutTools(t *testing.T) {
	forgetLocalModels(t)
	local := newScriptedLocal(t, []string{"completion"}, 4096, func(_ int, req map[string]any) (int, []string) {
		if _, withTools := req["tools"]; withTools {
			return http.StatusBadRequest, []string{ollamaNoTools}
		}
		return 0, textSSE("answered without tools")
	})
	sockAddr, _, _ := agentSocketServerWith(t, local.base(), MCPConfig{Enabled: true}, discardLogger(), "BASE PROMPT")
	_, dec, conn := agentClient(t, sockAddr, "what does greet.go do?", []string{protocol.CapToolApproval})
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var answer strings.Builder
	var done protocol.TokenResponse
	notified := false
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("reading the stream: %v", err)
		}
		answer.WriteString(tok.Token)
		for _, d := range tok.Degraded {
			notified = notified || d.Component == protocol.DegradedLocalNoTools
		}
		if tok.Done {
			done = tok
			break
		}
	}
	if done.Error != "" || answer.String() != "answered without tools" {
		t.Errorf("the turn ended with error %q and answer %q", done.Error, answer.String())
	}
	if !notified {
		t.Error("the person was not told the model cannot use tools")
	}
}
