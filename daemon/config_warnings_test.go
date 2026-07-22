package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes body as a models.json in a temp dir and loads it,
// failing the test if the load itself errors — these cases are all about
// configs that LOAD FINE and are merely not fully understood.
func loadConfigBody(t *testing.T, body string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cfg
}

const validTiers = `"default_tier":"primary","tiers":{"primary":{"slug":"a/b","active":true}}`

func warningsMentioning(cfg *Config, substr string) []string {
	var hits []string
	for _, w := range cfg.Warnings() {
		if strings.Contains(w, substr) {
			hits = append(hits, w)
		}
	}
	return hits
}

func TestConfigCleanFileWarnsNothing(t *testing.T) {
	cfg := loadConfigBody(t, `{"config_version":1,`+validTiers+`}`)
	if got := cfg.Warnings(); len(got) != 0 {
		t.Errorf("a fully-understood config produced warnings: %v", got)
	}
}

func TestConfigVersionWarnings(t *testing.T) {
	t.Run("future version", func(t *testing.T) {
		cfg := loadConfigBody(t, `{"config_version":99,`+validTiers+`}`)
		if len(warningsMentioning(cfg, "newer than this build")) == 0 {
			t.Errorf("config_version 99 produced no forward-version warning: %v", cfg.Warnings())
		}
	})

	t.Run("absent version", func(t *testing.T) {
		cfg := loadConfigBody(t, `{`+validTiers+`}`)
		if len(warningsMentioning(cfg, "config_version is absent")) == 0 {
			t.Errorf("absent config_version produced no warning: %v", cfg.Warnings())
		}
	})
}

// The case that motivated this: "retreival" is a misspelling of "retrieval",
// so encoding/json discards it along with the top_k the user meant to set, and
// retrieval runs at the default with nothing said.
func TestConfigUnknownKeyIsNamed(t *testing.T) {
	cfg := loadConfigBody(t, `{"config_version":1,`+validTiers+`,"retreival":{"top_k":50}}`)

	hits := warningsMentioning(cfg, `"retreival"`)
	if len(hits) == 0 {
		t.Fatalf("misspelled top-level key produced no warning: %v", cfg.Warnings())
	}
	if cfg.Retrieval.resolvedTopK() != defaultK {
		t.Errorf("top_k = %d; the misspelled key must NOT take effect", cfg.Retrieval.resolvedTopK())
	}
}

func TestConfigUnknownNestedKeys(t *testing.T) {
	cfg := loadConfigBody(t, `{"config_version":1,`+validTiers+`,
		"retrieval":{"top_kk":9},
		"zdr":{"allow_everything":true}}`)

	if len(warningsMentioning(cfg, "retrieval.top_kk")) == 0 {
		t.Errorf("unknown retrieval key not reported: %v", cfg.Warnings())
	}
	if len(warningsMentioning(cfg, "zdr.allow_everything")) == 0 {
		t.Errorf("unknown zdr key not reported: %v", cfg.Warnings())
	}
}

func TestConfigUnknownTierKey(t *testing.T) {
	cfg := loadConfigBody(t, `{"config_version":1,"default_tier":"primary",
		"tiers":{"primary":{"slug":"a/b","active":true,"activate":true}}}`)
	if len(warningsMentioning(cfg, "tiers.primary.activate")) == 0 {
		t.Errorf("unknown per-tier key not reported: %v", cfg.Warnings())
	}
}

func TestConfigRangeClamping(t *testing.T) {
	t.Run("negative falls back to defaults with a warning", func(t *testing.T) {
		cfg := loadConfigBody(t, `{"config_version":1,`+validTiers+`,
			"retrieval":{"top_k":-5,"context_budget_chars":-100}}`)

		if len(warningsMentioning(cfg, "top_k -5 is negative")) == 0 {
			t.Errorf("negative top_k not reported: %v", cfg.Warnings())
		}
		if got := cfg.Retrieval.resolvedTopK(); got != defaultK {
			t.Errorf("resolvedTopK = %d, want default %d", got, defaultK)
		}
		if got := cfg.Retrieval.resolvedContextBudgetChars(); got != defaultContextBudgetChars {
			t.Errorf("resolvedContextBudgetChars = %d, want default %d", got, defaultContextBudgetChars)
		}
	})

	t.Run("absurd values are clamped, not applied verbatim", func(t *testing.T) {
		cfg := loadConfigBody(t, `{"config_version":1,`+validTiers+`,
			"retrieval":{"top_k":100000,"context_budget_chars":100000000}}`)

		if got := cfg.Retrieval.resolvedTopK(); got != maxTopK {
			t.Errorf("resolvedTopK = %d, want clamp to %d", got, maxTopK)
		}
		if got := cfg.Retrieval.resolvedContextBudgetChars(); got != maxContextBudgetChars {
			t.Errorf("resolvedContextBudgetChars = %d, want clamp to %d", got, maxContextBudgetChars)
		}
		if len(warningsMentioning(cfg, "clamped")) < 2 {
			t.Errorf("expected a clamp warning for each value: %v", cfg.Warnings())
		}
	})

	t.Run("in-range values are untouched and unwarned", func(t *testing.T) {
		cfg := loadConfigBody(t, `{"config_version":1,`+validTiers+`,
			"retrieval":{"top_k":12,"context_budget_chars":16000}}`)

		if got := cfg.Retrieval.resolvedTopK(); got != 12 {
			t.Errorf("resolvedTopK = %d, want 12", got)
		}
		if got := cfg.Retrieval.resolvedContextBudgetChars(); got != 16000 {
			t.Errorf("resolvedContextBudgetChars = %d, want 16000", got)
		}
		if got := cfg.Warnings(); len(got) != 0 {
			t.Errorf("in-range values warned: %v", got)
		}
	})
}

// A config that cannot be honored at all must still be a hard error, not a
// warning — the warn-and-continue path must not have softened Validate.
func TestConfigStillRejectsUnservableFiles(t *testing.T) {
	for name, body := range map[string]string{
		"no tiers":            `{"config_version":1,"default_tier":"primary","tiers":{}}`,
		"missing default":     `{"config_version":1,"default_tier":"nope","tiers":{"primary":{"slug":"a/b","active":true}}}`,
		"inactive default":    `{"config_version":1,"default_tier":"primary","tiers":{"primary":{"slug":"a/b","active":false}}}`,
		"active tier no slug": `{"config_version":1,"default_tier":"primary","tiers":{"primary":{"slug":"a/b","active":true},"x":{"active":true}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "models.json")
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(path); err == nil {
				t.Fatal("LoadConfig accepted an unservable config")
			}
		})
	}
}
