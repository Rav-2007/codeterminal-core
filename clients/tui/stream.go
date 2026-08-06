package main

import (
	"context"
	"errors"
	"io"

	tea "github.com/charmbracelet/bubbletea"

	"codeterminal/protocol"
)

// tokenMsg is one streamed token from the model.
type tokenMsg string

// groundingMsg carries the daemon's report of whether/how this turn was
// grounded in local retrieved context (see protocol.GroundingInfo). It
// arrives before any tokens, as its own message — see streamPrompt.
type groundingMsg struct{ info *protocol.GroundingInfo }

// redactionsMsg carries the kinds of secret-shaped text the daemon's
// heuristic scrubber redacted from the prompt before sending it to the
// model (see protocol.TokenResponse.Redactions, daemon/scrub.go). Like
// groundingMsg, it arrives at most once, before any tokens — see
// streamPrompt. Never carries a matched value, only kind labels.
type redactionsMsg struct{ kinds []string }

// degradedMsg carries the daemon's report of which subsystems are currently
// running in a reduced mode (see protocol.TokenResponse.Degraded). Like
// groundingMsg it arrives at most once, before any tokens, on the same
// message as the grounding report.
//
// It exists because the daemon could serve a turn with hybrid retrieval
// silently collapsed to semantic-only, or with conversation memory silently
// not persisting, and the reply on the wire was identical to a healthy one.
// Rendering it is the whole point: a field that arrives correctly and is
// dropped by every client is a wire change, not a fix (see Fix 13).
type degradedMsg struct{ items []protocol.Degradation }

// providerMsg carries the upstream provider the daemon observed serving this
// turn (see protocol.TokenResponse.Provider). Unlike groundingMsg/degradedMsg
// it does NOT arrive before tokens: the provider is only known once the model
// response begins, so it arrives on its own message at or before the first
// token, at most once. Absence is normal (OpenRouter does not guarantee the
// field) and is shown as nothing, never as an error. It is a plain "served by
// X" fact — never a fallback claim or a ZDR judgement (see the field comment).
type providerMsg struct{ provider string }

// reasoningMsg carries a chunk of a reasoning-tier model's thinking tokens
// (see protocol.TokenResponse.Reasoning). It arrives on messages of its own,
// interleaved before/among the content tokens, and is accumulated SEPARATELY
// from the answer -- the daemon streamed these all along, but the client
// decoded and dropped them, so the user watched an empty screen (~907 ms of
// dead air observed) while the model thought, then got the answer in one burst.
type reasoningMsg struct{ text string }

// historyMsg carries the daemon's report of what it did with the conversation
// turns the client sent (see protocol.HistoryInfo). It rides on the same
// pre-token message as grounding. The client cares about Truncated: whether the
// oldest turns were dropped on the way to the model.
type historyMsg struct{ info *protocol.HistoryInfo }

// incompleteMsg carries the daemon's report that the model's answer was cut
// off rather than finishing on its own (see protocol.TokenResponse.Incomplete).
// It rides on the final Done message, so it is emitted immediately before
// streamDoneMsg -- a partial answer whose stream ended for a "length" cutoff
// used to arrive as a Done byte-identical to a complete one, rendering a
// sentence that stops mid-word as if it were the whole reply.
type incompleteMsg struct{ info *protocol.IncompleteInfo }

// toolApprovalMsg is the daemon asking permission to run one tool call, and
// the channel it is waiting for the answer on.
//
// It is the ONLY message here that expects something back. The stream goroutine
// blocks until reply carries a protocol.Approval* decision (or the turn is
// cancelled), which is what makes the guarantee literal rather than aspirational:
// the daemon does not proceed, and the tool does not run, while this sits on
// screen.
//
// reply is BUFFERED with capacity 1 and receives exactly one send, so answering
// can happen inline in Update without ever blocking Bubble Tea's loop.
type toolApprovalMsg struct {
	req   protocol.ToolApprovalRequest
	reply chan string
}

// toolActivityMsg narrates one step of an agent turn (see
// protocol.ToolActivity). Purely observational -- nothing in it needs an
// answer. It exists because an agent turn can take many seconds, and a client
// that showed only a spinner for that long is indistinguishable from a broken
// one.
type toolActivityMsg struct{ activity protocol.ToolActivity }

// streamDoneMsg signals the stream finished successfully.
type streamDoneMsg struct{}

// streamErrMsg signals the stream failed (connect/handshake error, a
// daemon-side error, or a malformed response). A deliberate cancellation
// (the user quitting mid-stream) does NOT produce this message — see
// streamPrompt.
type streamErrMsg struct{ err error }

// resetErrMsg signals that clearing conversation memory on the daemon (the
// network half of ctrl+n — see clearConversation in chat.go) failed. There
// is no corresponding "ok" message: the live transcript is already cleared
// synchronously before this Cmd is even fired, so success has nothing left
// to report.
type resetErrMsg struct{ err error }

// resetOkMsg is sent on a successful daemon-side reset. Update has no case
// for it (a successful reset needs no visible reaction — the transcript was
// already cleared) — it exists purely so startReset's blocking receive on
// ch always has something to unblock on.
type resetOkMsg struct{}

// startReset fires PromptRequest{Reset: true} at the daemon over a fresh
// connection (the wire protocol is one prompt per connection, same as
// startStream), in its own goroutine, returning a Cmd that reports only
// failure via resetErrMsg.
func startReset(ctx context.Context, clientName string, ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		go resetHistoryOnDaemon(ctx, clientName, ch)
		return <-ch
	}
}

// resetHistoryOnDaemon sends a Reset request and waits for the daemon's
// single TokenResponse. Mirrors streamPrompt's connect/encode/decode
// pattern but without a streaming loop, since a reset gets exactly one
// reply.
func resetHistoryOnDaemon(ctx context.Context, clientName string, ch chan tea.Msg) {
	sess, err := connectToDaemon(clientName)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		ch <- resetErrMsg{err}
		return
	}
	defer sess.Close()

	if err := sess.enc.Encode(protocol.PromptRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		Reset:           true,
	}); err != nil {
		if ctx.Err() != nil {
			return
		}
		ch <- resetErrMsg{err}
		return
	}

	var tok protocol.TokenResponse
	if err := sess.dec.Decode(&tok); err != nil {
		if ctx.Err() != nil {
			return
		}
		ch <- resetErrMsg{err}
		return
	}
	if tok.Error != "" {
		ch <- resetErrMsg{errors.New(tok.Error)}
		return
	}
	ch <- resetOkMsg{}
}

// startStream launches the daemon round-trip for prompt in its own
// goroutine (which pushes tokenMsg/streamDoneMsg/streamErrMsg values into ch
// as the response streams in) and returns a Cmd that waits for the first of
// those values. Bubble Tea's Update loop never blocks on the network: this
// Cmd runs in its own goroutine managed by the Bubble Tea runtime, and
// Update only ever sees a message once one is ready.
//
// history is the prior conversation (oldest first, built by buildHistory in
// chat.go), sent alongside prompt so the daemon/model can see it — see
// protocol.PromptRequest.History. A nil/empty history is exactly today's
// behavior.
//
// promptKind is the wire value parsePromptKind (chat.go) resolved from the
// raw input, or "" for ordinary prompts — see protocol.PromptRequest.PromptKind.
func startStream(ctx context.Context, clientName, workspace, prompt, promptKind, preferredTier string, history []protocol.Turn, ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		go streamPrompt(ctx, clientName, workspace, prompt, promptKind, preferredTier, history, ch)
		return <-ch
	}
}

// askForApproval hands the request to the UI and waits for the user's answer,
// then writes it back on the SAME connection the turn is streaming over.
// Reports whether streaming should continue.
//
// BLOCKING HERE IS CORRECT. This runs in streamPrompt's own goroutine, never in
// Bubble Tea's Update loop, and blocking is the whole point: the daemon is
// holding a tool call open until a human answers, so the client must too. The
// ctx case is not optional -- a user quitting mid-prompt cancels the context,
// and the conn-closing watcher in streamPrompt cannot unblock a channel
// receive, so without it this goroutine would leak for the life of the process.
func askForApproval(ctx context.Context, sess *daemonSession, req protocol.ToolApprovalRequest, ch chan tea.Msg) bool {
	reply := make(chan string, 1)
	select {
	case ch <- toolApprovalMsg{req: req, reply: reply}:
	case <-ctx.Done():
		return false
	}

	var decision string
	select {
	case decision = <-reply:
	case <-ctx.Done():
		return false
	}

	// CallID and ArgumentsSHA256 are echoed back exactly as they arrived. The
	// daemon re-checks both before dispatching, so this is what proves the
	// answer belongs to the question the user was actually shown -- a client
	// that recomputed the digest from its own copy would be attesting to its own
	// rendering rather than to the daemon's bytes.
	if err := sess.enc.Encode(protocol.ToolApprovalResponse{
		ProtocolVersion: protocol.ProtocolVersion,
		Approval:        decision == protocol.ApprovalApprove || decision == protocol.ApprovalApproveForTurn,
		CallID:          req.CallID,
		ArgumentsSHA256: req.ArgumentsSHA256,
		Decision:        decision,
	}); err != nil {
		if ctx.Err() != nil {
			return false
		}
		ch <- streamErrMsg{err}
		return false
	}
	return true
}

// waitForNext waits for the next message on an already-started stream.
// Update re-issues this after every tokenMsg — that's what keeps draining
// ch without Update itself ever blocking: each wait happens inside its own
// Cmd goroutine, not inside Update.
func waitForNext(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

// streamPrompt opens a fresh connection (the wire protocol is one prompt
// per connection — see daemonconn.go), sends prompt (plus workspace, so the
// daemon can flag a mismatch against its own actual grounding workspace —
// see protocol.GroundingInfo.WorkspaceMismatch), and pushes every
// grounding/token/done/error onto ch.
//
// ctx has no direct way to interrupt an in-flight, blocked net.Conn read, so
// a small watcher goroutine closes the connection when ctx is cancelled —
// that's what actually unblocks a pending Decode call. A deliberate
// cancellation is recognized via ctx.Err() and this function returns
// quietly (no streamErrMsg): the user chose to quit, that's not a failure,
// and it leaves nothing behind reading a dead socket.
func streamPrompt(ctx context.Context, clientName, workspace, prompt, promptKind, preferredTier string, history []protocol.Turn, ch chan tea.Msg) {
	sess, err := connectToDaemon(clientName, protocol.CapToolApproval)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		ch <- streamErrMsg{err}
		return
	}
	defer sess.Close()

	unblock := make(chan struct{})
	defer close(unblock)
	go func() {
		select {
		case <-ctx.Done():
			sess.Close()
		case <-unblock:
		}
	}()

	if err := sess.enc.Encode(protocol.PromptRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		Prompt:          prompt,
		Workspace:       workspace,
		History:         history,
		PromptKind:      promptKind,
		Tier:            preferredTier,
	}); err != nil {
		if ctx.Err() != nil {
			return
		}
		ch <- streamErrMsg{err}
		return
	}

	for {
		var tok protocol.TokenResponse
		if err := sess.dec.Decode(&tok); err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, io.EOF) {
				ch <- streamDoneMsg{}
				return
			}
			ch <- streamErrMsg{err}
			return
		}
		if tok.Error != "" {
			ch <- streamErrMsg{errors.New(tok.Error)}
			return
		}
		if tok.Grounding != nil {
			ch <- groundingMsg{tok.Grounding}
		}
		if tok.History != nil {
			ch <- historyMsg{tok.History}
		}
		if len(tok.Redactions) > 0 {
			ch <- redactionsMsg{tok.Redactions}
		}
		if tok.Reasoning != "" {
			ch <- reasoningMsg{tok.Reasoning}
		}
		if len(tok.Degraded) > 0 {
			ch <- degradedMsg{tok.Degraded}
		}
		if tok.Provider != "" {
			ch <- providerMsg{tok.Provider}
		}
		if tok.ToolActivity != nil {
			ch <- toolActivityMsg{*tok.ToolActivity}
		}
		if tok.ToolApproval != nil {
			if !askForApproval(ctx, sess, *tok.ToolApproval, ch) {
				return
			}
		}
		if tok.Token != "" {
			ch <- tokenMsg(tok.Token)
		}
		if tok.Done {
			// Rides on the final Done message; emit it before streamDoneMsg so
			// the "answer cut off" notice lands right under the just-finished
			// (partial) answer, ahead of any edit-review chrome.
			if tok.Incomplete != nil {
				ch <- incompleteMsg{tok.Incomplete}
			}
			ch <- streamDoneMsg{}
			return
		}
	}
}
