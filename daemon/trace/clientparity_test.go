package trace

import (
	"slices"
	"strings"
	"testing"

	"mochiii/clients/tui/tuitrace"
)

// THE DAEMON'S TABLES AND THE TERMINAL CLIENT'S, COMPARED DIRECTLY.
//
// clients/tui/tuitrace is a second copy of the vocabulary (it is another module,
// and the daemon module requires it, not the other way round). The schema file is
// the single source of truth and each side is tested against it; this file is the
// two Go tables tested against each other, which is the one comparison neither
// module's own tests can make. If a stage, a class or the tool-name rule changes
// on one side, one of these goes red.

func TestTheDaemonsStagesAndTheClientsAreTheSchemasStagesExactly(t *testing.T) {
	s := loadSchema(t)
	var daemon []string
	for st := StageRequest; st < numStages; st++ {
		daemon = append(daemon, stageNames[st])
	}
	client := tuitrace.StageNames()
	for _, n := range client {
		if slices.Contains(daemon, n) {
			t.Errorf("stage %q is in both tables: one process owns each stage", n)
		}
	}
	union := append(slices.Clone(daemon), client...)
	slices.Sort(union)
	want := s.StageNames()
	slices.Sort(want)
	if !slices.Equal(union, want) {
		t.Errorf("the daemon's stages plus the client's are not the schema's:\n daemon+client %v\n schema        %v", union, want)
	}
}

func TestTheClientsErrorClassesAndTheDaemonsAreTheSchemasClosedList(t *testing.T) {
	s := loadSchema(t)
	var daemon []string
	for c := ClassOther; c < numClasses; c++ {
		daemon = append(daemon, classNames[c])
	}
	// Every class the daemon can write, the client can write (it reads a Done's class
	// from the daemon, so it must know every name the daemon uses).
	client := tuitrace.ClassNames()
	for _, n := range daemon {
		if !slices.Contains(client, n) {
			t.Errorf("the daemon's class %q is not in the terminal client's table", n)
		}
	}
	// The client's extra names are exactly its dial classes, and nobody else has them.
	for _, n := range client {
		if !slices.Contains(daemon, n) && (n == "other" || !slices.Contains(tuitrace.DialClassNames(), n)) {
			t.Errorf("the client has a class %q the daemon does not and that is not a dial class", n)
		}
	}
	for _, n := range tuitrace.DialClassNames() {
		if n != "other" && slices.Contains(daemon, n) {
			t.Errorf("the dial class %q is also in the daemon's table", n)
		}
	}
	got := slices.Clone(client)
	want := slices.Clone(s.ErrorClasses)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("the client's classes are not the schema's closed list:\n client %v\n schema %v", got, want)
	}
	if !slices.Equal(tuitrace.StatusNames(), s.Statuses) {
		t.Errorf("statuses: client %v, schema %v", tuitrace.StatusNames(), s.Statuses)
	}
}

// The tool-name rule, both implementations, one corpus.
func TestTheClientsToolNameCheckAgreesWithTheDaemons(t *testing.T) {
	corpus := []string{
		"", "builtin__read_file", "github__create_issue", "Server__MixedCase", "v1.2:tool-x", strings.Repeat("a", 64), strings.Repeat("a", 65),
		`there is no tool called "x" available in this turn`, "two words", "line\nbreak", "tab\there", `{"a":1}`, "../../etc/passwd",
		"emoji-\U0001F600", "semi;colon", "slash/slash", `quote"quote`, "sk-ant-api03-CANARYdummy", "plainzebra_toolnamecanary_90ab12",
		"UPPER_CASE", "trailing space ", "\x00", "é", "a\u2028b", strings.Repeat("é", 40),
	}
	for _, in := range corpus {
		if d, c := ToolName(in).s, tuitrace.Tool(in).String(); d != c {
			t.Errorf("ToolName(%q) = %q but the client's Tool(%q) = %q", in, d, in, c)
		}
	}
}
