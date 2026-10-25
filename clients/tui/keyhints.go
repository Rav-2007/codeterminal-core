package tui

import "runtime"

// THE KEY IN THE IDLE HINT THAT IS NOT THIS PROGRAM'S.
//
// Since 2026-10-25 the terminal keeps the mouse (main.go starts without mouse
// capture; ADR-001): a drag selects in the terminal itself and the terminal's
// own copy key copies. This program sees neither, and no clipboard program is
// involved -- the owner's requirement: "it should copy when the user presses
// ctrl+shift+c, no new clipboards". So the hint names the terminal's key, which
// is ctrl+shift+c in the Linux terminals and Windows Terminal, and cmd+c on a
// Mac.

// copyKey is the terminal's copy key on this platform.
func copyKey() string { return copyKeyFor(runtime.GOOS) }

func copyKeyFor(goos string) string {
	if goos == "darwin" {
		return "cmd+c"
	}
	return "ctrl+shift+c"
}

// pasteKey is the terminal's paste key on this platform. It is listed in /help
// and nowhere else: ctrl+v pastes only with a clipboard program installed, and
// without one it does nothing and says nothing (handleKey, 2026-10-25).
func pasteKey() string { return pasteKeyFor(runtime.GOOS) }

func pasteKeyFor(goos string) string {
	if goos == "darwin" {
		return "cmd+v"
	}
	return "ctrl+shift+v"
}

// idleHelpText is helpText for this platform.
func idleHelpText() string { return idleHelpTextFor(runtime.GOOS) }

func idleHelpTextFor(goos string) string {
	return "enter send · ctrl+↑↓ history · " + copyKeyFor(goos) + " copy · ctrl+n new · ctrl+c quit"
}
