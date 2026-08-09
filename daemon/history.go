package main

import (
	"strings"

	"codeterminal/protocol"
)

// maxHistoryTurns caps how many prior turns of a conversation are sent to
// the model, keeping only the most recent ones. A turn-count cap (not a
// token/char budget) is deliberate: it's a simple, safe ceiling without
// pulling a tokenizer into the daemon just for a soft limit. 12 turns is
// generous for a coding-assistant back-and-forth while keeping the request
// bounded regardless of how long the client's session has run.
const maxHistoryTurns = 12

// maxHistoryBytes caps the total CONTENT bytes of history sent to the model,
// alongside the turn-count cap above (Fix 13).
//
// The turn cap alone does not bound the request. Turn COUNT and request SIZE
// are only loosely related: a coding assistant's turns routinely carry pasted
// files, build logs and diffs, so twelve 200KB turns is a 2.4MB request body
// for what the user experiences as a tiny follow-up question. That composes
// badly with the error taxonomy, where it surfaces as `context_too_large` --
// a confusing failure to be told your one-line question is too large.
//
// 256KB is chosen to be generous relative to real conversation (dozens of
// ordinary turns fit well under it) while being roughly an order of magnitude
// below the point where a mainstream model's context window is the binding
// constraint. It is deliberately independent of the RETRIEVAL budget
// (defaultContextBudgetChars, config.go): the two bound different things,
// travel through different code, and were verified independent -- coupling
// them would mean a long conversation silently starving grounding, or a
// grounded turn silently forgetting the conversation.
const maxHistoryBytes = 256 * 1024

// historyOutcome is what prepareHistory decided for one request: the
// validated, capped messages ready to hand to buildChatMessages, plus
// enough detail to log what happened (mirrors retrievalOutcome/logRetrieval
// in context.go).
type historyOutcome struct {
	Messages        []chatMessage
	ReceivedTurns   int
	KeptTurns       int
	DroppedInvalid  int
	Truncated       bool // true when valid turns had to be dropped (oldest first) to fit either cap
	SentBytes       int  // total content bytes of Messages, after both caps
	DroppedByBytes  int  // turns dropped by the byte budget specifically
	AnnotatedCutOff int  // assistant turns marked as cut off (see incompleteHistoryNote)
}

// incompleteHistoryNote is the ONE place a cut-off prior answer is described to
// the model. Given an IncompleteInfo.Reason slug it returns the sentence to
// append to that assistant turn's content, or "" for a reason it does not
// recognize.
//
// THE DAEMON OWNS THIS WORDING, and that is the security property, not a style
// preference. protocol.Turn.Incomplete is a slug precisely so a client cannot
// author text that arrives in the model's context wearing the daemon's
// authority -- the same reason validTurn refuses any role but user/assistant.
// An unrecognized slug therefore returns "" rather than being echoed: a client
// that invents a value gets silence, never a channel.
//
// The wording differs per reason because the REMEDY differs, which is the whole
// argument for the slugs being distinct in the first place (see their doc
// comments in protocol.go). Telling a model that the user cancelled the turn
// and telling it that it ran out of output tokens should not produce the same
// next move: one means stop and wait, the other means it is safe to continue.
func incompleteHistoryNote(reason string) string {
	switch reason {
	case protocol.IncompleteLength:
		return "\n\n[This answer was cut off: it reached the output-token limit before finishing. " +
			"The text above stops mid-thought and is not a complete reply.]"
	case protocol.IncompleteContentFilter:
		return "\n\n[This answer was cut off: the provider's content filter halted it mid-generation. " +
			"The text above is partial.]"
	case protocol.IncompleteBudgetExceeded:
		return "\n\n[This answer was cut off: the request crossed its spending ceiling and was stopped " +
			"mid-generation. The text above is partial.]"
	case protocol.IncompleteAgentBudget:
		return "\n\n[This answer was cut off: the turn hit a per-turn ceiling (iterations, time, or tool " +
			"output) and stopped with whatever had been produced. The work above is real but unfinished.]"
	case protocol.IncompleteUserCancelled:
		return "\n\n[This turn was stopped by the user before it finished. The work above is real, but " +
			"they chose to stop it -- do not simply resume it unless they ask.]"
	case protocol.IncompleteProviderError:
		return "\n\n[This answer was cut off: the model provider failed partway through the turn. " +
			"The work above is real but unfinished.]"
	default:
		return ""
	}
}

// validTurn reports whether a turn may be sent to the model at all.
//
// Two independent rules, both hard:
//
//  1. Role must be exactly "user" or "assistant". This is what prevents a
//     client from using History to smuggle in a message claiming system-level
//     authority (e.g. Role: "system", or anything else). Prior turns are
//     conversation, never system authority, regardless of what a client sends.
//
//  2. Content must not be empty or whitespace-only (Fix 13). A zero-content
//     upstream response used to be persisted as an empty assistant turn, which
//     then re-hydrated into every later session and rode along in every
//     subsequent request -- a permanent, invisible passenger in the context
//     window that teaches the model that empty answers are an acceptable
//     shape. Validating the CONTENT, not just the role, is what stops an
//     already-persisted one from ever reaching the model again. The write-side
//     half of this (never persisting it in the first place) is in
//     server.go's persistTurn; both halves are deliberate, since only the read
//     side can clean up rows written before the write side existed.
func validTurn(t protocol.Turn) bool {
	if t.Role != "user" && t.Role != "assistant" {
		return false
	}
	return strings.TrimSpace(t.Content) != ""
}

// prepareHistory validates and caps client-supplied history turns before
// they're allowed anywhere near a model call. It is the daemon's own
// server-side enforcement — it never trusts the client to have already
// capped or sanitized this list.
//
// Validation: see validTurn.
//
// Capping: two ceilings, both dropping the OLDEST turns first so the system
// prompt's authority and the most recent conversation (most relevant to the
// current prompt) are always preserved — first maxHistoryTurns, then
// maxHistoryBytes. The most recent valid turn is always kept even if it alone
// exceeds the byte budget: dropping it would mean answering a follow-up with
// no idea what it follows, and the connection-level request cap
// (defaultMaxRequestBytes, server_limits.go) already bounds how large any
// single turn can be in the first place. That residual is named rather than
// hidden — one enormous final turn can still exceed maxHistoryBytes.
func prepareHistory(turns []protocol.Turn) historyOutcome {
	outcome := historyOutcome{ReceivedTurns: len(turns)}
	if len(turns) == 0 {
		return outcome
	}

	valid := make([]protocol.Turn, 0, len(turns))
	for _, t := range turns {
		if !validTurn(t) {
			outcome.DroppedInvalid++
			continue
		}
		// Mark a cut-off prior answer as cut off, BEFORE either cap runs, so
		// the note sits inside the byte budget it costs instead of being
		// smuggled past a ceiling that has already been measured. `t` is a
		// range copy, so this never touches the caller's slice.
		//
		// Assistant turns only: "cut off" is a fact about generation, and a
		// user turn claiming it is meaningless -- so it is ignored rather than
		// honored, keeping the annotation reachable from exactly one direction.
		if t.Role == "assistant" {
			if note := incompleteHistoryNote(t.Incomplete); note != "" {
				t.Content += note
				outcome.AnnotatedCutOff++
			}
		}
		valid = append(valid, t)
	}

	if len(valid) > maxHistoryTurns {
		valid = valid[len(valid)-maxHistoryTurns:]
		outcome.Truncated = true
	}

	// Byte budget, applied newest-first so the survivors are the most recent
	// turns, then restored to oldest-first order for the model.
	keepFrom := len(valid)
	used := 0
	for i := len(valid) - 1; i >= 0; i-- {
		size := len(valid[i].Content)
		if i < len(valid)-1 && used+size > maxHistoryBytes {
			break
		}
		used += size
		keepFrom = i
	}
	if keepFrom > 0 {
		outcome.DroppedByBytes = keepFrom
		outcome.Truncated = true
		valid = valid[keepFrom:]
	}

	outcome.Messages = make([]chatMessage, len(valid))
	for i, t := range valid {
		outcome.Messages[i] = chatMessage{Role: t.Role, Content: t.Content}
	}
	outcome.KeptTurns = len(valid)
	outcome.SentBytes = used
	return outcome
}

// buildHistoryInfo translates prepareHistory's decision into the wire-level
// report sent back to the client, mirroring buildGroundingInfo (context.go).
//
// Truncation used to be invisible: retrieval reported its own Truncated flag
// while history silently dropped turn 1, so a client asking "what was my first
// question?" got a confidently wrong answer with nothing to indicate the turn
// had been dropped on the way out. Returns nil when there was no history at
// all, so the field is simply absent for a first turn.
func buildHistoryInfo(o historyOutcome) *protocol.HistoryInfo {
	if o.ReceivedTurns == 0 {
		return nil
	}
	return &protocol.HistoryInfo{
		Turns:          o.KeptTurns,
		Truncated:      o.Truncated,
		DroppedInvalid: o.DroppedInvalid,
	}
}

// logHistory writes one summary line per request describing what history
// processing did, following the existing key=value Printf logging
// convention (see logRetrieval in context.go).
func (s *Server) logHistory(o historyOutcome) {
	if o.ReceivedTurns == 0 {
		return
	}
	s.logger.Printf("history: received=%d kept=%d dropped_invalid=%d dropped_by_bytes=%d bytes=%d truncated=%t cut_off_marked=%d",
		o.ReceivedTurns, o.KeptTurns, o.DroppedInvalid, o.DroppedByBytes, o.SentBytes, o.Truncated, o.AnnotatedCutOff)
}
