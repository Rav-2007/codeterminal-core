package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// ModelTier describes one entry in models.json. Only active tiers may ever
// be called; inactive tiers are parsed and held in memory but unused.
type ModelTier struct {
	Slug   string `json:"slug"`
	Active bool   `json:"active"`
	Note   string `json:"note,omitempty"`
}

// Config is the parsed form of models.json. It contains model slugs and
// metadata only — never credentials. Those still come from
// CODETERMINAL_API_BASE / CODETERMINAL_API_KEY.
type Config struct {
	ConfigVersion int                  `json:"config_version"`
	DefaultTier   string               `json:"default_tier"`
	Tiers         map[string]ModelTier `json:"tiers"`
	Retrieval     RetrievalConfig      `json:"retrieval,omitempty"`
	ZDR           ZDRConfig            `json:"zdr,omitempty"`
}

// ZDRConfig controls the OpenRouter provider-routing constraints sent with
// every inference request (see providerRouting in provider.go). Every field
// here is a WEAKEN-bool — the opposite polarity from what it controls —
// deliberately, so the zero value (an absent "zdr" section, e.g. in a
// models.json predating this field) resolves to the strictest possible
// enforcement: zero-data-retention-only routing, no data-collecting
// providers, no fallback to an unconstrained provider. This mirrors
// RetrievalConfig's Disabled-bool-defaults-enabled pattern above, but here
// the stakes are the whole privacy pitch, so "off" must never be reachable
// by omission — only by an explicit, auditable "true" in models.json.
type ZDRConfig struct {
	// AllowNonZDR, if true, stops constraining routing to zero-data-retention
	// endpoints (sends provider.zdr=false instead of true).
	AllowNonZDR bool `json:"allow_non_zdr,omitempty"`
	// AllowDataCollection, if true, permits providers that may store/train
	// on request data (sends provider.data_collection="allow" instead of
	// "deny").
	AllowDataCollection bool `json:"allow_data_collection,omitempty"`
	// AllowFallbacks, if true, permits OpenRouter to reroute to a provider
	// outside the above constraints if none qualify, instead of the request
	// failing loudly (sends provider.allow_fallbacks=true instead of false).
	AllowFallbacks bool `json:"allow_fallbacks,omitempty"`
}

// resolvedProviderRouting returns the provider-routing object to send with
// every inference request. Called fresh per-request (not cached) so a
// config reload always takes effect immediately.
func (c ZDRConfig) resolvedProviderRouting() providerRouting {
	dataCollection := "deny"
	if c.AllowDataCollection {
		dataCollection = "allow"
	}
	return providerRouting{
		ZDR:            !c.AllowNonZDR,
		DataCollection: dataCollection,
		AllowFallbacks: c.AllowFallbacks,
	}
}

// RetrievalConfig controls retrieval-augmented context injection in the live
// prompt path. It is deliberately Disabled-bool (not Enabled-bool): a
// models.json predating this field decodes to the zero value, and the zero
// value of Disabled is false, so retrieval defaults ON for every existing
// config without requiring a migration.
type RetrievalConfig struct {
	Disabled bool `json:"disabled,omitempty"`
	// RerankDisabled bypasses file-class re-ranking (rerank.go), falling
	// back to raw vector-similarity order — the same Disabled-bool-defaults-
	// enabled pattern as Disabled above, so an existing models.json decodes
	// to false (re-ranking on) without needing a migration.
	RerankDisabled bool `json:"rerank_disabled,omitempty"`
	// TopK is how many chunks to retrieve per prompt. 0 (or absent) falls
	// back to defaultK, the same default the CLI `retrieve` command uses.
	TopK int `json:"top_k,omitempty"`
	// ContextBudgetChars caps the total rendered size of injected context.
	// This is a character budget, not a token budget: computing real token
	// counts would require pulling a tokenizer into the daemon module,
	// which is otherwise CGO-free and dependency-light by design (see
	// helper/onnxembedder.go for where that dependency already lives, on
	// the other side of the CGO boundary). A char-based ceiling is a fine
	// approximation for a soft safety cap. 0 (or absent) falls back to
	// defaultContextBudgetChars.
	ContextBudgetChars int `json:"context_budget_chars,omitempty"`
}

// defaultContextBudgetChars caps injected retrieval context at roughly
// 2000-2600 tokens (code averages ~3-4 chars/token), a small, conservative
// slice of any modern model's context window — plenty of headroom under the
// system prompt, conversation, and generation budget.
const defaultContextBudgetChars = 8000

// resolvedTopK returns the configured TopK, falling back to defaultK.
func (c RetrievalConfig) resolvedTopK() int {
	if c.TopK > 0 {
		return c.TopK
	}
	return defaultK
}

// resolvedContextBudgetChars returns the configured budget, falling back to
// defaultContextBudgetChars.
func (c RetrievalConfig) resolvedContextBudgetChars() int {
	if c.ContextBudgetChars > 0 {
		return c.ContextBudgetChars
	}
	return defaultContextBudgetChars
}

// LoadConfig reads and validates a models.json file at path.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}

	return &cfg, nil
}

// Validate checks that the config is internally consistent: the default
// tier exists, is active, and has a slug; and that no active tier is
// missing a slug (which would make it silently uncallable once enabled).
func (c *Config) Validate() error {
	if len(c.Tiers) == 0 {
		return fmt.Errorf("no tiers defined")
	}

	def, ok := c.Tiers[c.DefaultTier]
	if !ok {
		return fmt.Errorf("default_tier %q is not defined in tiers", c.DefaultTier)
	}
	if !def.Active {
		return fmt.Errorf("default_tier %q is not active", c.DefaultTier)
	}
	if def.Slug == "" {
		return fmt.Errorf("default_tier %q has an empty slug", c.DefaultTier)
	}

	for name, tier := range c.Tiers {
		if tier.Active && tier.Slug == "" {
			return fmt.Errorf("active tier %q has an empty slug", name)
		}
	}

	return nil
}

// ResolvedSlug returns the model slug for the configured default tier.
func (c *Config) ResolvedSlug() string {
	return c.Tiers[c.DefaultTier].Slug
}
