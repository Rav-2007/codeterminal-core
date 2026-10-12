package main

import (
	"context"
	"strings"
	"testing"

	"mochiii/protocol"
)

// modelChoiceServer is a daemon with two selectable models and one switched
// off, and a stored key for the address it is using -- the ordinary state after
// /connect.
func modelChoiceServer(t *testing.T) (*Server, string) {
	t.Helper()
	s, path := connectServer(t)
	s.cfg = &Config{
		DefaultTier: "tested",
		Tiers: map[string]ModelTier{
			"tested": {Slug: "vendor/tested", Active: true},
			"other":  {Slug: "vendor/other", Active: true},
			"off":    {Slug: "vendor/off", Active: false},
		},
	}
	if err := saveCredential(path, storedCredential{APIBase: s.apiBase, APIKey: s.apiKey, Verified: true}); err != nil {
		t.Fatal(err)
	}
	return s, path
}

func chooseModelRequest(name string) protocol.ConnectRequest {
	return protocol.ConnectRequest{Connect: true, ChooseModel: name}
}

// THE OWNER'S REQUIREMENT (2026-10-11): the model picked is the model -- for the
// very next prompt, and after a restart. So a choice has to change what the
// daemon routes to AND be written where the next start reads it.
//
// Neuter check: drop `s.chosenCfg = chosen` in chooseModel (the default does not
// move), or drop the saveCredential call (nothing survives a restart).
func TestAChosenModelIsTheDefaultAtOnceAndIsSavedBesideTheKey(t *testing.T) {
	s, path := modelChoiceServer(t)

	resp := s.connectResult(context.Background(), chooseModelRequest("other"))
	if !resp.Ok || resp.Outcome != protocol.ConnectModelChosen || resp.Model != "other" {
		t.Fatalf("outcome %q ok=%v model %q (%s %s); want model_chosen for other", resp.Outcome, resp.Ok, resp.Model, resp.Detail, resp.Error)
	}
	if !resp.ModelSaved {
		t.Errorf("the choice was not reported as saved: %s", resp.Detail)
	}
	if got := s.tierConfig().DefaultTier; got != "other" {
		t.Errorf("prompts still go to %q: the choice was acknowledged and not applied", got)
	}
	if got := s.tierConfig().ResolvedSlug(); got != "vendor/other" {
		t.Errorf("the default resolves to %q, want vendor/other", got)
	}
	stored, _, err := loadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ChosenModel != "other" {
		t.Errorf("credentials.json holds chosen_model %q, so a restart would forget the choice", stored.ChosenModel)
	}
	// The key it sits beside is untouched: this request carries none.
	if key, base := s.credentials(); key != "sk-the-key-it-started-with" || base != "https://old.example/v1" {
		t.Errorf("choosing a model changed the credential in force (%s at %s)", maskKey(key), base)
	}
	if stored.APIKey != "sk-the-key-it-started-with" || !stored.Verified {
		t.Error("choosing a model rewrote the stored key or its verified mark")
	}
	// The config every other setting is read from is not the one that changed.
	if s.cfg.DefaultTier != "tested" {
		t.Errorf("the daemon's own config was edited in place (default_tier %q)", s.cfg.DefaultTier)
	}
}

// /model clear goes back to the model the daemon found to answer -- which is why
// the choice is kept in a field of its own rather than written over that one.
func TestClearingTheChoiceGoesBackToTheModelInForceBefore(t *testing.T) {
	s, path := modelChoiceServer(t)
	s.connectResult(context.Background(), chooseModelRequest("other"))

	resp := s.connectResult(context.Background(), protocol.ConnectRequest{Connect: true, ClearModel: true})
	if !resp.Ok || resp.Outcome != protocol.ConnectModelChosen || resp.Model != "tested" {
		t.Fatalf("outcome %q model %q (%s); want model_chosen back on tested", resp.Outcome, resp.Model, resp.Error)
	}
	if got := s.tierConfig().DefaultTier; got != "tested" {
		t.Errorf("default is still %q after clear", got)
	}
	stored, _, err := loadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ChosenModel != "" {
		t.Errorf("chosen_model %q is still stored, so the next start would bring the choice back", stored.ChosenModel)
	}
}

// A NAME THE DAEMON CANNOT ROUTE TO IS REFUSED, NOT STORED. Accepting it would
// make every prompt fail on a model that does not exist, from a setting the user
// can no longer see.
func TestAModelThatIsNotOfferedIsRefusedAndChangesNothing(t *testing.T) {
	for _, name := range []string{"no-such-model", "off"} {
		s, path := modelChoiceServer(t)
		resp := s.connectResult(context.Background(), chooseModelRequest(name))
		if resp.Ok || resp.Error == "" {
			t.Errorf("%s: accepted (%q), though the daemon cannot send a prompt to it", name, resp.Outcome)
		}
		if !strings.Contains(resp.Error, "/model") {
			t.Errorf("%s: the refusal does not say where the valid names are: %s", name, resp.Error)
		}
		if got := s.tierConfig().DefaultTier; got != "tested" {
			t.Errorf("%s: a refused choice moved the default to %q", name, got)
		}
		if stored, _, _ := loadCredential(path); stored.ChosenModel != "" {
			t.Errorf("%s: a refused choice was stored as %q", name, stored.ChosenModel)
		}
	}
}

// WITH A KEY FROM THE ENVIRONMENT THERE IS NOTHING TO SAVE IT BESIDE. The choice
// still holds for this run -- refusing it would make /model useless there -- and
// the answer must not claim a restart keeps it.
func TestAChoiceIsNotSavedWhenTheKeyIsNotTheStoredOne(t *testing.T) {
	t.Run("key in the environment", func(t *testing.T) {
		s, path := modelChoiceServer(t)
		t.Setenv("MOCHIII_API_KEY", "sk-from-the-environment")
		resp := s.connectResult(context.Background(), chooseModelRequest("other"))
		if !resp.Ok || resp.ModelSaved {
			t.Fatalf("ok=%v saved=%v; want applied and NOT saved (%s)", resp.Ok, resp.ModelSaved, resp.Detail)
		}
		if !strings.Contains(resp.Detail, "until this daemon stops") {
			t.Errorf("the answer does not say the choice is temporary: %s", resp.Detail)
		}
		if got := s.tierConfig().DefaultTier; got != "other" {
			t.Errorf("default is %q: the choice was not applied for this run", got)
		}
		if stored, _, _ := loadCredential(path); stored.ChosenModel != "" {
			t.Errorf("a model was written beside a key that is not the one in use (%q)", stored.ChosenModel)
		}
	})
	t.Run("stored key is for another address", func(t *testing.T) {
		s, path := modelChoiceServer(t)
		s.apiBase = "http://127.0.0.1:11434/v1"
		resp := s.connectResult(context.Background(), chooseModelRequest("other"))
		if !resp.Ok || resp.ModelSaved {
			t.Fatalf("ok=%v saved=%v; want applied and NOT saved (%s)", resp.Ok, resp.ModelSaved, resp.Detail)
		}
		if stored, _, _ := loadCredential(path); stored.ChosenModel != "" {
			t.Errorf("a local server's model name was written beside another provider's key (%q)", stored.ChosenModel)
		}
	})
}

// A CHOICE IS AMONG ONE CREDENTIAL'S MODELS. Putting another provider in force
// must drop it, or the daemon would default to a name the new provider has never
// heard of.
func TestPuttingAnotherProviderInForceDropsTheChoice(t *testing.T) {
	s, _ := modelChoiceServer(t)
	s.connectResult(context.Background(), chooseModelRequest("other"))

	theirs := &Config{DefaultTier: "theirs", Tiers: map[string]ModelTier{"theirs": {Slug: "theirs", Active: true}}}
	s.setProvider("sk-another-key", "https://another.example/v1", theirs)
	if got := s.tierConfig().DefaultTier; got != "theirs" {
		t.Errorf("after switching provider the default is %q, a model of the provider that was left", got)
	}
}

// withDefaultTier is the whole validation of a choice, at a request and at start.
func TestWithDefaultTierAcceptsOnlyWhatCanBeRoutedTo(t *testing.T) {
	cfg := &Config{DefaultTier: "a", Tiers: map[string]ModelTier{
		"a": {Slug: "v/a", Active: true}, "b": {Slug: "v/b", Active: true},
		"off": {Slug: "v/off"}, "empty": {Active: true},
	}}
	got := withDefaultTier(cfg, "b")
	if got == nil || got.DefaultTier != "b" || got.ResolvedSlug() != "v/b" {
		t.Fatalf("an active tier was not accepted: %+v", got)
	}
	if cfg.DefaultTier != "a" {
		t.Error("the config passed in was changed")
	}
	for _, name := range []string{"", "missing", "off", "empty"} {
		if withDefaultTier(cfg, name) != nil {
			t.Errorf("%q was accepted as a default tier", name)
		}
	}
	if withDefaultTier(nil, "a") != nil {
		t.Error("a nil config produced a config")
	}
}

// The handshake names the feature, so a client can tell a daemon that would read
// a model choice as "connect with no key".
func TestTheDaemonSaysItKeepsAModelChoice(t *testing.T) {
	s := &Server{}
	var found bool
	for _, f := range s.features() {
		if f == protocol.FeatureModelChoice {
			found = true
		}
	}
	if !found {
		t.Error("features() does not name model_choice")
	}
}
