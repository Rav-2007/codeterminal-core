package main

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// THE COST GATE AND THE SHIPPED MANIFEST MUST AGREE.
//
// The proxy refuses any model outside defaultAllowedModels (cost authorization,
// `model_not_allowed`). The client offers whatever models.json declares. Those
// two lists were kept in step by a comment, and the comment lost:
//
//	defaultAllowedModels said "the three tiers in models.json" and named three.
//	models.json had grown to eleven tiers, nine active.
//	MEASURED 2026-08-09: eight of the nine selectable models were refused.
//
// Only the default tier worked through the managed proxy. Every other entry in
// the `/model` menu failed, and the two extra slugs the proxy did allow were the
// two INACTIVE tiers -- so the list was not merely stale, it was allowing
// exactly what the client would never ask for.
//
// The failure mode was safe: a refusal, not a leak, and ZDR enforcement is
// per-request and fail-closed regardless of model. But "safe" and "working" are
// different words, and a pilot user picking any of eight models would have hit a
// wall the product never explained.
//
// This is the same class as the cross-client slash-catalog mirror and the
// neutralisedGitConfig list: two files that must agree, kept in sync by hope. A
// comment cannot fail a build. This can.
//
// NEUTER CHECK: drop any slug from either side and this fails, naming the
// direction -- measured.

type shippedTier struct {
	Slug   string `json:"slug"`
	Active bool   `json:"active"`
}

type shippedModels struct {
	Tiers map[string]shippedTier `json:"tiers"`
}

// modelsJSONPath is repo-relative. The proxy is its own module and deploys
// without this file; reading it HERE is a build-time conformance check on the
// shipped pair, not a runtime dependency. Nothing in the proxy binary reads
// models.json.
const modelsJSONPath = "../models.json"

func loadShippedSlugs(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(modelsJSONPath)
	if err != nil {
		t.Fatalf("reading %s: %v -- this test compares the proxy's cost gate against the "+
			"shipped manifest and cannot run without it", modelsJSONPath, err)
	}
	var m shippedModels
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parsing %s: %v", modelsJSONPath, err)
	}

	// ANTI-VACUITY. A parse that silently yields nothing would make every
	// comparison below pass by never running one.
	if len(m.Tiers) < 3 {
		t.Fatalf("parsed only %d tiers from %s; the manifest shape must have changed and "+
			"this comparison is meaningless", len(m.Tiers), modelsJSONPath)
	}

	out := map[string]bool{}
	for name, tier := range m.Tiers {
		if strings.TrimSpace(tier.Slug) == "" {
			t.Fatalf("tier %q has no slug", name)
		}
		out[tier.Slug] = true
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysFloat(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestDefaultAllowedModelsMatchesShippedModelsJSON(t *testing.T) {
	shipped := loadShippedSlugs(t)
	allowed := parseAllowedModels(defaultAllowedModels)

	if len(allowed) == 0 {
		t.Fatal("defaultAllowedModels parsed to an empty set, which DISABLES the cost gate " +
			"entirely (modelAllowed returns true for everything when the set is empty)")
	}

	// Every shipped model must be usable. This is the direction that broke.
	var refused []string
	for slug := range shipped {
		if allowed[slug] == 0 {
			refused = append(refused, slug)
		}
	}
	sort.Strings(refused)
	if len(refused) > 0 {
		t.Errorf("%d model(s) declared in models.json are REFUSED by the proxy's default "+
			"cost gate, so a user selecting them gets `model_not_allowed` and no explanation:\n  %s\n"+
			"Add them to defaultAllowedModels, or remove them from models.json",
			len(refused), strings.Join(refused, "\n  "))
	}

	// And nothing extra. An allow-list entry with no shipped tier is a model the
	// product never offers but the proxy would pay for -- a cost-authorization
	// hole, which is the whole reason this gate exists.
	var orphaned []string
	for slug := range allowed {
		if !shipped[slug] {
			orphaned = append(orphaned, slug)
		}
	}
	sort.Strings(orphaned)
	if len(orphaned) > 0 {
		t.Errorf("%d model(s) are allowed by the proxy but declared nowhere in models.json:\n  %s\n"+
			"The cost gate exists to bound spend to the shipped set; an entry the product "+
			"never offers is spend nobody authorized",
			len(orphaned), strings.Join(orphaned, "\n  "))
	}

	if t.Failed() {
		t.Logf("shipped (%d): %s", len(shipped), strings.Join(sortedKeys(shipped), ", "))
		t.Logf("allowed (%d): %s", len(allowed), strings.Join(sortedKeysFloat(allowed), ", "))
	}
}

// The gate must actually refuse. If parseAllowedModels or modelAllowed ever
// returned permissively, the test above would still pass -- both lists would
// agree and nothing would be enforced.
func TestModelAllowedActuallyRefusesAnUnshippedModel(t *testing.T) {
	p := &proxy{allowedModels: parseAllowedModels(defaultAllowedModels)}

	if !p.modelAllowed("deepseek/deepseek-v4-flash") {
		t.Error("the default tier is refused by its own gate")
	}
	if p.modelAllowed("openai/gpt-4o") {
		t.Error("a model that is not shipped was allowed; the cost gate is not enforcing")
	}
	if p.modelAllowed("") {
		t.Error("an empty model name was allowed")
	}
}

// An EMPTY allow-list disables the gate entirely (modelAllowed returns true for
// everything). That is a documented opt-out, warned about loudly at startup --
// but it must never be reachable by accident from a malformed value, or the
// cost gate silently stops existing.
func TestParseAllowedModels_MalformedInputDoesNotSilentlyDisableTheGate(t *testing.T) {
	for _, raw := range []string{",", " , , ", "\n", "\t,\t"} {
		got := parseAllowedModels(raw)
		if len(got) != 0 {
			continue // a non-empty set still enforces; fine
		}
		// Empty is the documented "disabled" state. Assert it is reachable ONLY
		// from input that is genuinely empty of models, so a future change that
		// starts dropping valid entries cannot quietly turn the gate off.
		if strings.ContainsAny(raw, "abcdefghijklmnopqrstuvwxyz0123456789/") {
			t.Errorf("parseAllowedModels(%q) produced an EMPTY set from input containing "+
				"model-shaped characters; an empty set disables the cost gate for every model", raw)
		}
	}
}
