package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// mirrorChar returns c's left-right mirror image for the glyph set used in
// lotusLogo (diagonal/bracket characters swap sides; everything else,
// including ':' and '.', is visually the same either way).
func mirrorChar(c rune) rune {
	switch c {
	case '\\':
		return '/'
	case '/':
		return '\\'
	case '<':
		return '>'
	case '>':
		return '<'
	case '(':
		return ')'
	case ')':
		return '('
	default:
		return c
	}
}

// isHorizontallySymmetric checks the palindrome-under-mirroring property
// after trimming whitespace from BOTH ends. Trimming only one side would
// falsely flag a genuinely symmetric row: a row built as left+center+
// mirror(left) has matching leading and trailing padding by construction,
// and editors commonly strip only *trailing* whitespace, which would
// otherwise make an already-symmetric row look lopsided to this check.
func isHorizontallySymmetric(line string) bool {
	runes := []rune(strings.TrimSpace(line))
	n := len(runes)
	for i := 0; i < n; i++ {
		if runes[i] != mirrorChar(runes[n-1-i]) {
			return false
		}
	}
	return true
}

// TestLotusLogo_RowsAreSymmetric guards against the exact failure mode the
// logo must avoid: a lopsided edit that reads as a loose, off-center sketch
// instead of a deliberate bloom. Trailing whitespace is deliberately not
// checked (there's no background fill, so it's not visually meaningful);
// what matters is that each row's visible content mirrors around its own
// center.
func TestLotusLogo_RowsAreSymmetric(t *testing.T) {
	lines := strings.Split(lotusLogo, "\n")
	if len(lines) < 2 {
		t.Fatalf("lotusLogo has only %d line(s); expected a multi-row bloom", len(lines))
	}
	for i, line := range lines {
		if !isHorizontallySymmetric(line) {
			t.Errorf("lotusLogo line %d is not left-right symmetric: %q", i+1, line)
		}
	}
}

func TestLotusLogo_NoTabsOrCarriageReturns(t *testing.T) {
	if strings.ContainsAny(lotusLogo, "\t\r") {
		t.Error("lotusLogo contains a tab or carriage return, which will misalign it in a terminal")
	}
}

// TestRenderSplash_LogoRowsShareOneCenter guards the on-screen bloom, not just
// the source. Each row mirroring around its own center is not enough:
// lipgloss.Place centers every row on its own width, so rows of unequal width
// (the raw string has no trailing padding) each shifted by half their indent
// and the lotus came out skewed although TestLotusLogo_RowsAreSymmetric passed.
func TestRenderSplash_LogoRowsShareOneCenter(t *testing.T) {
	const width = 120
	placed := lipgloss.Place(width, 30, lipgloss.Center, lipgloss.Center, renderSplash())
	rows := strings.Split(ansi.Strip(placed), "\n")

	logoRows := len(strings.Split(lotusLogo, "\n"))
	var centers2x []int
	for _, row := range rows {
		trimmed := strings.TrimSpace(row)
		if trimmed == "" {
			continue
		}
		start := lipgloss.Width(row) - lipgloss.Width(strings.TrimLeft(row, " "))
		centers2x = append(centers2x, 2*start+lipgloss.Width(trimmed)-1)
		if len(centers2x) == logoRows {
			break
		}
	}
	if len(centers2x) != logoRows {
		t.Fatalf("found %d logo rows on screen, want %d", len(centers2x), logoRows)
	}
	for i, c := range centers2x {
		if c != centers2x[0] {
			t.Errorf("logo row %d is centered at column %.1f, row 1 at %.1f: the bloom is skewed",
				i+1, float64(c)/2, float64(centers2x[0])/2)
		}
	}
}
