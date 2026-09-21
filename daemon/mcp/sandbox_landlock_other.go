//go:build !linux

package mcp

import (
	"fmt"
	"os"
)

// Landlock is a Linux security module. Everywhere else the landlock backend is
// unavailable, and SandboxAuto never selects it.

// LandlockABI is 0: there is no Landlock here.
var LandlockABI = func() int { return 0 }

// LandlockUsable is false: there is no Landlock here.
var LandlockUsable = func() bool { return false }

func sandboxExecMain([]string) int {
	fmt.Fprintf(os.Stderr, "%sthe landlock helper runs only on Linux\n", helperErrorPrefix)
	return exitSandboxSetup
}
