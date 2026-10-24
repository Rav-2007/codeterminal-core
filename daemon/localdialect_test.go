package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// forgetLocalModels starts a test with no local server identified, and leaves
// none identified after it.
func forgetLocalModels(t *testing.T) {
	t.Helper()
	clear := func() {
		localKnown.kinds.Range(func(k, _ any) bool { localKnown.kinds.Delete(k); return true })
		localKnown.windows.Range(func(k, _ any) bool { localKnown.windows.Delete(k); return true })
	}
	clear()
	t.Cleanup(clear)
}

// vllmLike refuses a request whose input plus max_tokens exceeds window tokens,
// in vLLM's words, counting the input at four characters a token as a real
// tokenizer roughly does for English. A request with no max_tokens is given
// what is left, as vLLM does.
func vllmLike(window int) func(req map[string]any) (int, string) {
	return func(req map[string]any) (int, string) {
		input := len(fmt.Sprint(req["messages"])) / 4
		limit, sent := req["max_tokens"].(float64)
		if !sent {
			// vLLM takes either name for the cap.
			limit, sent = req["max_completion_tokens"].(float64)
		}
		if sent && int(limit)+input > window {
			return 400, fmt.Sprintf(`{"object":"error","message":"'max_tokens' or 'max_completion_tokens' is too large: %d. `+
				`This model's maximum context length is %d tokens and your request has %d input tokens (%d > %d - %d).",`+
				`"type":"BadRequestError","param":null,"code":400}`, int(limit), window, input, int(limit), window, input)
		}
		return 0, ""
	}
}

// ONLY A SERVER IDENTIFIED AS ONE ON THIS MACHINE GETS THE PLAIN REQUEST, and
// never in proxy mode. A loopback address nothing identified may be a gateway
// to OpenRouter, so its request stays the routing one.
func TestOnlyAnIdentifiedLocalServerGetsThePlainRequest(t *testing.T) {
	t.Setenv("MOCHIII_USE_PROXY", "")
	forgetLocalModels(t)
	forgetDialects(t)
	const base = "http://127.0.0.1:8000/v1"
	if d := dialectFor(base, "m"); !d.routing {
		t.Fatalf("an address nothing identified got %+v, want the routing request as before", d)
	}
	rememberLocalModel(base, "m", localWindow{kind: localKindByID("vllm"), window: 8192})
	if d := dialectFor(base, "m"); d != (requestDialect{streamOptions: true, maxTokensField: fieldMaxTokens, window: 8192}) {
		t.Errorf("an identified vLLM got %+v, want the plain request with its 8192-token window", d)
	}
	if d := dialectFor(base, "another-model"); d.routing || d.window != 0 {
		t.Errorf("a model whose window was never read got %+v, want the plain request with no window", d)
	}
	t.Setenv("MOCHIII_USE_PROXY", "true")
	if d := dialectFor(base, "m"); !d.routing {
		t.Error("proxy mode lost the routing request because a local server was once identified at that address")
	}
}

// A VLLM MODEL IS NO LONGER REFUSED FOR ITS OUTPUT CAP. Before 1.4.0 every
// request asked for 32,768 output tokens, and vLLM refused each one: input plus
// output exceeded the model's 8,192. Identified, the first request asks for
// what fits and is accepted.
//
// Neuter checks: drop `&& !servedLocally(apiBase)` from dialectFor, and the
// request is refused and never asked again; make fitOutputCap return want, and
// the first request is refused.
func TestAVLLMModelIsNotRefusedForItsOutputCap(t *testing.T) {
	t.Setenv("MOCHIII_USE_PROXY", "")
	forgetLocalModels(t)
	forgetDialects(t)
	p := (&fakeProvider{refuse: vllmLike(8192)}).start(t)
	messages := []chatMessage{{Role: "user", Content: strings.Repeat("what does this code do? ", 40)}}

	if resp, err := openCompletion(context.Background(), p.url(), "", "m", messages, nil, plainRouting(), transportErr); err == nil {
		_ = resp.Body.Close()
		t.Fatal("the stand-in accepted 32,768 output tokens in an 8,192 window, so it tests nothing")
	}
	rememberLocalModel(p.url(), "m", localWindow{kind: localKindByID("vllm"), window: 8192})
	before := len(p.bodies())
	resp, err := openCompletion(context.Background(), p.url(), "", "m", messages, nil, plainRouting(), transportErr)
	if err != nil {
		t.Fatalf("an identified vLLM still refused the request: %v", err)
	}
	_ = resp.Body.Close()
	sent := p.bodies()[before:]
	if len(sent) != 1 {
		t.Fatalf("%d requests were needed, want 1: the first should already fit", len(sent))
	}
	if limit, _ := sent[0]["max_tokens"].(float64); limit <= 0 || limit >= 8192 {
		t.Errorf("max_tokens = %v, want a cap that leaves the prompt room in 8,192", sent[0]["max_tokens"])
	}
	if _, routed := sent[0]["provider"]; routed {
		t.Error("the plain request still carried OpenRouter's routing object")
	}
}

// A WINDOW THAT WAS READ TOO LARGE IS NOT A DEAD END. The server refuses a cap
// fitted to the wrong window, and is then asked with no cap at all, which a
// server with its own limit fills with what is left.
//
// Neuter check: drop the d.window branch from requestDialect.adapt, and the
// request steps through 32,768-down caps that all stay too large.
func TestALocalServerThatRefusesAFittedCapIsAskedWithNone(t *testing.T) {
	t.Setenv("MOCHIII_USE_PROXY", "")
	forgetLocalModels(t)
	forgetDialects(t)
	p := (&fakeProvider{refuse: vllmLike(2048)}).start(t)
	rememberLocalModel(p.url(), "m", localWindow{kind: localKindByID("vllm"), window: 32768})
	resp, err := openCompletion(context.Background(), p.url(), "", "m", []chatMessage{{Role: "user", Content: "hi"}}, nil, plainRouting(), transportErr)
	if err != nil {
		t.Fatalf("refused after adapting: %v", err)
	}
	_ = resp.Body.Close()
	got := p.bodies()
	if len(got) != 2 {
		t.Fatalf("%d requests, want 2: the fitted cap, then none", len(got))
	}
	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		if _, sent := got[1][field]; sent {
			t.Errorf("the second request still sent %s=%v", field, got[1][field])
		}
	}
}

func TestFitOutputCap(t *testing.T) {
	short := []chatMessage{{Role: "user", Content: "hi"}}
	for _, tc := range []struct {
		name               string
		want, window, cap_ int
		messages           []chatMessage
	}{
		{"no window read: the tier's cap", 32768, 0, 32768, short},
		{"no cap asked for: none", 0, 4096, 0, short},
		{"a cap smaller than the room is kept", 1000, 32768, 1000, short},
		{"the floor, when the prompt fills the window", 32768, 4096, localOutputFloor,
			[]chatMessage{{Role: "user", Content: strings.Repeat("x", 4096*localCharsPerToken)}}},
	} {
		if got := fitOutputCap(tc.want, tc.window, tc.messages, nil); got != tc.cap_ {
			t.Errorf("%s: fitOutputCap = %d, want %d", tc.name, got, tc.cap_)
		}
	}
	got := fitOutputCap(32768, 4096, short, nil)
	if got <= localOutputFloor || got > 4096-localOutputMargin {
		t.Errorf("a short prompt in 4,096 tokens: cap %d, want most of the window", got)
	}
}
