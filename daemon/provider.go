package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// requestTimeout bounds a single prompt's total call to the model API so a
// stalled upstream can't hang a connection (and its goroutine) forever.
const requestTimeout = 5 * time.Minute

// chatCompletionsPath is appended to CODETERMINAL_API_BASE for every call.
const chatCompletionsPath = "/chat/completions"

// SSE lines are almost always small; the scanner starts with a modest
// buffer and grows up to sseMaxLineSize only if a provider sends an
// unusually large single chunk.
const (
	sseInitialBufferSize = 64 * 1024
	sseMaxLineSize       = 1024 * 1024
)

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// providerRouting is OpenRouter's "provider" request object, restricted to
// the three fields this codebase enforces. See ZDRConfig.resolvedProviderRouting
// in config.go for how these values are resolved (secure-by-default) and
// ErrZDRRefused below for the failure mode when no provider qualifies. No
// field here is omitempty: every request must state all three explicitly,
// on purpose, so enforcement is never silently absent from the wire body.
type providerRouting struct {
	ZDR            bool   `json:"zdr"`
	DataCollection string `json:"data_collection"`
	AllowFallbacks bool   `json:"allow_fallbacks"`
}

// streamOptions is OpenRouter's "stream_options" request object. Setting
// IncludeUsage is what makes OpenRouter emit a final SSE chunk carrying
// token-usage counts; without it, streamed responses never include usage at
// all. Consumed by the managed proxy for per-key metering (see
// proxy/main.go's extractUsage) -- this daemon does not itself read the
// usage chunk, it only requests it so the proxy sitting downstream can.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatCompletionRequest struct {
	Model         string          `json:"model"`
	Messages      []chatMessage   `json:"messages"`
	Stream        bool            `json:"stream"`
	Provider      providerRouting `json:"provider"`
	StreamOptions streamOptions   `json:"stream_options"`
}

type chatCompletionChunk struct {
	// Provider names the upstream provider that actually served this chunk,
	// when OpenRouter includes it (observed in practice, not formally
	// guaranteed on every chunk by OpenRouter's docs) — captured purely for
	// observability (see streamCompletion's onProvider callback). Never
	// used to gate or retry a request: provider.zdr/data_collection above
	// are what OpenRouter itself filters routing by, before this response
	// ever exists.
	Provider string `json:"provider,omitempty"`
	Choices  []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// ErrZDRRefused is the sentinel error streamCompletion returns when
// OpenRouter refuses a request because no provider satisfies the
// provider-routing constraints (e.g. zdr:true + data_collection:"deny" +
// allow_fallbacks:false excludes every available endpoint). Callers can
// distinguish this from an ordinary outage via errors.Is(err, ErrZDRRefused)
// and surface a privacy-specific message instead of a generic failure (see
// server.go's handlePrompt).
var ErrZDRRefused = errors.New("no provider satisfies the configured zero-data-retention routing constraints")

// zdrRefusalSubstrings are known (as of this writing) OpenRouter error-body
// phrasings for "no provider matches your routing constraints". OpenRouter
// does not document a single stable machine-readable code that's distinct
// from an ordinary provider outage (both can surface under similar-shaped
// errors), so this is deliberately a best-effort text match, not a
// guaranteed-correct signal — reactive, not proactive, same as the
// stable-substring match already used for pruned-backup detection in
// runUndoSession. If none of these phrases match, the real upstream error
// still reaches the caller unprefixed (see streamCompletion) — never
// swallowed, just without the friendlier ErrZDRRefused label.
//
// "zero data retention" was added after a live-induced refusal on
// 2026-07-09 (routing a real request at a model with zero ZDR-compliant
// providers) came back as `"No endpoints found matching your data policy
// (Zero data retention). Configure: https://openrouter.ai/settings/privacy"`
// — a third phrasing distinct from the two below, neither of which matched
// it. Deliberately "zero data retention" rather than the full observed
// sentence (too brittle against OpenRouter rewording the surrounding
// prose) or the shorter "data policy" (a generic-enough phrase it could
// plausibly appear in an unrelated policy/moderation message); "zero data
// retention" is the feature's own name, essentially guaranteed to appear
// verbatim in any refusal actually caused by this constraint and absent
// from rate-limit/auth/outage error text.
var zdrRefusalSubstrings = []string{
	"no allowed providers",
	"no available model provider",
	"zero data retention",
}

// isZDRRoutingRefusal reports whether body (an error response body from the
// model API) looks like OpenRouter refusing a request for lack of a
// qualifying provider, as opposed to an unrelated error (auth, rate limit,
// bad request, generic outage).
func isZDRRoutingRefusal(body string) bool {
	lower := strings.ToLower(body)
	for _, s := range zdrRefusalSubstrings {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// buildChatMessages assembles the message list sent to the model: an
// optional leading "system" message, then history (already validated and
// capped by prepareHistory, oldest first, roles "user"/"assistant" only),
// then exactly one final "user" message. It is extracted from
// streamCompletion so tests can assert directly on the constructed request
// structure — in particular, that retrieved context (folded into prompt by
// the caller, see buildAugmentedUserMessage) always lands in the final
// "user" message and never in "system", and that history turns never land
// in "system" either regardless of what a client sent.
func buildChatMessages(systemPrompt string, history []chatMessage, prompt string) []chatMessage {
	var messages []chatMessage
	if systemPrompt != "" {
		messages = append(messages, chatMessage{Role: "system", Content: systemPrompt})
	}
	messages = append(messages, history...)
	messages = append(messages, chatMessage{Role: "user", Content: prompt})
	return messages
}

// streamCompletion calls an OpenAI-compatible POST {apiBase}/chat/completions
// endpoint with stream=true and invokes onToken for each content fragment as
// it arrives over the SSE response. It never buffers the full reply.
// systemPrompt, if non-empty, is sent as the leading "system" message.
// history carries prior conversation turns (already validated/capped by the
// caller via prepareHistory), inserted between the system message and the
// final prompt message. routing is sent as the request's "provider" object
// on every call, never optional (see providerRouting's doc comment).
// onProvider, if non-nil, is invoked at most once with the upstream
// provider name as soon as it's observed in the response stream (purely for
// observability — see chatCompletionChunk.Provider's doc comment).
func streamCompletion(ctx context.Context, apiBase, apiKey, model, systemPrompt string, history []chatMessage, prompt string, routing providerRouting, onToken func(string) error, onProvider func(string)) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	messages := buildChatMessages(systemPrompt, history, prompt)

	reqBody, err := json.Marshal(chatCompletionRequest{
		Model:         model,
		Messages:      messages,
		Stream:        true,
		Provider:      routing,
		StreamOptions: streamOptions{IncludeUsage: true},
	})
	if err != nil {
		return fmt.Errorf("encoding request: %w", err)
	}

	url := strings.TrimRight(apiBase, "/") + chatCompletionsPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("calling model API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		bodyStr := strings.TrimSpace(string(body))
		if isZDRRoutingRefusal(bodyStr) {
			return fmt.Errorf("%w (model API returned %s: %s)", ErrZDRRefused, resp.Status, bodyStr)
		}
		return fmt.Errorf("model API returned %s: %s", resp.Status, bodyStr)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, sseInitialBufferSize), sseMaxLineSize)

	providerSeen := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return nil
		}

		var chunk chatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // skip malformed / keep-alive lines
		}
		if !providerSeen && chunk.Provider != "" {
			providerSeen = true
			if onProvider != nil {
				onProvider(chunk.Provider)
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		content := chunk.Choices[0].Delta.Content
		if content == "" {
			continue
		}
		if err := onToken(content); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading stream: %w", err)
	}
	return nil
}
