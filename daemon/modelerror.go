package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ModelErrorClass names WHAT kind of thing went wrong upstream, in terms a user
// can act on. Every upstream failure used to collapse into one string,
// "calling model API failed", so a transient 502, an exhausted quota, a
// rejected API key and a conversation too long for the context window were
// indistinguishable — the user could not tell "wait a second and retry" from
// "top up your account" from "start a new session".
type ModelErrorClass string

const (
	// ClassRateLimited is a temporary throttle. Retrying later works.
	ClassRateLimited ModelErrorClass = "rate_limited"
	// ClassQuotaExceeded is an exhausted allowance. Retrying does NOT work and
	// only burns time; it needs a top-up or a reset.
	ClassQuotaExceeded ModelErrorClass = "quota_exceeded"
	// ClassContextTooLarge means the request itself is too big to ever succeed
	// as written.
	ClassContextTooLarge ModelErrorClass = "context_too_large"
	// ClassAuth means the credentials were rejected.
	ClassAuth ModelErrorClass = "auth"
	// ClassUpstreamUnavailable covers outages, transport failures and timeouts:
	// nothing is wrong with the request, the other end is just not answering.
	ClassUpstreamUnavailable ModelErrorClass = "upstream_unavailable"
	// ClassPrivacyRefused is this product's own routing constraint refusing to
	// send data to a provider that does not guarantee zero data retention.
	ClassPrivacyRefused ModelErrorClass = "privacy_refused"
	// ClassInvalidRequest is the daemon refusing a request on its own terms,
	// before any upstream call — currently an empty prompt (Fix 14). It is the
	// one class in this set that never involves the provider at all, which is
	// exactly what makes it worth distinguishing: nothing was sent, nothing was
	// billed, and retrying the identical request will fail identically.
	ClassInvalidRequest ModelErrorClass = "invalid_request"
	// ClassUnavailableTier means the requested model is not available on your current plan.
	ClassUnavailableTier ModelErrorClass = "unavailable_tier"
	// ClassUnknown is the honest fallback for anything unrecognised. It keeps
	// the old generic message rather than inventing a confident wrong one.
	ClassUnknown ModelErrorClass = "unknown"
)

// clientMessages are what actually crosses the socket. They say what happened
// and what to do about it, and deliberately name NO infrastructure: no upstream
// host or URL, no HTTP status, no provider name, no raw response body, no host
// path. That is the Gate-7 line, and being more specific about the KIND of
// failure must not become a way of being more revealing about the shape of the
// deployment — the classification is derived from upstream detail, it does not
// carry it.
var clientMessages = map[ModelErrorClass]string{
	ClassRateLimited:         "the model provider is rate-limiting requests right now — wait a few seconds and try again",
	ClassQuotaExceeded:       "your inference quota is exhausted — further requests will keep failing until it resets or is topped up",
	ClassContextTooLarge:     "this conversation is too large for the model's context window — start a new session or shorten the request",
	ClassAuth:                "the configured API credentials were rejected — check the API key this daemon was started with",
	ClassUpstreamUnavailable: "the model provider is unreachable or failing right now — this is usually temporary",
	ClassPrivacyRefused:      "inference refused: no zero-data-retention endpoint available",
	ClassInvalidRequest:      "the request was rejected before it was sent: the prompt is empty",
	ClassUnavailableTier:     "Model %s is not available on your current plan.",
	ClassUnknown:             "calling model API failed",
}

// retryableClasses are the failures where trying again can plausibly succeed.
// The rest are deliberately excluded: retrying an exhausted quota, a rejected
// key, an over-long request or a privacy refusal cannot change the outcome and
// only spends time and (for metered inference) money. See streamWithRetry.
var retryableClasses = map[ModelErrorClass]bool{
	ClassRateLimited:         true,
	ClassUpstreamUnavailable: true,
}

// ModelError is a classified upstream failure. It carries two descriptions on
// purpose: the safe one, which is what Error() returns and what travels to
// clients, and the full upstream detail, which only Detail() exposes and only
// the daemon's own log ever sees.
//
// Error() is the SAFE form specifically so a leak cannot happen by accident. A
// future handler that reaches for %v on this error gets the scrubbed message,
// not the provider's response body; getting the diagnostic detail requires
// asking for it by name.
type ModelError struct {
	Class ModelErrorClass
	// RetryAfter is the upstream's own requested delay, when it sent one.
	RetryAfter time.Duration

	detail string
	// modelName is populated for tier refusal to display which model was denied.
	modelName string
}

func (e *ModelError) Error() string {
	if e.Class == ClassUnavailableTier && e.modelName != "" {
		return fmt.Sprintf(clientMessages[ClassUnavailableTier], e.modelName)
	}
	if msg, ok := clientMessages[e.Class]; ok {
		return msg
	}
	return clientMessages[ClassUnknown]
}

func (e *ModelError) withModelName(model string) *ModelError {
	e.modelName = model
	return e
}

// Detail is the full upstream description — status line, response body,
// transport error. For the operator's log only; never put it on the wire.
func (e *ModelError) Detail() string {
	if e.detail == "" {
		return e.Error()
	}
	return fmt.Sprintf("[%s] %s", e.Class, e.detail)
}

// withUpstreamRequestID folds the upstream's own request id into the operator
// detail, so a failure the user reports here can be looked up in the managed
// proxy's log rather than guessed at from timestamps. Local log only, like the
// rest of detail -- the socket still gets the generic message (Gate 7).
func (e *ModelError) withUpstreamRequestID(id string) *ModelError {
	if id == "" {
		return e
	}
	if e.detail == "" {
		e.detail = "upstream req_id=" + id
		return e
	}
	e.detail += " (upstream req_id=" + id + ")"
	return e
}

// upstreamRequestID reads a correlation id off an upstream response, and refuses
// anything that is not the shape our own proxy mints (lowercase hex, bounded).
//
// The validation is not pedantry: this value comes from whatever host apiBase
// points at, it is written straight into the daemon's log file, and a header
// containing a newline would let an upstream forge log lines in it. Hex cannot.
func upstreamRequestID(h http.Header) string {
	id := strings.TrimSpace(h.Get("X-Request-Id"))
	if id == "" || len(id) > 64 {
		return ""
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return id
}

// Retryable reports whether trying the same request again could plausibly
// succeed.
func (e *ModelError) Retryable() bool { return retryableClasses[e.Class] }

// Unwrap keeps errors.Is(err, ErrZDRRefused) working for the privacy refusal,
// which callers already branch on.
func (e *ModelError) Unwrap() error {
	if e.Class == ClassPrivacyRefused {
		return ErrZDRRefused
	}
	return nil
}

// contextLengthPhrases are how the providers this talks to describe a request
// that exceeds the model's context window. Like zdrRefusalSubstrings, this is a
// best-effort text match rather than a documented machine-readable code, and
// falls back to the status-code classification when nothing matches.
var contextLengthPhrases = []string{
	"context length",
	"context_length_exceeded",
	"maximum context",
	"too many tokens",
	"reduce the length",
	"string too long",
}

// quotaPhrases identify an exhausted allowance rather than a temporary
// throttle. This distinction matters more than it looks: the managed proxy
// signals its own quota exhaustion as HTTP 429 with {"error":"quota_exceeded"},
// the SAME status a real rate limit uses — so classifying on the status alone
// would mark an unretryable condition retryable and have the retry layer sit
// there backing off against something that can never succeed.
var quotaPhrases = []string{
	"quota_exceeded",
	"quota exceeded",
	"insufficient_quota",
	"insufficient credit",
	"out of credit",
	"billing",
}

// classifyHTTPError maps an upstream non-200 response to a class. Body text is
// consulted before the status code wherever the status is ambiguous.
func classifyHTTPError(status int, statusLine, body string) *ModelError {
	lower := strings.ToLower(body)
	detail := fmt.Sprintf("model API returned %s: %s", statusLine, body)

	switch {
	case isZDRRoutingRefusal(body):
		return &ModelError{Class: ClassPrivacyRefused, detail: detail}
	case strings.Contains(lower, "unavailable_tier") || strings.Contains(lower, "model_not_allowed") || strings.Contains(lower, "cost_surface_not_allowed"):
		return &ModelError{Class: ClassUnavailableTier, detail: detail}
	case containsAny(lower, quotaPhrases):
		return &ModelError{Class: ClassQuotaExceeded, detail: detail}
	case containsAny(lower, contextLengthPhrases):
		return &ModelError{Class: ClassContextTooLarge, detail: detail}
	}

	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &ModelError{Class: ClassAuth, detail: detail}
	case status == http.StatusPaymentRequired:
		return &ModelError{Class: ClassQuotaExceeded, detail: detail}
	case status == http.StatusTooManyRequests:
		return &ModelError{Class: ClassRateLimited, detail: detail}
	case status == http.StatusRequestEntityTooLarge:
		return &ModelError{Class: ClassContextTooLarge, detail: detail}
	case status >= 500:
		return &ModelError{Class: ClassUpstreamUnavailable, detail: detail}
	}
	return &ModelError{Class: ClassUnknown, detail: detail}
}

// classifyTransportError maps a failure to even get a response — connection
// refused, DNS failure, TLS problem, timeout — to a class. None of these say
// anything about the request, so they are all "the other end is not answering".
func classifyTransportError(err error) *ModelError {
	detail := err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		return &ModelError{Class: ClassUpstreamUnavailable, detail: "request to model API timed out: " + detail}
	}
	if errors.Is(err, context.Canceled) {
		return &ModelError{Class: ClassUpstreamUnavailable, detail: "request to model API cancelled: " + detail}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return &ModelError{Class: ClassUpstreamUnavailable, detail: "network error calling model API: " + detail}
	}
	return &ModelError{Class: ClassUpstreamUnavailable, detail: "calling model API: " + detail}
}

// parseRetryAfter reads an upstream Retry-After header in either documented
// form (delay in seconds, or an HTTP date). An unparseable or absurd value is
// ignored rather than trusted — a provider asking us to sleep for an hour does
// not get to hold a connection open that long; the retry bound decides.
func parseRetryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(v); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}

func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

// asModelError recovers the classified error from an error chain, classifying
// anything unrecognised as a transport failure so a caller always has a class
// to act on.
func asModelError(err error) *ModelError {
	var me *ModelError
	if errors.As(err, &me) {
		return me
	}
	return classifyTransportError(err)
}
