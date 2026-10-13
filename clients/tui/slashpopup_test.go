package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// FOUND IN THE REAL APP (2026-10-12): typing "/" drew all 37 commands in a
// 24-row terminal, and what scrolled off the top was the highlighted row and
// the first commands in the catalog -- /model, /provider, /account.
//
// Neuter check: make slashPopupWindow return (0, n) -- the popup is 39 lines
// tall again and the first assertion fails.
func TestTheCommandPopupFitsTheTerminalAndShowsTheHighlight(t *testing.T) {
	for _, size := range []struct{ w, h int }{{100, 40}, {80, 24}, {80, 16}, {80, 12}} {
		updated, _ := newTestModel().Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		m := typeText(updated.(chatModel), "/")
		popup, height := m.renderSlashPopup()
		if got := lipgloss.Height(popup); got != height {
			t.Errorf("at %dx%d the popup says it is %d lines and is %d", size.w, size.h, height, got)
		}
		// Room is left for the prompt, the line under it, the help and the header.
		if height > size.h-4 {
			t.Errorf("at %dx%d the popup is %d lines tall", size.w, size.h, height)
		}
		plain := ansi.Strip(popup)
		if !strings.Contains(plain, "> /help") {
			t.Errorf("at %dx%d the highlighted command is not in the popup:\n%s", size.w, size.h, plain)
		}
		if !strings.Contains(plain, "▼") || strings.Contains(plain, "▲") {
			t.Errorf("at %dx%d the popup does not say there is more below (and only below):\n%s", size.w, size.h, plain)
		}
	}

	// The window follows the highlight to the last command and back round.
	m := typeText(newTestModel(), "/")
	matches := m.slashMatches()
	for i := 0; i < len(matches)-1; i++ {
		m = press(m, tea.KeyDown)
	}
	popup, _ := m.renderSlashPopup()
	last := "> /" + matches[len(matches)-1].Name
	if plain := ansi.Strip(popup); !strings.Contains(plain, last) || !strings.Contains(plain, "▲") || strings.Contains(plain, "▼") {
		t.Errorf("at the last command the popup does not show %q with more above only:\n%s", last, plain)
	}

	// A list that fits is drawn as it always was: no markers.
	few, _ := typeText(newTestModel(), "/pro").renderSlashPopup()
	if plain := ansi.Strip(few); strings.Contains(plain, "more") || !strings.Contains(plain, "> /provider") {
		t.Errorf("a one-command popup changed:\n%s", plain)
	}
}

// The three commands the owner could not find are a few keys away, and their
// summaries are whole -- not cut at the popup's column.
func TestTheNewCommandsAreFoundByTyping(t *testing.T) {
	for prefix, name := range map[string]string{"/mod": "model", "/prov": "provider", "/acc": "account"} {
		popup, _ := typeText(newTestModel(), prefix).renderSlashPopup()
		plain := ansi.Strip(popup)
		def := lookupSlash(name)
		if def == nil {
			t.Fatalf("/%s is not in the catalog", name)
		}
		if !strings.Contains(plain, "> /"+name) || !strings.Contains(plain, def.Summary) {
			t.Errorf("typing %s does not show /%s with its whole summary:\n%s", prefix, name, plain)
		}
	}
}

// A summary is cut on a character, never in the middle of one.
func TestSlashSummaryCutsOnACharacter(t *testing.T) {
	long := strings.Repeat("—", 80)
	got := slashSummary(long)
	if !strings.HasSuffix(got, "...") || lipgloss.Width(got) > 50 {
		t.Errorf("a long summary came out %d columns wide: %q", lipgloss.Width(got), got)
	}
	for _, r := range got {
		if r == '�' {
			t.Fatalf("the cut split a character: %q", got)
		}
	}
}
