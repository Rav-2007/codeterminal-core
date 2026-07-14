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
func startStream(ctx context.Context, clientName, workspace, prompt string, history []protocol.Turn, ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		go streamPrompt(ctx, clientName, workspace, prompt, history, ch)
		return <-ch
	}
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
func streamPrompt(ctx context.Context, clientName, workspace, prompt string, history []protocol.Turn, ch chan tea.Msg) {
	sess, err := connectToDaemon(clientName)
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
		if len(tok.Redactions) > 0 {
			ch <- redactionsMsg{tok.Redactions}
		}
		if tok.Token != "" {
			ch <- tokenMsg(tok.Token)
		}
		if tok.Done {
			ch <- streamDoneMsg{}
			return
		}
	}
}
