package main

import "github.com/charmbracelet/lipgloss"

// Mochiii's palette, kept in one place so it's easy to retune on sight.
// Pink is the brand color (logo, "Mochiii", the user's own prompts); teal is
// pink's complement, reserved for accents and the streaming/state indicator;
// gray is body copy (assistant answers); red is errors only.
var (
	colorPink = lipgloss.Color("205") // ~#FF5FAF
	colorTeal = lipgloss.Color("44")  // ~#5FD7D7
	colorGray = lipgloss.Color("252")
	colorRed  = lipgloss.Color("203")
)

var (
	logoStyle      = lipgloss.NewStyle().Foreground(colorPink).Bold(true)
	brandStyle     = lipgloss.NewStyle().Foreground(colorPink).Bold(true)
	taglineStyle   = lipgloss.NewStyle().Foreground(colorGray)
	userStyle      = lipgloss.NewStyle().Foreground(colorPink)
	assistantStyle = lipgloss.NewStyle().Foreground(colorGray)
	accentStyle    = lipgloss.NewStyle().Foreground(colorTeal)
	errorStyle     = lipgloss.NewStyle().Foreground(colorRed)
	helpStyle      = lipgloss.NewStyle().Foreground(colorGray).Faint(true)

	// diffRemovedStyle/diffAddedStyle render an edit-review diff's SEARCH
	// (removed) and REPLACE (added) lines respectively.
	diffRemovedStyle = lipgloss.NewStyle().Foreground(colorRed)
	diffAddedStyle   = lipgloss.NewStyle().Foreground(colorTeal)
)
