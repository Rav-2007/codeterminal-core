package main

import (
	"context"
	"encoding/json"
	"errors"

	"mochiii/daemon/mcp"
	"mochiii/daemon/trace"
	"mochiii/protocol"
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

// traceTurnOf is the turn id a prompt request asks the daemon to use
// (protocol.PromptRequest.Trace), or "" when it names none or names anything
// that cannot be one. It is UNTRUSTED: it is looked at only for a prompt, only
// at the exact key "trace" (not "Trace", not a duplicate), and only when the
// value is a JSON string of exactly ten bytes, quotes included: eight characters
// with no escape in them. What it returns still goes through
// trace.ValidClientID in NewTurnWithID, so this is the cheap first line and that
// is the one that decides. A value that fails either is never logged or kept.
func traceTurnOf(raw json.RawMessage) string {
	if traceKindOf(raw) != trace.KindPrompt {
		return ""
	}
	fields, ok := requestFields(raw)
	if !ok {
		return ""
	}
	v := fields["trace"]
	if len(v) != protocol.TraceIDBytes+2 {
		return ""
	}
	var id string
	if json.Unmarshal(v, &id) != nil {
		return ""
	}
	return id
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
