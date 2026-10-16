package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"mochiii/protocol"
)

// verdicts runs one check through the judge and returns what it said.
func verdicts(j *correctionJudge, command, state string, o checkOutcome, restored bool, own func(string) bool) []string {
	before := len(j.events)
	j.observe(ranCheck{command: command, judgedRun: judgedRun{outcome: o, stateHash: state}}, restored, own)
	var out []string
	for _, ev := range j.events[before:] {
		out = append(out, ev.Verdict)
	}
	return out
}

func failingTests(sig string, names ...string) checkOutcome {
	return checkOutcome{Kind: checkTest, Failing: names, Tests: len(names), Signature: sig}
}

var passing = checkOutcome{Kind: checkPass}

// THE JUDGE COMPARES EACH CHECK WITH THE LAST ONE OF THE SAME COMMAND: fewer
// failures is better, more is worse (an overshoot), the same failure again is
// the same, and a pass is the end.
//
// Neuter checks: swap the improved and overshoot cases; compare with the best
// score instead of the last (5 -> 3 build errors is then an overshoot).
func TestTheJudgeComparesEachCheckWithTheLast(t *testing.T) {
	j := newCorrectionJudge(correctionShadow)
	steps := []struct {
		state string
		o     checkOutcome
		want  []string
	}{
		{"s1", failingTests("A", "T1", "T2", "T3"), []string{verdictFirst}},
		{"s2", failingTests("B", "T1"), []string{verdictImproved}},
		{"s3", failingTests("C", "T1", "T4", "T5"), []string{verdictOvershoot}},
		{"s4", failingTests("C", "T1", "T4", "T5"), []string{verdictSame}},
		{"s5", passing, []string{verdictConverged}},
		{"s6", failingTests("D", "T9"), []string{verdictOvershoot}},
	}
	for i, st := range steps {
		if got := verdicts(j, "go test ./...", st.state, st.o, false, nil); !reflect.DeepEqual(got, st.want) {
			t.Errorf("check %d: %v, want %v", i+1, got, st.want)
		}
	}
	// A refactor that is recovering is improving, however far from passing.
	r := newCorrectionJudge(correctionShadow)
	for i, n := range []int{5, 3, 1} {
		o := checkOutcome{Kind: checkBuild, Errors: n, Signature: string(rune('a' + i))}
		got := verdicts(r, "go build ./...", string(rune('p'+i)), o, false, nil)
		if want := map[int]string{0: verdictFirst, 1: verdictImproved, 2: verdictImproved}[i]; !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("build errors %d: %v, want %s", n, got, want)
		}
	}
}

// A FILE THAT NO LONGER LOADS IS WORSE THAN ITS TESTS FAILING -- through the
// parser, as pytest 9.1 prints it: two failing tests, then a syntax error that
// stops the module importing, is an overshoot. MEASURED 2026-10-16 before the
// fix: read as one failing test, it scored as an improvement.
//
// Neuter check: read pytest's module ERROR as a failing test.
func TestAModuleThatNoLongerImportsIsWorse(t *testing.T) {
	failed := outputLines(
		"FAILED test_m.py::test_a - assert 0 == 1",
		"FAILED test_m.py::test_b - assert 2 == 20",
		"========================= 2 failed, 1 passed in 0.03s ==========================")
	broken := outputLines(
		"__________________________ ERROR collecting test_m.py __________________________",
		"E   SyntaxError: invalid syntax",
		"=========================== short test summary info ============================",
		"ERROR test_m.py",
		"!!!!!!!!!!!!!!!!!!!! Interrupted: 1 error during collection !!!!!!!!!!!!!!!!!!!!")
	j := newCorrectionJudge(correctionShadow)
	first := verdicts(j, "pytest", "s1", parseCheck("pytest", exitedOne(), failed, ""), false, nil)
	then := verdicts(j, "pytest", "s2", parseCheck("pytest", execOutcome{ran: true, exit: 2}, broken, ""), false, nil)
	if !reflect.DeepEqual(first, []string{verdictFirst}) || !reflect.DeepEqual(then, []string{verdictOvershoot}) {
		t.Errorf("2 failing tests, then a module that does not import: %v then %v, want first then overshoot", first, then)
	}
}

// A FAILING TEST THE TASK HAS JUST WRITTEN IS ITS REPRODUCTION, NOT A
// REGRESSION. The same new failure in a test someone else wrote is.
//
// Neuter check: drop the own-test subtraction.
func TestATestTheTaskWroteIsNotARegression(t *testing.T) {
	mine := func(name string) bool { return name == "TestRepro" }
	for _, c := range []struct {
		own  func(string) bool
		want string
	}{{mine, verdictChanged}, {func(string) bool { return false }, verdictOvershoot}} {
		j := newCorrectionJudge(correctionShadow)
		verdicts(j, "go test ./...", "s1", failingTests("A", "TestBug"), false, c.own)
		if got := verdicts(j, "go test ./...", "s2", failingTests("B", "TestBug", "TestRepro"), false, c.own); !reflect.DeepEqual(got, []string{c.want}) {
			t.Errorf("a new failing TestRepro: %v, want %s", got, c.want)
		}
	}
}

// THE JUDGE SEES THE CIRCLE: code that goes back to what it was, after
// something else -- but not when the model restored its own checkpoint, and
// not when it went back to the untouched project, which is starting over.
//
// Neuter checks: drop the restored exemption; drop the pristine exemption;
// drop the oscillated guard (the circle is reported twice).
func TestTheJudgeSeesTheCircle(t *testing.T) {
	run := func(states []string, restoredLast bool) []string {
		j := newCorrectionJudge(correctionShadow)
		sigs := []string{"X", "Y", "X"}
		var last []string
		for i, s := range states {
			last = verdicts(j, "go test ./...", s, failingTests(sigs[i], "T"+sigs[i]), restoredLast && i == 2, nil)
		}
		return last
	}
	count := func(vs []string, v string) (n int) {
		for _, x := range vs {
			if x == v {
				n++
			}
		}
		return n
	}
	if got := run([]string{"h1", "h2", "h1"}, false); count(got, verdictOscillation) != 1 {
		t.Errorf("h1 -> h2 -> h1 said %v; want one oscillation", got)
	}
	if got := run([]string{"h1", "h2", "h1"}, true); count(got, verdictOscillation) != 0 {
		t.Errorf("a restored checkpoint was called a circle: %v", got)
	}
	if got := run([]string{pristineState, "h2", pristineState}, false); count(got, verdictOscillation) != 0 {
		t.Errorf("going back to the untouched project was called a circle: %v", got)
	}
	// The failure alone coming back, on new code, is the circle too.
	if got := run([]string{"h1", "h2", "h3"}, false); count(got, verdictOscillation) != 1 {
		t.Errorf("X -> Y -> X on new code said %v; want one oscillation", got)
	}
}

// A FAILURE THAT KEEPS COMING BACK IS NAMED ON ITS THIRD TIME, and once.
func TestARecurringFailureIsNamedOnItsThirdTime(t *testing.T) {
	j := newCorrectionJudge(correctionShadow)
	for i := 1; i <= 4; i++ {
		got := verdicts(j, "go test ./...", string(rune('a'+i)), failingTests("X", "T1"), false, nil)
		if has := contains(got, verdictRecurrence); has != (i == correctionRecurrence) {
			t.Errorf("time %d: %v", i, got)
		}
	}
}

// A FLAKY COMMAND IS NOT EVIDENCE. The same code giving two results is named
// once, and from then on that command's results are not compared -- a flaky
// test would otherwise read as an attempt that broke, or fixed, the code.
// Other commands are judged as before.
func TestAFlakyCommandIsNotJudged(t *testing.T) {
	j := newCorrectionJudge(correctionShadow)
	verdicts(j, "go test ./...", "h1", failingTests("X", "T1"), false, nil)
	if got := verdicts(j, "go test ./...", "h1", passing, false, nil); !reflect.DeepEqual(got, []string{verdictFlaky}) {
		t.Errorf("same code, another result: %v, want flaky", got)
	}
	if got := verdicts(j, "go test ./...", "h2", failingTests("Y", "T1", "T2", "T3"), false, nil); len(got) != 0 {
		t.Errorf("a flaky command was still judged: %v", got)
	}
	if got := verdicts(j, "go vet ./...", "h2", checkOutcome{Kind: checkVet, Errors: 1, Signature: "V"}, false, nil); !reflect.DeepEqual(got, []string{verdictFirst}) {
		t.Errorf("another command: %v, want first", got)
	}
}

// COMMANDS ARE COMPARED ONLY WITH THEMSELVES, and a timeout is unknown, not
// worse: three vet findings after one failing test is not an overshoot, and
// the run after a timeout is compared with the last run that said something.
func TestCommandsAreComparedOnlyWithThemselves(t *testing.T) {
	j := newCorrectionJudge(correctionShadow)
	steps := []struct {
		cmd  string
		o    checkOutcome
		want string
	}{
		{"go test ./...", failingTests("A", "T1"), verdictFirst},
		{"go  vet   ./...", checkOutcome{Kind: checkVet, Errors: 3, Signature: "V"}, verdictFirst},
		{"go test ./...", checkOutcome{Kind: checkTimeout, Signature: "T"}, verdictUnknown},
		{"go test  ./...", failingTests("A", "T1"), verdictSame},
	}
	for i, st := range steps {
		if got := verdicts(j, st.cmd, string(rune('a'+i)), st.o, false, nil); !reflect.DeepEqual(got, []string{st.want}) {
			t.Errorf("check %d (%s): %v, want %s", i+1, st.cmd, got, st.want)
		}
	}
}

// A HUNT IS NEVER JUDGED: its failing runs are the proof of what it found.
func TestAHuntIsNeverJudged(t *testing.T) {
	run := &taskRun{ledger: &taskLedger{Mode: modeHunt}, judge: newCorrectionJudge(correctionShadow)}
	run.judgeCheck(ranCheck{command: "go test ./...", judgedRun: judgedRun{outcome: failingTests("A", "T1"), stateHash: "h"}}, false, nil)
	if len(run.judge.events) != 0 {
		t.Errorf("a hunt was judged: %+v", run.judge.events)
	}
}

// THE LEDGER KEEPS THE NEWEST VERDICTS, AND COUNTS THEM ALL.
func TestFlushedVerdictsAreBoundedAndCounted(t *testing.T) {
	run := &taskRun{ledger: &taskLedger{Mode: modeFix}, judge: newCorrectionJudge(correctionShadow)}
	for i := 0; i < 30; i++ {
		run.judge.observe(ranCheck{command: "go test ./...", judgedRun: judgedRun{outcome: failingTests("A", "T1"), stateHash: "h"}}, false, nil)
	}
	run.flushCorrection(func(s string) string { return s })
	for i := 0; i < 30; i++ {
		run.judge.observe(ranCheck{command: "go vet ./...", judgedRun: judgedRun{outcome: passing, stateHash: "h"}}, false, nil)
	}
	if fresh := run.flushCorrection(func(s string) string { return s }); len(fresh) != 30 {
		t.Fatalf("the second flush returned %d verdicts, want 30", len(fresh))
	}
	c := run.ledger.Correction
	total := 0
	for _, n := range c.Counts {
		total += n
	}
	if len(c.Events) != maxCorrectionEvents || c.Events[len(c.Events)-1].Command != "go vet ./..." || total != 61 {
		t.Errorf("kept %d events (last %+v), counted %d; want %d, the newest last, 61 counted",
			len(c.Events), c.Events[len(c.Events)-1], total, maxCorrectionEvents)
	}
	if c.Mode != correctionShadow {
		t.Errorf("mode %q", c.Mode)
	}
}

// THE STATE HASH NAMES THE CODE: an edit changes it, undoing the edit brings
// back the untouched project's hash, and a file a command changed counts.
//
// Neuter checks: drop the "back to the project's own" skip (the undone edit
// keeps a hash of its own); drop byCommand from the paths.
func TestTheStateHashNamesTheCode(t *testing.T) {
	s, _ := stageProject(t, map[string]string{"a.txt": "one\n", "b.txt": "two\n"})
	sink := stagedSink(t, s)
	st, err := sink.workingCopy()
	if err != nil || st == nil {
		t.Fatalf("no working copy: %v", err)
	}
	if h := st.stateHash(); h != pristineState {
		t.Fatalf("untouched copy hashes to %s, want the pristine %s", h, pristineState)
	}
	if _, isErr := propose(t, s, sink, "a.txt", "one", "ONE"); isErr {
		t.Fatal("edit refused")
	}
	edited := st.stateHash()
	if edited == pristineState {
		t.Fatal("an edit did not change the hash")
	}
	propose(t, s, sink, "a.txt", "ONE", "one")
	if h := st.stateHash(); h != pristineState {
		t.Errorf("undoing the edit gave %s, want the pristine hash back", h)
	}
	propose(t, s, sink, "a.txt", "one", "ONE")
	if h := st.stateHash(); h != edited {
		t.Errorf("redoing the edit gave %s, want %s again", h, edited)
	}
	if err := os.WriteFile(filepath.Join(st.root, "b.txt"), []byte("changed by a command\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st.byCommand = map[string]string{"b.txt": "gofmt -w ."}
	if h := st.stateHash(); h == edited {
		t.Error("a file a command changed did not change the hash")
	}
}

// A TEST IS THE TASK'S OWN when its func is in a test file the task changed,
// or, for pytest, when its file is one the task changed.
func TestDefinesTestFindsTheTasksOwnTests(t *testing.T) {
	s, _ := stageProject(t, map[string]string{"m/m_test.go": "package m\n", "tests/test_x.py": "# tests\n"})
	sink := stagedSink(t, s)
	if _, isErr := propose(t, s, sink, "m/m_test.go", "package m\n",
		"package m\n\nimport \"testing\"\n\nfunc TestNew(t *testing.T) {}\n"); isErr {
		t.Fatal("edit refused")
	}
	if _, isErr := propose(t, s, sink, "tests/test_x.py", "# tests\n", "def test_a():\n    assert False\n"); isErr {
		t.Fatal("edit refused")
	}
	for name, want := range map[string]bool{
		"TestNew": true, "TestNew/case": true, "TestOld": false,
		"tests/test_x.py::test_a": true, "tests/test_y.py::test_a": false,
	} {
		if got := sink.stage.definesTest(name); got != want {
			t.Errorf("definesTest(%q) = %v, want %v", name, got, want)
		}
	}
}

// vetProject is a module whose one file go vet refuses, deterministically.
func vetProject(t *testing.T) (*Server, string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	return stageProject(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"m.go":   "package m\n\nimport \"fmt\"\n\nfunc Say(s string) {\n\tfmt.Printf(\"%d\\n\", s)\n}\n",
	})
}

// SHADOW VERDICTS NEVER REACH THE MODEL. The two ways anything reaches it in a
// long task are the tool results and the task state each segment opens with:
// a check's result reads the same with a judge as without one, and the task
// state renders the same with the judge's record as without it.
//
// Neuter check: render the ledger's Correction in renderTaskState.
func TestShadowVerdictsNeverReachTheModel(t *testing.T) {
	s, dir := vetProject(t)
	vet := json.RawMessage(`{"command":"go vet ./..."}`)
	judged := stagedSink(t, s)
	judged.task = &taskRun{ledger: newTaskLedger(dir, modeFix, "fix it", time.Now()), judge: newCorrectionJudge(correctionShadow)}
	plain := stagedSink(t, s)
	a, err := s.builtinSandboxExecStaged(context.Background(), vet, judged)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.builtinSandboxExecStaged(context.Background(), vet, plain)
	if a.Content != b.Content || a.IsError {
		t.Fatalf("the result with a judge differs from the one without:\n%q\n%q", a.Content, b.Content)
	}
	if judged.task.flushCorrection(func(s string) string { return s }) == nil {
		t.Fatal("the judge saw nothing: the test proves nothing")
	}
	l := judged.task.ledger
	budget := resolveTaskBudget(nil, nil)
	with := l.renderTaskState(budget, taskSpend{}, 2, false)
	saved := l.Correction
	l.Correction = nil
	if without := l.renderTaskState(budget, taskSpend{}, 2, false); with != without {
		t.Errorf("the task state shows the judge's record:\n%s", with)
	}
	l.Correction = saved
}

// A REAL CHECK, READ AND JUDGED, END TO END: a long task runs go vet, which
// fails; fixes the file; runs it again, which passes; and finishes. The
// failing run is a result, not an error; it is parsed as a vet finding at its
// line; the judge records "first" then "converged" in the ledger.
func TestALongTaskRecordsItsVerdicts(t *testing.T) {
	_, dir := vetProject(t)
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__sandbox_exec", `{"command":"go vet ./..."}`),
		toolCallSSE("c2", "builtin__propose_edit", `{"path":"m.go","search":"%d","replace":"%s"}`),
		toolCallSSE("c3", "builtin__sandbox_exec", `{"command":"go vet ./..."}`),
		toolCallSSE("c4", "builtin__finish_task", `{"status":"done","summary":"Fixed the format verb."}`),
	)
	cfg := taskPolicies()
	cfg.Builtin.Tools["sandbox_exec"] = PolicyAllow
	s := loopServer(t, base, cfg)
	s.workspace = dir
	run, p, runIt := directTaskRun(t, s, modeFix)
	if _, err := runIt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if run.ledger.State != protocol.TaskStateFinished {
		t.Fatalf("state %q (%s)", run.ledger.State, run.ledger.Detail)
	}
	if len(p.checks) != 2 || p.checks[0].passed || !p.checks[1].passed {
		t.Fatalf("checks %+v", p.checks)
	}
	first := p.checks[0].outcome
	if first.Kind != checkVet || first.Errors != 1 || !reflect.DeepEqual(first.Locations, []string{"m.go:6"}) {
		t.Errorf("the failing vet was read as %+v", first)
	}
	if p.checks[0].stateHash != pristineState || p.checks[1].stateHash == pristineState {
		t.Errorf("state hashes %s, %s", p.checks[0].stateHash, p.checks[1].stateHash)
	}
	c := run.ledger.Correction
	if c == nil || c.Mode != correctionShadow || c.Counts[verdictFirst] != 1 || c.Counts[verdictConverged] != 1 || len(c.Events) != 2 {
		t.Errorf("the ledger's record: %+v", c)
	}
}

// OFF IS OFF: no judge, no record.
func TestCorrectionOffKeepsNoRecord(t *testing.T) {
	_, dir := vetProject(t)
	base, _, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__sandbox_exec", `{"command":"go vet ./..."}`),
		toolCallSSE("c2", "builtin__finish_task", `{"status":"blocked","summary":"Cannot fix it."}`),
	)
	cfg := taskPolicies()
	cfg.Builtin.Tools["sandbox_exec"] = PolicyAllow
	cfg.Correction.Mode = correctionOff
	s := loopServer(t, base, cfg)
	s.workspace = dir
	run, _, runIt := directTaskRun(t, s, modeFix)
	if _, err := runIt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if run.judge != nil || run.ledger.Correction != nil {
		t.Errorf("mode off still judged: %+v", run.ledger.Correction)
	}
}
