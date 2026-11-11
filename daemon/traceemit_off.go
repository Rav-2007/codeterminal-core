//go:build !trace

package main

import (
	"context"

	"mochiii/daemon/mcp"
	"mochiii/daemon/trace"
	"mochiii/protocol"
)

// The default build's half of traceemit_on.go: the helpers a call site reaches
// without a guard, each an empty body.

func traceRefused(context.Context, ModelErrorClass)               {}
func traceDone(context.Context, string, *protocol.IncompleteInfo) {}
func traceToolRequested(context.Context, mcp.Tool)                {}
func traceApprovalResult(*trace.Turn, toolDecision, int)          {}
