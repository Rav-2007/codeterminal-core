//go:build !trace

package tuitrace

import (
	"time"

	"mochiii/protocol"
)

// The default build: no recorder. Begin returns a nil *Turn, so every `tr != nil`
// in the client is dead code; every method is an empty body that the compiler
// inlines away; nothing here reads the environment, touches the filesystem or names
// a file. trace.go is the real thing, under -tags trace.

// Turn is one turn's trace. In this build it has no state.
type Turn struct{}

// Begin starts a trace for one turn. It returns nil in this build.
func Begin() *Turn { return nil }

// ID is the id the client sends the daemon so the two files join. Empty in this
// build, so the request carries no trace field and its bytes are what they were.
func (*Turn) ID() protocol.TraceID { return "" }

// Reset exists for tests; here there is nothing to reset.
func Reset() {}

// Emit writes one line. It does nothing in this build.
func (*Turn) Emit(Stage, Status, time.Duration, Subject, Class) {}
