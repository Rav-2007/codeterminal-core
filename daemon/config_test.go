package main

import (
	"encoding/json"
	"reflect"
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
	if !reflect.DeepEqual(got, want) {
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
			if !reflect.DeepEqual(got, tc.want) {
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
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("a models.json with no \"zdr\" section resolved to %+v, want strict defaults %+v", got, want)
	}
}

// --- D4: provider ignore-list (deny-list, e.g. exclude DeepInfra) -----------

// TestZDRConfig_ProviderIgnoreList_WireBody proves the two properties D4 needs.
// Set: the resolved routing carries the ignore list and the marshalled wire
// body puts ignore:["DeepInfra"] ALONGSIDE the ZDR flags (it does not replace or
// disturb them). Unset: the marshalled provider object is BYTE-IDENTICAL to
// today's, so the new field is inert until models.json opts in (the whole point
// of it being omitempty).
func TestZDRConfig_ProviderIgnoreList_WireBody(t *testing.T) {
	t.Run("set: ignore list rides alongside the ZDR flags", func(t *testing.T) {
		// Mirrors the shipped models.json posture (allow_fallbacks:true) plus the
		// D4 deny-list.
		cfg := ZDRConfig{AllowFallbacks: true, ProviderIgnoreList: []string{"DeepInfra"}}
		routing := cfg.resolvedProviderRouting()

		if !reflect.DeepEqual(routing.Ignore, []string{"DeepInfra"}) {
			t.Errorf("routing.Ignore = %v, want [DeepInfra]", routing.Ignore)
		}
		// The ZDR flags must be untouched by the deny-list.
		if !routing.ZDR || routing.DataCollection != "deny" || !routing.AllowFallbacks {
			t.Errorf("ignore list disturbed the ZDR flags: %+v", routing)
		}

		body, err := json.Marshal(routing)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		want := `{"zdr":true,"data_collection":"deny","allow_fallbacks":true,"ignore":["DeepInfra"]}`
		if string(body) != want {
			t.Errorf("wire body = %s\n           want %s", body, want)
		}
	})

	t.Run("unset: wire body is byte-identical to today's (omitempty inert)", func(t *testing.T) {
		// The shipped posture with no ignore list configured.
		cfg := ZDRConfig{AllowFallbacks: true}
		body, err := json.Marshal(cfg.resolvedProviderRouting())
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		// Exactly what the daemon put on the wire before D4 existed — no "ignore"
		// key at all.
		want := `{"zdr":true,"data_collection":"deny","allow_fallbacks":true}`
		if string(body) != want {
			t.Errorf("an unset ignore list changed the wire body:\n got = %s\nwant = %s", body, want)
		}
	})
}
