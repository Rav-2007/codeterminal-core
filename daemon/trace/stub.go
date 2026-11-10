//go:build !trace

package trace

import (
	"context"
	"time"
)

// The default build: no recorder. Every function is an empty body that the
// compiler inlines away, From returns a nil *Turn so every `if tr != nil` in the
// daemon is dead code, and nothing here reads the environment, touches the
// filesystem or names a file. trace.go is the real thing, under -tags trace.

// Turn is one request's trace. In this build it has no state.
type Turn struct{}

// ModelCall is one model call's trace, and Span one timed stage's. In this
// build neither has any state.
type ModelCall struct{}
type Span struct{}

// On reports whether tracing is running. It is never true in this build.
func On() bool { return false }

// NewTurn starts a trace for one request. It returns nil in this build.
func NewTurn(string) *Turn { return nil }

// With and From carry a *Turn on a context. From returns nil in this build.
func With(ctx context.Context, _ *Turn) context.Context { return ctx }
func From(context.Context) *Turn                        { return nil }

// WithCall and CallFrom carry the daemon's call counter for the call in flight.
func WithCall(ctx context.Context, _ int) context.Context { return ctx }
func CallFrom(context.Context) int                        { return 0 }

func (*Turn) Emit(Event)                   {}
func (*Turn) NextCall() int                { return 0 }
func (*Turn) BeginModelCall() *ModelCall   { return nil }
func (*Turn) Begin(Stage, Tool, int) *Span { return nil }
func (*Turn) Since() time.Duration         { return 0 }

func (*ModelCall) Token()       {}
func (*ModelCall) End(Class)    {}
func (*Span) End(Status, Class) {}
