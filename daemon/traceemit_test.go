package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"mochiii/daemon/mcp"
	"mochiii/daemon/trace"
	"mochiii/protocol"
)

// The trace's request kind comes from the same sniffers serveConn dispatches on,
// so a body means the same thing to both. A body that names two request types
// is the prompt path to serveConn (requestFields refuses it), and so it is here.
func TestTheTraceKindIsWhatServeConnWouldDispatchOn(t *testing.T) {
	cases := map[string]trace.Kind{
		`{"edit":{"file_path":"a","search":"b","replace":"c"}}`: trace.KindApply,
		`{"undo":true}`:                       trace.KindUndo,
		`{"status":true}`:                     trace.KindStatus,
		`{"search":true,"query":"x"}`:         trace.KindSearch,
		`{"chats":true}`:                      trace.KindHistory,
		`{"connect":true}`:                    trace.KindConnect,
		`{"mcp":true}`:                        trace.KindMCP,
		`{"rules":true}`:                      trace.KindRules,
		`{"github":true}`:                     trace.KindGitHub,
		`{"local":true}`:                      trace.KindLocal,
		`{"prompt":"hello"}`:                  trace.KindPrompt,
		`{}`:                                  trace.KindPrompt,
		`not json`:                            trace.KindPrompt,
		`{"undo":true,"status":true}`:         trace.KindPrompt, // two request types: refused to the prompt path
		`{"EDIT":{"file_path":"a"}}`:          trace.KindPrompt, // keys are matched exactly
		`{"prompt":"edit","edit":"not-edit"}`: trace.KindPrompt,
	}
	for body, want := range cases {
		if got := traceKindOf(json.RawMessage(body)); got != want {
			t.Errorf("traceKindOf(%s) = %v, want %v", body, got, want)
		}
	}
}

func TestOnlyTheSessionNameIsReadFromABody(t *testing.T) {
	if got := traceSessionOf(json.RawMessage(`{"prompt":"x","session":"abc"}`)); got != "abc" {
		t.Errorf("session = %q", got)
	}
	for _, body := range []string{`{"prompt":"x"}`, `[]`, `nonsense`, `{"session":5}`} {
		if got := traceSessionOf(json.RawMessage(body)); got != "" {
			t.Errorf("traceSessionOf(%s) = %q", body, got)
		}
	}
}

// Every name in the daemon's closed sets must be a trace class, and a name that
// is not in them must not become one. A set that grows here without the trace
// learning of it would silently write "other"; this is the test that says so.
func TestEveryClosedSetMemberIsATraceClass(t *testing.T) {
	var names []string
	for _, c := range []ModelErrorClass{ClassRateLimited, ClassQuotaExceeded, ClassContextTooLarge, ClassAuth,
		ClassUpstreamUnavailable, ClassPrivacyRefused, ClassInvalidRequest, ClassUnavailableTier, ClassSpendLimit, ClassUnknown} {
		names = append(names, string(c))
	}
	names = append(names, auditDeniedConfig, auditDeniedUser, auditDeniedAsk, auditDeniedCredential, auditRefusedByTool,
		protocol.IncompleteLength, protocol.IncompleteContentFilter, protocol.IncompleteBudgetExceeded,
		protocol.IncompleteAgentBudget, protocol.IncompleteUserCancelled, protocol.IncompleteProviderError)
	for _, n := range names {
		if got := trace.ClassOf(n); got.Name() != n {
			t.Errorf("%q is in a closed set the trace draws on, and ClassOf(%q) = %q", n, n, got.Name())
		}
	}
	// The ones that are not failures are not classes: nothing may write them as one.
	for _, n := range []string{auditConfigAllow, auditUserApprove, auditUserForTurn, auditTurnGrant, auditUserForSpec, auditSpecGrant} {
		if got := trace.ClassOf(n); got.Name() != "other" {
			t.Errorf("%q is an approval, not a failure class, but ClassOf says %q", n, got.Name())
		}
	}
}

func TestTraceToolIsOnlyWhatTheRegistryResolved(t *testing.T) {
	if got := traceToolOf(mcp.Tool{}); got != trace.UnknownTool {
		t.Errorf("a lookup that found nothing is %v, want unknown_tool", got)
	}
	if got := traceToolOf(mcp.Tool{Server: "builtin"}); got != trace.UnknownTool {
		t.Errorf("a spec with no name is %v, want unknown_tool", got)
	}
	// A resolved name that is not an id (a server's tool with a space in it) is
	// unknown_tool too: the second line of defence.
	if got := traceToolOf(mcp.Tool{Server: "srv", Name: "has a space"}); got != trace.UnknownTool {
		t.Errorf("a name with a space is %v, want unknown_tool", got)
	}
	if got := traceToolOf(mcp.Tool{Server: "builtin", Name: "read_file"}); got != trace.ToolName("builtin__read_file") {
		t.Errorf("a resolved built-in is %v", got)
	}
}

func TestTraceOutcomesFollowTheSplitTheLoopMakes(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status trace.Status
		class  trace.Class
	}{
		{nil, trace.StatusOK, trace.ClassNone},
		{fmt.Errorf("%w: server x is not connected", mcp.ErrServerUnavailable), trace.StatusFail, trace.ClassServerUnavailable},
		{errors.New("open /home/someone/secret.txt: no such file or directory"), trace.StatusFail, trace.ClassToolFailed},
	} {
		if st, c := traceToolOutcome(tc.err); st != tc.status || c != tc.class {
			t.Errorf("traceToolOutcome(%v) = %v, %v", tc.err, st, c)
		}
	}
	if traceModelErrClass(nil) != trace.ClassNone {
		t.Error("no error must be no class")
	}
	if traceModelErrClass(context.Canceled) != trace.ClassUserCancelled {
		t.Error("a cancelled context is user_cancelled")
	}
	if got := traceModelErrClass(errors.New("some opaque failure with a path /x/y and a key sk-DUMMY")); got.Name() == "" {
		t.Error("an unclassified error must still be a class")
	}
	if traceSkipStatus(true) != trace.StatusSkip || traceSkipStatus(false) != trace.StatusOK {
		t.Error("traceSkipStatus")
	}
	if got := traceCallStage(mcp.Tool{Server: mcp.BuiltinServerName, Name: "x"}); got != trace.StageToolRun {
		t.Errorf("a built-in is %v", got)
	}
	if got := traceCallStage(mcp.Tool{Server: "github", Name: "x"}); got != trace.StageMCPCall {
		t.Errorf("a server's tool is %v", got)
	}
}
