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
	PromptKind    string
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
// It never returns an inactive or missing tier's slug: reasoning is only
// selected on a genuine escalation signal (non-zero exit, or an explicit
// client-set PromptKind -- see reasoningSignals), and only if the tier is
// active in cfg; every other case falls back to cfg's default tier. Route
// does not call the model — it only decides.
func Route(cfg *Config, input RouteInput) RouteDecision {
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
