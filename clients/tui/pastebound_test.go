package tui

import (
	"sort"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func pasteOf(runes int) tea.KeyMsg {
	body := strings.Repeat("pasted source line with unicode -> abcd\n", runes/40+1)
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(body)[:runes], Paste: true}
}

// A PASTE THAT DOES NOT FIT IS TRUNCATED, AND SAYING NOTHING ABOUT IT IS A
// DEFECT -- the same rule the transcript bound follows.
//
// The prompt box has held 4000 characters since it was written, and anything
// past that was dropped in silence. Paste a 40KB file expecting to ask about
// it, and the model is asked about the first 4000 characters instead, with
// nothing on screen to say so. The answer that comes back is confidently about
// the wrong input.
func TestAnOversizedPasteSaysWhatItDropped(t *testing.T) {
	m := newTestModel()
	u, _ := m.Update(pasteOf(40000))
	m = u.(chatModel)

	if got := len([]rune(m.input.Value())); got > m.input.CharLimit {
		t.Fatalf("the input holds %d runes past its own limit of %d", got, m.input.CharLimit)
	}
	notice := m.statusErr
	if notice == "" {
		t.Fatal("40,000 characters were pasted, at most 4,000 were kept, and the " +
			"client said nothing. The user is now asking about an input that was " +
			"silently cut.")
	}
	for _, want := range []string{"40,000", "paste"} {
		if !strings.Contains(strings.ToLower(notice), strings.ToLower(want)) {
			t.Errorf("the notice does not mention %q: %q", want, notice)
		}
	}
}

// A paste that fits must be left completely alone -- no notice, no truncation.
func TestAPasteThatFitsIsUntouched(t *testing.T) {
	m := newTestModel()
	u, _ := m.Update(pasteOf(500))
	m = u.(chatModel)

	if got := len([]rune(m.input.Value())); got != 500 {
		t.Errorf("a 500-rune paste left %d runes in the input", got)
	}
	if m.statusErr != "" {
		t.Errorf("a paste that fits produced a notice: %q", m.statusErr)
	}
}

// Ordinary typing must not be mistaken for a paste.
func TestTypingIsNotTreatedAsAPaste(t *testing.T) {
	m := newTestModel()
	for _, r := range "hello world" {
		u, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = u.(chatModel)
	}
	if m.input.Value() != "hello world" {
		t.Errorf("typing produced %q", m.input.Value())
	}
	if m.statusErr != "" {
		t.Errorf("typing produced a paste notice: %q", m.statusErr)
	}
}

// 3.6c: NO SINGLE UPDATE OVER ONE FRAME, FOR ANY PASTE SIZE, AT ANY TRANSCRIPT
// LENGTH. The spread is reported and not just the median, because a 2.6x
// run-to-run spread straddling the ceiling is what made this its own task.
//
// MEASURED BEFORE THE BOUND, 9 samples each:
//
//	  0 turns, 1MB paste: min 16.2ms  median 17.1ms  max 20.1ms
//	240 turns, 1MB paste: min 13.7ms  median 15.6ms  max 19.6ms
//
// Over the 16ms ceiling at the median on an empty transcript, and straddling
// it at 240 turns. The cost is linear in the size of the paste and independent
// of the transcript, because it was textinput processing a million runes in
// order to keep four thousand.
func TestNoPasteExceedsOneFrame(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("timing measurement")
	}
	sizes := []int{4096, 65536, 1024 * 1024}
	for _, prior := range []int{0, 240} {
		for _, size := range sizes {
			ds := make([]time.Duration, 9)
			for s := range ds {
				m := benchTranscript(prior, 1200)
				msg := pasteOf(size)
				start := time.Now()
				u, _ := m.Update(msg)
				ds[s] = time.Since(start)
				m = u.(chatModel)
				_ = m
			}
			sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
			lo, median, hi := ds[0], ds[4], ds[8]
			t.Logf("%4d turns, %8d runes: min=%-11v median=%-11v max=%-11v spread=%.1fx (%.0f%% of a %v frame)",
				prior, size, lo.Round(time.Microsecond), median.Round(time.Microsecond),
				hi.Round(time.Microsecond), float64(hi)/float64(lo),
				100*float64(median)/float64(updateCeilingAnyInput), updateCeilingAnyInput)

			// Asserted on the MAXIMUM, not the median: the ceiling says no
			// single Update, and a median under budget with a max over it is
			// still a dropped frame somebody sees.
			if hi > updateAssertedCeiling {
				t.Errorf("%d turns, %d runes: slowest Update %v exceeds %v",
					prior, size, hi.Round(time.Microsecond), updateAssertedCeiling)
			}
			if median > updateCeilingAnyInput {
				t.Errorf("%d turns, %d runes: median Update %v is over D-1's %v frame ceiling",
					prior, size, median.Round(time.Microsecond), updateCeilingAnyInput)
			}
		}
	}
}

// The bound must hold whatever the input already contains: a paste arriving on
// top of a nearly-full prompt box still cannot cost more than a frame.
func TestAPasteOntoAFullInputIsStillBounded(t *testing.T) {
	m := newTestModel()
	m.input.SetValue(strings.Repeat("x", m.input.CharLimit))
	u, _ := m.Update(pasteOf(1024 * 1024))
	m = u.(chatModel)
	if got := len([]rune(m.input.Value())); got > m.input.CharLimit {
		t.Errorf("the input holds %d runes past its limit of %d", got, m.input.CharLimit)
	}
	if m.statusErr == "" {
		t.Error("a 1MB paste onto a full input said nothing")
	}
}

// THE DETERMINISTIC GATE, and the one that actually holds this bound.
//
// The timing test above reports the win and asserts only loosely, per the
// standing rule: wall-clock on a shared machine cannot resolve a threshold the
// noise straddles, and the pre-fix median was 15.6ms against a 16ms ceiling --
// close enough that a timing assertion would not reliably have caught its own
// removal. What IS deterministic is how many runes reach the input, and that is
// the quantity the cost is linear in.
func TestNoMoreRunesReachTheInputThanCanBeKept(t *testing.T) {
	m := newTestModel()
	for _, size := range []int{0, 1, 100, 3999, 4000, 4001, 65536, 1 << 20} {
		got := m.boundPaste(pasteOf(size))
		if len(got.Runes) > m.input.CharLimit {
			t.Errorf("a %d-rune paste handed %d runes to the input, which can keep at most %d",
				size, len(got.Runes), m.input.CharLimit)
		}
		if size <= m.input.CharLimit && len(got.Runes) != size {
			t.Errorf("a %d-rune paste that fits was cut to %d", size, len(got.Runes))
		}
	}
}

// A non-rune key must pass through untouched whatever its size, or the bound
// becomes a filter on control keys.
func TestBoundPasteLeavesNonRuneKeysAlone(t *testing.T) {
	m := newTestModel()
	for _, k := range []tea.KeyType{tea.KeyEnter, tea.KeyEsc, tea.KeyCtrlC, tea.KeyBackspace, tea.KeyUp} {
		in := tea.KeyMsg{Type: k}
		if got := m.boundPaste(in); got.Type != k || len(got.Runes) != 0 {
			t.Errorf("key %v was altered: %+v", k, got)
		}
		if m.statusErr != "" {
			t.Errorf("key %v produced a paste notice: %q", k, m.statusErr)
		}
	}
}

// A PASTE THAT WAS CUT HAS TO SAY SO WHERE IT CAN BE SEEN. boundPaste has set
// statusErr since the bound was written, and statusErr is drawn only in
// stateError (stateLabel), so at a ready prompt -- where every paste happens --
// 30,008 characters went in, 4,000 stayed, and nothing on screen said so
// (Phase 3 audit, 2026-11-09). TestAnOversizedPasteSaysWhatItDropped above
// reads the field, which is why it passed throughout.
//
// These read the SCREEN.
//
// Neuter check: drop the pasteNoticeShown branch in stateLabel and the first
// test fails on the screen; stop clearing pasteCut in handleKey and the third
// fails; take the paste's cost into the transcript again and
// TestOneMegabytePasteIntoACeilingTranscript (repaintceiling_test.go) fails.
func TestAnOversizedPasteIsSaidOnTheScreenAtAReadyPrompt(t *testing.T) {
	m := newTestModel()
	u, _ := m.Update(pasteOf(30008))
	m = u.(chatModel)

	if got := len([]rune(m.input.Value())); got != 4000 {
		t.Fatalf("the box holds %d runes, want the 4,000 it can keep", got)
	}
	screen := ansi.Strip(m.View())
	if !strings.Contains(screen, "pasted text was cut to 4,000 characters") {
		t.Errorf("30,008 characters were pasted, 4,000 kept, and the screen does not say so:\n%s", screen)
	}
	// It is chrome, not conversation, and it costs the transcript nothing: a new
	// turn would invalidate the render cache (see boundPaste).
	if len(m.turns) != 0 {
		t.Errorf("the notice added %d turn(s) to the transcript: %+v", len(m.turns), m.turns)
	}
	if h := buildHistory(m.turns); len(h) != 0 {
		t.Errorf("the notice went into the history sent to the model: %+v", h)
	}
}

// On a narrow terminal the notice is cut, not wrapped: the header is one row.
func TestThePasteNoticeStaysOnTheHeadersOneRow(t *testing.T) {
	m := newTestModel()
	u, _ := m.Update(tea.WindowSizeMsg{Width: 30, Height: 24})
	m = u.(chatModel)
	u, _ = m.Update(pasteOf(30008))
	m = u.(chatModel)
	first := strings.SplitN(ansi.Strip(m.View()), "\n", 2)[0]
	if w := ansi.StringWidth(first); w > 30 {
		t.Errorf("the header is %d cells wide on a 30-column terminal: %q", w, first)
	}
	if !strings.Contains(first, "pasted") {
		t.Errorf("the notice is not on the first row: %q", first)
	}
}

// The notice is about what is in the box: it goes when the box is emptied
// (sent or deleted) and does not come back for what is typed next.
func TestThePasteNoticeGoesWhenTheBoxIsEmptied(t *testing.T) {
	m := newTestModel()
	u, _ := m.Update(pasteOf(30008))
	m = u.(chatModel)
	if !strings.Contains(ansi.Strip(m.View()), "pasted text was cut") {
		t.Fatal("no notice to begin with")
	}
	m.input.SetValue("")
	if strings.Contains(ansi.Strip(m.View()), "pasted text was cut") {
		t.Error("the box is empty and the screen still says the paste was cut")
	}
	u, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m = u.(chatModel)
	if strings.Contains(ansi.Strip(m.View()), "pasted text was cut") {
		t.Error("typing after the box was emptied brought the paste notice back")
	}
}

// A paste that fits says nothing, on the screen as in the field.
func TestAPasteThatFitsSaysNothingOnTheScreen(t *testing.T) {
	m := newTestModel()
	u, _ := m.Update(pasteOf(500))
	m = u.(chatModel)
	if screen := ansi.Strip(m.View()); strings.Contains(screen, "pasted text was cut") {
		t.Errorf("a 500-character paste was reported as cut:\n%s", screen)
	}
}

// In stateError the header already shows the fuller notice (statusErr); the
// short one must not replace or repeat it.
func TestAPasteInTheErrorStateKeepsTheFullerNotice(t *testing.T) {
	m := newTestModel()
	m.state = stateError
	m.statusErr = "an earlier failure"
	u, _ := m.Update(pasteOf(30008))
	m = u.(chatModel)
	screen := ansi.Strip(m.View())
	if !strings.Contains(screen, "paste of 30,008 characters") {
		t.Errorf("the error state lost the full notice:\n%s", screen)
	}
	if strings.Contains(screen, "pasted text was cut") {
		t.Errorf("the error state says it twice:\n%s", screen)
	}
	// And the error header is drawn exactly as it was before the flag existed:
	// the one-row cut belongs to the short notice alone, not to every state.
	u, _ = m.Update(tea.WindowSizeMsg{Width: 30, Height: 24})
	m = u.(chatModel)
	with := m.renderHeader()
	m.pasteCut = false
	if without := m.renderHeader(); with != without {
		t.Errorf("the paste flag changed the error header:\n with: %q\n without: %q", with, without)
	}
}
