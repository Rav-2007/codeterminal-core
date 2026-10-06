package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// WHICH MODELS A PROVIDER SERVES, ASKED OF THE PROVIDER.
//
// models.json names OpenRouter's models. Every other provider calls its models
// something else, so a key for one of them used to connect and then fail every
// prompt with "unknown model". Nothing compiled into this binary could fix
// that -- a list of other providers' model names would be stale on the day it
// shipped -- so the list is fetched from GET {base}/models when a key is
// connected, stored beside the key, and offered through /model exactly as
// models.json's tiers are.

// listedModel is one entry of a provider's model list.
type listedModel struct {
	ID string `json:"id"`
	// Type is set by providers that say what a model is for (Together:
	// "chat", "embedding", "image", ...). Empty almost everywhere.
	Type string `json:"type"`
}

// maxModelListBytes bounds a /models response. Larger than the other caps here
// because the big aggregators' lists run to megabytes; bounded for the same
// reason every network read here is.
const maxModelListBytes = 8 << 20

// listModels asks a provider what it serves. It returns the HTTP status with
// whatever it could parse, so the caller can tell "the key was refused" (401,
// 403, or a 400 that says so) from "this address has no model list".
func listModels(ctx context.Context, client *http.Client, apiBase, apiKey string) (models []listedModel, status int, body string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(apiBase, "/")+"/models", nil)
	if err != nil {
		return nil, 0, "", err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxModelListBytes))
	if err != nil {
		return nil, resp.StatusCode, "", err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, strings.TrimSpace(string(raw[:min(len(raw), 512)])), nil
	}

	// Two shapes are in use: the OpenAI one, {"data":[{"id":...}]}, and a bare
	// array of the same objects. Anything else is a list this cannot read, which
	// is reported as no list rather than as an error -- the key may be fine.
	var wrapped struct {
		Data []listedModel `json:"data"`
	}
	if json.Unmarshal(raw, &wrapped) == nil && len(wrapped.Data) > 0 {
		return wrapped.Data, resp.StatusCode, "", nil
	}
	var bare []listedModel
	if json.Unmarshal(raw, &bare) == nil {
		return bare, resp.StatusCode, "", nil
	}
	return nil, resp.StatusCode, "", nil
}

// modelTokens splits a model id into the words it is made of:
// "deepseek-ai/deepseek-v4.1-flash" -> deepseek, ai, deepseek, v4.1, flash.
// Everything below matches on these, never on substrings -- "mini" is a word in
// "gpt-5-mini" and merely four letters of "gemini" and "minimax".
func modelTokens(id string) []string {
	return strings.FieldsFunc(strings.ToLower(id), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '.'
	})
}

// notChatWords mark a model that does not hold a conversation: embedders,
// rerankers, safety classifiers, speech and image models. Offering one through
// /model would offer a failure.
var notChatWords = map[string]bool{
	"embed": true, "embedding": true, "embeddings": true, "embedqa": true, "rerank": true, "reranker": true,
	"guard": true, "nemoguard": true, "safety": true, "moderation": true, "shield": true,
	"whisper": true, "tts": true, "speech": true, "transcribe": true, "audio": true, "realtime": true,
	"image": true, "imagen": true, "dall": true, "sora": true, "veo": true, "video": true,
	"clip": true, "nvclip": true, "parse": true, "detector": true, "translate": true,
	"reward": true, "calibration": true, "deplot": true, "babbage": true, "davinci": true,
}

// chatTypes are the Type values that mean "a conversation model".
var chatTypes = map[string]bool{"": true, "chat": true, "language": true, "text": true, "model": true}

// chatModelIDs filters a provider's list down to the models a prompt can be
// sent to, sorted, without duplicates. Gemini lists its models as "models/<id>"
// and takes them without the prefix, so that prefix is dropped.
func chatModelIDs(models []listedModel) []string {
	seen := map[string]bool{}
	var ids []string
next:
	for _, m := range models {
		id := strings.TrimPrefix(strings.TrimSpace(m.ID), "models/")
		if id == "" || seen[id] || !chatTypes[strings.ToLower(m.Type)] || !plainModelID(id) {
			continue
		}
		for _, word := range modelTokens(id) {
			if notChatWords[word] {
				continue next
			}
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// maxModelIDLen is longer than any real model id (the longest measured is
// under 70 characters) and short enough to be a row of a list.
const maxModelIDLen = 128

// plainModelID reports whether id is something a provider would really call a
// model: printable ASCII with no spaces, of a sane length.
//
// THE LIST IS TEXT A REMOTE SERVER WROTE, and every id in it becomes a tier
// name -- shown by /model, written to the daemon's log and to
// credentials.json, and sent back as the "model" of every request. An id
// holding a line break forged a line in the log, one holding an escape
// sequence was stored to be drawn on a terminal, and one of 5,000 characters
// was a row (FOUND 2026-10-06 against a stand-in provider). No provider's real
// ids contain any of that, so an entry that does is left out, not cleaned up:
// a cleaned-up name would be a model the provider does not have.
func plainModelID(id string) bool {
	if len(id) > maxModelIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] <= ' ' || id[i] > '~' {
			return false
		}
	}
	return true
}

// modelFamilies orders the model families a default is picked from, best first
// for this product's work. deepseek leads because it is the one this
// repository's own task evals chose as the default; the rest are the families
// the major providers serve. THIS ORDERS CANDIDATES AND DECIDES NOTHING ON ITS
// OWN -- a candidate becomes the default only by answering a real request (see
// setUpProvider), and /model changes it.
var modelFamilies = []string{
	"deepseek", "claude", "gpt", "gemini", "kimi", "glm", "qwen", "grok",
	"minimax", "laguna", "mistral", "codestral", "devstral", "llama", "nemotron",
}

// demotedWords push a model behind its siblings as a DEFAULT: the small and
// cut-down variants, the previews, and the ones priced or built for something
// other than everyday coding. They stay selectable.
var demotedWords = map[string]bool{
	"mini": true, "nano": true, "lite": true, "tiny": true, "small": true, "xs": true, "instant": true, "lightning": true,
	"haiku": true, "opus": true, "oss": true, "preview": true, "exp": true, "experimental": true,
	"vision": true, "search": true, "coder": true, "turbo": true, "legacy": true, "distill": true,
}

var (
	paramCountRe = regexp.MustCompile(`^(\d+(?:\.\d+)?)b$`)
	versionRe    = regexp.MustCompile(`^[a-z]*?(\d+(?:\.\d+)?)[a-z]?$`)
)

// modelRank is what candidates are ordered by.
type modelRank struct {
	demotions int
	family    int
	version   []float64
	// params is the largest parameter count the name states, in billions
	// ("550b"). Zero when it states none.
	params float64
}

func rankModel(id string) modelRank {
	rank := modelRank{family: len(modelFamilies)}
	tokens := modelTokens(id)
	for _, tok := range tokens {
		if demotedWords[tok] {
			rank.demotions++
		}
		// A parameter count ("8b", "70b") is a size, not a version; under 20
		// billion is the small end of what these providers host.
		if m := paramCountRe.FindStringSubmatch(tok); m != nil {
			if n, err := strconv.ParseFloat(m[1], 64); err == nil {
				if n < 20 {
					rank.demotions++
				}
				rank.params = max(rank.params, n)
			}
			continue
		}
		if strings.HasSuffix(tok, "k") {
			continue // a context size: "32k"
		}
		if m := versionRe.FindStringSubmatch(tok); m != nil {
			// Three digits and up is a date or a build number, not a version.
			if n, err := strconv.ParseFloat(m[1], 64); err == nil && n < 100 {
				rank.version = append(rank.version, n)
			}
		}
	}
	for i, family := range modelFamilies {
		for _, tok := range tokens {
			if strings.HasPrefix(tok, family) {
				rank.family = min(rank.family, i)
			}
		}
	}
	// "gpt-5.5-pro" is priced far above "gpt-5.5"; elsewhere "pro" is simply the
	// better model ("deepseek-v4-pro"), so this one is a rule about one family.
	if rank.family < len(modelFamilies) && modelFamilies[rank.family] == "gpt" {
		for _, tok := range tokens {
			if tok == "pro" {
				rank.demotions++
			}
		}
	}
	return rank
}

// newerVersion reports whether a is a later version than b.
func newerVersion(a, b []float64) (newer, older bool) {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] > b[i], a[i] < b[i]
		}
	}
	return len(a) > len(b), len(a) < len(b)
}

// rankModels orders chat models as candidates for the default: undemoted
// before demoted, then by family, then newest version, then the larger model
// when both names state a size, then the plainest name.
func rankModels(ids []string) []string {
	ranked := append([]string(nil), ids...)
	ranks := make(map[string]modelRank, len(ranked))
	for _, id := range ranked {
		ranks[id] = rankModel(id)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranks[ranked[i]], ranks[ranked[j]]
		if a.demotions != b.demotions {
			return a.demotions < b.demotions
		}
		if a.family != b.family {
			return a.family < b.family
		}
		if newer, older := newerVersion(a.version, b.version); newer || older {
			return newer
		}
		// Same family, same version: the bigger model when the names say which.
		if a.params != b.params && a.params > 0 && b.params > 0 {
			return a.params > b.params
		}
		if len(ranked[i]) != len(ranked[j]) {
			return len(ranked[i]) < len(ranked[j])
		}
		return ranked[i] > ranked[j]
	})
	return ranked
}

// probeTool is the one tool a test request advertises. Agent mode cannot run on
// a model that refuses tools, and the only way to learn that a model refuses
// them is to offer one.
var probeTool = toolSpec{Type: "function", Function: toolSpecFunction{
	Name:        "noop",
	Description: "Does nothing. Never call it.",
	// One optional property rather than none: some providers refuse a tool
	// whose parameters are an object with no properties, and a tool refused for
	// its own shape would be read as "this model cannot use tools".
	Parameters: json.RawMessage(`{"type":"object","properties":{"note":{"type":"string","description":"Unused."}}}`),
}}

// probeCompletion sends model one real, tiny request and reports whether the
// provider began answering it. It reads the stream only as far as the first
// event and then hangs up, so what it costs is a handful of tokens.
//
// THIS IS THE ONLY CHECK THAT PROVES ANYTHING ABOUT A PROVIDER THAT IS NOT
// OPENROUTER. Measured 2026-10-05: NVIDIA has no endpoint that describes a key
// and serves its model list to any key at all, so every key "verified" there
// as unprovable -- including a typo. A request the provider actually answers
// proves the key, the model name, and that the provider accepts the request as
// this daemon words it, all at once.
func probeCompletion(ctx context.Context, apiBase, apiKey, model string, routing providerRouting, withTool bool) error {
	var tools []toolSpec
	if withTool {
		tools = []toolSpec{probeTool}
	}
	messages := []chatMessage{{Role: "user", Content: "Reply with the single word: ok"}}
	resp, err := openCompletion(ctx, apiBase, apiKey, model, messages, tools, routing, func(err error) error {
		return classifyTransportError(err)
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	// A 200 is not yet an answer: some providers accept the request and report
	// the failure as the first event of the stream.
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, sseInitialBufferSize), sseMaxLineSize)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(strings.TrimSpace(scanner.Text()), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			return nil
		}
		var event struct {
			Error   json.RawMessage   `json:"error"`
			Choices []json.RawMessage `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &event) != nil {
			continue
		}
		if len(event.Error) > 0 && string(event.Error) != "null" && len(event.Choices) == 0 {
			return classifyHTTPError(http.StatusBadGateway, "200 OK, then an error in the stream", string(event.Error)).withModelName(model)
		}
		return nil
	}
	if err := scanner.Err(); err != nil {
		return classifyTransportError(err)
	}
	return errors.New("the provider accepted the request and then sent nothing")
}

// providerTierNote is the note every tier built from a provider's own list
// carries, so /model says where the entry came from.
func providerTierNote(providerName string) string {
	return "served by " + providerName
}

// providerTierConfig is cfg with its tiers replaced by a provider's own
// models: one tier per model, named by the model's id, so `/model <id>` selects
// it. Everything else in cfg -- agent mode, retrieval, scrubbing -- is
// unchanged, because none of it is about which model answers.
//
// Tiers built here carry no context_window or reasoning_effort: the provider's
// list says neither, and a guess would be reported by /usage as fact.
func providerTierConfig(cfg *Config, providerName string, models, quiet []string, defaultModel string) *Config {
	if cfg == nil || len(models) == 0 || defaultModel == "" {
		return nil
	}
	out := *cfg
	out.Tiers = make(map[string]ModelTier, len(models)+1)
	note := providerTierNote(providerName)
	for _, id := range models {
		out.Tiers[id] = ModelTier{Slug: id, Active: true, Note: note}
	}
	// LISTED IS NOT THE SAME AS SERVED. Measured 2026-10-05 with a real NVIDIA
	// key: of the models its list offers, most either answered 404 for the
	// account or accepted the request and never sent a byte. The second kind
	// stays selectable -- it may answer another day -- and says what happened.
	for _, id := range quiet {
		if tier, listed := out.Tiers[id]; listed {
			tier.Note = "listed by " + providerName + quietTierNote
			out.Tiers[id] = tier
		}
	}
	if _, listed := out.Tiers[defaultModel]; !listed {
		out.Tiers[defaultModel] = ModelTier{Slug: defaultModel, Active: true, Note: note}
	}
	out.DefaultTier = defaultModel
	return &out
}

// quietTierNote ends the note of a tier the provider lists and that sent
// nothing back when the key was connected.
const quietTierNote = ", but it did not answer when the key was connected"

// quietModelHint is what to add to a failed turn's message when the model it
// ran on is one of those.
//
// MEASURED 2026-10-06 with a real key: a prompt to such a model waited 120.6
// seconds and then said "the model provider is unreachable or failing right
// now -- this is usually temporary". Neither half was true. The provider was
// answering other models in under a second, and this one has never answered
// this account; the daemon had written that down the day the key was
// connected and did not say it at the one moment it explained everything. The
// wait is the stall watchdog's and is unchanged -- a model slow to its first
// word is not a dead one -- but the user is no longer told to try again.
func (s *Server) quietModelHint(model string, class ModelErrorClass) string {
	if class != ClassUpstreamUnavailable {
		return ""
	}
	cfg := s.tierConfig()
	if cfg == nil {
		return ""
	}
	if tier, ok := cfg.Tiers[model]; !ok || !strings.HasSuffix(tier.Note, quietTierNote) {
		return ""
	}
	return ". But " + model + " also sent nothing back when the key was connected: it is on the provider's list " +
		"and does not answer this account, so waiting will not help. /model picks another"
}

// providerName is how a base is named to a person: the provider's name when it
// is a known one, otherwise the address itself.
func providerName(apiBase string) string {
	if p, ok := providerForBase(apiBase); ok {
		return p.Name
	}
	return apiBase
}

// describeProbeFailure turns a failed test request into the half sentence a
// connect result quotes.
func describeProbeFailure(model string, err error) string {
	var me *ModelError
	if errors.As(err, &me) {
		if me.status != 0 {
			return fmt.Sprintf("%s: %s (HTTP %d)", model, me.Class, me.status)
		}
		return fmt.Sprintf("%s: %s", model, me.Class)
	}
	return fmt.Sprintf("%s: %v", model, err)
}
