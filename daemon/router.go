package main

// tierReasoning is the only tier the router may escalate to, and only on a
// non-zero exit signal. Ghost-text is a separate, out-of-scope typing-
// completion path and is never selectable here.
const tierReasoning = "reasoning"

// RouteInput describes what's known about a request at routing time.
// PromptKind is reserved for future prompt-kind differentiation (e.g.
// distinguishing edits from questions) and is not branched on yet.
type RouteInput struct {
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

// Route selects a model tier for a request and resolves its slug from cfg.
// It never returns an inactive or missing tier's slug: reasoning is only
// selected on a genuine non-zero-exit signal, and only if it is active in
// cfg; every other case falls back to cfg's default tier. Route does not
// call the model — it only decides.
func Route(cfg *Config, input RouteInput) RouteDecision {
	if input.HasExitSignal && input.LastExitCode != 0 {
		if tier, ok := cfg.Tiers[tierReasoning]; ok && tier.Active && tier.Slug != "" {
			return RouteDecision{Tier: tierReasoning, Slug: tier.Slug, Reason: "non-zero exit escalation"}
		}
		return defaultDecision(cfg, "non-zero exit, but reasoning tier is inactive or missing")
	}
	return defaultDecision(cfg, "default")
}

// defaultDecision resolves cfg's own default tier, which LoadConfig has
// already validated to be active with a non-empty slug.
func defaultDecision(cfg *Config, reason string) RouteDecision {
	return RouteDecision{Tier: cfg.DefaultTier, Slug: cfg.ResolvedSlug(), Reason: reason}
}
