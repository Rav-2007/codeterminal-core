package tui

import "mochiii/protocol"

// workingCopyMore is what workingCopyText says after the check and the
// not-offered lines: the files a command changed in the real project, outside
// any review (the daemon's runplace.go). Named, though not undone: the review
// below cannot take them back.
func workingCopyMore(info *protocol.WorkingCopyInfo) []string {
	var lines []string
	for _, n := range info.OutsideReview {
		lines = append(lines, "! changed in your real project, outside review: "+sanitizeText(n))
	}
	return lines
}
