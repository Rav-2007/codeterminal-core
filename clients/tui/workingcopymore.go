package tui

import (
	"strings"

	"mochiii/protocol"
)

// workingCopyMore is what workingCopyText says after the check and the
// not-offered lines: the existing tests the agent changed -- a pass after that
// is not a tick (the daemon's testedits.go) -- and the files a command changed
// in the real project, outside any review (the daemon's runplace.go). Named,
// though not undone: the review below cannot take them back.
func workingCopyMore(info *protocol.WorkingCopyInfo) []string {
	var lines []string
	if len(info.TestsChanged) > 0 {
		lines = append(lines, "! the agent changed existing tests: "+sanitizeText(strings.Join(info.TestsChanged, ", "))+
			" — a pass after that may come from the changed test; read those lines first")
	}
	for _, n := range info.OutsideReview {
		lines = append(lines, "! changed in your real project, outside review: "+sanitizeText(n))
	}
	return lines
}
