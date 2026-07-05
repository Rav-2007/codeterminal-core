package main

import "testing"

func testConfig(reasoningActive bool) *Config {
	return &Config{
		ConfigVersion: 1,
		DefaultTier:   "primary",
		Tiers: map[string]ModelTier{
			"primary":   {Slug: "vendor/primary-model", Active: true},
			"reasoning": {Slug: "vendor/reasoning-model", Active: reasoningActive},
		},
	}
}

func TestRoute_DefaultPromptGoesToPrimary(t *testing.T) {
	cfg := testConfig(false)
	decision := Route(cfg, RouteInput{})

	if decision.Tier != "primary" {
		t.Errorf("Tier = %q, want %q", decision.Tier, "primary")
	}
	if decision.Slug != "vendor/primary-model" {
		t.Errorf("Slug = %q, want %q", decision.Slug, "vendor/primary-model")
	}
}

func TestRoute_NonZeroExitWithReasoningActive_SelectsReasoning(t *testing.T) {
	cfg := testConfig(true)
	decision := Route(cfg, RouteInput{HasExitSignal: true, LastExitCode: 1})

	if decision.Tier != "reasoning" {
		t.Errorf("Tier = %q, want %q", decision.Tier, "reasoning")
	}
	if decision.Slug != "vendor/reasoning-model" {
		t.Errorf("Slug = %q, want %q", decision.Slug, "vendor/reasoning-model")
	}
}

func TestRoute_NonZeroExitWithReasoningInactive_RealConfig_StaysOnPrimary(t *testing.T) {
	cfg, err := LoadConfig("../models.json")
	if err != nil {
		t.Fatalf("loading real repo config: %v", err)
	}
	if cfg.Tiers["reasoning"].Active {
		t.Fatal("models.json must ship with reasoning inactive for this test to be meaningful")
	}

	decision := Route(cfg, RouteInput{HasExitSignal: true, LastExitCode: 1})

	if decision.Tier != cfg.DefaultTier {
		t.Errorf("Tier = %q, want default tier %q (reasoning must stay gated off)", decision.Tier, cfg.DefaultTier)
	}
	if decision.Slug != cfg.ResolvedSlug() {
		t.Errorf("Slug = %q, want %q", decision.Slug, cfg.ResolvedSlug())
	}
}

func TestRoute_ZeroExitCodeDoesNotEscalate(t *testing.T) {
	cfg := testConfig(true)
	decision := Route(cfg, RouteInput{HasExitSignal: true, LastExitCode: 0})

	if decision.Tier != "primary" {
		t.Errorf("Tier = %q, want %q (exit 0 is success, not a failure signal)", decision.Tier, "primary")
	}
}

func TestRoute_MissingReasoningTier_FallsBackToPrimary(t *testing.T) {
	cfg := &Config{
		ConfigVersion: 1,
		DefaultTier:   "primary",
		Tiers: map[string]ModelTier{
			"primary": {Slug: "vendor/primary-model", Active: true},
		},
	}

	decision := Route(cfg, RouteInput{HasExitSignal: true, LastExitCode: 1})

	if decision.Tier != "primary" {
		t.Errorf("Tier = %q, want %q (unknown tier must never be selected)", decision.Tier, "primary")
	}
	if decision.Slug != "vendor/primary-model" {
		t.Errorf("Slug = %q, want %q", decision.Slug, "vendor/primary-model")
	}
}

func TestRoute_ReasoningActiveButEmptySlug_FallsBackToPrimary(t *testing.T) {
	cfg := &Config{
		ConfigVersion: 1,
		DefaultTier:   "primary",
		Tiers: map[string]ModelTier{
			"primary":   {Slug: "vendor/primary-model", Active: true},
			"reasoning": {Slug: "", Active: true},
		},
	}

	decision := Route(cfg, RouteInput{HasExitSignal: true, LastExitCode: 1})

	if decision.Tier != "primary" {
		t.Errorf("Tier = %q, want %q (active tier with empty slug must never be selected)", decision.Tier, "primary")
	}
}
