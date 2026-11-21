// The consent channel: how the daemon asks a human whether one tool call may
// run, and how it decides that the answer it got back is actually an answer.
//
// Phase 4 shipped the loop with policy "ask" treated as a refusal, because
// there was no way to ask. This file is that way. Everything here is written
// from one direction: a call runs only if a recognisable, correctly-bound "yes"
// came back. Silence, a timeout, a garbled body, an answer to a different
// question, or an invented decision are all the same outcome -- deny -- because
// the safe reading of anything that is not a yes is no.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"strings"
	"time"

	"mochiii/protocol"
)

// Why one approval was refused. The OUTCOME is identical in every case, and the
// EVENT is not: a user who said no and a client that never answered are
// different facts about the world, and an audit log that could not tell them
// apart would answer "did you approve this?" with a shrug.
const (
	denyByUser      = "user"       // the human said no
	denyByTimeout   = "timeout"    // nothing came back, or the channel broke
	denyByMismatch  = "mismatch"   // the answer did not echo the question
	denyByMalformed = "malformed"  // not recognisably an approval at all
	denyByNoChannel = "no_channel" // there was nobody to ask
)

// approvalDecision is one answer, plus the two things the caller needs to know
// beyond the verb itself.
type approvalDecision struct {
	// Decision is one of the protocol.Approval* constants. It is NEVER
	// ApprovalApprove or ApprovalApproveForTurn on any failure path.
	Decision string
	// Cause names why a denial happened, for the audit record. Empty when the
	// call was approved.
	Cause string
	// Waited is how long the human took. The caller pushes the turn deadline out
	// by this much: mcp.budget.turn_timeout_seconds exists to bound MACHINE
	// work, and killing the turn of a user who paused to read the arguments
	// would punish exactly the care this prompt is asking them to take.
	Waited time.Duration
	Auto   bool // a plain yes the client's auto-approve mode gave (answeredByAutoApprove)
}

func (d approvalDecision) approved() bool {
	return d.Decision == protocol.ApprovalApprove || d.Decision == protocol.ApprovalApproveForTurn ||
		d.Decision == protocol.ApprovalApproveForSpec
}

// approver asks a human to authorise one tool call.
//
// It is an interface so the loop never touches wire mechanics and its tests
// never need a socket, and because the three clients answer over the same
// connection in three quite different ways.
type approver interface {
	Ask(ctx context.Context, req protocol.ToolApprovalRequest) approvalDecision
}

// argumentsDigest is the binding between what a user was shown and what runs.
//
// Taken over the EXACT argument bytes that will be handed to the tool -- not a
// re-marshalled, whitespace-normalised, or key-sorted version of them. The
// point is to prove the two are the same object, and a digest over a tidied
// copy proves something about the tidying instead.
func argumentsDigest(arguments string) string {
	sum := sha256.Sum256([]byte(arguments))
	return hex.EncodeToString(sum[:])
}

// denied is the answer whenever there is nothing better to say.
func denied(cause string, waited time.Duration) approvalDecision {
	return approvalDecision{Decision: protocol.ApprovalDeny, Cause: cause, Waited: waited}
}

// connApprover asks over the SAME connection the turn is streaming on: it
// writes a TokenResponse carrying the request and then reads the client's reply
// off the same decoder every other request came from.
//
// Not safe for concurrent use, and it does not need to be: one connection is
// served by one goroutine (serveConn), and the loop asks one question at a time
// by design.
type connApprover struct {
	enc    *json.Encoder
	dec    *json.Decoder
	lc     *limitedConn
	logger *log.Logger

	// idleTimeout bounds the wait for one answer. ZERO -- what production
	// leaves it -- MEANS NO BOUND: the question waits for as long as the person
	// takes (see Ask). Tests set milliseconds to reach the paths a failed read
	// takes, which a client that goes away reaches in production.
	idleTimeout time.Duration

	// closed latches the first read failure, after which every later Ask denies
	// IMMEDIATELY instead of waiting.
	//
	// This is not an optimisation, it is a correctness fix. encoding/json's
	// Decoder caches its first error and returns that same error from every
	// subsequent Decode, so one failed read -- a client gone, a shutdown --
	// makes this channel permanently unreadable. Without the latch, every
	// remaining call in the turn would try to ask again on a decoder that can
	// no longer produce anything.
	closed bool
}

// Ask sends one approval request and waits for its answer.
//
// It never returns an error. Every failure mode -- unwritable connection, read
// timeout, malformed reply, an answer to some other question -- resolves to a
// denial, because the loop's only sensible response to all of them is the same
// and because an error return here would tempt a caller into treating "we could
// not ask" as something other than "no".
func (a *connApprover) Ask(ctx context.Context, req protocol.ToolApprovalRequest) approvalDecision {
	if a == nil || a.closed {
		return denied(denyByNoChannel, 0)
	}
	// A shutdown mid-turn must not open a question on a connection that is
	// about to be torn down.
	if ctx.Err() != nil {
		a.closed = true
		return denied(denyByNoChannel, 0)
	}

	started := time.Now()

	ask := req
	if err := a.enc.Encode(protocol.TokenResponse{
		ProtocolVersion: protocol.ProtocolVersion,
		ToolApproval:    &ask,
	}); err != nil {
		a.closed = true
		a.logger.Printf("agent: could not ask about %s__%s: %v", req.Server, req.Tool, err)
		return denied(denyByTimeout, 0)
	}

	// THE QUESTION WAITS FOR ITS ANSWER, HOWEVER LONG THAT TAKES (owner's
	// decision, 2026-11-21). It used to give up after five minutes, take silence
	// as a no, and carry on with the turn -- and the TUI, which does not read
	// the connection while its approval panel is up, then let the turn's output
	// back up until the 60-second write deadline ended the turn. The answer the
	// person finally gave went into a closed socket: "✓ approved", then LINK
	// SEVERED, and the edit they approved was never made.
	//
	// A limit was never what ended a wait that should end, and no limit costs
	// none of that:
	//   - a client that goes away -- a terminal closed, a TUI quit, a VS Code
	//     chat panel closed (it aborts its connection) -- is a closed socket, so
	//     this read fails AT ONCE and the call is denied;
	//   - a shutdown ends the wait through cancelReads, below, and the daemon's
	//     drain is bounded by its own grace whatever any connection does;
	//   - a client that cannot answer never declares CapToolApproval, so it is
	//     never asked (a piped or scripted one-shot run, oneshot.go);
	//   - nothing is spent meanwhile: no model call is open while the loop asks,
	//     and the time is given back to the turn's and the task's deadlines.
	// What remains is a client that stays connected and never answers (a
	// suspended terminal): its turn stays paused until it answers, goes away,
	// or the daemon stops. Silence is still never consent -- only a correctly
	// bound yes runs a call.
	//
	// Plus a read budget for the answer we just asked for (see grantReadBudget).
	if a.idleTimeout > 0 {
		defer a.lc.withIdleTimeout(a.idleTimeout)()
	} else {
		defer a.lc.withoutReadDeadline()()
	}
	a.lc.grantReadBudget(maxApprovalResponseBytes)

	// A shutdown arriving mid-question ends the wait. Nothing can interrupt a
	// blocked socket read from the outside, so cancelReads moves the read
	// deadline into the past -- and latches, so a read that re-arms its deadline
	// a moment later cannot undo it (limitedConn.Read checks the latch after
	// arming). Without the latch, a cancellation that landed just before the
	// re-arm was overwritten, and with no deadline left to fall back on the
	// wait would have outlived the shutdown.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			a.lc.cancelReads()
		case <-stopWatch:
		}
	}()

	// READ PAST ANSWERS TO OTHER QUESTIONS, rather than treating the first thing
	// on the wire as this question's answer.
	//
	// A client that answers twice -- a double-fired keypress, a webview that
	// posts on both click and keydown -- leaves a reply in the decoder's buffer.
	// Without this loop that leftover was read as the answer to the NEXT ask,
	// failed the call-id check, and denied it. The user's actual "yes" then
	// became the leftover, and every remaining call in the turn was denied in
	// the same way: one duplicate desynchronised the rest of the turn.
	//
	// Confirmed over a real socket before this existed (H5, 2026-08-01): two
	// answers to call 1, and call 2 came back denied_ask despite being approved.
	//
	// This does NOT weaken any check. A stale answer still cannot approve
	// anything -- verifyApproval runs unchanged on whatever is finally read, and
	// an answer bearing another call's id is skipped rather than believed. What
	// changes is only whether a stale reply is allowed to answer FOR the user.
	//
	// Bounded, because an unbounded skip loop is a client-controlled stall: the
	// read budget bounds bytes and this bounds messages, so a flood of stale
	// answers ends in a denial rather than in a daemon reading forever.
	const maxStaleAnswers = 8

	var (
		decision, cause string
		waited          time.Duration
		raw             json.RawMessage // each Decode replaces it whole (RawMessage.UnmarshalJSON)
	)
	for skipped := 0; ; skipped++ {
		err := a.dec.Decode(&raw)
		waited = time.Since(started)
		if err != nil {
			a.closed = true
			// Client-safe by construction: the tool's own name and a duration. The
			// underlying error stays local, like every other daemon-side detail.
			a.logger.Printf("agent: no approval for %s__%s after %s (%v) -- the client went away or the daemon is stopping; that is a no, and nothing further is asked this turn",
				req.Server, req.Tool, waited.Round(time.Second), err)
			return denied(denyByTimeout, waited)
		}

		decision, cause = verifyApproval(req, raw)

		// Only a WRONG-QUESTION answer is skippable. A malformed body or an
		// invented decision is this client answering this question badly, and
		// that is a denial, not something to wait past.
		if cause != denyByMismatch || !answersAnotherQuestion(req, raw) {
			break
		}
		if skipped >= maxStaleAnswers {
			a.logger.Printf("agent: %d stale approval(s) before an answer to %s__%s; refusing rather than reading on",
				skipped+1, req.Server, req.Tool)
			return denied(denyByMismatch, waited)
		}
		a.lc.grantReadBudget(maxApprovalResponseBytes)
		a.logger.Printf("agent: discarding an approval for a different call while asking about %s__%s",
			req.Server, req.Tool)
	}

	if cause != "" {
		a.logger.Printf("agent: refusing %s__%s: %s", req.Server, req.Tool, cause)
	}
	return approvalDecision{Decision: decision, Cause: cause, Waited: waited, Auto: decision == protocol.ApprovalApprove && answeredByAutoApprove(raw)}
}

// answersAnotherQuestion reports whether raw is a well-formed approval response
// that simply names a different call.
//
// The distinction Ask needs: an answer to a DIFFERENT question is stale and can
// be read past, while an answer to THIS question that fails its digest, or one
// carrying an invented decision, is this client answering badly and must deny.
// Collapsing the two would let a client keep the daemon reading by sending
// well-formed rubbish with the right call id.
func answersAnotherQuestion(req protocol.ToolApprovalRequest, raw json.RawMessage) bool {
	if !isToolApprovalResponse(raw) {
		return false
	}
	var resp protocol.ToolApprovalResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return false
	}
	return resp.CallID != req.CallID
}

// verifyApproval decides whether raw is a yes to THIS question.
//
// Four independent checks, each of which denies on its own:
//
//  1. it is recognisably an approval response at all (exact-key sniff, so
//     {"APPROVAL":true} is not one -- see requestfields.go);
//  2. it echoes the call id, so it is not an answer to some earlier prompt;
//  3. it echoes the argument digest, so the arguments shown and the arguments
//     about to run are provably the same bytes. This is the same class of check
//     editapply's VerifyUnchanged makes for edits, for the same reason: between
//     rendering a thing for a human and acting on it, something must prove the
//     thing did not change;
//  4. the decision is one this protocol defines, and an approving decision also
//     carries Approval:true. An invented verb is not a permission.
//
// ApprovalCancelTurn is honoured whatever Approval says, unlike the two
// approving decisions: stopping is never the unsafe direction, and a client
// that asked to stop should not have to get a boolean right to be listened to.
func verifyApproval(req protocol.ToolApprovalRequest, raw json.RawMessage) (decision, cause string) {
	if !isToolApprovalResponse(raw) {
		return protocol.ApprovalDeny, denyByMalformed
	}
	var resp protocol.ToolApprovalResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return protocol.ApprovalDeny, denyByMalformed
	}
	if resp.CallID != req.CallID {
		return protocol.ApprovalDeny, denyByMismatch
	}
	// EqualFold because hex has two spellings and neither is wrong; the bytes
	// are what matter. This is an integrity echo, not a secret, so a plain
	// comparison is the right tool.
	if !strings.EqualFold(resp.ArgumentsSHA256, req.ArgumentsSHA256) {
		return protocol.ApprovalDeny, denyByMismatch
	}

	switch resp.Decision {
	case protocol.ApprovalCancelTurn:
		return protocol.ApprovalCancelTurn, denyByUser
	case protocol.ApprovalApproveForSpec:
		// An answer to an offer that was never made is not consent to anything:
		// it is refused, not read as a plain yes.
		if req.SpecGrant == "" || !resp.Approval {
			return protocol.ApprovalDeny, denyByMalformed
		}
		return resp.Decision, ""
	case protocol.ApprovalApprove, protocol.ApprovalApproveForTurn:
		if !resp.Approval {
			// The two fields disagree. Reading the permissive one would make the
			// boolean decorative.
			return protocol.ApprovalDeny, denyByMismatch
		}
		return resp.Decision, ""
	case protocol.ApprovalDeny:
		return protocol.ApprovalDeny, denyByUser
	default:
		return protocol.ApprovalDeny, denyByMalformed
	}
}

// answeredByAutoApprove reports whether raw -- an answer already verified as a
// plain yes to the question asked -- came from the client's auto-approve mode,
// which the person turned on, rather than from the person reading this call
// (protocol.ToolApprovalResponse.Auto). The answer is verified exactly as a
// person's is; the mark only changes what the audit log says.
func answeredByAutoApprove(raw json.RawMessage) bool {
	var resp protocol.ToolApprovalResponse
	return json.Unmarshal(raw, &resp) == nil && resp.Auto
}
