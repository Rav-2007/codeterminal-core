package main

import (
	"fmt"
	"regexp"
	"strings"
)

// WHY A SERVER DID NOT START, IN WORDS A PERSON CAN ACT ON.
//
// FOUND 2026-11-19, live: @modelcontextprotocol/server-filesystem@2.4.0 needs
// Node 24 and this computer had Node 20. npm said so ("EBADENGINE ... required:
// { node: '>=24' }"), Node then crashed on a SyntaxError -- and the LAST line it
// wrote was its version banner, "Node.js v20.20.2". That banner was the
// "Issue" /mcp-server get showed, and the add answer said "fix the command".
// Node ends every crash with that banner, so a server that exited with
// "Error: X is required" was reported the same way: the reason was there, a
// few lines up.

// recentStderrLines is how many of a server's last stderr lines are kept to
// find a reason in: enough to reach back past a stack trace.
const recentStderrLines = 40

// appendRecent adds line to a server's recent lines, keeping the last
// recentStderrLines of them.
func appendRecent(lines []string, line string) []string {
	lines = append(lines, line)
	if len(lines) > recentStderrLines {
		lines = lines[len(lines)-recentStderrLines:]
	}
	return lines
}

// nodeEngineTooOld is how a failure caused by the Node version begins, so the
// add answer can say the command is likely fine.
const nodeEngineTooOld = "it needs Node.js "

var (
	engineRequired = regexp.MustCompile(`required:\s*\{\s*node:\s*'([^']+)'`)
	engineCurrent  = regexp.MustCompile(`current:\s*\{\s*node:\s*'([^']+)'`)
	nodeBanner     = regexp.MustCompile(`^Node\.js v\d`)
	errorWords     = regexp.MustCompile(`(?i)(\berror\b|exception|\brequired\b|not found|no such file|cannot|can't|` +
		`permission denied|\bdenied\b|unsupported|\bmissing\b|\binvalid\b|\bfatal\b|\brefused\b|\bfailed\b)`)
)

// failureReason picks, from the last lines a server wrote before it failed,
// the one worth showing: npm's engine check when it names the Node version
// needed, else the last line that reads as an error and is not a stack frame,
// else the last line that is not Node's version banner. "" when there is none.
func failureReason(lines []string) string {
	var need, have string
	for _, l := range lines {
		if !strings.Contains(l, "EBADENGINE") {
			continue
		}
		if m := engineRequired.FindStringSubmatch(l); m != nil {
			need = m[1]
		}
		if m := engineCurrent.FindStringSubmatch(l); m != nil {
			have = m[1]
		}
	}
	if need != "" && have != "" {
		return fmt.Sprintf("%s%s, and this computer has %s (npm's EBADENGINE warning)", nodeEngineTooOld, need, have)
	}
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" || nodeBanner.MatchString(l) || strings.HasPrefix(l, "at ") || strings.HasPrefix(l, `File "`) {
			continue
		}
		if errorWords.MatchString(l) {
			return l
		}
		if last == "" {
			last = l
		}
	}
	return last
}

// failedStartText answers an add whose server did not start, when no
// better-known cause (a missing key, a sign-in, a slow download) explains it.
// reason is the server's own, from failureReason ("" when it said nothing).
func failedStartText(done, name string, err error, reason string) string {
	if strings.HasPrefix(reason, nodeEngineTooOld) {
		return fmt.Sprintf("%s %s, but it did NOT start when tried: %s. It is saved, and its command is likely "+
			"fine: update Node.js, or add a version of the package that supports this one.", done, name, reason)
	}
	said := ""
	if reason != "" && !strings.Contains(err.Error(), reason) {
		said = " It said: " + reason + "."
	}
	return fmt.Sprintf("%s %s, but it did NOT start when tried: %v.%s It is saved; fix the command "+
		"(`/mcp-server remove %s`, then add it again) or see the daemon log.", done, name, err, said, name)
}
