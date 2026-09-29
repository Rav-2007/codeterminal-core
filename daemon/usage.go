package main

import (
	"context"
	"sync"

	"mochiii/protocol"
)

// TURN USAGE: what a turn cost, from the provider's own bill, for /usage.
//
// Every model call ends with a usage chunk (stream_options.include_usage):
// tokens in, cached, out, reasoning, and the dollars charged. A turn can make
// many calls -- an agent loop's iterations, a /team pipeline's phases, retries
// -- so the tally rides in the turn's context and every call adds to it, with
// no call site having to thread it through. The turn's final Done message
// carries the sum (protocol.TurnUsage).
//
// NOTHING IS ESTIMATED. A call whose provider sent no usage adds nothing, and
// one that sent tokens but no cost marks the cost as a lower bound.

// chunkUsage is OpenRouter's usage object on a stream's final chunk.
type chunkUsage struct {
	PromptTokens        int      `json:"prompt_tokens"`
	CompletionTokens    int      `json:"completion_tokens"`
	Cost                *float64 `json:"cost,omitempty"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details,omitempty"`
}

// usageTally sums one turn's calls. Safe for concurrent use: nothing today
// streams two calls at once within a turn, but nothing should have to know.
type usageTally struct {
	mu sync.Mutex
	u  protocol.TurnUsage
}

type usageTallyKey struct{}

// withUsageTally starts a turn's tally.
func withUsageTally(ctx context.Context) (context.Context, *usageTally) {
	t := &usageTally{}
	return context.WithValue(ctx, usageTallyKey{}, t), t
}

// usageTallyFrom returns the turn's tally, or nil outside a turn -- and a nil
// tally ignores what it is given, so callers need not check.
func usageTallyFrom(ctx context.Context) *usageTally {
	t, _ := ctx.Value(usageTallyKey{}).(*usageTally)
	return t
}

func (t *usageTally) add(c chunkUsage) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.u.Calls++
	t.u.PromptTokens += c.PromptTokens
	t.u.CompletionTokens += c.CompletionTokens
	t.u.ContextTokens = c.PromptTokens // the last call's prompt is what the model last saw
	if d := c.PromptTokensDetails; d != nil {
		t.u.CachedTokens += d.CachedTokens
	}
	if d := c.CompletionTokensDetails; d != nil {
		t.u.ReasoningTokens += d.ReasoningTokens
	}
	if c.Cost != nil {
		t.u.CostUSD += *c.Cost
	} else {
		t.u.CostMissing = true
	}
}

// report is the turn's usage for its final message, or nil when no call
// reported any -- a turn refused before the model, or a provider that sends
// no usage.
func (t *usageTally) report(model string, window int) *protocol.TurnUsage {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.u.Calls == 0 {
		return nil
	}
	u := t.u
	u.Model, u.ContextWindow = model, window
	return &u
}

// contextWindowFor is the context window models.json gives the model slug, or
// 0 when no tier names it (a -model override, or a tier without one).
func (s *Server) contextWindowFor(slug string) int {
	if s.cfg == nil {
		return 0
	}
	for _, t := range s.cfg.Tiers {
		if t.Slug == slug && t.ContextWindow > 0 {
			return t.ContextWindow
		}
	}
	return 0
}
