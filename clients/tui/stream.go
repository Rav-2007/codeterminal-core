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

// streamDoneMsg signals the stream finished successfully.
type streamDoneMsg struct{}

// streamErrMsg signals the stream failed (connect/handshake error, a
// daemon-side error, or a malformed response). A deliberate cancellation
// (the user quitting mid-stream) does NOT produce this message — see
// streamPrompt.
type streamErrMsg struct{ err error }

// startStream launches the daemon round-trip for prompt in its own
// goroutine (which pushes tokenMsg/streamDoneMsg/streamErrMsg values into ch
// as the response streams in) and returns a Cmd that waits for the first of
// those values. Bubble Tea's Update loop never blocks on the network: this
// Cmd runs in its own goroutine managed by the Bubble Tea runtime, and
// Update only ever sees a message once one is ready.
func startStream(ctx context.Context, clientName, workspace, prompt string, ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		go streamPrompt(ctx, clientName, workspace, prompt, ch)
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
func streamPrompt(ctx context.Context, clientName, workspace, prompt string, ch chan tea.Msg) {
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
		if tok.Token != "" {
			ch <- tokenMsg(tok.Token)
		}
		if tok.Done {
			ch <- streamDoneMsg{}
			return
		}
	}
}
