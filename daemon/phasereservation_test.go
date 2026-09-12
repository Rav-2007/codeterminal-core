package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// The arithmetic. Two properties, both about a reservation that can only ever
// take budget away from a phase and never hand it more.
// ---------------------------------------------------------------------------

// A pre-answer share of ZERO does not mean "unlimited", but turnLedger reads it
// that way -- so the reservation would switch itself off at exactly the budgets
// where the answering phase can least afford to be starved.
// The turn ceiling this test configures, named once so the config and the
// assertion cannot drift apart.
const (
	turnCeilingSeconds = 1
	turnCeiling        = turnCeilingSeconds * time.Second
)

func TestThePreAnswerByteShareIsNeverZero(t *testing.T) {
	// Zero is absent deliberately: resolvedMaxTotalToolBytes never returns it,
	// and a share of zero out of a budget of zero is arithmetic, not a hole.
	for _, budget := range []int{1, 2, 3, 7, 128 * 1024} {
		for _, phases := range []int{2, 3, 4, 8} {
			for answerAt := 1; answerAt < phases; answerAt++ {
				share, _ := preAnswerReservation(budget, time.Minute, time.Now(), answerAt, phases)
				if share < 1 {
					t.Errorf("budget=%d phases=%d answerAt=%d: share=%d, which turnLedger.toolByteCap "+
						"reads as NO CAP -- the reservation silently disabled itself",
						budget, phases, answerAt, share)
				}
			}
		}
	}
}

// The reservation exists to protect the answering phase, and the moment it can
// RAISE a ceiling it is instead a way for the orchestrator to spend past a
// privacy bound the user set. Neither half may exceed the turn's own budget.
func TestThePreAnswerReservationCanOnlyTighten(t *testing.T) {
	start := time.Now()
	for _, budget := range []int{1, 1000, 128 * 1024} {
		for _, phases := range []int{1, 2, 4, 9} {
			for answerAt := 0; answerAt < phases; answerAt++ {
				share, deadline := preAnswerReservation(budget, 10*time.Minute, start, answerAt, phases)
				if share > budget {
					t.Errorf("budget=%d phases=%d answerAt=%d: share=%d exceeds the configured budget",
						budget, phases, answerAt, share)
				}
				if !deadline.IsZero() && deadline.After(start.Add(10*time.Minute)) {
					t.Errorf("budget=%d phases=%d answerAt=%d: reserved deadline %v falls after the turn's own",
						budget, phases, answerAt, deadline)
				}
			}
		}
	}
}

// A first-phase answerer has nothing to reserve FROM. Reserving anyway would
// cap the only phase there is, against itself.
func TestNothingIsReservedWhenTheFirstPhaseAnswers(t *testing.T) {
	share, deadline := preAnswerReservation(4096, time.Minute, time.Now(), 0, 3)
	if share != 4096 {
		t.Errorf("share=%d, want the full budget: there is no earlier phase to reserve against", share)
	}
	if !deadline.IsZero() {
		t.Errorf("deadline=%v, want zero (no cap): capping the answering phase is the bug, not the fix", deadline)
	}
}

// ---------------------------------------------------------------------------
// The bound that had no reservation at all.
// ---------------------------------------------------------------------------

// A SLOW EARLY PHASE MUST NOT CONSUME THE ANSWERING PHASE'S TIME.
//
// max_total_tool_bytes, max_turn_iterations and turn_timeout_seconds all bound
// the TURN, and a pipeline is one turn. Iterations were reserved by accident
// (max_iterations is per phase). Bytes were reserved on purpose. Time was
// reserved by nothing: every phase saw turnStart+turn_timeout, so a Researcher
// that was merely SLOW -- not wrong, not looping, just talking to a slow
// provider -- could spend the whole turn and leave the Coder a deadline already
// in the past. That is the identical starvation the byte reservation exists to
// prevent, in the bound §14 measured closest to biting: six to seven minutes of
// four-phase pipeline against a ten-minute default.
//
// Scripted by ROLE rather than by call order, so the assertion does not depend
// on how many calls the Researcher gets through before its share runs out --
// which is the very thing under test.
func TestASlowEarlyPhaseCannotConsumeTheAnswerPhasesTime(t *testing.T) {
	const phaseDelay = 600 * time.Millisecond

	var reads atomic.Int64
	t0 := time.Now()
	base := rawSSEServerFunc(t, func(body []byte) []string {
		if strings.Contains(string(body), "You are the RESEARCHER") {
			// Slow, and never concludes: it always wants one more file.
			time.Sleep(phaseDelay)
			n := reads.Add(1)
			t.Logf("researcher model call %d at +%v", n, time.Since(t0))
			return toolCallSSE(fmt.Sprintf("r%d", n), "builtin__read_file",
				fmt.Sprintf(`{"path":"f%d.txt"}`, n))
		}
		t.Logf("coder model call at +%v", time.Since(t0))
		return textSSE("THE ANSWER")
	})

	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"read_file": "allow"}},
		Budget: MCPBudgetConfig{
			MaxIterations:     20,
			MaxTurnIterations: 40,
			// Two phases, so the Researcher's reserved share is one second and
			// the Coder is guaranteed the other.
			TurnTimeoutSeconds: 2,
		},
	})
	for i := 1; i <= 20; i++ {
		path := filepath.Join(s.workspace, fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
	}

	res, _, streamed, err := runPipeline(t, s, []*agentRole{&roleResearcher, &roleCoder})
	if err != nil {
		t.Fatalf("runOrchestrated: %v", err)
	}
	if !strings.Contains(streamed, "THE ANSWER") {
		t.Fatalf("a slow Researcher consumed the whole turn and the Coder never answered.\n"+
			"streamed %q (%d bytes); incomplete=%+v",
			streamed, len(streamed), res.Incomplete)
	}
	if res.Incomplete != nil {
		t.Errorf("the turn was reported incomplete, but only the Researcher's own reserved "+
			"share of time was spent: %+v", res.Incomplete)
	}
}

// The wall-clock reservation is the byte reservation's twin, so it inherits the
// twin's rule: a caller may not use it to buy time the user did not allow. The
// mirror of TestThePhaseReservationCanOnlyTightenTheToolBudget.
func TestThePhaseDeadlineCanOnlyTightenTheTurnDeadline(t *testing.T) {
	var calls atomic.Int64
	base := rawSSEServerFunc(t, func([]byte) []string {
		// Each call takes longer than the whole configured turn, so the SECOND
		// budgetStop must refuse -- whatever the ledger says.
		time.Sleep(1200 * time.Millisecond)
		return toolCallSSE(fmt.Sprintf("c%d", calls.Add(1)), "builtin__list_directory", `{"path":"."}`)
	})
	s := loopServer(t, base, MCPConfig{
		Enabled: true,
		Builtin: MCPBuiltinConfig{Tools: map[string]string{"list_directory": "allow"}},
		Budget:  MCPBudgetConfig{MaxIterations: 20, MaxTurnIterations: 40, TurnTimeoutSeconds: turnCeilingSeconds},
	})

	registry, _ := s.buildRegistry(context.Background(), s.logger, &proposalSink{}, "")
	t.Cleanup(func() { _ = registry.Close() })

	ledger := newTurnLedger()
	ledger.deadlineCap = time.Now().Add(time.Hour) // a caller trying to EXTEND the turn

	start := time.Now()
	res, err := s.runAgentLoop(context.Background(), start, registry, "m", "auto",
		[]chatMessage{{Role: "user", Content: "go"}}, providerRouting{}, nil,
		func(string) error { return nil }, nil, nil, nil, nil, nil, ledger)
	if err != nil {
		t.Fatalf("runAgentLoop: %v", err)
	}
	if res.Incomplete == nil {
		t.Fatalf("a phase deadline an hour out overrode turn_timeout_seconds=1: the turn ran to "+
			"completion after %v", time.Since(start))
	}
	// 5x the 1 s turn ceiling this test configures. The property -- that the turn
	// was cut short -- is asserted directly by res.Incomplete above; this only
	// catches a ceiling that fires far too late to be the thing that fired.
	if elapsed := time.Since(start); elapsed > 5*turnCeiling {
		t.Errorf("the turn ran %v against a %s ceiling", elapsed, turnCeiling)
	}
}

// A phase stopping on its OWN reservation must not tell the user to raise a
// setting that was never reached. The user is never shown this one -- the
// orchestrator swallows it and carries on -- but a message that survives to a
// log or to a future caller has to be true where it is written.
func TestAReservationStopDoesNotBlameTheTurnTimeout(t *testing.T) {
	s := &Server{cfg: &Config{}, logger: discardLogger()}
	turn := &agentTurn{iteration: 1}
	bud := budget{
		maxIterations:        10,
		maxTurnIterations:    10,
		maxTotalToolByte:     1 << 20,
		deadline:             time.Now().Add(-time.Second),
		deadlineIsPhaseShare: true,
	}
	stop := s.budgetStop(turn, bud)
	if stop == nil {
		t.Fatal("an expired deadline did not stop the phase")
	}
	if strings.Contains(stop.Detail, "turn_timeout_seconds") {
		t.Errorf("a phase that spent its reserved share was told to raise turn_timeout_seconds, "+
			"which is not what stopped it: %q", stop.Detail)
	}

	bud.deadlineIsPhaseShare = false
	stop = s.budgetStop(turn, bud)
	if stop == nil || !strings.Contains(stop.Detail, "turn_timeout_seconds") {
		t.Errorf("a genuine turn timeout no longer names turn_timeout_seconds: %+v", stop)
	}
}
