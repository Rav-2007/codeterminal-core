package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
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
//
// warnings holds non-fatal problems found while loading (unknown/misspelled
// keys, an unrecognized config_version, out-of-range values that were
// clamped). It is unexported and therefore invisible to encoding/json, which
// is deliberate: it describes THIS load of the file, not the file's content,
// and must never round-trip back out as if it were configuration. Read it via
// Warnings(); the daemon logs it at startup and reports it on the status
// surface, so a config typo is visible to an operator instead of silently
// discarding the setting the user thought they had made.
type Config struct {
	ConfigVersion int                  `json:"config_version"`
	DefaultTier   string               `json:"default_tier"`
	Tiers         map[string]ModelTier `json:"tiers"`
	Retrieval     RetrievalConfig      `json:"retrieval,omitempty"`
	ZDR           ZDRConfig            `json:"zdr,omitempty"`
	// NoScrub disables heuristic secret scrubbing of the user's prompt (see
	// daemon/scrub.go) when true. False (the zero value) is scrubbing ON,
	// so an existing models.json predating this field keeps scrubbing
	// enabled without needing a migration. Settable via models.json or
	// overridden at startup by the daemon's own --no-scrub flag (see
	// main.go) — either source setting it true disables scrubbing.
	NoScrub bool `json:"no_scrub,omitempty"`

	// MCP configures agent mode: which MCP servers may run, what their tools
	// are allowed to do without asking, and how far one turn may go. An absent
	// section means agent mode is OFF and the daemon behaves exactly as it did
	// before MCP existed — see mcpconfig.go for the full polarity rule.
	MCP MCPConfig `json:"mcp,omitempty"`

	warnings []string
}

// Warnings returns the non-fatal problems found when this config was loaded,
// in file order. Empty (nil) means the file was fully understood. Safe on a
// Config built directly by a test, which simply has none.
func (c *Config) Warnings() []string { return c.warnings }

func (c *Config) warnf(format string, args ...any) {
	c.warnings = append(c.warnings, fmt.Sprintf(format, args...))
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
	// ProviderIgnoreList names providers OpenRouter must never route to (sent
	// as the provider-routing "ignore" deny-list). Unlike the weaken-bools
	// above it carries no polarity subtlety: an empty/absent list is the
	// migration-free default and produces exactly today's wire body (the
	// providerRouting.Ignore field is omitempty). Its purpose is to exclude a
	// single provider (DeepInfra, pending the OpenRouter retention finding)
	// while keeping the rest of the ZDR pool available — a deny-list, not an
	// allow-list, chosen for pool width and self-maintenance (D4).
	ProviderIgnoreList []string `json:"provider_ignore_list,omitempty"`
	// ProviderOrder specifies a strict preference list of providers.
	ProviderOrder []string `json:"provider_order,omitempty"`
	// ProviderSort specifies the provider property to sort by ("price" or "throughput").
	ProviderSort string `json:"provider_sort,omitempty"`
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
		Ignore:         c.ProviderIgnoreList,
		Order:          c.ProviderOrder,
		Sort:           c.ProviderSort,
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

// supportedConfigVersion is the models.json schema version this build
// understands. A file declaring a HIGHER version may contain keys with
// semantics this build does not implement — which is exactly the silent-
// degradation shape this cluster exists to close — so it is warned about
// loudly rather than accepted mutely. It is deliberately NOT a hard failure:
// refusing to start would make a downgrade (running an older daemon against a
// newer config) unrecoverable, and every key this build does not recognize is
// already reported individually by checkUnknownKeys below.
const supportedConfigVersion = 1

// Value bounds for the tunable retrieval settings. Absent/zero still means
// "use the default" (the documented, migration-free behavior — see
// RetrievalConfig); these bound what an explicitly SET value may be.
//
// A negative value used to fall back to the default silently, and an absurd
// one (top_k: 100000, context_budget_chars: 100000000 were both accepted
// verbatim) was applied as written — the second defeats the entire purpose of
// a context budget. Both are now clamped WITH a warning, so the effective
// value and the operator's belief about it can't diverge.
const (
	maxTopK               = 200
	maxContextBudgetChars = 200_000
)

// knownConfigKeys, knownRetrievalKeys and knownZDRKeys mirror the json tags of
// Config, RetrievalConfig and ZDRConfig. encoding/json ignores a key it doesn't
// recognize, so a misspelling is silently discarded along with whatever the
// user meant by it: `"retreival": {"top_k": 50}` parses fine and leaves
// retrieval running at the default top_k, with nothing said. These sets are
// what turn that silence into a warning naming the offending key.
var (
	knownConfigKeys    = []string{"config_version", "default_tier", "tiers", "retrieval", "zdr", "no_scrub", "mcp"}
	knownRetrievalKeys = []string{"disabled", "rerank_disabled", "top_k", "context_budget_chars"}
	knownZDRKeys       = []string{"allow_non_zdr", "allow_data_collection", "allow_fallbacks", "provider_ignore_list", "provider_order", "provider_sort"}
	knownTierKeys      = []string{"slug", "active", "note"}
)

// LoadConfig reads and validates a models.json file at path.
//
// Two kinds of problem are distinguished. A config that cannot be honored at
// all — unreadable, unparseable, or internally inconsistent (see Validate) —
// is an error and the daemon refuses to start on it. A config that is usable
// but not fully understood — an unrecognized version, a misspelled key, an
// out-of-range value — is recorded on the returned Config's warnings and the
// daemon starts, since none of those makes the file unservable and refusing
// forward/backward compatibility would be worse than reporting it.
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

	cfg.checkVersion()
	cfg.checkUnknownKeys(data)
	cfg.clampRanges()

	return &cfg, nil
}

// checkVersion warns when config_version is absent or names a schema this
// build doesn't implement. Version 0 (absent) is the pre-versioning shape and
// is accepted as "assume current" — the same no-migration-needed courtesy the
// Disabled-bool defaults extend — but it is still reported, because "the file
// predates versioning" and "someone deleted the version line" look identical
// from here and only the operator can tell them apart.
func (c *Config) checkVersion() {
	switch {
	case c.ConfigVersion == 0:
		c.warnf("config_version is absent; assuming %d (the version this build implements)", supportedConfigVersion)
	case c.ConfigVersion > supportedConfigVersion:
		c.warnf("config_version %d is newer than this build understands (%d); settings this build does not implement are being IGNORED, not applied",
			c.ConfigVersion, supportedConfigVersion)
	case c.ConfigVersion < supportedConfigVersion:
		c.warnf("config_version %d is older than this build's %d", c.ConfigVersion, supportedConfigVersion)
	}
}

// checkUnknownKeys re-walks the raw JSON and warns about every key no field
// consumes, at the top level and inside the retrieval/zdr/tiers objects. It
// re-decodes rather than using json.Decoder's DisallowUnknownFields because
// that turns an unknown key into a hard parse failure, which would break
// forward compatibility outright — the goal here is to SAY something, not to
// refuse the file.
func (c *Config) checkUnknownKeys(data []byte) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return // Validate/Unmarshal above already accepted it; nothing to add
	}

	c.warnUnknown(raw, knownConfigKeys, "")

	if sub, ok := raw["retrieval"]; ok {
		c.warnNested(sub, knownRetrievalKeys, "retrieval")
	}
	if sub, ok := raw["zdr"]; ok {
		c.warnNested(sub, knownZDRKeys, "zdr")
	}
	if sub, ok := raw["tiers"]; ok {
		var tiers map[string]json.RawMessage
		if err := json.Unmarshal(sub, &tiers); err == nil {
			for name, tier := range tiers {
				c.warnNested(tier, knownTierKeys, "tiers."+name)
			}
		}
	}
	if sub, ok := raw["mcp"]; ok {
		c.warnNested(sub, knownMCPKeys, "mcp")

		var mcp map[string]json.RawMessage
		if err := json.Unmarshal(sub, &mcp); err == nil {
			if budget, ok := mcp["budget"]; ok {
				c.warnNested(budget, knownMCPBudgetKeys, "mcp.budget")
			}
			if builtin, ok := mcp["builtin"]; ok {
				c.warnNested(builtin, knownMCPBuiltinKeys, "mcp.builtin")
			}
			if servers, ok := mcp["servers"]; ok {
				var byName map[string]json.RawMessage
				if err := json.Unmarshal(servers, &byName); err == nil {
					// Sorted so a config with several misspelled server keys
					// reports them in a stable order across runs.
					names := make([]string, 0, len(byName))
					for name := range byName {
						names = append(names, name)
					}
					slices.Sort(names)
					for _, name := range names {
						c.warnNested(byName[name], knownMCPServerKeys, "mcp.servers."+name)
					}
				}
			}
		}
	}
}

// warnNested decodes one nested object and warns about its unknown keys.
func (c *Config) warnNested(raw json.RawMessage, known []string, prefix string) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return
	}
	c.warnUnknown(obj, known, prefix)
}

// warnUnknown emits one warning per key of obj that isn't in known, sorted so
// the output is stable across runs (Go map iteration order is not).
func (c *Config) warnUnknown(obj map[string]json.RawMessage, known []string, prefix string) {
	var unknown []string
	for key := range obj {
		if !slices.Contains(known, key) {
			unknown = append(unknown, key)
		}
	}
	slices.Sort(unknown)
	for _, key := range unknown {
		full := key
		if prefix != "" {
			full = prefix + "." + key
		}
		c.warnf("unknown config key %q is being IGNORED (check the spelling; whatever it was meant to set is not in effect)", full)
	}
}

// clampRanges brings explicitly-set retrieval values inside their bounds,
// warning for each one it changes. A value left at zero is untouched: that
// means "use the default" and is resolved later by resolvedTopK /
// resolvedContextBudgetChars, not here.
func (c *Config) clampRanges() {
	if c.Retrieval.TopK < 0 {
		c.warnf("retrieval.top_k %d is negative; using the default (%d)", c.Retrieval.TopK, defaultK)
		c.Retrieval.TopK = 0
	} else if c.Retrieval.TopK > maxTopK {
		c.warnf("retrieval.top_k %d exceeds the maximum %d; clamped to %d", c.Retrieval.TopK, maxTopK, maxTopK)
		c.Retrieval.TopK = maxTopK
	}

	if c.Retrieval.ContextBudgetChars < 0 {
		c.warnf("retrieval.context_budget_chars %d is negative; using the default (%d)", c.Retrieval.ContextBudgetChars, defaultContextBudgetChars)
		c.Retrieval.ContextBudgetChars = 0
	} else if c.Retrieval.ContextBudgetChars > maxContextBudgetChars {
		c.warnf("retrieval.context_budget_chars %d exceeds the maximum %d; clamped to %d",
			c.Retrieval.ContextBudgetChars, maxContextBudgetChars, maxContextBudgetChars)
		c.Retrieval.ContextBudgetChars = maxContextBudgetChars
	}

	c.clampMCPRanges()
	c.warnMCPPolicySurface()
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

	// MCP is validated last and can refuse the file. It is the one section
	// where an unreadable setting decides whether a program runs on the user's
	// machine, so its failures are errors rather than warnings -- see
	// validateMCP.
	return c.validateMCP()
}

// ResolvedSlug returns the model slug for the configured default tier.
func (c *Config) ResolvedSlug() string {
	return c.Tiers[c.DefaultTier].Slug
}
