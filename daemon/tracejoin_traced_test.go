//go:build trace && unix

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"mochiii/daemon/trace/tracecheck"
	"mochiii/protocol"
)

// THE JOIN FIELD, IN A DAEMON THAT IS TRACING (-tags trace, MOCHIII_TRACE=1).
//
// A client's id is used as the turn id, and only when it is exactly eight
// lower-case hex digits. Everything else the client could send is replaced by an
// id the daemon mints, and is nowhere in the trace, the daemon's log or its
// answer, in any spelling.
//
// NEUTER CHECKS (each on a throwaway copy of the repo; results are in the PR):
//   - protocol.ValidTraceID accepts any string: the invalid-id test goes red (a
//     hostile id is written as the turn id).
//   - traceTurnOf stops requiring the exact key (decodes with encoding/json, which
//     folds case): the upper-case-key and duplicate-key cases go red.
//   - traceTurnOf stops checking the request is a prompt: the status-request case
//     goes red.
//   - NewTurnWithID logs the id it rejected: the log assertion goes red.
//   - the daemon reads the id with the tracer OFF (traceTurnOf called outside
//     trace.On()): the default-build test, TestTheJoinIDIsIgnoredWithNoTracingAndSaysNothing,
//     goes red once the id is logged or echoed.

var reJoinTurn = regexp.MustCompile(`"turn":"([^"]*)"`)

// traceLines is every line of the daemon's trace file so far.
func traceLines(t *testing.T) []string {
	t.Helper()
	_, files := traceTree(t, traceDirOf(t))
	if len(files) != 1 {
		t.Fatalf("%d trace files, want 1", len(files))
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// turnsAfter is the set of turn ids on the lines written after the first `from`.
func turnsAfter(t *testing.T, from int) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, l := range traceLines(t)[from:] {
		m := reJoinTurn.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("a line with no turn: %s", l)
		}
		out[m[1]]++
	}
	return out
}

func TestTheJoinIDIsTheTurnIDOfEveryDaemonLineOfThatRequest(t *testing.T) {
	model := newE2EModel(t, byToolResults(
		toolCallSSE("c1", "builtin__read_file", `{"path":"greet.go"}`),
		withUsage(textSSE("ok"), 100, 5, 0.0002),
	))
	d := startTraced(t, model, map[string]string{"greet.go": e2eGreet}, "MOCHIII_TRACE=1")

	turn := d.request(t, protocol.PromptRequest{Prompt: "read it", Session: "a-session", Trace: "a1b2c3d4"}, e2eApproveAll)
	if turn.done.Error != "" {
		t.Fatalf("the turn failed: %s", turn.done.Error)
	}
	lines := traceLines(t)
	if len(lines) < 5 {
		t.Fatalf("only %d lines:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	for _, l := range lines {
		if !strings.Contains(l, `"turn":"a1b2c3d4"`) {
			t.Errorf("a line does not carry the id the client named: %s", l)
		}
	}
	// The sid and the sequence are untouched by it: sid on the first line only, n from 1.
	if !strings.Contains(lines[0], `"n":1,`) || !strings.Contains(lines[0], `"sid":"`) || strings.Contains(lines[1], `"sid"`) {
		t.Errorf("the first line should carry n=1 and sid, the second no sid:\n%s\n%s", lines[0], lines[1])
	}
	schema, err := tracecheck.Load("trace/trace-schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if errs := schema.CheckFile([]byte(strings.Join(lines, "\n") + "\n")); len(errs) != 0 {
		t.Errorf("a trace with a client's id does not fit the schema: %v", errs[0])
	}

	// A second request with no id gets one the daemon minted; a third names another.
	from := len(lines)
	d.request(t, protocol.PromptRequest{Prompt: "again"}, e2eApproveAll)
	got := turnsAfter(t, from)
	if len(got) != 1 {
		t.Fatalf("one request, turn ids %v", got)
	}
	for id := range got {
		if !hex8re.MatchString(id) || id == "a1b2c3d4" {
			t.Errorf("a request with no id was given %q, want a fresh minted id", id)
		}
	}
	from = len(traceLines(t))
	d.request(t, protocol.PromptRequest{Prompt: "third", Trace: "00000000"}, e2eApproveAll)
	if got := turnsAfter(t, from); len(got) != 1 || got["00000000"] == 0 {
		t.Errorf("a request that named 00000000 has turn ids %v", got)
	}
}

var hex8re = regexp.MustCompile(`^[0-9a-f]{8}$`)

func TestAnInvalidJoinIDIsReplacedAndIsNowhereInTheTraceTheLogOrTheAnswer(t *testing.T) {
	model := newE2EModel(t, saysHi)
	d := startTraced(t, model, map[string]string{"greet.go": e2eGreet}, "MOCHIII_TRACE=1")

	// Hostile values come in the two kinds the leak test uses: a key shape the
	// scrubber knows, and a plain string it does not.
	const (
		keyShape = "sk-ant-api03-JOINCANARYdummyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		plain    = "plainzebra-joinidcanary-6e0f27"
	)
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	cases := []struct {
		name  string
		extra string
		// notTheID: values the daemon must NOT have adopted. For the cases whose
		// value has the right shape, the id is not a canary but a real choice.
		notTheID []string
	}{
		{"upper case", `"trace":"A1B2C3D4"`, nil},
		{"seven digits", `"trace":"a1b2c3d"`, nil},
		{"nine digits", `"trace":"a1b2c3d4e"`, nil},
		{"not hex", `"trace":"g1b2c3d4"`, nil},
		{"empty", `"trace":""`, nil},
		{"newline", `"trace":"a1b2c3\n4"`, nil},
		{"line injection", `"trace":` + q(`a1b2c3d4","stage":"result","status":"fail`), nil},
		{"key shape", `"trace":` + q(keyShape), nil},
		{"plain string", `"trace":` + q(plain), nil},
		{"unicode digits", `"trace":` + q("١٢٣٤٥٦٧٨"), nil},
		{"huge", `"trace":` + q(strings.Repeat(plain, 4000)), nil},
		{"escaped, so not eight bytes", `"trace":"\u0063\u0030ffee42"`, []string{"c0ffee42"}},
		{"number", `"trace":12345678`, []string{"12345678"}},
		{"array", `"trace":["c0ffee42"]`, []string{"c0ffee42"}},
		{"object", `"trace":{"c0ffee42":1}`, []string{"c0ffee42"}},
		{"null", `"trace":null`, nil},
		{"duplicate key", `"trace":"c0ffee42","trace":"deadbeef"`, []string{"c0ffee42", "deadbeef"}},
		{"upper-case key", `"TRACE":"c0ffee42"`, []string{"c0ffee42"}},
		{"capitalised key", `"Trace":"c0ffee42"`, []string{"c0ffee42"}},
	}
	for _, c := range cases {
		from := 0
		if _, err := os.Stat(traceDirOf(t)); err == nil { // the first request creates it
			from = len(traceLines(t))
		}
		stream, done := d.rawPrompt(t, d.joinBody(c.extra))
		if done.Error != "" || !strings.Contains(stream, "hi") {
			t.Errorf("%s: the request did not simply succeed: %q %q", c.name, done.Error, stream)
		}
		got := turnsAfter(t, from)
		if len(got) != 1 {
			t.Errorf("%s: turn ids %v, want one", c.name, got)
		}
		for id := range got {
			if !hex8re.MatchString(id) {
				t.Errorf("%s: the turn id %q is not an id the daemon minted", c.name, id)
			}
			for _, bad := range c.notTheID {
				if id == bad {
					t.Errorf("%s: the daemon adopted %q, which it must not", c.name, bad)
				}
			}
		}
	}

	// Not one of the rejected values is in the trace directory (file names too),
	// the daemon's log or what it answered, in any spelling the leak test checks.
	blob, _ := traceTree(t, traceDirOf(t))
	haystack := string(blob) + d.log.String()
	for _, v := range []string{keyShape, plain, "c0ffee42", "deadbeef", "A1B2C3D4", "g1b2c3d4", `"stage":"result","status":"fail`} {
		for _, form := range canaryForms(v) {
			if strings.Contains(haystack, form) {
				t.Errorf("a rejected id, %q, is in the trace or the daemon log as %q", v, form)
			}
		}
	}
	for _, l := range traceLines(t) {
		if len(l) > 512 {
			t.Errorf("a line of %d bytes", len(l))
		}
	}
	schema, _ := tracecheck.Load("trace/trace-schema.json")
	if errs := schema.CheckFile([]byte(strings.Join(traceLines(t), "\n") + "\n")); len(errs) != 0 {
		t.Errorf("after hostile ids the trace does not fit the schema: %v", errs[0])
	}
	// The modes are the ones the trace always had, with a client's id in play.
	_, files := traceTree(t, traceDirOf(t))
	for p, want := range map[string]os.FileMode{traceDirOf(t): 0o700, files[0]: 0o600} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s is %v (%v), want %o", p, fi, err, want)
		}
	}
	if strings.Contains(d.log.String(), "plainzebra") {
		t.Errorf("a rejected id is in the daemon's log")
	}
}

// The id is for a prompt. A request of another kind that carries the key is not
// a request that names its turn.
func TestTheJoinIDIsIgnoredOnARequestThatIsNotAPrompt(t *testing.T) {
	model := newE2EModel(t, saysHi)
	d := startTraced(t, model, map[string]string{"greet.go": e2eGreet}, "MOCHIII_TRACE=1")
	conn, _, dec := d.dial(t)
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, `{"protocol_version":%d,"status":true,"trace":"c0ffee42"}`+"\n", protocol.ProtocolVersion); err != nil {
		t.Fatal(err)
	}
	var reply json.RawMessage
	if err := dec.Decode(&reply); err != nil {
		t.Fatalf("no answer to a status request: %v", err)
	}
	got := turnsAfter(t, 0)
	if len(got) != 1 {
		t.Fatalf("turn ids %v", got)
	}
	for id := range got {
		if id == "c0ffee42" {
			t.Errorf("a status request chose its own turn id")
		}
	}
	if !strings.Contains(strings.Join(traceLines(t), "\n"), `"tool":"status"`) {
		t.Errorf("the request was not traced as a status request:\n%s", strings.Join(traceLines(t), "\n"))
	}
}
