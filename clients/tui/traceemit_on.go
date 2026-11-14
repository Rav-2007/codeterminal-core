//go:build trace

package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"mochiii/clients/tui/tuitrace"
	"mochiii/protocol"
)

// The terminal client's half of the execution trace (tuitrace). Everything here
// turns a value the client already has into an enum or a validated id; none of it
// takes a string a person typed and writes it, and none of it passes an error's
// text. In a default build every helper is an empty body (traceemit_off.go), so
// none of this is linked, and the call sites cost nothing.
//
// EVERYTHING HERE RUNS ON THE UPDATE GOROUTINE (once per Enter) OR THE STREAM
// GOROUTINE (a handful of lines per turn). None of it runs from View(), per
// keystroke or per render, and none of it is reached from /connect's key entry.

// traceCommandSubject is the catalog command a line of input names, else unknown.
// It looks the typed name UP and records the catalog entry's own Name, so the
// bytes that are written are never the bytes that were typed.
func traceCommandSubject(raw string) tuitrace.Subject {
	name, _, _ := strings.Cut(strings.TrimPrefix(raw, "/"), " ")
	if d := lookupSlash(strings.ToLower(strings.TrimSpace(name))); d != nil {
		return tuitrace.Command(d.Name)
	}
	return tuitrace.UnknownCommand
}

// traceCommandParsed records that a line that starts with "/" was read as a
// command: which catalog command, or "unknown". Never its arguments, and nothing
// for a line that is not a command.
func traceCommandParsed(tr *tuitrace.Turn, raw string) {
	if tr == nil || !strings.HasPrefix(raw, "/") {
		return
	}
	tr.Emit(tuitrace.StageCommandParsed, tuitrace.StatusOK, 0, traceCommandSubject(raw), tuitrace.ClassNone)
}

// traceLocalDone records that a command that never reaches the daemon has been
// handled: its handler returned. It is the whole trace of such a command. Work a
// command starts for later (a request the daemon answers on its own connection) is
// not part of it.
func traceLocalDone(tr *tuitrace.Turn, raw string, began time.Time) {
	if tr == nil {
		return
	}
	tr.Emit(tuitrace.StageCommandLocalDone, tuitrace.StatusOK, time.Since(began), traceCommandSubject(raw), tuitrace.ClassNone)
}

// traceDialErr tags a connectToDaemon failure with its class, so the dial line can
// say which of the three ways it failed without reading the error's text. The
// error itself, and its text, are returned as they were.
func traceDialErr(c tuitrace.Class, err error) error { return &dialFailure{class: c, err: err} }

// dialFailure is err with a class. Error and Unwrap make it indistinguishable from
// err to every caller that prints it or asks errors.Is/As.
type dialFailure struct {
	class tuitrace.Class
	err   error
}

func (d *dialFailure) Error() string { return d.err.Error() }
func (d *dialFailure) Unwrap() error { return d.err }

// dialClass is the class of a dial failure: the one connectToDaemon tagged it
// with, else other.
func dialClass(err error) tuitrace.Class {
	var d *dialFailure
	if errors.As(err, &d) {
		return d.class
	}
	return tuitrace.ClassOther
}

// streamTrace is one prompt turn's client-side trace, kept by the stream goroutine.
// The shape of a request that never comes back is "request_sent start and nothing
// after": the request_sent line is written when the request goes, and closed with
// an ok when the first answer arrives or a fail when it does not.
type streamTrace struct {
	tr     *tuitrace.Turn
	dialAt time.Time
	sentAt time.Time
	phase  int // 0 before the request, 1 request_sent open, 2 answered, 3 over
}

func newStreamTrace(tr *tuitrace.Turn) *streamTrace {
	if tr == nil {
		return nil
	}
	return &streamTrace{tr: tr}
}

func (s *streamTrace) dialStart() {
	if s == nil {
		return
	}
	s.dialAt = time.Now()
	s.tr.Emit(tuitrace.StageDial, tuitrace.StatusStart, 0, tuitrace.KindPrompt, tuitrace.ClassNone)
}

// dialDone closes the dial: ok, or fail with the class connectToDaemon tagged. A
// failed dial ends the turn's trace: there is no request to follow.
func (s *streamTrace) dialDone(err error) {
	if s == nil {
		return
	}
	if err == nil {
		s.tr.Emit(tuitrace.StageDial, tuitrace.StatusOK, time.Since(s.dialAt), tuitrace.KindPrompt, tuitrace.ClassNone)
		return
	}
	s.tr.Emit(tuitrace.StageDial, tuitrace.StatusFail, time.Since(s.dialAt), tuitrace.KindPrompt, dialClass(err))
	s.phase = 3
}

func (s *streamTrace) requestSent() {
	if s == nil {
		return
	}
	s.sentAt, s.phase = time.Now(), 1
	s.tr.Emit(tuitrace.StageRequestSent, tuitrace.StatusStart, 0, tuitrace.KindPrompt, tuitrace.ClassNone)
}

// answered closes request_sent with an ok the first time the daemon says anything.
func (s *streamTrace) answered() {
	if s == nil || s.phase != 1 {
		return
	}
	s.phase = 2
	s.tr.Emit(tuitrace.StageRequestSent, tuitrace.StatusOK, time.Since(s.sentAt), tuitrace.KindPrompt, tuitrace.ClassNone)
}

// approvalShown records that a tool approval was handed to the screen, by the name
// the daemon sent (a server's name and a tool's, which the daemon's registry
// resolved).
func (s *streamTrace) approvalShown(req protocol.ToolApprovalRequest) {
	if s == nil {
		return
	}
	s.tr.Emit(tuitrace.StageApprovalShown, tuitrace.StatusOK, 0, tuitrace.Tool(req.Server+"__"+req.Tool), tuitrace.ClassNone)
}

// rendered records that the turn's result was handed to the screen: ok, or fail
// with the Done's ErrorClass when the turn ended in an error (anything outside the
// closed list is other).
func (s *streamTrace) rendered(errorClass string, failed bool) {
	if s == nil || s.phase == 3 {
		return
	}
	s.answered()
	s.phase = 3
	if failed {
		s.tr.Emit(tuitrace.StageResultRendered, tuitrace.StatusFail, time.Since(s.sentAt), tuitrace.NoSubject, tuitrace.ClassOf(errorClass))
		return
	}
	s.tr.Emit(tuitrace.StageResultRendered, tuitrace.StatusOK, time.Since(s.sentAt), tuitrace.NoSubject, tuitrace.ClassNone)
}

// finish is deferred by the stream goroutine and closes whatever is still open: a
// request that never got an answer fails request_sent, and one that was answered
// and never ended fails result_rendered, as user_cancelled when the person stopped
// it and other when it broke. Nothing is written for a turn that ended normally, or
// whose dial failed.
func (s *streamTrace) finish(ctx context.Context) {
	if s == nil || s.phase == 0 || s.phase == 3 {
		return
	}
	class := tuitrace.ClassOther
	if ctx.Err() != nil {
		class = tuitrace.ClassUserCancelled
	}
	stage := tuitrace.StageResultRendered
	if s.phase == 1 {
		stage = tuitrace.StageRequestSent
	}
	subject := tuitrace.NoSubject
	if stage == tuitrace.StageRequestSent {
		subject = tuitrace.KindPrompt
	}
	s.phase = 3
	s.tr.Emit(stage, tuitrace.StatusFail, time.Since(s.sentAt), subject, class)
}
