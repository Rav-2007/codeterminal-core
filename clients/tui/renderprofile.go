package main

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// RENDERING IS A FUNCTION OF STATE, NOT OF WHATEVER THE PROCESS HAPPENED TO
// FIND IN ITS ENVIRONMENT THE FIRST TIME SOMETHING DREW.
//
// Lip Gloss detects the terminal's colour capability LAZILY and caches it in a
// sync.Once on a package-level renderer (lipgloss/renderer.go: `var renderer =
// &Renderer{output: termenv.DefaultOutput()}`). Nothing detects anything at
// startup; the FIRST call to Style.Render anywhere in the process reads the
// environment, decides, and freezes that decision for the process lifetime.
//
// That makes the bytes we emit depend on WHEN the first render happened
// relative to any change in the environment, which is not a property a
// rendering function should have. MEASURED, identical model state and identical
// width, one process each:
//
//	CLICOLOR_FORCE set before the first render:  382 bytes
//	CLICOLOR_FORCE set after  the first render:  291 bytes
//
// Same state, same width, same environment at the moment of the render that was
// compared -- 91 bytes apart, decided entirely by whether something else had
// drawn earlier. In a test binary "something else had drawn earlier" means
// "another test ran first", so the suite's output depends on test ordering.
//
// It matters beyond tidiness because the transcript render cache (3.2) stores
// rendered bytes for completed turns and concatenates freshly rendered bytes
// onto them. If the profile could change between building the prefix and
// rendering the tail, the two halves would carry different escape forms and the
// transcript would be visibly wrong -- and the equivalence gate that is supposed
// to catch that (3.4) cannot prove anything about a renderer that is not a
// function of its inputs.
//
// pinColorProfile resolves the profile ONCE, deliberately, at startup, and then
// stores it explicitly: SetColorProfile sets Lip Gloss's explicitColorProfile
// flag, so every later ColorProfile() returns the stored value and the detection
// path is never entered again. After this call the environment is no longer an
// input to rendering.
//
// WHAT IT DOES NOT DO, and this is deliberate: it does not pin a CONSTANT
// profile. A 16-colour terminal must still get 16-colour codes and NO_COLOR=1
// must still turn colour off -- those are user-facing contracts, and honouring
// them is why the profile is detected at all. What changes here is that the
// detection happens at one named point instead of at an arbitrary one, and
// cannot happen twice. Tests pin a constant instead (see TestMain), which is
// what makes their output independent of the machine they run on.
//
// THE FULL SET OF PROCESS-GLOBAL INPUTS TO RENDERING, enumerated because a
// pinned profile is only worth having if it is the last one. Verified against
// termenv v0.16.0, lipgloss v1.1.0, bubbles v0.21.0 and x/ansi v0.8.0:
//
//	READ, AND PINNED BY THIS FUNCTION -- all of these feed profile detection
//	inside termenv's Output.ColorProfile / EnvNoColor / isTTY:
//	  TERM                 xterm-256color -> ANSI256, xterm/linux -> ANSI,
//	                       dumb or unset -> Ascii. Changes SGR form:
//	                       "\x1b[38;5;205m" vs "\x1b[95m" vs nothing.
//	  COLORTERM            truecolor/24bit -> TrueColor; yes/true -> ANSI256.
//	  TERM_PROGRAM         only to tell tmux from screen under COLORTERM.
//	  GOOGLE_CLOUD_SHELL   "true" forces TrueColor.
//	  NO_COLOR             any non-empty value forces Ascii.
//	  CLICOLOR             "0" forces Ascii unless CLICOLOR_FORCE overrides.
//	  CLICOLOR_FORCE       non-"0" upgrades an Ascii result to ANSI. This is
//	                       the one that bites in tests: it turns colour ON in a
//	                       pipe, so a developer with it exported ran a different
//	                       suite from CI.
//	  CI                   NON-EMPTY MEANS "NOT A TTY" to termenv, so a real
//	                       terminal renders colourless under CI=1. Measured.
//	  isatty(stdout)       a pipe means Ascii. This is why `go test` has always
//	                       rendered colourless and a pty test has not.
//
//	READ, AND NOT PINNED HERE -- a separate global with a separate blast radius:
//	  LC_ALL / LC_CTYPE / LANG / RUNEWIDTH_EASTASIAN
//	                       go-runewidth's init() sets a package-level
//	                       EastAsianWidth from these, which changes the width of
//	                       AMBIGUOUS runes. It reaches exactly one thing we
//	                       draw: bubbles/textinput's overflow scrolling, i.e.
//	                       the input line. MEASURED on View() with an
//	                       overflowing line of ambiguous runes: 892 bytes at
//	                       EastAsianWidth=0, 865 at 1.
//	                       It does NOT reach the transcript: renderTranscript
//	                       goes through ansi.Wrap and lipgloss.Width, both of
//	                       which resolve widths with x/ansi's GraphemeWidth
//	                       method and never call runewidth. The cached prefix
//	                       is therefore locale-independent, which is what 3.2
//	                       and 3.4 need. Pinned as a known gap by
//	                       TestKnownGapTheInputLineFollowsTheLocale rather than
//	                       forced to a constant, because forcing it would make
//	                       the input line scroll wrongly for CJK users to buy
//	                       determinism on a line nothing caches.
//
//	READ, WITH NO EFFECT ON OUR BYTES -- checked rather than assumed:
//	  terminal background  Bubble Tea's init() calls lipgloss.HasDarkBackground,
//	                       which queries the terminal (OSC 11, 5s timeout).
//	                       Consumed only by AdaptiveColor and CompleteColor.
//	                       We use neither, and neither does bubbles' viewport or
//	                       textinput. Adding one would make output depend on the
//	                       terminal's background colour;
//	                       TestTranscriptRenderIsByteIdenticalAcrossEnvironments
//	                       is what would catch that.
//	  FORCE_COLOR          NOT READ. It is a colorprofile/v2 convention, and
//	                       termenv v0.16 has no reference to it. Listed because
//	                       it is easy to assume it works and it does not.
//	  Windows console mode enableLegacyWindowsANSI, once, inside Style.Render.
//	                       Changes the console, never the returned string.
//
//	PACKAGE-LEVEL STATE OF OUR OWN:
//	  styles.go            eleven styles and four colours, all built from
//	                       constants at init and never reassigned afterwards
//	                       (nothing outside styles.go assigns to any of them).
//	                       They capture the default renderer pointer at init,
//	                       which is the same renderer this function pins.
//	  no clock, no random  the render path calls neither time.Now nor math/rand,
//	                       so nothing else varies run to run.
func pinColorProfile() termenv.Profile {
	p := lipgloss.ColorProfile() // the one detection, here and nowhere else
	lipgloss.SetColorProfile(p)  // and from now on it is a stored value
	return p
}
