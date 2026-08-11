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
	"sort"
	"strings"
	"time"

	"codeterminal/protocol"
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

// chatMessage is one message in the model conversation.
//
// The three tool fields are ALL omitempty, and that is load-bearing rather than
// tidy: with agent mode off none of them is ever set, so the serialized request
// body is byte-identical to the one whose ZDR routing was verified live on
// 2026-07-27. TestRequestBodyIsByteIdenticalWithoutTools pins that.
//
// NOTE: this struct is NOT ==-comparable -- ToolCalls is a slice. Compare
// values with reflect.DeepEqual, not == / != (which is a compile error, not a
// test failure). Same footgun providerRouting below already carries.
//
// Role is "system", "user", "assistant" or -- new here -- "tool". Note that a
// "tool" role NEVER reaches protocol.Turn or conversation memory: validTurn
// (history.go) rejects any role but user/assistant, deliberately, as an
// injection defense. Tool messages live only in the in-memory slice for the
// duration of one turn.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls is set on an ASSISTANT message when the model asked to call
	// tools. Echoed back verbatim on the next request so the provider can pair
	// each result with the call that produced it.
	ToolCalls []toolCall `json:"tool_calls,omitempty"`
	// ToolCallID is set on a TOOL message, naming which call this result
	// answers. The pairing is the provider's, not ours: getting it wrong makes
	// the model read one tool's output as another's.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Name carries the tool name on a tool message. Optional in the OpenAI
	// schema, sent because it costs almost nothing and makes a captured request
	// body readable by a human debugging a loop.
	Name string `json:"name,omitempty"`
	// CacheControl marks a message for provider-side prompt caching (e.g. Anthropic
	// ephemeral caching via OpenRouter).
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
}

// toolCall is one tool invocation the model requested, rebuilt from its
// streamed fragments (see toolCallAccumulator).
type toolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // always "function"
	Function toolCallFunction `json:"function"`
}

type toolCallFunction struct {
	Name string `json:"name"`
	// Arguments is a JSON *string* containing a JSON object -- the provider's
	// encoding, not ours. It is never parsed here: the daemon passes it to the
	// tool layer, which decides whether it is usable, so a malformed argument
	// blob becomes a tool error the model can correct rather than a stream
	// failure that ends the turn.
	Arguments string `json:"arguments"`
}

// toolSpec advertises one tool to the model, in the OpenAI-compatible shape the
// proxy forwards byte-for-byte. Verified live on 2026-07-31: the proxy's
// costSurfaceRefusal does not deny-list "tools", and zdrRoutingEnforced reads
// only provider.zdr/data_collection, so this passes the managed tier unchanged.
type toolSpec struct {
	Type     string           `json:"type"` // always "function"
	Function toolSpecFunction `json:"function"`
}

type toolSpecFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// providerRouting is OpenRouter's "provider" request object. See
// ZDRConfig.resolvedProviderRouting in config.go for how these values are
// resolved (secure-by-default) and ErrZDRRefused below for the failure mode
// when no provider qualifies. The three ZDR flags are never omitempty: every
// request must state all three explicitly, on purpose, so enforcement is never
// silently absent from the wire body (Ignore is the exception -- see its note).
//
// NOTE: this struct is NOT ==-comparable -- Ignore is a slice. Compare values
// with reflect.DeepEqual, not == / != (which is a compile error, not a test
// failure).
type providerRouting struct {
	ZDR            bool   `json:"zdr"`
	DataCollection string `json:"data_collection"`
	AllowFallbacks bool   `json:"allow_fallbacks"`
	// Ignore is a deny-list of provider names OpenRouter must NOT route to
	// (its "provider.ignore" field), used to exclude a specific provider from
	// the ZDR-eligible pool while keeping every other provider available (D4:
	// excluding DeepInfra pending the OpenRouter retention finding). Deny-list
	// rather than an "only" allow-list on purpose — it keeps the pool maximally
	// wide (only the named provider drops out, so it does not re-introduce the
	// congestion allow_fallbacks was flipped on to escape) and is self-
	// maintaining as OpenRouter's ZDR provider set changes. It is the ONE field
	// here that IS omitempty: an unset list must serialize to exactly today's
	// wire body, so this change is inert until models.json opts in.
	Ignore []string `json:"ignore,omitempty"`
	// Order enforces a strict preference list of providers.
	Order []string `json:"order,omitempty"`
	// Sort specifies the provider property to sort by ("price" or "throughput").
	Sort string `json:"sort,omitempty"`
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
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	// Tools is omitempty so a turn with no tools serializes exactly as it did
	// before this field existed. See chatMessage's note on why that matters.
	Tools         []toolSpec      `json:"tools,omitempty"`
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
			// Reasoning carries a reasoning-tier model's thinking tokens,
			// which OpenRouter streams in a field of their own alongside
			// content (Fix 14). The stream used to read delta.content only,
			// so every reasoning token was decoded and thrown away: the user
			// watched an empty screen for as long as the model thought (907ms
			// of dead air observed) and then got the answer in one burst. It
			// is deliberately NOT folded into content — see streamCompletion's
			// onReasoning — because thinking is not part of the answer and
			// must not reach edit-block parsing or conversation memory.
			Reasoning string `json:"reasoning"`
			// ToolCalls arrives as FRAGMENTS, not whole calls: the provider
			// sends id and function.name once on the first fragment for a given
			// index, then function.arguments in pieces that are only valid JSON
			// once concatenated. Any attempt to parse a single fragment fails on
			// every one but the last. Rebuilt by toolCallAccumulator.
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		// FinishReason is null on every chunk until the terminal one, where the
		// provider states why generation stopped: "stop" (natural end), "length"
		// (hit the output-token ceiling -- the answer is cut off), "content_filter",
		// etc. The stream used to ignore it entirely, so a "length" truncation was
		// indistinguishable on the wire from a clean "stop" (M1). Captured here and
		// reported once via streamCompletion's onFinish callback.
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`

	// Error carries a terminal error signalled INSIDE the SSE stream rather than
	// by an HTTP status -- by the time the first chunk has been written the status
	// line is long gone, so a mid-stream failure has nowhere else to go. The
	// managed proxy uses it to announce a budget kill
	// ({"error":"budget_exceeded","truncated":true}); OpenRouter can also surface
	// upstream errors this way.
	//
	// json.RawMessage, NOT string, deliberately: the value is a bare string in the
	// proxy's chunk but an OBJECT in OpenRouter's ({"error":{"message":...,
	// "code":...}}). Typing it as string would make the whole chunk fail to
	// unmarshal on the object form and hit the `continue` in streamCompletion,
	// silently discarding any content that chunk also carried. Keeping it raw
	// means an error shape this code does not recognize costs nothing -- see
	// errorSlug, which extracts a slug only when there is a string to extract.
	Error json.RawMessage `json:"error"`
}

// errorSlug extracts a machine-readable slug from a chunk's `error` field,
// reporting false when there is nothing usable. It accepts the two shapes seen
// in practice and refuses to guess at anything else:
//
//   - a bare string:  {"error":"budget_exceeded"}      -> "budget_exceeded"
//   - an object with a string "code": {"error":{"code":"budget_exceeded"}}
//
// Anything else (an object with no code, a number, null) yields false, so an
// unrecognized error shape falls through to the existing handling rather than
// inventing a reason. The extracted value is compared against known slugs by the
// caller and never rendered raw to the user: an upstream error string can carry
// a host or account detail, which Gate 7 keeps out of client-facing text.
func errorSlug(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return "", false
		}
		return s, true
	}
	var obj struct {
		Code json.RawMessage `json:"code"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", false
	}
	var code string
	if err := json.Unmarshal(obj.Code, &code); err != nil || code == "" {
		return "", false
	}
	return code, true
}

// incompleteInfoFor maps a terminal SSE finish_reason to the client-facing
// IncompleteInfo carried on the final Done message (M1), or nil when the answer
// finished naturally and there is nothing to flag. A natural "stop" -- and the
// common empty case, where the provider sent no finish_reason at all -- both map
// to nil, so the field is simply absent on a complete answer. The Detail strings
// follow the Gate-7 / Degradation discipline: they name what happened and what
// it costs the user, and carry no path, host, provider name, or raw upstream
// text. An unrecognized non-"stop" reason is still surfaced (better to flag an
// early end we can't name precisely than to hide it), labelled generically.
func incompleteInfoFor(finishReason string) *protocol.IncompleteInfo {
	switch finishReason {
	case "", "stop":
		return nil
	case finishReasonToolCalls:
		// A stream that ends in tool_calls is COMPLETE -- the model said what it
		// wanted to do next and stopped on purpose. Before this case existed it
		// fell to the default branch below and every tool-calling response was
		// reported to the user as "this answer may be incomplete", which is both
		// wrong and alarming: nothing was cut off.
		return nil
	case protocol.IncompleteLength:
		return &protocol.IncompleteInfo{
			Reason: protocol.IncompleteLength,
			Detail: "the model reached its output-length limit before finishing — this answer is cut off. Ask it to continue.",
		}
	case protocol.IncompleteContentFilter:
		return &protocol.IncompleteInfo{
			Reason: protocol.IncompleteContentFilter,
			Detail: "the provider's content filter stopped the response before it finished — this answer is incomplete.",
		}
	case protocol.IncompleteBudgetExceeded:
		return &protocol.IncompleteInfo{
			Reason: protocol.IncompleteBudgetExceeded,
			Detail: "this answer was cut short because the request reached its spending limit — what you see above is everything that was generated. Try asking for something narrower.",
		}
	default:
		return &protocol.IncompleteInfo{
			Reason: finishReason,
			Detail: "the model stopped before finishing its answer — this answer may be incomplete.",
		}
	}
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
//
// "zdr_required" is OUR OWN managed proxy's refusal, not OpenRouter's: when a
// caller's request reaches the proxy without the required routing flags it
// answers 403 `{"error":"zdr_required"}` (see proxy/main.go's F1 enforcement).
// That status alone classifies as ClassAuth in classifyHTTPError, so without
// this substring the user is told to check their API key for what is actually
// a privacy refusal. Unlike the phrases above this one is a machine-readable
// code we emit ourselves, so the match is exact and not brittle.
var zdrRefusalSubstrings = []string{
	"no allowed providers",
	"no available model provider",
	"zero data retention",
	"zdr_required",
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

// finishReasonToolCalls is the provider's terminal finish_reason when the model
// stopped in order to call tools rather than because it was done talking.
const finishReasonToolCalls = "tool_calls"

// toolCallAccumulator rebuilds whole tool calls from streamed fragments, keyed
// by the provider's `index`.
//
// This is the piece that has to be right, and it was prototyped and measured
// against real provider output in the Phase 0 eval before being promoted here
// (docs/TOOLCALL_RELIABILITY_2026-07-31.md records the observed shape). The
// arguments arrive as arbitrary string pieces that only form valid JSON once
// concatenated, so there is no per-fragment validation to do -- only assembly.
type toolCallAccumulator struct {
	calls map[int]*accumulatingCall
	order []int
}

type accumulatingCall struct {
	id   string
	name string
	typ  string
	args strings.Builder
	// sawName records whether any fragment ever supplied a function name.
	// Providers send it once, on the first fragment for an index; a call that
	// never got one is structurally incomplete and must not be executed.
	sawName bool
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{calls: make(map[int]*accumulatingCall)}
}

func (a *toolCallAccumulator) ingest(chunk chatCompletionChunk) {
	for _, choice := range chunk.Choices {
		for _, frag := range choice.Delta.ToolCalls {
			call, ok := a.calls[frag.Index]
			if !ok {
				call = &accumulatingCall{}
				a.calls[frag.Index] = call
				a.order = append(a.order, frag.Index)
			}
			if frag.ID != "" {
				call.id = frag.ID
			}
			if frag.Type != "" {
				call.typ = frag.Type
			}
			if frag.Function.Name != "" {
				call.name = frag.Function.Name
				call.sawName = true
			}
			call.args.WriteString(frag.Function.Arguments)
		}
	}
}

// finish returns the assembled calls in provider index order.
//
// It REFUSES a structurally incomplete call rather than returning a partial
// one. A stream cut mid-fragment leaves a call with no name, or with arguments
// that are a truncated prefix of a JSON object; executing either would mean
// acting on something the model did not finish saying. The turn ends with an
// error instead, and the caller never sees a half-built call to be tempted by.
//
// Arguments are validated as JSON here and NOWHERE ELSE in this file: this is
// the boundary between "the provider's stream" and "something the tool layer
// can dispatch", and it is the last point at which truncation is
// distinguishable from a tool that legitimately takes no arguments.
func (a *toolCallAccumulator) finish() ([]toolCall, error) {
	sort.Ints(a.order)
	calls := make([]toolCall, 0, len(a.order))
	for _, index := range a.order {
		call := a.calls[index]
		if !call.sawName {
			return nil, fmt.Errorf("tool call %d arrived without a function name (the stream ended mid-call)", index)
		}
		// THE ID IS STRUCTURAL, and this check was missing until the fuzzer
		// found it (QA gate 2026-08-01, P1-2; seed
		// testdata/fuzz/FuzzToolCallAccumulator/bd61f0f73fd1a68a).
		//
		// It is not merely a label. It is what an approval is BOUND to: an empty
		// id reaches the user as ToolApprovalRequest.CallID="" and makes
		// verifyApproval's `resp.CallID != req.CallID` check vacuous, leaving
		// consent bound by the argument digest alone -- and the digest covers
		// the arguments, not the tool name. Two id-less calls with identical
		// arguments then become indistinguishable to the verifier, so an
		// approval collected for one verifies for the other.
		//
		// It is also what pairs a tool RESULT back to its call for the provider
		// (see toolResultMessage), so an empty id is malformed on the way out
		// too. Refused here rather than defended against downstream, because
		// this function's whole contract is that a caller never sees a
		// half-built call to be tempted by.
		if call.id == "" {
			return nil, fmt.Errorf("tool call %d (%s) arrived without an id, which is what an approval binds to", index, call.name)
		}
		args := strings.TrimSpace(call.args.String())
		if args == "" {
			// A tool that genuinely takes no arguments still gets a valid
			// empty object, so the tool layer never has to special-case "".
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			return nil, fmt.Errorf("tool call %d (%s) had incomplete arguments (the stream ended mid-call)", index, call.name)
		}
		typ := call.typ
		if typ == "" {
			typ = "function"
		}
		calls = append(calls, toolCall{
			ID:       call.id,
			Type:     typ,
			Function: toolCallFunction{Name: call.name, Arguments: args},
		})
	}
	return calls, nil
}

// finishStream assembles any accumulated tool calls and reports the terminal
// finish reason. Shared by the two success returns in streamCompletion (the
// "[DONE]" sentinel and a body that simply closes) so they cannot drift -- one
// of them forgetting the accumulator would silently drop the model's tool call
// and end the turn as if it had said nothing.
//
// onFinish fires only when the calls assembled cleanly: a truncated call is an
// abnormal end, and reporting a finish reason for it would tell the caller the
// stream completed normally.
func finishStream(accumulator *toolCallAccumulator, finishReason string, onFinish func(string)) ([]toolCall, error) {
	calls, err := accumulator.finish()
	if err != nil {
		return nil, &ModelError{Class: ClassUpstreamUnavailable, detail: err.Error()}
	}
	if onFinish != nil {
		onFinish(finishReason)
	}
	return calls, nil
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

	// Breakpoint 1: the system prompt (static). If there is no history, this is the
	// only cacheable prefix before the current turn.
	var sysCache *cacheControl
	if len(history) == 0 {
		sysCache = &cacheControl{Type: "ephemeral"}
	}
	if systemPrompt != "" {
		messages = append(messages, chatMessage{Role: "system", Content: systemPrompt, CacheControl: sysCache})
	}

	if len(history) > 0 {
		// Breakpoint 2: the last message of the history. This caches the entire
		// prefix (system prompt + all prior turns + all prior retrieved context).
		history[len(history)-1].CacheControl = &cacheControl{Type: "ephemeral"}
		messages = append(messages, history...)
	}

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
//
// onReasoning, if non-nil, receives a reasoning-tier model's thinking tokens
// (delta.reasoning) as they arrive. It is a SEPARATE callback from onToken on
// purpose: reasoning is commentary, not answer. Routing it through onToken
// would splice thinking into the text that gets parsed for SEARCH/REPLACE
// blocks and written to conversation memory, which is how a model's musings
// about an edit would end up being mistaken for the edit. Its errors are not
// propagated — failing to deliver optional commentary must not fail a request
// that is otherwise succeeding.
//
// onFinish, if non-nil, is invoked exactly once when the stream ends
// SUCCESSFULLY, with the terminal SSE finish_reason (e.g. "stop", "length"),
// or "" if the provider sent none. It is how the caller learns an answer was
// cut off (M1): a "length" finish means the model hit its output ceiling
// mid-generation. It fires only on the success path — an error return already
// carries its own abnormal-end signal, so onFinish is not called then. Like the
// other observability callbacks it must never fail the request.
// It takes the message list ALREADY BUILT, rather than (systemPrompt, history,
// prompt) as it used to. The old shape rebuilt the conversation from scratch on
// every call, which is correct for exactly one call and impossible for a loop:
// an agent turn appends an assistant message carrying tool_calls and one tool
// message per result, then calls again with the accumulated list. Callers that
// send a single turn use buildChatMessages, which is unchanged.
//
// tools, when non-empty, advertises callable tools. Empty means the request
// body is byte-identical to the pre-tools one.
//
// It RETURNS any tool calls the model requested, rather than reporting them
// through a callback like the observability hooks above. They are a result of
// the stream, not an event during it: nothing can be done with a tool call
// until the stream has ended, and returning them keeps the "did the model ask
// for something?" question in the caller's control flow rather than in a
// closure. A non-nil error always comes with nil calls -- including the
// deliberate refusal of a call the stream truncated (see accumulator.finish).
func streamCompletion(ctx context.Context, apiBase, apiKey, model string, messages []chatMessage, tools []toolSpec, routing providerRouting, onToken func(string) error, onProvider func(string), onReasoning func(string), onFinish func(string)) ([]toolCall, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	reqBody, err := json.Marshal(chatCompletionRequest{
		Model:         model,
		Messages:      messages,
		Tools:         tools,
		Stream:        true,
		Provider:      routing,
		StreamOptions: streamOptions{IncludeUsage: true},
	})
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}

	url := strings.TrimRight(apiBase, "/") + chatCompletionsPath
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
		return nil, classifyTransportError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		bodyStr := strings.TrimSpace(string(body))
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

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, sseInitialBufferSize), sseMaxLineSize)

	providerSeen := false
	accumulator := newToolCallAccumulator()
	// finishReason is the most recent non-empty SSE finish_reason seen. The
	// provider reports it on the terminal content chunk (before the "[DONE]"
	// sentinel), so by the time either success return is reached it holds the
	// real stopping condition -- "stop" for a natural end, "length" for a cut-off
	// answer (M1). Reported once, via onFinish, on the success path only.
	finishReason := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return finishStream(accumulator, finishReason, onFinish)
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
		// Checked BEFORE the zero-choices guard below, not after: the proxy's
		// budget-kill chunk carries an `error` and NO choices, so a check placed
		// after that guard never runs -- which is exactly why the kill was
		// invisible. The chunk decoded to an empty struct, `continue` skipped it,
		// "[DONE]" arrived, and incompleteInfoFor("") returned nil, so a stream cut
		// off mid-answer was reported to the user as a complete success (P1-1).
		//
		// This sets the finish reason rather than returning an error: the answer so
		// far is real, streamed content the user should keep. It ends early, which
		// is what IncompleteInfo exists to say.
		// Deliberately does NOT `continue`: a chunk may carry an error alongside
		// content, and that content is real streamed output the user should still
		// receive. The budget-kill chunk has no choices of its own, so it falls
		// through to the guard below on its own merits.
		if slug, ok := errorSlug(chunk.Error); ok && slug == protocol.IncompleteBudgetExceeded {
			finishReason = protocol.IncompleteBudgetExceeded
		}
		// Fed BEFORE the zero-choices guard's siblings below so a chunk
		// carrying only tool-call fragments is not skipped by an early
		// `continue` further down.
		accumulator.ingest(chunk)

		if len(chunk.Choices) == 0 {
			continue
		}
		if fr := chunk.Choices[0].FinishReason; fr != "" {
			finishReason = fr
		}
		if reasoning := chunk.Choices[0].Delta.Reasoning; reasoning != "" && onReasoning != nil {
			onReasoning(reasoning)
		}
		content := chunk.Choices[0].Delta.Content
		if content == "" {
			continue
		}
		if err := onToken(content); err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, &ModelError{Class: ClassUpstreamUnavailable, detail: "reading model API stream: " + err.Error()}
	}
	// Stream ended without an explicit "[DONE]" sentinel (some providers just
	// close the body). Still a successful, complete read as far as we can tell,
	// so report whatever terminal finish_reason we captured.
	return finishStream(accumulator, finishReason, onFinish)
}
