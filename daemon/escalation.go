package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mochiii/protocol"
)

// THE LADDER'S LAST RESORTS, BEFORE STOPPING: a stronger model, and the model's
// own account of why it keeps failing.
//
// ESCALATION is ReAct's back-off -- when one way of working stops yielding,
// switch to another rather than repeat it -- applied to the model itself. It is
// OFF unless the user names a tier (mcp.correction.escalate_to): a stronger
// model costs more per call, and which one is worth it is theirs to say. A
// tier is a model of the provider in use (a provider's own models replace the
// shipped tiers when its key is connected), so the key never goes anywhere it
// was not already going.

// minEscalationWindow is the least context window a tier needs to take over a
// segment: a segment's tool output alone may reach 512 KB, near 130,000 tokens,
// and a tier that cannot hold its own opening would fail every call.
const minEscalationWindow = 100_000

// escalationTarget resolves the tier a task may move to, or says why it may
// not: no such active tier, the model already in use, or too small a window.
func (s *Server) escalationTarget(tier, current string) (model string, routing providerRouting, why string) {
	cfg := s.tierConfig()
	d := s.route("", tier)
	switch t, ok := cfg.Tiers[tier]; {
	case !ok || !t.Active || d.Tier != tier:
		return "", providerRouting{}, "there is no active tier of that name"
	case d.Slug == current:
		return "", providerRouting{}, "it is the model the task already runs on"
	case t.ContextWindow > 0 && t.ContextWindow < minEscalationWindow:
		return "", providerRouting{}, fmt.Sprintf("its context window of %d tokens is smaller than a segment needs (%d)",
			t.ContextWindow, minEscalationWindow)
	}
	return d.Slug, cfg.routingFor(d.Tier), ""
}

// providerTrouble reports a failure another model might not have: throttling,
// an outage, a request too large for this model's window, a model gone. Not a
// rejected key or a spent allowance -- the same key would meet those again.
func providerTrouble(err error) bool {
	switch asModelError(err).Class {
	case ClassRateLimited, ClassUpstreamUnavailable, ClassContextTooLarge, ClassUnavailableTier:
		return true
	}
	return false
}

// reflectionAsk is the one question the judge puts to the model outright.
const reflectionAsk = "Do not call any tool. The same failure has come back several times -- your task state above " +
	"lists the checks. In at most three sentences: why does it keep failing, and what will you do differently next?"

// reflect asks the model, in one tool-free call at a segment's end, why a
// failure keeps coming back: Reflexion's self-reflection, made only when the
// ladder reached it (control) and the model had not already said why in its
// own words (said). The answer is kept with the failure's frame, so every
// later segment opens with it. One call, counted against the run's budget,
// and made only while the budget has room for it.
func (s *Server) reflect(ctx context.Context, run *taskRun, system string, history []chatMessage, segment int,
	model string, routing providerRouting) {
	if !run.correcting() || !run.judge.wantReflection {
		return
	}
	run.judge.wantReflection = false
	c := run.ledger.Correction
	if c == nil || len(c.Frames) == 0 || c.Frames[len(c.Frames)-1].Reflection != "" || !run.roomForOneCall() {
		return
	}
	key, base := s.credentials()
	var out strings.Builder
	_, err := streamWithRetry(ctx, base, key, model, run.segmentMessages(system, history, segment, reflectionAsk), nil, routing,
		func(tok string) error { out.WriteString(tok); return nil }, nil, nil, nil, s.logger)
	run.doneCalls++
	run.reflections++
	if err != nil {
		s.logger.Printf("task %s: asking why the failure keeps coming back failed: %v", run.ledger.ID, err)
		return
	}
	if text := strings.TrimSpace(out.String()); text != "" {
		c.Frames[len(c.Frames)-1].Reflection = s.cleanForLedger(truncateRunes(text, maxReflectionRunes))
	}
}

// status is the TaskStatus a client shows for this run: the ledger's, and what
// the judge has done when it acts -- code put back, checks run again, a
// stronger model -- so a user is never left to guess why files changed.
func (r *taskRun) status(segment int) protocol.TaskStatus {
	st := r.ledger.status(r.budget, r.spent(), segment)
	if !r.correcting() {
		return st
	}
	var did []string
	if r.restores > 0 {
		did = append(did, plural(r.restores, "restore"))
	}
	if r.rechecks > 0 {
		did = append(did, plural(r.rechecks, "re-check"))
	}
	if r.escalated != "" {
		did = append(did, "moved to "+r.escalated)
	}
	if len(did) > 0 {
		st.Judge = strings.Join(did, " · ")
	}
	return st
}

// roomForOneCall reports that the run's budget can take one more model call:
// no stop decided, calls and dollars under their limits, time left.
func (r *taskRun) roomForOneCall() bool {
	spent := r.spent()
	return r.budgetStop == nil && r.stopWhy == "" && !r.closed() &&
		spent.Calls < r.budget.calls && spent.USD < r.budget.usd && time.Now().Before(r.deadline)
}
