package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Using a model server on this machine: the stored key stays with the address
// it was saved for, a model the config does not list can be named, and a slow
// local first token is not mistaken for a stalled provider.

func openRouterCredential() storedCredential {
	return storedCredential{APIBase: "https://openrouter.ai/api/v1", APIKey: "sk-or-v1-stored", Verified: true}
}

func discardf(string, ...any) {}

// The stored OpenRouter key is NOT sent to a local server.
func TestTheStoredKeyIsNotSentToALocalServer(t *testing.T) {
	var logged []string
	logf := func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	key, base := fillFromStored("", "http://localhost:11434/v1", openRouterCredential(), logf)
	if key != "" {
		t.Errorf("the key saved for OpenRouter was used for %s", base)
	}
	if base != "http://localhost:11434/v1" {
		t.Errorf("base = %q, the environment's base was replaced", base)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "not using the key") || strings.Contains(logged[0], "sk-or-v1-stored") {
		t.Errorf("log = %q, want one line saying why, without the key", logged)
	}
}

// The ordinary cases are unchanged.
func TestTheStoredKeyIsUsedWhereItWasSaved(t *testing.T) {
	stored := openRouterCredential()
	for _, tc := range []struct {
		name, envKey, envBase, wantKey, wantBase string
		cred                                     storedCredential
	}{
		{"no base in the environment", "", "", "sk-or-v1-stored", "https://openrouter.ai/api/v1", stored},
		{"the same base", "", "https://openrouter.ai/api/v1", "sk-or-v1-stored", "https://openrouter.ai/api/v1", stored},
		{"a trailing slash and upper case", "", "HTTPS://OpenRouter.ai/api/v1/", "sk-or-v1-stored", "HTTPS://OpenRouter.ai/api/v1/", stored},
		{"a file saved before api_base was recorded", "", "https://openrouter.ai/api/v1", "sk-or-v1-stored", "https://openrouter.ai/api/v1",
			storedCredential{APIKey: "sk-or-v1-stored"}},
		{"a key in the environment wins", "env-key", "http://localhost:11434/v1", "env-key", "http://localhost:11434/v1", stored},
		// CHANGED 2026-10-05, deliberately. This used to expect ("", openai): the
		// environment's base won and the daemon ran against OpenAI with no key,
		// which can only fail. A named provider's address with no key beside it
		// is not a configuration, so the stored credential -- a key AND its own
		// address -- is used whole. The key still goes only where it was saved.
		{"a named provider's address with no key of its own", "", "https://api.openai.com/v1", "sk-or-v1-stored", "https://openrouter.ai/api/v1", stored},
		// A custom address is different: it may be meant to run with no key, so
		// it is kept and the stored key is withheld from it, as before.
		{"a custom remote address", "", "https://llm.example.com/v1", "", "https://llm.example.com/v1", stored},
	} {
		key, base := fillFromStored(tc.envKey, tc.envBase, tc.cred, discardf)
		if key != tc.wantKey || base != tc.wantBase {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", tc.name, key, base, tc.wantKey, tc.wantBase)
		}
	}
}

// A local server with no key asks for none.
func TestALocalServerWithNoKeyNeedsNone(t *testing.T) {
	key, base := fillFromStored("", "http://127.0.0.1:1234/v1", openRouterCredential(), discardf)
	s := &Server{}
	s.setProvider(key, base, nil)
	if s.needsAPIKey() {
		t.Error("a local server with no key made the daemon ask for a provider key")
	}
}

// MOCHIII_MODEL replaces the tiers: every route, default or user-picked, calls
// that model.
func TestUseOnlyModelIsWhatEveryRouteCalls(t *testing.T) {
	cfg := &Config{DefaultTier: "primary", Tiers: map[string]ModelTier{
		"primary":   {Slug: "deepseek/deepseek-v4-flash", Active: true},
		"reasoning": {Slug: "deepseek/deepseek-r1", Active: true},
	}}
	cfg.UseOnlyModel("qwen2.5-coder:7b", "MOCHIII_MODEL")
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the config no longer validates: %v", err)
	}
	for _, in := range []RouteInput{
		{},
		{PreferredTier: "primary"},
		{PreferredTier: "reasoning"},
		{HasExitSignal: true, LastExitCode: 1},
	} {
		if got := Route(cfg, in).Slug; got != "qwen2.5-coder:7b" {
			t.Errorf("Route(%+v) = %q, want the local model", in, got)
		}
	}
}

// Only a local server gets the long first-token allowance.
func TestOnlyALocalServerWaitsLongForItsFirstToken(t *testing.T) {
	if got := stallTimeoutFor("https://openrouter.ai/api/v1"); got != streamStallTimeout {
		t.Errorf("a remote provider's stall timeout is %s, want %s", got, streamStallTimeout)
	}
	if got := stallTimeoutFor("http://localhost:11434/v1"); got != localStallTimeout || got <= streamStallTimeout {
		t.Errorf("a local server's stall timeout is %s, want the longer %s", got, localStallTimeout)
	}
}

// A local model reading a long prompt says nothing for longer than a remote
// provider may; that is not a stall.
func TestASlowLocalFirstTokenIsNotAStall(t *testing.T) {
	prev := streamStallTimeout
	streamStallTimeout = 100 * time.Millisecond
	t.Cleanup(func() { streamStallTimeout = prev })

	base, requests := stallServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(400 * time.Millisecond)
		sseContent(w, "hello from a local model")
		sseDone(w)
	})
	got, _, err := runStream(t, base)
	if err != nil || got != "hello from a local model" {
		t.Fatalf("got %q, err %v", got, err)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("%d requests, want 1: the slow first token was treated as a stall and retried", n)
	}
}
