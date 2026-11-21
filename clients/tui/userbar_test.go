package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// WHAT YOU SAID IS A SHADED BAR (owner, 2026-11-21, after Cline's): "➤ " and the
// words, every row the width of the terminal on the bar's shade, a long question
// wrapped under itself; the answer is on the terminal's own background. At
// width 0 (/copy) the bar is as long as the words.
//
// Neuter checks: drop the padding and the width assertion fails; render the
// text without the shade and the colour assertion fails.
func TestAQuestionIsAShadedBar(t *testing.T) {
	q := strings.Repeat("why does the build fail ", 6) + "\nsecond line"
	got := strings.Split(ansi.Strip(renderUserBlock(q, 60)), "\n")
	if !strings.HasPrefix(got[0], promptMark+" why does") || !strings.HasPrefix(got[1], "  ") || !strings.HasPrefix(got[len(got)-1], "  second line") {
		t.Errorf("the bar's rows:\n%s", strings.Join(got, "\n"))
	}
	for i, r := range got {
		if w := lipgloss.Width(r); w != 60 {
			t.Errorf("row %d is %d columns, want the terminal's 60: %q", i, w, r)
		}
	}
	if strings.Contains(strings.Join(got, ""), "You:") {
		t.Error("the bar still says You:")
	}
	if unbounded := ansi.Strip(renderUserBlock("short", 0)); unbounded != promptMark+" short" {
		t.Errorf("at width 0 the bar is %q", unbounded)
	}

	var styled string
	withColorProfile(t, termenv.ANSI256, func() { styled = renderTurnBlock(turn{role: roleUser, text: "hi"}, 20) })
	const shade = "48;5;238" // inside each run's SGR: the mark, the words, the fill
	if strings.Count(styled, shade) < 3 || !strings.HasSuffix(styled, "\x1b[0m") {
		t.Errorf("the words and the fill are not on the bar's shade, or the row does not end in a reset trimLineEnds leaves alone: %q", styled)
	}
	if trimLineEnds(styled) != styled {
		t.Error("printing would trim the bar")
	}
	var answer string
	withColorProfile(t, termenv.ANSI256, func() { answer = renderTurnBlock(turn{role: roleAssistant, text: "an answer"}, 20) })
	if strings.Contains(answer, "\x1b[48;") {
		t.Errorf("the answer is shaded too: %q", answer)
	}
}

// A REPLY STARTS WITH THE CURVED ARROW, NOT A NAME (owner, 2026-11-21: "remove
// the mochiii in reply, it should just reply with the curved arrow"): markdown,
// a command's plain output, and an answer that opens with a block.
//
// Neuter check: put "Mochiii: " back in assistantLabel.
func TestAReplyStartsWithTheCurvedArrow(t *testing.T) {
	for name, got := range map[string]string{
		"markdown":         renderAnswer("Hello! How can I help you today?", 80),
		"a command's text": renderPlainAnswer("## status"),
		"a block first":    renderAnswer("```\ncode\n```", 80),
	} {
		g := ansi.Strip(got)
		if !strings.HasPrefix(g, "↬") || strings.Contains(g, "Mochiii") {
			t.Errorf("%s: the reply reads %q", name, g)
		}
	}
}
