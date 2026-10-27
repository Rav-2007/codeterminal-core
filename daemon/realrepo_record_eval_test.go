//go:build eval

package main

import (
	"mochiii/editapply"
	"mochiii/protocol"
)

// Mochiii's record under the answer (turnreport.go), carried into the real-repo
// eval's "continue" as the user's chat carries it. In a file of its own: Round
// 6's arm A is main as it was before the record existed, and its tree leaves
// this file out (scripts/realrepo/baseline_tree.sh).
func init() {
	turnRecord = func(res agentResult, offered []editapply.EditBlock, wc *protocol.WorkingCopyInfo,
		sink *proposalSink, editsNow int) string {
		return turnReport(turnFacts{result: res, offered: offered, info: wc, checks: sink.checks,
			editsNow: editsNow, outside: sink.outsideReview})
	}
}
