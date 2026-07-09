package main

import (
	"encoding/json"
	"testing"
)

// --- ZDRConfig.resolvedProviderRouting: secure-by-default enforcement ------

// TestZDRConfig_ZeroValueResolvesToStrictEnforcement is the single most
// important test in this slice: an absent "zdr" section in models.json (or
// any models.json predating this field) decodes to the zero-value
// ZDRConfig{}, and that MUST resolve to the strictest possible
// provider-routing constraints — zdr:true, data_collection:"deny",
// allow_fallbacks:false — with no way for "off" to be reachable by mere
// omission. If this test ever fails, the whole privacy pitch's code-level
// enforcement is silently disabled for every config that doesn't mention
// "zdr" at all.
func TestZDRConfig_ZeroValueResolvesToStrictEnforcement(t *testing.T) {
	got := ZDRConfig{}.resolvedProviderRouting()
	want := providerRouting{ZDR: true, DataCollection: "deny", AllowFallbacks: false}
	if got != want {
		t.Fatalf("resolvedProviderRouting() on zero-value ZDRConfig = %+v, want %+v (strict defaults)", got, want)
	}
}

// TestZDRConfig_EachWeakenFlagOnlyAffectsItsOwnField proves the three
// weaken-bools are independent and each does exactly what its name says,
// nothing more — a config that only sets AllowFallbacks, for example, must
// NOT accidentally also weaken zdr or data_collection.
func TestZDRConfig_EachWeakenFlagOnlyAffectsItsOwnField(t *testing.T) {
	cases := []struct {
		name string
		cfg  ZDRConfig
		want providerRouting
	}{
		{
			name: "AllowNonZDR only",
			cfg:  ZDRConfig{AllowNonZDR: true},
			want: providerRouting{ZDR: false, DataCollection: "deny", AllowFallbacks: false},
		},
		{
			name: "AllowDataCollection only",
			cfg:  ZDRConfig{AllowDataCollection: true},
			want: providerRouting{ZDR: true, DataCollection: "allow", AllowFallbacks: false},
		},
		{
			name: "AllowFallbacks only",
			cfg:  ZDRConfig{AllowFallbacks: true},
			want: providerRouting{ZDR: true, DataCollection: "deny", AllowFallbacks: true},
		},
		{
			name: "all three weakened",
			cfg:  ZDRConfig{AllowNonZDR: true, AllowDataCollection: true, AllowFallbacks: true},
			want: providerRouting{ZDR: false, DataCollection: "allow", AllowFallbacks: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.resolvedProviderRouting()
			if got != tc.want {
				t.Errorf("resolvedProviderRouting() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestConfig_UnmarshalWithoutZDRSectionStillResolvesStrict proves the
// end-to-end path: a models.json JSON blob that never mentions "zdr" at all
// (exactly what every models.json predating this feature looks like)
// decodes its Config.ZDR field to the zero value, and that zero value still
// resolves to strict enforcement — not just the struct literal in the test
// above, but the actual json.Unmarshal path a real config file goes
// through.
func TestConfig_UnmarshalWithoutZDRSectionStillResolvesStrict(t *testing.T) {
	const raw = `{
		"config_version": 1,
		"default_tier": "primary",
		"tiers": {"primary": {"slug": "deepseek/deepseek-v4-flash", "active": true}}
	}`

	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	got := cfg.ZDR.resolvedProviderRouting()
	want := providerRouting{ZDR: true, DataCollection: "deny", AllowFallbacks: false}
	if got != want {
		t.Fatalf("a models.json with no \"zdr\" section resolved to %+v, want strict defaults %+v", got, want)
	}
}
