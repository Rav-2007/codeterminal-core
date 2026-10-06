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

// The answer's own palette (markdown.go): blue for what the model marked as
// mattering, grey for the rest, on the terminal's black. ASKED FOR 2026-10-06 by
// the owner -- "use those two colors (blue, grey)". Two of each, so an answer
// has four levels and no more; nothing else in an answer has a colour.
var (
	colorBlue  = lipgloss.Color("75")  // ~#5FAFFF: headings, **strong**
	colorSteel = lipgloss.Color("110") // ~#87AFD7: *emphasis*, `code`, links
	colorDim   = lipgloss.Color("243") // ~#767676: borders, rules, fences
	colorChip  = lipgloss.Color("236") // ~#303030: the ground behind `code`
)

// mdStyles is how each pen of an answer is drawn. Read once per colour profile
// (currentPens) and never per run: see mdPens.
var mdStyles = [penCount]lipgloss.Style{
	penBody:      lipgloss.NewStyle().Foreground(colorGray),
	penStrong:    lipgloss.NewStyle().Foreground(colorBlue).Bold(true),
	penEm:        lipgloss.NewStyle().Foreground(colorSteel).Italic(true),
	penStrongEm:  lipgloss.NewStyle().Foreground(colorBlue).Bold(true).Italic(true),
	penCode:      lipgloss.NewStyle().Foreground(colorSteel).Background(colorChip),
	penCodeBlock: lipgloss.NewStyle().Foreground(colorSteel),
	penHeading:   lipgloss.NewStyle().Foreground(colorBlue).Bold(true),
	penLink:      lipgloss.NewStyle().Foreground(colorSteel).Underline(true),
	penStrike:    lipgloss.NewStyle().Foreground(colorDim).Strikethrough(true),
	penQuote:     lipgloss.NewStyle().Foreground(colorGray).Italic(true),
	penMarker:    lipgloss.NewStyle().Foreground(colorBlue),
	penRule:      lipgloss.NewStyle().Foreground(colorDim),
}

var (
	logoStyle      = lipgloss.NewStyle().Foreground(colorPink).Bold(true)
	brandStyle     = lipgloss.NewStyle().Foreground(colorPink).Bold(true)
	taglineStyle   = lipgloss.NewStyle().Foreground(colorGray)
	userStyle      = lipgloss.NewStyle().Foreground(colorPink)
	assistantStyle = lipgloss.NewStyle().Foreground(colorGray)
	accentStyle    = lipgloss.NewStyle().Foreground(colorTeal)
	errorStyle     = lipgloss.NewStyle().Foreground(colorRed)
	helpStyle      = lipgloss.NewStyle().Foreground(colorGray).Faint(true)

	// severedStyle is the "LINK SEVERED" label on a prompt that never reached
	// the daemon. Defined here rather than built inline as errorStyle.Bold(true)
	// so the palette stays in one file -- and so nothing in the render path
	// derives a style from a package-level one, which is the shape of bug that
	// bites when a library's Style stops being a pure value type.
	severedStyle = lipgloss.NewStyle().Foreground(colorRed).Bold(true)

	// diffRemovedStyle/diffAddedStyle render an edit-review diff's SEARCH
	// (removed) and REPLACE (added) lines respectively.
	diffRemovedStyle = lipgloss.NewStyle().Foreground(colorRed)
	diffAddedStyle   = lipgloss.NewStyle().Foreground(colorTeal)
)
