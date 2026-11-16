package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// WHAT THE MODEL IS TOLD WHEN A CALL TO A SERVER GOES WRONG.
//
// Three different facts used to arrive as one of two sentences ("the tool's
// server is unavailable", "the tool failed to run"), and the loop could not tell
// a server that had died from a tool that did not exist: after a crash every
// later call was answered 'there is no tool called "x" available in this turn'.
// A model told its tool does not exist stops using a server that is, at worst,
// one restart away.
//
// Each type below is one fact and carries the words for it. ModelMessage is
// written by this daemon from numbers and names it holds -- never from text the
// server supplied -- so it can go to the model as it is. A server's own error
// text takes the other route (protocolErrorResult), where it is scrubbed and
// fenced like any other third-party output.

// ErrResultTooLarge marks a call that failed because the server's one message
// in reply was over the limit. The server is still connected.
var ErrResultTooLarge = errors.New("mcp result too large")

// ResultTooLargeError is that failure, with the numbers.
type ResultTooLargeError struct {
	Server, Tool string
	Size, Limit  int64 // bytes; Size is 0 when the server's own claim was unreadable
}

func (e *ResultTooLargeError) Error() string {
	return fmt.Sprintf("%v: server %s sent %d bytes in one message for %s; the limit is %d", ErrResultTooLarge, e.Server, e.Size, e.Tool, e.Limit)
}

func (e *ResultTooLargeError) Is(target error) bool { return target == ErrResultTooLarge }

// ModelMessage is the whole of what the model is told.
func (e *ResultTooLargeError) ModelMessage() string {
	what := fmt.Sprintf("the server %q", e.Server)
	if e.Tool != "" {
		what = fmt.Sprintf("the tool %q on the server %q", e.Tool, e.Server)
	}
	size := "more than the limit"
	if e.Size > 0 {
		size = fmt.Sprintf("%d bytes", e.Size)
	}
	return fmt.Sprintf("the result from %s was too large: it came back as one message of %s and the limit is %d bytes, "+
		"so it was dropped. The server is still running and its other tools work. Ask for less (a smaller part, "+
		"a narrower query) and try again.", what, size, e.Limit)
}

// tooLargeFrom reads the numbers frameReader put in its answer. They are only
// ever printed as numbers, so a server that forged the code gets nothing more
// than a "too large" notice.
func tooLargeFrom(werr *jsonrpc.Error, server, tool string) *ResultTooLargeError {
	e := &ResultTooLargeError{Server: server, Tool: tool}
	var d struct {
		Size  int64 `json:"size"`
		Limit int64 `json:"limit"`
	}
	if json.Unmarshal(werr.Data, &d) == nil {
		e.Size, e.Limit = d.Size, d.Limit
	}
	return e
}

// ServerFailure is a stdio server that stopped, and what became of it.
//
// Stopped says the call that failed is the one that was in flight when the
// process went (so it may have done some of its work); without it the call never
// reached a server that was down. Back says the server was started again and
// answers: then a later call can work. A server that is not Back stays down for
// the rest of the turn, and Why says why.
type ServerFailure struct {
	Server  string
	Tool    string // the tool whose call this was; "" for a listing
	Stopped bool
	Back    bool
	Why     string
}

// The reasons a server is left down. Words for the model.
const (
	whySecondStop    = "it stopped again after the one restart allowed in a turn"
	whyNotRestarted  = "it stopped and could not be started again"
	whyNoToolListing = "it stopped, and the restarted server did not answer when asked for its tools"
)

func (f *ServerFailure) Error() string {
	state := "down for this turn: " + f.Why
	if f.Back {
		state = "restarted"
	}
	if f.Stopped {
		return fmt.Sprintf("%v: server %s stopped while running %s (%s)", ErrServerUnavailable, f.Server, f.Tool, state)
	}
	return fmt.Sprintf("%v: server %s is %s", ErrServerUnavailable, f.Server, state)
}

// Unwrap keeps every ServerFailure an ErrServerUnavailable, as these calls always
// were to code that tests for it (the loop's trace class, the degraded notice):
// the call failed because its server was not there, whether or not it is again.
func (f *ServerFailure) Unwrap() error { return ErrServerUnavailable }

// ModelMessage is the whole of what the model is told.
func (f *ServerFailure) ModelMessage() string {
	switch {
	case f.Stopped && f.Back:
		return fmt.Sprintf("the server %q stopped while running the tool %q, so that call did not finish and "+
			"what it did is not certain. The server was restarted and is available again: its tools can be "+
			"called now.", f.Server, f.Tool)
	case f.Stopped:
		return fmt.Sprintf("the server %q stopped while running the tool %q, so that call did not finish and "+
			"what it did is not certain. The server is unavailable for the rest of this turn: %s. Do not call "+
			"its tools again in this turn.", f.Server, f.Tool, f.Why)
	case f.Tool != "":
		return fmt.Sprintf("the server %q is unavailable for the rest of this turn: %s. The tool %q is not "+
			"missing, but it cannot run now. Do not call this server's tools again in this turn.",
			f.Server, f.Why, f.Tool)
	}
	return fmt.Sprintf("the server %q is unavailable for the rest of this turn: %s. Its tools exist but "+
		"cannot run now. Do not call them again in this turn.", f.Server, f.Why)
}

// ModelFacing is an error that knows what the model should be told about it.
// The loop uses it for any error that carries one.
type ModelFacing interface {
	ModelMessage() string
}

// fromTheConnection reports whether a JSON-RPC error code is one the SDK itself
// makes up when the CONNECTION fails -- client closing (-32003), server closing
// (-32004), rejected by the transport (-32005) -- and not one a server sent. They
// travel as JSON-RPC errors too, so the code is all that tells them apart. A call
// that fails with one has had no answer from the server and stays an error of the
// call, as before: found by the first version of this, which told the model that
// a remote server which had gone away "refused the call with a protocol error".
func fromTheConnection(code int64) bool { return code >= -32005 && code <= -32003 }

// maxProtocolErrorBytes bounds a server's own error message as the model sees
// it: enough for "Invalid params: path is outside the allowed directories", not
// a place for an essay.
const maxProtocolErrorBytes = 500

// protocolErrorResult is a JSON-RPC error from a server -- a refusal at the
// protocol level, as against a tool that reports its own failure -- as a failed
// result. It is a RESULT and not a Go error for the reason a tool's own failure
// is: its words are the server's, so they must pass the egress choke point
// (daemon/toolresult.go) that scrubs secrets, defuses delimiters, strips control
// characters and fences third-party text. The code and the message both reach
// the model, which is what a refusal needs in order to be fixed (a wrong path, a
// missing argument).
func protocolErrorResult(werr *jsonrpc.Error) Result {
	msg := trimMessage(werr.Message, maxProtocolErrorBytes)
	if msg == "" {
		msg = "(no message)"
	}
	return Result{
		Content: fmt.Sprintf("the server refused the call with a protocol error (code %d): %s", werr.Code, msg),
		IsError: true,
	}
}

// trimMessage cuts s to at most limit bytes on a character boundary, marking the cut.
func trimMessage(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
