package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"mochiii/protocol"
)

// HOW A REQUEST IS WORDED FOR THE PROVIDER IT IS GOING TO.
//
// Until 2026-10-05 there was one wording, OpenRouter's, sent to every address:
// a "provider" routing object on every request, a "reasoning" object, and
// "max_tokens". OpenRouter and the managed proxy need exactly that. Most other
// providers refuse it -- OpenAI answers 400 to a field it does not know, and
// its newer models answer 400 to "max_tokens" itself -- so a key from anyone
// but OpenRouter could be stored and could never complete a prompt.
//
// TWO DIALECTS, AND THE OLD ONE IS STILL THE DEFAULT. The routing dialect goes
// to OpenRouter, to the managed proxy, and to every address this does not
// recognise (a local model server, a company gateway): those got it before and
// their request body is byte-for-byte what it was. The plain dialect goes only
// to a provider protocol.ProviderForBase knows by name and knows is not
// OpenRouter.
//
// THE ROUTING OBJECT IS NEVER DROPPED FROM A REQUEST THAT USED TO CARRY IT. It
// is what enforces zero-data-retention, and providerRouting's rule that its
// flags are always on the wire stands for every address that honours them. A
// provider in the plain dialect has no such routing to ask for; degraded.go
// reports that to the user instead of leaving it implied.

// requestDialect is how one provider wants a chat completion asked for.
type requestDialect struct {
	// routing sends OpenRouter's "provider" and "reasoning" objects.
	routing bool
	// streamOptions asks for the usage chunk at the end of the stream.
	streamOptions bool
	// maxTokensField names the output cap: fieldMaxTokens,
	// fieldMaxCompletionTokens, or "" to send no cap at all.
	maxTokensField string
	// maxTokens overrides the tier's cap once a provider has refused it as too
	// large. Zero means the tier's own.
	maxTokens int
	// noReasoningEffort stops sending reasoning_effort once a provider has
	// refused it for this model; the turn then runs at the model's default.
	noReasoningEffort bool
}

const (
	fieldMaxTokens           = "max_tokens"
	fieldMaxCompletionTokens = "max_completion_tokens"
)

// providerForBase is protocol.ProviderForBase. A var so a test can stand a
// loopback server in for a named provider; nothing else may reassign it.
var providerForBase = protocol.ProviderForBase

// usesRoutingDialect reports whether requests to apiBase carry OpenRouter's
// routing object -- true for everything except a named provider that is not
// OpenRouter. Proxy mode is decided first and by the daemon's own setting, not
// by the address: the proxy refuses a request without the object (403
// zdr_required), wherever it happens to be hosted.
func usesRoutingDialect(apiBase string) bool {
	if os.Getenv("MOCHIII_USE_PROXY") == "true" {
		return true
	}
	p, known := providerForBase(apiBase)
	return !known || p.ID == protocol.ProviderOpenRouter
}

// dialectMemo remembers, per provider address and model, the dialect a request
// last SUCCEEDED with after having to be adapted -- so a model that wants
// max_completion_tokens is asked the wrong way once per daemon, not once per
// call. Only successes are stored: a chain of adaptations that still ended in
// an error proved nothing about what the provider wants.
var dialectMemo sync.Map

func dialectKey(apiBase, model string) string {
	return strings.TrimRight(apiBase, "/") + "\x00" + model
}

// dialectFor is the dialect to open a request to model at apiBase with.
func dialectFor(apiBase, model string) requestDialect {
	if usesRoutingDialect(apiBase) {
		return requestDialect{routing: true}
	}
	if d, ok := dialectMemo.Load(dialectKey(apiBase, model)); ok {
		return d.(requestDialect)
	}
	return requestDialect{streamOptions: true, maxTokensField: fieldMaxTokens}
}

// plainChatRequest is the OpenAI chat-completions body with nothing added: the
// fields every compatible provider documents, and none that only one of them
// understands.
type plainChatRequest struct {
	Model               string         `json:"model"`
	Messages            []chatMessage  `json:"messages"`
	Tools               []toolSpec     `json:"tools,omitempty"`
	Stream              bool           `json:"stream"`
	StreamOptions       *streamOptions `json:"stream_options,omitempty"`
	MaxTokens           int            `json:"max_tokens,omitempty"`
	MaxCompletionTokens int            `json:"max_completion_tokens,omitempty"`
	// ReasoningEffort is the OpenAI-defined top-level field, not OpenRouter's
	// "reasoning" object: Groq and OpenAI read this one. Sent only when a tier or
	// the turn asks for an effort, so a request without one is unchanged.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// outputCap is the cap this dialect sends for a tier whose own cap is tierCap.
func (d requestDialect) outputCap(tierCap int) int {
	if d.maxTokens > 0 {
		return d.maxTokens
	}
	return tierCap
}

// body serializes one request in this dialect.
func (d requestDialect) body(model string, messages []chatMessage, tools []toolSpec, routing providerRouting) ([]byte, error) {
	if d.routing {
		var reasoning *reasoningParam
		if routing.reasoningEffort != "" {
			reasoning = &reasoningParam{Effort: routing.reasoningEffort}
		}
		return json.Marshal(chatCompletionRequest{
			Model:         model,
			Messages:      messages,
			Tools:         tools,
			Stream:        true,
			Provider:      routing,
			StreamOptions: streamOptions{IncludeUsage: true},
			Reasoning:     reasoning,
			MaxTokens:     routing.maxTokens,
		})
	}
	req := plainChatRequest{Model: model, Messages: messages, Tools: tools, Stream: true}
	if !d.noReasoningEffort {
		req.ReasoningEffort = routing.reasoningEffort
	}
	if d.streamOptions {
		req.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	switch d.maxTokensField {
	case fieldMaxTokens:
		req.MaxTokens = d.outputCap(routing.maxTokens)
	case fieldMaxCompletionTokens:
		req.MaxCompletionTokens = d.outputCap(routing.maxTokens)
	}
	return json.Marshal(req)
}

// outputCapLadder is what the cap is stepped down through when a provider says
// the one it was sent is too large. Many models allow less than
// defaultMaxOutputTokens, and one with a small context window refuses the
// request as too long for the cap alone, before a word of prompt is counted.
var outputCapLadder = []int{32768, 16384, 8192, 4096}

// outputLimitPhrases are how providers name the output cap when they refuse it.
var outputLimitPhrases = []string{
	"max_tokens",
	"max_completion_tokens",
	"max output",
	"maximum output",
	"output token",
	"completion token",
}

// maxDialectAttempts bounds the adaptations one call may make. Every one is a
// request the provider refused before generating anything, so they cost time
// and nothing else; the bound is what keeps a provider that refuses everything
// from being asked forever.
const maxDialectAttempts = 7

// adapt returns the dialect to ask again with after the provider refused a
// request, and whether there is anything left to change.
//
// IT READS THE PROVIDER'S OWN WORDS RATHER THAN A TABLE OF WHO WANTS WHAT. A
// table would have to be right about every provider and every model, today and
// after their next release, and it was written by someone who could test none
// of them with a real key. The refusal names the field it objects to, so the
// field is changed and the request is asked again -- at most once per field,
// and the result is remembered (dialectMemo) only if it then succeeds.
//
// The routing dialect is never adapted: nothing may be taken out of a request
// to OpenRouter or the proxy because a response said so.
func (d requestDialect) adapt(status int, body string, tierCap int) (requestDialect, bool) {
	if d.routing {
		return d, false
	}
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusRequestEntityTooLarge:
	default:
		return d, false
	}
	lower := strings.ToLower(body)

	// "Unsupported parameter: 'max_tokens' ... use 'max_completion_tokens'".
	if d.maxTokensField == fieldMaxTokens && strings.Contains(lower, fieldMaxCompletionTokens) {
		d.maxTokensField = fieldMaxCompletionTokens
		return d, true
	}
	if d.streamOptions && strings.Contains(lower, "stream_options") {
		d.streamOptions = false
		return d, true
	}
	// "`reasoning_effort` must be one of ..." or "Extra inputs are not
	// permitted: reasoning_effort". The effort is a preference; the answer is not.
	if !d.noReasoningEffort && (strings.Contains(lower, "reasoning_effort") || strings.Contains(lower, "reasoning effort")) {
		d.noReasoningEffort = true
		return d, true
	}
	if d.maxTokensField != "" && (containsAny(lower, outputLimitPhrases) || containsAny(lower, contextLengthPhrases)) {
		current := d.outputCap(tierCap)
		for _, step := range outputCapLadder {
			if step < current {
				d.maxTokens = step
				return d, true
			}
		}
		// Refused at the smallest cap worth sending: the provider's own default
		// is the only one left to try.
		d.maxTokensField, d.maxTokens = "", 0
		return d, true
	}
	return d, false
}

// openCompletion sends one chat completion and returns the provider's response
// once it has ACCEPTED the request (HTTP 200), with the body still to be read.
// Any other outcome is returned as a classified error.
//
// wrapTransport turns a failure to get a response at all into the caller's
// error; streamCompletion uses it to tell a stall from a refused connection.
func openCompletion(ctx context.Context, apiBase, apiKey, model string, messages []chatMessage, tools []toolSpec, routing providerRouting, wrapTransport func(error) error) (*http.Response, error) {
	url := strings.TrimRight(apiBase, "/") + chatCompletionsPath
	dialect := dialectFor(apiBase, model)
	adapted := false

	for attempt := 1; ; attempt++ {
		reqBody, err := dialect.body(model, messages, tools, routing)
		if err != nil {
			return nil, fmt.Errorf("encoding request: %w", err)
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
		if err != nil {
			return nil, fmt.Errorf("building request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		if apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		}

		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			return nil, wrapTransport(err)
		}
		if resp.StatusCode == http.StatusOK {
			if adapted {
				dialectMemo.Store(dialectKey(apiBase, model), dialect)
			}
			return resp, nil
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		bodyStr := strings.TrimSpace(string(body))

		if attempt < maxDialectAttempts {
			if next, changed := dialect.adapt(resp.StatusCode, bodyStr, routing.maxTokens); changed {
				dialect, adapted = next, true
				continue
			}
		}

		// Classified here, at the only place that can see the status, the body
		// and the headers together (Fix 9). The ZDR refusal keeps its identity:
		// ModelError.Unwrap returns ErrZDRRefused for that class, so existing
		// errors.Is checks are unaffected.
		modelErr := classifyHTTPError(resp.StatusCode, resp.Status, bodyStr).withModelName(model)
		modelErr.RetryAfter = parseRetryAfter(resp.Header)
		// The managed proxy answers every request with an X-Request-Id, including
		// the body-less 500 a contained panic produces. Carrying it into the
		// operator detail is what turns "a user says it failed at about 3pm" into
		// one id that resolves to one request's trail in the proxy's log.
		return nil, modelErr.withUpstreamRequestID(upstreamRequestID(resp.Header))
	}
}
