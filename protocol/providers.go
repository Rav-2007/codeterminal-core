package protocol

import (
	"net/url"
	"strings"
)

// THE PROVIDERS A KEY CAN BE FOR.
//
// One list, shared by the daemon and the clients, because both need it and two
// copies would drift: the daemon uses it to tell which provider a pasted key
// belongs to and where that provider lives, and a client uses it to say
// "Connected to NVIDIA" instead of an address and to accept `/connect nvidia`.
//
// Every APIBase here is the provider's OpenAI-compatible endpoint -- the one
// that answers POST {base}/chat/completions. Each was reached on 2026-10-05
// and refused a deliberately invalid key there, which is what shows the address
// is real and that it authenticates.
//
// THIS HOLDS NO MODEL NAMES, on purpose. A provider's catalogue changes weekly
// and a list compiled here would be wrong by the time it shipped; the daemon
// asks the provider what it serves when a key is connected.

// Provider is one model provider a key can be issued by.
type Provider struct {
	// ID is the short name a user types: `/connect nvidia`.
	ID string
	// Name is how the provider is written in a sentence.
	Name string
	// APIBase is its OpenAI-compatible endpoint, without a trailing slash.
	APIBase string
	// Aliases are other names people know it by (`/connect google`).
	Aliases []string
	// KeyPrefixes are prefixes ONLY this provider's keys carry. A key that
	// starts with one is sent to this provider and to no other.
	KeyPrefixes []string
	// SharedKeyPrefix marks a provider whose keys start with the bare "sk-"
	// that several providers use. Such a key names nobody on its own, so it is
	// never sent anywhere on a guess; see ProvidersForKey.
	SharedKeyPrefix bool
}

// ProviderOpenRouter is the ID of the provider models.json's tiers are written
// for, and the only one whose requests carry a routing object.
const ProviderOpenRouter = "openrouter"

// sharedKeyPrefix is the prefix that identifies a key as "an API key" and
// nothing more.
const sharedKeyPrefix = "sk-"

var providers = []Provider{
	{ID: ProviderOpenRouter, Name: "OpenRouter", APIBase: "https://openrouter.ai/api/v1",
		KeyPrefixes: []string{"sk-or-"}},
	{ID: "openai", Name: "OpenAI", APIBase: "https://api.openai.com/v1",
		KeyPrefixes: []string{"sk-proj-", "sk-svcacct-"}, SharedKeyPrefix: true},
	{ID: "anthropic", Name: "Anthropic", APIBase: "https://api.anthropic.com/v1",
		Aliases: []string{"claude"}, KeyPrefixes: []string{"sk-ant-"}},
	{ID: "gemini", Name: "Google Gemini", APIBase: "https://generativelanguage.googleapis.com/v1beta/openai",
		Aliases: []string{"google"}, KeyPrefixes: []string{"AIza"}},
	{ID: "nvidia", Name: "NVIDIA", APIBase: "https://integrate.api.nvidia.com/v1",
		KeyPrefixes: []string{"nvapi-"}},
	{ID: "groq", Name: "Groq", APIBase: "https://api.groq.com/openai/v1",
		KeyPrefixes: []string{"gsk_"}},
	{ID: "deepseek", Name: "DeepSeek", APIBase: "https://api.deepseek.com/v1",
		SharedKeyPrefix: true},
	{ID: "xai", Name: "xAI", APIBase: "https://api.x.ai/v1",
		Aliases: []string{"grok"}, KeyPrefixes: []string{"xai-"}},
	{ID: "mistral", Name: "Mistral", APIBase: "https://api.mistral.ai/v1"},
	{ID: "together", Name: "Together AI", APIBase: "https://api.together.xyz/v1",
		KeyPrefixes: []string{"tgp_v1_"}},
	{ID: "fireworks", Name: "Fireworks AI", APIBase: "https://api.fireworks.ai/inference/v1",
		KeyPrefixes: []string{"fw_"}},
	{ID: "cerebras", Name: "Cerebras", APIBase: "https://api.cerebras.ai/v1",
		KeyPrefixes: []string{"csk-"}},
	{ID: "moonshot", Name: "Moonshot AI", APIBase: "https://api.moonshot.ai/v1",
		Aliases: []string{"kimi"}, SharedKeyPrefix: true},
	{ID: "huggingface", Name: "Hugging Face", APIBase: "https://router.huggingface.co/v1",
		Aliases: []string{"hf"}, KeyPrefixes: []string{"hf_"}},
	{ID: "sambanova", Name: "SambaNova", APIBase: "https://api.sambanova.ai/v1"},
	{ID: "dashscope", Name: "Alibaba Cloud Model Studio", APIBase: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
		Aliases: []string{"alibaba", "qwen"}, SharedKeyPrefix: true},
	{ID: "zai", Name: "Z.ai", APIBase: "https://api.z.ai/api/paas/v4",
		Aliases: []string{"zhipu", "glm"}},
}

// Providers returns every known provider, in the order they are offered.
func Providers() []Provider {
	return append([]Provider(nil), providers...)
}

// ProviderByName finds a provider by its ID or an alias, ignoring case.
func ProviderByName(name string) (Provider, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return Provider{}, false
	}
	for _, p := range providers {
		if p.ID == name {
			return p, true
		}
		for _, alias := range p.Aliases {
			if alias == name {
				return p, true
			}
		}
	}
	return Provider{}, false
}

// ProviderForBase finds the provider an API base belongs to, by host. The path
// is ignored on purpose: "https://api.groq.com/openai/v1" and a hand-typed
// "https://api.groq.com/v1" are both Groq, and treating the second as a
// stranger would send it requests in the wrong shape.
func ProviderForBase(apiBase string) (Provider, bool) {
	host := baseHost(apiBase)
	if host == "" {
		return Provider{}, false
	}
	for _, p := range providers {
		if baseHost(p.APIBase) == host {
			return p, true
		}
	}
	return Provider{}, false
}

func baseHost(apiBase string) string {
	u, err := url.Parse(strings.TrimSpace(apiBase))
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// ProvidersForKey says which provider a key belongs to, going only by the
// prefix providers put on their keys. It never contacts anyone.
//
// Three answers:
//
//   - one provider, certain == true: the key starts with a prefix only that
//     provider uses ("nvapi-", "sk-or-", "sk-ant-", ...).
//
//   - several providers, certain == false: the key starts with the bare "sk-"
//     that OpenAI, DeepSeek and others all use. The caller must ASK which one
//     rather than try them in turn -- trying means handing a working
//     credential to companies that did not issue it.
//
//   - none: the key carries no prefix this recognises (Mistral's and
//     SambaNova's have none). The caller uses the provider already in force,
//     or asks.
//
// The longest matching prefix wins, so "sk-or-..." is OpenRouter's and never
// falls through to the shared "sk-".
func ProvidersForKey(key string) (matches []Provider, certain bool) {
	key = strings.TrimSpace(key)
	best := 0
	var owner Provider
	for _, p := range providers {
		for _, prefix := range p.KeyPrefixes {
			if len(prefix) > best && strings.HasPrefix(key, prefix) {
				best, owner = len(prefix), p
			}
		}
	}
	if best > 0 {
		return []Provider{owner}, true
	}
	if !strings.HasPrefix(key, sharedKeyPrefix) {
		return nil, false
	}
	for _, p := range providers {
		if p.SharedKeyPrefix {
			matches = append(matches, p)
		}
	}
	return matches, false
}
