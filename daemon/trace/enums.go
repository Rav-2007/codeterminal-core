// Package trace is the daemon's execution trace: one JSON object per line
// (trace-schema.json, in this directory, is the format) saying which stage a
// request reached, how long it took and how it ended. It exists so a person can
// see where a turn stopped, and it is built to be incapable of saying anything
// else.
//
// TWO SWITCHES, AND THE FIRST IS NOT A FLAG. The recorder is compiled only under
// `-tags trace` (trace.go). A default build gets stub.go: every function here is
// an empty body, `From` returns a nil *Turn, and the call sites in the daemon are
// all of the form `if tr := trace.From(ctx); tr != nil { ... }`, so the compiler
// removes them. A shipped binary therefore contains no trace code to switch on,
// and trace_symbols_test.go in the daemon proves it from the binary's symbol
// table. A traced build still writes nothing unless MOCHIII_TRACE=1 is set at
// daemon start.
//
// THE API CANNOT CARRY FREE TEXT. An Event holds an enum for the stage, an enum
// for the status, an enum for the error class, a number for the duration, a
// number for the call counter, and a Tool, which is a value only ToolName or a
// Kind can make and which is validated on the way in. There is no string
// parameter that accepts a sentence and no `error` parameter: a caller that has
// an error converts it to a Class first, and a text the closed sets do not know
// becomes ClassOther. That is the whole privacy argument, and the leak test in
// daemon/trace_leak_test.go is what checks it end to end.
//
// This file is compiled in every build. It holds only the enums, their name
// tables and the two validating constructors, all of which the linker drops
// from a default binary because nothing in a default build calls them.
package trace

import "time"

// Stage is where in a request's life an event happened. It is a closed list:
// the daemon's half of trace-schema.json's "stages". The client stages
// (command_parsed, dial, request_sent, approval_shown, result_rendered, ...)
// belong to the client emitters and are not here.
type Stage uint8

const (
	stageInvalid Stage = iota
	StageRequest
	StageRequestRefused
	StageRetrieval
	StageModelCall
	StageFirstToken
	StageToolRequested
	StageApprovalWait
	StageApprovalResult
	StageToolRun
	StageMCPCall
	StageBudgetStop
	StagePersist
	StageResult
	numStages
)

var stageNames = [numStages]string{
	stageInvalid:        "",
	StageRequest:        "request",
	StageRequestRefused: "request_refused",
	StageRetrieval:      "retrieval",
	StageModelCall:      "model_call",
	StageFirstToken:     "first_token",
	StageToolRequested:  "tool_requested",
	StageApprovalWait:   "approval_wait",
	StageApprovalResult: "approval_result",
	StageToolRun:        "tool_run",
	StageMCPCall:        "mcp_call",
	StageBudgetStop:     "budget_stop",
	StagePersist:        "persist",
	StageResult:         "result",
}

// Status is how a stage stands: it began, it finished, it failed, or it was
// not needed.
type Status uint8

const (
	statusInvalid Status = iota
	StatusStart
	StatusOK
	StatusFail
	StatusSkip
	numStatuses
)

var statusNames = [numStatuses]string{"", "start", "ok", "fail", "skip"}

// Class is an error class: a member of one of the daemon's existing closed sets
// (ModelErrorClass, the audit* decision sources, protocol.Incomplete*) or one of
// the two tool failure classes the loop already distinguishes. ClassNone means
// no error. Anything else is ClassOther: the value of a Class can only be a
// name in classNames, so no text can ride in it.
type Class uint8

const (
	ClassNone Class = iota
	ClassOther
	// ModelErrorClass (daemon/modelerror.go)
	ClassRateLimited
	ClassQuotaExceeded
	ClassContextTooLarge
	ClassAuth
	ClassUpstreamUnavailable
	ClassPrivacyRefused
	ClassInvalidRequest
	ClassUnavailableTier
	ClassSpendLimit
	ClassUnknown
	// The audit* decision sources that mean a call was not allowed
	// (daemon/toolaudit.go, daemon/runplace.go).
	ClassDeniedConfig
	ClassDeniedUser
	ClassDeniedAsk
	ClassDeniedCredential
	ClassRefusedByTool
	// protocol.Incomplete*
	ClassLength
	ClassContentFilter
	ClassBudgetExceeded
	ClassAgentBudget
	ClassUserCancelled
	ClassProviderError
	// The split dispatchToolCall already makes when registry.Call fails.
	ClassToolFailed
	ClassServerUnavailable
	numClasses
)

var classNames = [numClasses]string{
	ClassNone:                "",
	ClassOther:               "other",
	ClassRateLimited:         "rate_limited",
	ClassQuotaExceeded:       "quota_exceeded",
	ClassContextTooLarge:     "context_too_large",
	ClassAuth:                "auth",
	ClassUpstreamUnavailable: "upstream_unavailable",
	ClassPrivacyRefused:      "privacy_refused",
	ClassInvalidRequest:      "invalid_request",
	ClassUnavailableTier:     "unavailable_tier",
	ClassSpendLimit:          "spend_limit",
	ClassUnknown:             "unknown",
	ClassDeniedConfig:        "denied_config",
	ClassDeniedUser:          "denied_user",
	ClassDeniedAsk:           "denied_ask",
	ClassDeniedCredential:    "denied_credential",
	ClassRefusedByTool:       "refused_by_tool",
	ClassLength:              "length",
	ClassContentFilter:       "content_filter",
	ClassBudgetExceeded:      "budget_exceeded",
	ClassAgentBudget:         "agent_budget",
	ClassUserCancelled:       "user_cancelled",
	ClassProviderError:       "provider_error",
	ClassToolFailed:          "tool_failed",
	ClassServerUnavailable:   "server_unavailable",
}

// ClassOf maps a name from one of the closed sets to its Class. A name that is
// not in the list, including the empty string and any text, is ClassOther. It
// never returns ClassNone: "no error" is a decision the caller makes by passing
// ClassNone, not something a string can say.
func ClassOf(name string) Class {
	for c := ClassOther; c < numClasses; c++ {
		if classNames[c] == name {
			return c
		}
	}
	return ClassOther
}

// Name is the class as it is written.
func (c Class) Name() string {
	if c >= numClasses {
		return classNames[ClassOther]
	}
	return classNames[c]
}

// Kind is a request kind: which of the wire's request types a connection
// carried (daemon/requestfields.go's discriminators; a prompt has none).
type Kind uint8

const (
	kindInvalid Kind = iota
	KindApply
	KindUndo
	KindStatus
	KindSearch
	KindHistory
	KindConnect
	KindMCP
	KindRules
	KindGitHub
	KindLocal
	KindApproval
	KindPrompt
	numKinds
)

var kindNames = [numKinds]string{"", "apply", "undo", "status", "search", "history", "connect", "mcp", "rules", "github", "local", "approval", "prompt"}

// Tool is the subject of an event: a resolved tool name (builtin__read_file), a
// request kind, or empty. Its field is unexported and only ToolName and
// Kind.Tool make one, so a caller cannot put a string of its own into an Event.
type Tool struct{ s string }

// UnknownTool is what a name the registry did not resolve is recorded as. The
// model chooses tool names; what it chose is never written down, only that it
// chose one that does not exist.
var UnknownTool = Tool{"unknown_tool"}

// maxIDBytes is the longest subject, and the schema's id pattern's bound.
const maxIDBytes = 64

// ToolName validates a tool name the REGISTRY resolved: 1 to 64 characters of
// [a-z0-9_.:-] after lower-casing. Anything else, including every sentence, is
// UnknownTool. The caller must pass only a name the registry returned (the
// daemon passes decision.tool.QualifiedName() and nothing the model typed); this
// check is the second line, for the day a caller does not.
func ToolName(resolved string) Tool {
	if resolved == "" {
		return Tool{}
	}
	if len(resolved) > maxIDBytes {
		return UnknownTool
	}
	b := make([]byte, len(resolved))
	for i := 0; i < len(resolved); i++ {
		c := resolved[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == ':' || c == '-') {
			return UnknownTool
		}
		b[i] = c
	}
	return Tool{string(b)}
}

// Tool is the request kind as a subject.
func (k Kind) Tool() Tool {
	if k == kindInvalid || k >= numKinds {
		return Tool{}
	}
	return Tool{kindNames[k]}
}

// Event is one line. Time and sequence are the recorder's, not the caller's.
type Event struct {
	Stage  Stage
	Status Status
	// Dur is how long the stage took; it is written for ok and fail.
	Dur time.Duration
	// Tool is the subject (see Tool).
	Tool Tool
	// Call is the daemon's own per-turn call counter (Turn.NextCall), never an
	// id the model sent. Zero means the event is not about a tool call.
	Call int
	// Err is the error class; it is written only when Status is StatusFail.
	Err Class
}
