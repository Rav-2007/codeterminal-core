package main

import (
	"context"
	"encoding/json"
	"errors"

	"mochiii/daemon/mcp"
	"mochiii/daemon/trace"
)

// The daemon's side of the execution trace (daemon/trace). Everything here turns
// a value the daemon already has into an enum or a validated id; none of it
// takes a string a person or a model wrote, and none of it passes an error's
// text. In a default build every caller is behind `trace.From(ctx) != nil` or
// `trace.On()`, both constant false, so none of this is reached or linked.

// traceKindOf is the request kind of a decoded connection body, by the same
// discriminator sniffers serveConn dispatches on and in the same order. It
// reads only which keys are present.
func traceKindOf(raw json.RawMessage) trace.Kind {
	switch {
	case isApplyEditRequest(raw):
		return trace.KindApply
	case isUndoRequest(raw):
		return trace.KindUndo
	case isStatusRequest(raw):
		return trace.KindStatus
	case isConnectRequest(raw):
		return trace.KindConnect
	case isMCPRequest(raw):
		return trace.KindMCP
	case isRulesRequest(raw):
		return trace.KindRules
	case isGitHubRequest(raw):
		return trace.KindGitHub
	case isLocalRequest(raw):
		return trace.KindLocal
	case isSearchRequest(raw):
		return trace.KindSearch
	case isHistoryRequest(raw):
		return trace.KindHistory
	}
	return trace.KindPrompt
}

// traceSessionOf is the session name a request carries, for trace.NewTurn to
// hash. Nothing else is read from the body.
func traceSessionOf(raw json.RawMessage) string {
	var s struct {
		Session string `json:"session"`
	}
	_ = json.Unmarshal(raw, &s)
	return s.Session
}

// traceToolOf is the name of a tool the REGISTRY resolved. A zero spec, which
// is what a lookup that failed returns, is unknown_tool: what the model typed is
// never recorded.
func traceToolOf(spec mcp.Tool) trace.Tool {
	if spec.Server == "" || spec.Name == "" {
		return trace.UnknownTool
	}
	return trace.ToolName(spec.QualifiedName())
}

// traceModelErrClass is the class of a model call's error: a member of
// ModelErrorClass, or user_cancelled when the turn was stopped. nil is no error.
func traceModelErrClass(err error) trace.Class {
	switch {
	case err == nil:
		return trace.ClassNone
	case errors.Is(err, context.Canceled):
		return trace.ClassUserCancelled
	}
	return trace.ClassOf(string(asModelError(err).Class))
}

// traceSkipStatus is the status of a stage that may have been skipped.
func traceSkipStatus(skipped bool) trace.Status {
	if skipped {
		return trace.StatusSkip
	}
	return trace.StatusOK
}

// traceCallStage is tool_run for a built-in and mcp_call for a server's tool.
func traceCallStage(spec mcp.Tool) trace.Stage {
	if spec.Server == mcp.BuiltinServerName {
		return trace.StageToolRun
	}
	return trace.StageMCPCall
}

// traceToolOutcome is the status and class of a tool call's Go error, by the
// same split dispatchToolCall makes for what it tells the model.
func traceToolOutcome(err error) (trace.Status, trace.Class) {
	switch {
	case err == nil:
		return trace.StatusOK, trace.ClassNone
	case errors.Is(err, mcp.ErrServerUnavailable):
		return trace.StatusFail, trace.ClassServerUnavailable
	}
	return trace.StatusFail, trace.ClassToolFailed
}
