package main

import (
	"context"
	"strings"
	"testing"

	"mochiii/protocol"
)

func showRequest() protocol.ConnectRequest {
	return protocol.ConnectRequest{Connect: true, Show: true}
}

// WHAT /account AND THE LINE UNDER THE PROMPT ARE DRAWN FROM (OWNER, 2026-10-12):
// the provider, key and model this daemon is sending to NOW. Show used to answer
// only for the stored credential, which is the same thing right up until it is
// not.
//
// Neuter check: return resp unchanged from shownInForce -- every field below is
// then empty, KeySource included, and a client reads that as an older daemon.
func TestShowSaysWhatIsInForce(t *testing.T) {
	s, _ := modelChoiceServer(t)

	resp := s.connectResult(context.Background(), showRequest())
	if !resp.Ok || resp.Outcome != protocol.ConnectShown {
		t.Fatalf("outcome %q ok=%v (%s)", resp.Outcome, resp.Ok, resp.Error)
	}
	if resp.KeySource != protocol.KeySourceStored {
		t.Errorf("key source = %q, want %q", resp.KeySource, protocol.KeySourceStored)
	}
	if resp.ActiveAPIBase != "https://old.example/v1" {
		t.Errorf("address in force = %q, want the one the daemon is using", resp.ActiveAPIBase)
	}
	if resp.Model != "tested" || resp.ModelSlug != "vendor/tested" {
		t.Errorf("model in force = %q (%q), want tested (vendor/tested)", resp.Model, resp.ModelSlug)
	}
	// Two can be chosen between: the one switched off is not a choice.
	if resp.ModelCount != 2 {
		t.Errorf("model count = %d, want 2", resp.ModelCount)
	}
	if resp.ModelSaved {
		t.Error("a model nobody chose is reported as remembered")
	}
	// The key in force is described, and never sent back.
	if resp.ActiveMaskedKey == "" || strings.Contains(resp.ActiveMaskedKey, "sk-the-key") {
		t.Errorf("the key in force is shown as %q: want a mask, and never the key", resp.ActiveMaskedKey)
	}
}

// A choice the daemon saved is reported as remembered, and one it could only
// keep for this run is not -- the same line chooseModel draws, read back.
func TestShowSaysRememberedOnlyForAChoiceARestartWouldFind(t *testing.T) {
	s, _ := modelChoiceServer(t)
	if resp := s.connectResult(context.Background(), chooseModelRequest("other")); !resp.ModelSaved {
		t.Fatalf("setup: the choice was not saved (%s)", resp.Detail)
	}
	shown := s.connectResult(context.Background(), showRequest())
	if shown.Model != "other" || !shown.ModelSaved {
		t.Errorf("after choosing other: model %q saved=%v, want other and remembered", shown.Model, shown.ModelSaved)
	}

	// The same choice with a key from the environment holds only for this run.
	t.Setenv("MOCHIII_API_KEY", "sk-from-the-environment")
	shown = s.connectResult(context.Background(), showRequest())
	if shown.ModelSaved {
		t.Error("with the key coming from the environment the choice is still called remembered")
	}
}

// THE CASE THE STORED CREDENTIAL GETS WRONG. A key in the environment is what
// the daemon sends, to the address IT was started with -- and a client that
// named the stored key's provider would name one nothing is being sent to.
func TestShowNamesTheEnvironmentWhenItsKeyIsTheOneInForce(t *testing.T) {
	s, path := connectServer(t)
	if err := saveCredential(path, storedCredential{
		APIBase: "https://stored.example/v1", APIKey: testProviderKey, Verified: true}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOCHIII_API_KEY", "sk-from-the-environment")

	resp := s.connectResult(context.Background(), showRequest())
	if resp.KeySource != protocol.KeySourceEnvironment {
		t.Errorf("key source = %q, want %q", resp.KeySource, protocol.KeySourceEnvironment)
	}
	if resp.ActiveAPIBase != "https://old.example/v1" {
		t.Errorf("address in force = %q, want the daemon's own", resp.ActiveAPIBase)
	}
	// What is stored is still described as stored -- beside, not instead of.
	if resp.APIBase != "https://stored.example/v1" || resp.InUse {
		t.Errorf("the stored key is described as %q in_use=%v; want its own address, and not in use", resp.APIBase, resp.InUse)
	}
}

// The proxy's credential is not a provider key and is not shown as one; and a
// daemon with no key says so rather than masking an empty string into a claim.
func TestShowDoesNotDescribeAKeyThatIsNotAProviderKey(t *testing.T) {
	s, _ := connectServer(t)
	t.Setenv("MOCHIII_USE_PROXY", "true")
	if resp := s.connectResult(context.Background(), showRequest()); resp.KeySource != protocol.KeySourceProxy || resp.ActiveMaskedKey != "" {
		t.Errorf("proxy mode: source %q, key shown as %q; want proxy and nothing", resp.KeySource, resp.ActiveMaskedKey)
	}

	t.Setenv("MOCHIII_USE_PROXY", "")
	s.apiKey = ""
	if resp := s.connectResult(context.Background(), showRequest()); resp.KeySource != protocol.KeySourceNone || resp.ActiveMaskedKey != "" {
		t.Errorf("no key: source %q, key shown as %q; want none and nothing", resp.KeySource, resp.ActiveMaskedKey)
	}
}
