package protocol

import (
	"net/url"
	"strings"
	"testing"
)

// A KEY'S PREFIX NAMES ITS PROVIDER, and the daemon acts on that by sending the
// key there. So each case below is a claim about where a credential goes.
func TestProvidersForKeyNamesTheProviderTheKeyIsFrom(t *testing.T) {
	for key, want := range map[string]string{
		"sk-or-v1-0123456789abcdef":      "openrouter",
		"sk-ant-api03-0123456789abcdef":  "anthropic",
		"sk-proj-0123456789abcdef":       "openai",
		"sk-svcacct-0123456789abcdef":    "openai",
		"AIzaSy0123456789abcdef":         "gemini",
		"nvapi-0123456789abcdef":         "nvidia",
		"gsk_0123456789abcdef":           "groq",
		"xai-0123456789abcdef":           "xai",
		"tgp_v1_0123456789abcdef":        "together",
		"fw_0123456789abcdef":            "fireworks",
		"csk-0123456789abcdef":           "cerebras",
		"hf_0123456789abcdef":            "huggingface",
		"  nvapi-with-space-around-it  ": "nvidia",
	} {
		got, certain := ProvidersForKey(key)
		if !certain || len(got) != 1 || got[0].ID != want {
			t.Errorf("ProvidersForKey(%q) = %v certain=%v, want exactly %s", key, ids(got), certain, want)
		}
	}
}

// A BARE "sk-" KEY NAMES NOBODY. It is reported as a set of candidates and
// never as a certainty, because the caller sends a certain key to its provider
// and must ask about the rest -- trying each candidate would hand a working
// credential to companies that did not issue it.
//
// Neuter check: return matches[:1], true for the shared prefix.
func TestABareSkKeyIsNeverAttributedToOneProvider(t *testing.T) {
	got, certain := ProvidersForKey("sk-0123456789abcdef0123456789abcdef")
	if certain {
		t.Fatalf("a bare sk- key was attributed with certainty to %v", ids(got))
	}
	if len(got) < 2 {
		t.Fatalf("candidates = %v, want every provider that issues bare sk- keys", ids(got))
	}
	for _, p := range got {
		if !p.SharedKeyPrefix {
			t.Errorf("%s is a candidate for a bare sk- key but is not marked as issuing them", p.ID)
		}
		// A provider whose keys ALWAYS carry a longer prefix cannot have issued
		// this one: OpenRouter's are sk-or-, Anthropic's sk-ant-.
		if p.ID == ProviderOpenRouter || p.ID == "anthropic" {
			t.Errorf("%s offered for a bare sk- key; its keys always carry its own prefix", p.ID)
		}
	}
}

func TestAKeyWithNoKnownPrefixNamesNoProvider(t *testing.T) {
	for _, key := range []string{"", "0123456789abcdef0123456789abcdef", "Bearer abc", "SK-OR-v1-upper"} {
		if got, certain := ProvidersForKey(key); certain || len(got) != 0 {
			t.Errorf("ProvidersForKey(%q) = %v certain=%v, want nothing", key, ids(got), certain)
		}
	}
}

func TestProviderByNameTakesIDsAndAliasesIgnoringCase(t *testing.T) {
	for name, want := range map[string]string{
		"nvidia": "nvidia", "NVIDIA": "nvidia", " openai ": "openai", "google": "gemini",
		"claude": "anthropic", "grok": "xai", "kimi": "moonshot", "hf": "huggingface", "qwen": "dashscope",
	} {
		if p, ok := ProviderByName(name); !ok || p.ID != want {
			t.Errorf("ProviderByName(%q) = %q, %v; want %q", name, p.ID, ok, want)
		}
	}
	for _, name := range []string{"", "nvidi", "sk-or-v1-abc", "https://api.openai.com/v1"} {
		if p, ok := ProviderByName(name); ok {
			t.Errorf("ProviderByName(%q) found %q", name, p.ID)
		}
	}
}

// The path is ignored: a hand-typed base on a provider's host is still that
// provider, and treating it as a stranger would word its requests wrongly.
func TestProviderForBaseGoesByHost(t *testing.T) {
	for base, want := range map[string]string{
		"https://openrouter.ai/api/v1":              "openrouter",
		"HTTPS://OpenRouter.ai/api/v1/":             "openrouter",
		"https://integrate.api.nvidia.com/v1":       "nvidia",
		"https://api.groq.com/v1":                   "groq",
		"https://www.api.openai.com/v1":             "openai",
		"https://generativelanguage.googleapis.com": "gemini",
	} {
		if p, ok := ProviderForBase(base); !ok || p.ID != want {
			t.Errorf("ProviderForBase(%q) = %q, %v; want %q", base, p.ID, ok, want)
		}
	}
	for _, base := range []string{"", "http://localhost:11434/v1", "https://llm.example.com/v1", "::not a url",
		"https://openrouter.ai.evil.example/api/v1"} {
		if p, ok := ProviderForBase(base); ok {
			t.Errorf("ProviderForBase(%q) found %q", base, p.ID)
		}
	}
}

// THE LIST ITSELF. A key is sent wherever this says, so the properties that
// would send one to the wrong place are checked for every entry rather than
// trusted to whoever adds the next one.
func TestProviderListIsSafeToActOn(t *testing.T) {
	seenName := map[string]string{}
	seenHost := map[string]string{}
	var prefixes []struct{ prefix, owner string }

	for _, p := range Providers() {
		if p.ID == "" || p.ID != strings.ToLower(p.ID) || strings.ContainsAny(p.ID, " /:.") {
			t.Errorf("%q is not a name a user can type after /connect", p.ID)
		}
		if p.Name == "" {
			t.Errorf("%s has no display name", p.ID)
		}
		for _, name := range append([]string{p.ID}, p.Aliases...) {
			if other, dup := seenName[name]; dup {
				t.Errorf("the name %q means both %s and %s", name, other, p.ID)
			}
			seenName[name] = p.ID
		}

		u, err := url.Parse(p.APIBase)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" {
			t.Errorf("%s: %q is not a plain https address", p.ID, p.APIBase)
			continue
		}
		if strings.HasSuffix(p.APIBase, "/") {
			t.Errorf("%s: %q ends in a slash", p.ID, p.APIBase)
		}
		if other, dup := seenHost[u.Hostname()]; dup {
			t.Errorf("%s and %s share the host %s, so ProviderForBase cannot tell them apart", other, p.ID, u.Hostname())
		}
		seenHost[u.Hostname()] = p.ID
		if got, ok := ProviderForBase(p.APIBase); !ok || got.ID != p.ID {
			t.Errorf("%s: its own base resolves to %q", p.ID, got.ID)
		}

		for _, prefix := range p.KeyPrefixes {
			if len(prefix) < 3 {
				t.Errorf("%s: key prefix %q is too short to identify anyone", p.ID, prefix)
			}
			if prefix == sharedKeyPrefix {
				t.Errorf("%s claims the shared prefix %q as its own; use SharedKeyPrefix", p.ID, prefix)
			}
			prefixes = append(prefixes, struct{ prefix, owner string }{prefix, p.ID})
		}
	}

	// No provider's prefix may begin another provider's: the longer one would
	// win for its own keys, but every key of the shorter prefix's owner that
	// happened to continue that way would go to the wrong company.
	for _, a := range prefixes {
		for _, b := range prefixes {
			if a.owner != b.owner && strings.HasPrefix(b.prefix, a.prefix) {
				t.Errorf("%s's prefix %q begins %s's prefix %q", a.owner, a.prefix, b.owner, b.prefix)
			}
		}
	}

	// Providers hands out a copy: a caller that sorts or edits it must not
	// change where the next key goes.
	list := Providers()
	list[0].APIBase = "https://elsewhere.example"
	if Providers()[0].APIBase == "https://elsewhere.example" {
		t.Error("Providers returned the list itself, not a copy")
	}
}

func ids(ps []Provider) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}
