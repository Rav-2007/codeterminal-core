//go:build !trace

package tui

import (
	"context"
	"time"

	"mochiii/clients/tui/tuitrace"
	"mochiii/protocol"
)

// The default build's half of traceemit_on.go: every helper is an empty body the
// compiler inlines to nothing, so a shipped client has no trace code, which
// TestADefaultBuildHasNoTraceCode reads from the binary's symbol table.

func traceCommandParsed(*tuitrace.Turn, string)        {}
func traceLocalDone(*tuitrace.Turn, string, time.Time) {}
func traceDialErr(_ tuitrace.Class, err error) error   { return err }

type streamTrace struct{}

func newStreamTrace(*tuitrace.Turn) *streamTrace                { return nil }
func (*streamTrace) dialStart()                                 {}
func (*streamTrace) dialDone(error)                             {}
func (*streamTrace) requestSent()                               {}
func (*streamTrace) answered()                                  {}
func (*streamTrace) approvalShown(protocol.ToolApprovalRequest) {}
func (*streamTrace) rendered(string, bool)                      {}
func (*streamTrace) finish(context.Context)                     {}
