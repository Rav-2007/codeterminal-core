// Package retry re-attempts a failing operation with exponential backoff,
// for use around flaky network calls.
package retry

import (
	"context"
	"math/rand"
	"time"
)

// Options controls the backoff schedule.
type Options struct {
	MaxAttempts  int
	InitialDelay time.Duration
	MaxDelay     time.Duration
	// Jitter adds up to this fraction of random variance to each delay, to
	// avoid many clients retrying in lockstep against the same upstream.
	Jitter float64
}

// DefaultOptions is a reasonable starting point for retrying HTTP calls.
var DefaultOptions = Options{
	MaxAttempts:  5,
	InitialDelay: 200 * time.Millisecond,
	MaxDelay:     10 * time.Second,
	Jitter:       0.2,
}

// Do calls fn, retrying with exponential backoff if it returns an error,
// until it succeeds, ctx is canceled, or MaxAttempts is reached. The delay
// between attempts doubles each time, capped at MaxDelay, with random
// jitter applied so retries from multiple callers don't all land at once.
func Do(ctx context.Context, opts Options, fn func() error) error {
	var lastErr error
	delay := opts.InitialDelay

	for attempt := 1; attempt <= opts.MaxAttempts; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}

		if attempt == opts.MaxAttempts {
			break
		}

		wait := addJitter(delay, opts.Jitter)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}

		delay *= 2
		if delay > opts.MaxDelay {
			delay = opts.MaxDelay
		}
	}

	return lastErr
}

// addJitter returns d plus or minus up to jitterFraction of d, at random.
func addJitter(d time.Duration, jitterFraction float64) time.Duration {
	if jitterFraction <= 0 {
		return d
	}
	spread := float64(d) * jitterFraction
	offset := (rand.Float64()*2 - 1) * spread
	return d + time.Duration(offset)
}
