//go:build !trace

package tuitrace

import (
	"testing"
	"time"
)

// In a default build there is no recorder: Begin never returns a turn, so every
// call site that is guarded by `tr != nil` is dead, and what a turn would have said
// is nothing, including the id that would have been sent to the daemon.
func TestAStubTurnDoesNothingAndHasNoID(t *testing.T) {
	t.Setenv("MOCHIII_TRACE", "1") // the switch alone is not enough: there is no code to switch on
	tr := Begin()
	if tr != nil {
		t.Fatal("a default build returned a turn")
	}
	tr.Emit(StageDial, StatusOK, time.Second, KindPrompt, ClassNone)
	if tr.ID() != "" {
		t.Errorf("the stub's id is %q, so a request would carry a trace key", tr.ID())
	}
	Reset()
}
