package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sendWith runs one streamCompletion against a capturing server and returns
// the raw request body it sent.
func sendWith(t *testing.T, routing providerRouting) string {
	t.Helper()
	srv, captured := sseServer(t, "SomeProvider", "hi")
	defer srv.Close()
	_, err := streamCompletion(context.Background(), srv.URL, "test-key", "some/model",
		buildChatMessages("sys", nil, "hello"), nil, routing,
		func(string) error { return nil }, nil, nil, nil)
	if err != nil {
		t.Fatalf("streamCompletion: %v", err)
	}
	return string(*captured)
}

func tierConfig() *Config {
	return &Config{
		DefaultTier: "primary",
		Tiers: map[string]ModelTier{
			"primary": {Slug: "cheap/model", Active: true},
			"thinker": {Slug: "cheap/model", Active: true, ReasoningEffort: "medium", ProviderSort: "throughput"},
		},
		ZDR: ZDRConfig{AllowFallbacks: true, ProviderSort: "price"},
	}
}

// A tier that sets nothing must send the body it sent before these settings
// existed: no "reasoning" key, and the zdr block's own sort.
func TestATierWithoutSettingsSendsTheSameBodyAsBefore(t *testing.T) {
	cfg := tierConfig()
	body := sendWith(t, cfg.routingFor("primary"))
	if strings.Contains(body, `"reasoning"`) {
		t.Errorf("a tier with no reasoning_effort sent a reasoning field:\n%s", body)
	}
	if !strings.Contains(body, `"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true,"sort":"price"}`) {
		t.Errorf("the provider object changed for a tier with no settings:\n%s", body)
	}
	// And the routing it builds is exactly the zdr block's.
	if got, want := cfg.routingFor("primary"), cfg.ZDR.resolvedProviderRouting(); got.Sort != want.Sort || got.reasoningEffort != "" {
		t.Errorf("routingFor(primary) = %+v, want the zdr routing %+v", got, want)
	}
}

// The tier's settings reach the wire: its reasoning effort as the top-level
// "reasoning" object, its sort in place of the zdr block's -- and the ZDR
// flags are untouched by either.
func TestATiersReasoningEffortAndSortReachTheRequest(t *testing.T) {
	body := sendWith(t, tierConfig().routingFor("thinker"))
	if !strings.Contains(body, `"reasoning":{"effort":"medium"}`) {
		t.Errorf("the tier's reasoning effort is not on the wire:\n%s", body)
	}
	if !strings.Contains(body, `"provider":{"zdr":true,"data_collection":"deny","allow_fallbacks":true,"sort":"throughput"}`) {
		t.Errorf("the tier's sort did not replace the zdr block's, or disturbed the ZDR flags:\n%s", body)
	}
	// The effort must never leak into the provider object itself.
	var sent struct {
		Provider map[string]any `json:"provider"`
	}
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatal(err)
	}
	for key := range sent.Provider {
		if strings.Contains(strings.ToLower(key), "reason") {
			t.Errorf("provider object carries %q", key)
		}
	}
}

// A request routed to something that is not a tier (the -model override, a
// test config) gets the plain ZDR routing -- never another tier's settings.
func TestAnUnknownTierGetsThePlainZDRRouting(t *testing.T) {
	got := tierConfig().routingFor("override")
	if got.Sort != "price" || got.reasoningEffort != "" {
		t.Errorf("routingFor(override) = %+v, want the zdr block's sort and no reasoning", got)
	}
}

// The two keys are understood (no "unknown key" warning), and an effort
// OpenRouter would reject is dropped with a warning instead of failing every
// request to that tier.
func TestTierSettingsAreParsedAndABadEffortIsDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	cfgJSON := `{"config_version":1,"default_tier":"primary","tiers":{
		"primary":{"slug":"a/b","active":true,"reasoning_effort":"medium","provider_sort":"throughput"},
		"odd":{"slug":"c/d","active":true,"reasoning_effort":"maximum"}}}`
	if err := os.WriteFile(path, []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range cfg.Warnings() {
		if strings.Contains(w, "unknown config key") {
			t.Errorf("a tier setting was reported unknown: %s", w)
		}
	}
	if p := cfg.Tiers["primary"]; p.ReasoningEffort != "medium" || p.ProviderSort != "throughput" {
		t.Errorf("primary = %+v, want its reasoning_effort and provider_sort", p)
	}
	if cfg.Tiers["odd"].ReasoningEffort != "" {
		t.Errorf("an unsupported effort was kept: %q", cfg.Tiers["odd"].ReasoningEffort)
	}
	warned := false
	for _, w := range cfg.Warnings() {
		warned = warned || strings.Contains(w, `tiers.odd.reasoning_effort "maximum"`)
	}
	if !warned {
		t.Errorf("no warning for the dropped effort; warnings: %v", cfg.Warnings())
	}
}

// The status surface says which tier is the default, so a client marks the
// configured default rather than assuming it is named "primary".
func TestTheStatusSurfaceNamesTheDefaultTier(t *testing.T) {
	cfg := tierConfig()
	cfg.DefaultTier = "thinker"
	defaults := 0
	for _, tier := range availableTiers(cfg) {
		if tier.Default {
			defaults++
			if tier.Name != "thinker" {
				t.Errorf("%q is marked default, want thinker", tier.Name)
			}
		}
	}
	if defaults != 1 {
		t.Errorf("%d tiers marked default, want exactly 1", defaults)
	}
}
