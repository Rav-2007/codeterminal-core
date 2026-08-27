package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// THE REPORTED BUG, AS A TEST.
//
// Live, against a stopped daemon: five prompts in a row, five "You: ..." lines,
// and not one word underneath any of them. The failure WAS reported -- into
// m.statusErr, a single header line that each identical failure overwrote with
// identical text, so it read as a stale line from earlier rather than as five
// fresh refusals. The transcript, which is where a reader looking for their
// answer is actually looking, said nothing at all.
const severedDetail = "daemon not found (expected a lockfile at /run/user/1000/codeterminal/daemon-e06123af88966937.lock)"

func TestAPromptThatNeverLeftIsRecordedInTheTranscript(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "who is the current cm of tamil nadu")
	m, _ = pressEnter(m)

	updated, _ := m.Update(streamErrMsg{err: errors.New(severedDetail)})
	m = updated.(chatModel)

	var severed []turn
	for _, tn := range m.turns {
		if tn.role == roleSevered {
			severed = append(severed, tn)
		}
	}
	if len(severed) != 1 {
		t.Fatalf("got %d severed turn(s), want 1 — the transcript records the failure nowhere", len(severed))
	}
	// VERBATIM. A marker that looks striking and swallows the daemon's own
	// words would be a worse bug than the silence it replaced.
	if severed[0].text != severedDetail {
		t.Errorf("severed turn text = %q, want the daemon's error unchanged", severed[0].text)
	}
	// The header keeps working; this is an addition, not a replacement.
	if m.statusErr != severedDetail {
		t.Errorf("statusErr = %q, want the error still in the header too", m.statusErr)
	}

	rendered := renderTranscript(m.turns, 80)
	if !strings.Contains(rendered, severedDetail) {
		t.Error("the rendered transcript does not contain the error text")
	}
	if !strings.Contains(rendered, "LINK SEVERED") {
		t.Error("the rendered transcript carries no severed marker")
	}
}

// The actual reported experience: several failures in a row. Each must leave
// its own mark, under the message that caused it.
func TestEveryFailedPromptLeavesItsOwnMark(t *testing.T) {
	m := newTestModel()
	for _, prompt := range []string{"who is the current cm of tamil nadu", "hi", "klnkjb", "mbjj", "m"} {
		m = typeText(m, prompt)
		m, _ = pressEnter(m)
		updated, _ := m.Update(streamErrMsg{err: errors.New(severedDetail)})
		m = updated.(chatModel)
	}

	n := 0
	for _, tn := range m.turns {
		if tn.role == roleSevered {
			n++
		}
	}
	if n != 5 {
		t.Fatalf("got %d severed marker(s) for 5 failed prompts, want 5", n)
	}
	// Interleaved, so each marker sits under its own prompt rather than
	// collecting at the end.
	for i := 0; i+1 < len(m.turns); i += 2 {
		if m.turns[i].role != roleUser || m.turns[i+1].role != roleSevered {
			t.Fatalf("turn %d/%d = %v/%v, want user then severed", i, i+1, m.turns[i].role, m.turns[i+1].role)
		}
	}
}

// A MESSAGE THAT NEVER LEFT MUST NEVER REACH THE MODEL AS THOUGH IT HAD.
// roleSevered is display-only, exactly like roleSystem.
func TestSeveredTurnsAreNeverSentAsHistory(t *testing.T) {
	turns := []turn{
		{role: roleUser, text: "who is the current cm of tamil nadu"},
		{role: roleSevered, text: severedDetail},
		{role: roleUser, text: "hi"},
		{role: roleSevered, text: severedDetail},
	}
	history := buildHistory(turns)
	if len(history) != 2 {
		t.Fatalf("history has %d turn(s), want 2 (the two user prompts only)", len(history))
	}
	for _, h := range history {
		if strings.Contains(h.Content, "LINK SEVERED") || strings.Contains(h.Content, "daemon not found") {
			t.Errorf("TUI chrome leaked into model history: %+v", h)
		}
		if h.Role != "user" {
			t.Errorf("unexpected role %q in history", h.Role)
		}
	}
}

// A failure PART-WAY through keeps the existing behaviour: the partial answer
// is marked incomplete and no severed marker is added, because the prompt did
// reach the daemon. The two failures are different events and must not look
// alike.
func TestAMidStreamFailureIsNotMarkedSevered(t *testing.T) {
	m := newTestModel()
	m = typeText(m, "explain goroutines")
	m, _ = pressEnter(m)
	updated, _ := m.Update(tokenMsg("A goroutine is"))
	m = updated.(chatModel)
	updated, _ = m.Update(streamErrMsg{err: errors.New("connection reset by peer")})
	m = updated.(chatModel)

	for _, tn := range m.turns {
		if tn.role == roleSevered {
			t.Fatal("a mid-stream drop was marked as never-sent; the prompt did reach the daemon")
		}
	}
	// The pre-existing path still fires.
	marked := false
	for _, h := range buildHistory(m.turns) {
		if h.Incomplete != "" {
			marked = true
		}
	}
	if !marked {
		t.Error("the partial answer lost its incomplete marking")
	}
}

// THE RAIL MUST FIT THE TERMINAL EXACTLY.
//
// It is drawn to the full width on purpose, and wrapToWidth runs over the whole
// transcript afterwards. One column too many and the rule folds onto a second
// line, where it stops reading as a rule and starts reading as damage.
func TestTheRailFillsTheWidthExactlyAndSurvivesWrapping(t *testing.T) {
	for _, w := range []int{120, 100, 80, 60, 40, 26} {
		rendered := renderTranscript([]turn{{role: roleSevered, text: severedDetail}}, w)
		rail := strings.Split(rendered, "\n")[0]
		if got := lipgloss.Width(rail); got != w {
			t.Errorf("width %d: rail is %d column(s), want exactly %d", w, got, w)
		}
		// The property that matters: wrapping must not touch it.
		if before, after := rail, strings.Split(wrapToWidth(rendered, w), "\n")[0]; before != after {
			t.Errorf("width %d: wrapToWidth folded the rail", w)
		}
	}
}

// Narrow and degenerate widths must not panic, must not emit a stub rail, and
// must still show the error. This client is expected to survive width 0 (see
// narrowterminal_test.go, written after a real crash).
func TestTheRailDegradesRatherThanBreakingWhenThereIsNoRoom(t *testing.T) {
	// DERIVED FROM THE RENDERER'S OWN RULE rather than written as a number, so
	// retuning the label or the threshold cannot leave this test asserting a
	// boundary the code no longer has.
	narrowest := lipgloss.Width(severedLabel) + 8 // the widest width with no rail

	for _, w := range []int{narrowest, 20, 16, 8, 1, 0, -5} {
		rendered := renderTranscript([]turn{{role: roleSevered, text: severedDetail}}, w)
		if !strings.Contains(rendered, "LINK SEVERED") {
			t.Errorf("width %d: the marker vanished", w)
		}
		if !strings.Contains(rendered, severedDetail) {
			t.Errorf("width %d: the error text vanished", w)
		}
		// No half-drawn fade: below the threshold it is the label alone.
		if strings.ContainsAny(rendered, severedDecay) {
			t.Errorf("width %d: a stub rail was drawn with no room for a fade:\n%s", w, rendered)
		}
		// And no trailing styled space where the rail would have been.
		if first := strings.Split(rendered, "\n")[0]; strings.HasSuffix(first, " ") {
			t.Errorf("width %d: the bare label kept its trailing space: %q", w, first)
		}
	}

	// One column more and the rail appears, so the threshold is a real edge and
	// not an accident of the widths chosen above.
	rendered := renderTranscript([]turn{{role: roleSevered, text: severedDetail}}, narrowest+1)
	if !strings.ContainsAny(rendered, severedDecay) {
		t.Errorf("width %d: no rail drawn just above the threshold", narrowest+1)
	}
}

// The fade must actually fade. A rail whose glyphs are not ordered dense to
// sparse is just a row of noise.
func TestTheFadeIsMonotonicFromDenseToSparse(t *testing.T) {
	dense, sparse := severedRail(64)
	all := []rune(dense + sparse)
	if len(all) != 64 {
		t.Fatalf("rail is %d rune(s), want 64", len(all))
	}
	// RANGE OVER A STRING YIELDS BYTE OFFSETS, not indices -- these glyphs are
	// multi-byte, so `i` here would be 0,3,6,9 rather than 0,1,2,3. Ordered over
	// the rune slice instead, which is also what severedRail itself indexes.
	stages := []rune(severedDecay)
	order := map[rune]int{}
	for i, r := range stages {
		order[r] = i
	}
	for i := 1; i < len(all); i++ {
		prev, ok1 := order[all[i-1]]
		cur, ok2 := order[all[i]]
		if !ok1 || !ok2 {
			t.Fatalf("rail contains a glyph outside the decay set: %q", string(all[i]))
		}
		if cur < prev {
			t.Fatalf("the fade reverses at column %d (%q after %q)", i, string(all[i]), string(all[i-1]))
		}
	}
	// It must END at the faintest stage, not restart.
	if last := all[len(all)-1]; last != stages[len(stages)-1] {
		t.Errorf("the fade ends on %q, want the faintest glyph", string(last))
	}
}
