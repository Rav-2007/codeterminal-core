package main

import (
	"context"
	"log"
	"math/rand/v2"
	"time"
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
	totalRetryBudget  = 45 * time.Second
)

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

	for attempt := 1; attempt <= maxStreamAttempts; attempt++ {
		streamed := false
		calls, err := streamCompletion(ctx, apiBase, apiKey, model, messages, tools, routing,
			func(token string) error {
				streamed = true
				return onToken(token)
			},
			onProvider,
			onReasoning,
			onFinish,
		)
		if err == nil {
			if attempt > 1 && logger != nil {
				logger.Printf("model API: succeeded on attempt %d/%d", attempt, maxStreamAttempts)
			}
			return calls, nil
		}
		lastErr = err
		modelErr := asModelError(err)

		if streamed {
			if logger != nil {
				logger.Printf("model API: failed mid-stream, after output had already reached the client; not retrying (would duplicate it): %s",
					modelErr.Detail())
			}
			return nil, err
		}
		if !modelErr.Retryable() {
			return nil, err
		}
		if attempt == maxStreamAttempts {
			break
		}

		delay := backoffFor(attempt, modelErr.RetryAfter)
		if time.Since(start)+delay > totalRetryBudget {
			if logger != nil {
				logger.Printf("model API: giving up after %s (retry budget); last error: %s", time.Since(start).Round(time.Millisecond), modelErr.Detail())
			}
			break
		}
		if logger != nil {
			logger.Printf("model API: attempt %d/%d failed (%s), retrying in %s: %s",
				attempt, maxStreamAttempts, modelErr.Class, delay.Round(time.Millisecond), modelErr.Detail())
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}

	return nil, lastErr
}
