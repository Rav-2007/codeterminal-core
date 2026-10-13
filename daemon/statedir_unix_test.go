//go:build unix

package main

// The environment variable, and the home-relative default, that stateHome
// consults on this platform. memory_test.go asserts the behaviour; these two
// supply the only things that differ between platforms.
const stateDirEnvVar = "XDG_STATE_HOME"

// ~/.local/state, the XDG Base Directory default. Correct here, and the whole
// trouble on Windows -- see statedir_windows_test.go's counterpart.
var stateDirHomeFallback = []string{".local", "state"}
