//go:build trace

package main

import (
	"context"

	"mochiii/daemon/mcp"
	"mochiii/daemon/trace"
	"mochiii/protocol"
)

// The trace helpers a call site reaches unconditionally. In a default build the
// same four names are empty functions (traceemit_off.go), which the compiler
// inlines to nothing; keeping the bodies out of that build is what lets
// TestADefaultBuildHasNoTraceEmitter find no trace code in it.

// traceRefused records a request the daemon refused before any model was
// called, with the ErrorClass already on its Done.
func traceRefused(ctx context.Context, class ModelErrorClass) {
	trace.From(ctx).Emit(trace.Event{
		Stage: trace.StageRequestRefused, Status: trace.StatusFail,
		Tool: trace.KindPrompt.Tool(), Err: trace.ClassOf(string(class)),
	})
}

// traceDone records how a turn ended: ok, or a failure whose class is the
// Done's ErrorClass or, for an answer that stopped early, its Incomplete reason.
// The persist line is the caller's.
func traceDone(ctx context.Context, errorClass string, incomplete *protocol.IncompleteInfo) {
	tr := trace.From(ctx)
	if tr == nil {
		return
	}
	st, class := trace.StatusOK, trace.ClassNone
	switch {
	case errorClass != "":
		st, class = trace.StatusFail, trace.ClassOf(errorClass)
	case incomplete != nil:
		st, class = trace.StatusFail, trace.ClassOf(incomplete.Reason)
	}
	tr.Emit(trace.Event{Stage: trace.StageResult, Status: st, Dur: tr.Since(), Err: class})
}

// traceToolRequested records that the model asked for a tool, by the name the
// registry resolved (unknown_tool when it did not).
func traceToolRequested(ctx context.Context, spec mcp.Tool) {
	if tr := trace.From(ctx); tr != nil {
		tr.Emit(trace.Event{Stage: trace.StageToolRequested, Status: trace.StatusOK, Tool: traceToolOf(spec), Call: trace.CallFrom(ctx)})
	}
}

// traceApprovalResult records how a call's permission was settled when a person
// was involved or the call was refused: ok when someone approved it, and a
// failure with the audit source (denied_user, denied_config, ...) when it was
// not allowed. A call the configuration or an earlier grant allowed without
// asking writes nothing here.
func traceApprovalResult(tr *trace.Turn, d toolDecision, call int) {
	if tr == nil {
		return
	}
	switch {
	case !d.run:
		tr.Emit(trace.Event{Stage: trace.StageApprovalResult, Status: trace.StatusFail, Tool: traceToolOf(d.tool), Call: call, Err: trace.ClassOf(d.source)})
	case d.source == auditUserApprove || d.source == auditUserForTurn || d.source == auditUserForSpec:
		tr.Emit(trace.Event{Stage: trace.StageApprovalResult, Status: trace.StatusOK, Tool: traceToolOf(d.tool), Call: call})
	}
}
