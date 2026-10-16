package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mochiii/protocol"
)

// decisions runs one check through the judge and returns what it asks control
// to do.
func decisions(j *correctionJudge, command, state string, o checkOutcome, restored bool) controlDecision {
	j.observe(ranCheck{command: command, judgedRun: judgedRun{outcome: o, stateHash: state}}, restored, nil)
	return j.decision
}

func failingN(sig string, n int) checkOutcome {
	names := make([]string, n)
	for i := range names {
		names[i] = "T" + string(rune('A'+i))
	}
	return checkOutcome{Kind: checkTest, Failing: names, Tests: n, Signature: sig}
}

// THE CONTROL LAW: the best is kept; getting worse goes back to it only on the
// SECOND check in a row worse than it, and only if that one is not better than
// the first; a change that is mending -- however far from passing -- is left
// alone; a circle starts the segment fresh, from the best.
//
// Neuter checks: restore on the first worse check (drop the worse >= 2); drop
// the improving guard (5 -> 3 build errors is reverted); drop the cut on a
// circle.
func TestTheControlLaw(t *testing.T) {
	j := newCorrectionJudge(correctionOn)
	if d := decisions(j, "go test ./...", "s1", failingN("A", 1), false); d.newBest == "" || d.restore != "" {
		t.Errorf("first check: %+v, want a new best and nothing else", d)
	}
	if d := decisions(j, "go test ./...", "s2", failingN("B", 2), false); d.restore != "" {
		t.Errorf("first worse check restored: %+v", d)
	}
	d := decisions(j, "go test ./...", "s3", failingN("C", 3), false)
	if d.restore == "" || d.cut == "" || d.command != "go test ./..." {
		t.Errorf("second worse, not improving: %+v, want a restore and a cut", d)
	}

	// Worse than the best twice, but mending: 1 -> 3 -> 2 is left alone.
	m := newCorrectionJudge(correctionOn)
	decisions(m, "go test ./...", "s1", failingN("A", 1), false)
	decisions(m, "go test ./...", "s2", failingN("B", 3), false)
	if d := decisions(m, "go test ./...", "s3", failingN("C", 2), false); d.restore != "" {
		t.Errorf("a mending run was reverted: %+v", d)
	}

	// A refactor's road: builds, breaks, mends, builds.
	r := newCorrectionJudge(correctionOn)
	for i, o := range []checkOutcome{{Kind: checkPass}, {Kind: checkBuild, Errors: 5, Signature: "e5"},
		{Kind: checkBuild, Errors: 3, Signature: "e3"}, {Kind: checkPass}} {
		if d := decisions(r, "go build ./...", string(rune('p'+i)), o, false); d.restore != "" || d.cut != "" {
			t.Errorf("refactor step %d: %+v", i, d)
		}
	}

	// A circle: back to code it had left, away from the best.
	c := newCorrectionJudge(correctionOn)
	decisions(c, "go test ./...", "best", failingN("A", 1), false)
	decisions(c, "go test ./...", "h2", failingN("B", 2), false)
	decisions(c, "go test ./...", "h3", failingN("C", 2), false)
	if d := decisions(c, "go test ./...", "h2", failingN("B", 2), false); d.cut == "" || d.restore == "" {
		t.Errorf("a circle: %+v, want a cut and a restore of the best", d)
	}
}

// A FAILURE THAT KEEPS COMING BACK CLIMBS THE LADDER: at the count it asks the
// model to find out why, one more starts the segment fresh, two more stop.
func TestTheLadder(t *testing.T) {
	j := newCorrectionJudge(correctionOn)
	j.attempts = 3
	want := []struct {
		ladder    int
		cut, stop bool
	}{{0, false, false}, {0, false, false}, {1, false, false}, {2, true, false}, {0, false, true}}
	for i, w := range want {
		d := decisions(j, "go test ./...", "s"+string(rune('a'+i)), failingN("X", 1), false)
		if d.ladder != w.ladder || (d.cut != "") != w.cut || (d.stop != "") != w.stop {
			t.Errorf("time %d: %+v, want %+v", i+1, d, w)
		}
	}
}

// threeTests is a module with three tests, one of them failing: TestA.
func threeTests(t *testing.T) (*Server, string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	return stageProject(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"m.go":   "package m\n\nfunc A() int { return 0 }\n\nfunc B() int { return 2 }\n\nfunc C() int { return 3 }\n",
		"m_test.go": "package m\n\nimport \"testing\"\n\n" +
			"func TestA(t *testing.T) { if A() != 1 { t.Fatal(\"A\") } }\n\n" +
			"func TestB(t *testing.T) { if B() != 2 { t.Fatal(\"B\") } }\n\n" +
			"func TestC(t *testing.T) { if C() != 3 { t.Fatal(\"C\") } }\n",
	})
}

// worseningScript runs the tests (1 failing), breaks B (2), breaks C (3) --
// the second worse check in a row -- then, in whatever segment follows, stops.
func worseningScript(t *testing.T) (string, *[][]byte) {
	t.Helper()
	base, _, bodies := agentUpstream(t,
		toolCallSSE("c1", "builtin__sandbox_exec", `{"command":"go test ./..."}`),
		toolCallSSE("c2", "builtin__propose_edit", `{"path":"m.go","search":"return 2","replace":"return 20"}`),
		toolCallSSE("c3", "builtin__sandbox_exec", `{"command":"go test ./..."}`),
		toolCallSSE("c4", "builtin__propose_edit", `{"path":"m.go","search":"return 3","replace":"return 30"}`),
		toolCallSSE("c5", "builtin__sandbox_exec", `{"command":"go test ./..."}`),
		toolCallSSE("c6", "builtin__finish_task", `{"status":"blocked","summary":"Stopping."}`),
	)
	return base, bodies
}

func controlServer(t *testing.T, base, dir string, checkpointPolicy string) *Server {
	t.Helper()
	cfg := taskPolicies()
	cfg.Builtin.Tools["sandbox_exec"] = PolicyAllow
	if checkpointPolicy != "" {
		cfg.Builtin.Tools["checkpoint"] = checkpointPolicy
	}
	cfg.Correction.Mode = correctionOn
	s := loopServer(t, base, cfg)
	s.workspace = dir
	return s
}

// THE JUDGE PUTS BACK THE BEST CODE, on a real `go test`: after the second
// check in a row worse than the first, the working copy holds the first
// check's code again, what the model had is saved as a checkpoint it can
// restore, and the segment ends there -- with no call asking for a handoff the
// judge has already written -- so the next one starts from the task state.
//
// Neuter checks: drop the restore in control; ask for a wrap-up on a cut.
func TestTheJudgePutsBackTheBestCode(t *testing.T) {
	_, dir := threeTests(t)
	base, bodies := worseningScript(t)
	s := controlServer(t, base, dir, PolicyAllow)
	run, p, runIt := directTaskRun(t, s, modeFix)
	if _, err := runIt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*bodies) != 6 {
		t.Fatalf("%d model calls, want 6: five in the first segment, none for a handoff, one after", len(*bodies))
	}
	now, err := p.stage.readStageFile("m.go")
	if err != nil || now == nil || strings.Contains(*now, "return 20") || strings.Contains(*now, "return 30") {
		t.Fatalf("the working copy's m.go after the judge: %v %v", now, err)
	}
	saved := p.stage.checkpoint(autoRestoreName)
	if saved == nil || saved.files["m.go"] == nil || !strings.Contains(*saved.files["m.go"], "return 30") {
		t.Errorf("what the model had is not saved as %q", autoRestoreName)
	}
	if run.restores != 1 {
		t.Errorf("restores = %d", run.restores)
	}
	opening := segmentOpening(t, (*bodies)[5])
	if !strings.Contains(opening, "The judge ended this segment: two checks in a row were worse than the best so far") ||
		!strings.Contains(opening, "the judge put back the code of check 1") {
		t.Errorf("segment 2 does not say what the judge did:\n%s", opening)
	}
}

// THE CODE THE JUDGE PUT BACK IS NOT A CIRCLE. The best here is a fixed A,
// not the untouched project; after the judge puts it back, the next check runs
// on code seen before -- by the judge's own hand, which is not the model going
// round. Without that, the restore's own result would cut the next segment too.
//
// Neuter check: drop p.restored = true after the judge's restore.
func TestTheJudgesRestoreIsNotACircle(t *testing.T) {
	_, dir := threeTests(t)
	base, calls, _ := agentUpstream(t,
		toolCallSSE("c1", "builtin__propose_edit", `{"path":"m.go","search":"return 0","replace":"return 1"}`),
		toolCallSSE("c2", "builtin__sandbox_exec", `{"command":"go test ./..."}`),
		toolCallSSE("c3", "builtin__propose_edit", `{"path":"m.go","search":"return 2","replace":"return 20"}`),
		toolCallSSE("c4", "builtin__sandbox_exec", `{"command":"go test ./..."}`),
		toolCallSSE("c5", "builtin__propose_edit", `{"path":"m.go","search":"return 3","replace":"return 30"}`),
		toolCallSSE("c6", "builtin__sandbox_exec", `{"command":"go test ./..."}`),
		// the judge puts back c2's code and ends the segment
		toolCallSSE("c7", "builtin__sandbox_exec", `{"command":"go test ./..."}`),
		toolCallSSE("c8", "builtin__finish_task", `{"status":"done","summary":"A is fixed."}`),
	)
	s := controlServer(t, base, dir, PolicyAllow)
	run, _, runIt := directTaskRun(t, s, modeFix)
	if _, err := runIt(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := run.ledger.Correction
	if run.ledger.State != protocol.TaskStateFinished || calls.Load() != 8 || run.restores != 1 || c.Counts[verdictOscillation] != 0 {
		t.Errorf("state %q after %d calls, %d restores, verdicts %v; want finished after 8, one restore, no circle",
			run.ledger.State, calls.Load(), run.restores, c.Counts)
	}
}

// A REFACTOR IS TOLD, NOT REVERTED, and nothing is put back without the
// consent a restore by the model would need: the checkpoint tool's "allow".
//
// Neuter checks: drop the refactor case; drop mayRestore from the restore.
func TestNoRestoreForARefactorOrWithoutConsent(t *testing.T) {
	for _, c := range []struct {
		mode, policy, says string
	}{
		{modeRefactor, PolicyAllow, "A refactor breaks code on its way, so nothing was put back"},
		{modeFix, "", "Go back to it if this is not on its way there"},
	} {
		_, dir := threeTests(t)
		base, bodies := worseningScript(t)
		s := controlServer(t, base, dir, c.policy)
		run, p, runIt := directTaskRun(t, s, c.mode)
		if _, err := runIt(context.Background()); err != nil {
			t.Fatal(err)
		}
		now, _ := p.stage.readStageFile("m.go")
		if now == nil || !strings.Contains(*now, "return 30") || run.restores != 0 {
			t.Errorf("%s/%q: the code was put back", c.mode, c.policy)
		}
		if got := toolResultOf(t, (*bodies)[5], "c5"); !strings.Contains(got, c.says) {
			t.Errorf("%s/%q: the result does not say %q:\n%s", c.mode, c.policy, c.says, got)
		}
	}
}

// PROGRESS IS NEW CODE OR A BETTER BEST, when the judge is on: going back to
// code seen before is not progress, where counting edits said it was.
//
// Neuter check: count edits in the progress mark when correcting.
func TestProgressSeesTheCircle(t *testing.T) {
	s, dir := stageProject(t, map[string]string{"a.txt": "one\n"})
	sink := stagedSink(t, s)
	run := &taskRun{ledger: newTaskLedger(dir, modeFix, "g", time.Now()), judge: newCorrectionJudge(correctionOn)}
	sink.task = run
	if _, err := sink.workingCopy(); err != nil {
		t.Fatal(err)
	}
	start := run.progressMark(sink)
	propose(t, s, sink, "a.txt", "one", "two")
	away := run.progressMark(sink)
	propose(t, s, sink, "a.txt", "two", "one")
	back := run.progressMark(sink)
	propose(t, s, sink, "a.txt", "one", "two")
	again := run.progressMark(sink)
	if start == away || back != away || again != away {
		t.Errorf("marks %q %q %q %q: going away is progress, coming back and going again is not", start, away, back, again)
	}
	propose(t, s, sink, "a.txt", "two", "three")
	if run.progressMark(sink) == again {
		t.Error("new code was not progress")
	}
}

// A PASS THAT COMES FROM A TEST NO LONGER RUNNING IS SAID TO BE ONE.
func TestAWeakenedTestIsSaid(t *testing.T) {
	for want, edit := range map[string][2]string{
		"removed TestB":                  {"func TestA(t *testing.T) {}\nfunc TestB(t *testing.T) {}\n", "func TestA(t *testing.T) {}\n"},
		"added a skip":                   {"func TestA(t *testing.T) {\n", "func TestA(t *testing.T) {\n\tt.Skip(\"later\")\n"},
		"removed TestB and added a skip": {"func TestB(t *testing.T) {}\nfunc TestC(t *testing.T) {\n", "func TestC(t *testing.T) {\n\tt.SkipNow()\n"},
		"":                               {"x := 1\n", "x := 2\n"},
	} {
		if got := testsWeakened(edit[0], edit[1]); got != want {
			t.Errorf("testsWeakened = %q, want %q", got, want)
		}
	}
	j := newCorrectionJudge(correctionOn)
	j.edited("m_test.go", "func TestB(t *testing.T) {}\n", "")
	j.observe(ranCheck{command: "go test ./...", judgedRun: judgedRun{outcome: checkOutcome{Kind: checkPass}, stateHash: "h"}}, false, nil)
	if !strings.Contains(j.note, "this run passed, but since the last check an edit to m_test.go removed TestB") {
		t.Errorf("note: %q", j.note)
	}
	j.observe(ranCheck{command: "go test ./...", judgedRun: judgedRun{outcome: checkOutcome{Kind: checkPass}, stateHash: "h"}}, false, nil)
	if j.note != "" {
		t.Errorf("the warning was said twice: %q", j.note)
	}
}

// THE LADDER'S LAST STEP STOPS THE TASK AS STUCK, with why: a failure that
// came back attempts+2 times is not going to yield to one more edit.
func TestTheLadderStopsTheTask(t *testing.T) {
	_, dir := threeTests(t)
	base, _, _ := agentUpstream(t,
		toolCallSSE("t1", "builtin__sandbox_exec", `{"command":"go test ./..."}`),
		toolCallSSE("e1", "builtin__propose_edit", `{"path":"m.go","search":"func C()","replace":"// one\nfunc C()"}`),
		toolCallSSE("t2", "builtin__sandbox_exec", `{"command":"go test ./..."}`),
		toolCallSSE("e2", "builtin__propose_edit", `{"path":"m.go","search":"// one","replace":"// two"}`),
		toolCallSSE("t3", "builtin__sandbox_exec", `{"command":"go test ./..."}`),
		// the second step cut the segment here, and the judge asks why
		textSSE("A still returns 0; the comments change nothing."),
		toolCallSSE("e3", "builtin__propose_edit", `{"path":"m.go","search":"// two","replace":"// three"}`),
		toolCallSSE("t4", "builtin__sandbox_exec", `{"command":"go test ./..."}`),
		toolCallSSE("x", "builtin__finish_task", `{"status":"blocked","summary":"unreachable"}`),
	)
	cfg := taskPolicies()
	cfg.Builtin.Tools["sandbox_exec"] = PolicyAllow
	cfg.Correction = MCPCorrectionConfig{Mode: correctionOn, Attempts: 2}
	s := loopServer(t, base, cfg)
	s.workspace = dir
	r, _, runIt := directTaskRun(t, s, modeFix)
	if _, err := runIt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.ledger.State != protocol.TaskStateStuck || !strings.Contains(r.ledger.Detail, "came back 4 times") {
		t.Errorf("state %q: %s", r.ledger.State, r.ledger.Detail)
	}
}

// mcp.correction.attempts is bounded and says so.
func TestTheAttemptsSettingIsBounded(t *testing.T) {
	for in, want := range map[int]int{0: 3, 1: 2, 2: 2, 4: 4, 5: 5, 9: 5} {
		if got := (MCPCorrectionConfig{Attempts: in}).resolvedAttempts(); got != want {
			t.Errorf("attempts %d resolved to %d, want %d", in, got, want)
		}
	}
	body := "{" + validTiers + `,"mcp":{"enabled":true,"correction":{"attempts":9}}}`
	cfg, err := loadMCPConfig(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if !mentions(cfg, "mcp.correction.attempts 9 is outside 2-5") {
		t.Errorf("warnings: %v", cfg.Warnings())
	}
}

// A MODEL THAT NEVER STOPS STILL ENDS. A scripted model that never finishes --
// it runs the tests, edits a new line in, breaks and mends code, saves and
// puts back its own checkpoint, or only talks, in an order a seed picks --
// meets the judge acting: restores, cuts, the ladder, re-checks, a stronger
// tier, a reflection. Every run ends by itself, in a state it can be resumed
// from, having asked the provider no more often than its call budget allows
// (and the one report after a last step, TestTheCallBudgetIsTheNumberOfCalls)
// -- and knowing how often it asked, though the provider reports no usage.
//
// Neuter checks: leave the handoff uncounted; drop the cut's reset in
// runLongTask.
func TestAModelThatNeverStopsStillEnds(t *testing.T) {
	const calls = 18
	for seed := uint64(1); seed <= 6; seed++ {
		_, dir := threeTests(t)
		var mu sync.Mutex
		rng, asked := rand.New(rand.NewPCG(seed, seed)), 0
		base := rawSSEServerFunc(t, func(body []byte) []string {
			mu.Lock()
			defer mu.Unlock()
			asked++
			if _, tools := sentModel(t, body); !tools {
				return textSSE("A returns 0, and the edits went elsewhere.")
			}
			id := fmt.Sprintf("c%d", asked)
			switch rng.IntN(8) {
			case 0, 1:
				return toolCallSSE(id, "builtin__sandbox_exec", `{"command":"go test ./..."}`)
			case 2:
				return toolCallSSE(id, "builtin__propose_edit", fmt.Sprintf(`{"path":"m.go","search":"func C()","replace":"// try %d\nfunc C()"}`, asked))
			case 3:
				return toolCallSSE(id, "builtin__propose_edit", `{"path":"m.go","search":"return 2","replace":"return 20"}`)
			case 4:
				return toolCallSSE(id, "builtin__propose_edit", `{"path":"m.go","search":"return 20","replace":"return 2"}`)
			case 5:
				return toolCallSSE(id, "builtin__checkpoint", `{"action":"save","name":"mine"}`)
			case 6:
				return toolCallSSE(id, "builtin__checkpoint", `{"action":"restore","name":"mine"}`)
			}
			return textSSE("Looking further.")
		})
		cfg := taskPolicies()
		cfg.Builtin.Tools["sandbox_exec"] = PolicyAllow
		cfg.Builtin.Tools["checkpoint"] = PolicyAllow
		cfg.Correction = MCPCorrectionConfig{Mode: correctionOn, EscalateTo: "strong"}
		cfg.Budget.Task = &MCPTaskBudgetConfig{MaxCalls: calls, SegmentCalls: 6}
		s := loopServer(t, base, cfg)
		s.workspace = dir
		withTiers(s, map[string]ModelTier{"primary": {Slug: "test-model", Active: true}, "strong": {Slug: "m-strong", Active: true}})
		run, _, runIt := directTaskRun(t, s, modeFix)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		_, err := runIt(ctx)
		cancel()
		mu.Lock()
		n := asked
		mu.Unlock()
		// A budget stop is a budget spent: anything else ending the run as one
		// would be a stop that is not the budget's, under the budget's name.
		if st := run.ledger.State; err != nil || (st != protocol.TaskStateStuck && st != protocol.TaskStateBudget) ||
			(st == protocol.TaskStateBudget && n < calls) || n > calls+1 || run.spent().Calls != n {
			t.Errorf("seed %d: state %q (%s), err %v, %d calls asked of a budget of %d, %d counted",
				seed, st, run.ledger.Detail, err, n, calls, run.spent().Calls)
		}
	}
}

// A DELETE IN THE PROJECT IS A NEW STATE, AND THE JUDGE DOES NOT UNDO IT. A
// file the task never edited, deleted in the project (delete_path) and followed
// by the copy, changes the code's state -- or the next check on the same edits
// reads as the same code giving another result, flaky. And the judge's
// snapshot from before a delete is not restored, as the model's checkpoints
// are not (TestACheckpointNeverUndoesADeleteOrMove).
//
// Neuter checks: leave reshaped out of stateHash; drop the stamp check in
// restore.
func TestADeleteInTheProjectIsANewState(t *testing.T) {
	s, dir := stageProject(t, map[string]string{
		"old.go": "package m\n\nvar Old = 1\n", "spare.go": "package m\n\nvar Spare = 1\n"})
	p := stagedSink(t, s)
	if msg, isErr := propose(t, s, p, "old.go", "var Old = 1", "var Old = 2"); isErr {
		t.Fatal(msg)
	}
	st, err := p.workingCopy()
	if err != nil || st == nil {
		t.Fatal(err)
	}
	snap, err := st.snapshot("best")
	if err != nil {
		t.Fatal(err)
	}
	deleted := func(rel string) {
		if err := os.Remove(filepath.Join(dir, rel)); err != nil {
			t.Fatal(err)
		}
		p.mirrorDelete(rel)
	}
	before := st.stateHash()
	deleted("spare.go")
	if st.stateHash() == before {
		t.Error("deleting a file the task never edited left the code's state as it was")
	}
	deleted("old.go")
	if res, _ := st.restoreSnapshot(snap); !res.IsError {
		t.Errorf("the judge's snapshot was restored past a delete: %q", res.Content)
	}
	if _, err := os.Lstat(filepath.Join(st.root, "old.go")); !os.IsNotExist(err) {
		t.Errorf("the deleted file is back in the copy (%v)", err)
	}
}

// THE JUDGE TELLS RATHER THAN UNDOES A DELETE: when the best code it holds is
// from before files were deleted or moved in the project, the model is told
// where the best was, and nothing is reverted.
//
// Neuter check: drop the reshaped condition in control.
func TestTheJudgeTellsRatherThanUndoesADelete(t *testing.T) {
	s, dir := stageProject(t, map[string]string{"a.go": "package m\n\nvar A = 1\n", "old.go": "package m\n"})
	p := stagedSink(t, s)
	run := &taskRun{ledger: &taskLedger{Mode: modeFix, State: protocol.TaskStateRunning},
		judge: newCorrectionJudge(correctionOn), mayRestore: true, budget: resolveTaskBudget(nil, nil)}
	p.task = run
	if msg, isErr := propose(t, s, p, "a.go", "var A = 1", "var A = 2"); isErr {
		t.Fatal(msg)
	}
	st, _ := p.workingCopy()
	snap, err := st.snapshot("best")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "old.go")); err != nil {
		t.Fatal(err)
	}
	p.mirrorDelete("old.go")
	const cmd = "go test ./..."
	run.judge.tracks[cmd] = &commandTrack{bestSnap: snap, bestCheck: 1, best: checkScore{failing: 1}}
	run.judge.decision = controlDecision{restore: "two checks in a row were worse", command: cmd, cut: "two checks in a row were worse"}
	p.control()
	if run.restores != 0 || !strings.Contains(run.judge.note, "Go back to it") {
		t.Errorf("%d restore(s); note %q", run.restores, run.judge.note)
	}
}
