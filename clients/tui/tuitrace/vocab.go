// Package tuitrace is the terminal client's half of the execution trace: one JSON
// object per line, in the daemon's format (daemon/trace/trace-schema.json is the
// definition; docs/EXECUTION_TRACE.md is the explanation), saying which stage a
// turn reached on the client's side: the command that was typed, the dial, the
// request sent, the approval shown, the result handed to the screen.
//
// IT IS THE DAEMON'S RULES, AGAIN. The recorder is compiled only under
// `-tags trace` (trace.go); every other build gets stub.go, where Begin returns a
// nil *Turn and every method is an empty body, and a test proves from the shipped
// binary's symbol table that nothing of it is there. A traced build still writes
// nothing unless MOCHIII_TRACE=1. An Event is an enum stage, an enum status, an
// enum error class, a duration and a Subject, which only this package can make:
// a command from the catalog, a request kind, or a tool name that passed the id
// pattern. There is no string parameter that takes free text and no error
// parameter.
//
// WHY THIS IS A SECOND COPY AND NOT THE DAEMON'S PACKAGE. clients/tui is its own
// module, the daemon module requires it (the one binary is both), and a client
// importing the daemon's packages would reverse that. The protocol module is the
// one both share, and it already holds the one thing that must be the same
// everywhere: protocol.ValidTraceID. Everything else here is a copy, and each
// copy is pinned by a test that fails when it drifts:
//
//   - the names (stages, statuses, error classes, request kinds) against
//     daemon/trace/trace-schema.json, which is the single source of truth:
//     TestEveryNameHereIsInTheSchema (this module) and
//     TestTheClientsErrorClassesAndTheDaemonsAreTheSchemasClosedList (the daemon
//     module, which imports this package, so the two Go tables are compared
//     directly too);
//   - the validators (what counts as a tool name) against the daemon's, on a
//     hostile corpus: TestTheClientsToolNameCheckAgreesWithTheDaemons;
//   - the line template and the 512-byte cap against the schema checker the
//     daemon's leak test uses: TestEveryLineFitsTheSchema.
//
// This file is compiled in every build. It holds only the enums, their name
// tables and the validating constructors; the linker drops them from a default
// binary because nothing in a default build calls them.
package tuitrace

// Stage is where on the client a turn was. It is the client's half of
// trace-schema.json's "stages": the daemon's stages are in daemon/trace.
type Stage uint8

const (
	stageInvalid Stage = iota
	StageCommandParsed
	StageCommandLocalDone
	StageDial
	StageRequestSent
	StageApprovalShown
	StageResultRendered
	numStages
)

var stageNames = [numStages]string{
	stageInvalid:          "",
	StageCommandParsed:    "command_parsed",
	StageCommandLocalDone: "command_local_done",
	StageDial:             "dial",
	StageRequestSent:      "request_sent",
	StageApprovalShown:    "approval_shown",
	StageResultRendered:   "result_rendered",
}

// StageNames are the stages this client writes.
func StageNames() []string { return append([]string(nil), stageNames[1:]...) }

// Status is how a stage stands. The names are the schema's.
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

// StatusNames are the schema's statuses.
func StatusNames() []string { return append([]string(nil), statusNames[1:]...) }

// Class is an error class: a member of the schema's closed list. ClassNone means
// no error. The three dial classes are this client's own; the rest are the
// daemon's (daemon/trace/enums.go), which the client uses for the class a Done
// carried. Anything else is ClassOther.
type Class uint8

const (
	ClassNone Class = iota
	ClassOther
	// The daemon's closed list, in the daemon's order (daemon/trace/enums.go).
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
	ClassDeniedConfig
	ClassDeniedUser
	ClassDeniedAsk
	ClassDeniedCredential
	ClassRefusedByTool
	ClassLength
	ClassContentFilter
	ClassBudgetExceeded
	ClassAgentBudget
	ClassUserCancelled
	ClassProviderError
	ClassToolFailed
	ClassServerUnavailable
	// What a dial can fail with: the three ways connectToDaemon (daemonconn.go)
	// can fail, and "other" for the rest. These three are the client's; the daemon
	// never writes them.
	ClassNoLockfile
	ClassUnreachable
	ClassHandshakeRejected
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
	ClassNoLockfile:          "no_lockfile",
	ClassUnreachable:         "unreachable",
	ClassHandshakeRejected:   "handshake_rejected",
}

// ClassNames is every error class this package can write, in order, without the
// empty one.
func ClassNames() []string { return append([]string(nil), classNames[1:]...) }

// DialClassNames are the classes that belong to the client alone: the closed list
// a dial failure draws from, with "other". The daemon's own table does not have
// the first three, and the schema's list is the union of the two tables.
func DialClassNames() []string {
	return []string{classNames[ClassNoLockfile], classNames[ClassUnreachable], classNames[ClassHandshakeRejected], classNames[ClassOther]}
}

// ClassOf maps a name from the daemon's closed set (a Done's ErrorClass) to its
// Class. Anything that is not a member, including the empty string and any text,
// is ClassOther; it never returns ClassNone, and the client's own dial classes are
// not something a daemon's string can select.
func ClassOf(name string) Class {
	for c := ClassOther; c < ClassNoLockfile; c++ {
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

// Subject is what an event is about: a command from the catalog, a request kind
// or a tool name. Its field is unexported and only the constructors below make
// one, so a caller cannot put a string of its own into an Event.
type Subject struct{ s string }

// maxIDBytes is the schema's id pattern's bound.
const maxIDBytes = 64

var (
	// NoSubject is an event that is about nothing in particular.
	NoSubject = Subject{}
	// UnknownCommand is what a typed command that is not in the catalog is
	// recorded as. What was typed is never written down: the TUI's own
	// unknown-command note echoes it, and the trace must not.
	UnknownCommand = Subject{"unknown"}
	// UnknownTool is a tool name that is not an id.
	UnknownTool = Subject{"unknown_tool"}
	// KindPrompt is the request kind of a turn sent to the daemon.
	KindPrompt = Subject{"prompt"}
)

// Command is a catalog command's name, without its slash. The caller passes the
// NAME FIELD OF THE CATALOG ENTRY it looked up, never the text the person typed
// (clients/tui/traceemit_on.go does, and TestAnUnknownCommandIsNeverWritten is
// what fails if it stops); this check is the second line: an id, or unknown.
func Command(catalogName string) Subject {
	if s, ok := asID(catalogName); ok {
		return Subject{s}
	}
	return UnknownCommand
}

// Tool is a tool name the DAEMON sent in an approval question: 1 to 64 characters
// of [a-z0-9_.:-] after lower-casing, or unknown_tool. It is the daemon's
// trace.ToolName; TestTheClientsToolNameCheckAgreesWithTheDaemons is the parity.
func Tool(name string) Subject {
	if name == "" {
		return NoSubject
	}
	if s, ok := asID(name); ok {
		return Subject{s}
	}
	return UnknownTool
}

// String is the subject as it is written.
func (s Subject) String() string { return s.s }

func asID(in string) (string, bool) {
	if in == "" || len(in) > maxIDBytes {
		return "", false
	}
	b := make([]byte, len(in))
	for i := 0; i < len(in); i++ {
		c := in[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == ':' || c == '-') {
			return "", false
		}
		b[i] = c
	}
	return string(b), true
}
