package main

import (
	"codeterminal/protocol"
)

// maxHistoryTurns caps how many prior turns of a conversation are sent to
// the model, keeping only the most recent ones. A turn-count cap (not a
// token/char budget) is deliberate: it's a simple, safe ceiling without
// pulling a tokenizer into the daemon just for a soft limit. 12 turns is
// generous for a coding-assistant back-and-forth while keeping the request
// bounded regardless of how long the client's session has run.
const maxHistoryTurns = 12

// historyOutcome is what prepareHistory decided for one request: the
// validated, capped messages ready to hand to buildChatMessages, plus
// enough detail to log what happened (mirrors retrievalOutcome/logRetrieval
// in context.go).
type historyOutcome struct {
	Messages       []chatMessage
	ReceivedTurns  int
	KeptTurns      int
	DroppedInvalid int
	Truncated      bool // true when valid turns had to be dropped (oldest first) to fit maxHistoryTurns
}

// prepareHistory validates and caps client-supplied history turns before
// they're allowed anywhere near a model call. It is the daemon's own
// server-side enforcement — it never trusts the client to have already
// capped or sanitized this list.
//
// Validation: only turns with Role exactly "user" or "assistant" survive.
// This is a hard rule, not a convenience filter — it's what prevents a
// client from using History to smuggle in a message claiming system-level
// authority (e.g. Role: "system", or anything else). Prior turns are
// conversation, never system authority, regardless of what a client sends.
//
// Capping: if more than maxHistoryTurns valid turns remain, the OLDEST are
// dropped first so the system prompt's authority and the most recent
// conversation (most relevant to the current prompt) are always preserved.
func prepareHistory(turns []protocol.Turn) historyOutcome {
	outcome := historyOutcome{ReceivedTurns: len(turns)}
	if len(turns) == 0 {
		return outcome
	}

	valid := make([]protocol.Turn, 0, len(turns))
	for _, t := range turns {
		if t.Role != "user" && t.Role != "assistant" {
			outcome.DroppedInvalid++
			continue
		}
		valid = append(valid, t)
	}

	if len(valid) > maxHistoryTurns {
		valid = valid[len(valid)-maxHistoryTurns:]
		outcome.Truncated = true
	}

	outcome.Messages = make([]chatMessage, len(valid))
	for i, t := range valid {
		outcome.Messages[i] = chatMessage{Role: t.Role, Content: t.Content}
	}
	outcome.KeptTurns = len(valid)
	return outcome
}

// logHistory writes one summary line per request describing what history
// processing did, following the existing key=value Printf logging
// convention (see logRetrieval in context.go).
func (s *Server) logHistory(o historyOutcome) {
	if o.ReceivedTurns == 0 {
		return
	}
	s.logger.Printf("history: received=%d kept=%d dropped_invalid=%d truncated=%t", o.ReceivedTurns, o.KeptTurns, o.DroppedInvalid, o.Truncated)
}
