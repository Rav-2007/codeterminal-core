package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"time"

	"mochiii/protocol"
)

// Retry bounds. There was no retry anywhere in the request path, so a single
// transient 502 ended the turn and the user retyped their prompt.
//
// The numbers are chosen to be worth doing and impossible to hide behind: three
// attempts total, and a whole-retry budget short enough that a persistent
// outage fails while the user is still watching rather than after they have
// given up. maxRetryBackoff caps any single sleep, including one an upstream
// asked for via Retry-After.
const (
	maxStreamAttempts = 3
	baseRetryBackoff  = 500 * time.Millisecond
	maxRetryBackoff   = 8 * time.Second
)

// totalRetryBudget is the whole-retry budget described above. A var so tests
// can shorten it.
var totalRetryBudget = 45 * time.Second

// A RATE LIMIT IS WAITED OUT, NOT HURRIED (2026-10-25). The three quick tries
// above were built for a dropped connection, and a rate limit is not one.
// MEASURED on the owner's NVIDIA key: an agent task made 8 calls in 21 seconds,
// the 9th got 429, both retries -- 0.7 s and 1 s later -- got 429 too, and a task
// the user had been watching stopped with "the model provider failed". A
// provider counts requests (and often tokens) over a minute, so a second try
// inside the same second is spent before it starts. A rate limit gets five
// tries, waits that start at 5 seconds and double to 30, and 75 seconds in all,
// and the user is told about every wait (withRetryNotice).
const (
	maxRateLimitAttempts = 5
	maxRateLimitBackoff  = 30 * time.Second
)

// baseRateLimitBackoff is the first rate-limit wait, doubling from there, and
// rateLimitBudget the whole wait a rate limit may take. Vars so tests can
// shorten them.
var (
	baseRateLimitBackoff = 5 * time.Second
	rateLimitBudget      = 75 * time.Second
)

// rateLimitBackoffFor is backoffFor for a rate limit: at least half the doubling
// wait, so a draw near zero cannot spend a try inside the same window, and the
// provider's own Retry-After when it asks for longer, up to the cap.
func rateLimitBackoffFor(attempt int, retryAfter time.Duration) time.Duration {
	exp := min(baseRateLimitBackoff<<(attempt-1), maxRateLimitBackoff)
	delay := exp/2 + time.Duration(rand.Int64N(int64(exp/2)+1))
	return min(max(delay, retryAfter), maxRateLimitBackoff)
}

// retryNoticeKey carries a turn's way of telling its user that a model call is
// waiting out a rate limit. Unset, the wait is silent -- a spinner, still turning.
type retryNoticeKey struct{}

// withRetryNotice returns ctx carrying notice, which streamWithRetry calls with
// the length of each rate-limit wait before it starts it.
func withRetryNotice(ctx context.Context, notice func(wait time.Duration)) context.Context {
	return context.WithValue(ctx, retryNoticeKey{}, notice)
}

// rateLimitWait is what a user is shown while a call waits out a rate limit: a
// step line, as a pipeline's phases are (no call id, so each wait is its own
// line), naming no provider -- the Gate-7 line clientMessages keeps.
func rateLimitWait(wait time.Duration) protocol.ToolActivity {
	return protocol.ToolActivity{
		Phase: protocol.ToolPhaseStep, Tool: protocol.ToolWaiting, DurationMS: wait.Milliseconds(),
		Detail: fmt.Sprintf("the model provider is rate-limiting requests; trying again in %ds", int(wait.Round(time.Second)/time.Second)),
	}
}

// stoppedBecause is the reason in a task's stop notice: the kind of failure in
// the user's terms, never its detail (clientMessages' rule).
func stoppedBecause(err error) string {
	if asModelError(err).Class == ClassRateLimited {
		return "the model provider kept rate-limiting requests through a minute of waiting (give it a minute or two)"
	}
	return "the model provider failed"
}

// backoffFor computes the delay before the given retry attempt (1-based:
// attempt 1 is the wait after the first failure).
//
// Exponential with FULL jitter -- a uniform draw from [0, exponential) rather
// than the exponential itself. Fixed backoff would have every client that hit
// the same upstream blip come back in the same instant and re-create the
// thundering herd that caused it; full jitter spreads them out. An upstream's
// own Retry-After wins when it asks for longer, since it knows more than we do,
// but is still capped: a provider does not get to hold a connection open for an
// hour because it named a large number.
func backoffFor(attempt int, retryAfter time.Duration) time.Duration {
	exp := baseRetryBackoff << (attempt - 1)
	if exp > maxRetryBackoff {
		exp = maxRetryBackoff
	}
	delay := time.Duration(rand.Int64N(int64(exp)) + int64(baseRetryBackoff)/2)

	if retryAfter > delay {
		delay = retryAfter
	}
	if delay > maxRetryBackoff {
		delay = maxRetryBackoff
	}
	return delay
}

// streamWithRetry runs streamCompletion, retrying transient failures with
// jittered backoff (Fix 10). It is a drop-in wrapper: same arguments, same
// return, so the prompt path gains recovery without gaining a second code path
// through the provider.
//
// TWO RULES DECIDE WHETHER A RETRY HAPPENS, and both are about not making
// things worse:
//
//  1. Nothing may have streamed yet. onToken is wrapped to record whether the
//     client has seen any output; once it has, the request is no longer safely
//     repeatable — a second attempt would re-emit text the user already read,
//     interleaved with the first attempt's. So a mid-stream failure is returned
//     immediately, however transient it looks. This is the idempotency
//     boundary: an inference call is safe to repeat only before it has produced
//     anything.
//
//     REASONING tokens deliberately do NOT trip this (Fix 14). The boundary
//     protects the ANSWER: only content tokens accumulate into the reply that
//     gets parsed for edit blocks and written to conversation memory, so a
//     retry after reasoning-only output cannot duplicate or interleave any of
//     that. Counting them would instead make a reasoning-tier request
//     unretryable from its first millisecond, since thinking starts before any
//     content does — trading all recovery for a cosmetic repeat of ephemeral
//     commentary. Named cost: a client that renders reasoning inline will show
//     the discarded attempt's thinking before the successful attempt's.
//
//  2. The failure must be in a retryable class. rate_limited and
//     upstream_unavailable can succeed on a second try; auth, quota_exceeded,
//     context_too_large and privacy_refused cannot, and retrying them only
//     spends the user's time and, on metered inference, their money. That
//     distinction is exactly what Fix 9's classification bought.
//
// Attempts and total elapsed time are both bounded, so a persistent failure
// terminates cleanly with the last real error rather than looping. A cancelled
// context aborts immediately without waiting out a pending backoff.
//
// IN AN AGENT LOOP, both rules keep working unchanged, and it is worth saying
// why rather than leaving it to be rediscovered. `streamed` is reset per
// ATTEMPT, so each iteration of the loop gets its own retry budget. And an
// iteration whose tool calls already ran is never re-entered here: the loop
// appends those results to `messages` and calls again with a longer list, so a
// retry can only ever repeat the model call that produced a request to act --
// never the acting. The dangerous case, retrying a call whose side effects
// already happened, is unreachable by construction rather than by care.
func streamWithRetry(
	ctx context.Context,
	apiBase, apiKey, model string,
	messages []chatMessage,
	tools []toolSpec,
	routing providerRouting,
	onToken func(string) error,
	onProvider func(string),
	onReasoning func(string),
	onFinish func(string),
	logger *log.Logger,
) ([]toolCall, error) {
	start := time.Now()
	var lastErr error
	// ONE STALL GETS ONE RETRY, whatever the budget says. The watchdog only
	// fires after streamStallTimeout of silence, which alone is longer than
	// totalRetryBudget -- so without this a stall could never be retried, and
	// a stall is the failure a second attempt (possibly routed to another
	// provider) fixes most often. One, so two stalls in a row still end the
	// turn in bounded time.
	//
	// AND SO DOES A REQUEST THAT REACHED requestTimeout STILL SENDING (ranOut),
	// for the same arithmetic: five minutes is past the budget before the first
	// attempt has finished failing, so it was never retried either (MEASURED
	// 2026-09-29, a 300 s reasoning runaway). The one retry is shared: a stall
	// and then a run-out, in either order, is two attempts and not three.
	// Whether a second attempt usually finishes is NOT measured -- it needs a
	// real model -- so this is one retry and no more.
	stallRetried := false

	// The loop runs to the larger limit; each failure says which limit is its
	// own below (a rate limit gets more tries than a dropped connection).
	for attempt := 1; attempt <= maxRateLimitAttempts; attempt++ {
		streamed := false
		// Reference marks no client can open are taken out here, the one place
		// every model call passes (citemarks.go). A filter per attempt: what one
		// attempt held back is not the next one's.
		marks := newCiteMarkFilter(messages)
		// streamed means what it always meant -- the client has SEEN output -- so
		// text the filter is still holding does not close the door on a retry.
		emit := func(text string) error {
			if text == "" {
				return nil
			}
			streamed = true
			return onToken(text)
		}
		calls, err := streamCompletion(ctx, apiBase, apiKey, model, messages, tools, routing,
			func(token string) error { return emit(marks.feed(token)) },
			onProvider,
			onReasoning,
			onFinish,
		)
		if err == nil {
			// The stream ended, so what was held is ordinary text after all.
			if err := emit(marks.flush()); err != nil {
				return nil, err
			}
			if marks.marks > 0 && logger != nil {
				logger.Printf("model API: the model wrote %d reference mark(s) no client can open (the 【…】 kind); "+
					"removed, keeping any address written inside", marks.marks)
			}
			if attempt > 1 && logger != nil {
				logger.Printf("model API: succeeded on attempt %d", attempt)
			}
			return calls, nil
		}
		lastErr = err
		// A CANCELLED TURN IS NEVER RETRIED, and says so. The client hung up
		// (turnwriter.go) or the daemon is stopping; either way nobody wants a
		// second attempt. Without this, a stop after a long think reached the
		// retry-budget break below and came back as the stream's own error, so
		// the caller could not tell an interrupt from a failure.
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ctx.Err()
		}
		modelErr := asModelError(err)

		if streamed {
			// The client keeps everything the provider sent before it failed,
			// and what the filter was holding is part of that. (When nothing has
			// been shown, the held text goes with the attempt: releasing it here
			// would be the output that forbids the retry below.)
			_ = emit(marks.flush())
			if logger != nil {
				logger.Printf("model API: failed mid-stream, after output had already reached the client; not retrying (would duplicate it): %s",
					modelErr.Detail())
			}
			return nil, err
		}
		if !modelErr.Retryable() {
			return nil, err
		}
		limit, budget, delay := maxStreamAttempts, totalRetryBudget, backoffFor(attempt, modelErr.RetryAfter)
		if modelErr.Class == ClassRateLimited {
			limit, budget, delay = maxRateLimitAttempts, rateLimitBudget, rateLimitBackoffFor(attempt, modelErr.RetryAfter)
		}
		if attempt >= limit {
			break
		}

		retryStall := (modelErr.stalled || modelErr.ranOut) && !stallRetried
		if retryStall {
			stallRetried = true
		}
		if time.Since(start)+delay > budget && !retryStall {
			if logger != nil {
				logger.Printf("model API: giving up after %s (retry budget); last error: %s", time.Since(start).Round(time.Millisecond), modelErr.Detail())
			}
			break
		}
		if logger != nil {
			logger.Printf("model API: attempt %d/%d failed (%s), retrying in %s: %s",
				attempt, limit, modelErr.Class, delay.Round(time.Millisecond), modelErr.Detail())
		}
		if notice, ok := ctx.Value(retryNoticeKey{}).(func(time.Duration)); ok && modelErr.Class == ClassRateLimited {
			notice(delay)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}

	return nil, lastErr
}
