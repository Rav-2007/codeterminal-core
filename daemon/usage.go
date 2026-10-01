package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	// logger, when set, gets one line per call: the host that served it and
	// what it billed, cached tokens included. /usage shows a turn's sum; only
	// this says WHICH call missed the cache. A live test on 2026-09-30 cached
	// 512 of 23.2K tokens over three calls to one host, and nothing recorded
	// which of them missed or why.
	logger *log.Logger
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

// add counts one call's bill; provider is the host that served it, if known.
func (t *usageTally) add(c chunkUsage, provider string) {
	if t == nil {
		return
	}
	cached, reasoning := 0, 0
	if d := c.PromptTokensDetails; d != nil {
		cached = d.CachedTokens
	}
	if d := c.CompletionTokensDetails; d != nil {
		reasoning = d.ReasoningTokens
	}
	t.mu.Lock()
	t.u.Calls++
	t.u.PromptTokens += c.PromptTokens
	t.u.CompletionTokens += c.CompletionTokens
	t.u.ContextTokens = c.PromptTokens // the last call's prompt is what the model last saw
	t.u.CachedTokens += cached
	t.u.ReasoningTokens += reasoning
	if c.Cost != nil {
		t.u.CostUSD += *c.Cost
	} else {
		t.u.CostMissing = true
	}
	t.mu.Unlock()

	if t.logger != nil {
		cost := "not reported"
		if c.Cost != nil {
			cost = fmt.Sprintf("$%.6f", *c.Cost)
		}
		t.logger.Printf("model API usage: provider=%q prompt=%d cached=%d completion=%d reasoning=%d cost=%s",
			provider, c.PromptTokens, cached, c.CompletionTokens, reasoning, cost)
	}
}

// addUnreported counts a call that ended before its bill arrived: cut off by a
// stop, a stall or a broken stream. The provider may well have charged for it
// and nothing says how much, so it makes the turn's cost a lower bound rather
// than a guess.
func (t *usageTally) addUnreported(provider string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.u.Calls++
	t.u.CostMissing = true
	t.mu.Unlock()

	if t.logger != nil {
		t.logger.Printf("model API usage: provider=%q -- the call ended before its usage arrived (cost unknown)", provider)
	}
}

// spent is what the turn has billed so far: its calls, and its dollars where
// the provider reported them. A long task's budget reads it before every step
// (longtask.go); a call with no reported cost counts as a call and as $0, so
// the call budget is the backstop for a provider that sends no cost.
func (t *usageTally) spent() (calls int, usd float64) {
	if t == nil {
		return 0, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.u.Calls, t.u.CostUSD
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

// STOPPED TURNS ARE BILLED TOO. A turn the user stopped sends no Done -- the
// client hung up -- so the bill for every call it had already made used to be
// dropped, and in agent mode that can be many steps. stoppedUsage holds each
// stopped turn's bill until the next Done this daemon sends, which carries it
// (protocol.TokenResponse.StoppedUsage). It lives in memory only: a daemon
// restart before the next turn loses it.
type stoppedUsage struct {
	mu    sync.Mutex
	turns []protocol.TurnUsage
}

// maxStoppedUsage bounds what is held for a client that never finishes a turn.
// Past it, bills are merged into the last entry: the money is kept, only the
// count of stopped turns undercounts.
const maxStoppedUsage = 64

// hold keeps a stopped turn's bill. A nil bill -- stopped before any model call
// -- is nothing to keep.
func (h *stoppedUsage) hold(u *protocol.TurnUsage) {
	if u == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.turns) < maxStoppedUsage {
		h.turns = append(h.turns, *u)
		return
	}
	last := &h.turns[len(h.turns)-1]
	last.Calls += u.Calls
	last.PromptTokens += u.PromptTokens
	last.CompletionTokens += u.CompletionTokens
	last.CachedTokens += u.CachedTokens
	last.ReasoningTokens += u.ReasoningTokens
	last.CostUSD += u.CostUSD
	last.CostMissing = last.CostMissing || u.CostMissing
}

// take hands every held bill to a Done about to be sent.
func (h *stoppedUsage) take() []protocol.TurnUsage {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.turns
	h.turns = nil
	return out
}

// giveBack returns bills whose Done could not be written, ahead of any held
// since, so the next Done still carries them in order.
func (h *stoppedUsage) giveBack(us []protocol.TurnUsage) {
	if len(us) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.turns = append(append([]protocol.TurnUsage(nil), us...), h.turns...)
}

// sendDone writes a turn's final message with any stopped turns' bills on it.
// If it cannot be written the client left at the very end, which is a stop
// like any other: those bills, and this turn's own, wait for the next Done.
func (s *Server) sendDone(enc *json.Encoder, resp protocol.TokenResponse) error {
	resp.StoppedUsage = s.stopped.take()
	err := enc.Encode(resp)
	if err != nil {
		s.stopped.giveBack(resp.StoppedUsage)
		s.stopped.hold(resp.Usage)
	}
	return err
}
