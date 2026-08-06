package main

import (
	"strings"
	"testing"
)

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

func TestRoute_PromptKindSignals(t *testing.T) {
	cases := []struct {
		name            string
		reasoningActive bool
		input           RouteInput
		wantTier        string
		wantSlug        string
		wantReasonHas   []string // substrings that must all appear in Reason
	}{
		{
			name:            "reason kind escalates when tier active",
			reasoningActive: true,
			input:           RouteInput{PromptKind: PromptKindReason},
			wantTier:        "reasoning",
			wantSlug:        "vendor/reasoning-model",
			wantReasonHas:   []string{`explicit prompt kind "reason"`},
		},
		{
			name:            "refactor kind escalates when tier active",
			reasoningActive: true,
			input:           RouteInput{PromptKind: PromptKindRefactor},
			wantTier:        "reasoning",
			wantSlug:        "vendor/reasoning-model",
			wantReasonHas:   []string{`explicit prompt kind "refactor"`},
		},
		{
			name:            "unrecognized kind is inert, falls through to default",
			reasoningActive: true,
			input:           RouteInput{PromptKind: "explain"},
			wantTier:        "primary",
			wantSlug:        "vendor/primary-model",
		},
		{
			name:            "empty kind (today's default) is inert",
			reasoningActive: true,
			input:           RouteInput{PromptKind: ""},
			wantTier:        "primary",
			wantSlug:        "vendor/primary-model",
		},
		{
			name:            "reason kind present but reasoning tier inactive falls back",
			reasoningActive: false,
			input:           RouteInput{PromptKind: PromptKindReason},
			wantTier:        "primary",
			wantSlug:        "vendor/primary-model",
			wantReasonHas:   []string{`explicit prompt kind "reason"`, "inactive or missing"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(tc.reasoningActive)
			decision := Route(cfg, tc.input)

			if decision.Tier != tc.wantTier {
				t.Errorf("Tier = %q, want %q", decision.Tier, tc.wantTier)
			}
			if decision.Slug != tc.wantSlug {
				t.Errorf("Slug = %q, want %q", decision.Slug, tc.wantSlug)
			}
			for _, substr := range tc.wantReasonHas {
				if !strings.Contains(decision.Reason, substr) {
					t.Errorf("Reason = %q, want it to contain %q", decision.Reason, substr)
				}
			}
		})
	}
}

// TestRoute_ExitSignalAndPromptKindBothTrue is the dual-condition case for
// the new signal: a request can carry both a non-zero exit signal and an
// explicit reasoning PromptKind at once. Unlike the P4 header-notice bug,
// there is only one escalation target, so the decision (Tier/Slug) can
// never be ambiguous -- this test proves that, and separately proves the
// Reason log string reports BOTH true causes rather than silently keeping
// only the first-checked one.
func TestRoute_ExitSignalAndPromptKindBothTrue(t *testing.T) {
	cfg := testConfig(true)
	decision := Route(cfg, RouteInput{
		HasExitSignal: true,
		LastExitCode:  1,
		PromptKind:    PromptKindReason,
	})

	if decision.Tier != "reasoning" {
		t.Errorf("Tier = %q, want %q", decision.Tier, "reasoning")
	}
	if decision.Slug != "vendor/reasoning-model" {
		t.Errorf("Slug = %q, want %q", decision.Slug, "vendor/reasoning-model")
	}
	if !strings.Contains(decision.Reason, "non-zero exit escalation") {
		t.Errorf("Reason = %q, want it to mention the exit-code cause", decision.Reason)
	}
	if !strings.Contains(decision.Reason, `explicit prompt kind "reason"`) {
		t.Errorf("Reason = %q, want it to mention the PromptKind cause too (not silently dropped)", decision.Reason)
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

func TestRoute_PreferredTierWins(t *testing.T) {
	cfg := testConfig(true)
	cfg.Tiers["minimax_m3"] = ModelTier{Slug: "minimax/minimax-m3", Active: true}

	decision := Route(cfg, RouteInput{
		PreferredTier: "minimax_m3",
		PromptKind:    PromptKindReason, // must NOT override preferred
	})
	if decision.Tier != "minimax_m3" || decision.Slug != "minimax/minimax-m3" {
		t.Fatalf("got tier=%q slug=%q, want minimax_m3", decision.Tier, decision.Slug)
	}
	if decision.Reason != "user-selected tier" {
		t.Errorf("Reason = %q", decision.Reason)
	}
}

func TestRoute_PreferredTierUnknownFallsBack(t *testing.T) {
	cfg := testConfig(false)
	decision := Route(cfg, RouteInput{PreferredTier: "no_such_tier"})
	if decision.Tier != "primary" {
		t.Errorf("Tier = %q, want primary", decision.Tier)
	}
	if !strings.Contains(decision.Reason, "no_such_tier") {
		t.Errorf("Reason = %q, want mention of unknown tier", decision.Reason)
	}
}

func TestRoute_PreferredTierInactiveFallsBack(t *testing.T) {
	cfg := testConfig(false)
	cfg.Tiers["ghost_text"] = ModelTier{Slug: "vendor/ghost", Active: false}
	decision := Route(cfg, RouteInput{PreferredTier: "ghost_text"})
	if decision.Tier != "primary" {
		t.Errorf("Tier = %q, want primary for inactive preferred", decision.Tier)
	}
}

func TestLoadConfig_ExpandedModelsCatalog(t *testing.T) {
	cfg, err := LoadConfig("../models.json")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	want := []string{
		"primary", "laguna_s_21", "step_37_flash", "ling_30_flash",
		"minimax_m3", "qwen35_397b", "qwen36_plus", "deepseek_v4_pro", "gemini_36_flash",
	}
	for _, name := range want {
		tier, ok := cfg.Tiers[name]
		if !ok {
			t.Errorf("missing tier %q", name)
			continue
		}
		if !tier.Active {
			t.Errorf("tier %q should be active", name)
		}
		if tier.Slug == "" {
			t.Errorf("tier %q has empty slug", name)
		}
	}
}
