package tui

import (
	"os"
	"strings"
	"sync/atomic"
)

// ADDRESSES IN AN ANSWER ARE HYPERLINKS (OSC 8), SO A WRAPPED ONE OPENS WHOLE.
//
// The owner, 2026-11-21, with a screenshot of the Sources under an answer:
// "it can't be able to redirect to the link". Measured by feeding this
// program's own bytes into VTE 0.76, the engine inside their GNOME Terminal:
// with no hyperlink sequence the terminal finds addresses by pattern, line by
// line, so ctrl+click opened every address that fit on one line -- and an
// address the answer had to break across two lines opened CUT SHORT
// (".../most-visited-websites-in-the-world-202", the "4/" left behind on the
// next line). A plain click opens nothing in any case: that is the terminal's
// rule, and the mouse is the terminal's here (ADR-001).
//
// An OSC 8 sequence names the target once for every piece of the address, so
// each piece opens all of it, and the terminal offers "Open Hyperlink" on a
// right click.
//
// ONLY TEXT THAT IS THE ADDRESS IS LINKED. "[the list](https://...)" is drawn
// as the label followed by the address, and only the address carries the
// link; the label never does. A label linked to somewhere else is the one
// thing a hyperlink can hide -- the sanitizer strips OSC 8 from untrusted text
// for exactly that reason (sanitize.go) -- and a target that is the visible
// text hides nothing: it is what the terminal already opened by pattern.
//
// NOT UNDER NO_COLOR. That profile's promise is not one escape sequence
// (TestAnAnswerIsDrawnInBlueAndGreyOnly); mdPens.links keeps it.
//
// A TARGET IS PRINTABLE ASCII, http or https. The address is text a model or a
// web page chose. No ESC, BEL or other control byte can be in it, so nothing
// in it can end the sequence early and write the rest as commands; and a
// space or a non-ASCII letter (a look-alike host) is not linked at all -- it
// is still shown, as text.

const (
	hyperlinkClose = "\x1b]8;;\x1b\\"

	// maxHyperlinkTarget is under VTE's own limit (2083 bytes); a longer
	// target is ignored by the terminal anyway.
	maxHyperlinkTarget = 2000
)

// hyperlinksOn says whether this terminal is sent hyperlinks. Set once at
// startup (pinHyperlinks); on by default, so a test renders as a terminal that
// takes them, whatever TERM the test happens to run under.
var hyperlinksOn atomic.Bool

func init() { hyperlinksOn.Store(true) }

// pinHyperlinks decides, once, from TERM.
func pinHyperlinks() { hyperlinksOn.Store(hyperlinksFor(os.Getenv("TERM"))) }

// hyperlinksFor is false for the two terminals known to PRINT an OSC 8
// sequence instead of reading it: the Linux console, which ends an OSC it
// does not know after its first character and shows the rest, and "dumb".
// Every other terminal reads the sequence; one without hyperlinks ignores it.
// An unset TERM is Windows, whose terminals read it.
func hyperlinksFor(term string) bool {
	return term != "dumb" && !strings.HasPrefix(term, "linux")
}

// hyperlinkTarget is s if it may be drawn as a link to itself, else "".
func hyperlinkTarget(s string) string {
	if len(s) > maxHyperlinkTarget {
		return ""
	}
	lower := strings.ToLower(s)
	var rest string
	switch {
	case strings.HasPrefix(lower, "https://"):
		rest = s[len("https://"):]
	case strings.HasPrefix(lower, "http://"):
		rest = s[len("http://"):]
	default:
		return ""
	}
	if rest == "" || rest[0] == '/' {
		return "" // no host
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= ' ' || c >= 0x7f {
			return ""
		}
	}
	return s
}

// hyperlinkOpen is the sequence that starts a link to target, or "" for no
// link.
func hyperlinkOpen(target string) string {
	if target == "" || !hyperlinksOn.Load() {
		return ""
	}
	return "\x1b]8;;" + target + "\x1b\\"
}
