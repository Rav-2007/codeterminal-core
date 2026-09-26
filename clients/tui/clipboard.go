package main

import (
	"os/exec"
	"runtime"
)

// clipboardToolAvailable reports whether ctrl+v can reach the system
// clipboard. On Linux the input widget shells out to one of these; macOS and
// Windows have a clipboard API built in. A var so tests can pin it.
var clipboardToolAvailable = func() bool {
	if runtime.GOOS != "linux" {
		return true
	}
	for _, tool := range []string{"wl-paste", "xclip", "xsel"} {
		if _, err := exec.LookPath(tool); err == nil {
			return true
		}
	}
	return false
}
