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

// editProposalsMsg carries the edit blocks the DAEMON parsed out of the
// just-completed response (see protocol.TokenResponse.EditProposals). Like
// incompleteMsg it rides the final Done message, and it is delivered before
// streamDoneMsg so the review that streamDoneMsg triggers can use it.
//
// The TUI used to ignore this field entirely and re-parse the assistant text
// locally, which looked equivalent and was not. The daemon merges TWO sources
// into it (daemon/agentturn.go:232): blocks the model wrote as text, AND edits
// it filed through the propose_edit tool. Only the first of those is in the
// assistant text, so in agent mode every propose_edit proposal was invisible
// here -- while daemon/agentturn.go's own comment asserts "there is no path by
// which an agent turn changes a file without the user seeing a diff first".
// That invariant was true of the wire and false of this client.
type editProposalsMsg struct{ blocks []protocol.EditBlockWire }

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
	defer func() { _ = sess.Close() }() // see daemonSession.Close

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
//
// pipeline is the specialist shape for this turn only, or nil to use whatever
// the daemon has configured — which is what every ordinary prompt sends. It is
// set from an explicit command and never inferred from what the question looks
// like; see protocol.PromptRequest.Pipeline for why that is a measured decision
// rather than caution.
func startStream(ctx context.Context, clientName, workspace, prompt, promptKind, mode, preferredTier string, pipeline []string, history []protocol.Turn, ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		go streamPrompt(ctx, clientName, workspace, prompt, promptKind, mode, preferredTier, pipeline, history, ch)
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
		deliver(ctx, ch, streamErrMsg{err})
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

// deliver hands one message to the UI, and gives up if the turn is cancelled
// first. Reports whether it was delivered.
//
// EVERY SEND IN streamPrompt MUST GO THROUGH THIS, because ch is unbuffered and
// the UI only reads it while it still cares. Interrupting a turn (esc/ctrl+c --
// see interruptTurn in chat.go) stops re-issuing waitForNext, so a bare `ch <-`
// after that point blocks forever: one leaked goroutine, holding the turn's
// buffers, for every interrupt in a session that may run for hours. That was
// invisible while cancellation only ever happened on the way out of the
// process; an interrupt key makes it a live leak.
func deliver(ctx context.Context, ch chan tea.Msg, msg tea.Msg) bool {
	select {
	case ch <- msg:
		return true
	case <-ctx.Done():
		return false
	}
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
func streamPrompt(ctx context.Context, clientName, workspace, prompt, promptKind, mode, preferredTier string, pipeline []string, history []protocol.Turn, ch chan tea.Msg) {
	sess, err := connectToDaemon(clientName, protocol.CapToolApproval)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		deliver(ctx, ch, streamErrMsg{err})
		return
	}
	defer func() { _ = sess.Close() }() // see daemonSession.Close

	unblock := make(chan struct{})
	defer close(unblock)
	go func() {
		select {
		case <-ctx.Done():
			// Racing the read below on purpose: closing under it is what
			// unblocks it. An error here is the expected outcome of that race,
			// not information.
			_ = sess.Close()
		case <-unblock:
		}
	}()

	if err := sess.enc.Encode(protocol.PromptRequest{
		ProtocolVersion: protocol.ProtocolVersion,
		Prompt:          prompt,
		Workspace:       workspace,
		History:         history,
		PromptKind:      promptKind,
		Mode:            mode,
		Tier:            preferredTier,
		Pipeline:        pipeline,
	}); err != nil {
		if ctx.Err() != nil {
			return
		}
		deliver(ctx, ch, streamErrMsg{err})
		return
	}

	for {
		var tok protocol.TokenResponse
		if err := sess.dec.Decode(&tok); err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, io.EOF) {
				deliver(ctx, ch, streamDoneMsg{})
				return
			}
			deliver(ctx, ch, streamErrMsg{err})
			return
		}
		if tok.Error != "" {
			deliver(ctx, ch, streamErrMsg{errors.New(tok.Error)})
			return
		}
		if tok.Grounding != nil && !deliver(ctx, ch, groundingMsg{tok.Grounding}) {
			return
		}
		if tok.History != nil && !deliver(ctx, ch, historyMsg{tok.History}) {
			return
		}
		if len(tok.Redactions) > 0 && !deliver(ctx, ch, redactionsMsg{tok.Redactions}) {
			return
		}
		if tok.Reasoning != "" && !deliver(ctx, ch, reasoningMsg{tok.Reasoning}) {
			return
		}
		if len(tok.Degraded) > 0 && !deliver(ctx, ch, degradedMsg{tok.Degraded}) {
			return
		}
		if tok.Provider != "" && !deliver(ctx, ch, providerMsg{tok.Provider}) {
			return
		}
		if tok.ToolActivity != nil && !deliver(ctx, ch, toolActivityMsg{*tok.ToolActivity}) {
			return
		}
		if tok.ToolApproval != nil {
			if !askForApproval(ctx, sess, *tok.ToolApproval, ch) {
				return
			}
		}
		if tok.Token != "" && !deliver(ctx, ch, tokenMsg(tok.Token)) {
			return
		}
		if tok.Done {
			// Rides on the final Done message; emit it before streamDoneMsg so
			// the "answer cut off" notice lands right under the just-finished
			// (partial) answer, ahead of any edit-review chrome.
			if tok.Incomplete != nil && !deliver(ctx, ch, incompleteMsg{tok.Incomplete}) {
				return
			}
			// Before streamDoneMsg, which is what starts the review: the
			// proposals have to be in hand by the time it arrives.
			if len(tok.EditProposals) > 0 && !deliver(ctx, ch, editProposalsMsg{tok.EditProposals}) {
				return
			}
			deliver(ctx, ch, streamDoneMsg{})
			return
		}
	}
}
