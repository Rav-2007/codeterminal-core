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
}

// RetrievalConfig controls retrieval-augmented context injection in the live
// prompt path. It is deliberately Disabled-bool (not Enabled-bool): a
// models.json predating this field decodes to the zero value, and the zero
// value of Disabled is false, so retrieval defaults ON for every existing
// config without requiring a migration.
type RetrievalConfig struct {
	Disabled bool `json:"disabled,omitempty"`
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
