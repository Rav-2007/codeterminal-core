package main

import "strings"

// lotusLogo is Mochiii's splash-screen wordmark art: a dense, filled,
// blooming lotus, built from mirror-symmetric rows so it reads as a
// deliberate bloom rather than a lopsided sketch. It's a single raw string
// so it's trivial to hand-edit — TestLotusLogo_RowsAreSymmetric checks that
// every row still mirrors correctly around its own center after an edit.
const lotusLogo = `        .::::::.  .::::::.  .::::::.
     :::::::::::\ :::::::: /:::::::::::
    ::::::::::::::\::::::/::::::::::::::
   .::::.  ':::::::\::::/:::::::'  .::::.
      :::::::::>---( ●● )---<:::::::::
   '::::'  .:::::::/::::\:::::::.  '::::'
    ::::::::::::::/::::::\::::::::::::::
     :::::::::::/ :::::::: \:::::::::::
        '::::::'  '::::::'  '::::::'`

// lotusGlyph is the small single-glyph lotus shown inline in the chat
// header (the full lotusLogo is splash-only — keeping it on screen during
// chat would waste vertical space). Swap to "✿" if your terminal font
// doesn't render the Unicode LOTUS emoji cleanly.
const lotusGlyph = "🪷"

// brandName is Mochiii's display name, used on the splash and in the chat
// header.
const brandName = "Mochiii"

// splashTagline is the one-line hint shown under the brand name on the
// splash screen.
const splashTagline = "a small pink lotus for your terminal — press any key to begin"

// renderSplash renders the logo (styled per-line, pink), brand name, and
// tagline as one block.
func renderSplash() string {
	lines := strings.Split(lotusLogo, "\n")
	styledLines := make([]string, len(lines))
	for i, line := range lines {
		styledLines[i] = logoStyle.Render(line)
	}
	logo := strings.Join(styledLines, "\n")

	brand := brandStyle.Render(brandName)
	tagline := taglineStyle.Render(splashTagline)
	return strings.Join([]string{logo, "", brand, tagline}, "\n")
}
