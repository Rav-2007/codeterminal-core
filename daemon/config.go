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
