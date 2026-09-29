package main

import (
	"context"
	"encoding/json"

	"mochiii/protocol"
)

// turnWriter is the single write path for everything a turn streams, and the
// reason it exists is the stop key the clients have (interruptTurn in
// clients/tui/chat.go).
//
// An interrupt reaches the daemon as a closed connection -- there is no
// "cancel" message on this wire, and there could not usefully be one: the
// daemon is not reading from this connection except while it is asking an
// approval question. What it has instead is the write side, and a write that
// fails is proof the client is gone. A failed write therefore CANCELS THE TURN:
// runAgentLoop checks ctx.Err() between iterations and between every tool call,
// and a model call in flight is cancelled mid-stream.
//
// BOTH PATHS USE IT. The agent turn had this first; the single-turn path kept
// dropping the error from its reasoning write, so a client that hung up while
// the model was still thinking was noticed only at the first answer token --
// after every thinking token had been generated and billed. MEASURED by the
// 2026-09-29 cost audit: esc stopped an agent turn in 0.6 s and did not stop a
// plain one at all until the answer began.
type turnWriter struct {
	enc    *json.Encoder
	cancel context.CancelFunc
	// gone latches the failed write for the LOG, so an interrupt is not reported
	// as a turn that failed. "context canceled" is what shutdown looks like too,
	// and an operator reading the log should not have to guess which happened.
	gone bool
}

// newTurnWriter returns the turn's own cancellable context and its writer. The
// caller defers w.done().
func newTurnWriter(ctx context.Context, enc *json.Encoder) (context.Context, *turnWriter) {
	ctx, cancel := context.WithCancel(ctx)
	return ctx, &turnWriter{enc: enc, cancel: cancel}
}

// write sends one message. A failure means the answer has nowhere left to go --
// either the peer hung up, or it has not drained a byte for a full idle
// timeout, and neither is a state to keep working through.
func (w *turnWriter) write(resp protocol.TokenResponse) error {
	if err := w.enc.Encode(resp); err != nil {
		w.gone = true
		w.cancel()
		return err
	}
	return nil
}

// clientGone reports whether a write has failed this turn.
func (w *turnWriter) clientGone() bool { return w.gone }

// done releases the turn's context.
func (w *turnWriter) done() { w.cancel() }
