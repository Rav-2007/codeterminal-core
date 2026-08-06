package main

import "strings"

// tierReasoning is the only tier the router may escalate to. Escalation is
// triggered by explicit signals only (see reasoningSignals) -- never by
// reading or guessing at prompt content. Ghost-text is a separate,
// out-of-scope typing-completion path and is never selectable here.
const tierReasoning = "reasoning"

// PromptKindReason and PromptKindRefactor are the only two RouteInput.
// PromptKind values that request reasoning-tier escalation. A client sets
// this directly via an explicit action (slash command / keybind) -- it is
// never inferred from prompt text. Any other value, including the empty
// string every request sends today, is inert.
const (
	PromptKindReason   = "reason"
	PromptKindRefactor = "refactor"
)

var reasoningPromptKinds = map[string]bool{
	PromptKindReason:   true,
	PromptKindRefactor: true,
}

// RouteInput describes what's known about a request at routing time.
type RouteInput struct {
	// PromptKind is an explicit, client-stated signal (see
	// reasoningPromptKinds) -- not inferred from prompt content.
	PromptKind string
	// PreferredTier is an explicit user model choice (models.json tier name).
	// When set to an active tier with a non-empty slug it wins over default
	// routing and over PromptKind escalation. Unknown/inactive names fall
	// through with a reason string — they never invent a slug.
	PreferredTier string
	HasExitSignal bool
	LastExitCode  int
}

// RouteDecision is the tier the router selected and the slug resolved for
// it. Reason explains why, for logging.
type RouteDecision struct {
	Tier   string
	Slug   string
	Reason string
}

// reasoningSignals returns every true escalation cause in input, for
// RouteDecision.Reason. There is only ever one escalation target
// (tierReasoning), so multiple true causes never change the decision --
// but a Reason string that silently reported only the first one would
// still misrepresent why a request escalated, so every true cause is
// recorded.
func reasoningSignals(input RouteInput) []string {
	var signals []string
	if input.HasExitSignal && input.LastExitCode != 0 {
		signals = append(signals, "non-zero exit escalation")
	}
	if reasoningPromptKinds[input.PromptKind] {
		signals = append(signals, "explicit prompt kind \""+input.PromptKind+"\"")
	}
	return signals
}

// Route selects a model tier for a request and resolves its slug from cfg.
// Preference order:
//  1. PreferredTier when it names an active tier with a non-empty slug
//  2. reasoning escalation on a genuine signal, if that tier is active
//  3. cfg's default tier
//
// It never returns an inactive or missing tier's slug. Route does not call
// the model — it only decides.
func Route(cfg *Config, input RouteInput) RouteDecision {
	if name := strings.TrimSpace(input.PreferredTier); name != "" {
		if tier, ok := cfg.Tiers[name]; ok && tier.Active && tier.Slug != "" {
			return RouteDecision{Tier: name, Slug: tier.Slug, Reason: "user-selected tier"}
		}
		return defaultDecision(cfg, "preferred tier \""+name+"\" is unknown, inactive, or has no slug")
	}
	if signals := reasoningSignals(input); len(signals) > 0 {
		reason := strings.Join(signals, "; ")
		if tier, ok := cfg.Tiers[tierReasoning]; ok && tier.Active && tier.Slug != "" {
			return RouteDecision{Tier: tierReasoning, Slug: tier.Slug, Reason: reason}
		}
		return defaultDecision(cfg, reason+", but reasoning tier is inactive or missing")
	}
	return defaultDecision(cfg, "default")
}

// defaultDecision resolves cfg's own default tier, which LoadConfig has
// already validated to be active with a non-empty slug.
func defaultDecision(cfg *Config, reason string) RouteDecision {
	return RouteDecision{Tier: cfg.DefaultTier, Slug: cfg.ResolvedSlug(), Reason: reason}
}
