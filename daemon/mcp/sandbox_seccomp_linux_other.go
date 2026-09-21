//go:build linux && !amd64 && !arm64

package mcp

// No seccomp filter has been written for this architecture, so the landlock
// backend is never selected here: Landlock alone leaves the Unix-socket escape
// open (see socketFilterProgram), and a half sandbox must not be offered as one.
const (
	seccompSupported   = false
	seccompAuditArch   = 0
	seccompX32Possible = false
)
