//go:build linux && amd64

package mcp

import "golang.org/x/sys/unix"

// The architecture the seccomp filter admits, and whether this architecture has
// an x32 ABI to shut: on amd64, x32 syscalls arrive with AUDIT_ARCH_X86_64 and
// the x32 bit set in the number.
const (
	seccompSupported   = true
	seccompAuditArch   = unix.AUDIT_ARCH_X86_64
	seccompX32Possible = true
)
