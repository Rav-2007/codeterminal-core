// Package config loads and validates the application's on-disk
// configuration file.
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config holds the settings read from the application's config file.
type Config struct {
	ListenAddr   string `json:"listen_addr"`
	DatabaseURL  string `json:"database_url"`
	MaxConns     int    `json:"max_conns"`
	RequestLimit int    `json:"request_limit"`
}

// Load reads the JSON configuration file at path and parses it into a
// Config, applying defaults for any fields left unset.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}

	applyDefaults(&cfg)

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}

	return &cfg, nil
}

// applyDefaults fills in zero-valued fields with sane defaults so a mostly
// empty config file still produces a usable Config.
func applyDefaults(cfg *Config) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:8080"
	}
	if cfg.MaxConns == 0 {
		cfg.MaxConns = 100
	}
	if cfg.RequestLimit == 0 {
		cfg.RequestLimit = 1000
	}
}

// Validate checks that required fields are present and within sane bounds.
func (c *Config) Validate() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("database_url must be set")
	}
	if c.MaxConns < 1 {
		return fmt.Errorf("max_conns must be at least 1, got %d", c.MaxConns)
	}
	if c.RequestLimit < 1 {
		return fmt.Errorf("request_limit must be at least 1, got %d", c.RequestLimit)
	}
	return nil
}
