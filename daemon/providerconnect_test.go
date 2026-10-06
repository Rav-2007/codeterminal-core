package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"mochiii/protocol"
)

// Keys from providers other than OpenRouter: where a key goes, how a request to
// that provider is worded, which of its models is used, and what proves the
// three work together. Everything here runs against a provider on loopback --
// no real provider is contacted and nothing is spent.

// fakeProvider is an OpenAI-compatible provider with the STRICTNESS the real
// ones have: it answers a request it does not like with HTTP 400 and words
// naming the field, which is what the daemon has to cope with.
type fakeProvider struct {
	// models is what GET /models lists.
	models []string
	// openModels serves /models to any key at all, as NVIDIA does.
	openModels bool
	// goodKey is the only key chat accepts ("" accepts every key).
	goodKey string
	// badKeyStatus is what chat answers a wrong key with (default 401).
	badKeyStatus int
	// refuse inspects an authenticated chat request and returns a non-zero
	// status with a body to refuse it.
	refuse func(req map[string]any) (int, string)
	// reasoning, when set, is streamed as reasoning_content before the answer.
	reasoning string
	// silent models accept the request and never send a byte -- what NVIDIA did
	// for most of the well-known models on its list (measured 2026-10-05).
	silent map[string]bool

	mu   sync.Mutex
	chat []map[string]any // every chat body received, authenticated or not
	srv  *httptest.Server
}

func (f *fakeProvider) start(t *testing.T) *fakeProvider {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		if !f.openModels && !f.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"Invalid API Key"}}`)
			return
		}
		data := make([]map[string]any, 0, len(f.models))
		for _, id := range f.models {
			data = append(data, map[string]any{"id": id, "object": "model"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.chat = append(f.chat, req)
		f.mu.Unlock()

		if !f.authorized(r) {
			status := f.badKeyStatus
			if status == 0 {
				status = http.StatusUnauthorized
			}
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"status":403,"title":"Forbidden","detail":"Authorization failed"}`)
			return
		}
		if model, _ := req["model"].(string); f.silent[model] {
			<-r.Context().Done()
			return
		}
		if f.refuse != nil {
			if status, body := f.refuse(req); status != 0 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, body)
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if f.reasoning != "" {
			_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":%q}}]}\n\n", f.reasoning)
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeProvider) authorized(r *http.Request) bool {
	return f.goodKey == "" || r.Header.Get("Authorization") == "Bearer "+f.goodKey
}

func (f *fakeProvider) url() string { return f.srv.URL }

// bodies returns the chat requests received so far.
func (f *fakeProvider) bodies() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.chat...)
}

// knownAs makes a loopback address a named provider for one test, and starts
// that test with nothing remembered about how to word a request.
func knownAs(t *testing.T, apiBase string, p protocol.Provider) {
	t.Helper()
	p.APIBase = apiBase
	prev := providerForBase
	providerForBase = func(base string) (protocol.Provider, bool) {
		if sameAPIBase(base, apiBase) {
			return p, true
		}
		return prev(base)
	}
	t.Cleanup(func() { providerForBase = prev })
	forgetDialects(t)
}

// keyPrefixNames makes keys starting with prefix belong to p for one test.
func keyPrefixNames(t *testing.T, prefix string, p protocol.Provider) {
	t.Helper()
	prev := providersForKey
	providersForKey = func(key string) ([]protocol.Provider, bool) {
		if strings.HasPrefix(key, prefix) {
			return []protocol.Provider{p}, true
		}
		return prev(key)
	}
	t.Cleanup(func() { providersForKey = prev })
}

func forgetDialects(t *testing.T) {
	t.Helper()
	clear := func() {
		dialectMemo.Range(func(k, _ any) bool { dialectMemo.Delete(k); return true })
	}
	clear()
	t.Cleanup(clear)
}

var (
	nvidiaLike     = protocol.Provider{ID: "nvidia", Name: "NVIDIA"}
	openRouterLike = protocol.Provider{ID: protocol.ProviderOpenRouter, Name: "OpenRouter"}
)

const (
	nvidiaKey       = "nvapi-0123456789abcdefSECRET"
	openRouterBase  = "https://openrouter.ai/api/v1"
	testRoutingCap  = defaultMaxOutputTokens
	fakeModelFlash  = "deepseek-ai/deepseek-v4.1-flash"
	fakeModelKimi   = "moonshotai/kimi-k3"
	fakeModelEmbed  = "nvidia/nemotron-3-embed-1b"
	fakeModelLegacy = "deepseek-ai/deepseek-coder-6.7b-instruct"
)

// transportErr is what a caller with no stall watchdog passes openCompletion.
func transportErr(err error) error { return classifyTransportError(err) }

func plainRouting() providerRouting { return providerRouting{maxTokens: testRoutingCap} }

// ---------------------------------------------------------------------------
// Where a key goes
// ---------------------------------------------------------------------------

// THE REPORTED BUG, at its root: an NVIDIA key pasted into a daemon on
// OpenRouter was checked by OpenRouter. The key's own prefix says whose it is,
// and that is now where it goes -- with every case that must NOT move a key.
func TestResolveConnectBase(t *testing.T) {
	const custom = "https://llm.example.com/v1"
	nvidia, _ := protocol.ProviderByName("nvidia")
	openAI, _ := protocol.ProviderByName("openai")
	deepSeek, _ := protocol.ProviderByName("deepseek")
	stored := func(base string) storedCredential { return storedCredential{APIBase: base, APIKey: "k"} }

	for _, tc := range []struct {
		name                      string
		explicit, key, inUse, env string
		stored                    storedCredential
		wantBase                  string
		wantCandidates            bool
	}{
		{name: "a named address always wins", explicit: custom, key: nvidiaKey, inUse: openRouterBase, wantBase: custom},
		{name: "an NVIDIA key on OpenRouter goes to NVIDIA", key: nvidiaKey, inUse: openRouterBase, wantBase: nvidia.APIBase},
		{name: "an OpenRouter key on NVIDIA goes back to OpenRouter", key: "sk-or-v1-abc", inUse: nvidia.APIBase, wantBase: openRouterBase},
		{name: "a key for the provider in use stays on the address in use", key: "sk-or-v1-abc", inUse: "https://openrouter.ai/api/v1/", wantBase: "https://openrouter.ai/api/v1/"},
		{name: "a custom address in use is never left on a guess", key: nvidiaKey, inUse: custom, wantBase: custom},
		{name: "nor is a local server", key: "sk-proj-abc", inUse: "http://localhost:11434/v1", wantBase: "http://localhost:11434/v1"},
		{name: "a bare sk- key on OpenRouter is nobody's", key: "sk-0123456789abcdef", inUse: openRouterBase, wantCandidates: true},
		{name: "a bare sk- key on DeepSeek is DeepSeek's", key: "sk-0123456789abcdef", inUse: deepSeek.APIBase, wantBase: deepSeek.APIBase},
		{name: "a key with no prefix stays with the provider in use", key: "0123456789abcdef0123", inUse: openRouterBase, wantBase: openRouterBase},
		{name: "with nothing running, the stored provider is the one in use", key: "sk-0123456789abcdef", stored: stored(openAI.APIBase), wantBase: openAI.APIBase},
		{name: "then the environment's", key: "0123456789abcdef0123", env: nvidia.APIBase, wantBase: nvidia.APIBase},
		{name: "then the default", key: "sk-or-v1-abc", wantBase: defaultAPIBase},
		{name: "and the key still names its provider from the CLI", key: nvidiaKey, wantBase: nvidia.APIBase},
	} {
		got := resolveConnectBase(tc.explicit, tc.key, tc.inUse, tc.stored, tc.env)
		if tc.wantCandidates {
			if got.Base != "" || len(got.Candidates) < 2 {
				t.Errorf("%s: got base %q and %d candidate(s); want no base and a choice to put to the user", tc.name, got.Base, len(got.Candidates))
			}
			continue
		}
		if got.Base != tc.wantBase || len(got.Candidates) != 0 {
			t.Errorf("%s: base = %q (%d candidates), want %q", tc.name, got.Base, len(got.Candidates), tc.wantBase)
		}
	}
}

// ---------------------------------------------------------------------------
// How a request is worded
// ---------------------------------------------------------------------------

// The routing dialect goes everywhere it always did. Only a provider known by
// name, and known not to be OpenRouter, is spoken to differently.
func TestOnlyANamedProviderGetsThePlainDialect(t *testing.T) {
	t.Setenv("MOCHIII_USE_PROXY", "")
	for base, want := range map[string]bool{
		"https://openrouter.ai/api/v1":        true,
		"http://localhost:11434/v1":           true,
		"http://127.0.0.1:8080/v1":            true,
		"https://llm.example.com/v1":          true,
		"":                                    true,
		"https://integrate.api.nvidia.com/v1": false,
		"https://api.openai.com/v1":           false,
		"https://api.anthropic.com/v1":        false,
	} {
		if got := usesRoutingDialect(base); got != want {
			t.Errorf("usesRoutingDialect(%q) = %v, want %v", base, got, want)
		}
	}

	// PROXY MODE IS DECIDED BY THE DAEMON'S SETTING, NOT THE ADDRESS. The proxy
	// refuses a request without the routing object, wherever it is hosted.
	t.Setenv("MOCHIII_USE_PROXY", "true")
	if !usesRoutingDialect("https://api.openai.com/v1") {
		t.Error("proxy mode dropped the routing object because of what the address looked like")
	}
}

// THE BODY SENT TO OPENROUTER IS BYTE-FOR-BYTE WHAT IT WAS. Same literal as
// TestRequestBodyIsByteIdenticalWithoutTools, but through the code that now
// builds every request -- that test marshals the struct directly and would not
// notice the dialect layer changing what goes on the wire.
//
// Neuter check: drop Provider from requestDialect.body's routing branch.
func TestTheRoutingDialectSendsExactlyWhatWasAlwaysSent(t *testing.T) {
	t.Setenv("MOCHIII_USE_PROXY", "")
	routing := (&ZDRConfig{}).resolvedProviderRouting()
	messages := buildChatMessages("you are an assistant", []chatMessage{{Role: "user", Content: "earlier"}}, "hello")

	got, err := dialectFor(openRouterBase, "deepseek/deepseek-v4-flash").body("deepseek/deepseek-v4-flash", messages, nil, routing)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"deepseek/deepseek-v4-flash",` +
		`"messages":[{"role":"system","content":"you are an assistant"},` +
		`{"role":"user","content":"earlier"},` +
		`{"role":"user","content":"hello"}],` +
		`"stream":true,` +
		`"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":false},` +
		`"stream_options":{"include_usage":true}}`
	if string(got) != want {
		t.Errorf("the body sent to OpenRouter changed.\n got: %s\nwant: %s", got, want)
	}

	// And a reasoning effort and a cap still travel exactly as they did.
	routing.reasoningEffort, routing.maxTokens = "high", 1234
	got, _ = dialectFor(openRouterBase, "m").body("m", messages[:1], nil, routing)
	for _, part := range []string{`"reasoning":{"effort":"high"}`, `"max_tokens":1234`, `"provider":{"zdr":true`} {
		if !strings.Contains(string(got), part) {
			t.Errorf("the routing dialect lost %s:\n%s", part, got)
		}
	}
}

// What OpenAI and the rest refuse with HTTP 400 is exactly what this leaves out.
func TestThePlainDialectCarriesNothingOnlyOpenRouterUnderstands(t *testing.T) {
	routing := (&ZDRConfig{}).resolvedProviderRouting()
	routing.reasoningEffort, routing.maxTokens = "high", 4096
	body, err := dialectFor("https://integrate.api.nvidia.com/v1", "some-model-never-memoised").
		body("m", []chatMessage{{Role: "user", Content: "hi"}}, []toolSpec{probeTool}, routing)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	// reasoning_effort is the OpenAI-defined field Groq and OpenAI read; it is
	// NOT OpenRouter's "reasoning" object, which must never appear here.
	want := map[string]bool{"model": true, "messages": true, "tools": true, "stream": true, "stream_options": true, "max_tokens": true, "reasoning_effort": true}
	for name := range fields {
		if !want[name] {
			t.Errorf("the plain dialect sent %q, which only OpenRouter understands:\n%s", name, body)
		}
		delete(want, name)
	}
	for name := range want {
		t.Errorf("the plain dialect did not send %q:\n%s", name, body)
	}
	if string(fields["max_tokens"]) != "4096" {
		t.Errorf("max_tokens = %s, want the tier's cap", fields["max_tokens"])
	}
	if string(fields["reasoning_effort"]) != `"high"` {
		t.Errorf("reasoning_effort = %s, want the effort asked for", fields["reasoning_effort"])
	}
}

// No effort asked for means no field: a turn that never touched the picker
// sends exactly the body it sent before the field existed.
func TestThePlainDialectSendsNoEffortUnlessOneIsAsked(t *testing.T) {
	routing := (&ZDRConfig{}).resolvedProviderRouting()
	body, err := dialectFor("https://api.groq.com/openai/v1", "never-memoised-effort-model").
		body("m", []chatMessage{{Role: "user", Content: "hi"}}, nil, routing)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "reasoning") {
		t.Errorf("no effort was asked for, yet the body mentions reasoning:\n%s", body)
	}
	// And once a provider has refused the field, it is not sent again.
	routing.reasoningEffort = "medium"
	body, _ = requestDialect{noReasoningEffort: true}.body("m", []chatMessage{{Role: "user", Content: "hi"}}, nil, routing)
	if strings.Contains(string(body), "reasoning_effort") {
		t.Errorf("reasoning_effort was sent after the provider refused it:\n%s", body)
	}
}

// A provider's refusal names the field it objects to, and that field is what
// changes. Nothing changes for a refusal that names none of them, and nothing
// ever changes in the routing dialect.
func TestDialectAdaptsToWhatTheProviderSaid(t *testing.T) {
	plain := requestDialect{streamOptions: true, maxTokensField: fieldMaxTokens}
	for _, tc := range []struct {
		name   string
		from   requestDialect
		status int
		body   string
		want   requestDialect
		change bool
	}{
		{"max_tokens is the wrong name", plain, 400,
			`Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.`,
			requestDialect{streamOptions: true, maxTokensField: fieldMaxCompletionTokens}, true},
		{"stream_options is not understood", plain, 422,
			`{"detail":[{"type":"extra_forbidden","loc":["body","stream_options"],"msg":"Extra inputs are not permitted"}]}`,
			requestDialect{maxTokensField: fieldMaxTokens}, true},
		{"the cap is too large", plain, 400,
			"`max_tokens` must be less than or equal to `8192`",
			requestDialect{streamOptions: true, maxTokensField: fieldMaxTokens, maxTokens: 16384}, true},
		{"the cap alone overflows a small context window", plain, 400,
			`This model's maximum context length is 16384 tokens. However, you requested 32790 tokens`,
			requestDialect{streamOptions: true, maxTokensField: fieldMaxTokens, maxTokens: 16384}, true},
		{"the next step down", requestDialect{maxTokensField: fieldMaxCompletionTokens, maxTokens: 16384}, 400,
			`max_completion_tokens is too large`,
			requestDialect{maxTokensField: fieldMaxCompletionTokens, maxTokens: 8192}, true},
		{"refused at the smallest cap: send none", requestDialect{maxTokensField: fieldMaxTokens, maxTokens: 4096}, 400,
			`max_tokens is not supported`,
			requestDialect{}, true},
		{"the model refuses reasoning_effort (Groq's own words, measured)", plain, 400,
			"`reasoning_effort` must be one of `low`, `medium`, or `high`",
			requestDialect{streamOptions: true, maxTokensField: fieldMaxTokens, noReasoningEffort: true}, true},
		{"a strict server forbids the extra field", plain, 422,
			`{"detail":[{"type":"extra_forbidden","loc":["body","reasoning_effort"],"msg":"Extra inputs are not permitted"}]}`,
			requestDialect{streamOptions: true, maxTokensField: fieldMaxTokens, noReasoningEffort: true}, true},
		{"a refusal about something else", plain, 400, `{"error":"messages: field required"}`, plain, false},
		{"a refused key is not a wording problem", plain, 401, `invalid api key, check max_tokens`, plain, false},
		{"a server error is not a wording problem", plain, 500, `max_tokens`, plain, false},
		{"the routing dialect is never adapted", requestDialect{routing: true}, 400,
			`unknown field "provider"; max_tokens too large; stream_options not allowed`, requestDialect{routing: true}, false},
	} {
		got, changed := tc.from.adapt(tc.status, tc.body, testRoutingCap)
		if changed != tc.change || got != tc.want {
			t.Errorf("%s: adapt = %+v changed=%v, want %+v changed=%v", tc.name, got, changed, tc.want, tc.change)
		}
	}
}

// END TO END AGAINST A STRICT PROVIDER: wrong name for the cap, then a cap that
// is too large, and the request is asked again each time until it is accepted.
// The second call then asks the right way FIRST, because what worked was
// remembered.
//
// Neuter check: return d, false from requestDialect.adapt.
func TestARequestIsRewordedUntilTheProviderAcceptsIt(t *testing.T) {
	p := (&fakeProvider{refuse: func(req map[string]any) (int, string) {
		if _, sent := req["provider"]; sent {
			return 400, `Unrecognized request argument supplied: provider`
		}
		if _, sent := req["max_tokens"]; sent {
			return 400, `Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.`
		}
		if limit, _ := req["max_completion_tokens"].(float64); limit > 8192 {
			return 400, fmt.Sprintf("max_completion_tokens is too large: %d. This model supports at most 8192 completion tokens", int(limit))
		}
		return 0, ""
	}}).start(t)
	knownAs(t, p.url(), nvidiaLike)

	call := func() {
		t.Helper()
		resp, err := openCompletion(context.Background(), p.url(), "k", "m", []chatMessage{{Role: "user", Content: "hi"}}, nil, plainRouting(), transportErr)
		if err != nil {
			t.Fatalf("the provider never accepted the request: %v", err)
		}
		_ = resp.Body.Close()
	}

	call()
	got := p.bodies()
	want := []string{"max_tokens=32768", "max_completion_tokens=32768", "max_completion_tokens=16384", "max_completion_tokens=8192"}
	if len(got) != len(want) {
		t.Fatalf("%d request(s) sent, want %d: %v", len(got), len(want), capsOf(got))
	}
	for i, c := range capsOf(got) {
		if c != want[i] {
			t.Errorf("request %d carried %s, want %s", i+1, c, want[i])
		}
	}
	for i, body := range got {
		if _, sent := body["provider"]; sent {
			t.Errorf("request %d carried OpenRouter's routing object to a provider that refuses it", i+1)
		}
	}

	call()
	if all := capsOf(p.bodies()); len(all) != 5 || all[4] != "max_completion_tokens=8192" {
		t.Errorf("the second call did not start from what worked: %v", all)
	}
}

func capsOf(bodies []map[string]any) []string {
	var out []string
	for _, b := range bodies {
		switch {
		case b["max_tokens"] != nil:
			out = append(out, fmt.Sprintf("max_tokens=%v", b["max_tokens"]))
		case b["max_completion_tokens"] != nil:
			out = append(out, fmt.Sprintf("max_completion_tokens=%v", b["max_completion_tokens"]))
		default:
			out = append(out, "no cap")
		}
	}
	return out
}

// NOTHING IS TAKEN OUT OF A REQUEST TO OPENROUTER BECAUSE A RESPONSE SAID SO.
// An address not known by name gets the routing dialect, one request, and the
// provider's refusal reported as it stands.
func TestARoutingRequestIsSentOnceAndNeverReworded(t *testing.T) {
	t.Setenv("MOCHIII_USE_PROXY", "")
	p := (&fakeProvider{refuse: func(map[string]any) (int, string) {
		return 400, `unknown field "provider"; max_tokens is too large; stream_options is not permitted`
	}}).start(t)
	forgetDialects(t)

	resp, err := openCompletion(context.Background(), p.url(), "k", "m", []chatMessage{{Role: "user", Content: "hi"}}, nil,
		(&ZDRConfig{}).resolvedProviderRouting(), transportErr)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a refused request was reported as accepted")
	}
	bodies := p.bodies()
	if len(bodies) != 1 {
		t.Fatalf("%d requests sent, want exactly 1", len(bodies))
	}
	if _, sent := bodies[0]["provider"]; !sent {
		t.Error("the routing object was not on the one request that was sent")
	}
}

// A chain of rewordings that still ends in a refusal proved nothing about what
// the provider wants, so none of it is kept -- and the chain is bounded.
func TestAFailedRewordingIsBoundedAndNotRemembered(t *testing.T) {
	p := (&fakeProvider{refuse: func(map[string]any) (int, string) { return 400, `max_tokens is too large` }}).start(t)
	knownAs(t, p.url(), nvidiaLike)

	resp, err := openCompletion(context.Background(), p.url(), "k", "m", []chatMessage{{Role: "user", Content: "hi"}}, nil, plainRouting(), transportErr)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a provider that refuses everything was reported as accepting")
	}
	if n := len(p.bodies()); n < 2 || n > maxDialectAttempts {
		t.Errorf("%d requests sent, want between 2 and %d", n, maxDialectAttempts)
	}
	if got := dialectFor(p.url(), "m"); got != (requestDialect{streamOptions: true, maxTokensField: fieldMaxTokens}) {
		t.Errorf("a wording that never worked was remembered: %+v", got)
	}
}

// A whole turn against a named provider: the answer streams, and thinking sent
// as reasoning_content -- the name DeepSeek's own API uses -- reaches the
// reasoning callback rather than being dropped or mixed into the answer.
func TestStreamCompletionAgainstANamedProvider(t *testing.T) {
	p := (&fakeProvider{reasoning: " thinking it over"}).start(t)
	knownAs(t, p.url(), nvidiaLike)

	var answer, reasoning strings.Builder
	_, err := streamCompletion(context.Background(), p.url(), "k", "m", []chatMessage{{Role: "user", Content: "hi"}}, nil,
		(&ZDRConfig{}).resolvedProviderRouting(),
		func(tok string) error { answer.WriteString(tok); return nil }, nil,
		func(r string) { reasoning.WriteString(r) }, nil)
	if err != nil {
		t.Fatalf("streamCompletion: %v", err)
	}
	if answer.String() != "ok" {
		t.Errorf("answer = %q, want %q", answer.String(), "ok")
	}
	if reasoning.String() != " thinking it over" {
		t.Errorf("reasoning = %q, want it delivered exactly as sent, leading space included", reasoning.String())
	}
	if _, sent := p.bodies()[0]["provider"]; sent {
		t.Error("the routing object was sent to a named provider that is not OpenRouter")
	}
}

// Google and xAI answer a wrong key with HTTP 400, not 401 (measured
// 2026-10-05). Read as "unknown", the client never offered to take another key.
func TestABadKeyReportedAs400IsStillABadKey(t *testing.T) {
	for _, body := range []string{
		`[{"error":{"code":400,"message":"Please pass a valid API key","status":"INVALID_ARGUMENT"}}]`,
		`{"code":"invalid-argument","error":"Incorrect API key provided. You can obtain an API key from https://console.x.ai."}`,
		`{"error":{"message":"API key not valid. Please pass a valid API key."}}`,
	} {
		if got := classifyHTTPError(http.StatusBadRequest, "400 Bad Request", body).Class; got != ClassAuth {
			t.Errorf("class = %q for %s, want auth", got, body)
		}
	}
	if got := classifyHTTPError(http.StatusBadRequest, "400 Bad Request", `{"error":"messages: field required"}`).Class; got == ClassAuth {
		t.Error("an ordinary 400 was classed as a bad key")
	}
}

// ---------------------------------------------------------------------------
// Which model
// ---------------------------------------------------------------------------

func TestListModelsReadsBothShapesInUse(t *testing.T) {
	for name, payload := range map[string]string{
		"the OpenAI shape": `{"object":"list","data":[{"id":"a"},{"id":"b","type":"chat"}]}`,
		"a bare array":     `[{"id":"a","type":"chat"},{"id":"b","type":"embedding"}]`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer k" {
				t.Errorf("%s: the key was not sent as a bearer token", name)
			}
			_, _ = io.WriteString(w, payload)
		}))
		models, status, _, err := listModels(context.Background(), http.DefaultClient, srv.URL, "k")
		srv.Close()
		if err != nil || status != http.StatusOK || len(models) != 2 || models[0].ID != "a" {
			t.Errorf("%s: models=%v status=%d err=%v", name, models, status, err)
		}
	}

	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"Please pass a valid API key"}}`)
	}))
	defer refusing.Close()
	_, status, body, err := listModels(context.Background(), http.DefaultClient, refusing.URL, "k")
	if err != nil || !refusesKey(status, body) {
		t.Errorf("a 400 that says the key is bad was not read as a refusal: status=%d body=%q err=%v", status, body, err)
	}
	if refusesKey(http.StatusBadRequest, "something else") || refusesKey(http.StatusNotFound, "") {
		t.Error("a response that says nothing about the key was read as refusing it")
	}
}

// /model must not offer something a prompt cannot be sent to -- and "mini" is a
// word, not four letters of gemini or minimax.
func TestChatModelIDsKeepsOnlyModelsThatHoldAConversation(t *testing.T) {
	got := chatModelIDs([]listedModel{
		{ID: "nvidia/nemotron-3-embed-1b"}, {ID: "nvidia/llama-3.1-nemoguard-8b-content-safety"},
		{ID: "nvidia/nemotron-parse"}, {ID: "openai/whisper-large-v3"}, {ID: "gpt-image-2"},
		{ID: "text-embedding-3-large"}, {ID: "black-forest/flux", Type: "image"},
		{ID: "models/gemini-3.6-flash"}, {ID: "minimax/minimax-m3"}, {ID: "moonshotai/kimi-k3", Type: "chat"},
		{ID: "moonshotai/kimi-k3"}, {ID: " "}, {ID: "gpt-5.5-mini"},
	})
	want := []string{"gemini-3.6-flash", "gpt-5.5-mini", "minimax/minimax-m3", "moonshotai/kimi-k3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("chat models = %v, want %v", got, want)
	}
}

// The order candidates are TRIED in. It decides nothing by itself -- a model is
// used only once it has answered -- but it decides what is tried first.
func TestRankModelsPutsTheEverydayModelFirst(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ids   []string
		first string
	}{
		// NVIDIA's real list on 2026-10-05, the chat models that matter here.
		{"NVIDIA", []string{"deepseek-ai/deepseek-coder-6.7b-instruct", "openai/gpt-oss-20b", "moonshotai/kimi-k2.6",
			"moonshotai/kimi-k3", "z-ai/glm-5.3", "meta/llama2-70b", "deepseek-ai/deepseek-v4.1-flash",
			"google/gemma-3-4b-it", "mistralai/mistral-large"}, "deepseek-ai/deepseek-v4.1-flash"},
		{"the plain model beats its small and its costly sibling", []string{"gpt-5.5-mini", "gpt-5.5-pro", "gpt-5.5", "gpt-5.5-nano"}, "gpt-5.5"},
		{"a newer version beats an older one", []string{"gpt-4o", "gpt-5", "gpt-5.5", "gpt-4.1"}, "gpt-5.5"},
		{"sonnet before opus and haiku", []string{"claude-haiku-4-5", "claude-opus-5-5", "claude-sonnet-5-5"}, "claude-sonnet-5-5"},
		{"gemini and minimax are not 'mini'", []string{"gemini-3.6-flash-lite", "gemini-3.6-flash"}, "gemini-3.6-flash"},
		{"pro is the better model outside gpt", []string{"deepseek-v4-flash", "deepseek-v4-pro"}, "deepseek-v4-pro"},
		{"a dated build is not a version", []string{"mistral-large-2411", "mistral-large-3"}, "mistral-large-3"},
		{"an unknown family still ranks", []string{"zeta-2", "zeta-10"}, "zeta-10"},
	} {
		if got := rankModels(tc.ids); got[0] != tc.first {
			t.Errorf("%s: first candidate = %q, want %q (order: %v)", tc.name, got[0], tc.first, got)
		}
	}
	if kimi := rankModels([]string{"moonshotai/kimi-k2.6", "moonshotai/kimi-k3"}); kimi[0] != "moonshotai/kimi-k3" {
		t.Errorf("k2.6 ranked ahead of k3: %v", kimi)
	}
	if got := rankModels(nil); len(got) != 0 {
		t.Errorf("rankModels(nil) = %v", got)
	}
}

// ---------------------------------------------------------------------------
// What makes a key ready
// ---------------------------------------------------------------------------

var fakeModels = []string{fakeModelLegacy, fakeModelEmbed, fakeModelKimi, fakeModelFlash}

// A good key ends with a model that ANSWERED: the proof of the key, the model
// name and the wording at once.
func TestSetUpProviderFindsAModelThatAnswers(t *testing.T) {
	p := (&fakeProvider{models: fakeModels, openModels: true, goodKey: nvidiaKey}).start(t)
	knownAs(t, p.url(), nvidiaLike)

	got := setUpProvider(context.Background(), http.DefaultClient, p.url(), nvidiaKey, t.Logf)
	if got.Check.Outcome != verifyAccepted || !got.Tested || got.DefaultModel != fakeModelFlash {
		t.Fatalf("outcome=%v tested=%v default=%q (%s); want accepted, tested, %s",
			got.Check.Outcome, got.Tested, got.DefaultModel, got.Check.Detail, fakeModelFlash)
	}
	if strings.Join(got.Models, ",") != strings.Join([]string{fakeModelLegacy, fakeModelFlash, fakeModelKimi}, ",") {
		t.Errorf("models = %v, want the chat models and not the embedder", got.Models)
	}
	if !strings.Contains(got.Check.Detail, "NVIDIA") || !strings.Contains(got.Check.Detail, fakeModelFlash) {
		t.Errorf("the detail does not say who answered, or for which model: %s", got.Check.Detail)
	}
	bodies := p.bodies()
	if len(bodies) != 1 {
		t.Fatalf("%d test requests sent, want 1", len(bodies))
	}
	if tools, _ := bodies[0]["tools"].([]any); len(tools) != 1 {
		t.Error("the test request offered no tool, so it proved nothing about agent mode")
	}
}

// NVIDIA SERVES ITS MODEL LIST TO ANY KEY and has no endpoint describing one,
// so a wrong key used to be stored as "unverified" and fail at the first
// prompt. The test request is what finds out -- and a typo is refused HERE.
//
// Neuter check: treat ClassAuth like any other failure in setUpProvider.
func TestSetUpProviderRefusesAKeyNoModelWillAccept(t *testing.T) {
	p := (&fakeProvider{models: fakeModels, openModels: true, goodKey: nvidiaKey, badKeyStatus: http.StatusForbidden}).start(t)
	knownAs(t, p.url(), nvidiaLike)

	got := setUpProvider(context.Background(), http.DefaultClient, p.url(), "nvapi-a-typo-0123456789", t.Logf)
	if got.Check.Outcome != verifyRejected {
		t.Fatalf("outcome = %v (%s), want rejected", got.Check.Outcome, got.Check.Detail)
	}
	if got.DefaultModel != "" || got.Models != nil {
		t.Errorf("a refused key still chose a model: %q", got.DefaultModel)
	}
}

// A provider whose model list needs the key refuses a wrong one before any
// test request is made.
func TestSetUpProviderRefusesAKeyTheModelListRefuses(t *testing.T) {
	p := (&fakeProvider{models: fakeModels, goodKey: nvidiaKey}).start(t)
	knownAs(t, p.url(), nvidiaLike)

	got := setUpProvider(context.Background(), http.DefaultClient, p.url(), "nvapi-a-typo-0123456789", t.Logf)
	if got.Check.Outcome != verifyRejected {
		t.Fatalf("outcome = %v (%s), want rejected", got.Check.Outcome, got.Check.Detail)
	}
	if n := len(p.bodies()); n != 0 {
		t.Errorf("%d test request(s) sent with a key the provider had already refused", n)
	}
}

// The first candidate being unavailable is not the end: the next is tried, and
// ONE model refusing with 403 -- a model this account may not use -- is not
// read as a bad key.
func TestSetUpProviderMovesOnFromAModelThatWillNotAnswer(t *testing.T) {
	p := (&fakeProvider{models: fakeModels, openModels: true, goodKey: nvidiaKey, refuse: func(req map[string]any) (int, string) {
		if req["model"] == fakeModelFlash {
			return http.StatusForbidden, `{"detail":"Account does not have access to this model"}`
		}
		return 0, ""
	}}).start(t)
	knownAs(t, p.url(), nvidiaLike)

	got := setUpProvider(context.Background(), http.DefaultClient, p.url(), nvidiaKey, t.Logf)
	if got.Check.Outcome != verifyAccepted || got.DefaultModel != fakeModelKimi || !got.Tested {
		t.Errorf("outcome=%v default=%q tested=%v (%s); want accepted on %s", got.Check.Outcome, got.DefaultModel, got.Tested, got.Check.Detail, fakeModelKimi)
	}
}

// A model that answers only when no tool is offered can chat and cannot run
// agent mode. Both halves are reported.
func TestSetUpProviderSaysWhenOnlyToolFreeRequestsWork(t *testing.T) {
	p := (&fakeProvider{models: fakeModels, openModels: true, refuse: func(req map[string]any) (int, string) {
		if _, offered := req["tools"]; offered {
			return 400, `{"error":"this model does not support tool calling"}`
		}
		return 0, ""
	}}).start(t)
	knownAs(t, p.url(), nvidiaLike)

	got := setUpProvider(context.Background(), http.DefaultClient, p.url(), nvidiaKey, t.Logf)
	if got.Check.Outcome != verifyAccepted || !got.ToolsRefused || got.DefaultModel != fakeModelFlash {
		t.Fatalf("outcome=%v toolsRefused=%v default=%q (%s)", got.Check.Outcome, got.ToolsRefused, got.DefaultModel, got.Check.Detail)
	}
	notes := strings.Join(connectNotes(p.url(), got, false), "\n")
	if !strings.Contains(notes, "Agent mode") {
		t.Errorf("nothing says agent mode will not run on this model:\n%s", notes)
	}
}

// A rate limit is not proof a prompt will run, and is not reported as proof.
func TestSetUpProviderDoesNotCallARateLimitProof(t *testing.T) {
	p := (&fakeProvider{models: fakeModels, openModels: true, refuse: func(map[string]any) (int, string) {
		return http.StatusTooManyRequests, `{"error":"rate limit exceeded"}`
	}}).start(t)
	knownAs(t, p.url(), nvidiaLike)

	got := setUpProvider(context.Background(), http.DefaultClient, p.url(), nvidiaKey, t.Logf)
	if got.Check.Outcome != verifyInconclusive || got.Tested {
		t.Errorf("outcome=%v tested=%v (%s); want unproven and untested", got.Check.Outcome, got.Tested, got.Check.Detail)
	}
	if got.DefaultModel == "" || len(got.Models) == 0 {
		t.Error("the provider's models were dropped; /model would have nothing to offer")
	}
}

// OpenRouter and an address not known by name are checked exactly as before:
// no model list, no test request, models.json's tiers kept.
func TestSetUpProviderLeavesOpenRouterAndCustomAddressesAsTheyWere(t *testing.T) {
	t.Setenv("MOCHIII_USE_PROXY", "")
	for name, as := range map[string]*protocol.Provider{"OpenRouter": &openRouterLike, "a custom address": nil} {
		srv := keyServer(t, http.StatusOK, http.StatusOK, `{"data":{"label":"laptop"}}`)
		if as != nil {
			knownAs(t, srv.URL, *as)
		}
		got := setUpProvider(context.Background(), http.DefaultClient, srv.URL, testProviderKey, t.Logf)
		if got.Check.Outcome != verifyAccepted || got.Models != nil || got.DefaultModel != "" || got.Tested {
			t.Errorf("%s: outcome=%v models=%v default=%q; want the key verified and nothing else decided",
				name, got.Check.Outcome, got.Models, got.DefaultModel)
		}
	}
}

// ---------------------------------------------------------------------------
// /connect, end to end
// ---------------------------------------------------------------------------

// connectedTo is a daemon in the state the bug was reported in: running on
// OpenRouter with models.json's tiers.
func onOpenRouter(t *testing.T) (*Server, string) {
	t.Helper()
	s, path := connectServer(t)
	s.apiBase = openRouterBase
	s.cfg = &Config{
		DefaultTier: "deepseek_v4_pro",
		Tiers: map[string]ModelTier{
			"deepseek_v4_pro": {Slug: "deepseek/deepseek-v4-pro", Active: true, ContextWindow: 1024000},
			"primary":         {Slug: "deepseek/deepseek-v4-flash", Active: true},
		},
	}
	return s, path
}

// THE WHOLE REQUIREMENT: /connect, paste a key from another provider, and the
// daemon is ready -- on that provider's address, on a model it serves that has
// just answered, with nothing else typed and no restart.
//
// Neuter check: pass nil for the tiers in connectResult's setProvider call.
func TestConnectWithAnotherProvidersKeyIsReadyToUse(t *testing.T) {
	p := (&fakeProvider{models: fakeModels, openModels: true, goodKey: nvidiaKey, badKeyStatus: http.StatusForbidden}).start(t)
	knownAs(t, p.url(), nvidiaLike)
	fake := nvidiaLike
	fake.APIBase = p.url()
	keyPrefixNames(t, "nvapi-", fake)
	s, path := onOpenRouter(t)

	// Exactly what the client sends for a bare /connect: the key and nothing else.
	resp := s.connectResult(context.Background(), protocol.ConnectRequest{Connect: true, APIKey: nvidiaKey})

	if resp.Outcome != protocol.ConnectAccepted || !resp.InUse || resp.APIBase != p.url() {
		t.Fatalf("outcome=%q in_use=%v base=%q (%s); want accepted, in use, on the key's own provider",
			resp.Outcome, resp.InUse, resp.APIBase, resp.Detail)
	}
	if resp.Model != fakeModelFlash || !resp.ModelTested || resp.ModelCount != 3 {
		t.Errorf("model=%q tested=%v count=%d; want %s, tested, 3", resp.Model, resp.ModelTested, resp.ModelCount, fakeModelFlash)
	}
	if strings.Contains(fmt.Sprintf("%+v", resp), nvidiaKey) {
		t.Fatal("the response carries the key")
	}

	// The next prompt goes to the new provider, with the new key, to a model
	// that provider serves.
	key, base := s.credentials()
	if key != nvidiaKey || base != p.url() {
		t.Errorf("the daemon would send %s to %q", maskKey(key), base)
	}
	decision := s.route("", "")
	if decision.Slug != fakeModelFlash {
		t.Errorf("the next prompt would ask for %q, which this provider does not serve", decision.Slug)
	}
	// And /model offers that provider's models, selectable by their own names.
	var offered []string
	for _, tier := range availableTiers(s.tierConfig()) {
		offered = append(offered, tier.Slug)
	}
	if strings.Join(offered, ",") != strings.Join([]string{fakeModelLegacy, fakeModelFlash, fakeModelKimi}, ",") {
		t.Errorf("/model would offer %v", offered)
	}
	if picked := s.route("", fakeModelKimi); picked.Slug != fakeModelKimi {
		t.Errorf("/model %s routed to %q", fakeModelKimi, picked.Slug)
	}
	// models.json's own tiers are untouched underneath, for the way back.
	if s.cfg.DefaultTier != "deepseek_v4_pro" || len(s.cfg.Tiers) != 2 {
		t.Errorf("the configured tiers were modified: default %q, %d tiers", s.cfg.DefaultTier, len(s.cfg.Tiers))
	}

	// It is stored whole, so a restart comes back to the same place.
	stored, _, err := loadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.APIBase != p.url() || stored.APIKey != nvidiaKey || !stored.Verified ||
		stored.DefaultModel != fakeModelFlash || len(stored.Models) != 3 {
		t.Errorf("stored: base=%q verified=%v default=%q models=%d", stored.APIBase, stored.Verified, stored.DefaultModel, len(stored.Models))
	}

	// The one thing that got weaker is said, here and on every turn after.
	if notes := strings.Join(resp.Notes, "\n"); !strings.Contains(notes, "Zero-data-retention") {
		t.Errorf("nothing says zero-data-retention routing no longer applies:\n%s", notes)
	}
	degraded := s.routingDegradations()
	if len(degraded) != 1 || !strings.Contains(degraded[0].Detail, "NVIDIA") {
		t.Errorf("routingDegradations = %v, want one entry naming the provider", degraded)
	}
}

// A WRONG KEY FOR ANOTHER PROVIDER CHANGES NOTHING: not the key, not the
// address, not the tiers, not the file.
func TestConnectWithAnotherProvidersBadKeyChangesNothing(t *testing.T) {
	p := (&fakeProvider{models: fakeModels, openModels: true, goodKey: nvidiaKey, badKeyStatus: http.StatusForbidden}).start(t)
	knownAs(t, p.url(), nvidiaLike)
	fake := nvidiaLike
	fake.APIBase = p.url()
	keyPrefixNames(t, "nvapi-", fake)
	s, path := onOpenRouter(t)

	resp := s.connectResult(context.Background(), protocol.ConnectRequest{Connect: true, APIKey: "nvapi-a-typo-0123456789"})

	if resp.Outcome != protocol.ConnectRejected || resp.Ok || resp.APIBase != p.url() {
		t.Fatalf("outcome=%q ok=%v base=%q; want rejected, naming the provider that refused it", resp.Outcome, resp.Ok, resp.APIBase)
	}
	if key, base := s.credentials(); key != "sk-the-key-it-started-with" || base != openRouterBase {
		t.Errorf("a refused key moved the daemon to %s at %q", maskKey(key), base)
	}
	if s.route("", "").Slug != "deepseek/deepseek-v4-pro" {
		t.Error("a refused key replaced the tiers")
	}
	if stored, _, _ := loadCredential(path); stored.configured() {
		t.Error("a refused key was stored")
	}
}

// A BARE sk- KEY IS SENT NOWHERE. Several providers issue them, so the daemon
// asks -- it does not try each one with a credential that works at one of them.
//
// Neuter check: return matches[0].APIBase for the shared prefix in
// resolveConnectBase.
func TestConnectAsksWhichProviderABareSkKeyIsFor(t *testing.T) {
	contacted := 0
	watch := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { contacted++ }))
	defer watch.Close()
	s, path := onOpenRouter(t)
	// Every candidate's address is the watcher: if the key leaves, it lands here.
	prev := providersForKey
	providersForKey = func(key string) ([]protocol.Provider, bool) {
		got, certain := prev(key)
		for i := range got {
			got[i].APIBase = watch.URL
		}
		return got, certain
	}
	t.Cleanup(func() { providersForKey = prev })

	resp := s.connectResult(context.Background(), protocol.ConnectRequest{Connect: true, APIKey: "sk-0123456789abcdef0123456789abcdef"})

	if resp.Outcome != protocol.ConnectNeedsProvider || resp.Ok || resp.InUse {
		t.Fatalf("outcome=%q ok=%v in_use=%v, want needs_provider and nothing adopted", resp.Outcome, resp.Ok, resp.InUse)
	}
	if len(resp.Candidates) < 2 {
		t.Errorf("candidates = %v, want the providers to choose between", resp.Candidates)
	}
	for _, id := range resp.Candidates {
		if _, ok := protocol.ProviderByName(id); !ok {
			t.Errorf("candidate %q is not a name /connect accepts", id)
		}
	}
	if contacted != 0 {
		t.Errorf("the key was sent to %d address(es) before anyone said whose it was", contacted)
	}
	if stored, _, _ := loadCredential(path); stored.configured() {
		t.Error("a key nobody had claimed was stored")
	}
	if key, base := s.credentials(); key != "sk-the-key-it-started-with" || base != openRouterBase {
		t.Errorf("the daemon moved to %s at %q", maskKey(key), base)
	}
}

// AND BACK AGAIN. Connecting an OpenRouter key puts models.json's tiers back in
// force -- the provider's list is not left behind to name models OpenRouter
// calls something else.
func TestConnectingBackToOpenRouterRestoresTheConfiguredTiers(t *testing.T) {
	t.Setenv("MOCHIII_USE_PROXY", "")
	s, _ := onOpenRouter(t)
	s.apiBase = "https://integrate.api.nvidia.com/v1"
	s.tierCfg = providerTierConfig(s.cfg, "NVIDIA", fakeModels, nil, fakeModelFlash)
	if s.route("", "").Slug != fakeModelFlash {
		t.Fatal("setup: the provider's tiers are not in force")
	}

	or := keyServer(t, http.StatusOK, http.StatusOK, `{"data":{"label":"laptop"}}`)
	knownAs(t, or.URL, openRouterLike)
	fake := openRouterLike
	fake.APIBase = or.URL
	keyPrefixNames(t, "sk-or-", fake)

	resp := s.connectResult(context.Background(), protocol.ConnectRequest{Connect: true, APIKey: "sk-or-v1-0123456789abcdef"})

	if resp.Outcome != protocol.ConnectAccepted || resp.APIBase != or.URL || resp.Model != "" {
		t.Fatalf("outcome=%q base=%q model=%q (%s)", resp.Outcome, resp.APIBase, resp.Model, resp.Detail)
	}
	if got := s.route("", "").Slug; got != "deepseek/deepseek-v4-pro" {
		t.Errorf("after returning to OpenRouter the next prompt would ask for %q", got)
	}
	if len(s.routingDegradations()) != 0 {
		t.Errorf("routing is reported as weakened on OpenRouter: %v", s.routingDegradations())
	}
}

// ---------------------------------------------------------------------------
// After a restart
// ---------------------------------------------------------------------------

// The stored credential decides the provider at startup when the environment
// names a provider and gives it no key -- the state a .env that still says
// MOCHIII_API_BASE=<the old provider> leaves behind. Without this the key just
// connected was withheld at every start and the client asked for it again.
//
// Neuter check: drop the providerForBase branch from fillFromStored.
func TestAConnectedProviderSurvivesARestartOverAKeylessEnvironmentBase(t *testing.T) {
	nvidia, _ := protocol.ProviderByName("nvidia")
	connected := storedCredential{APIBase: nvidia.APIBase, APIKey: nvidiaKey, Verified: true,
		DefaultModel: fakeModelFlash, Models: fakeModels}

	var logged []string
	logf := func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	key, base := fillFromStored("", openRouterBase, connected, logf)
	if key != nvidiaKey || base != nvidia.APIBase {
		t.Fatalf("at the next start the daemon would send %s to %q; want the connected key to %q", maskKey(key), base, nvidia.APIBase)
	}
	all := strings.Join(logged, "\n")
	if !strings.Contains(all, "MOCHIII_API_BASE") || strings.Contains(all, nvidiaKey) {
		t.Errorf("the log does not say the environment's base was set aside, or it carries the key:\n%s", all)
	}

	// THE KEY STILL GOES ONLY WHERE IT WAS SAVED. None of these may receive it.
	for name, envBase := range map[string]string{
		"a local server":           "http://localhost:11434/v1",
		"a custom gateway":         "https://llm.example.com/v1",
		"a loopback address by IP": "http://127.0.0.1:8080/v1",
	} {
		if key, base := fillFromStored("", envBase, connected, discardf); key != "" || base != envBase {
			t.Errorf("%s: got (%s, %q); want no key and the environment's base kept", name, maskKey(key), base)
		}
	}
	// And a key in the environment still wins outright.
	if key, base := fillFromStored("env-key", openRouterBase, connected, discardf); key != "env-key" || base != openRouterBase {
		t.Errorf("a key in the environment did not win: (%s, %q)", maskKey(key), base)
	}
}

// What startup builds from the stored list: one tier per model, named by the
// model's own id, with the configured tiers left exactly as they were.
func TestProviderTierConfig(t *testing.T) {
	cfg := &Config{
		DefaultTier: "primary",
		Tiers:       map[string]ModelTier{"primary": {Slug: "deepseek/deepseek-v4-flash", Active: true}},
		MCP:         MCPConfig{Enabled: true},
	}
	got := providerTierConfig(cfg, "NVIDIA", []string{fakeModelKimi, fakeModelFlash}, []string{fakeModelKimi}, fakeModelFlash)
	if got == nil || got.DefaultTier != fakeModelFlash || got.ResolvedSlug() != fakeModelFlash || len(got.Tiers) != 2 {
		t.Fatalf("got %+v", got)
	}
	if tier := got.Tiers[fakeModelKimi]; tier.Slug != fakeModelKimi || !tier.Active || !strings.Contains(tier.Note, "NVIDIA") {
		t.Errorf("tier = %+v", tier)
	}
	// A model that was listed and sent nothing back says so in /model; one that
	// answered does not carry the warning.
	if note := got.Tiers[fakeModelKimi].Note; !strings.Contains(note, "did not answer") {
		t.Errorf("a quiet model is offered with no warning: %q", note)
	}
	if note := got.Tiers[fakeModelFlash].Note; strings.Contains(note, "did not answer") {
		t.Errorf("a model that answered is marked as quiet: %q", note)
	}
	if !got.MCP.Enabled {
		t.Error("agent mode was lost: only the tiers may change")
	}
	if cfg.DefaultTier != "primary" || len(cfg.Tiers) != 1 {
		t.Errorf("the configured tiers were modified: %+v", cfg.Tiers)
	}
	// A default the list no longer mentions is still usable, rather than
	// leaving the daemon with a default tier that does not exist.
	if kept := providerTierConfig(cfg, "NVIDIA", []string{fakeModelKimi}, nil, fakeModelFlash); kept.Tiers[fakeModelFlash].Slug != fakeModelFlash {
		t.Error("a default missing from the list was dropped")
	}
	for name, got := range map[string]*Config{
		"no models":  providerTierConfig(cfg, "NVIDIA", nil, nil, fakeModelFlash),
		"no default": providerTierConfig(cfg, "NVIDIA", fakeModels, nil, ""),
		"no config":  providerTierConfig(nil, "NVIDIA", fakeModels, nil, fakeModelFlash),
	} {
		if got != nil {
			t.Errorf("%s: got a tier config, want none (the configured tiers stay in force)", name)
		}
	}
}

// ---------------------------------------------------------------------------
// The command line
// ---------------------------------------------------------------------------

func TestConnectCommandAsksWhichProviderABareSkKeyIsFor(t *testing.T) {
	path, _, logger := connectHarness(t)
	feedStdin(t, "sk-0123456789abcdef0123456789abcdef\n")

	err := runConnectCommand([]string{"--stdin", "--no-verify"}, logger)
	if err == nil || !strings.Contains(err.Error(), "--provider") || !strings.Contains(err.Error(), "sent nowhere") {
		t.Fatalf("err = %v; want a refusal that says nothing was sent and how to name the provider", err)
	}
	if strings.Contains(err.Error(), "0123456789abcdef") {
		t.Error("the refusal repeats the key")
	}
	if stored, _, _ := loadCredential(path); stored.configured() {
		t.Error("a key nobody had claimed was saved")
	}
}

func TestConnectCommandTakesAProviderByName(t *testing.T) {
	path, out, logger := connectHarness(t)
	feedStdin(t, "sk-0123456789abcdef0123456789abcdef\n")
	if err := runConnectCommand([]string{"--stdin", "--no-verify", "--provider", "DeepSeek"}, logger); err != nil {
		t.Fatalf("connect --provider deepseek: %v", err)
	}
	stored, _, _ := loadCredential(path)
	if want, _ := protocol.ProviderByName("deepseek"); stored.APIBase != want.APIBase {
		t.Errorf("saved for %q, want %q", stored.APIBase, want.APIBase)
	}
	if !strings.Contains(out.String(), "No model was chosen") {
		t.Errorf("--no-verify for a provider with its own models did not say none was chosen:\n%s", out)
	}

	for _, args := range [][]string{
		{"--stdin", "--provider", "nosuchprovider"},
		{"--stdin", "--provider", "nvidia", "--api-base", "https://llm.example.com/v1"},
	} {
		feedStdin(t, nvidiaKey+"\n")
		if err := runConnectCommand(args, logger); err == nil {
			t.Errorf("connect %v succeeded", args)
		}
	}
}

// NOT READY IS SAID. A named provider that could not be asked for its models
// leaves models.json's tiers in force, and they name another provider's models
// -- so the result says the connect did not finish, instead of leaving the next
// prompt to fail on a model name.
func TestConnectNotesSayWhenNoModelCouldBeChosen(t *testing.T) {
	t.Setenv("MOCHIII_USE_PROXY", "")
	const nvidia = "https://integrate.api.nvidia.com/v1"
	unreachable := providerSetup{Check: verification{Outcome: verifyUnreachable, Detail: "did not answer"}}

	notes := strings.Join(connectNotes(nvidia, unreachable, true), "\n")
	for _, want := range []string{"No model could be chosen for NVIDIA", "not ready", "Zero-data-retention"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the notes do not say %q:\n%s", want, notes)
		}
	}
	// With a model chosen, and on OpenRouter, there is nothing to warn about.
	ready := providerSetup{Models: fakeModels, DefaultModel: fakeModelFlash, Tested: true}
	if notes := connectNotes(nvidia, ready, false); len(notes) != 0 {
		t.Errorf("a finished connect on the same provider still warns: %q", notes)
	}
	if notes := connectNotes(openRouterBase, providerSetup{}, true); len(notes) != 0 {
		t.Errorf("connecting to OpenRouter warns about something: %q", notes)
	}
	// A custom address that gave no list keeps the tiers, and says so.
	if notes := strings.Join(connectNotes("https://llm.example.com/v1", providerSetup{}, true), "\n"); !strings.Contains(notes, "Model names are each provider's own") {
		t.Errorf("a switch to a custom address says nothing about model names:\n%s", notes)
	}
}

// `connect --show` says which model the stored key will use, for a provider
// whose models were listed when it was saved.
func TestShowCredentialNamesTheModel(t *testing.T) {
	path, out, _ := connectHarness(t)
	if err := saveCredential(path, storedCredential{
		APIBase: "https://integrate.api.nvidia.com/v1", APIKey: nvidiaKey, Verified: true,
		DefaultModel: fakeModelFlash, Models: fakeModels,
	}); err != nil {
		t.Fatal(err)
	}
	if err := showCredential(path); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, fakeModelFlash) || strings.Contains(got, nvidiaKey) {
		t.Errorf("--show does not name the model, or prints the key:\n%s", got)
	}
}

// A RESTRICTED KEY IS NOT A WRONG KEY. A provider whose model list needs the
// key has already accepted it by serving that list; when the models tried then
// all answer 403, the account may not use those models. The key is stored and
// said to be unproven -- refusing it would lock out someone whose key works.
// (Where the list is served to anyone, the same five refusals are all there is
// to go on, and the key is refused: TestSetUpProviderRefusesAKeyNoModelWillAccept.)
//
// Neuter check: return false from modelListNeedsKey.
func TestSetUpProviderDoesNotRefuseAKeyTheProviderAlreadyAccepted(t *testing.T) {
	p := (&fakeProvider{models: fakeModels, goodKey: nvidiaKey, refuse: func(map[string]any) (int, string) {
		return http.StatusForbidden, `{"error":{"message":"Project does not have access to this model"}}`
	}}).start(t)
	knownAs(t, p.url(), nvidiaLike)

	got := setUpProvider(context.Background(), http.DefaultClient, p.url(), nvidiaKey, t.Logf)
	if got.Check.Outcome != verifyInconclusive {
		t.Fatalf("outcome = %v (%s); want the key kept, unproven", got.Check.Outcome, got.Check.Detail)
	}
	if !strings.Contains(got.Check.Detail, "accepts the key") || got.Tested {
		t.Errorf("detail = %q tested=%v; want it to say the key is accepted and no model answered", got.Check.Detail, got.Tested)
	}
	if len(got.Models) == 0 || got.DefaultModel == "" {
		t.Error("the provider's models were dropped; /model would have nothing to offer")
	}
}

// A PROVIDER KEY IS NEVER SENT TO THE PROXY. In proxy mode MOCHIII_API_BASE is
// the proxy's address; with nothing stored it used to be where a key typed into
// /connect was "verified" -- a provider credential handed to a host that did not
// issue it.
//
// Neuter check: pass os.Getenv("MOCHIII_API_BASE") to resolveConnectBase as it was.
func TestConnectInProxyModeNeverSendsAProviderKeyToTheProxy(t *testing.T) {
	sentToProxy := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sentToProxy++
		}
	}))
	defer proxy.Close()
	nvidia := (&fakeProvider{models: fakeModels, openModels: true}).start(t)
	fake := nvidiaLike
	fake.APIBase = nvidia.url()
	keyPrefixNames(t, "nvapi-", fake)

	s, path := connectServer(t)
	t.Setenv("MOCHIII_USE_PROXY", "true")
	t.Setenv("MOCHIII_API_BASE", proxy.URL)
	s.apiBase, s.apiKey = proxy.URL, "mochi_the-proxy-key"

	resp := s.connectResult(context.Background(), protocol.ConnectRequest{Connect: true, APIKey: nvidiaKey})

	if sentToProxy != 0 {
		t.Fatalf("the provider key was sent to the proxy %d time(s)", sentToProxy)
	}
	if resp.APIBase != nvidia.url() || resp.InUse || !resp.EnvOverride {
		t.Errorf("base=%q in_use=%v env_override=%v; want the key stored for its own provider and NOT adopted", resp.APIBase, resp.InUse, resp.EnvOverride)
	}
	if key, base := s.credentials(); key != "mochi_the-proxy-key" || base != proxy.URL {
		t.Errorf("proxy mode's own credential changed: %s at %q", maskKey(key), base)
	}
	if stored, _, _ := loadCredential(path); stored.APIBase != nvidia.url() {
		t.Errorf("stored for %q, want the provider the key belongs to", stored.APIBase)
	}
}

// LISTED IS NOT THE SAME AS SERVED -- what NVIDIA actually did with a real key
// on 2026-10-05. Its list offered 59 chat models; the best-known ones accepted
// the request and never sent a byte, many more answered 404 for the account,
// and a handful further down answered in seconds. Trying five in a row and
// stopping reported a good key as "nothing answered" and left the user on a
// default that hung every prompt.
//
// The list below keeps that shape: the first EIGHT candidates in rank order
// are silent or missing, and the first model that answers is the ninth.
//
// Neuter check: set maxProbeCandidates back to 5 (the answering model is never
// reached), or probeConcurrency to 1 (the silent models are waited out one at a
// time and the deadline below is missed).
func TestSetUpProviderFindsTheModelThatAnswersOnAListThatMostlyDoesNot(t *testing.T) {
	silent := []string{"deepseek-ai/deepseek-v4.1-flash", "moonshotai/kimi-k3", "z-ai/glm-5.3", "z-ai/glm-5.3-flash", "poolside/laguna-xs-2.1"}
	missing := []string{"moonshotai/kimi-k2.6", "mistralai/mistral-large-2-instruct", "mistralai/mistral-large", "nvidia/llama-3.1-nemotron-70b-instruct"}
	answering := []string{"nvidia/nemotron-3-ultra-550b-a55b", "nvidia/nemotron-3-super-120b-a12b", "openai/gpt-oss-20b"}

	p := &fakeProvider{openModels: true, goodKey: nvidiaKey, badKeyStatus: http.StatusForbidden, silent: map[string]bool{}}
	for _, id := range silent {
		p.silent[id] = true
	}
	notThere := map[string]bool{}
	for _, id := range missing {
		notThere[id] = true
	}
	p.refuse = func(req map[string]any) (int, string) {
		if model, _ := req["model"].(string); notThere[model] {
			return http.StatusNotFound, `{"status":404,"title":"Not Found","detail":"Function 'x': Not found for account 'y'"}`
		}
		return 0, ""
	}
	p.models = append(append(append([]string{}, silent...), missing...), answering...)
	p.start(t)
	knownAs(t, p.url(), nvidiaLike)

	// The setup must be right about its own premise: nothing that answers is
	// among the first five candidates.
	ranked := rankModels(chatModelIDs(listed(p.models)))
	for i, id := range ranked[:8] {
		if !p.silent[id] && !notThere[id] {
			t.Fatalf("candidate %d (%s) answers; the test no longer reproduces a list whose best entries are dead: %v", i+1, id, ranked)
		}
	}

	origTimeout, origHead := probeTimeout, probeHeadStart
	probeTimeout, probeHeadStart = 400*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { probeTimeout, probeHeadStart = origTimeout, origHead })

	start := time.Now()
	got := setUpProvider(context.Background(), http.DefaultClient, p.url(), nvidiaKey, t.Logf)
	elapsed := time.Since(start)

	if got.Check.Outcome != verifyAccepted || !got.Tested {
		t.Fatalf("outcome=%v tested=%v (%s); a good key was not recognised as one", got.Check.Outcome, got.Tested, got.Check.Detail)
	}
	if got.DefaultModel != "nvidia/nemotron-3-ultra-550b-a55b" {
		t.Errorf("default = %q, want the best-ranked model that actually answered", got.DefaultModel)
	}
	// One at a time, five silent models cost five full timeouts before anything
	// that answers is reached. Together they cost about one.
	if limit := 3 * probeTimeout; elapsed > limit {
		t.Errorf("took %s; the silent models were waited out one after another (limit %s)", elapsed.Round(time.Millisecond), limit)
	}
	// /model offers what can be used: the missing ones are gone, the silent ones
	// are still there and marked.
	for _, id := range missing {
		if slicesContains(got.Models, id) {
			t.Errorf("%s answered 404 for this account and is still offered", id)
		}
	}
	for _, id := range silent {
		if !slicesContains(got.Models, id) || !slicesContains(got.Quiet, id) {
			t.Errorf("%s sent nothing back; want it kept and marked quiet (models %v, quiet %v)", id, slicesContains(got.Models, id), slicesContains(got.Quiet, id))
		}
	}
	if got.Unavailable != len(missing) || got.Listed != len(p.models) {
		t.Errorf("unavailable=%d listed=%d, want %d and %d", got.Unavailable, got.Listed, len(missing), len(p.models))
	}
	notes := strings.Join(connectNotes(p.url(), got, true), "\n")
	if !strings.Contains(notes, "not all of them are served") {
		t.Errorf("nothing tells the user the provider's list overstates what it serves:\n%s", notes)
	}
}

// AN HONEST LIST COSTS ONE REQUEST. The head start exists so that a provider
// whose best model answers is asked exactly once, however many it lists.
func TestSetUpProviderSendsOneRequestWhenTheBestModelAnswers(t *testing.T) {
	var many []string
	for i := 0; i < 30; i++ {
		many = append(many, fmt.Sprintf("vendor/zeta-%d", i))
	}
	p := (&fakeProvider{models: many, goodKey: nvidiaKey}).start(t)
	knownAs(t, p.url(), nvidiaLike)

	got := setUpProvider(context.Background(), http.DefaultClient, p.url(), nvidiaKey, t.Logf)
	if !got.Tested || got.Tried != 1 || len(p.bodies()) != 1 {
		t.Errorf("tested=%v tried=%d requests=%d; want one request and nothing more", got.Tested, got.Tried, len(p.bodies()))
	}
	if got.Unavailable != 0 || len(got.Quiet) != 0 || len(connectNotes(p.url(), got, false)) != 0 {
		t.Errorf("an honest list was described as overstated: %+v", got)
	}
}

// The models that answered on NVIDIA, in the order they are preferred: the
// largest of the newest generation, and the small fast ones last.
func TestRankModelsPrefersTheLargerOfTwoSiblings(t *testing.T) {
	got := rankModels([]string{"openai/gpt-oss-20b", "nvidia/nemotron-3.5-lightning-30b-a3b",
		"nvidia/nemotron-3-super-120b-a12b", "nvidia/nemotron-3-ultra-550b-a55b"})
	want := []string{"nvidia/nemotron-3-ultra-550b-a55b", "nvidia/nemotron-3-super-120b-a12b"}
	if got[0] != want[0] || got[1] != want[1] {
		t.Errorf("order = %v, want %v first", got, want)
	}
}

// The status survives classification, because "this model is not there" (404)
// has no class of its own and must not be mistaken for a model that hung.
func TestAModelErrorKeepsTheStatusItWasBuiltFrom(t *testing.T) {
	notFound := candidateOutcome{tried: true, err: classifyHTTPError(http.StatusNotFound, "404 Not Found", `{"detail":"Not found for account"}`)}
	hung := candidateOutcome{tried: true, err: classifyTransportError(context.DeadlineExceeded)}
	refused := candidateOutcome{tried: true, err: classifyHTTPError(http.StatusForbidden, "403 Forbidden", `{"detail":"Authorization failed"}`)}
	if !notFound.unavailable() || notFound.quiet() {
		t.Error("a 404 was not read as a model that is not there")
	}
	if !hung.quiet() || hung.unavailable() {
		t.Error("a timeout was not read as a model that sent nothing")
	}
	if refused.unavailable() || refused.quiet() || refused.answered() {
		t.Error("a refused key was read as something else")
	}
	if (candidateOutcome{}).quiet() || (candidateOutcome{}).unavailable() || (candidateOutcome{}).answered() {
		t.Error("a model that was never tried was given an outcome")
	}
}

func listed(ids []string) []listedModel {
	out := make([]listedModel, 0, len(ids))
	for _, id := range ids {
		out = append(out, listedModel{ID: id})
	}
	return out
}

func slicesContains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// What NVIDIA's answering models were on 2026-10-05, in the order they are
// preferred: the large one first, and "xs" read as the small variant it is.
func TestRankModelsOnTheModelsThatAnsweredOnNVIDIA(t *testing.T) {
	got := rankModels([]string{"poolside/laguna-xs-2.1", "openai/gpt-oss-20b", "nvidia/nemotron-3.5-lightning-30b-a3b",
		"nvidia/nemotron-3-super-120b-a12b", "nvidia/nemotron-3-ultra-550b-a55b"})
	if got[0] != "nvidia/nemotron-3-ultra-550b-a55b" {
		t.Errorf("first = %q, want the largest undemoted model (order: %v)", got[0], got)
	}
}

// --- QA 2026-10-06: the address a key is sent to, and the list a provider sends back ---

// A provider this product knows by name is never contacted over plain http,
// whatever was typed; an unknown address is left as the user wrote it.
func TestANamedProviderIsNeverReachedOverPlainHTTP(t *testing.T) {
	const anyKey = "sk-abcdefghijklmnopqrstuvwxyz012345"
	for typed, want := range map[string]string{
		"http://integrate.api.nvidia.com/v1":  "https://integrate.api.nvidia.com/v1",
		"http://api.openai.com/v1":            "https://api.openai.com/v1",
		"http://API.GROQ.com/openai/v1":       "https://API.GROQ.com/openai/v1",
		"https://api.openai.com/v1":           "https://api.openai.com/v1",
		"http://192.168.1.20:8000/v1":         "http://192.168.1.20:8000/v1", // a server on the user's own network
		"http://127.0.0.1:11434/v1":           "http://127.0.0.1:11434/v1",
		"http://gateway.internal.example/v1":  "http://gateway.internal.example/v1",
		"https://gateway.internal.example/v1": "https://gateway.internal.example/v1",
	} {
		if got := resolveConnectBase(typed, anyKey, defaultAPIBase, storedCredential{}, "").Base; got != want {
			t.Errorf("/connect %s would send the key to %q, want %q", typed, got, want)
		}
	}
}

// "https://api.openai.com@evil.example/v1" names evil.example. It is refused
// before the key goes anywhere, by the daemon and not only by one client.
func TestConnectRefusesAnAddressDisguisedAsAProviders(t *testing.T) {
	for _, base := range []string{"https://api.openai.com@evil.example/v1", "https://integrate.api.nvidia.com:x@evil.example/v1"} {
		if err := validateConnectBase(base); err == nil || !strings.Contains(err.Error(), "evil.example is the host it really names") {
			t.Errorf("validateConnectBase(%s) = %v, want a refusal naming the real host", base, err)
		}
		sent := (&fakeProvider{models: fakeModels}).start(t)
		s, path := onOpenRouter(t)
		resp := s.connectResult(context.Background(), protocol.ConnectRequest{Connect: true, APIKey: "sk-or-v1-" + strings.Repeat("ab12", 16), APIBase: base})
		if resp.Ok || resp.Error == "" || resp.InUse {
			t.Errorf("connect to %s answered %+v, want a refusal", base, resp)
		}
		if _, err := os.Stat(path); err == nil {
			t.Errorf("a credential was stored for %s", base)
		}
		if len(sent.bodies()) != 0 {
			t.Error("a request was sent")
		}
	}
	for _, fine := range []string{"https://api.openai.com/v1", "http://127.0.0.1:8080/v1", "http://10.0.0.5:8000/v1"} {
		if err := validateConnectBase(fine); err != nil {
			t.Errorf("validateConnectBase(%s) = %v, want it accepted", fine, err)
		}
	}
}

// Plain http to another machine is allowed -- a model server on the user's own
// network has no certificate -- and is SAID, once, where the user will read it.
func TestConnectNotesSayWhenTheKeyTravelsUnencrypted(t *testing.T) {
	said := func(base string) bool {
		for _, n := range connectNotes(base, providerSetup{}, false) {
			if strings.Contains(n, "plain http") && strings.Contains(n, "unencrypted") {
				return true
			}
		}
		return false
	}
	if !said("http://192.168.1.20:8000/v1") || !said("http://gateway.example/v1") {
		t.Error("nothing says a plain-http address sends the key unencrypted")
	}
	if said("http://127.0.0.1:8000/v1") || said("http://localhost:11434/v1") || said("https://gateway.example/v1") {
		t.Error("the warning is shown where nothing travels unencrypted across a network")
	}
}

// A model list is text a remote server wrote. An entry with a line break, an
// escape sequence or absurd length is not a model and is not kept; a list of
// thousands is cut to the best-ranked hundred -- and the model chosen is real.
func TestAHostileModelListIsNotStoredAsItCame(t *testing.T) {
	bad := []string{
		"evil/model-\x1b]0;PWNED\x07\x1b[2J\x1b[31mRED",
		"evil/model\nconnect: the key was rejected, paste it at http://evil.example",
		"evil/" + strings.Repeat("A", 5000),
		"evil/with space", "evil/tab\there", "evil/café", "evil/\u202egnp.exe",
	}
	models := append([]string{}, bad...)
	for i := 0; i < 3000; i++ {
		models = append(models, fmt.Sprintf("bulk/llama-%d-70b", i))
	}
	models = append(models, "deepseek-ai/deepseek-v4-pro")
	p := (&fakeProvider{models: models, goodKey: nvidiaKey}).start(t)
	knownAs(t, p.url(), nvidiaLike)
	forgetDialects(t)

	var logged strings.Builder
	setup := setUpProvider(context.Background(), http.DefaultClient, p.url(), nvidiaKey, func(format string, args ...any) {
		fmt.Fprintf(&logged, format+"\n", args...)
	})
	if !setup.Tested || setup.DefaultModel != "deepseek-ai/deepseek-v4-pro" {
		t.Fatalf("default = %q tested = %v, want the best-ranked real model, tested", setup.DefaultModel, setup.Tested)
	}
	if len(setup.Models) != maxStoredModels {
		t.Errorf("%d models kept from a list of %d, want %d", len(setup.Models), len(models), maxStoredModels)
	}
	for _, id := range setup.Models {
		if !plainModelID(id) {
			t.Errorf("kept %q", id)
		}
	}
	for _, id := range bad {
		if slicesContains(setup.Models, id) {
			t.Errorf("a hostile entry was kept as a tier: %q", id)
		}
	}
	if !slicesContains(setup.Models, setup.DefaultModel) {
		t.Error("the default model is not among the tiers kept")
	}
	if strings.Contains(logged.String(), "\x1b") || strings.Contains(logged.String(), "the key was rejected, paste it") {
		t.Errorf("the provider's text reached the log:\n%q", logged.String())
	}
	found := false
	for _, n := range connectNotes(p.url(), setup, true) {
		found = found || strings.Contains(n, fmt.Sprintf("/model offers the %d best suited", maxStoredModels))
	}
	if !found {
		t.Errorf("nothing says the list was cut: %q", connectNotes(p.url(), setup, true))
	}

	// A SHORT hostile list, where no cap stands in the way: every entry would be
	// tried and kept, so only the check on the id itself keeps them out -- of
	// the tiers, of the requests sent, and of the log.
	small := (&fakeProvider{models: append(append([]string{}, bad...), "good/chat-model-70b"), goodKey: nvidiaKey}).start(t)
	knownAs(t, small.url(), nvidiaLike)
	forgetDialects(t)
	logged.Reset()
	setup = setUpProvider(context.Background(), http.DefaultClient, small.url(), nvidiaKey, func(format string, args ...any) {
		fmt.Fprintf(&logged, format+"\n", args...)
	})
	if len(setup.Models) != 1 || setup.Models[0] != "good/chat-model-70b" || setup.DefaultModel != "good/chat-model-70b" {
		t.Errorf("from a short hostile list the tiers are %q (default %q), want only the one real model", setup.Models, setup.DefaultModel)
	}
	for _, body := range small.bodies() {
		if model, _ := body["model"].(string); model != "good/chat-model-70b" {
			t.Errorf("a request was sent for the hostile entry %q", model)
		}
	}
	if strings.ContainsAny(logged.String(), "\x1b\x07") || strings.Contains(logged.String(), "paste it at") {
		t.Errorf("a hostile entry reached the log:\n%q", logged.String())
	}

	// Real ids, as the providers write them, are all kept.
	for _, id := range []string{"nvidia/nemotron-3-super-120b-a12b", "accounts/fireworks/models/llama-v3p3-70b-instruct",
		"meta-llama/Llama-3.3-70B-Instruct:fastest", "claude-sonnet-4-5@20250929", "gpt-4o-2024-08-06", "Qwen/Qwen2.5-72B-Instruct-Turbo"} {
		if !plainModelID(id) {
			t.Errorf("a real model id is refused: %q", id)
		}
	}
}

// A turn that fails on a model the provider lists and never answers says THAT,
// instead of "this is usually temporary" (measured live: 120.6 s, then that).
func TestAFailureOnAModelThatNeverAnsweredSaysSo(t *testing.T) {
	tiers := providerTierConfig(&Config{}, "NVIDIA", []string{"good/model-70b", "quiet/model-70b"}, []string{"quiet/model-70b"}, "good/model-70b")
	s := &Server{cfg: &Config{}, tierCfg: tiers}
	hint := s.quietModelHint("quiet/model-70b", ClassUpstreamUnavailable)
	for _, want := range []string{"quiet/model-70b also sent nothing back when the key was connected", "/model picks another"} {
		if !strings.Contains(hint, want) {
			t.Errorf("the hint %q is missing %q", hint, want)
		}
	}
	for name, got := range map[string]string{
		"a model that answered":             s.quietModelHint("good/model-70b", ClassUpstreamUnavailable),
		"a different kind of failure":       s.quietModelHint("quiet/model-70b", ClassAuth),
		"a model that is no tier":           s.quietModelHint("unknown/model", ClassUpstreamUnavailable),
		"the configured tiers (no overlay)": (&Server{cfg: &Config{}}).quietModelHint("quiet/model-70b", ClassUpstreamUnavailable),
	} {
		if got != "" {
			t.Errorf("%s got the hint: %q", name, got)
		}
	}

	// Through the real socket: the provider does not answer, the turn fails, and
	// the message the client is shown carries the reason.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(dead.Close)
	sockAddr, _, srv := agentSocketServer(t, dead.URL, MCPConfig{})
	// setProvider, not a field write: Serve is already reading under its lock.
	srv.setProvider("k", dead.URL, providerTierConfig(&Config{}, "NVIDIA", []string{"test-model"}, []string{"test-model"}, "test-model"))
	_, dec, conn := agentClient(t, sockAddr, "hello", nil)
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	for {
		var tok protocol.TokenResponse
		if err := dec.Decode(&tok); err != nil {
			t.Fatalf("reading the stream: %v", err)
		}
		if !tok.Done {
			continue
		}
		if tok.ErrorClass != string(ClassUpstreamUnavailable) || !strings.Contains(tok.Error, "does not answer this account") {
			t.Errorf("the failure was reported as %q (%s), want the reason it will not get better", tok.Error, tok.ErrorClass)
		}
		break
	}
}
