package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func fruitPicker() listPicker {
	return newListPicker("Pick one", []pickerRow{
		{ID: "a", Label: "Apple", Hint: "red"},
		{ID: "b", Label: "Banana", Hint: "yellow", Match: "plantain"},
		{ID: "c", Label: "Cherry", Hint: "red"},
		{ID: "d", Label: "Damson", Hint: "purple", Dim: true},
	})
}

func typeIntoPicker(p *listPicker, s string) {
	for _, r := range s {
		p.typeRunes([]rune{r}, false)
	}
}

// The search narrows by every word typed, in any case, and looks at the names a
// row is also known by -- "claude" has to find Anthropic.
func TestThePickerNarrowsByWhatIsTyped(t *testing.T) {
	p := fruitPicker()
	typeIntoPicker(&p, "AN")
	if got, ok := p.selected(); !ok || got.ID != "b" {
		t.Fatalf("\"AN\" selected %q, want Banana", got.ID)
	}
	if n := len(p.visible()); n != 1 {
		t.Errorf("\"AN\" left %d rows, want 1", n)
	}

	p = fruitPicker()
	typeIntoPicker(&p, "plant")
	if got, ok := p.selected(); !ok || got.ID != "b" {
		t.Errorf("a row was not found by its other name: got %q", got.ID)
	}

	// The real list, by the names people actually type.
	for typed, want := range map[string]string{"claude": "anthropic", "google": "gemini", "open ro": "openrouter", "kimi": "moonshot"} {
		p = newListPicker("", providerRows(""))
		typeIntoPicker(&p, typed)
		if got, ok := p.selected(); !ok || got.ID != want {
			t.Errorf("typing %q selected %q, want %q", typed, got.ID, want)
		}
	}
}

// Narrowing must never leave the highlight on a row that is no longer shown:
// Enter would then choose something the user cannot see.
func TestTheHighlightFollowsTheNarrowedList(t *testing.T) {
	p := fruitPicker()
	p.move(3)
	if got, _ := p.selected(); got.ID != "d" {
		t.Fatalf("down x3 selected %q, want Damson", got.ID)
	}
	typeIntoPicker(&p, "e") // Apple, Cherry
	if got, ok := p.selected(); !ok || got.ID != "a" {
		t.Errorf("after narrowing, the highlight is on %q (ok=%v), want the first match", got.ID, ok)
	}
	p.move(50)
	if got, _ := p.selected(); got.ID != "c" {
		t.Errorf("moving past the end selected %q, want the last match", got.ID)
	}
	p.move(-50)
	if got, _ := p.selected(); got.ID != "a" {
		t.Errorf("moving past the start selected %q, want the first match", got.ID)
	}

	typeIntoPicker(&p, "zzz")
	if _, ok := p.selected(); ok {
		t.Error("a search that matches nothing still reports a selection")
	}
	if !strings.Contains(ansi.Strip(p.view(40, 5)), "nothing matches") {
		t.Error("an empty result does not say so")
	}
	for range "ezzz" {
		p.backspace()
	}
	if n := len(p.visible()); n != 4 {
		t.Errorf("backspacing the search away left %d rows, want all 4", n)
	}
}

// A PASTE IS NOT TAKEN. This list is on screen when a new user is holding an API
// key on the clipboard, and a search box that echoes a paste would show it.
//
// Neuter check: drop the `pasted || len(runes) != 1` test in typeRunes.
func TestThePickerRefusesAPaste(t *testing.T) {
	const secret = "nvapi-0123456789abcdefSECRET"
	for name, pasted := range map[string]bool{"bracketed paste": true, "a burst of runes": false} {
		p := fruitPicker()
		if p.typeRunes([]rune(secret), pasted) {
			t.Errorf("%s: the picker took it", name)
		}
		if p.filter != "" {
			t.Errorf("%s: the search text holds %d characters of it", name, len(p.filter))
		}
		if strings.Contains(p.view(60, 5), "SECRET") {
			t.Errorf("%s: the pasted text is on screen", name)
		}
	}
	// One character at a time is typing, and it is bounded: a held key cannot
	// grow a line the box cannot hold.
	p := fruitPicker()
	typeIntoPicker(&p, strings.Repeat("a", 200))
	if n := len([]rune(p.filter)); n != pickerFilterMax {
		t.Errorf("the search text grew to %d characters, want it capped at %d", n, pickerFilterMax)
	}
	p = fruitPicker()
	if p.typeRunes([]rune{0x1b}, false) || p.filter != "" {
		t.Error("a control character was taken as search text")
	}
}

// The window follows the highlight and says how much is out of sight, and no
// line is ever wider than it was told to be -- a wrapped row would move the
// highlight off the line the user thinks it is on.
func TestThePickerViewFitsItsBoxAndSaysWhatIsHidden(t *testing.T) {
	var rows []pickerRow
	for i := 0; i < 30; i++ {
		rows = append(rows, pickerRow{ID: string(rune('a' + i)), Label: strings.Repeat("model-", 8) + string(rune('a'+i)), Hint: strings.Repeat("hint ", 20)})
	}
	p := newListPicker("A title that is itself much longer than the box it is drawn in", rows)

	top := ansi.Strip(p.view(30, 5))
	if strings.Contains(top, "▲") || !strings.Contains(top, "▼ 25 more") {
		t.Errorf("at the top the markers are wrong:\n%s", top)
	}
	p.move(15)
	mid := ansi.Strip(p.view(30, 5))
	if !strings.Contains(mid, "▲ 13 more") || !strings.Contains(mid, "▼ 12 more") {
		t.Errorf("in the middle the markers are wrong:\n%s", mid)
	}
	p.move(100)
	end := ansi.Strip(p.view(30, 5))
	if !strings.Contains(end, "▲ 25 more") || strings.Contains(end, "▼") {
		t.Errorf("at the end the markers are wrong:\n%s", end)
	}
	// One height wherever the highlight is, so nothing under the list jumps.
	if a, b, c := lipgloss.Height(top), lipgloss.Height(mid), lipgloss.Height(end); a != b || b != c {
		t.Errorf("the list changes height as the highlight moves: %d, %d, %d lines", a, b, c)
	}

	for _, width := range []int{8, 12, 30, 64} {
		for _, line := range strings.Split(p.view(width, 5), "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Errorf("at width %d a line is %d columns wide: %q", width, w, ansi.Strip(line))
			}
		}
	}
	// Sizes nobody should pass still must not panic.
	_ = p.view(0, 0)
	_ = p.view(-5, -5)
}

// selectID puts the highlight on the row a caller already knows is current.
func TestSelectIDHighlightsThatRow(t *testing.T) {
	p := fruitPicker()
	p.selectID("c")
	if got, _ := p.selected(); got.ID != "c" {
		t.Errorf("selectID(c) highlighted %q", got.ID)
	}
	p.selectID("no-such-row")
	if got, _ := p.selected(); got.ID != "c" {
		t.Errorf("an unknown id moved the highlight to %q", got.ID)
	}
}
