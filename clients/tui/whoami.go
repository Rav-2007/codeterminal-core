package main

import (
	"os"
	"os/user"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// WHO IS RUNNING THE CLIENT, AND ON WHICH MACHINE: "ravi@laptop", at the right
// end of the row under the prompt.
//
// WHY (OWNER, 2026-10-13): "it should display the user/desktop name in the
// bottom of the prompt bar". The chat takes the whole terminal, shell prompt
// included, so the one line that said whose session this is, and where, was
// gone the moment the client started. The row under the prompt already said
// which model and provider (statusline.go); the name is on the same row.
//
// ON THE SAME ROW, NOT ON ONE OF ITS OWN. That row is fixed -- it never appears
// or disappears, so the transcript never moves for it -- and every further row
// under the prompt would be a row taken from the answer being read. The line
// keeps the left, where the eye already looks for the model; the name goes
// against the right edge, under the end of the prompt's box.
//
// THE LINE COMES FIRST. The row is for the model, the provider and what the
// session has cost, and all three change; the name never does. So the name
// takes only the columns the line leaves: whole when it fits, cut with "…"
// when it nearly does, and not drawn when what would be left is not a name.
//
// ASKED ONCE, AND ONLY DRAWN. runChat asks the operating system before the
// first frame and stores the answer (chatExtras.whoami); the drawing code reads
// that string and asks nobody, so a frame is still a function of the model's
// state (renderprofile.go). And the name stays on this screen. A user's name
// and their machine's are facts about a person: they are not put in a prompt,
// a saved chat, a log line or anything sent to the daemon, from where they
// could reach a provider. TestTheNameIsDrawnAndNothingElse holds that, by
// listing the functions that may read the field.

const (
	// whoamiGap is the least space between the line and the name.
	whoamiGap = 2
	// whoamiMinCols is the narrowest a CUT name is drawn. Under it what is left
	// is a few letters and a "…", which says neither who nor where.
	whoamiMinCols = 8
)

// lookupWhoami asks the operating system who is running this process and what
// the machine is called. The machine's name is the kernel's own: no DNS is
// asked for it.
func lookupWhoami() string {
	host, _ := os.Hostname() // "" on failure, which whoamiLabel takes as no answer
	return whoamiLabel(currentUserName(), host)
}

// currentUserName is the account this process runs as. The environment is asked
// only when the system has no entry for that account -- a container started
// under a bare user id is the usual case -- because the environment says who
// logged in, and `su` does not always bring it up to date.
func currentUserName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return firstNonEmptyText(os.Getenv("USER"), os.Getenv("LOGNAME"), os.Getenv("USERNAME"))
}

// whoamiLabel makes the one line that is drawn out of the two names: user@host,
// as a shell prompt writes it. Half an answer is still drawn; no answer is "".
//
// Neither name is this program's -- an account or a machine can be called
// anything -- so both are made safe to draw here, once, and the row draws the
// result as it is.
func whoamiLabel(userName, hostName string) string {
	userName, hostName = oneCleanLine(userName), oneCleanLine(hostName)
	// Windows puts the machine or the domain in front of the user: SCARX\ravi.
	if i := strings.LastIndex(userName, `\`); i >= 0 {
		userName = strings.TrimSpace(userName[i+1:])
	}
	// A fully qualified name is cut where a shell prompt cuts it, at the first
	// dot: Ravis-MacBook-Pro.local is Ravis-MacBook-Pro.
	hostName, _, _ = strings.Cut(hostName, ".")
	hostName = strings.TrimSpace(hostName)
	switch {
	case userName == "":
		return hostName
	case hostName == "":
		return userName
	}
	return userName + "@" + hostName
}

// oneCleanLine is s as text for one row: nothing in it a terminal would act on
// (sanitize.go), and every run of white space, a line break included, a single
// space. A line break that reached the row would make the frame a row taller
// than the terminal.
func oneCleanLine(s string) string {
	return strings.Join(strings.Fields(sanitizeText(s)), " ")
}

// statusRow is the whole row under the prompt: the line (statusLine), and at
// the right edge the name, in the columns the line has left.
func (m chatModel) statusRow() string {
	line := m.statusLine()
	if m.whoami == "" {
		return line
	}
	used := lipgloss.Width(line)
	room := m.width - used
	if used > 0 {
		room -= whoamiGap
	}
	name := m.whoami
	if lipgloss.Width(name) > room {
		if room < whoamiMinCols {
			return line
		}
		name = ansi.Truncate(name, room, "…")
	}
	pad := m.width - used - lipgloss.Width(name)
	return line + strings.Repeat(" ", pad) + statusLineStyle.Render(name)
}
