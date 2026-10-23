package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mochiii/daemon/mcp"
	"mochiii/protocol"
)

// Tests for the terminal app's local model servers: localserver.go,
// localfit.go, localconnect.go and localhandler.go. Every server here is a
// stand-in on 127.0.0.1 that answers the way that kind of server documents.

// standIn is one stand-in model server: the routes it answers, and how many
// chat requests reached it.
type standIn struct {
	srv   *httptest.Server
	chats atomic.Int64
}

func (s *standIn) base() string { return s.srv.URL + "/v1" }

func (s *standIn) port(t *testing.T) int {
	t.Helper()
	u, err := url.Parse(s.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func newStandIn(t *testing.T, routes map[string]any) *standIn {
	t.Helper()
	si := &standIn{}
	mux := http.NewServeMux()
	for pattern, answer := range routes {
		answer := answer
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if f, ok := answer.(func(*http.Request) any); ok {
				_ = json.NewEncoder(w).Encode(f(r))
				return
			}
			_ = json.NewEncoder(w).Encode(answer)
		})
	}
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		si.chats.Add(1)
		http.Error(w, "a stand-in that answers no questions", http.StatusServiceUnavailable)
	})
	si.srv = httptest.NewServer(mux)
	t.Cleanup(si.srv.Close)
	return si
}

func modelList(ids ...string) map[string]any {
	var data []map[string]any
	for _, id := range ids {
		data = append(data, map[string]any{"id": id, "object": "model"})
	}
	return map[string]any{"object": "list", "data": data}
}

// ollamaStandIn loads its one model with window tokens of context when asked
// to generate, as Ollama does, and reports it only while loaded.
func ollamaStandIn(t *testing.T, model string, window int) *standIn {
	t.Helper()
	var mu sync.Mutex
	loaded := false
	return newStandIn(t, map[string]any{
		"GET /v1/models":   modelList(model),
		"GET /api/version": map[string]any{"version": "0.40.2"},
		"POST /api/show":   map[string]any{"model_info": map[string]any{"qwen2.context_length": 32768}},
		"POST /api/generate": func(*http.Request) any {
			mu.Lock()
			loaded = true
			mu.Unlock()
			return map[string]any{"model": model, "done": true}
		},
		"GET /api/ps": func(*http.Request) any {
			mu.Lock()
			defer mu.Unlock()
			if !loaded {
				return map[string]any{"models": []any{}}
			}
			return map[string]any{"models": []any{map[string]any{"name": model, "model": model, "context_length": window}}}
		},
	})
}

func lmstudioStandIn(t *testing.T) *standIn {
	return newStandIn(t, map[string]any{
		"GET /v1/models": modelList("qwen2.5-coder-7b-instruct"),
		"GET /api/v1/models": map[string]any{"models": []any{map[string]any{
			"key": "qwen2.5-coder-7b-instruct", "type": "llm", "max_context_length": 32768,
			"loaded_instances": []any{map[string]any{"id": "qwen2.5-coder-7b-instruct",
				"config": map[string]any{"context_length": 8192}}},
		}}},
	})
}

func llamacppStandIn(t *testing.T) *standIn {
	return newStandIn(t, map[string]any{
		"GET /v1/models": modelList("qwen2.5-coder-7b-q4_k_m.gguf"),
		"GET /props":     map[string]any{"default_generation_settings": map[string]any{"n_ctx": 4096}, "total_slots": 1},
	})
}

func vllmStandIn(t *testing.T) *standIn {
	return newStandIn(t, map[string]any{
		"GET /v1/models": map[string]any{"object": "list", "data": []any{map[string]any{
			"id": "Qwen/Qwen2.5-Coder-7B-Instruct", "object": "model", "owned_by": "vllm", "max_model_len": 16384,
		}}},
	})
}

// pointKindsAt makes discovery probe the given stand-ins in place of the
// default ports, for the duration of the test.
func pointKindsAt(t *testing.T, ports map[string]int) {
	t.Helper()
	saved := localServerKinds
	t.Cleanup(func() { localServerKinds = saved })
	var kinds []localServerKind
	for _, k := range saved {
		if p, ok := ports[k.id]; ok {
			k.port = p
		} else {
			k.port = 1 // nothing listens on port 1
		}
		kinds = append(kinds, k)
	}
	localServerKinds = kinds
}

// Each kind is found, named, and listed with its models; a web app on one of
// the ports, which does not serve the model list, is not.
func TestDiscoveryFindsEachKindOfServerOnThisMachine(t *testing.T) {
	ollama := ollamaStandIn(t, "qwen2.5-coder:3b", 4096)
	lm := lmstudioStandIn(t)
	webapp := newStandIn(t, map[string]any{"GET /": map[string]any{"hello": "a web app"}})
	pointKindsAt(t, map[string]int{"ollama": ollama.port(t), "lmstudio": lm.port(t), "llamacpp": webapp.port(t)})

	servers := discoverLocalServers(context.Background())
	got := map[string]protocol.LocalServer{}
	for _, s := range servers {
		got[s.Kind] = s
	}
	if len(servers) != 2 {
		t.Fatalf("found %d servers, want Ollama and LM Studio only: %+v", len(servers), servers)
	}
	if s := got["ollama"]; s.Name != "Ollama" || s.APIBase != ollama.base() || len(s.Models) != 1 || s.Models[0] != "qwen2.5-coder:3b" {
		t.Errorf("Ollama: %+v", s)
	}
	if s := got["lmstudio"]; s.Name != "LM Studio" || s.APIBase != lm.base() {
		t.Errorf("LM Studio: %+v", s)
	}
}

// The port is not trusted: a server is told by what it serves, so vLLM on
// LM Studio's port is still vLLM.
func TestAServerIsToldByWhatItServesNotItsPort(t *testing.T) {
	vllm := vllmStandIn(t)
	llama := llamacppStandIn(t)
	pointKindsAt(t, map[string]int{"lmstudio": vllm.port(t), "vllm": llama.port(t)})
	kinds := map[string]bool{}
	for _, s := range discoverLocalServers(context.Background()) {
		kinds[s.Kind] = true
	}
	if !kinds["vllm"] || !kinds["llamacpp"] || len(kinds) != 2 {
		t.Errorf("kinds found = %v, want vllm and llamacpp", kinds)
	}
}

// The window is read the way each server reports it. Ollama reports it only
// for a loaded model, so one that is not loaded is loaded first.
func TestTheContextWindowIsReadTheWayEachServerReportsIt(t *testing.T) {
	cases := []struct {
		name            string
		si              *standIn
		model           string
		window, trained int
	}{
		{"ollama, loaded on demand", ollamaStandIn(t, "qwen2.5-coder:3b", 16384), "qwen2.5-coder:3b", 16384, 32768},
		{"lm studio", lmstudioStandIn(t), "qwen2.5-coder-7b-instruct", 8192, 32768},
		{"llama.cpp", llamacppStandIn(t), "qwen2.5-coder-7b-q4_k_m.gguf", 4096, 0},
		{"vllm", vllmStandIn(t), "Qwen/Qwen2.5-Coder-7B-Instruct", 16384, 16384},
	}
	for _, c := range cases {
		w := localContextWindow(context.Background(), c.si.base(), c.model)
		if w.window != c.window || w.trained != c.trained {
			t.Errorf("%s: window %d, trained %d; want %d, %d", c.name, w.window, w.trained, c.window, c.trained)
		}
		if c.si.chats.Load() != 0 {
			t.Errorf("%s: reading the window sent %d chat request(s)", c.name, c.si.chats.Load())
		}
	}
}

func TestTheCodeBudgetFitsTheWindow(t *testing.T) {
	for _, c := range []struct {
		window, configured, want int
		fitted                   bool
	}{
		{0, 32000, 32000, false},     // unknown window: nothing changes
		{4096, 32000, 3072, true},    // Ollama's default
		{8192, 32000, 15360, true},   // LM Studio's common default
		{16384, 32000, 32000, false}, // room enough
		{2048, 32000, 0, true},       // no room for any code at all
		{4096, 0, 0, false},          // retrieval off: nothing to fit
	} {
		got, fitted := fitBudgetChars(c.window, c.configured)
		if got != c.want || fitted != c.fitted {
			t.Errorf("fitBudgetChars(%d, %d) = %d, %v; want %d, %v", c.window, c.configured, got, fitted, c.want, c.fitted)
		}
	}
}

// A fit applies to the address and model it was made for and to nothing else:
// another model on the same server has its own window.
func TestAFitAppliesOnlyToTheModelAndAddressItWasFor(t *testing.T) {
	ollama := ollamaStandIn(t, "qwen2.5-coder:3b", 4096)
	s := &Server{apiBase: ollama.base(), modelOverride: "qwen2.5-coder:3b", contextBudgetChars: 32000,
		cfg: &Config{}, logger: log.New(os.Stderr, "test: ", 0)}

	resp := s.fitLocal(context.Background())
	if !resp.Ok || resp.Context == nil || !resp.Context.Fitted || resp.Context.Window != 4096 {
		t.Fatalf("fit = %+v", resp.Context)
	}
	if resp.Context.AgentFits {
		t.Error("a 4096-token window was said to fit agent mode")
	}
	if !strings.Contains(resp.Context.Raise, "OLLAMA_CONTEXT_LENGTH") {
		t.Errorf("the advice does not say how to raise Ollama's window: %q", resp.Context.Raise)
	}
	if got := s.budgetChars(); got != 3072 {
		t.Errorf("budget after the fit = %d, want 3072", got)
	}
	s.modelOverride = "another-model"
	if got := s.budgetChars(); got != 32000 {
		t.Errorf("another model kept the first one's fit: budget %d", got)
	}
	s.modelOverride = "qwen2.5-coder:3b"
	s.apiBase = "https://openrouter.ai/api/v1"
	if got := s.budgetChars(); got != 32000 {
		t.Errorf("another address kept the local fit: budget %d", got)
	}
	if resp := s.fitLocal(context.Background()); !resp.Ok || resp.Context != nil {
		t.Errorf("a fit for an address off this machine = %+v, want nothing to fit", resp)
	}
}

// THE POINT OF IT: a server on this machine connects with no key, and no model
// is tried -- each try would load a model into memory.
func TestALocalServerConnectsWithNoKeyAndNoModelIsTried(t *testing.T) {
	ollama := ollamaStandIn(t, "qwen2.5-coder:3b", 4096)
	s, path := connectServer(t)

	resp := s.connectResult(context.Background(), protocol.ConnectRequest{Connect: true, APIBase: ollama.base()})
	if !resp.Ok || resp.Outcome != protocol.ConnectAccepted || !resp.InUse || resp.Model != "qwen2.5-coder:3b" {
		t.Fatalf("connect = %+v", resp)
	}
	if key, base := s.credentials(); key != "" || base != ollama.base() {
		t.Errorf("in force: key %q, base %q; want no key and %s", key, base, ollama.base())
	}
	if n := ollama.chats.Load(); n != 0 {
		t.Errorf("connecting sent %d chat request(s); a local connect tries no model", n)
	}
	stored, _, err := loadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.APIKey != "" || stored.APIBase != ollama.base() || !stored.usable() || !stored.inForce("", ollama.base()) {
		t.Errorf("stored = %+v, want a keyless local credential that is usable and in force", stored)
	}
	if !stored.inForce("", ollama.base()+"/") || stored.inForce("", "https://openrouter.ai/api/v1") {
		t.Error("inForce does not tell the local server's address from another")
	}
	if got := s.route("", "").Slug; got != "qwen2.5-coder:3b" {
		t.Errorf("the next question goes to %q, want the server's own model", got)
	}
}

// An empty key is still refused for any address off this machine, so no
// provider is ever sent a request with no credential by this path.
func TestAnEmptyKeyIsStillRefusedForAnAddressOffThisMachine(t *testing.T) {
	s, path := connectServer(t)
	resp := s.connectResult(context.Background(), protocol.ConnectRequest{Connect: true, APIBase: "https://api.groq.com/openai/v1"})
	// Refused by the key rule, before anything is sent: "nothing answered" or
	// "asks for a key" would mean the request went out to find that out.
	if resp.Ok || resp.Error != "no key given; nothing was saved" {
		t.Fatalf("connect with no key to a provider = %+v, want refused for having no key", resp)
	}
	if got := s.connectLocal(context.Background(), "https://api.groq.com/openai/v1", path, false); got.Ok ||
		!strings.Contains(got.Error, "not on this computer") {
		t.Errorf("the keyless connect itself took an address off this machine: %+v", got)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a refused connect stored a credential")
	}
	if key, base := s.credentials(); key != "sk-the-key-it-started-with" || base != "https://old.example/v1" {
		t.Error("a refused connect changed what is in force")
	}
}

func TestALocalAddressWithNothingRunningIsSaidPlainly(t *testing.T) {
	s, path := connectServer(t)
	resp := s.connectResult(context.Background(), protocol.ConnectRequest{Connect: true, APIBase: "http://127.0.0.1:1/v1"})
	if resp.Ok || !strings.Contains(resp.Error, "nothing answered") {
		t.Fatalf("connect to nothing = %+v", resp)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a failed connect stored a credential")
	}
}

// A keyless local credential survives a restart: startup fills the address in
// from it and treats it as the one in force, so its models are used.
func TestAKeylessLocalServerIsInForceAfterARestart(t *testing.T) {
	stored := storedCredential{APIBase: "http://127.0.0.1:11434/v1", DefaultModel: "qwen2.5-coder:3b", Models: []string{"qwen2.5-coder:3b"}}
	key, base := fillFromStored("", "", stored, t.Logf)
	if key != "" || base != stored.APIBase {
		t.Fatalf("fillFromStored = %q, %q", key, base)
	}
	if !stored.inForce(key, base) {
		t.Error("the stored local server is not taken as the one in force, so its models would not be used")
	}
	if (storedCredential{APIBase: "https://api.groq.com/openai/v1"}).usable() {
		t.Error("a provider's address with no key counts as usable")
	}
}

// THE PRIVACY PROMISE: with CapNoNetworkTools, an internet tool is neither on
// the menu nor run when the model names it anyway.
//
// Neuter checks: drop dropsNetworkTool from advertisedToolSpecsFor and the menu
// carries web_search; make lookupForTurn plain registry.Lookup and the search
// runs.
func TestInternetToolsAreNeitherOfferedNorRunWhenTheClientAskedForNone(t *testing.T) {
	run := func(t *testing.T, caps ...string) (offered string, searched int) {
		t.Helper()
		base, _, bodies := agentUpstream(t,
			toolCallSSE("c1", "builtin__web_search", `{"query":"what is new"}`),
			textSSE("done"))
		cfg := MCPConfig{Enabled: true, Builtin: MCPBuiltinConfig{Tools: map[string]string{
			"read_file": PolicyAllow, "web_search": PolicyAllow,
		}}}
		s := loopServer(t, base, cfg)
		registry := mcp.NewRegistry(configPolicy{cfg: s.cfg}, 20)
		t.Cleanup(func() { _ = registry.Close() })
		var calls atomic.Int64
		for _, b := range []mcp.Builtin{
			{Tool: mcp.Tool{Name: "read_file", Description: "stub", Schema: schema(`{"type":"object"}`), ReadOnlyHint: true, Confined: true},
				Handler: func(context.Context, json.RawMessage) (mcp.Result, error) { return mcp.Result{Content: "a file"}, nil }},
			{Tool: mcp.Tool{Name: "web_search", Description: "stub", Schema: schema(`{"type":"object"}`), ReadOnlyHint: true, ReachesNetwork: true},
				Handler: func(context.Context, json.RawMessage) (mcp.Result, error) {
					calls.Add(1)
					return mcp.Result{Content: "results"}, nil
				}},
		} {
			if err := registry.RegisterBuiltin(b); err != nil {
				t.Fatal(err)
			}
		}
		ctx := withClientCaps(context.Background(), protocol.HandshakeRequest{Capabilities: caps})
		if _, err := s.runAgentLoop(ctx, time.Now(), registry, "m", "auto",
			[]chatMessage{{Role: "system", Content: "SYSTEM"}, {Role: "user", Content: "search the web"}}, providerRouting{},
			&capturingApprover{decision: protocol.ApprovalDeny},
			func(string) error { return nil }, func(protocol.ToolActivity) {}, nil, nil,
			func(protocol.Degradation) {}, nil, nil); err != nil {
			t.Fatalf("runAgentLoop: %v", err)
		}
		if len(*bodies) == 0 {
			t.Fatal("the model was never asked")
		}
		return string((*bodies)[0]), int(calls.Load())
	}

	offered, searched := run(t, protocol.CapToolApproval)
	if !strings.Contains(offered, "web_search") || searched != 1 {
		t.Fatalf("control: web_search offered=%v, run %d time(s); the stand-ins are not exercising the tool",
			strings.Contains(offered, "web_search"), searched)
	}
	offered, searched = run(t, protocol.CapToolApproval, protocol.CapNoNetworkTools)
	if strings.Contains(offered, "web_search") {
		t.Error("web_search was on the menu of a client that asked for no internet tools")
	}
	if !strings.Contains(offered, "read_file") {
		t.Error("read_file left the menu too: only internet tools may go")
	}
	if searched != 0 {
		t.Errorf("web_search ran %d time(s) when the model named it anyway", searched)
	}
}

// The local request is its own type on the socket: "local" is a discriminator,
// and a body with it and another is refused as ambiguous rather than guessed.
func TestALocalRequestIsRecognisedByItsOwnKeyAlone(t *testing.T) {
	if !isLocalRequest(json.RawMessage(`{"protocol_version":1,"local":true,"discover":true}`)) {
		t.Error("a local request was not recognised")
	}
	if isLocalRequest(json.RawMessage(`{"local":true,"connect":true}`)) {
		t.Error("a body naming two request types was taken as a local request")
	}
	if isLocalRequest(json.RawMessage(`{"prompt":"local"}`)) {
		t.Error("a prompt mentioning the word was taken as a local request")
	}
}

// The handshake names a local model in force, and nothing else does.
func TestTheHandshakeSaysWhenALocalModelIsInForce(t *testing.T) {
	has := func(s *Server, f string) bool {
		for _, x := range s.features() {
			if x == f {
				return true
			}
		}
		return false
	}
	local := &Server{apiBase: "http://127.0.0.1:11434/v1", cfg: &Config{}}
	cloud := &Server{apiBase: "https://openrouter.ai/api/v1", apiKey: "sk-or-x", cfg: &Config{}}
	if !has(local, protocol.FeatureLocalModels) || !has(local, protocol.FeatureLocalInForce) {
		t.Errorf("a local server in force: features %v", local.features())
	}
	if !has(cloud, protocol.FeatureLocalModels) || has(cloud, protocol.FeatureLocalInForce) {
		t.Errorf("a cloud provider in force: features %v", cloud.features())
	}
}
