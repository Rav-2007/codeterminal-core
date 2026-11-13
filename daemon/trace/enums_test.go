package trace

import (
	"context"
	"slices"
	"strings"
	"testing"

	"mochiii/daemon/trace/tracecheck"
)

func loadSchema(t *testing.T) *tracecheck.Schema {
	t.Helper()
	s, err := tracecheck.Load("trace-schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The enums are the daemon's half of the schema file. The schema is the source
// of truth, so a stage, a request kind or an error class that exists in one and
// not the other fails here.
func TestTheEnumsAreMembersOfTheSchema(t *testing.T) {
	s := loadSchema(t)
	for st := StageRequest; st < numStages; st++ {
		if !slices.Contains(s.StageNames(), stageNames[st]) {
			t.Errorf("stage %q is not in trace-schema.json", stageNames[st])
		}
	}
	for st := StatusStart; st < numStatuses; st++ {
		if !slices.Contains(s.Statuses, statusNames[st]) {
			t.Errorf("status %q is not in trace-schema.json", statusNames[st])
		}
	}
	for k := KindApply; k < numKinds; k++ {
		if !slices.Contains(s.RequestKinds, kindNames[k]) {
			t.Errorf("request kind %q is not in trace-schema.json", kindNames[k])
		}
	}
	if len(s.RequestKinds) != int(numKinds)-1 {
		t.Errorf("the schema lists %d request kinds, the enum has %d", len(s.RequestKinds), numKinds-1)
	}
	var classes []string
	for c := ClassOther; c < numClasses; c++ {
		classes = append(classes, classNames[c])
	}
	slices.Sort(classes)
	want := slices.Clone(s.ErrorClasses)
	slices.Sort(want)
	if !slices.Equal(classes, want) {
		t.Errorf("the error classes in the enum and in the schema differ:\n enum   %v\n schema %v", classes, want)
	}
	// Every name table is complete: a zero string would write an empty value
	// where a member is required.
	for st := StageRequest; st < numStages; st++ {
		if stageNames[st] == "" {
			t.Errorf("stage %d has no name", st)
		}
	}
}

func TestToolNameRefusesEverythingThatIsNotAnID(t *testing.T) {
	for _, hostile := range []string{
		`there is no tool called "x" available in this turn`,
		"two words", "line\nbreak", "tab\there", `{"a":1}`, "../../etc/passwd", "emoji-\U0001F600",
		strings.Repeat("a", 65), "semi;colon", "slash/slash", `quote"quote`,
	} {
		if got := ToolName(hostile); got != UnknownTool {
			t.Errorf("ToolName(%q) = %q, want unknown_tool", hostile, got.s)
		}
	}
	for in, want := range map[string]string{
		"builtin__read_file":    "builtin__read_file",
		"github__create_issue":  "github__create_issue",
		"Server__MixedCase":     "server__mixedcase", // a registry name may carry capitals; ids are lower case
		strings.Repeat("a", 64): strings.Repeat("a", 64),
		"v1.2:tool-x":           "v1.2:tool-x",
	} {
		if got := ToolName(in).s; got != want {
			t.Errorf("ToolName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := ToolName(""); got != (Tool{}) {
		t.Errorf("ToolName(\"\") = %q, want no subject", got.s)
	}
}

func TestEveryKindIsAValidSubject(t *testing.T) {
	for k := KindApply; k < numKinds; k++ {
		if got := k.Tool(); got.s != kindNames[k] || ToolName(got.s) != got {
			t.Errorf("kind %d is %q", k, got.s)
		}
	}
	if (Kind(0).Tool() != Tool{}) || (numKinds.Tool() != Tool{}) {
		t.Error("a kind outside the list must have no subject")
	}
}

// ClassOf is closed: no string, however it is spelled, becomes anything but a
// member, and "no error" is never the answer.
func TestClassOfNeverReturnsATextOrNone(t *testing.T) {
	for c := ClassOther; c < numClasses; c++ {
		if got := ClassOf(classNames[c]); got != c {
			t.Errorf("ClassOf(%q) = %v, want %v", classNames[c], got, c)
		}
	}
	for _, in := range []string{"", "Rate_Limited", "denied_user ", "the file /home/x/.env could not be read", "sk-ant-api03-CANARY"} {
		if got := ClassOf(in); got != ClassOther {
			t.Errorf("ClassOf(%q) = %q, want other", in, got.Name())
		}
	}
	if Class(200).Name() != "other" {
		t.Error("a Class outside the table must read as other")
	}
}

// Off is nil, and nil is safe: the daemon's call sites rely on it.
func TestNoTurnIsSafeEverywhere(t *testing.T) {
	if NewTurn("a session") != nil || NewTurnWithID("a session", "a1b2c3d4") != nil {
		t.Skip("a recorder is enabled in this process")
	}
	ctx := context.Background()
	if tr := From(ctx); tr != nil {
		t.Fatalf("From(background) = %v", tr)
	}
	if With(ctx, nil) != ctx {
		t.Error("With(nil) must return the context it was given")
	}
	var tr *Turn
	tr.Emit(Event{Stage: StageRequest, Status: StatusOK})
	if tr.NextCall() != 0 || tr.BeginModelCall() != nil || tr.Begin(StageRetrieval, Tool{}, 0) != nil || tr.Since() != 0 {
		t.Error("a nil Turn must do nothing and return zero values")
	}
	var mc *ModelCall
	mc.Token()
	mc.End(ClassNone)
	var sp *Span
	sp.End(StatusOK, ClassNone)
	if CallFrom(WithCall(ctx, 3)) != 3 && On() {
		t.Error("the call counter did not survive the context")
	}
}
