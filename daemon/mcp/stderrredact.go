package mcp

import (
	"io"
	"sort"
	"strings"
)

// minRedactableEnvValue is the shortest provisioned value that will be replaced
// in a server's stderr.
//
// A JUDGEMENT, STATED RATHER THAN IMPLIED, because it is the one number in this
// file that is not forced. Below some length a "value" is a substring of
// ordinary text: a one-character value would replace every occurrence of that
// character, and a three-character one turns `cannot find abc` into noise.
// Eight is long enough that a collision with prose is unlikely and short enough
// that real credentials are covered -- no provisioned secret this daemon has
// seen is shorter. The cost of being wrong is asymmetric and mild in both
// directions: too low mangles diagnostics, too high leaves a short secret on
// screen, and a short secret is not the shape operators actually use.
const minRedactableEnvValue = 8

// redactEnvValues replaces, in one line of a server's stderr, every value this
// daemon provisioned under an ALLOW-LISTED name.
//
// ALLOW-LISTED NAMES ONLY, NEVER BaselineEnvNames. Redacting PATH would turn
// `cannot find node in /usr/bin:/bin` into `cannot find node in [REDACTED:PATH]`
// and destroy the diagnostic this whole path exists to deliver. The allow-list
// is where an operator puts a credential by construction -- it is the list they
// had to write in order to grant it.
//
// WHAT IT DOES NOT CATCH, enumerated because that is the difference from a shape
// matcher: a credential the server reads from its OWN config file or keychain
// (the daemon never held those bytes), and anything a LAUNCHER adds -- Connect's
// header records handing a reference Node server 2 variables and the child
// reporting 25, the other 23 being npx's. "Values this daemon provisioned" is a
// set you can list; "things that look like keys" is not.
//
// The placeholder shape matches scrub()'s in the daemon, so a user meets one
// vocabulary rather than two.
func redactEnvValues(line string, env []string, allow []string) string {
	if line == "" || len(allow) == 0 {
		return line
	}
	allowed := make(map[string]bool, len(allow))
	for _, name := range allow {
		allowed[name] = true
	}

	type pair struct{ name, value string }
	var subs []pair
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !allowed[name] || len(value) < minRedactableEnvValue {
			continue
		}
		// A VALUE THAT IS ITSELF AN ALLOW-LISTED NAME IS CONFIGURATION, NOT A
		// CREDENTIAL. Found by the end-to-end test, not by reasoning: a
		// variable pointing at another variable (FOO=BAR) had its value
		// redacted, which replaced every occurrence of the NAME "BAR" in the
		// line and destroyed the diagnostic the user needed. Redacting a
		// pointer protects nothing and costs the message.
		if allowed[value] {
			continue
		}
		subs = append(subs, pair{name, value})
	}

	// LONGEST VALUE FIRST. Without an order, a short value that is a substring
	// of a longer one gets replaced inside the longer one's text, and a value
	// that is a substring of an already-inserted placeholder gets replaced
	// inside THAT -- the end-to-end test produced
	// "[REDACTED:[REDACTED:NAME]]" before this was added. Longest-first makes
	// the overlapping case resolve to the most specific match.
	sort.SliceStable(subs, func(i, j int) bool { return len(subs[i].value) > len(subs[j].value) })

	for _, sub := range subs {
		line = strings.ReplaceAll(line, sub.value, "[REDACTED:"+sub.name+"]")
	}
	return line
}

// redactingWriter wraps a server's stderr sink and strips provisioned values
// from everything written through it.
//
// LINE-ORIENTED, because a value split across two Write calls would otherwise
// slip through the middle of it. prefixWriter upstream already emits whole
// lines, so this holds a partial line only until its newline arrives.
type redactingWriter struct {
	dest  io.Writer
	env   []string
	allow []string
	buf   strings.Builder
}

func (w *redactingWriter) Write(p []byte) (int, error) {
	if w.dest == nil {
		return len(p), nil
	}
	w.buf.Write(p)
	s := w.buf.String()
	idx := strings.LastIndexByte(s, '\n')
	if idx < 0 {
		// No complete line yet. Reported as fully written: the bytes are held,
		// not dropped, and a short count here would look like a write error to
		// os/exec's copier and stop the stream.
		return len(p), nil
	}
	complete, rest := s[:idx+1], s[idx+1:]
	w.buf.Reset()
	w.buf.WriteString(rest)
	if _, err := io.WriteString(w.dest, redactEnvValues(complete, w.env, w.allow)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// newRedactingStderr wraps dest so provisioned values never reach it. Returns
// dest unchanged when there is nothing to redact, so the common case adds no
// buffering at all.
func newRedactingStderr(dest io.Writer, env []string, allow []string) io.Writer {
	if dest == nil || len(allow) == 0 {
		return dest
	}
	return &redactingWriter{dest: dest, env: env, allow: allow}
}
