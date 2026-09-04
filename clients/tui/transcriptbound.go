package main

import (
	"fmt"
	"os"
	"strconv"
)

// THE TRANSCRIPT IS BOUNDED, AND SAYS SO WHEN IT TRIMS.
//
// THE DEFECT: m.turns only ever grew. Nothing trimmed it except an explicit
// /compact, so a session left open for a day held every byte of every answer it
// had ever received, and so did the render cache beside it and the viewport's
// line index beside that. MEASURED at 2000 turns of ~1.2KB answers: 1.2MB of
// turn text, 6.3MB of heap in use, 30MB peak RSS against 8.6MB idle. Nothing
// about that is catastrophic at 2000 turns, which is exactly why it survived --
// it is a slope, not a cliff, and the machine it eventually hurts is somebody
// else's.
//
// TWO CEILINGS, NOT ONE, because either alone leaves a hole. A count-only bound
// lets a model that answers in 50KB blocks hold 25MB inside a "500 turn" limit.
// A byte-only bound lets thousands of one-line tool-activity turns accumulate
// under a generous byte budget, and each of those is a slice header, a cache
// entry and a render. The two failure modes are different shapes, so both are
// measured.
//
// SILENT EVICTION WOULD BE A DEFECT, not a feature. A transcript that quietly
// forgets is worse than one that grows: the user scrolls up, finds the
// conversation simply stops, and has no way to tell a dropped turn from one
// that was never sent. So a single marker turn sits at the top saying exactly
// how many turns and how many bytes went, and it accumulates rather than
// repeating -- one line however many times eviction has run.

const (
	// defaultMaxTurns is roughly 250 exchanges. Chosen as more conversation
	// than anyone scrolls back through, not as a memory figure: the byte
	// ceiling is what bounds memory, and this one bounds the per-turn costs
	// that are counted rather than measured -- a slice header, a cache entry,
	// a rendered block and a map slot each.
	defaultMaxTurns = 500

	// defaultMaxTranscriptBytes is the total of every turn's text and
	// reasoning. 2 MiB holds roughly 1700 turns of a 1.2KB answer or 260 of an
	// 8KB one, and costs about 6MB once the render cache's copy and the
	// viewport's line index are counted alongside it.
	defaultMaxTranscriptBytes = 2 << 20

	// The floors exist because these are configurable, and a ceiling of zero
	// read out of a mistyped environment variable would evict the conversation
	// as fast as it arrived -- a bound that destroys the product it protects.
	minMaxTurns           = 8
	minMaxTranscriptBytes = 64 << 10

	maxTurnsEnv = "CODETERMINAL_MAX_TURNS"
	maxBytesEnv = "CODETERMINAL_MAX_TRANSCRIPT_BYTES"
)

// evictionMarkerPrefix identifies the marker turn. Matched as a prefix rather
// than kept as a model field so that a transcript restored from a previous
// session, which arrives as plain turns over the wire, is understood too.
const evictionMarkerPrefix = "⋮ "

// transcriptLimits are the two ceilings, read once when the model is built.
type transcriptLimits struct {
	turns int
	bytes int
}

// loadTranscriptLimits reads the environment, falling back to the defaults. An
// unparseable or too-small value is IGNORED rather than honoured: the operator
// meant to set a bound, and a bound that is wrong in the unsafe direction is
// worse than the default they were trying to change.
func loadTranscriptLimits() transcriptLimits {
	return transcriptLimits{
		turns: envInt(maxTurnsEnv, defaultMaxTurns, minMaxTurns),
		bytes: envInt(maxBytesEnv, defaultMaxTranscriptBytes, minMaxTranscriptBytes),
	}
}

func envInt(name string, fallback, floor int) int {
	v, err := strconv.Atoi(os.Getenv(name))
	if err != nil || v < floor {
		return fallback
	}
	return v
}

// transcriptBytes is the total text carried by the turns, which is what the
// byte ceiling is expressed in. Reasoning counts: it is held on the turn, it is
// rendered, and it is as large as an answer for a reasoning-tier model.
func transcriptBytes(turns []turn) int {
	n := 0
	for i := range turns {
		n += len(turns[i].text) + len(turns[i].reasoning)
	}
	return n
}

// enforceTranscriptBound drops the oldest turns until both ceilings are met and
// updates the marker to say what went.
//
// CALLED ONLY AT THE START OF A TURN, and that placement is load-bearing rather
// than convenient. Two model fields are INDEXES INTO m.turns -- streamAssistant,
// and every value in activityTurns -- so dropping from the front shifts what
// they point at. Running here, before startTurn resets streamAssistant to -1 and
// clears activityTurns, means there is no live index to invalidate. Evicting
// from inside appendTurn would have been the obvious place and would have
// silently repointed the streaming turn at somebody else's answer.
//
// The cost of that choice, stated: the ceiling can be exceeded within a single
// turn, by at most whatever one exchange produces. It is a bound on sessions,
// not on answers; a single answer is bounded by the daemon and the model.
func (m *chatModel) enforceTranscriptBound() {
	if m.limits.turns <= 0 || m.limits.bytes <= 0 {
		m.limits = loadTranscriptLimits() // a zero-valued model, as tests build
	}

	bytes := transcriptBytes(m.turns)
	if len(m.turns) <= m.limits.turns && bytes <= m.limits.bytes {
		return
	}

	// Where the real turns start: past the marker, if one is already there.
	// The marker is rewritten below rather than dropped and re-added, so it
	// must not be counted as a candidate for eviction.
	start := 0
	if len(m.turns) > 0 && m.turns[0].role == roleSystem &&
		len(m.turns[0].text) >= len(evictionMarkerPrefix) &&
		m.turns[0].text[:len(evictionMarkerPrefix)] == evictionMarkerPrefix {
		start = 1
	}

	// The marker counts against the ceiling. Without this the transcript
	// settles one turn above the limit forever -- a small lie, but the kind
	// that makes a bound impossible to assert exactly, and an inexact
	// assertion is one nobody can tell from a slow leak.
	extra := 1 // a marker will exist afterwards
	if start == 1 {
		extra = 0 // ...and is already counted in len(m.turns)
	}

	dropped, droppedBytes := 0, 0
	i := start
	for i < len(m.turns) && (len(m.turns)-dropped+extra > m.limits.turns || bytes > m.limits.bytes) {
		bytes -= len(m.turns[i].text) + len(m.turns[i].reasoning)
		droppedBytes += len(m.turns[i].text) + len(m.turns[i].reasoning)
		dropped++
		i++
	}
	if dropped == 0 {
		return
	}

	m.evictedTurns += dropped
	m.evictedBytes += droppedBytes

	// A COPY, not a re-slice. Re-slicing would keep the whole original backing
	// array alive behind a shorter view -- every evicted turn still resident,
	// which is the bug this function exists to prevent, hidden inside its fix.
	kept := make([]turn, 0, len(m.turns)-dropped+1)
	kept = append(kept, turn{role: roleSystem, text: m.evictionNotice()})
	kept = append(kept, m.turns[i:]...)
	m.turns = kept
}

func (m *chatModel) evictionNotice() string {
	return fmt.Sprintf("%s%d earlier turn%s (%s) dropped to bound memory — this session has run long. /compact trims deliberately; /clear starts over.",
		evictionMarkerPrefix, m.evictedTurns, plural(m.evictedTurns), humanBytes(m.evictedBytes))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}
