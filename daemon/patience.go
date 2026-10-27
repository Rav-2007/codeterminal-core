package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mochiii/protocol"
)

// A BUSY PROVIDER IS WAITED OUT INSIDE THE TURN (2026-10-26).
//
// streamWithRetry waits out a rate limit for about a minute and a stall once
// (retry.go). In a run of real fixes on a congested free provider that was not
// enough: a rate limit or a silence that outlasted it ended the turn part-way,
// and nothing resumed on its own -- the user had to type "continue", and the
// next turn started from what the last one said. A turn that has already done
// work is worth more patience than a first request: its working copy, its
// reads and its checks are all in memory, and asking the same step again
// costs nothing the provider did not already refuse.
//
// So, when a step fails in a way a wait can cure, after the turn has run at
// least one tool, and before the step showed the user any words, the loop
// waits -- a minute, then two -- and asks the same step again, the user told
// with the countdown they already see for a rate limit. It stops when the
// patience is spent (mcp.budget.provider_patience_seconds, five minutes by
// default, 0 to switch it off) or the turn's own time would run out, and the
// turn then ends as it did before, with Mochiii's record saying where.

// defaultProviderPatience is how long one turn may wait on its provider, in
// all, past streamWithRetry's own retries.
const defaultProviderPatience = 5 * time.Minute

// providerWaitBase is the first wait; later ones are twice it. A var so a test
// can shorten it.
var providerWaitBase = time.Minute

// providerWaitFor is the n-th wait (0-based): a minute, then two.
func providerWaitFor(n int) time.Duration {
	if n == 0 {
		return providerWaitBase
	}
	return 2 * providerWaitBase
}

// minStepRoom is the turn time a step needs after a wait to be worth asking.
const minStepRoom = 30 * time.Second

// maxProviderWaits is the most waits one turn makes, whatever their length: a
// minute and two of two minutes is the default patience, and a shorter wait
// (a test's) must not turn the same bound into a hundred tries.
const maxProviderWaits = 3

func (b MCPBudgetConfig) resolvedProviderPatience() time.Duration {
	switch {
	case b.ProviderPatienceSeconds < 0:
		return 0
	case b.ProviderPatienceSeconds == 0:
		return defaultProviderPatience
	}
	return time.Duration(b.ProviderPatienceSeconds) * time.Second
}

// waitOutProvider waits before the same step is asked again, and reports
// whether it did; false leaves the failure to end the turn as before.
func (s *Server) waitOutProvider(ctx context.Context, turn *agentTurn, bud *budget, err error, stepText string,
	show func(protocol.ToolActivity)) bool {
	me := asModelError(err)
	if !me.Retryable() || len(turn.toolNames) == 0 || strings.TrimSpace(stepText) != "" || s.cfg == nil ||
		turn.providerWaits >= maxProviderWaits {
		return false
	}
	spent := time.Duration(0)
	for n := 0; n < turn.providerWaits; n++ {
		spent += providerWaitFor(n)
	}
	wait := providerWaitFor(turn.providerWaits)
	if spent+wait > s.cfg.MCP.Budget.resolvedProviderPatience() || time.Until(bud.deadline) < wait+minStepRoom {
		return false
	}
	why := "the model provider is not answering"
	if me.Class == ClassRateLimited {
		why = "the model provider is still rate-limiting requests"
	}
	s.logger.Printf("agent: %s after %d step(s) of work; waiting %s before asking the same step again (%s)",
		why, turn.calls, wait, me.Detail())
	// Shown as the rate-limit countdown is (the TUI's ratewait.go), in words
	// that say which wait it is.
	if show != nil {
		show(providerWait(wait, why))
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(wait):
	}
	turn.providerWaits++
	if me.Class == ClassRateLimited {
		// Refused outright, so the provider billed nothing: the step asked again
		// is the same step, not one more of the turn's.
		turn.calls--
		turn.iteration--
	}
	return true
}

// providerWait is the step a user sees while the turn waits on its provider.
func providerWait(wait time.Duration, why string) protocol.ToolActivity {
	return protocol.ToolActivity{
		Phase: protocol.ToolPhaseStep, Tool: protocol.ToolWaiting, DurationMS: wait.Milliseconds(),
		Detail: fmt.Sprintf("%s; trying again in %ds", why, int(wait.Round(time.Second)/time.Second)),
	}
}
