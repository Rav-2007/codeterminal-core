package tuitrace

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// schemaFile is the one source of truth for every name in this package: the
// daemon's trace-schema.json, owned by the repository root's daemon/trace. This
// module cannot import the daemon, so it reads the file; the daemon module, which
// does import this package, compares the Go tables directly.
const schemaFile = "../../../daemon/trace/trace-schema.json"

type schema struct {
	MaxLineBytes int      `json:"max_line_bytes"`
	Fields       []string `json:"fields"`
	Srcs         []string `json:"srcs"`
	Statuses     []string `json:"statuses"`
	Stages       []struct {
		Name string `json:"name"`
	} `json:"stages"`
	ErrorClasses []string          `json:"error_classes"`
	Patterns     map[string]string `json:"patterns"`
}

func loadSchema(t *testing.T) schema {
	t.Helper()
	b, err := os.ReadFile(schemaFile)
	if err != nil {
		t.Fatalf("the schema is not where the trace's tests expect it: %v", err)
	}
	var s schema
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Stages) == 0 || len(s.ErrorClasses) == 0 || len(s.Statuses) == 0 {
		t.Fatalf("the schema file is incomplete: %+v", s)
	}
	return s
}

func (s schema) stageNames() []string {
	var out []string
	for _, st := range s.Stages {
		out = append(out, st.Name)
	}
	return out
}

func TestEveryNameHereIsInTheSchema(t *testing.T) {
	s := loadSchema(t)
	for _, n := range StageNames() {
		if !slices.Contains(s.stageNames(), n) {
			t.Errorf("stage %q is not in trace-schema.json", n)
		}
	}
	if !slices.Equal(StatusNames(), s.Statuses) {
		t.Errorf("statuses differ: here %v, schema %v", StatusNames(), s.Statuses)
	}
	// The error classes are the schema's closed list, exactly: this package carries
	// the daemon's names (for a Done's class) and the client's own three.
	have, want := ClassNames(), slices.Clone(s.ErrorClasses)
	slices.Sort(have)
	slices.Sort(want)
	if !slices.Equal(have, want) {
		t.Errorf("the error classes here and in the schema differ:\n here   %v\n schema %v", have, want)
	}
	for _, n := range DialClassNames() {
		if !slices.Contains(s.ErrorClasses, n) {
			t.Errorf("dial class %q is not in trace-schema.json's closed list", n)
		}
	}
	if !slices.Contains(s.Srcs, "tui") {
		t.Error(`the schema has no src "tui"`)
	}
	// Every name table is complete: a zero string would write an empty value where
	// a member is required.
	for st := StageCommandParsed; st < numStages; st++ {
		if stageNames[st] == "" {
			t.Errorf("stage %d has no name", st)
		}
	}
	for c := ClassOther; c < numClasses; c++ {
		if classNames[c] == "" {
			t.Errorf("class %d has no name", c)
		}
	}
}

// The three dial classes and "other" are what connectToDaemon can fail with, no
// more: the test is on the list, the emit is on clients/tui's TestADialFailure...
func TestTheDialClassesAreThreeAndOther(t *testing.T) {
	if got := DialClassNames(); !slices.Equal(got, []string{"no_lockfile", "unreachable", "handshake_rejected", "other"}) {
		t.Errorf("dial classes %v", got)
	}
}

func TestClassOfIsClosedAndNeverNoneOrADialClass(t *testing.T) {
	for c := ClassOther; c < ClassNoLockfile; c++ {
		if got := ClassOf(classNames[c]); got != c {
			t.Errorf("ClassOf(%q) = %v, want %v", classNames[c], got, c)
		}
	}
	// A daemon's string cannot select the client's own classes, and no text is a class.
	for _, in := range []string{"", "no_lockfile", "unreachable", "handshake_rejected", "Rate_Limited", "denied_user ",
		"the file /home/x/.env could not be read", "sk-ant-api03-CANARY"} {
		if got := ClassOf(in); got != ClassOther {
			t.Errorf("ClassOf(%q) = %q, want other", in, got.Name())
		}
	}
	if Class(200).Name() != "other" {
		t.Error("a Class outside the table must read as other")
	}
}

// What counts as a tool name is the daemon's rule, copied. The daemon module's
// TestTheClientsToolNameCheckAgreesWithTheDaemons runs the same corpus through
// both; this one pins the rule itself.
func TestToolRefusesEverythingThatIsNotAnID(t *testing.T) {
	for _, hostile := range []string{
		`there is no tool called "x" available in this turn`,
		"two words", "line\nbreak", "tab\there", `{"a":1}`, "../../etc/passwd", "emoji-\U0001F600",
		strings.Repeat("a", 65), "semi;colon", "slash/slash", `quote"quote`,
	} {
		if got := Tool(hostile); got != UnknownTool {
			t.Errorf("Tool(%q) = %q, want unknown_tool", hostile, got.s)
		}
	}
	for in, want := range map[string]string{
		"builtin__read_file":    "builtin__read_file",
		"Server__MixedCase":     "server__mixedcase",
		strings.Repeat("a", 64): strings.Repeat("a", 64),
		"v1.2:tool-x":           "v1.2:tool-x",
	} {
		if got := Tool(in).String(); got != want {
			t.Errorf("Tool(%q) = %q, want %q", in, got, want)
		}
	}
	if Tool("") != NoSubject {
		t.Error("Tool(\"\") must have no subject")
	}
}

func TestCommandIsAnIDOrUnknown(t *testing.T) {
	if got := Command("fix").String(); got != "fix" {
		t.Errorf("Command(fix) = %q", got)
	}
	for _, in := range []string{"", "two words", "fix; rm -rf", strings.Repeat("a", 65), "sk-ant-api03-\n"} {
		if got := Command(in); got != UnknownCommand {
			t.Errorf("Command(%q) = %q, want unknown", in, got.s)
		}
	}
}

func TestTheSchemasPatternsAreWhatTheValidatorsImplement(t *testing.T) {
	s := loadSchema(t)
	re := regexp.MustCompile(s.Patterns["id"])
	for _, in := range []string{"builtin__read_file", "a", "a.b:c-d", "UPPER", "with space", "", strings.Repeat("a", 65), "quote\"", "é"} {
		lowered := strings.ToLower(in)
		_, ok := asID(in)
		if want := re.MatchString(lowered) && in != ""; ok != want {
			t.Errorf("asID(%q) accepted=%v but the schema's id pattern says %v", in, ok, want)
		}
	}
}
